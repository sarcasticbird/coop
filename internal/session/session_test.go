package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/image"
	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/core"
	"github.com/sarcasticbird/coop/internal/credential"
	"github.com/sarcasticbird/coop/internal/project"
	"github.com/sarcasticbird/coop/internal/releasetool"
	"github.com/sarcasticbird/coop/internal/runtime"
)

func testSession(t *testing.T, m *runtime.Mock) *Session {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // isolate per-project locks
	s := &Session{
		RT:      m,
		Project: "/Users/u/Projects/proj",
		Name:    "coop-proj",
		Cfg: config.Config{
			Image:     config.Image{Name: "coop:latest"},
			Resources: config.Resources{CPUs: 4, Memory: "8G"},
			Agents: map[string]config.Agent{
				"opencode": {State: "~/.local/share/opencode"},
				"claude":   {State: "~/.claude"},
				"codex":    {State: "~/.codex"},
			},
		},
		HostHome:          "/Users/u",
		GuestHome:         "/Users/u",
		CredentialManager: credential.NewManager(t.TempDir()),
		revokeCredentials: credential.RevokeAll,
	}
	m.Images = map[string]bool{EffectiveImageName(s.Cfg): true}
	return s
}

func TestNewLoadsMatchingGitHubReleaseToolLockWithoutNetwork(t *testing.T) {
	home := t.TempDir()
	xdgConfig := t.TempDir()
	xdgState := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_STATE_HOME", xdgState)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
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
	cfg, err := config.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	resolved := []config.ResolvedReleaseTool{{
		Name: "kata", Repo: "kenn-io/kata", RequestedTag: "latest", Tag: "v0.10.0",
		Asset: "kata_0.10.0_linux_arm64.tar.gz", URL: "https://github.com/example",
		Digest: "sha256:" + strings.Repeat("a", 64), Binary: "kata",
	}}
	if err := releasetool.SaveLock(filepath.Join(xdgState, "coop"), project, cfg.Tools.GitHubReleases, resolved); err != nil {
		t.Fatal(err)
	}

	s, err := New(runtime.NewMock(), project)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Cfg.Tools.ResolvedReleases) != 1 || s.Cfg.Tools.ResolvedReleases[0].Tag != "v0.10.0" {
		t.Fatalf("resolved releases = %+v", s.Cfg.Tools.ResolvedReleases)
	}
}

func TestNewTreatsMalformedGitHubReleaseToolLockAsUnresolved(t *testing.T) {
	home := t.TempDir()
	xdgConfig := t.TempDir()
	xdgState := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_STATE_HOME", xdgState)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
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
	lockPath := filepath.Join(
		xdgState,
		"coop",
		"release-tools",
		config.ReleaseSpecFingerprint([]config.GitHubReleaseTool{{
			Name: "kata", Repo: "kenn-io/kata", Tag: "latest",
			Asset: "kata_{version}_linux_arm64.tar.gz", Binary: "kata",
		}})+".lock",
	)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := New(runtime.NewMock(), project)
	if err != nil {
		t.Fatalf("derived release lock blocked recovery: %v", err)
	}
	if len(s.Cfg.Tools.ResolvedReleases) != 0 {
		t.Fatalf("malformed lock supplied resolved tools: %+v", s.Cfg.Tools.ResolvedReleases)
	}
	if len(s.Cfg.Warnings) != 1 || !strings.Contains(s.Cfg.Warnings[0], "run `coop rebuild`") {
		t.Fatalf("malformed lock warning = %v", s.Cfg.Warnings)
	}
}

func TestRunAcquiresCredentialsBeforeUp(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Cfg.Credentials = map[string]config.Credential{
		"token": {
			Source: config.CredentialSource{Type: "command", Argv: []string{"token-helper"}},
			Inject: config.CredentialInjection{Type: "environment", Name: "TOKEN"},
		},
	}
	s.CredentialManager.Run = func(context.Context, string, []string) ([]byte, error) {
		if len(m.Run_) != 0 || len(m.Started) != 0 || len(m.ExecCalls) != 0 {
			t.Fatal("runtime mutated before credential acquisition completed")
		}
		return []byte("secret"), nil
	}

	if err := s.Run(s.Project, []string{"agent"}, []string{"token"}); err != nil {
		t.Fatal(err)
	}
	if len(m.Run_) != 1 || len(m.Interactive) != 1 {
		t.Fatalf("entry did not proceed after acquisition: runs=%d interactive=%d", len(m.Run_), len(m.Interactive))
	}
}

func TestRunAcquiresDefaultCredentialBeforeUp(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Cfg.Credentials = map[string]config.Credential{
		"token": {
			Source: config.CredentialSource{Type: "command", Argv: []string{"token-helper"}},
			Inject: config.CredentialInjection{Type: "environment", Name: "TOKEN"},
		},
	}
	s.Cfg.IncludeCredentials = []string{"token"}
	s.CredentialManager.Run = func(context.Context, string, []string) ([]byte, error) {
		if len(m.Run_) != 0 || len(m.Started) != 0 || len(m.ExecCalls) != 0 {
			t.Fatal("runtime mutated before default credential acquisition completed")
		}
		return []byte("secret"), nil
	}

	if err := s.Run(s.Project, []string{"agent"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.Run_) != 1 || len(m.Interactive) != 1 {
		t.Fatalf("entry did not proceed after default acquisition: runs=%d interactive=%d", len(m.Run_), len(m.Interactive))
	}
}

func TestRunCleansLeaseWhenStageReportsFailure(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	markRunning(m, s)
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Credentials = map[string]config.Credential{
		"token": {
			Source: config.CredentialSource{Type: "file", Path: secret},
			Inject: config.CredentialInjection{Type: "environment", Name: "TOKEN"},
		},
	}
	stageErr := errors.New("stage transport failed")
	m.ExecErrors = []error{nil, stageErr}

	err := s.Run(s.Project, []string{"agent"}, []string{"token"})
	if !errors.Is(err, stageErr) {
		t.Fatalf("stage error missing: %v", err)
	}
	if len(m.Interactive) != 0 {
		t.Fatal("guest command ran after staging failure")
	}
	if len(m.ExecCalls) != 3 || len(m.ExecCalls[2].Argv) != 5 ||
		m.ExecCalls[2].Argv[0] != "rm" || !strings.HasSuffix(m.ExecCalls[2].Argv[4], ".staging") {
		t.Fatalf("staging failure cleanup missing: %#v", m.ExecCalls)
	}
}

func TestRunAcquisitionFailureDoesNotMutateRuntime(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Cfg.Credentials = map[string]config.Credential{
		"token": {
			Source: config.CredentialSource{Type: "command", Argv: []string{"token-helper"}},
			Inject: config.CredentialInjection{Type: "environment", Name: "TOKEN"},
		},
	}
	s.CredentialManager.Run = func(context.Context, string, []string) ([]byte, error) {
		return nil, errors.New("helper unavailable")
	}

	if err := s.Run(s.Project, []string{"agent"}, []string{"token"}); err == nil {
		t.Fatal("acquisition failure was ignored")
	}
	if len(m.Run_) != 0 || len(m.Started) != 0 || len(m.ExecCalls) != 0 || len(m.Interactive) != 0 {
		t.Fatalf("runtime mutated after acquisition failure: runs=%d started=%v exec=%d interactive=%d",
			len(m.Run_), m.Started, len(m.ExecCalls), len(m.Interactive))
	}
}

func TestRunSignalDuringAcquisitionUnwindsNormally(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Cfg.Credentials = map[string]config.Credential{
		"token": {
			Source: config.CredentialSource{Type: "command", Argv: []string{"token-helper"}},
			Inject: config.CredentialInjection{Type: "environment", Name: "TOKEN"},
		},
	}
	s.CredentialManager.Run = func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			return nil, err
		}
		<-ctx.Done()
		return nil, context.Cause(ctx)
	}

	err := s.Run(s.Project, []string{"agent"}, []string{"token"})
	var exitCoder interface{ ExitCode() int }
	if !errors.As(err, &exitCoder) || exitCoder.ExitCode() != 143 {
		t.Fatalf("signal error = %v", err)
	}
	if len(m.Run_) != 0 || len(m.Started) != 0 || len(m.ExecCalls) != 0 || len(m.Interactive) != 0 {
		t.Fatal("runtime mutated after acquisition was interrupted")
	}
}

func TestRunSignalDuringInteractiveEntryCleansLease(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	markRunning(m, s)
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Credentials = map[string]config.Credential{
		"token": {
			Source: config.CredentialSource{Type: "file", Path: secret},
			Inject: config.CredentialInjection{Type: "environment", Name: "TOKEN"},
		},
	}
	m.InteractiveFunc = func(ctx context.Context, _, _ string, _ []string) error {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			return err
		}
		<-ctx.Done()
		return context.Cause(ctx)
	}

	err := s.Run(s.Project, []string{"agent"}, []string{"token"})
	var exitCoder interface{ ExitCode() int }
	if !errors.As(err, &exitCoder) || exitCoder.ExitCode() != 143 {
		t.Fatalf("signal error = %v", err)
	}
	if len(m.ExecCalls) != 3 || m.ExecCalls[2].Argv[0] != "rm" {
		t.Fatalf("signal cleanup missing: %#v", m.ExecCalls)
	}
}

type stateFuncRuntime struct {
	runtime.Runtime
	state func(string) (runtime.State, error)
}

func (r stateFuncRuntime) State(name string) (runtime.State, error) {
	return r.state(name)
}

func TestUpContextDoesNotMutateAfterCancellationDuringStateInspection(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	cause := errors.New("entry interrupted")
	ctx, cancel := context.WithCancelCause(context.Background())
	s.RT = stateFuncRuntime{Runtime: m, state: func(string) (runtime.State, error) {
		cancel(cause)
		return runtime.StateAbsent, nil
	}}

	err := s.UpContext(ctx)
	if !errors.Is(err, cause) {
		t.Fatalf("cancellation error = %v", err)
	}
	if len(m.Run_) != 0 || len(m.Started) != 0 || len(m.ExecCalls) != 0 || len(m.Volumes) != 0 {
		t.Fatalf("runtime mutated after state cancellation: %#v", m)
	}
}

func TestRunSignalDuringLeaseCleanupIsReturned(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	markRunning(m, s)
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Credentials = map[string]config.Credential{
		"token": {
			Source: config.CredentialSource{Type: "file", Path: secret},
			Inject: config.CredentialInjection{Type: "environment", Name: "TOKEN"},
		},
	}
	var lifecycle context.Context
	m.InteractiveFunc = func(ctx context.Context, _, _ string, _ []string) error {
		lifecycle = ctx
		return nil
	}
	m.ExecContextFunc = func(_ context.Context, index int, _ runtime.ExecCall) error {
		if index != 2 {
			return nil
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			return err
		}
		<-lifecycle.Done()
		return nil
	}

	err := s.Run(s.Project, []string{"agent"}, []string{"token"})
	var signalErr *runtime.SignalError
	if !errors.As(err, &signalErr) || signalErr.ExitCode() != 143 {
		t.Fatalf("cleanup signal error = %v", err)
	}
}

func TestRunSignalDuringRevocationIsReturned(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	markRunning(m, s)
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Credentials = map[string]config.Credential{
		"token": {
			Source: config.CredentialSource{Type: "file", Path: secret},
			Inject: config.CredentialInjection{Type: "environment", Name: "TOKEN"},
		},
	}
	var lifecycle context.Context
	m.InteractiveFunc = func(ctx context.Context, _, _ string, _ []string) error {
		lifecycle = ctx
		return nil
	}
	s.revokeCredentials = func(context.Context, []credential.Acquired) error {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			return err
		}
		<-lifecycle.Done()
		return nil
	}

	err := s.Run(s.Project, []string{"agent"}, []string{"token"})
	var signalErr *runtime.SignalError
	if !errors.As(err, &signalErr) || signalErr.ExitCode() != 143 {
		t.Fatalf("revocation signal error = %v", err)
	}
}

func TestRunSignalDuringStagingCancelsTransportAndCleansLease(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	markRunning(m, s)
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Credentials = map[string]config.Credential{
		"token": {
			Source: config.CredentialSource{Type: "file", Path: secret},
			Inject: config.CredentialInjection{Type: "environment", Name: "TOKEN"},
		},
	}
	m.ExecContextFunc = func(ctx context.Context, index int, _ runtime.ExecCall) error {
		if index != 1 {
			return nil
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			return err
		}
		<-ctx.Done()
		return context.Cause(ctx)
	}

	err := s.Run(s.Project, []string{"agent"}, []string{"token"})
	var exitCoder interface{ ExitCode() int }
	if !errors.As(err, &exitCoder) || exitCoder.ExitCode() != 143 {
		t.Fatalf("signal error = %v", err)
	}
	if len(m.Interactive) != 0 {
		t.Fatal("interactive entry started after staging cancellation")
	}
	if len(m.ExecCalls) != 3 || m.ExecCalls[2].Argv[0] != "rm" {
		t.Fatalf("staging cancellation cleanup missing: %#v", m.ExecCalls)
	}
}

func TestRunSignalDuringSeedCancelsTransport(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	markRunning(m, s)
	source := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(source, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Seeds = []config.Seed{{Src: source, Dest: "~/.config/tool/settings.json", Policy: config.PolicyAlways}}

	var seedCtx context.Context
	m.ExecContextFunc = func(ctx context.Context, index int, _ runtime.ExecCall) error {
		if index != 1 {
			return nil
		}
		seedCtx = ctx
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(time.Second):
			return errors.New("seed transport ignored lifecycle cancellation")
		}
	}

	err := s.Run(s.Project, []string{"agent"}, nil)
	var signalErr *runtime.SignalError
	if !errors.As(err, &signalErr) || signalErr.ExitCode() != 143 {
		t.Fatalf("signal error = %v", err)
	}
	if seedCtx == nil || context.Cause(seedCtx) == nil {
		t.Fatal("seed transport did not receive lifecycle cancellation")
	}
	if len(m.Interactive) != 0 {
		t.Fatal("interactive entry started after seed cancellation")
	}
}

func TestRunStagesAfterSeedsAndKeepsFloxInsideCredentialWrapper(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".flox"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.Project = project
	seedSource := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(seedSource, []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Seeds = []config.Seed{{Src: seedSource, Dest: "~/.config/tool", Policy: config.PolicyAlways}}
	s.Cfg.Credentials = map[string]config.Credential{
		"token": {
			Source: config.CredentialSource{Type: "file", Path: secret},
			Inject: config.CredentialInjection{Type: "environment", Name: "TOKEN"},
		},
	}

	if err := s.Run(project, []string{"agent", "arg with spaces"}, []string{"token"}); err != nil {
		t.Fatal(err)
	}
	seedIndex, scrubIndex, stageIndex := -1, -1, -1
	for i, call := range m.ExecCalls {
		if len(call.Argv) >= 3 && strings.Contains(call.Argv[2], `cat > "$t"`) {
			seedIndex = i
		}
		if len(call.Argv) >= 3 && strings.Contains(call.Argv[2], "root=/dev/shm/coop-credentials") {
			scrubIndex = i
		}
		if len(call.Argv) >= 3 && strings.Contains(call.Argv[2], "tmp=$final.staging") {
			stageIndex = i
		}
	}
	if seedIndex < 0 || scrubIndex <= seedIndex || stageIndex <= scrubIndex {
		t.Fatalf("wrong seed/scrub/stage order: seed=%d scrub=%d stage=%d calls:\n%s", seedIndex, scrubIndex, stageIndex, m.ExecString())
	}
	call := m.Interactive[0]
	wantSuffix := []string{
		"flox", "activate", "--dir", "/opt/coop-core", "--",
		"/usr/local/bin/coop-user-env", "--project-flox", project, "--", "agent", "arg with spaces",
	}
	if !slices.Equal(call.Argv[len(call.Argv)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("Flox command is not inside credential wrapper: %#v", call.Argv)
	}
	if len(call.Argv) < 2 || call.Argv[0] != "sh" || call.Argv[1] != "-c" {
		t.Fatalf("credential wrapper missing: %#v", call.Argv)
	}
}

func TestRunAlwaysCleansUpAndPreservesGuestExit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interactive error
		wantExit    int
	}{
		{name: "successful guest"},
		{name: "nonzero guest", interactive: &runtime.ExitError{Code: 23}, wantExit: 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := runtime.NewMock()
			s := testSession(t, m)
			markRunning(m, s)
			secret := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			s.Cfg.Credentials = map[string]config.Credential{
				"token": {
					Source: config.CredentialSource{Type: "file", Path: secret},
					Inject: config.CredentialInjection{Type: "environment", Name: "TOKEN"},
				},
			}
			cleanupErr := errors.New("cleanup failed")
			m.ExecErrors = []error{nil, nil, cleanupErr}
			m.InteractiveErr = tc.interactive

			err := s.Run(s.Project, []string{"agent"}, []string{"token"})
			if !errors.Is(err, cleanupErr) {
				t.Fatalf("cleanup error missing: %v", err)
			}
			if tc.wantExit != 0 {
				var exitErr *runtime.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.wantExit {
					t.Fatalf("guest exit was replaced: %v", err)
				}
			}
			if len(m.ExecCalls) != 3 || !slices.Equal(m.ExecCalls[2].Argv[:3], []string{"rm", "-rf", "--"}) {
				t.Fatalf("cleanup not attempted: %#v", m.ExecCalls)
			}
		})
	}
}

func TestRunWithoutCredentialsScrubsAndPreservesArgvThroughEnvironment(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	markRunning(m, s)
	original := []string{"agent", "arg with spaces", "$literal"}
	if err := s.Run(s.Project, original, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.ExecCalls) != 1 || len(m.ExecCalls[0].Argv) < 3 || !strings.Contains(m.ExecCalls[0].Argv[2], "root=/dev/shm/coop-credentials") {
		t.Fatalf("stale leases not scrubbed: %#v", m.ExecCalls)
	}
	want := append([]string{
		"flox", "activate", "--dir", "/opt/coop-core", "--",
		"/usr/local/bin/coop-user-env", "--",
	}, original...)
	if !slices.Equal(m.Interactive[0].Argv, want) {
		t.Fatalf("credential-free argv was not preserved through environment wrapper: %#v", m.Interactive[0].Argv)
	}
}

func markRunning(m *runtime.Mock, s *Session) {
	m.Existing[s.Name] = true
	m.Started = append(m.Started, s.Name)
	labelCurrent(m, s)
}

func TestUpCreatesContainerWithIdenticalPathMount(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if len(m.Run_) != 1 {
		t.Fatalf("expected 1 run, got %d", len(m.Run_))
	}
	spec := m.Run_[0]
	if spec.Name != "coop-proj" {
		t.Errorf("bad spec: %+v", spec)
	}
	if spec.SSH {
		t.Errorf("SECURITY: ssh agent forwarding must default OFF")
	}
	if got := spec.Labels[runtime.ProjectLabel]; got != s.Project {
		t.Errorf("project label = %q, want %q", got, s.Project)
	}
	if len(spec.Mounts) != 1 || spec.Mounts[0].Source != spec.Mounts[0].Target {
		t.Errorf("identical-path mount violated: %+v", spec.Mounts)
	}
	// three per-project state volumes, targets under guest home
	if len(spec.Volumes) != 3 {
		t.Fatalf("expected 3 state volumes, got %+v", spec.Volumes)
	}
	for _, v := range spec.Volumes {
		if !strings.HasPrefix(v.Name, "coop-proj--") {
			t.Errorf("volume not project-scoped: %+v", v)
		}
		if !strings.HasPrefix(v.Target, "/Users/u/") {
			t.Errorf("volume target not under guest home: %+v", v)
		}
	}
	gotNames := []string{spec.Volumes[0].Name, spec.Volumes[1].Name, spec.Volumes[2].Name}
	wantNames := []string{"coop-proj--claude", "coop-proj--codex", "coop-proj--opencode"}
	if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
		t.Errorf("volumes not sorted before Run: %v", gotNames)
	}
}

func TestUpAddsConfiguredMountsWithIdenticalPathsAndAccess(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Cfg.Mounts = []config.Mount{
		{Source: "/Users/u/Projects/homebrew-tap", Access: config.MountReadWrite},
		{Source: "/Users/u/Projects/reference", Access: config.MountReadOnly},
	}

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	got := m.Run_[0].Mounts
	if len(got) != 3 {
		t.Fatalf("got %d mounts, want project plus two configured mounts: %+v", len(got), got)
	}
	if got[1].Source != "/Users/u/Projects/homebrew-tap" || got[1].Target != got[1].Source || got[1].ReadOnly {
		t.Errorf("read-write configured mount = %+v", got[1])
	}
	if got[2].Source != "/Users/u/Projects/reference" || got[2].Target != got[2].Source || !got[2].ReadOnly {
		t.Errorf("read-only configured mount = %+v", got[2])
	}
}

func TestUpAddsProjectVolumeAndPublishesLoopbackPort(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Project = t.TempDir()
	parent := filepath.Join(s.Project, "web")
	target := filepath.Join(parent, "node_modules")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	hostSentinel := filepath.Join(target, "host.txt")
	if err := os.WriteFile(hostSentinel, []byte("host tree"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Volumes = []config.Volume{{Path: "web/node_modules"}}
	s.Cfg.Publishes = []config.Publish{{HostPort: 5173, GuestPort: 5173}}

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	spec := m.Run_[0]
	wantVolume := runtime.Volume{
		Name:   projectVolumeName(s.Name, "web/node_modules"),
		Target: target,
	}
	if !slices.Contains(spec.Volumes, wantVolume) {
		t.Fatalf("project volume = %+v, want %+v", spec.Volumes, wantVolume)
	}
	wantPublish := runtime.Publish{HostPort: 5173, GuestPort: 5173}
	if !slices.Contains(spec.Publishes, wantPublish) {
		t.Fatalf("publications = %+v, want %+v", spec.Publishes, wantPublish)
	}
	got, err := os.ReadFile(hostSentinel)
	if err != nil || string(got) != "host tree" {
		t.Fatalf("host project contents changed: %q, %v", got, err)
	}
}

func TestProjectVolumeNameIsStableScopedAndPathSpecific(t *testing.T) {
	first := projectVolumeName("coop-proj", "web/node_modules")
	if again := projectVolumeName("coop-proj", "web/node_modules"); again != first {
		t.Fatalf("project volume name changed: %q != %q", first, again)
	}
	if !strings.HasPrefix(first, "coop-proj"+project.VolumeSep+"project-volume-") {
		t.Fatalf("project volume %q is not owned by coop-proj", first)
	}
	if other := projectVolumeName("coop-proj", "target"); other == first {
		t.Fatalf("different paths share project volume name %q", first)
	}
}

func TestSpecFingerprintChangesWithProjectRuntime(t *testing.T) {
	s := testSession(t, runtime.NewMock())
	before := s.SpecFingerprint()
	s.Cfg.Volumes = []config.Volume{{Path: "web/node_modules"}}
	withVolume := s.SpecFingerprint()
	if withVolume == before {
		t.Fatal("adding a project volume did not change the container spec fingerprint")
	}
	s.Cfg.Publishes = []config.Publish{{HostPort: 5173, GuestPort: 5173}}
	if withPublish := s.SpecFingerprint(); withPublish == withVolume {
		t.Fatal("adding a publication did not change the container spec fingerprint")
	}
}

func TestSpecFingerprintProjectRuntimeOrderIndependent(t *testing.T) {
	a := testSession(t, runtime.NewMock())
	b := testSession(t, runtime.NewMock())
	a.Cfg.Volumes = []config.Volume{{Path: "web/node_modules"}, {Path: "target"}}
	b.Cfg.Volumes = []config.Volume{{Path: "target"}, {Path: "web/node_modules"}}
	a.Cfg.Publishes = []config.Publish{{HostPort: 5173, GuestPort: 5173}, {HostPort: 8080, GuestPort: 80}}
	b.Cfg.Publishes = []config.Publish{{HostPort: 8080, GuestPort: 80}, {HostPort: 5173, GuestPort: 5173}}
	if got, want := a.SpecFingerprint(), b.SpecFingerprint(); got != want {
		t.Fatalf("authored order changed fingerprint: %q != %q", got, want)
	}
}

func TestUpCreatesOnlyMissingProjectVolumeTarget(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Project = t.TempDir()
	parent := filepath.Join(s.Project, "web")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(parent, "host.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Volumes = []config.Volume{{Path: "web/node_modules"}}

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "node_modules")
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("target was not created as a real directory: %v, %v", info, err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep" {
		t.Fatalf("parent contents changed: %q, %v", got, err)
	}
}

func TestUpProjectVolumePreparationFailureDoesNotCreateContainer(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Project = t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(s.Project, "web")); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Volumes = []config.Volume{{Path: "web/node_modules"}}

	err := s.Up()
	if err == nil || !strings.Contains(err.Error(), `prepare project volume "web/node_modules"`) || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("preparation error = %v", err)
	}
	if len(m.Run_) != 0 || m.Existing[s.Name] {
		t.Fatalf("container created after preparation failure: runs=%+v", m.Run_)
	}
}

func TestUpProjectVolumeRuntimeFailureIdentifiesAuthoredPath(t *testing.T) {
	m := runtime.NewMock()
	m.EnsureVolumeErr = errors.New("volume service unavailable")
	s := testSession(t, m)
	s.Cfg.Agents = nil
	s.Project = t.TempDir()
	if err := os.Mkdir(filepath.Join(s.Project, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Volumes = []config.Volume{{Path: "web/node_modules"}}

	err := s.Up()
	name := projectVolumeName(s.Name, "web/node_modules")
	if err == nil || !strings.Contains(err.Error(), "volume "+name+` for "web/node_modules"`) || !strings.Contains(err.Error(), "volume service unavailable") {
		t.Fatalf("volume creation error = %v", err)
	}
	if len(m.Run_) != 0 {
		t.Fatalf("container created after volume failure: %+v", m.Run_)
	}
}

func TestUpProjectVolumeNamesSurviveRecreation(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Project = t.TempDir()
	if err := os.Mkdir(filepath.Join(s.Project, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Volumes = []config.Volume{{Path: "web/node_modules"}}
	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	name := projectVolumeName(s.Name, "web/node_modules")
	if !m.Volumes[name] {
		t.Fatalf("project volume %q was not created", name)
	}

	s.Cfg.Publishes = []config.Publish{{HostPort: 5173, GuestPort: 5173}}
	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if len(m.Run_) != 2 || !m.Volumes[name] {
		t.Fatalf("recreation lost project volume: runs=%d volumes=%+v", len(m.Run_), m.Volumes)
	}
	for _, spec := range m.Run_ {
		if !slices.Contains(spec.Volumes, runtime.Volume{Name: name, Target: filepath.Join(s.Project, "web/node_modules")}) {
			t.Fatalf("run did not reuse stable project volume: %+v", spec.Volumes)
		}
	}
}

func TestSpecFingerprintChangesWithConfiguredMounts(t *testing.T) {
	s := testSession(t, runtime.NewMock())
	before := s.SpecFingerprint()
	s.Cfg.Mounts = []config.Mount{{
		Source: "/Users/u/Projects/homebrew-tap",
		Access: config.MountReadWrite,
	}}
	if after := s.SpecFingerprint(); after == before {
		t.Fatal("adding a configured mount did not change the container spec fingerprint")
	}
}

func TestSpecFingerprintPreservesLegacyValueWithoutConfiguredMounts(t *testing.T) {
	s := testSession(t, runtime.NewMock())
	vols := make([]string, 0, len(s.Cfg.Agents))
	for agent, spec := range s.Cfg.Agents {
		vols = append(vols, agent+"="+config.ExpandHome(spec.State, s.GuestHome))
	}
	slices.Sort(vols)
	legacy := fmt.Sprintf("v1|img=%s|cpus=%d|mem=%s|ssh=%t|proj=%s|home=%s|vols=%s",
		s.DesiredImageName(), s.Cfg.Resources.CPUs, s.Cfg.Resources.Memory,
		s.Cfg.SSH, s.Project, s.GuestHome, strings.Join(vols, ","))
	sum := sha256.Sum256([]byte(legacy))
	want := hex.EncodeToString(sum[:])[:16]
	if got := s.SpecFingerprint(); got != want {
		t.Fatalf("mount-free fingerprint = %q, want legacy %q", got, want)
	}
}

func TestUpHonorsGlobalSSHOptIn(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Cfg.SSH = true
	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if !m.Run_[0].SSH {
		t.Errorf("explicit ssh opt-in not honored")
	}
}

// labelCurrent marks an existing mock container as matching the
// session's current spec fingerprint.
func labelCurrent(m *runtime.Mock, s *Session) {
	m.ContainerLabels = map[string]map[string]string{
		s.Name: {SpecLabel: s.SpecFingerprint()},
	}
}

func TestUpStartsExistingStoppedContainer(t *testing.T) {
	m := runtime.NewMock()
	m.Existing["coop-proj"] = true
	s := testSession(t, m)
	labelCurrent(m, s)

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if len(m.Run_) != 0 {
		t.Errorf("should not recreate existing container")
	}
	if !m.Running("coop-proj") {
		t.Errorf("container should have been started")
	}
}

func TestUpRecreatesWhenSSHRevoked(t *testing.T) {
	m := runtime.NewMock()
	m.Existing["coop-proj"] = true
	s := testSession(t, m)
	// container was created when ssh was on; config has since revoked it
	sshOn := *s
	sshOn.Cfg.SSH = true
	m.ContainerLabels = map[string]map[string]string{
		s.Name: {SpecLabel: sshOn.SpecFingerprint()},
	}

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if len(m.Removed) != 1 || len(m.Run_) != 1 {
		t.Fatalf("ssh revocation must recreate: removed=%v runs=%d", m.Removed, len(m.Run_))
	}
	if m.Run_[0].SSH {
		t.Errorf("recreated container still has ssh")
	}
}

func TestUpRecreatesLegacyUnlabeledContainer(t *testing.T) {
	m := runtime.NewMock()
	m.Existing["coop-proj"] = true
	s := testSession(t, m)
	// no labels at all — pre-hardening container

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if len(m.Removed) != 1 || len(m.Run_) != 1 {
		t.Fatalf("legacy container must be recreated once: removed=%v", m.Removed)
	}
	if m.Run_[0].Labels[SpecLabel] != s.SpecFingerprint() {
		t.Errorf("recreated container not labeled")
	}
}

func TestUpAppliesSeeds(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)

	src := filepath.Join(t.TempDir(), "conf.json")
	if err := os.WriteFile(src, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Seeds = []config.Seed{{Src: src, Dest: "~/.config/x/conf.json", Policy: config.PolicyAlways}}

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if got := m.GuestFiles["/Users/u/.config/x/conf.json"]; got != `{"x":1}` {
		t.Errorf("seed content = %q; calls:\n%s", got, m.ExecString())
	}
}

func TestUpSeedsNativeLoginOnceIntoProjectAgentState(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	src := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(src, []byte(`{"login":"host"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Cfg.Seeds = []config.Seed{{
		Src: src, Dest: "~/.codex/auth.json", Policy: config.PolicyIfAbsent,
	}}

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if got := m.GuestFiles["/Users/u/.codex/auth.json"]; got != `{"login":"host"}` {
		t.Fatalf("initial login seed = %q", got)
	}
	var codexVolume bool
	for _, volume := range m.Run_[0].Volumes {
		if volume.Target == "/Users/u/.codex" {
			codexVolume = true
		}
	}
	if !codexVolume {
		t.Fatal("login destination is not backed by project Codex state")
	}

	m.GuestFiles["/Users/u/.codex/auth.json"] = `{"login":"guest-refreshed"}`
	if err := os.WriteFile(src, []byte(`{"login":"host-new"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if got := m.GuestFiles["/Users/u/.codex/auth.json"]; got != `{"login":"guest-refreshed"}` {
		t.Fatalf("later entry overwrote guest login: %q", got)
	}
}

func TestEntryArgvLayersCoreToolsAndOptionalProjectFlox(t *testing.T) {
	s := testSession(t, runtime.NewMock())
	workdir, argv := s.entryArgv("/elsewhere", []string{"opencode", "two words", "$HOME;echo nope"})
	if workdir != s.Project {
		t.Errorf("cwd outside project must clamp to root, got %q", workdir)
	}
	want := []string{
		"flox", "activate", "--dir", "/opt/coop-core", "--",
		"/usr/local/bin/coop-user-env", "--", "opencode", "two words", "$HOME;echo nope",
	}
	if !slices.Equal(argv, want) {
		t.Fatalf("non-Flox argv = %#v, want %#v", argv, want)
	}
	_, argv = s.entryArgv(s.Project, nil)
	want = []string{
		"flox", "activate", "--dir", "/opt/coop-core", "--",
		"/usr/local/bin/coop-user-env", "--", "zsh", "-l",
	}
	if !slices.Equal(argv, want) {
		t.Fatalf("non-Flox bare shell argv = %#v, want %#v", argv, want)
	}

	proj := t.TempDir()
	nested := filepath.Join(proj, "nested", "work")
	if err := os.MkdirAll(filepath.Join(proj, ".flox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	s.Project = proj
	_, argv = s.entryArgv(nested, []string{"claude", "arg with spaces"})
	want = []string{
		"flox", "activate", "--dir", "/opt/coop-core", "--",
		"/usr/local/bin/coop-user-env", "--project-flox", proj, "--", "claude", "arg with spaces",
	}
	if !slices.Equal(argv, want) {
		t.Fatalf("project Flox argv = %#v, want %#v", argv, want)
	}

	_, argv = s.entryArgv(nested, nil)
	want = []string{
		"flox", "activate", "--dir", "/opt/coop-core", "--",
		"/usr/local/bin/coop-user-env", "--project-flox", proj, "--", "zsh", "-l",
	}
	if !slices.Equal(argv, want) {
		t.Fatalf("bare shell argv = %#v, want %#v", argv, want)
	}
}

func TestEntryArgvRejectsSiblingPrefixPath(t *testing.T) {
	s := testSession(t, runtime.NewMock())
	if got, _ := s.entryArgv(s.Project+"-other", []string{"opencode"}); got != s.Project {
		t.Fatalf("sibling prefix escaped project boundary: %q", got)
	}
}

func TestConfiguredAndProjectToolsStayOutOfMaintenancePlane(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".flox"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.Project = project
	s.Cfg.Tools.Packages = []string{"shellcheck"}
	m.Images = map[string]bool{EffectiveImageName(s.Cfg): true}

	if err := s.Run(project, []string{"opencode"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, call := range m.ExecCalls {
		joined := strings.Join(call.Argv, " ")
		if strings.Contains(joined, "/opt/coop-tools") || strings.Contains(joined, project) || strings.Contains(joined, "coop-user-env") {
			t.Fatalf("maintenance call inherited user tool layers: %#v", call.Argv)
		}
	}
	if len(m.Interactive) != 1 {
		t.Fatalf("interactive calls = %d", len(m.Interactive))
	}
	userArgv := strings.Join(m.Interactive[0].Argv, " ")
	if !strings.Contains(userArgv, "coop-user-env") || !strings.Contains(userArgv, project) {
		t.Fatalf("user call missing configured/project layering: %#v", m.Interactive[0].Argv)
	}
}

func TestEnsureImageRequiresLocalBuildWhenMissing(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	delete(m.Images, EffectiveImageName(s.Cfg))

	if err := s.EnsureImage(); err == nil || !strings.Contains(err.Error(), "coop rebuild") {
		t.Fatalf("missing local image did not require rebuild: %v", err)
	}
}

func TestImageStatusReportsBuildAndRecreationState(t *testing.T) {
	t.Run("absent desired image", func(t *testing.T) {
		m := runtime.NewMock()
		s := testSession(t, m)
		delete(m.Images, EffectiveImageName(s.Cfg))
		got, err := s.ImageStatus()
		if err != nil {
			t.Fatal(err)
		}
		if got.State != runtime.StateAbsent || got.RunningImage != "" || !got.RebuildRequired || got.RecreationPending {
			t.Fatalf("status = %+v", got)
		}
	})

	t.Run("current running image", func(t *testing.T) {
		m := runtime.NewMock()
		s := testSession(t, m)
		m.Existing[s.Name] = true
		m.Started = append(m.Started, s.Name)
		m.ContainerImages = map[string]string{s.Name: EffectiveImageName(s.Cfg)}
		labelCurrent(m, s)
		got, err := s.ImageStatus()
		if err != nil {
			t.Fatal(err)
		}
		if got.State != runtime.StateRunning || got.RebuildRequired || got.RecreationPending || got.RunningImage != got.DesiredImage {
			t.Fatalf("status = %+v", got)
		}
	})

	t.Run("stale stopped image", func(t *testing.T) {
		m := runtime.NewMock()
		s := testSession(t, m)
		m.Existing[s.Name] = true
		m.ContainerImages = map[string]string{s.Name: "coop:local-old"}
		m.ContainerLabels = map[string]map[string]string{s.Name: {SpecLabel: "old-spec"}}
		got, err := s.ImageStatus()
		if err != nil {
			t.Fatal(err)
		}
		if got.State != runtime.StateStopped || got.RebuildRequired || !got.RecreationPending || got.RunningImage != "coop:local-old" {
			t.Fatalf("status = %+v", got)
		}
	})
}

func TestImageStatusSurfacesInspectionErrors(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	m.Existing[s.Name] = true
	m.ContainerImages = map[string]string{s.Name: EffectiveImageName(s.Cfg)}
	m.LabelErr = errors.New("label unavailable")
	if _, err := s.ImageStatus(); err == nil || !strings.Contains(err.Error(), "label unavailable") {
		t.Fatalf("inspection error hidden: %v", err)
	}
}

func TestEnsureImageCustomConfigRequiresLocalBuild(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Cfg.Tools.Packages = []string{"gemini-cli"}

	err := s.EnsureImage()
	if err == nil || !strings.Contains(err.Error(), "coop rebuild") {
		t.Errorf("custom config must demand local build, got %v", err)
	}
}

func TestExtraPackagesNeverReuseStaleStockImage(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m) // the base local image is present
	s.Cfg.Tools.Packages = []string{"gemini-cli"}

	err := s.EnsureImage()
	if err == nil || !strings.Contains(err.Error(), "coop rebuild") {
		t.Errorf("stale stock image silently reused: %v", err)
	}
}

func TestUpRecreatesContainerOnImageChange(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	// container exists, labeled with the stock-config fingerprint
	m.Existing["coop-proj"] = true
	labelCurrent(m, s)
	// config now wants extra packages; derived image already built
	s.Cfg.Tools.Packages = []string{"gemini-cli"}
	derived := EffectiveImageName(s.Cfg)
	m.Images[derived] = true

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	if len(m.Removed) != 1 {
		t.Fatalf("stale container not recreated")
	}
	if len(m.Run_) != 1 || m.Run_[0].Image != derived {
		t.Errorf("recreated with wrong image: %+v", m.Run_)
	}
	if len(m.RemovedVol) != 0 {
		t.Errorf("recreation must preserve state volumes: %v", m.RemovedVol)
	}
}

func TestUpRecreationWarnsAboutUndeclaredRootfsChanges(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	m.Existing[s.Name] = true
	labelCurrent(m, s)
	s.Cfg.Tools.Packages = []string{"shellcheck"}
	m.Images[EffectiveImageName(s.Cfg)] = true
	var output bytes.Buffer
	s.Output = &output

	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"state volumes preserved", "root-filesystem changes will be discarded"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("recreation warning missing %q: %s", want, output.String())
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("sink failed") }

func TestUpRecreationNoticeWriteFailureAbortsBeforeTeardown(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	m.Existing[s.Name] = true
	labelCurrent(m, s)
	s.Cfg.Tools.Packages = []string{"shellcheck"}
	m.Images[EffectiveImageName(s.Cfg)] = true
	s.Output = failingWriter{}

	err := s.Up()
	if err == nil || !strings.Contains(err.Error(), "write recreation notice") {
		t.Fatalf("recreation notice write failure not propagated: %v", err)
	}
	if len(m.Removed) != 0 {
		t.Fatalf("container torn down despite failed notice: %v", m.Removed)
	}
}

func TestEffectiveImageNameRegistryPort(t *testing.T) {
	cfg := config.Config{Image: config.Image{Name: "localhost:5000/team/coop:latest"}, Tools: config.Tools{Packages: []string{"x"}}}
	got := EffectiveImageName(cfg)
	if !strings.HasPrefix(got, "localhost:5000/team/coop:local-") {
		t.Errorf("registry port corrupted: %q", got)
	}
	digest := config.Config{Image: config.Image{Name: "repo/coop@sha256:deadbeef"}, Tools: config.Tools{Packages: []string{"x"}}}
	if got := EffectiveImageName(digest); !strings.HasPrefix(got, "repo/coop:local-") {
		t.Errorf("digest ref corrupted: %q", got)
	}
}

func TestDesiredImageChangesWithCoreLock(t *testing.T) {
	s := testSession(t, runtime.NewMock())
	embedded := s.DesiredImageName()
	s.CoreLock = append(image.EmbeddedCoreLock(), '\n')
	if got := s.DesiredImageName(); got == embedded {
		t.Fatal("changed core lock did not change desired image")
	}
}

func TestImageStatusMarksUpgradedCoreAsRebuildRequiredWithoutMutation(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	oldImage := s.DesiredImageName()
	m.Existing[s.Name] = true
	m.ContainerImages = map[string]string{s.Name: oldImage}
	m.ContainerLabels = map[string]map[string]string{s.Name: {SpecLabel: s.SpecFingerprint()}}
	s.CoreLock = append(image.EmbeddedCoreLock(), '\n')

	status, err := s.ImageStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !status.RebuildRequired || !status.RecreationPending {
		t.Fatalf("status = %+v", status)
	}
	if len(m.Stopped) != 0 || len(m.Removed) != 0 {
		t.Fatalf("status mutated container: stopped=%v removed=%v", m.Stopped, m.Removed)
	}
}

func TestUpgradedCoreRefusesContainerReplacementUntilImageExists(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	oldImage := s.DesiredImageName()
	m.Existing[s.Name] = true
	m.ContainerImages = map[string]string{s.Name: oldImage}
	m.ContainerLabels = map[string]map[string]string{s.Name: {SpecLabel: s.SpecFingerprint()}}
	s.CoreLock = append(image.EmbeddedCoreLock(), '\n')

	err := s.Up()
	if err == nil || !strings.Contains(err.Error(), "coop rebuild") {
		t.Fatalf("error = %v", err)
	}
	if len(m.Stopped) != 0 || len(m.Removed) != 0 {
		t.Fatalf("missing upgraded image mutated container: stopped=%v removed=%v", m.Stopped, m.Removed)
	}
}

func TestNewLoadsActiveCoreLockAndSurfacesInvalidStateWarning(t *testing.T) {
	home := t.TempDir()
	xdgState := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", xdgState)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".coop.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(xdgState, "coop")
	if err := core.Install(stateDir, image.EmbeddedCoreLock()); err != nil {
		t.Fatal(err)
	}
	s, err := New(runtime.NewMock(), project)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.CoreLock) == 0 {
		t.Fatal("session did not load active core lock")
	}

	path := filepath.Join(stateDir, "core", image.CoreDefinitionFingerprint(), "manifest.lock")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = New(runtime.NewMock(), project)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(s.Cfg.Warnings, func(warning string) bool {
		return strings.Contains(warning, "invalid core upgrade state ignored")
	}) {
		t.Fatalf("warnings = %v", s.Cfg.Warnings)
	}
}

func TestEffectiveImageNameIncludesGitHubReleaseDeclarationAndResolution(t *testing.T) {
	base := config.Config{
		Image: config.Image{Name: "coop:latest"},
		Tools: config.Tools{GitHubReleases: []config.GitHubReleaseTool{{
			Name: "kata", Repo: "kenn-io/kata", Tag: "latest",
			Asset: "kata_{version}_linux_arm64.tar.gz", Binary: "kata",
		}}},
	}
	unresolved := EffectiveImageName(base)
	if unresolved == EffectiveImageName(config.Config{Image: config.Image{Name: "coop:latest"}}) {
		t.Fatal("unresolved declaration did not affect image identity")
	}
	base.Tools.ResolvedReleases = []config.ResolvedReleaseTool{{
		Name: "kata", Repo: "kenn-io/kata", RequestedTag: "latest", Tag: "v0.10.0",
		Asset: "kata_0.10.0_linux_arm64.tar.gz", Digest: "sha256:" + strings.Repeat("a", 64),
		Binary: "kata", URL: "https://github.com/ignored", CachePath: "/host/ignored",
	}}
	resolved := EffectiveImageName(base)
	if resolved == unresolved {
		t.Fatal("resolved release did not affect image identity")
	}
	base.Tools.ResolvedReleases[0].URL = "https://github.com/different-but-ignored"
	base.Tools.ResolvedReleases[0].CachePath = "/different/host/path"
	if got := EffectiveImageName(base); got != resolved {
		t.Fatalf("host-specific release state affected image identity: %s != %s", got, resolved)
	}
	base.Tools.ResolvedReleases[0].Digest = "sha256:" + strings.Repeat("b", 64)
	if got := EffectiveImageName(base); got == resolved {
		t.Fatal("release digest change did not affect image identity")
	}
}

func TestEffectiveImageNameDerivation(t *testing.T) {
	stock := config.Config{Image: config.Image{Name: "coop:latest"}}
	if got := EffectiveImageName(stock); !strings.HasPrefix(got, "coop:local-") {
		t.Errorf("default embedded image tag not derived: %s", got)
	}
	a := config.Config{Image: config.Image{Name: "coop:latest"}, Tools: config.Tools{Packages: []string{"x", "y"}}}
	b := config.Config{Image: config.Image{Name: "coop:latest"}, Tools: config.Tools{Packages: []string{"y", "x", "y"}}}
	if EffectiveImageName(a) != EffectiveImageName(b) {
		t.Errorf("package order and duplicates should not change tag")
	}
	if EffectiveImageName(a) == EffectiveImageName(stock) || !strings.HasPrefix(EffectiveImageName(a), "coop:local-") {
		t.Errorf("derived tag wrong: %s", EffectiveImageName(a))
	}
	// base ref changes must change the derived tag even with identical packages
	v1 := config.Config{Image: config.Image{Name: "coop:v1"}, Tools: config.Tools{Packages: []string{"x"}}}
	v2 := config.Config{Image: config.Image{Name: "coop:v2"}, Tools: config.Tools{Packages: []string{"x"}}}
	if EffectiveImageName(v1) == EffectiveImageName(v2) {
		t.Errorf("base ref not part of derivation")
	}
	custom := config.Config{Image: config.Image{Name: "team/coop:custom"}}
	if got := EffectiveImageName(custom); got == custom.Image.Name || !strings.HasPrefix(got, "team/coop:local-") {
		t.Errorf("custom image name bypassed embedded core identity: %s", got)
	}
}

func TestUpRefusesTeardownWhenReplacementImageMissing(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	m.Existing["coop-proj"] = true
	labelCurrent(m, s)
	s.Cfg.Tools.Packages = []string{"gemini-cli"} // derived image NOT built

	err := s.Up()
	if err == nil || !strings.Contains(err.Error(), "coop rebuild") {
		t.Fatalf("expected actionable error, got %v", err)
	}
	if len(m.Removed) != 0 {
		t.Errorf("working container torn down before replacement validated")
	}
}

func TestUpSurfacesRuntimeErrors(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	m.StateErr = fmt.Errorf("apiserver unreachable")

	err := s.Up()
	if err == nil || !strings.Contains(err.Error(), "apiserver unreachable") {
		t.Fatalf("runtime failure must surface, not read as absent: %v", err)
	}
	if len(m.Run_) != 0 {
		t.Errorf("created a container on inconclusive state")
	}
}

func TestDestroyRemovesVolumesByPrefixNotConfig(t *testing.T) {
	m := runtime.NewMock()
	m.Existing["coop-proj"] = true
	// this coop's volumes, incl. one from an OLDER agent config (gemini)
	m.Volumes["coop-proj--opencode"] = true
	m.Volumes["coop-proj--gemini"] = true
	// other coops, including a name whose slug begins
	// with this coop's full name (the "--" separator must fence it)
	m.Volumes["coop-other--claude"] = true
	m.Volumes["coop-proj-worker-abcdef1234567890--opencode"] = true
	s := testSession(t, m)
	if err := s.Destroy(); err != nil {
		t.Fatal(err)
	}
	if len(m.Removed) != 1 {
		t.Errorf("container not removed")
	}
	if len(m.RemovedVol) != 2 {
		t.Errorf("prefix destroy wrong: %v", m.RemovedVol)
	}
	if !m.Volumes["coop-other--claude"] || !m.Volumes["coop-proj-worker-abcdef1234567890--opencode"] {
		t.Errorf("destroyed another coop's volume: %v", m.RemovedVol)
	}
}

func TestUpUsesConfiguredAgentSet(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Cfg.Agents = map[string]config.Agent{
		"aider": {State: "~/.aider"},
	}
	if err := s.Up(); err != nil {
		t.Fatal(err)
	}
	spec := m.Run_[0]
	if len(spec.Volumes) != 1 || spec.Volumes[0].Name != "coop-proj--aider" ||
		spec.Volumes[0].Target != "/Users/u/.aider" {
		t.Errorf("agent config not honored: %+v", spec.Volumes)
	}
}
