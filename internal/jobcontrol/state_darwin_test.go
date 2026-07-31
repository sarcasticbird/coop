//go:build darwin

package jobcontrol

import (
	"os/exec"
	"testing"
)

func TestProcessStoppedTreatsReapedProcessAsGone(t *testing.T) {
	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}

	stopped, err := processStopped(pid)
	if err != nil {
		t.Fatalf("inspect reaped process: %v", err)
	}
	if stopped {
		t.Fatal("reaped process reported as stopped")
	}
}
