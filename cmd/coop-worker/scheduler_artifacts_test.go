package main

import (
	"os/exec"
	"testing"
)

func TestSchedulerArtifactManifest(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 is required for offline artifact manifest fixtures")
	}
	command := exec.Command(python, "-B", "../../scripts/test_scheduler_artifacts.py", "-v")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("offline artifact manifest: %v\n%s", err, out)
	}
}
