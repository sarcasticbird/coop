package hostcontrol

import (
	"context"
	"encoding/json"
	"github.com/sarcasticbird/coop/internal/agent"
	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCancellationHasReservedLedgerCapacity(t *testing.T) {
	e, c, _ := fixtureEngine(t)
	e.ledger.limits = Limits{Unresolved: 1, Records: 2, Terminal: 1, OutputBytes: 8}
	a, err := e.Submit(context.Background(), c, Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "exec", Argv: []string{"true"}, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.CancelRequest(context.Background(), c, a.OperationID, testKey(time.Now())); err != nil {
		t.Fatalf("full ledger prevented cancellation: %v", err)
	}
	target, _ := e.ledger.Lookup(c, a.OperationID)
	if target.State != "cancelled" {
		t.Fatal("target not cancelled")
	}
	if len(e.ledger.all()) > 2 {
		t.Fatal("cancellation exceeded total record bound")
	}
}
func TestStopPendingRecoveryUsesStoppedRuntimeEvidence(t *testing.T) {
	for _, action := range []string{"stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			e, c, b := fixtureEngine(t)
			b.state.Store(int32(runtime.StateStopped))
			r, _, err := e.ledger.Accept(c, Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: action}, "fp", "incarnation-1", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			r.State = "indeterminate"
			r.Dispatched = true
			r.Phase = "stop_pending"
			if err = e.ledger.Update(r); err != nil {
				t.Fatal(err)
			}
			r, _ = e.ledger.Lookup(c, r.ID)
			e.reconcileLifecycle(context.Background(), r)
			r, _ = e.ledger.Lookup(c, r.ID)
			want := "succeeded"
			if action == "restart" {
				want = "accepted"
			}
			if r.State != want || r.Phase != "stopped" {
				t.Fatalf("proven stopped phase held: %s %s", r.State, r.Phase)
			}
		})
	}
}
func TestAppliedWorkerRequiresInterruptionAfterDesiredRemoval(t *testing.T) {
	e, c, base := fixtureEngine(t)
	b := &reviewActivityBackend{execBackend: base, statusRead: make(chan struct{}, 2)}
	b.active.Store(true)
	load := e.control.loadAgent
	e.control.loadAgent = func(n string) (*agent.Session, error) {
		s, err := load(n)
		s.Runner = b
		s.Controller.Backend = b
		return s, err
	}
	rec, err := e.control.store.Load(workspace.Agent, "robin")
	if err != nil {
		t.Fatal(err)
	}
	rec.Applied.Services = map[string]config.Service{"worker": {Command: []string{"true"}}}
	if err = e.control.store.Save(rec); err != nil {
		t.Fatal(err)
	}
	a, err := e.Submit(context.Background(), c, Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := e.ledger.Lookup(c, a.OperationID)
	e.execute(context.Background(), r)
	if base.stops.Load() != 0 {
		t.Fatal("desired worker removal hid active applied worker")
	}
}
func TestInventoryFiltersBeforeWorkspaceResolution(t *testing.T) {
	e, c, _ := fixtureEngine(t)
	root := privateTemp(t)
	t.Setenv("XDG_CONFIG_HOME", root)
	if err := os.Mkdir(filepath.Join(root, "coop"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "coop/coop.toml"), []byte("[profiles.robin]\ncommand=['true']\n[profiles.hawk]\ncommand=['true']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	load := e.control.loadAgent
	e.control.loadAgent = func(n string) (*agent.Session, error) {
		if n == "hawk" {
			return nil, fail("bad_unrelated_profile")
		}
		return load(n)
	}
	rows, err := e.control.List(context.Background(), func(sel string) bool { return e.policy.Authorize(c, sel, "read") == nil && sel == "agent:robin" })
	if err != nil || len(rows) != 1 {
		t.Fatalf("ungranted workspace broke list: %v", err)
	}
}

type recoveringWakeBackend struct {
	*execBackend
	observed map[string]int
	ready    string
}

func (b *recoveringWakeBackend) Exec(_ context.Context, _ string, _ string, args []string, _ io.Reader, out, _ io.Writer) error {
	if len(args) == 6 && args[1] == "requests" {
		id := args[5]
		b.observed[id]++
		if id == b.ready {
			return json.NewEncoder(out).Encode([]map[string]string{{"id": id, "phase": "queued"}})
		}
		return fail("unavailable")
	}
	return fail("unexpected")
}
func TestReconciliationRotatesPastUnobservableRecords(t *testing.T) {
	e, c, base := fixtureEngine(t)
	b := &recoveringWakeBackend{execBackend: base, observed: map[string]int{}}
	load := e.control.loadAgent
	e.control.loadAgent = func(n string) (*agent.Session, error) {
		s, err := load(n)
		s.Spec.Profile = configProfileForWorker()
		s.Runner = b
		return s, err
	}
	var last OperationRecord
	for i := range 5 {
		now := time.Now().Add(time.Duration(i) * time.Millisecond)
		r, _, err := e.ledger.Accept(c, Mutation{Key: testKey(now), Workspace: e.control.reference("agent:robin"), Action: "ping"}, "fp", "incarnation-1", now)
		if err != nil {
			t.Fatal(err)
		}
		r.State = "indeterminate"
		r.Dispatched = true
		if err = e.ledger.Update(r); err != nil {
			t.Fatal(err)
		}
		last = r
	}
	b.ready = last.WakeID
	e.reconcile(context.Background())
	e.reconcile(context.Background())
	saved, _ := e.ledger.Lookup(c, last.ID)
	if saved.State != "succeeded" {
		t.Fatal("old unresolved operations starved observable later wake")
	}
}
