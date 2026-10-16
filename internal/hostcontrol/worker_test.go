package hostcontrol

import (
	"context"
	"encoding/json"
	"github.com/sarcasticbird/coop/internal/agent"
	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/worker"
	"github.com/sarcasticbird/coop/internal/workspace"
	"io"
	"strings"
	"testing"
	"time"
)

type workerBackend struct {
	execBackend
	wake            worker.WakeSummary
	prompt          string
	pings           int
	loseAck         bool
	oversized       bool
	pauseSupported  bool
	admissionPaused bool
	controls        int
}

func (f *workerBackend) Exec(ctx context.Context, n, cwd string, args []string, in io.Reader, out, errout io.Writer) error {
	if len(args) < 2 || args[0] != "/usr/local/libexec/coop-worker" {
		return f.execBackend.Exec(ctx, n, cwd, args, in, out, errout)
	}
	if f.oversized {
		_, e := io.WriteString(out, strings.Repeat("x", 128<<10))
		return e
	}
	switch args[1] {
	case "status":
		return json.NewEncoder(out).Encode(worker.Status{Enabled: true, WakeEnabled: true, State: "idle", Heartbeat: time.Now().UTC(), AdmissionPauseSupported: f.pauseSupported, AdmissionPaused: f.admissionPaused})
	case "pause", "resume":
		f.controls++
		if args[len(args)-1] != "--admission-only" {
			return fail("interrupting_pause_forbidden")
		}
		f.admissionPaused = args[1] == "pause"
		return json.NewEncoder(out).Encode(map[string]any{"admission_paused": f.admissionPaused, "state": args[1] + "_requested"})
	case "ping":
		var input worker.PingInput
		if e := json.NewDecoder(in).Decode(&input); e != nil {
			return e
		}
		f.prompt = input.Prompt
		f.pings++
		f.wake = worker.WakeSummary{ID: input.RequestID, Kind: "ping", Phase: "queued"}
		if f.loseAck {
			return fail("ack_lost")
		}
		return json.NewEncoder(out).Encode(f.wake)
	case "requests":
		if len(args) != 6 || args[4] != "--request-id" || args[5] != f.wake.ID {
			return fail("exact_request_required")
		}
		return json.NewEncoder(out).Encode([]worker.WakeSummary{f.wake})
	}
	return json.NewEncoder(out).Encode(map[string]any{"paused": true, "state": "pause_requested"})
}
func fixtureWorkerEngine(t *testing.T) (*Engine, Caller, *workerBackend) {
	e, caller, _ := fixtureEngine(t)
	b := &workerBackend{}
	b.state.Store(int32(runtime.StateRunning))
	spec := workspace.Spec{Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin", Fingerprint: "fp", Workdir: "/workspace", Profile: configProfileForWorker()}
	e.control.loadAgent = func(string) (*agent.Session, error) {
		return &agent.Session{Spec: spec, Controller: workspace.Controller{Backend: b, Store: e.control.store}, Runner: b}, nil
	}
	return e, caller, b
}
func TestMCPPingLostAcknowledgementReconcilesExactWake(t *testing.T) {
	e, caller, b := fixtureWorkerEngine(t)
	b.loseAck = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	defer func() { cancel(); <-done }()
	prompt := "literal 'quotes'\n$HOME; no interpolation"
	in := Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: "ping", Prompt: prompt}
	a, err := e.Submit(ctx, caller, in)
	if err != nil {
		t.Fatal(err)
	}
	if a.WakeID == "" || a.WakePhase != "host_pending" {
		t.Fatal("wake identity was not persisted before acceptance")
	}
	op := awaitOperation(t, e, caller, a.OperationID, "succeeded")
	if op.WakeID != a.WakeID || op.WakePhase != "queued" {
		t.Fatal("queue mistaken for delivery")
	}
	again, err := e.Submit(ctx, caller, in)
	if err != nil || again.WakeID != a.WakeID {
		t.Fatal("retry changed wake")
	}
	if b.pings != 1 || b.prompt != prompt {
		t.Fatal("ping replayed or prompt altered")
	}
}
func TestMCPWorkerRejectsOversizedStatus(t *testing.T) {
	e, _, b := fixtureWorkerEngine(t)
	b.oversized = true
	if _, err := e.control.WorkerStatus(context.Background(), e.control.reference("agent:robin")); err == nil {
		t.Fatal("oversized status accepted")
	}
}

func configProfileForWorker() config.Profile {
	return config.Profile{Services: map[string]config.Service{"worker": {Command: []string{"true"}}}}
}

func TestMCPAdmissionPauseRejectsLegacyAndPreservesMode(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "supported"}[supported], func(t *testing.T) {
			e, c, b := fixtureWorkerEngine(t)
			b.pauseSupported = supported
			for _, action := range []string{"pause", "resume"} {
				a, err := e.Submit(context.Background(), c, Mutation{Key: testKey(time.Now()), Workspace: e.control.reference("agent:robin"), Action: action})
				if err != nil {
					t.Fatal(err)
				}
				r, _ := e.ledger.Lookup(c, a.OperationID)
				e.execute(context.Background(), r)
				r, _ = e.ledger.Lookup(c, r.ID)
				if !supported {
					if b.controls != 0 || r.Outcome.Category != "noninterrupting_pause_unsupported" {
						t.Fatal("legacy interrupting pause was called")
					}
				} else if r.State != "succeeded" || b.admissionPaused != (action == "pause") {
					t.Fatalf("wrong admission control result: %+v", r.Outcome)
				}
			}
		})
	}
}
