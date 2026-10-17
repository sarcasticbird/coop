//go:build darwin || linux

package worker

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecRunnerCleansInheritedPipeChildren(t *testing.T) {
	for _, exitCode := range []string{"0", "1"} {
		t.Run("parent_exit_"+exitCode, func(t *testing.T) {
			testInheritedPipeChild(t, exitCode)
		})
	}
}

func testInheritedPipeChild(t *testing.T, exitCode string) {
	t.Helper()
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child.pid")
	_, runErr := (ExecRunner{Dir: dir}).Run(context.Background(), []string{"/bin/sh", "-c", `/bin/sleep 30 & printf '%s' "$!" > "$1"; exit "$2"`, "fixture", pidPath, exitCode}, nil, nil)
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		t.Fatal("invalid owned child PID")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if runErr == nil {
		t.Fatal("inherited pipe timeout was hidden")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		// Linux CI's PID 1 may leave an orphan zombie unreaped. It cannot run.
		if stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")); err == nil {
			_, rest, _ := strings.Cut(string(stat), ") ")
			if strings.HasPrefix(rest, "Z ") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("runner returned while its inherited-pipe child was still running")
}
