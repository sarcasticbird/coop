package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

type nativeObserveFailure struct{ *fakeOpenCode }

func (c nativeObserveFailure) Observe(context.Context, Job) (Turn, error) {
	return Turn{}, errors.New("native evidence missing")
}
func TestNativeEngineeringRecoveryNeverReplaysOrAbandonsRunningTurn(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
	if _, err = d.advance(context.Background(), &x); err != nil {
		t.Fatal(err)
	}
	if _, err = ResolveNativeEngineering(context.Background(), cfg, code, x.Job.ID, "ses_wrong", x.Job.MessageID, "cancel"); err == nil {
		t.Fatal("foreign native identity accepted")
	}
	if _, err = ResolveNativeEngineering(context.Background(), cfg, code, x.Job.ID, x.Job.SessionID, x.Job.MessageID, "cancel"); err == nil {
		t.Fatal("running turn abandoned")
	}
	broken := nativeObserveFailure{code}
	d.code = broken
	for i := 0; i < Defaults(cfg).Limits.MaxAttempts; i++ {
		_, _ = d.advance(context.Background(), &x)
	}
	if x.Phase != "blocked" || x.Reason != "native_observation_unavailable" {
		t.Fatalf("unbounded native uncertainty %+v", x)
	}
	code.running = false
	resolved, err := ResolveNativeEngineering(context.Background(), cfg, code, x.Job.ID, x.Job.SessionID, x.Job.MessageID, "cancel")
	if err != nil || resolved.Phase != "cancelled" {
		t.Fatal("terminal cancellation failed", err)
	}
	restarted, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil || restarted.HasActive() || restarted.RecoveryBlocked() {
		t.Fatal("cancelled native work reopened", err)
	}
	if code.calls != 1 {
		t.Fatal("recovery resubmitted native model")
	}
}

func TestNativeObservedTerminalCanContinue(t *testing.T) {
	cfg, m, g, code, _, x := timedOutNativeFixture(t)
	x.Reason = "native_observation_unavailable"
	x.Job.Phase = "active"
	x.Job.SubmissionState = "accepted"
	x.Job.TerminalAt = time.Time{}
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	code.running = false
	observed, err := ResolveNativeEngineering(t.Context(), cfg, code, x.Job.ID, x.Job.SessionID, x.Job.MessageID, "observe")
	if err != nil || observed.Job.Phase != "completed" || observed.Job.TerminalAt.IsZero() {
		t.Fatal("terminal observation left recovery checkpoint incomplete", err)
	}
	continued, err := ContinueNativeEngineering(t.Context(), cfg, m, g, code, x.Job.ID, x.Job.SessionID, x.Job.MessageID)
	if err != nil || continued.Phase != "selected" {
		t.Fatal("observed terminal could not continue", err)
	}
}

type requireNativePair struct{ *fakeOpenCode }

func (c requireNativePair) Observe(ctx context.Context, j Job) (Turn, error) {
	if !validMessage(j.MessageID) {
		return Turn{}, errors.New("missing message identity")
	}
	return c.fakeOpenCode.Observe(ctx, j)
}
func TestNativeUnsubmittedContinuationCanCancelRetainedTurn(t *testing.T) {
	cfg, m, g, code, _, x := timedOutNativeFixture(t)
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.resumeBlocked(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	previous := x.Turns[len(x.Turns)-1]
	cancelled, err := ResolveNativeEngineering(t.Context(), cfg, requireNativePair{code}, x.Job.ID, previous.SessionID, previous.MessageID, "cancel")
	if err != nil || cancelled.Phase != "cancelled" || len(cancelled.Turns) != 1 || code.calls != 1 {
		t.Fatal("unsubmitted continuation could not be cancelled without replay", err)
	}
}
