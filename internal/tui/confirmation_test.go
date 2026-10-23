package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/tui/tuifixture"
)

// Review using real paging keys, including the last page (credited by y).
func reviewFleetConfirmation(t *testing.T, m fleetModel) fleetModel {
	t.Helper()
	if m.pending == nil {
		t.Fatal("missing confirmation")
	}
	for range 1000 {
		previous := m.scroll
		var cmd tea.Cmd
		m, cmd = fleetKey(m, "pgdown")
		if cmd != nil || m.pending == nil || m.busy {
			t.Fatal("review dispatched an action")
		}
		if m.scroll == previous {
			return m
		}
	}
	t.Fatal("confirmation paging did not terminate")
	return m
}

func TestConfirmationCompleteReviewAtEverySize(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m := fixtureFleet()
			m.width, m.height = size.Width, size.Height
			h := hostrouter.Host{Name: strings.Repeat("fixture-host-", 12), Address: strings.Repeat("fixture-ssh-", 16) + ".invalid"}
			e := fixtureRow(h, hostrouter.Interactive, strings.Repeat("fixture-target-", 14))
			e.Name = strings.Repeat("fixture-name-", 1000)
			e.Project = "/fixture/" + strings.Repeat("project with spaces/", 14)
			m.hosts[1].host, m.hosts[1].entries = h, []hostrouter.InventoryEntry{e}
			m.services.Registry.(*fixtureRegistry).hosts = []hostrouter.Host{h}
			m.selected = e.Target
			literal := "--fixture-reference=" + strings.Repeat("literal $(not-a-shell); 'quoted' ", 15)
			m.services.PrepareAction = func(r hostrouter.ActionRequest) (hostrouter.ActionRequest, error) {
				r.Args = append(r.Args, literal)
				return r, nil
			}
			m, _ = fleetKey(m, "tab")
			m, cmd := fleetKey(m, "d")
			if cmd != nil || m.pending == nil || m.busy {
				t.Fatal("shortcut dispatched without consent")
			}
			m, cmd = fleetKey(m, "y")
			if cmd != nil || m.pending == nil || !strings.Contains(m.status, "Review complete") {
				t.Fatal("early consent was accepted")
			}
			lines, _, _ := m.content()
			// Jumping directly to the final page must not count skipped pages.
			m.scroll = len(lines)
			m, cmd = fleetKey(m, "y")
			if cmd != nil || m.pending == nil || m.pending.reviewedThrough >= len(lines) {
				t.Fatal("noncontiguous review accepted")
			}
			m.scroll = 0
			var visible []string
			for range 1000 {
				view := m.View()
				if maxLineWidth(view) > size.Width || len(strings.Split(view, "\n")) > size.Height {
					t.Fatal("confirmation exceeded viewport bounds")
				}
				page := strings.Split(view, "\n")
				visible = append(visible, page[:len(page)-2]...)
				previous := m.scroll
				m, cmd = fleetKey(m, "pgdown")
				if cmd != nil || m.busy || len(m.services.Actions.(*fixtureActions).requests) != 0 {
					t.Fatal("paging dispatched")
				}
				if m.scroll == previous {
					break
				}
			}
			// Every physical content line must occur in order in the actual viewports.
			cursor := 0
			for _, line := range visible {
				if cursor < len(lines) && line == lines[cursor] {
					cursor++
				}
			}
			if cursor != len(lines) {
				t.Fatalf("review hid content at line %d of %d", cursor, len(lines))
			}
			all := strings.Join(lines, "")
			for _, want := range []string{identity(e.Target), e.Name, h.Address, e.Project, fmt.Sprintf("%q", []string{"destroy", literal}), "Remove this Coop and its persistent state volumes?", "No automatic retry or rollback."} {
				if !strings.Contains(all, want) {
					t.Fatalf("confirmation lost %q", want)
				}
			}
			m, cmd = fleetKey(m, "y")
			if cmd == nil || m.pending != nil || !m.busy {
				t.Fatal("complete review did not dispatch", m.status)
			}
			if done := cmd().(fleetActionDone); done.err != nil {
				t.Fatal(done.err)
			}
			requests := m.services.Actions.(*fixtureActions).requests
			if len(requests) != 1 || requests[0].Target != e.Target || requests[0].Directory != e.Project || !reflect.DeepEqual(requests[0].Args, []string{"destroy", literal}) {
				t.Fatal("dispatch differed from consent", requests)
			}
		})
	}
}

func TestConfirmationResizeResetsReviewAtEverySize(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, dimension := range []string{"width", "height"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.Width, size.Height, dimension), func(t *testing.T) {
				m := fixtureFleet()
				m.width, m.height = size.Width, size.Height
				m.hosts[0].entries[0].Name = strings.Repeat("long-fixture-name-", 600)
				m, _ = fleetKey(m, "tab")
				m, _ = fleetKey(m, "d")
				m = reviewFleetConfirmation(t, m)
				if m.pending.reviewedThrough == 0 || m.scroll == 0 {
					t.Fatal("fixture did not require paging")
				}
				width, height := m.width, m.height
				if dimension == "width" {
					width++
				} else {
					height++
				}
				out, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
				m = out.(fleetModel)
				if m.scroll != 0 || m.pending.reviewedThrough != 0 {
					t.Fatal("resize retained review progress")
				}
				m, cmd := fleetKey(m, "y")
				if cmd != nil || m.pending == nil {
					t.Fatal("old geometry authorized consent")
				}
				m = reviewFleetConfirmation(t, m)
				m, cmd = fleetKey(m, "y")
				if cmd == nil || !m.busy {
					t.Fatal("fresh review could not authorize action")
				}
				m.actionCancel()
			})
		}
	}
}

func TestConfirmationCancelRestoresDetailsScrollAtEverySize(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, key := range []string{"n", "esc"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.Width, size.Height, key), func(t *testing.T) {
				m := fixtureFleet()
				m.width, m.height = size.Width, size.Height
				m, _ = fleetKey(m, "tab")
				m.scroll = 7
				captured := *m.detailEntry
				m, _ = fleetKey(m, "s")
				if m.pending == nil || m.pending.returnScroll != 7 || m.scroll != 0 {
					t.Fatal("confirmation lost Details return position")
				}
				m = reviewFleetConfirmation(t, m)
				m, cmd := fleetKey(m, key)
				if cmd != nil || m.pending != nil || m.busy || !m.detail || m.scroll != 7 || !reflect.DeepEqual(*m.detailEntry, captured) || len(m.services.Actions.(*fixtureActions).requests) != 0 {
					t.Fatal("cancel changed Details or dispatched")
				}
			})
		}
	}
}

func TestConfirmationMenuRestoresDetailsAndFreezesPreparedArguments(t *testing.T) {
	m := fixtureFleet()
	m.width, m.height = 40, 20
	m, _ = fleetKey(m, "tab")
	m.scroll = 3
	m, _ = fleetKey(m, "x")
	m, _ = fleetKey(m, "esc")
	if m.operations != nil || m.scroll != 3 || !m.detail {
		t.Fatal("operation menu lost original Details viewport")
	}
	prepares := 0
	var executed hostrouter.ActionRequest
	m.services.PrepareAction = func(r hostrouter.ActionRequest) (hostrouter.ActionRequest, error) {
		prepares++
		r.Args = append([]string{"--credentials=fixture-reference"}, r.Args...)
		return r, nil
	}
	m.services.Foreground = func(_ context.Context, r hostrouter.ActionRequest) (*exec.Cmd, error) {
		executed = r
		return exec.Command("true"), nil // Only capture; this model test does not run it.
	}
	m, _ = fleetKey(m, "x")
	m.operations.index = specIndex(t, "up")
	m, _ = fleetKey(m, "enter")
	m, _ = fleetKey(m, "enter")
	if m.pending == nil || m.pending.returnScroll != 3 || prepares != 1 {
		t.Fatal("menu confirmation lost return position or prepared references")
	}
	args := append([]string(nil), m.pending.args...)
	m.services.PrepareAction = func(r hostrouter.ActionRequest) (hostrouter.ActionRequest, error) {
		t.Fatal("dispatch reconstructed an already captured invocation")
		return r, nil
	}
	m = reviewFleetConfirmation(t, m)
	m, cmd := fleetKey(m, "y")
	if cmd == nil || m.scroll != 3 || !reflect.DeepEqual(executed.Args, args) || executed.Target != m.detailEntry.Target {
		t.Fatal("dispatch changed captured argv, target or return viewport")
	}
	m.actionCancel()
}

func TestConfirmationReconcileNeverRevivesInvalidatedIntent(t *testing.T) {
	for _, change := range []string{"name", "project", "target", "removed"} {
		t.Run(change, func(t *testing.T) {
			m := fixtureFleet()
			original := m.hosts[0].entries[0]
			m, _ = fleetKey(m, "tab")
			m, _ = fleetKey(m, "s")
			switch change {
			case "name":
				m.hosts[0].entries[0].Name = "replacement-name"
			case "project":
				m.hosts[0].entries[0].Project = "/replacement-project"
			case "target":
				m.hosts[0].entries[0].Target.ID = "replacement-target"
			case "removed":
				m.hosts[0].entries = nil
			}
			m.reconcile()
			if m.pending != nil || !m.detailInvalid || !strings.Contains(m.status, "action cancelled") {
				t.Fatal("reconcile retained changed intent")
			}
			m.hosts[0].entries = []hostrouter.InventoryEntry{original}
			m.reconcile()
			for _, key := range []string{"y", "s"} {
				var cmd tea.Cmd
				m, cmd = fleetKey(m, key)
				if cmd != nil || m.pending != nil || !m.detailInvalid || len(m.services.Actions.(*fixtureActions).requests) != 0 {
					t.Fatal("restored row revived invalidated intent")
				}
			}
		})
	}
}

func TestConfirmationDetailsMenuParityAndLiteralArguments(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, tc := range []struct{ key, name, consequence string }{
			{"u", "up", "may acquire configured credentials"},
			{"s", "down", "persistent state remains"},
			{"d", "destroy", "persistent state volumes"},
			{"c", "enter", "no automatic reconnect"},
		} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.Width, size.Height, tc.name), func(t *testing.T) {
				var previews [2][]string
				for i := range previews {
					m := fixtureFleet()
					m.width, m.height = size.Width, size.Height
					m.services.Foreground = func(context.Context, hostrouter.ActionRequest) (*exec.Cmd, error) {
						return nil, errors.New("unused fixture")
					}
					m.services.PrepareAction = func(r hostrouter.ActionRequest) (hostrouter.ActionRequest, error) {
						r.Args = append(r.Args, "--fixture=literal $(text); 'quoted'", "two words")
						return r, nil
					}
					m, _ = fleetKey(m, "tab")
					if i == 0 {
						m, _ = fleetKey(m, tc.key)
					} else {
						m, _ = fleetKey(m, "x")
						m.operations.index = specIndex(t, tc.name)
						m, _ = fleetKey(m, "enter")
						m, _ = fleetKey(m, "enter")
					}
					if m.pending == nil || m.busy {
						t.Fatal("missing consent")
					}
					previews[i], _, _ = m.content()
					all := strings.Join(previews[i], "")
					if !strings.Contains(all, tc.consequence) || !strings.Contains(all, fmt.Sprintf("%q", m.pending.args)) {
						t.Fatal("preview lost literal argv or consequence", all)
					}
				}
				if !reflect.DeepEqual(previews[0], previews[1]) {
					t.Fatal("Details and menu consent differ")
				}
			})
		}
	}
}

func TestConfirmationBackupSecretConsequenceAtEverySize(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m := fixtureFleet()
			m.width, m.height = size.Width, size.Height
			m.selected = m.hosts[1].entries[0].Target
			m.hosts[1].entries[0].State = "stopped"
			m, _ = fleetKey(m, "tab")
			m, _ = fleetKey(m, "x")
			m.operations.index = specIndex(t, "backup")
			m, _ = fleetKey(m, "enter")
			archive := "/fixture/archive with spaces; $(literal).tar"
			m, _ = fleetKey(m, archive)
			m, _ = fleetKey(m, "enter")
			if m.pending == nil {
				t.Fatal("backup skipped consent")
			}
			lines, _, _ := m.content()
			all := strings.Join(lines, "")
			want := []string{"--agent", "same", "backup", "--", archive}
			if !reflect.DeepEqual(m.pending.args, want) || !strings.Contains(all, fmt.Sprintf("%q", want)) || !strings.Contains(all, "Private but UNENCRYPTED archive includes guest secrets; host path only") {
				t.Fatal("backup consent hid arguments or secret consequence", all)
			}
			m = reviewFleetConfirmation(t, m)
			m, cmd := fleetKey(m, "y")
			m, cmd = startOperation(t, m, cmd)
			if done := cmd().(fleetActionDone); done.err != nil {
				t.Fatal(done.err)
			}
			requests := m.services.Actions.(*fixtureActions).requests
			if len(requests) != 1 || !reflect.DeepEqual(requests[0].Args, want) {
				t.Fatal("backup argv changed after consent")
			}
			m.actionCancel()
		})
	}
}

func TestConfirmationStaleRegistrationAndDeniedOutcome(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, mode := range []string{"registration", "denied"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.Width, size.Height, mode), func(t *testing.T) {
				m := fixtureFleet()
				m.width, m.height = size.Width, size.Height
				m.selected = m.hosts[1].entries[0].Target
				calls := 0
				m.services.Actions = actionFunc(func(_ context.Context, r hostrouter.ActionRequest, _ io.Reader, _, _ io.Writer) (hostrouter.ActionResult, error) {
					calls++
					return hostrouter.ActionResult{Target: r.Target}, errors.New("permission denied")
				})
				m, _ = fleetKey(m, "tab")
				m, _ = fleetKey(m, "s")
				m = reviewFleetConfirmation(t, m)
				if mode == "registration" {
					m.services.Registry.(*fixtureRegistry).hosts[0].Address = "replacement-alias"
				}
				m, cmd := fleetKey(m, "y")
				if mode == "registration" {
					if cmd != nil || m.pending != nil || m.busy || calls != 0 || !strings.Contains(m.status, "stale target") {
						t.Fatal("stale registration not refused", m.status)
					}
					return
				}
				if cmd == nil {
					t.Fatal("consented action not dispatched", m.status)
				}
				done := cmd().(fleetActionDone)
				if done.err == nil || calls != 1 {
					t.Fatal("denial lost or action retried")
				}
				out, _ := m.Update(done)
				m = out.(fleetModel)
				if m.busy || m.pending != nil || !strings.Contains(m.status, "failed: permission denied") || strings.Contains(m.status, "succeeded") || calls != 1 {
					t.Fatal("denied action reported success or revived consent", m.status)
				}
				m, cmd = fleetKey(m, "y")
				if cmd != nil || calls != 1 {
					t.Fatal("denied action retried without new intent")
				}
			})
		}
	}
}
