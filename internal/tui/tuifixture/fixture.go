// Package tuifixture supplies synthetic, data-only inputs for runtime TUI tests.
// It has no clients, credentials, transport, or mutation hooks.
package tuifixture

import (
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

type Size struct{ Width, Height int }

func Sizes() []Size { return []Size{{120, 40}, {80, 24}, {40, 20}} }

type Fixture struct {
	At                  time.Time
	Hosts               []hostrouter.HostInventory
	Presence            []c.Presence
	Diagnostics         map[string]c.AgentDiagnostics
	Moved               c.Presence
	ChangedRegistration hostrouter.Host
}

// New returns independent mutable data on every call. The supplied observation
// time lets model adapters use their own clock without sleep-based freshness tests.
func New(at time.Time) Fixture {
	remote := hostrouter.Host{Name: "fixture-remote", Address: "fixture-ssh-alias"}
	empty := hostrouter.Host{Name: "fixture-empty", Address: "fixture-empty-alias"}
	offline := hostrouter.Host{Name: "fixture-offline", Address: "fixture-offline-alias"}
	row := func(h hostrouter.Host, id, state string) hostrouter.InventoryEntry {
		return hostrouter.InventoryEntry{Target: hostrouter.Target{Host: h, Kind: hostrouter.Agent, ID: id}, Name: "same-name", State: state}
	}
	f := Fixture{At: at, Hosts: []hostrouter.HostInventory{
		{Host: hostrouter.Local, Entries: []hostrouter.InventoryEntry{row(hostrouter.Local, "fixture-a", "running")}},
		{Host: remote, Entries: []hostrouter.InventoryEntry{row(remote, "fixture-b", "stopped"), row(remote, "fixture-c", "absent")}},
		{Host: empty, Entries: []hostrouter.InventoryEntry{}},
		{Host: offline, Error: "unreachable"},
	}, Diagnostics: make(map[string]c.AgentDiagnostics), ChangedRegistration: hostrouter.Host{Name: remote.Name, Address: "fixture-replacement-alias"}}
	for i, state := range []string{"idle", "paused", "unknown"} {
		id := []string{"fixture-a", "fixture-b", "fixture-c"}[i]
		host := hostrouter.Local.Name
		if i > 0 {
			host = remote.Name
		}
		p := c.Presence{Placement: c.Placement{AgentID: id, HostID: host, InstanceID: "fixture-worker-1", Generation: 1}, Alias: "same-name", State: state, LastSeen: at}
		if i == 1 {
			p.LastSeen = at.Add(-2 * c.PresenceStale)
		}
		if i == 2 {
			p.LastSeen = time.Time{}
		}
		f.Presence = append(f.Presence, p)
		if i == 2 {
			continue // Missing report is not idle, nor a successful empty report.
		}
		d := c.AgentDiagnostics{ReceivedAt: p.LastSeen, Stale: i == 1, Report: c.WorkerDiagnostics{
			Placement: p.Placement, Version: 1, RestoreEpoch: 1, ObservedAt: p.LastSeen, StartedAt: at.Add(-time.Hour),
			Wait: c.DiagnosticWait{ObservedAt: p.LastSeen, Handling: "waiting", Reason: "local_pause"},
		}}
		f.Diagnostics[id] = d
	}
	f.Moved = f.Presence[0]
	f.Moved.HostID, f.Moved.InstanceID, f.Moved.Generation = remote.Name, "fixture-worker-2", 2
	return f
}
