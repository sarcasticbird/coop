package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Assessment probes assert observed behavior at 04e6dff, not desired behavior.
func TestAuditCrossRepositoryPRAdmission(t *testing.T) {
	c, s, now := policyFixture(t)
	p := PR{URL: "https://github.com/code/unapproved-repo/pull/5", TaskURL: s.Tasks[0].URL, GoalURL: s.Goals[0].URL, HeadSHA: strings.Repeat("a", 40), Author: c.Implementer, ChecksState: "success", MergeState: "CLEAN"}
	p.Reviews = []Review{{ID: "review", HeadSHA: p.HeadSHA, State: "APPROVED", Author: c.Reviewer, SubmittedAt: now}}
	s.PRs = []PR{p}
	jobs := Select(c, s, State{}, now)
	if len(jobs) != 1 || jobs[0].Kind != "deliver" {
		t.Fatalf("cross-repository delivery was not reproduced: %+v", jobs)
	}
	c.Role, c.Self = "planner", c.Reviewer
	s.PRs[0].Reviews = nil
	jobs = Select(c, s, State{}, now)
	if len(jobs) != 1 || jobs[0].Kind != "review" {
		t.Fatalf("cross-repository review was not reproduced: %+v", jobs)
	}
	t.Log("PR in code/unapproved-repo admitted for review/delivery although goal authorizes only code/repo")
}

func TestAuditMalformedMetadataPoisonsLaterRepair(t *testing.T) {
	c, s, now := policyFixture(t)
	task := s.Tasks[0]
	bad := Comment{ID: "1", Body: "<!-- coop-task:v1 {broken} -->", Author: c.Reviewer, CreatedAt: now.Add(-time.Minute)}
	repair := Comment{ID: "2", Body: encodeMetadata("task", taskMetadata{Goal: task.GoalURL, ScopeSHA: task.BodySHA}), Author: c.Reviewer, CreatedAt: now}
	if err := ApplyTaskMetadata(c, &task, []Comment{repair}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyTaskMetadata(c, &task, []Comment{bad, repair}); err == nil {
		t.Fatal("historical malformed metadata did not poison the repair")
	}
	if task.MetadataAuthor.ID != 0 {
		t.Fatal("expected metadata author to be cleared")
	}
	t.Log("valid replacement metadata works alone but is rejected while an older malformed trusted comment remains")
}

func TestAuditValidApprovalTemporarilyUnapproved(t *testing.T) {
	c, s, now := policyFixture(t)
	c.DiscussionRepositories = []string{"planning/board"}
	old := s.Goals[0]
	old.URL = "https://github.com/planning/board/discussions/1"
	old.Body += "\n" + encodeMetadata("goal", struct {
		Repositories []string `json:"repositories"`
	}{old.Repositories})
	old.BodySHA = BodyHash(old.Body)
	current := old
	current.Body = "New scope\n" + encodeMetadata("goal", struct {
		Repositories []string `json:"repositories"`
	}{old.Repositories})
	current.BodySHA = BodyHash(current.Body)
	current.LastEditedAt = now.Add(-30 * time.Second)
	approval, err := GoalApprovalComment(c, current, current.Repositories)
	if err != nil {
		t.Fatal(err)
	}
	listed := old
	f := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		var req struct{ Query string }
		_ = json.Unmarshal(body, &req)
		page := func(nodes []any) map[string]any {
			return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}
		}
		switch {
		case strings.Contains(req.Query, "WorkerDiscussions("):
			d := graphDiscussion{ID: "discussion", URL: listed.URL, Body: listed.Body, CreatedAt: listed.CreatedAt, LastEditedAt: listed.LastEditedAt}
			return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"discussions": page([]any{d})}}}), nil
		case strings.Contains(req.Query, "WorkerDiscussionComments("):
			comment := graphComment{ID: "approval", Body: approval, CreatedAt: now.Add(-time.Second), Author: graphActor{ID: c.Owner.ID, Login: c.Owner.Login, Kind: c.Owner.Kind}}
			return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"comments": page([]any{comment})}}}), nil
		case strings.Contains(req.Query, "WorkerDiscussionReplies("):
			return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"replies": page(nil)}}}), nil
		default:
			t.Fatalf("unexpected mocked request: %s", req.Query)
			return CommandResult{}, nil
		}
	}}
	g := NewGitHubClient(f)
	g.config = c
	goals, _, _, err := g.discussions(context.Background(), "planning/board")
	if err != nil || len(goals) != 1 {
		t.Fatalf("read: %v %+v", err, goals)
	}
	if a := EvaluateGoal(c, goals[0], now); a.State != "unapproved" {
		t.Fatalf("expected old-body/new-approval mixed read to be unapproved: %+v", a)
	}
	listed = current
	goals, _, _, err = g.discussions(context.Background(), "planning/board")
	if err != nil || len(goals) != 1 {
		t.Fatalf("second read: %v %+v", err, goals)
	}
	if a := EvaluateGoal(c, goals[0], now); a.State != "approved" {
		t.Fatalf("current body and same valid approval did not recover: %+v", a)
	}
	t.Log("same valid owner approval is unapproved with a stale listed body, approved on the next consistent read")
}

// Assessment probes below assert observed authorization gaps at 0f897bb.
func TestAuditOrdinaryApprovalApprovesStaleBroadScope(t *testing.T) {
	c, s, now := policyFixture(t)
	old := graphDiscussion{ID: "discussion", URL: s.Goals[0].URL, CreatedAt: now.Add(-2 * time.Hour), LastEditedAt: now.Add(-time.Hour)}
	old.Body = "Implement both repositories\n" + encodeMetadata("goal", struct {
		Repositories []string `json:"repositories"`
	}{[]string{"code/repo", "code/extra"}})
	current := old
	current.Body = "Implement only code/repo\n" + encodeMetadata("goal", struct {
		Repositories []string `json:"repositories"`
	}{[]string{"code/repo"}})
	current.LastEditedAt = now.Add(-30 * time.Second)
	g := NewGitHubClient(&fakeCommands{})
	g.config = c
	narrow, _ := g.goalFromDiscussion(current, nil)
	bound, err := GoalApprovalComment(c, narrow, narrow.Repositories)
	if err != nil {
		t.Fatal(err)
	}
	s.Tasks[0].URL, s.Tasks[0].Repo = "https://github.com/code/extra/issues/4", "code/extra"
	for _, tc := range []struct {
		name, command, staleState string
	}{
		{"command", "/coop approve", "approved"},
		{"ordinary statement", "approved", "approved"},
		{"hash-bound host control", bound, "unapproved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comments := []Comment{{ID: "approval", Body: tc.command, Author: c.Owner, CreatedAt: now.Add(-time.Second)}}
			// Model the old listing body paired with comments read after narrowing.
			stale, _ := g.goalFromDiscussion(old, comments)
			if a := EvaluateGoal(c, stale, now); a.State != tc.staleState || a.BodySHA != BodyHash(old.Body) {
				t.Fatalf("unexpected mixed-read approval: %+v", a)
			}
			s.Goals = []Goal{stale}
			jobs := Select(c, s, State{}, now)
			if tc.staleState == "approved" {
				if len(jobs) != 1 || jobs[0].Kind != "implement" || jobs[0].TaskURL != s.Tasks[0].URL {
					t.Fatalf("stale out-of-narrow-scope admission not reproduced: %+v", jobs)
				}
			} else if len(jobs) != 0 {
				t.Fatalf("hash-bound approval admitted stale scope: %+v", jobs)
			}
			fresh, _ := g.goalFromDiscussion(current, comments)
			if a := EvaluateGoal(c, fresh, now); a.State != "approved" {
				t.Fatalf("consistent narrowed scope should be approved: %+v", a)
			}
			s.Goals = []Goal{fresh}
			if jobs := Select(c, s, State{}, now); len(jobs) != 0 {
				t.Fatalf("consistent narrowed scope admitted code/extra: %+v", jobs)
			}
		})
	}
	t.Log("plain owner decisions approve stale broad scope and admit code/extra; hash-bound host approval fails closed, and a consistent fresh read excludes code/extra")
}

func TestAuditPRMetadataRetargetReusesEarlierReview(t *testing.T) {
	c, s, now := policyFixture(t)
	s.Tasks[0].Ready = false
	s.Tasks[0].MetadataUpdatedAt = now.Add(-3 * time.Minute)
	goal := s.Goals[0]
	goal.URL = "https://github.com/orgs/planning/discussions/2"
	goal.Body, goal.BodySHA = "Different approved goal", BodyHash("Different approved goal")
	task := s.Tasks[0]
	task.URL, task.Title, task.Body = "https://github.com/code/repo/issues/6", "Different task", "Different scope"
	task.BodySHA = TaskScopeHash(task.Title, task.Body)
	if err := ApplyTaskMetadata(c, &task, []Comment{{ID: "target-metadata", Author: c.Reviewer, CreatedAt: now.Add(-2 * time.Minute), Body: encodeMetadata("task", taskMetadata{Goal: goal.URL, ScopeSHA: task.BodySHA})}}); err != nil {
		t.Fatal(err)
	}
	s.Goals, s.Tasks = append(s.Goals, goal), append(s.Tasks, task)
	const ref = "https://github.com/code/repo/pull/5"
	head := strings.Repeat("a", 40)
	linked := s.Tasks[0]
	updated := now.Add(-2 * time.Minute)
	reviewed := now.Add(-30 * time.Second)
	f := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		switch endpoint {
		case "graphql":
			return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
				"url": ref, "body": encodeMetadata("pr", prMetadata{Task: linked.URL, Goal: linked.GoalURL}), "state": "OPEN", "headRefOid": head, "updatedAt": updated, "mergeStateStatus": "CLEAN",
				"author":  map[string]any{"databaseId": c.Implementer.ID, "login": c.Implementer.Login, "__typename": c.Implementer.Kind},
				"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": "SUCCESS"}}}}},
			}}}}), nil
		case "repos/code/repo/pulls/5/reviews?per_page=100&page=1":
			return jsonResponse([]any{map[string]any{"id": 1, "commit_id": head, "state": "APPROVED", "submitted_at": reviewed, "user": map[string]any{"id": c.Reviewer.ID, "login": c.Reviewer.Login, "type": c.Reviewer.Kind}}}), nil
		default:
			t.Fatalf("unexpected mocked request: %s %s", method, endpoint)
			return CommandResult{}, nil
		}
	}}
	g := NewGitHubClient(f)
	g.config = c
	for _, retarget := range []bool{false, true} {
		if retarget {
			linked, updated = task, now
		}
		pr, err := g.readPR(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		bindReviewScope(&pr, linked)
		s.PRs = []PR{pr}
		jobs := Select(c, s, State{}, now)
		if len(jobs) != 1 || jobs[0].Kind != "deliver" || jobs[0].TaskURL != linked.URL || jobs[0].GoalURL != linked.GoalURL || jobs[0].HeadSHA != head {
			t.Fatalf("retarget=%v: earlier exact-head review did not admit delivery: %+v", retarget, jobs)
		}
		if retarget {
			if pr.Revision != now.Format(time.RFC3339Nano) || !pr.ScopeUpdatedAt.Before(reviewed) {
				t.Fatalf("expected fresh PR revision but pre-review task scope: %+v", pr)
			}
			pr.ScopeUpdatedAt = updated
			s.PRs = []PR{pr}
			if jobs := Select(c, s, State{}, now); len(jobs) != 0 {
				t.Fatalf("control: post-review PR scope timestamp should block delivery: %+v", jobs)
			}
		}
	}
	t.Log("editing only PR task/goal metadata after approval reuses the old exact-head review to deliver another trusted task/goal; PR updatedAt is read but ignored by review scope binding")
}

func TestAuditWakePromptUsesStaleApproval(t *testing.T) {
	c, s, now := policyFixture(t)
	source := ConversationSource{DiscussionID: "discussion", NodeID: "discussion", RootID: "discussion", DiscussionURL: s.Goals[0].URL, Body: "Changed scope awaiting renewed approval", Author: c.Owner}
	r := WakeRequest{Target: c.Name, Kind: "ping", Source: source}
	prompt, err := BuildWakePrompt(c, r, source, s)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "Quoted wake input:\n"
	_, input, ok := strings.Cut(prompt, prefix)
	if !ok {
		t.Fatal("wake input absent")
	}
	var quoted struct {
		Approval Approval
		Goal     Goal
		Source   ConversationSource
	}
	if err := json.Unmarshal([]byte(input), &quoted); err != nil {
		t.Fatal(err)
	}
	if quoted.Approval.State != "approved" || quoted.Goal.Body == quoted.Source.Body {
		t.Fatalf("stale approval not reproduced: %+v", quoted)
	}
	current := s.Goals[0]
	current.Body, current.BodySHA, current.LastEditedAt = source.Body, BodyHash(source.Body), now
	if EvaluateGoal(c, current, now).State == "approved" {
		t.Fatal("fresh changed body should require renewed approval")
	}
	t.Log("fresh wake source contains a changed goal body, but supplied scheduler Approval still says approved for cached old goal")
}
