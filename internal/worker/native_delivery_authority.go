package worker

import (
	"context"
	"encoding/json"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func nativeDeliveryActor(cfg Config) bool {
	if cfg.Version != 3 || cfg.Master == nil || cfg.Team == nil || cfg.Name != c.DeliveryPrincipalID || cfg.Master.AgentID != c.DeliveryPrincipalID || cfg.Self.ID != c.DeliveryGitHubBotID || cfg.Self.Kind != "Bot" {
		return false
	}
	m, ok := TeamMemberForActor(*cfg.Team, cfg.Self)
	return ok && m.Name == c.DeliveryPrincipalID && memberCapability(m, "deliver")
}

func nativeDeliveryJob(cfg Config, j Job) bool {
	m := j.Master
	return nativeDeliveryActor(cfg) && j.Kind == "deliver" && m != nil && m.Role == "delivery" && m.AgentID == c.DeliveryPrincipalID && m.ExecutorID == c.DeliveryGitHubBotID && m.DeliveryHandoffID != ""
}

// Select the latest decision before checking its scope. Never fall back to an
// older grant when a replacement or revocation no longer matches the artifact.
func nativeDeliveryDecision(cat NativeCatalog, t NativeTask, p PR) *c.Handoff {
	h := nativeRetainedDecision(cat, t.Binding.Goal, t.Task.URL, p.URL, "deliver")
	if h == nil || h.Input.AssignmentSHA != NativeAssignmentSHA(t.Binding) {
		return nil
	}
	return h
}

func nativeDeliveryHandoff(cfg Config, cat NativeCatalog, t NativeTask, a c.GoalAuthority, p PR) *c.Handoff {
	if nativeDeliveryActor(cfg) && !cfg.DeliveryHold {
		if h := nativeRetainedDecision(cat, t.Binding.Goal, t.Task.URL, p.URL, "retained-deliver"); h != nil {
			return nativeRetainedHandoff(cfg, cat, t, a, p, "retained-deliver")
		}
	}
	if !nativeDeliveryActor(cfg) || cfg.DeliveryHold || !cat.Complete || !a.Valid || a.State != "approved" || a.Revision.Ref != t.Binding.Goal || a.ActiveRevisionSHA256 != t.Binding.Goal.RevisionSHA256 || a.ApprovalSequence != t.Binding.ApprovalSequence || validateNativeRoster(cfg, t.Binding) != nil || t.Task.BodySHA != t.Binding.TaskScopeSHA {
		return nil
	}
	h := nativeDeliveryDecision(cat, t, p)
	if h == nil {
		return nil
	}
	in := h.Input
	if in.ApprovalSequence != a.ApprovalSequence || in.TaskScopeSHA != t.Binding.TaskScopeSHA || in.BaseBranch != t.Binding.BaseBranch || p.BaseBranch != in.BaseBranch || !p.Merged && in.BaseSHA != p.BaseSHA || in.HeadSHA != p.HeadSHA || in.PRScopeRevision != p.ScopeRevision || in.AcceptanceSHA != NativeAcceptanceSHA(a) || p.QAArtifact == nil || p.QAArtifact.HeadSHA != p.HeadSHA || in.ArtifactSHA != p.QAArtifact.SHA256 || p.TaskURL != t.Task.URL || p.MasterGoal == nil || *p.MasterGoal != t.Binding.Goal || p.AssignmentSHA != in.AssignmentSHA || p.Author.ID != t.Binding.AuthorID || p.Author.Kind != "Bot" || !p.ReviewsComplete || !p.ContributorsComplete || p.ChecksState != "success" && p.ChecksState != "none" || !p.Merged && (p.Closed || p.MergeState != "CLEAN") {
		return nil
	}
	evidence, ok := cat.Reports[in.EvidenceMessageID]
	if !ok || evidence.ID != in.EvidenceMessageID || evidence.Imported || evidence.AuthorKind != "owner" || evidence.ThreadID != a.Revision.Proposal.ThreadID && evidence.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID {
		return nil
	}
	peer, cleared := nativePeerCleared(t.Binding, p)
	if !cleared || peer.ID != in.PeerReviewID {
		return nil
	}
	if a.Revision.Proposal.QA.Required {
		r, cleared := nativeQAState(t.Binding, a, p, cat.Reports, cat.Handoffs...)
		var qa NativeQAResult
		if !cleared || r.ID != in.QAReviewID || nativeMetadata(r.Body, "qa", &qa) != nil || qa.EvidenceMessageID != in.QAEvidenceMessageID || qa.EvidenceBodySHA != in.QAEvidenceBodySHA || qa.Environment != in.Environment || qa.EnvironmentRevision != in.EnvironmentRevision {
			return nil
		}
	}
	return h
}

func nativeValidateDelivery(ctx context.Context, cfg Config, m NativeMaster, g *GitHubClient, x NativeExecution) error {
	if !nativeDeliveryJob(cfg, x.Job) || cfg.DeliveryHold || x.Binding == nil {
		return c.ErrForbidden
	}
	cat, err := nativeValidationSnapshot(ctx, cfg, m, g, x.Job)
	if err != nil {
		return err
	}
	for _, t := range cat.Tasks {
		if t.Task.URL != x.Job.TaskURL || NativeAssignmentSHA(t.Binding) != x.Job.Master.AssignmentSHA {
			continue
		}
		for _, a := range cat.Goals {
			for _, p := range t.PRs {
				if p.URL != x.Job.PRURL || p.HeadSHA != x.Job.HeadSHA || p.ScopeRevision != x.Job.PRScopeRevision || !qaArtifactEqual(p.QAArtifact, x.Job.QAArtifact) {
					continue
				}
				h := nativeDeliveryHandoff(cfg, cat, t, a, p)
				if h == nil || h.ID != x.Job.Master.DeliveryHandoffID {
					return c.ErrForbidden
				}
				if h.Input.Kind == "retained-deliver" {
					if err := nativeValidateRetainedDecision(ctx, m, *h); err != nil {
						return err
					}
				}
				if p.Merged {
					return nativeConfirmMergeBase(ctx, g, t.Task.Repo, p.URL, h.Input.BaseSHA)
				}
				return nil
			}
		}
	}
	return c.ErrForbidden
}

// The live base advances on merge. Confirm the squash commit's original parent
// instead of mistaking that expected advancement for pre-merge base drift.
func nativeConfirmMergeBase(ctx context.Context, g *GitHubClient, repo, ref, base string) error {
	_, n, err := parseRef(ref)
	if err != nil {
		return err
	}
	owner := strings.Split(repo, "/")[0]
	raw, _, err := g.api(ctx, owner, "GET", "repos/"+repo+"/pulls/"+stringID(n), nil)
	if err != nil {
		return err
	}
	var pr struct {
		MergeCommitSHA string `json:"merge_commit_sha"`
	}
	if json.Unmarshal(raw, &pr) != nil || !qaHeadSHA.MatchString(pr.MergeCommitSHA) {
		return c.ErrConflict
	}
	raw, _, err = g.api(ctx, owner, "GET", "repos/"+repo+"/git/commits/"+pr.MergeCommitSHA, nil)
	if err != nil {
		return err
	}
	var commit struct{ Parents []struct{ SHA string } }
	if json.Unmarshal(raw, &commit) != nil || len(commit.Parents) != 1 || commit.Parents[0].SHA != base {
		return c.ErrConflict
	}
	return nil
}
