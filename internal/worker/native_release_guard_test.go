package worker

import (
	"context"
	"encoding/json"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"strings"
	"testing"
	"time"
)

type releaseMasterFixture struct {
	*dispatchMaster
	handoff c.Handoff
}

func (m *releaseMasterFixture) Handoffs(context.Context, c.HandoffQuery) (c.HandoffPage, error) {
	return c.HandoffPage{Handoffs: []c.Handoff{m.handoff}}, nil
}

func TestNativeReleaseValidatesSourceBeforeTagWrites(t *testing.T) {
	for _, mode := range []string{"happy", "unmerged", "stale-head", "qa-changes", "wrong-author", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			cfg, m, catalog, _ := dispatchFixture(t)
			task := catalog.catalog.Tasks[0]
			head := strings.Repeat("a", 40)
			merged := strings.Repeat("c", 40)
			m.links = []c.ArtifactLink{{Goal: task.Binding.Goal, Kind: "task", URL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding)}}
			transport := &nativeDeliveryTransport{t: t, cfg: cfg, head: head, merged: true, closed: true, taskTitle: task.Task.Title, taskBody: task.Task.Body, metadata: NativeTaskMetadata(task.Binding)}
			transport.prBody = NativePRBody(NativePRMetadata{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding)})
			publisher := &releaseTestAPI{}
			transport.commands = &fakeCommands{respond: func(owner, path, method string, body []byte) (CommandResult, error) {
				if strings.Contains(path, "/git/") || strings.Contains(path, "/actions/workflows/") || strings.Contains(path, "/releases/tags/") {
					raw, _, err := publisher.api(t.Context(), owner, method, path, nil)
					if githubMissing(err) {
						return response("404 Not Found", "{}"), nil
					}
					if err != nil {
						return CommandResult{}, err
					}
					var v any
					if err = json.Unmarshal(raw, &v); err != nil {
						t.Fatal(err)
					}
					return jsonResponse(v), nil
				}
				if strings.Contains(path, "/compare/") {
					return jsonResponse(map[string]any{"status": "identical", "behind_by": 0}), nil
				}
				if strings.Contains(path, "/contents/") {
					return jsonResponse(map[string]string{"type": "file", "path": ".github/workflows/release.yml"}), nil
				}
				res, err := transport.response(owner, path, method, body)
				if path == "repos/code/repo/pulls/1" && method == "GET" {
					var v map[string]any
					parts := strings.SplitN(string(res.Stdout), "\r\n\r\n", 2)
					if len(parts) != 2 || json.Unmarshal([]byte(parts[1]), &v) != nil {
						t.Fatal("fixture response")
					}
					v["merged"] = transport.merged
					v["merge_commit_sha"] = merged
					return jsonResponse(v), err
				}
				return res, err
			}}
			g := NewGitHubClient(transport)
			p, err := g.ReadNativeReleasePR(t.Context(), cfg, task, m.authority, "https://github.com/code/repo/pull/1")
			if err != nil {
				t.Fatal(err)
			}
			peer := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: head, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
			qa := NativeQAResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: head, PRScopeRevision: p.ScopeRevision, ArtifactSHA: p.QAArtifact.SHA256, AcceptanceSHA: NativeAcceptanceSHA(m.authority), Environment: m.authority.Revision.Proposal.QA.Environment, EnvironmentRevision: m.authority.Revision.Proposal.QA.EnvironmentRevision, EvidenceMessageID: "qa-report", EvidenceBodySHA: BodyHash("QA passed"), Verdict: "approved"}
			review := func(id int, actor Actor, body string) map[string]any {
				return map[string]any{"fullDatabaseId": id, "state": "APPROVED", "body": body, "submittedAt": time.Now().Add(-time.Second).UTC(), "commit": map[string]string{"oid": head}, "author": map[string]any{"__typename": "Bot", "databaseId": actor.ID, "login": actor.Login}}
			}
			transport.reviews = []map[string]any{review(1, cfg.Team.Members[0].Actor, NativePeerMetadata(peer)), review(2, cfg.Team.Members[2].Actor, NativeQAMetadata(qa))}
			m.reports = map[string]c.Message{"qa-report": {ID: "qa-report", ThreadID: m.authority.Revision.Proposal.QA.EvidenceDestination.ThreadID, AuthorID: m.authority.Actors.QA.PrincipalID, AuthorKind: "agent", Body: "QA passed"}}
			h := c.Handoff{ID: "ho_release", ActorID: "owner", CreatedAt: time.Now().UTC(), Input: c.HandoffInput{Kind: "release", Goal: task.Binding.Goal, TaskURL: task.Task.URL, PRURL: p.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: head, ArtifactSHA: p.QAArtifact.SHA256, EvidenceMessageID: "owner-approval", Tag: "v0.2.7", Workflow: "release.yml", RequestID: "ship"}}
			switch mode {
			case "unmerged":
				transport.merged = false
			case "stale-head":
				h.Input.HeadSHA = strings.Repeat("b", 40)
			case "qa-changes":
				transport.reviews[1]["state"] = "CHANGES_REQUESTED"
			case "wrong-author":
				cfg.Self = cfg.Team.Members[0].Actor
			case "revoked":
				m.authority.Valid = false
				m.authority.State = "paused"
			}
			result, err := RunNativeRelease(t.Context(), cfg, &releaseMasterFixture{m, h}, g, task.Binding.Goal, h.ID)
			if mode == "happy" {
				if err != nil || result.Phase != "waiting" {
					t.Fatal("valid merged source blocked", result, err)
				}
				publisher.published = true
				m.authority.Valid = false
				m.authority.State = "paused"
				if err = atomicPrivate(cfg.StateDir+"/admission-control.json", mustRecoveryJSON(control{Paused: true})); err != nil {
					t.Fatal(err)
				}
				result, err = RunNativeRelease(t.Context(), cfg, &releaseMasterFixture{m, h}, g, task.Binding.Goal, h.ID)
				if err != nil || result.Phase != "published" {
					t.Fatal("read-only publication observation blocked by quiescence/pause", result, err)
				}
				m.self.RestoreEpoch++
				if _, err = RunNativeRelease(t.Context(), cfg, &releaseMasterFixture{m, h}, g, task.Binding.Goal, h.ID); err == nil {
					t.Fatal("write executor ignored changed restore epoch")
				}
				result, err = ObserveNativeRelease(t.Context(), cfg, m, g, task.Binding.Goal, h.ID)
				if err != nil || result.Phase != "published" {
					t.Fatal("read-only observation could not supply restore proof", result, err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid source released", mode)
			}
			for _, call := range publisher.calls {
				if strings.HasPrefix(call, "POST ") {
					t.Fatal("invalid source wrote tag", mode)
				}
			}
		})
	}
}
