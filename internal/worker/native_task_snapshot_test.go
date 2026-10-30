package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func taskSnapshotFixture(t *testing.T, change ...func(*NativeTaskBinding)) (Config, *nativeMasterFixture, *fakeCommands, Job, *int) {
	t.Helper()
	cfg, catalog, task := nativeWorkFixture(t)
	cfg.Projects, cfg.ProjectOwners = nil, nil
	job := NativeCandidates(cfg, catalog, nil, time.Now())[0].Job
	for _, mutate := range change {
		mutate(&task.Binding)
	}
	links := []c.ArtifactLink{{Kind: "task", Goal: task.Binding.Goal, URL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding)}}
	m := &nativeMasterFixture{authority: catalog.Goals[0], links: links}
	count := new(int)
	now := time.Now().UTC()
	runner := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		(*count)++
		issue := strings.TrimPrefix(endpoint, "repos/code/repo/issues/")
		issue, _, _ = strings.Cut(issue, "/")
		number, _ := strconv.Atoi(issue)
		binding := task.Binding
		if number > 1 {
			binding.TaskURL = fmt.Sprintf("https://github.com/code/repo/issues/%d", number)
			binding.DependsOn = nil
		}
		switch {
		case endpoint == "graphql":
			return jsonResponse(map[string]any{"data": map[string]any{"viewer": map[string]any{"__typename": "Bot", "databaseId": cfg.Self.ID, "login": cfg.Self.Login}}}), nil
		case number > 0 && !strings.Contains(endpoint, "/comments"):
			return jsonResponse(map[string]any{"html_url": binding.TaskURL, "number": number, "title": "Task", "body": "scope", "state": "open", "updated_at": now}), nil
		case number > 0 && strings.Contains(endpoint, "/comments?"):
			return jsonResponse([]any{map[string]any{"id": 1, "body": NativeTaskMetadata(binding), "user": map[string]any{"id": 11, "type": "Bot", "login": "planner[bot]"}, "created_at": now, "updated_at": now}}), nil
		default:
			t.Fatalf("unrelated GitHub read: %s", endpoint)
			return CommandResult{}, nil
		}
	}}
	return cfg, m, runner, job, count
}

func TestNativeTaskSnapshotIgnoresUnrelatedWork(t *testing.T) {
	cfg, m, runner, job, calls := taskSnapshotFixture(t)
	g := NewGitHubClient(runner)
	first, err := g.NativeTaskSnapshot(t.Context(), cfg, m, job)
	if err != nil || !first.Complete || len(first.Tasks) != 1 {
		t.Fatal(first, err)
	}
	baseline := *calls
	for i := 2; i <= 101; i++ {
		b := first.Tasks[0].Binding
		b.TaskURL = fmt.Sprintf("https://github.com/code/repo/issues/%d", i)
		m.links = append(m.links, c.ArtifactLink{Kind: "task", Goal: *job.Master.Goal, URL: b.TaskURL, AssignmentSHA: NativeAssignmentSHA(b)})
	}
	*calls = 0
	second, err := g.NativeTaskSnapshot(t.Context(), cfg, m, job)
	if err != nil || len(second.Tasks) != 1 || *calls != baseline {
		t.Fatalf("unrelated work expanded selected read: calls %d/%d, err %v", *calls, baseline, err)
	}
	if got := NativeCandidates(cfg, second, nil, time.Now()); len(got) != 1 || got[0].Job.ID != job.ID {
		t.Fatal("selection changed", got)
	}
	t.Logf("selected-task GitHub requests: %d with 1 or 101 registered tasks", baseline)
	*calls = 0
	full, err := g.NativeSnapshot(t.Context(), cfg, m)
	if err != nil || len(full.Tasks) != 101 || *calls <= baseline {
		t.Fatal("missing full catalog baseline", err, *calls)
	}
	t.Logf("full catalog GitHub requests for the same 101 tasks: %d", *calls)
}

func TestKataDiscoveryOmitsUnretainedGitHubTaskButSavedValidationReadsIt(t *testing.T) {
	cfg, master, runner, job, calls := taskSnapshotFixture(t)
	cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
	installTaskProviderHTTP(t, &cfg, func(path string) any {
		if path != "/api/v1/tasks/capabilities" {
			t.Fatalf("unexpected Kata request %s", path)
		}
		sha, _ := TeamPolicySHA(*cfg.Team)
		return c.TaskCapabilities{Provider: "kata", ProjectUID: providerProject, PolicySHA: sha}
	})
	reader := NewGitHubClient(runner)
	catalog, err := reader.NativeSnapshot(t.Context(), cfg, master)
	if err != nil || !catalog.Complete || len(catalog.Tasks) != 0 || *calls != 1 {
		t.Fatalf("unretained GitHub task reached discovery: tasks=%d calls=%d err=%v", len(catalog.Tasks), *calls, err)
	}
	*calls = 0
	selected, err := reader.NativeTaskSnapshot(t.Context(), cfg, master, job)
	if err != nil || !selected.Complete || selected.TaskScope != job.TaskURL || len(selected.Tasks) != 1 || selected.Tasks[0].Task.URL != job.TaskURL || *calls <= 1 {
		t.Fatalf("saved GitHub task lost exact validation: tasks=%d calls=%d err=%v", len(selected.Tasks), *calls, err)
	}
}

func TestNativeTaskSnapshotRejectsChangedAuthority(t *testing.T) {
	cfg, m, runner, job, _ := taskSnapshotFixture(t)
	m.links[0].AssignmentSHA = BodyHash("changed")
	if _, err := NewGitHubClient(runner).NativeTaskSnapshot(t.Context(), cfg, m, job); err == nil {
		t.Fatal("changed artifact accepted")
	}
	m.links = nil
	if _, err := NewGitHubClient(runner).NativeTaskSnapshot(t.Context(), cfg, m, job); err == nil {
		t.Fatal("unregistered task accepted")
	}
}

func TestNativeTaskSnapshotIncompleteAndCycles(t *testing.T) {
	cfg, m, runner, job, _ := taskSnapshotFixture(t)
	m.links = append(m.links, m.links[0])
	if _, err := NewGitHubClient(runner).NativeTaskSnapshot(context.Background(), cfg, m, job); err == nil {
		t.Fatal("duplicate task accepted")
	}
	if _, err := NewGitHubClient(runner).NativeTaskSnapshot(t.Context(), cfg, m, job, "https://github.com/outsider/repo/issues/1"); err == nil {
		t.Fatal("outside dependency accepted")
	}
	cfg, m, runner, job, _ = taskSnapshotFixture(t, func(b *NativeTaskBinding) { b.DependsOn = []string{b.TaskURL} })
	job.Master.AssignmentSHA = m.links[0].AssignmentSHA
	if _, err := NewGitHubClient(runner).NativeTaskSnapshot(t.Context(), cfg, m, job); err == nil || !strings.Contains(err.Error(), "native_dependency_cycle") {
		t.Fatal("cycle not rejected", err)
	}
}

func TestNativeTaskSnapshotIncludesDependencyClosure(t *testing.T) {
	ref := "https://github.com/code/repo/issues/2"
	cfg, m, runner, job, _ := taskSnapshotFixture(t, func(b *NativeTaskBinding) { b.DependsOn = []string{ref} })
	job.Master.AssignmentSHA = m.links[0].AssignmentSHA
	_, _, task := nativeWorkFixture(t)
	dep := task.Binding
	dep.TaskURL = ref
	m.links = append(m.links, c.ArtifactLink{Kind: "task", Goal: dep.Goal, URL: ref, AssignmentSHA: NativeAssignmentSHA(dep)})
	out, err := NewGitHubClient(runner).NativeTaskSnapshot(t.Context(), cfg, m, job)
	if err != nil || len(out.Tasks) != 2 {
		t.Fatal("dependency omitted", out, err)
	}
	x := NativeExecution{Job: job, Binding: &task.Binding}
	if !nativeScopeChanged(x, out) {
		t.Fatal("changed dependency assignment hidden from stale-work validation")
	}
	for _, candidate := range NativeCandidates(cfg, out, nil, time.Now()) {
		if candidate.Job.TaskURL == job.TaskURL {
			t.Fatal("open dependency admitted implementation")
		}
	}
}

type outsideEngagementMaster struct{ *nativeMasterFixture }

func (m outsideEngagementMaster) ApprovedGoals(context.Context, string, string) (c.GoalPage, error) {
	return c.GoalPage{}, nil
}

func TestNativeTaskSnapshotKeepsEngagementScope(t *testing.T) {
	cfg, m, runner, job, _ := taskSnapshotFixture(t)
	if _, err := NewGitHubClient(runner).NativeTaskSnapshot(t.Context(), cfg, outsideEngagementMaster{m}, job); err == nil {
		t.Fatal("goal outside configured engagement accepted")
	}
}

func TestNativeTaskSnapshotRebindCancelsBeforeCycle(t *testing.T) {
	cfg, m, runner, job, _ := taskSnapshotFixture(t, func(b *NativeTaskBinding) { b.DependsOn = []string{b.TaskURL} })
	if _, err := NewGitHubClient(runner).NativeTaskSnapshot(t.Context(), cfg, m, job); !errors.Is(err, errNativeCandidateStale) {
		t.Fatal("conclusive rebind hidden by dependency cycle", err)
	}
}

func TestNativeTaskSnapshotTargetsProjectMembership(t *testing.T) {
	checkNativeProjectMembership(t, "actual")
}

func TestNativeTaskSnapshotProjectCompatibility(t *testing.T) {
	for _, mode := range []string{"draft", "cross_owner", "quota"} {
		t.Run(mode, func(t *testing.T) { checkNativeProjectMembership(t, mode) })
	}
}

func checkNativeProjectMembership(t *testing.T, mode string) {
	t.Helper()
	cfg, m, runner, job, _ := taskSnapshotFixture(t)
	cfg.Projects = Defaults(Config{Projects: []ProjectRef{{Owner: "code", Number: 1}}}).Projects
	if mode == "cross_owner" {
		cfg.Projects[0].Owner = "planning"
	}
	original := runner.respond
	page := func(nodes any) map[string]any {
		return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}
	}
	runner.respond = func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		var req struct{ Query string }
		_ = json.Unmarshal(body, &req)
		switch {
		case strings.Contains(req.Query, "WorkerNativeTaskProjectItems"):
			if owner != "code" {
				t.Errorf("repository read used Project owner's installation: %s", owner)
			}
			if mode == "cross_owner" {
				return CommandResult{Stdout: []byte("HTTP/1.1 403 Forbidden\r\n\r\n")}, nil
			}
			if mode == "quota" {
				return CommandResult{Stdout: []byte("HTTP/1.1 429 Too Many Requests\r\nRetry-After: 60\r\n\r\n")}, nil
			}
			if mode == "draft" {
				return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"issue": map[string]any{"projectItems": page([]any{})}}}}), nil
			}
			return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"issue": map[string]any{"projectItems": page([]any{map[string]any{"id": "item", "project": map[string]any{"id": "project"}, "content": map[string]any{"__typename": "Issue", "url": job.TaskURL}}})}}}}), nil
		case strings.Contains(req.Query, "WorkerProjectItems"):
			if mode == "draft" || mode == "cross_owner" {
				if owner != cfg.Projects[0].Owner {
					t.Errorf("board fallback used wrong installation: %s", owner)
				}
				return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"items": page([]any{map[string]any{"id": "draft", "content": map[string]any{"__typename": "DraftIssue", "body": job.TaskURL}}})}}}), nil
			}
			t.Error("selected read scanned unrelated Project items")
			return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"items": page([]any{map[string]any{"id": "inaccessible", "content": nil}})}}}), nil
		case strings.Contains(req.Query, "WorkerProjectFields"):
			return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"fields": page([]any{map[string]any{"id": "status", "name": "Status", "options": []any{map[string]any{"id": "ready", "name": "Ready"}, map[string]any{"id": "progress", "name": "In Progress"}, map[string]any{"id": "done", "name": "Done"}}}})}}}), nil
		case strings.Contains(req.Query, "WorkerProjectValues"):
			return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"fieldValues": page([]any{map[string]any{"name": "Ready", "field": map[string]any{"id": "status", "name": "Status"}}})}}}), nil
		case strings.Contains(req.Query, "WorkerProject"):
			return jsonResponse(map[string]any{"data": map[string]any{"repositoryOwner": map[string]any{"projectV2": map[string]any{"id": "project"}}}}), nil
		}
		return original(owner, endpoint, method, body)
	}
	out, err := NewGitHubClient(runner).NativeTaskSnapshot(t.Context(), cfg, m, job)
	if mode == "quota" {
		var quota *GitHubError
		if !errors.As(err, &quota) || quota.Category != "rate_limited" {
			t.Fatal("quota wait hidden by fallback", err)
		}
		return
	}
	if err != nil || len(out.Tasks) != 1 || !out.Tasks[0].Task.Ready {
		t.Fatal("selected Project facts unavailable", out, err)
	}
}
