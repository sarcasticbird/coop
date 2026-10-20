package worker

import (
	"errors"
	"testing"
)

func coordinatorDispatcher(t *testing.T) (*NativeDispatcher, *dispatchMaster, *dispatchCatalog) {
	t.Helper()
	cfg, master, catalog, code := dispatchFixture(t)
	actor := Actor{ID: 99, Login: "coordinator[bot]", Kind: "Bot"}
	cfg.Team.Members = append(cfg.Team.Members, TeamMember{Name: "coordinator", Actor: actor})
	if err := ValidateTeamPolicy(*cfg.Team); err != nil {
		t.Fatal(err)
	}
	cfg.Name, cfg.Self, cfg.Master.AgentID = "coordinator", actor, "coordinator"
	master.self.Principal.ID, master.self.Principal.GitHubBotID = "coordinator", actor.ID
	master.self.Placement.AgentID = "coordinator"
	catalog.err = errors.New("native roster policy changed")
	d, err := NewNativeDispatcher(cfg, master, catalog, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	return d, master, catalog
}

func TestNativeCoordinatorDoesNotDiscoverEngineering(t *testing.T) {
	d, _, _ := coordinatorDispatcher(t)
	if d.Ready() || d.refresh != nil {
		t.Fatal("coordinator started engineering discovery without any engineering capabilities")
	}
	preview, err := d.Preview(t.Context())
	if err != nil || len(preview.Jobs) != 0 || preview.Recovery {
		t.Fatalf("coordinator preflight required an engineering catalog: %+v, %v", preview, err)
	}
	if d.Progress().Reason != "" {
		t.Fatal("coordinator reported an engineering blocker")
	}
}

func TestNativeCoordinatorStillVerifiesPlacement(t *testing.T) {
	d, master, _ := coordinatorDispatcher(t)
	master.self.Placement.Generation++
	if _, err := d.Preview(t.Context()); err == nil {
		t.Fatal("coordinator preflight ignored changed placement")
	}
}

func TestNativeCoordinatorRetainsEngineeringReconciliation(t *testing.T) {
	for _, phase := range []string{"active", "blocked", "completed", "cancelled"} {
		t.Run(phase, func(t *testing.T) {
			d, _, catalog := coordinatorDispatcher(t)
			d.history["retained"] = NativeExecution{Phase: phase}
			d.poll()
			if d.refresh == nil {
				t.Fatal("capability removal suppressed discovery for retained engineering history")
			}
			if _, err := d.Preview(t.Context()); !errors.Is(err, catalog.err) {
				t.Fatal("capability removal hid retained engineering validation", err)
			}
		})
	}
}
