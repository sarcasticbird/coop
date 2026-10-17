package hostcontrol

import (
	"context"
	"encoding/json"
	"github.com/sarcasticbird/coop/internal/agent"
	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/lock"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type reviewBackend struct {
	*execBackend
	incarnation string
	fingerprint string
}

func (b *reviewBackend) Inspect(ctx context.Context, s workspace.Spec) (workspace.Info, error) {
	v, err := b.execBackend.Inspect(ctx, s)
	v.ChildContainerID = b.incarnation
	v.Fingerprint = b.fingerprint
	return v, err
}
func TestReviewRestartRejectsReplacementIncarnation(t *testing.T) {
	e, caller, base := fixtureEngine(t)
	base.state.Store(int32(runtime.StateStopped))
	replacement := &reviewBackend{base, "replacement-incarnation", "fp"}
	load := e.control.loadAgent
	e.control.loadAgent = func(name string) (*agent.Session, error) {
		s, err := load(name)
		s.Controller.Backend = replacement
		return s, err
	}
	in := Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "restart", InterruptActive: true}
	r, _, err := e.ledger.Accept(caller, in, "fp", "original-incarnation", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r.State = "indeterminate"
	r.Dispatched = true
	r.Phase = "stopped"
	if err = e.ledger.Update(r); err != nil {
		t.Fatal(err)
	}
	r, _ = e.ledger.Lookup(caller, r.ID)
	e.reconcileLifecycle(context.Background(), r)
	r, _ = e.ledger.Lookup(caller, r.ID)
	if r.State == "accepted" {
		e.execute(context.Background(), r)
	}
	if base.starts.Load() != 0 {
		t.Fatalf("recovered restart started replacement incarnation: starts=%d", base.starts.Load())
	}
}
func TestReviewMaintenanceRefusalReleasesWorkspace(t *testing.T) {
	e, caller, base := fixtureEngine(t)
	base.state.Store(int32(runtime.StateStopped))
	backend := &reviewBackend{base, "incarnation-1", "old-fp"}
	load := e.control.loadAgent
	e.control.loadAgent = func(name string) (*agent.Session, error) {
		s, err := load(name)
		s.Controller.Backend = backend
		return s, err
	}
	now := time.Now()
	in := Mutation{Key: testKey(now), Workspace: e.control.reference("agent:robin"), Action: "start"}
	accepted, err := e.Submit(context.Background(), caller, in)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := e.ledger.Lookup(caller, accepted.OperationID)
	e.execute(context.Background(), r)
	r, _ = e.ledger.Lookup(caller, accepted.OperationID)
	if base.starts.Load() != 0 {
		t.Fatal("fixture unexpectedly started")
	}
	// A local maintenance action repairs the runtime's applied fingerprint.
	backend.fingerprint = "fp"
	e.reconcileLifecycle(context.Background(), r)
	in.Key = testKey(now.Add(time.Millisecond))
	_, err = e.Submit(context.Background(), caller, in)
	if err != nil {
		t.Fatalf("no side effect occurred, but repaired workspace remains blocked: %v, state=%s phase=%s", err, r.State, r.Phase)
	}
}

func TestReviewAcceptedCancellationSurvivesCrash(t *testing.T) {
	e, caller, base := fixtureEngine(t)
	now := time.Now()
	in := Mutation{Key: testKey(now), Workspace: e.control.reference("agent:robin"), Action: "exec", Argv: []string{"true"}, Timeout: time.Minute}
	a, err := e.Submit(context.Background(), caller, in)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := e.ledger.Lookup(caller, a.OperationID)
	// Crash immediately after CancelRequest durably accepts its request.
	cancelRecord, _, err := e.ledger.Accept(caller, Mutation{Key: testKey(now.Add(time.Millisecond)), Workspace: in.Workspace, Action: "cancel", TargetOperation: a.OperationID}, target.Fingerprint, target.Incarnation, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := NewEngine(e.host, e.ledger, e.policy, e.control)
	if err != nil {
		t.Fatal(err)
	}
	recovered.execute(context.Background(), cancelRecord)
	recovered.execute(context.Background(), target)
	saved, _ := e.ledger.Lookup(caller, cancelRecord.ID)
	if base.calls.Load() != 0 {
		t.Fatalf("durably accepted cancellation was lost and target dispatched; cancellation state=%s category=%s", saved.State, saved.Outcome.Category)
	}
}

type reviewActivityBackend struct {
	*execBackend
	statusRead chan struct{}
	active     atomic.Bool
}

func (b *reviewActivityBackend) Exec(ctx context.Context, n, cwd string, args []string, in io.Reader, out, errout io.Writer) error {
	if len(args) > 1 && args[0] == "/usr/local/libexec/coop-worker" && args[1] == "status" {
		status := map[string]any{"enabled": true, "state": "idle", "heartbeat": time.Now().UTC()}
		if b.active.Load() {
			status["active_task"] = "native-task"
		}
		err := json.NewEncoder(out).Encode(status)
		select {
		case b.statusRead <- struct{}{}:
		default:
		}
		return err
	}
	return b.execBackend.Exec(ctx, n, cwd, args, in, out, errout)
}
func TestReviewActivityRecheckedAfterLockWait(t *testing.T) {
	e, caller, base := fixtureEngine(t)
	backend := &reviewActivityBackend{execBackend: base, statusRead: make(chan struct{}, 1)}
	load := e.control.loadAgent
	e.control.loadAgent = func(name string) (*agent.Session, error) {
		s, err := load(name)
		s.Controller.Backend = backend
		s.Runner = backend
		s.Spec.Profile.Services = map[string]config.Service{"worker": {Command: []string{"true"}}}
		return s, err
	}
	a, err := e.Submit(context.Background(), caller, Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := e.ledger.Lookup(caller, a.OperationID)
	release, err := lock.Acquire("coop-agent-robin", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan struct{})
	go func() { defer close(done); e.execute(context.Background(), r) }()
	select {
	case <-backend.statusRead:
	case <-time.After(time.Second):
		t.Fatal("no activity read")
	}
	backend.active.Store(true)
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("operation did not finish")
	}
	if base.stops.Load() != 0 {
		t.Fatalf("stopped a now-active worker without interrupt_active after waiting for lifecycle lock")
	}
}

func TestLifecycleRejectsReplacementAtEverySavedPhase(t *testing.T) {
	for _, phase := range []string{"stop_pending", "stopped", "start_pending", "started"} {
		t.Run(phase, func(t *testing.T) {
			e, c, b := fixtureEngine(t)
			replacement := &reviewBackend{b, "replacement", "fp"}
			if phase != "started" {
				b.state.Store(int32(runtime.StateStopped))
			}
			load := e.control.loadAgent
			e.control.loadAgent = func(name string) (*agent.Session, error) {
				s, err := load(name)
				s.Controller.Backend = replacement
				return s, err
			}
			r, _, err := e.ledger.Accept(c, Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "restart", InterruptActive: true}, "fp", "original", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			r.State = "indeterminate"
			r.Dispatched = true
			r.Phase = phase
			r.PhaseIncarnation = "original"
			if err = e.ledger.Update(r); err != nil {
				t.Fatal(err)
			}
			r, _ = e.ledger.Lookup(c, r.ID)
			e.reconcileLifecycle(context.Background(), r)
			saved, _ := e.ledger.Lookup(c, r.ID)
			if saved.State != "indeterminate" || saved.HoldResolved || b.starts.Load() != 0 {
				t.Fatalf("replacement accepted at %s: %+v", phase, saved)
			}
			if phase == "start_pending" {
				r.State = "running"
				if err = e.ledger.Update(r); err != nil {
					t.Fatal(err)
				}
				r, _ = e.ledger.Lookup(c, r.ID)
				if err = e.lifecycleCheckpoint(context.Background(), c, r)("start_pending"); err == nil {
					t.Fatal("replacement passed locked start dispatch check")
				}
			}
		})
	}
}
func TestCancellationRecoveryAcrossEveryCheckpoint(t *testing.T) {
	for _, phase := range []string{"accepted", "cancel_dispatch_pending", "target_marked"} {
		t.Run(phase, func(t *testing.T) {
			e, c, b := fixtureEngine(t)
			now := time.Now()
			a, err := e.Submit(context.Background(), c, Mutation{Key: testKey(now), Workspace: e.control.reference("agent:robin"), Action: "exec", Argv: []string{"true"}, Timeout: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			target, _ := e.ledger.Lookup(c, a.OperationID)
			r, _, err := e.ledger.Accept(c, Mutation{Key: testKey(now.Add(time.Millisecond)), Workspace: target.Input.Workspace, Action: "cancel", TargetOperation: target.ID}, target.Fingerprint, target.Incarnation, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if phase != "accepted" {
				r.State = "running"
				if err = e.ledger.Update(r); err != nil {
					t.Fatal(err)
				}
				if err = e.ledger.Checkpoint(c, r.ID, "cancel_dispatch_pending"); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "target_marked" {
				if _, err = e.Cancel(context.Background(), c, target.ID); err != nil {
					t.Fatal(err)
				}
			}
			next, err := NewEngine(e.host, e.ledger, e.policy, e.control)
			if err != nil {
				t.Fatal(err)
			}
			next.execute(context.Background(), target)
			saved, _ := e.ledger.Lookup(c, r.ID)
			target, _ = e.ledger.Lookup(c, target.ID)
			if saved.State != "succeeded" || target.State != "cancelled" || b.calls.Load() != 0 {
				t.Fatalf("cancellation crash phase %s lost: %s %s", phase, saved.State, target.State)
			}
		})
	}
}
func TestRecoveredCancellationUsesTargetCapability(t *testing.T) {
	e, c, b := fixtureEngine(t)
	now := time.Now()
	a, err := e.Submit(context.Background(), c, Mutation{Key: testKey(now), Workspace: e.control.reference("agent:robin"), Action: "exec", Argv: []string{"true"}, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := e.ledger.Lookup(c, a.OperationID)
	r, _, err := e.ledger.Accept(c, Mutation{Key: testKey(now.Add(time.Millisecond)), Workspace: target.Input.Workspace, Action: "cancel", TargetOperation: target.ID}, target.Fingerprint, target.Incarnation, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.control.cfg
	cfg.Callers[0].Capabilities = []string{"read", "lifecycle"}
	if err = e.policy.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	next, err := NewEngine(e.host, e.ledger, e.policy, e.control)
	if err != nil {
		t.Fatal(err)
	}
	next.execute(context.Background(), target)
	saved, _ := e.ledger.Lookup(c, r.ID)
	if saved.State != "failed" || saved.Outcome.Category != "forbidden" || b.calls.Load() != 0 {
		t.Fatal("recovered cancellation bypassed target capability")
	}
}
