package worker

import (
	"errors"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func selectNativeRook(cfg *Config, master *dispatchMaster) {
	for _, member := range cfg.Team.Members {
		if member.Name == "rook" {
			cfg.Name, cfg.Self, cfg.Master.AgentID = member.Name, member.Actor, "rook"
			if master != nil {
				master.self.Principal.ID, master.self.Principal.GitHubBotID, master.self.Placement.AgentID = "rook", member.Actor.ID, "rook"
				master.self.Principal.Capabilities = []string{"read", "send", "plan", "deliver"}
			}
			return
		}
	}
	panic("test roster missing Rook")
}

func TestNativeRookSelectionNeverFallsBackToAuthor(t *testing.T) {
	cfg, catalog, task := nativeWorkFixture(t)
	task.Binding.QAID = 0
	catalog.Goals[0].Revision.Proposal.QA.Required = false
	pr := nativeReviewPRFixture(cfg, task)
	meta := NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: NativeAssignmentSHA(task.Binding), HeadSHA: pr.HeadSHA, PRScopeRevision: pr.ScopeRevision, Verdict: "approved"}
	pr.Reviews = []Review{{ID: "peer", Author: cfg.Team.Members[0].Actor, HeadSHA: pr.HeadSHA, State: "APPROVED", Body: NativePeerMetadata(meta), SubmittedAt: time.Now()}}
	task.PRs = []PR{pr}
	catalog.Tasks[0] = task
	for _, member := range cfg.Team.Members {
		cfg.Name, cfg.Self = member.Name, member.Actor
		cfg.Master.AgentID = member.Name
		if member.Actor.ID == task.Binding.AuthorID {
			cfg.Master.AgentID = "author"
		}
		if member.Actor.ID == task.Binding.PeerID {
			cfg.Master.AgentID = "peer"
		}
		for _, job := range NativeCandidates(cfg, catalog, nil, time.Now()) {
			if job.Job.Kind == "deliver" && member.Name != "rook" {
				t.Fatal("non-Rook merge candidate", member.Name)
			}
		}
	}
	selectNativeRook(&cfg, nil)
	jobs := NativeCandidates(cfg, catalog, nil, time.Now())
	if len(jobs) != 1 || jobs[0].Job.Kind != "deliver" || jobs[0].Job.Master.Role != "delivery" || jobs[0].Job.Master.ExecutorID != cfg.Self.ID {
		t.Fatal("Rook not selected with separate delivery ownership", jobs)
	}
	v := jobs[0]
	x := NativeExecution{Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding}
	v.Handoff = &c.Handoff{ID: "release-handoff", Input: c.HandoffInput{Kind: "release", Tag: "v1.2.3", Workflow: "release.yml"}}
	prompt, err := nativePrompt(cfg, x, v)
	if err != nil || strings.Contains(prompt, "native release") || strings.Contains(prompt, "publishing tag") || !strings.Contains(prompt, "Rook owns integration only") {
		t.Fatal("ordinary merge prompt gained release instructions", err)
	}
	catalog.Tasks[0].PRs[0].Contributors = append(catalog.Tasks[0].PRs[0].Contributors, cfg.Self)
	if jobs := NativeCandidates(cfg, catalog, nil, time.Now()); len(jobs) != 0 {
		t.Fatal("contributing Rook admitted to delivery", jobs)
	}
	catalog.Tasks[0].PRs[0].Contributors = nil
	cfg.DeliveryHold = true
	if jobs := NativeCandidates(cfg, catalog, nil, time.Now()); len(jobs) != 0 {
		t.Fatal("Rook ignored hold")
	}
	cfg.DeliveryHold = false
	for i := range cfg.Team.Members {
		if cfg.Team.Members[i].Name == "rook" {
			cfg.Team.Members[i].Capabilities = []string{"plan"}
		}
	}
	// Update the test binding's policy digest so this exercises capability denial,
	// not merely the ordinary stale-policy guard.
	catalog.Tasks[0].Binding.PolicySHA, _ = TeamPolicySHA(*cfg.Team)
	if nativeDeliveryActor(cfg, catalog.Tasks[0].Binding) {
		t.Fatal("Rook retained removed delivery capability")
	}
}

func TestNativeRookDeliveryChecksLiveIdentityAndCapabilities(t *testing.T) {
	for _, fault := range []string{"none", "alias", "local-capability", "master-capability", "placement", "identity", "author", "peer", "qa"} {
		t.Run(fault, func(t *testing.T) {
			cfg, master, cat, _ := dispatchFixture(t)
			selectNativeRook(&cfg, master)
			binding := cat.catalog.Tasks[0].Binding
			switch fault {
			case "alias":
				cfg.Master.AgentID = "alias-rook"
			case "local-capability":
				cfg.Team.Members[3].Capabilities = []string{"plan"}
			case "master-capability":
				master.self.Principal.Capabilities = []string{"read", "send"}
			case "placement":
				master.self.Placement.Generation++
			case "identity":
				master.self.Principal.GitHubBotID++
			case "author":
				binding.AuthorID = cfg.Self.ID
			case "peer":
				binding.PeerID = cfg.Self.ID
			case "qa":
				binding.QAID = cfg.Self.ID
			}
			err := verifyNativeDelivery(t.Context(), cfg, master, binding, master.authority)
			if fault == "none" && err != nil || fault != "none" && !errors.Is(err, c.ErrForbidden) {
				t.Fatal(fault, err)
			}
		})
	}
}

func TestNativeRookPolicyRejectsContributorCapabilities(t *testing.T) {
	for _, capability := range []string{"implement", "engineering-review", "customer-qa", "wrong-name"} {
		t.Run(capability, func(t *testing.T) {
			cfg, _, _ := nativeWorkFixture(t)
			if capability == "wrong-name" {
				cfg.Team.Members[3].Name = "not-rook"
			} else {
				cfg.Team.Members[3].Capabilities = append(cfg.Team.Members[3].Capabilities, capability)
			}
			if err := ValidateTeamPolicy(*cfg.Team); err == nil {
				t.Fatal("invalid delivery policy accepted")
			}
		})
	}
}
