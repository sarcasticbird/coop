package worker

import (
	"context"
	"encoding/json"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"net/http"
	"strings"
	"testing"
)

type releaseTestAPI struct {
	calls     []string
	fail      string
	published bool
	tagged    bool
	failed    bool
}

func (f *releaseTestAPI) api(_ context.Context, _, method, path string, _ any) ([]byte, http.Header, error) {
	f.calls = append(f.calls, method+" "+path)
	if method == "POST" && f.fail == path {
		return nil, nil, c.ErrUnavailable
	}
	var v any
	switch {
	case strings.Contains(path, "git/ref/tags/"):
		if !f.tagged {
			return nil, nil, &GitHubError{StatusCode: 404, Category: "not_found"}
		}
		v = map[string]any{"object": map[string]string{"sha": strings.Repeat("d", 40), "type": "tag"}}
	case strings.HasSuffix(path, "git/tags"):
		v = map[string]string{"sha": strings.Repeat("d", 40)}
	case strings.HasSuffix(path, "git/refs"):
		f.tagged = true
		v = map[string]any{"ref": "refs/tags/v0.2.7", "object": map[string]string{"sha": strings.Repeat("d", 40), "type": "tag"}}
	case strings.Contains(path, "actions/workflows/"):
		conclusion := ""
		if f.published {
			conclusion = "success"
		}
		if f.failed {
			conclusion = "failure"
		}
		v = map[string]any{"workflow_runs": []any{map[string]string{"head_sha": strings.Repeat("c", 40), "head_branch": "v0.2.7", "conclusion": conclusion}}}
	case strings.Contains(path, "releases/tags/"):
		if !f.published {
			return nil, nil, &GitHubError{StatusCode: 404, Category: "not_found"}
		}
		v = map[string]any{"tag_name": "v0.2.7", "html_url": "https://github.com/code/repo/releases/tag/v0.2.7", "published_at": "2026-10-25T00:00:00Z", "assets": []any{map[string]string{"name": "checksums.txt"}}}
	default:
		return nil, nil, errors.New("unexpected release request " + path)
	}
	raw, _ := json.Marshal(v)
	return raw, nil, nil
}

func TestReleaseWorkflowFailureIsActionable(t *testing.T) {
	cfg, _, _ := nativeWorkFixture(t)
	cfg.StateDir = t.TempDir()
	f := &releaseTestAPI{failed: true}
	h := c.Handoff{ID: "ho_failure", Input: c.HandoffInput{Tag: "v0.2.7", Workflow: "release.yml"}}
	target := releaseTarget{Repository: "code/repo", SHA: strings.Repeat("c", 40)}
	r, err := publishNativeRelease(t.Context(), cfg, f, h, target)
	if err != nil || r.Phase != "blocked" {
		t.Fatal("failed CI reported as pending", r, err)
	}
	f.failed = false
	f.published = true
	r, err = publishNativeRelease(t.Context(), cfg, f, h, target)
	if err != nil || r.Phase != "published" {
		t.Fatal("successful workflow rerun did not recover", r, err)
	}
}
func TestReleasePublicationJournalAndUnknownWrites(t *testing.T) {
	for _, fail := range []string{"", "repos/code/repo/git/tags", "repos/code/repo/git/refs"} {
		t.Run(fail, func(t *testing.T) {
			cfg, _, _ := nativeWorkFixture(t)
			cfg.StateDir = t.TempDir()
			h := c.Handoff{ID: "ho_release", Input: c.HandoffInput{Tag: "v0.2.7", Workflow: "release.yml"}}
			f := &releaseTestAPI{fail: fail}
			target := releaseTarget{Repository: "code/repo", SHA: strings.Repeat("c", 40)}
			receipt, err := publishNativeRelease(t.Context(), cfg, f, h, target)
			if fail != "" {
				if err == nil {
					t.Fatal("lost write response accepted")
				}
				before := len(f.calls)
				if _, err = publishNativeRelease(t.Context(), cfg, f, h, target); !errors.Is(err, c.ErrConflict) || len(f.calls) != before {
					t.Fatal("uncertain write replayed", err)
				}
				return
			}
			if err != nil || receipt.Phase != "waiting" {
				t.Fatal("release did not wait for CI", receipt, err)
			}
			f.published = true
			receipt, err = publishNativeRelease(t.Context(), cfg, f, h, target)
			if err != nil || receipt.Phase != "published" {
				t.Fatal("missing published receipt", receipt, err)
			}
			writes := 0
			for _, call := range f.calls {
				if strings.HasPrefix(call, "POST ") {
					writes++
				}
			}
			if writes != 2 {
				t.Fatal("tag writes replayed", f.calls)
			}
		})
	}
}
