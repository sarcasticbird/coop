package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPRAdmissionRequiresCanonicalTaskRepository(t *testing.T) {
	for _, kind := range []string{"review", "fix", "deliver", "merged"} {
		for _, ref := range []string{
			"https://github.com/code/repo/pull/5",
			"https://github.com/code/other/pull/5",
			"https://github.com/code/repo-extra/pull/5",
			"https://github.com/other/repo/pull/5",
			"https://github.com/code/Repo/pull/5",
			"https://github.com/code/repo/issues/5",
			"https://github.com/code/repo/pull/05",
			"https://github.com/code/repo/pull/5?x=1",
			"https://github.com/code/repo/pull/5#fragment",
			"https://github.com/code/%72epo/pull/5",
		} {
			t.Run(kind+"/"+ref, func(t *testing.T) {
				c, s, now := policyFixture(t)
				s.Tasks[0].Ready = false
				p := PR{URL: ref, TaskURL: s.Tasks[0].URL, GoalURL: s.Goals[0].URL, HeadSHA: strings.Repeat("a", 40), Author: c.Implementer, ChecksState: "success", MergeState: "CLEAN"}
				state := "APPROVED"
				switch kind {
				case "review":
					c.Role, c.Self, state = "planner", c.Reviewer, ""
				case "fix":
					state = "CHANGES_REQUESTED"
				case "merged":
					p.Merged, p.Closed = true, true
				}
				p.Reviews = []Review{{ID: "manual-formal-review", State: state, HeadSHA: p.HeadSHA, Author: c.Reviewer, SubmittedAt: now}}
				s.PRs = []PR{p}
				jobKind := kind
				if kind == "merged" {
					jobKind = "deliver"
				}
				j := Job{Kind: jobKind, GoalURL: p.GoalURL, TaskURL: p.TaskURL, PRURL: p.URL, HeadSHA: p.HeadSHA}
				want := ref == "https://github.com/code/repo/pull/5"
				if ok, reason := Admit(c, j, s, now); ok != want {
					t.Fatalf("admitted=%v, want %v: %s", ok, want, reason)
				}
				if jobs := Select(c, s, State{}, now); (len(jobs) == 1 && jobs[0].Kind == jobKind) != want {
					t.Fatalf("selected jobs=%+v, want admitted=%v", jobs, want)
				}
				if kind == "merged" {
					c.Role, c.Self = "planner", c.Reviewer
					s.Tasks[0].Completed, s.Tasks[0].Closed = true, true
					if events := planningEvents(c, s, now); (len(events) == 1) != want {
						t.Fatalf("delivery completion events=%+v, want admitted=%v", events, want)
					}
				}
			})
		}
	}
}

func TestTaskAdmissionRequiresCanonicalIssueRepository(t *testing.T) {
	for _, ref := range []string{
		"https://github.com/code/repo/issues/4",
		"https://github.com/code/repo-extra/issues/4",
		"https://github.com/code/Repo/issues/4",
		"https://github.com/code/repo/pull/4",
		"https://github.com/code/repo/discussions/4",
		"https://github.com/code/repo/issues/04",
		"https://github.com/code/repo/issues/4?",
	} {
		t.Run(ref, func(t *testing.T) {
			c, s, _ := policyFixture(t)
			s.Tasks[0].URL = ref
			want := ref == "https://github.com/code/repo/issues/4"
			if got := taskAdmitted(c, s.Tasks[0], s.Goals[0], s); got != want {
				t.Fatalf("task admitted=%v, want %v", got, want)
			}
		})
	}
}

func TestPRRepositoryBindingDiscoveryAndRevalidation(t *testing.T) {
	for _, repo := range []string{"code/repo", "code/other", "code/repo-extra"} {
		for _, merged := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/merged=%t", repo, merged), func(t *testing.T) {
				c, old, now := policyFixture(t)
				c = Defaults(c)
				c.Repositories, c.Projects = nil, nil
				c.ProjectOwners = nil
				c.DiscussionRepositories = []string{"planning/board"}
				goal, task := old.Goals[0], old.Tasks[0]
				goal.URL = "https://github.com/planning/board/discussions/1"
				goal.Body = encodeMetadata("goal", map[string]any{"repositories": goal.Repositories})
				task.GoalURL = goal.URL
				ref := "https://github.com/" + repo + "/pull/5"
				head := strings.Repeat("a", 40)
				page := func(nodes any) map[string]any {
					return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}
				}
				f := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
					switch {
					case strings.HasPrefix(endpoint, "installation/repositories"):
						return jsonResponse(map[string]any{"repositories": []any{map[string]any{"full_name": "code/repo", "has_issues": true}, map[string]any{"full_name": "code/other", "has_issues": true}, map[string]any{"full_name": "code/repo-extra", "has_issues": true}}}), nil
					case strings.HasPrefix(endpoint, "repos/code/repo/issues/4/comments"):
						return jsonResponse([]any{map[string]any{"id": 10, "body": encodeMetadata("task", taskMetadata{Goal: goal.URL, ScopeSHA: task.BodySHA}), "user": restActor(c.Reviewer), "created_at": now.Add(-time.Minute), "updated_at": now.Add(-time.Minute)}}), nil
					case endpoint == "repos/code/repo/issues/4" || strings.HasPrefix(endpoint, "repos/code/repo/issues?"):
						issue := map[string]any{"html_url": task.URL, "number": 4, "title": task.Title, "body": task.Body, "state": "open"}
						if strings.Contains(endpoint, "?") {
							return jsonResponse([]any{issue}), nil
						}
						return jsonResponse(issue), nil
					case strings.HasPrefix(endpoint, "repos/"+repo+"/pulls?"):
						return jsonResponse([]any{map[string]any{"html_url": ref}}), nil
					case strings.Contains(endpoint, "/reviews?"):
						return jsonResponse([]any{map[string]any{"id": 20, "commit_id": head, "state": "APPROVED", "user": restActor(c.Reviewer), "submitted_at": now}}), nil
					case strings.Contains(endpoint, "/issues?") || strings.Contains(endpoint, "/pulls?"):
						return jsonResponse([]any{}), nil
					case endpoint == "graphql":
						var req struct{ Query string }
						_ = json.Unmarshal(body, &req)
						var data any
						switch {
						case strings.Contains(req.Query, "WorkerIdentity"):
							data = map[string]any{"viewer": graphActor{Kind: c.Self.Kind, Login: c.Self.Login, ID: c.Self.ID}}
						case strings.Contains(req.Query, "WorkerDiscussions"), strings.Contains(req.Query, "WorkerGoalRefresh"), strings.Contains(req.Query, "WorkerGoalVersion"):
							d := graphDiscussion{ID: "goal", URL: goal.URL, Body: goal.Body, CreatedAt: goal.CreatedAt, LastEditedAt: goal.LastEditedAt}
							if !strings.Contains(req.Query, "WorkerDiscussions") {
								data = map[string]any{"node": d}
							} else {
								data = map[string]any{"repository": map[string]any{"discussions": page([]any{d})}}
							}
						case strings.Contains(req.Query, "WorkerDiscussionComments"):
							data = map[string]any{"node": map[string]any{"comments": page([]any{graphComment{ID: "approval", Body: "/coop approve", CreatedAt: now.Add(-time.Minute), Author: graphActor{Kind: c.Owner.Kind, Login: c.Owner.Login, ID: c.Owner.ID}}})}}
						case strings.Contains(req.Query, "WorkerDiscussionReplies"):
							data = map[string]any{"node": map[string]any{"replies": page([]any{})}}
						case strings.Contains(req.Query, "WorkerPR"):
							data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"url": ref, "createdAt": now.Add(-time.Hour), "body": encodeMetadata("pr", prMetadata{Task: task.URL, Goal: goal.URL}), "state": "OPEN", "merged": merged, "headRefOid": head, "author": graphActor{Kind: c.Implementer.Kind, Login: c.Implementer.Login, ID: c.Implementer.ID}, "mergeStateStatus": "CLEAN", "commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": "SUCCESS"}}}}}}}}
						default:
							t.Fatalf("unexpected query: %s", req.Query)
						}
						return jsonResponse(map[string]any{"data": data}), nil
					}
					t.Fatalf("unexpected endpoint: %s", endpoint)
					return CommandResult{}, nil
				}}
				g := NewGitHubClient(f)
				s, err := g.Snapshot(context.Background(), c, Snapshot{})
				if err != nil {
					t.Fatal(err)
				}
				want := repo == task.Repo
				if len(s.PRs) != 1 || (s.PRs[0].TaskURL == task.URL) != want {
					t.Fatalf("discovered PR linkage: %+v", s.PRs)
				}
				j := Job{Kind: "deliver", GoalURL: goal.URL, TaskURL: task.URL, PRURL: ref, HeadSHA: head}
				if ok, reason := Admit(c, j, s, now); ok != want {
					t.Fatalf("discovery admitted=%v, want %v: %s", ok, want, reason)
				}
				// A saved linked PR must not survive a fresh cross-repository read.
				s.PRs[0].TaskURL, s.PRs[0].GoalURL = task.URL, goal.URL
				fresh, err := g.Revalidate(context.Background(), j, s)
				if err != nil {
					t.Fatal(err)
				}
				if ok, reason := Admit(c, j, fresh, now); ok != want {
					t.Fatalf("fresh admitted=%v, want %v: %s", ok, want, reason)
				}
			})
		}
	}
}
