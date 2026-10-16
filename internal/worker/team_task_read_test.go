package worker

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTeamTaskReadIssueVersion(t *testing.T) {
	for _, source := range []string{"catalog", "url"} {
		for _, phase := range []string{"first_comments", "final_comments"} {
			for _, field := range []string{"body", "title", "url", "number", "state", "state_reason", "updated_at", "pull_request"} {
				t.Run(source+"/"+phase+"/"+field, func(t *testing.T) {
					c, task, _, metadata, now := teamScopeFixture(t)
					issue := restIssue{URL: task.URL, Number: 1, Title: task.Title, Body: task.Body, State: "open", UpdatedAt: now}
					initial := issue
					issueReads, commentReads := 0, 0
					f := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
						if owner != "code" || method != "GET" {
							t.Fatalf("unexpected route: %s %s %s", owner, method, endpoint)
						}
						if endpoint == "repos/code/repo/issues/1" {
							issueReads++
							return jsonResponse(issue), nil
						}
						if !strings.HasPrefix(endpoint, "repos/code/repo/issues/1/comments?") {
							t.Fatalf("unexpected endpoint: %s", endpoint)
						}
						commentReads++
						if (phase == "first_comments" && commentReads == 1) || (phase == "final_comments" && commentReads == 2) {
							// Keep updated_at unchanged for content edits, including same-second edits.
							switch field {
							case "body":
								issue.Body += " changed"
							case "title":
								issue.Title += " changed"
							case "url":
								issue.URL += "0"
							case "number":
								issue.Number++
							case "state":
								issue.State = "closed"
							case "state_reason":
								issue.StateReason = "completed"
							case "updated_at":
								issue.UpdatedAt = now.Add(time.Second)
							case "pull_request":
								issue.PullRequest = json.RawMessage(`{"url":"pull"}`)
							}
						}
						return jsonResponse([]restComment{{ID: 2, Body: metadata.Body, Author: restActor(metadata.Author), CreatedAt: now}}), nil
					}}
					g := NewGitHubClient(f)
					g.config = c
					var got Task
					var err error
					if source == "catalog" {
						got, err = g.readTask(context.Background(), task.Repo, initial)
					} else {
						got, err = g.taskByURL(context.Background(), task.URL)
					}
					if err == nil || err.Error() != "GitHub: task_version_changed" || !reflect.DeepEqual(got, Task{}) {
						t.Fatalf("mixed issue snapshot returned: task=%+v err=%v", got, err)
					}
					if issueReads > 3 || commentReads > 2 {
						t.Fatalf("unstable read retried: issues=%d comments=%d", issueReads, commentReads)
					}
				})
			}
		}
	}
}

func TestTeamTaskReadRevalidateCommentTransitions(t *testing.T) {
	for _, change := range []string{"body", "title", "assignment_edit", "assignment_add", "assignment_delete", "claim_add", "claim_edit", "claim_delete", "claim_conflict", "claim_malformed", "unchanged"} {
		t.Run(change, func(t *testing.T) {
			f := newTeamClaimFixture(t, false, "implement")
			path := "repos/code/repo/issues/1/comments"
			claim := f.comment(f.g.config.Self, BodyHash("live instance"), *f.j.Binding)
			if change == "claim_edit" || change == "claim_delete" || change == "claim_conflict" || change == "claim_malformed" || change == "unchanged" {
				f.comments[path] = append(f.comments[path], claim)
			}
			base := f.g.runner.(*fakeCommands).respond
			commentReads := 0
			f.g.runner.(*fakeCommands).respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
				result, err := base(owner, endpoint, method, body)
				if strings.HasPrefix(endpoint, path+"?") {
					commentReads++
					if commentReads == 1 {
						switch change {
						case "body":
							f.issue.Body += " changed"
						case "title":
							f.issue.Title += " changed"
						case "assignment_edit":
							var m TeamTaskMetadata
							if err := teamMetadata(f.comments[path][0].Body, "task", &m); err != nil {
								t.Fatal(err)
							}
							m.Assignment.Author, m.Assignment.Peer = m.Assignment.Peer, m.Assignment.Author
							f.comments[path][0].Body = teamMarker("task", m)
						case "assignment_add":
							added := f.comments[path][0]
							added.ID++
							f.comments[path] = append(f.comments[path], added)
						case "assignment_delete":
							f.comments[path] = nil
						case "claim_add":
							f.comments[path] = append(f.comments[path], claim)
						case "claim_edit":
							f.comments[path][1] = f.comment(f.g.config.Self, BodyHash("other instance"), *f.j.Binding)
						case "claim_delete":
							f.comments[path] = f.comments[path][:1]
						case "claim_conflict":
							other := f.comment(f.g.config.Self, BodyHash("other instance"), *f.j.Binding)
							other.ID++
							f.comments[path] = append(f.comments[path], other)
						case "claim_malformed":
							f.comments[path][1].Body = "<!-- coop-claim:v2 invalid -->"
						}
					}
				}
				return result, err
			}
			before := f.g.snapshot
			got, err := f.g.Revalidate(context.Background(), f.j, before)
			if change == "unchanged" {
				if err != nil || len(got.Tasks) != 1 || got.Tasks[0].Claim == nil || got.Tasks[0].ClaimConflict || got.Tasks[0].Assignment == nil {
					t.Fatalf("unchanged revalidation failed: %+v, %v", got, err)
				}
				if commentReads != 2 || f.taskReads != 3 {
					t.Fatalf("missing version checks: issues=%d comments=%d", f.taskReads, commentReads)
				}
			} else if err == nil || err.Error() != "GitHub: task_version_changed" || !reflect.DeepEqual(got, Snapshot{}) || !reflect.DeepEqual(f.g.snapshot, before) {
				t.Fatalf("comment transition authorized or published: snapshot=%+v err=%v", got, err)
			}
			if commentReads > 2 || f.taskReads > 3 || f.posts != 0 {
				t.Fatalf("read was unbounded or mutated GitHub: issues=%d comments=%d posts=%d", f.taskReads, commentReads, f.posts)
			}
		})
	}
}

func TestTeamTaskReadCompleteCommentSet(t *testing.T) {
	for _, change := range []string{"unchanged", "body", "author", "id", "node_id", "created_at", "updated_at", "addition", "deletion"} {
		t.Run(change, func(t *testing.T) {
			c, task, goal, metadata, now := teamScopeFixture(t)
			issue := restIssue{URL: task.URL, Number: 1, Title: task.Title, Body: task.Body, State: "open", UpdatedAt: now}
			comment := restComment{ID: 2, NodeID: "assignment", Body: metadata.Body, Author: restActor(metadata.Author), CreatedAt: now, UpdatedAt: now}
			commentPages, issueReads := 0, 0
			f := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
				if owner != "code" || method != "GET" {
					t.Fatalf("unexpected route: %s %s %s", owner, method, endpoint)
				}
				if endpoint == "repos/code/repo/issues/1" {
					issueReads++
					return jsonResponse(issue), nil
				}
				if !strings.HasPrefix(endpoint, "repos/code/repo/issues/1/comments?") {
					t.Fatalf("unexpected endpoint: %s", endpoint)
				}
				commentPages++
				if commentPages%2 == 1 {
					// The trusted assignment is on the second page of each scan.
					return CommandResult{Stdout: []byte("HTTP/2.0 200 OK\r\nLink: <https://api.github.com/repos/code/repo/issues/1/comments?per_page=100&page=2>; rel=\"next\"\r\nContent-Type: application/json\r\n\r\n[{\"id\":1,\"body\":\"ordinary comment\"}]")}, nil
				}
				fresh := comment
				if commentPages == 4 {
					switch change {
					case "body":
						fresh.Body += " edited"
					case "author":
						fresh.Author.ID++
					case "id":
						fresh.ID++
					case "node_id":
						fresh.NodeID += " changed"
					case "created_at":
						fresh.CreatedAt = now.Add(time.Second)
					case "updated_at":
						fresh.UpdatedAt = now.Add(time.Second)
					case "addition":
						return jsonResponse([]restComment{comment, {ID: 3, Body: "new comment"}}), nil
					case "deletion":
						return jsonResponse([]restComment{}), nil
					}
				}
				return jsonResponse([]restComment{fresh}), nil
			}}
			g := NewGitHubClient(f)
			g.config = c
			got, err := g.readTask(context.Background(), task.Repo, issue)
			if change == "unchanged" {
				if err != nil || issueReads != 2 || commentPages != 4 {
					t.Fatalf("stable paginated read failed: issues=%d pages=%d err=%v", issueReads, commentPages, err)
				}
				if _, err := ValidateTeamScope(c, got, goal, now); err != nil {
					t.Fatal("stable assignment not admitted:", err)
				}
			} else if err == nil || err.Error() != "GitHub: task_version_changed" || !reflect.DeepEqual(got, Task{}) || commentPages != 4 {
				t.Fatalf("changed second page returned: pages=%d task=%+v err=%v", commentPages, got, err)
			}
		})
	}
}

func TestTeamTaskReadV1Historical(t *testing.T) {
	c, snapshot, now := policyFixture(t)
	task := snapshot.Tasks[0]
	issue := restIssue{URL: task.URL, Number: 4, Title: task.Title, Body: task.Body, State: "open", UpdatedAt: now}
	reads := 0
	f := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		reads++
		if endpoint == "repos/code/repo/issues/4" {
			return jsonResponse(issue), nil
		}
		if !strings.HasPrefix(endpoint, "repos/code/repo/issues/4/comments?") {
			t.Fatalf("unexpected v1 version recheck: %s", endpoint)
		}
		issue.Title += " changed during comments"
		return jsonResponse([]restComment{{ID: 2, Author: restActor(c.Reviewer), CreatedAt: now, Body: encodeMetadata("task", taskMetadata{Goal: task.GoalURL, ScopeSHA: task.BodySHA})}}), nil
	}}
	g := NewGitHubClient(f)
	g.config = c
	got, err := g.taskByURL(context.Background(), task.URL)
	if err != nil || reads != 2 || got.Title != task.Title || got.MetadataAuthor != c.Reviewer || got.MetadataScopeSHA != task.BodySHA {
		t.Fatalf("v1 historical read changed: task=%+v reads=%d err=%v", got, reads, err)
	}
}
