package worker

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"strings"
	"testing"
	"time"
)

type dependencyReadFailure struct {
	*fakeOpenCode
}

func (c *dependencyReadFailure) Observe(context.Context, Job) (Turn, error) {
	return Turn{}, &OpenCodeError{Category: "transport_failed"}
}

func TestNativeDependencyTransportRecoverySurvivesRetryBudget(t *testing.T) {
	for _, category := range []string{"transport_failed", "continuation_ownership_unknown"} {
		t.Run(category, func(t *testing.T) {
			cfg, m, g, code, clock, x := dependencyWaitFixture(t)
			wait, err := prepareNativeWait(t.Context(), cfg, m, g, x, NativeAction{DependsOn: []string{g.catalog.Tasks[1].Task.URL}, Body: "backend"})
			if err != nil {
				t.Fatal(err)
			}
			x.Wait, x.Phase, x.Reason = wait, "blocked", "native_dependency_wait"
			x.Job.Phase, x.Job.SubmissionState, x.Job.TerminalAt = "completed", "completed", clock.Now()
			x.Job.ReconcileFailures = Defaults(cfg).Limits.MaxAttempts
			x.Job.ReconcileError = category
			x.Job.NextAttemptAt = clock.Now().Add(time.Minute)
			g.catalog.Tasks[1].Task.Completed = true
			if err = saveNativeExecution(cfg, x); err != nil {
				t.Fatal(err)
			}
			d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
			if err != nil {
				t.Fatal(err)
			}
			d.catalog, d.nextRefresh = g.catalog, clock.Now().Add(time.Hour)
			if len(d.dependencyWaits()) != 0 {
				t.Fatal("recovery ignored durable cooldown")
			}
			clock.now = clock.now.Add(2 * time.Minute)
			if category != "transport_failed" {
				if len(d.dependencyWaits()) != 0 {
					t.Fatal("ownership uncertainty was retried automatically")
				}
				return
			}
			if len(d.dependencyWaits()) != 1 {
				t.Fatal("recovered transport left the saved task permanently excluded")
			}
			if _, err = d.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err = consumeNativeStep(t, d); err != nil {
				t.Fatal(err)
			}
			saved, err := loadNativeExecution(cfg, x.Job.ID)
			if err != nil || saved.Phase != "selected" || len(saved.Turns) != 1 || saved.Job.SessionID != x.Job.SessionID || saved.Job.Checkout != x.Job.Checkout || code.calls != 1 {
				t.Fatal("recovery replayed or lost the original work", saved.Phase, err)
			}
		})
	}
}

func TestNativeDependencyTransportBackoffIsBoundedWithoutModelReplay(t *testing.T) {
	cfg, m, g, code, clock, x := dependencyWaitFixture(t)
	x.Phase, x.Reason = "blocked", "native_dependency_wait"
	d, err := NewNativeDispatcher(cfg, m, g, &dependencyReadFailure{code}, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	last := time.Duration(0)
	for attempt := 1; attempt <= 9; attempt++ {
		if _, err := d.advance(t.Context(), &x); errorCategory(err) != "transport_failed" {
			t.Fatal("expected failed read revalidation", err)
		}
		delay := x.Job.NextAttemptAt.Sub(clock.Now())
		if delay > 15*time.Minute || delay < last || attempt == 2 && delay <= last {
			t.Fatal("transport retries lack capped exponential backoff", delay, last)
		}
		last = delay
		clock.now = x.Job.NextAttemptAt
	}
	if code.calls != 1 || code.aborts != 0 || len(x.Turns) != 0 {
		t.Fatal("read recovery replayed model work")
	}
}

func dependencyWaitFixture(t *testing.T) (Config, *dispatchMaster, *dispatchCatalog, *fakeOpenCode, *fakeClock, NativeExecution) {
	t.Helper()
	cfg, m, g, code := dispatchFixture(t)
	cfg.Team.Members[0].Capabilities = append(cfg.Team.Members[0].Capabilities, "implement")
	cfg.Team.EngineeringPeers[cfg.Team.Members[0].Name] = cfg.Team.Members[1].Name
	policy, err := TeamPolicySHA(*cfg.Team)
	if err != nil {
		t.Fatal(err)
	}
	g.catalog.Tasks[0].Binding.PolicySHA = policy
	clock := &fakeClock{now: time.Now()}
	v := NativeCandidates(cfg, g.catalog, nil, clock.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	x.Phase, x.Reason = "active", ""
	x.Job.Deadline = clock.Now().Add(time.Hour)
	code.running = false
	dep := g.catalog.Tasks[0]
	dep.Task.URL = "https://github.com/code/repo/issues/2"
	dep.Binding.TaskURL = dep.Task.URL
	// Prerequisites may belong to another approved goal on the same engagement.
	dep.Binding.Goal.GoalID = "other-approved-goal"
	dep.Binding.AuthorID, dep.Binding.PeerID = dep.Binding.PeerID, dep.Binding.AuthorID
	a := g.catalog.Goals[0]
	a.Revision.Ref = dep.Binding.Goal
	a.Actors.Author, a.Actors.Peer = a.Actors.Peer, a.Actors.Author
	a.Revision.Proposal.AuthorID, a.Revision.Proposal.PeerID = a.Revision.Proposal.PeerID, a.Revision.Proposal.AuthorID
	g.catalog.Goals = append(g.catalog.Goals, a)
	g.catalog.Tasks = append(g.catalog.Tasks, dep)
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	return cfg, m, g, code, clock, x
}
func TestNativeWaitCapturesExactApprovedDependencies(t *testing.T) {
	cfg, m, g, _, _, x := dependencyWaitFixture(t)
	wait, err := prepareNativeWait(t.Context(), cfg, m, g, x, NativeAction{DependsOn: []string{g.catalog.Tasks[1].Task.URL}, Body: "needs backend"})
	if err != nil || wait.SessionID != x.Job.SessionID || wait.MessageID != x.Job.MessageID || wait.Dependencies[0].AssignmentSHA != NativeAssignmentSHA(g.catalog.Tasks[1].Binding) {
		t.Fatal("dependency wait not bound to exact turn/artifact", err)
	}
}
func TestNativeWaitRejectsInvalidDependencies(t *testing.T) {
	for _, bad := range []string{"empty", "self", "unknown", "duplicate", "completed", "closed", "unapproved", "incomplete-catalog", "wrong-stage"} {
		t.Run(bad, func(t *testing.T) {
			cfg, m, g, _, _, x := dependencyWaitFixture(t)
			refs := []string{g.catalog.Tasks[1].Task.URL}
			switch bad {
			case "empty":
				refs = nil
			case "self":
				refs = []string{x.Job.TaskURL}
			case "unknown":
				refs = []string{"https://github.com/code/repo/issues/999"}
			case "duplicate":
				refs = append(refs, refs[0])
			case "completed":
				g.catalog.Tasks[1].Task.Completed = true
			case "closed":
				g.catalog.Tasks[1].Task.Closed = true
			case "unapproved":
				g.catalog.Goals[1].State = "paused"
			case "incomplete-catalog":
				g.catalog.Complete = false
			case "wrong-stage":
				x.Job.Kind = "review"
			}
			if _, err := prepareNativeWait(t.Context(), cfg, m, g, x, NativeAction{DependsOn: refs, Body: "wait"}); err == nil {
				t.Fatal("unsafe wait accepted")
			}
		})
	}
}
func TestNativeDependencyWaitResumesOriginalTaskAfterRestart(t *testing.T) {
	cfg, m, g, code, clock, x := dependencyWaitFixture(t)
	wait, err := prepareNativeWait(t.Context(), cfg, m, g, x, NativeAction{DependsOn: []string{g.catalog.Tasks[1].Task.URL}, Body: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	if err = saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "wait", Operation: "wait", State: "completed", Wait: wait}); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil || x.Reason != "native_dependency_wait" || x.Wait == nil {
		t.Fatal("terminal turn lost declared wait", err)
	}
	d, err = NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil || d.RecoveryBlocked() {
		t.Fatal("dependency wait froze worker", err)
	}
	d.catalog, d.nextRefresh = g.catalog, clock.Now().Add(time.Hour)
	if d.Ready() {
		t.Fatal("unfinished dependency resumed")
	}
	g.catalog.Tasks[1].Task.Completed = true
	g.catalog.Tasks[1].Task.Closed = true
	d.catalog = g.catalog
	if !d.Ready() {
		t.Fatal("finished exact dependency did not wake saved task")
	}
	if _, err = d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, d); err != nil {
		t.Fatal(err)
	}
	resumed, err := loadNativeExecution(cfg, x.Job.ID)
	if err != nil || resumed.Phase != "selected" || len(resumed.Turns) != 1 || resumed.Job.SessionID != x.Job.SessionID || resumed.Job.Checkout != x.Job.Checkout || code.calls != 1 {
		t.Fatal("dependency continuation lost saved identity or replayed", err)
	}
}
func TestNativeDependencyWaitRefusesChangedEvidence(t *testing.T) {
	for _, bad := range []string{"binding", "goal", "missing", "catalog", "uncertain", "takeover", "limit"} {
		t.Run(bad, func(t *testing.T) {
			cfg, m, g, code, clock, x := dependencyWaitFixture(t)
			wait, err := prepareNativeWait(t.Context(), cfg, m, g, x, NativeAction{DependsOn: []string{g.catalog.Tasks[1].Task.URL}, Body: "backend"})
			if err != nil {
				t.Fatal(err)
			}
			x.Wait = wait
			x.Phase, x.Reason = "blocked", "native_dependency_wait"
			x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
			x.Job.TerminalAt = clock.Now()
			g.catalog.Tasks[1].Task.Completed = true
			switch bad {
			case "binding":
				g.catalog.Tasks[1].Binding.BaseBranch = "other"
			case "goal":
				g.catalog.Goals[1].State = "paused"
			case "missing":
				g.catalog.Tasks = g.catalog.Tasks[:1]
			case "catalog":
				g.catalog.Complete = false
			case "uncertain":
				if err = saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: "unknown", State: "uncertain"}); err != nil {
					t.Fatal(err)
				}
			case "takeover":
				code.running = true
			case "limit":
				x.Turns = make([]Job, Defaults(cfg).Limits.MaxAttempts)
			}
			if err = saveNativeExecution(cfg, x); err != nil {
				t.Fatal(err)
			}
			d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.resumeBlocked(t.Context(), &x); err == nil || x.Phase == "selected" || code.calls != 1 {
				t.Fatal("unsafe dependency resumed", err)
			}
		})
	}
}

func TestNativeWaitJournalIsTurnBoundAndLocal(t *testing.T) {
	cfg, m, g, _, _, x := dependencyWaitFixture(t)
	in := NativeAction{Operation: "wait", RequestID: "one", DependsOn: []string{g.catalog.Tasks[1].Task.URL}, Body: "backend"}
	record := nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: in.RequestID, Operation: in.Operation}
	result, err := recordNativeWait(t.Context(), cfg, m, g, x, in, record)
	if err != nil || result.Phase != "waiting" {
		t.Fatal(err)
	}
	saved, err := loadNativeExecution(cfg, x.Job.ID)
	if err != nil || saved.Phase != "active" {
		t.Fatal("declaration freed a live native slot", err)
	}
	wait, err := nativeTurnWait(cfg, x)
	if err != nil || wait == nil {
		t.Fatal("wait missing from durable journal", err)
	}
	x.Job.MessageID = "msg_next_turn"
	if wait, err = nativeTurnWait(cfg, x); err != nil || wait != nil {
		t.Fatal("old journal leaked into another turn", err)
	}
}
func TestNativeDependencyWaitAllowsPeerReviewFirst(t *testing.T) {
	cfg, m, g, code, clock, x := dependencyWaitFixture(t)
	wait, err := prepareNativeWait(t.Context(), cfg, m, g, x, NativeAction{DependsOn: []string{g.catalog.Tasks[1].Task.URL}, Body: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	x.Phase, x.Reason = "blocked", "native_dependency_wait"
	x.Wait = wait
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = clock.Now()
	if err = saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	// The dependency belongs to the peer, so its author and reviewer swap here.
	other := g.catalog.Tasks[1]
	other.PRs = []PR{nativeReviewPRFixture(cfg, other)}
	other.PRs[0].Author = cfg.Team.Members[0].Actor
	other.PRs[0].Contributors = []Actor{cfg.Team.Members[0].Actor}
	g.catalog.Tasks[1] = other
	m.authority = g.catalog.Goals[1]
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	d.catalog, d.nextRefresh = g.catalog, clock.Now().Add(time.Hour)
	if _, err = d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, d); err != nil || code.calls != 2 || code.last.Kind != "review" {
		t.Fatal("dependency wait starved peer review", err)
	}
}

func TestNativeDependencyContinuationRevalidatesAfterSelection(t *testing.T) {
	cfg, m, g, code, clock, x := dependencyWaitFixture(t)
	wait, err := prepareNativeWait(t.Context(), cfg, m, g, x, NativeAction{DependsOn: []string{g.catalog.Tasks[1].Task.URL}, Body: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	x.Wait = wait
	x.Phase, x.Reason = "blocked", "native_dependency_wait"
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = clock.Now()
	g.catalog.Tasks[1].Task.Completed = true
	if err = saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil || x.Phase != "selected" {
		t.Fatal(err)
	}
	g.catalog.Tasks[1].Task.Completed = false
	if _, err = d.advance(t.Context(), &x); err != nil || code.calls != 1 {
		t.Fatal("dependency reopened after selection but continuation submitted", err)
	}
	if x.Phase != "blocked" || x.Reason != "native_dependency_wait" {
		t.Fatal("reopened dependency retained the native slot", x.Phase)
	}
	restarted, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil || restarted.HasActive() || restarted.RecoveryBlocked() {
		t.Fatal("parked dependency froze worker", err)
	}
	g.catalog.Tasks[1].Task.Completed = true
	if _, err = restarted.advance(t.Context(), &x); err != nil || x.Phase != "selected" || len(x.Turns) != 1 {
		t.Fatal("parked continuation could not resume without reallocating a turn", err)
	}

}
func TestNativeDependencyContinuationRetiresStaleTaskAndBacksOffFailures(t *testing.T) {
	for _, mode := range []string{"stale", "temporary"} {
		t.Run(mode, func(t *testing.T) {
			cfg, m, g, code, clock, x := dependencyWaitFixture(t)
			wait, err := prepareNativeWait(t.Context(), cfg, m, g, x, NativeAction{DependsOn: []string{g.catalog.Tasks[1].Task.URL}, Body: "backend"})
			if err != nil {
				t.Fatal(err)
			}
			x.Wait = wait
			x.Phase, x.Reason = "blocked", "native_dependency_wait"
			x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
			x.Job.TerminalAt = clock.Now()
			g.catalog.Tasks[1].Task.Completed = true
			if err = saveNativeExecution(cfg, x); err != nil {
				t.Fatal(err)
			}
			d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "stale" {
				g.catalog.Tasks[0].Binding.BaseBranch = "other"
			} else {
				m.offline = true
			}
			if _, err = d.advance(t.Context(), &x); err == nil {
				t.Fatal("expected failed revalidation")
			}
			if mode == "stale" && x.Phase != "cancelled" {
				t.Fatal("stale saved job repeatedly eligible", x.Phase)
			}
			if mode == "temporary" && (x.Job.ReconcileFailures != 1 || !x.Job.NextAttemptAt.After(clock.Now())) {
				t.Fatal("failed recovery lacks durable backoff")
			}
		})
	}
}

func TestNativeWaitActionUsesAuthenticatedJournalWithoutRemoteWrites(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	cfg.Projects = nil
	cfg.ProjectOwners = nil
	clock := &fakeClock{now: time.Now()}
	v := NativeCandidates(cfg, g.catalog, nil, clock.Now())[0]
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	tasks := map[string]NativeTask{}
	for i := 1; i <= 2; i++ {
		task := g.catalog.Tasks[0]
		task.Task.URL = "https://github.com/code/repo/issues/" + stringID(i)
		task.Binding.TaskURL = task.Task.URL
		tasks[task.Task.URL] = task
		m.links = append(m.links, c.ArtifactLink{Kind: "task", URL: task.Task.URL, Goal: task.Binding.Goal, AssignmentSHA: NativeAssignmentSHA(task.Binding)})
	}
	writes := 0
	api := NewGitHubClient(&fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		if endpoint == "graphql" {
			return jsonResponse(map[string]any{"data": map[string]any{"viewer": map[string]any{"__typename": "Bot", "databaseId": cfg.Self.ID, "login": cfg.Self.Login}}}), nil
		}
		if method != "GET" {
			writes++
			return CommandResult{}, c.ErrForbidden
		}
		for ref, task := range tasks {
			path := strings.TrimPrefix(ref, "https://github.com/")
			if endpoint == "repos/"+path {
				return jsonResponse(map[string]any{"html_url": ref, "number": func() int { _, n, _ := parseRef(ref); return n }(), "title": task.Task.Title, "body": task.Task.Body, "state": "open"}), nil
			}
			if strings.HasPrefix(endpoint, "repos/"+path+"/comments?") {
				return jsonResponse([]any{map[string]any{"id": 1, "body": NativeTaskMetadata(task.Binding), "user": map[string]any{"id": teamPlanner(cfg).ID, "type": "Bot", "login": teamPlanner(cfg).Login}, "created_at": "2026-01-01T00:00:00Z"}}), nil
			}
		}
		t.Fatalf("unexpected wait read %s", endpoint)
		return CommandResult{}, c.ErrUnavailable
	}})
	in := NativeAction{Operation: "wait", RequestID: "backend", DependsOn: []string{"https://github.com/code/repo/issues/2"}, Body: "backend prerequisite"}
	for i := 0; i < 2; i++ {
		result, err := RunNativeAction(t.Context(), cfg, m, api, x.Job.ID, in)
		if err != nil || result.Phase != "waiting" {
			t.Fatal("wait helper failed", err)
		}
	}
	if writes != 0 {
		t.Fatal("wait changed GitHub")
	}
	wait, err := nativeTurnWait(cfg, x)
	if err != nil || wait == nil {
		t.Fatal("helper did not save exact wait", err)
	}
}

func TestNativeFailedDependencyContinuationYieldsToIndependentReview(t *testing.T) {
	cfg, m, g, code, clock, x := dependencyWaitFixture(t)
	wait, err := prepareNativeWait(t.Context(), cfg, m, g, x, NativeAction{DependsOn: []string{g.catalog.Tasks[1].Task.URL}, Body: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	x.Wait = wait
	x.Phase, x.Reason = "blocked", "native_dependency_wait"
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = clock.Now()
	x.Job.Kind = "fix"
	x.Job.FirstEligibleAt = clock.Now().Add(-time.Hour)
	g.catalog.Tasks[1].Task.Completed = true
	review := g.catalog.Tasks[1]
	review.Task.URL = "https://github.com/code/repo/issues/3"
	review.Binding.TaskURL = review.Task.URL
	review.Task.Completed = false
	review.PRs = []PR{nativeReviewPRFixture(cfg, review)}
	review.PRs[0].Author = cfg.Team.Members[0].Actor
	review.PRs[0].Contributors = []Actor{cfg.Team.Members[0].Actor}
	g.catalog.Tasks = append(g.catalog.Tasks, review)
	if err = saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	d.catalog, d.nextRefresh = g.catalog, clock.Now().Add(time.Hour)
	m.offline = true
	if _, err = d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, d); err == nil {
		t.Fatal("expected first continuation authority failure")
	}
	m.offline = false
	m.authority = g.catalog.Goals[1]
	if _, err = d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, d); err != nil || code.calls != 2 || code.last.Kind != "review" {
		t.Fatal("failed older continuation starved independent review", err)
	}
}

func TestNativeDependencyParkingPreservesPreparedIntent(t *testing.T) {
	cfg, m, g, code, clock, x := dependencyWaitFixture(t)
	wait, err := prepareNativeWait(t.Context(), cfg, m, g, x, NativeAction{DependsOn: []string{g.catalog.Tasks[1].Task.URL}, Body: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	x.Wait = wait
	x.Phase, x.Reason = "blocked", "native_dependency_wait"
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = clock.Now()
	g.catalog.Tasks[1].Task.Completed = true
	if err = saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.advance(t.Context(), &x); err != nil {
		t.Fatal(err)
	}
	x.Phase = "prepared"
	x.Job.MessageID = "msg_reserved_intent"
	x.Job.SubmissionState = "prepared"
	if err = saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	g.catalog.Tasks[1].Task.Completed = false
	if _, err = d.advance(t.Context(), &x); err != nil || x.Phase != "blocked" || x.PendingContinuation == nil || x.PendingContinuation.MessageID != "msg_reserved_intent" {
		t.Fatal("prepared intent lost or kept slot", err)
	}
	d, err = NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil || d.HasActive() || d.RecoveryBlocked() {
		t.Fatal("parked prepared intent held worker", err)
	}
	g.catalog.Tasks[1].Task.Completed = true
	if _, err = d.advance(t.Context(), &x); err != nil || x.Phase != "prepared" || x.Job.MessageID != "msg_reserved_intent" || len(x.Turns) != 1 || code.calls != 1 {
		t.Fatal("prepared continuation reallocated or replayed", err)
	}
}
