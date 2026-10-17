package worker

import (
	"context"
	"strings"
	"testing"
	"time"
)

func deliveryPR(e *Engine, s Snapshot, now time.Time) PR {
	p := PR{URL: "https://github.com/code/repo/pull/5", TaskURL: s.Tasks[0].URL, GoalURL: s.Goals[0].URL, Author: e.Config.Implementer, HeadSHA: strings.Repeat("a", 40), ChecksState: "success", MergeState: "CLEAN"}
	p.Reviews = []Review{{ID: "approval", Author: e.Config.Reviewer, HeadSHA: p.HeadSHA, State: "APPROVED", SubmittedAt: now}}
	return p
}
func TestSavedDeliveryRequiresCurrentVerdictAndChecks(t *testing.T) {
	for _, change := range []string{"dismissed", "failed", "closed"} {
		t.Run(change, func(t *testing.T) {
			e, g, code, clock, _ := engineFixture(t)
			g.snapshot.PRs = []PR{deliveryPR(e, g.snapshot, clock.Now())}
			g.snapshot.Tasks[0].Ready = false // Claimed delivery is already In Progress.
			if err := e.initialize(); err != nil {
				t.Fatal(err)
			}
			j := Select(e.Config, g.snapshot, e.state, clock.Now())[0]
			j.Phase = "paused"
			if err := e.saveJob(j); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "dismissed":
				g.snapshot.PRs[0].Reviews[0].State = "DISMISSED"
			case "failed":
				g.snapshot.PRs[0].ChecksState = "failure"
			case "closed":
				g.snapshot.PRs[0].Closed = true
			}
			tick(t, e, clock)
			if code.calls != 0 {
				t.Fatal("saved delivery bypassed current admission")
			}
		})
	}
}

type busyOpenCode struct{ fakeOpenCode }

func (*busyOpenCode) CheckIdle(context.Context, Job) error {
	return &OpenCodeError{Category: "session_busy"}
}

type countingCheckouts struct {
	fakeCheckouts
	prepared int
}

func (c *countingCheckouts) Prepare(ctx context.Context, j Job) (Job, error) {
	c.prepared++
	return c.fakeCheckouts.Prepare(ctx, j)
}
func TestBusySavedSessionHoldsBeforeCheckoutWithoutFailureAttempt(t *testing.T) {
	e, g, _, clock, _ := engineFixture(t)
	code := &busyOpenCode{}
	checkout := &countingCheckouts{}
	e.OpenCode, e.Checkouts = code, checkout
	if err := e.initialize(); err != nil {
		t.Fatal(err)
	}
	j := Select(e.Config, g.snapshot, e.state, clock.Now())[0]
	e.state.Sessions[BindingKey(e.Config, j)] = SessionBinding{Checkout: "/workspace/worker/task", Branch: "agent/task", SessionID: "ses_manual"}
	tick(t, e, clock)
	if checkout.prepared != 0 || code.calls != 0 || code.aborts != 0 || e.state.Attempts[BindingKey(e.Config, j)] != 0 {
		t.Fatal("busy session modified checkout, dispatched or consumed attempts")
	}
}

func TestMergedDeliveryWaitsForCompletedIssue(t *testing.T) {
	e, g, _, clock, _ := engineFixture(t)
	p := deliveryPR(e, g.snapshot, clock.Now())
	p.Merged = true
	p.Closed = true
	j := Job{Kind: "deliver", TaskURL: p.TaskURL, GoalURL: p.GoalURL, PRURL: p.URL, HeadSHA: p.HeadSHA}
	if artifactCompletes(e.Config, j, Artifacts{PR: &p}) {
		t.Fatal("merge alone completed task delivery")
	}
	g.snapshot.PRs = []PR{p}
	jobs := Select(e.Config, g.snapshot, State{}, clock.Now())
	if len(jobs) != 1 || jobs[0].Kind != "deliver" {
		t.Fatal("merged PR with open issue cannot finalize")
	}
}

func TestRecoveryFinalizesMergedPRWithoutAnotherImplementation(t *testing.T) {
	e, g, code, clock, _ := engineFixture(t)
	p := deliveryPR(e, g.snapshot, clock.Now())
	p.Merged = true
	p.Closed = true
	g.snapshot.PRs = []PR{p}
	code.effect = func(_ Config, j Job) {
		if j.Kind != "deliver" {
			t.Fatal("merged task started another implementation")
		}
		g.snapshot.Tasks[0].Completed = true
		g.snapshot.Tasks[0].Closed = true
	}
	tick(t, e, clock)
	clock.now = clock.now.Add(e.Config.OpenCode.ReconcileInterval)
	tick(t, e, clock)
	if code.calls != 1 || !g.snapshot.Tasks[0].Completed {
		t.Fatal("merged issue finalization did not recover")
	}
	for _, job := range e.state.Jobs {
		if job.Phase != "completed" {
			t.Fatal("finalization job not completed")
		}
	}
}

type limitedGitHub struct {
	*workflowGitHub
	calls   int
	retryAt time.Time
}

func (g *limitedGitHub) Snapshot(context.Context, Config, Snapshot) (Snapshot, error) {
	g.calls++
	return Snapshot{}, &GitHubError{Category: "rate_limited", RetryAt: g.retryAt}
}
func TestCachedWorkCannotBypassRateLimitDeadline(t *testing.T) {
	e, g, _, clock, _ := engineFixture(t)
	limited := &limitedGitHub{workflowGitHub: g, retryAt: clock.Now().Add(time.Hour)}
	e.GitHub = limited
	if err := e.initialize(); err != nil {
		t.Fatal(err)
	}
	e.state.SnapshotCache = g.snapshot
	if err := e.Tick(context.Background()); err == nil {
		t.Fatal("expected rate limit")
	}
	for range 3 {
		tick(t, e, clock)
	}
	if limited.calls != 1 {
		t.Fatal("cached work bypassed Retry-After")
	}
}

func TestCancelledTaskCannotStartOrContinue(t *testing.T) {
	e, g, code, clock, _ := engineFixture(t)
	code.running = true
	tick(t, e, clock)
	g.snapshot.Tasks[0].Closed = true
	g.snapshot.Tasks[0].Completed = false
	tick(t, e, clock)
	if code.aborts != 1 {
		t.Fatal("cancelled task continued running")
	}
	if jobs := Select(e.Config, g.snapshot, State{}, clock.Now()); len(jobs) != 0 {
		t.Fatal("cancelled task admitted")
	}
}
