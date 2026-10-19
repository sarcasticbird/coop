package worker

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func nativeBranchRefreshFixture(t *testing.T, state string) (Config, NativeCatalog) {
	t.Helper()
	cfg, catalog, task := nativeWorkFixture(t)
	p := nativeReviewPRFixture(cfg, task)
	p.MergeState, p.BaseSHA = state, strings.Repeat("b", 40)
	catalog.Tasks[0].PRs = []PR{p}
	return cfg, catalog
}

func TestNativeBranchRefreshAdmitsOnlyApprovedAuthor(t *testing.T) {
	for _, state := range []string{"BEHIND", "DIRTY"} {
		t.Run(state, func(t *testing.T) {
			cfg, catalog := nativeBranchRefreshFixture(t, state)
			jobs := NativeCandidates(cfg, catalog, nil, time.Now())
			if len(jobs) != 1 || jobs[0].Job.Kind != "fix" || jobs[0].Job.HeadSHA != strings.Repeat("a", 40) {
				t.Fatalf("branch maintenance left author idle: %+v", jobs)
			}
			v := jobs[0]
			prompt, err := nativePrompt(cfg, NativeExecution{Job: v.Job, Binding: &v.Task.Binding}, v)
			if err != nil || !strings.Contains(prompt, strings.Repeat("b", 40)) || !strings.Contains(prompt, "branch refresh") {
				t.Fatalf("pinned branch refresh missing from author prompt: %s %v", prompt, err)
			}
			cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[2].Name, cfg.Team.Members[2].Actor, "qa"
			if jobs := NativeCandidates(cfg, catalog, nil, time.Now()); len(jobs) != 0 {
				t.Fatal("QA admitted for unreviewed branch maintenance", jobs)
			}
			cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[1].Name, cfg.Team.Members[1].Actor, "author"
			catalog.Goals[0].State = "paused"
			if jobs := NativeCandidates(cfg, catalog, nil, time.Now()); len(jobs) != 0 {
				t.Fatal("paused goal authorized branch update", jobs)
			}
		})
	}
}

func TestNativeBranchRefreshIsBoundedAndPinsBase(t *testing.T) {
	cfg, catalog := nativeBranchRefreshFixture(t, "BEHIND")
	cfg.Limits.MaxReviewCycles = 2
	now := time.Now()
	jobs := NativeCandidates(cfg, catalog, nil, now)
	if len(jobs) != 1 {
		t.Fatal("missing branch refresh", jobs)
	}
	old := jobs[0].Job
	history := map[string]NativeExecution{old.ID: {Job: old, Phase: "completed"}}
	if jobs = NativeCandidates(cfg, catalog, history, now); len(jobs) != 0 {
		t.Fatal("repeated the same PR/base update", jobs)
	}
	catalog.Tasks[0].PRs[0].BaseSHA = strings.Repeat("c", 40)
	jobs = NativeCandidates(cfg, catalog, history, now)
	if len(jobs) != 1 || jobs[0].Job.ID == old.ID {
		t.Fatal("changed base did not produce a fresh bounded job", jobs)
	}
	newJob := jobs[0].Job
	history[newJob.ID] = NativeExecution{Job: newJob, Phase: "completed"}
	catalog.Tasks[0].PRs[0].BaseSHA = strings.Repeat("d", 40)
	if jobs = NativeCandidates(cfg, catalog, history, now); len(jobs) != 0 {
		t.Fatal("branch maintenance ignored cycle bound", jobs)
	}
	for _, base := range []string{"", "not-a-commit"} {
		catalog.Tasks[0].PRs[0].BaseSHA = base
		if jobs = NativeCandidates(cfg, catalog, nil, now); len(jobs) != 0 {
			t.Fatal("unresolved base admitted", jobs)
		}
	}
}

func TestNativeBranchRefreshStaleBaseCannotSubmit(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	task := g.catalog.Tasks[0]
	p := nativeReviewPRFixture(cfg, task)
	p.MergeState, p.BaseSHA = "BEHIND", strings.Repeat("b", 40)
	g.catalog.Tasks[0].PRs = []PR{p}
	jobs := NativeCandidates(cfg, g.catalog, nil, time.Now())
	if len(jobs) != 1 {
		t.Fatal("missing refresh job", jobs)
	}
	v := jobs[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	g.catalog.Tasks[0].PRs[0].BaseSHA = strings.Repeat("c", 40)
	if _, err = d.freshCandidate(t.Context(), x); !errors.Is(err, errNativeCandidateStale) || code.calls != 0 {
		t.Fatal("old base can submit", err)
	}
}

func TestNativeBranchRefreshProgressShowsWorkAndLimits(t *testing.T) {
	cfg, catalog := nativeBranchRefreshFixture(t, "BEHIND")
	cfg.Limits.MaxReviewCycles = 1
	clock := &fakeClock{now: time.Now()}
	d := &NativeDispatcher{config: cfg, catalog: catalog, clock: clock, history: map[string]NativeExecution{}}
	p := d.Progress()
	if p.Reason != "native_branch_refresh_required" || !strings.Contains(p.heartbeatReason(), catalog.Tasks[0].Task.URL) {
		t.Fatal("behind branch appears idle without actionable status", p)
	}
	j := NativeCandidates(cfg, catalog, nil, clock.Now())[0].Job
	d.active = &NativeExecution{Job: j, Phase: "active"}
	d.active.Job.Deadline = clock.Now().Add(time.Hour)
	p = d.Progress()
	if p.Reason != "native_branch_refresh" || p.Current == nil || p.Current.Stage != "fix" {
		t.Fatal("author refresh not visible as actual fix work", p)
	}
	d.active = nil
	d.history[j.ID] = NativeExecution{Job: j, Phase: "completed"}
	p = d.Progress()
	if p.Reason != "native_branch_refresh_limit" {
		t.Fatal("exhausted maintenance hidden", p)
	}
	comms := &MasterComms{}
	comms.engineeringHeartbeat(p)
	if comms.EngineeringState != "recovering" {
		t.Fatal("exhausted maintenance looks idle to owner", comms.EngineeringState)
	}
	d.config.Limits.MaxReviewCycles = 2
	if p = d.Progress(); p.Reason != "native_branch_refresh_no_progress" {
		t.Fatal("completed unchanged refresh silently idle", p)
	}
	d.history[j.ID] = NativeExecution{Job: j, Phase: "blocked", Reason: "native_completed_without_result"}
	if jobs := NativeCandidates(cfg, catalog, d.history, clock.Now()); len(jobs) != 0 {
		t.Fatal("fixture must reserve the blocked author checkout", jobs)
	}
	if p = d.Progress(); p.Reason != "native_task_wait" || len(p.Blocked) != 1 || p.Blocked[0].Reason != "native_completed_without_result" {
		t.Fatal("synthetic queued refresh hid operator continuation", p)
	}
	d.held = true
	if p = d.Progress(); p.Reason != "native_recovery_required" {
		t.Fatal("maintenance diagnostics hid uncertain work", p)
	}
}
