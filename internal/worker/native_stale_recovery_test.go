package worker

import (
	"context"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nativeStaleAttemptFixture(t *testing.T) (Config, *dispatchMaster, *dispatchCatalog, *fakeOpenCode, NativeExecution) {
	t.Helper()
	cfg, m, g, code := dispatchFixture(t)
	p := nativeReviewPRFixture(cfg, g.catalog.Tasks[0])
	p.Reviews = []Review{{ID: "late-peer-findings", State: "CHANGES_REQUESTED", HeadSHA: p.HeadSHA, Author: cfg.Team.Members[0].Actor}}
	g.catalog.Tasks[0].PRs = []PR{p}
	jobs := NativeCandidates(cfg, g.catalog, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != "fix" {
		t.Fatal("missing original author fix", jobs)
	}
	v := jobs[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Binding: &v.Task.Binding, Job: v.Job, Phase: "blocked", Reason: "native_completed_without_result"}
	x.Job.SessionID, x.Job.MessageID, x.Job.Checkout = "ses_stale", "msg_stale", filepath.Join(cfg.WorkRoot, "preserved")
	x.Job.Phase, x.Job.SubmissionState, x.Job.TerminalAt = "completed", "completed", time.Now()
	if err := os.MkdirAll(x.Job.Checkout, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(x.Job.Checkout, "findings.txt"), []byte("preserve checkout"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	code.running = false
	g.catalog.Tasks[0].PRs[0].MergeState = "BEHIND"
	g.catalog.Tasks[0].PRs[0].BaseSHA = strings.Repeat("b", 40)
	x, err := loadNativeExecution(cfg, x.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, m, g, code, x
}

func TestNativeStaleTerminalAttemptAutomaticallyReleasesReplacement(t *testing.T) {
	cfg, m, g, code, x := nativeStaleAttemptFixture(t)
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	d.catalog, d.nextRefresh = g.catalog, time.Now().Add(time.Hour)
	if len(NativeCandidates(cfg, g.catalog, d.history, time.Now())) != 0 {
		t.Fatal("fixture must retain the task fence")
	}
	if !d.Ready() {
		t.Fatal("obsolete terminal attempt never becomes eligible for retirement")
	}
	if _, err = d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, d); err != nil {
		t.Fatal(err)
	}
	saved, err := loadNativeExecution(cfg, x.Job.ID)
	if err != nil || saved.Phase != "cancelled" || saved.Reason != "native_terminal_attempt_superseded" {
		t.Fatal("obsolete task reservation retained", saved.Phase, saved.Reason, err)
	}
	if saved.Job.SessionID != x.Job.SessionID || saved.Job.MessageID != x.Job.MessageID || saved.Job.Checkout != x.Job.Checkout || !saved.Job.TerminalAt.Equal(x.Job.TerminalAt) || len(saved.Turns) != 0 || code.calls != 0 || code.aborts != 0 {
		t.Fatal("retirement changed or replayed native work", saved.Job)
	}
	raw, err := os.ReadFile(filepath.Join(x.Job.Checkout, "findings.txt"))
	if err != nil || string(raw) != "preserve checkout" {
		t.Fatal("retirement changed local checkout", err)
	}
	jobs := NativeCandidates(cfg, g.catalog, d.history, time.Now())
	if len(jobs) != 1 || !nativeIsBranchRefresh(jobs[0].Job) || jobs[0].Job.ID == x.Job.ID {
		t.Fatal("current branch refresh remains fenced", jobs)
	}
	restarted, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil || restarted.history[x.Job.ID].Phase != "cancelled" {
		t.Fatal("retirement did not survive restart", err)
	}
}

func TestNativeStaleTerminalRetirementRefusesUnsafeEvidence(t *testing.T) {
	for _, failure := range []string{"unchanged", "running", "foreign-session", "foreign-message", "observation-unavailable", "idle-unavailable", "revoked", "placement", "paused", "admission-paused", "incomplete", "transport", "assignment", "prepared-action", "uncertain-action", "completed-action", "intent-only", "pending-continuation", "nonterminal", "producer-busy", "new-checkpoint"} {
		t.Run(failure, func(t *testing.T) {
			cfg, m, g, code, x := nativeStaleAttemptFixture(t)
			d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "unchanged":
				g.catalog.Tasks[0].PRs[0].MergeState = "CLEAN"
			case "running":
				code.running = true
			case "foreign-session", "foreign-message", "observation-unavailable", "idle-unavailable":
				d.code = staleRetirementEvidence{OpenCode: code, failure: failure}
			case "revoked":
				m.authority.Valid, m.authority.State = false, "paused"
			case "placement":
				m.self.Placement.Generation++
			case "paused":
				if err = atomicPrivate(filepath.Join(cfg.StateDir, "control.json"), []byte(`{"paused":true}`)); err != nil {
					t.Fatal(err)
				}
			case "admission-paused":
				if err = atomicPrivate(filepath.Join(cfg.StateDir, "admission-control.json"), []byte(`{"paused":true}`)); err != nil {
					t.Fatal(err)
				}
			case "incomplete":
				g.catalog.Complete = false
			case "transport":
				g.err = c.ErrUnavailable
			case "assignment":
				g.catalog.Tasks[0].Binding.ApprovalSequence++
			case "prepared-action", "uncertain-action", "completed-action":
				state := strings.TrimSuffix(failure, "-action")
				if err = saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "unresolved", State: state}); err != nil {
					t.Fatal(err)
				}
			case "intent-only":
				if err = persistNativeActionIntent(cfg, x, NativeAction{Operation: "publish-pr", RequestID: "retained"}); err != nil {
					t.Fatal(err)
				}
			case "pending-continuation":
				x.PendingContinuation = &Job{ID: "pending"}
			case "nonterminal":
				x.Job.TerminalAt = time.Time{}
			case "producer-busy":
				release, lockErr := lockNativeProducer(cfg)
				if lockErr != nil {
					t.Fatal(lockErr)
				}
				defer release()
			case "new-checkpoint":
				newer := x
				newer.Reason = "operator_changed"
				if err = saveNativeExecution(cfg, newer); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "pending-continuation" || failure == "nonterminal" {
				if err = saveNativeExecution(cfg, x); err != nil {
					t.Fatal(err)
				}
				d.history[x.Job.ID] = x
			}
			_, err = d.retireStaleTerminalAttempt(t.Context(), &x)
			if err == nil {
				t.Fatal("unsafe evidence accepted for retirement")
			}
			saved, readErr := loadNativeExecution(cfg, x.Job.ID)
			if readErr != nil || saved.Phase != "blocked" || code.calls != 0 || code.aborts != 0 {
				t.Fatal("unsafe attempt retired or replayed", saved.Phase, err, readErr)
			}
			if failure == "new-checkpoint" && saved.Reason != "operator_changed" {
				t.Fatal("retirement overwrote a newer checkpoint", saved.Reason)
			}
		})
	}
}

type staleRetirementEvidence struct {
	OpenCode
	failure string
}

func (s staleRetirementEvidence) Observe(ctx context.Context, j Job) (Turn, error) {
	if s.failure == "observation-unavailable" {
		return Turn{}, c.ErrUnavailable
	}
	turn, err := s.OpenCode.Observe(ctx, j)
	if s.failure == "foreign-session" {
		turn.SessionID = "ses_foreign"
	}
	if s.failure == "foreign-message" {
		turn.MessageID = "msg_foreign"
	}
	return turn, err
}
func (s staleRetirementEvidence) CheckContinuation(ctx context.Context, j Job) error {
	if s.failure == "idle-unavailable" {
		return c.ErrUnavailable
	}
	return s.OpenCode.CheckContinuation(ctx, j)
}

func TestNativeStaleTerminalRetirementBacksOffFailedRead(t *testing.T) {
	cfg, m, g, code, _ := nativeStaleAttemptFixture(t)
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	d.catalog, d.nextRefresh = g.catalog, time.Now().Add(time.Hour)
	g.err = c.ErrUnavailable
	if _, err = d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, d); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("remote read failure not reported", err)
	}
	if d.Ready() {
		t.Fatal("failed read retried without backoff")
	}
	for _, x := range d.history {
		if x.Phase != "blocked" || x.Job.ReconcileFailures != 1 || !x.Job.NextAttemptAt.After(time.Now()) {
			t.Fatal("transient read did not retain bounded retry", x)
		}
	}
}

type staleRetirementCatalog struct {
	*dispatchCatalog
	beforeRead func()
}

func (g staleRetirementCatalog) NativeTaskSnapshot(ctx context.Context, cfg Config, m NativeMaster, j Job, extra ...string) (NativeCatalog, error) {
	if g.beforeRead != nil {
		g.beforeRead()
	}
	return g.dispatchCatalog.NativeTaskSnapshot(ctx, cfg, m, j, extra...)
}

func TestNativeStaleTerminalRetirementRevalidatesUnderProducerLock(t *testing.T) {
	for _, change := range []string{"unchanged-current-input", "revoked-during-read", "paused-during-read", "placement-during-read"} {
		t.Run(change, func(t *testing.T) {
			cfg, m, g, code, x := nativeStaleAttemptFixture(t)
			reads := 0
			reader := staleRetirementCatalog{dispatchCatalog: g, beforeRead: func() {
				reads++
				release, lockErr := lockNativeProducer(cfg)
				if lockErr == nil {
					release()
					t.Error("revalidation ran without exclusive producer ownership")
				}
				if !errors.Is(lockErr, errNativeProducerBusy) {
					t.Error("unexpected producer lock evidence", lockErr)
				}
				switch change {
				case "unchanged-current-input":
					g.catalog.Tasks[0].PRs[0].MergeState = "CLEAN"
				case "revoked-during-read":
					m.authority.Valid, m.authority.State = false, "paused"
				case "paused-during-read":
					if err := atomicPrivate(filepath.Join(cfg.StateDir, "control.json"), []byte(`{"paused":true}`)); err != nil {
						t.Error(err)
					}
				case "placement-during-read":
					m.self.Placement.Generation++
				}
			}}
			d, err := NewNativeDispatcher(cfg, m, reader, code, &fakeCheckouts{}, RealClock{})
			if err != nil {
				t.Fatal(err)
			}
			d.catalog, d.nextRefresh = g.catalog, time.Now().Add(time.Hour)
			if !d.Ready() {
				t.Fatal("cached replacement did not nominate retirement")
			}
			if _, err = d.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err = consumeNativeStep(t, d); err == nil {
				t.Fatal("cached nomination authorized cancellation after current state changed")
			}
			saved, readErr := loadNativeExecution(cfg, x.Job.ID)
			if readErr != nil || saved.Phase != "blocked" || reads != 1 || code.calls != 0 {
				t.Fatal("current state was not safely revalidated", saved.Phase, readErr, reads)
			}
		})
	}
}
