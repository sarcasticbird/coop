package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/tui/tuifixture"
)

func TestLedgerDetailsConnectReturn(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m := runtimeBaselineFleet()
			m.selected = hostrouter.Target{}
			m.width, m.height = size.Width, size.Height
			if !strings.Contains(m.View(), "Filter: All") || !strings.Contains(m.View(), "No target selected") {
				t.Fatal("startup must be All without a target")
			}
			for _, key := range []string{"enter", "u", "s", "d", "x"} {
				var cmd tea.Cmd
				m, cmd = fleetKey(m, key)
				if cmd != nil || m.detail || m.pending != nil || m.status == "" {
					t.Fatalf("unselected control %s dispatched or lacked feedback", key)
				}
			}
			m, _ = fleetKey(m, "down")
			target := m.selected
			if target == (hostrouter.Target{}) || m.detail || m.pending != nil || (!m.splitFleet() && strings.Contains(m.View(), "Project:")) {
				t.Fatal("highlight must not open an inspector or authorize Connect")
			}
			m, _ = fleetKey(m, "pgdown")
			scroll := m.scroll
			m, cmd := fleetKey(m, "enter")
			if cmd != nil || !m.detail || m.detailEntry == nil || m.detailEntry.Target != target || m.selected != target {
				t.Fatal("Enter must focus Details for the selected target")
			}
			if m.splitFleet() {
				if !strings.Contains(m.View(), "FLEET | Filter: All") || !strings.Contains(m.View(), "DETAILS [focus]") || !strings.Contains(m.View(), "fixture-empty") || !strings.Contains(strings.Join(actualSplitRightInterior(t, m), ""), identity(target)) {
					t.Fatal("wide Details must retain both panes and the selected identity", m.View())
				}
			} else if !strings.HasPrefix(m.View(), "Fleet > Details") || strings.Contains(m.View(), "fixture-empty") {
				t.Fatal("narrow Details must retain a single full-page body")
			}
			for _, resized := range tuifixture.Sizes() {
				out, _ := m.Update(tea.WindowSizeMsg{Width: resized.Width, Height: resized.Height})
				m = out.(fleetModel)
				m, _ = fleetKey(m, "down")
				if !m.detail || m.detailEntry.Target != target || m.selected != target || m.ledgerScroll != scroll || maxLineWidth(m.View()) > resized.Width || len(strings.Split(m.View(), "\n")) > resized.Height {
					t.Fatal("resize or Details navigation stole target, focus or ledger scroll")
				}
			}
			m.width, m.height = size.Width, size.Height
			calls := 0
			m.services.Foreground = func(_ context.Context, req hostrouter.ActionRequest) (*exec.Cmd, error) {
				calls++
				if req.Target != target {
					t.Fatal("Connect retargeted")
				}
				return exec.Command("true"), nil // Never executed by this model test.
			}
			m, cmd = fleetKey(m, "enter")
			if cmd != nil || m.pending != nil || calls != 0 || !m.detail || m.detailEntry.Target != target {
				t.Fatal("Enter in Details must retain focus without opening Connect")
			}
			m, cmd = fleetKey(m, "c")
			if cmd != nil || m.pending == nil || m.pending.operation != "enter" || calls != 0 {
				t.Fatal("Connect must capture a confirmation before foreground dispatch")
			}
			for range 10 {
				m, _ = fleetKey(m, "pgdown")
			}
			m, cmd = fleetKey(m, "enter")
			if cmd == nil || calls != 1 || !m.detail || !m.busy {
				t.Fatalf("confirmed Connect did not use foreground adapter: %s", m.status)
			}
			// Model the existing foreground callback without attaching a live guest.
			// Update returns a registry-read command; it is not run here.
			out, _ := m.Update(fleetActionDone{target: target, request: m.actionSequence, operation: "enter"})
			m = out.(fleetModel)
			if !m.detail || m.selected != target || m.busy {
				t.Fatal("foreground return lost Details context")
			}
			m, _ = fleetKey(m, "esc")
			if m.detail || m.selected != target || m.scroll != scroll {
				t.Fatal("return lost ledger focus, identity or scroll")
			}
			if maxLineWidth(m.View()) > size.Width || len(strings.Split(m.View(), "\n")) > size.Height {
				t.Fatal("ledger exceeds terminal bounds")
			}
		})
	}
}

func TestLedgerInitialLoadingNeverSelectsTarget(t *testing.T) {
	m := fixtureFleet()
	m.hosts, m.selected = nil, hostrouter.Target{}
	m.services.Inventory = hostrouter.Router{}
	if !strings.Contains(m.View(), "Loading host registry") {
		t.Fatal("initial loading is hidden")
	}
	out, _ := m.Update(registryLoaded{epoch: m.epoch})
	m = out.(fleetModel)
	defer m.cancel()
	entry := fixtureRow(hostrouter.Local, hostrouter.Agent, "hawk")
	out, _ = m.Update(hostLoaded{epoch: m.epoch, inventory: hostrouter.HostInventory{Host: hostrouter.Local, Entries: []hostrouter.InventoryEntry{entry}}})
	m = out.(fleetModel)
	if m.selected != (hostrouter.Target{}) || m.detail {
		t.Fatal("arriving inventory automatically selected a target")
	}
	m, _ = fleetKey(m, "enter")
	if !strings.Contains(m.View(), "No target selected") || m.status == "" {
		t.Fatal("blocked initial control lacks feedback")
	}
}

func TestLedgerShortcutsOnlyOpenDetails(t *testing.T) {
	for _, key := range []string{"u", "s", "d", "x", "enter"} {
		m := fixtureFleet()
		m, cmd := fleetKey(m, key)
		if cmd != nil || !m.detail || m.pending != nil || m.operations != nil || m.busy || len(m.services.Actions.(*fixtureActions).requests) != 0 {
			t.Fatalf("ledger shortcut %s bypassed Details", key)
		}
	}
}

func TestLedgerFilterCapturesRegistrationAndClearsSelection(t *testing.T) {
	m := fixtureFleet()
	m, _ = fleetKey(m, "h")
	m, _ = fleetKey(m, "down") // local
	m, _ = fleetKey(m, "down") // remote
	remote := m.hosts[1].host
	// The filter picker retains identities, not mutable list indices.
	m.hosts[0], m.hosts[1] = m.hosts[1], m.hosts[0]
	m, _ = fleetKey(m, "enter")
	if m.filter != remote || m.selected != (hostrouter.Target{}) || len(m.rows()) != 1 || !strings.Contains(m.View(), "1H/1T hidden") {
		t.Fatalf("filter retargeted, selected implicitly or hid its scope: filter matches=%t selected=%v rows=%d\n%s", m.filter == remote, m.selected, len(m.rows()), m.View())
	}
	m, _ = fleetKey(m, "down")
	if m.selected.Host != remote {
		t.Fatal("filtered selection escaped its host")
	}
	m.hosts = m.hosts[1:]
	m.reconcile()
	if m.filter != remote || m.selected != (hostrouter.Target{}) || !strings.Contains(m.View(), "Filtered host removed") {
		t.Fatal("removed filter silently expanded or retargeted")
	}
	m, _ = fleetKey(m, "h")
	m, _ = fleetKey(m, "enter")
	if m.filter != (hostrouter.Host{}) || m.selected != (hostrouter.Target{}) {
		t.Fatal("All must not select a replacement")
	}
}

func TestLedgerCapturedIntentDoesNotRevive(t *testing.T) {
	for _, change := range []string{"removal", "registration", "project", "placement"} {
		t.Run(change, func(t *testing.T) {
			m := fixtureFleet()
			m.selected = m.hosts[1].entries[0].Target
			m, _ = fleetKey(m, "enter")
			m, _ = fleetKey(m, "d")
			if m.pending == nil {
				t.Fatal("missing captured action")
			}
			old := m.hosts[1]
			switch change {
			case "removal":
				m.hosts[1].entries = nil
			case "registration":
				m.hosts[1].host.Address = "replacement"
				m.hosts[1].entries = nil
			case "project":
				m.hosts[1].entries = append([]hostrouter.InventoryEntry(nil), old.entries...)
				m.hosts[1].entries[0].Project = "/replacement"
			case "placement":
				m.hosts[1].entries = nil
				m.hosts[0].entries = append(m.hosts[0].entries, fixtureRow(hostrouter.Local, hostrouter.Agent, old.entries[0].Target.ID))
			}
			m.reconcile()
			m.hosts[1] = old
			m.reconcile()
			if !m.detailInvalid || m.pending != nil {
				t.Fatal("captured intent revived after identity disappeared/changed")
			}
			m, cmd := fleetKey(m, "s")
			if cmd != nil || m.status == "" {
				t.Fatal("invalid Details dispatched")
			}
			m, _ = fleetKey(m, "esc")
			m, _ = fleetKey(m, "down")
			m, _ = fleetKey(m, "enter")
			if m.detailInvalid {
				t.Fatal("fresh explicit selection could not establish new intent")
			}
		})
	}
}

func TestLedgerRegistryFailureRetainsBlockedCachedRows(t *testing.T) {
	m := fixtureFleet()
	m.services.Inventory = hostrouter.Router{}
	remote := m.hosts[1].host
	out, _ := m.Update(registryLoaded{epoch: m.epoch, err: errors.New("unavailable")})
	m = out.(fleetModel)
	defer m.cancel()
	if len(m.hosts) != 2 || len(m.hosts[1].entries) != 1 || !strings.Contains(m.View(), "cached destination") {
		t.Fatalf("registry failure discarded cached remote hosts or hid cached-destination warning: hosts=%d\n%s", len(m.hosts), m.View())
	}
	if _, ok := m.entry(m.hosts[1].entries[0].Target); ok {
		t.Fatal("cached registration authorized an action")
	}
	if m.hosts[1].host != remote || m.busy || len(m.services.Actions.(*fixtureActions).requests) != 0 {
		t.Fatal("read failure retargeted or dispatched")
	}
}

func TestLedgerLongIdentitiesRemainDistinct(t *testing.T) {
	m := fixtureFleet()
	m.width, m.height = 40, 20
	h := hostrouter.Host{Name: "a-long-remote-host-registration", Address: "fixture-ssh"}
	a := fixtureRow(h, hostrouter.Agent, "duplicate-prefix-target-one")
	b := fixtureRow(h, hostrouter.Agent, "duplicate-prefix-target-two")
	m.hosts[1] = hostState{host: h, entries: []hostrouter.InventoryEntry{a, b}, updated: m.hosts[0].updated}
	// The ledger favors labels and bounded cells. Exact identities remain in
	// drill-in, including duplicate labels with identical truncated prefixes.
	for _, e := range []hostrouter.InventoryEntry{a, b} {
		m.selected = e.Target
		m, _ = fleetKey(m, "enter")
		lines, _, _ := m.content()
		text := strings.Join(strings.Fields(strings.Join(lines, "")), "")
		if !strings.Contains(text, strings.Join(strings.Fields(identity(e.Target)), "")) || m.detailEntry.Target != e.Target || !strings.Contains(text, "VMlifecycle:running") {
			t.Fatal("duplicate label lost its exact drill-in identity or lifecycle")
		}
		m, _ = fleetKey(m, "esc")
	}
}

func TestLedgerConnectRechecksLifecycleAtConfirmation(t *testing.T) {
	m := fixtureFleet()
	m, _ = fleetKey(m, "enter")
	m, _ = fleetKey(m, "c")
	if m.pending == nil {
		t.Fatal("Connect was not captured")
	}
	m.hosts[0].entries[0].State = "stopped"
	m, cmd := fleetKey(m, "y")
	if cmd != nil || !strings.Contains(m.status, "not running") {
		t.Fatal("confirmation used an obsolete running state")
	}
}

func TestLedgerResizeKeepsSelectedHighlightVisible(t *testing.T) {
	m := fixtureFleet()
	m.hosts = m.hosts[:1]
	m.hosts[0].entries = nil
	m.selected = hostrouter.Target{}
	m.width, m.height = 120, 40
	for i := range 20 {
		m.hosts[0].entries = append(m.hosts[0].entries, fixtureRow(hostrouter.Local, hostrouter.Agent, fmt.Sprintf("long-but-valid-agent-profile-%02d", i)))
	}
	for range 15 {
		m, _ = fleetKey(m, "down")
	}
	target := m.selected
	if target.ID != "long-but-valid-agent-profile-14" || !m.ledgerSelectionVisible() || !strings.Contains(m.View(), "> ") {
		t.Fatal("wide Ledger did not show the selected target")
	}
	for _, size := range []struct{ width, height int }{{40, 20}, {80, 24}, {120, 40}, {40, 20}} {
		out, cmd := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
		m = out.(fleetModel)
		if cmd != nil || m.selected != target || !m.ledgerSelectionVisible() || !strings.Contains(m.View(), "> ") {
			t.Fatalf("resize to %dx%d hid or changed the selected target:\n%s", size.width, size.height, m.View())
		}
		if maxLineWidth(m.View()) > size.width || len(strings.Split(m.View(), "\n")) > size.height {
			t.Fatal("resized Ledger exceeds terminal bounds")
		}
	}
	m, cmd := fleetKey(m, "enter")
	if cmd != nil || !m.detail || m.detailEntry.Target != target {
		t.Fatal("Enter did not open Details for the visible selected target")
	}
}

func TestLedgerResizePreservesVisibleManualScroll(t *testing.T) {
	m := fixtureFleet()
	m.width, m.height = 120, 40
	// Pad the Ledger so paging is meaningful without changing its target.
	for i := range 20 {
		m.hosts = append(m.hosts, hostState{host: hostrouter.Host{Name: fmt.Sprintf("empty-%02d", i)}})
	}
	m, _ = fleetKey(m, "pgdown")
	scroll, target := m.scroll, m.selected
	out, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = out.(fleetModel)
	if scroll == 0 || m.scroll != scroll || m.selected != target || !m.ledgerSelectionVisible() || !strings.Contains(m.View(), "> ") {
		t.Fatal("resize disturbed intentional scroll despite a visible selected target")
	}
}

func TestLedgerFocusSurvivesReturnAndRefresh(t *testing.T) {
	for _, journey := range []string{"details-resize-esc", "details-resize-tab", "refresh-reorder", "help-return", "runbook-return"} {
		t.Run(journey, func(t *testing.T) {
			m := fixtureFleet()
			m.hosts = m.hosts[:1]
			m.hosts[0].entries = nil
			m.selected = hostrouter.Target{}
			m.width, m.height = 120, 40
			for i := range 20 {
				m.hosts[0].entries = append(m.hosts[0].entries, fixtureRow(hostrouter.Local, hostrouter.Agent, fmt.Sprintf("long-but-valid-agent-profile-%02d", i)))
			}
			for range 15 {
				m, _ = fleetKey(m, "down")
			}
			target := m.selected
			if strings.HasPrefix(journey, "details") {
				m, _ = fleetKey(m, "enter")
			}
			out, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
			m = out.(fleetModel)
			switch journey {
			case "details-resize-esc":
				m, _ = fleetKey(m, "esc")
			case "details-resize-tab":
				m, _ = fleetKey(m, "tab")
			case "refresh-reorder":
				rows := append([]hostrouter.InventoryEntry(nil), m.hosts[0].entries...)
				for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
					rows[i], rows[j] = rows[j], rows[i]
				}
				out, _ = m.Update(hostLoaded{epoch: m.epoch, inventory: hostrouter.HostInventory{Host: hostrouter.Local, Entries: rows}})
				m = out.(fleetModel)
			case "help-return":
				m, _ = fleetKey(m, "?")
				m, _ = fleetKey(m, "pgdown")
				m, _ = fleetKey(m, "esc")
			case "runbook-return":
				m, _ = fleetKey(m, "v")
				m, _ = fleetKey(m, "pgdown")
				m, _ = fleetKey(m, "esc")
			}
			if m.detail || m.selected != target || !m.ledgerSelectionVisible() || !strings.Contains(m.View(), "> ") {
				t.Fatalf("journey hid or replaced selected target %s at scroll %d:\n%s", identity(target), m.scroll, m.View())
			}
			m, cmd := fleetKey(m, "enter")
			if cmd != nil || !m.detail || m.detailEntry.Target != target {
				t.Fatal("Details did not open the same visible target")
			}
		})
	}
}

func TestLedgerReturnAndRefreshPreserveIntentionalPaging(t *testing.T) {
	for _, journey := range []string{"details", "help", "runbook", "refresh"} {
		t.Run(journey, func(t *testing.T) {
			m := fixtureFleet()
			m.width, m.height = 40, 20
			for i := range 20 {
				m.hosts = append(m.hosts, hostState{host: hostrouter.Host{Name: fmt.Sprintf("empty-%02d", i)}})
			}
			for range 30 {
				m, _ = fleetKey(m, "pgdown")
			}
			scroll, target := m.scroll, m.selected
			if m.ledgerSelectionVisible() || strings.Contains(m.View(), "> ") {
				t.Fatal("setup did not page away from the selected target")
			}
			switch journey {
			case "details":
				m, _ = fleetKey(m, "enter")
				m, _ = fleetKey(m, "esc")
			case "help":
				m, _ = fleetKey(m, "?")
				m, _ = fleetKey(m, "esc")
			case "runbook":
				m, _ = fleetKey(m, "v")
				m, _ = fleetKey(m, "esc")
			case "refresh":
				out, _ := m.Update(hostLoaded{epoch: m.epoch, inventory: hostrouter.HostInventory{Host: hostrouter.Local, Entries: m.hosts[0].entries}})
				m = out.(fleetModel)
			}
			if m.selected != target || m.scroll != scroll || m.detail || m.help || m.runbook || len(m.services.Actions.(*fixtureActions).requests) != 0 {
				t.Fatal("return/read stole intentional paging, changed target or dispatched an action")
			}
		})
	}
}
