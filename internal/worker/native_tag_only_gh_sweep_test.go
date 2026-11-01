package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func rewriteGHSweepArchive(t *testing.T, raw []byte, change func(string, *tar.Header, []byte) []byte) []byte {
	t.Helper()
	z, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = z.Close() }()
	r := tar.NewReader(z)
	var out bytes.Buffer
	compressed := gzip.NewWriter(&out)
	w := tar.NewWriter(compressed)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := strings.SplitN(h.Name, "/", 2)[1]
		body = change(name, h, body)
		h.Size = int64(len(body))
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func ghSweepPayloadFixture(t *testing.T) (c.Handoff, []byte, []byte) {
	t.Helper()
	_, _, h, _, _ := tagOnlyVersionFixture(t)
	source, err := os.ReadFile("testdata/retained_pr9/source.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := sourceBundleSHA(source); err != nil || got != pr9Artifact {
		t.Fatal("actual PR9 archive changed", err)
	}
	h.Input.Tag = "v0.5.0"
	version := rewriteGHSweepArchive(t, source, func(name string, _ *tar.Header, body []byte) []byte {
		if name == "gh-sweep" {
			return bytes.Replace(body, []byte(`VERSION="0.4.0"`), []byte(`VERSION="0.5.0"`), 1)
		}
		return body
	})
	r := &h.Input.TagOnly
	h.Input.PRURL, h.Input.HeadSHA, h.Input.BaseSHA, h.Input.ArtifactSHA = pr9URL, pr9Head, pr9Base, pr9Artifact
	r.SourceCommitSHA, r.SourceTreeSHA, r.IntegratedTreeSHA = pr9Head, "88a6d8efe9db403584a8bf69bac874b41c926614", "88a6d8efe9db403584a8bf69bac874b41c926614"
	r.VersionPRURL, r.PayloadSchema, r.SourceBundleSHA = "https://github.com/sarcasticbird/gh-sweep/pull/10", c.TagOnlyGHSweepPayloadV1, pr9Artifact
	r.VersionBundleSHA, err = sourceBundleSHA(version)
	if err != nil {
		t.Fatal(err)
	}
	r.PayloadSHA, err = tagOnlyPayloadSHA(source, r.PayloadSchema, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateHandoff(h.Input); err != nil {
		t.Fatal(err)
	}
	return h, source, version
}

func TestTagOnlyGHSweepActualPR9Payload(t *testing.T) {
	h, source, version := ghSweepPayloadFixture(t)
	if err := VerifyTagOnlyPayload(h.Input, source, source, version, version); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"executable-byte", "executable-mode", "lib/sweep.sh", "lib/runs.jq", "library-mode", "path", "link", "wrong-version", "duplicate", "comment", "heredoc", "function", "fake-header", "arbitrary-schema"} {
		t.Run(change, func(t *testing.T) {
			bad := rewriteGHSweepArchive(t, version, func(name string, header *tar.Header, body []byte) []byte {
				if name == "lib/sweep.sh" {
					switch change {
					case "library-mode":
						header.Mode ^= 0100
					case "path":
						header.Name += ".renamed"
					case "link":
						header.Typeflag, header.Linkname = tar.TypeSymlink, "foreign.sh"
						return nil
					}
				}
				if name == change {
					return append(body, '\n')
				}
				if name != "gh-sweep" {
					return body
				}
				switch change {
				case "executable-byte":
					return append(body, []byte("\n# altered executable\n")...)
				case "executable-mode":
					header.Mode ^= 0100
				case "wrong-version":
					return bytes.Replace(body, []byte(`VERSION="0.5.0"`), []byte(`VERSION="0.6.0"`), 1)
				case "duplicate":
					return append(body, []byte("\nVERSION=\"0.5.0\"\n")...)
				case "comment":
					return append(body, []byte("\n# VERSION=\"0.5.0\"\n")...)
				case "heredoc":
					return append(body, []byte("\ncat <<'EOF'\nVERSION=\"0.5.0\"\nEOF\n")...)
				case "function":
					return append(body, []byte("\nfake() { VERSION=\"0.5.0\"; }\n")...)
				case "fake-header":
					return bytes.Replace(body, []byte("\nVERSION="), []byte("\n# VERSION="), 1)
				}
				return body
			})
			in := h.Input
			in.TagOnly.VersionBundleSHA, _ = sourceBundleSHA(bad)
			if change == "duplicate" || change == "comment" || change == "heredoc" || change == "function" || change == "fake-header" {
				if _, err := tagOnlyPayloadSHA(bad, c.TagOnlyGHSweepPayloadV1, "", false); err == nil {
					t.Fatal("ambiguous shell version assignment parsed", change)
				}
			}
			if change == "arbitrary-schema" {
				in.TagOnly.PayloadSchema = "ignore-shell-files"
			}
			if err := VerifyTagOnlyPayload(in, source, source, bad, bad); err == nil {
				t.Fatal("actual PR9 payload drift accepted", change)
			}
		})
	}
}

func TestTagOnlyGHSweepProductionTarget(t *testing.T) {
	h, source, version := ghSweepPayloadFixture(t)
	r := h.Input.TagOnly
	repo := "sarcasticbird/gh-sweep"
	api := tagOnlyReadAPI(func(endpoint string) ([]byte, error) {
		var result any
		switch {
		case strings.Contains(endpoint, "/pulls/"):
			head, merge := h.Input.HeadSHA, r.IntegratedCommitSHA
			if strings.HasSuffix(endpoint, "/10") {
				head, merge = r.VersionHeadSHA, r.VersionCommitSHA
			}
			result = map[string]any{"merged": true, "merge_commit_sha": merge, "head": map[string]string{"sha": head}, "base": map[string]any{"ref": h.Input.BaseBranch, "repo": map[string]string{"full_name": repo}}}
		case strings.Contains(endpoint, "/git/commits/"):
			sha := strings.TrimPrefix(endpoint, "repos/"+repo+"/git/commits/")
			tree, parent := r.SourceTreeSHA, h.Input.BaseSHA
			if sha == r.VersionHeadSHA || sha == r.VersionCommitSHA {
				tree, parent = r.VersionTreeSHA, r.VersionBaseSHA
			}
			result = map[string]any{"sha": sha, "tree": map[string]string{"sha": tree}, "parents": []map[string]string{{"sha": parent}}}
		case strings.Contains(endpoint, "/compare/"):
			result = map[string]any{"status": "ahead", "behind_by": 0}
		case strings.Contains(endpoint, "/tarball/"):
			if strings.HasSuffix(endpoint, r.VersionHeadSHA) || strings.HasSuffix(endpoint, r.VersionCommitSHA) {
				return version, nil
			}
			return source, nil
		default:
			t.Fatal("unexpected request", endpoint)
		}
		return json.Marshal(result)
	})
	target, err := nativeTagOnlyTarget(t.Context(), api, repo, h)
	if err != nil || target.SHA != r.VersionCommitSHA {
		t.Fatal("gh-sweep version target rejected", target, err)
	}
}

func TestTagOnlyGHSweepEveryExecutableAndLibrary(t *testing.T) {
	h, source, version := ghSweepPayloadFixture(t)
	var paths []string
	_ = rewriteGHSweepArchive(t, version, func(name string, header *tar.Header, body []byte) []byte {
		if header.Typeflag == tar.TypeReg && (header.Mode&0111 != 0 || strings.HasPrefix(name, "lib/")) {
			paths = append(paths, name)
		}
		return body
	})
	if len(paths) < 3 {
		t.Fatal("actual executable/library fixture is incomplete")
	}
	for _, name := range paths {
		for _, change := range []string{"bytes", "mode"} {
			t.Run(name+"/"+change, func(t *testing.T) {
				bad := rewriteGHSweepArchive(t, version, func(path string, header *tar.Header, body []byte) []byte {
					if path == name {
						if change == "mode" {
							header.Mode ^= 0100
						} else {
							return append(body, 0)
						}
					}
					return body
				})
				in := h.Input
				in.TagOnly.VersionBundleSHA, _ = sourceBundleSHA(bad)
				if err := VerifyTagOnlyPayload(in, source, source, bad, bad); err == nil {
					t.Fatal("executable/library drift escaped fixed contract")
				}
			})
		}
	}
}
