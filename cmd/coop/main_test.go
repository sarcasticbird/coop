package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/image"
	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
	"github.com/sarcasticbird/coop/internal/tui"
)

func TestRebuildPrintsCanonicalInputsAndPreservesContainerOnFailure(t *testing.T) {
	m := runtime.NewMock()
	withRuntime(t, m)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "coop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "coop", "coop.toml"), []byte("[tools]\npackages = [\"bat\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "coop.toml"), []byte("[tools]\npackages = [\"shellcheck\", \"actionlint\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	oldBuild := buildImage
	var gotArgs []string
	var packageInput string
	buildImage = func(args []string, _, _ io.Writer) error {
		gotArgs = slices.Clone(args)
		data, err := os.ReadFile(filepath.Join(args[len(args)-1], "configured-packages.txt"))
		if err != nil {
			t.Fatal(err)
		}
		packageInput = string(data)
		return errors.New("resolver failed")
	}
	t.Cleanup(func() { buildImage = oldBuild })

	var output bytes.Buffer
	cmd := root()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"rebuild"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "resolver failed") {
		t.Fatalf("build error = %v", err)
	}
	for _, want := range []string{
		"core tools:     26 packages",
		"global tools:   bat",
		"project tools:  actionlint, shellcheck",
		"image:          coop:local-",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("rebuild output missing %q:\n%s", want, output.String())
		}
	}
	if strings.Contains(strings.Join(gotArgs, " "), "EXTRA_PKGS") {
		t.Fatalf("build argv contains legacy shell package input: %v", gotArgs)
	}
	wantInput := image.NixpkgsRef + "#actionlint\n" + image.NixpkgsRef + "#bat\n" + image.NixpkgsRef + "#shellcheck\n"
	if packageInput != wantInput {
		t.Fatalf("configured package input = %q, want %q", packageInput, wantInput)
	}
	if len(m.Stopped) != 0 || len(m.Removed) != 0 {
		t.Fatalf("failed rebuild mutated container: stopped=%v removed=%v", m.Stopped, m.Removed)
	}
}

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

func TestCredentialsFlagAcceptsCommaAndRepeat(t *testing.T) {
	withRuntime(t, runtime.NewMock())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	old := runSession
	var got []string
	runSession = func(_ *session.Session, _ string, _ []string, credentials []string) error {
		got = slices.Clone(credentials)
		return nil
	}
	t.Cleanup(func() { runSession = old })

	cmd := root()
	cmd.SetArgs([]string{"--credentials", "aws-dev, github", "--credentials", "kubernetes"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := []string{"aws-dev", "github", "kubernetes"}
	if !slices.Equal(got, want) {
		t.Fatalf("credentials = %v, want %v", got, want)
	}
}

func TestCredentialsFlagRejectsExplicitEmptyValue(t *testing.T) {
	withRuntime(t, runtime.NewMock())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	cmd := root()
	cmd.SetArgs([]string{"--credentials="})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "empty grant name") {
		t.Fatalf("error = %v, want empty grant name rejection", err)
	}
}

func TestCredentialsAfterAgentRemainGuestArgv(t *testing.T) {
	withRuntime(t, runtime.NewMock())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	old := runSession
	var gotArgv, gotCredentials []string
	runSession = func(_ *session.Session, _ string, argv, credentials []string) error {
		gotArgv = slices.Clone(argv)
		gotCredentials = slices.Clone(credentials)
		return nil
	}
	t.Cleanup(func() { runSession = old })

	cmd := root()
	cmd.SetArgs([]string{"agent", "--credentials", "aws-dev"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotArgv, []string{"agent", "--credentials", "aws-dev"}) || len(gotCredentials) != 0 {
		t.Fatalf("argv=%v credentials=%v", gotArgv, gotCredentials)
	}
}

func TestCredentialsFlagPropagatesThroughTUIEntry(t *testing.T) {
	withRuntime(t, runtime.NewMock())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	oldTUI, oldSession := runTUI, runSession
	runTUI = func(runtime.Runtime) (tui.Result, error) {
		return tui.Result{EnterWorkdir: project}, nil
	}
	var got []string
	runSession = func(_ *session.Session, _ string, _ []string, credentials []string) error {
		got = slices.Clone(credentials)
		return nil
	}
	t.Cleanup(func() {
		runTUI = oldTUI
		runSession = oldSession
	})

	cmd := root()
	cmd.SetArgs([]string{"--credentials", "aws-dev,github", "tui"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"aws-dev", "github"}) {
		t.Fatalf("TUI credentials = %v", got)
	}
}

func TestCredentialsFlagRejectedByNonEntryCommands(t *testing.T) {
	withRuntime(t, runtime.NewMock())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	for _, subcommand := range []string{"up", "down", "status", "ls", "doctor", "rebuild", "destroy"} {
		t.Run(subcommand, func(t *testing.T) {
			cmd := root()
			cmd.SetArgs([]string{"--credentials", "token", subcommand})
			if err := cmd.Execute(); err == nil {
				t.Fatal("credential flag accepted by non-entry command")
			}
		})
	}
}

func TestEmptyCredentialsFlagRejectedByNonEntryCommand(t *testing.T) {
	cmd := root()
	cmd.SetArgs([]string{"--credentials=", "up"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "only valid when entering") {
		t.Fatalf("error = %v, want non-entry command rejection", err)
	}
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
	m.Images = map[string]bool{session.EffectiveImageName(config.Default()): true}
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
