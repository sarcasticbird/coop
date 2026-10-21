package team

import (
	"context"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestRefreshKeepsSuccessfulSnapshotVisible(t *testing.T) {
	s := sourceFunc(func(context.Context) ([]c.Presence, error) { return nil, nil })
	m := New(t.Context(), s, "fixture")
	defer m.Close()
	m.Update(resultMsg{at: time.Now(), rows: []c.Presence{{Alias: "Hawk", State: "idle"}}})
	before := m.View()
	if m.refresh() == nil {
		t.Fatal("refresh was not scheduled")
	}
	if got := m.View(); got != before {
		t.Fatalf("background refresh changed cached view\nbefore:\n%s\nafter:\n%s", before, got)
	}
}

func TestDiagnosticRefreshDoesNotInsertOrShiftRows(t *testing.T) {
	m := New(t.Context(), detailSource{read: func(context.Context, string) (c.AgentDiagnostics, error) { return teamDetailFixture("hawk"), nil }}, "fixture")
	defer m.Close()
	m.height = 60
	m.detailOpen, m.detailAgent = true, "hawk"
	snapshot := teamDetailFixture("hawk")
	m.detailPlacement = snapshot.Report.Placement
	m.rows = []c.Presence{{Placement: snapshot.Report.Placement}}
	m.detail = &snapshot
	before := m.View()
	if m.refreshDetail() == nil {
		t.Fatal("diagnostic refresh was not scheduled")
	}
	if got := m.View(); got != before {
		t.Fatalf("diagnostic refresh shifted cached rows\nbefore:\n%s\nafter:\n%s", before, got)
	}
}
