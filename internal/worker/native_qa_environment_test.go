package worker

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestQARequestAgainstPlanningJobIsForbidden(t *testing.T) {
	cfg, master, catalog, _ := dispatchFixture(t)
	cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Planner, teamPlanner(cfg), "peer"
	master.self.Principal.ID, master.self.Principal.GitHubBotID, master.self.Placement.AgentID = "peer", cfg.Self.ID, "peer"
	catalog.catalog.Tasks = nil
	jobs := NativeCandidates(cfg, catalog.catalog, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != "plan" {
		t.Fatalf("missing planning fixture: %+v", jobs)
	}
	v := jobs[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Phase: "active"}
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	sends := 0
	master.onSend = func() { sends++ }
	_, err := RunNativeAction(t.Context(), cfg, master, NewGitHubClient(nil), x.Job.ID, NativeAction{Operation: "qa", RequestID: "invalid-planner-qa", Verdict: "approved", ReportBody: "not a QA assignment"})
	if !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("planning QA request: %v", err)
	}
	if sends != 0 {
		t.Fatal("invalid QA request published evidence")
	}
}

func newEnvironmentQAActionFixture(t *testing.T) *postMergeQAActionFixture {
	t.Helper()
	f := newPostMergeQAActionFixture(t)
	f.transport.merged, f.transport.closed = false, false
	task, err := f.github.ReadNativeTask(t.Context(), f.cfg, f.execution.Job.TaskURL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.github.ReadNativePR(t.Context(), f.cfg, task, f.master.authority, f.execution.Job.PRURL)
	if err != nil {
		t.Fatal(err)
	}
	a := f.master.authority
	q := NativeQAResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, ArtifactSHA: p.QAArtifact.SHA256, AcceptanceSHA: NativeAcceptanceSHA(a), Environment: a.Revision.Proposal.QA.Environment, EnvironmentRevision: a.Revision.Proposal.QA.EnvironmentRevision, EvidenceMessageID: "old-report", EvidenceBodySHA: BodyHash("old"), Verdict: "changes-needed"}
	f.master.reports = map[string]c.Message{"old-report": {ID: "old-report", Body: "old", AuthorID: f.cfg.Master.AgentID, AuthorKind: "agent", ThreadID: a.Revision.Proposal.QA.EvidenceDestination.ThreadID}}
	f.transport.reviews = append(f.transport.reviews, map[string]any{"fullDatabaseId": 2, "state": "CHANGES_REQUESTED", "body": NativeQAMetadata(q), "submittedAt": time.Now().Add(-time.Second).UTC(), "commit": map[string]any{"oid": p.HeadSHA}, "author": map[string]any{"__typename": "Bot", "databaseId": task.Binding.QAID, "login": f.cfg.Self.Login}})
	h := &f.master.handoff
	h.ID, h.ActorKind, h.Input.Kind, h.Input.ReviewID = "environment-reassessment", "owner", "reassess-qa", "2"
	h.Input.Environment, h.Input.EnvironmentRevision = "repaired worker on same artifact", "repair-v2"
	cat, err := f.github.NativeSnapshot(t.Context(), f.cfg, f.master)
	if err != nil {
		t.Fatal(err)
	}
	jobs := NativeCandidates(f.cfg, cat, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Handoff == nil {
		t.Fatalf("missing environment reassessment: %+v", jobs)
	}
	v := jobs[0]
	prompt, err := nativePrompt(f.cfg, NativeExecution{Job: v.Job, Binding: &task.Binding}, v)
	if err != nil || !strings.Contains(prompt, "Resolved QA environment: repaired worker on same artifact") || !strings.Contains(prompt, "Resolved environment revision: repair-v2") {
		t.Fatal("prompt did not resolve authority", err)
	}
	if err := os.Remove(nativeExecutionPath(f.cfg, f.execution.Job.ID)); err != nil {
		t.Fatal(err)
	}
	f.execution = NativeExecution{Version: 3, MasterID: f.cfg.Master.MasterID, InstanceID: f.cfg.Master.InstanceID, Generation: f.cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "active"}
	if err := saveNativeExecution(f.cfg, f.execution); err != nil {
		t.Fatal(err)
	}
	f.action.RequestID = "environment-result"
	return f
}

func TestQAEnvironmentResolvedThroughPublicationAndClearance(t *testing.T) {
	f := newEnvironmentQAActionFixture(t)
	original := f.master.authority.Revision.Proposal.QA
	result, err := RunNativeAction(t.Context(), f.cfg, f.master, f.github, f.execution.Job.ID, f.action)
	if err != nil {
		t.Fatal(err)
	}
	task, err := f.github.ReadNativeTask(t.Context(), f.cfg, f.execution.Job.TaskURL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.github.ReadNativePR(t.Context(), f.cfg, task, f.master.authority, f.execution.Job.PRURL)
	if err != nil {
		t.Fatal(err)
	}
	r := nativeLatestReview(p, task.Binding.QAID)
	var q NativeQAResult
	if err := nativeMetadata(r.Body, "qa", &q); err != nil {
		t.Fatal(err)
	}
	if q.Environment != f.master.handoff.Input.Environment || q.EnvironmentRevision != "repair-v2" || q.Assessment != "reassessment" || q.HandoffID != f.master.handoff.ID {
		t.Fatalf("wrong recorded environment: %+v", q)
	}
	report, err := f.master.Message(t.Context(), result.EvidenceMessageID)
	if err != nil {
		t.Fatal(err)
	}
	reports := map[string]c.Message{report.ID: report}
	if _, ok := nativeQAState(task.Binding, f.master.authority, p, reports, f.master.handoff); !ok {
		t.Fatal("resolved QA did not clear")
	}
	if _, ok := nativeQAState(task.Binding, f.master.authority, p, reports); ok {
		t.Fatal("unproven handoff cleared")
	}
	for _, change := range []string{"actor", "head", "artifact", "task", "environment", "review"} {
		h := f.master.handoff
		switch change {
		case "actor":
			h.ActorKind = "agent"
		case "head":
			h.Input.HeadSHA = strings.Repeat("f", 40)
		case "artifact":
			h.Input.ArtifactSHA = strings.Repeat("f", 64)
		case "task":
			h.Input.TaskURL += "9"
		case "environment":
			h.Input.Environment = "unrelated"
		case "review":
			h.Input.ReviewID = r.ID
		}
		if _, ok := nativeQAState(task.Binding, f.master.authority, p, reports, h); ok {
			t.Fatal("tampered environment cleared", change)
		}
	}
	if f.master.authority.Revision.Proposal.QA.Environment != original.Environment || f.master.authority.Revision.Proposal.QA.EnvironmentRevision != original.EnvironmentRevision {
		t.Fatal("original authority mutated")
	}
	cat, err := f.github.NativeSnapshot(t.Context(), f.cfg, f.master)
	if err != nil {
		t.Fatal(err)
	}
	authorCfg := f.cfg
	masterCfg := *f.cfg.Master
	authorCfg.Master = &masterCfg
	for _, member := range f.cfg.Team.Members {
		if member.Actor.ID == task.Binding.AuthorID {
			authorCfg.Name, authorCfg.Self, authorCfg.Master.AgentID = member.Name, member.Actor, f.master.authority.Actors.Author.PrincipalID
		}
	}
	jobs := NativeCandidates(authorCfg, cat, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != "deliver" {
		t.Fatalf("resolved QA missing from delivery gate: %+v", jobs)
	}
	cat.Handoffs = nil
	for _, job := range NativeCandidates(authorCfg, cat, nil, time.Now()) {
		if job.Job.Kind == "deliver" {
			t.Fatal("delivery accepted missing environment authority")
		}
	}
	again, err := RunNativeAction(t.Context(), f.cfg, f.master, f.github, f.execution.Job.ID, f.action)
	if err != nil || again != result || f.sends != 1 || len(f.events) != 1 {
		t.Fatal("retry duplicated publication", err)
	}
}

func TestQAEnvironmentInheritedByEvidenceOnlyReassessment(t *testing.T) {
	f := newEnvironmentQAActionFixture(t)
	f.action.Verdict = "changes-needed"
	if _, err := RunNativeAction(t.Context(), f.cfg, f.master, f.github, f.execution.Job.ID, f.action); err != nil {
		t.Fatal(err)
	}
	cat, err := f.github.NativeSnapshot(t.Context(), f.cfg, f.master)
	if err != nil {
		t.Fatal(err)
	}
	task := cat.Tasks[0]
	p := task.PRs[0]
	latest := nativeLatestReview(p, task.Binding.QAID)
	if !nativeChangesRequested("qa", task.Binding, f.master.authority, p, latest, cat.Handoffs...) {
		t.Fatal("repaired-environment hold was lost")
	}
	h := f.master.handoff
	h.ID, h.ActorKind, h.CreatedAt = "evidence-followup", "agent", time.Now().UTC()
	h.Input.ReviewID, h.Input.EvidenceMessageID, h.Input.RequestID = latest.ID, "new-evidence", "followup"
	h.Input.Environment, h.Input.EnvironmentRevision = "", ""
	cat.Handoffs = append(cat.Handoffs, h)
	jobs := NativeCandidates(f.cfg, cat, nil, time.Now())
	if len(jobs) != 1 || jobs[0].QAEnvironment == nil || jobs[0].QAEnvironment.Revision != "repair-v2" || jobs[0].Handoff.ID != h.ID {
		t.Fatalf("evidence followup reverted environment: %+v", jobs)
	}
	if _, err := resolveNativeQAEnvironment(task.Binding, f.master.authority, p, &h, []c.Handoff{h}); err == nil {
		t.Fatal("missing prior environment authority ignored")
	}
}

func TestQAEnvironmentRevalidatedBeforeEffects(t *testing.T) {
	f := newEnvironmentQAActionFixture(t)
	f.master.handoff.ActorKind = "agent"
	if _, err := RunNativeAction(t.Context(), f.cfg, f.master, f.github, f.execution.Job.ID, f.action); err == nil {
		t.Fatal("lost owner authority accepted")
	}
	if f.sends != 0 || len(f.events) != 0 {
		t.Fatal("effects preceded environment validation")
	}
}
