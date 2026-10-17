package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

type claimBudgetGitHub struct {
	*teamEngineGitHub
	check func(context.Context) error
	calls int
}

func (g *claimBudgetGitHub) Claim(ctx context.Context, j Job, instance string) (Claim, error) {
	g.calls++
	if err := g.check(ctx); err != nil {
		return Claim{}, err
	}
	return g.teamEngineGitHub.Claim(ctx, j, instance)
}

type claimBudgetCheckouts struct {
	fakeCheckouts
	calls int
}

func (c *claimBudgetCheckouts) Prepare(ctx context.Context, j Job) (Job, error) {
	c.calls++
	return c.fakeCheckouts.Prepare(ctx, j)
}

func claimBudgetFixture(t *testing.T, kind string) (*Engine, *claimBudgetGitHub, *fakeOpenCode, *claimBudgetCheckouts, Job) {
	t.Helper()
	var e *Engine
	var g *teamEngineGitHub
	var code *fakeOpenCode
	var j Job
	if kind == "qa" {
		c, s, now := qaGateFixture(t)
		c = Defaults(qaGateActor(c, "qa"))
		s.PRs[0].Reviews = s.PRs[0].Reviews[:1]
		j = qaGateSelected(t, c, s, now, "qa")
		state := newState()
		state.Version, state.SnapshotCache = 2, s
		state.Attempts = map[string]int{}
		g = &teamEngineGitHub{workflowGitHub: &workflowGitHub{snapshot: s}, actor: c.Self}
		code = &fakeOpenCode{}
		e = &Engine{Config: c, GitHub: g, Store: &memoryStore{}, OpenCode: code, Clock: &fakeClock{now: now}, state: state}
	} else {
		e, g, code, j = teamEngineFixture(t, false, kind)
	}
	checkouts := &claimBudgetCheckouts{}
	github := &claimBudgetGitHub{teamEngineGitHub: g}
	e.GitHub, e.Checkouts = github, checkouts
	return e, github, code, checkouts, j
}

func TestTeamEngineClaimBudget(t *testing.T) {
	for _, kind := range []string{"implement", "review", "qa"} {
		t.Run(kind, func(t *testing.T) {
			e, g, code, checkouts, j := claimBudgetFixture(t, kind)
			started := time.Now()
			g.check = func(ctx context.Context) error {
				deadline, ok := ctx.Deadline()
				if !ok || deadline.After(time.Now().Add(time.Minute)) {
					t.Fatal("claim lacks the bounded validation deadline")
				}
				// Model the real multi-request validation without sleeping.
				if deadline.Before(started.Add(11 * time.Second)) {
					return context.DeadlineExceeded
				}
				return nil
			}
			if err := e.dispatch(context.Background(), j); err != nil {
				t.Fatal(err)
			}
			if g.calls != 1 || g.claims != 1 || g.reads < 2 || checkouts.calls != 1 || code.calls != 1 {
				t.Fatalf("claim calls=%d effects=%d reads=%d checkouts=%d submits=%d job=%+v", g.calls, g.claims, g.reads, checkouts.calls, code.calls, e.state.Jobs[j.ID])
			}
			if got := e.state.Jobs[j.ID]; got.Binding == nil || *got.Binding != *j.Binding {
				t.Fatal("submission changed the job binding")
			}
		})
	}
}

func TestTeamEngineClaimParentDeadline(t *testing.T) {
	for _, kind := range []string{"implement", "review", "qa"} {
		t.Run(kind, func(t *testing.T) {
			e, g, code, checkouts, j := claimBudgetFixture(t, kind)
			deadline := time.Now().Add(5 * time.Second)
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			g.check = func(claimCtx context.Context) error {
				if got, ok := claimCtx.Deadline(); !ok || !got.Equal(deadline) {
					t.Fatalf("claim extended parent deadline: %v, %v", got, ok)
				}
				cancel()
				return claimCtx.Err()
			}
			if err := e.dispatch(ctx, j); err != nil {
				t.Fatal(err)
			}
			if g.calls != 1 || g.claims != 0 || checkouts.calls != 0 || code.calls != 0 {
				t.Fatal("cancelled claim reached checkout or submission")
			}
		})
	}
}

func TestTeamEngineClaimFailureStopsSubmission(t *testing.T) {
	for _, kind := range []string{"implement", "review", "qa"} {
		t.Run(kind, func(t *testing.T) {
			e, g, code, checkouts, j := claimBudgetFixture(t, kind)
			g.check = func(context.Context) error { return errors.New("transport failure") }
			if err := e.dispatch(context.Background(), j); err != nil {
				t.Fatal(err)
			}
			got := e.state.Jobs[j.ID]
			if g.calls != 1 || g.claims != 0 || checkouts.calls != 0 || code.calls != 0 || got.Checkout != "" || got.SessionID != "" || got.MessageID != "" {
				t.Fatal("failed claim produced execution side effects")
			}
			if got.Phase != "waiting" || got.WaitReason != "execution" || got.Attempts != 1 || !got.NextAttemptAt.After(e.Clock.Now()) {
				t.Fatalf("failed claim lost retry accounting: %+v", got)
			}
		})
	}
}
