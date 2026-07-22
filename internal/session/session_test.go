package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
	"github.com/sarcasticbird/coop/internal/credential"
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
	m.Images = map[string]bool{EffectiveImageName(s.Cfg.Image): true}
	return s
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
	wantSuffix := []string{"flox", "activate", "--dir", project, "--", "agent", "arg with spaces"}
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

func TestRunWithoutCredentialsScrubsAndUsesOriginalArgv(t *testing.T) {
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
	if !slices.Equal(m.Interactive[0].Argv, original) {
		t.Fatalf("credential-free argv was wrapped: %#v", m.Interactive[0].Argv)
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

func TestEntryArgvClampsCwdAndWrapsFlox(t *testing.T) {
	s := testSession(t, runtime.NewMock())
	// project WITHOUT .flox: plain argv
	workdir, argv := s.entryArgv("/elsewhere", []string{"opencode"})
	if workdir != s.Project {
		t.Errorf("cwd outside project must clamp to root, got %q", workdir)
	}
	if strings.Join(argv, " ") != "opencode" {
		t.Errorf("unexpected argv: %v", argv)
	}

	// project WITH .flox: wrapped in flox activate
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".flox"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.Project = proj
	_, argv = s.entryArgv(proj, []string{"claude"})
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "flox activate") || !strings.Contains(joined, "claude") {
		t.Errorf("flox wrap missing: %v", argv)
	}
}

func TestEntryArgvRejectsSiblingPrefixPath(t *testing.T) {
	s := testSession(t, runtime.NewMock())
	if got, _ := s.entryArgv(s.Project+"-other", []string{"opencode"}); got != s.Project {
		t.Fatalf("sibling prefix escaped project boundary: %q", got)
	}
}

func TestEnsureImageRequiresLocalBuildWhenMissing(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	delete(m.Images, EffectiveImageName(s.Cfg.Image))

	if err := s.EnsureImage(); err == nil || !strings.Contains(err.Error(), "coop rebuild") {
		t.Fatalf("missing local image did not require rebuild: %v", err)
	}
}

func TestEnsureImageCustomConfigRequiresLocalBuild(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	s.Cfg.Image.ExtraPackages = []string{"gemini-cli"}

	err := s.EnsureImage()
	if err == nil || !strings.Contains(err.Error(), "coop rebuild") {
		t.Errorf("custom config must demand local build, got %v", err)
	}
}

func TestExtraPackagesNeverReuseStaleStockImage(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m) // the base local image is present
	s.Cfg.Image.ExtraPackages = []string{"gemini-cli"}

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
	s.Cfg.Image.ExtraPackages = []string{"gemini-cli"}
	derived := EffectiveImageName(s.Cfg.Image)
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

func TestEffectiveImageNameRegistryPort(t *testing.T) {
	img := config.Image{Name: "localhost:5000/team/coop:latest", ExtraPackages: []string{"x"}}
	got := EffectiveImageName(img)
	if !strings.HasPrefix(got, "localhost:5000/team/coop:local-") {
		t.Errorf("registry port corrupted: %q", got)
	}
	digest := config.Image{Name: "repo/coop@sha256:deadbeef", ExtraPackages: []string{"x"}}
	if got := EffectiveImageName(digest); !strings.HasPrefix(got, "repo/coop:local-") {
		t.Errorf("digest ref corrupted: %q", got)
	}
}

func TestEffectiveImageNameDerivation(t *testing.T) {
	stock := config.Image{Name: "coop:latest"}
	if got := EffectiveImageName(stock); !strings.HasPrefix(got, "coop:local-") {
		t.Errorf("default embedded image tag not derived: %s", got)
	}
	a := config.Image{Name: "coop:latest", ExtraPackages: []string{"x", "y"}}
	b := config.Image{Name: "coop:latest", ExtraPackages: []string{"y", "x"}}
	if EffectiveImageName(a) != EffectiveImageName(b) {
		t.Errorf("package order should not change tag")
	}
	if EffectiveImageName(a) == EffectiveImageName(stock) || !strings.HasPrefix(EffectiveImageName(a), "coop:local-") {
		t.Errorf("derived tag wrong: %s", EffectiveImageName(a))
	}
	// base ref changes must change the derived tag even with identical packages
	v1 := config.Image{Name: "coop:v1", ExtraPackages: []string{"x"}}
	v2 := config.Image{Name: "coop:v2", ExtraPackages: []string{"x"}}
	if EffectiveImageName(v1) == EffectiveImageName(v2) {
		t.Errorf("base ref not part of derivation")
	}
}

func TestUpRefusesTeardownWhenReplacementImageMissing(t *testing.T) {
	m := runtime.NewMock()
	s := testSession(t, m)
	m.Existing["coop-proj"] = true
	labelCurrent(m, s)
	s.Cfg.Image.ExtraPackages = []string{"gemini-cli"} // derived image NOT built

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
