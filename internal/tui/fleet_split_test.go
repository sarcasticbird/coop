package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

func TestSplitFleetBreakpointAndExplicitConnect(t *testing.T) {
	for _, width := range []int{40, 80, 99, 100, 101, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := runtimeBaselineFleet()
			m.width, m.height = width, 40
			m.selected = hostrouter.Target{}
			m, cmd := fleetKey(m, "down")
			if cmd != nil || m.detail || m.pending != nil || m.busy {
				t.Fatal("selection did more than preview")
			}
			target := m.selected
			view := m.View()
			if m.splitFleet() != (width >= 100) {
				t.Fatal("wrong breakpoint")
			}
			if width >= 100 && (!strings.Contains(view, "FLEET [focus]") || !strings.Contains(view, "REPORTED WORK") || !strings.Contains(view, "DETAILS")) {
				t.Fatal("selection did not preview right pane", view)
			}
			m, _ = fleetKey(m, "tab")
			if !m.detail || m.detailEntry.Target != target || m.pending != nil {
				t.Fatal("Enter did not focus captured Details")
			}
			for _, nextWidth := range []int{40, 120, 99, 100, width} {
				out, _ := m.Update(tea.WindowSizeMsg{Width: nextWidth, Height: 24})
				m = out.(fleetModel)
				m, cmd = fleetKey(m, "enter")
				if cmd != nil || m.pending != nil || !m.detail || m.selected != target || m.detailEntry.Target != target {
					t.Fatal("resize changed Enter into Connect or substituted target")
				}
				if maxLineWidth(m.View()) > nextWidth || len(strings.Split(m.View(), "\n")) != 24 {
					t.Fatal("pane exceeds terminal bounds", m.View())
				}
			}
			m, _ = fleetKey(m, "c")
			if m.pending == nil || m.pending.operation != "enter" || m.pending.entry.Target != target || m.splitFleet() {
				t.Fatal("explicit Connect lost captured full-page confirmation")
			}
			m, _ = fleetKey(m, "esc")
			m, _ = fleetKey(m, "esc")
			if m.detail || m.selected != target || len(m.services.Actions.(*fixtureActions).requests) != 0 {
				t.Fatal("return lost selection or dispatched")
			}
		})
	}
}

func TestSplitPreviewReadOnlyAndAccessGuarded(t *testing.T) {
	m, source := boundFleetFixture()
	m.width, m.height = 120, 40
	for id, report := range source.reports {
		report.Report.Work = c.DiagnosticWork{ObservedAt: time.Now(), TaskURL: "https://github.com/example/fixture/issues/42", PRURL: "https://github.com/example/fixture/pull/43"}
		source.reports[id] = report
	}
	cmd := m.refreshLedgerDiagnostics()
	if cmd == nil {
		t.Fatal("no fixture ledger read")
	}
	out, _ := m.Update(cmd())
	m = out.(fleetModel)
	beforeReads, target := source.reads, m.selected
	for range 3 {
		_ = m.View()
	}
	if source.reads != beforeReads || m.selected != target || m.detail || m.pending != nil {
		t.Fatal("render performed work or changed focus")
	}
	if !strings.Contains(strings.Join(actualSplitRightInterior(t, m), ""), "fixture #42") {
		t.Fatal("compatible report not previewed", m.View())
	}
	m.coordination.failed = true
	m.coordination.checked = m.ledgerDiagnostics.checked.Add(time.Second)
	view := m.View()
	if strings.Contains(view, "fixture #42") || !strings.Contains(view, "Task: unknown") {
		t.Fatal("preview promoted old access context", view)
	}
}

func TestSplitRemovedTargetRemainsUnavailable(t *testing.T) {
	m := runtimeBaselineFleet()
	m.width, m.height = 120, 40
	m.selected = hostrouter.Target{}
	m, _ = fleetKey(m, "down")
	target := m.selected
	m.hosts[0].entries = nil
	m.reconcile()
	if m.selected != (hostrouter.Target{}) || !m.detailInvalid || m.detailEntry.Target != target || !strings.Contains(m.View(), "unavailable or changed") {
		t.Fatal("removed preview silently moved", m.View())
	}
	m, cmd := fleetKey(m, "enter")
	if cmd != nil || m.detail || m.pending != nil {
		t.Fatal("removed selection opened action intent")
	}
}

func TestSplitFocusKeepsIndependentScroll(t *testing.T) {
	m := fixtureFleet()
	m.width, m.height = 120, 24
	for i := range 20 {
		m.hosts = append(m.hosts, hostState{host: hostrouter.Host{Name: fmt.Sprintf("spare-%02d", i)}})
	}
	m, _ = fleetKey(m, "pgdown")
	leftScroll, target := m.scroll, m.selected
	m, _ = fleetKey(m, "tab")
	m, _ = fleetKey(m, "pgdown")
	rightScroll := m.scroll
	if m.ledgerScroll != leftScroll || m.selected != target {
		t.Fatal("Details paging moved Fleet")
	}
	m, _ = fleetKey(m, "tab")
	if m.detail || m.scroll != leftScroll {
		t.Fatal("Tab did not return retained Fleet")
	}
	m, _ = fleetKey(m, "tab")
	if !m.detail || m.scroll != rightScroll {
		t.Fatal("Tab did not resume Details scroll")
	}
}

func TestSplitDetailsKeyboardReachesExactContext(t *testing.T) {
	for _, size := range [][2]int{{100, 24}, {120, 24}, {120, 20}, {120, 40}} {
		for _, key := range []string{"pgdown", "down"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], key), func(t *testing.T) {
				m := runtimeBaselineFleet()
				coord, source := teamViewFixture()
				master := "master-" + strings.Repeat("shared-prefix-", 8) + "second"
				principal := "principal-" + strings.Repeat("shared-prefix-", 8) + "second"
				source.master = master
				coord.coordination.self.Principal.ID = principal
				m.services.Coordination, m.coordination = source, coord.coordination
				m.width, m.height = size[0], size[1]
				m, _ = fleetKey(m, "tab")
				target := m.selected
				seenMaster, seenPrincipal, seenEnd := false, false, false
				var pinned []string
				for step := 0; step <= 100; step++ {
					body := actualSplitRightInterior(t, m)
					if !strings.HasPrefix(body[0], "Master:") || !strings.HasPrefix(body[1], "Principal:") {
						t.Fatal("split context is not one pinned row per identity", body[:2])
					}
					if step == 0 {
						pinned = append([]string(nil), body[:2]...)
					}
					if body[0] != pinned[0] || body[1] != pinned[1] {
						t.Fatal("paging changed pinned context")
					}
					visible := strings.Join(body[2:], "")
					seenMaster = seenMaster || strings.Contains(visible, master)
					seenPrincipal = seenPrincipal || strings.Contains(visible, principal)
					seenEnd = seenEnd || strings.Contains(visible, "l then t: full Team diagnostics")
					if step < 100 {
						var cmd tea.Cmd
						m, cmd = fleetKey(m, key)
						if cmd != nil || m.pending != nil || m.selected != target || !m.detail {
							t.Fatal("paging changed intent or dispatched")
						}
					}
				}
				if !seenMaster || !seenPrincipal || !seenEnd {
					t.Fatalf("keyboard cannot reach full context/end: master=%t principal=%t end=%t scroll=%d", seenMaster, seenPrincipal, seenEnd, m.scroll)
				}
				lines, _, _ := m.focusedContent()
				if m.scroll != max(0, len(lines)-(m.height-4)) {
					t.Fatal("keyboard clamp differs from physical projection")
				}
			})
		}
	}
}
