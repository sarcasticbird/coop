package worker

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type missingWtRunner struct{ calls [][]string }

func (r *missingWtRunner) Run(context.Context, []string, []string, io.Reader) (CommandResult, error) {
	return CommandResult{}, errors.New("wrong command path")
}
func (r *missingWtRunner) RunIn(ctx context.Context, dir string, args, env []string, input io.Reader) (CommandResult, error) {
	r.calls = append(r.calls, args)
	return CommandResult{}, errors.New("wt unavailable")
}
func TestCheckoutWTRequiresSupportedWorkflowWithoutFallback(t *testing.T) {
	c, s, n := policyFixture(t)
	c.CheckoutModes = map[string]string{"code/repo": "wt"}
	j := Select(c, s, State{}, n)[0]
	r := &missingWtRunner{}
	checkouts := NewCheckouts(c, r)
	if _, err := checkouts.Prepare(context.Background(), j); err == nil {
		t.Fatal("missing wt silently accepted")
	}
	if len(r.calls) != 1 || r.calls[0][0] != "wt" || r.calls[0][1] != "clone" {
		t.Fatal("bypassed supported wt workflow")
	}
}

func localGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git failed: %v", err)
	}
	return strings.TrimSpace(string(raw))
}
func TestCheckoutRecoveryPreservesDirtyTaskAndManualClone(t *testing.T) {
	c, s, n := policyFixture(t)
	j := Select(c, s, State{}, n)[0]
	root := filepath.Dir(c.StateDir)
	origin := filepath.Join(root, "origin")
	if os.Mkdir(origin, 0700) != nil {
		t.Fatal("mkdir")
	}
	localGit(t, origin, "init", "-b", "main")
	_ = os.WriteFile(filepath.Join(origin, "code.txt"), []byte("original"), 0600)
	localGit(t, origin, "add", "code.txt")
	localGit(t, origin, "commit", "-m", "fixture")
	checkouts := NewCheckouts(c, ExecRunner{})
	checkouts.origin = func(string) string { return origin }
	owned, err := checkouts.Prepare(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(owned.Checkout, "code.txt"), []byte("dirty task"), 0600)
	_ = os.WriteFile(filepath.Join(origin, "code.txt"), []byte("manual work"), 0600)
	recovered, err := checkouts.Prepare(context.Background(), owned)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Checkout != owned.Checkout {
		t.Fatal("task checkout changed")
	}
	for path, want := range map[string]string{filepath.Join(origin, "code.txt"): "manual work", filepath.Join(owned.Checkout, "code.txt"): "dirty task"} {
		raw, _ := os.ReadFile(path)
		if string(raw) != want {
			t.Fatal("recovery overwrote work")
		}
	}
	first, err := checkouts.Progress(context.Background(), owned)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(owned.Checkout, "code.txt"), []byte("more work"), 0600)
	second, err := checkouts.Progress(context.Background(), owned)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("dirty content progress not detected")
	}
	foreign := owned
	foreign.TaskURL = "https://github.com/code/repo/issues/99"
	if _, err := checkouts.Prepare(context.Background(), foreign); err == nil {
		t.Fatal("foreign checkout adopted")
	}
}
