package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMatchesProjectExactAndDescendant(t *testing.T) {
	root := t.TempDir()
	org := filepath.Join(root, "sarcasticbird")
	repo := filepath.Join(org, "coop")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if !matchesProject(org, "", org) {
		t.Error("a scope must match its own root")
	}
	if !matchesProject(org, "", repo) {
		t.Error("a scope must match a descendant")
	}
}

func TestMatchesProjectRespectsComponentBoundary(t *testing.T) {
	root := t.TempDir()
	foo := filepath.Join(root, "foo")
	foobar := filepath.Join(root, "foobar")
	for _, d := range []string{foo, foobar} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if matchesProject(foo, "", foobar) {
		t.Error("foo must not match foobar — prefix comparison must respect component boundaries")
	}
}

func TestMatchesProjectHandlesWorktreeDepth(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "org", "repo", "main")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if !matchesProject(filepath.Join(root, "org"), "", worktree) {
		t.Error("a scope must match a bare-repo worktree nested below <org>/<repo>/")
	}
}

func TestMatchesProjectExpandsHome(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "Projects", "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if !matchesProject("~/Projects", home, project) {
		t.Error("~ must expand against the host home")
	}
}

func TestMatchesProjectMissingPathFallsBackToLexical(t *testing.T) {
	// One trusted config may describe several machines: a match entry naming a
	// path absent here must simply not match, never fail.
	if !matchesProject("/nonexistent/org", "", "/nonexistent/org/repo") {
		t.Error("absent paths must still compare lexically")
	}
	if matchesProject("/nonexistent/org", "", "/nonexistent/other/repo") {
		t.Error("absent, unrelated paths must not match")
	}
}

func TestMatchesProjectEmptyRootNeverMatches(t *testing.T) {
	// `coop doctor` loads config with no project root.
	if matchesProject("/anything", "", "") {
		t.Error("an empty project root must never match")
	}
}

func TestMatchesProjectResolvesSymlinkedProject(t *testing.T) {
	// project.Resolve canonicalizes before Load, but a match entry naming a
	// symlinked path must still line up with the resolved project root.
	root := t.TempDir()
	real := filepath.Join(root, "real", "repo")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "real"), link); err != nil {
		t.Fatal(err)
	}
	if !matchesProject(link, "", real) {
		t.Error("a symlinked match entry must resolve to the same root")
	}
}

func TestMatchesProjectHandlesCaseAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("case aliases are a macOS filesystem behavior")
	}
	root := t.TempDir()
	real := filepath.Join(root, "MixedCase")
	project := filepath.Join(real, "repo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, strings.ToUpper(filepath.Base(real)))
	if _, err := os.Stat(alias); err != nil {
		t.Skip("temporary filesystem is case-sensitive")
	}
	if !matchesProject(alias, "", project) {
		t.Error("a case alias must resolve to the same project ancestor")
	}
}

func TestResolveMountRejectsCaseAliasedHome(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("case aliases are a macOS filesystem behavior")
	}
	home := t.TempDir()
	alias := filepath.Join(filepath.Dir(home), strings.ToUpper(filepath.Base(home)))
	if _, err := os.Stat(alias); err != nil {
		t.Skip("temporary filesystem is case-sensitive")
	}
	_, err := resolveMount(Mount{Source: alias}, home, filepath.Join(home, "Projects", "app"))
	if err == nil || !strings.Contains(err.Error(), "broad source") {
		t.Fatalf("case-aliased home mount error = %v", err)
	}
}

func TestResolveMountRejectsRuntimeGrammarAfterSymlinkExpansion(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "with,comma")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	_, err := resolveMount(Mount{Source: alias}, t.TempDir(), "")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("symlink-expanded mount grammar error = %v", err)
	}
}
