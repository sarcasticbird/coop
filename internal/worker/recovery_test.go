package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPermanentReconciliationEscalatesAcrossRestarts(t *testing.T) {
	for _, wake := range []bool{false, true} {
		for _, submission := range []string{"submitting", "unknown", "accepted", "running"} {
			for _, expired := range []bool{false, true} {
				for _, status := range []int{404, 500} {
					t.Run(fmt.Sprintf("wake=%t/%s/expired=%t/http=%d", wake, submission, expired, status), func(t *testing.T) {
						var e *Engine
						var clock *fakeClock
						var wakes *FileWakeStore
						var code *fakeOpenCode
						if wake {
							e, code, clock, wakes = wakeEngineFixture(t)
							if _, err := QueuePing(e.Config, wakes, PingInput{Prompt: "check queue"}, clock.Now()); err != nil {
								t.Fatal(err)
							}
						} else {
							e, _, code, clock, _ = engineFixture(t)
						}
						code.running = true
						if wake {
							e.GitHub.(*wakeGitHub).snapshot.Cursor = map[string]string{}
						} else {
							e.GitHub.(*workflowGitHub).snapshot.Cursor = map[string]string{}
						}
						e.Store = NewFileStore(filepath.Join(t.TempDir(), "state"))
						tick(t, e, clock)
						j := code.last
						j.SubmissionState = submission
						j.Deadline = clock.Now().Add(time.Hour)
						if expired {
							j.Deadline = clock.Now().Add(-time.Second)
						}
						if wake {
							v, _ := wakes.Receipt(j.ID)
							v.Job = j
							if err := wakes.SaveReceipt(v); err != nil {
								t.Fatal(err)
							}
						} else if err := e.saveJob(j); err != nil {
							t.Fatal(err)
						}
						mutations := 0
						_, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
							if r.Method != "GET" {
								mutations++
							}
							if r.URL.Path == "/global/health" {
								writeJSON(w, ServerHealth{Healthy: true, Version: "1.18.18"})
								return
							}
							w.WriteHeader(status)
						})
						for i := 0; i < e.Config.Limits.MaxAttempts+3; i++ {
							// Every error crosses a process restart, including the last one.
							e = &Engine{Config: e.Config, Store: e.Store, GitHub: e.GitHub, Checkouts: e.Checkouts, OpenCode: client, Clock: clock}
							if wake {
								e.Wakes = NewWakeCoordinator(e.Config, wakes, e.GitHub, client, clock)
							}
							clock.now = clock.now.Add(e.Config.Limits.RetryBase + e.Config.OpenCode.ReconcileInterval)
							if err := e.Tick(context.Background()); err != nil {
								t.Fatal(err)
							}
						}
						var after Job
						if wake {
							v, _ := wakes.Receipt(j.ID)
							after = v.Job
							if v.Phase != "recovery" {
								t.Fatal(v)
							}
						} else {
							after = e.state.Jobs[j.ID]
						}
						if after.Phase != "recovery" || after.MessageID != j.MessageID || after.SessionID != j.SessionID || after.SubmissionState != submission || after.Attempts != j.Attempts || mutations != 0 || e.status.WaitReason != "reconciliation_required" {
							t.Fatalf("lost intent or replayed: %+v mutations=%d status=%+v", after, mutations, e.status)
						}
						if status == 404 && after.ReconcileError != "native_record_missing" {
							t.Fatal(after)
						}
					})
				}
			}
		}
	}
}

func TestRecoveryIntentRequiresLockAndExactIdentity(t *testing.T) {
	for _, wake := range []bool{false, true} {
		t.Run(fmt.Sprint(wake), func(t *testing.T) {
			c, _ := wakeFixture(t)
			c.StateDir = filepath.Join(t.TempDir(), "state")
			s := NewFileStore(c.StateDir)
			state := newState()
			j := Job{ID: BodyHash("intent"), Phase: "recovery", SessionID: "ses_owned", MessageID: "msg_owned", SubmissionState: "unknown", ReconcileFailures: 3}
			var wakes *FileWakeStore
			if wake {
				// Use the configured private directory for the real recovery API.
				wakes = NewWakeStore(filepath.Join(c.StateDir, "wake", "v1"))
				if _, err := QueuePing(c, wakes, PingInput{RequestID: j.ID, Prompt: "reconcile"}, time.Now()); err != nil {
					t.Fatal(err)
				}
				if err := wakes.SaveReceipt(WakeReceipt{Version: 1, RequestID: j.ID, Phase: "recovery", Job: j}); err != nil {
					t.Fatal(err)
				}
			} else {
				state.Jobs[j.ID] = j
			}
			if err := s.Save(state); err != nil {
				t.Fatal(err)
			}
			unlock, err := s.Lock()
			if err != nil {
				t.Fatal(err)
			}
			if err := RecoveryIntent(c, j.ID, j.SessionID, j.MessageID); err == nil {
				t.Fatal("recovered while worker locked")
			}
			unlock()
			if err := RecoveryIntent(c, j.ID, j.SessionID, "msg_latermanual"); err == nil {
				t.Fatal("accepted different turn")
			}
			if err := RecoveryIntent(c, j.ID, j.SessionID, j.MessageID); err != nil {
				t.Fatal(err)
			}
			var after Job
			if wake {
				v, err := wakes.Receipt(j.ID)
				if err != nil {
					t.Fatal(err)
				}
				after = v.Job
			} else {
				state, err = s.Load()
				if err != nil {
					t.Fatal(err)
				}
				after = state.Jobs[j.ID]
			}
			if after.Phase != "active" || after.MessageID != j.MessageID || after.SubmissionState != "unknown" || after.ReconcileFailures != 0 {
				t.Fatal(after)
			}
		})
	}
}

type observationOutage struct {
	*fakeOpenCode
	err error
}

func (c *observationOutage) Observe(ctx context.Context, j Job) (Turn, error) {
	if c.err != nil {
		return Turn{}, c.err
	}
	return c.fakeOpenCode.Observe(ctx, j)
}

func TestReconciliationOutageRecoveryPreservesExternalArtifacts(t *testing.T) {
	for _, wake := range []bool{false, true} {
		t.Run(fmt.Sprint(wake), func(t *testing.T) {
			var e *Engine
			var code *fakeOpenCode
			var clock *fakeClock
			var wakes *FileWakeStore
			if wake {
				e, code, clock, wakes = wakeEngineFixture(t)
			} else {
				e, _, code, clock, _ = engineFixture(t)
			}
			e.Config.StateDir = filepath.Join(t.TempDir(), "state")
			e.Store = NewFileStore(e.Config.StateDir)
			if wake {
				wakes = NewWakeStore(filepath.Join(e.Config.StateDir, "wake", "v1"))
				e.Wakes.Store = wakes
				if _, err := QueuePing(e.Config, wakes, PingInput{Prompt: "check"}, clock.Now()); err != nil {
					t.Fatal(err)
				}
			}
			code.running, code.lost = true, true
			if wake {
				e.GitHub.(*wakeGitHub).snapshot.Cursor = map[string]string{}
			} else {
				e.GitHub.(*workflowGitHub).snapshot.Cursor = map[string]string{}
			}
			tick(t, e, clock)
			j := code.last
			outage := &observationOutage{fakeOpenCode: code, err: &OpenCodeError{Category: "transport_failed"}}
			e.OpenCode = outage
			if wake {
				e.Wakes.OpenCode = outage
			}
			step := func() {
				clock.now = clock.now.Add(e.Config.Limits.RetryBase + e.Config.OpenCode.ReconcileInterval)
				tick(t, e, clock)
			}
			step()
			outage.err = nil
			step()
			var after Job
			if wake {
				v, _ := wakes.Receipt(j.ID)
				after = v.Job
			} else {
				after = e.state.Jobs[j.ID]
			}
			if after.ReconcileFailures != 0 || after.Phase != "active" || code.calls != 1 {
				t.Fatal(after)
			}
			outage.err = &OpenCodeError{Category: "request_failed", Status: 404}
			for i := 0; i < e.Config.Limits.MaxAttempts; i++ {
				step()
			}
			if err := RecoveryIntent(e.Config, j.ID, j.SessionID, j.MessageID); err != nil {
				t.Fatal(err)
			}
			// The lost native response can coexist with a completed external artifact.
			// Recovery must reconcile it, not create another PR or reply.
			if wake {
				g := e.GitHub.(*wakeGitHub)
				g.replies[j.ID] = Comment{NodeID: "reply", URL: "https://github.com/orgs/planning/discussions/1#reply"}
			} else {
				g := e.GitHub.(*workflowGitHub)
				g.snapshot.PRs = []PR{{URL: "https://github.com/code/repo/pull/1", TaskURL: j.TaskURL, GoalURL: j.GoalURL, Branch: j.Branch, Author: e.Config.Implementer}}
			}
			code.running = false
			e = &Engine{Config: e.Config, Store: e.Store, GitHub: e.GitHub, Checkouts: e.Checkouts, OpenCode: code, Clock: clock}
			if wake {
				e.Wakes = NewWakeCoordinator(e.Config, wakes, e.GitHub, code, clock)
			}
			step()
			if wake {
				v, _ := wakes.Receipt(j.ID)
				after = v.Job
			} else {
				after = e.state.Jobs[j.ID]
			}
			if after.Phase != "completed" || after.MessageID != j.MessageID || code.calls != 1 || code.aborts != 0 {
				t.Fatalf("replayed or lost recovered artifact: %+v calls=%d aborts=%d", after, code.calls, code.aborts)
			}
		})
	}
}

func TestAbortFailureEscalatesWithoutLosingOwnership(t *testing.T) {
	for _, path := range []string{"pause", "deadline", "wake_pause", "wake_deadline"} {
		t.Run(path, func(t *testing.T) {
			e, code, clock, wakes := wakeEngineFixture(t)
			wake := strings.HasPrefix(path, "wake_")
			if wake {
				if _, err := QueuePing(e.Config, wakes, PingInput{Prompt: "check"}, clock.Now()); err != nil {
					t.Fatal(err)
				}
			} else {
				_, snapshot, _ := policyFixture(t)
				e.GitHub.(*wakeGitHub).snapshot = snapshot
			}
			code.running = true
			tick(t, e, clock)
			j := code.last
			code.AbortError = &OpenCodeError{Category: "turn_ownership_unknown"}
			if strings.HasSuffix(path, "pause") {
				e.Store.(*memoryStore).paused = true
			} else {
				clock.now = j.Deadline
			}
			for i := 0; i < e.Config.Limits.MaxAttempts+2; i++ {
				clock.now = clock.now.Add(e.Config.Limits.RetryBase + e.Config.OpenCode.ReconcileInterval)
				_ = e.Tick(context.Background())
			}
			var after Job
			if wake {
				v, _ := wakes.Receipt(j.ID)
				after = v.Job
			} else {
				after = e.state.Jobs[j.ID]
			}
			if after.Phase != "recovery" || after.MessageID != j.MessageID || code.aborts != e.Config.Limits.MaxAttempts || code.calls != 1 {
				t.Fatalf("unbounded abort or lost ownership: %+v aborts=%d", after, code.aborts)
			}
		})
	}
}

func TestNeverPostedIntentEscalatesWithoutReplay(t *testing.T) {
	for _, wake := range []bool{false, true} {
		t.Run(fmt.Sprint(wake), func(t *testing.T) {
			e, code, clock, wakes := wakeEngineFixture(t)
			var saved Job
			if wake {
				q, err := QueuePing(e.Config, wakes, PingInput{Prompt: "never posted"}, clock.Now())
				if err != nil {
					t.Fatal(err)
				}
				e.Wakes.Store = &checkpointWakeStore{WakeStore: wakes, commit: true}
				if err := e.Wakes.Tick(context.Background(), Snapshot{Complete: true}); err == nil {
					t.Fatal("checkpoint unexpectedly succeeded")
				}
				v, err := wakes.Receipt(q.ID)
				if err != nil {
					t.Fatal(err)
				}
				saved = v.Job
			} else {
				_, snapshot, _ := policyFixture(t)
				snapshot.Cursor = map[string]string{}
				e.GitHub.(*wakeGitHub).snapshot = snapshot
				e.Store = &checkpointStore{Store: NewFileStore(filepath.Join(t.TempDir(), "state")), commit: true, fail: func(s State) bool {
					for _, j := range s.Jobs {
						if j.Phase == "active" && j.SubmissionState == "submitting" {
							return true
						}
					}
					return false
				}}
				if err := e.Tick(context.Background()); !errors.Is(err, errCheckpoint) {
					t.Fatal(err)
				}
				saved, _ = e.active()
			}
			if code.calls != 0 || saved.SubmissionState != "submitting" {
				t.Fatal("fixture posted input")
			}
			_, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("unexpected mutation: %s", r.Method)
				}
				if r.URL.Path == "/global/health" {
					writeJSON(w, ServerHealth{Healthy: true, Version: "1.18.18"})
					return
				}
				w.WriteHeader(404)
			})
			for i := 0; i < e.Config.Limits.MaxAttempts+2; i++ {
				e = &Engine{Config: e.Config, Store: e.Store, GitHub: e.GitHub, Checkouts: e.Checkouts, OpenCode: client, Clock: clock}
				if wake {
					e.Wakes = NewWakeCoordinator(e.Config, wakes, e.GitHub, client, clock)
				}
				clock.now = clock.now.Add(e.Config.Limits.RetryBase + e.Config.OpenCode.ReconcileInterval)
				if err := e.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			var after Job
			if wake {
				v, _ := wakes.Receipt(saved.ID)
				after = v.Job
			} else {
				after = e.state.Jobs[saved.ID]
			}
			if after.Phase != "recovery" || after.MessageID != saved.MessageID || code.calls != 0 {
				t.Fatal(after)
			}
		})
	}
}
