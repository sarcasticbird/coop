package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	textansi "github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

func TestLedgerRecoversAfterCoordinationFailure(t *testing.T) {
	for _, failure := range []error{c.ErrNotFound, c.ErrForbidden} {
		t.Run(failure.Error(), func(t *testing.T) {
			m, _ := boundFleetFixture()
			e := m.hosts[1].entries[0]
			m = runLedgerDiagnostic(t, m)
			m.workspace = 1
			m = coordinationRun(t, m, m.coordCall("read", func(context.Context, Coordination) (any, error) {
				return nil, failure
			}))
			if !m.coordination.failed {
				t.Fatal("Coordination failure not recorded")
			}
			m, _ = fleetKey(m, "1")
			if activity, _ := m.ledgerSummary(e); activity != "unknown" {
				t.Fatal("newer failure did not suppress the old report")
			}
			m = runLedgerDiagnostic(t, m)
			if activity, work := m.ledgerSummary(e); activity != "waiting" {
				t.Fatal("new successful Fleet authorization did not restore activity", activity, work)
			}
			if !m.coordination.failed {
				t.Fatal("Fleet recovery incorrectly cleared separate Coordination failure")
			}
		})
	}
}

func TestLedgerLateReadCannotOverrideNewerCoordinationEvidence(t *testing.T) {
	for _, change := range []string{"denied", "context"} {
		t.Run(change, func(t *testing.T) {
			m, source := boundFleetFixture()
			e := m.hosts[1].entries[0]
			cmd := m.refreshLedgerDiagnostics()
			late := cmd()
			if change == "context" {
				source.self.Principal.ID = "replacement-reader"
			}
			m = coordinationRun(t, m, m.coordCall("read", func(context.Context, Coordination) (any, error) {
				if change == "denied" {
					return nil, c.ErrForbidden
				}
				return nil, nil
			}))
			out, _ := m.Update(late)
			m = out.(fleetModel)
			if activity, _ := m.ledgerSummary(e); activity != "unknown" {
				t.Fatal("late successful read overrode newer denied/incompatible context")
			}
			m = runLedgerDiagnostic(t, m)
			if activity, work := m.ledgerSummary(e); activity != "waiting" {
				t.Fatal("newly authorized Fleet read could not recover", activity, work)
			}
		})
	}
}

func TestLedgerCellAlignmentAndLabels(t *testing.T) {
	for _, width := range []int{120, 100, 80, 65, 40} {
		for _, label := range []string{"Hawk", strings.Repeat("long-label", 20), "\u754c\u754ce\u0301", "bad\nlabel\x1b\x07\t"} {
			t.Run(fmt.Sprintf("%d/%q", width, label), func(t *testing.T) {
				row := ledgerRow(width, "> ", label, "interactive", "running", "waiting", "coop #132 / dependency #131")
				if textansi.StringWidth(row) != width || strings.ContainsAny(row, "\n\x1b\x07\t") {
					t.Fatalf("row lost terminal-cell alignment: %q (%d)", row, textansi.StringWidth(row))
				}
				kind := strings.Index(row, "interactive")
				want := 22
				if width < 100 {
					want = 20
				}
				if width < 65 {
					want = width - 21
				}
				if kind < 0 || textansi.StringWidth(row[:kind]) != want {
					t.Fatalf("kind column moved: %q", row)
				}
			})
		}
	}
	m := fixtureFleet()
	e := m.hosts[1].entries[0]
	e.Name = "coop-agent-" + e.Target.ID
	if ledgerName(e) != e.Target.ID {
		t.Fatal("generated runtime dominates agent label")
	}
	e.Target.Kind, e.Project = hostrouter.Interactive, "/work/customer-project"
	if ledgerName(e) != "customer-project" {
		t.Fatal("project label lost to generated runtime")
	}
	m.width = 120
	view := m.View()
	for _, column := range []string{"BIRD / HOST", "REPORTED ACTIVITY"} {
		if !strings.Contains(view, column) {
			t.Fatal("missing column", column, view)
		}
	}
	left, _ := m.splitWidths()
	var fleetPane strings.Builder
	for _, line := range strings.Split(view, "\n")[:m.height-2] {
		fleetPane.WriteString(textansi.Cut(line, 0, left+2))
	}
	if strings.Count(view, "FLEET [focus]") != 1 || strings.Contains(view, "0H/0T") || strings.Contains(fleetPane.String(), identity(m.selected)) {
		t.Fatal("redundant headings/counts/raw row identity remain", view)
	}
}

func runLedgerDiagnostic(t *testing.T, m fleetModel) fleetModel {
	t.Helper()
	m.ledgerDiagnostics.next = time.Time{}
	cmd := m.refreshLedgerDiagnostics()
	if cmd == nil {
		t.Fatal("no ledger diagnostic read")
	}
	out, _ := m.Update(cmd())
	return out.(fleetModel)
}

func TestLedgerDiagnosticsWithoutDetailsAndQuietRefresh(t *testing.T) {
	m, source := boundFleetFixture()
	m.width, m.height = 120, 40
	e := m.hosts[1].entries[0]
	v := source.reports[e.Diagnostics.AgentID]
	v.Report.Work = c.DiagnosticWork{ObservedAt: time.Now(), TaskURL: "https://github.com/sarcasticbird/coop/issues/132", Stage: "implement"}
	v.Report.Wait.Dependencies = []string{"https://github.com/sarcasticbird/coop/issues/131"}
	source.reports[e.Diagnostics.AgentID] = v
	m = runLedgerDiagnostic(t, m)
	activity, work := m.ledgerSummary(e)
	if m.detail || activity != "waiting" || work != "ticket title unknown (coop #132) / implement / dependency coop #131" || !strings.Contains(m.View(), "coop #132") {
		t.Fatal("list did not consume exact evidence without drill-in", activity, work, m.View())
	}
	before, selected, scroll := m.View(), m.selected, m.scroll
	m.ledgerDiagnostics.next = time.Time{}
	cmd := m.refreshLedgerDiagnostics()
	if cmd == nil || m.View() != before || m.refreshLedgerDiagnostics() != nil {
		t.Fatal("cached read flashes loading or overlaps")
	}
	out, _ := m.Update(cmd())
	m = out.(fleetModel)
	if m.View() != before || m.selected != selected || m.scroll != scroll || len(m.services.Actions.(*fixtureActions).requests) > 0 {
		t.Fatal("quiet read changed focus/content or dispatched an action")
	}
	// Duplicate display labels cannot lend their report to the other row.
	if activity, _ := m.ledgerSummary(m.hosts[0].entries[0]); activity != "unknown" {
		t.Fatal("name-based join")
	}
}

func TestLedgerDiagnosticSafety(t *testing.T) {
	for _, change := range []string{"denied", "team-denied", "identity-denied", "unsupported", "missing", "moved", "replaced", "epoch", "old", "old-work", "skew", "skew-work", "missing-wait-time", "context", "out-of-order", "inventory-stale"} {
		t.Run(change, func(t *testing.T) {
			m, source := boundFleetFixture()
			e := m.hosts[1].entries[0]
			m = runLedgerDiagnostic(t, m)
			v := source.reports[e.Diagnostics.AgentID]
			switch change {
			case "denied":
				source.err = c.ErrForbidden
			case "team-denied":
				source.teamErr = c.ErrForbidden
			case "identity-denied":
				source.selfErr = c.ErrForbidden
			case "unsupported":
				source.err = c.ErrUnsupported
			case "missing":
				delete(source.reports, e.Diagnostics.AgentID)
			case "moved":
				source.rows[0].HostID = "moved-host"
			case "replaced":
				source.rows[0].InstanceID = "replacement"
			case "epoch":
				source.self.RestoreEpoch++
			case "old":
				v.Report.ObservedAt = time.Now().Add(-time.Minute)
			case "old-work":
				v.Report.Work.ObservedAt = time.Now().Add(-time.Minute)
			case "skew":
				v.Report.Wait.ObservedAt = time.Now().Add(time.Minute)
			case "skew-work":
				v.Report.Work.ObservedAt = time.Now().Add(time.Minute)
			case "missing-wait-time":
				v.Report.Wait.ObservedAt = time.Time{}
			case "context":
				m.coordination.self = source.self
				m.coordination.self.Principal.ID = "replacement-reader"
				m.coordination.seen = time.Now().Add(time.Second)
			case "out-of-order":
				v.ReceivedAt = v.ReceivedAt.Add(-time.Second)
			case "inventory-stale":
				m.hosts[1].updated = time.Now().Add(-time.Minute)
			}
			if change != "missing" {
				source.reports[e.Diagnostics.AgentID] = v
			}
			if change != "inventory-stale" {
				m = runLedgerDiagnostic(t, m)
			}
			activity, work := m.ledgerSummary(e)
			if change == "old-work" || change == "skew-work" {
				if activity != "waiting" || !strings.Contains(work, "work ") {
					t.Fatal("fresh activity erased or stale work treated as fresh", activity, work)
				}
			} else if activity != "unknown" && activity != "stale" {
				t.Fatal("incompatible evidence shown as current", activity, work)
			}
			if work == "" {
				t.Fatal("missing honest reason")
			}
		})
	}
}

func TestLedgerDiagnosticLateResultsAndRegistrationChanges(t *testing.T) {
	for _, change := range []string{"removed", "binding", "registration", "ambiguous"} {
		t.Run(change, func(t *testing.T) {
			m, _ := boundFleetFixture()
			cmd := m.refreshLedgerDiagnostics()
			late := cmd()
			e := m.hosts[1].entries[0]
			switch change {
			case "removed":
				m.hosts[1].entries = nil
			case "binding":
				binding := *e.Diagnostics
				binding.Generation++
				m.hosts[1].entries[0].Diagnostics = &binding
			case "registration":
				m.hosts[1].host.Address = "replacement-address"
				m.hosts[1].entries[0].Target.Host = m.hosts[1].host
			case "ambiguous":
				copy := e
				copy.Target.Host = hostrouter.Local
				m.hosts[0].entries = append(m.hosts[0].entries, copy)
			}
			m.reconcile()
			out, _ := m.Update(late)
			m = out.(fleetModel)
			if cached := m.ledgerDiagnostics.entries[e.Target]; cached.report != nil {
				t.Fatal("late report revived invalid cache")
			}
			if m.detail || len(m.services.Actions.(*fixtureActions).requests) != 0 {
				t.Fatal("read changed interaction authority")
			}
		})
	}
}

func TestLedgerDiagnosticStartupAndBindingCompatibility(t *testing.T) {
	m, _ := boundFleetFixture()
	m.refreshing = true
	out, cmd := m.Update(hostLoaded{epoch: m.epoch, done: true})
	m = out.(fleetModel)
	if m.refreshing || !m.ledgerDiagnostics.busy || cmd == nil || m.detail {
		t.Fatal("initial inventory did not schedule ledger evidence without drill-in")
	}
	out, _ = m.Update(cmd())
	m = out.(fleetModel)
	if activity, _ := m.ledgerSummary(m.hosts[1].entries[0]); activity != "waiting" {
		t.Fatal("startup evidence unavailable")
	}
	for _, change := range []string{"master", "invalid", "unbound", "ambiguous", "no-master"} {
		t.Run(change, func(t *testing.T) {
			m, source := boundFleetFixture()
			e := &m.hosts[1].entries[0]
			switch change {
			case "master":
				e.Diagnostics.MasterID = "other-master"
			case "invalid":
				e.Diagnostics.Generation = 0
			case "unbound":
				e.Diagnostics = nil
			case "no-master":
				m.services.Coordination = nil
			case "ambiguous":
				copy := *e
				copy.Target.Host = hostrouter.Local
				m.hosts[0].entries = append(m.hosts[0].entries, copy)
			}
			if m.refreshLedgerDiagnostics() != nil || source.reads != 0 {
				t.Fatal("incompatible binding triggered diagnostic reads")
			}
			if activity, _ := m.ledgerSummary(*e); activity != "unknown" {
				t.Fatal("incompatible binding shown as work")
			}
		})
	}
}
