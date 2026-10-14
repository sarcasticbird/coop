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
