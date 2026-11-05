package worker

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRetiredRuntimeLeavesHistoryAndWorkspaceUntouched(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			cfg, store, state, wakes, _, _ := legacyCompletedWakeFixture(t, "discussion")
			if version == 2 {
				if err := MigrateState(cfg, 1, 2, state.InstanceID); err != nil {
					t.Fatal(err)
				}
			}
			cfg.Version = version
			// These represent pending uncertainty, not permission to resume an old
			// protocol, delete evidence or manufacture a successful terminal state.
			for _, name := range []string{"unresolved-action.json", "pending-publication.json"} {
				if err := os.WriteFile(filepath.Join(store.Dir, name), []byte(`{"outcome":"unknown","session":"ses_original","request":"original"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(cfg.WorkRoot, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cfg.WorkRoot, "uncommitted-work"), []byte("preserve original work and session\n"), 0600); err != nil {
				t.Fatal(err)
			}
			before := map[string][]byte{}
			for _, dir := range []string{store.Dir, cfg.WorkRoot} {
				if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if entry.IsDir() {
						return nil
					}
					raw, err := os.ReadFile(path)
					before[path] = raw
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			code := &fakeOpenCode{}
			e := &Engine{Config: cfg, Store: NewVersionedFileStore(store.Dir, version), OpenCode: code}
			for range 2 {
				if err := e.Tick(t.Context()); !errors.Is(err, errLegacyRuntime) {
					t.Fatal(err)
				}
				if err := e.Run(t.Context()); !errors.Is(err, errLegacyRuntime) {
					t.Fatal(err)
				}
			}
			if code.calls != 0 || code.aborts != 0 || code.healthCalls != 0 {
				t.Fatal("retired runtime touched a coding session")
			}
			for path, raw := range before {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(raw, after) {
					t.Fatal("refusal changed retained data", path, err)
				}
			}
			// Historical diagnostics still use the retained codecs, without a
			// WakeCoordinator or a dispatch pass.
			summaries, err := WakeSummaries(NewVersionedWakeStore(wakes.Dir, version), 20, "")
			if err != nil || len(summaries) != 1 || summaries[0].Phase != "completed" {
				t.Fatal(summaries, err)
			}
		})
	}
}
