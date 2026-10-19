package worker

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNativeFailedChecksAdmitBoundedAuthorRepair(t *testing.T) {
	for _, state := range []string{"failure", "error"} {
		t.Run(state, func(t *testing.T) {
			cfg, catalog := nativeBranchRefreshFixture(t, "BLOCKED")
			catalog.Tasks[0].PRs[0].ChecksState = state
			jobs := NativeCandidates(cfg, catalog, nil, time.Now())
			if len(jobs) != 1 || jobs[0].Job.Kind != "fix" || !strings.HasPrefix(jobs[0].Job.InputRevision, "checks-repair:") {
				t.Fatalf("failed CI left the approved author idle: %+v", jobs)
			}
			v := jobs[0]
			prompt, err := nativePrompt(cfg, NativeExecution{Job: v.Job, Binding: &v.Task.Binding}, v)
			if err != nil || !strings.Contains(prompt, "failed checks") || !strings.Contains(prompt, v.Job.HeadSHA) {
				t.Fatal("repair lacks exact-head CI instructions", err)
			}
			history := map[string]NativeExecution{v.Job.ID: {Job: v.Job, Phase: "completed"}}
			if got := NativeCandidates(cfg, catalog, history, time.Now()); len(got) != 0 {
				t.Fatal("same failed head repeated", got)
			}
			cfg.Limits.MaxReviewCycles = 1
			catalog.Tasks[0].PRs[0].HeadSHA = strings.Repeat("c", 40)
			if got := NativeCandidates(cfg, catalog, history, time.Now()); len(got) != 0 {
				t.Fatal("CI repairs exceeded shared fix budget", got)
			}
			cfg.Limits.MaxReviewCycles = 2
			if got := NativeCandidates(cfg, catalog, history, time.Now()); len(got) != 1 || got[0].Job.ID == v.Job.ID {
				t.Fatal("new failed head did not get its own repair", got)
			}
			cfg.Self, cfg.Master.AgentID = cfg.Team.Members[0].Actor, "peer"
			if got := NativeCandidates(cfg, catalog, nil, time.Now()); len(got) != 0 {
				t.Fatal("peer reviewed a head waiting for CI repair", got)
			}
		})
	}
}

func TestNativeChecksRepairRetiresWhenChecksRecoverBeforeSubmission(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	p := nativeReviewPRFixture(cfg, g.catalog.Tasks[0])
	p.ChecksState = "failure"
	g.catalog.Tasks[0].PRs = []PR{p}
	v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
	x := NativeExecution{Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	g.catalog.Tasks[0].PRs[0].ChecksState = "success"
	if _, err = d.freshCandidate(t.Context(), x); !errors.Is(err, errNativeCandidateStale) || code.calls != 0 {
		t.Fatal("a recovered CI run retained an obsolete repair slot", err)
	}
}

func TestNativeChecksRepairIgnoresUnfinishedAndPassingChecks(t *testing.T) {
	for _, state := range []string{"pending", "unknown", "success", "none"} {
		cfg, catalog := nativeBranchRefreshFixture(t, "BLOCKED")
		catalog.Tasks[0].PRs[0].ChecksState = state
		if got := NativeCandidates(cfg, catalog, nil, time.Now()); len(got) != 0 {
			t.Fatal("nonfailed checks created repair work", state, got)
		}
	}
}

func TestNativeChecksRepairProgressExposesExhaustion(t *testing.T) {
	cfg, catalog := nativeBranchRefreshFixture(t, "BLOCKED")
	catalog.Tasks[0].PRs[0].ChecksState = "failure"
	d := &NativeDispatcher{config: cfg, catalog: catalog, clock: RealClock{}, history: map[string]NativeExecution{}}
	p := d.Progress()
	if p.Reason != "native_checks_repair_required" || len(p.Blocked) != 1 {
		t.Fatal("failed-check work hidden as idle", p)
	}
	v := NativeCandidates(cfg, catalog, nil, time.Now())[0]
	d.history[v.Job.ID] = NativeExecution{Job: v.Job, Phase: "completed"}
	if p = d.Progress(); p.Reason != "native_checks_repair_no_progress" {
		t.Fatal("unchanged failed head silently idle", p)
	}
	d.config.Limits.MaxReviewCycles = 1
	if p = d.Progress(); p.Reason != "native_checks_repair_limit" {
		t.Fatal("exhausted checks repair hidden", p)
	}
}

func TestNativeActiveChecksRecoveryRetiresOnlyUneffectedWork(t *testing.T) {
	for _, action := range []string{"none", "prepared", "pushed", "uncertain"} {
		t.Run(action, func(t *testing.T) {
			cfg, m, g, code := dispatchFixture(t)
			p := nativeReviewPRFixture(cfg, g.catalog.Tasks[0])
			p.ChecksState = "failure"
			g.catalog.Tasks[0].PRs = []PR{p}
			v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
			x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
			d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.advance(t.Context(), &x); err != nil || x.Phase != "active" {
				t.Fatal("repair did not start", err)
			}
			if action != "none" {
				r := nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "rejected-publish", Operation: "publish-pr", State: "prepared"}
				switch action {
				case "pushed":
					r.Step = "push"
				case "uncertain":
					r.State = "uncertain"
				}
				if err = saveNativeAction(cfg, r); err != nil {
					t.Fatal(err)
				}
			}
			g.catalog.Tasks[0].PRs[0].ChecksState = "success"
			code.running = false
			if _, err = d.advance(t.Context(), &x); err != nil {
				t.Fatal(err)
			}
			d.history[x.Job.ID] = x
			if err = d.updateRecoveryHold(); err != nil {
				t.Fatal(err)
			}
			if action == "pushed" || action == "uncertain" {
				if x.Phase == "cancelled" || !d.RecoveryBlocked() {
					t.Fatal("remote effects were discarded as an obsolete repair", x.Phase)
				}
				return
			}
			if x.Phase != "cancelled" || x.Reason != "native_checks_recovered" || d.RecoveryBlocked() {
				t.Fatal("recovered CI froze independent work", x.Phase, x.Reason)
			}
			other := g.catalog.Tasks[0]
			other.Task.URL = "https://github.com/code/repo/issues/2"
			other.Binding.TaskURL, other.PRs = other.Task.URL, nil
			g.catalog.Tasks = append(g.catalog.Tasks, other)
			d.catalog, d.nextRefresh = g.catalog, time.Now().Add(time.Hour)
			if _, err = d.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err = consumeNativeStep(t, d); err != nil || code.calls != 2 || code.last.TaskURL != other.Task.URL {
				t.Fatal("independent job did not proceed after obsolete repair", err)
			}
		})
	}
}
