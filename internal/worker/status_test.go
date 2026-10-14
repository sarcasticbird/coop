package worker

import (
	"testing"
	"time"
)

func TestStatusCannotCallStaleMonitoringHealthy(t *testing.T) {
	s := storeFixture(t)
	now := time.Now().UTC()
	if err := s.WriteStatus(Status{Name: "builder", Enabled: true, State: "idle", ServerHealthy: true, Heartbeat: now.Add(-3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStatus(s.Dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "stale" || status.ServerHealthy {
		t.Fatal("stale monitoring appeared healthy")
	}
}

func TestUnlinkedImplementerPRIsVisibleButNeverAdmitted(t *testing.T) {
	e, g, code, clock, _ := engineFixture(t)
	e.Config.Name, e.Config.Role, e.Config.Self = "hawk", "planner", e.Config.Reviewer
	g.snapshot.PRs = []PR{{URL: "https://github.com/code/repo/pull/5", Author: e.Config.Implementer, HeadSHA: "current"}}
	tick(t, e, clock)
	if code.calls != 0 || e.status.WaitReason != "pr_metadata" || e.status.State != "blocked" {
		t.Fatalf("unlinked PR was hidden or executed: calls=%d, status=%+v", code.calls, e.status)
	}
	inspection := InspectWork(e.Config, g.snapshot, State{}, clock.Now())
	found := false
	for _, blocked := range inspection.Blocked {
		if blocked.TaskURL == g.snapshot.PRs[0].URL && blocked.Reason == "PR missing task/goal metadata" {
			found = true
		}
	}
	if !found || len(inspection.Jobs) != 0 {
		t.Fatal("dry-run omitted unlinked PR blocker or admitted unauthorized work")
	}
}
func TestStatusExplainsWaitingForReviewAndRetry(t *testing.T) {
	e, g, code, clock, _ := engineFixture(t)
	g.snapshot.PRs = []PR{{URL: "https://github.com/code/repo/pull/5", TaskURL: g.snapshot.Tasks[0].URL, GoalURL: g.snapshot.Goals[0].URL, Author: e.Config.Implementer, HeadSHA: "new"}}
	tick(t, e, clock)
	if code.calls != 0 || e.status.WaitReason != "review" {
		t.Fatal("waiting-for-review state not explained")
	}
	e, _, _, clock, _ = engineFixture(t)
	tick(t, e, clock)
	clock.now = clock.now.Add(6 * time.Second)
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.status.WaitReason != "retry" || e.status.NextRetry.IsZero() {
		t.Fatal("retry checkpoint not visible")
	}
}
