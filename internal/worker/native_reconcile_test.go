package worker

import (
	"context"
	"encoding/json"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type staleRecoveryCatalog struct {
	*dispatchCatalog
	failure error
}

func (g staleRecoveryCatalog) NativeTaskSnapshot(context.Context, Config, NativeMaster, Job, ...string) (NativeCatalog, error) {
	return NativeCatalog{}, g.failure
}

func TestNativeDeliveryRecoveryCancelsReboundUnstartedAction(t *testing.T) {
	for _, test := range []struct {
		name, step string
		failure    error
		cancel     bool
	}{
		{"rebound", "", errNativeCandidateStale, true},
		{"transport", "", context.DeadlineExceeded, false},
		{"started", "merge", errNativeCandidateStale, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, m, cat, _ := dispatchFixture(t)
			v := NativeCandidates(cfg, cat.catalog, nil, time.Now())[0]
			x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: v.Goal.Revision.Ref, Binding: &v.Task.Binding, Job: v.Job, Phase: "blocked"}
			x.Job.Kind = "deliver"
			if err := saveNativeExecution(cfg, x); err != nil {
				t.Fatal(err)
			}
			d := &NativeDispatcher{config: cfg, master: m, github: staleRecoveryCatalog{cat, test.failure}, clock: RealClock{}}
			done, err := d.reconcileDeliveryReadiness(t.Context(), &x, []nativeActionRecord{{State: "prepared", Operation: "deliver", Step: test.step}})
			if test.cancel {
				saved, readErr := loadNativeExecution(cfg, x.Job.ID)
				if err != nil || !done || readErr != nil || saved.Phase != "cancelled" || saved.Reason != "native_delivery_superseded" {
					t.Fatal("superseded delivery retained its reservation", done, err, readErr, saved.Phase)
				}
			} else if done || x.Phase != "blocked" || test.step == "" && !errors.Is(err, test.failure) {
				t.Fatal("unsafe cancellation", done, err, x.Phase)
			}
		})
	}
}

func TestNativeWorkerAutomaticallyRecoversAcknowledgedTaskRegistration(t *testing.T) {
	cfg, m, _, code := dispatchFixture(t)
	cfg.Projects = Defaults(Config{Projects: []ProjectRef{{Owner: "code", Number: 1}}}).Projects
	cfg.ProjectOwners = nil
	cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Planner, teamPlanner(cfg), "peer"
	m.self.Principal.ID, m.self.Principal.GitHubBotID, m.self.Placement.AgentID = "peer", cfg.Self.ID, "peer"
	transport := &nativeDeliveryTransport{t: t, cfg: cfg, head: strings.Repeat("a", 40), projectReadLag: 1}
	transport.commands = &fakeCommands{respond: transport.response}
	api := NewGitHubClient(transport)
	catalog, err := api.NativeSnapshot(t.Context(), cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	jobs := NativeCandidates(cfg, catalog, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != "plan" {
		t.Fatal("missing planner")
	}
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: jobs[0].Goal.Revision.Ref, Job: jobs[0].Job, Phase: "selected"}
	d, err := NewNativeDispatcher(cfg, m, api, code, NewCheckouts(cfg, transport), RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	in := NativeAction{Operation: "create-task", RequestID: "original-create", Repository: "code/repo", Title: "Task", Body: "approved scope", BaseBranch: "main"}
	if _, err = RunNativeAction(t.Context(), cfg, m, api, x.Job.ID, in); err == nil {
		t.Fatal("fixture did not reproduce lagging Ready confirmation")
	}
	code.running = false
	if _, err = d.advance(t.Context(), &x); err != nil || x.Phase != "blocked" {
		t.Fatal("missing retained terminal hold", err)
	}
	restarted, err := NewNativeDispatcher(cfg, m, api, code, NewCheckouts(cfg, transport), RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	restarted.nextRefresh = time.Now().Add(time.Hour)
	if !restarted.Ready() {
		t.Fatal("worker never admits its known-safe recovery")
	}
	if _, err = restarted.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, restarted); err != nil {
		t.Fatal("automatic registration recovery failed", err)
	}
	saved, err := loadNativeExecution(cfg, x.Job.ID)
	if err != nil || saved.Job.SessionID != x.Job.SessionID || saved.Job.MessageID != x.Job.MessageID || code.calls != 1 {
		t.Fatal("recovery replayed a model or changed native identity", err)
	}
	record, err := loadNativeJobActions(cfg, saved)
	if err != nil || len(record) != 1 || record[0].State != "completed" {
		t.Fatal("original journal not completed", err)
	}
	creates := 0
	for _, w := range transport.writes {
		if w == "POST repos/code/repo/issues" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatal("registration recovery duplicated the issue", creates)
	}
	// The next recovery tick checkpoints a continuation; it cannot resubmit old messages.
	if _, err = restarted.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, restarted); err != nil {
		t.Fatal(err)
	}
	saved, err = loadNativeExecution(cfg, x.Job.ID)
	if err != nil || saved.Phase != "selected" || len(saved.Turns) != 1 || saved.Turns[0].MessageID != x.Job.MessageID || saved.Job.SessionID != x.Job.SessionID || code.calls != 1 {
		t.Fatal("planner did not continue its original session", err)
	}
}

func automaticRecoveryFixture(t *testing.T) (Config, *dispatchMaster, *GitHubClient, *fakeOpenCode, *NativeDispatcher, *nativeDeliveryTransport, NativeExecution, NativeAction, nativeActionRecord) {
	t.Helper()
	cfg, m, _, code, x := nativeTerminalWaitFixture(t)
	cfg.Projects = Defaults(Config{Projects: []ProjectRef{{Owner: "code", Number: 1}}}).Projects
	cfg.ProjectOwners = nil
	transport := &nativeDeliveryTransport{t: t, cfg: cfg, head: strings.Repeat("a", 40)}
	transport.commands = &fakeCommands{respond: transport.response}
	api := NewGitHubClient(transport)
	in := NativeAction{Operation: "create-task", RequestID: "saved", Repository: "code/repo", Title: "Task", Body: "approved scope", BaseBranch: "main"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	r := nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: in.RequestID, InputSHA: BodyHash(string(raw)), Operation: in.Operation, State: "prepared"}
	if err = saveNativeAction(cfg, r); err != nil {
		t.Fatal(err)
	}
	if err = RetainNativeAction(cfg, x.Job.ID, in); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, api, code, NewCheckouts(cfg, transport), RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	d.nextRefresh = time.Now().Add(time.Hour)
	return cfg, m, api, code, d, transport, x, in, r
}

func TestNativeAutomaticRecoveryRefusesUnsafeEvidence(t *testing.T) {
	for _, failure := range []string{"running", "revoked", "placement", "paused", "uncertain", "foreign-intent", "retired", "attempt-limit", "producer-busy"} {
		t.Run(failure, func(t *testing.T) {
			cfg, m, _, code, d, transport, x, _, r := automaticRecoveryFixture(t)
			switch failure {
			case "running":
				code.running = true
			case "revoked":
				m.authority.Valid = false
				m.authority.State = "paused"
			case "placement":
				m.self.Placement.Generation++
			case "paused":
				if err := atomicPrivate(filepath.Join(cfg.StateDir, "control.json"), []byte(`{"paused":true}`)); err != nil {
					t.Fatal(err)
				}
			case "uncertain":
				r.State = "uncertain"
				if err := saveNativeAction(cfg, r); err != nil {
					t.Fatal(err)
				}
			case "foreign-intent":
				raw, err := readPrivate(nativeActionIntentPath(cfg, x.Job.ID, r.RequestID), c.MaxJSON)
				if err != nil {
					t.Fatal(err)
				}
				var in nativeActionIntent
				if json.Unmarshal(raw, &in) != nil {
					t.Fatal("intent")
				}
				in.Generation++
				raw, err = json.Marshal(in)
				if err != nil {
					t.Fatal(err)
				}
				if err = atomicPrivate(nativeActionIntentPath(cfg, x.Job.ID, r.RequestID), raw); err != nil {
					t.Fatal(err)
				}
			case "retired":
				x.Phase = "cancelled"
				d.history[x.Job.ID] = x
			case "attempt-limit":
				x.Job.ReconcileFailures = Defaults(cfg).Limits.MaxAttempts
				x.Job.ReconcileError = "execution"
				d.history[x.Job.ID] = x
				if err := saveNativeExecution(cfg, x); err != nil {
					t.Fatal(err)
				}
			case "producer-busy":
				release, err := lockNativeProducer(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			before, err := readPrivate(nativeActionPath(cfg, x.Job.ID, r.RequestID), c.MaxJSON)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = d.Tick(t.Context())
			if d.step != nil {
				_, _ = consumeNativeStep(t, d)
			}
			after, err := readPrivate(nativeActionPath(cfg, x.Job.ID, r.RequestID), c.MaxJSON)
			if err != nil || string(before) != string(after) || len(transport.writes) != 0 || code.calls != 0 {
				t.Fatal("unsafe recovery crossed action/model boundary", err)
			}
		})
	}
}

func TestNativeAutomaticRecoveryPersistsTransportCooldown(t *testing.T) {
	cfg, m, api, code, d, transport, x, _, _ := automaticRecoveryFixture(t)
	clock := &fakeClock{now: time.Now().UTC()}
	d.clock = clock
	m.offline = true
	if _, err := d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := consumeNativeStep(t, d); err == nil {
		t.Fatal("unavailable master was ignored")
	}
	saved, err := loadNativeExecution(cfg, x.Job.ID)
	if err != nil || saved.Job.ReconcileFailures != 1 || saved.Job.ReconcileError != "transport_failed" || !saved.Job.NextAttemptAt.Equal(clock.Now().Add(time.Minute)) {
		t.Fatal("cooldown not persisted", saved.Job, err)
	}
	restarted, err := NewNativeDispatcher(cfg, m, api, code, NewCheckouts(cfg, transport), clock)
	if err != nil {
		t.Fatal(err)
	}
	restarted.nextRefresh = clock.Now().Add(time.Hour)
	if restarted.Ready() {
		t.Fatal("cooldown ignored after restart")
	}
	// Transport reads keep retrying even past the safety-refusal bound.
	saved.Job.ReconcileFailures = Defaults(cfg).Limits.MaxAttempts + 1
	if err = saveNativeExecution(cfg, saved); err != nil {
		t.Fatal(err)
	}
	restarted.history[x.Job.ID] = saved
	clock.now = clock.now.Add(time.Minute)
	m.offline = false
	if !restarted.Ready() {
		t.Fatal("transport failure became a permanent operator hold")
	}
	if _, err = restarted.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, restarted); err != nil {
		t.Fatal(err)
	}
	saved, err = loadNativeExecution(cfg, x.Job.ID)
	if err != nil || saved.Job.ReconcileFailures != 0 || !transport.projectReady || code.calls != 0 {
		t.Fatal("recovery did not complete original request without a model", err, transport.writes)
	}
	if nativeRecoveryBackoff(100) != 15*time.Minute {
		t.Fatal("unbounded cooldown")
	}
}

func TestNativeAutomaticRecoveryFindsIntentBeforeJournal(t *testing.T) {
	cfg, m, _, code, d, transport, x, in, r := automaticRecoveryFixture(t)
	if err := os.Remove(nativeActionPath(cfg, x.Job.ID, r.RequestID)); err != nil {
		t.Fatal(err)
	}
	x.Phase, x.Reason = "active", ""
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	m.offline = true
	if _, err := RunNativeAction(t.Context(), cfg, m, d.github.(*GitHubClient), x.Job.ID, in); err == nil {
		t.Fatal("initial read failure not reproduced")
	}
	x.Phase, x.Reason = "blocked", "native_completed_without_result"
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d.history[x.Job.ID] = x
	m.offline = false
	if !d.Ready() {
		t.Fatal("exact retained intent lost before journal creation")
	}
	if _, err := d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := consumeNativeStep(t, d); err != nil {
		t.Fatal(err)
	}
	creates := 0
	for _, w := range transport.writes {
		if w == "POST repos/code/repo/issues" {
			creates++
		}
	}
	if creates != 1 || code.calls != 0 {
		t.Fatal("intent-only recovery duplicated issue/model", creates)
	}
}

func TestNativeAutomaticRecoveryCrashAfterOriginalDeadline(t *testing.T) {
	cfg, m, api, code, _, transport, x, _, _ := automaticRecoveryFixture(t)
	x.Phase, x.Reason = "active", ""
	x.Job.Deadline = time.Now().Add(-time.Hour)
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, api, code, NewCheckouts(cfg, transport), RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	if x.Phase != "blocked" || x.Reason != "native_completed_without_result" || code.aborts != 0 || code.calls != 0 || len(x.Turns) != 0 {
		t.Fatal("recovery crash became a model timeout/continuation", x.Reason)
	}
	d.history[x.Job.ID] = x
	d.active = nil
	d.nextRefresh = time.Now().Add(time.Hour)
	if _, err = d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, d); err != nil {
		t.Fatal(err)
	}
	if code.calls != 0 || code.aborts != 0 {
		t.Fatal("crash recovery replayed/aborted a terminal model")
	}
}

func TestNativeAutomaticRecoveryHonorsGitHubQuotaReset(t *testing.T) {
	cfg, _, api, code, d, _, x, _, _ := automaticRecoveryFixture(t)
	clock := &fakeClock{now: time.Now().UTC()}
	d.clock = clock
	d.github = NewGitHubClient(&fakeCommands{respond: func(_, _, _ string, _ []byte) (CommandResult, error) {
		return CommandResult{Stdout: []byte("HTTP/2.0 429 Too Many Requests\r\nRetry-After: 3600\r\nContent-Type: application/json\r\n\r\n{}")}, nil
	}})
	if _, err := d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := consumeNativeStep(t, d); err == nil {
		t.Fatal("quota response ignored")
	}
	saved, err := loadNativeExecution(cfg, x.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Job.ReconcileError != "rate_limited" || saved.Job.NextAttemptAt.Before(clock.Now().Add(59*time.Minute)) {
		t.Fatal("GitHub reset ignored", saved.Job.NextAttemptAt)
	}
	saved.Job.ReconcileFailures = Defaults(cfg).Limits.MaxAttempts + 1
	if err = saveNativeExecution(cfg, saved); err != nil {
		t.Fatal(err)
	}
	d.history[x.Job.ID] = saved
	d.github = api
	clock.now = saved.Job.NextAttemptAt.Add(time.Second)
	d.nextRefresh = clock.Now().Add(time.Hour)
	if !d.Ready() {
		t.Fatal("quota wait became permanent after ordinary attempt limit")
	}
	if _, err = d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, d); err != nil {
		t.Fatal(err)
	}
	if code.calls != 0 {
		t.Fatal("quota wait consumed model turn")
	}
}
