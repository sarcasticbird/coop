package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Keep the API history independent of the client so a restart cannot erase a
// scope transition that happened entirely between polls.
type reviewScopeFixture struct {
	c                                       Config
	s                                       Snapshot
	now                                     time.Time
	version                                 map[string]any
	edits                                   []map[string]any
	historyPageInfo                         map[string]any
	reviews                                 []map[string]any
	historyReads, reviewReads, versionReads int
	change                                  func(string) error
}

func newReviewScopeFixture(t *testing.T) *reviewScopeFixture {
	t.Helper()
	c, s, now := policyFixture(t)
	s.Tasks[0].Ready = false
	s.Goals[0].URL = "https://github.com/planning/board/discussions/1"
	s.Goals[0].Body = encodeMetadata("goal", map[string]any{"repositories": s.Goals[0].Repositories})
	s.Goals[0].BodySHA = BodyHash(s.Goals[0].Body)
	s.Tasks[0].GoalURL = s.Goals[0].URL
	c.DiscussionRepositories = []string{"planning/board"}
	f := &reviewScopeFixture{c: c, s: s, now: now, historyPageInfo: map[string]any{"hasNextPage": false}}
	f.version = map[string]any{
		"url": "https://github.com/code/repo/pull/5", "body": f.body(),
		"createdAt": now.Add(-time.Hour), "lastEditedAt": time.Time{},
		"headRefOid": strings.Repeat("a", 40), "state": "OPEN", "updatedAt": now,
		"author":           graphActor{ID: c.Implementer.ID, Login: c.Implementer.Login, Kind: c.Implementer.Kind},
		"mergeStateStatus": "CLEAN",
		"commits":          map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": "SUCCESS"}}}}},
	}
	return f
}

func (f *reviewScopeFixture) body() string {
	return encodeMetadata("pr", prMetadata{Task: f.s.Tasks[0].URL, Goal: f.s.Goals[0].URL})
}

func (f *reviewScopeFixture) edit(body string, at time.Time) map[string]any {
	return map[string]any{"id": at.Format(time.RFC3339Nano), "editedAt": at, "diff": body, "deletedAt": nil}
}

func (f *reviewScopeFixture) review(state string, at time.Time) {
	f.reviews = []map[string]any{{"id": 20, "commit_id": f.version["headRefOid"], "state": state, "user": restActor(f.c.Reviewer), "submitted_at": at}}
}

func (f *reviewScopeFixture) client(t *testing.T) *GitHubClient {
	t.Helper()
	page := func(nodes any) any {
		return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}
	}
	commands := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if strings.HasPrefix(endpoint, "installation/repositories") {
			return jsonResponse(map[string]any{"repositories": []any{map[string]any{"full_name": "code/repo", "has_issues": true}}}), nil
		}
		if strings.HasPrefix(endpoint, "repos/code/repo/pulls?") {
			return jsonResponse([]any{map[string]any{"html_url": f.version["url"]}}), nil
		}
		if strings.Contains(endpoint, "/reviews?") {
			f.reviewReads++
			if f.change != nil {
				if err := f.change("reviews"); err != nil {
					return CommandResult{}, err
				}
			}
			return jsonResponse(f.reviews), nil
		}
		if strings.HasPrefix(endpoint, "repos/code/repo/issues/4/comments") {
			task := f.s.Tasks[0]
			return jsonResponse([]any{map[string]any{"id": 10, "body": encodeMetadata("task", taskMetadata{Goal: task.GoalURL, ScopeSHA: task.BodySHA}), "user": restActor(f.c.Reviewer), "created_at": task.MetadataUpdatedAt, "updated_at": task.MetadataUpdatedAt}}), nil
		}
		if endpoint == "repos/code/repo/issues/4" || strings.HasPrefix(endpoint, "repos/code/repo/issues?") {
			task := f.s.Tasks[0]
			issue := map[string]any{"html_url": task.URL, "number": 4, "title": task.Title, "body": task.Body, "state": "open"}
			if strings.Contains(endpoint, "?") {
				return jsonResponse([]any{issue}), nil
			}
			return jsonResponse(issue), nil
		}
		var req struct{ Query string }
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		var data any
		switch {
		case strings.Contains(req.Query, "WorkerIdentity"):
			data = map[string]any{"viewer": graphActor{ID: f.c.Self.ID, Login: f.c.Self.Login, Kind: f.c.Self.Kind}}
		case strings.Contains(req.Query, "WorkerScopeHistory"):
			f.historyReads++
			if f.change != nil {
				if err := f.change("history"); err != nil {
					return CommandResult{}, err
				}
			}
			data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"userContentEdits": map[string]any{"nodes": f.edits, "pageInfo": f.historyPageInfo}}}}
		case strings.Contains(req.Query, "WorkerPRVersion"):
			f.versionReads++
			if f.change != nil {
				if err := f.change("version"); err != nil {
					return CommandResult{}, err
				}
			}
			data = map[string]any{"repository": map[string]any{"pullRequest": f.version}}
		case strings.Contains(req.Query, "WorkerPR("):
			data = map[string]any{"repository": map[string]any{"pullRequest": f.version}}
		case strings.Contains(req.Query, "WorkerDiscussions"), strings.Contains(req.Query, "WorkerGoalRefresh"), strings.Contains(req.Query, "WorkerGoalVersion"):
			goal := f.s.Goals[0]
			d := graphDiscussion{ID: "goal", URL: goal.URL, Body: goal.Body, CreatedAt: goal.CreatedAt, LastEditedAt: goal.LastEditedAt}
			if strings.Contains(req.Query, "WorkerDiscussions") {
				data = map[string]any{"repository": map[string]any{"discussions": page([]any{d})}}
			} else {
				data = map[string]any{"node": d}
			}
		case strings.Contains(req.Query, "WorkerDiscussionComments"):
			data = map[string]any{"node": map[string]any{"comments": page([]any{graphComment{ID: "approval", Body: "/coop approve", CreatedAt: f.now.Add(-time.Minute), Author: graphActor{ID: f.c.Owner.ID, Login: f.c.Owner.Login, Kind: f.c.Owner.Kind}}})}}
		case strings.Contains(req.Query, "WorkerDiscussionReplies"):
			data = map[string]any{"node": map[string]any{"replies": page([]any{})}}
		default:
			t.Fatalf("unexpected endpoint/query: %s %s", endpoint, req.Query)
		}
		return jsonResponse(map[string]any{"data": data}), nil
	}}
	g := NewGitHubClient(commands)
	g.config = f.c
	g.goalIDs[f.s.Goals[0].URL] = "goal"
	return g
}

func TestReviewScopeCatalogSelection(t *testing.T) {
	for _, scenario := range []string{"restored", "same_scope_unrelated_edits", "oldest_known_prose_edit"} {
		for _, state := range []string{"APPROVED", "CHANGES_REQUESTED"} {
			t.Run(scenario+"/"+state, func(t *testing.T) {
				f := newReviewScopeFixture(t)
				f.c = Defaults(f.c)
				f.c.Repositories, f.c.Projects, f.c.ProjectOwners = nil, nil, nil
				oldest, latest := f.now.Add(-40*time.Minute), f.now.Add(-20*time.Minute)
				f.s.Tasks[0].MetadataUpdatedAt = f.now.Add(-35 * time.Minute)
				body := "Updated explanation\n\n" + f.body()
				f.version["body"], f.version["lastEditedAt"] = body, latest
				f.edits = []map[string]any{f.edit(body, latest), f.edit(f.body(), oldest)}
				wantBoundary := f.s.Tasks[0].MetadataUpdatedAt
				switch scenario {
				case "restored":
					other := encodeMetadata("pr", prMetadata{Task: "https://github.com/code/repo/issues/3", Goal: f.s.Goals[0].URL})
					f.edits = []map[string]any{f.edit(body, latest), f.edit(other, f.now.Add(-25*time.Minute)), f.edit(f.body(), oldest)}
					wantBoundary = latest
				case "oldest_known_prose_edit":
					// The pre-edit body is unavailable, so a single same-scope
					// prose version cannot establish continuity back to creation.
					f.edits = []map[string]any{f.edit(body, latest)}
					wantBoundary = latest
				}
				f.review(state, f.now.Add(-30*time.Minute))
				g := f.client(t)
				s, err := g.Snapshot(context.Background(), f.c, Snapshot{})
				if err != nil {
					t.Fatal(err)
				}
				if !s.Complete || len(s.PRs) != 1 || len(s.Tasks) != 1 || len(s.Goals) != 1 {
					t.Fatalf("incomplete catalog: %+v", s)
				}
				pr := s.PRs[0]
				if !pr.ScopeUpdatedAt.Equal(wantBoundary) || pr.ScopeRevision == "" || !s.Tasks[0].MetadataUpdatedAt.Equal(f.s.Tasks[0].MetadataUpdatedAt) {
					t.Fatalf("catalog did not bind history/task scope: PR=%+v task=%+v, want boundary %s", pr, s.Tasks[0], wantBoundary)
				}
				if f.historyReads != 2 || f.reviewReads != 1 || f.versionReads != 1 {
					t.Fatalf("catalog history/reviews/version reads=%d/%d/%d", f.historyReads, f.reviewReads, f.versionReads)
				}
				jobs := Select(f.c, s, State{}, f.now)
				if scenario == "same_scope_unrelated_edits" {
					kind := "deliver"
					if state == "CHANGES_REQUESTED" {
						kind = "fix"
					}
					if len(jobs) != 1 || jobs[0].Kind != kind {
						t.Fatalf("catalog invalidated same-scope review: %+v", jobs)
					}
				} else if len(jobs) != 0 {
					t.Fatalf("catalog accepted review before known scope boundary: %+v", jobs)
				}
				planner := f.c
				planner.Role, planner.Self = "planner", planner.Reviewer
				var reviews []Job
				for _, job := range Select(planner, s, State{}, f.now) {
					if job.Kind == "review" {
						reviews = append(reviews, job)
					}
				}
				if scenario == "same_scope_unrelated_edits" {
					if len(reviews) != 0 {
						t.Fatalf("catalog unnecessarily requested re-review: %+v", reviews)
					}
				} else if len(reviews) != 1 || reviews[0].InputRevision != pr.HeadSHA+":"+pr.ScopeRevision {
					t.Fatalf("catalog did not select bound-scope review: %+v", reviews)
				}
			})
		}
	}
}

func TestReviewScopeHistorySelection(t *testing.T) {
	for _, scenario := range []string{"retarget_task", "retarget_goal", "restored", "missing_repaired", "malformed_repaired", "formatting", "prose", "unrelated_updated_at"} {
		for _, state := range []string{"APPROVED", "CHANGES_REQUESTED"} {
			t.Run(scenario+"/"+state, func(t *testing.T) {
				f := newReviewScopeFixture(t)
				body := f.body()
				oldAt, boundary, latest := f.now.Add(-40*time.Minute), f.now.Add(-20*time.Minute), f.now.Add(-10*time.Minute)
				previous := body
				wantBoundary := boundary
				transition := true
				switch scenario {
				case "retarget_task":
					previous = encodeMetadata("pr", prMetadata{Task: "https://github.com/code/repo/issues/3", Goal: f.s.Goals[0].URL})
				case "retarget_goal":
					previous = encodeMetadata("pr", prMetadata{Task: f.s.Tasks[0].URL, Goal: "https://github.com/planning/board/discussions/2"})
				case "restored":
					previous = encodeMetadata("pr", prMetadata{Task: "https://github.com/code/repo/issues/3", Goal: f.s.Goals[0].URL})
				case "missing_repaired":
					previous = "No tracking metadata yet"
				case "malformed_repaired":
					previous = "<!-- coop-pr:v1 {broken} -->"
				case "formatting":
					body = fmt.Sprintf("<!-- coop-pr:v1 {\n  \"goal\": %q, \"task\": %q\n} -->", f.s.Goals[0].URL, f.s.Tasks[0].URL)
					transition, wantBoundary = false, oldAt
				case "prose":
					body = "New explanation\n\n" + body
					transition, wantBoundary = false, oldAt
				case "unrelated_updated_at":
					transition, wantBoundary = false, oldAt
				}
				f.version["body"], f.version["lastEditedAt"] = body, latest
				f.edits = []map[string]any{f.edit(body, latest), f.edit(f.body(), boundary), f.edit(previous, oldAt)}
				if scenario == "restored" {
					f.edits = append(f.edits, f.edit(f.body(), f.now.Add(-50*time.Minute)))
				}
				f.review(state, f.now.Add(-30*time.Minute))
				g := f.client(t)
				pr, err := g.readPR(context.Background(), f.version["url"].(string))
				if err != nil {
					t.Fatal(err)
				}
				if !pr.ScopeUpdatedAt.Equal(wantBoundary) || pr.ScopeRevision == "" {
					t.Fatalf("scope boundary=%s revision=%q, want %s", pr.ScopeUpdatedAt, pr.ScopeRevision, wantBoundary)
				}
				if f.historyReads != 2 || f.reviewReads != 1 || f.versionReads != 1 {
					t.Fatalf("history/reviews/version reads=%d/%d/%d", f.historyReads, f.reviewReads, f.versionReads)
				}
				bindReviewScope(&pr, f.s.Tasks[0])
				f.s.PRs = []PR{pr}
				kind := "deliver"
				if state == "CHANGES_REQUESTED" {
					kind = "fix"
				}
				jobs := Select(f.c, f.s, State{}, f.now)
				if transition {
					if len(jobs) != 0 {
						t.Fatalf("old review authorized work after scope transition: %+v", jobs)
					}
				} else if len(jobs) != 1 || jobs[0].Kind != kind {
					t.Fatalf("same-scope edit invalidated review: %+v", jobs)
				}
				planner := f.c
				planner.Role, planner.Self = "planner", planner.Reviewer
				jobs = Select(planner, f.s, State{}, f.now)
				if (len(jobs) == 1 && jobs[0].Kind == "review") != transition {
					t.Fatalf("re-review selection=%+v, transition=%v", jobs, transition)
				}
				if !transition {
					// Comments and checks update updatedAt, not the review scope.
					f.version["updatedAt"] = f.now.Add(time.Second)
					fresh, err := g.readPR(context.Background(), pr.URL)
					if err != nil {
						t.Fatal(err)
					}
					bindReviewScope(&fresh, f.s.Tasks[0])
					if fresh.ScopeRevision != pr.ScopeRevision || !fresh.ScopeUpdatedAt.Equal(pr.ScopeUpdatedAt) {
						t.Fatal("unrelated updatedAt change invalidated the review scope")
					}
					return
				}
				// Equality is not evidence that the review followed the transition.
				for _, at := range []time.Time{boundary, boundary.Add(time.Nanosecond)} {
					f.review(state, at)
					fresh, err := g.readPR(context.Background(), pr.URL)
					if err != nil {
						t.Fatal(err)
					}
					bindReviewScope(&fresh, f.s.Tasks[0])
					f.s.PRs = []PR{fresh}
					jobs := Select(f.c, f.s, State{}, f.now)
					if at.Equal(boundary) {
						if len(jobs) != 0 {
							t.Fatalf("equal-timestamp review accepted: %+v", jobs)
						}
					} else if len(jobs) != 1 || jobs[0].Kind != kind {
						t.Fatalf("post-transition review not accepted: %+v", jobs)
					}
				}
			})
		}
	}
}

func TestReviewScopeMergedFinalizationNeedsNoNewReview(t *testing.T) {
	f := newReviewScopeFixture(t)
	f.version["merged"], f.version["state"] = true, "MERGED"
	f.version["lastEditedAt"] = f.now
	f.change = func(kind string) error {
		if kind == "history" {
			return errors.New("history unavailable after merge")
		}
		return nil
	}
	pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	bindReviewScope(&pr, f.s.Tasks[0])
	f.s.PRs = []PR{pr}
	jobs := Select(f.c, f.s, State{}, f.now)
	if len(jobs) != 1 || jobs[0].Kind != "deliver" || f.historyReads != 0 {
		t.Fatalf("merged issue finalization required another review: %+v", jobs)
	}
}

func TestReviewScopeRestartAndSavedJobRevalidation(t *testing.T) {
	f := newReviewScopeFixture(t)
	ref := f.version["url"].(string)
	g := f.client(t)
	pr, err := g.readPR(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if !pr.ScopeUpdatedAt.Equal(f.version["createdAt"].(time.Time)) {
		t.Fatal("unedited PR must use creation boundary")
	}
	bindReviewScope(&pr, f.s.Tasks[0])
	f.s.PRs = []PR{pr}
	f.c.Role, f.c.Self = "planner", f.c.Reviewer
	g.config = f.c
	jobs := Select(f.c, f.s, State{}, f.now)
	if len(jobs) != 1 || jobs[0].Kind != "review" {
		t.Fatalf("initial review not selected: %+v", jobs)
	}
	saved := jobs[0]
	body := f.body()
	boundary := f.now.Add(-20 * time.Minute)
	f.version["lastEditedAt"] = boundary
	f.edits = []map[string]any{
		f.edit(body, boundary),
		f.edit(encodeMetadata("pr", prMetadata{Task: "https://github.com/code/repo/issues/3", Goal: f.s.Goals[0].URL}), f.now.Add(-30*time.Minute)),
		f.edit(body, f.now.Add(-40*time.Minute)),
	}
	// The saved job still points to exactly the same PR, task, goal and head.
	fresh, err := g.Revalidate(context.Background(), saved, f.s)
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := Admit(f.c, saved, fresh, f.now); ok || reason != "saved PR review scope changed" {
		t.Fatalf("stale job admitted=%v: %s", ok, reason)
	}
	jobs = Select(f.c, fresh, State{}, f.now)
	if len(jobs) == 0 || jobs[0].Kind != "review" || jobs[0].ID == saved.ID {
		t.Fatalf("transition did not create a new review job: %+v", jobs)
	}
	if !fresh.PRs[0].ScopeUpdatedAt.Equal(boundary) {
		t.Fatalf("restored boundary=%s", fresh.PRs[0].ScopeUpdatedAt)
	}
	restarted := f.client(t)
	afterRestart, err := restarted.readPR(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	bindReviewScope(&afterRestart, f.s.Tasks[0])
	if afterRestart.ScopeRevision != fresh.PRs[0].ScopeRevision || !afterRestart.ScopeUpdatedAt.Equal(boundary) {
		t.Fatal("restart lost durable scope boundary/revision")
	}
	// Task metadata can move the boundary later, but never earlier than the PR.
	for _, at := range []time.Time{boundary.Add(-time.Minute), boundary.Add(time.Minute)} {
		task := f.s.Tasks[0]
		task.MetadataUpdatedAt = at
		bound, err := restarted.readPR(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		bindReviewScope(&bound, task)
		want := boundary
		if at.After(want) {
			want = at
		}
		if !bound.ScopeUpdatedAt.Equal(want) {
			t.Fatalf("combined boundary=%s, want %s", bound.ScopeUpdatedAt, want)
		}
	}
	f.s.Tasks[0].MetadataUpdatedAt = boundary.Add(time.Minute)
	fresh, err = restarted.Revalidate(context.Background(), jobs[0], fresh)
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := Admit(f.c, jobs[0], fresh, f.now); ok || reason != "saved PR review scope changed" {
		t.Fatalf("metadata-stale review job admitted=%v: %s", ok, reason)
	}
	if !fresh.PRs[0].ScopeUpdatedAt.Equal(f.s.Tasks[0].MetadataUpdatedAt) {
		t.Fatal("revalidation did not bind fresh task metadata")
	}
	for _, at := range []time.Time{boundary.Add(30 * time.Second), f.s.Tasks[0].MetadataUpdatedAt, f.s.Tasks[0].MetadataUpdatedAt.Add(time.Nanosecond)} {
		f.review("APPROVED", at)
		refreshed, err := restarted.Revalidate(context.Background(), jobs[0], fresh)
		if err != nil {
			t.Fatal(err)
		}
		implementer := f.c
		implementer.Role, implementer.Self = "implementer", implementer.Implementer
		selected := Select(implementer, refreshed, State{}, f.now)
		wantDelivery := at.After(f.s.Tasks[0].MetadataUpdatedAt)
		if (len(selected) == 1 && selected[0].Kind == "deliver") != wantDelivery {
			t.Fatalf("task metadata boundary did not govern review at %s: %+v", at, selected)
		}
	}
}

func TestReviewScopeHistoryChangesDuringReviews(t *testing.T) {
	for _, scenario := range []string{"same_second_scope_ABA", "edit_identity_changed", "post_review_history_unavailable", "unchanged_control"} {
		t.Run(scenario, func(t *testing.T) {
			f := newReviewScopeFixture(t)
			at := f.now.Add(-20 * time.Minute).Truncate(time.Second)
			f.version["lastEditedAt"] = at
			f.edits = []map[string]any{f.edit(f.body(), at)}
			f.review("APPROVED", f.now.Add(-time.Minute))
			before, _ := json.Marshal(f.version)
			f.change = func(stage string) error {
				if stage == "history" && f.historyReads == 2 {
					if f.reviewReads != 1 {
						t.Fatal("second history scan must follow the review read")
					}
					if scenario == "post_review_history_unavailable" {
						return errors.New("post-review history unavailable")
					}
				}
				if stage != "reviews" {
					return nil
				}
				switch scenario {
				case "same_second_scope_ABA":
					b := encodeMetadata("pr", prMetadata{Task: "https://github.com/code/repo/issues/3", Goal: f.s.Goals[0].URL})
					restored, retargeted := f.edit(f.body(), at), f.edit(b, at)
					restored["id"], retargeted["id"] = "restored-A", "retargeted-B"
					f.edits = append([]map[string]any{restored, retargeted}, f.edits...)
				case "edit_identity_changed":
					f.edits[0]["id"] = "replacement-edit"
				}
				return nil
			}
			pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
			after, _ := json.Marshal(f.version)
			if string(before) != string(after) || f.reviewReads != 1 {
				t.Fatal("test must mutate history during reviews without changing the PR body version")
			}
			if scenario == "unchanged_control" {
				if err != nil || latestReview(f.c, pr).State != "APPROVED" {
					t.Fatalf("unchanged history rejected: %v", err)
				}
			} else if err == nil || pr.TaskURL != "" || len(pr.Reviews) != 0 {
				t.Fatalf("changed history returned actionable PR: err=%v pr=%+v", err, pr)
			}
		})
	}
}

func TestReviewScopeHistoryFailsClosed(t *testing.T) {
	for _, scenario := range []string{"unavailable", "redacted", "deleted", "empty", "incomplete", "stalled_pagination", "mismatch_current_body", "mismatch_current_timestamp", "changed_between_scans", "version_changed_after_reviews"} {
		t.Run(scenario, func(t *testing.T) {
			f := newReviewScopeFixture(t)
			at := f.now.Add(-20 * time.Minute)
			f.version["lastEditedAt"] = at
			f.edits = []map[string]any{f.edit(f.body(), at), f.edit(f.body(), f.now.Add(-40*time.Minute))}
			f.review("APPROVED", f.now.Add(-time.Minute))
			switch scenario {
			case "redacted":
				f.edits[0]["diff"] = nil
			case "deleted":
				f.edits[1]["deletedAt"] = f.now
			case "empty":
				f.edits = nil
			case "incomplete":
				f.historyPageInfo = nil
			case "stalled_pagination":
				f.historyPageInfo = map[string]any{"hasNextPage": true}
			case "mismatch_current_body":
				f.edits[0]["diff"] = "different body"
			case "mismatch_current_timestamp":
				f.edits[0]["editedAt"] = at.Add(-time.Second)
			}
			f.change = func(stage string) error {
				if stage == "history" && scenario == "unavailable" {
					return errors.New("history unavailable")
				}
				if stage == "history" && scenario == "changed_between_scans" && f.historyReads == 2 {
					f.edits[1]["diff"] = "edited during scan"
				}
				if stage == "reviews" && scenario == "version_changed_after_reviews" {
					f.version["lastEditedAt"] = at.Add(time.Second)
				}
				return nil
			}
			pr, err := f.client(t).readPR(context.Background(), f.version["url"].(string))
			if err == nil {
				t.Fatalf("unverifiable history accepted: %+v", pr)
			}
			if pr.TaskURL != "" || len(pr.Reviews) != 0 {
				t.Fatalf("failed read leaked actionable PR: %+v", pr)
			}
			if scenario == "version_changed_after_reviews" && (f.reviewReads != 1 || f.versionReads != 1) {
				t.Fatal("test did not reach post-review version verification")
			}
		})
	}
}
