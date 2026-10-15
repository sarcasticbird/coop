package worker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Complete a real background Snapshot call before letting Tick consume it.
// This exercises Run's poll-result path without scheduler timing assumptions.
func failedArtifactPoll(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.initialize(); err != nil {
		t.Fatal(err)
	}
	e.asynchronous = true
	e.nextPoll = time.Time{}
	if err := e.refresh(context.Background()); !errors.Is(err, errPollPending) {
		t.Fatal(err)
	}
	select {
	case result := <-e.poll:
		if errorCategory(result.err) != "api_error" {
			t.Fatal(result.err)
		}
		e.poll <- result
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot poll did not complete")
	}
	if err := e.Tick(context.Background()); errorCategory(err) != "api_error" {
		t.Fatalf("poll error = %v", err)
	}
}

func TestAsynchronousArtifactPollFailureEscalatesAcrossRestarts(t *testing.T) {
	for _, wake := range []bool{false, true} {
		for _, knownTerminal := range []bool{false, true} {
			t.Run(fmt.Sprintf("wake=%t/known-terminal=%t", wake, knownTerminal), func(t *testing.T) {
				e, code, clock, wakes := wakeEngineFixture(t)
				g := e.GitHub.(*wakeGitHub)
				if wake {
					if _, err := QueuePing(e.Config, wakes, PingInput{Prompt: "artifact poll"}, clock.Now()); err != nil {
						t.Fatal(err)
					}
				} else {
					_, g.snapshot, _ = policyFixture(t)
				}
				g.snapshot.Cursor = map[string]string{}
				e.Store = NewFileStore(filepath.Join(t.TempDir(), "state"))
				code.running = true
				tick(t, e, clock)
				j := code.last
				instance := e.state.InstanceID
				code.running = false
				if knownTerminal {
					j.TerminalAt = clock.Now()
					j.SubmissionState = "completed"
					if wake {
						v, _ := wakes.Receipt(j.ID)
						v.Job = j
						if err := wakes.SaveReceipt(v); err != nil {
							t.Fatal(err)
						}
					} else if err := e.saveJob(j); err != nil {
						t.Fatal(err)
					}
				}
				g.pollError = ghError("api_error")
				for i := 1; i <= e.Config.Limits.MaxAttempts; i++ {
					e = &Engine{Config: e.Config, Store: e.Store, GitHub: g, Checkouts: e.Checkouts, OpenCode: code, Clock: clock, asynchronous: true}
					if wake {
						e.Wakes = NewWakeCoordinator(e.Config, wakes, g, code, clock)
					}
					clock.now = clock.now.Add(e.Config.Limits.RetryBase + e.Config.OpenCode.ReconcileInterval)
					failedArtifactPoll(t, e)
					var after Job
					if wake {
						v, _ := wakes.Receipt(j.ID)
						after = v.Job
					} else {
						after = e.state.Jobs[j.ID]
					}
					if after.ReconcileFailures != i || after.ReconcileError != "api_error" || after.TerminalAt.IsZero() || after.MessageID != j.MessageID || after.SessionID != j.SessionID || after.Checkout != j.Checkout || after.Branch != j.Branch || after.Deadline != j.Deadline || after.SubmittedAt != j.SubmittedAt || after.Attempts != j.Attempts || e.state.InstanceID != instance {
						t.Fatalf("lost durable artifact reconciliation: %+v", after)
					}
					if i == e.Config.Limits.MaxAttempts && after.Phase != "recovery" {
						t.Fatal(after)
					}
				}
				polls := g.polls
				// Restart once more: neither new input nor another API/abort loop
				// may escape the durable recovery hold.
				e = &Engine{Config: e.Config, Store: e.Store, GitHub: g, Checkouts: e.Checkouts, OpenCode: code, Clock: clock, asynchronous: true}
				if wake {
					e.Wakes = NewWakeCoordinator(e.Config, wakes, g, code, clock)
				}
				tick(t, e, clock)
				if e.status.WaitReason != "reconciliation_required" || e.status.RecoveryIntent == nil || g.polls != polls || code.calls != 1 || code.aborts != 0 {
					t.Fatalf("recovery did not hold dispatch: %+v", e.status)
				}
			})
		}
	}
}

func TestAsynchronousArtifactPollRecoversWithoutReplay(t *testing.T) {
	for _, wake := range []bool{false, true} {
		t.Run(fmt.Sprint(wake), func(t *testing.T) {
			e, code, clock, wakes := wakeEngineFixture(t)
			g := e.GitHub.(*wakeGitHub)
			if wake {
				if _, err := QueuePing(e.Config, wakes, PingInput{Prompt: "recover poll"}, clock.Now()); err != nil {
					t.Fatal(err)
				}
			} else {
				_, g.snapshot, _ = policyFixture(t)
			}
			g.snapshot.Cursor = map[string]string{}
			e.Store = NewFileStore(filepath.Join(t.TempDir(), "state"))
			code.running = true
			tick(t, e, clock)
			j := code.last
			code.running = false
			g.pollError = ghError("api_error")
			failedArtifactPoll(t, e)
			// Ticks inside the persisted API/reconciliation backoff must not
			// consume the remaining budget, including after restart.
			e = &Engine{Config: e.Config, Store: e.Store, GitHub: g, Checkouts: e.Checkouts, OpenCode: code, Clock: clock, asynchronous: true}
			if wake {
				e.Wakes = NewWakeCoordinator(e.Config, wakes, g, code, clock)
			}
			if err := e.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			var after Job
			if wake {
				v, _ := wakes.Receipt(j.ID)
				after = v.Job
			} else {
				after = e.state.Jobs[j.ID]
			}
			if after.ReconcileFailures != 1 {
				t.Fatal(after)
			}
			g.pollError = nil
			if wake {
				g.replies[j.ID] = Comment{NodeID: "saved_reply", URL: "https://github.com/orgs/planning/discussions/1#reply"}
			} else {
				g.snapshot.PRs = []PR{{URL: "https://github.com/code/repo/pull/1", TaskURL: j.TaskURL, GoalURL: j.GoalURL, Branch: j.Branch, Author: e.Config.Implementer}}
			}
			clock.now = clock.now.Add(e.Config.Limits.RetryBase + e.Config.OpenCode.ReconcileInterval)
			e.nextPoll = time.Time{}
			if err := e.refresh(context.Background()); !errors.Is(err, errPollPending) {
				t.Fatal(err)
			}
			select {
			case result := <-e.poll:
				if result.err != nil {
					t.Fatal(result.err)
				}
				e.poll <- result
			case <-time.After(5 * time.Second):
				t.Fatal("healthy poll did not complete")
			}
			if err := e.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if wake {
				v, _ := wakes.Receipt(j.ID)
				after = v.Job
				if v.ReplyNodeID != "saved_reply" {
					t.Fatal(v)
				}
			} else {
				after = e.state.Jobs[j.ID]
			}
			if after.Phase != "completed" || after.ReconcileFailures != 0 || after.MessageID != j.MessageID || code.calls != 1 || code.aborts != 0 {
				t.Fatalf("transient poll outage lost artifact or replayed: %+v", after)
			}
		})
	}
}

func TestAsynchronousCatalogOutageDoesNotEscalateWithoutTerminalTurn(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			e, g, code, clock, _ := engineFixture(t)
			g.snapshot.Cursor = map[string]string{}
			e.Store = NewFileStore(filepath.Join(t.TempDir(), "state"))
			code.running = true
			if active {
				tick(t, e, clock)
			}
			backend := &wakeGitHub{workflowGitHub: g, pollError: ghError("api_error")}
			e.GitHub = backend
			for i := 0; i < e.Config.Limits.MaxAttempts+2; i++ {
				e = &Engine{Config: e.Config, Store: e.Store, GitHub: backend, Checkouts: e.Checkouts, OpenCode: code, Clock: clock, asynchronous: true}
				clock.now = clock.now.Add(e.Config.Limits.RetryBase)
				failedArtifactPoll(t, e)
			}
			for _, j := range e.state.Jobs {
				if j.ReconcileFailures != 0 || j.Phase == "recovery" {
					t.Fatal(j)
				}
			}
			want := 0
			if active {
				want = 1
			}
			if code.calls != want || code.aborts != 0 {
				t.Fatal("catalog outage mutated native turn")
			}
		})
	}
}

func TestAsynchronousRunningObservationResetsNativeFailureAcrossRestarts(t *testing.T) {
	for _, wake := range []bool{false, true} {
		t.Run(fmt.Sprint(wake), func(t *testing.T) {
			e, code, clock, wakes := wakeEngineFixture(t)
			g := e.GitHub.(*wakeGitHub)
			if wake {
				if _, err := QueuePing(e.Config, wakes, PingInput{Prompt: "transient native outage"}, clock.Now()); err != nil {
					t.Fatal(err)
				}
			} else {
				_, g.snapshot, _ = policyFixture(t)
			}
			g.snapshot.Cursor = map[string]string{}
			e.Store = NewFileStore(filepath.Join(t.TempDir(), "state"))
			code.running = true
			tick(t, e, clock)
			j := code.last
			instance := e.state.InstanceID
			g.pollError = ghError("api_error")
			outage := &observationOutage{fakeOpenCode: code}
			for i := 0; i < e.Config.Limits.MaxAttempts+2; i++ {
				for _, nativeError := range []bool{true, false} {
					outage.err = nil
					if nativeError {
						outage.err = &OpenCodeError{Category: "transport_failed"}
					}
					e = &Engine{Config: e.Config, Store: e.Store, GitHub: g, Checkouts: e.Checkouts, OpenCode: outage, Clock: clock, asynchronous: true}
					if wake {
						e.Wakes = NewWakeCoordinator(e.Config, wakes, g, outage, clock)
					}
					clock.now = clock.now.Add(e.Config.Limits.RetryBase + e.Config.OpenCode.ReconcileInterval)
					failedArtifactPoll(t, e)
					// Read the durable record, not just the live engine copy.
					state, err := e.Store.Load()
					if err != nil {
						t.Fatal(err)
					}
					after := state.Jobs[j.ID]
					if wake {
						v, err := wakes.Receipt(j.ID)
						if err != nil {
							t.Fatal(err)
						}
						if v.Phase != "running" {
							t.Fatal(v)
						}
						after = v.Job
						if !nativeError && v.Reason != "" {
							t.Fatal(v)
						}
					}
					if after.Phase != "active" || !after.TerminalAt.IsZero() || after.MessageID != j.MessageID || after.SessionID != j.SessionID || state.InstanceID != instance || code.calls != 1 || code.aborts != 0 {
						t.Fatalf("transient outage changed intent: %+v", after)
					}
					if nativeError {
						if after.ReconcileFailures != 1 || after.ReconcileError != "transport_failed" {
							t.Fatal(after)
						}
					} else if after.ReconcileFailures != 0 || after.ReconcileError != "" || after.SubmissionState != "running" || after.WaitReason != "" || !after.NextAttemptAt.IsZero() {
						t.Fatalf("successful observation did not durably reset failure: %+v", after)
					}
				}
			}
		})
	}
}
