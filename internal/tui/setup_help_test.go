package tui

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

// Read through the actual viewport so narrow layouts cannot hide guidance below
// an unreachable clipping boundary. Whitespace normalization permits hard wraps.
func setupHelpPages(t *testing.T, m fleetModel) string {
	t.Helper()
	lines, _, _ := m.content()
	var pages []string
	for range len(lines) + 1 {
		assertComposeBounds(t, m)
		pages = append(pages, m.View())
		next, cmd := composeKey(m, tea.KeyPgDown)
		if cmd != nil {
			t.Fatal("paging setup/help dispatched a command")
		}
		if next.scroll == m.scroll {
			break
		}
		m = next
	}
	return strings.Join(pages, "\n")
}

func setupHelpContains(t *testing.T, text string, wants ...string) {
	t.Helper()
	compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
	for _, want := range wants {
		if !strings.Contains(compact(text), compact(want)) {
			t.Errorf("setup/help omitted %q", want)
		}
	}
}

func TestSetupHelpMissingMasterAndSSH(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 20}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := fixtureFleet()
			m.width, m.height, m.workspace = size[0], size[1], 1
			setupHelpContains(t, setupHelpPages(t, m), "Master setup", "Master client unavailable", "Fleet remains available", "existing private master-client.toml", "credential references", "token_file or token_env", "outside this TUI", "never enter tokens", "relaunch", "coop --master-config /private/master-client.toml tui", "does not accept token input, switch masters, or start a master service", "SSH setup is separate")

			// A local-only registry is loaded, not still loading remote hosts.
			m.workspace = 2
			m.hosts = m.hosts[:1]
			m.services.Registry = &fixtureRegistry{}
			setupHelpContains(t, setupHelpPages(t, m), "SSH setup", "~/.ssh/config")
			if strings.Contains(m.View(), "Master client unavailable") {
				t.Fatal("Servers incorrectly presents missing master as SSH setup")
			}
		})
	}
}

func TestSetupHelpEditorsPreserveStateAndLiteralQuestionMark(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 20}} {
		for _, editor := range []string{"server", "operation", "composer"} {
			t.Run(fmt.Sprint(size)+"/"+editor, func(t *testing.T) {
				m := fixtureFleet()
				m.width, m.height = size[0], size[1]
				switch editor {
				case "server":
					m.workspace = 2
					m.form = &serverForm{operation: "add", name: "fixture", address: "synthetic-editor-private", field: 1}
				case "operation":
					index := slices.IndexFunc(operationSpecs, func(s operationSpec) bool { return s.name == "restore" })
					if index < 0 {
						t.Fatal("restore operation missing")
					}
					m.operations = &operationForm{entry: hostrouter.InventoryEntry{Target: hostrouter.Target{Host: hostrouter.Local}}, index: index, field: 1, editing: true, values: []string{"profile", "synthetic-editor-private", "/archive"}, requestID: "captured-request"}
				case "composer":
					m, _ = coordinationFixture()
					m.width, m.height = size[0], size[1]
					m, _ = coordinationKey(m, "n")
					m, _ = coordinationKey(m, "synthetic-editor-private")
				}
				m, cmd := fleetKey(m, "?")
				if cmd != nil || m.help {
					t.Fatal("question mark opened help or dispatched from editor")
				}
				var value string
				switch editor {
				case "server":
					value = m.form.address
				case "operation":
					value = m.operations.values[m.operations.field]
				case "composer":
					value = m.coordination.form.values[m.coordination.form.field]
				}
				if value != "synthetic-editor-private?" {
					t.Fatalf("question mark was not literal: %q", value)
				}
				m.scroll = 3
				before := m
				m, cmd = composeKey(m, tea.KeyF1)
				if cmd != nil || !m.help || m.scroll != 0 || m.overlayScroll != before.scroll {
					t.Fatal("F1 did not open help at the top while retaining editor viewport")
				}
				pages := setupHelpPages(t, m)
				setupHelpContains(t, strings.ToLower(pages), "help", "esc")
				if strings.Contains(pages, "synthetic-editor-private") {
					t.Fatal("help exposed editor buffer")
				}
				m, _ = composeKey(m, tea.KeyPgDown)
				m, cmd = composeKey(m, tea.KeyEsc)
				if cmd != nil || m.help || m.scroll != before.scroll || m.workspace != before.workspace || !reflect.DeepEqual(m.form, before.form) || !reflect.DeepEqual(m.operations, before.operations) || !reflect.DeepEqual(m.coordination.form, before.coordination.form) {
					t.Fatal("help return changed field, buffer, captured context or viewport")
				}
			})
		}
	}
}

func TestSetupHelpExcludesPrivateFeedbackAndOutput(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 20}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := fixtureFleet()
			m.width, m.height = size[0], size[1]
			m.status, m.output, m.registryError = "synthetic-private-status", "synthetic-private-output", "synthetic-private-registry"
			before := m
			m, cmd := fleetKey(m, "?")
			if cmd != nil || !m.help {
				t.Fatal("help dispatched a command or failed to open")
			}
			pages := setupHelpPages(t, m)
			for _, private := range []string{before.status, before.output, before.registryError} {
				if strings.Contains(pages, private) {
					t.Errorf("help exposed %q", private)
				}
			}
			m, _ = fleetKey(m, "esc")
			if m.status != before.status || m.output != before.output || m.registryError != before.registryError {
				t.Fatal("help discarded private underlying feedback")
			}
		})
	}
}

func TestSetupHelpCannotReviewUnderlyingRemoval(t *testing.T) {
	m := fixtureFleet()
	m.width, m.height, m.workspace = 40, 20, 2
	m.server = m.hosts[1].host
	m, _ = fleetKey(m, "d")
	m, _ = composeKey(m, tea.KeyF1)
	m, _ = composeKey(m, tea.KeyPgDown)
	if !m.help || m.form.reviewedThrough != 0 {
		t.Fatal("help paging credited unseen removal content")
	}
	out, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = out.(fleetModel)
	m, _ = composeKey(m, tea.KeyEsc)
	if m.help || m.scroll != 0 || m.form.reviewedThrough != 0 {
		t.Fatal("resized help did not reset underlying removal review")
	}
}
