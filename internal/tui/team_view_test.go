package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/tui/tuifixture"
)

type teamViewSource struct {
	*coordinationMock
	rows    []c.Presence
	reports map[string]c.AgentDiagnostics
	err     error
	teamErr error
	reads   int
}

func (s *teamViewSource) Team(context.Context) ([]c.Presence, error) {
	return slices.Clone(s.rows), s.teamErr
}

func (s *teamViewSource) AgentDiagnostics(_ context.Context, id string) (c.AgentDiagnostics, error) {
	s.reads++
	if s.err != nil {
		return c.AgentDiagnostics{}, s.err
	}
	v, ok := s.reports[id]
	if !ok {
		return v, c.ErrNotFound
	}
	return v, nil
}

func teamViewFixture() (fleetModel, *teamViewSource) {
	m, base := coordinationFixture()
	f := tuifixture.New(time.Now())
	s := &teamViewSource{coordinationMock: base, rows: f.Presence, reports: f.Diagnostics}
	m.services.Coordination = s
	m.coordination.section = "team"
	m.coordination.team = slices.Clone(s.rows)
	m.width, m.height = 80, 24
	return m, s
}

func TestTeamFullPageDetailSizesAndReturn(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m, s := teamViewFixture()
			m.width, m.height = size.Width, size.Height
			m.revealTeamSelection()
			scroll := m.scroll
			placement := m.coordination.team[0].Placement
			m, cmd := coordinationKey(m, "enter")
			if cmd == nil || !strings.Contains(m.View(), "Loading read") {
				t.Fatal("missing visible navigation read", m.View())
			}
			m = coordinationRun(t, m, cmd)
			if m.coordination.teamReport == nil || m.coordination.teamReport.Report.Placement != placement || s.reads != 1 {
				t.Fatal("detail did not use authoritative placement", m.coordination.teamFailure)
			}
			all := strings.Join(m.coordLines(), "\n")
			for _, want := range []string{"Team > Agent", "Handling: waiting", "Work observation age:", "Wait observation age:", "Report age:", "receipt age:", "not current health"} {
				if !strings.Contains(all, want) {
					t.Fatalf("missing %q: %s", want, all)
				}
			}
			for _, resized := range tuifixture.Sizes() {
				out, _ := m.Update(tea.WindowSizeMsg{Width: resized.Width, Height: resized.Height})
				m = out.(fleetModel)
				if maxLineWidth(m.View()) > resized.Width || len(strings.Split(m.View(), "\n")) > resized.Height || !strings.HasPrefix(m.View(), "Team > Agent") {
					t.Fatal("detail lost full-page context or bounds", m.View())
				}
			}
			m, _ = coordinationKey(m, "esc")
			if m.coordination.teamDetail != nil || m.coordination.cursor != 0 || m.scroll != scroll || m.workspace != 1 {
				t.Fatal("detail return lost list context")
			}
		})
	}
}

func TestTeamReportFaultsAndPrivacy(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, test := range []struct {
			err  error
			want string
		}{{c.ErrForbidden, "denied"}, {c.ErrUnsupported, "unsupported"}, {c.ErrNotFound, "no visible"}, {errors.New("private-body secret-token"), "unavailable"}} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.Width, size.Height, test.want), func(t *testing.T) {
				m, s := teamViewFixture()
				m.width, m.height = size.Width, size.Height
				m, cmd := coordinationKey(m, "enter")
				m = coordinationRun(t, m, cmd)
				cached := m.coordination.teamReport
				s.err = test.err
				m, cmd = coordinationKey(m, "r")
				m = coordinationRun(t, m, cmd)
				all := strings.Join(m.coordLines(), "\n")
				if m.coordination.teamReport != cached || !strings.Contains(all, test.want) || !strings.Contains(all, "STALE retained") || strings.Contains(all, "private-body") || strings.Contains(all, "secret-token") {
					t.Fatal("fault lost cache/context or leaked response", all)
				}
			})
		}
	}
}

func TestTeamPlacementAndLateResults(t *testing.T) {
	for _, changed := range []string{"agent", "host", "instance", "generation", "missing"} {
		t.Run(changed, func(t *testing.T) {
			m, s := teamViewFixture()
			m, cmd := coordinationKey(m, "enter")
			m = coordinationRun(t, m, cmd)
			p := m.coordination.teamDetail
			v := s.reports[p.AgentID]
			switch changed {
			case "agent":
				v.Report.AgentID = "other"
			case "host":
				v.Report.HostID = "elsewhere"
			case "instance":
				v.Report.InstanceID = "replacement"
			case "generation":
				v.Report.Generation++
			case "missing":
				s.rows = nil
			}
			s.reports[p.AgentID] = v
			m, cmd = coordinationKey(m, "r")
			m = coordinationRun(t, m, cmd)
			if m.coordination.teamReport != nil || !strings.Contains(m.coordination.teamFailure, "unknown") {
				t.Fatal("mismatched report attached", m.coordination.teamFailure)
			}
		})
	}
	m, _ := teamViewFixture()
	m, cmd := coordinationKey(m, "enter")
	late := cmd()
	m, _ = coordinationKey(m, "esc")
	m = coordinationRun(t, m, func() tea.Msg { return late })
	if m.coordination.teamDetail != nil || m.coordination.teamReport != nil || m.coordination.busy {
		t.Fatal("late response reopened a returned detail")
	}
}

func TestTeamStableSelectionReorderResizeRemove(t *testing.T) {
	m, s := teamViewFixture()
	for i := range 25 {
		p := s.rows[0]
		p.AgentID = fmt.Sprintf("bird-%02d", i)
		p.Alias = "duplicate name"
		m.coordination.team = append(m.coordination.team, p)
	}
	m.coordination.cursor = 20
	id := m.coordination.team[20].AgentID
	m.revealTeamSelection()
	for _, size := range tuifixture.Sizes() {
		out, _ := m.Update(tea.WindowSizeMsg{Width: size.Width, Height: size.Height})
		m = out.(fleetModel)
		if !strings.Contains(m.View(), "> "+id) {
			t.Fatal("resize hid selected agent", m.View())
		}
	}
	rows := slices.Clone(m.coordination.team)
	slices.Reverse(rows)
	m.setTeamRows(rows)
	if m.coordination.team[m.coordination.cursor].AgentID != id || !strings.Contains(m.View(), "> "+id) {
		t.Fatal("refresh retargeted selection or hid it", m.View())
	}
	rows = slices.DeleteFunc(slices.Clone(rows), func(p c.Presence) bool { return p.AgentID == id })
	m.setTeamRows(rows)
	m.setTeamRows(rows)
	if m.coordination.cursor != -1 {
		t.Fatal("removed identity silently retargeted")
	}
}

func TestTeamReportAgesAndOutOfOrder(t *testing.T) {
	m, s := teamViewFixture()
	m, cmd := coordinationKey(m, "enter")
	m = coordinationRun(t, m, cmd)
	id := m.coordination.teamDetail.AgentID
	cached := m.coordination.teamReport
	v := s.reports[id]
	v.Report.ObservedAt = v.Report.ObservedAt.Add(-time.Minute)
	s.reports[id] = v
	m, cmd = coordinationKey(m, "r")
	m = coordinationRun(t, m, cmd)
	if m.coordination.teamReport != cached || !strings.Contains(m.coordination.teamFailure, "out-of-order") {
		t.Fatal("older observation replaced cache")
	}
	for _, skew := range []bool{false, true} {
		m, s = teamViewFixture()
		v = s.reports[s.rows[0].AgentID]
		if skew {
			v.ReceivedAt = time.Now().Add(time.Hour)
		} else {
			v.Report.ObservedAt = time.Now().Add(-time.Hour)
		}
		s.reports[s.rows[0].AgentID] = v
		m, cmd = coordinationKey(m, "enter")
		m = coordinationRun(t, m, cmd)
		all := strings.Join(m.coordLines(), "\n")
		want := "Report age: STALE"
		if skew {
			want = "unknown freshness"
		}
		if !strings.Contains(all, want) || skew && strings.Contains(all, "fresh receipt") {
			t.Fatal("bad freshness", all)
		}
	}
}

func TestTeamCachedRefreshQuietAndInitialErrors(t *testing.T) {
	m, s := teamViewFixture()
	m, cmd := coordinationKey(m, "enter")
	m = coordinationRun(t, m, cmd)
	before := m.View()
	m, cmd = coordinationKey(m, "r")
	if cmd == nil || m.View() != before {
		t.Fatal("cached read shifted full-page detail")
	}
	m = coordinationRun(t, m, cmd)
	s.teamErr = c.ErrForbidden
	m, cmd = coordinationKey(m, "r")
	m = coordinationRun(t, m, cmd)
	if m.coordination.teamReport == nil || !strings.Contains(m.View(), "STALE/OFFLINE") {
		t.Fatal("Team read failure hid retained report/error", m.View())
	}
	for _, err := range []error{c.ErrForbidden, c.ErrNotFound, c.ErrUnsupported} {
		m, s = teamViewFixture()
		s.err = err
		m, cmd = coordinationKey(m, "enter")
		m = coordinationRun(t, m, cmd)
		if m.coordination.teamReport != nil || !strings.Contains(strings.Join(m.coordLines(), "\n"), "work unknown") {
			t.Fatal("absent report became healthy")
		}
	}
}

func TestTeamAuthenticatedEpochAndReadOnlyDetail(t *testing.T) {
	m, s := teamViewFixture()
	s.self.RestoreEpoch = 2
	m, cmd := coordinationKey(m, "enter")
	m = coordinationRun(t, m, cmd)
	if m.coordination.teamReport != nil || !strings.Contains(m.coordination.teamFailure, "mismatch") {
		t.Fatal("report from another master epoch accepted")
	}
	for _, key := range []string{"n", "c", "p", "m", "o", "a", "d", "s"} {
		m, cmd = coordinationKey(m, key)
		if cmd != nil || m.coordination.form != nil || !strings.Contains(m.coordination.status, "Read only") {
			t.Fatal("detail control performed a mutation or failed silently", key)
		}
	}
}

func TestTeamScrolledErrorsRemainVisible(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, failedTeam := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d/team-%t", size.Width, size.Height, failedTeam), func(t *testing.T) {
				m, s := teamViewFixture()
				m.width, m.height = size.Width, size.Height
				m, cmd := coordinationKey(m, "enter")
				m = coordinationRun(t, m, cmd)
				m.scroll = 25
				if failedTeam {
					s.teamErr = c.ErrForbidden
				} else {
					s.err = c.ErrForbidden
				}
				m, cmd = coordinationKey(m, "r")
				m = coordinationRun(t, m, cmd)
				view := m.View()
				if m.scroll != 25 || !strings.Contains(view, "STALE") || !strings.Contains(view, "denied") || maxLineWidth(view) > size.Width || len(strings.Split(view, "\n")) > size.Height {
					t.Fatal("scrolled error lost feedback or viewport", view)
				}
			})
		}
	}
}

func TestTeamDetailAndHelpReturnAfterResize(t *testing.T) {
	for _, route := range []string{"detail", "help"} {
		for _, manual := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/manual-%t", route, manual), func(t *testing.T) {
				m, s := teamViewFixture()
				m.width, m.height = 120, 40
				for i := range 24 {
					p := s.rows[0]
					p.AgentID = fmt.Sprintf("long-bird-%02d", i)
					m.coordination.team = append(m.coordination.team, p)
				}
				m.coordination.cursor = 20
				id := m.coordination.team[20].AgentID
				m.revealTeamSelection()
				if manual {
					m.scroll = 0
				}
				saved := m.scroll
				key := "enter"
				if route == "help" {
					key = "?"
				}
				m, _ = coordinationKey(m, key)
				out, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
				m = out.(fleetModel)
				m, _ = coordinationKey(m, "esc")
				if m.coordination.team[m.coordination.cursor].AgentID != id {
					t.Fatal("return changed identity")
				}
				if manual && m.scroll != saved || !manual && !strings.Contains(m.View(), "> "+id) {
					t.Fatal("return lost intentional paging or visible focus", m.View())
				}
			})
		}
	}
}
