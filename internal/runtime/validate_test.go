package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNotifyContextSecondSignalTerminates(t *testing.T) {
	const helperEnv = "COOP_NOTIFY_CONTEXT_SECOND_SIGNAL_HELPER"
	if os.Getenv(helperEnv) == "1" {
		ctx, stopSignals := NotifyContext(context.Background())
		defer stopSignals()
		fmt.Println("ready")
		<-ctx.Done()
		fmt.Println("canceled")
		select {}
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestNotifyContextSecondSignalTerminates$")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatalf("helper readiness = %q, %v", scanner.Text(), scanner.Err())
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() || scanner.Text() != "canceled" {
		t.Fatalf("helper cancellation = %q, %v", scanner.Text(), scanner.Err())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case err := <-waitDone:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("second signal result = %v", err)
		}
		status, ok := exitErr.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
			t.Fatalf("second signal status = %v", status)
		}
	case <-time.After(time.Second):
		_ = cmd.Process.Kill()
		<-waitDone
		t.Fatal("second signal did not terminate helper")
	}
}

func TestValidateMountFieldRejectsGrammarInjection(t *testing.T) {
	// the documented attack: a checkout path that smuggles a second
	// source/target directive into the comma-delimited --mount grammar
	evil := "/Users/u/Projects/x,source=/Users,target=/host"
	if err := ValidateMountField(evil); err == nil {
		t.Fatal("mount grammar injection accepted")
	}
	for _, bad := range []string{"a=b", "a,b", ""} {
		if err := ValidateMountField(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := ValidateMountField("/Users/u/Projects/normal path/repo"); err != nil {
		t.Errorf("legitimate path rejected: %v", err)
	}
}

func TestRunRejectsInjectedMounts(t *testing.T) {
	a := NewApple()
	err := a.Run(RunSpec{
		Name: "x", Image: "i", CPUs: 1, Memory: "1G",
		Mounts: []Mount{{Source: "/p,source=/Users,target=/host", Target: "/p"}},
	})
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("injected mount reached the runtime: %v", err)
	}
}

func TestRunRejectsVolumeGrammarColon(t *testing.T) {
	a := NewApple()
	err := a.Run(RunSpec{
		Name: "x", Image: "i", CPUs: 1, Memory: "1G",
		Volumes: []Volume{{Name: "coop-x", Target: "/home/u/.agent:ro"}},
	})
	if err == nil || !strings.Contains(err.Error(), "reserved by -v grammar") {
		t.Fatalf("volume grammar colon reached the runtime: %v", err)
	}
}

func TestVolumeNamesFromListFailsClosedOnMissingID(t *testing.T) {
	for _, input := range []string{
		`[{"id":""}]`,
		`[{"name":"coop-x"}]`,
		`[{"id":"coop-x"},{}]`,
	} {
		if names, err := volumeNamesFromList([]byte(input)); err == nil || names != nil {
			t.Errorf("accepted inconclusive volume list %s: names=%v err=%v", input, names, err)
		}
	}
	names, err := volumeNamesFromList([]byte(`[{"id":"coop-x"}]`))
	if err != nil || len(names) != 1 || names[0] != "coop-x" {
		t.Fatalf("valid volume list rejected: %v %v", names, err)
	}
}

func TestRuntimeQueryErrorsIncludeStderr(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "container")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho query-detail >&2\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Apple{Bin: bin}
	_, err := a.State("coop-x")
	if err == nil || !strings.Contains(err.Error(), "query-detail") {
		t.Fatalf("query stderr not captured: %v", err)
	}
	_, err = a.GuestFileExists("coop-x", "/x")
	if err == nil || !strings.Contains(err.Error(), "query-detail") {
		t.Fatalf("guest query stderr not captured: %v", err)
	}
	err = a.EnsureVolume("coop-x")
	if err == nil || !strings.Contains(err.Error(), "query-detail") {
		t.Fatalf("volume query error collapsed into absence: %v", err)
	}
}

func TestImageExistsUsesDenormalizedQuietOutput(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "container")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'alpine:latest\\ncoop:latest\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Apple{Bin: bin}
	exists, err := a.ImageExists("coop:latest")
	if err != nil || !exists {
		t.Fatalf("short image reference not found in quiet output: exists=%v err=%v", exists, err)
	}
	exists, err = a.ImageExists("coop:missing")
	if err != nil || exists {
		t.Fatalf("missing image reference result: exists=%v err=%v", exists, err)
	}
}

func TestExecInteractiveReturnsGuestExitCode(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "container")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := (&Apple{Bin: bin}).ExecInteractive(context.Background(), "coop-x", "/work", []string{"tool", "a b", "$x"})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("exit error = %#v", err)
	}
	if _, ok := err.(*ExitError); !ok {
		t.Fatalf("lone guest exit was wrapped: %T", err)
	}
}

func TestExecInteractiveTranslatesSignalExitCode(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "container")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nkill -TERM $$\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := (&Apple{Bin: bin}).ExecInteractive(context.Background(), "coop-x", "/work", []string{"tool"})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 143 {
		t.Fatalf("signal exit error = %#v", err)
	}
}

func TestExecInteractiveForwardsPIDDirectedSignals(t *testing.T) {
	t.Run("interrupt", func(t *testing.T) {
		testExecInteractiveForwardsPIDSignal(t, syscall.SIGINT, "INT", 42)
	})
	t.Run("hangup", func(t *testing.T) {
		testExecInteractiveForwardsPIDSignal(t, syscall.SIGHUP, "HUP", 43)
	})
}

func testExecInteractiveForwardsPIDSignal(t *testing.T, sig syscall.Signal, trap string, code int) {
	t.Helper()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	bin := filepath.Join(dir, "container")
	script := "#!/bin/sh\ntrap 'exit " + strconv.Itoa(code) + "' " + trap + "\nfor ready do :; done\n: > \"$ready\"\nwhile :; do :; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	ctx, stopSignals := NotifyContext(context.Background())
	defer stopSignals()
	go func() {
		result <- (&Apple{Bin: bin}).ExecInteractive(ctx, "coop-x", "/work", []string{"tool", ready})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("interactive child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-result:
		var exitErr *ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != code {
			t.Fatalf("signal exit error = %#v", err)
		}
	case <-time.After(time.Second):
		// ExecInteractive still owns SIGTERM here, so use it to stop the
		// child before failing instead of leaking a busy subprocess.
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		<-result
		t.Fatalf("PID-directed %s was not forwarded", trap)
	}
}

func TestExecInteractiveUsesDedicatedProcessGroup(t *testing.T) {
	dir := t.TempDir()
	groupFile := filepath.Join(dir, "pgrp")
	bin := filepath.Join(dir, "container")
	script := "#!/bin/sh\nfor output do :; done\nps -o pgid= -p $$ > \"$output\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (&Apple{Bin: bin}).ExecInteractive(context.Background(), "coop-x", "/work", []string{"tool", groupFile}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(groupFile)
	if err != nil {
		t.Fatal(err)
	}
	childGroup, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if childGroup == syscall.Getpgrp() {
		t.Fatalf("interactive child shares Coop process group %d", childGroup)
	}
}

func TestExecInteractiveStopsAndContinuesWithChildJob(t *testing.T) {
	if _, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP); err != nil {
		t.Skip("requires a controlling terminal")
	}
	dir := t.TempDir()
	resumed := filepath.Join(dir, "resumed")
	bin := filepath.Join(dir, "container")
	script := "#!/bin/sh\nfor resumed do :; done\nkill -TSTP $$\nprintf resumed > \"$resumed\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	watchdog := continueStoppedTestProcess(t)
	if err := (&Apple{Bin: bin}).ExecInteractive(context.Background(), "coop-x", "/work", []string{"tool", resumed}); err != nil {
		t.Fatal(err)
	}
	if err := watchdog.Wait(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(resumed); err != nil || string(raw) != "resumed" {
		t.Fatalf("continued child marker = %q, %v", raw, err)
	}
}

func continueStoppedTestProcess(t *testing.T) *exec.Cmd {
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
	watchdog := exec.Command("sh", "-c", script, "coop-job-watchdog", strconv.Itoa(os.Getpid()))
	watchdog.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := watchdog.Start(); err != nil {
		t.Fatal(err)
	}
	return watchdog
}

func TestInteractiveSignalsIncludeHangup(t *testing.T) {
	if !slices.Contains(interactiveSignals(), os.Signal(syscall.SIGHUP)) {
		t.Fatal("interactive signal relay does not include SIGHUP")
	}
}

func TestRelayInteractiveCancellationReportsFailureAndKillsChild(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(&SignalError{Signal: syscall.SIGINT})
	killed := false
	err := relayInteractiveCancellation(
		ctx,
		123,
		make(chan struct{}),
		func(group int, sig syscall.Signal) error {
			if group != -123 || sig != syscall.SIGINT {
				t.Fatalf("relay target = (%d, %s)", group, sig)
			}
			return syscall.EPERM
		},
		func() error {
			killed = true
			return nil
		},
	)
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("relay error = %v", err)
	}
	if !killed {
		t.Fatal("relay failure did not trigger direct child kill")
	}
}

func TestRelayInteractiveCancellationIgnoresExitedGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := relayInteractiveCancellation(
		ctx,
		123,
		make(chan struct{}),
		func(int, syscall.Signal) error { return syscall.ESRCH },
		func() error {
			t.Fatal("fallback kill called for exited group")
			return nil
		},
	)
	if err != nil {
		t.Fatalf("exited group relay error = %v", err)
	}
}

func TestRelayInteractiveCancellationEscalatesToGroupKill(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var signals []syscall.Signal
	err := relayInteractiveCancellation(
		ctx,
		123,
		make(chan struct{}),
		func(group int, sig syscall.Signal) error {
			if group != -123 {
				t.Fatalf("relay group = %d", group)
			}
			signals = append(signals, sig)
			return nil
		},
		func() error {
			t.Fatal("direct child kill called after successful group kill")
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(signals, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("relay signals = %v", signals)
	}
}

func TestExecInteractivePreservesArgv(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "container")
	captured := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + captured + "\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	argv := []string{"tool", "arg with spaces", "$literal", "semi;colon"}
	if err := (&Apple{Bin: bin}).ExecInteractive(context.Background(), "coop-x", "/work tree", argv); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	want := append([]string{"exec", "-it", "-w", "/work tree", "coop-x"}, argv...)
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}
