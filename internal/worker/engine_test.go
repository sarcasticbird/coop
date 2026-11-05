package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Wait(ctx context.Context, d time.Duration) error {
	c.now = c.now.Add(d)
	return ctx.Err()
}

type memoryStore struct {
	state  State
	paused bool
}

func (s *memoryStore) Load() (State, error) {
	if s.state.Version == 0 {
		s.state = newState()
	}
	return copyState(s.state), nil
}
func (s *memoryStore) Save(state State) error { s.state = copyState(state); return nil }
func (s *memoryStore) Lock() (func(), error)  { return func() {}, nil }
func (s *memoryStore) Paused() (bool, error)  { return s.paused, nil }
func copyState(s State) State {
	raw, _ := json.Marshal(s)
	var copy State
	_ = json.Unmarshal(raw, &copy)
	return copy
}

type fakeCheckouts struct{ progress string }

func (c *fakeCheckouts) Prepare(ctx context.Context, j Job) (Job, error) {
	if j.Checkout == "" {
		j.Checkout = "/workspace/worker/task"
		j.Branch = "agent/task"
	}
	return j, nil
}
func (c *fakeCheckouts) Progress(context.Context, Job) (string, error) { return c.progress, nil }

type fakeOpenCode struct {
	HealthError, AbortError error
	healthCalls             int
	calls, aborts           int
	running, lost           bool
	last                    Job
	effect                  func(Config, Job)
}

func (c *fakeOpenCode) Health(context.Context) (ServerHealth, error) {
	c.healthCalls++
	if c.HealthError != nil {
		return ServerHealth{}, c.HealthError
	}
	return ServerHealth{Healthy: true, Version: "1.18.18"}, nil
}
func (c *fakeOpenCode) EnsureSession(ctx context.Context, cfg Config, j Job) (Job, error) {
	if j.SessionID == "" {
		j.SessionID = "ses_" + BindingKey(cfg, j)[:16]
	}
	return j, nil
}
func (c *fakeOpenCode) Submit(ctx context.Context, cfg Config, j Job, prompt string) (Turn, error) {
	c.calls++
	c.last = j
	if c.effect != nil {
		c.effect(cfg, j)
	}
	if c.lost {
		return Turn{State: "unknown"}, errors.New("lost response")
	}
	return Turn{Accepted: true, State: "accepted", SessionID: j.SessionID, MessageID: j.MessageID}, nil
}
func (c *fakeOpenCode) Observe(ctx context.Context, j Job) (Turn, error) {
	state := "completed"
	if c.running {
		state = "running"
	}
	return Turn{Accepted: true, State: state, Terminal: !c.running, SessionID: j.SessionID, MessageID: j.MessageID}, nil
}
func (c *fakeOpenCode) Abort(context.Context, Job) error {
	c.aborts++
	if c.AbortError != nil {
		return c.AbortError
	}
	c.running = false
	return nil
}
func (c *fakeOpenCode) CheckContinuation(context.Context, Job) error {
	if c.running {
		return errors.New("session is busy")
	}
	return nil
}

func TestEngineRejectsRetiredRuntimeBeforeAnyDependency(t *testing.T) {
	for _, version := range []int{0, 1, 2, 4} {
		// Nil store/clients ensure refusal precedes any state, session or API access.
		e := &Engine{Config: Config{Version: version}}
		if err := e.Tick(t.Context()); !errors.Is(err, errLegacyRuntime) {
			t.Fatal(version, err)
		}
		if err := e.Run(t.Context()); !errors.Is(err, errLegacyRuntime) {
			t.Fatal(version, err)
		}
	}
}
