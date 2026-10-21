package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	textansi "github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/tui/tuifixture"
)

func TestTeamParityLedgerSizes(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m, source := teamViewFixture()
			m.width, m.height = size.Width, size.Height
			m.coordination.teamSeen = time.Now()
			m.coordination.team = nil
			for i := range 30 {
				p := source.rows[0]
				p.AgentID = fmt.Sprintf("exact-agent-%02d", i)
				p.Alias = fmt.Sprintf("\u754ce\u0301-%02d", i)
				p.HostID = "\u4e3b\u673a"
				m.coordination.team = append(m.coordination.team, p)
			}
			m.coordination.cursor = 20
			if size.Width >= 100 {
				all := strings.Join(m.teamLines("test-master"), "\n")
				for _, column := range []string{"AGENT", "HOST", "PRESENCE", "HANDLING", "WORK / BLOCKER"} {
					if !strings.Contains(all, column) {
						t.Fatal("missing wide column", column)
					}
				}
			}
			m.revealTeamSelection()
			for _, resized := range tuifixture.Sizes() {
				out, _ := m.Update(tea.WindowSizeMsg{Width: resized.Width, Height: resized.Height})
				m = out.(fleetModel)
				view := m.View()
				if !strings.Contains(view, "> \u754ce\u0301-20") || maxLineWidth(view) > resized.Width || len(strings.Split(view, "\n")) > resized.Height {
					t.Fatal("selected alias lost or viewport overflow", view)
				}
			}
			all := strings.Join(m.teamLines(strings.Repeat("\u754c", 80)), "\n")
			if strings.Contains(all, "exact-agent-") || strings.Contains(all, "Enter inspect") || !strings.Contains(all, "unknown") || source.reads != 0 {
				t.Fatal("list leaked raw identity, duplicate controls, or inferred work", all)
			}
			if textansi.StringWidth(m.teamLines(strings.Repeat("\u754c", 80))[0]) > m.width {
				t.Fatal("pinned context wraps")
			}
		})
	}
	for _, label := range []string{"Hawk", "\u754c\u754ce\u0301", strings.Repeat("long", 30), "bad\nlabel\x1b\x07\t"} {
		row := teamLedgerRow(120, "> ", label, "HOST", "online", "unknown", "unknown")
		host := strings.Index(row, "HOST")
		if textansi.StringWidth(row) != 120 || host < 0 || textansi.StringWidth(row[:host]) != 22 || strings.ContainsAny(row, "\n\x1b\x07\t") {
			t.Fatal("wide cells are not aligned", row)
		}
	}
}

func TestTeamParityWorkFirstAndCapturedPlacement(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m, _ := teamViewFixture()
			m.width, m.height = size.Width, size.Height
			m.coordination.team[0].Alias = strings.Repeat("\u754ce\u0301", 80)
			m, cmd := coordinationKey(m, "enter")
			m = coordinationRun(t, m, cmd)
			activity, work := diagnosticSummary(time.Now(), m.coordination.teamReport, "")
			view := m.View()
			if !strings.Contains(view, "Handling: "+activity) || !strings.Contains(strings.ReplaceAll(view, "\n", ""), "Work / blocker: "+work) {
				t.Fatal("actual work is not visible before raw diagnostics", view)
			}
			all := strings.Join(m.teamLines("test-master"), "\n")
			for _, want := range []string{"Principal:", "Source:", "Team snapshot age:", "AgentID: " + m.coordination.teamDetail.AgentID, "Worker: " + m.coordination.teamDetail.InstanceID, fmt.Sprintf("Generation: %d", m.coordination.teamDetail.Generation)} {
				if !strings.Contains(all, want) {
					t.Fatal("missing captured context", want, all)
				}
			}
			if strings.Index(all, "Work / blocker:") > strings.Index(all, "Work observation age:") {
				t.Fatal("raw diagnostics precede summary")
			}
			m.coordination.teamReport = nil
			all = strings.Join(m.teamLines("test-master"), "\n")
			if !strings.Contains(all, "Worker: "+m.coordination.teamDetail.InstanceID) || !strings.Contains(all, "Handling: unknown") {
				t.Fatal("missing report lost captured placement or claimed activity", all)
			}
		})
	}
}

func TestTeamParityReturnResizeGeometry(t *testing.T) {
	for _, route := range []string{"enter", "?"} {
		for _, manual := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/manual-%t", route, manual), func(t *testing.T) {
				m, source := teamViewFixture()
				m.width, m.height = 120, 40
				for i := range 60 {
					p := source.rows[0]
					p.AgentID = fmt.Sprintf("exact-%02d", i)
					p.Alias = fmt.Sprintf("label-%02d", i)
					m.coordination.team = append(m.coordination.team, p)
				}
				m.coordination.cursor = 50
				selected := m.coordination.team[50]
				m.revealTeamSelection()
				if manual {
					m.scroll = 0
				}
				saved := m.scroll
				m, _ = coordinationKey(m, route)
				out, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
				m = out.(fleetModel)
				m, _ = coordinationKey(m, "esc")
				if m.coordination.team[m.coordination.cursor].AgentID != selected.AgentID || manual && m.scroll != saved || !manual && !strings.Contains(m.View(), "> "+selected.Alias) {
					t.Fatal("return changed selection, manual paging, or visibility", m.View())
				}
			})
		}
	}
}

func TestTeamParityFailureSummary(t *testing.T) {
	for _, scenario := range []string{"denied", "stale", "missing", "clock", "team-failed"} {
		t.Run(scenario, func(t *testing.T) {
			m, _ := teamViewFixture()
			m.width, m.height = 40, 20
			m, cmd := coordinationKey(m, "enter")
			m = coordinationRun(t, m, cmd)
			want := "Handling: unknown"
			switch scenario {
			case "denied":
				m.coordination.teamFailure = diagnosticFailure(c.ErrForbidden)
			case "stale":
				m.coordination.teamReport.Stale = true
				want = "Handling: stale"
			case "missing":
				m.coordination.teamReport = nil
			case "clock":
				m.coordination.teamReport.ReceivedAt = time.Now().Add(time.Hour)
			case "team-failed":
				m.coordination.failed = true
			}
			view := m.View()
			if !strings.Contains(view, want) || strings.Contains(view, "Handling: waiting") || !strings.Contains(view, "Work / blocker:") {
				t.Fatal("failure presented cached work as current", view)
			}
		})
	}
}
