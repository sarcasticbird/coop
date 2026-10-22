package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/tui/tuifixture"
)

func boundFleetFixture() (fleetModel, *teamViewSource) {
	m := fixtureFleet()
	_, source := teamViewFixture()
	source.self.RestoreEpoch = 1
	m.services.Coordination = source
	entry := &m.hosts[1].entries[0]
	entry.Diagnostics = &c.DiagnosticBinding{MasterID: source.MasterID(), Placement: source.rows[0].Placement}
	m.selected = entry.Target
	return m, source
}

func runFleetDiagnostic(t *testing.T, m fleetModel, cmd tea.Cmd) fleetModel {
	t.Helper()
	if cmd == nil {
		t.Fatal("no diagnostic read")
	}
	msg := cmd()
	if _, ok := msg.(fleetDiagnosticResult); !ok {
		t.Fatalf("not a read-only diagnostic result: %T", msg)
	}
	out, _ := m.Update(msg)
	return out.(fleetModel)
}

func TestFleetMappedWorkAtEverySize(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m, source := boundFleetFixture()
			m.width, m.height = size.Width, size.Height
			target := m.selected
			m, cmd := fleetKey(m, "enter")
			if !m.diagnostics.busy || !strings.Contains(m.View(), "Loading diagnostics") {
				t.Fatal("initial read not visible", m.View())
			}
			m = runFleetDiagnostic(t, m, cmd)
			if m.diagnostics.report == nil || m.diagnostics.report.Report.Placement != source.rows[0].Placement || m.detailEntry.Target != target {
				t.Fatal("selected target not bound to exact placement", m.diagnostics.failure)
			}
			lines, _, _ := m.content()
			all := strings.Join(lines, "")
			for _, want := range []string{"VM lifecycle: running", "Activity: waiting", "Source: mapped master diagnostics", "Work age:", "Receipt age:"} {
				if !strings.Contains(all, want) {
					t.Fatalf("missing %q: %s", want, all)
				}
			}
			before := m.View()
			cmd = m.refreshFleetDiagnostics()
			if m.View() != before {
				t.Fatal("cached diagnostic refresh shifted view")
			}
			m = runFleetDiagnostic(t, m, cmd)
			if len(m.services.Actions.(*fixtureActions).requests) != 0 || m.pending != nil || m.busy {
				t.Fatal("diagnostic read performed an action")
			}
			if maxLineWidth(m.View()) > size.Width || len(strings.Split(m.View(), "\n")) > size.Height {
				t.Fatal("view exceeded bounds")
			}
			m, _ = fleetKey(m, "esc")
			m.selected = m.hosts[0].entries[0].Target
			m, cmd = fleetKey(m, "enter")
			if cmd != nil || m.diagnostics.report != nil || !strings.Contains(strings.Join(m.fleetDetailsLines("running", "ok"), "\n"), "Task: unknown") {
				t.Fatal("unbound duplicate-name target borrowed report")
			}
		})
	}
}

func TestFleetBindingMismatchAndAmbiguityStayUnknown(t *testing.T) {
	for _, changed := range []string{"master", "agent", "host", "instance", "generation", "ambiguous", "epoch"} {
		t.Run(changed, func(t *testing.T) {
			m, source := boundFleetFixture()
			b := m.hosts[1].entries[0].Diagnostics
			switch changed {
			case "master":
				b.MasterID = "different-master"
			case "agent":
				b.AgentID = "different-agent"
			case "host":
				b.HostID = "different-host"
			case "instance":
				b.InstanceID = "replacement-worker"
			case "generation":
				b.Generation++
			case "epoch":
				source.self.RestoreEpoch++
			case "ambiguous":
				row := m.hosts[1].entries[0]
				row.Target.Host = hostrouter.Local
				m.hosts[0].entries = append(m.hosts[0].entries, row)
			}
			m, cmd := fleetKey(m, "enter")
			if cmd != nil {
				m = runFleetDiagnostic(t, m, cmd)
			}
			if m.diagnostics.report != nil || m.diagnostics.failure == "" {
				t.Fatal("wrong report attached", changed)
			}
			if (changed == "master" || changed == "ambiguous") && source.reads != 0 {
				t.Fatal("invalid binding used to read reports")
			}
		})
	}
}

func TestFleetChangedBindingRejectsPendingAndLateReads(t *testing.T) {
	for _, change := range []string{"placement", "registration", "removed"} {
		t.Run(change, func(t *testing.T) {
			m, _ := boundFleetFixture()
			m, cmd := fleetKey(m, "enter")
			late := cmd()
			m, _ = fleetKey(m, "d")
			switch change {
			case "placement":
				binding := *m.hosts[1].entries[0].Diagnostics
				binding.Generation++
				binding.InstanceID = "replacement-worker"
				m.hosts[1].entries[0].Diagnostics = &binding
			case "registration":
				m.hosts[1].host.Address = "changed-alias"
				m.hosts[1].entries[0].Target.Host = m.hosts[1].host
			case "removed":
				m.hosts[1].entries = nil
			}
			m.reconcile()
			out, _ := m.Update(late)
			m = out.(fleetModel)
			if m.diagnostics.report != nil || !m.detailInvalid || m.pending != nil {
				t.Fatal("changed target retained report or intent")
			}
		})
	}
	m, _ := boundFleetFixture()
	m, cmd := fleetKey(m, "enter")
	late := cmd()
	m, _ = fleetKey(m, "esc")
	out, _ := m.Update(late)
	m = out.(fleetModel)
	if m.diagnostics.report != nil || m.detail {
		t.Fatal("late report reopened detail")
	}
}

func TestFleetScrolledFaultsAndRestoreEpoch(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, failure := range []string{"diagnostics", "team", "inventory"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.Width, size.Height, failure), func(t *testing.T) {
				m, source := boundFleetFixture()
				m.width, m.height = size.Width, size.Height
				m, cmd := fleetKey(m, "enter")
				m = runFleetDiagnostic(t, m, cmd)
				cached := m.diagnostics.report
				m.scroll = 25
				switch failure {
				case "diagnostics":
					source.err = c.ErrForbidden
				case "team":
					source.teamErr = c.ErrForbidden
				case "inventory":
					m.hosts[1].errorCode = "ssh_unavailable"
					m.reconcile()
				}
				cmd = m.refreshFleetDiagnostics()
				if cmd != nil {
					m = runFleetDiagnostic(t, m, cmd)
				}
				view := m.View()
				if m.diagnostics.report != cached || m.scroll != 25 || !strings.Contains(view, "STALE") || failure != "inventory" && !strings.Contains(view, "denied") {
					t.Fatal("scrolled fault lost safe cached feedback", view)
				}
			})
		}
	}
	m, source := boundFleetFixture()
	m, cmd := fleetKey(m, "enter")
	m = runFleetDiagnostic(t, m, cmd)
	id := m.detailEntry.Diagnostics.AgentID
	v := source.reports[id]
	v.Report.RestoreEpoch++
	v.Report.ObservedAt = v.Report.ObservedAt.Add(-time.Minute)
	v.ReceivedAt = time.Now()
	source.reports[id], source.self.RestoreEpoch = v, v.Report.RestoreEpoch
	m = runFleetDiagnostic(t, m, m.refreshFleetDiagnostics())
	if m.diagnostics.report == nil || m.diagnostics.report.Report.RestoreEpoch != v.Report.RestoreEpoch {
		t.Fatal("old master epoch retained after clock correction")
	}
}

func TestFleetDiagnosticBindingThroughMasterHTTP(t *testing.T) {
	owner, worker := coordinationMasterFixture(t)
	identity, err := worker.Self(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	report := c.WorkerDiagnostics{Version: 1, Placement: identity.Placement, RestoreEpoch: identity.RestoreEpoch, StartedAt: now.Add(-time.Hour), ObservedAt: now, Work: c.DiagnosticWork{ObservedAt: now, Stage: "implement", Phase: "waiting"}, Wait: c.DiagnosticWait{ObservedAt: now, Handling: "waiting", Reason: "local_pause"}}
	if err := worker.ReportDiagnostics(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	m := fixtureFleet()
	m.services.Coordination = owner
	m.hosts[1].entries[0].Diagnostics = &c.DiagnosticBinding{MasterID: owner.MasterID(), Placement: identity.Placement}
	m.selected = m.hosts[1].entries[0].Target
	m, cmd := fleetKey(m, "enter")
	m = runFleetDiagnostic(t, m, cmd)
	if m.diagnostics.report == nil || m.diagnostics.report.Report.Placement != identity.Placement || m.diagnostics.report.Report.Wait.Reason != "local_pause" {
		t.Fatal("real master report not mapped to explicit target binding", m.diagnostics.failure)
	}
	if len(m.services.Actions.(*fixtureActions).requests) != 0 || m.busy || m.pending != nil {
		t.Fatal("read performed a lifecycle operation")
	}
}

func TestFleetWrappedIdentityConflictInvalidatesCache(t *testing.T) {
	for _, err := range []error{c.ErrConflict, c.ErrEpoch} {
		m, source := boundFleetFixture()
		m, cmd := fleetKey(m, "enter")
		m = runFleetDiagnostic(t, m, cmd)
		source.err = fmt.Errorf("private response sentinel: %w", err)
		cmd = m.refreshFleetDiagnostics()
		m = runFleetDiagnostic(t, m, cmd)
		if m.diagnostics.report != nil || strings.Contains(m.View(), "private response") {
			t.Fatal("identity conflict retained cache or leaked response")
		}
	}
}

func TestFleetDiagnosticFeedbackPreservesControlOutcomes(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, diagnostic := range []string{"denied", "stale", "loading"} {
			for _, action := range []string{"cancel", "refuse"} {
				t.Run(fmt.Sprintf("%dx%d/%s/%s", size.Width, size.Height, diagnostic, action), func(t *testing.T) {
					m, source := boundFleetFixture()
					m.width, m.height = size.Width, size.Height
					m, cmd := fleetKey(m, "enter")
					switch diagnostic {
					case "denied":
						m = runFleetDiagnostic(t, m, cmd)
						source.err = c.ErrForbidden
						cmd = m.refreshFleetDiagnostics()
						m = runFleetDiagnostic(t, m, cmd)
					case "stale":
						m = runFleetDiagnostic(t, m, cmd)
						m.diagnostics.report.Stale = true
					case "loading":
						t.Cleanup(m.diagnostics.cancel)
					}
					want := "action cancelled"
					if action == "cancel" {
						m, _ = fleetKey(m, "d")
						m, _ = fleetKey(m, "n")
					} else {
						m.services.Registry.(*fixtureRegistry).hosts = nil
						m, cmd = fleetKey(m, "s")
						if cmd != nil || m.pending == nil {
							t.Fatal("stop bypassed confirmation")
						}
						m = reviewFleetConfirmation(t, m)
						m, cmd = fleetKey(m, "y")
						if cmd != nil {
							t.Fatal("stale registration dispatched")
						}
						want = "stale target"
					}
					m.scroll = 25
					view := m.View()
					lines := strings.Split(view, "\n")
					if m.scroll != 25 || !strings.Contains(lines[len(lines)-2], want) {
						t.Fatal("diagnostics hid fixed control outcome or moved viewport", view)
					}
					truth := map[string]string{"denied": "denied", "stale": "STALE", "loading": "Loading diagnostics"}[diagnostic]
					if !strings.Contains(strings.Join(lines[len(lines)-2:], " "), truth) || !strings.Contains(lines[len(lines)-1], "Esc") || maxLineWidth(view) > size.Width || len(lines) > size.Height {
						t.Fatal("fixed diagnostic truth lost or bounds exceeded", view)
					}
				})
			}
		}
	}
}
