package tuifixture

import (
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestRuntimeInputs(t *testing.T) {
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	f := New(at)
	for id, d := range f.Diagnostics {
		if err := c.ValidateDiagnostics(d.Report); err != nil {
			t.Fatalf("%s: invalid native diagnostics: %v", id, err)
		}
	}
	if f.Hosts[0].Entries[0].State != "running" || f.Diagnostics["fixture-a"].Report.Wait.Handling != "waiting" || f.Presence[0].State != "idle" {
		t.Fatal("fixture collapsed lifecycle, work handling and availability")
	}
	if !f.Diagnostics["fixture-b"].Stale || !f.Presence[2].LastSeen.IsZero() {
		t.Fatal("missing stale/unknown freshness inputs")
	}
	if _, ok := f.Diagnostics["fixture-c"]; ok {
		t.Fatal("missing diagnostic report became an empty successful report")
	}
	if f.Moved.AgentID != f.Presence[0].AgentID || f.Moved.Generation <= f.Presence[0].Generation || f.Moved.HostID == f.Presence[0].HostID || f.Moved.InstanceID == f.Presence[0].InstanceID {
		t.Fatal("placement change must retain agent identity but replace placement")
	}
	f.Hosts[0].Entries[0].Name = "modified"
	f.Presence[0].Alias = "modified"
	delete(f.Diagnostics, "fixture-a")
	other := New(at)
	if other.Hosts[0].Entries[0].Name != "same-name" || other.Presence[0].Alias != "same-name" || len(other.Diagnostics) != 2 {
		t.Fatal("fixture mutations leaked between tests")
	}
}
