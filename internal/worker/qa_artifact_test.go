package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"
	"time"
)

type qaBundleEntry struct {
	name, content, link string
	mode                int64
	kind                byte
	uid                 int
	modified            time.Time
}

func qaBundle(t *testing.T, entries ...qaBundleEntry) []byte {
	t.Helper()
	var raw bytes.Buffer
	z := gzip.NewWriter(&raw)
	w := tar.NewWriter(z)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: kind, Linkname: e.link, Size: int64(len(e.content)), ModTime: time.Unix(123, 0)}
		h.Uid = e.uid
		if !e.modified.IsZero() {
			h.ModTime = e.modified
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, e.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func TestSourceBundleSHACanonical(t *testing.T) {
	entries := []qaBundleEntry{
		{name: "root/z", content: "hello\n", mode: 0755},
		{name: "root/a", link: "z", mode: 0777, kind: tar.TypeSymlink},
	}
	// Spell out the wire representation independently of the implementation.
	empty, file := sha256.Sum256(nil), sha256.Sum256([]byte("hello\n"))
	canonical := fmt.Sprintf("a\x00511\x0050\x00z\x00%x\x00z\x00493\x0048\x00\x00%x\x00", empty, file)
	want := sha256.Sum256([]byte(canonical))
	digest, err := sourceBundleSHA(qaBundle(t, entries...))
	if err != nil || digest != hex.EncodeToString(want[:]) {
		t.Fatalf("digest = %q, %v; want %x", digest, err, want)
	}
	reordered := []qaBundleEntry{{name: "different/", kind: tar.TypeDir, mode: 0700}, entries[1], entries[0]}
	for i := 1; i < len(reordered); i++ {
		reordered[i].name = strings.Replace(reordered[i].name, "root/", "different/", 1)
		reordered[i].uid = 1000
		reordered[i].modified = time.Unix(456, 0)
	}
	raw := qaBundle(t, reordered...)
	// Gzip timestamp is not part of the source identity.
	raw[4] = 1
	if got, err := sourceBundleSHA(raw); err != nil || got != digest {
		t.Fatalf("archive order/root/metadata changed digest: %q, %v", got, err)
	}
	for _, field := range []string{"mode", "content", "link", "path", "type"} {
		t.Run(field, func(t *testing.T) {
			changed := append([]qaBundleEntry(nil), entries...)
			switch field {
			case "mode":
				changed[0].mode = 0644
			case "content":
				changed[0].content += "!"
			case "link":
				changed[1].link = "elsewhere"
			case "path":
				changed[0].name = "root/other"
			case "type":
				changed[1].kind, changed[1].link = tar.TypeReg, ""
			}
			if got, err := sourceBundleSHA(qaBundle(t, changed...)); err != nil || got == digest {
				t.Fatalf("changed %s did not change digest: %q, %v", field, got, err)
			}
		})
	}
}

func TestSourceBundleSHARejectsInvalidArchives(t *testing.T) {
	valid := qaBundleEntry{name: "root/file", content: "source", mode: 0644}
	for _, tc := range []struct {
		name    string
		entries []qaBundleEntry
	}{
		{"empty", nil},
		{"directories only", []qaBundleEntry{{name: "root/", kind: tar.TypeDir}}},
		{"no root", []qaBundleEntry{{name: "file"}}},
		{"absolute root", []qaBundleEntry{{name: "/file"}}},
		{"dot root", []qaBundleEntry{{name: "./file"}}},
		{"parent root", []qaBundleEntry{{name: "../file"}}},
		{"backslash root", []qaBundleEntry{{name: "bad\\root/file"}}},
		{"mixed roots", []qaBundleEntry{valid, {name: "other/file"}}},
		{"dot entry", []qaBundleEntry{{name: "root/."}}},
		{"parent entry", []qaBundleEntry{{name: "root/../file"}}},
		{"absolute entry", []qaBundleEntry{{name: "root//file"}}},
		{"unclean entry", []qaBundleEntry{{name: "root/a/../file"}}},
		{"backslash entry", []qaBundleEntry{{name: "root/a\\file"}}},
		{"invalid directory", []qaBundleEntry{valid, {name: "root/../outside/", kind: tar.TypeDir}}},
		{"duplicate", []qaBundleEntry{valid, valid}},
		{"hardlink", []qaBundleEntry{{name: "root/link", kind: tar.TypeLink, link: "root/file"}}},
		{"fifo", []qaBundleEntry{{name: "root/fifo", kind: tar.TypeFifo}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := sourceBundleSHA(qaBundle(t, tc.entries...)); err == nil || got != "" {
				t.Fatalf("invalid archive accepted: %q, %v", got, err)
			}
		})
	}
	raw := qaBundle(t, valid)
	corrupt := append([]byte(nil), raw...)
	corrupt[len(corrupt)-8] ^= 1
	for name, data := range map[string][]byte{"not gzip": []byte("source"), "truncated": raw[:len(raw)/2], "gzip checksum": corrupt, "missing trailer": raw[:len(raw)-8]} {
		t.Run(name, func(t *testing.T) {
			if got, err := sourceBundleSHA(data); err == nil || got != "" {
				t.Fatalf("corrupt archive accepted: %q, %v", got, err)
			}
		})
	}
}

func TestSourceBundleSHABounds(t *testing.T) {
	entries := make([]qaBundleEntry, MaxConnection)
	for i := range entries {
		entries[i] = qaBundleEntry{name: fmt.Sprintf("root/%05d", i), mode: 0644}
	}
	if _, err := sourceBundleSHA(qaBundle(t, entries...)); err != nil {
		t.Fatal("entry-count boundary rejected:", err)
	}
	entries = append(entries, qaBundleEntry{name: "root/extra"})
	if _, err := sourceBundleSHA(qaBundle(t, entries...)); err == nil {
		t.Fatal("unbounded entry count accepted")
	}
	for i := range entries {
		entries[i].kind = tar.TypeDir
	}
	entries[0] = qaBundleEntry{name: "root/file", content: "source"}
	if _, err := sourceBundleSHA(qaBundle(t, entries...)); err == nil {
		t.Fatal("directory headers bypassed entry-count bound")
	}
	// A 512-byte header and 1024-byte terminator fill the remaining bytes.
	boundary := qaBundle(t, qaBundleEntry{name: "root/large", content: strings.Repeat("x", (64<<20)-1536)})
	if _, err := sourceBundleSHA(boundary); err != nil {
		t.Fatal("decompressed-size boundary rejected:", err)
	}
	for _, size := range []int{64 << 20, (64 << 20) + 1} {
		raw := qaBundle(t, qaBundleEntry{name: "root/large", content: strings.Repeat("x", size)})
		if _, err := sourceBundleSHA(raw); err == nil {
			t.Fatalf("archive exceeding decompressed limit accepted: content size %d", size)
		}
	}
}

func qaArtifactClient(t *testing.T, endpoint string, result CommandResult, runErr error) (*GitHubClient, *fakeCommands) {
	t.Helper()
	f := &fakeCommands{respond: func(owner, got, method string, body []byte) (CommandResult, error) {
		if owner != "code" || got != endpoint || method != "GET" || len(body) != 0 {
			t.Fatalf("unexpected native request: %s %s %s %q", owner, method, got, body)
		}
		return result, runErr
	}}
	return NewGitHubClient(f), f
}

func TestBindQAArtifactSource(t *testing.T) {
	head := strings.Repeat("a", 40)
	raw := qaBundle(t, qaBundleEntry{name: "generated/file", content: "source", mode: 0644})
	file := sha256.Sum256([]byte("source"))
	wantSHA := sha256.Sum256([]byte(fmt.Sprintf("file\x00420\x0048\x00\x00%x\x00", file)))
	want := QAArtifact{Kind: "source", HeadSHA: head, SHA256: hex.EncodeToString(wantSHA[:]), URL: "https://api.github.com/repos/code/repo/tarball/" + head, Revision: "source-bundle-v1"}
	task := Task{Repo: "code/repo", QA: QAPolicy{Required: true}}
	for _, field := range []string{"no candidate", "exact", "digest", "head", "url", "revision"} {
		t.Run(field, func(t *testing.T) {
			candidate := want
			ptr := &candidate
			switch field {
			case "no candidate":
				ptr = nil
			case "digest":
				candidate.SHA256 = strings.Repeat("0", 64)
			case "head":
				candidate.HeadSHA = strings.Repeat("b", 40)
			case "url":
				candidate.URL += "?spoof"
			case "revision":
				candidate.Revision = "other"
			}
			g, f := qaArtifactClient(t, "repos/code/repo/tarball/"+head, response("200 OK", string(raw)), nil)
			pr := PR{HeadSHA: head}
			err := g.bindQAArtifact(context.Background(), task, &pr, ptr)
			if field == "no candidate" || field == "exact" {
				if err != nil || pr.QAArtifact == nil || *pr.QAArtifact != want {
					t.Fatalf("binding = %+v, %v; want %+v", pr.QAArtifact, err, want)
				}
			} else if err == nil || pr.QAArtifact != nil {
				t.Fatalf("changed source accepted: %+v, %v", pr.QAArtifact, err)
			}
			if len(f.calls) != 1 {
				t.Fatalf("native reads = %v", f.calls)
			}
		})
	}
}

func TestBindQAArtifactSourceNativeFailures(t *testing.T) {
	head := strings.Repeat("a", 40)
	// Incompressible bytes make the compressed API response exceed 2 MiB.
	content := make([]byte, MaxAPIPage+4096)
	_, _ = rand.New(rand.NewSource(1)).Read(content)
	oversized := qaBundle(t, qaBundleEntry{name: "root/file", content: string(content)})
	if len(oversized) <= MaxAPIPage {
		t.Fatal("fixture did not exceed compressed API limit")
	}
	for _, tc := range []struct {
		name, status, body, category string
		err                          error
	}{
		{"missing", "404 Not Found", "{}", "not_found", nil},
		{"transport", "200 OK", "", "request_failed", errors.New("sentinel-secret")},
		{"invalid bundle", "200 OK", "not gzip", "invalid_QA_source_bundle", nil},
		{"compressed limit", "200 OK", string(oversized), "response_too_large", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := qaArtifactClient(t, "repos/code/repo/tarball/"+head, response(tc.status, tc.body), tc.err)
			pr := PR{HeadSHA: head}
			err := g.bindQAArtifact(context.Background(), Task{Repo: "code/repo", QA: QAPolicy{Required: true}}, &pr, nil)
			var apiErr *GitHubError
			if !errors.As(err, &apiErr) || apiErr.Category != tc.category || pr.QAArtifact != nil {
				t.Fatalf("binding = %+v, %v; want %s", pr.QAArtifact, err, tc.category)
			}
		})
	}
}

func TestBindQAArtifactWorkflowNativeValidation(t *testing.T) {
	head, digest := strings.Repeat("a", 40), strings.Repeat("b", 64)
	want := QAArtifact{Kind: "workflow", HeadSHA: head, SHA256: digest, URL: "https://api.github.com/repos/code/repo/actions/artifacts/17", Revision: "23"}
	for _, fault := range []string{"none", "digest", "digest prefix", "candidate digest", "head", "run", "expired", "id", "zero run", "missing artifact", "missing run", "malformed", "deleted", "transport"} {
		t.Run(fault, func(t *testing.T) {
			candidate := want
			run := map[string]any{"id": 23, "head_sha": head}
			native := map[string]any{"id": 17, "digest": "sha256:" + digest, "expired": false, "workflow_run": run}
			switch fault {
			case "digest":
				native["digest"] = "sha256:" + strings.Repeat("c", 64)
			case "digest prefix":
				native["digest"] = digest
			case "candidate digest":
				candidate.SHA256 = "not-sha"
				native["digest"] = "sha256:not-sha"
			case "head":
				run["head_sha"] = strings.Repeat("c", 40)
			case "run":
				run["id"] = 24
			case "expired":
				native["expired"] = true
			case "id":
				native["id"] = 18
			case "zero run":
				run["id"] = 0
			case "missing artifact":
				native = map[string]any{}
			case "missing run":
				delete(native, "workflow_run")
			}
			result, runErr := jsonResponse(native), error(nil)
			if fault == "malformed" {
				result = response("200 OK", "{")
			}
			if fault == "deleted" {
				result = response("404 Not Found", "{}")
			}
			if fault == "transport" {
				runErr = errors.New("sentinel-secret")
			}
			g, f := qaArtifactClient(t, "repos/code/repo/actions/artifacts/17", result, runErr)
			pr := PR{HeadSHA: head}
			err := g.bindQAArtifact(context.Background(), Task{Repo: "code/repo", QA: QAPolicy{Required: true}}, &pr, &candidate)
			if fault == "none" {
				if err != nil || pr.QAArtifact == nil || *pr.QAArtifact != want || pr.QAArtifact == &candidate {
					t.Fatalf("native binding = %+v, %v", pr.QAArtifact, err)
				}
			} else if err == nil || pr.QAArtifact != nil {
				t.Fatalf("invalid native artifact accepted: %+v, %v", pr.QAArtifact, err)
			}
			if len(f.calls) != 1 {
				t.Fatalf("native reads = %v", f.calls)
			}
		})
	}
}

func TestBindQAArtifactRejectsBeforeNativeRead(t *testing.T) {
	head := strings.Repeat("a", 40)
	valid := QAArtifact{Kind: "workflow", HeadSHA: head, URL: "https://api.github.com/repos/code/repo/actions/artifacts/17"}
	for _, fault := range []string{"optional", "bad PR head", "kind", "head", "foreign repo", "foreign host", "query", "no id", "path"} {
		t.Run(fault, func(t *testing.T) {
			candidate, pr := valid, PR{HeadSHA: head}
			task := Task{Repo: "code/repo", QA: QAPolicy{Required: true}}
			switch fault {
			case "optional":
				task.QA.Required = false
			case "bad PR head":
				pr.HeadSHA = "head"
			case "kind":
				candidate.Kind = "external"
			case "head":
				candidate.HeadSHA = strings.Repeat("b", 40)
			case "foreign repo":
				candidate.URL = strings.Replace(candidate.URL, "code/repo", "other/repo", 1)
			case "foreign host":
				candidate.URL = strings.Replace(candidate.URL, "api.github.com", "example.com", 1)
			case "query":
				candidate.URL += "?id=1"
			case "no id":
				candidate.URL = strings.TrimSuffix(candidate.URL, "17")
			case "path":
				candidate.URL += "/zip"
			}
			g, f := qaArtifactClient(t, "no request allowed", CommandResult{}, nil)
			err := g.bindQAArtifact(context.Background(), task, &pr, &candidate)
			if (err == nil) != (fault == "optional") || len(f.calls) != 0 || pr.QAArtifact != nil {
				t.Fatalf("preflight = %+v, %v; calls %v", pr.QAArtifact, err, f.calls)
			}
		})
	}
}
