package session

import (
	"errors"
	"testing"

	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/workspace"
)

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
