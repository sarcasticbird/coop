package worker

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestKataContinuationExposesAndResumesExactSavedTask(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	ref := "kata://01arz3ndektsv4rrffq69g5fav/01ARZ3NDEKTSV4RRFFQ69G5FAW/code/repo"
	g.catalog.Tasks[0].Task.URL = ref
	g.catalog.Tasks[0].Binding.TaskURL = ref
	clock := &fakeClock{now: time.Now()}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	v := NativeCandidates(cfg, g.catalog, nil, clock.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	clock.now = x.Job.Deadline.Add(time.Second)
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	x.Reason = "native_completed_without_result"
	x.Job.Phase = "completed"
	x.Job.SubmissionState = "completed"
	x.Job.TerminalAt = clock.Now()
	if err = saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d, err = NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	target := d.Progress().Continuation
	if target == nil || target.TaskURL != ref {
		t.Fatal("canonical Kata task has no continuation control")
	}
	other := *target
	other.TaskURL = strings.Replace(ref, "code/repo", "other/repo", 1)
	if err = d.RequestContinuation(t.Context(), "wrong-task", x.Job.Master.ThreadID, other, "msg_handoff", func(context.Context) error { return nil }); err == nil || code.calls != 1 {
		t.Fatal("foreign task continued", err)
	}
	if err = d.RequestContinuation(t.Context(), "continue-kata", x.Job.Master.ThreadID, *target, "msg_handoff", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	saved, err := loadNativeExecution(cfg, x.Job.ID)
	if err != nil || saved.Phase != "selected" || saved.Job.SessionID != x.Job.SessionID || len(saved.Turns) != 1 || saved.Turns[0].MessageID != x.Job.MessageID || code.calls != 1 {
		t.Fatal("admission changed identity, history, or submitted early", err)
	}
	if err = d.RequestContinuation(t.Context(), "continue-kata", x.Job.Master.ThreadID, *target, "msg_handoff", func(context.Context) error { return nil }); err != nil {
		t.Fatal("same request not idempotent", err)
	}
	restarted, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.advance(t.Context(), restarted.active); err != nil {
		t.Fatal(err)
	}
	state, intent, err := restarted.ContinuationState("continue-kata", *target)
	if err != nil || state != "resumed" || intent == nil || intent.SessionID != x.Job.SessionID || intent.MessageID == x.Job.MessageID || code.calls != 2 {
		t.Fatal("lost exact successor identity", state, err)
	}
}
