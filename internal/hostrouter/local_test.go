package hostrouter

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

// Unimplemented methods panic: inventory must never mutate or adopt anything.
type localTestRuntime struct {
	runtime.Runtime
	list func() ([]runtime.ContainerInfo, error)
}

func (r localTestRuntime) Containers() ([]runtime.ContainerInfo, error) { return r.list() }

type localTestStore struct {
	workspace.Store
	records []workspace.Record
	err     error
	reads   int
}

func (s *localTestStore) List() ([]workspace.Record, error) {
	s.reads++
	return s.records, s.err
}

func localFixture(t *testing.T) LocalClient {
	t.Helper()
	return LocalClient{
		Runtime: localTestRuntime{list: func() ([]runtime.ContainerInfo, error) { return nil, nil }},
		Store:   &localTestStore{},
		Config:  &config.Config{},
		Machine: runtime.AppleMachine{Command: func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
			switch strings.Join(args, " ") {
			case "--version":
				_, err := io.WriteString(out, "container CLI version 1.5.0")
				return err
			case "machine list --format json":
				_, err := io.WriteString(out, `[{"id":"coop-agent-robin","status":"running"},{"id":"unowned-machine","status":"stopped"}]`)
				return err
			case "machine inspect coop-agent-robin":
				_, err := io.WriteString(out, `[{"id":"coop-agent-robin","containerId":"coop-robin-child"}]`)
				return err
			case "machine inspect unowned-machine":
				_, err := io.WriteString(out, `[{"id":"unowned-machine","containerId":"coop-unowned-child"}]`)
				return err
			default:
				t.Errorf("unexpected runtime command: %v", args)
				return errors.New("unexpected runtime command")
			}
		}},
		AgentInventory: func(_ context.Context, name string) (runtime.State, error) {
			t.Errorf("unexpected profile inspection: %s", name)
			return runtime.StateAbsent, errors.New("unexpected profile inspection")
		},
	}
}

func TestLocalInventoryMergesFleetProfilesAndOwnership(t *testing.T) {
	c := localFixture(t)
	c.Runtime = localTestRuntime{list: func() ([]runtime.ContainerInfo, error) {
		return []runtime.ContainerInfo{
			{Name: "coop-project", State: "stopped", Labels: map[string]string{runtime.ProjectLabel: "/projects/selected"}, Mounts: []runtime.MountInfo{
				{Source: "/projects/other", Destination: "/projects/other", Bind: true},
				{Source: "/projects/selected", Destination: "/projects/selected", Bind: true},
			}},
			{Name: "coop-discovered", State: "running", Labels: map[string]string{runtime.ProjectLabel: "/unverified"}, Mounts: []runtime.MountInfo{
				{Source: "/unverified", Destination: "/different", Bind: true},
			}},
			{Name: "coop-robin-child", State: "running"},
			{Name: "coop-unowned-child", State: "stopped"},
		}, nil
	}}
	store := &localTestStore{records: []workspace.Record{
		{Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin"},
		{Mode: workspace.Agent, Identity: "missing", RuntimeName: "coop-agent-missing"},
		{Mode: workspace.Interactive, Identity: "coop-project", RuntimeName: "coop-project", Applied: workspace.AppliedSpec{Project: "/projects/selected"}},
	}}
	c.Store = store
	c.Config.Profiles = map[string]config.Profile{"missing": {}, "fresh": {}, "discovered": {}}
	var inspected []string
	c.AgentInventory = func(ctx context.Context, name string) (runtime.State, error) {
		if ctx == nil {
			t.Fatal("missing context")
		}
		inspected = append(inspected, name)
		if name == "discovered" {
			return runtime.StateStopped, nil
		}
		return runtime.StateAbsent, nil
	}
	rows, err := c.Inventory(context.Background(), Local)
	if err != nil {
		t.Fatal(err)
	}
	want := []InventoryEntry{
		{Target: Target{Host: Local, Kind: Agent, ID: "discovered"}, Name: "coop-agent-discovered", State: "stopped"},
		{Target: Target{Host: Local, Kind: Agent, ID: "fresh"}, Name: "coop-agent-fresh", State: "absent"},
		{Target: Target{Host: Local, Kind: Agent, ID: "missing"}, Name: "coop-agent-missing", State: "absent", Owned: true},
		{Target: Target{Host: Local, Kind: Agent, ID: "robin"}, Name: "coop-agent-robin", State: "running", Owned: true},
		{Target: Target{Host: Local, Kind: Interactive, ID: "coop-discovered"}, Name: "coop-discovered", State: "running"},
		{Target: Target{Host: Local, Kind: Interactive, ID: "coop-project"}, Name: "coop-project", State: "stopped", Project: "/projects/selected", Owned: true},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("inventory = %+v; want %+v", rows, want)
	}
	if store.reads != 1 || !reflect.DeepEqual(inspected, []string{"discovered", "fresh", "missing"}) {
		t.Fatalf("ownership reads=%d, inspected=%v", store.reads, inspected)
	}
	// A configured, already-instantiated owned agent must not be inspected again.
	c.Config.Profiles["robin"] = config.Profile{}
	if _, err := c.Inventory(context.Background(), Local); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inspected, []string{"discovered", "fresh", "missing", "discovered", "fresh", "missing"}) {
		t.Fatalf("already-instantiated agent was inspected again: %v", inspected)
	}
	store.records[2].Applied.Project = "/different-project"
	rows, err = c.Inventory(context.Background(), Local)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Target.ID == "coop-project" && row.Owned {
			t.Fatal("mismatched project reported owned")
		}
	}
}

func TestLocalInventoryRejectsNonlocalBeforeIO(t *testing.T) {
	for _, host := range []Host{{}, {Name: "remote", Address: "server"}, {Name: "local", Address: "server"}} {
		if rows, err := (LocalClient{}).Inventory(context.Background(), host); err == nil || rows != nil {
			t.Fatalf("host %+v: rows=%v error=%v", host, rows, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if rows, err := (LocalClient{}).Inventory(ctx, Local); !errors.Is(err, context.Canceled) || rows != nil {
		t.Fatalf("canceled request: rows=%v error=%v", rows, err)
	}
}

func TestLocalInventoryFailsClosedOnReadErrors(t *testing.T) {
	failure := errors.New("runtime unavailable")
	for _, stage := range []string{"store", "containers", "--version", "machine list --format json", "machine inspect coop-agent-robin", "agent"} {
		t.Run(stage, func(t *testing.T) {
			c := localFixture(t)
			switch stage {
			case "store":
				c.Store = &localTestStore{err: failure}
			case "containers":
				c.Runtime = localTestRuntime{list: func() ([]runtime.ContainerInfo, error) { return nil, failure }}
			case "agent":
				c.Config.Profiles = map[string]config.Profile{"fresh": {}}
				c.AgentInventory = func(context.Context, string) (runtime.State, error) { return runtime.StateAbsent, failure }
			default:
				command := c.Machine.Command
				c.Machine.Command = func(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
					if strings.Join(args, " ") == stage {
						return failure
					}
					return command(ctx, args, in, out, stderr)
				}
			}
			if rows, err := c.Inventory(context.Background(), Local); !errors.Is(err, failure) || rows != nil {
				t.Fatalf("rows=%v error=%v", rows, err)
			}
		})
	}
}

func TestLocalInventoryMachineAndAgentReadsUseRequestContext(t *testing.T) {
	for _, stage := range []string{"--version", "machine list --format json", "machine inspect coop-agent-robin", "agent"} {
		t.Run(stage, func(t *testing.T) {
			c := localFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			wait := func(got context.Context) error {
				if got != ctx {
					return errors.New("request context was replaced")
				}
				close(entered)
				<-got.Done()
				return got.Err()
			}
			if stage == "agent" {
				c.Config.Profiles = map[string]config.Profile{"fresh": {}}
				c.AgentInventory = func(ctx context.Context, _ string) (runtime.State, error) { return runtime.StateAbsent, wait(ctx) }
			} else {
				command := c.Machine.Command
				c.Machine.Command = func(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
					if strings.Join(args, " ") == stage {
						return wait(ctx)
					}
					return command(ctx, args, in, out, stderr)
				}
			}
			done := make(chan error, 1)
			go func() { _, err := c.Inventory(ctx, Local); done <- err }()
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("returned before reaching read: %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("inventory did not reach read")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("inventory ignored cancellation")
			}
		})
	}
}

func TestLocalInventoryRejectsNonAuthoritativeState(t *testing.T) {
	for _, agentState := range []bool{false, true} {
		c := localFixture(t)
		if agentState {
			c.Config.Profiles = map[string]config.Profile{"fresh": {}}
			c.AgentInventory = func(context.Context, string) (runtime.State, error) { return runtime.State(99), nil }
		} else {
			c.Runtime = localTestRuntime{list: func() ([]runtime.ContainerInfo, error) {
				return []runtime.ContainerInfo{{Name: "coop-project", State: "unknown"}}, nil
			}}
		}
		if rows, err := c.Inventory(context.Background(), Local); err == nil || rows != nil {
			t.Fatalf("agent=%v: rows=%v error=%v", agentState, rows, err)
		}
	}
}

func TestLocalInventoryEmptyIsNonNilAndNeedsNoRemoteRegistry(t *testing.T) {
	c := localFixture(t)
	c.Config = nil
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c.Machine.Command = func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
		if strings.Join(args, " ") != "--version" {
			t.Fatalf("old runtime received machine command: %v", args)
		}
		_, err := io.WriteString(out, "container CLI version 1.4.9")
		return err
	}
	rows, err := c.Inventory(context.Background(), Local)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("rows=%v error=%v", rows, err)
	}
}
