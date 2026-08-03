package project

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestName(t *testing.T) {
	// slug + path-hash suffix
	got := Name("/Users/x/Projects/Foo.Bar")
	if !strings.HasPrefix(got, "coop-foo-bar-") || len(got) != len("coop-foo-bar-")+16 {
		t.Errorf("Name = %q, want coop-foo-bar-<16 hex>", got)
	}
	// "--" is reserved as the volume separator: names must never contain it
	if strings.Contains(Name("/x/weird--dir..name"), "--") {
		t.Errorf("Name contains reserved separator: %q", Name("/x/weird--dir..name"))
	}
	// deterministic
	first := Name("/a/api")
	if second := Name("/a/api"); first != second {
		t.Errorf("Name not deterministic: %q vs %q", first, second)
	}
	// same basename, different paths must NOT collide
	if Name("/work/client/api") == Name("/work/internal/api") {
		t.Errorf("basename collision: %q", Name("/work/client/api"))
	}
}

func TestNameBoundsLongProjectSlug(t *testing.T) {
	root := "/Users/u/Projects/" + strings.Repeat("very-long-project-", 30)
	name := Name(root)
	if got := len(name + VolumeSep + strings.Repeat("a", 63)); got > 255 {
		t.Fatalf("maximum volume name is %d characters: %q", got, name)
	}
	if len(name) > 255 {
		t.Fatalf("container name is %d characters", len(name))
	}
	sum := sha256.Sum256([]byte(root))
	if want := hex.EncodeToString(sum[:])[:16]; !strings.HasSuffix(name, "-"+want) {
		t.Fatalf("path hash identity was truncated: %q", name)
	}
}

// tempDir resolves macOS's /var -> /private/var symlink so expectations
// match git's resolved output.
func tempDir(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveMarkerWins(t *testing.T) {
	root := tempDir(t)
	sub := filepath.Join(root, "org", "repo", "deep")
	mustMkdir(t, sub)
	mustWrite(t, filepath.Join(root, "org", ".coop.toml"), "")
	// a git repo below the marker must NOT win
	gitInit(t, filepath.Join(root, "org", "repo"))

	got, err := Resolve(sub)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "org"); got != want {
		t.Errorf("Resolve = %q, want marker dir %q", got, want)
	}
}

func TestResolveCommittedConfigIsNotMarker(t *testing.T) {
	root := tempDir(t)
	project := filepath.Join(root, "project")
	sub := filepath.Join(project, "nested")
	mustMkdir(t, sub)
	mustWrite(t, filepath.Join(root, "coop.toml"), "")
	gitInit(t, project)

	got, err := Resolve(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != project {
		t.Errorf("Resolve = %q, want Git root %q", got, project)
	}
}

func TestResolveRejectsTrackedLocalConfig(t *testing.T) {
	root := tempDir(t)
	gitInit(t, root)
	mustWrite(t, filepath.Join(root, ".coop.toml"), "")
	cmd := exec.Command("git", "add", ".coop.toml")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		t.Skipf("git add unavailable: %v", err)
	}

	_, err := Resolve(root)
	if err == nil || !strings.Contains(err.Error(), "tracked") {
		t.Fatalf("tracked .coop.toml error = %v", err)
	}
}

func TestResolveRejectsCaseAliasedTrackedLocalConfig(t *testing.T) {
	root := tempDir(t)
	gitInit(t, root)
	trackedPath := filepath.Join(root, ".COOP.toml")
	mustWrite(t, trackedPath, "ssh = true\n")
	configInfo, err := os.Stat(filepath.Join(root, ".coop.toml"))
	if err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	trackedInfo, err := os.Stat(trackedPath)
	if err != nil || !os.SameFile(configInfo, trackedInfo) {
		t.Skip("filesystem does not alias case variants")
	}
	cmd := exec.Command("git", "add", ".COOP.toml")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		t.Skipf("git add unavailable: %v", err)
	}

	_, err = Resolve(root)
	if err == nil || !strings.Contains(err.Error(), "tracked") {
		t.Fatalf("case-aliased tracked .coop.toml error = %v", err)
	}
}

func TestResolveAllowsIgnoredLocalConfig(t *testing.T) {
	root := tempDir(t)
	gitInit(t, root)
	mustWrite(t, filepath.Join(root, ".gitignore"), "/.coop.toml\n")
	mustWrite(t, filepath.Join(root, ".coop.toml"), "")
	cmd := exec.Command("git", "add", ".gitignore")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		t.Skipf("git add unavailable: %v", err)
	}

	got, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("Resolve = %q, want ignored marker %q", got, root)
	}
}

func TestResolveRejectsLocalConfigWhenGitInspectionFails(t *testing.T) {
	root := tempDir(t)
	gitInit(t, root)
	mustWrite(t, filepath.Join(root, ".coop.toml"), "")
	t.Setenv("PATH", "")

	_, err := Resolve(root)
	if err == nil || !strings.Contains(err.Error(), "inspect Git repository") {
		t.Fatalf("Git inspection error = %v", err)
	}
}

func TestResolveBareLayout(t *testing.T) {
	root := tempDir(t)
	wt := filepath.Join(root, "proj", "main")
	mustMkdir(t, filepath.Join(root, "proj", ".bare"))
	mustMkdir(t, wt)
	gitInit(t, wt)

	got, err := Resolve(wt)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "proj"); got != want {
		t.Errorf("Resolve = %q, want bare container %q", got, want)
	}
}

func TestResolveGitToplevel(t *testing.T) {
	root := tempDir(t)
	repo := filepath.Join(root, "repo")
	sub := filepath.Join(repo, "src")
	mustMkdir(t, sub)
	gitInit(t, repo)

	got, err := Resolve(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != repo {
		t.Errorf("Resolve = %q, want git toplevel %q", got, repo)
	}
}

func TestResolveRefusesBroadRoots(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	for _, dir := range []string{home, filepath.Dir(home), "/"} {
		if _, err := Resolve(dir); err == nil {
			t.Errorf("Resolve(%q) should refuse — home/root would become a rw mount", dir)
		}
	}
}

func TestResolveFallbackPwd(t *testing.T) {
	dir := tempDir(t)
	got, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("Resolve = %q, want %q", got, dir)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
}
