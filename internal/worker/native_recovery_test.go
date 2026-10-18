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
