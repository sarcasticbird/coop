package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

type retryContinuationCode struct {
	*fakeOpenCode
	fail bool
}

func (c *retryContinuationCode) CheckContinuation(ctx context.Context, j Job) error {
	if c.fail {
		return errors.New("temporary continuation read failure")
	}
	return c.fakeOpenCode.CheckContinuation(ctx, j)
}

func TestNativeFinishedBlockedWorkDoesNotFreezeCoworker(t *testing.T) {
	for _, state := range []string{"prepared", "uncertain", "legacy-recovery", "legacy-completed"} {
		t.Run(state, func(t *testing.T) {
			cfg, master, catalog, code, blocked := nativeTerminalWaitFixture(t)
			blocked.Reason = "native_observation_unavailable"
			if state == "legacy-recovery" || state == "legacy-completed" {
				// Unsupported historical requests are readable, never executable.
				blocked.Recovery = &nativeRecoveryAudit{Adopted: false}
				blocked.Phase = "active"
				if state == "legacy-completed" {
					blocked.Phase = "completed"
					blocked.Job.TerminalAt = time.Time{}
				}
			}
			raw, err := json.Marshal(blocked)
			if err != nil {
				t.Fatal(err)
			}
			path := nativeExecutionPath(cfg, blocked.Job.ID)
			if err := atomicPrivate(path, raw); err != nil {
				t.Fatal(err)
			}
			action := nativeActionRecord{Version: 3, JobID: blocked.Job.ID, RequestID: "old-request", Operation: "create-task", State: "uncertain", Step: "create-task"}
			if state == "prepared" {
				action.State = "prepared"
			}
			if err := saveNativeAction(cfg, action); err != nil {
				t.Fatal(err)
			}
			actionPath := nativeActionPath(cfg, blocked.Job.ID, action.RequestID)
			actionRaw, err := os.ReadFile(actionPath)
			if err != nil {
				t.Fatal(err)
			}
			catalog.catalog.Tasks[0].PRs = []PR{nativeReviewPRFixture(cfg, catalog.catalog.Tasks[0])}
			for range 2 {
				d, err := NewNativeDispatcher(cfg, master, catalog, code, &fakeCheckouts{}, RealClock{})
				if err != nil || d.RecoveryBlocked() || d.HasActive() {
					t.Fatal("finished historical work occupied the coworker", err)
				}
				progress := d.Progress()
				if state != "legacy-completed" && len(progress.Blocked) != 1 {
					t.Fatal("blocked task disappeared from coworker status", progress)
				}
				if blocked.unresolvedHistoricalRecovery() && progress.Continuation != nil {
					t.Fatal("status offered to replay unsupported historical work")
				}
				if retry, err := nativeAutomaticRecovery(cfg, blocked); err != nil || retry {
					t.Fatal("old blocked effect became automatic work", err)
				}
				d.catalog, d.nextRefresh = catalog.catalog, time.Now().Add(time.Hour)
				candidates, err := d.candidates(d.catalog)
				if err != nil || len(candidates) != 1 || candidates[0].Job.Kind != "review" {
					t.Fatal("unrelated review was blocked", len(candidates), err)
				}
			}
			d, err := NewNativeDispatcher(cfg, master, catalog, code, &fakeCheckouts{}, RealClock{})
			if err != nil {
				t.Fatal(err)
			}
			d.catalog, d.nextRefresh = catalog.catalog, time.Now().Add(time.Hour)
			if _, err := d.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := consumeNativeStep(t, d); err != nil || code.calls != 1 || code.last.Kind != "review" {
				t.Fatal("coworker could not take unrelated work", err)
			}
			if _, err := RunNativeAction(t.Context(), cfg, master, NewGitHubClient(nil), blocked.Job.ID, NativeAction{Operation: "create-task", RequestID: "replacement-request"}); err == nil {
				t.Fatal("replacement request replayed old work")
			}
			for path, before := range map[string][]byte{path: raw, actionPath: actionRaw} {
				after, err := os.ReadFile(path)
				if err != nil || string(after) != string(before) {
					t.Fatal("old work was rewritten to free the coworker", path, err)
				}
			}
		})
	}
}

func TestNativeUnknownTaskRemainsReservedAcrossNewHeads(t *testing.T) {
	cfg, master, catalog, code, _ := nativeTerminalWaitFixture(t)
	task := catalog.catalog.Tasks[0]
	task.PRs = []PR{nativeReviewPRFixture(cfg, task)}
	v := NativeCandidates(cfg, NativeCatalog{Complete: true, Goals: catalog.catalog.Goals, Tasks: []NativeTask{task}}, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "blocked", Reason: "native_completed_without_result"}
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.SessionID, x.Job.MessageID, x.Job.TerminalAt = "ses_blocked", "msg_blocked", time.Now()
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	if err := saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "unknown", Operation: "review", State: "uncertain", Step: "review-published"}); err != nil {
		t.Fatal(err)
	}
	task.PRs[0].HeadSHA = BodyHash("new head")[:40]
	other := task
	other.Task.URL, other.Binding.TaskURL = "https://github.com/code/repo/issues/2", "https://github.com/code/repo/issues/2"
	other.PRs = []PR{nativeReviewPRFixture(cfg, other)}
	other.PRs[0].URL = "https://github.com/code/repo/pull/2"
	catalog.catalog.Tasks = []NativeTask{task, other}
	d, err := NewNativeDispatcher(cfg, master, catalog, code, &fakeCheckouts{}, RealClock{})
	if err != nil || d.RecoveryBlocked() {
		t.Fatal("task uncertainty froze the coworker", err)
	}
	candidates, err := d.candidates(catalog.catalog)
	if err != nil || len(candidates) != 1 || candidates[0].Job.TaskURL != other.Task.URL {
		t.Fatal("unknown task escaped its reservation or blocked unrelated work", candidates, err)
	}
}

func TestNativeUnaccountedLegacyProcessPreventsOtherContinuation(t *testing.T) {
	cfg, master, catalog, code, clock, waiting := timedOutNativeFixture(t)
	legacy := waiting
	legacy.Job.ID = BodyHash("unaccounted historical process")
	legacy.Phase, legacy.Reason = "active", "native_observation_unavailable"
	legacy.Job.Phase, legacy.Job.SubmissionState, legacy.Job.TerminalAt = "active", "accepted", time.Time{}
	legacy.Recovery = &nativeRecoveryAudit{Adopted: false}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicPrivate(nativeExecutionPath(cfg, legacy.Job.ID), raw); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, master, catalog, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	d.catalog, d.nextRefresh = catalog.catalog, clock.Now().Add(time.Hour)
	if !d.RecoveryBlocked() || d.Ready() || d.resumableBlocked() != nil {
		t.Fatal("unaccounted legacy process released the native slot")
	}
	if _, err := d.Tick(t.Context()); err != nil || d.HasActive() || code.calls != 1 {
		t.Fatal("another continuation entered an occupied slot", err)
	}
}

func TestNativeFinishedTaskRetriesTransientContinuationWithoutGlobalHold(t *testing.T) {
	cfg, master, catalog, code, clock, waiting := timedOutNativeFixture(t)
	flaky := &retryContinuationCode{fakeOpenCode: code, fail: true}
	d, err := NewNativeDispatcher(cfg, master, catalog, flaky, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	d.catalog, d.nextRefresh = catalog.catalog, clock.Now().Add(time.Hour)
	if _, err := d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := consumeNativeStep(t, d); err == nil {
		t.Fatal("temporary failure was not exercised")
	}
	saved, err := loadNativeExecution(cfg, waiting.Job.ID)
	if err != nil || !nativeModelFinished(saved.Job) || !saved.Job.NextAttemptAt.After(clock.Now()) || d.RecoveryBlocked() {
		t.Fatal("finished task failed to become a backed-off task-local wait", saved.Job, err)
	}
	flaky.fail = false
	d, err = NewNativeDispatcher(cfg, master, catalog, flaky, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	d.catalog, d.nextRefresh = catalog.catalog, clock.Now().Add(time.Hour)
	if d.Ready() {
		t.Fatal("continuation ignored its retry backoff")
	}
	clock.now = saved.Job.NextAttemptAt.Add(time.Second)
	if !d.Ready() || d.RecoveryBlocked() {
		t.Fatal("finished continuation was stranded outside ordinary scheduling")
	}
	if _, err := d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := consumeNativeStep(t, d); err != nil || d.active == nil || d.active.Phase != "selected" {
		t.Fatal("normal scheduling did not resume the task", err)
	}
	if d.active.Job.SessionID != waiting.Job.SessionID || len(d.active.Turns) != 1 || d.active.Turns[0].MessageID != waiting.Job.MessageID || code.calls != 1 {
		t.Fatal("continuation lost context or submitted before preparation")
	}
	if _, err := d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := consumeNativeStep(t, d); err != nil || code.calls != 2 {
		t.Fatal("resumed work was not submitted exactly once", code.calls, err)
	}
}
