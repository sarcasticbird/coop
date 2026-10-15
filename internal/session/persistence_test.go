package session

import (
	"errors"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

func TestUpRefusesIncompleteMaintenance(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := runtime.NewMock()
	s := testSession(t, m)
	store, err := workspace.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRecovery(workspace.Interactive, s.Name, []byte(`{"pending":true}`)); err != nil {
		t.Fatal(err)
	}
	err = s.Up()
	if !errors.Is(err, workspace.ErrMaintenanceRequired) || !strings.Contains(err.Error(), "coop apply") {
		t.Fatalf("Up = %v", err)
	}
	if len(m.Started)+len(m.Run_)+len(m.Removed) != 0 {
		t.Fatal("incomplete runtime was mutated")
	}
}

func TestUpConfigMismatchPreservesContainer(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	markRunning(m, s)
	m.ContainerLabels = map[string]map[string]string{s.Name: {SpecLabel: "old"}}
	err := s.Up()
	if !errors.Is(err, workspace.ErrMaintenanceRequired) {
		t.Fatalf("error = %v", err)
	}
	if len(m.Stopped)+len(m.Removed)+len(m.Run_) != 0 {
		t.Fatalf("existing workspace was replaced: stops %q removals %q", m.Stopped, m.Removed)
	}
}
