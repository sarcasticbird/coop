package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/tui/tuifixture"
)

func setupActionsFleet(size tuifixture.Size) fleetModel {
	m := fixtureFleet()
	m.workspace, m.width, m.height = 2, size.Width, size.Height
	m.server = m.hosts[1].host
	return m
}

// Match every physical content line against actual viewports, in order.
func reviewSetupPages(t *testing.T, m fleetModel) fleetModel {
	t.Helper()
	lines, _, _ := m.content()
	cursor := 0
	for range 1000 {
		view := m.View()
		page := strings.Split(view, "\n")
		if maxLineWidth(view) > m.width || len(page) > m.height {
			t.Fatalf("setup exceeds %dx%d viewport: %s", m.width, m.height, view)
		}
		for _, line := range page[:len(page)-2] {
			if cursor < len(lines) && line == lines[cursor] {
				cursor++
			}
		}
		previous := m.scroll
		var cmd tea.Cmd
		m, cmd = fleetKey(m, "pgdown")
		if cmd != nil || m.busy || len(m.services.Registry.(*fixtureRegistry).changes) != 0 {
			t.Fatal("paging dispatched a registry change")
		}
		if m.scroll == previous {
			if cursor != len(lines) {
				t.Fatalf("setup hid content at line %d of %d", cursor, len(lines))
			}
			return m
		}
	}
	t.Fatal("setup paging did not terminate")
	return m
}

func TestSetupActionsCRUDAtEverySize(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, operation := range []string{"add", "update", "remove"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.Width, size.Height, operation), func(t *testing.T) {
				m := setupActionsFleet(size)
				m.scroll = 7
				captured := m.server
				key := map[string]string{"add": "a", "update": "e", "remove": "d"}[operation]
				m, cmd := fleetKey(m, key)
				if cmd != nil || m.form == nil || m.form.operation != operation || m.form.original != captured || m.scroll != 0 || m.formScroll != 7 {
					t.Fatal("opening form lost operation, identity or return viewport")
				}
				address := strings.Repeat("ssh-reference-", 17) + "end.invalid"
				name := captured.Name
				switch operation {
				case "add":
					name = "new-server"
					m, _ = fleetKey(m, name)
					m, _ = fleetKey(m, "tab")
				case "update":
					if m.form.name != name || m.form.address != captured.Address || m.form.field != 1 {
						t.Fatal("update did not prefill captured registration")
					}
					for range len(captured.Address) {
						m, _ = fleetKey(m, "backspace")
					}
				}
				if operation != "remove" {
					m, _ = fleetKey(m, address)
				} else {
					address = captured.Address
				}
				lines, _, _ := m.content()
				all := strings.Join(lines, "")
				for _, want := range []string{strings.ToUpper(operation) + " SERVER", name, address, "Existing SSH reference only; not master setup. No provisioning, tokens or trust bypass."} {
					if !strings.Contains(all, want) {
						t.Fatalf("form lost %q", want)
					}
				}
				m = reviewSetupPages(t, m)
				// Selection changes cannot redirect a captured form.
				m.server = hostrouter.Local
				consent := "enter"
				if operation == "remove" {
					m, cmd = fleetKey(m, "enter")
					if cmd != nil || m.busy {
						t.Fatal("Enter removed a server without explicit y")
					}
					m.scroll = 0
					m = reviewSetupPages(t, m)
					consent = "y"
				}
				m, cmd = fleetKey(m, consent)
				if cmd == nil || !m.busy {
					t.Fatal("save not submitted", m.status)
				}
				done := cmd().(registryDone)
				if done.err != nil {
					t.Fatal(done.err)
				}
				r := m.services.Registry.(*fixtureRegistry)
				if !reflect.DeepEqual(r.changes, []string{operation + ":" + name + ":" + address}) {
					t.Fatal("save redirected or changed literal values", r.changes)
				}
				out, refresh := m.Update(done)
				m = out.(fleetModel)
				if m.cancel != nil {
					defer m.cancel()
				}
				if m.busy || m.form != nil || m.scroll != 7 || refresh == nil || !strings.Contains(m.status, "updated") {
					t.Fatal("successful save did not return and refresh Servers", m.status)
				}
				if len(m.services.Actions.(*fixtureActions).requests) != 0 {
					t.Fatal("server CRUD dispatched a target action")
				}
			})
		}
	}
}

func TestSetupActionsCancelAndValidationAtEverySize(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, operation := range []string{"add", "update", "remove"} {
			for _, cancel := range []string{"esc", "n"} {
				if cancel == "n" && operation != "remove" {
					continue
				}
				t.Run(fmt.Sprintf("%dx%d/%s/%s", size.Width, size.Height, operation, cancel), func(t *testing.T) {
					m := setupActionsFleet(size)
					m.scroll = 9
					server, selected := m.server, m.selected
					m, _ = fleetKey(m, map[string]string{"add": "a", "update": "e", "remove": "d"}[operation])
					m = reviewSetupPages(t, m)
					if operation != "remove" {
						before := *m.form
						r := m.services.Registry.(*fixtureRegistry)
						r.err = errors.New("validation rejected")
						var cmd tea.Cmd
						m, cmd = fleetKey(m, "enter")
						if cmd == nil {
							t.Fatal("validation never reached registry")
						}
						done := cmd().(registryDone)
						if done.err == nil {
							t.Fatal("validation failure lost")
						}
						out, cmd := m.Update(done)
						m = out.(fleetModel)
						if cmd != nil || m.busy || m.form == nil || *m.form != before || !strings.Contains(m.status, "server change failed: validation rejected") {
							t.Fatal("failed save lost recoverable editor", m.status)
						}
						if len(r.hosts) != 1 || r.hosts[0] != server {
							t.Fatal("validation failure changed registry")
						}
					}
					changes := len(m.services.Registry.(*fixtureRegistry).changes)
					m, cmd := fleetKey(m, cancel)
					if cmd != nil || m.form != nil || m.busy || m.workspace != 2 || m.scroll != 9 || m.server != server || m.selected != selected || len(m.services.Registry.(*fixtureRegistry).changes) != changes {
						t.Fatal("cancel failed to restore Servers or dispatched a change")
					}
				})
			}
		}
	}
}

func TestSetupActionsStaleRegistrationRefusedAtEverySize(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, operation := range []string{"update", "remove"} {
			for _, change := range []string{"address", "removed"} {
				t.Run(fmt.Sprintf("%dx%d/%s/%s", size.Width, size.Height, operation, change), func(t *testing.T) {
					m := setupActionsFleet(size)
					m.scroll = 5
					m, _ = fleetKey(m, map[string]string{"update": "e", "remove": "d"}[operation])
					m = reviewSetupPages(t, m)
					key := "enter"
					if operation == "remove" {
						key = "y"
					}
					m, cmd := fleetKey(m, key)
					if cmd == nil {
						t.Fatal("save not queued")
					}
					// Change after queuing: validation must occur when the command runs.
					r := m.services.Registry.(*fixtureRegistry)
					if change == "address" {
						r.hosts[0].Address = "replacement-alias"
					} else {
						r.hosts = nil
					}
					done := cmd().(registryDone)
					if done.err == nil || len(r.changes) != 0 {
						t.Fatal("stale registration reached Change", r.changes)
					}
					out, cmd := m.Update(done)
					m = out.(fleetModel)
					if cmd != nil || m.busy || m.form == nil || !strings.Contains(m.status, "stale target") {
						t.Fatal("stale save did not leave a recoverable failure", m.status)
					}
					m, cmd = fleetKey(m, "esc")
					if cmd != nil || m.form != nil || m.scroll != 5 || len(r.changes) != 0 {
						t.Fatal("back from stale refusal failed")
					}
				})
			}
		}
	}
}

func TestSetupActionsRemovalRequiresContiguousReviewAndResizeAtEverySize(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, dimension := range []string{"width", "height"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.Width, size.Height, dimension), func(t *testing.T) {
				m := setupActionsFleet(size)
				// Stored registrations can exceed the editor's new-input limit.
				h := hostrouter.Host{Name: "long-server", Address: strings.Repeat("ssh-reference-", 900) + "end.invalid"}
				m.server, m.hosts[1].host = h, h
				m.services.Registry.(*fixtureRegistry).hosts = []hostrouter.Host{h}
				m, _ = fleetKey(m, "d")
				m, cmd := fleetKey(m, "y")
				if cmd != nil || m.busy || m.form == nil || !strings.Contains(m.status, "Review complete removal") {
					t.Fatal("early y authorized removal")
				}
				lines, _, _ := m.content()
				if !strings.Contains(strings.Join(lines, ""), h.Address) {
					t.Fatal("removal truncated captured SSH reference")
				}
				m.scroll = len(lines)
				m, cmd = fleetKey(m, "y")
				if cmd != nil || m.busy || m.form == nil || m.form.reviewedThrough >= len(lines) {
					t.Fatal("skipped pages authorized removal")
				}
				m.scroll = 0
				m = reviewSetupPages(t, m)
				if m.form.reviewedThrough == 0 || m.scroll == 0 {
					t.Fatal("fixture did not require review")
				}
				out, _ := m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
				unchanged := out.(fleetModel)
				if unchanged.scroll != m.scroll || unchanged.form.reviewedThrough != m.form.reviewedThrough {
					t.Fatal("unchanged geometry erased review")
				}
				width, height := m.width, m.height
				if dimension == "width" {
					width++
				} else {
					height++
				}
				out, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
				m = out.(fleetModel)
				if m.scroll != 0 || m.form.reviewedThrough != 0 || m.form.original != h {
					t.Fatal("resize retained review or changed captured identity")
				}
				m, cmd = fleetKey(m, "y")
				if cmd != nil || m.busy || m.form == nil {
					t.Fatal("old geometry authorized removal")
				}
				m = reviewSetupPages(t, m)
				if len(m.services.Registry.(*fixtureRegistry).changes) != 0 {
					t.Fatal("review alone removed server")
				}
				m, cmd = fleetKey(m, "y")
				if cmd == nil || !m.busy {
					t.Fatal("complete fresh review did not permit removal", m.status)
				}
				if done := cmd().(registryDone); done.err != nil {
					t.Fatal(done.err)
				}
				if r := m.services.Registry.(*fixtureRegistry); len(r.hosts) != 0 || !reflect.DeepEqual(r.changes, []string{"remove:" + h.Name + ":" + h.Address}) {
					t.Fatal("removal did not use reviewed registration")
				}
			})
		}
	}
}

func TestSetupActionsOperationSpecsCompleteness(t *testing.T) {
	want := map[string]string{
		"enter": "target", "status": "target", "up": "target", "down": "target", "destroy": "target", "rebuild": "target", "apply": "target", "backup": "target",
		"sync": "agent", "credentials refresh": "agent", "credentials watch": "agent", "worker status": "agent", "worker requests": "agent", "worker pause": "agent", "worker resume": "agent", "ping": "agent",
		"doctor": "host", "upgrade": "host", "init": "host", "restore": "host", "mcp register": "host", "mcp inspect": "host",
	}
	readOnly := map[string]bool{"status": true, "worker status": true, "worker requests": true, "doctor": true, "mcp inspect": true}
	seen := make(map[string]bool)
	for _, spec := range operationSpecs {
		if seen[spec.name] || want[spec.name] != spec.scope {
			t.Errorf("unexpected, duplicate or incorrectly scoped operation: %+v", spec)
		}
		seen[spec.name] = true
		if spec.note == "" || spec.confirm == readOnly[spec.name] || spec.foreground && !spec.confirm {
			t.Errorf("operation lacks consequence or consent policy: %+v", spec)
		}
		for _, field := range spec.fields {
			if strings.TrimSpace(field) == "" {
				t.Errorf("%s has an unlabeled field", spec.name)
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("missing operation %q", name)
		}
	}
}

func TestSetupActionsRunbookCompletenessAtEverySize(t *testing.T) {
	text := strings.Join(serviceRunbook(), "\n")
	for _, want := range []string{
		"operator-managed", "never daemonized", "exact intended host", "private host configuration",
		"coop mcp serve --config", "coop mcp stdio --config", "coop master serve --config",
		"coop credentials watch --profile", "Ctrl+C ends watch", "master backup/restore",
		"reconcile status/pause/resume/evidence/resolve", "placement set", "history import/list", "cutover",
		"docs/master.md", "Never run recovery against a live service", "Presence and receipts never imply goal approval",
		"coop --agent NAME approve GOAL_URL", "not a native goal decision", "server list/add/update/remove",
		"coop cache status", "coop team inspect", "coop message continue", "not automatic reply/wake",
		"Servers > x > init", "coop --help", "coop --version", "bash|zsh|fish|powershell",
		"live fleet UAT remains pending", "no production restart or rollout",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("runbook omits %q", want)
		}
	}
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m := setupActionsFleet(size)
			m.services.Version = "fixture-build-120"
			m.scroll = 6
			m, cmd := fleetKey(m, "v")
			if cmd != nil || !m.runbook || m.scroll != 0 {
				t.Fatal("runbook did not open without dispatch")
			}
			lines, _, _ := m.content()
			if !strings.Contains(strings.Join(lines, ""), "Coop build: fixture-build-120") {
				t.Fatal("About omitted build identity")
			}
			m = reviewSetupPages(t, m)
			m, cmd = fleetKey(m, "esc")
			if cmd != nil || m.runbook || m.scroll != 6 || len(m.services.Actions.(*fixtureActions).requests) != 0 {
				t.Fatal("runbook return lost viewport or dispatched action")
			}
		})
	}
}
