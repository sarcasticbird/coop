package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestNativeActionFreshAuthorityBlocksWrites(t *testing.T) {
	cfg, m, cat, _ := dispatchFixture(t)
	candidate := NativeCandidates(cfg, cat.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: candidate.Goal.Revision.Ref, Job: candidate.Job, Binding: &candidate.Task.Binding, Phase: "active"}
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	writes := 0
	g := NewGitHubClient(&fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if method != "GET" {
			writes++
		}
		return CommandResult{}, c.ErrForbidden
	}})
	for _, state := range []string{"paused", "completed", "unapproved", "rejected"} {
		m.authority.State = state
		if _, err := RunNativeAction(context.Background(), cfg, m, g, x.Job.ID, NativeAction{Operation: "deliver", RequestID: "try-" + state}); err == nil {
			t.Fatal("stale authority allowed action")
		}
	}
	if writes != 0 {
		t.Fatal("published with stale authority")
	}
}
func TestNativePlannerCreatesRegisteredV3Task(t *testing.T) {
	cfg, m, cat, _ := dispatchFixture(t)
	cfg.Projects = nil
	cfg.ProjectOwners = nil
	cfg.Name = cfg.Team.Planner
	cfg.Self = teamPlanner(cfg)
	cfg.Master.AgentID = "peer"
	m.self.Principal.ID = "peer"
	m.self.Principal.GitHubBotID = cfg.Self.ID
	m.self.Placement.AgentID = "peer"
	cat.catalog.Tasks = nil
	candidate := NativeCandidates(cfg, cat.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: candidate.Goal.Revision.Ref, Job: candidate.Job, Phase: "active"}
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	var metadata string
	created := 0
	now := time.Now().UTC()
	commands := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if endpoint == "graphql" {
			return jsonResponse(map[string]any{"data": map[string]any{"viewer": map[string]any{"__typename": "Bot", "databaseId": cfg.Self.ID, "login": cfg.Self.Login}}}), nil
		}
		if endpoint == "repos/code/repo/issues" && method == "POST" {
			created++
			return jsonResponse(map[string]any{"html_url": "https://github.com/code/repo/issues/1", "number": 1, "title": "Task", "body": "scope", "state": "open"}), nil
		}
		if endpoint == "repos/code/repo/issues/1/comments" && method == "POST" {
			var in struct{ Body string }
			_ = json.Unmarshal(body, &in)
			metadata = in.Body
			return jsonResponse(map[string]any{"id": 1}), nil
		}
		if endpoint == "repos/code/repo/issues/1" {
			return jsonResponse(map[string]any{"html_url": "https://github.com/code/repo/issues/1", "number": 1, "title": "Task", "body": "scope", "state": "open"}), nil
		}
		if strings.HasPrefix(endpoint, "repos/code/repo/issues/1/comments?") {
			return jsonResponse([]any{map[string]any{"id": 1, "body": metadata, "user": map[string]any{"id": cfg.Self.ID, "login": cfg.Self.Login, "type": "Bot"}, "created_at": now}}), nil
		}
		t.Fatalf("unexpected %s %s", method, endpoint)
		return CommandResult{}, nil
	}}
	action := NativeAction{Operation: "create-task", RequestID: "task-one", Repository: "code/repo", Title: "Task", Body: "scope", BaseBranch: "main"}
	result, err := RunNativeAction(context.Background(), cfg, m, NewGitHubClient(commands), x.Job.ID, action)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseNativeTaskMetadata(metadata)
	if err != nil || b.Goal != x.Goal || b.AuthorID != 12 || b.PeerID != 11 || b.QAID != 13 {
		t.Fatalf("invalid production metadata %+v %v", b, err)
	}
	if result.URL != b.TaskURL || len(m.links) != 1 || m.links[0].AssignmentSHA != NativeAssignmentSHA(b) {
		t.Fatal("task not registered")
	}
	if _, err = RunNativeAction(context.Background(), cfg, m, NewGitHubClient(commands), x.Job.ID, action); err != nil || created != 1 {
		t.Fatal("replayed task creation", err)
	}
	action.Body = "changed"
	if _, err = RunNativeAction(context.Background(), cfg, m, NewGitHubClient(commands), x.Job.ID, action); err == nil {
		t.Fatal("changed replay accepted")
	}
}
