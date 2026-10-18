package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeCheckoutUsesApprovedBaseAndRejectsForeignPrincipal(t *testing.T) {
	cfg, _, catalog, _ := dispatchFixture(t)
	j := NativeCandidates(cfg, catalog.catalog, nil, time.Now())[0].Job
	j.Master.BaseBranch = "release"
	origin := filepath.Join(t.TempDir(), "origin")
	if err := os.Mkdir(origin, 0700); err != nil {
		t.Fatal(err)
	}
	localGit(t, origin, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "code.txt"), []byte("main"), 0600); err != nil {
		t.Fatal(err)
	}
	localGit(t, origin, "add", "code.txt")
	localGit(t, origin, "commit", "-m", "main fixture")
	localGit(t, origin, "switch", "-c", "release")
	if err := os.WriteFile(filepath.Join(origin, "code.txt"), []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	localGit(t, origin, "commit", "-am", "release fixture")
	localGit(t, origin, "switch", "main")
	checkouts := NewCheckouts(cfg, ExecRunner{})
	checkouts.origin = func(string) string { return origin }
	owned, err := checkouts.Prepare(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(owned.Checkout, "code.txt"))
	if err != nil || string(raw) != "release" {
		t.Fatalf("native checkout ignored approved base: %s %v", raw, err)
	}
	foreign := owned
	copy := *owned.Master
	copy.AgentID = "foreign"
	foreign.Master = &copy
	if _, err = checkouts.Prepare(context.Background(), foreign); err == nil {
		t.Fatal("foreign master principal adopted checkout")
	}
}

func TestPlacementCheckoutScopedDirectory(t *testing.T) {
	cfg, _, catalog, _ := dispatchFixture(t)
	job := NativeCandidates(cfg, catalog.catalog, nil, time.Now())[0].Job
	old, origin := placementCheckoutFixture(t, cfg, job)
	cfg.Master.Generation++
	verifyPlacementCheckout(t, cfg, old, origin)
}

func TestNativeFixWorktreeExactHead(t *testing.T) {
	if _, err := exec.LookPath("wt"); err != nil {
		t.Skip("supported wt workflow unavailable")
	}
	cfg, _, catalog, _ := dispatchFixture(t)
	job := NativeCandidates(cfg, catalog.catalog, nil, time.Now())[0].Job
	old, origin := placementCheckoutFixture(t, cfg, job)
	cfg.Master.Generation++
	cfg.CheckoutModes = map[string]string{"code/repo": "wt"}
	verifyPlacementCheckout(t, cfg, old, origin)
}
