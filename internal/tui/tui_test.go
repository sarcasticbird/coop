package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sarcasticbird/coop/internal/project"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func longProjectModel(width int) model {
	return model{
		width:  width,
		result: &Result{},
		infos: []runtime.ContainerInfo{{
			// realistic generated name: unbounded slug + 16-hex hash
			Name:    project.Name("/Users/u/Projects/org/a-truly-remarkably-long-project-directory-name"),
			State:   "running",
			IP:      "192.168.64.24",
			CPUs:    4,
			Memory:  8 << 30,
			Started: time.Now().Add(-90 * time.Minute),
			Mounts: []runtime.MountInfo{{
				Source:      "/Users/u/Projects/org/a-truly-remarkably-long-project-directory-name/main",
				Destination: "/Users/u/Projects/org/a-truly-remarkably-long-project-directory-name/main",
				Bind:        true,
			}},
		}},
	}
}

func maxLineWidth(view string) int {
	widest := 0
	for _, line := range strings.Split(ansi.ReplaceAllString(view, ""), "\n") {
		if n := len([]rune(line)); n > widest {
			widest = n
		}
	}
	return widest
}

func TestViewFitsTerminalWidth(t *testing.T) {
	for _, width := range []int{100, 110, 140} {
		if got := maxLineWidth(longProjectModel(width).View()); got > width {
			t.Errorf("width %d: rendered %d columns (row wrap)", width, got)
		}
	}
}

func TestLongSameBasenameNamesRenderDistinguishably(t *testing.T) {
	a := project.Name("/Users/u/work/client/an-extremely-long-shared-service-name")
	b := project.Name("/Users/u/work/internal/an-extremely-long-shared-service-name")
	ra, rb := truncateMiddle(a, 30), truncateMiddle(b, 30)
	if ra == rb {
		t.Errorf("truncation erased identity: %q vs %q", ra, rb)
	}
	if len([]rune(ra)) > 30 {
		t.Errorf("truncated name exceeds column: %q", ra)
	}
}

func TestBusyGatesActionsAndTicks(t *testing.T) {
	m := longProjectModel(120)
	m.infos[0].State = "stopped"
	m.busy = true

	// action keys must be ignored while an action is in flight
	for _, k := range []string{"u", "s", "d", "r"} {
		out, cmd := m.key(keyMsg(k))
		got := out.(model)
		if cmd != nil || got.confirmName != "" {
			t.Errorf("key %q acted while busy", k)
		}
	}
	// ticks reschedule but must not refresh (no stacked subprocesses)
	_, cmd := m.Update(tickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("tick must reschedule")
	}
	// a background refresh completing must NOT clear the busy gate —
	// only the action's own completion may
	out, _ := m.Update(refreshMsg{infos: m.infos, gen: 0})
	if got := out.(model); !got.busy {
		t.Fatal("refresh cleared busy while action in flight")
	}
	out, _ = out.(model).Update(actionDoneMsg{infos: m.infos})
	if got := out.(model); got.busy {
		t.Fatal("action completion should clear busy")
	}
	// action errors surface; a later successful refresh clears them
	out, _ = out.(model).Update(actionDoneMsg{err: errStale})
	if got := out.(model); got.err == nil {
		t.Fatal("action error swallowed")
	}
	out, _ = out.(model).Update(refreshMsg{infos: m.infos, gen: out.(model).gen})
	if got := out.(model); got.err != nil {
		t.Fatal("stale error not cleared by successful refresh")
	}
}

func TestStaleRefreshCannotOverwriteActionOutcome(t *testing.T) {
	m := longProjectModel(120)
	staleGen := m.gen
	staleInfos := m.infos

	// an action starts (gen bumps) and completes with fresh results
	m.busy = true
	m.gen++
	out, _ := m.Update(actionDoneMsg{infos: nil}) // fleet now empty
	got := out.(model)
	if len(got.infos) != 0 {
		t.Fatal("setup failed")
	}

	// a refresh launched BEFORE the action now lands: must be dropped
	out, _ = got.Update(refreshMsg{infos: staleInfos, gen: staleGen})
	if got := out.(model); len(got.infos) != 0 {
		t.Fatal("stale refresh overwrote action outcome")
	}

	// stale refresh errors must not overwrite either
	out, _ = out.(model).Update(actionDoneMsg{err: errStale})
	out, _ = out.(model).Update(refreshMsg{infos: staleInfos, gen: staleGen})
	if got := out.(model); got.err == nil {
		t.Fatal("stale refresh cleared an action error")
	}
}

func TestEnterGatedWhileBusy(t *testing.T) {
	m := longProjectModel(120)
	m.busy = true
	out, cmd := m.key(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || out.(model).result.EnterName != "" {
		t.Fatal("enter acted on possibly-stale state while busy")
	}
}

func TestUpUsesSessionLifecycleForSelectedProject(t *testing.T) {
	projectPath, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(src, []byte("seeded"), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(xdg, "coop", "coop.toml")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	globalConfig := fmt.Sprintf("[[seed]]\nsrc = %q\ndest = \"~/.config/tui-test/settings.json\"\npolicy = \"always\"\n", src)
	if err := os.WriteFile(global, []byte(globalConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := runtime.NewMock()
	s, err := session.New(rt, projectPath)
	if err != nil {
		t.Fatal(err)
	}
	rt.Images = map[string]bool{session.EffectiveImageName(s.Cfg): true}
	rt.Existing[s.Name] = true // unlabeled: Session.Up must reconcile it
	m := model{rt: rt, result: &Result{}, infos: []runtime.ContainerInfo{{
		Name: s.Name, State: "stopped", Mounts: []runtime.MountInfo{{
			Source: projectPath, Destination: projectPath, Bind: true,
		}},
	}}}
	out, cmd := m.key(keyMsg("u"))
	if cmd == nil || !out.(model).busy {
		t.Fatal("u did not launch a lifecycle action")
	}
	msg := cmd()
	done, ok := msg.(actionDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("u action failed: %#v", msg)
	}
	if len(rt.Removed) != 1 || len(rt.Run_) != 1 {
		t.Fatalf("u bypassed spec reconciliation: removed=%v runs=%d starts=%v", rt.Removed, len(rt.Run_), rt.Started)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.GuestFiles[filepath.Join(home, ".config", "tui-test", "settings.json")]; got != "seeded" {
		t.Fatalf("u bypassed seed application: got %q", got)
	}
}

func TestUpRequiresSelectedProjectMount(t *testing.T) {
	rt := runtime.NewMock()
	m := model{rt: rt, result: &Result{}, infos: []runtime.ContainerInfo{{Name: "coop-x", State: "stopped"}}}
	out, cmd := m.key(keyMsg("u"))
	got := out.(model)
	if cmd != nil || got.err == nil || len(rt.Started) != 0 {
		t.Fatalf("u started without a project path: cmd=%v err=%v starts=%v", cmd != nil, got.err, rt.Started)
	}
}

func TestDestroyUsesCurrentSessionIdentity(t *testing.T) {
	projectPath, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	rt := runtime.NewMock()
	s, err := session.New(rt, projectPath)
	if err != nil {
		t.Fatal(err)
	}
	rt.Existing[s.Name] = true
	rt.Volumes[s.Name+project.VolumeSep+"claude"] = true
	m := model{rt: rt, result: &Result{}, infos: []runtime.ContainerInfo{{
		Name: s.Name, State: "stopped", Mounts: []runtime.MountInfo{{
			Source: projectPath, Destination: projectPath, Bind: true,
		}},
	}}}

	out, cmd := m.key(keyMsg("d"))
	if cmd != nil || out.(model).confirmProject != projectPath {
		t.Fatal("destroy did not capture the selected project identity")
	}
	out, cmd = out.(model).key(keyMsg("y"))
	if cmd == nil || !out.(model).busy {
		t.Fatal("destroy confirmation did not launch an action")
	}
	done, ok := cmd().(actionDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("destroy action failed: %#v", done)
	}
	if len(rt.Removed) != 1 || len(rt.RemovedVol) != 1 {
		t.Fatalf("destroy did not remove current session state: containers=%v volumes=%v", rt.Removed, rt.RemovedVol)
	}
}

func TestDestroyRefusesLegacyContainerIdentity(t *testing.T) {
	projectPath, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	rt := runtime.NewMock()
	rt.Existing["coop-legacy"] = true
	m := model{rt: rt, result: &Result{}, infos: []runtime.ContainerInfo{{
		Name: "coop-legacy", State: "stopped", Mounts: []runtime.MountInfo{{
			Source: projectPath, Destination: projectPath, Bind: true,
		}},
	}}}
	out, _ := m.key(keyMsg("d"))
	_, cmd := out.(model).key(keyMsg("y"))
	done, ok := cmd().(actionDoneMsg)
	if !ok || done.err == nil {
		t.Fatalf("legacy identity was not rejected: %#v", done)
	}
	if len(rt.Removed) != 0 {
		t.Fatalf("legacy container was removed: %v", rt.Removed)
	}
}

func TestTickDoesNotStackRefreshes(t *testing.T) {
	m := longProjectModel(120)
	out, cmd := m.Update(tickMsg(time.Now()))
	if cmd == nil || !out.(model).refreshing {
		t.Fatal("first tick should start a refresh (returned model must carry the flag)")
	}
	// second tick while refresh in flight: reschedule only
	out2, _ := out.(model).Update(tickMsg(time.Now()))
	if !out2.(model).refreshing {
		t.Fatal("refreshing flag lost")
	}
	// manual refresh must also carry the flag in the returned model
	m2 := longProjectModel(120)
	out3, cmd3 := m2.key(keyMsg("r"))
	if cmd3 == nil || !out3.(model).refreshing {
		t.Fatal("manual refresh did not mark in-flight on returned model")
	}
}

func TestEnterGatedByStaleListing(t *testing.T) {
	m := longProjectModel(120)
	// action fails -> listing marked non-authoritative
	out, _ := m.Update(actionDoneMsg{err: errStale})
	got := out.(model)
	if !got.stale {
		t.Fatal("action error should mark listing stale")
	}
	_, cmd := got.key(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || got.result.EnterName != "" {
		t.Fatal("enter acted on non-authoritative listing")
	}
	// a successful current-generation refresh restores authority
	out, _ = got.Update(refreshMsg{infos: got.infos, gen: got.gen})
	if out.(model).stale {
		t.Fatal("successful refresh should clear staleness")
	}
}

var errStale = fmt.Errorf("stale")

func keyMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestViewDropsProjectColumnWhenNarrow(t *testing.T) {
	view := ansi.ReplaceAllString(longProjectModel(90).View(), "")
	if strings.Contains(view, "PROJECT") {
		t.Errorf("narrow terminal should omit PROJECT column")
	}
}
