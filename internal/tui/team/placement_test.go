package team

import (
	"slices"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestPlacementReplacementInvalidatesPendingReport(t *testing.T) {
	m := New(t.Context(), nil, "fixture-master")
	defer m.Close()
	v := teamDetailFixture("hawk")
	p := c.Presence{Placement: v.Report.Placement, Alias: "duplicate"}
	m.Update(resultMsg{rows: []c.Presence{p}, at: time.Now()})
	m.detailOpen, m.detailAgent, m.detailPlacement, m.detailID = true, p.AgentID, p.Placement, 1
	m.detail = &v
	oldID := m.detailID
	p.InstanceID = "replacement"
	p.Generation++
	m.Update(resultMsg{rows: []c.Presence{p}, at: time.Now()})
	m.Update(diagnosticResultMsg{id: oldID, value: v, at: time.Now()})
	if m.detail != nil || !strings.Contains(m.detailFailure, "placement changed") {
		t.Fatal("replacement retained or accepted old worker report")
	}
	if m.refreshDetail() != nil {
		t.Fatal("old detail retargeted replacement")
	}
}

func TestSelectedAgentSurvivesReorderAndRemoval(t *testing.T) {
	m := New(t.Context(), nil, "fixture-master")
	defer m.Close()
	rows := []c.Presence{{Placement: c.Placement{AgentID: "one"}, Alias: "duplicate"}, {Placement: c.Placement{AgentID: "two"}, Alias: "duplicate"}}
	m.Update(resultMsg{rows: slices.Clone(rows), at: time.Now()})
	m.Update(key("tab"))
	slices.Reverse(rows)
	m.Update(resultMsg{rows: rows, at: time.Now()})
	if m.selected != 0 || m.rows[m.selected].AgentID != "two" {
		t.Fatal("reorder changed selected identity")
	}
	for range 2 {
		m.Update(resultMsg{rows: []c.Presence{rows[1]}, at: time.Now()})
	}
	if m.selected != -1 || m.openDetail() != nil {
		t.Fatal("removed selection silently retargeted")
	}
}

func TestDetailRejectsWrongPlacementAndOlderReport(t *testing.T) {
	m := New(t.Context(), nil, "fixture-master")
	defer m.Close()
	v := teamDetailFixture("hawk")
	m.detailOpen, m.detailAgent, m.detailPlacement, m.detailID = true, "hawk", v.Report.Placement, 1
	m.detail = &v
	old := v
	old.Report.ObservedAt = old.Report.ObservedAt.Add(-time.Minute)
	m.Update(diagnosticResultMsg{id: 1, value: old, at: time.Now()})
	if m.detail != &v || !strings.Contains(m.detailFailure, "out-of-order") {
		t.Fatal("older report replaced snapshot")
	}
	wrong := v
	wrong.Report.HostID = "elsewhere"
	m.Update(diagnosticResultMsg{id: 1, value: wrong, at: time.Now()})
	if m.detail != nil || !strings.Contains(m.detailFailure, "mismatch") {
		t.Fatal("report attached to wrong placement")
	}
}

func TestDetailOrderingResetsAcrossRestoreEpoch(t *testing.T) {
	m := New(t.Context(), nil, "fixture-master")
	defer m.Close()
	old := teamDetailFixture("hawk")
	m.detailOpen, m.detailAgent, m.detailPlacement, m.detailID = true, "hawk", old.Report.Placement, 1
	m.detail = &old
	current := old
	current.Report.RestoreEpoch++
	current.Report.ObservedAt = old.Report.ObservedAt.Add(-time.Minute)
	current.ReceivedAt = old.ReceivedAt.Add(time.Second)
	m.Update(diagnosticResultMsg{id: 1, value: current, at: time.Now()})
	if m.detail.Report.RestoreEpoch != current.Report.RestoreEpoch || m.detailFailure != "" {
		t.Fatal("previous epoch survived valid current-epoch report", m.detailFailure)
	}
	m.Update(diagnosticResultMsg{id: 1, value: old, at: time.Now()})
	if m.detail.Report.RestoreEpoch != current.Report.RestoreEpoch || !strings.Contains(m.detailFailure, "out-of-order") {
		t.Fatal("late previous-epoch report replaced current report")
	}
}

func TestScrolledDetailFailureRemainsVisible(t *testing.T) {
	m := New(t.Context(), nil, "fixture-master")
	defer m.Close()
	v := teamDetailFixture("hawk")
	for range c.MaxDiagnosticTransitions {
		v.Report.Transitions = append(v.Report.Transitions, c.DiagnosticTransition{At: time.Now(), Stage: "implement", Phase: "waiting", Origin: "worker"})
	}
	m.detailOpen, m.detailAgent, m.detailPlacement, m.detailID = true, "hawk", v.Report.Placement, 1
	m.detail = &v
	m.width, m.height, m.offset = 40, 20, 25
	m.Update(diagnosticResultMsg{id: 1, err: c.ErrForbidden, at: time.Now()})
	if m.offset != 25 || !strings.Contains(m.View(), "STALE") || !strings.Contains(m.View(), "denied") {
		t.Fatal("CLI Team detail hid scrolled diagnostic failure", m.View())
	}
}
