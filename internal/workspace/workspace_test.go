package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sarcasticbird/coop/internal/runtime"
)

func TestSpecificationMismatchRequiresMaintenance(t *testing.T) {
	for _, state := range []runtime.State{runtime.StateRunning, runtime.StateStopped} {
		if err := CheckSpecification(state, "old", "new"); !errors.Is(err, ErrMaintenanceRequired) {
			t.Fatalf("state %v: %v", state, err)
		}
	}
	if err := CheckSpecification(runtime.StateAbsent, "", "new"); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRoundTripAndRejectsSymlink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := FileStore{Root: root}
	record := Record{Version: 1, Mode: Agent, Identity: "robin", RuntimeName: "coop-agent-robin", Fingerprint: "abc", Image: "test"}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(Agent, "robin")
	if err != nil || got.RuntimeName != "coop-agent-robin" {
		t.Fatalf("record=%+v err=%v", got, err)
	}
	info, err := os.Stat(filepath.Join(root, "agent", "robin.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %v %v", info, err)
	}
	if err := store.Save(Record{Version: 1, Mode: Agent, Identity: "../escape"}); err == nil {
		t.Fatal("traversal accepted")
	}
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(root, "interactive")); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(Record{Version: 1, Mode: Interactive, Identity: "coop-test", RuntimeName: "coop-test"}); err == nil {
		t.Fatal("symlink store accepted")
	}
}
