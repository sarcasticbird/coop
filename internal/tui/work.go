package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

// Coordination uses the existing authenticated master, never a host selected
// from the table. Kata remains the task and conversation UI.
type Coordination interface {
	MasterID() string
	Self(context.Context) (c.SelfIdentity, error)
	Team(context.Context) ([]c.Presence, error)
	AgentDiagnostics(context.Context, string) (c.AgentDiagnostics, error)
	TaskCapabilities(context.Context) (c.TaskCapabilities, error)
	Threads(context.Context, c.ThreadQuery) (c.ThreadPage, error)
	Thread(context.Context, string) (c.Thread, error)
	Send(context.Context, c.SendMessage) (c.MessageReceipt, error)
}
type workLoaded struct {
	reports map[hostrouter.Target]c.AgentDiagnostics
}

// Host-owned stable bird identity maps the row to the master's current placement.
// A container replacement must not make its coworker permanently "unknown" just
// because a display-only inventory binding still contains the old instance ID.
func birdPlacement(source Coordination, e hostrouter.InventoryEntry, team []c.Presence) (c.Placement, error) {
	if source == nil || e.Target.Kind != hostrouter.Agent || e.Diagnostics == nil || c.ValidateDiagnosticBinding(*e.Diagnostics) != nil || e.Diagnostics.MasterID != source.MasterID() {
		return c.Placement{}, c.ErrUnavailable
	}
	var found *c.Placement
	for _, p := range team {
		if p.AgentID == e.Diagnostics.AgentID && p.HostID == e.Diagnostics.HostID {
			if found != nil {
				return c.Placement{}, c.ErrConflict
			}
			placement := p.Placement
			found = &placement
		}
	}
	if found == nil {
		return c.Placement{}, c.ErrNotFound
	}
	return *found, nil
}
func (m fleetModel) uniqueBird(e hostrouter.InventoryEntry) bool {
	if e.Diagnostics == nil {
		return false
	}
	count := 0
	for _, row := range m.rows() {
		b := row.Diagnostics
		if b != nil && b.MasterID == e.Diagnostics.MasterID && b.AgentID == e.Diagnostics.AgentID && b.HostID == e.Diagnostics.HostID {
			count++
		}
	}
	return count == 1
}
func (m *fleetModel) refreshWork() tea.Cmd {
	if m.workBusy || m.busy || m.services.Coordination == nil {
		return nil
	}
	var rows []hostrouter.InventoryEntry
	for _, row := range m.rows() {
		if m.current(row.Target) && row.Diagnostics != nil && m.uniqueBird(row) {
			copy := *row.Diagnostics
			row.Diagnostics = &copy
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	m.workBusy = true
	source, ctx := m.services.Coordination, m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out := workLoaded{reports: map[hostrouter.Target]c.AgentDiagnostics{}}
		team, err := source.Team(ctx)
		if err != nil {
			return out
		}
		for _, row := range rows {
			placement, err := birdPlacement(source, row, team)
			if err != nil {
				continue
			}
			report, err := source.AgentDiagnostics(ctx, placement.AgentID)
			if err == nil && report.Report.Placement == placement && !report.Stale && fresh(report.ReceivedAt) && fresh(report.Report.ObservedAt) {
				out.reports[row.Target] = report
			}
		}
		return out
	}
}
func (m fleetModel) task(target hostrouter.Target) (c.AgentDiagnostics, bool) {
	e, ok := m.entry(target)
	report, exists := m.work[target]
	if !ok || !exists || !m.uniqueBird(e) || m.services.Coordination == nil || e.Diagnostics.MasterID != m.services.Coordination.MasterID() || report.Stale || !fresh(report.ReceivedAt) || !fresh(report.Report.ObservedAt) || !fresh(report.Report.Work.ObservedAt) || report.Report.AgentID != e.Diagnostics.AgentID || report.Report.HostID != e.Diagnostics.HostID {
		return c.AgentDiagnostics{}, false
	}
	return report, true
}
