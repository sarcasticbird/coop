package credential

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/config"
	coopruntime "github.com/sarcasticbird/coop/internal/runtime"
	"golang.org/x/sys/unix"
)

func TestAcquireFileExpandsHomeAndRequiresRegularFile(t *testing.T) {
	home := t.TempDir()
	secretPath := filepath.Join(home, ".token")
	if err := os.WriteFile(secretPath, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(t.TempDir())

	acquired, err := mgr.AcquireAll(context.Background(), home, []Selected{{
		Name: "token",
		Spec: config.Credential{Source: config.CredentialSource{Type: "file", Path: "~/.token"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(acquired[0].payload); got != "secret" {
		t.Fatalf("payload = %q", got)
	}

	_, err = mgr.AcquireAll(context.Background(), home, []Selected{{
		Name: "directory",
		Spec: config.Credential{Source: config.CredentialSource{Type: "file", Path: "~/"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory accepted: %v", err)
	}
}

func TestAcquireFileRejectsPathThatExpandsRelative(t *testing.T) {
	mgr := NewManager(t.TempDir())
	opened := false
	mgr.OpenFile = func(string) (*os.File, error) {
		opened = true
		return nil, errors.New("unexpected open")
	}

	_, err := mgr.AcquireAll(context.Background(), "relative-home", []Selected{{
		Name: "token",
		Spec: config.Credential{Source: config.CredentialSource{
			Type: "file", Path: "~/.token",
		}},
	}})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative expanded path accepted: %v", err)
	}
	if opened {
		t.Fatal("relative credential path was opened")
	}
}

func TestAcquireFileRejectsProjectControlledPathsAndSymlinkChains(t *testing.T) {
	project := t.TempDir()
	projectSecret := filepath.Join(project, "secret")
	if err := os.WriteFile(projectSecret, []byte("project-controlled"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalAlias := filepath.Join(t.TempDir(), "credential")
	if err := os.Symlink(projectSecret, externalAlias); err != nil {
		t.Fatal(err)
	}
	externalTarget := filepath.Join(t.TempDir(), "safe")
	if err := os.WriteFile(externalTarget, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectEscape := filepath.Join(project, "escape")
	if err := os.Symlink(externalTarget, projectEscape); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{projectSecret, externalAlias, projectEscape} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			mgr := NewManager(project)
			opened := false
			mgr.OpenFile = func(string) (*os.File, error) {
				opened = true
				return nil, errors.New("unexpected open")
			}
			_, err := mgr.AcquireAll(context.Background(), t.TempDir(), []Selected{{
				Name: "token",
				Spec: config.Credential{Source: config.CredentialSource{Type: "file", Path: path}},
			}})
			if err == nil || !strings.Contains(err.Error(), "enters the project") {
				t.Fatalf("project-controlled path error = %v", err)
			}
			if opened {
				t.Fatal("project-controlled credential path was opened")
			}
		})
	}
}

func TestAcquireFileOpensCanonicalTargetAfterAliasRetarget(t *testing.T) {
	project := t.TempDir()
	safePath := filepath.Join(t.TempDir(), "safe")
	if err := os.WriteFile(safePath, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherPath := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(otherPath, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "credential")
	if err := os.Symlink(safePath, alias); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(project)
	mgr.OpenFile = func(path string) (*os.File, error) {
		if path == alias {
			t.Fatal("credential alias was passed to the opener")
		}
		if err := os.Remove(alias); err != nil {
			return nil, err
		}
		if err := os.Symlink(otherPath, alias); err != nil {
			return nil, err
		}
		return openCredentialFile(path)
	}

	acquired, err := mgr.AcquireAll(context.Background(), t.TempDir(), []Selected{{
		Name: "token",
		Spec: config.Credential{Source: config.CredentialSource{Type: "file", Path: alias}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(acquired[0].payload); got != "safe" {
		t.Fatalf("retargeted alias changed payload to %q", got)
	}
}

func TestOpenCredentialFileRejectsSymlink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if file, err := openCredentialFile(alias); err == nil {
		_ = file.Close()
		t.Fatal("no-follow credential opener accepted a symlink")
	}
}

func TestWithinRootRecognizesCaseAliases(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "CoopProject")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	caseAlias := filepath.Join(parent, "coopproject")
	if _, err := os.Stat(caseAlias); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	secret := filepath.Join(project, "Secret")
	if err := os.WriteFile(secret, []byte("project-controlled"), 0o600); err != nil {
		t.Fatal(err)
	}
	aliasSecret := filepath.Join(caseAlias, "secret")
	if !withinRoot(project, aliasSecret) {
		t.Fatalf("case alias %q was treated as outside %q", aliasSecret, project)
	}
	if _, err := trustedCredentialFilePath(project, aliasSecret); err == nil {
		t.Fatal("case-aliased project credential file was accepted")
	}
}

func TestAcquireFileOpensOnceAndBoundsRead(t *testing.T) {
	home := t.TempDir()
	secretPath := filepath.Join(home, ".token")
	if err := os.WriteFile(secretPath, bytes.Repeat([]byte("x"), MaxPayloadBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(t.TempDir())
	opened := 0
	mgr.OpenFile = func(path string) (*os.File, error) {
		opened++
		return openCredentialFile(path)
	}

	_, err := mgr.AcquireAll(context.Background(), home, []Selected{{
		Name: "token",
		Spec: config.Credential{Source: config.CredentialSource{Type: "file", Path: "~/.token"}},
	}})
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("error = %v", err)
	}
	if opened != 1 {
		t.Fatalf("credential file opened %d times", opened)
	}
}

func TestAcquireEnforcesPayloadLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "boundary", size: MaxPayloadBytes},
		{name: "too large", size: MaxPayloadBytes + 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := NewManager(t.TempDir())
			mgr.Run = func(context.Context, string, []string) ([]byte, error) {
				return bytes.Repeat([]byte("x"), tc.size), nil
			}
			_, err := mgr.AcquireAll(context.Background(), t.TempDir(), []Selected{{
				Name: "token",
				Spec: config.Credential{Source: config.CredentialSource{
					Type: "command", Argv: []string{"secret-tool"},
				}},
			}})
			if tc.wantErr && !errors.Is(err, ErrPayloadTooLarge) {
				t.Fatalf("error = %v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAcquireCommandPreservesArgvAndSetsDeadline(t *testing.T) {
	want := []string{"secret-tool", "--account", "dev profile", "$literal"}
	var got []string
	var gotHome string
	var remaining time.Duration
	mgr := NewManager(t.TempDir())
	mgr.Run = func(ctx context.Context, home string, argv []string) ([]byte, error) {
		gotHome = home
		got = slices.Clone(argv)
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("credential command has no deadline")
		}
		remaining = time.Until(deadline)
		return []byte("secret"), nil
	}

	home := t.TempDir()
	_, err := mgr.AcquireAll(context.Background(), home, []Selected{{
		Name: "token",
		Spec: config.Credential{Source: config.CredentialSource{Type: "command", Argv: want}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %#v", got)
	}
	if gotHome != home {
		t.Fatalf("command home = %q, want %q", gotHome, home)
	}
	if remaining <= 9*time.Minute || remaining > commandTimeout {
		t.Fatalf("deadline remaining = %v", remaining)
	}
}

func TestResolveExecutableSkipsProjectControlledPATHEntries(t *testing.T) {
	project := t.TempDir()
	projectBin := filepath.Join(project, "bin")
	if err := os.Mkdir(projectBin, 0o755); err != nil {
		t.Fatal(err)
	}
	projectHelper := filepath.Join(projectBin, "credential-helper")
	if err := os.WriteFile(projectHelper, []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	alias := filepath.Join(t.TempDir(), "project-bin")
	if err := os.Symlink(projectBin, alias); err != nil {
		t.Fatal(err)
	}
	safeBin := t.TempDir()
	safeHelper := filepath.Join(safeBin, "credential-helper")
	if err := os.WriteFile(safeHelper, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	search := strings.Join([]string{projectBin, alias, safeBin}, string(os.PathListSeparator))
	got, err := resolveExecutable(t.TempDir(), project, "credential-helper", search)
	if err != nil {
		t.Fatal(err)
	}
	wantHelper, err := filepath.EvalSymlinks(safeHelper)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantHelper {
		t.Fatalf("resolved helper = %q, want trusted %q", got, wantHelper)
	}
	if _, err := resolveExecutable(t.TempDir(), project, projectHelper, search); err == nil {
		t.Fatal("absolute project-controlled credential helper accepted")
	}
}

func TestRunCommandSanitizesInheritedPATH(t *testing.T) {
	project := t.TempDir()
	projectBin := filepath.Join(project, "bin")
	if err := os.Mkdir(projectBin, 0o755); err != nil {
		t.Fatal(err)
	}
	safeBin := t.TempDir()
	helper := filepath.Join(safeBin, "credential-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s' \"$PATH\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", strings.Join([]string{projectBin, safeBin}, string(os.PathListSeparator)))

	payload, err := runCommand(context.Background(), t.TempDir(), project, []string{"credential-helper"})
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := filepath.EvalSymlinks(safeBin)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(payload); got != wantPath {
		t.Fatalf("credential helper PATH = %q, want %q", got, wantPath)
	}
}

func TestRunCommandDropsExecutionInfluencingEnvironment(t *testing.T) {
	project := t.TempDir()
	for name, value := range map[string]string{
		"AWS_CONFIG_FILE":  filepath.Join(project, "aws-config"),
		"BASH_ENV":         filepath.Join(project, "bash-env"),
		"GIT_CONFIG_COUNT": "1",
		"NODE_OPTIONS":     "--require=" + filepath.Join(project, "hook.js"),
		"PYTHONPATH":       project,
	} {
		t.Setenv(name, value)
	}

	script := `printf '%s\n' "${AWS_CONFIG_FILE-unset}" "${BASH_ENV-unset}" "${GIT_CONFIG_COUNT-unset}" "${NODE_OPTIONS-unset}" "${PYTHONPATH-unset}" "$HOME" "$PWD"`
	home := t.TempDir()
	payload, err := runCommand(context.Background(), home, project, []string{"/bin/sh", "-c", script})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("unset\n", 5) + home + "\n" + home + "\n"
	if got := string(payload); got != want {
		t.Fatalf("credential helper environment = %q, want %q", got, want)
	}
}

func TestTrustedCommandEnvironmentDropsShellAndCanonicalizesPaths(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	target := t.TempDir()
	projectAlias := filepath.Join(project, "retargetable")
	if err := os.Symlink(target, projectAlias); err != nil {
		t.Fatal(err)
	}
	externalAlias := filepath.Join(t.TempDir(), "config-link")
	if err := os.Symlink(target, externalAlias); err != nil {
		t.Fatal(err)
	}

	got, err := trustedCommandEnvironment([]string{
		"SHELL=" + filepath.Join(project, "shell"),
		"XDG_CONFIG_HOME=" + projectAlias,
		"XDG_CACHE_HOME=" + externalAlias,
	}, home, project, "/usr/bin:/bin")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "SHELL=") {
		t.Fatalf("inherited SHELL survived: %v", got)
	}
	if strings.Contains(joined, "XDG_CONFIG_HOME=") {
		t.Fatalf("project-contained path alias survived: %v", got)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "XDG_CACHE_HOME="+resolvedTarget) {
		t.Fatalf("external path was not canonicalized: %v", got)
	}
}

func TestAWSProfileRejectsProjectControlledCLI(t *testing.T) {
	project := t.TempDir()
	projectBin := filepath.Join(project, "bin")
	if err := os.Mkdir(projectBin, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "executed")
	aws := filepath.Join(projectBin, "aws")
	if err := os.WriteFile(aws, []byte("#!/bin/sh\ntouch \"$MARKER\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARKER", marker)
	t.Setenv("PATH", projectBin)

	mgr := NewManager(project)
	_, err := mgr.AcquireAll(context.Background(), t.TempDir(), []Selected{{
		Name: "aws-dev",
		Spec: config.Credential{
			Source: config.CredentialSource{Type: "aws-profile", Profile: "dev"},
			Inject: config.CredentialInjection{Type: "aws"},
		},
	}})
	if err == nil || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("project-controlled AWS CLI error = %v, want executable not found", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project-controlled AWS CLI executed: %v", err)
	}
}

func TestRunCommandCancellationTerminatesDescendants(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	helper := filepath.Join(dir, "helper")
	script := "#!/bin/sh\nsleep 30 &\nchild=$!\nprintf '%s\\n' \"$child\" > \"$1\"\nwait \"$child\"\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	home := t.TempDir()
	project := t.TempDir()
	go func() {
		_, err := runCommand(ctx, home, project, []string{helper, pidFile})
		result <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	var childPID int
	for {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			if _, err := fmt.Sscanf(string(raw), "%d", &childPID); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("credential helper did not start its descendant")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled credential helper succeeded")
		}
	case <-time.After(commandWaitDelay + time.Second):
		t.Fatal("credential helper remained blocked after cancellation")
	}
	deadline = time.Now().Add(time.Second)
	for {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("credential helper descendant %d survived cancellation: %v", childPID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunCommandUsesTrustedHome(t *testing.T) {
	home := t.TempDir()
	helperDir := t.TempDir()
	helper := filepath.Join(helperDir, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\npwd -P\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	payload, err := runCommand(context.Background(), home, t.TempDir(), []string{helper})
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(payload)); got != want {
		t.Fatalf("credential command cwd = %q, want %q", got, want)
	}
}

func TestRunCommandRejectsRelativeExecutablePath(t *testing.T) {
	_, err := runCommand(context.Background(), t.TempDir(), t.TempDir(), []string{"./credential-helper"})
	if err == nil || !strings.Contains(err.Error(), "relative") {
		t.Fatalf("relative credential executable accepted: %v", err)
	}
}

func TestResolveExecutableAnchorsRelativePATHAtTrustedHome(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(bin, "credential-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := resolveExecutable(home, t.TempDir(), "credential-helper", "bin")
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(helper)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved executable = %q, want %q", got, want)
	}
}

func TestRunCommandReturnsSignalError(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\ntrap - INT\nkill -INT $$\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := runCommand(context.Background(), t.TempDir(), t.TempDir(), []string{helper})
	var signalErr *coopruntime.SignalError
	if !errors.As(err, &signalErr) || signalErr.ExitCode() != 130 {
		t.Fatalf("credential command signal error = %v", err)
	}
}

func TestRunCommandPreservesContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	_, err := runCommand(ctx, t.TempDir(), t.TempDir(), []string{"sh", "-c", "exec sleep 30"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("credential command deadline error = %v", err)
	}
	var signalErr *coopruntime.SignalError
	if errors.As(err, &signalErr) {
		t.Fatalf("credential command deadline became signal error: %v", signalErr)
	}
}

func TestRunCommandStopsImmediatelyAtPayloadLimit(t *testing.T) {
	started := time.Now()
	_, err := runCommand(context.Background(), t.TempDir(), t.TempDir(), []string{"sh", "-c", "exec yes x"})
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("payload error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("payload overflow took %v to stop helper", elapsed)
	}
}

func TestRunCommandStopsAndContinuesWithChildJob(t *testing.T) {
	if _, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP); err != nil {
		t.Skip("requires a controlling terminal")
	}
	dir := t.TempDir()
	resumed := filepath.Join(dir, "resumed")
	helper := filepath.Join(dir, "helper")
	script := "#!/bin/sh\nkill -TSTP $$\nprintf resumed > \"$1\"\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	watchdog := continueStoppedCredentialTest(t)
	if _, err := runCommand(context.Background(), t.TempDir(), t.TempDir(), []string{helper, resumed}); err != nil {
		t.Fatal(err)
	}
	if err := watchdog.Wait(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(resumed); err != nil || string(raw) != "resumed" {
		t.Fatalf("continued helper marker = %q, %v", raw, err)
	}
}

func continueStoppedCredentialTest(t *testing.T) *exec.Cmd {
	t.Helper()
	script := `attempts=0
while :; do
  state=$(ps -o state= -p "$1")
  case "$state" in (*T*) break;; esac
	attempts=$((attempts + 1))
	test "$attempts" -lt 500 || exit 1
  sleep 0.02
done
kill -CONT "$1"`
	watchdog := exec.Command("sh", "-c", script, "coop-job-watchdog", fmt.Sprint(os.Getpid()))
	watchdog.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := watchdog.Start(); err != nil {
		t.Fatal(err)
	}
	return watchdog
}

func TestAcquireRollsBackInReverseOrder(t *testing.T) {
	var revoked []string
	mgr := NewManager(t.TempDir())
	mgr.acquire = func(_ context.Context, _ string, selected Selected) (Acquired, error) {
		if selected.Name == "three" {
			return Acquired{}, errors.New("acquisition failed")
		}
		name := selected.Name
		return Acquired{
			Selected: selected,
			revoke: func(context.Context) error {
				revoked = append(revoked, name)
				return nil
			},
		}, nil
	}

	_, err := mgr.AcquireAll(context.Background(), t.TempDir(), []Selected{
		{Name: "one"}, {Name: "two"}, {Name: "three"},
	})
	if err == nil || !strings.Contains(err.Error(), "acquisition failed") {
		t.Fatalf("error = %v", err)
	}
	if !slices.Equal(revoked, []string{"two", "one"}) {
		t.Fatalf("revocation order = %v", revoked)
	}
}

func TestAcquireRollsBackFinalItemWhenCanceledDuringAcquisition(t *testing.T) {
	cause := errors.New("entry interrupted")
	ctx, cancel := context.WithCancelCause(context.Background())
	var revoked []string
	mgr := NewManager(t.TempDir())
	mgr.acquire = func(_ context.Context, _ string, selected Selected) (Acquired, error) {
		cancel(cause)
		name := selected.Name
		return Acquired{
			Selected: selected,
			revoke: func(context.Context) error {
				revoked = append(revoked, name)
				return nil
			},
		}, nil
	}

	acquired, err := mgr.AcquireAll(ctx, t.TempDir(), []Selected{{Name: "final"}})
	if !errors.Is(err, cause) {
		t.Fatalf("cancellation error = %v", err)
	}
	if acquired != nil {
		t.Fatalf("canceled acquisition returned credentials: %v", acquired)
	}
	if !slices.Equal(revoked, []string{"final"}) {
		t.Fatalf("rollback order = %v", revoked)
	}
}

func TestAcquireErrorsDoNotContainPayload(t *testing.T) {
	const secret = "do-not-print-this-secret"
	mgr := NewManager(t.TempDir())
	mgr.Run = func(context.Context, string, []string) ([]byte, error) {
		return []byte(secret), errors.New("credential helper failed")
	}
	_, err := mgr.AcquireAll(context.Background(), t.TempDir(), []Selected{{
		Name: "token",
		Spec: config.Credential{Source: config.CredentialSource{Type: "command", Argv: []string{"helper"}}},
	}})
	if err == nil {
		t.Fatal("expected acquisition error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed payload: %v", err)
	}
}
