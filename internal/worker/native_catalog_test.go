package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type nativeMasterFixture struct {
	authority c.GoalAuthority
	links     []c.ArtifactLink
}

func (m *nativeMasterFixture) ResolveGoal(context.Context, c.GoalRef) (c.GoalAuthority, error) {
	return m.authority, nil
}
func (m *nativeMasterFixture) ApprovedGoals(context.Context, string, string) (c.GoalPage, error) {
	return c.GoalPage{Goals: []c.GoalAuthority{m.authority}}, nil
}
func (m *nativeMasterFixture) Artifacts(_ context.Context, in c.ArtifactQuery) (c.ArtifactPage, error) {
	var links []c.ArtifactLink
	for _, link := range m.links {
		if link.Goal == in.Goal {
			links = append(links, link)
		}
	}
	return c.ArtifactPage{Links: links}, nil
}
func (m *nativeMasterFixture) RegisterArtifact(_ context.Context, in c.ArtifactLink) (c.ArtifactLink, error) {
	for i, existing := range m.links {
		if existing.URL == in.URL && existing.Goal == in.Goal {
			m.links[i] = in
			return in, nil
		}
	}
	m.links = append(m.links, in)
	return in, nil
}
func (m *nativeMasterFixture) Message(context.Context, string) (c.Message, error) {
	return c.Message{}, errors.New("unexpected message read")
}
func (m *nativeMasterFixture) Send(context.Context, c.SendMessage) (c.MessageReceipt, error) {
	return c.MessageReceipt{}, errors.New("unexpected message send")
}
func TestNativeCatalogPreservesProjectStatus(t *testing.T) {
	cfg, _, _, _, _ := teamScopeFixture(t)
	cfg.Version = 3
	cfg.Master = &MasterConfig{MasterID: "master", Engagement: "coop"}
	cfg.Projects = []ProjectRef{{Owner: "planning", Number: 1, StatusField: "Status", ReadyValue: "Ready", InProgressValue: "In Progress", DoneValue: "Done"}}
	b, source := nativeBindingFixture()
	b.AuthorID = 12
	b.PeerID = 11
	b.QAID = 13
	b.PolicySHA, _ = TeamPolicySHA(*cfg.Team)
	b.TaskScopeSHA = TaskScopeHash("Task", "scope")
	now := time.Now().UTC()
	a := source.authority
	a.Actors.Author.GitHubBotID = 12
	a.Actors.Peer.GitHubBotID = 11
	a.Actors.QA.GitHubBotID = 13
	m := &nativeMasterFixture{authority: a, links: []c.ArtifactLink{{Kind: "task", Goal: b.Goal, URL: b.TaskURL, AssignmentSHA: NativeAssignmentSHA(b)}}}
	page := func(nodes any) map[string]any {
		return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}
	}
	commands := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if strings.HasPrefix(endpoint, "repos/code/repo/issues/99") {
			t.Fatal("native catalog fetched an unregistered legacy board task")
		}
		if endpoint == "repos/code/repo/issues/1" {
			return jsonResponse(map[string]any{"html_url": b.TaskURL, "number": 1, "title": "Task", "body": "scope", "state": "open", "updated_at": now}), nil
		}
		if strings.HasPrefix(endpoint, "repos/code/repo/issues/1/comments?") {
			return jsonResponse([]any{map[string]any{"id": 1, "body": NativeTaskMetadata(b), "user": map[string]any{"id": 11, "type": "Bot", "login": "planner[bot]"}, "created_at": now, "updated_at": now}}), nil
		}
		var req struct{ Query string }
		if json.Unmarshal(body, &req) != nil {
			t.Fatalf("unexpected endpoint %s", endpoint)
		}
		var data any
		switch {
		case strings.Contains(req.Query, "WorkerIdentity"):
			data = map[string]any{"viewer": map[string]any{"__typename": "Bot", "databaseId": cfg.Self.ID, "login": cfg.Self.Login}}
		case strings.Contains(req.Query, "WorkerProjectFields"):
			data = map[string]any{"node": map[string]any{"fields": page([]any{map[string]any{"id": "status", "name": "Status", "options": []any{map[string]any{"id": "ready", "name": "Ready"}, map[string]any{"id": "progress", "name": "In Progress"}, map[string]any{"id": "done", "name": "Done"}}}})}}
		case strings.Contains(req.Query, "WorkerProjectItems"):
			data = map[string]any{"node": map[string]any{"items": page([]any{map[string]any{"id": "legacy", "content": map[string]any{"__typename": "Issue", "url": "https://github.com/code/repo/issues/99"}}, map[string]any{"id": "item", "content": map[string]any{"__typename": "Issue", "url": b.TaskURL}}})}}
		case strings.Contains(req.Query, "WorkerProjectValues"):
			data = map[string]any{"node": map[string]any{"fieldValues": page([]any{map[string]any{"name": "In Progress", "field": map[string]any{"id": "status", "name": "Status"}}})}}
		case strings.Contains(req.Query, "WorkerProject"):
			data = map[string]any{"repositoryOwner": map[string]any{"projectV2": map[string]any{"id": "project"}}}
		default:
			t.Fatalf("unexpected query %s", req.Query)
		}
		return jsonResponse(map[string]any{"data": data}), nil
	}}
	result, err := NewGitHubClient(commands).NativeSnapshot(context.Background(), cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || len(result.Tasks) != 1 || result.Tasks[0].Task.Ready || result.Tasks[0].Task.ProjectID != "project" {
		t.Fatalf("Project tracking was dropped: %+v", result.Tasks)
	}
	// Same revision reapproval preserves linked tasks for explicit planner rebind,
	// without making the whole catalog unavailable or admitting the old binding.
	m.authority.ApprovalSequence++
	renewed, err := NewGitHubClient(commands).NativeSnapshot(context.Background(), cfg, m)
	if err != nil || !renewed.Complete || len(renewed.Tasks) != 1 {
		t.Fatalf("renewed approval broke catalog: %+v %v", renewed, err)
	}
	cfg.Name = cfg.Team.Planner
	cfg.Self = teamPlanner(cfg)
	cfg.Master.AgentID = "peer"
	jobs := NativeCandidates(cfg, renewed, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != "plan" || len(jobs[0].RebindTasks) != 1 || jobs[0].RebindTasks[0] != b.TaskURL {
		t.Fatalf("missing explicit rebind: %+v", jobs)
	}

}
