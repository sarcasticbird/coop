package coordination_test

import (
	"path/filepath"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
)

func TestDiagnosticBindingSupportsExistingMasterIdentity(t *testing.T) {
	for _, id := range []string{"master@rack", "master:room+rack", "master\u96c0@rack", strings.Repeat("m", 128)} {
		t.Run(id, func(t *testing.T) {
			store, err := master.OpenStore(t.Context(), master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.db"), MasterID: id})
			if err != nil {
				t.Fatal("existing master identity rejected", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			binding := c.DiagnosticBinding{MasterID: id, Placement: c.Placement{AgentID: "agent", HostID: "host", InstanceID: "worker", Generation: 1}}
			if err := c.ValidateDiagnosticBinding(binding); err != nil {
				t.Fatal("supported master cannot be bound", err)
			}
		})
	}
}

func TestDiagnosticBindingRejectsUnsafeMasterIdentity(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("m", 129), "master/rack", "master\\rack", "master\rline", "master\nline", "master\x00rack", "master\x1b[2J", "master\track", "master\u202erack", string([]byte{0xff})} {
		binding := c.DiagnosticBinding{MasterID: id, Placement: c.Placement{AgentID: "agent", HostID: "host", InstanceID: "worker", Generation: 1}}
		if c.ValidateDiagnosticBinding(binding) == nil {
			t.Fatalf("unsafe master ID accepted: %q", id)
		}
	}
}
