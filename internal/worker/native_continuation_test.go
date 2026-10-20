package worker

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

type slowNativeCatalog struct {
	*dispatchCatalog
	clock *fakeClock
}

func (g slowNativeCatalog) NativeTaskSnapshot(ctx context.Context, cfg Config, m NativeMaster, _ Job, _ ...string) (NativeCatalog, error) {
	return g.NativeSnapshot(ctx, cfg, m)
}

type continuationCheckouts struct {
	fakeCheckouts
	calls int
}

func (c *continuationCheckouts) Prepare(ctx context.Context, j Job) (Job, error) {
	c.calls++
	return c.fakeCheckouts.Prepare(ctx, j)
}

func (g slowNativeCatalog) NativeSnapshot(ctx context.Context, cfg Config, m NativeMaster) (NativeCatalog, error) {
	g.clock.now = g.clock.now.Add(40 * time.Minute)
	return g.dispatchCatalog.NativeSnapshot(ctx, cfg, m)
}

func TestNativeTurnBudgetStartsAtSubmission(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(map[bool]string{false: "slow-preparation", true: "expired-prepared-restart"}[prepared], func(t *testing.T) {
			cfg, m, g, code := dispatchFixture(t)
			clock := &fakeClock{now: time.Now()}
			v := NativeCandidates(cfg, g.catalog, nil, clock.Now())[0]
			x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
			if prepared {
				x.Phase = "prepared"
				x.Job.SessionID, x.Job.MessageID, x.Job.SubmissionState = "ses_saved", "msg_saved", "prepared"
				x.Job.SubmittedAt, x.Job.Deadline = clock.Now().Add(-2*time.Hour), clock.Now().Add(-time.Hour)
				if err := saveNativeExecution(cfg, x); err != nil {
					t.Fatal(err)
				}
			}
			d, err := NewNativeDispatcher(cfg, m, slowNativeCatalog{g, clock}, code, &fakeCheckouts{}, clock)
			if err != nil {
				t.Fatal(err)
			}
			if prepared {
				x = *d.active
			}
			code.effect = func(_ Config, j Job) {
				t.Helper()
				saved, err := loadNativeExecution(cfg, j.ID)
				if err != nil || saved.Job.SubmissionState != "submitting" || !saved.Job.SubmittedAt.Equal(clock.Now()) || !saved.Job.Deadline.Equal(clock.Now().Add(Defaults(cfg).Limits.TurnLimit)) {
					t.Fatal("submission did not durably receive its full turn budget", err)
				}
			}
			if _, err = d.advance(t.Context(), &x); err != nil || code.calls != 1 {
				t.Fatal("prepared work was not submitted", err)
			}
			if prepared && (x.Job.SessionID != "ses_saved" || x.Job.MessageID != "msg_saved") {
				t.Fatal("prepared native identity changed")
			}
			deadline := x.Job.Deadline
			clock.now = clock.now.Add(time.Minute)
			if _, err = d.advance(t.Context(), &x); err != nil || code.calls != 1 || !x.Job.Deadline.Equal(deadline) {
				t.Fatal("observation refreshed or replayed a submitted turn", err)
			}
		})
	}
}

func timedOutNativeFixture(t *testing.T) (Config, *dispatchMaster, *dispatchCatalog, *fakeOpenCode, *fakeClock, NativeExecution) {
	t.Helper()
	cfg, m, g, code := dispatchFixture(t)
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
	if _, err = d.advance(t.Context(), &x); err != nil || x.Reason != "native_turn_limit" {
		t.Fatal("expected interrupted timeout", err)
	}
	return cfg, m, g, code, clock, x
}

func TestNativeTimedOutJobContinuesSavedWorkAfterRestart(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	first := x.Job
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	d.catalog, d.nextRefresh = g.catalog, clock.Now().Add(time.Hour)
	if !d.Ready() {
		t.Fatal("terminal timeout remains permanently held")
	}
	if _, err = d.Tick(t.Context()); err != nil || d.step == nil {
		t.Fatal("timeout continuation was not scheduled", err)
	}
	if _, err = consumeNativeStep(t, d); err != nil {
		t.Fatal(err)
	}
	x, err = loadNativeExecution(cfg, first.ID)
	if err != nil || x.Phase != "selected" || len(x.Turns) != 1 || x.Turns[0].MessageID != first.MessageID || x.Job.SessionID != first.SessionID || x.Job.Checkout != first.Checkout || code.calls != 1 {
		t.Fatal("continuation lost saved work or replayed the old message", err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil || code.calls != 2 || x.Job.MessageID == first.MessageID || x.Job.SessionID != first.SessionID {
		t.Fatal("continuation did not use a new message in its saved session", err)
	}
}

type foreignNativeEvidence struct{ *fakeOpenCode }

func (c foreignNativeEvidence) Observe(ctx context.Context, j Job) (Turn, error) {
	turn, err := c.fakeOpenCode.Observe(ctx, j)
	turn.MessageID = "msg_foreign"
	return turn, err
}

func TestNativeTimedOutContinuationRefusesUnsafeEvidence(t *testing.T) {
	for _, failure := range []string{"running", "unavailable", "foreign", "uncertain-action", "producer-running", "authority", "placement", "scope", "attempt-limit"} {
		t.Run(failure, func(t *testing.T) {
			cfg, m, g, code, clock, x := timedOutNativeFixture(t)
			d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "running":
				code.running = true
			case "unavailable":
				d.code = nativeObserveFailure{code}
			case "foreign":
				d.code = foreignNativeEvidence{code}
			case "uncertain-action":
				if err := saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "uncertain", State: "uncertain"}); err != nil {
					t.Fatal(err)
				}
			case "producer-running":
				release, err := lockNativeProducer(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "authority":
				m.authority.State = "paused"
			case "placement":
				m.self.Placement.Generation++
			case "scope":
				g.catalog.Tasks[0].Binding.TaskScopeSHA = BodyHash("changed")
			case "attempt-limit":
				x.Turns = make([]Job, Defaults(cfg).Limits.MaxAttempts)
			}
			if _, err = d.advance(t.Context(), &x); err == nil || x.Phase != "blocked" || code.calls != 1 {
				t.Fatal("unsafe timeout resumed or lost its hold", err)
			}
		})
	}
}

func TestNativeTimedOutContinuationStopsAtAttemptLimit(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	for attempt := 0; attempt < Defaults(cfg).Limits.MaxAttempts; attempt++ {
		d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = d.advance(t.Context(), &x); err != nil || x.Phase != "selected" {
			t.Fatal("bounded continuation refused", err)
		}
		code.effect = func(Config, Job) { code.running = true }
		if _, err = d.advance(t.Context(), &x); err != nil {
			t.Fatal(err)
		}
		clock.now = x.Job.Deadline.Add(time.Second)
		if _, err = d.advance(t.Context(), &x); err != nil || x.Reason != "native_turn_limit" {
			t.Fatal("continued turn did not time out", err)
		}
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err == nil || x.Reason != "native_continuation_limit" || code.calls != 1+Defaults(cfg).Limits.MaxAttempts {
		t.Fatal("unbounded timeout continuation", err)
	}
	restarted, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	restarted.catalog, restarted.nextRefresh = g.catalog, clock.Now().Add(time.Hour)
	if restarted.Ready() || !restarted.RecoveryBlocked() {
		t.Fatal("exhausted continuation was retried after restart")
	}
}

func TestNativeTimedOutFinalResultDoesNotReplay(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	x.Turns = make([]Job, Defaults(cfg).Limits.MaxAttempts)
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	if err := saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "published", Operation: "publish-pr", State: "completed"}); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	d.catalog, d.nextRefresh = g.catalog, clock.Now().Add(time.Hour)
	if !d.Ready() {
		t.Fatal("completed final turn was stranded")
	}
	if done, err := d.advance(t.Context(), &x); err != nil || !done || x.Phase != "completed" || code.calls != 1 {
		t.Fatal("completed result was replayed", err)
	}
}

func TestNativeTimedOutContinuationHonorsAdmissionPause(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	if err := atomicPrivate(filepath.Join(cfg.StateDir, "admission-control.json"), []byte(`{"Paused":true}`)); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil || x.Phase != "blocked" || len(x.Turns) != 0 || code.calls != 1 {
		t.Fatal("paused admission changed saved turn", err)
	}
}

func TestNativeTimeoutModelChangeIsDurableBeforeSubmission(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	old := &ModelIdentity{ProviderID: "openai", ModelID: "gpt-5.6-sol", Variant: "high"}
	x.Job.SubmittedModel, x.Job.ExecutedModel = old, old
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	cfg.Model, cfg.ModelVariant = "openai/gpt-6.1-sol", "medium"
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	if x.Job.SubmittedModel != nil || x.Job.ExecutedModel != nil || len(x.Turns) != 1 || x.Turns[0].ExecutedModel != old {
		t.Fatal("new turn retained old model metadata or lost its historical model")
	}
	code.effect = func(_ Config, j Job) {
		t.Helper()
		saved, err := loadNativeExecution(cfg, j.ID)
		want := ModelIdentity{ProviderID: "openai", ModelID: "gpt-6.1-sol", Variant: "medium"}
		if err != nil || saved.Job.SubmittedModel == nil || *saved.Job.SubmittedModel != want || saved.Job.ExecutedModel != nil {
			t.Fatal("uncertain submission was not pinned to its requested model", err)
		}
	}
	code.lost = true
	if _, err = d.advance(t.Context(), &x); err == nil || x.Job.SubmissionState != "unknown" {
		t.Fatal("expected uncertain model submission", err)
	}
}

func TestNativeContinuationRejectsRealManualTakeover(t *testing.T) {
	for _, stage := range []string{"idle-manual", "busy-manual", "after-resume"} {
		t.Run(stage, func(t *testing.T) {
			cfg, m, g, _, clock, x := timedOutNativeFixture(t)
			old := x.Job
			manual := stage != "after-resume"
			_, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("continuation mutated the native server before verifying ownership")
					w.WriteHeader(500)
					return
				}
				switch r.URL.Path {
				case "/global/health":
					writeJSON(w, ServerHealth{Healthy: true, Version: "1.18.18"})
				case "/session/" + old.SessionID:
					writeJSON(w, map[string]string{"id": old.SessionID, "directory": old.Checkout})
				case "/session/status":
					state := "idle"
					if stage == "busy-manual" {
						state = "busy"
					}
					writeJSON(w, map[string]any{old.SessionID: map[string]string{"type": state}})
				case "/session/" + old.SessionID + "/message/" + old.MessageID:
					writeJSON(w, ownershipMessage(old, old.MessageID, "user", ""))
				case "/session/" + old.SessionID + "/message":
					reply := ownershipMessage(old, "msg_finished", "assistant", old.MessageID)
					reply["info"].(map[string]any)["error"] = map[string]string{"name": "MessageAbortedError"}
					messages := []any{ownershipMessage(old, old.MessageID, "user", ""), reply}
					if manual {
						messages = append(messages, ownershipMessage(old, "msg_manual", "user", ""))
					}
					writeJSON(w, messages)
				default:
					w.WriteHeader(404)
				}
			})
			checkouts := &continuationCheckouts{}
			d, err := NewNativeDispatcher(cfg, m, g, client, checkouts, clock)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "after-resume" {
				if _, err = d.advance(t.Context(), &x); err != nil || x.Phase != "selected" {
					t.Fatal("owned terminal turn failed to resume", err)
				}
				manual = true
			}
			if _, err = d.advance(t.Context(), &x); err == nil || checkouts.calls != 0 {
				t.Fatal("automatic continuation admitted a manually used session")
			}
		})
	}
}

func TestNativeWorkerAcceptsHourTurnLimit(t *testing.T) {
	cfg := configFixture(t)
	cfg.Limits.TurnLimit = time.Hour
	if err := ValidateConfig(cfg); err != nil {
		t.Fatal("one hour turn budget refused", err)
	}
	cfg.Limits.TurnLimit += time.Second
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("unbounded turn limit accepted")
	}
}

type nativeRejectedSubmission struct{ *fakeOpenCode }

func (c nativeRejectedSubmission) Submit(context.Context, Config, Job, string) (Turn, error) {
	return Turn{State: "not_submitted"}, errors.New("manual ownership changed")
}
func TestNativeKnownUnsubmittedRemainsRecoverable(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	d.code = nativeRejectedSubmission{code}
	if _, err = d.advance(t.Context(), &x); err == nil || x.Phase != "prepared" || x.Job.SubmissionState != "prepared" {
		t.Fatal("known unsubmitted message became uncertain", err)
	}
	if _, err = ResolveNativeEngineering(t.Context(), cfg, code, x.Job.ID, x.Job.SessionID, x.Job.MessageID, "cancel"); err != nil {
		t.Fatal("known unsubmitted could not be cancelled", err)
	}
}
func TestNativePausedUnsubmittedContinuationCanCancel(t *testing.T) {
	cfg, m, g, code, clock, x := timedOutNativeFixture(t)
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	old := x.Turns[len(x.Turns)-1]
	if err = atomicPrivate(filepath.Join(cfg.StateDir, "control.json"), []byte(`{"Paused":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil || x.Reason != "local_pause" {
		t.Fatal(err)
	}
	if _, err = ResolveNativeEngineering(t.Context(), cfg, requireNativePair{code}, x.Job.ID, old.SessionID, old.MessageID, "cancel"); err != nil {
		t.Fatal("paused unsubmitted continuation became irrecoverable", err)
	}
}
