package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/githubapp"
)

func TestGitHubCacheCrossOwnerProjectReadiness(t *testing.T) {
	for _, scenario := range []string{"ready", "absent", "unreadable", "opaque"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, _, live, _ := dispatchFixture(t)
			cfg.Projects = Defaults(Config{Projects: []ProjectRef{{Owner: "tracking", Number: 3}}}).Projects
			task := live.catalog.Tasks[0]
			tr := &nativeDeliveryTransport{t: t, cfg: cfg, taskBody: task.Task.Body, taskTitle: task.Task.Title, metadata: NativeTaskMetadata(task.Binding), projectAdded: true, projectReady: true}
			boardReads := 0
			commands := &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
				switch {
				case endpoint == "repos/code/repo":
					return jsonResponse(map[string]any{"id": 1, "full_name": "code/repo"}), nil
				case endpoint == "repos/code/repo/issues/1":
					return jsonResponse(map[string]any{"id": 2, "node_id": "I_1", "number": 1, "html_url": task.Task.URL, "title": task.Task.Title, "body": task.Task.Body, "state": "open"}), nil
				case strings.Contains(string(body), "WorkerNativeTaskProjectItems"):
					if owner != "code" {
						t.Fatal("membership read used the Project owner's credentials")
					}
					return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"issue": map[string]any{"projectItems": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false}}}}}}), nil
				case strings.Contains(string(body), "WorkerCacheProjectItems"):
					boardReads++
					if owner != "tracking" || strings.Contains(string(body), "DraftIssue{") || strings.Contains(string(body), "fieldValues") {
						t.Fatal("fallback widened content access or used the wrong owner")
					}
					if scenario == "unreadable" {
						return CommandResult{Stdout: []byte("HTTP/1.1 403 Forbidden\r\nContent-Type: application/json\r\n\r\n{}"), ExitCode: 1}, nil
					}
					nodes := []any{map[string]any{"id": "unrelated-item", "content": map[string]any{"__typename": "Issue", "url": "https://github.com/private-other/customer/issues/9"}}}
					if scenario == "opaque" {
						nodes = append(nodes, map[string]any{"id": "hidden-item", "content": nil})
					}
					if scenario == "ready" {
						nodes = append(nodes, map[string]any{"id": "item", "content": map[string]any{"__typename": "Issue", "url": task.Task.URL}})
					}
					return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"items": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}}}}), nil
				case strings.Contains(string(body), "WorkerCacheProjectOrder"):
					return jsonResponse(map[string]any{"data": map[string]any{"node": map[string]any{"items": map[string]any{"nodes": []any{map[string]any{"content": map[string]any{"url": "https://github.com/private-other/customer/issues/9"}}, map[string]any{"content": map[string]any{"url": task.Task.URL}}}, "pageInfo": map[string]any{"hasNextPage": false}}}}}), nil
				case strings.Contains(string(body), "WorkerProjectValues"):
					var request struct{ Variables struct{ ID string } }
					if json.Unmarshal(body, &request) != nil || request.Variables.ID != "item" {
						t.Fatal("fetched details for an unregistered Project item")
					}
				}
				return tr.response(owner, endpoint, method, body)
			}}
			now := time.Now().UTC()
			source := NewGitHubObservationSource(NewGitHubClient(commands)).(*githubObservationSource)
			source.identity = func(context.Context) (githubapp.Status, error) {
				return githubapp.Status{AppID: 1, BotLogin: cfg.Self.Login, Installations: map[string]githubapp.InstallationStatus{"code": {ID: 2, ExpiresAt: now.Add(time.Hour)}}}, nil
			}
			lease := c.GitHubLease{ID: "lease", AppID: 1, InstallationID: 2, Object: c.GitHubObjectRef{RouteID: "route", Goal: task.Binding.Goal, TaskURL: task.Task.URL, URL: task.Task.URL, Kind: "task", AssignmentSHA: NativeAssignmentSHA(task.Binding)}}
			observation, err := source.Observe(t.Context(), cfg, lease, nil)
			if scenario == "unreadable" || scenario == "opaque" {
				if err == nil {
					t.Fatal("unreadable Project was published as known not-ready")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got NativeTask
			if err = json.Unmarshal(observation.Payloads["task"], &got); err != nil {
				t.Fatal(err)
			}
			if boardReads != 1 {
				t.Fatalf("missing issue membership was not checked against the scoped board: reads=%d", boardReads)
			}
			if scenario == "ready" {
				if !got.Task.Ready || got.Task.ItemID != "item" || got.Task.ProjectOwner != "tracking" || got.Task.Priority != 1000 || got.Task.Ordinal != 1 {
					t.Fatalf("eligible cross-owner task lost readiness or order: %+v", got.Task)
				}
				catalog := live.catalog
				catalog.Tasks[0] = got
				if candidates := NativeCandidates(cfg, catalog, nil, now); len(candidates) != 1 || candidates[0].Job.Kind != "implement" {
					t.Fatal("cached task lost its eligible author candidate", candidates)
				}
			} else if got.Task.Ready {
				t.Fatal("truly absent Project item became ready")
			}
			for _, raw := range observation.Payloads {
				if bytes.Contains(raw, []byte("private-other")) || bytes.Contains(raw, []byte("unrelated-item")) {
					t.Fatal("unregistered board metadata entered shared observations")
				}
			}
		})
	}
}
