package projectinit

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureLocalExcludeAddsAnchoredPatternWithoutTouchingGitignore(t *testing.T) {
	root := initGitRepository(t)
	gitignore := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(gitignore, []byte("tracked-content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(gitignore)
	if err != nil {
		t.Fatal(err)
	}

	if err := EnsureLocalExclude(root); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLocalExclude(root); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(gitignore)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf(".gitignore changed: %q != %q", after, before)
	}
	exclude, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(exclude), "/.coop.toml\n") != 1 {
		t.Fatalf("local exclude = %q", exclude)
	}
}

func TestEnsureLocalExcludeHandlesNestedIgnoredTrackedAndNonGitProjects(t *testing.T) {
	t.Run("nested project", func(t *testing.T) {
		root := initGitRepository(t)
		projectRoot := filepath.Join(root, "apps", "web")
		if err := os.MkdirAll(projectRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := EnsureLocalExclude(projectRoot); err != nil {
			t.Fatal(err)
		}
		exclude, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(exclude), "/apps/web/.coop.toml\n") {
			t.Fatalf("nested local exclude = %q", exclude)
		}
	})

	t.Run("nested project with Git ignore metacharacters", func(t *testing.T) {
		root := initGitRepository(t)
		for _, name := range []string{"[web]", "web*", "web?", `web\legacy`, "web "} {
			projectRoot := filepath.Join(root, "apps", name)
			if err := os.MkdirAll(projectRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := EnsureLocalExclude(projectRoot); err != nil {
				t.Fatalf("EnsureLocalExclude(%q): %v", name, err)
			}
			relative := filepath.ToSlash(filepath.Join("apps", name, ".coop.toml"))
			if output, err := gitOutput(root, "check-ignore", "-q", "--no-index", "--", relative); err != nil {
				t.Fatalf("literal project config path %q is not ignored: %v: %s", relative, err, output)
			}
		}
	})

	t.Run("already ignored", func(t *testing.T) {
		root := initGitRepository(t)
		if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/.coop.toml\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		excludePath := filepath.Join(root, ".git", "info", "exclude")
		before, err := os.ReadFile(excludePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := EnsureLocalExclude(root); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(excludePath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after, before) {
			t.Fatalf("already-effective exclude changed: %q != %q", after, before)
		}
	})

	t.Run("tracked config", func(t *testing.T) {
		root := initGitRepository(t)
		if err := os.WriteFile(filepath.Join(root, ".coop.toml"), []byte("# tracked\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, root, "add", ".coop.toml")
		if err := EnsureLocalExclude(root); err == nil || !strings.Contains(err.Error(), "tracked") {
			t.Fatalf("tracked config error = %v", err)
		}
	})

	t.Run("non git", func(t *testing.T) {
		root := t.TempDir()
		if err := EnsureLocalExclude(root); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("non-Git directory mutated: %v", err)
		}
	})

	t.Run("localized non git error", func(t *testing.T) {
		bin := t.TempDir()
		fakeGit := filepath.Join(bin, "git")
		script := `#!/bin/sh
if [ "$LC_ALL" = "C" ]; then
  printf '%s\n' 'fatal: not a git repository' >&2
else
  printf '%s\n' 'fatal: kein Git-Repository' >&2
fi
exit 128
`
		if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		t.Setenv("LC_ALL", "de_DE.UTF-8")
		if err := EnsureLocalExclude(t.TempDir()); err != nil {
			t.Fatalf("localized non-Git error was not recognized: %v", err)
		}
	})

	t.Run("missing info exclude", func(t *testing.T) {
		root := initGitRepository(t)
		infoDir := filepath.Join(root, ".git", "info")
		if err := os.RemoveAll(infoDir); err != nil {
			t.Fatal(err)
		}
		if err := EnsureLocalExclude(root); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(infoDir, "exclude"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "/.coop.toml\n" {
			t.Fatalf("created local exclude = %q", got)
		}
	})
}

func TestAppendConfigCreatesPrivateFileAndPreservesExistingBytesAndMode(t *testing.T) {
	block := []byte("[[volume]]\npath = \"web/node_modules\"\n")
	t.Run("new", func(t *testing.T) {
		root := t.TempDir()
		if err := AppendConfig(root, block); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, ".coop.toml")
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, block) || info.Mode().Perm() != 0o600 {
			t.Fatalf("new config = %q mode %o", got, info.Mode().Perm())
		}
	})

	t.Run("existing", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, ".coop.toml")
		existing := []byte("# preserve this byte-for-byte")
		if err := os.WriteFile(path, existing, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := AppendConfig(root, block); err != nil {
			t.Fatal(err)
		}
		want := append(append(bytes.Clone(existing), '\n'), block...)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) || info.Mode().Perm() != 0o640 {
			t.Fatalf("existing config = %q mode %o, want %q mode 640", got, info.Mode().Perm(), want)
		}
		if err := AppendConfig(root, block); err != nil {
			t.Fatal(err)
		}
		again, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again, want) {
			t.Fatalf("idempotent rerun changed config: %q", again)
		}
	})
}

func TestAppendConfigNoopAndFailuresLeaveTargetUntouched(t *testing.T) {
	t.Run("empty block", func(t *testing.T) {
		root := t.TempDir()
		if err := AppendConfig(root, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(root, ".coop.toml")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("empty append created config: %v", err)
		}
	})

	t.Run("symlink target", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "shared.toml")
		before := []byte("# shared config\n")
		if err := os.WriteFile(target, before, 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, ".coop.toml")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}

		err := AppendConfig(root, []byte("[[volume]]\npath = \"target\"\n"))
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("symlinked config error = %v", err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("project config symlink was replaced: mode %v", info.Mode())
		}
		after, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after, before) {
			t.Fatalf("symlink target changed: %q", after)
		}
	})

	for _, tc := range []struct {
		name string
		set  func(t *testing.T)
	}{
		{name: "read", set: func(t *testing.T) {
			original := readConfigFile
			readConfigFile = func(string) ([]byte, error) { return nil, errors.New("injected read") }
			t.Cleanup(func() { readConfigFile = original })
		}},
		{name: "write", set: func(t *testing.T) {
			original := writeConfigTemp
			writeConfigTemp = func(string, []byte, fs.FileMode) (string, error) { return "", errors.New("injected write") }
			t.Cleanup(func() { writeConfigTemp = original })
		}},
		{name: "rename", set: func(t *testing.T) {
			original := renameConfigFile
			renameConfigFile = func(string, string) error { return errors.New("injected rename") }
			t.Cleanup(func() { renameConfigFile = original })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".coop.toml")
			before := []byte("# original\n")
			if err := os.WriteFile(path, before, 0o600); err != nil {
				t.Fatal(err)
			}
			tc.set(t)
			if err := AppendConfig(root, []byte("[[volume]]\npath = \"target\"\n")); err == nil || !strings.Contains(err.Error(), "injected") {
				t.Fatalf("failure = %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("target changed after failure: %q", after)
			}
			matches, err := filepath.Glob(filepath.Join(root, ".coop.toml.tmp-*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("temporary files remain: %v, %v", matches, err)
			}
		})
	}
}

func initGitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	return root
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
