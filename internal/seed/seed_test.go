package seed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
)

const (
	hostHome  = "/host/home"
	guestHome = "/Users/u"
)

func writeTemp(t *testing.T, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAlwaysSeedsAndPreservesMode(t *testing.T) {
	m := runtime.NewMock()
	src := writeTemp(t, "wt", "#!/bin/sh\n", 0o755)

	seeds := []config.Seed{{Src: src, Dest: "/usr/local/bin/wt", Policy: config.PolicyAlways}}
	if err := Apply(m, "c", hostHome, guestHome, seeds); err != nil {
		t.Fatal(err)
	}
	if m.GuestFiles["/usr/local/bin/wt"] != "#!/bin/sh\n" {
		t.Errorf("content missing:\n%s", m.ExecString())
	}
	if m.GuestModes["/usr/local/bin/wt"] != "755" {
		t.Errorf("mode not preserved: %v", m.GuestModes)
	}
	// the write must be atomic (exclusive random temp + no-target-dir
	// rename) and refuse symlink/non-regular destinations
	joined := m.ExecString()
	for _, marker := range []string{"mktemp", "mv -T", `-L "$d"`, `! -f "$d"`} {
		if !strings.Contains(joined, marker) {
			t.Errorf("hardened write missing %q:\n%s", marker, joined)
		}
	}
}

func TestIfAbsentFailsClosedOnCheckError(t *testing.T) {
	m := runtime.NewMock()
	m.ExistsErr = errCheck
	src := writeTemp(t, "auth.json", `{"t":"x"}`, 0o600)

	seeds := []config.Seed{{Src: src, Dest: "~/auth.json", Policy: config.PolicyIfAbsent}}
	err := Apply(m, "c", hostHome, guestHome, seeds)
	if err == nil {
		t.Fatal("inconclusive existence check must fail closed")
	}
	if _, wrote := m.GuestFiles["/Users/u/auth.json"]; wrote {
		t.Errorf("wrote despite inconclusive check")
	}
}

var errCheck = os.ErrPermission

func TestIfAbsentSkipsExistingGuestFile(t *testing.T) {
	m := runtime.NewMock()
	m.GuestFiles["/Users/u/.claude/settings.json"] = `{"model":"guest-edited"}`
	src := writeTemp(t, "settings.json", `{"model":"host"}`, 0o644)

	seeds := []config.Seed{{Src: src, Dest: "~/.claude/settings.json", Policy: config.PolicyIfAbsent}}
	if err := Apply(m, "c", hostHome, guestHome, seeds); err != nil {
		t.Fatal(err)
	}
	if got := m.GuestFiles["/Users/u/.claude/settings.json"]; got != `{"model":"guest-edited"}` {
		t.Errorf("if-absent clobbered guest file: %q", got)
	}
}

func TestMissingSourceIsSkippedSilently(t *testing.T) {
	m := runtime.NewMock()
	seeds := []config.Seed{{Src: "/nonexistent", Dest: "/x", Policy: config.PolicyAlways}}
	if err := Apply(m, "c", hostHome, guestHome, seeds); err != nil {
		t.Fatal(err)
	}
	if len(m.ExecCalls) != 0 {
		t.Errorf("missing source should not exec:\n%s", m.ExecString())
	}
}

func TestOverlayPipesTar(t *testing.T) {
	m := runtime.NewMock()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "skills", "helper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "helper", "SKILL.md"), []byte("# helper"), 0o644); err != nil {
		t.Fatal(err)
	}

	seeds := []config.Seed{{Src: filepath.Join(dir, "skills"), Dest: "~/.claude/skills", Policy: config.PolicyOverlay}}
	if err := Apply(m, "c", hostHome, guestHome, seeds); err != nil {
		t.Fatal(err)
	}

	var tarCall *runtime.ExecCall
	for i := range m.ExecCalls {
		if strings.HasPrefix(strings.Join(m.ExecCalls[i].Argv, " "), "tar -xf") {
			tarCall = &m.ExecCalls[i]
		}
	}
	if tarCall == nil {
		t.Fatalf("no tar extract call:\n%s", m.ExecString())
	}
	if !strings.Contains(strings.Join(tarCall.Argv, " "), "/Users/u/.claude/skills") {
		t.Errorf("wrong extract dest: %v", tarCall.Argv)
	}
	if !strings.Contains(tarCall.Stdin, "SKILL.md") {
		t.Errorf("tar stream missing content (len=%d)", len(tarCall.Stdin))
	}
}

func TestHomeExpansionUsesRespectiveSides(t *testing.T) {
	m := runtime.NewMock()
	// src ~ expands against HOST home, dest ~ against GUEST home
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "f")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	seeds := []config.Seed{{Src: src, Dest: "~/f", Policy: config.PolicyAlways}}
	if err := Apply(m, "c", hostHome, guestHome, seeds); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.GuestFiles["/Users/u/f"]; !ok {
		t.Errorf("dest not expanded against guest home:\n%s", m.ExecString())
	}
}
