package tui

import (
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	textansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/tui/tuifixture"
)

func runtimeIntegrationFixture(t *testing.T, size tuifixture.Size) (fleetModel, *coordinationMock) {
	t.Helper()
	m := runtimeBaselineFleet()
	coord, source := coordinationFixture()
	m.coordination, m.services.Coordination = coord.coordination, source
	m.width, m.height = size.Width, size.Height
	m.coordination.threads = []c.Thread{{ID: "original-thread"}}
	source.thread = func(id string) (c.Thread, error) {
		if id != "original-thread" {
			t.Fatalf("reader retargeted to %q", id)
		}
		return c.Thread{ID: id, Members: []string{"owner", "hawk"}}, nil
	}
	source.messages = func(id string, after int64) (c.MessagePage, error) {
		return c.MessagePage{Messages: []c.Message{{ID: "original-parent", ThreadID: id, AuthorID: "hawk", Sequence: 1, Body: "private-reader-marker"}}}, nil
	}
	source.send = func(c.SendMessage) (c.MessageReceipt, error) {
		t.Fatal("navigation sent a message")
		return c.MessageReceipt{}, nil
	}
	m, cmd := coordinationKey(m, "2")
	if cmd != nil || m.workspace != 1 {
		t.Fatal("could not enter cached coordination workspace")
	}
	m, cmd = coordinationKey(m, "enter")
	if cmd == nil {
		t.Fatal("reader did not schedule a read")
	}
	m = coordinationRun(t, m, cmd)
	if m.coordination.thread == nil || m.coordination.thread.ID != "original-thread" || len(m.coordination.messages) != 1 {
		t.Fatal("reader did not open original thread")
	}
	return m, source
}

func TestRuntimeIntegrationColorProfileParity(t *testing.T) {
	// The default Lip Gloss renderer is global; keep this journey serial.
	profile, dark := lipgloss.ColorProfile(), lipgloss.HasDarkBackground()
	t.Cleanup(func() {
		lipgloss.SetColorProfile(profile)
		lipgloss.SetHasDarkBackground(dark)
	})
	profiles := []struct {
		name    string
		profile termenv.Profile
		dark    bool
		noColor string
	}{
		{"truecolor-light", termenv.TrueColor, false, ""},
		{"truecolor-dark", termenv.TrueColor, true, ""},
		{"ansi-low-color", termenv.ANSI, true, ""},
		{"ascii-no-color", termenv.Ascii, true, "1"},
	}
	sgr := regexp.MustCompile(`\x1b\[[0-9;:]*m`)
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m, _ := runtimeIntegrationFixture(t, size)
			metadata := "synthetic-\u754ce\u0301-" + strings.Repeat("long-label-", 20)
			m.hosts[0].entries[0].Name = metadata
			m.hosts[0].entries[0].Project = metadata
			// Coordination metadata is untrusted display input, not private body text.
			m.coordination.thread.Members = []string{"owner", metadata + "\x1b[2J\x07\r\t\x00\u009b"}
			m.coordination.messages[0].AuthorID = metadata + "\x1b]0;unsafe-title\x07"
			check := func(state string) {
				t.Helper()
				var baseline string
				for i, p := range profiles {
					t.Run(state+"/"+p.name, func(t *testing.T) {
						t.Setenv("NO_COLOR", p.noColor)
						lipgloss.SetColorProfile(p.profile)
						lipgloss.SetHasDarkBackground(p.dark)
						if lipgloss.ColorProfile() != p.profile || lipgloss.HasDarkBackground() != p.dark {
							t.Fatal("explicit renderer settings were not applied")
						}
						assertConversationBounds(t, m)
						view := m.View()
						for _, marker := range []string{"private-reader-marker", "private-profile-draft-marker"} {
							if strings.Contains(view, marker) {
								t.Fatal("private input leaked into journey view")
							}
						}
						// Permit color SGR, but not cursor/OSC sequences or other controls.
						for _, r := range sgr.ReplaceAllString(view, "") {
							if unicode.IsControl(r) && r != '\n' {
								t.Fatalf("unsafe display control U+%04X", r)
							}
						}
						plain := textansi.Strip(view)
						if p.profile == termenv.Ascii && view != plain {
							t.Fatal("Ascii/NO_COLOR view contains ANSI escapes")
						}
						// Runtime views currently assemble plain text. This checks the
						// model-view boundary, not terminal detection or added styling.
						if i == 0 {
							baseline = plain
						} else if plain != baseline {
							t.Fatal("color profile changed semantic view")
						}
					})
				}
			}
			key := func(key string) {
				t.Helper()
				var cmd tea.Cmd
				m, cmd = coordinationKey(m, key)
				if cmd != nil {
					t.Fatalf("navigation %q scheduled an operation", key)
				}
			}
			key("1")
			if m.workspace != 0 || m.detail {
				t.Fatal("did not reach Fleet")
			}
			check("Fleet")
			key("enter")
			if !m.detail {
				t.Fatal("did not reach Details")
			}
			check("Details")
			key("?")
			if !m.help {
				t.Fatal("did not reach Details help")
			}
			check("help")
			key("esc")
			key("esc")
			key("3")
			if m.workspace != 2 {
				t.Fatal("did not reach Servers")
			}
			check("Servers")
			key("2")
			if m.workspace != 1 || m.coordination.thread == nil || m.coordination.readerReveal {
				t.Fatal("did not return to masked reader")
			}
			check("masked-reader")
			key("p")
			if m.coordination.form == nil || m.coordination.form.parent != "original-parent" || m.coordination.form.reveal {
				t.Fatal("did not open masked reply to original parent")
			}
			key("tab")
			const draft = "private-profile-draft-marker\n\u754ce\u0301"
			key(draft)
			if m.coordination.form.values[1] != draft {
				t.Fatal("reply editor did not retain private input")
			}
			check("reply-form")
			var cmd tea.Cmd
			m, cmd = composeKey(m, tea.KeyF1)
			if cmd != nil || !m.help {
				t.Fatal("F1 did not open reply help without dispatch")
			}
			check("F1-help")
			key("esc")
			key("ctrl+s")
			if !m.coordination.form.preview || m.coordination.form.reveal {
				t.Fatal("did not reach masked preview")
			}
			check("preview")
			if len(m.services.Actions.(*fixtureActions).requests) != 0 || len(m.services.Registry.(*fixtureRegistry).changes) != 0 {
				t.Fatal("profile journey dispatched a mutation")
			}
		})
	}
}

func TestRuntimeIntegrationKeptReplyAcrossWorkspaces(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m, _ := runtimeIntegrationFixture(t, size)
			target := m.selected
			calls := 0
			m.services.Foreground = func(_ context.Context, req hostrouter.ActionRequest) (*exec.Cmd, error) {
				calls++
				if req.Target != target || req.Operation != "command" || !slices.Equal(req.Args, []string{"--agent", target.ID, "--"}) {
					t.Fatalf("Connect retargeted: %+v", req)
				}
				return exec.Command("unused"), nil // The model callback is delivered below; no process runs.
			}
			m, _ = coordinationKey(m, "p")
			m, _ = coordinationKey(m, "tab")
			m, _ = coordinationKey(m, "private-draft-marker\n\u754ce\u0301\x1b[31m\x07")
			m, _ = composeKey(m, tea.KeyLeft)
			m, _ = coordinationKey(m, "X")
			f := m.coordination.form
			if f == nil || f.requestID == "" || f.parent != "original-parent" {
				t.Fatal("reply did not capture original intent")
			}
			id, values, positions, field := f.requestID, slices.Clone(f.values), slices.Clone(f.positions), f.field
			assertComposeBounds(t, m)
			private := func() {
				t.Helper()
				assertConversationBounds(t, m)
				for _, marker := range []string{"private-draft-marker", "private-reader-marker", "\u754c", "e\u0301", "\x1b[31m", "\x07"} {
					if strings.Contains(m.View(), marker) {
						t.Fatalf("private content %q leaked outside editor", marker)
					}
				}
				if len(m.services.Actions.(*fixtureActions).requests) != 0 || len(m.services.Registry.(*fixtureRegistry).changes) != 0 {
					t.Fatal("navigation dispatched a write")
				}
			}
			m, _ = coordinationKey(m, "esc")
			private()
			m, _ = coordinationKey(m, "k")
			if m.coordination.form != nil || len(m.coordination.drafts) != 1 {
				t.Fatal("Keep did not suspend reply")
			}
			for _, key := range []string{"1", "enter", "enter"} {
				var cmd tea.Cmd
				m, cmd = coordinationKey(m, key)
				if cmd != nil {
					t.Fatalf("%s dispatched before Connect consent", key)
				}
				private()
			}
			if m.workspace != 0 || !m.detail || m.pending == nil || calls != 0 {
				t.Fatal("Fleet Details Connect did not require consent")
			}
			m = composeReview(m)
			m, cmd := coordinationKey(m, "y")
			if cmd == nil || calls != 1 || !m.busy || m.selected != target {
				t.Fatal("explicit Connect did not dispatch captured foreground target")
			}
			private()
			// Deliver the foreground completion without a terminal or external command.
			out, refresh := m.Update(fleetActionDone{target: target, request: m.actionSequence, operation: "enter"})
			m = out.(fleetModel)
			if refresh == nil || m.busy || !m.detail || m.detailEntry.Target != target {
				t.Fatal("foreground return lost Details or failed to schedule refresh")
			}
			private()
			for _, key := range []string{"esc", "3", "?", "pgdown", "esc", "2"} {
				m, cmd = coordinationKey(m, key)
				if cmd != nil {
					t.Fatalf("navigation %s scheduled an automatic operation", key)
				}
				if key == "3" && m.workspace != 2 || key == "?" && !m.help {
					t.Fatalf("%s did not reach requested view", key)
				}
				private()
			}
			if m.workspace != 1 || m.coordination.thread.ID != "original-thread" || m.coordination.messages[m.coordination.cursor].ID != "original-parent" {
				t.Fatal("workspace return retargeted reader")
			}
			m, cmd = coordinationKey(m, "p")
			f = m.coordination.form
			if cmd != nil || f == nil || f.requestID != id || f.thread.ID != "original-thread" || f.parent != "original-parent" || f.field != field || !reflect.DeepEqual(f.values, values) || !reflect.DeepEqual(f.positions, positions) || len(m.coordination.drafts) != 0 || calls != 1 {
				t.Fatal("resume changed captured request, parent, values or cursor, or dispatched an operation")
			}
			assertComposeBounds(t, m)
			m, _ = coordinationKey(m, "esc")
			private()
		})
	}
}

func TestRuntimeIntegrationDelayedReaderRefresh(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, workspace := range []string{"1", "3"} {
			t.Run(fmt.Sprintf("%dx%d/workspace%s", size.Width, size.Height, workspace), func(t *testing.T) {
				m, source := runtimeIntegrationFixture(t, size)
				calls := 0
				source.messages = func(id string, after int64) (c.MessagePage, error) {
					calls++
					if id != "original-thread" || after != 1 {
						t.Fatalf("refresh retargeted: %q after %d", id, after)
					}
					if calls == 1 {
						return c.MessagePage{}, c.ErrUnavailable
					}
					return c.MessagePage{Messages: []c.Message{{ID: "arrival", ThreadID: id, Sequence: 2, Body: "private-arrival-marker"}}}, nil
				}
				target, server := m.selected, m.server
				var cmd tea.Cmd
				for _, failed := range []bool{true, false} {
					m, cmd = coordinationKey(m, "r")
					if cmd == nil || !m.coordination.busy {
						t.Fatal("reader refresh was not scheduled")
					}
					var nav tea.Cmd
					m, nav = coordinationKey(m, workspace)
					if nav != nil || m.workspace == 1 {
						t.Fatal("pending reader refresh blocked workspace navigation")
					}
					m, _ = coordinationKey(m, "pgdown")
					before, active, scroll := m.View(), m.workspace, m.scroll
					m = coordinationRun(t, m, cmd)
					if m.workspace != active || m.scroll != scroll || m.View() != before || m.selected != target || m.server != server {
						t.Fatal("delayed reader result hijacked active workspace or retargeted selection")
					}
					if m.coordination.busy || m.coordination.failed != failed || m.coordination.readerBlocked != failed || m.coordination.thread.ID != "original-thread" || m.coordination.messages[m.coordination.cursor].ID != "original-parent" {
						t.Fatal("delayed result lost reader error/recovery state or original parent")
					}
					assertConversationBounds(t, m)
					m, nav = coordinationKey(m, "2")
					if nav != nil || m.workspace != 1 || m.coordination.readerReveal {
						t.Fatal("reader return scheduled an operation or revealed private messages")
					}
					for _, marker := range []string{"private-reader-marker", "private-arrival-marker"} {
						if strings.Contains(m.View(), marker) {
							t.Fatal("reader return leaked masked body")
						}
					}
					assertConversationBounds(t, m)
					if !failed && (len(m.coordination.messages) != 2 || m.coordination.after != 2) {
						t.Fatal("recovery did not append delayed arrival")
					}
				}
				if calls != 2 || len(m.services.Actions.(*fixtureActions).requests) != 0 || len(m.services.Registry.(*fixtureRegistry).changes) != 0 {
					t.Fatal("refresh/navigation retried automatically or dispatched a mutation")
				}
			})
		}
	}
}
