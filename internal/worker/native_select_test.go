package worker

import (
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func nativeWorkFixture(t *testing.T) (Config, NativeCatalog, NativeTask) {
	cfg, _, _, _, _ := teamScopeFixture(t)
	cfg.Version = 3
	cfg.Master = &MasterConfig{MasterID: "master", AgentID: "author", Engagement: "coop"}
	cfg.Name = cfg.Team.Members[1].Name
	cfg.Self = cfg.Team.Members[1].Actor
	b, source := nativeBindingFixture()
	b.AuthorID = 12
	b.PeerID = 11
	b.QAID = 13
	b.PolicySHA, _ = TeamPolicySHA(*cfg.Team)
	b.TaskScopeSHA = TaskScopeHash("Task", "scope")
	a := source.authority
	a.Actors.Author.GitHubBotID = 12
	a.Actors.Peer.GitHubBotID = 11
	a.Actors.QA.GitHubBotID = 13
	task := NativeTask{Binding: b, Task: Task{URL: b.TaskURL, Repo: "code/repo", Title: "Task", Body: "scope", BodySHA: b.TaskScopeSHA, Ready: true}}
	return cfg, NativeCatalog{Complete: true, Goals: []c.GoalAuthority{a}, Tasks: []NativeTask{task}, Reports: map[string]c.Message{}}, task
}
func TestNativeEngineeringStageHandoffs(t *testing.T) {
	cfg, catalog, task := nativeWorkFixture(t)
	now := time.Now().UTC()
	jobs := NativeCandidates(cfg, catalog, nil, now)
	if len(jobs) != 1 || jobs[0].Job.Kind != "implement" {
		t.Fatalf("missing author work %+v", jobs)
	}
	p := PR{URL: "https://github.com/code/repo/pull/1", TaskURL: task.Task.URL, MasterGoal: &task.Binding.Goal, AssignmentSHA: NativeAssignmentSHA(task.Binding), Author: cfg.Self, HeadSHA: strings.Repeat("a", 40), BaseBranch: "main", ScopeRevision: BodyHash("scope"), ScopeUpdatedAt: now.Add(-time.Hour), ChecksState: "success", MergeState: "CLEAN", ReviewsComplete: true, ContributorsComplete: true, Contributors: []Actor{cfg.Self}, QAArtifact: &QAArtifact{SHA256: strings.Repeat("e", 64), HeadSHA: strings.Repeat("a", 40)}}
	task.PRs = []PR{p}
	catalog.Tasks[0] = task
	cfg.Name = cfg.Team.Members[0].Name
	cfg.Self = cfg.Team.Members[0].Actor
	cfg.Master.AgentID = "peer"
	jobs = NativeCandidates(cfg, catalog, nil, now)
	if len(jobs) != 1 || jobs[0].Job.Kind != "review" {
		t.Fatalf("missing independent peer %+v", jobs)
	}
	peer := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
	p.Reviews = []Review{{ID: "peer-review", HeadSHA: p.HeadSHA, Author: cfg.Self, State: "APPROVED", Body: NativePeerMetadata(peer), SubmittedAt: now}}
	task.PRs[0] = p
	catalog.Tasks[0] = task
	cfg.Name = cfg.Team.Members[2].Name
	cfg.Self = cfg.Team.Members[2].Actor
	cfg.Master.AgentID = "qa"
	jobs = NativeCandidates(cfg, catalog, nil, now)
	if len(jobs) != 1 || jobs[0].Job.Kind != "qa" {
		t.Fatalf("missing QA handoff %+v", jobs)
	}
	qa := NativeQAResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, ArtifactSHA: p.QAArtifact.SHA256, AcceptanceSHA: NativeAcceptanceSHA(catalog.Goals[0]), Environment: "staging", EnvironmentRevision: "staging-v1", EvidenceMessageID: "report", EvidenceBodySHA: BodyHash("private report"), Verdict: "approved"}
	p.Reviews = append(p.Reviews, Review{ID: "qa-review", HeadSHA: p.HeadSHA, Author: cfg.Self, State: "APPROVED", Body: NativeQAMetadata(qa), SubmittedAt: now})
	task.PRs[0] = p
	catalog.Tasks[0] = task
	catalog.Reports["report"] = c.Message{ID: "report", ThreadID: "private", AuthorID: "qa", AuthorKind: "agent", Body: "private report"}
	cfg.Name = cfg.Team.Members[1].Name
	cfg.Self = cfg.Team.Members[1].Actor
	cfg.Master.AgentID = "author"
	jobs = NativeCandidates(cfg, catalog, nil, now)
	if len(jobs) != 1 || jobs[0].Job.Kind != "deliver" {
		t.Fatalf("missing author delivery %+v", jobs)
	}
	p.HeadSHA = strings.Repeat("b", 40)
	task.PRs[0] = p
	catalog.Tasks[0] = task
	if jobs = NativeCandidates(cfg, catalog, nil, now); len(jobs) != 0 {
		t.Fatal("stale reviews cleared delivery")
	}
}
func TestNativeCompletedJobsNeverReopen(t *testing.T) {
	cfg, catalog, _ := nativeWorkFixture(t)
	jobs := NativeCandidates(cfg, catalog, nil, time.Now())
	if len(jobs) != 1 {
		t.Fatal("missing candidate")
	}
	history := map[string]NativeExecution{jobs[0].Job.ID: {Job: jobs[0].Job, Phase: "completed"}}
	if next := NativeCandidates(cfg, catalog, history, time.Now()); len(next) != 0 {
		t.Fatal("completed job reopened")
	}
}
