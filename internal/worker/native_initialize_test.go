package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeInitializationNeedsNoLegacyArtifacts(t *testing.T) {
	cfg, _, _ := masterMigrationFixture(t)
	cfg.StateDir = filepath.Join(t.TempDir(), "fresh")
	cfg.TaskSource = &TaskSourceConfig{Provider: "kata", ProjectUID: providerProject}
	if _, err := InitializeTeamState(cfg); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "state.json" && entry.Name() != "worker.lock" {
			t.Fatal("native initialization created legacy machinery", entry.Name())
		}
	}
	if err := checkMasterInstall(cfg, ""); err != nil {
		t.Fatal("native storage cannot be installed", err)
	}
	path := filepath.Join(cfg.StateDir, "state.json")
	before, _ := os.ReadFile(path)
	if _, err := InitializeTeamState(cfg); err == nil {
		t.Fatal("reinitialized existing native identity")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("refused initialization rewrote state")
	}
}

func TestNativeInitializationCannotHideLegacyOrPartialMigration(t *testing.T) {
	for _, artifact := range []string{"migration.json", "migration-backup.json", "master-migration.json", "master-migration-backup.json", "master-legacy-retirement.json", "wake"} {
		t.Run(artifact, func(t *testing.T) {
			cfg, _, _ := masterMigrationFixture(t)
			cfg.StateDir = filepath.Join(t.TempDir(), "fresh")
			if _, err := InitializeTeamState(cfg); err != nil {
				t.Fatal(err)
			}
			if err := atomicPrivate(filepath.Join(cfg.StateDir, artifact), []byte("{}")); err != nil {
				t.Fatal(err)
			}
			if _, err := NewVersionedFileStore(cfg.StateDir, 3).Load(); err == nil {
				t.Fatal("legacy/partial migration evidence was ignored")
			}
		})
	}
}
