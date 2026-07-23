// Package releasetool resolves and materializes trusted user-declared GitHub
// release executables without making ordinary Coop entry depend on the network.
package releasetool

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
)

const (
	defaultAPIBaseURL       = "https://api.github.com"
	maxMetadataBytes        = 2 << 20
	maxArchiveBytes         = 256 << 20
	maxBinaryBytes          = 128 << 20
	maxArchiveExpandedBytes = 512 << 20
	maxArchiveEntries       = 4096
	cachePruneGrace         = time.Hour
)

// Resolver fetches public GitHub release metadata and digest-addressed assets.
// Fields are injectable so automated tests never depend on live GitHub.
type Resolver struct {
	Client   *http.Client
	BaseURL  string
	CacheDir string

	allowDownloadHost func(string) bool
}

type releaseMetadata struct {
	TagName    string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
}

type lockFile struct {
	Version         int                          `json:"version"`
	SpecFingerprint string                       `json:"spec_fingerprint"`
	Tools           []config.ResolvedReleaseTool `json:"tools"`
}

// HydrateConfig applies matching local release-tool state to cfg. Invalid
// derived state is ignored with a warning so rebuild can repair it, while the
// unresolved identity prevents ordinary commands from reusing a resolved
// image accidentally.
func HydrateConfig(cfg *config.Config) error {
	if len(cfg.Tools.GitHubReleases) == 0 {
		return nil
	}
	stateDir, err := StateDir()
	if err != nil {
		return err
	}
	resolved, err := LoadLock(stateDir, cfg.Tools.GitHubReleases)
	if err != nil {
		cfg.Warnings = append(cfg.Warnings,
			fmt.Sprintf("invalid GitHub release tool state ignored: %v; run `coop rebuild` to repair it", err))
		return nil
	}
	cfg.Tools.ResolvedReleases = resolved
	return nil
}

// Resolve resolves, verifies, caches, and extracts every declaration.
func (r Resolver) Resolve(ctx context.Context, specs []config.GitHubReleaseTool) ([]config.ResolvedReleaseTool, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	cacheRoot, err := r.cacheRoot()
	if err != nil {
		return nil, err
	}
	resolved := make([]config.ResolvedReleaseTool, 0, len(specs))
	for _, spec := range specs {
		metadata, err := r.resolveMetadata(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("GitHub release tool %s: %w", spec.Name, err)
		}
		assetName := expandAsset(spec.Asset, metadata.TagName)
		var matches []releaseAsset
		for _, asset := range metadata.Assets {
			if asset.Name == assetName {
				matches = append(matches, asset)
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("GitHub release tool %s: release %s has %d assets named %q", spec.Name, metadata.TagName, len(matches), assetName)
		}
		asset := matches[0]
		digestHex, err := parseDigest(asset.Digest)
		if err != nil {
			return nil, fmt.Errorf("GitHub release tool %s: asset %s: %w", spec.Name, asset.Name, err)
		}
		cachePath, err := r.materializeAsset(ctx, cacheRoot, asset.URL, digestHex, spec.Binary, spec.Name)
		if err != nil {
			return nil, fmt.Errorf("GitHub release tool %s: %w", spec.Name, err)
		}
		resolved = append(resolved, config.ResolvedReleaseTool{
			Name:         spec.Name,
			Repo:         spec.Repo,
			RequestedTag: spec.Tag,
			Tag:          metadata.TagName,
			Asset:        asset.Name,
			URL:          asset.URL,
			Digest:       "sha256:" + digestHex,
			Binary:       spec.Binary,
			CachePath:    cachePath,
		})
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].Name < resolved[j].Name })
	return resolved, nil
}

func (r Resolver) resolveMetadata(ctx context.Context, spec config.GitHubReleaseTool) (releaseMetadata, error) {
	base := strings.TrimRight(r.BaseURL, "/")
	if base == "" {
		base = defaultAPIBaseURL
	}
	endpoint := base + "/repos/" + spec.Repo + "/releases/"
	if spec.Tag == "latest" {
		endpoint += "latest"
	} else {
		endpoint += "tags/" + url.PathEscape(spec.Tag)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return releaseMetadata{}, fmt.Errorf("create metadata request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := r.client().Do(req)
	if err != nil {
		return releaseMetadata{}, fmt.Errorf("fetch release metadata: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return releaseMetadata{}, fmt.Errorf("fetch release metadata: HTTP %s", resp.Status)
	}
	data, err := readLimited(resp.Body, maxMetadataBytes)
	if err != nil {
		return releaseMetadata{}, fmt.Errorf("read release metadata: %w", err)
	}
	var metadata releaseMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return releaseMetadata{}, fmt.Errorf("decode release metadata: %w", err)
	}
	if metadata.TagName == "" {
		return releaseMetadata{}, errors.New("release metadata has an empty tag")
	}
	if metadata.Draft {
		return releaseMetadata{}, fmt.Errorf("release %s is a draft", metadata.TagName)
	}
	if spec.Tag == "latest" && metadata.Prerelease {
		return releaseMetadata{}, fmt.Errorf("latest release %s is a prerelease", metadata.TagName)
	}
	if spec.Tag != "latest" && metadata.TagName != spec.Tag {
		return releaseMetadata{}, fmt.Errorf("release tag %s does not match requested tag %s", metadata.TagName, spec.Tag)
	}
	return metadata, nil
}

func (r Resolver) materializeAsset(ctx context.Context, cacheRoot, assetURL, digestHex, binary, name string) (string, error) {
	archivePath := filepath.Join(cacheRoot, "archives", digestHex+".tar.gz")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o700); err != nil {
		return "", fmt.Errorf("create archive cache: %w", err)
	}
	valid, err := fileDigestMatches(archivePath, digestHex)
	if err != nil {
		return "", fmt.Errorf("verify cached archive: %w", err)
	}
	if !valid {
		if err := r.download(ctx, assetURL, digestHex, archivePath); err != nil {
			return "", err
		}
	}
	executable := executableCachePath(cacheRoot, digestHex, name, binary)
	if err := extractBinary(archivePath, binary, executable); err != nil {
		return "", fmt.Errorf("extract %s from %s: %w", binary, filepath.Base(archivePath), err)
	}
	now := time.Now()
	for _, filename := range []string{
		archivePath, executable, filepath.Dir(executable), filepath.Dir(filepath.Dir(executable)),
	} {
		if err := os.Chtimes(filename, now, now); err != nil {
			return "", fmt.Errorf("mark release cache entry active: %w", err)
		}
	}
	return executable, nil
}

func (r Resolver) download(ctx context.Context, assetURL, digestHex, destination string) error {
	parsed, err := url.Parse(assetURL)
	if err != nil {
		return fmt.Errorf("parse asset URL: %w", err)
	}
	allowHost := r.allowDownloadHost
	customHostPolicy := allowHost != nil
	if allowHost == nil {
		allowHost = allowedGitHubHost
	}
	if (!customHostPolicy && parsed.Scheme != "https") || !allowHost(parsed.Hostname()) {
		return fmt.Errorf("asset URL host %q is not an allowed GitHub HTTPS host", parsed.Hostname())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return fmt.Errorf("create asset request: %w", err)
	}
	client := *r.client()
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if (!customHostPolicy && req.URL.Scheme != "https") || !allowHost(req.URL.Hostname()) {
			return fmt.Errorf("redirect host %q is not an allowed GitHub HTTPS host", req.URL.Hostname())
		}
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download release asset: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download release asset: HTTP %s", resp.Status)
	}
	if resp.ContentLength > maxArchiveBytes {
		return fmt.Errorf("release asset is larger than %d bytes", maxArchiveBytes)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".release-archive-*")
	if err != nil {
		return fmt.Errorf("create archive cache temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, maxArchiveBytes+1))
	if err != nil {
		return fmt.Errorf("download release asset: %w", err)
	}
	if n > maxArchiveBytes {
		return fmt.Errorf("release asset is larger than %d bytes", maxArchiveBytes)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(actual), []byte(digestHex)) != 1 {
		return fmt.Errorf("release asset digest mismatch: got sha256:%s, want sha256:%s", actual, digestHex)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync release asset cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close release asset cache: %w", err)
	}
	if err := os.Rename(tmpName, destination); err != nil {
		return fmt.Errorf("install release asset cache: %w", err)
	}
	return nil
}

func extractBinary(archivePath, binary, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(&expandedReader{reader: gz, remaining: maxArchiveExpandedBytes})
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create executable cache: %w", err)
	}
	var body []byte
	found := 0
	entries := 0
	var expandedBytes int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("archive contains more than %d entries", maxArchiveEntries)
		}
		clean := path.Clean(header.Name)
		normalized := header.Name
		if header.Typeflag == tar.TypeDir {
			normalized = strings.TrimSuffix(normalized, "/")
		}
		if path.IsAbs(header.Name) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != normalized {
			return fmt.Errorf("archive entry %q is not a confined normalized path", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return fmt.Errorf("archive directory %q has non-zero size %d", header.Name, header.Size)
			}
			continue
		case tar.TypeReg:
			if header.Size < 0 || header.Size > maxArchiveExpandedBytes ||
				expandedBytes > maxArchiveExpandedBytes-header.Size {
				return fmt.Errorf("archive expanded size exceeds %d bytes", maxArchiveExpandedBytes)
			}
			expandedBytes += header.Size
		default:
			return fmt.Errorf("archive entry %q has unsupported type %d", header.Name, header.Typeflag)
		}
		if clean != binary {
			continue
		}
		found++
		if found > 1 {
			return fmt.Errorf("archive contains multiple entries for %q", binary)
		}
		if header.Size > maxBinaryBytes {
			return fmt.Errorf("archive binary %q is larger than %d bytes", binary, maxBinaryBytes)
		}
		body, err = io.ReadAll(io.LimitReader(reader, maxBinaryBytes+1))
		if err != nil {
			return fmt.Errorf("read archive binary %q: %w", binary, err)
		}
		if len(body) > maxBinaryBytes {
			return fmt.Errorf("archive binary %q is larger than %d bytes", binary, maxBinaryBytes)
		}
	}
	if found != 1 {
		return fmt.Errorf("archive contains %d entries for %q", found, binary)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".release-binary-*")
	if err != nil {
		return fmt.Errorf("create executable cache temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o755); err != nil {
		return fmt.Errorf("set executable cache mode: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		return fmt.Errorf("write executable cache: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync executable cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close executable cache: %w", err)
	}
	if err := os.Rename(tmpName, destination); err != nil {
		return fmt.Errorf("install executable cache: %w", err)
	}
	return nil
}

// LoadLock returns resolved state only when it belongs to the current specs.
func LoadLock(stateDir string, specs []config.GitHubReleaseTool) ([]config.ResolvedReleaseTool, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "release-tools.lock"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read release tool lock: %w", err)
	}
	var lock lockFile
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("decode release tool lock: %w", err)
	}
	if lock.Version != 1 || lock.SpecFingerprint != config.ReleaseSpecFingerprint(specs) {
		return nil, nil
	}
	if err := validateLockedTools(specs, lock.Tools); err != nil {
		return nil, err
	}
	cacheRoot, err := CacheDir()
	if err != nil {
		return nil, err
	}
	for i := range lock.Tools {
		digestHex, err := parseDigest(lock.Tools[i].Digest)
		if err != nil {
			return nil, fmt.Errorf("release tool lock %s: %w", lock.Tools[i].Name, err)
		}
		lock.Tools[i].CachePath = executableCachePath(cacheRoot, digestHex, lock.Tools[i].Name, lock.Tools[i].Binary)
	}
	sort.Slice(lock.Tools, func(i, j int) bool { return lock.Tools[i].Name < lock.Tools[j].Name })
	return lock.Tools, nil
}

func validateLockedTools(specs []config.GitHubReleaseTool, tools []config.ResolvedReleaseTool) error {
	if len(tools) != len(specs) {
		return fmt.Errorf("release tool lock contains %d tools, want %d", len(tools), len(specs))
	}
	specByName := make(map[string]config.GitHubReleaseTool, len(specs))
	for _, spec := range specs {
		specByName[spec.Name] = spec
	}
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		spec, ok := specByName[tool.Name]
		if !ok {
			return fmt.Errorf("release tool lock contains unknown tool %q", tool.Name)
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			return fmt.Errorf("release tool lock contains duplicate tool %q", tool.Name)
		}
		seen[tool.Name] = struct{}{}
		if tool.Repo != spec.Repo || tool.RequestedTag != spec.Tag || tool.Binary != spec.Binary {
			return fmt.Errorf("release tool lock %s does not match its declaration", tool.Name)
		}
		if tool.Tag == "" || strings.ContainsAny(tool.Tag, "\x00\r\n\t") {
			return fmt.Errorf("release tool lock %s has an invalid resolved tag", tool.Name)
		}
		if tool.Asset != expandAsset(spec.Asset, tool.Tag) {
			return fmt.Errorf("release tool lock %s has asset %q, want %q", tool.Name, tool.Asset, expandAsset(spec.Asset, tool.Tag))
		}
		if _, err := parseDigest(tool.Digest); err != nil {
			return fmt.Errorf("release tool lock %s: %w", tool.Name, err)
		}
	}
	return nil
}

// SaveLock atomically records a successful rebuild's resolved release tools.
func SaveLock(stateDir string, specs []config.GitHubReleaseTool, resolved []config.ResolvedReleaseTool) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create release tool state: %w", err)
	}
	portable := append([]config.ResolvedReleaseTool(nil), resolved...)
	for i := range portable {
		portable[i].CachePath = ""
	}
	sort.Slice(portable, func(i, j int) bool { return portable[i].Name < portable[j].Name })
	data, err := json.MarshalIndent(lockFile{
		Version: 1, SpecFingerprint: config.ReleaseSpecFingerprint(specs), Tools: portable,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode release tool lock: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(stateDir, ".release-tools-lock-*")
	if err != nil {
		return fmt.Errorf("create release tool lock temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("set release tool lock mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write release tool lock: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync release tool lock: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close release tool lock: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(stateDir, "release-tools.lock")); err != nil {
		return fmt.Errorf("install release tool lock: %w", err)
	}
	return nil
}

// PruneCache removes digest entries that are not referenced by the newly
// successful lock and have been inactive long enough that another concurrent
// rebuild cannot reasonably still be materializing them.
func PruneCache(resolved []config.ResolvedReleaseTool) error {
	cacheRoot, err := CacheDir()
	if err != nil {
		return err
	}
	return pruneCache(cacheRoot, resolved, time.Now())
}

func pruneCache(cacheRoot string, resolved []config.ResolvedReleaseTool, now time.Time) error {
	keep := make(map[string]struct{}, len(resolved))
	for _, tool := range resolved {
		digest, err := parseDigest(tool.Digest)
		if err != nil {
			return fmt.Errorf("prune release cache for %s: %w", tool.Name, err)
		}
		keep[digest] = struct{}{}
	}
	cutoff := now.Add(-cachePruneGrace)
	archivesDir := filepath.Join(cacheRoot, "archives")
	archives, err := os.ReadDir(archivesDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read release archive cache: %w", err)
	}
	for _, entry := range archives {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tar.gz") {
			continue
		}
		digest := strings.TrimSuffix(entry.Name(), ".tar.gz")
		if _, err := parseDigest("sha256:" + digest); err != nil {
			continue
		}
		if _, current := keep[digest]; current {
			continue
		}
		filename := filepath.Join(archivesDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect cached release archive %q: %w", entry.Name(), err)
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filename); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("prune cached release archive %q: %w", entry.Name(), err)
			}
		}
	}
	binDir := filepath.Join(cacheRoot, "bin")
	binaries, err := os.ReadDir(binDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read release binary cache: %w", err)
	}
	for _, entry := range binaries {
		if !entry.IsDir() {
			continue
		}
		digest := entry.Name()
		if _, err := parseDigest("sha256:" + digest); err != nil {
			continue
		}
		if _, current := keep[digest]; current {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect cached release binary directory %q: %w", entry.Name(), err)
		}
		if info.ModTime().Before(cutoff) {
			if err := os.RemoveAll(filepath.Join(binDir, digest)); err != nil {
				return fmt.Errorf("prune cached release binary directory %q: %w", entry.Name(), err)
			}
		}
	}
	return nil
}

// StateDir returns Coop's user-local derived state directory.
func StateDir() (string, error) {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "coop"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home for release tool state: %w", err)
	}
	return filepath.Join(home, ".local", "state", "coop"), nil
}

// CacheDir returns Coop's digest-addressed release tool cache directory.
func CacheDir() (string, error) {
	if cache := os.Getenv("XDG_CACHE_HOME"); cache != "" {
		return filepath.Join(cache, "coop", "release-tools"), nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache for release tools: %w", err)
	}
	return filepath.Join(cache, "coop", "release-tools"), nil
}

func (r Resolver) cacheRoot() (string, error) {
	if r.CacheDir != "" {
		return r.CacheDir, nil
	}
	return CacheDir()
}

func (r Resolver) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func expandAsset(template, tag string) string {
	version := strings.TrimPrefix(tag, "v")
	return strings.ReplaceAll(strings.ReplaceAll(template, "{tag}", tag), "{version}", version)
}

func parseDigest(digest string) (string, error) {
	const prefix = "sha256:"
	if !strings.HasPrefix(digest, prefix) {
		return "", fmt.Errorf("digest %q must use sha256", digest)
	}
	value := strings.TrimPrefix(digest, prefix)
	if len(value) != sha256.Size*2 {
		return "", fmt.Errorf("digest %q must contain 64 hexadecimal characters", digest)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("digest %q is not hexadecimal", digest)
	}
	return strings.ToLower(value), nil
}

func fileDigestMatches(filename, digestHex string) (bool, error) {
	file, err := os.Open(filename)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, maxArchiveBytes+1)); err != nil {
		return false, err
	}
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxArchiveBytes {
		return false, nil
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(actual), []byte(digestHex)) == 1, nil
}

func executableCachePath(cacheRoot, digestHex, name, binary string) string {
	binarySum := sha256.Sum256([]byte(binary))
	return filepath.Join(cacheRoot, "bin", digestHex, hex.EncodeToString(binarySum[:]), name)
}

type expandedReader struct {
	reader    io.Reader
	remaining int64
}

func (r *expandedReader) Read(buffer []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, fmt.Errorf("archive expanded size exceeds %d bytes", maxArchiveExpandedBytes)
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	n, err := r.reader.Read(buffer)
	r.remaining -= int64(n)
	return n, err
}

func allowedGitHubHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "github.com" || strings.HasSuffix(host, ".github.com") ||
		host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com")
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}
