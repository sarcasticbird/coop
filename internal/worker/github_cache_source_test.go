package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/githubapp"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestGitHubCacheSourceIncremental(t *testing.T) {
	for _, scenario := range []string{"reuse", "comment_churn", "warm_comment_update", "slow"} {
		t.Run(scenario, func(t *testing.T) {
			churn := scenario == "comment_churn"
			warm := false
			cfg, _, _, _, _ := teamScopeFixture(t)
			cfg.Version = 3
			cfg.Projects = nil
			cfg.ProjectOwners = nil
			b, _ := nativeBindingFixture()
			b.AuthorID = 12
			b.PeerID = 11
			b.QAID = 13
			b.TaskScopeSHA = TaskScopeHash("Task", "scope")
			b.PolicySHA, _ = TeamPolicySHA(*cfg.Team)
			now := time.Now().UTC()
			clock := &fakeClock{now: now}
			comments := 0
			commands := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
				if endpoint == "repos/code/repo" {
					return jsonResponse(map[string]any{"id": 1, "full_name": "code/repo"}), nil
				}
				if endpoint == "repos/code/repo/issues/1" {
					return jsonResponse(map[string]any{"id": 2, "node_id": "I_1", "html_url": b.TaskURL, "number": 1, "title": "Task", "body": "scope", "state": "open", "updated_at": now}), nil
				}
				if strings.Contains(endpoint, "/comments?") {
					comments++
					if scenario == "slow" && comments == 1 {
						clock.now = clock.now.Add(2 * time.Minute)
					}
					metadata := NativeTaskMetadata(b)
					if churn && comments > 1 || scenario == "warm_comment_update" && warm {
						metadata += "\nChanged during observation"
					}
					return jsonResponse([]any{map[string]any{"id": 1, "body": metadata, "user": map[string]any{"id": teamPlanner(cfg).ID, "login": teamPlanner(cfg).Login, "type": "Bot"}, "created_at": now.Add(-time.Minute)}}), nil
				}
				t.Fatalf("unexpected %s", endpoint)
				return CommandResult{}, nil
			}}
			src := NewGitHubObservationSource(NewGitHubClient(commands)).(*githubObservationSource)
			src.clock = clock
			src.identity = func(context.Context) (githubapp.Status, error) {
				return githubapp.Status{AppID: 1, BotLogin: cfg.Self.Login, Installations: map[string]githubapp.InstallationStatus{"code": {ID: 2, ExpiresAt: now.Add(time.Hour)}}}, nil
			}
			lease := c.GitHubLease{ID: "lease", Object: c.GitHubObjectRef{RouteID: "route", Goal: b.Goal, TaskURL: b.TaskURL, URL: b.TaskURL, Kind: "task", AssignmentSHA: NativeAssignmentSHA(b)}, AppID: 1, InstallationID: 2}
			first, e := src.Observe(context.Background(), cfg, lease, nil)
			if churn {
				if e == nil || comments != 2 {
					t.Fatal("published incoherent task comments", comments, e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if !first.Commit.HeaderObservedAt.Equal(clock.Now()) {
				t.Fatal("header verification time lost", first.Commit.HeaderObservedAt, clock.Now())
			}
			for _, ref := range first.Commit.Datasets {
				if !ref.ObservedAt.Equal(clock.Now()) {
					t.Fatal("fresh dataset verification time lost", ref)
				}
			}
			if comments != 2 {
				t.Fatal(comments)
			}
			var prior []c.GitHubDatasetChunk
			for kind, raw := range first.Payloads {
				prior = append(prior, c.GitHubDatasetChunk{Ref: c.GitHubDatasetRef{Kind: kind}, Data: raw})
			}
			warm = true
			second, e := src.Observe(context.Background(), cfg, lease, prior)
			if e != nil {
				t.Fatal(e)
			}
			if comments != 4 {
				t.Fatal("expected independent validation of both comment scans", comments)
			}
			var task NativeTask
			if json.Unmarshal(second.Payloads["task"], &task) != nil || task.Binding.TaskURL != b.TaskURL {
				t.Fatal("lost normalized binding")
			}
		})
	}
}

func TestGitHubCacheSourceProjectAbsence(t *testing.T) {
	for _, state := range []string{"open", "closed"} {
		t.Run(state, func(t *testing.T) {
			cfg, _, cat, _ := dispatchFixture(t)
			cfg.Projects = Defaults(Config{Projects: []ProjectRef{{Owner: "code", Number: 1}, {Owner: "code", Number: 2}}}).Projects
			task := cat.catalog.Tasks[0]
			tr := &nativeDeliveryTransport{t: t, cfg: cfg, taskBody: task.Task.Body, taskTitle: task.Task.Title, metadata: NativeTaskMetadata(task.Binding), projectAdded: true, projectReady: true}
			projectReads := 0
			commands := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
				if endpoint == "repos/code/repo" {
					return jsonResponse(map[string]any{"id": 1, "full_name": "code/repo"}), nil
				}
				if endpoint == "repos/code/repo/issues/1" {
					return jsonResponse(map[string]any{"id": 2, "node_id": "I_1", "html_url": task.Task.URL, "number": 1, "title": task.Task.Title, "body": task.Task.Body, "state": state, "state_reason": "completed"}), nil
				}
				if strings.Contains(string(body), "WorkerProject") || strings.Contains(string(body), "WorkerNativeTaskProjectItems") || strings.Contains(string(body), "WorkerCacheProjectOrder") {
					projectReads++
				}
				if strings.Contains(string(body), "WorkerProject(") {
					var req struct{ Variables struct{ Number int } }
					_ = json.Unmarshal(body, &req)
					id := "project"
					if req.Variables.Number == 2 {
						id = "other-board"
					}
					return jsonResponse(map[string]any{"data": map[string]any{"repositoryOwner": map[string]any{"projectV2": map[string]any{"id": id}}}}), nil
				}
				if strings.Contains(string(body), "WorkerCacheProjectOrder") {
					return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"items": map[string]any{"nodes": []any{map[string]any{"content": map[string]any{"url": task.Task.URL}}}, "pageInfo": map[string]any{"hasNextPage": false}}}}}), nil
				}
				if strings.Contains(string(body), "WorkerCacheProjectItems") {
					return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"items": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}}}}}), nil
				}
				return tr.response(owner, endpoint, method, body)
			}}
			src := NewGitHubObservationSource(NewGitHubClient(commands)).(*githubObservationSource)
			now := time.Now()
			src.identity = func(context.Context) (githubapp.Status, error) {
				return githubapp.Status{AppID: 1, BotLogin: cfg.Self.Login, Installations: map[string]githubapp.InstallationStatus{"code": {ID: 2, ExpiresAt: now.Add(time.Hour)}}}, nil
			}
			l := c.GitHubLease{ID: "lease", AppID: 1, InstallationID: 2, Object: c.GitHubObjectRef{RouteID: "route", Goal: task.Binding.Goal, TaskURL: task.Task.URL, URL: task.Task.URL, Kind: "task", AssignmentSHA: NativeAssignmentSHA(task.Binding)}}
			out, err := src.Observe(t.Context(), cfg, l, nil)
			if err != nil {
				t.Fatal("readable absence on another board blocked task", err)
			}
			var got NativeTask
			if err = json.Unmarshal(out.Payloads["task"], &got); err != nil {
				t.Fatal(err)
			}
			if state == "closed" {
				if !got.Task.Completed || got.Task.Ready || projectReads != 0 {
					t.Fatal("terminal dependency required active Project membership", got, projectReads)
				}
			} else if !got.Task.Ready || got.Task.ProjectID != "project" {
				t.Fatal("membership on first board lost", got)
			}
		})
	}
}

func TestGitHubCacheSourcePRHeadAndTerminalReuse(t *testing.T) {
	cfg, _, live, _ := dispatchFixture(t)
	task := live.catalog.Tasks[0]
	transport := &nativeDeliveryTransport{t: t, cfg: cfg, taskTitle: task.Task.Title, taskBody: task.Task.Body, metadata: NativeTaskMetadata(task.Binding), head: strings.Repeat("a", 40), baseSHA: strings.Repeat("b", 40), branch: "agent/test"}
	transport.prBody = NativePRBody(NativePRMetadata{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding)})
	archives, reviews := 0, 0
	taskVersion := time.Now().UTC()
	commands := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if endpoint == "repos/code/repo" {
			return jsonResponse(map[string]any{"id": 1, "full_name": "code/repo"}), nil
		}
		if strings.Contains(endpoint, "/tarball/") {
			archives++
		}
		if strings.Contains(string(body), "WorkerTeamReviews") {
			reviews++
		}
		result, e := transport.response(owner, endpoint, method, body)
		if endpoint == "repos/code/repo/issues/1" || endpoint == "repos/code/repo/pulls/1" {
			res, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(result.Stdout)), nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			var data map[string]any
			if json.Unmarshal(raw, &data) != nil {
				t.Fatal("header JSON")
			}
			data["id"], data["node_id"] = 2, "object-node"
			if endpoint == "repos/code/repo/issues/1" {
				data["updated_at"] = taskVersion
			}
			state := "open"
			if transport.merged {
				state = "closed"
			}
			data["state"] = state
			return jsonResponse(data), nil
		}
		return result, e
	}}
	source := NewGitHubObservationSource(NewGitHubClient(commands)).(*githubObservationSource)
	now := time.Now().UTC()
	clock := &fakeClock{now: now}
	source.clock = clock
	source.identity = func(context.Context) (githubapp.Status, error) {
		return githubapp.Status{AppID: 1, BotLogin: cfg.Self.Login, Installations: map[string]githubapp.InstallationStatus{"code": {ID: 2, ExpiresAt: now.Add(time.Hour)}}}, nil
	}
	a := live.catalog.Goals[0]
	a.Revision.Proposal.QA.Required = true
	lease := c.GitHubLease{ID: "lease", AppID: 1, InstallationID: 2, Authority: a, Object: c.GitHubObjectRef{RouteID: "route", Goal: task.Binding.Goal, TaskURL: task.Task.URL, URL: "https://github.com/code/repo/pull/1", Kind: "pull_request", AssignmentSHA: NativeAssignmentSHA(task.Binding)}}
	var previous []c.GitHubDatasetChunk
	run := func() {
		t.Helper()
		out, e := source.Observe(t.Context(), cfg, lease, previous)
		if e != nil {
			t.Fatal(e)
		}
		previous = nil
		for kind, raw := range out.Payloads {
			previous = append(previous, c.GitHubDatasetChunk{Ref: c.GitHubDatasetRef{Kind: kind}, Data: raw})
		}
		// Apply the master's publication freshness rules, then exercise the real
		// cached projection. A warm 61-second refresh must still cover this PR.
		_, cache, master, _ := cachedFixture(t)
		cache.page.Objects[0].DueAt = clock.Now().Add(time.Minute)
		cache.page.Objects[0].Datasets[0].DueAt = clock.Now().Add(5 * time.Minute)
		coverage := c.GitHubCoverage{Object: lease.Object, Header: out.Commit.Header, DueAt: out.Commit.HeaderObservedAt.Add(time.Minute)}
		for _, ref := range out.Commit.Datasets {
			ref.Object, ref.HeaderRevision = lease.Object, out.Commit.Header.Revision
			ref.DueAt = ref.ObservedAt.Add(time.Minute)
			coverage.Datasets = append(coverage.Datasets, ref)
			cache.data[ref.SHA256] = out.Payloads[ref.Kind]
		}
		cache.page.Objects = append(cache.page.Objects, coverage)
		projection, err := NewCachedNativeReader(cfg, source.github, cache, clock).NativeSnapshot(t.Context(), cfg, master)
		if err != nil || !projection.Cache.Ready[task.Task.URL] {
			t.Fatal("warm published PR lost coverage", projection.Cache, err)
		}
	}
	run()
	if archives != 1 {
		t.Fatal("initial artifact", archives)
	}
	clock.now = clock.now.Add(61 * time.Second)
	taskVersion = taskVersion.Add(61 * time.Second)
	run()
	if archives != 1 || reviews != 4 {
		t.Fatal("warm mutable/immutable refresh", archives, reviews)
	}
	transport.head = strings.Repeat("c", 40)
	clock.now = clock.now.Add(61 * time.Second)
	run()
	if archives != 2 {
		t.Fatal("new head reused old artifact", archives)
	}
	transport.merged = true
	clock.now = clock.now.Add(time.Minute)
	run()
	priorReviews := reviews
	clock.now = clock.now.Add(16 * time.Minute)
	run()
	if reviews != priorReviews {
		t.Fatal("unchanged terminal PR reread reviews", reviews, priorReviews)
	}
}

func TestGitHubCacheProjectOrderContainsOnlyScopedObservations(t *testing.T) {
	cfg, _, cat, _ := dispatchFixture(t)
	cfg.Projects = Defaults(Config{Projects: []ProjectRef{{Owner: "code", Number: 1}}}).Projects
	task := cat.catalog.Tasks[0]
	tr := &nativeDeliveryTransport{t: t, cfg: cfg, taskBody: task.Task.Body, taskTitle: task.Task.Title, metadata: NativeTaskMetadata(task.Binding), projectAdded: true, projectReady: true}
	orderReads := 0
	commands := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if endpoint == "repos/code/repo" {
			return jsonResponse(map[string]any{"id": 1, "full_name": "code/repo"}), nil
		}
		if strings.Contains(string(body), "WorkerCacheProjectOrder") {
			orderReads++
			return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"items": map[string]any{"nodes": []any{map[string]any{"content": map[string]any{"url": "https://github.com/private-other/customer/issues/9"}}, map[string]any{"content": map[string]any{"url": task.Task.URL}}}, "pageInfo": map[string]any{"hasNextPage": false}}}}}), nil
		}
		res, e := tr.response(owner, endpoint, method, body)
		if endpoint == "repos/code/repo/issues/1" {
			parsed, _ := http.ReadResponse(bufio.NewReader(bytes.NewReader(res.Stdout)), nil)
			raw, _ := io.ReadAll(parsed.Body)
			_ = parsed.Body.Close()
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			m["id"] = 1
			return jsonResponse(m), nil
		}
		return res, e
	}}
	src := NewGitHubObservationSource(NewGitHubClient(commands)).(*githubObservationSource)
	now := time.Now()
	src.identity = func(context.Context) (githubapp.Status, error) {
		return githubapp.Status{AppID: 1, BotLogin: cfg.Self.Login, Installations: map[string]githubapp.InstallationStatus{"code": {ID: 2, ExpiresAt: now.Add(time.Hour)}}}, nil
	}
	l := c.GitHubLease{ID: "lease", AppID: 1, InstallationID: 2, Object: c.GitHubObjectRef{RouteID: "route", Goal: task.Binding.Goal, TaskURL: task.Task.URL, URL: task.Task.URL, Kind: "task", AssignmentSHA: NativeAssignmentSHA(task.Binding)}}
	var rankObserved time.Time
	for range 2 {
		out, e := src.Observe(t.Context(), cfg, l, nil)
		if e != nil {
			t.Fatal(e)
		}
		var got NativeTask
		_ = json.Unmarshal(out.Payloads["task"], &got)
		if got.Task.Ordinal != 1 {
			t.Fatal("Project order lost", got.Task.Ordinal)
		}
		for _, ref := range out.Commit.Datasets {
			if ref.Kind == "task" {
				if !rankObserved.IsZero() && !ref.ObservedAt.Equal(rankObserved) {
					t.Fatal("reused Project rank acquired new freshness", ref.ObservedAt, rankObserved)
				}
				rankObserved = ref.ObservedAt
			}
		}
		var state providerState
		_ = json.Unmarshal(out.Payloads["provider"], &state)
		for _, entry := range state.Entries {
			if bytes.Contains(entry.Body, []byte("private-other")) {
				t.Fatal("unrelated Project content entered shared cache")
			}
		}
	}
	if orderReads != 1 {
		t.Fatal("Project ordering not reused", orderReads)
	}
}
