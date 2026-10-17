package worker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type interruptedBranchRunner struct {
	ExecRunner
	interrupt bool
}

func (r *interruptedBranchRunner) RunIn(ctx context.Context, dir string, argv, env []string, input io.Reader) (CommandResult, error) {
	if r.interrupt && len(argv) >= 3 && argv[0] == "git" && argv[1] == "switch" && argv[2] == "-c" {
		return CommandResult{}, errors.New("fixture interruption after clone")
	}
	return r.ExecRunner.RunIn(ctx, dir, argv, env, input)
}

func TestCheckoutRetriesInterruptedInitialization(t *testing.T) {
	c, snapshot, now := policyFixture(t)
	j := Select(c, snapshot, State{}, now)[0]
	origin := filepath.Join(t.TempDir(), "origin")
	if err := os.Mkdir(origin, 0700); err != nil {
		t.Fatal(err)
	}
	localGit(t, origin, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "code.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	localGit(t, origin, "add", "code.txt")
	localGit(t, origin, "commit", "-m", "fixture")
	runner := &interruptedBranchRunner{interrupt: true}
	checkouts := NewCheckouts(c, runner)
	checkouts.origin = func(string) string { return origin }
	if _, err := checkouts.Prepare(context.Background(), j); err == nil {
		t.Fatal("interruption was hidden")
	}
	runner.interrupt = false
	owned, err := checkouts.Prepare(context.Background(), j)
	if err != nil {
		t.Fatal("interrupted initialization remained stuck", err)
	}
	if got := localGit(t, owned.Checkout, "branch", "--show-current"); got != owned.Branch {
		t.Fatal("recovered checkout is on the wrong branch")
	}
	localGit(t, owned.Checkout, "switch", "main")
	if _, err = checkouts.Prepare(context.Background(), owned); err == nil {
		t.Fatal("recovery adopted a manually changed published checkout")
	}
}
