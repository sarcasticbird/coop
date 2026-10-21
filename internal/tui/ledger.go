package tui

import (
	"path/filepath"
	"strings"
	"time"

	textansi "github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

func ledgerName(e hostrouter.InventoryEntry) string {
	name := e.Name
	if e.Target.Kind == hostrouter.Agent && (name == "" || name == "coop-agent-"+e.Target.ID) {
		name = e.Target.ID
	} else if e.Target.Kind == hostrouter.Interactive && e.Project != "" {
		name = filepath.Base(e.Project)
	}
	return coordClean(valueOr(name, e.Target.ID))
}

func ledgerCell(s string, width int) string {
	s = textansi.Truncate(strings.ReplaceAll(coordClean(s), "\n", " "), width, "...")
	return s + strings.Repeat(" ", max(0, width-textansi.StringWidth(s)))
}

func ledgerRow(width int, prefix, name, kind, vm, activity, work string) string {
	if width >= 100 || width <= 0 {
		return prefix + ledgerCell(name, 18) + "  " + ledgerCell(kind, 11) + "  " + ledgerCell(vm, 9) + "  " + ledgerCell(activity, 15) + "  " + ledgerCell(work, max(14, width-63))
	}
	if width >= 65 {
		return prefix + ledgerCell(name, 16) + "  " + ledgerCell(kind, 11) + "  " + ledgerCell(vm, 9) + "  " + ledgerCell(activity+" / "+work, width-44)
	}
	return prefix + ledgerCell(name, max(4, width-25)) + "  " + ledgerCell(kind, 11) + "  " + ledgerCell(vm, 8)
}

func diagnosticReference(ref string) string {
	parts := strings.Split(ref, "/")
	if len(parts) >= 7 {
		return parts[4] + " #" + parts[6]
	}
	return ref
}

func diagnosticSummary(now time.Time, report *c.AgentDiagnostics, failure string) (string, string) {
	if failure != "" || report == nil {
		reason, _, _ := strings.Cut(failure, ";")
		reason = strings.TrimPrefix(reason, "diagnostics ")
		if strings.Contains(reason, "placement changed") {
			reason = "placement changed"
		}
		if strings.Contains(reason, "out-of-order") {
			reason = "older report ignored"
		}
		return "unknown", valueOr(reason, "no visible report")
	}
	d := report.Report
	for _, at := range []time.Time{report.ReceivedAt, d.ObservedAt, d.Wait.ObservedAt} {
		if at.IsZero() {
			return "unknown", "observation time unknown"
		}
		if at.After(now) {
			return "unknown", "clock ahead"
		}
		if now.Sub(at) >= c.PresenceStale || report.Stale {
			return "stale", "old diagnostic report"
		}
	}
	activity := strings.ReplaceAll(valueOr(d.Wait.Handling, "unknown"), "_", " ")
	work := "work unknown"
	if !d.Work.ObservedAt.IsZero() && !d.Work.ObservedAt.After(now) && now.Sub(d.Work.ObservedAt) < c.PresenceStale {
		work = valueOr(diagnosticReference(d.Work.TaskURL), "no task reported")
		if d.Work.Stage != "" {
			work += " / " + strings.ReplaceAll(d.Work.Stage, "_", " ")
		}
	} else if d.Work.ObservedAt.After(now) {
		work = "work clock ahead"
	} else if !d.Work.ObservedAt.IsZero() {
		work = "work stale"
	}
	if len(d.Wait.Dependencies) > 0 {
		work += " / dependency " + diagnosticReference(d.Wait.Dependencies[0])
	} else if d.Wait.Reason != "" {
		work += " / " + strings.ReplaceAll(d.Wait.Reason, "_", " ")
	}
	return activity, work
}

func (m fleetModel) ledgerSummary(e hostrouter.InventoryEntry) (string, string) {
	if _, ok := m.entry(e.Target); !ok {
		return "unknown", "host unavailable"
	}
	for _, h := range m.hosts {
		if h.host == e.Target.Host && (h.updated.After(time.Now()) || time.Since(h.updated) >= c.PresenceStale) {
			return "unknown", "inventory stale"
		}
	}
	if e.Target.Kind != hostrouter.Agent {
		return "unknown", "not an agent"
	}
	if e.Diagnostics == nil {
		return "unknown", "not bound"
	}
	if c.ValidateDiagnosticBinding(*e.Diagnostics) != nil || !m.diagnosticBindingUnique(*e.Diagnostics) {
		return "unknown", "ambiguous binding"
	}
	if m.services.Coordination == nil || m.services.Coordination.MasterID() != e.Diagnostics.MasterID {
		return "unknown", "master unavailable"
	}
	entry, ok := m.ledgerDiagnostics.entries[e.Target]
	if !ok || entry.binding != *e.Diagnostics {
		return "unknown", "no visible report"
	}
	if m.coordination.failed || m.coordination.seen.After(m.ledgerDiagnostics.checked) && !ledgerSameContext(m.coordination.self, m.ledgerDiagnostics.self) {
		return "unknown", "access context changed"
	}
	return diagnosticSummary(time.Now(), entry.report, entry.failure)
}
