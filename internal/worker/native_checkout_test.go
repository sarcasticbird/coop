package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestNativeRookCheckoutExactHeadAndPushProtection(t *testing.T) {
	for _, fault := range []string{"none", "head", "push", "role", "identity"} {
		t.Run(fault, func(t *testing.T) {
			cfg, master, cat, _ := dispatchFixture(t)
			job := NativeCandidates(cfg, cat.catalog, nil, time.Now())[0].Job
			selectNativeRook(&cfg, master)
			job.Kind, job.Master.Role, job.Master.AgentID, job.Master.ExecutorID = "deliver", "delivery", "rook", cfg.Self.ID
			job.PRURL = "https://github.com/code/repo/pull/1"
			origin := filepath.Join(t.TempDir(), "origin")
			if err := os.Mkdir(origin, 0700); err != nil {
				t.Fatal(err)
			}
			localGit(t, origin, "init", "-b", "main")
			if err := os.WriteFile(filepath.Join(origin, "code.txt"), []byte("base"), 0600); err != nil {
				t.Fatal(err)
			}
			localGit(t, origin, "add", "code.txt")
			localGit(t, origin, "commit", "-m", "base")
			base := localGit(t, origin, "rev-parse", "HEAD")
			localGit(t, origin, "switch", "-c", "source")
			if err := os.WriteFile(filepath.Join(origin, "code.txt"), []byte("reviewed head"), 0600); err != nil {
				t.Fatal(err)
			}
			localGit(t, origin, "commit", "-am", "source")
			job.HeadSHA = localGit(t, origin, "rev-parse", "HEAD")
			localGit(t, origin, "update-ref", "refs/pull/1/head", job.HeadSHA)
			localGit(t, origin, "switch", "main")
			co := NewCheckouts(cfg, ExecRunner{})
			co.origin = func(string) string { return origin }
			switch fault {
			case "head":
				job.HeadSHA = base
			case "role":
				job.Master.Role = "implementer"
			case "identity":
				co.config.Name = "alias-rook"
			}
			owned, err := co.Prepare(t.Context(), job)
			if fault == "head" || fault == "role" || fault == "identity" {
				if err == nil {
					t.Fatal("unsafe delivery checkout admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if head := localGit(t, owned.Checkout, "rev-parse", "HEAD"); head != job.HeadSHA || head == base {
				t.Fatal("Rook not at reviewed head", head)
			}
			if push := localGit(t, owned.Checkout, "remote", "get-url", "--push", "origin"); !strings.HasPrefix(push, "/dev/null/") {
				t.Fatal("Rook push not disabled", push)
			}
			if fault == "push" {
				localGit(t, owned.Checkout, "config", "remote.origin.pushurl", origin)
				owned.ContinuationMessageID = "msg_previous"
				if _, err := co.Prepare(t.Context(), owned); err == nil {
					t.Fatal("tampered push URL accepted")
				}
			}
		})
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

func TestNativeReviewContinuationPreservesDirtyCheckout(t *testing.T) {
	for _, kind := range []string{"review", "qa"} {
		t.Run(kind, func(t *testing.T) {
			cfg, _, g, _ := dispatchFixture(t)
			j := NativeCandidates(cfg, g.catalog, nil, time.Now())[0].Job
			j.Kind = kind
			j.PRURL = "https://github.com/code/repo/pull/1"
			j.Master.Role = "engineering-review"
			origin := filepath.Join(t.TempDir(), "origin")
			if err := os.Mkdir(origin, 0700); err != nil {
				t.Fatal(err)
			}
			localGit(t, origin, "init", "-b", "main")
			if err := os.WriteFile(filepath.Join(origin, "code.txt"), []byte("base"), 0600); err != nil {
				t.Fatal(err)
			}
			localGit(t, origin, "add", "code.txt")
			localGit(t, origin, "commit", "-m", "fixture")
			j.HeadSHA = localGit(t, origin, "rev-parse", "HEAD")
			localGit(t, origin, "update-ref", "refs/pull/1/head", j.HeadSHA)
			co := NewCheckouts(cfg, ExecRunner{})
			co.origin = func(string) string { return origin }
			owned, err := co.Prepare(t.Context(), j)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(owned.Checkout, "code.txt")
			if err = os.WriteFile(file, []byte("test fixture changes"), 0600); err != nil {
				t.Fatal(err)
			}
			report := filepath.Join(owned.Checkout, "report.txt")
			if err = os.WriteFile(report, []byte("review evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			owned.ContinuationMessageID = "msg_previous"
			if _, err = co.Prepare(t.Context(), owned); err != nil {
				t.Fatal("dirty continuation rejected", err)
			}
			raw, err := os.ReadFile(file)
			if err != nil || string(raw) != "test fixture changes" {
				t.Fatal("continuation reset local changes", err)
			}
			if _, err = os.Stat(report); err != nil {
				t.Fatal("continuation removed report", err)
			}
		})
	}
}
