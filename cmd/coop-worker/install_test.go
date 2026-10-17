package main

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestGuestInstallRecovery(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("guest transaction fixtures require Linux coreutils")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 is required for guest transaction fixtures")
	}
	command := exec.Command(python, "-B", "../../scripts/test_worker_install_recovery.py", "-v")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("guest installation recovery: %v\n%s", err, out)
	}
}
