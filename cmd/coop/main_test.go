package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/image"
	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/releasetool"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
	"github.com/sarcasticbird/coop/internal/tui"
)

func TestRebuildResolvesMaterializesAndLocksGitHubReleaseTools(t *testing.T) {
	m := runtime.NewMock()
	withRuntime(t, m)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_STATE_HOME", filepath.Join(xdg, "state"))
	if err := os.MkdirAll(filepath.Join(xdg, "coop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "coop", "coop.toml"), []byte(`
[[tools.github_release]]
name = "kata"
repo = "kenn-io/kata"
tag = "latest"
asset = "kata_{version}_linux_arm64.tar.gz"
binary = "kata"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	cached := filepath.Join(t.TempDir(), "kata")
	if err := os.WriteFile(cached, []byte("verified-kata"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := []config.ResolvedReleaseTool{{
		Name: "kata", Repo: "kenn-io/kata", RequestedTag: "latest", Tag: "v0.10.0",
		Asset: "kata_0.10.0_linux_arm64.tar.gz", Digest: "sha256:" + strings.Repeat("a", 64),
		Binary: "kata", CachePath: cached,
	}}

	oldResolve, oldSave, oldPrune, oldBuild := resolveReleaseTools, saveReleaseToolLock, pruneReleaseToolCache, buildImage
	resolveReleaseTools = func(context.Context, []config.GitHubReleaseTool) ([]config.ResolvedReleaseTool, error) {
		return resolved, nil
	}
	saved := false
	pruned := false
	saveReleaseToolLock = func(_ string, specs []config.GitHubReleaseTool, got []config.ResolvedReleaseTool) error {
		saved = len(specs) == 1 && slices.EqualFunc(got, resolved, func(a, b config.ResolvedReleaseTool) bool {
			return a.Name == b.Name && a.Digest == b.Digest
		})
		return nil
	}
	pruneReleaseToolCache = func(got []config.ResolvedReleaseTool) error {
		pruned = len(got) == 1 && got[0].Digest == resolved[0].Digest
		return nil
	}
	buildImage = func(args []string, _, _ io.Writer) error {
		archive, err := os.Open(filepath.Join(args[len(args)-1], "release-tools.tar.gz"))
		if err != nil {
			return err
		}
		defer archive.Close()
		gz, err := gzip.NewReader(archive)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		header, err := tr.Next()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		if header.Name != "kata" {
			return errors.New("wrong release executable name")
		}
		if string(data) != "verified-kata" {
			return errors.New("wrong release executable")
		}
		return nil
	}
	t.Cleanup(func() {
		resolveReleaseTools, saveReleaseToolLock, pruneReleaseToolCache, buildImage = oldResolve, oldSave, oldPrune, oldBuild
	})

	var output bytes.Buffer
	cmd := root()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"rebuild"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !saved {
		t.Fatal("successful rebuild did not save resolved release lock")
	}
	if !pruned {
		t.Fatal("successful rebuild did not prune stale release cache entries")
	}
	if !strings.Contains(output.String(), "release tools:  kata@v0.10.0") {
		t.Fatalf("rebuild output missing resolved release: %s", output.String())
	}
}

func TestRebuildDoesNotSaveReleaseLockWhenImageBuildFails(t *testing.T) {
	withRuntime(t, runtime.NewMock())
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_STATE_HOME", filepath.Join(xdg, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(xdg, "cache"))
	if err := os.MkdirAll(filepath.Join(xdg, "coop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "coop", "coop.toml"), []byte(`
[[tools.github_release]]
name = "kata"
repo = "kenn-io/kata"
tag = "latest"
asset = "kata_{version}_linux_arm64.tar.gz"
binary = "kata"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	cfg, err := config.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	oldResolved := []config.ResolvedReleaseTool{{
		Name: "kata", Repo: "kenn-io/kata", RequestedTag: "latest", Tag: "v0.9.0",
		Asset:  "kata_0.9.0_linux_arm64.tar.gz",
		Digest: "sha256:" + strings.Repeat("9", 64), Binary: "kata",
	}}
	stateDir := filepath.Join(xdg, "state", "coop")
	if err := releasetool.SaveLock(stateDir, cfg.Tools.GitHubReleases, oldResolved); err != nil {
		t.Fatal(err)
	}
	cached := filepath.Join(t.TempDir(), "kata")
	if err := os.WriteFile(cached, []byte("new-kata"), 0o755); err != nil {
		t.Fatal(err)
	}
	newResolved := []config.ResolvedReleaseTool{{
		Name: "kata", Repo: "kenn-io/kata", RequestedTag: "latest", Tag: "v0.10.0",
		Asset:  "kata_0.10.0_linux_arm64.tar.gz",
		Digest: "sha256:" + strings.Repeat("a", 64), Binary: "kata", CachePath: cached,
	}}

	oldResolve, oldSave, oldPrune, oldBuild := resolveReleaseTools, saveReleaseToolLock, pruneReleaseToolCache, buildImage
	resolveReleaseTools = func(context.Context, []config.GitHubReleaseTool) ([]config.ResolvedReleaseTool, error) {
		return newResolved, nil
	}
	saveReleaseToolLock = func(string, []config.GitHubReleaseTool, []config.ResolvedReleaseTool) error {
		t.Fatal("failed build saved release lock")
		return nil
	}
	pruneReleaseToolCache = func([]config.ResolvedReleaseTool) error {
		t.Fatal("failed build pruned release cache")
		return nil
	}
	buildImage = func([]string, io.Writer, io.Writer) error { return errors.New("build failed") }
	t.Cleanup(func() {
		resolveReleaseTools, saveReleaseToolLock, pruneReleaseToolCache, buildImage = oldResolve, oldSave, oldPrune, oldBuild
	})

	cmd := root()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"rebuild"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "build failed") {
		t.Fatalf("build failure = %v", err)
	}
	got, err := releasetool.LoadLock(stateDir, cfg.Tools.GitHubReleases)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Tag != "v0.9.0" {
		t.Fatalf("failed build replaced previous lock: %+v", got)
	}
}

func TestRebuildWithoutReleaseToolsPersistsEmptyStateAndPrunesCache(t *testing.T) {
	withRuntime(t, runtime.NewMock())
	emptyProject(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	oldSave, oldPrune, oldBuild := saveReleaseToolLock, pruneReleaseToolCache, buildImage
	saved, pruned := false, false
	saveReleaseToolLock = func(_ string, specs []config.GitHubReleaseTool, resolved []config.ResolvedReleaseTool) error {
		saved = len(specs) == 0 && len(resolved) == 0
		return nil
	}
	pruneReleaseToolCache = func(resolved []config.ResolvedReleaseTool) error {
		pruned = len(resolved) == 0
		return nil
	}
	buildImage = func([]string, io.Writer, io.Writer) error { return nil }
	t.Cleanup(func() {
		saveReleaseToolLock, pruneReleaseToolCache, buildImage = oldSave, oldPrune, oldBuild
	})

	cmd := root()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"rebuild"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !saved || !pruned {
		t.Fatalf("release-free rebuild cleanup: saved=%t pruned=%t", saved, pruned)
	}
}

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
	wantBuildError := "build image \"" + gotArgs[2] + "\": resolver failed"
	if err == nil || !strings.Contains(err.Error(), wantBuildError) {
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

func TestBuildImageWrapsCommandError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := buildImage([]string{"build"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "run container image build") {
		t.Fatalf("build error = %v", err)
	}
}

func TestStatusReportsDesiredRunningAndPendingState(t *testing.T) {
	m := runtime.NewMock()
	withRuntime(t, m)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	s, err := session.New(m, project)
	if err != nil {
		t.Fatal(err)
	}
	m.Existing[s.Name] = true
	m.Started = append(m.Started, s.Name)
	m.Images = map[string]bool{session.EffectiveImageName(s.Cfg): true}
	m.ContainerImages = map[string]string{s.Name: "coop:local-old"}
	m.ContainerLabels = map[string]map[string]string{s.Name: {session.SpecLabel: "old-spec"}}

	var output bytes.Buffer
	cmd := root()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"state:              running",
		"running image:      coop:local-old",
		"desired image:      " + session.EffectiveImageName(s.Cfg),
		"rebuild required:   no",
		"recreation pending: yes",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("status output missing %q:\n%s", want, output.String())
		}
	}
}

func TestLegacyToolAliasWarnsOnce(t *testing.T) {
	m := runtime.NewMock()
	withRuntime(t, m)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "coop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "coop", "coop.toml"), []byte("[image]\nextra_packages = [\"hello\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	oldWarnings := warningOutput
	var warnings bytes.Buffer
	warningOutput = &warnings
	t.Cleanup(func() { warningOutput = oldWarnings })
	cmd := root()
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(warnings.String(), "image.extra_packages is deprecated"); got != 1 {
		t.Fatalf("deprecation warning count = %d:\n%s", got, warnings.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("sink failed") }

func emptyProject(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
}

func TestStatusPropagatesOutputWriteFailure(t *testing.T) {
	withRuntime(t, runtime.NewMock())
	emptyProject(t)

	cmd := root()
	cmd.SetOut(failingWriter{})
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"status"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "write status") {
		t.Fatalf("status write failure not propagated: %v", err)
	}
}

func TestRebuildPropagatesOutputWriteFailure(t *testing.T) {
	withRuntime(t, runtime.NewMock())
	emptyProject(t)

	cmd := root()
	cmd.SetOut(failingWriter{})
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"rebuild"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "write rebuild summary") {
		t.Fatalf("rebuild write failure not propagated: %v", err)
	}
}

func TestConfigWarningWriteFailureDoesNotBlockCommand(t *testing.T) {
	withRuntime(t, runtime.NewMock())
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "coop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "coop", "coop.toml"), []byte("[image]\nextra_packages = [\"hello\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	oldWarnings := warningOutput
	warningOutput = failingWriter{}
	t.Cleanup(func() { warningOutput = oldWarnings })
	cmd := root()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("advisory warning blocked command: %v", err)
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

func TestTUIEntryEmitsConfigWarnings(t *testing.T) {
	withRuntime(t, runtime.NewMock())
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "coop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "coop", "coop.toml"), []byte("[image]\nextra_packages = [\"hello\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	oldTUI, oldSession, oldWarnings := runTUI, runSession, warningOutput
	runTUI = func(runtime.Runtime) (tui.Result, error) {
		return tui.Result{EnterWorkdir: project}, nil
	}
	runSession = func(*session.Session, string, []string, []string) error { return nil }
	var warnings bytes.Buffer
	warningOutput = &warnings
	t.Cleanup(func() {
		runTUI = oldTUI
		runSession = oldSession
		warningOutput = oldWarnings
	})

	cmd := root()
	cmd.SetArgs([]string{"tui"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(warnings.String(), "image.extra_packages is deprecated"); got != 1 {
		t.Fatalf("TUI deprecation warning count = %d:\n%s", got, warnings.String())
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

func TestRootHelpDescribesProjectFloxAsOptional(t *testing.T) {
	help := root().Long
	if !strings.Contains(help, "optional project flox") {
		t.Fatalf("root help does not describe project flox as optional:\n%s", help)
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

func TestDoctorUsesLockedGitHubReleaseToolImageIdentity(t *testing.T) {
	m := runtime.NewMock()
	withRuntime(t, m)
	xdgConfig := t.TempDir()
	xdgState := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_STATE_HOME", xdgState)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	global := filepath.Join(xdgConfig, "coop", "coop.toml")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`
[[tools.github_release]]
name = "kata"
repo = "kenn-io/kata"
tag = "latest"
asset = "kata_{version}_linux_arm64.tar.gz"
binary = "kata"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	resolved := []config.ResolvedReleaseTool{{
		Name: "kata", Repo: "kenn-io/kata", RequestedTag: "latest", Tag: "v0.12.1",
		Asset:  "kata_0.12.1_linux_arm64.tar.gz",
		Digest: "sha256:" + strings.Repeat("a", 64), Binary: "kata",
	}}
	if err := releasetool.SaveLock(filepath.Join(xdgState, "coop"), cfg.Tools.GitHubReleases, resolved); err != nil {
		t.Fatal(err)
	}
	cfg.Tools.ResolvedReleases = resolved
	m.Images = map[string]bool{session.EffectiveImageName(cfg): true}

	cmd := root()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"doctor"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor ignored matching release-tool lock: %v", err)
	}
}
