package hostrouter

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

func testDiagnosticBinding() c.DiagnosticBinding {
	return c.DiagnosticBinding{MasterID: "master@rack", Placement: c.Placement{AgentID: "principal-not-profile", HostID: "host-not-ssh-alias", InstanceID: "worker", Generation: 3}}
}

func TestLocalInventoryExportsOnlyExplicitDiagnosticBinding(t *testing.T) {
	client := localFixture(t)
	binding := testDiagnosticBinding()
	client.Config.Profiles = map[string]config.Profile{"robin": {Diagnostics: &binding}, "absent": {Diagnostics: &c.DiagnosticBinding{MasterID: binding.MasterID, Placement: c.Placement{AgentID: "absent-principal", HostID: binding.HostID, InstanceID: "other-worker", Generation: 1}}}, "unbound": {}}
	client.Store = &localTestStore{records: []workspace.Record{{Mode: workspace.Agent, Identity: "robin", RuntimeName: "coop-agent-robin"}}}
	client.Runtime = localTestRuntime{list: func() ([]runtime.ContainerInfo, error) {
		return []runtime.ContainerInfo{{Name: "coop-robin-child", State: "running"}}, nil
	}}
	client.AgentInventory = func(context.Context, string) (runtime.State, error) { return runtime.StateAbsent, nil }
	rows, err := client.Inventory(t.Context(), Local)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range rows {
		if e.Target.Kind != Agent {
			continue
		}
		seen[e.Target.ID] = true
		want := client.Config.Profiles[e.Target.ID].Diagnostics
		if want == nil && e.Diagnostics != nil || want != nil && (e.Diagnostics == nil || *e.Diagnostics != *want) {
			t.Fatalf("incorrect binding for %+v", e.Target)
		}
	}
	if !seen["robin"] || !seen["absent"] || !seen["unbound"] {
		t.Fatal("missing inventory cases", seen)
	}
}

func TestRemoteInventoryPreservesAndValidatesDiagnosticBinding(t *testing.T) {
	binding := testDiagnosticBinding()
	row := testEntry(Agent, "profile-not-principal")
	row.Diagnostics = &binding
	host := Host{Name: "registration-not-host-id", Address: "ssh-alias"}
	remote := SSH{Command: func(_ context.Context, _ []string, out io.Writer) error {
		return json.NewEncoder(out).Encode(Snapshot{Version: 1, Hosts: []HostInventory{{Host: Local, Entries: []InventoryEntry{row}}}})
	}}
	snapshot := (Router{Remote: remote}).Inventory(t.Context(), []Host{host})
	if len(snapshot.Hosts) != 1 || snapshot.Hosts[0].Error != "" || len(snapshot.Hosts[0].Entries) != 1 {
		t.Fatal(snapshot)
	}
	got := snapshot.Hosts[0].Entries[0]
	if got.Target.Host != host || got.Diagnostics == nil || *got.Diagnostics != binding {
		t.Fatalf("transport rewrote binding: %+v", got)
	}
	for _, broken := range []string{"missing", "control", "interactive", "duplicate"} {
		t.Run(broken, func(t *testing.T) {
			e := row
			b := binding
			e.Diagnostics = &b
			rows := []InventoryEntry{e}
			switch broken {
			case "missing":
				b.InstanceID = ""
			case "control":
				b.MasterID = "bad\x1bmaster"
			case "interactive":
				rows[0].Target.Kind = Interactive
			case "duplicate":
				other := e
				other.Target.ID = "second-profile"
				rows = append(rows, other)
			}
			if ValidateEntries(rows) == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}
