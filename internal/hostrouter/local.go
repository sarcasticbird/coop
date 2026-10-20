package hostrouter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/sarcasticbird/coop/internal/agent"
	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/fleet"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

// LocalClient reads local runtime inventory without starting or adopting guests.
// Zero values use the local runtime, ownership store and host configuration.
type LocalClient struct {
	Runtime runtime.Runtime
	Machine runtime.AppleMachine
	Store   workspace.Store
	Config  *config.Config
	// AgentInventory optionally replaces read-only profile inspection in fixtures.
	AgentInventory func(context.Context, string) (runtime.State, error)
}

// localInventoryStore gives fleet and display ownership the same read snapshot.
type localInventoryStore struct {
	workspace.Store
	records []workspace.Record
}

func (s localInventoryStore) List() ([]workspace.Record, error) { return s.records, nil }

func (c LocalClient) Inventory(ctx context.Context, h Host) ([]InventoryEntry, error) {
	if h != Local {
		return nil, errors.New("local inventory requires the local host")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store := c.Store
	if store == nil {
		var err error
		store, err = workspace.DefaultStore()
		if err != nil {
			return nil, err
		}
	}
	records, err := store.List()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base := c.Runtime
	if base == nil {
		base = runtime.NewApple()
	}
	if apple, ok := base.(*runtime.Apple); ok {
		base = apple.WithContext(ctx)
	}
	machine := c.Machine
	// fleet currently passes Background to machine reads. Bind only this request's
	// copy to ctx, including when the underlying command is a fixture.
	boundMachine := runtime.AppleMachine{Command: func(_ context.Context, args []string, _ io.Reader, out, _ io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := machine.Output(ctx, args...)
		if err != nil {
			return err
		}
		_, err = out.Write(data)
		return err
	}}
	f := fleet.Runtime{Runtime: base, Machine: boundMachine, Store: localInventoryStore{Store: store, records: records}}
	containers, err := f.Containers()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	owned := map[Target]workspace.Record{}
	for _, r := range records {
		owned[Target{Host: Local, Kind: TargetKind(r.Mode), ID: r.Identity}] = r
	}
	rows := []InventoryEntry{}
	seen := map[Target]bool{}
	for _, container := range containers {
		target := Target{Host: Local, Kind: Interactive, ID: container.Name}
		if container.Mode == "agent" {
			target.Kind, target.ID = Agent, container.Profile
		}
		record := owned[target]
		project := container.ProjectMount()
		isOwned := record.RuntimeName == container.Name
		if target.Kind == Interactive {
			isOwned = isOwned && project != "" && record.Applied.Project == project
		}
		rows = append(rows, InventoryEntry{Target: target, Name: container.Name, State: container.State, Project: project, Owned: isOwned})
		seen[target] = true
	}
	cfg := c.Config
	if cfg == nil {
		loaded, err := config.Load("")
		if err != nil {
			return nil, err
		}
		cfg = &loaded
	}
	profiles := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		profiles = append(profiles, name)
	}
	sort.Strings(profiles)
	for _, name := range profiles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := config.ValidateProfileName(name); err != nil {
			return nil, err
		}
		target := Target{Host: Local, Kind: Agent, ID: name}
		if seen[target] {
			continue
		}
		var state runtime.State
		if c.AgentInventory != nil {
			state, err = c.AgentInventory(ctx, name)
		} else {
			var s *agent.Session
			s, err = agent.New(name)
			if err == nil {
				var info workspace.Info
				info, err = s.Controller.Backend.Inspect(ctx, s.Spec)
				state = info.State
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, fmt.Errorf("inspect agent %s: %w", name, err)
		}
		if state != runtime.StateAbsent && state != runtime.StateStopped && state != runtime.StateRunning {
			return nil, fmt.Errorf("agent %s state is not authoritative", name)
		}
		runtimeName := "coop-agent-" + name
		rows = append(rows, InventoryEntry{Target: target, Name: runtimeName, State: state.String(), Owned: owned[target].RuntimeName == runtimeName})
	}
	for i := range rows {
		if rows[i].Target.Kind == Agent {
			if binding := cfg.Profiles[rows[i].Target.ID].Diagnostics; binding != nil {
				captured := *binding
				rows[i].Diagnostics = &captured
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateEntries(rows); err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Target.Kind != rows[j].Target.Kind {
			return rows[i].Target.Kind < rows[j].Target.Kind
		}
		return rows[i].Target.ID < rows[j].Target.ID
	})
	return rows, nil
}
