package hostcontrol

import (
	"context"
	"github.com/sarcasticbird/coop/internal/agent"
	"github.com/sarcasticbird/coop/internal/credential"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
	"io"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type execBackend struct {
	state  atomic.Int32
	stops  atomic.Int32
	starts atomic.Int32
	calls  atomic.Int32
	block  <-chan struct{}
	broken bool
}

func (f *execBackend) Inspect(_ context.Context, s workspace.Spec) (workspace.Info, error) {
	return workspace.Info{Name: s.RuntimeName, State: runtime.State(f.state.Load()), Fingerprint: "fp", ChildContainerID: "incarnation-1"}, nil
}
func (f *execBackend) Create(context.Context, workspace.Spec) error { return nil }
func (f *execBackend) Start(context.Context, workspace.Spec) error {
	f.starts.Add(1)
	f.state.Store(int32(runtime.StateRunning))
	return nil
}
func (f *execBackend) Stop(context.Context, workspace.Spec) error {
	f.stops.Add(1)
	f.state.Store(int32(runtime.StateStopped))
	return nil
}
func (f *execBackend) Remove(context.Context, workspace.Spec) error { return nil }
func (f *execBackend) Apply(context.Context, workspace.Spec) error  { return nil }
func (f *execBackend) Attach(context.Context, string, string, []string) error {
	return fail("test_no_pty")
}
func (f *execBackend) Exec(ctx context.Context, _ string, _ string, args []string, in io.Reader, out, errout io.Writer) error {
	if len(args) > 0 && args[0] != "/usr/local/libexec/coop-env" {
		if args[0] == "sh" {
			_, e := io.WriteString(out, "yes")
			return e
		}
		return nil
	}
	f.calls.Add(1)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.broken {
		return fail("lost_transport")
	}
	args = args[1:]
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = errout
	return cmd.Run()
}
func fixtureEngine(t *testing.T) (*Engine, Caller, *execBackend) {
	t.Helper()
	cfg := fixtureConfig(t)
	root := privateTemp(t)
	t.Setenv("XDG_STATE_HOME", root)
	_, close, e := OpenHost(cfg.StateDir, cfg.Label)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = close() })
	c, e := NewControl(cfg)
	if e != nil {
		t.Fatal(e)
	}
	backend := &execBackend{}
	backend.state.Store(int32(runtime.StateRunning))
	spec := workspace.Spec{Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin", Fingerprint: "fp", Workdir: "/workspace"}
	if e = c.store.Save(spec.Record()); e != nil {
		t.Fatal(e)
	}
	c.loadAgent = func(string) (*agent.Session, error) {
		return &agent.Session{Spec: spec, Controller: workspace.Controller{Backend: backend, Store: c.store}, Runner: backend, CredentialManager: credential.NewManager("")}, nil
	}
	l, e := OpenLedger(filepath.Join(cfg.StateDir, "operations"), c.host, cfg.Limits)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = l.Close() })
	p, e := NewPolicy(cfg)
	if e != nil {
		t.Fatal(e)
	}
	caller, e := p.Authenticate("test-token-with-32-random-bytes-in-real-use")
	if e != nil {
		t.Fatal(e)
	}
	engine, e := NewEngine(c.host, l, p, c)
	if e != nil {
		t.Fatal(e)
	}
	return engine, caller, backend
}
func awaitOperation(t *testing.T, e *Engine, c Caller, id string, state string) Operation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		op, err := e.Status(context.Background(), c, id, 0, 0, 65536)
		if err != nil {
			t.Fatal(err)
		}
		if op.State == state {
			return op
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("operation did not reach %s", state)
	return Operation{}
}
func TestEngineDisconnectAndDuplicateDoNotReplay(t *testing.T) {
	e, caller, b := fixtureEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	now := time.Now().UTC()
	in := Mutation{Key: testKey(now), Workspace: e.control.reference("agent:robin"), Action: "exec", Argv: []string{"sh", "-c", "printf hello; printf problem >&2; exit 17"}, Timeout: time.Minute}
	request, disconnect := context.WithCancel(context.Background())
	a, err := e.Submit(request, caller, in)
	if err != nil {
		t.Fatal(err)
	}
	disconnect()
	again, err := e.Submit(context.Background(), caller, in)
	if err != nil || again.OperationID != a.OperationID {
		t.Fatal("retry changed identity")
	}
	op := awaitOperation(t, e, caller, a.OperationID, "failed")
	if op.ExitCode == nil || *op.ExitCode != 17 || op.Stdout.Data != "hello" || op.Stderr.Data != "problem" || b.calls.Load() != 1 {
		t.Fatalf("incorrect command evidence: %+v, calls=%d", op, b.calls.Load())
	}
}
func TestEngineUnknownExecHoldsAndNeverReplays(t *testing.T) {
	e, caller, b := fixtureEngine(t)
	b.broken = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	now := time.Now().UTC()
	in := Mutation{Key: testKey(now), Workspace: e.control.reference("agent:robin"), Action: "exec", Argv: []string{"true"}, Timeout: time.Minute}
	a, err := e.Submit(ctx, caller, in)
	if err != nil {
		t.Fatal(err)
	}
	op := awaitOperation(t, e, caller, a.OperationID, "indeterminate")
	if op.ExitCode != nil {
		t.Fatal("invented exit status")
	}
	cancel()
	<-done
	next, err := NewEngine(e.host, e.ledger, e.policy, e.control)
	if err != nil {
		t.Fatal(err)
	}
	in.Key = testKey(now.Add(time.Millisecond))
	if _, err = next.Submit(context.Background(), caller, in); err == nil {
		t.Fatal("uncertain execution hold released")
	}
	if b.calls.Load() != 1 {
		t.Fatal("uncertain exec replayed")
	}
}
func TestEngineCancelQueuedNeverDispatches(t *testing.T) {
	e, caller, b := fixtureEngine(t)
	in := Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "exec", Argv: []string{"true"}, Timeout: time.Minute}
	a, err := e.Submit(context.Background(), caller, in)
	if err != nil {
		t.Fatal(err)
	}
	op, err := e.Cancel(context.Background(), caller, a.OperationID)
	if err != nil || op.State != "cancelled" || b.calls.Load() != 0 {
		t.Fatal("queued cancellation dispatched")
	}
}

func TestEngineCancelledRecordCannotDispatch(t *testing.T) {
	e, caller, _ := fixtureEngine(t)
	in := Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "exec", Argv: []string{"true"}, Timeout: time.Minute}
	a, err := e.Submit(context.Background(), caller, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Cancel(context.Background(), caller, a.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.ledger.Dispatch(caller, a.OperationID, "nonce"); err == nil {
		t.Fatal("cancelled operation dispatched")
	}
}

func TestEngineCancellationRequestIdentityCannotChangeTarget(t *testing.T) {
	e, caller, _ := fixtureEngine(t)
	now := time.Now()
	in := Mutation{Key: testKey(now), Workspace: e.control.reference("agent:robin"), Action: "exec", Argv: []string{"true"}, Timeout: time.Minute}
	a, err := e.Submit(context.Background(), caller, in)
	if err != nil {
		t.Fatal(err)
	}
	key := testKey(now.Add(time.Millisecond))
	first, err := e.CancelRequest(context.Background(), caller, a.OperationID, key)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.CancelRequest(context.Background(), caller, a.OperationID, key)
	if err != nil || first.OperationID != again.OperationID {
		t.Fatal("cancellation retry lost identity")
	}
	in.Key = testKey(now.Add(2 * time.Millisecond))
	b, err := e.Submit(context.Background(), caller, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.CancelRequest(context.Background(), caller, b.OperationID, key); err == nil {
		t.Fatal("cancellation ID changed target")
	}
}
