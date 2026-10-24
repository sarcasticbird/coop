package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	textansi "github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

type fleetDiagnostics struct {
	self     c.SelfIdentity
	checked  time.Time
	serial   uint64
	busy     bool
	cancel   context.CancelFunc
	next     time.Time
	report   *c.AgentDiagnostics
	presence *c.Presence
	failure  string
}

type fleetDiagnosticResult struct {
	self     c.SelfIdentity
	checked  time.Time
	serial   uint64
	target   hostrouter.Target
	binding  c.DiagnosticBinding
	snapshot teamSnapshot
	epoch    int64
	err      error
}

func sameBinding(a, b *c.DiagnosticBinding) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (m *fleetModel) resetFleetDiagnostics() {
	if m.diagnostics.cancel != nil {
		m.diagnostics.cancel()
	}
	m.diagnostics = fleetDiagnostics{serial: m.diagnostics.serial + 1}
}

func (m fleetModel) diagnosticBindingUnique(binding c.DiagnosticBinding) bool {
	count := 0
	for _, h := range m.hosts {
		for _, e := range h.entries {
			if e.Diagnostics != nil && *e.Diagnostics == binding {
				count++
			}
		}
	}
	return count == 1
}

func (m *fleetModel) refreshFleetDiagnostics() tea.Cmd {
	q := &m.diagnostics
	if !m.detail || m.detailEntry == nil || q.busy {
		return nil
	}
	q.next = time.Now().Add(5 * time.Second)
	if m.detailInvalid {
		q.report, q.failure = nil, "target changed; return and inspect again"
		return nil
	}
	e, ok := m.entry(m.detailEntry.Target)
	if !ok {
		q.failure = "host inventory unavailable; current work unknown"
		return nil
	}
	if !sameBinding(e.Diagnostics, m.detailEntry.Diagnostics) {
		m.resetFleetDiagnostics()
		m.detailInvalid = true
		m.diagnostics.failure = "diagnostic binding changed; return and inspect again"
		return nil
	}
	if e.Diagnostics == nil {
		q.failure = "no host-owned diagnostic binding; work unknown"
		return nil
	}
	binding := *e.Diagnostics
	if c.ValidateDiagnosticBinding(binding) != nil || !m.diagnosticBindingUnique(binding) {
		q.report, q.failure = nil, "ambiguous or invalid diagnostic binding; work unknown"
		return nil
	}
	source := m.services.Coordination
	if source == nil || source.MasterID() != binding.MasterID {
		q.report, q.failure = nil, "bound master unavailable or mismatched; work unknown"
		return nil
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	q.serial++
	q.busy, q.cancel = true, cancel
	serial, target := q.serial, e.Target
	return func() tea.Msg {
		defer cancel()
		r := fleetDiagnosticResult{serial: serial, target: target, binding: binding, checked: time.Now()}
		identity, err := source.Self(ctx)
		r.self, r.epoch, r.err = identity, identity.RestoreEpoch, err
		if err == nil {
			value, err := readTeamDetail(ctx, source, binding.Placement)
			r.err = err
			if err == nil {
				r.snapshot = value.(teamSnapshot)
			}
		}
		return r
	}
}

func (m fleetModel) fleetDiagnosticUpdate(r fleetDiagnosticResult) (tea.Model, tea.Cmd) {
	q := &m.diagnostics
	if r.serial != q.serial || !m.detail || m.detailEntry == nil || m.detailEntry.Target != r.target || m.detailInvalid || !sameBinding(m.detailEntry.Diagnostics, &r.binding) {
		return m, nil
	}
	q.busy, q.cancel = false, nil
	q.next = time.Now().Add(5 * time.Second)
	m.acceptFleetDiagnostic(q, r)
	return m, nil
}

func (m fleetModel) acceptFleetDiagnostic(q *fleetDiagnostics, r fleetDiagnosticResult) {
	e, ok := m.entry(r.target)
	if !ok {
		q.failure = "host inventory unavailable; current work unknown"
		return
	}
	if !sameBinding(e.Diagnostics, &r.binding) || !m.diagnosticBindingUnique(r.binding) {
		q.report, q.failure = nil, "diagnostic binding changed or ambiguous; work unknown"
		return
	}
	if q.report != nil && r.epoch > 0 && q.report.Report.RestoreEpoch != r.epoch {
		q.report = nil
	}
	if r.err != nil {
		q.failure = diagnosticFailure(r.err)
		if errors.Is(r.err, c.ErrConflict) || errors.Is(r.err, c.ErrEpoch) {
			q.report = nil
		}
		return
	}
	if m.fleetDiagnosticContextChanged(fleetDiagnostics{self: r.self, checked: r.checked}) {
		q.report, q.presence = nil, nil
		q.failure = "access context changed; work unknown"
		return
	}
	s := r.snapshot
	if s.err != nil {
		q.failure = diagnosticFailure(s.err)
		if errors.Is(s.err, c.ErrConflict) || errors.Is(s.err, c.ErrEpoch) {
			q.report = nil
		}
		return
	}
	if s.report.Report.Placement != r.binding.Placement || r.epoch < 1 || s.report.Report.RestoreEpoch != r.epoch {
		q.report, q.failure = nil, "report identity/epoch mismatch; work unknown"
		return
	}
	if q.report == nil || s.report.Report.RestoreEpoch > q.report.Report.RestoreEpoch || (s.report.Report.RestoreEpoch == q.report.Report.RestoreEpoch && !s.report.Report.ObservedAt.Before(q.report.Report.ObservedAt) && !s.report.ReceivedAt.Before(q.report.ReceivedAt)) {
		q.report, q.failure = &s.report, ""
		q.checked, q.self = r.checked, r.self
		q.self.Principal.Engagements = slices.Clone(r.self.Principal.Engagements)
		q.self.Principal.Capabilities = slices.Clone(r.self.Principal.Capabilities)
		q.presence = nil
		for _, p := range s.rows {
			if p.Placement == r.binding.Placement {
				q.presence = &p
			}
		}
	} else {
		q.failure = "out-of-order report ignored; current work unknown"
	}
}

// Diagnostic evidence belongs to the complete authenticated read context. A
// later accepted identity or denial invalidates it until a fresh read succeeds.
func (m fleetModel) fleetDiagnosticContextChanged(q fleetDiagnostics) bool {
	if q.checked.IsZero() {
		return false
	}
	return m.coordination.checked.After(q.checked) && (m.coordination.failed || !ledgerSameContext(m.coordination.self, q.self)) || m.ledgerDiagnostics.checked.After(q.checked) && !ledgerSameContext(m.ledgerDiagnostics.self, q.self) || m.work.checked.After(q.checked) && !ledgerSameContext(m.work.self, q.self)
}

func (m *fleetModel) invalidateFleetDiagnosticContext() {
	if m.diagnostics.report != nil && m.fleetDiagnosticContextChanged(m.diagnostics) {
		m.diagnostics.report, m.diagnostics.presence = nil, nil
		m.diagnostics.failure = "access context changed; work unknown"
	}
}

func (m fleetModel) fleetPresenceLine() string {
	q := m.diagnostics
	if q.failure != "" || q.presence == nil || m.detailInvalid {
		return "Presence: unknown (no current bound observation)"
	}
	p := q.presence
	return "Presence: " + valueOr(p.State, "unknown") + " | heartbeat: " + teamAge(p.LastSeen)
}

func (m fleetModel) fleetDetailsLines(state, health string) []string {
	e, q, now := *m.detailEntry, m.diagnostics, time.Now()
	lines := m.pageContext("Fleet > Details")
	if m.preview {
		lines = []string{"Fleet > Work", "Bird: " + ledgerName(e), "Host: " + e.Target.Host.Name}
	}
	row := func(left, right string) {
		left, right = coordClean(left), coordClean(right)
		column := (m.width - 3) / 2
		if m.width <= 0 {
			column = 58
		}
		if right != "" && (m.width >= 65 || m.width <= 0) && textansi.StringWidth(left) <= column && textansi.StringWidth(right) <= column {
			lines = append(lines, left+strings.Repeat(" ", column-textansi.StringWidth(left))+" | "+right)
		} else {
			lines = append(lines, left)
			if right != "" {
				lines = append(lines, right)
			}
		}
	}
	failure := q.failure
	if m.detailInvalid {
		failure = "target changed; work unknown"
	}
	activity, _ := diagnosticSummary(now, q.report, failure)
	current := q.report != nil && failure == "" && !q.report.Stale
	if current {
		for _, at := range []time.Time{q.report.ReceivedAt, q.report.Report.ObservedAt, q.report.Report.Wait.ObservedAt} {
			current = current && !at.IsZero() && !at.After(now) && now.Sub(at) < c.PresenceStale
		}
	}
	task, pr, blocker := "unknown", "unknown", "unknown"
	progress := diagnosticProgress(now, q.report, failure)
	if current {
		d := q.report.Report
		if at := d.Work.ObservedAt; !at.IsZero() && !at.After(now) && now.Sub(at) < c.PresenceStale {
			task, pr = diagnosticWorkName(d.Work), valueOr(diagnosticReference(d.Work.PRURL), "none reported")
			if d.Work.Stage != "" {
				task += " / " + strings.ReplaceAll(d.Work.Stage, "_", " ")
			}
		}
		blocker = diagnosticBlocker(d.Wait)
	}
	if !m.preview {
		row("Name: "+ledgerName(e), "Host: "+e.Target.Host.Name+" / "+valueOr(e.Target.Host.Address, "this machine"))
	}
	row("REPORTED WORK", "")
	row("Activity: "+activity, "")
	row("Task: "+task, "PR: "+pr)
	row("Progress: "+progress, "")
	row("Blocker: "+blocker, "")
	row("OBSERVATION", "")
	source := "Source: no host-owned diagnostic binding"
	if e.Diagnostics != nil {
		source = "Source: mapped master diagnostics"
		if !current {
			source = "Source: master diagnostics; no current report"
		}
	}
	row(source, "")
	row("VM lifecycle: "+state, "Host health: "+health)
	row(m.fleetPresenceLine(), "")
	if q.report != nil {
		row("Report age: "+teamAge(q.report.Report.ObservedAt), "Receipt age: "+teamAge(q.report.ReceivedAt))
		row("Work age: "+teamAge(q.report.Report.Work.ObservedAt), "Wait age: "+teamAge(q.report.Report.Wait.ObservedAt))
	}
	row("Heartbeat is not work progress.", "")
	if m.preview {
		return append(lines, "Tab: Details and diagnostics | g: goal decisions")
	}
	row("TARGET", "")
	if e.Name != ledgerName(e) {
		row("Runtime: "+e.Name, "")
	}
	if e.Project != "" {
		row("Project: "+e.Project, "")
	}
	row("Target: "+identity(e.Target), "")
	// Repeat only clipped context; the pinned header already holds short IDs.
	for _, context := range lines[1:conversationHeader] {
		if m.width > 0 && textansi.StringWidth(context) > m.width {
			row("Exact "+context, "")
		}
	}
	return append(lines, "l then t: full Team diagnostics")
}
