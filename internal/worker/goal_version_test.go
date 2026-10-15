package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGoalVersionCatalogAndRevalidation(t *testing.T) {
	for _, kind := range []string{"discussion", "issue"} {
		for _, path := range []string{"catalog", "revalidation"} {
			for _, approval := range []string{"/coop approve", "approved", "I approve this goal", "this looks good to me"} {
				for _, scenario := range []string{
					"stable", "narrowed", "edited_without_approval", "paused", "rejected",
					"narrow_between_body_and_comments", "edit_after_first_comments", "edit_after_second_comments",
					"restored_body_new_timestamp", "approval_arrives", "pause_arrives", "reject_arrives",
					"decision_edited", "decision_deleted", "verification_unavailable",
				} {
					t.Run(kind+"/"+path+"/"+approval+"/"+scenario, func(t *testing.T) {
						c, old, now := policyFixture(t)
						goal := old.Goals[0]
						goal.URL = "https://github.com/planning/board/discussions/1"
						if kind == "issue" {
							goal.URL = "https://github.com/code/repo/issues/1"
						}
						broad := encodeMetadata("goal", map[string]any{"repositories": []string{"code/repo", "code/extra"}})
						narrow := encodeMetadata("goal", map[string]any{"repositories": []string{"code/repo"}})
						d := graphDiscussion{ID: "goal", URL: goal.URL, Body: broad, CreatedAt: now.Add(-time.Hour), LastEditedAt: now.Add(-time.Hour)}
						owner := graphActor{ID: c.Owner.ID, Login: c.Owner.Login, Kind: c.Owner.Kind}
						comments := []graphComment{{ID: "approval", Body: approval, Author: owner, CreatedAt: now.Add(-time.Minute)}}
						switch scenario {
						case "narrowed":
							d.Body, d.LastEditedAt = narrow, now.Add(-2*time.Minute)
						case "edited_without_approval":
							d.LastEditedAt = now.Add(-30 * time.Second)
						case "paused", "rejected":
							command := "/coop pause"
							if scenario == "rejected" {
								command = "/coop reject"
							}
							comments = append(comments, graphComment{ID: "decision", Body: command, Author: owner, CreatedAt: now.Add(-time.Second)})
						case "approval_arrives":
							comments = nil
						}
						commentReads, versionReads := 0, 0
						page := func(nodes any) any {
							return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}
						}
						readComments := func() {
							commentReads++
							if commentReads == 1 && scenario == "narrow_between_body_and_comments" {
								d.Body, d.LastEditedAt = narrow, now.Add(-2*time.Minute)
							}
							if commentReads != 2 {
								return
							}
							command := "/coop pause"
							switch scenario {
							case "approval_arrives":
								command = approval
							case "reject_arrives":
								command = "/coop reject"
							case "decision_edited":
								comments[0].Body, comments[0].LastEditedAt = "/coop reject", now.Add(-time.Second)
								return
							case "decision_deleted":
								comments = nil
								return
							case "pause_arrives":
							default:
								return
							}
							comments = append(comments, graphComment{ID: "new", Body: command, Author: owner, CreatedAt: now.Add(-time.Second)})
						}
						f := &fakeCommands{respond: func(o, endpoint, method string, body []byte) (CommandResult, error) {
							if strings.HasPrefix(endpoint, "repos/code/repo/issues/1/comments") {
								readComments()
								var rest []any
								for i, comment := range comments {
									rest = append(rest, map[string]any{"id": i + 1, "body": comment.Body, "user": restActor(c.Owner), "created_at": comment.CreatedAt, "updated_at": comment.LastEditedAt})
								}
								return jsonResponse(rest), nil
							}
							var req struct{ Query string }
							if err := json.Unmarshal(body, &req); err != nil {
								t.Fatal(err)
							}
							var data any
							switch {
							case strings.Contains(req.Query, "WorkerDiscussions"):
								data = map[string]any{"repository": map[string]any{"discussions": page([]graphDiscussion{d})}}
							case strings.Contains(req.Query, "WorkerIssueGoal"):
								data = map[string]any{"repository": map[string]any{"issue": d}}
							case strings.Contains(req.Query, "WorkerGoalRefresh"):
								data = map[string]any{"node": d}
							case strings.Contains(req.Query, "WorkerGoalVersion"):
								versionReads++
								if scenario == "verification_unavailable" {
									return CommandResult{}, errors.New("unavailable")
								}
								if (scenario == "edit_after_first_comments" && versionReads == 1) || (scenario == "edit_after_second_comments" && versionReads == 2) {
									d.Body += "\nChanged scope"
								}
								if scenario == "restored_body_new_timestamp" {
									d.LastEditedAt = now.Add(-2 * time.Minute)
								}
								data = map[string]any{"node": d}
							case strings.Contains(req.Query, "WorkerDiscussionComments"):
								readComments()
								data = map[string]any{"node": map[string]any{"comments": page(comments)}}
							case strings.Contains(req.Query, "WorkerDiscussionReplies"):
								data = map[string]any{"node": map[string]any{"replies": page([]any{})}}
							default:
								t.Fatalf("unexpected endpoint/query: %s %s", endpoint, req.Query)
							}
							return jsonResponse(map[string]any{"data": data}), nil
						}}
						g := NewGitHubClient(f)
						g.config = c
						g.goalIDs[goal.URL] = d.ID
						var fresh Goal
						var err error
						if path == "revalidation" {
							old.Goals = []Goal{{URL: goal.URL}}
							var s Snapshot
							// Planning revalidation uses the same goal read as task dispatch,
							// without unrelated task/PR API fixtures hiding version failures.
							s, err = g.Revalidate(context.Background(), Job{Kind: "plan", GoalURL: goal.URL}, old)
							if err == nil {
								fresh = s.Goals[0]
							}
						} else if kind == "issue" {
							fresh, _, err = g.issueGoal(context.Background(), Task{URL: goal.URL, Repo: "code/repo"})
						} else {
							var goals []Goal
							goals, _, _, err = g.discussions(context.Background(), "planning/board")
							if err == nil {
								fresh = goals[0]
							}
						}
						coherent := scenario == "stable" || scenario == "narrowed" || scenario == "edited_without_approval" || scenario == "paused" || scenario == "rejected"
						if !coherent {
							if err == nil || fresh.Readable {
								t.Fatal("mixed or unverifiable goal version accepted")
							}
							if scenario != "verification_unavailable" && errorCategory(err) != "goal_version_changed" {
								t.Fatal(err)
							}
							return
						}
						if err != nil || commentReads != 2 || versionReads != 2 {
							t.Fatalf("coherent goal failed: comments=%d versions=%d err=%v", commentReads, versionReads, err)
						}
						state := EvaluateGoal(c, fresh, now).State
						want := "approved"
						switch scenario {
						case "edited_without_approval":
							want = "unapproved"
						case "paused":
							want = "paused"
						case "rejected":
							want = "rejected"
						}
						if state != want {
							t.Fatalf("approval state=%s, want %s", state, want)
						}
						old.Goals = []Goal{fresh}
						old.Tasks[0].GoalURL = goal.URL
						old.Tasks[0].Repo, old.Tasks[0].URL = "code/extra", "https://github.com/code/extra/issues/4"
						if jobs := Select(c, old, State{}, now); (len(jobs) == 1) != (scenario == "stable") {
							t.Fatalf("excluded repository/decision admitted incorrectly: %+v", jobs)
						}
						old.Tasks[0].Repo, old.Tasks[0].URL = "code/repo", "https://github.com/code/repo/issues/4"
						if jobs := Select(c, old, State{}, now); (len(jobs) == 1) != (want == "approved") {
							t.Fatalf("in-scope repository control failed: %+v", jobs)
						}
					})
				}
			}
		}
	}
}
