package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRejectsTrackedBackingFile(t *testing.T) {
	for _, link := range []string{"symlink", "hardlink"} {
		t.Run(link, func(t *testing.T) {
			root := tempDir(t)
			gitInit(t, root)
			backing := filepath.Join(root, "settings.toml")
			mustWrite(t, backing, "ssh = true\n")
			cmd := exec.Command("git", "-C", root, "add", "settings.toml")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("track fixture: %v: %s", err, out)
			}
			makeLink := os.Symlink
			if link == "hardlink" {
				makeLink = os.Link
			}
			if err := makeLink(backing, filepath.Join(root, ".coop.toml")); err != nil {
				t.Fatal(err)
			}
			if _, err := Resolve(root); err == nil || !strings.Contains(err.Error(), "tracked") {
				t.Fatalf("repository-controlled backing file accepted: %v", err)
			}
		})
	}
}

func TestResolveAllowsUnrelatedTrackedSymlinkCycle(t *testing.T) {
	root := tempDir(t)
	gitInit(t, root)
	mustWrite(t, filepath.Join(root, ".coop.toml"), "")
	mustWrite(t, filepath.Join(root, ".gitignore"), "/.coop.toml\n")
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", root, "add", ".gitignore", "loop")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("track fixture: %v: %s", err, out)
	}
	if got, err := Resolve(root); err != nil || got != root {
		t.Fatalf("unrelated symlink cycle blocked local config: %q %v", got, err)
	}
}
