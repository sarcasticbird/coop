package worker

import (
	"context"
	"encoding/json"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type reviewOfflineMaster struct{ MasterTransport }

func (m reviewOfflineMaster) Pending(context.Context, c.PendingQuery) (c.DeliveryPage, error) {
	return c.DeliveryPage{}, c.ErrUnavailable
}

func TestCommitReviewRevokedReceivedDelivery(t *testing.T) {
	_, m, s, o, b, th, _ := commsFixture(t)
	ctx := context.Background()
	if _, e := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "revoked", RequestID: "revoked"}); e != nil {
		t.Fatal(e)
	}
	m.Busy = true
	if e := m.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SetMembers(ctx, o, c.SetMembers{ThreadID: th.ID, ExpectedMembers: th.Members, Members: []string{o.ID}, RequestID: "revoke"}); e != nil {
		t.Fatal(e)
	}
	m.Busy = false
	if turn, e := m.Next(ctx); e != nil || turn != nil {
		t.Fatalf("revoked delivery stranded inbox: %+v %v", turn, e)
	}
	next, e := s.CreateThread(ctx, o, c.CreateThread{Title: "next", Engagement: "coop", Members: []string{o.ID, b.ID}, RequestID: "next"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Send(ctx, o, c.SendMessage{ThreadID: next.ID, Recipients: []string{b.ID}, Body: "later", RequestID: "later"}); e != nil {
		t.Fatal(e)
	}
	if e = m.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	turn, e := m.Next(ctx)
	if e != nil || turn == nil || turn.Message.Body != "later" {
		t.Fatalf("later conversation blocked: %+v %v", turn, e)
	}
}
func TestCommitReviewConversationOutageLocalGuards(t *testing.T) {
	for _, pause := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "pause"}[pause], func(t *testing.T) {
			cfg, m, s, o, b, th, code := commsFixture(t)
			ctx := context.Background()
			if _, e := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "start", RequestID: "outage"}); e != nil {
				t.Fatal(e)
			}
			store := NewVersionedFileStore(cfg.StateDir, 3)
			e := &Engine{Config: cfg, Store: store, OpenCode: code, MasterRuntime: &MasterRuntime{Comms: m}}
			if err := e.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			turn, err := m.Active()
			if err != nil || turn == nil {
				t.Fatal(err)
			}
			if pause {
				if err = store.SetPaused(true); err != nil {
					t.Fatal(err)
				}
			} else {
				turn.Job.Deadline = time.Now().Add(-time.Hour)
				if err = saveMasterTurn(cfg, turn); err != nil {
					t.Fatal(err)
				}
			}
			m.Client = reviewOfflineMaster{m.Client}
			if err = e.Tick(ctx); err == nil {
				t.Fatal("outage hidden")
			}
			if code.aborts != 1 {
				t.Fatal("outage skipped local guard", code.aborts)
			}
		})
	}
}
func TestCommitReviewReplyProducerFence(t *testing.T) {
	cfg, m, s, o, b, th, _ := commsFixture(t)
	ctx := context.Background()
	if _, e := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "reply", RequestID: "lock"}); e != nil {
		t.Fatal(e)
	}
	if e := m.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	turn, e := m.Next(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Advance(ctx, turn); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(masterDir(cfg), "actions")
	if e = ensurePrivateDir(dir, true); e != nil {
		t.Fatal(e)
	}
	lock, e := os.OpenFile(filepath.Join(dir, "producer.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = lock.Close() }()
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		t.Fatal(e)
	}
	if e = QueueMasterReply(cfg, turn.Delivery.ID, "answer"); e == nil {
		t.Fatal("reply escaped recovery producer fence")
	}
	if e = m.FlushOutbox(ctx); e == nil {
		t.Fatal("receipt publication escaped recovery producer fence")
	}
}
func TestCommitReviewRenewedApprovalPlansAgain(t *testing.T) {
	cfg, m, g, _ := dispatchFixture(t)
	cfg.Name = cfg.Team.Planner
	cfg.Self = teamPlanner(cfg)
	cfg.Master.AgentID = "peer"
	old := g.catalog
	old.Tasks = nil
	first := NativeCandidates(cfg, old, nil, time.Now())
	if len(first) != 1 {
		t.Fatal("no initial planner")
	}
	history := map[string]NativeExecution{first[0].Job.ID: {Phase: "completed", Job: first[0].Job}}
	g.catalog.Goals[0].ApprovalSequence++
	m.authority = g.catalog.Goals[0]
	jobs := NativeCandidates(cfg, g.catalog, history, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != "plan" || jobs[0].Job.ID == first[0].Job.ID {
		t.Fatalf("renewed approval has no explicit rebind work: %+v", jobs)
	}
}
func TestCommitReviewOversizePromptReleasesSlot(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	g.catalog.Goals[0].Revision.Proposal.Scope = strings.Repeat("g", 20<<10)
	m.authority = g.catalog.Goals[0]
	g.catalog.Tasks[0].Task.Body = strings.Repeat("t", 20<<10)
	d, e := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if e != nil {
		t.Fatal(e)
	}
	v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
	_, e = d.advance(context.Background(), &x)
	if e != nil {
		t.Fatal(e)
	}
	if x.Phase != "cancelled" || !strings.Contains(x.Reason, "prompt_size") || code.calls != 0 {
		t.Fatalf("oversize work still active: %s %s %v", x.Phase, x.Reason, e)
	}
	restarted, e := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if e != nil || restarted.HasActive() || restarted.RecoveryBlocked() {
		t.Fatal("oversize record retained slot after restart", e)
	}
}

func TestCommitReviewPausedEngineeringContinuation(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	ctx := context.Background()
	d, e := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if e != nil {
		t.Fatal(e)
	}
	v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
	if _, e = d.advance(ctx, &x); e != nil {
		t.Fatal(e)
	}
	first := x.Job
	m.authority.State = "paused"
	m.authority.Valid = false
	m.authority.DecisionSequence = 2
	if _, e = d.advance(ctx, &x); e != nil || x.Reason != "goal_paused" {
		t.Fatal(e, x)
	}
	m.authority.State = "approved"
	m.authority.Valid = true
	m.authority.DecisionSequence = 3
	g.catalog.Goals[0] = m.authority
	// An uncertain effect remains a hard hold even after the goal resumes.
	uncertain := nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "uncertain", State: "uncertain"}
	if e = saveNativeAction(cfg, uncertain); e != nil {
		t.Fatal(e)
	}
	_, e = d.advance(ctx, &x)
	if x.Phase != "blocked" {
		t.Fatal("resumed through uncertain native action", e)
	}
	uncertain.State = "completed"
	if e = saveNativeAction(cfg, uncertain); e != nil {
		t.Fatal(e)
	}
	restarted, e := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if e != nil {
		t.Fatal(e)
	}
	// Exercise the runtime hold/refresh route, not only advance directly.
	commCfg, comms, _, _, _, _, _ := commsFixture(t)
	engine := &Engine{Config: commCfg, Store: NewVersionedFileStore(commCfg.StateDir, 3), MasterRuntime: &MasterRuntime{Comms: comms, Engineering: restarted}}
	restarted.catalog = g.catalog
	restarted.nextRefresh = time.Now().Add(time.Hour)
	for i := 0; i < 100; i++ {
		if e = engine.Tick(ctx); e != nil {
			t.Fatal(e)
		}
		if restarted.step == nil && restarted.active != nil && restarted.active.Phase == "active" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if restarted.step != nil || restarted.active == nil || restarted.active.Phase != "active" {
		t.Fatal("resume never admitted continuation")
	}
	saved, e := loadNativeExecution(cfg, x.Job.ID)
	if e != nil || len(saved.Turns) != 1 || saved.Turns[0].MessageID != first.MessageID || saved.Job.MessageID == first.MessageID || saved.Job.SessionID != first.SessionID || code.calls != 2 {
		t.Fatalf("continuation lost identity/evidence: %+v calls=%d %v", saved, code.calls, e)
	}
}

func TestCommitReviewNativeScopeHistoryChanges(t *testing.T) {
	for _, scenario := range []string{"stable", "body_ABA", "base_ABA"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, catalog, task := nativeWorkFixture(t)
			authority := catalog.Goals[0]
			authority.Revision.Proposal.QA.Required = false
			body := NativePRBody(NativePRMetadata{Version: 3, TaskURL: task.Task.URL, Goal: task.Binding.Goal, AssignmentSHA: NativeAssignmentSHA(task.Binding)})
			transport := &nativeDeliveryTransport{t: t, cfg: cfg, prBody: body, head: strings.Repeat("a", 40), branch: "branch"}
			mutated := false
			edited := "2026-01-01T01:00:00Z"
			commands := &fakeCommands{respond: func(owner, endpoint, method string, input []byte) (CommandResult, error) {
				var request struct{ Query string }
				if endpoint == "graphql" {
					if e := json.Unmarshal(input, &request); e != nil {
						t.Fatal(e)
					}
				}
				query := request.Query
				page := func(key string, nodes []any) CommandResult {
					return jsonResponse(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{key: map[string]any{"nodes": nodes, "totalCount": len(nodes), "pageInfo": map[string]any{"hasNextPage": false}}}}}})
				}
				if strings.Contains(query, "WorkerScopeHistory") {
					nodes := []any{map[string]any{"id": "original", "editedAt": edited, "diff": body}}
					if mutated && scenario == "body_ABA" {
						nodes = append([]any{map[string]any{"id": "restored", "editedAt": edited, "diff": body}, map[string]any{"id": "foreign", "editedAt": edited, "diff": "changed scope"}}, nodes...)
					}
					return page("userContentEdits", nodes), nil
				}
				if strings.Contains(query, "WorkerTeamBaseScope") {
					nodes := []any{}
					if mutated && scenario == "base_ABA" {
						nodes = []any{map[string]any{"__typename": "BaseRefChangedEvent", "id": "out", "createdAt": edited, "previousRefName": "main", "currentRefName": "other"}, map[string]any{"__typename": "BaseRefChangedEvent", "id": "back", "createdAt": edited, "previousRefName": "other", "currentRefName": "main"}}
					}
					return page("timelineItems", nodes), nil
				}
				if strings.Contains(query, "WorkerTeamReviews") {
					mutated = true
				}
				result, e := transport.response(owner, endpoint, method, input)
				if e == nil && (strings.Contains(query, "WorkerNativePR") || strings.Contains(query, "WorkerPRVersion")) {
					var raw map[string]any
					if e = json.Unmarshal([]byte(strings.SplitN(string(result.Stdout), "\r\n\r\n", 2)[1]), &raw); e != nil {
						t.Fatal(e)
					}
					raw["data"].(map[string]any)["repository"].(map[string]any)["pullRequest"].(map[string]any)["lastEditedAt"] = edited
					result = jsonResponse(raw)
				}
				return result, e
			}}
			_, e := NewGitHubClient(commands).ReadNativePR(context.Background(), cfg, task, authority, "https://github.com/code/repo/pull/1")
			if scenario == "stable" {
				if e != nil {
					t.Fatal(e)
				}
			} else if e == nil || !strings.Contains(e.Error(), "PR_scope_history_changed_during_read") {
				t.Fatalf("mixed scope accepted: %v", e)
			}
		})
	}
}

func TestCommitReviewNativeReviewBoundary(t *testing.T) {
	_, catalog, task := nativeWorkFixture(t)
	a := catalog.Goals[0]
	at := time.Now().UTC().Truncate(time.Second)
	p := PR{HeadSHA: strings.Repeat("a", 40), ScopeRevision: BodyHash("scope"), ScopeUpdatedAt: at, ReviewsComplete: true, ContributorsComplete: true, QAArtifact: &QAArtifact{SHA256: strings.Repeat("e", 64)}}
	peer := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
	qa := NativeQAResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, ArtifactSHA: p.QAArtifact.SHA256, AcceptanceSHA: NativeAcceptanceSHA(a), Environment: a.Revision.Proposal.QA.Environment, EnvironmentRevision: a.Revision.Proposal.QA.EnvironmentRevision, EvidenceMessageID: "report", EvidenceBodySHA: BodyHash("report"), Verdict: "approved"}
	reports := map[string]c.Message{"report": {ID: "report", ThreadID: a.Revision.Proposal.QA.EvidenceDestination.ThreadID, AuthorID: a.Actors.QA.PrincipalID, AuthorKind: "agent", Body: "report"}}
	for _, offset := range []time.Duration{-time.Second, 0, time.Second} {
		p.Reviews = []Review{{ID: "peer", HeadSHA: p.HeadSHA, Author: Actor{ID: task.Binding.PeerID, Kind: "Bot"}, State: "APPROVED", Body: NativePeerMetadata(peer), SubmittedAt: at.Add(offset)}, {ID: "qa", HeadSHA: p.HeadSHA, Author: Actor{ID: task.Binding.QAID, Kind: "Bot"}, State: "APPROVED", Body: NativeQAMetadata(qa), SubmittedAt: at.Add(offset)}}
		if _, ok := nativePeerCleared(task.Binding, p); ok != (offset > 0) {
			t.Fatalf("peer boundary offset %v accepted=%v", offset, ok)
		}
		if _, ok := nativeQAState(task.Binding, a, p, reports); ok != (offset > 0) {
			t.Fatalf("QA boundary offset %v accepted=%v", offset, ok)
		}
	}
}

type plannerChangingCheckouts struct {
	Checkouts
	change func()
}

func (p plannerChangingCheckouts) Prepare(ctx context.Context, j Job) (Job, error) {
	j, e := p.Checkouts.Prepare(ctx, j)
	if e == nil {
		p.change()
	}
	return j, e
}

func TestNativePlannerContinuationRejectsRenewedApproval(t *testing.T) {
	for _, stage := range []string{"before_resume", "before_preparation", "during_preparation"} {
		t.Run(stage, func(t *testing.T) {
			cfg, m, g, code := dispatchFixture(t)
			cfg.Name = cfg.Team.Planner
			cfg.Self = teamPlanner(cfg)
			cfg.Master.AgentID = "peer"
			m.self.Principal.ID = "peer"
			m.self.Principal.GitHubBotID = cfg.Self.ID
			m.self.Placement.AgentID = "peer"
			tasks := g.catalog.Tasks
			g.catalog.Tasks = nil
			d, e := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
			if e != nil {
				t.Fatal(e)
			}
			v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
			x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: v.Goal.Revision.Ref, Job: v.Job, Phase: "selected"}
			ctx := context.Background()
			if _, e = d.advance(ctx, &x); e != nil {
				t.Fatal(e)
			}
			g.catalog.Tasks = tasks // Already registered: discovery no longer emits a plan.
			if e = ensurePrivateDir(filepath.Join(masterDir(cfg), "actions"), true); e != nil {
				t.Fatal(e)
			}
			m.authority.State, m.authority.Valid = "paused", false
			m.authority.DecisionSequence++
			if _, e = d.advance(ctx, &x); e != nil || x.Reason != "goal_paused" {
				t.Fatal(e, x.Reason)
			}
			m.authority.State, m.authority.Valid = "approved", true
			m.authority.DecisionSequence++
			g.catalog.Goals[0] = m.authority
			renew := func() { m.authority.ApprovalSequence++ } // Keep catalog stale to exercise the fresh authority fence.
			if stage == "before_resume" {
				renew()
			} else {
				if _, e = d.advance(ctx, &x); e != nil || x.Phase != "selected" {
					t.Fatal("continuation setup", e, x.Phase)
				}
				if stage == "before_preparation" {
					renew()
				} else {
					d.checkouts = plannerChangingCheckouts{Checkouts: d.checkouts, change: renew}
				}
			}
			if _, e = d.advance(ctx, &x); e == nil {
				t.Fatal("saved planner crossed renewed approval")
			}
			if code.calls != 1 || x.Phase == "active" {
				t.Fatal("stale planner submitted", code.calls, x.Phase)
			}
		})
	}
}
