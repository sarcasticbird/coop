package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	textansi "github.com/charmbracelet/x/ansi"
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
	return readTeamReport(ctx, source, placement, rows), nil
}

func readTeamReport(ctx context.Context, source Coordination, placement c.Placement, rows []c.Presence) teamSnapshot {
	result := teamSnapshot{rows: rows}
	if !slices.ContainsFunc(rows, func(p c.Presence) bool { return p.Placement == placement }) {
		result.err = c.ErrConflict
		return result
	}
	if source, ok := source.(team.DiagnosticSource); ok {
		result.report, result.err = source.AgentDiagnostics(ctx, placement.AgentID)
	} else {
		result.err = c.ErrUnsupported
	}
	if result.err == nil && (placement.HostID == "" || placement.InstanceID == "" || placement.Generation < 1 || result.report.ReceivedAt.IsZero() || result.report.Report.Placement != placement || c.ValidateDiagnostics(result.report.Report) != nil) {
		result.err = c.ErrConflict
	}
	return result
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
	return line >= conversationHeader+m.scroll && line < m.scroll+max(conversationHeader+1, m.height-2)
}

func (m *fleetModel) revealTeamSelection() {
	if !m.teamList() || m.height <= 0 {
		return
	}
	lines, _, line := m.content()
	if line < 0 {
		return
	}
	limit := max(1, m.height-conversationHeader-2)
	m.scroll = min(m.scroll, max(0, len(lines)-conversationHeader-limit))
	line -= conversationHeader
	if line < m.scroll || line >= m.scroll+limit {
		m.scroll = min(line, max(0, len(lines)-conversationHeader-limit))
	}
}

func (m fleetModel) teamLines(master string) []string {
	q := m.coordination
	title := "Team"
	if q.teamDetail != nil {
		title = "Team > Agent"
	}
	if m.width > 0 {
		title = textansi.Truncate(strings.ReplaceAll(coordClean(title), "\n", " "), m.width, "...")
	}
	lines := []string{title, "Master: " + master, "Principal: " + valueOr(q.self.Principal.ID, "unknown")}
	for i := range lines {
		if m.width > 0 {
			lines[i] = textansi.Truncate(coordClean(lines[i]), m.width, "...")
		}
	}
	status := "No successful Team snapshot"
	if !q.teamSeen.IsZero() {
		status = "Team snapshot age: " + teamAge(q.teamSeen)
	}
	if q.failed {
		status = "STALE/OFFLINE | " + status
	}
	context := []string{"Source: master Team presence | " + status, "Read only; heartbeat is not work progress"}
	if q.teamDetail != nil {
		name := q.teamDetail.AgentID
		presence := "Presence / heartbeat: unknown"
		for _, p := range q.team {
			if p.Placement == *q.teamDetail {
				name = valueOr(p.Alias, p.AgentID)
				presence = "Presence: " + valueOr(p.State, "unknown") + " | heartbeat: " + teamAge(p.LastSeen)
			}
		}
		if q.failed {
			presence = "STALE | " + presence
		}
		failure := q.teamFailure
		if q.failed && failure == "" {
			failure = "Team snapshot unavailable; work unknown"
		}
		activity, work := diagnosticSummary(time.Now(), q.teamReport, failure)
		agent, host := "Agent: "+strings.ReplaceAll(coordClean(name), "\n", " "), "Host: "+strings.ReplaceAll(coordClean(valueOr(q.teamDetail.HostID, "unknown")), "\n", " ")
		if m.width > 0 {
			agent = textansi.Truncate(agent, m.width, "...")
			host = textansi.Truncate(host, m.width, "...")
		}
		lines = append(lines, agent, host, presence, "Handling: "+activity, "Work / blocker: "+work)
		lines = append(lines, context...)
		lines = append(lines, "Exact Master: "+master, "Exact Principal: "+valueOr(q.self.Principal.ID, "unknown"))
		lines = append(lines, "AgentID: "+q.teamDetail.AgentID, fmt.Sprintf("Captured Host: %s | Worker: %s | Generation: %d", valueOr(q.teamDetail.HostID, "unknown"), valueOr(q.teamDetail.InstanceID, "unknown"), q.teamDetail.Generation))
		lines = append(lines, team.DetailLines(time.Now(), 0, q.teamDetail.AgentID, q.teamReport, q.busy, q.teamFailure)...)
		return lines
	}
	lines = append(lines, context...)
	if m.width >= 100 || m.width <= 0 {
		lines = append(lines, teamLedgerRow(m.width, "  ", "AGENT", "HOST", "PRESENCE", "HANDLING", "WORK / BLOCKER"))
	} else {
		lines = append(lines, "  AGENT / HOST")
	}
	if len(q.team) == 0 {
		lines = append(lines, "No visible agents; work unknown")
	}
	for i, p := range q.team {
		prefix := "  "
		if i == q.cursor {
			prefix = "> "
		}
		name, host := valueOr(p.Alias, p.AgentID), valueOr(p.HostID, "unknown")
		if m.width >= 100 || m.width <= 0 {
			lines = append(lines, teamLedgerRow(m.width, prefix, name, host, valueOr(p.State, "unknown"), "unknown", "unknown"))
		} else {
			width := max(1, m.width)
			nameWidth := max(1, (width-5)/2)
			lines = append(lines, prefix+ledgerCell(name, nameWidth)+" / "+ledgerCell(host, max(1, width-5-nameWidth)), textansi.Truncate("  Presence: "+coordClean(valueOr(p.State, "unknown")), width, "..."), textansi.Truncate("  Handling/work: unknown", width, "..."))
		}
	}
	return lines
}

func teamLedgerRow(width int, prefix, name, host, presence, handling, work string) string {
	return prefix + ledgerCell(name, 18) + "  " + ledgerCell(host, 16) + "  " + ledgerCell(presence, 12) + "  " + ledgerCell(handling, 12) + "  " + ledgerCell(work, max(14, width-68))
}

func teamAge(at time.Time) string {
	if at.IsZero() {
		return "unknown"
	}
	if at.After(time.Now()) {
		return "unknown (clock ahead)"
	}
	label := time.Since(at).Truncate(time.Second).String() + " ago"
	if time.Since(at) >= c.PresenceStale {
		label = "STALE " + label
	}
	return label
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
