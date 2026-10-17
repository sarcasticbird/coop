package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/sarcasticbird/coop/internal/worker"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

type workerBackend struct {
	failingBackend
	state         runtime.State
	execs, starts int
	argv          []string
	input         []byte
}

func (b *workerBackend) Inspect(context.Context, workspace.Spec) (workspace.Info, error) {
	return workspace.Info{State: b.state}, nil
}
func (b *workerBackend) Start(context.Context, workspace.Spec) error { b.starts++; return nil }
func (b *workerBackend) Exec(ctx context.Context, name, dir string, args []string, input io.Reader, out, stderr io.Writer) error {
	b.execs++
	b.argv = args
	if input != nil {
		var err error
		b.input, err = io.ReadAll(input)
		if err != nil {
			return err
		}
	}
	if len(args) > 1 && args[1] == "status" {
		return json.NewEncoder(out).Encode(worker.Status{Enabled: true, WakeEnabled: true, State: "idle", Heartbeat: time.Now().UTC()})
	}
	_, err := io.WriteString(out, `{"state":"idle"}`)
	return err
}
func TestWorkerControlsNeverStartOrAcquireCredentials(t *testing.T) {
	for _, state := range []runtime.State{runtime.StateRunning, runtime.StateStopped} {
		t.Run(state.String(), func(t *testing.T) {
			b := &workerBackend{state: state}
			var out bytes.Buffer
			s := Session{Spec: workspace.Spec{Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin", Profile: config.Profile{Services: map[string]config.Service{"worker": {Command: []string{"worker"}}}}}, Controller: workspace.Controller{Backend: b}, Runner: b, Output: &out}
			err := s.Worker(context.Background(), "pause")
			if b.starts != 0 || b.creates != 0 {
				t.Fatal("worker control started a VM")
			}
			if state == runtime.StateStopped {
				if err == nil || b.execs != 0 {
					t.Fatal("stopped VM was used")
				}
			} else {
				if err != nil || b.execs != 1 || !strings.Contains(strings.Join(b.argv, " "), "pause") {
					t.Fatal("running worker was not controlled")
				}
			}
		})
	}
}

func TestPingUsesStructuredStdinAndNeverStartsVM(t *testing.T) {
	for _, state := range []runtime.State{runtime.StateRunning, runtime.StateStopped} {
		b := &workerBackend{state: state}
		var out bytes.Buffer
		s := Session{Spec: workspace.Spec{Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin", Profile: config.Profile{Services: map[string]config.Service{"worker": {Command: []string{"worker"}}}}}, Controller: workspace.Controller{Backend: b}, Runner: b, Output: &out}
		prompt := "check `head` $(literal) \"quote\"\ncafé"
		err := s.Ping(context.Background(), worker.PingInput{Prompt: prompt})
		if b.starts != 0 || b.creates != 0 {
			t.Fatal("ping started VM")
		}
		if state == runtime.StateStopped {
			if err == nil || b.execs != 0 {
				t.Fatal("stopped ping accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var input worker.PingInput
		if json.Unmarshal(b.input, &input) != nil || input.Prompt != prompt {
			t.Fatal("prompt changed in transit")
		}
		if strings.Contains(strings.Join(b.argv, " "), "literal") || strings.Contains(out.String(), "literal") {
			t.Fatal("private text exposed")
		}
	}
}
