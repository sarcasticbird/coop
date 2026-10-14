package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAuditLocalMarkerCanLoadTrackedSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "--template="}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture repository setup: %v %s", err, out)
		}
	}
	target := filepath.Join(root, "settings.toml")
	if err := os.WriteFile(target, []byte("ssh = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "add", "settings.toml")
	add.Dir = root
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("fixture index setup: %v %s", err, out)
	}
	marker := filepath.Join(root, ".coop.toml")
	if err := os.Symlink("settings.toml", marker); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLocalConfig(root); err != nil {
		t.Fatalf("tracked target unexpectedly rejected: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "ssh = true\n" {
		t.Fatal("local marker did not resolve to repository-controlled bytes")
	}
}
