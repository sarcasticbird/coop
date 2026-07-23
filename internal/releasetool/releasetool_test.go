package releasetool

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
)

func TestResolveLatestDownloadsVerifiesExtractsAndCaches(t *testing.T) {
	archive := tarGzip(t, []tarEntry{{Name: "kata", Body: []byte("linux-kata"), Mode: 0o755, Type: tar.TypeReg}})
	sum := sha256.Sum256(archive)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	assetRequests := 0

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/kenn-io/kata/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name":   "v0.10.0",
				"draft":      false,
				"prerelease": false,
				"assets": []map[string]string{{
					"name":                 "kata_0.10.0_linux_arm64.tar.gz",
					"browser_download_url": server.URL + "/assets/kata.tar.gz",
					"digest":               digest,
				}},
			})
		case "/assets/kata.tar.gz":
			assetRequests++
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	resolver := Resolver{
		Client:            server.Client(),
		BaseURL:           server.URL,
		CacheDir:          t.TempDir(),
		allowDownloadHost: func(string) bool { return true },
	}
	specs := []config.GitHubReleaseTool{{
		Name: "kata", Repo: "kenn-io/kata", Tag: "latest",
		Asset: "kata_{version}_linux_arm64.tar.gz", Binary: "kata",
	}}

	got, err := resolver.Resolve(context.Background(), specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Tag != "v0.10.0" || got[0].Asset != "kata_0.10.0_linux_arm64.tar.gz" || got[0].Digest != digest {
		t.Fatalf("resolved = %+v", got)
	}
	data, err := os.ReadFile(got[0].CachePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "linux-kata" {
		t.Fatalf("cached executable = %q", data)
	}
	if info, err := os.Stat(got[0].CachePath); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("cached executable mode: %v %v", info, err)
	}

	if _, err := resolver.Resolve(context.Background(), specs); err != nil {
		t.Fatal(err)
	}
	if assetRequests != 1 {
		t.Fatalf("asset requests = %d, want cached second resolution", assetRequests)
	}
}

func TestResolveExactTagUsesTagEndpoint(t *testing.T) {
	archive := tarGzip(t, []tarEntry{{Name: "tool", Body: []byte("x"), Mode: 0o755, Type: tar.TypeReg}})
	sum := sha256.Sum256(archive)
	var requested string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/o/r/releases/tags/") {
			requested = r.URL.EscapedPath()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "release/1", "draft": false, "prerelease": false,
				"assets": []map[string]string{{
					"name":                 "tool_linux_arm64.tar.gz",
					"browser_download_url": server.URL + "/asset",
					"digest":               "sha256:" + hex.EncodeToString(sum[:]),
				}},
			})
			return
		}
		if r.URL.Path == "/asset" {
			_, _ = w.Write(archive)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	resolver := Resolver{
		Client: server.Client(), BaseURL: server.URL, CacheDir: t.TempDir(),
		allowDownloadHost: func(string) bool { return true },
	}
	_, err := resolver.Resolve(context.Background(), []config.GitHubReleaseTool{{
		Name: "tool", Repo: "o/r", Tag: "release/1",
		Asset: "tool_linux_arm64.tar.gz", Binary: "tool",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requested, "release%2F1") {
		t.Fatalf("exact tag endpoint did not path-escape tag: %q", requested)
	}
}

func TestResolveExactTagRejectsMismatchedMetadataTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v2.0.0", "draft": false, "prerelease": false, "assets": []any{},
		})
	}))
	defer server.Close()
	resolver := Resolver{Client: server.Client(), BaseURL: server.URL, CacheDir: t.TempDir()}
	_, err := resolver.resolveMetadata(context.Background(), config.GitHubReleaseTool{
		Name: "tool", Repo: "o/r", Tag: "v1.0.0",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match requested tag") {
		t.Fatalf("mismatched exact tag accepted: %v", err)
	}
}

func TestResolveFailsClosedOnReleaseMetadata(t *testing.T) {
	tests := map[string]map[string]any{
		"draft": {
			"tag_name": "v1", "draft": true, "prerelease": false, "assets": []any{},
		},
		"prerelease": {
			"tag_name": "v1", "draft": false, "prerelease": true, "assets": []any{},
		},
		"missing asset": {
			"tag_name": "v1", "draft": false, "prerelease": false, "assets": []any{},
		},
		"missing digest": {
			"tag_name": "v1", "draft": false, "prerelease": false,
			"assets": []map[string]string{{"name": "tool_1_linux_arm64.tar.gz", "browser_download_url": "https://github.com/x"}},
		},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			resolver := Resolver{Client: server.Client(), BaseURL: server.URL, CacheDir: t.TempDir()}
			_, err := resolver.Resolve(context.Background(), []config.GitHubReleaseTool{{
				Name: "tool", Repo: "o/r", Tag: "latest",
				Asset: "tool_{version}_linux_arm64.tar.gz", Binary: "tool",
			}})
			if err == nil {
				t.Fatal("unsafe release metadata accepted")
			}
		})
	}
}

func TestResolveRejectsDigestMismatch(t *testing.T) {
	archive := tarGzip(t, []tarEntry{{Name: "tool", Body: []byte("x"), Type: tar.TypeReg}})
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			_, _ = w.Write(archive)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1", "draft": false, "prerelease": false,
			"assets": []map[string]string{{
				"name":                 "tool_1_linux_arm64.tar.gz",
				"browser_download_url": server.URL + "/asset",
				"digest":               "sha256:" + strings.Repeat("0", 64),
			}},
		})
	}))
	defer server.Close()
	resolver := Resolver{
		Client: server.Client(), BaseURL: server.URL, CacheDir: t.TempDir(),
		allowDownloadHost: func(string) bool { return true },
	}
	_, err := resolver.Resolve(context.Background(), []config.GitHubReleaseTool{{
		Name: "tool", Repo: "o/r", Tag: "latest",
		Asset: "tool_{version}_linux_arm64.tar.gz", Binary: "tool",
	}})
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("digest mismatch accepted: %v", err)
	}
}

func TestExtractBinaryRejectsUnsafeArchives(t *testing.T) {
	tests := map[string][]tarEntry{
		"absolute":  {{Name: "/tool", Body: []byte("x"), Type: tar.TypeReg}},
		"traversal": {{Name: "../tool", Body: []byte("x"), Type: tar.TypeReg}},
		"symlink":   {{Name: "tool", Linkname: "/etc/passwd", Type: tar.TypeSymlink}},
		"hardlink":  {{Name: "tool", Linkname: "other", Type: tar.TypeLink}},
		"fifo":      {{Name: "tool", Type: tar.TypeFifo}},
		"duplicate": {
			{Name: "tool", Body: []byte("one"), Type: tar.TypeReg},
			{Name: "tool", Body: []byte("two"), Type: tar.TypeReg},
		},
		"missing": {{Name: "other", Body: []byte("x"), Type: tar.TypeReg}},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "tool.tar.gz")
			if err := os.WriteFile(archive, tarGzip(t, entries), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := extractBinary(archive, "tool", filepath.Join(t.TempDir(), "tool")); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func TestExtractBinaryAllowsNormalizedDirectoryEntries(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "tool.tar.gz")
	if err := os.WriteFile(archive, tarGzip(t, []tarEntry{
		{Name: "tool-v1/", Type: tar.TypeDir},
		{Name: "tool-v1/tool", Body: []byte("binary"), Type: tar.TypeReg},
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "tool")
	if err := extractBinary(archive, "tool-v1/tool", destination); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "binary" {
		t.Fatalf("extracted nested binary = %q, %v", data, err)
	}
}

func TestExtractBinaryRejectsExcessiveExpandedSize(t *testing.T) {
	const expectedMaxArchiveExpandedBytes = 512 << 20
	archive := filepath.Join(t.TempDir(), "tool.tar.gz")
	if err := os.WriteFile(archive, tarGzipHeaderOnly(t, tar.Header{
		Name: "unrelated", Size: expectedMaxArchiveExpandedBytes + 1, Mode: 0o644, Typeflag: tar.TypeReg,
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	err := extractBinary(archive, "tool", filepath.Join(t.TempDir(), "tool"))
	if err == nil || !strings.Contains(err.Error(), "expanded size") {
		t.Fatalf("excessive expanded archive accepted: %v", err)
	}
}

func TestExtractBinaryRejectsDirectoryWithBody(t *testing.T) {
	const expectedMaxArchiveExpandedBytes = 512 << 20
	archive := filepath.Join(t.TempDir(), "tool.tar.gz")
	if err := os.WriteFile(archive, tarGzipHeaderOnly(t, tar.Header{
		Name: "dir/", Size: expectedMaxArchiveExpandedBytes + 1, Mode: 0o755, Typeflag: tar.TypeDir,
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	err := extractBinary(archive, "dir/tool", filepath.Join(t.TempDir(), "tool"))
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("directory body bypassed expanded-size limit: %v", err)
	}
}

func TestExecutableCachePathIncludesArchiveBinaryPath(t *testing.T) {
	cacheRoot := t.TempDir()
	archive := tarGzip(t, []tarEntry{
		{Name: "bin/tool", Body: []byte("bin"), Type: tar.TypeReg},
		{Name: "nested/tool", Body: []byte("nested"), Type: tar.TypeReg},
	})
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	archivePath := filepath.Join(cacheRoot, "archives", digest+".tar.gz")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := Resolver{CacheDir: cacheRoot}
	first, err := resolver.materializeAsset(context.Background(), cacheRoot, "", digest, "bin/tool", "tool")
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolver.materializeAsset(context.Background(), cacheRoot, "", digest, "nested/tool", "tool")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("different archive binary paths shared executable cache path %q", first)
	}
	firstData, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondData, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstData) != "bin" || string(secondData) != "nested" {
		t.Fatalf("immutable executable cache contents = %q, %q", firstData, secondData)
	}
}

func TestReleaseLockRoundTripAndSpecMismatch(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	specs := []config.GitHubReleaseTool{{
		Name: "kata", Repo: "kenn-io/kata", Tag: "latest",
		Asset: "kata_{version}_linux_arm64.tar.gz", Binary: "kata",
	}}
	resolved := []config.ResolvedReleaseTool{{
		Name: "kata", Repo: "kenn-io/kata", RequestedTag: "latest", Tag: "v0.10.0",
		Asset: "kata_0.10.0_linux_arm64.tar.gz", URL: "https://github.com/x",
		Digest: "sha256:" + strings.Repeat("a", 64), Binary: "kata",
	}}
	if err := SaveLock(state, specs, resolved); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLock(state, specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Tag != "v0.10.0" || got[0].CachePath == "" {
		t.Fatalf("loaded lock = %+v", got)
	}
	changed := append([]config.GitHubReleaseTool(nil), specs...)
	changed[0].Tag = "v0.11.0"
	got, err = LoadLock(state, changed)
	if err != nil || got != nil {
		t.Fatalf("stale lock used after spec change: %+v, %v", got, err)
	}
}

func TestReleaseLockRejectsTamperedResolvedState(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	specs := []config.GitHubReleaseTool{{
		Name: "kata", Repo: "kenn-io/kata", Tag: "latest",
		Asset: "kata_{version}_linux_arm64.tar.gz", Binary: "kata",
	}}
	resolved := []config.ResolvedReleaseTool{{
		Name: "other", Repo: "kenn-io/kata", RequestedTag: "latest", Tag: "v0.10.0",
		Asset:  "kata_0.10.0_linux_arm64.tar.gz",
		Digest: "sha256:" + strings.Repeat("a", 64), Binary: "kata",
	}}
	if err := SaveLock(state, specs, resolved); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLock(state, specs); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("tampered release tool lock accepted: %v", err)
	}
}

func TestPruneCacheRemovesOnlyOldUnreferencedDigests(t *testing.T) {
	cacheRoot := t.TempDir()
	now := time.Now()
	current := strings.Repeat("a", 64)
	old := strings.Repeat("b", 64)
	recent := strings.Repeat("c", 64)
	for _, digest := range []string{current, old, recent} {
		archive := filepath.Join(cacheRoot, "archives", digest+".tar.gz")
		binary := filepath.Join(cacheRoot, "bin", digest, "tool")
		if err := os.MkdirAll(filepath.Dir(archive), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archive, []byte("archive"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := now.Add(-2 * time.Hour)
	for _, filename := range []string{
		filepath.Join(cacheRoot, "archives", old+".tar.gz"),
		filepath.Join(cacheRoot, "bin", old, "tool"),
		filepath.Join(cacheRoot, "bin", old),
	} {
		if err := os.Chtimes(filename, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}
	resolved := []config.ResolvedReleaseTool{{Digest: "sha256:" + current}}
	if err := pruneCache(cacheRoot, resolved, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cacheRoot, "archives", old+".tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("old unreferenced archive was not pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheRoot, "bin", old)); !os.IsNotExist(err) {
		t.Fatalf("old unreferenced binary directory was not pruned: %v", err)
	}
	for _, filename := range []string{
		filepath.Join(cacheRoot, "archives", current+".tar.gz"),
		filepath.Join(cacheRoot, "bin", current),
		filepath.Join(cacheRoot, "archives", recent+".tar.gz"),
		filepath.Join(cacheRoot, "bin", recent),
	} {
		if _, err := os.Stat(filename); err != nil {
			t.Fatalf("active or recent cache entry %q was pruned: %v", filename, err)
		}
	}
}

type tarEntry struct {
	Name     string
	Body     []byte
	Mode     int64
	Type     byte
	Linkname string
}

func tarGzip(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		mode := entry.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: entry.Name, Size: int64(len(entry.Body)), Mode: mode,
			Typeflag: entry.Type, Linkname: entry.Linkname,
		}); err != nil {
			t.Fatal(err)
		}
		if len(entry.Body) > 0 {
			if _, err := io.Copy(tw, bytes.NewReader(entry.Body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarGzipHeaderOnly(t *testing.T, header tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&header); err != nil {
		t.Fatal(err)
	}
	// Deliberately omit the declared body. The extractor must reject the
	// declared size before attempting to consume it.
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
