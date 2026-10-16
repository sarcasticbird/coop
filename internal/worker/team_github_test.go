package worker

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTeamGitHubTaskMetadataFreshReads(t *testing.T) {
	for _, name := range []string{"unchanged", "reassigned", "stale_scope", "malformed_trusted", "v1_only", "mixed_versions", "malformed_outsider"} {
		t.Run(name, func(t *testing.T) {
			c, task, goal, comment, now := teamScopeFixture(t)
			var metadata TeamTaskMetadata
			if err := teamMetadata(comment.Body, "task", &metadata); err != nil {
				t.Fatal(err)
			}
			if name == "reassigned" {
				c.Team.Members[0].Capabilities = append(c.Team.Members[0].Capabilities, "implement")
				c.Team.EngineeringPeers["planner"] = "engineer"
				var err error
				metadata.Assignment.PolicySHA, err = TeamPolicySHA(*c.Team)
				if err != nil {
					t.Fatal(err)
				}
				comment.Body = teamMarker("task", metadata)
			}
			freshBody := comment.Body
			legacy := encodeMetadata("task", taskMetadata{Goal: goal.URL, ScopeSHA: task.BodySHA})
			switch name {
			case "reassigned":
				metadata.Assignment.Author, metadata.Assignment.Peer = metadata.Assignment.Peer, metadata.Assignment.Author
				freshBody = teamMarker("task", metadata)
			case "stale_scope":
				metadata.ScopeSHA = BodyHash("stale issue scope")
				freshBody = teamMarker("task", metadata)
			case "malformed_trusted":
				freshBody = "<!-- coop-task:v2 invalid -->"
			case "v1_only":
				freshBody = legacy
			case "mixed_versions":
				freshBody += legacy
			}
			issueReads, commentReads := 0, 0
			useFreshComments := false
			f := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
				if owner != "code" || method != "GET" {
					t.Fatalf("unexpected route: %s %s %s", owner, method, endpoint)
				}
				switch {
				case endpoint == "repos/code/repo/issues/1":
					issueReads++
					return jsonResponse(map[string]any{"html_url": task.URL, "number": 1, "title": task.Title, "body": task.Body, "state": "open", "updated_at": now}), nil
				case strings.HasPrefix(endpoint, "repos/code/repo/issues/1/comments?"):
					commentReads++
					currentBody, edited := comment.Body, now
					if useFreshComments {
						currentBody, edited = freshBody, now.Add(time.Second)
					}
					comments := []restComment{{ID: 2, Body: currentBody, Author: restActor(comment.Author), CreatedAt: now, UpdatedAt: edited}}
					if useFreshComments && (name == "malformed_trusted" || name == "v1_only") {
						// An older valid record must not rescue an invalid trusted edit.
						comments = append([]restComment{{ID: 1, Body: comment.Body, Author: restActor(comment.Author), CreatedAt: now.Add(-time.Second)}}, comments...)
					}
					if useFreshComments && name == "malformed_outsider" {
						comments = append(comments, restComment{ID: 3, Body: "<!-- coop-task:v2 invalid -->", Author: restActor{ID: 999, Login: comment.Author.Login, Kind: "Bot"}, CreatedAt: edited})
					}
					return jsonResponse(comments), nil
				default:
					t.Fatalf("unexpected endpoint: %s", endpoint)
					return CommandResult{}, nil
				}
			}}
			g := NewGitHubClient(f)
			g.config = c
			first, err := g.taskByURL(context.Background(), task.URL)
			if err != nil {
				t.Fatal(err)
			}
			original, err := ValidateTeamScope(c, first, goal, now)
			if err != nil {
				t.Fatal("initial API metadata not admitted:", err)
			}
			useFreshComments = true
			fresh, err := g.taskByURL(context.Background(), task.URL)
			if err != nil {
				t.Fatal("metadata rejection must not be an API failure:", err)
			}
			if issueReads != 6 || commentReads != 4 || fresh.Revision != first.Revision {
				t.Fatalf("expected fresh comment reads without issue revision change: issues=%d comments=%d", issueReads, commentReads)
			}
			got, scopeErr := ValidateTeamScope(c, fresh, goal, now.Add(2*time.Second))
			switch name {
			case "unchanged", "malformed_outsider":
				if scopeErr != nil || got != original {
					t.Fatalf("valid assignment changed: digest=%s err=%v", got, scopeErr)
				}
			case "reassigned":
				if scopeErr != nil || got == original || fresh.Assignment == nil || fresh.Assignment.Author != metadata.Assignment.Author {
					t.Fatalf("edited assignment was not freshly bound: task=%+v err=%v", fresh, scopeErr)
				}
			default:
				if scopeErr == nil || fresh.Assignment != nil || fresh.MetadataAuthor != (Actor{}) || fresh.GoalURL != "" || fresh.MetadataScopeSHA != "" {
					t.Fatalf("invalid trusted metadata retained authority: task=%+v err=%v", fresh, scopeErr)
				}
			}
		})
	}
}

func TestTeamGitHubGoalRepositoriesNoLegacyFallback(t *testing.T) {
	for _, name := range []string{"v2", "legacy", "bootstrap", "malformed_v2", "mixed_versions"} {
		t.Run(name, func(t *testing.T) {
			c, _, goal, _, now := teamScopeFixture(t)
			body := goal.Body
			switch name {
			case "legacy":
				body = encodeMetadata("goal", map[string]any{"repositories": goal.Repositories})
			case "bootstrap":
				body = "Owner-approved legacy bootstrap scope"
			case "malformed_v2":
				body = "<!-- coop-goal:v2 invalid -->"
			case "mixed_versions":
				body += encodeMetadata("goal", map[string]any{"repositories": goal.Repositories})
			}
			c.BootstrapGoals = []BootstrapGoal{{URL: goal.URL, BodySHA: BodyHash(body), Repositories: goal.Repositories, OwnerID: c.Owner.ID, Source: "owner-confirmed"}}
			reads := 0
			f := &fakeCommands{respond: func(owner, endpoint, method string, raw []byte) (CommandResult, error) {
				if owner != "planning" || endpoint != "graphql" || method != "POST" {
					t.Fatalf("unexpected route: %s %s %s", owner, method, endpoint)
				}
				var req struct{ Query string }
				if err := json.Unmarshal(raw, &req); err != nil {
					t.Fatal(err)
				}
				d := graphDiscussion{ID: "goal", URL: goal.URL, Body: body, CreatedAt: now.Add(-time.Hour)}
				page := map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}}
				reads++
				switch {
				case strings.Contains(req.Query, "WorkerDiscussions"):
					page["nodes"] = []graphDiscussion{d}
					return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"discussions": page}}}), nil
				case strings.Contains(req.Query, "WorkerDiscussionComments"):
					return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"comments": page}}}), nil
				case strings.Contains(req.Query, "WorkerGoalVersion"):
					return jsonResponse(map[string]any{"data": map[string]any{"node": d}}), nil
				default:
					t.Fatalf("unexpected query: %s", req.Query)
					return CommandResult{}, nil
				}
			}}
			g := NewGitHubClient(f)
			g.config = c
			goals, _, _, err := g.discussions(context.Background(), "planning/board")
			if err != nil || len(goals) != 1 || reads != 5 {
				t.Fatalf("goal API read failed: goals=%+v reads=%d err=%v", goals, reads, err)
			}
			if name == "v2" {
				if !slices.Equal(goals[0].Repositories, goal.Repositories) {
					t.Fatalf("v2 repositories lost: %+v", goals[0])
				}
			} else if len(goals[0].Repositories) != 0 || len(g.goalRepositories(goal.URL, body)) != 0 {
				t.Fatalf("v2 recovered scope from legacy/bootstrap metadata: %+v", goals[0])
			}
		})
	}
}

func TestTeamGitHubChangedCurrentGoalBody(t *testing.T) {
	for _, duringRead := range []bool{false, true} {
		name := "current_body"
		if duringRead {
			name = "edit_during_read"
		}
		t.Run(name, func(t *testing.T) {
			c, task, goal, comment, now := teamScopeFixture(t)
			if err := ApplyTaskMetadata(c, &task, []Comment{comment}); err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateTeamScope(c, task, goal, now); err != nil {
				t.Fatal(err)
			}
			changedBody := goal.Body + "\nChanged acceptance criteria"
			versions := 0
			f := &fakeCommands{respond: func(owner, endpoint, method string, raw []byte) (CommandResult, error) {
				if owner != "planning" || endpoint != "graphql" || method != "POST" {
					t.Fatalf("unexpected route: %s %s %s", owner, method, endpoint)
				}
				var req struct{ Query string }
				if err := json.Unmarshal(raw, &req); err != nil {
					t.Fatal(err)
				}
				d := graphDiscussion{ID: "goal", URL: goal.URL, Body: changedBody, CreatedAt: goal.CreatedAt, LastEditedAt: now.Add(time.Second)}
				page := func(nodes any) map[string]any {
					return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}
				}
				switch {
				case strings.Contains(req.Query, "WorkerDiscussions"):
					if duringRead {
						d.Body, d.LastEditedAt = goal.Body, time.Time{}
					}
					return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"discussions": page([]graphDiscussion{d})}}}), nil
				case strings.Contains(req.Query, "WorkerDiscussionComments"):
					// Even a new owner approval cannot rebind the old task assignment.
					approval := graphComment{ID: "approval", Body: "/coop approve", CreatedAt: now.Add(2 * time.Second), Author: graphActor{ID: c.Owner.ID, Login: c.Owner.Login, Kind: c.Owner.Kind}}
					return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"comments": page([]graphComment{approval})}}}), nil
				case strings.Contains(req.Query, "WorkerDiscussionReplies"):
					return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"replies": page([]any{})}}}), nil
				case strings.Contains(req.Query, "WorkerGoalVersion"):
					versions++
					return jsonResponse(map[string]any{"data": map[string]any{"node": d}}), nil
				default:
					t.Fatalf("unexpected query: %s", req.Query)
					return CommandResult{}, nil
				}
			}}
			g := NewGitHubClient(f)
			g.config = c
			goals, _, _, err := g.discussions(context.Background(), "planning/board")
			if duringRead {
				if err == nil || err.Error() != "GitHub: goal_version_changed" || len(goals) != 0 || versions != 1 {
					t.Fatalf("mixed goal snapshot accepted: goals=%+v versions=%d err=%v", goals, versions, err)
				}
				return
			}
			if err != nil || len(goals) != 1 || versions != 2 {
				t.Fatalf("current goal read failed: goals=%+v versions=%d err=%v", goals, versions, err)
			}
			fresh := goals[0]
			if fresh.Body != changedBody || fresh.BodySHA != BodyHash(changedBody) || EvaluateGoal(c, fresh, now.Add(3*time.Second)).State != "approved" {
				t.Fatalf("current owner-approved body not read: %+v", fresh)
			}
			if _, err := ValidateTeamScope(c, task, fresh, now.Add(3*time.Second)); err == nil {
				t.Fatal("old assignment admitted against changed current goal body")
			}
		})
	}
}
