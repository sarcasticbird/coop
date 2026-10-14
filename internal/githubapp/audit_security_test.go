package githubapp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditAliasBypassesCredentialPrintingGuard(t *testing.T) {
	if _, err := os.Stat(CoreGH); err != nil {
		t.Skip("fixed guest core gh executable unavailable")
	}
	root := t.TempDir()
	args := []string{"alias", "set", "audit-dump", "auth token"}
	if owner, err := SelectOwner(args, "code", ""); err != nil || owner != "code" {
		t.Fatalf("alias creation unexpectedly refused: %q %v", owner, err)
	}
	if _, err := SelectOwner([]string{"auth", "token"}, "code", ""); err == nil {
		t.Fatal("direct credential printing unexpectedly allowed")
	}
	if owner, err := SelectOwner([]string{"audit-dump"}, "code", ""); err != nil || owner != "code" {
		t.Fatalf("alias invocation unexpectedly refused: %q %v", owner, err)
	}
	now := time.Now()
	b := Bundle{document: wireBundle{Installations: map[string]token{"code": {Value: "audit-fixture-not-a-credential", ExpiresAt: now.Add(time.Hour)}}}}
	env, err := b.Environment("code", []string{"HOME=" + root, "PATH=/usr/bin:/bin", "GH_CONFIG_DIR=discarded"}, now)
	if err != nil {
		t.Fatal(err)
	}
	env = append(env, "GH_CONFIG_DIR="+filepath.Join(root, "config"))
	set := exec.Command(CoreGH, args...)
	set.Env = env
	if out, err := set.CombinedOutput(); err != nil {
		t.Fatalf("synthetic alias setup failed: %v %s", err, out)
	}
	dump := exec.Command(CoreGH, "audit-dump")
	dump.Env = env
	out, err := dump.Output()
	if err != nil || strings.TrimSpace(string(out)) != "audit-fixture-not-a-credential" {
		t.Fatalf("alias did not print the synthetic environment token: %v", err)
	}
}
