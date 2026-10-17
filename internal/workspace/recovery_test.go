package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryStoreRoundTripAndOwnership(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := FileStore{Root: root}
	for _, name := range []string{"../escape", "foreign--name"} {
		if err := s.SaveRecovery(Interactive, name, []byte("data")); err == nil {
			t.Fatalf("invalid identity %q accepted", name)
		}
	}
	if err := s.SaveRecovery(Interactive, "coop-test", []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRecovery(Interactive, "coop-test", []byte("updated")); err != nil {
		t.Fatal(err)
	}
	data, err := s.LoadRecovery(Interactive, "coop-test")
	if err != nil || string(data) != "updated" {
		t.Fatalf("LoadRecovery = %q, %v", data, err)
	}
	if records, err := s.List(); err != nil || len(records) != 0 {
		t.Fatalf("journal exposed as ownership record: %v, %v", records, err)
	}
	if err := s.ClearRecovery(Interactive, "coop-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadRecovery(Interactive, "coop-test"); !os.IsNotExist(err) {
		t.Fatalf("cleared journal remains: %v", err)
	}
	path := filepath.Join(root, "recovery", "interactive", "coop-test.json")
	other := filepath.Join(root, "foreign")
	if err := os.WriteFile(other, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadRecovery(Interactive, "coop-test"); err == nil {
		t.Fatal("journal symlink accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRecovery(Interactive, "coop-test", []byte("data")); err == nil {
		t.Fatal("journal directory symlink accepted")
	}
}
