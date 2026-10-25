package worker

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"strings"
	"testing"
	"time"
)

func TestNativeQAReassessmentBoundedToChangedEvidence(t *testing.T) {
	cfg, cat, task := nativeWorkFixture(t)
	now := time.Now().UTC()
	p := nativeReviewPRFixture(cfg, task)
	peer := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"}
	p.Reviews = []Review{{ID: "peer", HeadSHA: p.HeadSHA, Author: cfg.Team.Members[0].Actor, State: "APPROVED", Body: NativePeerMetadata(peer), SubmittedAt: now}}
	cfg.Name, cfg.Self, cfg.Master.AgentID = cfg.Team.Members[2].Name, cfg.Team.Members[2].Actor, "qa"
	task.PRs = []PR{p}
	cat.Tasks[0] = task
	first := NativeCandidates(cfg, cat, nil, now)[0]
	history := map[string]NativeExecution{first.Job.ID: {Job: first.Job, Phase: "completed"}}
	q := NativeQAResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, ArtifactSHA: p.QAArtifact.SHA256, AcceptanceSHA: NativeAcceptanceSHA(cat.Goals[0]), Environment: "staging", EnvironmentRevision: "staging-v1", EvidenceMessageID: "old-report", EvidenceBodySHA: BodyHash("old"), Verdict: "changes-needed"}
	p.Reviews = append(p.Reviews, Review{ID: "qa-old", HeadSHA: p.HeadSHA, Author: cfg.Self, State: "CHANGES_REQUESTED", Body: NativeQAMetadata(q), SubmittedAt: now})
	cat.Tasks[0].PRs = []PR{p}
	if got := NativeCandidates(cfg, cat, history, now); len(got) != 0 {
		t.Fatal("QA repeated without new evidence")
	}
	h := c.Handoff{ID: "handoff-one", Input: c.HandoffInput{Kind: "reassess-qa", Goal: task.Binding.Goal, TaskURL: task.Task.URL, PRURL: p.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, ArtifactSHA: p.QAArtifact.SHA256, ReviewID: "qa-old", EvidenceMessageID: "owner-uat", RequestID: "one"}}
	cat.Handoffs = []c.Handoff{h}
	got := NativeCandidates(cfg, cat, history, now)
	if len(got) != 1 || got[0].Job.Kind != "qa" || got[0].Job.ID == first.Job.ID {
		t.Fatalf("no fresh reassessment %+v", got)
	}
	prompt, err := nativePrompt(cfg, NativeExecution{Job: got[0].Job, Binding: &task.Binding}, got[0])
	if err != nil || !strings.Contains(prompt, "owner-uat") {
		t.Fatal("new evidence missing", err)
	}
	history[got[0].Job.ID] = NativeExecution{Job: got[0].Job, Phase: "completed"}
	if got := NativeCandidates(cfg, cat, history, now); len(got) != 0 {
		t.Fatal("completed reassessment replayed")
	}
	delete(history, got[0].Job.ID)
	for _, change := range []string{"head", "artifact", "review", "evidence", "assignment"} {
		copy := h
		switch change {
		case "head":
			copy.Input.HeadSHA = strings.Repeat("b", 40)
		case "artifact":
			copy.Input.ArtifactSHA = strings.Repeat("f", 64)
		case "review":
			copy.Input.ReviewID = "other"
		case "evidence":
			copy.Input.EvidenceMessageID = "old-report"
		case "assignment":
			copy.Input.AssignmentSHA = strings.Repeat("f", 64)
		}
		cat.Handoffs = []c.Handoff{copy}
		if got := NativeCandidates(cfg, cat, history, now); len(got) != 0 {
			t.Fatal("mismatched reassessment admitted", change)
		}
	}
}

func TestReleaseExtensionForQAOptionalSource(t *testing.T) {
	cfg, cat, task := nativeWorkFixture(t)
	p := nativeReviewPRFixture(cfg, task)
	p.QAArtifact = nil
	h := c.Handoff{ID: "ho_release", Input: c.HandoffInput{Kind: "release", Goal: task.Binding.Goal, TaskURL: task.Task.URL, PRURL: p.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: p.HeadSHA, ArtifactSHA: strings.Repeat("e", 64), Tag: "v1.2.3", Workflow: "release.yml"}}
	cat.Handoffs = []c.Handoff{h}
	if releaseHandoff(cat, task, p) == nil {
		t.Fatal("QA-optional delivery lost owner extension")
	}
	cat.Handoffs[0].Input.HeadSHA = strings.Repeat("f", 40)
	if releaseHandoff(cat, task, p) != nil {
		t.Fatal("stale owner extension matched")
	}
}
