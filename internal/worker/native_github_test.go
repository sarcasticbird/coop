package worker

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNativeTaskReadsV3WithoutLegacyGoalURL(t *testing.T) {
	cfg, _, _, _, _ := teamScopeFixture(t)
	cfg.Version = 3
	b, _ := nativeBindingFixture()
	b.AuthorID = 12
	b.PeerID = 11
	b.QAID = 13
	b.TaskScopeSHA = TaskScopeHash("Coop task", "bounded scope")
	b.PolicySHA, _ = TeamPolicySHA(*cfg.Team)
	now := time.Now().UTC()
	commands := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if endpoint == "repos/code/repo/issues/1" {
			return jsonResponse(map[string]any{"html_url": b.TaskURL, "number": 1, "title": "Coop task", "body": "bounded scope", "state": "open", "updated_at": now}), nil
		}
		if strings.HasPrefix(endpoint, "repos/code/repo/issues/1/comments?") {
			return jsonResponse([]any{map[string]any{"id": 1, "body": NativeTaskMetadata(b), "user": map[string]any{"id": teamPlanner(cfg).ID, "login": teamPlanner(cfg).Login, "type": "Bot"}, "created_at": now.Add(-time.Minute), "updated_at": now.Add(-time.Minute)}}), nil
		}
		t.Fatalf("unexpected native task request: %s", endpoint)
		return CommandResult{}, nil
	}}
	g := NewGitHubClient(commands)
	task, err := g.ReadNativeTask(context.Background(), cfg, b.TaskURL)
	if err != nil {
		t.Fatal(err)
	}
	if task.Binding.Goal != b.Goal || task.Task.GoalURL != "" {
		t.Fatal("native goal was reinterpreted as a GitHub URL")
	}
}

func TestNativePRScopeRejectsLegacyBinding(t *testing.T) {
	b, _ := nativeBindingFixture()
	g := NewGitHubClient(&fakeCommands{})
	g.config.Version = 3
	pr := PR{URL: "https://github.com/code/repo/pull/1", TaskURL: b.TaskURL, AssignmentSHA: NativeAssignmentSHA(b), MasterGoal: &b.Goal, BaseBranch: b.BaseBranch}
	version := prBodyVersion{URL: pr.URL, Body: `<!-- coop-pr:v2 {} -->`, HeadRefOid: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(), BaseRefName: b.BaseBranch}
	if _, err := g.readPRScope(context.Background(), "code/repo", 1, version, &pr); err == nil {
		t.Fatal("legacy metadata accepted as native review scope")
	}
}

func TestNativeTaskRejectsChangeDuringMetadataRead(t *testing.T) {
	cfg, _, _, _, _ := teamScopeFixture(t)
	cfg.Version = 3
	b, _ := nativeBindingFixture()
	b.AuthorID = 12
	b.PeerID = 11
	b.QAID = 13
	b.TaskScopeSHA = TaskScopeHash("Task", "scope")
	b.PolicySHA, _ = TeamPolicySHA(*cfg.Team)
	reads := 0
	g := NewGitHubClient(&fakeCommands{respond: func(owner, endpoint, method string, raw []byte) (CommandResult, error) {
		if endpoint == "repos/code/repo/issues/1" {
			reads++
			body := "scope"
			if reads > 1 {
				body = "changed"
			}
			return jsonResponse(map[string]any{"html_url": b.TaskURL, "number": 1, "title": "Task", "body": body, "state": "open"}), nil
		}
		return jsonResponse([]any{map[string]any{"id": 1, "body": NativeTaskMetadata(b), "user": map[string]any{"id": teamPlanner(cfg).ID, "login": teamPlanner(cfg).Login, "type": "Bot"}, "created_at": "2026-01-01T00:00:00Z"}}), nil
	}})
	if _, err := g.ReadNativeTask(context.Background(), cfg, b.TaskURL); err == nil {
		t.Fatal("mixed native task and metadata accepted")
	}
}
