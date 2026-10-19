package worker

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func nativeTerminalWaitFixture(t *testing.T) (Config, *dispatchMaster, *dispatchCatalog, *fakeOpenCode, NativeExecution) {
	t.Helper()
	cfg, m, g, code := dispatchFixture(t)
	cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Planner, teamPlanner(cfg), "peer"
	m.self.Principal.ID, m.self.Principal.GitHubBotID, m.self.Placement.AgentID = "peer", cfg.Self.ID, "peer"
	planCatalog := g.catalog
	planCatalog.Tasks = nil
	v := NativeCandidates(cfg, planCatalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Phase: "blocked", Reason: "native_completed_without_result"}
	x.Job.SessionID, x.Job.MessageID, x.Job.Checkout = "ses_wait", "msg_wait", cfg.WorkRoot
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = time.Now()
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	code.running = false
	return cfg, m, g, code, x
}

func TestNativeTerminalWaitAllowsIndependentReview(t *testing.T) {
	cfg, m, g, code, waiting := nativeTerminalWaitFixture(t)
	g.catalog.Tasks[0].PRs = []PR{nativeReviewPRFixture(cfg, g.catalog.Tasks[0])}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	if d.RecoveryBlocked() {
		t.Fatal("verified terminal task wait froze the whole engineer")
	}
	d.catalog, d.nextRefresh = g.catalog, time.Now().Add(time.Hour)
	if _, err = d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, d); err != nil || code.calls != 1 || code.last.Kind != "review" {
		t.Fatal("independent review not admitted", err)
	}
	saved, err := loadNativeExecution(cfg, waiting.Job.ID)
	if err != nil || saved.Phase != "blocked" || saved.Reason != waiting.Reason || saved.Job.MessageID != waiting.Job.MessageID {
		t.Fatal("waiting task was discarded or replayed", err)
	}
	restarted, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil || restarted.RecoveryBlocked() {
		t.Fatal("terminal task reblocked after restart", err)
	}
}

func TestNativeTerminalWaitAllowsConversation(t *testing.T) {
	cfg, m, g, code, _ := nativeTerminalWaitFixture(t)
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	d.catalog = NativeCatalog{Complete: true}
	d.nextRefresh = time.Now().Add(time.Hour)
	cc, comms, s, owner, bot, thread, conversationCode := commsFixture(t)
	receipt, err := s.Send(t.Context(), owner, c.SendMessage{ThreadID: thread.ID, Recipients: []string{bot.ID}, Body: "Please review the published backend.", RequestID: "terminal-wait-comms"})
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Config: cc, Store: NewVersionedFileStore(cc.StateDir, 3), OpenCode: conversationCode, MasterRuntime: &MasterRuntime{Comms: comms, Engineering: d}}
	if err = e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	active, err := comms.Active()
	if err != nil || active == nil || active.Delivery.MessageID != receipt.MessageID || conversationCode.calls != 1 {
		t.Fatal("terminal wait starved queued conversation", err)
	}
}

func TestNativeUnsafeTerminalWaitRetainsGlobalHold(t *testing.T) {
	for _, failure := range []string{"unknown-submission", "missing-terminal", "prepared-action", "uncertain-action", "producer-busy", "other-failure"} {
		t.Run(failure, func(t *testing.T) {
			cfg, m, g, code, x := nativeTerminalWaitFixture(t)
			switch failure {
			case "unknown-submission":
				x.Job.SubmissionState = "unknown"
			case "missing-terminal":
				x.Job.TerminalAt = time.Time{}
			case "prepared-action", "uncertain-action":
				state := "prepared"
				if failure == "uncertain-action" {
					state = "uncertain"
				}
				if err := saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "pending", State: state}); err != nil {
					t.Fatal(err)
				}
			case "producer-busy":
				release, err := lockNativeProducer(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "other-failure":
				x.Reason = "native_observation_unavailable"
			}
			if err := saveNativeExecution(cfg, x); err != nil {
				t.Fatal(err)
			}
			d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
			if err != nil || !d.RecoveryBlocked() {
				t.Fatal("unsafe work lost global recovery hold", err)
			}
		})
	}
}

func TestNativeTerminalTaskContinuesOnlyByExplicitRecovery(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	x.Reason = "native_completed_without_result"
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = clock.Now()
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	resumed, err := ContinueNativeEngineering(t.Context(), cfg, m, g, code, x.Job.ID, x.Job.SessionID, x.Job.MessageID)
	if err != nil || resumed.Phase != "selected" || len(resumed.Turns) != 1 || resumed.Job.SessionID != x.Job.SessionID || resumed.Turns[0].MessageID != x.Job.MessageID || code.calls != 1 {
		t.Fatal("checked continuation lost or replayed saved task", err)
	}
}

func TestNativeTerminalWaitRechecksBusyProducer(t *testing.T) {
	cfg, m, g, code, _ := nativeTerminalWaitFixture(t)
	release, err := lockNativeProducer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	release()
	if err != nil || !d.RecoveryBlocked() {
		t.Fatal("live producer was not held", err)
	}
	d.catalog, d.nextRefresh = g.catalog, time.Now().Add(time.Hour)
	_ = d.Ready()
	if d.RecoveryBlocked() {
		t.Fatal("finished producer left a permanent worker hold")
	}
}

func TestNativeExplicitContinuationRejectsUnsafeRecovery(t *testing.T) {
	for _, failure := range []string{"foreign-message", "running", "uncertain-action", "changed-authority", "attempt-limit", "occupied-engineering", "blocked-conversation"} {
		t.Run(failure, func(t *testing.T) {
			cfg, m, g, code, clock, x := timedOutNativeFixture(t)
			x.Reason = "native_completed_without_result"
			x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
			x.Job.TerminalAt = clock.Now()
			message := x.Job.MessageID
			switch failure {
			case "foreign-message":
				message = "msg_foreign"
			case "running":
				code.running = true
			case "uncertain-action":
				if err := saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "unknown", State: "uncertain"}); err != nil {
					t.Fatal(err)
				}
			case "changed-authority":
				m.offline = true
			case "attempt-limit":
				x.Turns = make([]Job, Defaults(cfg).Limits.MaxAttempts)
			case "blocked-conversation":
				turn := MasterTurn{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, RestoreEpoch: 1, Delivery: c.Delivery{ID: "delivery-wait", MessageID: "message-wait"}, Message: c.Message{ID: "message-wait", Body: "review"}, BodySHA: BodyHash("review"), Phase: "blocked", Reason: "native_observation_unavailable"}
				if err := saveMasterTurn(cfg, &turn); err != nil {
					t.Fatal(err)
				}
			case "occupied-engineering":
				other := x
				other.Job.ID = BodyHash("other-live-job")
				other.Phase = "active"
				other.Reason = ""
				other.Job.SubmissionState = "unknown"
				if err := saveNativeExecution(cfg, other); err != nil {
					t.Fatal(err)
				}
			}
			if err := saveNativeExecution(cfg, x); err != nil {
				t.Fatal(err)
			}
			resumed, err := ContinueNativeEngineering(t.Context(), cfg, m, g, code, x.Job.ID, x.Job.SessionID, message)
			if err == nil || resumed.Phase == "selected" || code.calls != 1 {
				t.Fatal("unsafe continuation admitted", err)
			}
		})
	}
}

func TestNativeHoldReadFailureNeverReplaysAcceptedStep(t *testing.T) {
	cfg, m, g, code, _ := nativeTerminalWaitFixture(t)
	g.catalog.Tasks[0].PRs = []PR{nativeReviewPRFixture(cfg, g.catalog.Tasks[0])}
	code.effect = func(Config, Job) { code.running = true }
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	d.catalog, d.nextRefresh = g.catalog, time.Now().Add(time.Hour)
	if _, err = d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	var result nativeStep
	select {
	case result = <-d.step:
	case <-time.After(5 * time.Second):
		t.Fatal("step did not finish")
	}
	if result.err != nil || result.execution.Phase != "active" || code.calls != 1 {
		t.Fatal("expected an accepted step", result.err)
	}
	bad := filepath.Join(masterDir(cfg), "actions", "invalid.json")
	if err = atomicPrivate(bad, []byte("invalid journal")); err != nil {
		t.Fatal(err)
	}
	d.step <- result
	if _, err = d.Tick(t.Context()); err == nil {
		t.Fatal("hold scan error was hidden")
	}
	if d.active == nil || d.active.Phase != "active" || d.active.Job.MessageID != result.execution.Job.MessageID || !d.RecoveryBlocked() {
		t.Fatal("scan error left a stale active snapshot")
	}
	if err = os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, d); err != nil || code.calls != 1 {
		t.Fatal("accepted step replayed after recovering scan error", err)
	}
}

func TestNativeTerminalPlannerContinuesAfterPartialRegistration(t *testing.T) {
	cfg, m, g, code, x := nativeTerminalWaitFixture(t)
	if err := saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "registered-one", Operation: "create-task", State: "completed"}); err != nil {
		t.Fatal(err)
	}
	resumed, err := ContinueNativeEngineering(t.Context(), cfg, m, g, code, x.Job.ID, x.Job.SessionID, x.Job.MessageID)
	if err != nil || resumed.Phase != "selected" || len(resumed.Turns) != 1 || resumed.Turns[0].MessageID != x.Job.MessageID || code.calls != 0 {
		t.Fatal("partially registered plan could not continue safely", err)
	}
}

func TestNativeTerminalWaitReservesItsCheckoutAcrossPRHeads(t *testing.T) {
	cfg, m, g, code, _ := nativeTerminalWaitFixture(t)
	task := &g.catalog.Tasks[0]
	task.PRs = []PR{nativeReviewPRFixture(cfg, *task)}
	v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Binding: &v.Task.Binding, Job: v.Job, Phase: "blocked", Reason: "native_completed_without_result"}
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.SessionID, x.Job.MessageID = "ses_report", "msg_report"
	x.Job.TerminalAt = time.Now()
	x.Job.Checkout = cfg.WorkRoot
	report := filepath.Join(cfg.WorkRoot, "unfinished-review.txt")
	if err := atomicPrivate(report, []byte("retained review evidence")); err != nil {
		t.Fatal(err)
	}
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	task.PRs[0].HeadSHA = BodyHash("new-head")[:40]
	other := *task
	other.Task.URL = "https://github.com/code/repo/issues/2"
	other.Binding.TaskURL = other.Task.URL
	other.PRs = []PR{nativeReviewPRFixture(cfg, other)}
	other.PRs[0].URL = "https://github.com/code/repo/pull/2"
	g.catalog.Tasks = append(g.catalog.Tasks, other)
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := d.candidates(g.catalog)
	if err != nil || len(candidates) != 1 || candidates[0].Job.TaskURL != other.Task.URL {
		t.Fatal("held checkout reused by another PR head", len(candidates), err)
	}
	raw, err := os.ReadFile(report)
	if err != nil || string(raw) != "retained review evidence" || code.calls != 0 {
		t.Fatal("retained report changed", err)
	}
}
