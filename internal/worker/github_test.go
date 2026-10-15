package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type fakeCommands struct {
	respond func(owner, endpoint, method string, body []byte) (CommandResult, error)
	calls   []string
}

func (f *fakeCommands) Run(ctx context.Context, argv, env []string, input io.Reader) (CommandResult, error) {
	if len(argv) < 7 || argv[0] != "/usr/local/libexec/coop-github" || argv[1] != "gh" || argv[2] != "--owner" || argv[4] != "--" || argv[5] != "api" {
		return CommandResult{}, errors.New("unrouted command")
	}
	method := "GET"
	for i, a := range argv {
		if a == "--method" && i+1 < len(argv) {
			method = argv[i+1]
		}
	}
	var body []byte
	if input != nil {
		body, _ = io.ReadAll(input)
	}
	f.calls = append(f.calls, argv[3]+":"+argv[6])
	return f.respond(argv[3], argv[6], method, body)
}
func response(status, body string) CommandResult {
	return CommandResult{Stdout: []byte("HTTP/2.0 " + status + "\r\nContent-Type: application/json\r\n\r\n" + body)}
}
func emptyGitHubResponse(c Config, owner, endpoint string, body []byte) (CommandResult, error) {
	if endpoint == "graphql" {
		var req struct{ Query string }
		_ = json.Unmarshal(body, &req)
		if strings.Contains(req.Query, "WorkerIdentity") {
			return response("200 OK", `{"data":{"viewer":{"__typename":"Bot","databaseId":2,"login":"builder"}}}`), nil
		}
		if strings.Contains(req.Query, "WorkerProject") {
			return response("200 OK", `{"data":{"organization":{"projectV2":{"id":"project-2","fields":{"nodes":[],"pageInfo":{"hasNextPage":false}},"items":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`), nil
		}
	}
	if strings.HasPrefix(endpoint, "installation/repositories") {
		return response("200 OK", `{"total_count":0,"repositories":[]}`), nil
	}
	if strings.Contains(endpoint, "/issues?") || strings.Contains(endpoint, "/pulls?") {
		return response("200 OK", `[]`), nil
	}
	if strings.Contains(endpoint, "graphql") {
		return response("200 OK", `{"data":{"repository":{"discussions":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}`), nil
	}
	return CommandResult{}, errors.New("unexpected endpoint")
}
func TestSnapshotRejectsPartialOwnerRead(t *testing.T) {
	c := configFixture(t)
	c.DiscussionRepositories = nil
	f := &fakeCommands{respond: func(o, e, m string, b []byte) (CommandResult, error) {
		if o == "planning" {
			return CommandResult{Stderr: []byte("sentinel-secret")}, errors.New("sentinel-secret")
		}
		return emptyGitHubResponse(c, o, e, b)
	}}
	s, err := NewGitHubClient(f).Snapshot(context.Background(), c, Snapshot{})
	if err == nil || s.Complete || strings.Contains(err.Error(), "sentinel-secret") {
		t.Fatal("partial snapshot was authorized or exposed stderr")
	}
}
func TestSnapshotVerifiesBotIdentity(t *testing.T) {
	c := configFixture(t)
	c.DiscussionRepositories = nil
	c.Projects = nil
	f := &fakeCommands{respond: func(o, e, m string, b []byte) (CommandResult, error) {
		return response("200 OK", `{"data":{"viewer":{"__typename":"Bot","databaseId":99,"login":"builder"}}}`), nil
	}}
	if s, err := NewGitHubClient(f).Snapshot(context.Background(), c, Snapshot{}); err == nil || s.Complete {
		t.Fatal("wrong bot accepted")
	}
}

func TestSnapshotRetainsUnlinkedImplementerPRForDiagnostics(t *testing.T) {
	c := Defaults(configFixture(t))
	c.Repositories, c.DiscussionRepositories, c.Projects = []string{"code/repo"}, nil, nil
	f := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if strings.Contains(endpoint, "/pulls?") {
			return jsonResponse([]any{map[string]any{"html_url": "https://github.com/code/repo/pull/5"}}), nil
		}
		if endpoint == "graphql" {
			var req struct{ Query string }
			_ = json.Unmarshal(body, &req)
			if strings.Contains(req.Query, "WorkerPR") {
				return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
					"url": "https://github.com/code/repo/pull/5", "body": "Manual PR without worker metadata", "state": "OPEN", "headRefOid": "head", "headRefName": "feature", "baseRefName": "main", "author": map[string]any{"__typename": "Bot", "databaseId": c.Implementer.ID, "login": c.Implementer.Login},
				}}}}), nil
			}
		}
		return emptyGitHubResponse(c, owner, endpoint, body)
	}}
	s, err := NewGitHubClient(f).Snapshot(context.Background(), c, Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.PRs) != 1 || s.PRs[0].TaskURL != "" || s.PRs[0].Author != c.Implementer {
		t.Fatal("unlinked implementer PR disappeared from observation")
	}
	if jobs := Select(c, s, State{}, time.Now()); len(jobs) != 0 {
		t.Fatal("observed unlinked PR bypassed approval/metadata admission")
	}
}

func TestViewerUserShapeStillVerifiesConfiguredAppBot(t *testing.T) {
	c := configFixture(t)
	f := &fakeCommands{respond: func(o, e, m string, b []byte) (CommandResult, error) {
		var request struct{ Query string }
		_ = json.Unmarshal(b, &request)
		if strings.Contains(request.Query, "... on Bot") {
			t.Fatal("viewer has User type; Bot fragment is impossible")
		}
		return response("200 OK", `{"data":{"viewer":{"__typename":"User","databaseId":2,"login":"builder[bot]"}}}`), nil
	}}
	g := NewGitHubClient(f)
	g.config = c
	if err := g.verifyOwner(context.Background(), "code"); err != nil {
		t.Fatal("installation viewer reports User shape but matches configured bot ID")
	}
}
func TestMetadataOnlyTrustedAndScopeBound(t *testing.T) {
	c, s, n := policyFixture(t)
	task := s.Tasks[0]
	body := `<!-- coop-task:v1 {"goal":"` + task.GoalURL + `","scope_sha256":"` + task.BodySHA + `","depends_on":[]} -->`
	comments := []Comment{{ID: "bad", Body: body, Author: Actor{ID: 9, Login: c.Reviewer.Login, Kind: "Bot"}, CreatedAt: n}}
	if err := ApplyTaskMetadata(c, &task, comments); err == nil {
		t.Fatal("untrusted metadata accepted")
	}
	comments = append(comments, Comment{ID: "good", Body: body, Author: c.Reviewer, CreatedAt: n.Add(time.Second)})
	if err := ApplyTaskMetadata(c, &task, comments); err != nil || task.MetadataScopeSHA != task.BodySHA {
		t.Fatal("trusted metadata rejected")
	}
	comments[1].Body = strings.ReplaceAll(body, task.BodySHA, strings.Repeat("a", 64))
	if err := ApplyTaskMetadata(c, &task, comments); err == nil {
		t.Fatal("changed issue scope accepted")
	}
	if _, err := ParseGoalMetadata(`<!-- coop-goal:v1 {"repositories":["code/repo"],"token":"sentinel-secret"} -->`); err == nil || strings.Contains(err.Error(), "sentinel-secret") {
		t.Fatal("unknown metadata accepted or echoed")
	}
}
func TestGitHubRateLimitRedactsAndSetsRetry(t *testing.T) {
	f := &fakeCommands{respond: func(o, e, m string, b []byte) (CommandResult, error) {
		return CommandResult{ExitCode: 1, Stderr: []byte("sentinel-secret"), Stdout: []byte("HTTP/2.0 429 Too Many Requests\r\nRetry-After: 120\r\n\r\n{\"message\":\"sentinel-secret\"}")}, nil
	}}
	c := NewGitHubClient(f)
	_, _, err := c.api(context.Background(), "code", "GET", "installation/repositories", nil)
	var apiErr *GitHubError
	if !errors.As(err, &apiErr) || apiErr.Category != "rate_limited" || !apiErr.RetryAt.After(time.Now().Add(time.Minute)) || strings.Contains(err.Error(), "sentinel-secret") {
		t.Fatal("rate limit not safely categorized")
	}
}
func TestGitHubPaginationNeverSilentlyTruncates(t *testing.T) {
	f := &fakeCommands{respond: func(o, e, m string, b []byte) (CommandResult, error) {
		return response("200 OK", `{"data":{"node":{"comments":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"same"}}}}}`), nil
	}}
	c := NewGitHubClient(f)
	if _, err := c.connection(context.Background(), "code", "query WorkerTest($after:String){node{id}}", nil, []string{"node", "comments"}); err == nil {
		t.Fatal("stalled cursor accepted")
	}
	if len(f.calls) > 3 {
		t.Fatal("cursor loop not bounded")
	}
}

func jsonResponse(value any) CommandResult {
	raw, _ := json.Marshal(value)
	return response("200 OK", string(raw))
}
func TestSnapshotReadsNestedApprovalAndCrossOwnerProject(t *testing.T) {
	c, source, now := policyFixture(t)
	c = Defaults(c)
	goal := source.Goals[0]
	goal.Body = encodeMetadata("goal", map[string]any{"repositories": goal.Repositories})
	task := source.Tasks[0]
	issue := map[string]any{"html_url": task.URL, "number": 4, "title": task.Title, "body": task.Body, "state": "open", "updated_at": now.Format(time.RFC3339)}
	page := func(nodes any) map[string]any {
		return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}
	}
	f := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if strings.HasPrefix(endpoint, "installation/repositories") {
			return jsonResponse(map[string]any{"repositories": []any{map[string]any{"full_name": "code/repo", "has_issues": true}}}), nil
		}
		if strings.Contains(endpoint, "/issues/4/comments") {
			if owner != "code" {
				t.Fatal("Project token used for personal issue")
			}
			return jsonResponse([]any{map[string]any{"id": 10, "body": encodeMetadata("task", taskMetadata{Goal: goal.URL, ScopeSHA: task.BodySHA}), "user": map[string]any{"id": 3, "login": "reviewer[bot]", "type": "Bot"}, "created_at": now.Format(time.RFC3339), "updated_at": now.Format(time.RFC3339)}}), nil
		}
		if strings.Contains(endpoint, "/issues?") {
			return jsonResponse([]any{issue}), nil
		}
		if endpoint == "repos/code/repo/issues/4" {
			return jsonResponse(issue), nil
		}
		if strings.Contains(endpoint, "/pulls?") {
			return jsonResponse([]any{}), nil
		}
		if endpoint != "graphql" {
			return CommandResult{}, errors.New("unexpected endpoint")
		}
		var req struct {
			Query     string
			Variables map[string]any
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		var data any
		switch {
		case strings.Contains(req.Query, "WorkerIdentity"):
			data = map[string]any{"viewer": map[string]any{"__typename": "Bot", "databaseId": 2, "login": "builder"}}
		case strings.Contains(req.Query, "WorkerDiscussions"):
			data = map[string]any{"repository": map[string]any{"discussions": page([]any{map[string]any{"id": "discussion", "url": goal.URL, "body": goal.Body, "createdAt": now.Add(-time.Hour), "lastEditedAt": now.Add(-time.Hour), "author": map[string]any{"__typename": "Bot", "databaseId": 3, "login": "reviewer"}}})}}
		case strings.Contains(req.Query, "WorkerGoalVersion"):
			data = map[string]any{"node": map[string]any{"id": "discussion", "url": goal.URL, "body": goal.Body, "createdAt": now.Add(-time.Hour), "lastEditedAt": now.Add(-time.Hour), "author": map[string]any{"__typename": "Bot", "databaseId": 3, "login": "reviewer"}}}
		case strings.Contains(req.Query, "WorkerDiscussionComments"):
			data = map[string]any{"node": map[string]any{"comments": page([]any{map[string]any{"id": "parent-comment", "body": "Plan", "createdAt": now.Add(-time.Minute), "author": map[string]any{"__typename": "Bot", "databaseId": 3, "login": "reviewer"}}})}}
		case strings.Contains(req.Query, "WorkerDiscussionReplies"):
			data = map[string]any{"node": map[string]any{"replies": page([]any{map[string]any{"id": "owner-reply", "body": "/coop approve", "createdAt": now, "author": map[string]any{"__typename": "User", "databaseId": 1, "login": "owner"}}})}}
		case strings.Contains(req.Query, "WorkerProjectFields"):
			data = map[string]any{"node": map[string]any{"fields": page([]any{map[string]any{"id": "status", "name": "Status", "options": []any{map[string]any{"id": "progress", "name": "In Progress"}, map[string]any{"id": "done", "name": "Done"}}}})}}
		case strings.Contains(req.Query, "WorkerProjectItems"):
			data = map[string]any{"node": map[string]any{"items": page([]any{map[string]any{"id": "item", "content": map[string]any{"__typename": "Issue", "url": task.URL}}})}}
		case strings.Contains(req.Query, "WorkerProjectValues"):
			data = map[string]any{"node": map[string]any{"fieldValues": page([]any{map[string]any{"name": "Ready", "field": map[string]any{"id": "status", "name": "Status"}}})}}
		case strings.Contains(req.Query, "WorkerProject"):
			data = map[string]any{"repositoryOwner": map[string]any{"projectV2": map[string]any{"id": "PVT_project"}}}
		default:
			return CommandResult{}, errors.New("unexpected query")
		}
		return jsonResponse(map[string]any{"data": data}), nil
	}}
	g := NewGitHubClient(f)
	s, err := g.Snapshot(context.Background(), c, Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	jobs := Select(c, s, State{}, now.Add(time.Second))
	if !s.Complete || len(jobs) != 1 || jobs[0].Kind != "implement" {
		t.Fatalf("nested approval/Project not actionable: %+v", s)
	}
	if len(s.Tasks) != 1 || s.Tasks[0].ProjectOwner != "planning" {
		t.Fatal("Project owner not preserved")
	}
	if _, err := g.Snapshot(context.Background(), c, s); err != nil {
		t.Fatal(err)
	}
}

func TestClaimReconcilesAfterAmbiguousPost(t *testing.T) {
	c, s, n := policyFixture(t)
	j := Select(c, s, State{}, n)[0]
	instance := BodyHash("instance")
	var comments []any
	posts := 0
	f := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if method == "GET" {
			return jsonResponse(comments), nil
		}
		posts++
		var payload struct{ Body string }
		_ = json.Unmarshal(body, &payload)
		comments = append(comments, map[string]any{"id": 20, "body": payload.Body, "user": map[string]any{"id": 2, "login": "builder[bot]", "type": "Bot"}, "created_at": n})
		return CommandResult{}, errors.New("response lost")
	}}
	g := NewGitHubClient(f)
	g.config = c
	if _, err := g.Claim(context.Background(), j, instance); err == nil {
		t.Fatal("ambiguous response appeared successful")
	}
	claim, err := g.Claim(context.Background(), j, instance)
	if err != nil || posts != 1 || claim.InstanceID != instance {
		t.Fatal("claim duplicated instead of reconciled")
	}
}

func TestPrivateBootstrapGoalOutsideCodeCatalogIsRefreshed(t *testing.T) {
	c, s, n := policyFixture(t)
	ref := "https://github.com/planning/private/issues/7"
	c.BootstrapGoals = []BootstrapGoal{{URL: ref, BodySHA: BodyHash("Private goal"), Repositories: []string{"code/repo"}, OwnerID: 1, Source: "owner-confirmed"}}
	if !goalInScope(c, Goal{URL: ref}) {
		t.Fatal("explicit private bootstrap thread outside code catalog rejected")
	}
	refs := goalReferences(c, map[string]Task{s.Tasks[0].URL: {GoalURL: ref}}, nil)
	if len(refs) != 1 || refs[0] != ref {
		t.Fatal("private bootstrap thread not queued for refresh")
	}
	_ = n
}
