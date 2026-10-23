package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/githubapp"
)

func TestGHAliasCredentialGuard(t *testing.T) {
	gh := testGH(t)
	config := t.TempDir()
	if err := os.WriteFile(filepath.Join(config, "config.yml"), []byte("aliases:\n    leak: auth token\n    shell-leak: '!printf %s \"$GH_TOKEN\"'\n    login-alias: auth login\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth", "leak", "shell-leak", "login-alias"} {
		t.Run(name, func(t *testing.T) {
			args := []string{name}
			if name == "auth" {
				args = append(args, "token")
			}
			if _, err := githubapp.SelectOwner(args, "example", ""); err != nil {
				t.Log("blocked before credential injection")
				return
			}
			env := []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GH_CONFIG_DIR=" + config, "GH_TOKEN=synthetic-noncredential-alias-test", "GH_HOST=github.com", "GH_PROMPT_DISABLED=1"}
			if name == "leak" || name == "shell-leak" {
				// Positive control: the pre-fix dispatch exposes this synthetic token.
				cmd := exec.Command(gh, args...)
				cmd.Env = env
				out, err := cmd.CombinedOutput()
				if err != nil || string(out) != "synthetic-noncredential-alias-test" && string(out) != "synthetic-noncredential-alias-test\n" {
					t.Fatalf("alias fixture did not reproduce the bypass: %v", err)
				}
			}
			var out strings.Builder
			err := runGH(gh, args, env, nil, &out, &out)
			if strings.Contains(out.String(), "synthetic-noncredential-alias-test") {
				t.Fatal("alias printed the synthetic token")
			}
			if err == nil {
				t.Fatal("alias unexpectedly succeeded")
			}
			if !strings.Contains(out.String(), "unknown command") {
				t.Fatalf("expected alias refusal, got %q (%v)", out.String(), err)
			}
		})
	}
}

func TestGHConfigDoesNotPersistAliases(t *testing.T) {
	gh := testGH(t)
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GH_TOKEN=synthetic-noncredential-alias-test", "GH_HOST=github.com", "GH_PROMPT_DISABLED=1"}
	for _, args := range [][]string{{"alias", "set", "leak", "auth token"}, {"leak"}, {"api", "repos/example/repo", "--help"}, {"pr", "list", "--help", "-R", "example/repo"}, {"project", "list", "--help", "--owner", "example"}} {
		owner, err := githubapp.SelectOwner(args, "example", "")
		if err != nil || owner != "example" {
			t.Fatalf("owner routing changed: %q %v", owner, err)
		}
		var out strings.Builder
		err = runGH(gh, args, env, nil, &out, &out)
		if args[0] == "leak" {
			if err == nil || !strings.Contains(out.String(), "unknown command") || strings.Contains(out.String(), "synthetic-noncredential-alias-test") {
				t.Fatal("alias persisted between invocations")
			}
		} else if err != nil {
			t.Fatalf("legitimate command failed: %v: %v (%s)", args, err, out.String())
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("temporary gh configuration was not removed: %v", err)
		}
	}
	if _, err := githubapp.SelectOwner([]string{"pr", "list", "-R", "other/repo"}, "example", ""); err == nil {
		t.Fatal("conflicting owner accepted")
	}
}

func testGH(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat(githubapp.CoreGH); err == nil {
		return githubapp.CoreGH
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Skip("GitHub CLI is unavailable")
	}
	return gh
}
