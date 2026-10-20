package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/tui/team"
)

type teamSnapshot struct {
	rows   []c.Presence
	report c.AgentDiagnostics
	err    error
}

// Read presence first so a report can never lend its identity to a fleet row.
func readTeamDetail(ctx context.Context, source Coordination, placement c.Placement) (any, error) {
	rows, err := source.Team(ctx)
	if err != nil {
		return nil, err
	}
	result := teamSnapshot{rows: rows}
	if !slices.ContainsFunc(rows, func(p c.Presence) bool { return p.Placement == placement }) {
		result.err = c.ErrConflict
		return result, nil
	}
	if source, ok := source.(team.DiagnosticSource); ok {
		result.report, result.err = source.AgentDiagnostics(ctx, placement.AgentID)
	} else {
		result.err = c.ErrUnsupported
	}
	if result.err == nil && (placement.HostID == "" || placement.InstanceID == "" || placement.Generation < 1 || result.report.ReceivedAt.IsZero() || result.report.Report.Placement != placement || c.ValidateDiagnostics(result.report.Report) != nil) {
		result.err = c.ErrConflict
	}
	return result, nil
}

func (m *fleetModel) setTeamRows(rows []c.Presence) {
	visible := m.teamSelectionVisible()
	q := &m.coordination
	id := q.teamSelected
	if q.cursor >= 0 && q.cursor < len(q.team) {
		id = q.team[q.cursor].AgentID
	}
	q.team = rows
	q.cursor = slices.IndexFunc(rows, func(p c.Presence) bool { return p.AgentID == id })
	if q.cursor < 0 && id == "" && len(rows) > 0 {
		q.cursor = 0
		id = rows[0].AgentID
	}
	q.teamSelected = id
	if q.teamDetail != nil && !slices.ContainsFunc(rows, func(p c.Presence) bool { return p.Placement == *q.teamDetail }) {
		q.teamReport = nil
		q.teamFailure = "placement changed or removed; work unknown; return to Team and inspect again"
	}
	if visible {
		m.revealTeamSelection()
	}
}

func (m fleetModel) teamList() bool {
	q := m.coordination
	return m.workspace == 1 && q.section == "team" && q.teamDetail == nil && q.form == nil && q.thread == nil && q.receipt == nil && q.authority == nil
}

func (m fleetModel) teamSelectionVisible() bool {
	if !m.teamList() || m.height <= 0 {
		return false
	}
	_, _, line := m.content()
	return line > m.scroll && line <= m.scroll+max(1, m.height-3)
}

func (m *fleetModel) revealTeamSelection() {
	if !m.teamList() || m.height <= 0 {
		return
	}
	lines, _, line := m.content()
	if line < 0 {
		return
	}
	limit := max(1, m.height-3)
	m.scroll = min(m.scroll, max(0, len(lines)-1-limit))
	line-- // The context header is pinned outside the scrolling body.
	if line < m.scroll || line >= m.scroll+limit {
		m.scroll = min(line, max(0, len(lines)-1-limit))
	}
}

func (m fleetModel) teamLines(master string) []string {
	q := m.coordination
	lines := []string{"Team | Master: " + master, "Read only observations; heartbeat is not work progress"}
	status := "No successful Team snapshot"
	if !q.teamSeen.IsZero() {
		status = "Team snapshot age: " + teamAge(q.teamSeen)
	}
	if q.failed {
		status = "STALE/OFFLINE | " + status
	}
	lines = append(lines, status, q.status)
	if q.teamDetail != nil {
		lines[0] = "Team > Agent: " + q.teamDetail.AgentID + " | Master: " + master
		presence := "Availability / heartbeat: unknown (placement not in current Team snapshot)"
		for _, p := range q.team {
			if p.Placement == *q.teamDetail {
				presence = "Availability (master): " + valueOr(p.State, "unknown") + " | heartbeat: " + teamAge(p.LastSeen)
			}
		}
		lines = append(lines, presence)
		lines = append(lines, team.DetailLines(time.Now(), 0, q.teamDetail.AgentID, q.teamReport, q.busy, q.teamFailure)...)
		return lines
	}
	lines = append(lines, "Enter inspect | arrows select | PgUp/PgDn scroll | Esc Fleet")
	if len(q.team) == 0 {
		lines = append(lines, "No visible agents; work unknown")
	}
	for i, p := range q.team {
		prefix := "  "
		if i == q.cursor {
			prefix = "> "
		}
		lines = append(lines, prefix+p.AgentID+" ("+p.Alias+")", fmt.Sprintf("  Host: %s | Worker: %s | Generation: %d", valueOr(p.HostID, "unknown"), valueOr(p.InstanceID, "unknown"), p.Generation), "  Availability (master): "+valueOr(p.State, "unknown")+" | heartbeat: "+teamAge(p.LastSeen), "  Work/blocker: inspect diagnostics (not inferred)")
	}
	return lines
}

func teamAge(at time.Time) string {
	if at.IsZero() {
		return "unknown"
	}
	if at.After(time.Now()) {
		return "unknown (clock ahead)"
	}
	return time.Since(at).Truncate(time.Second).String() + " ago"
}

func diagnosticFailure(err error) string {
	switch {
	case errors.Is(err, c.ErrForbidden):
		return "diagnostics access denied; work unknown"
	case errors.Is(err, c.ErrUnsupported):
		return "diagnostics unsupported; work unknown"
	case errors.Is(err, c.ErrNotFound):
		return "no visible diagnostic report; work unknown"
	case errors.Is(err, c.ErrConflict):
		return "placement changed or report identity mismatch; work unknown; return and inspect again"
	default:
		return "diagnostics unavailable; current work unknown"
	}
}
