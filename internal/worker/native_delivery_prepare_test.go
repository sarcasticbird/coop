package worker

import (
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestNativeDeliveryPrepareRejectsForeignRole(t *testing.T) {
	for _, change := range []string{"author", "peer", "qa", "role", "grant", "executor", "capability"} {
		t.Run(change, func(t *testing.T) {
			cfg, m, g, _, _, x := nativeDeliveryActionFixture(t)
			switch change {
			case "author", "peer", "qa":
				i := map[string]int{"author": 1, "peer": 0, "qa": 2}[change]
				cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[i].Name, cfg.Team.Members[i].Actor, change
			case "role":
				x.Job.Master.Role = "implementer"
			case "grant":
				x.Job.Master.DeliveryHandoffID = ""
			case "executor":
				x.Job.Master.ExecutorID = 12
			case "capability":
				cfg.Team.Members[3].Capabilities = nil
			}
			if _, err := NewCheckouts(cfg, g.runner).Prepare(t.Context(), x.Job); err == nil {
				t.Fatal("prepared unauthorized delivery checkout")
			}
			d := &NativeDispatcher{config: cfg, master: m, github: g, clock: RealClock{}}
			if _, err := d.freshCandidate(t.Context(), x); err != c.ErrForbidden {
				t.Fatal("revalidated unauthorized delivery role", err)
			}
		})
	}
}
