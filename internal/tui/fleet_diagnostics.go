package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/tui/team"
)

type fleetDiagnostics struct {
	serial  uint64
	busy    bool
	cancel  context.CancelFunc
	next    time.Time
	report  *c.AgentDiagnostics
	failure string
}

type fleetDiagnosticResult struct {
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
		r := fleetDiagnosticResult{serial: serial, target: target, binding: binding}
		identity, err := source.Self(ctx)
		r.epoch, r.err = identity.RestoreEpoch, err
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
	} else {
		q.failure = "out-of-order report ignored; current work unknown"
	}
}

func (m fleetModel) fleetDiagnosticLines() []string {
	if m.detailEntry.Diagnostics == nil {
		return []string{"Agent work / blocker: unknown (not inferred from VM)", "Source: no host-owned diagnostic binding for this target", "2 then t: independent Team diagnostics; no name-based mapping"}
	}
	b := m.detailEntry.Diagnostics
	lines := []string{"Diagnostic master: " + b.MasterID + " | source: host-owned target binding", fmt.Sprintf("Bound host: %s | worker: %s | generation: %d", b.HostID, b.InstanceID, b.Generation)}
	return append(lines, team.DetailLines(time.Now(), 0, b.AgentID, m.diagnostics.report, m.diagnostics.busy, m.diagnostics.failure)...)
}
