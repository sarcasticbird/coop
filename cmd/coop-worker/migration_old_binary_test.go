package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/worker"
)

// The compatibility lane supplies a binary built from the pre-migration source.
func TestMigrationSavedOldBinaryRefusesV2(t *testing.T) {
	binary := os.Getenv("COOP_TEST_OLD_WORKER")
	if binary == "" {
		t.Skip("set COOP_TEST_OLD_WORKER to a saved v1 binary")
	}
	path, c, state := cliMigrationFixture(t, 1)
	c.Version = 2
	if err := worker.MigrateState(c, 1, 2, state.InstanceID); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(binary, "dry-run", "--config", path).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "invalid worker state") {
		t.Fatalf("saved binary did not reject v2 ledger: %v %s", err, output)
	}
}
