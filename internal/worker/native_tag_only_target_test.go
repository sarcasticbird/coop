package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

type tagOnlyReadAPI func(string) ([]byte, error)

func (f tagOnlyReadAPI) api(_ context.Context, _, method, endpoint string, _ any) ([]byte, http.Header, error) {
	if method != "GET" {
		panic("provenance verifier attempted a mutation")
	}
	raw, err := f(endpoint)
	return raw, nil, err
}

func TestTagOnlyIntegratedSourceVerification(t *testing.T) {
	for _, change := range []string{"none", "unmerged", "head", "merge", "base", "repository", "tree", "parent", "ancestry", "version"} {
		t.Run(change, func(t *testing.T) {
			_, cat, h := tagOnlyFixture(t)
			r := h.Input.TagOnly
			repo := cat.Tasks[0].Task.Repo
			if change == "version" {
				h.Input.TagOnly.VersionPRURL = "https://github.com/" + repo + "/pull/2"
			}
			api := tagOnlyReadAPI(func(endpoint string) ([]byte, error) {
				var v any
				switch {
				case strings.Contains(endpoint, "/pulls/"):
					merged, head, sha, base, repository := true, h.Input.HeadSHA, r.IntegratedCommitSHA, h.Input.BaseBranch, repo
					if change == "unmerged" {
						merged = false
					}
					if change == "head" {
						head = strings.Repeat("f", 40)
					}
					if change == "merge" {
						sha = strings.Repeat("f", 40)
					}
					if change == "base" {
						base = "other"
					}
					if change == "repository" {
						repository = "foreign/repo"
					}
					v = map[string]any{"merged": merged, "merge_commit_sha": sha, "head": map[string]string{"sha": head}, "base": map[string]any{"ref": base, "repo": map[string]string{"full_name": repository}}}
				case strings.Contains(endpoint, "/git/commits/"):
					sha := strings.TrimPrefix(endpoint, "repos/"+repo+"/git/commits/")
					tree, parent := r.SourceTreeSHA, h.Input.BaseSHA
					if change == "tree" {
						tree = strings.Repeat("f", 40)
					}
					if change == "parent" {
						parent = strings.Repeat("f", 40)
					}
					v = map[string]any{"sha": sha, "tree": map[string]string{"sha": tree}, "parents": []map[string]string{{"sha": parent}}}
				case strings.Contains(endpoint, "/compare/"):
					behind := 0
					if change == "ancestry" {
						behind = 1
					}
					v = map[string]any{"status": "ahead", "behind_by": behind}
				default:
					t.Fatal("unexpected provenance endpoint", endpoint)
				}
				return json.Marshal(v)
			})
			got, err := nativeTagOnlyTarget(t.Context(), api, repo, h)
			if change == "none" {
				if err != nil || got.Repository != repo || got.SHA != r.IntegratedCommitSHA {
					t.Fatalf("%+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("unverified provenance accepted")
			}
		})
	}
}

func TestTagOnlyExpectedObjectMatchesGit(t *testing.T) {
	cfg, cat, h := tagOnlyFixture(t)
	j := newTagOnlyIntent(cfg, h, releaseTarget{Repository: cat.Tasks[0].Task.Repo, SHA: h.Input.TagOnly.IntegratedCommitSHA}, nativeActionRecord{})
	body := fmt.Sprintf("object %s\ntype commit\ntag %s\ntagger %s <%s> %d +0000\n\n%s", j.Binding.Target.SHA, h.Input.Tag, j.Name, j.Email, h.CreatedAt.Unix(), j.Message)
	cmd := exec.Command("git", "hash-object", "--stdin", "-t", "tag")
	cmd.Stdin = strings.NewReader(body)
	raw, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(raw)) != j.Object {
		t.Fatalf("git=%s expected=%s err=%v", raw, j.Object, err)
	}
}
