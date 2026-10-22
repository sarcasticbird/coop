package worker

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The transport models GitHub's task, PR, contributor/review connections,
// immutable artifact and merge APIs. All readers and producer helpers are real.
type nativeDeliveryTransport struct {
	t                                                              *testing.T
	cfg                                                            Config
	commands                                                       *fakeCommands
	taskBody, taskTitle, metadata, prBody, branch, head, localHead string
	reviews                                                        []map[string]any
	closed, merged                                                 bool
	autoCloseIssue                                                 bool
	projectReadLag                                                 int
	projectAdded, projectReady                                     bool
	writes                                                         []string
	checks                                                         string
	baseSHA, restBaseSHA, mergeState                               string
	rejectAncestor                                                 bool
	snapshotBaseSHA, baseReadSHA                                   string
	advanceBaseAfterAncestry                                       bool
	checksAfterCheckout                                            string
	failSourceAfterPush                                            bool
}

func (s *nativeDeliveryTransport) Run(ctx context.Context, argv, env []string, in io.Reader) (CommandResult, error) {
	return s.commands.Run(ctx, argv, env, in)
}
func (s *nativeDeliveryTransport) RunIn(_ context.Context, dir string, argv, env []string, in io.Reader) (CommandResult, error) {
	if len(argv) < 2 || argv[0] != "git" {
		s.t.Fatalf("unexpected checkout command %v", argv)
	}
	out := ""
	switch argv[1] {
	case "config":
		out = "https://github.com/code/repo.git"
		if strings.Contains(strings.Join(argv, " "), "pushurl") {
			out = "/dev/null/coop-qa-push-disabled"
		}
	case "remote":
		out = "/dev/null/coop-qa-push-disabled"
	case "branch":
		out = s.branch
	case "rev-parse":
		out = s.head
	case "status", "switch", "clone", "fetch", "reset":
		if argv[1] == "status" && s.checksAfterCheckout != "" {
			s.checks = s.checksAfterCheckout
		}
	case "merge-base":
		if len(argv) != 5 || argv[2] != "--is-ancestor" || argv[3] != s.baseSHA || argv[4] != s.localHead {
			s.t.Fatalf("wrong branch ancestry check: %v", argv)
		}
		if s.rejectAncestor {
			return CommandResult{ExitCode: 1}, nil
		}
		if s.advanceBaseAfterAncestry {
			s.baseSHA = strings.Repeat("e", 40)
		}
	case "push":
		s.writes = append(s.writes, "push")
	default:
		s.t.Fatalf("unexpected git %v", argv)
	}
	if argv[1] == "rev-parse" && s.localHead != "" {
		out = s.localHead
	}
	if argv[1] == "push" && s.localHead != "" {
		s.head = s.localHead
		if s.mergeState == "BEHIND" {
			s.mergeState = "CLEAN"
		}
	}
	return CommandResult{Stdout: []byte(out)}, nil
}
func (s *nativeDeliveryTransport) response(owner, endpoint, method string, body []byte) (CommandResult, error) {
	if method != "GET" && endpoint != "graphql" {
		s.writes = append(s.writes, method+" "+endpoint)
	}
	taskURL := "https://github.com/code/repo/issues/1"
	prURL := "https://github.com/code/repo/pull/1"
	page := func(nodes any, count int) map[string]any {
		return map[string]any{"nodes": nodes, "totalCount": count, "pageInfo": map[string]any{"hasNextPage": false}}
	}
	actor := func(id int64, login string) map[string]any {
		return map[string]any{"__typename": "Bot", "databaseId": id, "login": login}
	}
	created := time.Now().Add(-time.Hour).UTC()
	if endpoint == "graphql" {
		var req struct{ Query string }
		if json.Unmarshal(body, &req) != nil {
			s.t.Fatal("bad graphql")
		}
		var data any
		pr := map[string]any{"url": prURL, "body": s.prBody, "createdAt": created, "headRefOid": s.head, "headRefName": s.branch, "baseRefName": "main", "updatedAt": created, "mergeStateStatus": "CLEAN", "state": "OPEN", "merged": s.merged, "author": actor(12, "implementer[bot]"), "commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": "SUCCESS"}}}}}}
		pr["baseRefOid"] = s.baseSHA
		if s.snapshotBaseSHA != "" {
			pr["baseRefOid"] = s.snapshotBaseSHA
		}
		if s.baseSHA != "" {
			pr["baseRef"] = map[string]any{"name": "main", "target": map[string]any{"oid": s.baseSHA}}
		}
		if s.mergeState != "" {
			pr["mergeStateStatus"] = s.mergeState
		}
		if s.merged {
			pr["state"] = "MERGED"
		}
		if s.checks != "" {
			pr["commits"] = map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": s.checks}}}}}
		}
		switch {
		case strings.Contains(req.Query, "WorkerNativeBase"):
			sha := s.baseSHA
			if s.baseReadSHA != "" {
				sha = s.baseReadSHA
			}
			data = map[string]any{"repository": map[string]any{"ref": map[string]any{"name": "main", "target": map[string]any{"oid": sha}}}}
		case strings.Contains(req.Query, "WorkerNativeProjectAdd"):
			s.projectAdded = true
			data = map[string]any{"addProjectV2ItemById": map[string]any{"item": map[string]any{"id": "item"}}}
		case strings.Contains(req.Query, "WorkerNativeProjectReady"):
			s.projectReady = true
			data = map[string]any{"updateProjectV2ItemFieldValue": map[string]any{"projectV2Item": map[string]any{"id": "item"}}}
		case strings.Contains(req.Query, "WorkerProjectFields"):
			data = map[string]any{"node": map[string]any{"fields": page([]any{map[string]any{"id": "status", "name": "Status", "options": []any{map[string]any{"id": "ready", "name": "Ready"}, map[string]any{"id": "progress", "name": "In Progress"}, map[string]any{"id": "done", "name": "Done"}}}}, 1)}}
		case strings.Contains(req.Query, "WorkerNativeTaskProjectItems"):
			items := []any{}
			if s.projectAdded {
				items = append(items, map[string]any{"id": "item", "project": map[string]any{"id": "project"}, "content": map[string]any{"__typename": "Issue", "url": taskURL}})
			}
			data = map[string]any{"repository": map[string]any{"issue": map[string]any{"projectItems": page(items, len(items))}}}
		case strings.Contains(req.Query, "WorkerProjectItems"):
			items := []any{}
			if s.projectAdded {
				items = append(items, map[string]any{"id": "item", "content": map[string]any{"__typename": "Issue", "url": taskURL}})
			}
			data = map[string]any{"node": map[string]any{"items": page(items, len(items))}}
		case strings.Contains(req.Query, "WorkerProjectValues"):
			status := "Todo"
			if s.projectReady {
				status = "Ready"
			}
			if s.projectReadLag > 0 && s.projectReady {
				status = "Todo"
				s.projectReadLag--
			}
			data = map[string]any{"node": map[string]any{"fieldValues": page([]any{map[string]any{"name": status, "field": map[string]any{"id": "status", "name": "Status"}}}, 1)}}
		case strings.Contains(req.Query, "WorkerProject"):
			data = map[string]any{"repositoryOwner": map[string]any{"projectV2": map[string]any{"id": "project"}}}
		case strings.Contains(req.Query, "WorkerIdentity"):
			data = map[string]any{"viewer": actor(s.cfg.Self.ID, s.cfg.Self.Login)}
		case strings.Contains(req.Query, "WorkerNativePR"), strings.Contains(req.Query, "WorkerPRVersion"):
			data = map[string]any{"repository": map[string]any{"pullRequest": pr}}
		case strings.Contains(req.Query, "WorkerTeamBaseScope"):
			data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"timelineItems": page([]any{}, 0)}}}
		case strings.Contains(req.Query, "WorkerTeamReviews"):
			nodes := s.reviews
			if nodes == nil {
				nodes = []map[string]any{}
			}
			data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviews": page(nodes, len(nodes))}}}
		case strings.Contains(req.Query, "WorkerTeamCommits"):
			data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"commits": page([]any{map[string]any{"commit": map[string]any{"id": "commit", "oid": s.head, "committer": map[string]any{"user": actor(12, "implementer[bot]")}}}}, 1)}}}
		case strings.Contains(req.Query, "WorkerTeamAuthors"):
			data = map[string]any{"node": map[string]any{"authors": page([]any{map[string]any{"user": actor(12, "implementer[bot]")}}, 1)}}
		default:
			s.t.Fatalf("unexpected query %s", req.Query)
		}
		// Stable native timestamps across all rereads.
		if repo, ok := data.(map[string]any)["repository"].(map[string]any); ok {
			if item, ok := repo["pullRequest"].(map[string]any); ok {
				if _, ok = item["createdAt"]; ok {
					item["createdAt"] = "2026-01-01T00:00:00Z"
					item["updatedAt"] = "2026-01-01T00:00:00Z"
				}
			}
		}
		return jsonResponse(map[string]any{"data": data}), nil
	}
	if endpoint == "repos/code/repo/issues" && method == "POST" {
		var req struct{ Title, Body string }
		_ = json.Unmarshal(body, &req)
		s.taskTitle, s.taskBody = req.Title, req.Body
		return jsonResponse(map[string]any{"html_url": taskURL, "number": 1}), nil
	}
	if endpoint == "repos/code/repo/issues/1/comments" && method == "POST" {
		var req struct{ Body string }
		_ = json.Unmarshal(body, &req)
		s.metadata = req.Body
		return jsonResponse(map[string]any{"id": 1}), nil
	}
	if endpoint == "repos/code/repo/issues/1" {
		if method == "PATCH" {
			s.closed = true
		}
		state := "open"
		reason := ""
		if s.closed {
			state, reason = "closed", "completed"
		}
		return jsonResponse(map[string]any{"html_url": taskURL, "node_id": "issue-node", "number": 1, "title": s.taskTitle, "body": s.taskBody, "state": state, "state_reason": reason}), nil
	}
	if strings.HasPrefix(endpoint, "repos/code/repo/issues/1/comments?") {
		return jsonResponse([]any{map[string]any{"id": 1, "body": s.metadata, "user": map[string]any{"id": 11, "login": "planner[bot]", "type": "Bot"}, "created_at": "2026-01-01T00:00:00Z"}}), nil
	}
	if endpoint == "repos/code/repo/pulls" && method == "POST" {
		var req struct{ Body string }
		_ = json.Unmarshal(body, &req)
		s.prBody = req.Body
		return jsonResponse(map[string]any{"html_url": prURL}), nil
	}
	if endpoint == "repos/code/repo/pulls/1/reviews" && method == "POST" {
		var req struct {
			Body, Event string
			CommitID    string `json:"commit_id"`
		}
		_ = json.Unmarshal(body, &req)
		state := "APPROVED"
		if req.Event == "REQUEST_CHANGES" {
			state = "CHANGES_REQUESTED"
		}
		s.reviews = append(s.reviews, map[string]any{"fullDatabaseId": len(s.reviews) + 1, "state": state, "body": req.Body, "submittedAt": time.Now().Add(-time.Millisecond).UTC(), "commit": map[string]any{"oid": req.CommitID}, "author": actor(s.cfg.Self.ID, s.cfg.Self.Login)})
		return jsonResponse(map[string]any{"id": len(s.reviews)}), nil
	}
	if endpoint == "repos/code/repo/pulls/1/merge" && method == "PUT" {
		s.merged = true
		if s.autoCloseIssue {
			s.closed = true
		}
		return jsonResponse(map[string]any{"merged": true}), nil
	}
	if endpoint == "repos/code/repo/pulls/1" {
		if method == "PATCH" {
			var req struct{ Body string }
			_ = json.Unmarshal(body, &req)
			s.prBody = req.Body
		}
		baseSHA := s.baseSHA
		if s.restBaseSHA != "" {
			baseSHA = s.restBaseSHA
		}
		return jsonResponse(map[string]any{"html_url": prURL, "body": s.prBody, "user": map[string]any{"id": 12, "login": "implementer[bot]", "type": "Bot"}, "head": map[string]any{"sha": s.head, "ref": s.branch, "repo": map[string]any{"full_name": "code/repo"}}, "base": map[string]any{"ref": "main", "sha": baseSHA, "repo": map[string]any{"full_name": "code/repo"}}}), nil
	}
	if endpoint == "user/12" {
		return jsonResponse(map[string]any{"id": 12, "login": "implementer[bot]", "type": "Bot"}), nil
	}
	if endpoint == "repos/code/repo/actions/artifacts/1" {
		return jsonResponse(map[string]any{"id": 1, "digest": "sha256:" + strings.Repeat("e", 64), "expired": false, "workflow_run": map[string]any{"id": 1, "head_sha": s.head}}), nil
	}
	if strings.HasPrefix(endpoint, "repos/code/repo/tarball/") {
		if s.failSourceAfterPush && s.localHead != "" && s.head == s.localHead {
			return response("503 Service Unavailable", "{}"), nil
		}
		raw := qaBundle(s.t, qaBundleEntry{name: "source/code.txt", content: s.head, mode: 0644})
		return CommandResult{Stdout: append([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\n\r\n"), raw...)}, nil
	}
	s.t.Fatalf("unexpected GitHub %s %s", method, endpoint)
	return CommandResult{}, nil
}

func TestNativeProductionDeliveryEndToEnd(t *testing.T) {
	cfg, m, _, _ := dispatchFixture(t)
	cfg.Projects = Defaults(Config{Projects: []ProjectRef{{Owner: "code", Number: 1}}}).Projects
	cfg.ProjectOwners = nil
	transport := &nativeDeliveryTransport{t: t, head: strings.Repeat("a", 40)}
	transport.commands = &fakeCommands{respond: transport.response}
	ctx := context.Background()
	g := NewGitHubClient(transport)
	runs := map[string]*NativeDispatcher{}
	models := map[string]*fakeOpenCode{}
	stateRoot, workRoot := cfg.StateDir, cfg.WorkRoot
	stage := func(index int, agent, kind string) NativeExecution {
		cfg.StateDir = filepath.Join(stateRoot, agent)
		cfg.WorkRoot = filepath.Join(workRoot, agent)
		saveNativeFixtureConnection(t, cfg)
		cfg.Name = cfg.Team.Members[index].Name
		cfg.Self = cfg.Team.Members[index].Actor
		cfg.Master.AgentID = agent
		m.self.Principal.ID = agent
		m.self.Principal.GitHubBotID = cfg.Self.ID
		m.self.Placement.AgentID = agent
		transport.cfg = cfg
		catalog, err := g.NativeSnapshot(ctx, cfg, m)
		if err != nil {
			t.Fatal(kind, err)
		}
		candidates := NativeCandidates(cfg, catalog, nil, time.Now())
		if len(candidates) != 1 || candidates[0].Job.Kind != kind {
			t.Fatalf("missing %s %+v", kind, candidates)
		}
		code := &fakeOpenCode{running: true}
		d, err := NewNativeDispatcher(cfg, m, g, code, NewCheckouts(cfg, transport), RealClock{})
		if err != nil {
			t.Fatal(err)
		}
		candidate := candidates[0]
		if kind != "plan" {
			selected, err := g.NativeTaskSnapshot(ctx, cfg, m, candidate.Job)
			if err != nil {
				t.Fatal("selected-task evidence", kind, err)
			}
			matched := false
			for _, v := range NativeCandidates(cfg, selected, nil, time.Now()) {
				if v.Job.ID == candidate.Job.ID {
					matched = true
				}
			}
			if !matched || selected.TaskScope != candidate.Job.TaskURL {
				t.Fatal("selected read changed stage/peer/QA eligibility", kind)
			}
		}
		x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: candidate.Goal.Revision.Ref, Job: candidate.Job, Phase: "selected"}
		if candidate.Task != nil {
			x.Binding = &candidate.Task.Binding
		}
		if kind != "plan" {
			transport.branch = "agent/" + cfg.Name + "/" + BindingKey(cfg, x.Job)[:16]
		}
		if _, err = d.advance(ctx, &x); err != nil {
			t.Fatal("dispatch", kind, err)
		}
		if code.calls != 1 {
			t.Fatal("no actual dispatch")
		}
		runs[x.Job.ID] = d
		models[x.Job.ID] = code
		return x
	}
	resumePlan := func(x *NativeExecution) {
		first := x.Job
		m.authority.State, m.authority.Valid = "paused", false
		m.authority.DecisionSequence++
		if _, err := runs[x.Job.ID].advance(ctx, x); err != nil || x.Reason != "goal_paused" {
			t.Fatal("pause planner", err, x.Reason)
		}
		m.authority.State, m.authority.Valid = "approved", true
		m.authority.DecisionSequence++
		if _, err := runs[x.Job.ID].advance(ctx, x); err != nil || x.Phase != "selected" {
			t.Fatal("resume registered planner", err, x.Phase)
		}
		if _, err := runs[x.Job.ID].advance(ctx, x); err != nil || x.Phase != "active" {
			t.Fatal("prepare continued planner", err, x.Phase)
		}
		if len(x.Turns) != 1 || x.Turns[0].MessageID != first.MessageID || x.Job.MessageID == first.MessageID || x.Job.SessionID != first.SessionID || models[x.Job.ID].calls != 2 {
			t.Fatal("planner continuation lost original evidence")
		}
	}
	plan := stage(0, "peer", "plan")
	if _, err := RunNativeAction(ctx, cfg, m, g, plan.Job.ID, NativeAction{Operation: "create-task", RequestID: "task", Repository: "code/repo", Title: "Task", Body: "scope", BaseBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	resumePlan(&plan)
	if _, err := RunNativeAction(ctx, cfg, m, g, plan.Job.ID, NativeAction{Operation: "complete-plan", RequestID: "planned"}); err != nil {
		t.Fatal(err)
	}
	// Native terminal observation must consume the helper's real result journal
	// before the next role is admitted in its own worker state directory.
	finish := func(x NativeExecution) {
		models[x.Job.ID].running = false
		done, err := runs[x.Job.ID].advance(ctx, &x)
		if err != nil || !done || x.Phase != "completed" || x.Job.SubmissionState != "completed" {
			t.Fatal("native terminal result was not confirmed", err)
		}
	}
	finish(plan)
	// Renewing approval of the same revision needs its own planner job and an
	// explicit helper rebind; the retained task must not poison discovery.
	sameRevision := m.authority.Revision.Ref
	m.authority.ApprovalSequence++
	renewed := stage(0, "peer", "plan")
	if renewed.Job.ID == plan.Job.ID {
		t.Fatal("renewed approval reused completed plan")
	}
	if _, err := RunNativeAction(ctx, cfg, m, g, renewed.Job.ID, NativeAction{Operation: "rebind-task", RequestID: "renewed-binding", TaskURL: "https://github.com/code/repo/issues/1", BaseBranch: "main"}); err != nil {
		t.Fatal("same revision rebind", err)
	}
	resumePlan(&renewed)
	if _, err := RunNativeAction(ctx, cfg, m, g, renewed.Job.ID, NativeAction{Operation: "complete-plan", RequestID: "renewed-plan"}); err != nil {
		t.Fatal(err)
	}
	finish(renewed)
	renewedBinding, err := ParseNativeTaskMetadata(transport.metadata)
	if err != nil || renewedBinding.Goal != sameRevision || renewedBinding.ApprovalSequence != m.authority.ApprovalSequence {
		t.Fatal("renewed assignment not retained", err)
	}
	old := m.authority.Revision.Ref
	m.authority.Revision.Ref.RevisionSHA256 = BodyHash("explicit replacement")
	m.authority.ActiveRevisionSHA256 = m.authority.Revision.Ref.RevisionSHA256
	m.authority.ApprovalSequence++
	replacement := stage(0, "peer", "plan")
	if _, err := RunNativeAction(ctx, cfg, m, g, replacement.Job.ID, NativeAction{Operation: "rebind-task", RequestID: "rebound", TaskURL: "https://github.com/code/repo/issues/1", BaseBranch: "main"}); err != nil {
		t.Fatal("explicit planner rebinding", err)
	}
	if _, err := RunNativeAction(ctx, cfg, m, g, replacement.Job.ID, NativeAction{Operation: "complete-plan", RequestID: "replanned"}); err != nil {
		t.Fatal(err)
	}
	finish(replacement)
	rebound, err := ParseNativeTaskMetadata(transport.metadata)
	if err != nil || rebound.Goal == old || rebound.ApprovalSequence != m.authority.ApprovalSequence {
		t.Fatal("old assignment was silently retained", err)
	}
	author := stage(1, "author", "implement")
	artifact := &QAArtifact{Kind: "workflow", HeadSHA: transport.head, SHA256: strings.Repeat("e", 64), URL: "https://api.github.com/repos/code/repo/actions/artifacts/1", Revision: "1"}
	if _, err := RunNativeAction(ctx, cfg, m, g, author.Job.ID, NativeAction{Operation: "publish-pr", RequestID: "pr", Title: "Implementation", Body: "Public change summary"}); err != nil {
		t.Fatal(err)
	}
	finish(author)
	peer := stage(0, "peer", "review")
	if _, err := RunNativeAction(ctx, cfg, m, g, peer.Job.ID, NativeAction{Operation: "review", RequestID: "peer-changes", Verdict: "changes-needed", Body: "Please address the exact-head finding."}); err != nil {
		t.Fatal(err)
	}
	finish(peer)
	fix := stage(1, "author", "fix")
	transport.localHead = strings.Repeat("b", 40)
	artifact.HeadSHA = transport.localHead
	if _, err := RunNativeAction(ctx, cfg, m, g, fix.Job.ID, NativeAction{Operation: "publish-pr", RequestID: "fix-pr", Title: "Fixed implementation", Body: "Public fix summary", Artifact: artifact}); err != nil {
		t.Fatal("fix publication", err)
	}
	transport.localHead = ""
	finish(fix)
	// A base update is ordinary author work even without a review request.
	// Publication must prove it actually includes the pinned base commit.
	transport.baseSHA, transport.mergeState = strings.Repeat("d", 40), "BEHIND"
	refresh := stage(1, "author", "fix")
	refreshAction := NativeAction{Operation: "publish-pr", RequestID: "refresh-pr", Title: "Updated implementation", Body: "Merge approved base without rewriting history"}
	writesBeforeRefresh := len(transport.writes)
	if _, err := RunNativeAction(ctx, cfg, m, g, refresh.Job.ID, refreshAction); err == nil || len(transport.writes) != writesBeforeRefresh {
		t.Fatal("unchanged branch was published as a successful refresh", err)
	}
	transport.localHead, transport.rejectAncestor = strings.Repeat("c", 40), true
	if _, err := RunNativeAction(ctx, cfg, m, g, refresh.Job.ID, refreshAction); err == nil || len(transport.writes) != writesBeforeRefresh {
		t.Fatal("refresh without the approved base was published", err)
	}
	transport.rejectAncestor = false
	transport.advanceBaseAfterAncestry = true
	if _, err := RunNativeAction(ctx, cfg, m, g, refresh.Job.ID, refreshAction); err == nil || len(transport.writes) != writesBeforeRefresh {
		t.Fatal("changed base after ancestry check still allowed publication", err)
	}
	transport.advanceBaseAfterAncestry, transport.baseSHA = false, strings.Repeat("d", 40)
	transport.failSourceAfterPush = true
	if _, err := RunNativeAction(ctx, cfg, m, g, refresh.Job.ID, refreshAction); err == nil {
		t.Fatal("source read failure after push was not retained")
	}
	models[refresh.Job.ID].running = false
	if _, err := runs[refresh.Job.ID].advance(ctx, &refresh); err != nil || refresh.Phase != "blocked" {
		t.Fatal("failed publication did not retain terminal checkpoint", err)
	}
	transport.failSourceAfterPush = false
	writesBeforeRecovery := len(transport.writes)
	if result, err := RecoverNativePublication(ctx, cfg, m, g, models[refresh.Job.ID], refresh.Job.ID, refresh.Job.SessionID, refresh.Job.MessageID, refreshAction); err != nil || result.Phase != "completed" {
		t.Fatal("acknowledged push publication recovery", result, err)
	}
	for _, write := range transport.writes[writesBeforeRecovery:] {
		if write == "push" {
			t.Fatal("publication recovery repeated acknowledged push")
		}
	}
	transport.localHead = ""
	recoveredRefresh, err := loadNativeExecution(cfg, refresh.Job.ID)
	if err != nil || recoveredRefresh.Phase != "completed" {
		t.Fatal("publication recovery did not persist completion", err)
	}
	// Failed CI directly hands the exact current head back to its author.
	transport.checks = "FAILURE"
	checksFix := stage(1, "author", "fix")
	checksAction := NativeAction{Operation: "publish-pr", RequestID: "checks-pr", Title: "Repair failed checks", Body: "Fix the exact-head lint failures"}
	writesBeforeChecks := len(transport.writes)
	if _, err := RunNativeAction(ctx, cfg, m, g, checksFix.Job.ID, checksAction); err == nil || len(transport.writes) != writesBeforeChecks {
		t.Fatal("unchanged failed head was published as a successful CI repair", err)
	}
	transport.localHead = strings.Repeat("f", 40)
	transport.checksAfterCheckout = "SUCCESS"
	if _, err := RunNativeAction(ctx, cfg, m, g, checksFix.Job.ID, checksAction); err == nil || len(transport.writes) != writesBeforeChecks {
		t.Fatal("CI recovered after eligibility read but an obsolete repair still pushed", err)
	}
	transport.checksAfterCheckout, transport.checks = "", "FAILURE"
	if _, err := RunNativeAction(ctx, cfg, m, g, checksFix.Job.ID, checksAction); err != nil {
		t.Fatal("checks repair publication", err)
	}
	transport.localHead, transport.checks = "", ""
	finish(checksFix)
	peer = stage(0, "peer", "review")
	if _, err := RunNativeAction(ctx, cfg, m, g, peer.Job.ID, NativeAction{Operation: "review", RequestID: "peer-review", Verdict: "approved", Body: "Reviewed the exact head."}); err != nil {
		t.Fatal(err)
	}
	finish(peer)
	qa := stage(2, "qa", "qa")
	private := "Private customer acceptance details must never appear in a PR."
	m.onSend = func() { transport.checks = "FAILURE" }
	if _, err := RunNativeAction(ctx, cfg, m, g, qa.Job.ID, NativeAction{Operation: "qa", RequestID: "qa-review", Verdict: "approved", ReportBody: private}); err == nil || len(transport.reviews) != 2 {
		t.Fatal("stale checks after private evidence publication cleared QA")
	}
	m.onSend = nil
	transport.checks = ""
	if _, err := RunNativeAction(ctx, cfg, m, g, qa.Job.ID, NativeAction{Operation: "qa", RequestID: "qa-review", Verdict: "approved", ReportBody: private}); err != nil {
		t.Fatal(err)
	}
	finish(qa)
	for _, review := range transport.reviews {
		if strings.Contains(review["body"].(string), private) {
			t.Fatal("private evidence leaked to public GitHub")
		}
	}
	delivery := stage(1, "author", "deliver")
	transport.autoCloseIssue = true
	// Reproduce a pre-effect helper failure followed by a terminal model turn.
	action := NativeAction{Operation: "deliver", RequestID: "delivery"}
	rawAction, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	record := nativeActionRecord{Version: 3, JobID: delivery.Job.ID, RequestID: action.RequestID, InputSHA: BodyHash(string(rawAction)), Operation: action.Operation, State: "prepared"}
	if err = saveNativeAction(cfg, record); err != nil {
		t.Fatal(err)
	}
	models[delivery.Job.ID].running = false
	delivery.Phase, delivery.Reason = "blocked", "native_completed_without_result"
	delivery.Job.Phase, delivery.Job.SubmissionState = "completed", "completed"
	delivery.Job.TerminalAt = time.Now().UTC()
	if err = saveNativeExecution(cfg, delivery); err != nil {
		t.Fatal(err)
	}
	if _, err = RunNativeAction(ctx, cfg, m, g, delivery.Job.ID, action); err == nil {
		t.Fatal("normal action bypassed terminal recovery hold")
	}

	if err = RetainNativeAction(cfg, delivery.Job.ID, action); err != nil {
		t.Fatal(err)
	}
	autoWait, err := NewNativeDispatcher(cfg, m, g, models[delivery.Job.ID], NewCheckouts(cfg, transport), RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	waitClock := &fakeClock{now: time.Now().UTC()}
	autoWait.clock = waitClock
	autoWait.nextRefresh = waitClock.Now().Add(24 * time.Hour)
	transport.checks = "PENDING"
	for range 4 {
		if _, err = autoWait.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err = consumeNativeStep(t, autoWait); err == nil {
			t.Fatal("pending CI did not park delivery")
		}
		saved, e := loadNativeExecution(cfg, delivery.Job.ID)
		if e != nil || saved.Job.ReconcileError != "native_prerequisite_wait" || autoWait.RecoveryBlocked() || transport.merged {
			t.Fatal("routine CI wait became an operator hold", saved.Job.ReconcileError, e)
		}
		waitClock.now = waitClock.now.Add(16 * time.Minute)
	}
	transport.checks = "FAILURE"
	if _, err = autoWait.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, autoWait); err != nil {
		t.Fatal(err)
	}
	retired, e := loadNativeExecution(cfg, delivery.Job.ID)
	if e != nil || retired.Phase != "cancelled" || autoWait.RecoveryBlocked() || transport.merged {
		t.Fatal("failed CI kept unstarted delivery reservation", e)
	}
	autoWait.catalog, err = g.NativeSnapshot(ctx, cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	fixCandidates, err := autoWait.candidates(autoWait.catalog)
	if err != nil || len(fixCandidates) != 1 || !nativeIsChecksRepair(fixCandidates[0].Job) {
		t.Fatal("delivery reservation blocked CI repair", err)
	}
	transport.checks = ""
	if err = saveNativeExecution(cfg, delivery); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"peer", "checks", "qa"} {
		fired := false
		m.onMessage = func() {
			if fired {
				return
			}
			fired = true
			switch failure {
			case "peer":
				transport.reviews[1]["state"] = "DISMISSED"
			case "checks":
				transport.checks = "FAILURE"
			case "qa":
				transport.reviews[2]["state"] = "DISMISSED"
			}
		}
		if _, err := RecoverNativeDelivery(ctx, cfg, m, g, models[delivery.Job.ID], delivery.Job.ID, delivery.Job.SessionID, delivery.Job.MessageID, action); err == nil || transport.merged {
			t.Fatalf("stale %s passed final merge effect gate", failure)
		}
		saved, loadErr := loadNativeExecution(cfg, delivery.Job.ID)
		if loadErr != nil || saved.Phase != "blocked" || models[delivery.Job.ID].calls != 1 {
			t.Fatal("refused recovery changed original run", loadErr)
		}
		m.onMessage = nil
		transport.checks = ""
		transport.reviews[1]["state"] = "APPROVED"
		transport.reviews[2]["state"] = "APPROVED"
	}
	if _, err := RecoverNativeDelivery(ctx, cfg, m, g, models[delivery.Job.ID], delivery.Job.ID, delivery.Job.SessionID, delivery.Job.MessageID, action); err != nil {
		t.Fatal(err)
	}
	savedDelivery, err := loadNativeExecution(cfg, delivery.Job.ID)
	if err != nil || savedDelivery.Phase != "completed" || savedDelivery.Reason != "" || savedDelivery.Job.SessionID != delivery.Job.SessionID || savedDelivery.Job.MessageID != delivery.Job.MessageID || models[delivery.Job.ID].calls != 1 {
		t.Fatal("recovery lost terminal native identity or replayed model", err)
	}
	checkpoint, err := readPrivate(nativeActionPath(cfg, delivery.Job.ID, "delivery"), 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	var known nativeActionRecord
	if json.Unmarshal(checkpoint, &known) != nil {
		t.Fatal("journal unreadable")
	}
	// A known merge may have succeeded before final local receipt publication.
	known.State, known.Step, known.Result = "prepared", "merge", NativeActionResult{}
	if err = saveNativeAction(cfg, known); err != nil {
		t.Fatal(err)
	}
	if err = saveNativeExecution(cfg, delivery); err != nil {
		t.Fatal(err)
	}
	knownWrites := len(transport.writes)
	for _, unavailable := range []string{"task-open", "head-changed"} {
		if unavailable == "task-open" {
			transport.closed = false
		} else {
			transport.head = BodyHash("different recovery PR head")[:40]
		}
		if _, e := RecoverNativeDelivery(ctx, cfg, m, g, models[delivery.Job.ID], delivery.Job.ID, delivery.Job.SessionID, delivery.Job.MessageID, action); e == nil || len(transport.writes) != knownWrites {
			t.Fatal("unconfirmed completion changed remote state", unavailable, e)
		}
		transport.closed, transport.head = true, delivery.Job.HeadSHA
	}
	if result, e := RecoverNativeDelivery(ctx, cfg, m, g, models[delivery.Job.ID], delivery.Job.ID, delivery.Job.SessionID, delivery.Job.MessageID, action); e != nil || result.Phase != "completed" || len(transport.writes) != knownWrites {
		t.Fatal("known merged/closed delivery was not confirmed read-only", e)
	}

	// The permanent worker confirms the same acknowledged merge without
	// replaying it or sending another model prompt.
	if err = saveNativeAction(cfg, known); err != nil {
		t.Fatal(err)
	}
	if err = saveNativeExecution(cfg, delivery); err != nil {
		t.Fatal(err)
	}
	automatic, err := NewNativeDispatcher(cfg, m, g, models[delivery.Job.ID], NewCheckouts(cfg, transport), RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	automatic.nextRefresh = time.Now().Add(time.Hour)
	for range 2 {
		if _, err = automatic.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err = consumeNativeStep(t, automatic); err != nil {
			t.Fatal(err)
		}
	}
	automaticResult, err := loadNativeExecution(cfg, delivery.Job.ID)
	if err != nil || automaticResult.Phase != "completed" || len(transport.writes) != knownWrites || models[delivery.Job.ID].calls != 1 {
		t.Fatal("automatic delivery confirmation replayed effects/model", err)
	}
	known.Step = "task-completed"
	known.State = "prepared"
	known.Result.Phase = ""
	if err = saveNativeAction(cfg, known); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash before the helper's final receipt and terminal checkpoint.
	interrupted := delivery
	interrupted.Phase, interrupted.Reason = "active", ""
	if err = saveNativeExecution(cfg, interrupted); err != nil {
		t.Fatal(err)
	}
	writesBefore := len(transport.writes)
	if result, err := RunNativeAction(ctx, cfg, m, g, delivery.Job.ID, NativeAction{Operation: "deliver", RequestID: "delivery"}); err != nil || result.Phase != "completed" || len(transport.writes) != writesBefore {
		t.Fatal("known task completion was not reconciled without writes", err)
	}
	delivery = interrupted
	finish(delivery)
	if !transport.merged || !transport.closed || len(m.links) != 3 || len(m.reports) != 1 {
		t.Fatalf("delivery incomplete: merged=%v completed=%v links=%d reports=%d", transport.merged, transport.closed, len(m.links), len(m.reports))
	}
	if err := os.RemoveAll(delivery.Job.Checkout); err != nil {
		t.Fatal(err)
	}
}
