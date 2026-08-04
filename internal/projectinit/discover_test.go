package projectinit

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
)

func TestDiscoverFindsPlatformSpecificTreesWithoutDescendingIntoThem(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "package.json", `{}`)
	writeProjectFile(t, root, "apps/web/package.json", `{}`)
	writeProjectFile(t, root, "Cargo.toml", "[workspace]\nmembers = [\"crates/*\"]\n")
	writeProjectFile(t, root, "crates/api/Cargo.toml", "[package]\nname = \"api\"\n")
	writeProjectFile(t, root, ".git/ignored/package.json", `{}`)
	writeProjectFile(t, root, ".flox/ignored/package.json", `{}`)
	writeProjectFile(t, root, "node_modules/dependency/package.json", `{}`)
	writeProjectFile(t, root, "target/generated/package.json", `{}`)
	writeProjectFile(t, root, ".venv/pyvenv.cfg", "")
	writeProjectFile(t, root, "services/worker/venv/pyvenv.cfg", "")
	writeProjectFile(t, root, "vendor-cache/nested/package.json", `{}`)
	outside := t.TempDir()
	writeProjectFile(t, outside, "package.json", `{}`)
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(root, []config.Volume{{Path: "apps/web/node_modules"}, {Path: "vendor-cache"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".venv", "node_modules", "services/worker/venv", "target"}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates = %q, want %q", got, want)
	}
}

func TestDiscoverBoundsCandidates(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxCandidates+1; i++ {
		writeProjectFile(t, root, fmt.Sprintf("package-%03d/package.json", i), `{}`)
	}
	got, err := Discover(root, nil)
	if err == nil || !strings.Contains(err.Error(), "64") {
		t.Fatalf("candidates = %d, error = %v", len(got), err)
	}
}

func TestDiscoverSkipsSymlinkedCargoManifest(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeProjectFile(t, outside, "Cargo.toml", "[workspace]\nmembers = []\n")
	if err := os.Symlink(filepath.Join(outside, "Cargo.toml"), filepath.Join(root, "Cargo.toml")); err != nil {
		t.Fatal(err)
	}

	got, err := Discover(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("symlinked Cargo.toml produced candidates: %q", got)
	}
}

func TestDiscoverSkipsUnreadableDirectory(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "package.json", `{}`)
	writeProjectFile(t, root, "private/nested/package.json", `{}`)
	private := filepath.Join(root, "private")
	if err := os.Chmod(private, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(private, 0o755) })

	got, err := Discover(root, nil)
	if err != nil {
		t.Fatalf("unreadable directory aborted discovery: %v", err)
	}
	want := []string{"node_modules"}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates = %q, want %q", got, want)
	}
}

func writeProjectFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
