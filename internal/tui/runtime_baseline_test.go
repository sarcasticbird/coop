package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/tui/tuifixture"
)

func runtimeBaselineFleet() fleetModel {
	f := tuifixture.New(time.Now())
	m := fixtureFleet()
	m.hosts = nil
	r := &fixtureRegistry{}
	for _, h := range f.Hosts {
		s := hostState{host: h.Host, entries: h.Entries, errorCode: h.Error}
		if h.Error == "" {
			s.updated = f.At
		}
		m.hosts = append(m.hosts, s)
		if h.Host != hostrouter.Local {
			r.hosts = append(r.hosts, h.Host)
		}
	}
	m.services.Registry = r
	m.selected = m.hosts[0].entries[0].Target
	return m
}

func TestRuntimeBaselineSizesAndQuietRefresh(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m := runtimeBaselineFleet()
			out, _ := m.Update(tea.WindowSizeMsg{Width: size.Width, Height: size.Height})
			m = out.(fleetModel)
			selected := m.selected
			seenEmpty, seenOffline := false, false
			for range 12 {
				before := m.View()
				if maxLineWidth(before) > size.Width || len(strings.Split(before, "\n")) > size.Height {
					t.Fatal("runtime viewport exceeds terminal bounds")
				}
				seenEmpty = seenEmpty || strings.Contains(before, "ok; snapshot") && strings.Contains(before, "fixture-empty")
				seenOffline = seenOffline || strings.Contains(before, "unreachable") && strings.Contains(before, "fixture-offline")
				for i := range m.hosts {
					m.hosts[i].loading = true
				}
				if got := m.View(); got != before {
					t.Fatalf("cached read changed the visible snapshot\nbefore:\n%s\nafter:\n%s", before, got)
				}
				m, _ = fleetKey(m, "pgdown")
			}
			if !seenEmpty || !seenOffline || m.selected != selected {
				t.Fatalf("paging lost empty/offline host or retargeted: empty=%v offline=%v selected=%+v", seenEmpty, seenOffline, m.selected)
			}
			if len(m.services.Actions.(*fixtureActions).requests) != 0 {
				t.Fatal("read/resize/paging dispatched a mutation")
			}
		})
	}
}

func TestRuntimeBaselineAuthorityAtEverySize(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, change := range []string{"registration", "unreachable", "removed"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.Width, size.Height, change), func(t *testing.T) {
				m := runtimeBaselineFleet()
				m.width, m.height = size.Width, size.Height
				m.selected = m.hosts[1].entries[0].Target
				m, _ = fleetKey(m, "tab")
				m, _ = fleetKey(m, "d")
				if m.pending == nil {
					t.Fatal("baseline confirmation did not capture the target")
				}
				switch change {
				case "registration":
					m.services.Registry.(*fixtureRegistry).hosts[0] = tuifixture.New(time.Now()).ChangedRegistration
				case "unreachable":
					m.hosts[1].errorCode = "unreachable"
				case "removed":
					m.hosts[1].entries = nil
				}
				m, cmd := fleetKey(m, "y")
				if cmd != nil || len(m.services.Actions.(*fixtureActions).requests) != 0 || m.status == "" {
					t.Fatal("stale captured action dispatched or refusal had no feedback")
				}
			})
		}
	}
}
