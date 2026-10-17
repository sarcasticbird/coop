package hostcontrol

import (
	"context"
	"testing"
	"time"
)

func TestMCPLifecycleRevokedBeforeSecondPhase(t *testing.T) {
	e, caller, _ := fixtureEngine(t)
	r, _, err := e.ledger.Accept(caller, Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "restart", InterruptActive: true}, "fp", "incarnation-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r.State = "running"
	if err = e.ledger.Update(r); err != nil {
		t.Fatal(err)
	}
	r, err = e.ledger.Lookup(caller, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.control.cfg
	cfg.Callers = nil
	checkpoint := e.lifecycleCheckpoint(context.Background(), caller, r)
	if err = checkpoint("stop_pending"); err != nil {
		t.Fatal(err)
	}
	if err = e.policy.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	if err = checkpoint("start_pending"); err == nil {
		t.Fatal("revoked caller retained restart authority")
	}
}

func TestMCPLifecycleCancelledCheckpointHasNoSideEffect(t *testing.T) {
	e, caller, _ := fixtureEngine(t)
	r, _, err := e.ledger.Accept(caller, Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "stop"}, "fp", "incarnation-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r.State = "cancelled"
	if err = e.ledger.Update(r); err != nil {
		t.Fatal(err)
	}
	if err = e.lifecycleCheckpoint(context.Background(), caller, r)("stop_pending"); err == nil {
		t.Fatal("cancelled lifecycle allowed side effect")
	}
}

func TestMCPLifecycleRestartUsesExistingPreparation(t *testing.T) {
	e, caller, b := fixtureEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	defer func() { cancel(); <-done }()
	in := Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "restart"}
	a, err := e.Submit(ctx, caller, in)
	if err != nil {
		t.Fatal(err)
	}
	op := awaitOperation(t, e, caller, a.OperationID, "succeeded")
	if op.Phase != "started" || b.stops.Load() != 1 || b.starts.Load() != 1 {
		t.Fatalf("restart phase/side effects: %+v stops=%d starts=%d", op, b.stops.Load(), b.starts.Load())
	}
}
