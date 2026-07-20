package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
)

func withRuntime(t *testing.T, rt runtime.Runtime) {
	t.Helper()
	oldRuntime, oldLookPath := newRuntime, lookPath
	newRuntime = func() runtime.Runtime { return rt }
	lookPath = func(string) (string, error) { return "/bin/container", nil }
	t.Cleanup(func() {
		newRuntime = oldRuntime
		lookPath = oldLookPath
	})
}

func TestRootSilencesCobraErrorAndUsageOutput(t *testing.T) {
	cmd := root()
	if !cmd.SilenceErrors || !cmd.SilenceUsage {
		t.Fatalf("root must leave error rendering to main: errors=%v usage=%v", cmd.SilenceErrors, cmd.SilenceUsage)
	}
}

func TestResolvedVersionHonorsReleaseOverride(t *testing.T) {
	old := version
	version = "v0.1.0-beta.1"
	t.Cleanup(func() { version = old })
	if got := resolvedVersion(); got != version {
		t.Fatalf("resolvedVersion() = %q, want %q", got, version)
	}
}

func TestListDoesNotLoadCurrentProject(t *testing.T) {
	withRuntime(t, runtime.NewMock())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "coop.toml"), []byte("not valid toml ="), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	cmd := root()
	cmd.SetArgs([]string{"ls"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ls depended on current project config/root: %v", err)
	}
}

func TestDoctorLoadsGlobalConfigFromHome(t *testing.T) {
	m := runtime.NewMock()
	m.Images = map[string]bool{session.EffectiveImageName(config.Default().Image): true}
	withRuntime(t, m)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(home, "coop.toml"), []byte("not valid toml ="), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(home)
	cmd := root()
	cmd.SetArgs([]string{"doctor"})
	if err := cmd.Execute(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		t.Fatalf("doctor depended on current project config/root: %v", err)
	}
}
