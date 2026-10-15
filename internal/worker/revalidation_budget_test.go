package worker

import (
	"context"
	"testing"
	"time"
)

type budgetedGitHub struct {
	*workflowGitHub
	budget time.Duration
}

func (g *budgetedGitHub) Revalidate(ctx context.Context, _ Job, s Snapshot) (Snapshot, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return Snapshot{}, ghError("unbounded_revalidation")
	}
	g.budget = time.Until(deadline)
	// A coherent goal, dependencies and PR require many independent reads.
	// Model their combined budget without sleeping or invoking live GitHub.
	if g.budget < 20*time.Second {
		return Snapshot{}, ghError("transport_failed")
	}
	return s, nil
}
func TestDispatchAllowsBoundedMultiReadRevalidation(t *testing.T) {
	e, g, code, clock, _ := engineFixture(t)
	fresh := &budgetedGitHub{workflowGitHub: g}
	e.GitHub = fresh
	tick(t, e, clock)
	if code.calls != 1 {
		t.Fatalf("valid multi-read input did not submit: budget=%s calls=%d", fresh.budget, code.calls)
	}
	if fresh.budget > time.Minute {
		t.Fatalf("revalidation was not bounded: %s", fresh.budget)
	}
}
func TestDispatchPreservesShorterParentDeadline(t *testing.T) {
	e, g, code, _, _ := engineFixture(t)
	fresh := &budgetedGitHub{workflowGitHub: g}
	e.GitHub = fresh
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if code.calls != 0 {
		t.Fatal("failed revalidation submitted work")
	}
	if fresh.budget <= 0 || fresh.budget > 2*time.Second {
		t.Fatalf("parent deadline replaced: %s", fresh.budget)
	}
}
