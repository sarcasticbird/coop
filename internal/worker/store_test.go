package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func storeFixture(t *testing.T) *FileStore {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewFileStore(filepath.Join(root, "worker"))
}
func TestStoreLockAndIntentRecovery(t *testing.T) {
	s := storeFixture(t)
	release, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if second, err := s.Lock(); err == nil {
		second()
		t.Fatal("two workers acquired one store")
	}
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	j := Job{ID: BodyHash("job"), Phase: "active", SessionID: "ses_task", MessageID: "msg_pending", SubmissionState: "submitting"}
	state.Jobs[j.ID] = j
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewFileStore(s.Dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Jobs[j.ID].MessageID != j.MessageID || loaded.InstanceID != state.InstanceID {
		t.Fatal("in-flight submission lost on recovery")
	}
	if paused, err := s.Paused(); err != nil || !paused {
		t.Fatal("pause control lost")
	}
	release()
	release()
	third, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	third()
}
func TestStoreRejectsSymlinksCorruptionAndWrongModes(t *testing.T) {
	for _, kind := range []string{"symlink root", "symlink state", "public state", "corrupt", "wrong version"} {
		t.Run(kind, func(t *testing.T) {
			s := storeFixture(t)
			release, err := s.Lock()
			if err != nil {
				t.Fatal(err)
			}
			release()
			path := filepath.Join(s.Dir, "state.json")
			switch kind {
			case "symlink root":
				moved := s.Dir + "-real"
				if os.Rename(s.Dir, moved) != nil {
					t.Fatal("rename")
				}
				if os.Symlink(moved, s.Dir) != nil {
					t.Fatal("symlink")
				}
			case "symlink state":
				target := filepath.Join(filepath.Dir(s.Dir), "manual")
				_ = os.WriteFile(target, []byte("manual data"), 0600)
				_ = os.Symlink(target, path)
			case "public state":
				_ = os.WriteFile(path, []byte(`{}`), 0644)
			case "corrupt":
				_ = os.WriteFile(path, []byte(`not json`), 0600)
			case "wrong version":
				_ = os.WriteFile(path, []byte(`{"Version":99}`), 0600)
			}
			if _, err := s.Load(); err == nil {
				t.Fatal("unsafe state accepted")
			}
		})
	}
}
func TestStoreAtomicSaveKeepsPreviousStateOnInvalidInput(t *testing.T) {
	s := storeFixture(t)
	release, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(s.Dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	bad := state
	bad.Version = 99
	if err := s.Save(bad); err == nil {
		t.Fatal("invalid state saved")
	}
	after, _ := os.ReadFile(filepath.Join(s.Dir, "state.json"))
	if string(before) != string(after) {
		t.Fatal("failed save damaged previous state")
	}
	_ = os.WriteFile(filepath.Join(s.Dir, ".state-abandoned"), []byte("partial"), 0600)
	if _, err := s.Load(); err != nil {
		t.Fatal("abandoned temp file prevented recovery")
	}
}
