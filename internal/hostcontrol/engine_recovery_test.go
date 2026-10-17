package hostcontrol

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestCrashCheckpointHelper(t *testing.T) {
	root := os.Getenv("COOP_MCP_CRASH_ROOT")
	if root == "" {
		return
	}
	l, err := OpenLedger(root, Host{ID: os.Getenv("COOP_MCP_CRASH_HOST")}, Limits{})
	if err != nil {
		os.Exit(2)
	}
	r, err := l.Lookup(Caller{ID: "chief"}, os.Getenv("COOP_MCP_CRASH_ID"))
	if err != nil {
		os.Exit(3)
	}
	switch os.Getenv("COOP_MCP_CRASH_PHASE") {
	case "dispatch":
		r.State = "running"
		r.Dispatched = true
	case "completed":
		r.State = "succeeded"
		r.Outcome = Outcome{Completed: true}
	}
	if err = l.Update(r); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}
func TestCrashDispatchCheckpointNeverReplays(t *testing.T) {
	for _, phase := range []string{"accepted", "dispatch", "completed"} {
		t.Run(phase, func(t *testing.T) {
			e, caller, b := fixtureEngine(t)
			a, err := e.Submit(context.Background(), caller, Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "exec", Argv: []string{"true"}, Timeout: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.Command(bin, "-test.run=^TestCrashCheckpointHelper$")
			child.Env = append(os.Environ(), "COOP_MCP_CRASH_ROOT="+e.ledger.root, "COOP_MCP_CRASH_HOST="+e.host.ID, "COOP_MCP_CRASH_ID="+a.OperationID, "COOP_MCP_CRASH_PHASE="+phase)
			if out, err := child.CombinedOutput(); err != nil {
				t.Fatalf("crash fixture: %v %s", err, out)
			}
			reopened, err := OpenLedger(e.ledger.root, e.host, e.ledger.limits)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			recovered, err := NewEngine(e.host, reopened, e.policy, e.control)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- recovered.Run(ctx) }()
			defer func() { cancel(); <-done }()
			want := "succeeded"
			if phase == "dispatch" {
				want = "indeterminate"
			}
			awaitOperation(t, recovered, caller, a.OperationID, want)
			if phase == "accepted" {
				if b.calls.Load() != 1 {
					t.Fatal("undispatched accepted command did not resume exactly once")
				}
			} else if b.calls.Load() != 0 {
				t.Fatal("saved dispatch or completion replayed")
			}
		})
	}
}
