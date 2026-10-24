package tui

import (
	"context"
	"reflect"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

type ledgerDiagnosticEntry struct {
	binding c.DiagnosticBinding
	fleetDiagnostics
}

type ledgerDiagnostics struct {
	entries map[hostrouter.Target]ledgerDiagnosticEntry
	serial  uint64
	busy    bool
	cancel  context.CancelFunc
	next    time.Time
	self    c.SelfIdentity
	master  string
	checked time.Time
}

type ledgerDiagnosticResult struct {
	serial  uint64
	self    c.SelfIdentity
	master  string
	checked time.Time
	results []fleetDiagnosticResult
}

// One batch at a time, one authenticated identity/Team snapshot per batch, and
// serial report reads: inventory size cannot create unbounded concurrency.
func (m *fleetModel) refreshLedgerDiagnostics() tea.Cmd {
	q := &m.ledgerDiagnostics
	if m.workspace != 0 || q.busy || time.Now().Before(q.next) || m.services.Coordination == nil {
		return nil
	}
	if q.entries == nil {
		q.entries = make(map[hostrouter.Target]ledgerDiagnosticEntry)
	}
	source := m.services.Coordination
	var requests []fleetDiagnosticResult
	for _, h := range m.hosts {
		for _, e := range h.entries {
			if _, ok := m.entry(e.Target); !ok || e.Target.Kind != hostrouter.Agent || e.Diagnostics == nil || c.ValidateDiagnosticBinding(*e.Diagnostics) != nil || e.Diagnostics.MasterID != source.MasterID() || !m.diagnosticBindingUnique(*e.Diagnostics) {
				continue
			}
			entry := q.entries[e.Target]
			if entry.binding != *e.Diagnostics {
				entry = ledgerDiagnosticEntry{binding: *e.Diagnostics}
			}
			q.entries[e.Target] = entry
			requests = append(requests, fleetDiagnosticResult{target: e.Target, binding: *e.Diagnostics})
		}
	}
	q.next = time.Now().Add(5 * time.Second)
	if len(requests) == 0 {
		return nil
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	q.serial++
	q.busy, q.cancel = true, cancel
	serial, master, checked := q.serial, source.MasterID(), time.Now()
	return func() tea.Msg {
		defer cancel()
		result := ledgerDiagnosticResult{serial: serial, master: master, checked: checked, results: requests}
		identity, err := source.Self(ctx)
		result.self = identity
		var rows []c.Presence
		if err == nil {
			rows, err = source.Team(ctx)
		}
		for i := range result.results {
			r := &result.results[i]
			r.self, r.checked, r.epoch, r.err = identity, checked, identity.RestoreEpoch, err
			if err == nil {
				r.snapshot = readTeamReport(ctx, source, r.binding.Placement, rows)
			}
		}
		return result
	}
}

func (m *fleetModel) reconcileLedgerDiagnostics() {
	q := &m.ledgerDiagnostics
	invalid := false
	for target, cached := range q.entries {
		e, ok := m.entry(target)
		if !ok || !sameBinding(e.Diagnostics, &cached.binding) || !m.diagnosticBindingUnique(cached.binding) {
			delete(q.entries, target)
			invalid = true
		}
	}
	if invalid {
		if q.cancel != nil {
			q.cancel()
		}
		q.serial++
		q.busy, q.cancel, q.next = false, nil, time.Time{}
	}
}

func (m fleetModel) ledgerDiagnosticUpdate(r ledgerDiagnosticResult) (tea.Model, tea.Cmd) {
	q := &m.ledgerDiagnostics
	if r.serial != q.serial || m.services.Coordination == nil || r.master != m.services.Coordination.MasterID() {
		return m, nil
	}
	visible := m.ledgerSelectionVisible()
	q.busy, q.cancel = false, nil
	q.next = time.Now().Add(5 * time.Second)
	if r.master != q.master || !ledgerSameContext(r.self, q.self) {
		for target, entry := range q.entries {
			entry.report = nil
			q.entries[target] = entry
		}
	}
	q.self, q.master, q.checked = r.self, r.master, r.checked
	m.invalidateFleetDiagnosticContext()
	for _, result := range r.results {
		entry, ok := q.entries[result.target]
		if !ok || entry.binding != result.binding {
			continue
		}
		m.acceptFleetDiagnostic(&entry.fleetDiagnostics, result)
		q.entries[result.target] = entry
	}
	if visible {
		m.revealLedgerSelection()
	}
	return m, nil
}

func ledgerSameContext(a, b c.SelfIdentity) bool {
	return reflect.DeepEqual(a, b)
}
