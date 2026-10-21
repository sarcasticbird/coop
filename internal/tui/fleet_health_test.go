package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/hostrouter"
)

func TestFleetLongReferenceKeepsFailureAndStaleAgeReadable(t *testing.T) {
	for _, code := range []string{"unreachable", "unauthorized", "invalid_inventory"} {
		for _, width := range []int{40, 120} {
			t.Run(fmt.Sprintf("%s/%d", code, width), func(t *testing.T) {
				m := fixtureFleet()
				m.width, m.height = width, 20
				h := hostrouter.Host{Name: "remote", Address: "operator@fleet-fixture.example.invalid"}
				e := fixtureRow(h, hostrouter.Agent, "same")
				m.hosts[1] = hostState{host: h, entries: []hostrouter.InventoryEntry{e}, updated: time.Now().Add(-3 * time.Minute)}
				m.services.Registry.(*fixtureRegistry).hosts = []hostrouter.Host{h}
				m.selected = e.Target
				out, _ := m.Update(hostLoaded{epoch: m.epoch, inventory: hostrouter.HostInventory{Host: h, Error: code}})
				m = out.(fleetModel)
				var views strings.Builder
				for range 50 {
					view := m.View()
					if maxLineWidth(view) > width || len(strings.Split(view, "\n")) > m.height {
						t.Fatal("health view exceeded terminal bounds")
					}
					views.WriteString(view)
					m, _ = fleetKey(m, "pgdown")
				}
				visible := strings.Join(strings.Fields(views.String()), " ")
				if !strings.Contains(visible, code) || !strings.Contains(visible, "stale snapshot 3m") || !strings.Contains(visible, "[stale]") {
					t.Fatalf("failure or stale state/age was hidden; last viewport: %s", m.View())
				}
				m, _ = fleetKey(m, "tab")
				views.Reset()
				for range 50 {
					view := m.View()
					if maxLineWidth(view) > width || len(strings.Split(view, "\n")) > m.height {
						t.Fatal("detail view exceeded terminal bounds")
					}
					views.WriteString(view)
					views.WriteString("\n")
					previousScroll := m.scroll
					m, _ = fleetKey(m, "pgdown")
					if m.scroll == previousScroll {
						break
					}
				}
				// Details can hard-wrap inside health words at narrow widths.
				detail := strings.Join(strings.Fields(views.String()), "")
				if !strings.Contains(detail, "Hosthealth:"+code) || !strings.Contains(detail, "stalesnapshot3m") {
					t.Fatalf("failed-host detail hid health/stale age: %s", detail)
				}
				if len(m.hosts[1].entries) != 1 || m.hosts[1].entries[0].State != "running" {
					t.Fatal("failure erased or misrepresented retained inventory")
				}
				m, cmd := fleetKey(m, "s")
				if cmd != nil || len(m.services.Actions.(*fixtureActions).requests) != 0 {
					t.Fatal("failed host allowed a mutation")
				}
				out, _ = m.Update(hostLoaded{epoch: m.epoch, inventory: hostrouter.HostInventory{Host: h, Entries: []hostrouter.InventoryEntry{e}}})
				m = out.(fleetModel)
				m.scroll = 0
				if strings.Contains(m.View(), code) || strings.Contains(m.View(), "[stale]") || m.selected != e.Target {
					t.Fatal("reconnect retained stale health or redirected selection")
				}
				m, cmd = fleetKey(m, "s")
				if cmd != nil || m.pending == nil {
					t.Fatal("reconnected target bypassed confirmation")
				}
				m = reviewFleetConfirmation(t, m)
				m, cmd = fleetKey(m, "y")
				if cmd == nil {
					t.Fatal("reconnected target action was unavailable")
				}
				if done := cmd().(fleetActionDone); done.err != nil {
					t.Fatal(done.err)
				}
				requests := m.services.Actions.(*fixtureActions).requests
				if len(requests) != 1 || requests[0].Target != e.Target {
					t.Fatal("reconnected action lost captured identity")
				}
			})
		}
	}
}
