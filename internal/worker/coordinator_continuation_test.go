package worker

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func coordinatorTarget(cfg Config, x NativeExecution) c.TaskContinuationTarget {
	return c.TaskContinuationTarget{JobID: x.Job.ID, TaskURL: x.Job.TaskURL, SessionID: x.Job.SessionID, MessageID: x.Job.MessageID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation}
}
func TestCoordinatorContinuationRetainsAndDeduplicatesSavedRun(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	x.Reason = "native_completed_without_result"
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = clock.Now()
	if e := saveNativeExecution(cfg, x); e != nil {
		t.Fatal(e)
	}
	d, e := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if e != nil {
		t.Fatal(e)
	}
	target := coordinatorTarget(cfg, x)
	if e = d.RequestContinuation(t.Context(), "delivery-one", x.Job.Master.ThreadID, target, "msg_handoff", func(context.Context) error { return nil }); e != nil {
		t.Fatal(e)
	}
	saved, e := loadNativeExecution(cfg, x.Job.ID)
	if e != nil || saved.Phase != "selected" || len(saved.Turns) != 1 || saved.Job.SessionID != x.Job.SessionID || code.calls != 1 {
		t.Fatal("lost saved work or submitted during admission", e)
	}
	if e = d.RequestContinuation(t.Context(), "delivery-one", x.Job.Master.ThreadID, target, "msg_handoff", func(context.Context) error { return nil }); e != nil {
		t.Fatal("duplicate admission not recognized", e)
	}
	restarted, e := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if e != nil {
		t.Fatal(e)
	}
	state, intent, e := restarted.ContinuationState("delivery-one", target)
	if e != nil || state != "admitted" || intent != nil {
		t.Fatal("checkpoint falsely claimed resumed", state, e)
	}
	if _, e = restarted.advance(t.Context(), restarted.active); e != nil {
		t.Fatal(e)
	}
	restarted.history[x.Job.ID] = *restarted.active
	state, intent, e = restarted.ContinuationState("delivery-one", target)
	if e != nil || state != "resumed" || intent == nil || intent.MessageID == x.Job.MessageID || intent.SessionID != x.Job.SessionID || code.calls != 2 {
		t.Fatal("missing exact resumed identity", state, e)
	}
	if e = restarted.RequestContinuation(t.Context(), "delivery-two", x.Job.Master.ThreadID, target, "msg_handoff", func(context.Context) error { return nil }); e == nil {
		t.Fatal("old target continued twice")
	}
}
func TestCoordinatorContinuationRejectsUnsafeTargets(t *testing.T) {
	for _, kind := range []string{"message", "placement", "task", "thread", "running", "uncertain", "prepared-action", "limit", "revoked", "paused"} {
		t.Run(kind, func(t *testing.T) {
			cfg, m, g, code, clock, x := timedOutNativeFixture(t)
			x.Reason = "native_completed_without_result"
			x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
			x.Job.TerminalAt = clock.Now()
			target := coordinatorTarget(cfg, x)
			thread := x.Job.Master.ThreadID
			switch kind {
			case "message":
				target.MessageID = "msg_foreign"
			case "placement":
				target.Generation++
			case "task":
				target.TaskURL += "2"
			case "thread":
				thread = "other"
			case "running":
				code.running = true
			case "uncertain", "prepared-action":
				state := "uncertain"
				if kind == "prepared-action" {
					state = "prepared"
				}
				if e := saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "pending", State: state}); e != nil {
					t.Fatal(e)
				}
			case "limit":
				x.Turns = make([]Job, Defaults(cfg).Limits.MaxAttempts)
			case "revoked":
				m.authority.State = "paused"
			case "paused":
				if e := atomicPrivate(filepath.Join(cfg.StateDir, "control.json"), []byte(`{"paused":true}`)); e != nil {
					t.Fatal(e)
				}
			}
			if e := saveNativeExecution(cfg, x); e != nil {
				t.Fatal(e)
			}
			d, e := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
			if e != nil {
				t.Fatal(e)
			}
			if e = d.RequestContinuation(t.Context(), "delivery", thread, target, "msg_handoff", func(context.Context) error { return nil }); e == nil || code.calls != 1 {
				t.Fatal("unsafe continuation admitted", e)
			}
		})
	}
}
func TestCoordinatorContinuationWaitsForProducerWithoutConsumingAttempt(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	x.Reason = "native_completed_without_result"
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = time.Now()
	if e := saveNativeExecution(cfg, x); e != nil {
		t.Fatal(e)
	}
	d, e := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if e != nil {
		t.Fatal(e)
	}
	release, e := lockNativeProducer(cfg)
	if e != nil {
		t.Fatal(e)
	}
	e = d.RequestContinuation(t.Context(), "delivery", x.Job.Master.ThreadID, coordinatorTarget(cfg, x), "msg_handoff", func(context.Context) error { return nil })
	release()
	if !errors.Is(e, errNativeProducerBusy) {
		t.Fatal("contention not retryable", e)
	}
	saved, e := loadNativeExecution(cfg, x.Job.ID)
	if e != nil || len(saved.Turns) != 0 || saved.Phase != "blocked" {
		t.Fatal("contention consumed continuation", e)
	}
}

func TestContinuationTargetIsAvailableInScopedDiagnostics(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	x.Reason = "native_completed_without_result"
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = clock.Now()
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	p := d.Progress()
	report := newDiagnosticBuilder(clock.Now()).snapshot(cfg, 1, Status{Native: &p}, nil, c.DiagnosticService{}, c.DiagnosticService{}, clock.Now())
	if report.Work.Continuation == nil || *report.Work.Continuation != coordinatorTarget(cfg, x) {
		t.Fatal("coordinator lacks an exact scoped continuation target")
	}
	if err = c.ValidateDiagnostics(report); err != nil {
		t.Fatal("target broke bounded diagnostic validation", err)
	}
}

func TestCoordinatorContinuationCarriesHandoffIntoNativePrompt(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	x.Reason = "native_completed_without_result"
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = clock.Now()
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.RequestContinuation(t.Context(), "delivery", x.Job.Master.ThreadID, coordinatorTarget(cfg, x), "msg_handoff", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	saved, err := loadNativeExecution(cfg, x.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := nativePrompt(cfg, saved, NativeCandidates(cfg, g.catalog, nil, clock.Now())[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "msg_handoff") || !strings.Contains(prompt, "Read that handoff before editing") {
		t.Fatal("coordinator direction was not carried into engineering context")
	}
}

func TestCoordinatorAuthorityRecheckedBeforeCheckpoint(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	x.Reason = "native_completed_without_result"
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = clock.Now()
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	err = d.RequestContinuation(t.Context(), "delivery", x.Job.Master.ThreadID, coordinatorTarget(cfg, x), "msg_handoff", func(context.Context) error { checks++; return c.ErrForbidden })
	saved, readErr := loadNativeExecution(cfg, x.Job.ID)
	if !errors.Is(err, c.ErrForbidden) || checks != 1 || readErr != nil || len(saved.Turns) != 0 || saved.ContinuationRequestID != "" || code.calls != 1 {
		t.Fatal("revoked coordinator checkpointed work", err, readErr, checks)
	}
}

func TestCoordinatorContinuationPreservesEarlierReceiptAcrossLaterRequest(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	x.Reason = "native_completed_without_result"
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = clock.Now()
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	target1 := coordinatorTarget(cfg, x)
	allow := func(context.Context) error { return nil }
	if err = d.RequestContinuation(t.Context(), "first", x.Job.Master.ThreadID, target1, "msg_first", allow); err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), d.active); err != nil {
		t.Fatal(err)
	}
	d.history[x.Job.ID] = *d.active
	code.running = false
	if _, err = d.advance(t.Context(), d.active); err != nil {
		t.Fatal(err)
	}
	d.history[x.Job.ID] = *d.active
	next := *d.active
	d.active = nil
	target2 := coordinatorTarget(cfg, next)
	if err = d.RequestContinuation(t.Context(), "second", x.Job.Master.ThreadID, target2, "msg_second", allow); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	state, intent, err := restarted.ContinuationState("first", target1)
	if err != nil || state != "resumed" || intent == nil || intent.MessageID != target2.MessageID {
		t.Fatal("later admission lost earlier accepted native identity", state, intent, err)
	}
	state, intent, err = restarted.ContinuationState("second", target2)
	if err != nil || state != "admitted" || intent != nil {
		t.Fatal("new admission falsely claimed resume", state, intent, err)
	}
}

func parkedCoordinatorFixture(t *testing.T) (*NativeDispatcher, NativeExecution, c.TaskContinuationTarget) {
	t.Helper()
	cfg, m, g, code, clock, x := dependencyWaitFixture(t)
	wait, err := prepareNativeWait(t.Context(), cfg, m, g, x, NativeAction{DependsOn: []string{g.catalog.Tasks[1].Task.URL}, Body: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	x.Wait = wait
	x.Phase, x.Reason = "blocked", "native_dependency_wait"
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = clock.Now()
	g.catalog.Tasks[1].Task.Completed = true
	if err = saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	target := coordinatorTarget(cfg, x)
	if err = d.RequestContinuation(t.Context(), "first", x.Job.Master.ThreadID, target, "msg_first", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	parked, err := loadNativeExecution(cfg, x.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	g.catalog.Tasks[1].Task.Completed = false
	if _, err = d.advance(t.Context(), &parked); err != nil || parked.PendingContinuation == nil {
		t.Fatal("continuation did not park", err)
	}
	d.history[x.Job.ID] = parked
	d.active = nil
	g.catalog.Tasks[1].Task.Completed = true
	return d, parked, target
}
func TestCoordinatorContinuationCannotReplaceParkedRequest(t *testing.T) {
	d, x, target := parkedCoordinatorFixture(t)
	err := d.RequestContinuation(t.Context(), "second", x.Job.Master.ThreadID, target, "msg_second", func(context.Context) error { return nil })
	if !errors.Is(err, errContinuationTarget) {
		t.Fatal("second request replaced a parked continuation", err)
	}
	saved, err := loadNativeExecution(d.config, x.Job.ID)
	if err != nil || saved.ContinuationRequestID != "first" || saved.ContinuationSourceMessageID != "msg_first" {
		t.Fatal("owning handoff changed", err)
	}
	if err = d.RequestContinuation(t.Context(), "first", x.Job.Master.ThreadID, target, "msg_first", func(context.Context) error { return nil }); err != nil {
		t.Fatal("owning retry refused", err)
	}
}
func TestCoordinatorContinuationCancelledWhileParkedIsRefused(t *testing.T) {
	d, x, target := parkedCoordinatorFixture(t)
	x.Phase, x.Reason = "cancelled", "native_task_scope_changed"
	if err := saveNativeExecution(d.config, x); err != nil {
		t.Fatal(err)
	}
	d.history[x.Job.ID] = x
	state, intent, err := d.ContinuationState("first", target)
	if state != "refused" || intent != nil || err == nil {
		t.Fatal("cancelled parked continuation stayed admitted", state, intent, err)
	}
}
func TestCoordinatorContinuationRetriesOtherTaskProducerContention(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	x.Reason = "native_completed_without_result"
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = clock.Now()
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	other := x
	other.Job.ID = strings.Repeat("f", 64)
	d.history[other.Job.ID] = other
	release, err := lockNativeProducer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = d.RequestContinuation(t.Context(), "delivery", x.Job.Master.ThreadID, coordinatorTarget(cfg, x), "msg_handoff", func(context.Context) error { return nil })
	if !errors.Is(err, errNativeProducerBusy) || !continuationRetryable(err) {
		t.Fatal("other task hid retryable producer contention", err)
	}
}
