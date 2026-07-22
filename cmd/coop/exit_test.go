package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sarcasticbird/coop/internal/runtime"
)

func TestExecutePreservesCoopProcessExitCodes(t *testing.T) {
	helperErr := exec.Command("sh", "-c", "exit 42").Run()
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "success", want: 0},
		{name: "infrastructure", err: errors.New("runtime unavailable"), want: 1},
		{name: "guest", err: &runtime.ExitError{Code: 23}, want: 23},
		{name: "guest with cleanup", err: errors.Join(&runtime.ExitError{Code: 23}, errors.New("cleanup failed")), want: 23},
		{name: "signal", err: &runtime.SignalError{Signal: syscall.SIGINT}, want: 130},
		{name: "credential helper subprocess", err: fmt.Errorf("credential helper failed: %w", helperErr), want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "test", SilenceErrors: true, SilenceUsage: true}
			cmd.RunE = func(*cobra.Command, []string) error { return tc.err }
			if got := execute(cmd); got != tc.want {
				t.Fatalf("exit code = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestExecuteOnlySuppressesBareGuestExit(t *testing.T) {
	bareOutput, bareCode := captureExecuteStderr(t, &runtime.ExitError{Code: 23})
	if bareCode != 23 || bareOutput != "" {
		t.Fatalf("bare guest exit: code=%d stderr=%q", bareCode, bareOutput)
	}

	joinedOutput, joinedCode := captureExecuteStderr(t, errors.Join(
		&runtime.ExitError{Code: 23},
		errors.New("lease cleanup failed"),
	))
	if joinedCode != 23 || joinedOutput == "" {
		t.Fatalf("joined guest exit: code=%d stderr=%q", joinedCode, joinedOutput)
	}
}

func TestExecuteSuppressesActualRuntimeGuestExit(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "container")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runtimeErr := (&runtime.Apple{Bin: bin}).ExecInteractive(
		context.Background(), "coop-x", "/work", []string{"tool"},
	)
	output, code := captureExecuteStderr(t, runtimeErr)
	if code != 23 || output != "" {
		t.Fatalf("actual runtime guest exit: code=%d stderr=%q error=%T", code, output, runtimeErr)
	}
}

func captureExecuteStderr(t *testing.T, executeErr error) (string, int) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = writer
	t.Cleanup(func() { os.Stderr = original })

	cmd := &cobra.Command{Use: "test", SilenceErrors: true, SilenceUsage: true}
	cmd.RunE = func(*cobra.Command, []string) error { return executeErr }
	code := execute(cmd)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stderr = original
	return string(raw), code
}
