package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestNativeUncertainActionHoldsWholeJob(t *testing.T) {
	cfg, m, cat, _ := dispatchFixture(t)
	cfg.Projects, cfg.ProjectOwners = nil, nil
	cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Planner, teamPlanner(cfg), "peer"
	m.self.Principal.ID, m.self.Principal.GitHubBotID, m.self.Placement.AgentID = "peer", cfg.Self.ID, "peer"
	cat.catalog.Tasks = nil
	v := NativeCandidates(cfg, cat.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Phase: "active"}
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	completed := NativeAction{Operation: "complete-plan", RequestID: "prior-completed"}
	raw, _ := json.Marshal(completed)
	receipt := NativeActionResult{Phase: "completed", URL: "https://github.com/code/repo/issues/9"}
	if err := saveNativeAction(cfg, nativeActionRecord{Version: 3, JobID: x.Job.ID, RequestID: completed.RequestID, InputSHA: BodyHash(string(raw)), Operation: completed.Operation, State: "completed", Result: receipt}); err != nil {
		t.Fatal(err)
	}
	transport := &nativeDeliveryTransport{t: t, cfg: cfg, head: strings.Repeat("a", 40)}
	creates := 0
	transport.commands = &fakeCommands{respond: func(owner, endpoint, method string, body []byte) (CommandResult, error) {
		result, err := transport.response(owner, endpoint, method, body)
		if endpoint == "repos/code/repo/issues" && method == "POST" {
			creates++
			return CommandResult{}, c.ErrUnavailable
		}
		return result, err
	}}
	g := NewGitHubClient(transport)
	in := NativeAction{Operation: "create-task", RequestID: "first", Repository: "code/repo", Title: "Task", Body: "scope", BaseBranch: "main"}
	if _, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in); err == nil || creates != 1 {
		t.Fatal("expected one committed mutation with lost response", err)
	}
	first, err := readPrivate(nativeActionPath(cfg, x.Job.ID, in.RequestID), c.MaxJSON)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID = "different"
	if _, err = RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in); err == nil || creates != 1 {
		t.Fatalf("new request bypassed job uncertainty hold: creates=%d err=%v", creates, err)
	}
	in.Operation = "complete-plan"
	in.RequestID = "new-operation"
	if _, err = RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, in); err == nil {
		t.Fatal("different operation bypassed job uncertainty hold")
	}
	after, err := readPrivate(nativeActionPath(cfg, x.Job.ID, "first"), c.MaxJSON)
	if err != nil || string(after) != string(first) {
		t.Fatal("uncertain journal changed", err)
	}
	m.offline = true
	if got, err := RunNativeAction(t.Context(), cfg, m, g, x.Job.ID, completed); err != nil || got != receipt {
		t.Fatal("completed receipt read blocked by uncertainty", err)
	}
}

func nativeReviewPRFixture(cfg Config, task NativeTask) PR {
	return PR{URL: "https://github.com/code/repo/pull/1", TaskURL: task.Task.URL, MasterGoal: &task.Binding.Goal, AssignmentSHA: NativeAssignmentSHA(task.Binding), Author: cfg.Team.Members[1].Actor, HeadSHA: strings.Repeat("a", 40), BaseBranch: task.Binding.BaseBranch, ScopeRevision: BodyHash("scope"), ScopeUpdatedAt: time.Now().Add(-time.Hour), ChecksState: "success", MergeState: "CLEAN", ReviewsComplete: true, ContributorsComplete: true, Contributors: []Actor{cfg.Team.Members[1].Actor}, QAArtifact: &QAArtifact{SHA256: strings.Repeat("e", 64), HeadSHA: strings.Repeat("a", 40)}}
}
func consumeNativeStep(t *testing.T, d *NativeDispatcher) (bool, error) {
	t.Helper()
	select {
	case result := <-d.step:
		d.step <- result
		return d.Tick(t.Context())
	case <-time.After(5 * time.Second):
		t.Fatal("native step did not finish")
		return false, nil
	}
}

func TestNativeStaleUnsubmittedIntentReleasesSlot(t *testing.T) {
	for _, phase := range []string{"selected", "prepared"} {
		t.Run(phase, func(t *testing.T) {
			cfg, m, g, code := dispatchFixture(t)
			if phase == "prepared" {
				cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[0].Name, cfg.Team.Members[0].Actor, "peer"
				m.self.Principal.ID, m.self.Principal.GitHubBotID, m.self.Placement.AgentID = "peer", cfg.Self.ID, "peer"
				task := g.catalog.Tasks[0]
				task.PRs = []PR{nativeReviewPRFixture(cfg, task)}
				g.catalog.Tasks[0] = task
			}
			v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
			x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: phase}
			x.Job.Checkout = filepath.Join(cfg.WorkRoot, "saved-checkpoint")
			if err := os.MkdirAll(x.Job.Checkout, 0700); err != nil {
				t.Fatal(err)
			}
			checkpoint := filepath.Join(x.Job.Checkout, "local.txt")
			if err := os.WriteFile(checkpoint, []byte("preserve me"), 0600); err != nil {
				t.Fatal(err)
			}
			if phase == "prepared" {
				x.Job.SessionID, x.Job.MessageID, x.Job.SubmissionState = "ses_prepared", "msg_prepared", "prepared"
			}
			if err := saveNativeExecution(cfg, x); err != nil {
				t.Fatal(err)
			}
			d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
			if err != nil {
				t.Fatal(err)
			}
			d.nextRefresh = time.Now().Add(time.Hour)
			d.catalog = g.catalog
			if phase == "selected" {
				m.authority.ActiveRevisionSHA256 = BodyHash("replacement")
			} else {
				g.catalog.Tasks[0].PRs[0].HeadSHA = strings.Repeat("b", 40)
			}
			if _, err = d.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			_, _ = consumeNativeStep(t, d)
			saved, err := loadNativeExecution(cfg, x.Job.ID)
			if err != nil || saved.Phase != "cancelled" || d.HasActive() || d.RecoveryBlocked() || code.calls != 0 {
				t.Fatalf("stale %s retained slot: phase=%s active=%v hold=%v calls=%d err=%v", phase, saved.Phase, d.HasActive(), d.RecoveryBlocked(), code.calls, err)
			}
			raw, err := os.ReadFile(checkpoint)
			if err != nil || string(raw) != "preserve me" {
				t.Fatal("cancellation changed local checkpoint", err)
			}
			if phase == "selected" {
				m.authority.ActiveRevisionSHA256 = x.Goal.RevisionSHA256
				task := g.catalog.Tasks[0]
				task.Task.Body = "rebound scope"
				task.Task.BodySHA = TaskScopeHash(task.Task.Title, task.Task.Body)
				task.Binding.TaskScopeSHA = task.Task.BodySHA
				g.catalog.Tasks[0] = task
			}
			d.catalog = g.catalog
			d.nextRefresh = time.Now().Add(time.Hour)
			if _, err = d.Tick(t.Context()); err != nil || d.step == nil {
				t.Fatal("replacement work was not selected", err)
			}
			if _, err = consumeNativeStep(t, d); err != nil || code.calls != 1 {
				t.Fatal("replacement work did not start without operator recovery", err)
			}
		})
	}
}

func TestNativeUnavailableUnsubmittedIntentRemainsPending(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "selected"}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	m.offline = true
	if _, err = d.advance(t.Context(), &x); err == nil || x.Phase != "selected" {
		t.Fatal("temporary master failure cancelled pending work", err)
	}
	m.offline = false
	g.err = errors.New("GitHub unavailable")
	if _, err = d.advance(t.Context(), &x); err == nil || x.Phase != "selected" {
		t.Fatal("temporary GitHub failure cancelled pending work", err)
	}
	g.err = nil
	m.authority.ActiveRevisionSHA256 = BodyHash("untrusted partial response")
	d.master = nativeFailedResolution{m}
	if _, err = d.advance(t.Context(), &x); err == nil || x.Phase != "selected" {
		t.Fatal("failed authority read was treated as conclusive supersession", err)
	}
}

type nativeFailedResolution struct{ *dispatchMaster }

func (m nativeFailedResolution) ResolveGoal(context.Context, c.GoalRef) (c.GoalAuthority, error) {
	return m.authority, c.ErrUnavailable
}

func TestNativePendingChecksDoNotCancelPreparedIdentity(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	task := g.catalog.Tasks[0]
	task.Binding.QAID = 0
	m.authority.Revision.Proposal.QA.Required = false
	g.catalog.Goals[0] = m.authority
	p := nativeReviewPRFixture(cfg, task)
	meta := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
	p.Reviews = []Review{{ID: "peer", HeadSHA: p.HeadSHA, Author: cfg.Team.Members[0].Actor, State: "APPROVED", Body: NativePeerMetadata(meta), SubmittedAt: time.Now()}}
	task.PRs = []PR{p}
	g.catalog.Tasks[0] = task
	v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
	if v.Job.Kind != "deliver" {
		t.Fatal("fixture requires ready delivery")
	}
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "prepared"}
	x.Job.SessionID, x.Job.MessageID, x.Job.SubmissionState = "ses_prepared", "msg_prepared", "prepared"
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	g.catalog.Tasks[0].PRs[0].ChecksState = "pending"
	if _, err = d.advance(t.Context(), &x); err == nil || x.Phase != "prepared" || code.calls != 0 {
		t.Fatal("temporary checks wait cancelled exact prepared identity", err)
	}
}

func TestNativeDismissedApprovalsScheduleOneReplacement(t *testing.T) {
	for _, kind := range []string{"review", "qa"} {
		t.Run(kind, func(t *testing.T) {
			cfg, cat, task := nativeWorkFixture(t)
			p := nativeReviewPRFixture(cfg, task)
			now := time.Now().UTC()
			peer := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
			p.Reviews = []Review{{ID: "peer", HeadSHA: p.HeadSHA, Author: cfg.Team.Members[0].Actor, State: "APPROVED", Body: NativePeerMetadata(peer), SubmittedAt: now}}
			index, principal := 0, "peer"
			if kind == "review" {
				p.Reviews = nil
			} else {
				index, principal = 2, "qa"
			}
			cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[index].Name, cfg.Team.Members[index].Actor, principal
			task.PRs = []PR{p}
			cat.Tasks[0] = task
			initial := NativeCandidates(cfg, cat, nil, now)
			if len(initial) != 1 || initial[0].Job.Kind != kind {
				t.Fatal("missing initial review turn")
			}
			history := map[string]NativeExecution{initial[0].Job.ID: {Job: initial[0].Job, Phase: "completed"}}
			body := NativePeerMetadata(peer)
			if kind == "qa" {
				q := NativeQAResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, ArtifactSHA: p.QAArtifact.SHA256, AcceptanceSHA: NativeAcceptanceSHA(cat.Goals[0]), Environment: "staging", EnvironmentRevision: "staging-v1", EvidenceMessageID: "report", EvidenceBodySHA: BodyHash("private report"), Verdict: "approved"}
				body = NativeQAMetadata(q)
				cat.Reports["report"] = c.Message{ID: "report", ThreadID: "private", AuthorID: "qa", AuthorKind: "agent", Body: "private report"}
			}
			dismissed := Review{ID: "dismissed-approval", HeadSHA: p.HeadSHA, Author: cfg.Self, State: "DISMISSED", Body: body, SubmittedAt: now}
			p.Reviews = append(p.Reviews, dismissed)
			task.PRs[0] = p
			cat.Tasks[0] = task
			replacement := NativeCandidates(cfg, cat, history, now)
			if len(replacement) != 1 || replacement[0].Job.ID == initial[0].Job.ID {
				t.Fatalf("dismissed %s needs a new bounded identity, got %+v", kind, replacement)
			}
			history[replacement[0].Job.ID] = NativeExecution{Job: replacement[0].Job, Phase: "completed"}
			if got := NativeCandidates(cfg, cat, history, now); len(got) != 0 {
				t.Fatal("unchanged dismissal reopened completed replacement")
			}
			p.Reviews[len(p.Reviews)-1].State = "CHANGES_REQUESTED"
			p.Reviews[len(p.Reviews)-1].ID = "replacement-changes-requested"
			if kind == "review" {
				var meta NativeReviewResult
				if nativeMetadata(body, "review", &meta) != nil {
					t.Fatal("bad peer fixture")
				}
				meta.Verdict = "changes-needed"
				p.Reviews[len(p.Reviews)-1].Body = NativePeerMetadata(meta)
			} else {
				var meta NativeQAResult
				if nativeMetadata(body, "qa", &meta) != nil {
					t.Fatal("bad QA fixture")
				}
				meta.Verdict = "changes-needed"
				p.Reviews[len(p.Reviews)-1].Body = NativeQAMetadata(meta)
			}
			task.PRs[0] = p
			cat.Tasks[0] = task
			if got := NativeCandidates(cfg, cat, history, now); len(got) != 0 {
				t.Fatal("unchanged changes-requested review reopened")
			}
			delete(history, initial[0].Job.ID)
			if got := NativeCandidates(cfg, cat, history, now); len(got) != 0 {
				t.Fatal("changes-requested replacement reopened without initial local history")
			}
			p.Merged, p.Closed = true, true
			task.PRs[0] = p
			cat.Tasks[0] = task
			if got := NativeCandidates(cfg, cat, history, now); len(got) != 0 {
				t.Fatal("completed PR reopened independent review")
			}
		})
	}
}

func TestNativeCompletedDeliveryRemainsTerminal(t *testing.T) {
	cfg, catalog, task := nativeWorkFixture(t)
	task.Binding.QAID = 0
	catalog.Goals[0].Revision.Proposal.QA.Required = false
	p := nativeReviewPRFixture(cfg, task)
	meta := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
	p.Reviews = []Review{{ID: "peer-approval", HeadSHA: p.HeadSHA, Author: cfg.Team.Members[0].Actor, State: "APPROVED", Body: NativePeerMetadata(meta), SubmittedAt: time.Now()}}
	task.PRs = []PR{p}
	catalog.Tasks[0] = task
	jobs := NativeCandidates(cfg, catalog, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != "deliver" {
		t.Fatal("fixture requires delivery work")
	}
	history := map[string]NativeExecution{jobs[0].Job.ID: {Job: jobs[0].Job, Phase: "completed"}}
	if got := NativeCandidates(cfg, catalog, history, time.Now()); len(got) != 0 {
		t.Fatal("completed delivery identity reopened")
	}
	p.Reviews[0].State = "DISMISSED"
	task.PRs[0] = p
	catalog.Tasks[0] = task
	if got := NativeCandidates(cfg, catalog, history, time.Now()); len(got) != 0 {
		t.Fatal("dismissal reopened completed delivery")
	}
	p.Merged, p.Closed = true, true
	task.PRs[0] = p
	task.Task.Completed, task.Task.Closed = true, true
	catalog.Tasks[0] = task
	if got := NativeCandidates(cfg, catalog, history, time.Now()); len(got) != 0 {
		t.Fatal("completed delivery reopened for merged head")
	}
}

// A real submitted identity stays on the existing observation-only path even
// when authority is replaced; only conclusively unsubmitted work is cancelled.
type nativeObservationCounter struct {
	*fakeOpenCode
	observations int
}

func (n *nativeObservationCounter) Observe(ctx context.Context, j Job) (Turn, error) {
	n.observations++
	return n.fakeOpenCode.Observe(ctx, j)
}
func TestNativeSupersededSubmittedIntentOnlyObserves(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	counter := &nativeObservationCounter{fakeOpenCode: code}
	d, err := NewNativeDispatcher(cfg, m, g, counter, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "active"}
	x.Job.SessionID, x.Job.MessageID, x.Job.SubmissionState = "ses_saved", "msg_saved", "unknown"
	m.authority.ActiveRevisionSHA256 = BodyHash("replacement")
	if _, err = d.advance(t.Context(), &x); err != nil || x.Phase != "active" || counter.observations != 1 || code.calls != 0 {
		t.Fatal("superseded submitted turn was replayed or cancelled", err)
	}
}
