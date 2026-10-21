package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/hostrouter"
)

func TestFleetEntrylessHostsRemainReachable(t *testing.T) {
	for _, state := range []string{"disconnected", "loading", "empty"} {
		for _, width := range []int{40, 100} {
			t.Run(fmt.Sprintf("%s/%d", state, width), func(t *testing.T) {
				m := fixtureFleet()
				m.width, m.height = width, 24
				m.hosts = m.hosts[:1]
				for i := range 20 {
					h := hostState{host: hostrouter.Host{Name: fmt.Sprintf("offline-%02d", i), Address: "ssh"}}
					switch state {
					case "disconnected":
						h.errorCode = "unreachable"
					case "loading":
						h.loading = true
					case "empty":
						h.updated = time.Now()
					}
					m.hosts = append(m.hosts, h)
				}
				selected := m.selected
				seen := make(map[string]bool)
				for range 100 {
					view := m.View()
					if len(strings.Split(view, "\n")) > m.height || maxLineWidth(view) > m.width {
						t.Fatal("viewport exceeded terminal bounds")
					}
					for _, h := range m.hosts {
						if strings.Contains(view, h.host.Name) {
							if h.host == hostrouter.Local {
								seen[h.host.Name] = true
							} else {
								marker := map[string]string{"disconnected": "unreachable", "loading": "loading", "empty": "ok; snapshot"}[state]
								lines := strings.Split(view, "\n")
								for i, line := range lines {
									if strings.Contains(line, h.host.Name) && i+1 < len(lines) && strings.Contains(lines[i+1], marker) {
										seen[h.host.Name] = true
									}
								}
							}
						}
					}
					m, _ = fleetKey(m, "down")
					m, _ = fleetKey(m, "pgdown")
				}
				for _, h := range m.hosts {
					if !seen[h.host.Name] {
						t.Errorf("entry-less host %s was unreachable", h.host.Name)
					}
				}
				if m.selected != selected {
					t.Fatal("paging redirected the target selection")
				}
				ledgerScroll := m.scroll
				m, _ = fleetKey(m, "tab")
				if !strings.Contains(m.View(), "Project: /project") {
					t.Fatal("details focus did not reveal the selected target's pane")
				}
				m, _ = fleetKey(m, "tab")
				if m.detail || m.scroll != ledgerScroll || m.selected != selected {
					t.Fatal("return from details did not retain ledger scroll and selection")
				}
				for range 100 {
					m, _ = fleetKey(m, "pgup")
				}
				if !strings.Contains(m.View(), "local") {
					t.Fatal("cannot scroll back to local")
				}

				// A previously entry-less host can reconnect and its target can
				// become selected without losing the viewport or host identity.
				h := m.hosts[len(m.hosts)-1].host
				e := fixtureRow(h, hostrouter.Agent, "back")
				out, _ := m.Update(hostLoaded{epoch: m.epoch, inventory: hostrouter.HostInventory{Host: h, Entries: []hostrouter.InventoryEntry{e}}})
				m = out.(fleetModel)
				m, _ = fleetKey(m, "down")
				if m.selected != e.Target || !m.ledgerSelectionVisible() || !strings.Contains(m.View(), "> ") {
					t.Fatal("reconnected target selection was not revealed")
				}
				if m.hosts[len(m.hosts)-1].errorCode != "" || m.hosts[len(m.hosts)-1].loading {
					t.Fatal("reconnect retained failure/loading state")
				}
				m, _ = fleetKey(m, "up")
				if m.selected != selected || !m.ledgerSelectionVisible() || !strings.Contains(m.View(), "> ") {
					t.Fatal("returning local selection was not revealed")
				}
			})
		}
	}
}

func TestServerSelectionFollowsViewportWithoutTargets(t *testing.T) {
	m := fixtureFleet()
	m.workspace, m.height = 2, 10
	m.hosts = m.hosts[:1]
	for i := range 20 {
		m.hosts = append(m.hosts, hostState{host: hostrouter.Host{Name: fmt.Sprintf("remote-%02d", i), Address: "ssh"}, errorCode: "unreachable"})
	}
	for range 20 {
		m, _ = fleetKey(m, "down")
	}
	if m.server != m.hosts[len(m.hosts)-1].host || !strings.Contains(m.View(), "> remote-19") {
		t.Fatal("last server selection was off-screen")
	}
	// Opening an overlay after paging must still show its start and confirmation.
	m, _ = fleetKey(m, "d")
	if !strings.Contains(m.View(), "REMOVE SERVER") || !strings.Contains(m.View(), "remote-19") {
		t.Fatal("server overlay inherited browser scroll")
	}
}
