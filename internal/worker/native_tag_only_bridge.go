package worker

import (
	"context"
	"encoding/json"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func tagOnlyInputSHA(in c.HandoffInput) string {
	raw, _ := json.Marshal(in)
	return BodyHash(string(raw))
}

func nativeTagOnlyGateRoster(cfg Config, p PR, sourceAuthor int64) bool {
	var previous int64
	for _, check := range []struct{ name, capability string }{{"hawk", "engineering-review"}, {"wren", "customer-qa"}} {
		found := false
		for _, member := range cfg.Team.Members {
			if member.Name == check.name && member.Actor.Kind == "Bot" && memberCapability(member, check.capability) && member.Actor.ID != previous && member.Actor.ID != sourceAuthor && member.Actor.ID != c.DeliveryGitHubBotID && !nativeContributor(p, member.Actor.ID) {
				previous, found = member.Actor.ID, true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func nativeTagOnlySourceProof(cat NativeCatalog, in c.HandoffInput) *c.Handoff {
	for i := range cat.Handoffs {
		h := &cat.Handoffs[i]
		if h.ID != in.TagOnly.SourceDeliveryHandoffID {
			continue
		}
		if tagOnlyInputSHA(h.Input) != in.TagOnly.SourceDeliveryInputSHA || !c.TagOnlySourceMatches(in, h.Input) {
			return nil
		}
		latest := nativeRetainedDecision(cat, in.Goal, in.TaskURL, in.PRURL, h.Input.Kind)
		if latest == nil || latest.ID != h.ID {
			return nil
		}
		return h
	}
	return nil
}

// Only this explicit bridge can read an original assignment under a newer
// policy. It does not authorize ordinary source stages or rerun the old merge.
func nativeTagOnlyReadRoster(cfg Config, cat NativeCatalog, b NativeTaskBinding) bool {
	if !nativeDeliveryActor(cfg) || cfg.DeliveryHold {
		return false
	}
	policy, err := TeamPolicySHA(*cfg.Team)
	if err != nil {
		return false
	}
	for _, candidate := range cat.Handoffs {
		in := candidate.Input
		if in.Kind != "tag-only" || in.Goal != b.Goal || in.TaskURL != b.TaskURL {
			continue
		}
		h := nativeTagOnlyDecision(cat, NativeTask{Task: Task{URL: b.TaskURL}, Binding: b}, PR{URL: in.PRURL})
		if h == nil || h.ID != candidate.ID || in.TagOnly.ExecutionPolicySHA != policy || in.TagOnly.SourcePolicySHA != b.PolicySHA || in.AssignmentSHA != NativeAssignmentSHA(b) || in.TaskScopeSHA != b.TaskScopeSHA || in.BaseBranch != b.BaseBranch || in.ApprovalSequence != b.ApprovalSequence {
			continue
		}
		source := nativeTagOnlySourceProof(cat, in)
		if source == nil || source.Input.Kind != "retained-deliver" {
			continue
		}
		owner := cat.Reports[in.EvidenceMessageID]
		if owner.ID != in.EvidenceMessageID || owner.Imported || owner.AuthorKind != "owner" || BodyHash(owner.Body) != in.TagOnly.OwnerBodySHA {
			continue
		}
		for _, a := range cat.Goals {
			if a.Revision.Ref != b.Goal || a.State != "approved" || a.ActiveRevisionSHA256 != b.Goal.RevisionSHA256 || a.ApprovalSequence != b.ApprovalSequence || in.AcceptanceSHA != NativeAcceptanceSHA(a) || owner.ThreadID != a.Revision.Proposal.ThreadID && owner.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID {
				continue
			}
			repo, e := nativeTaskRepo(b.TaskURL)
			for _, scope := range a.Revision.Proposal.Repositories {
				if e == nil && scope.Name == repo && containsExact(scope.BaseBranches, b.BaseBranch) {
					return true
				}
			}
		}
	}
	return false
}

func nativeTagOnlyRetainedSource(cfg Config, cat NativeCatalog, t NativeTask, a c.GoalAuthority, p PR, tag, source c.Handoff) bool {
	in, r, original := tag.Input, tag.Input.TagOnly, source.Input.Retained
	if !nativeTagOnlyReadRoster(cfg, cat, t.Binding) || !a.Valid || t.Task.BodySHA != t.Binding.TaskScopeSHA || in.ApprovalSequence != a.ApprovalSequence || in.AcceptanceSHA != NativeAcceptanceSHA(a) || original.SourcePolicySHA != r.SourcePolicySHA || original.SourceTreeSHA != r.SourceTreeSHA || original.SourcePeerBodySHA != r.PeerBodySHA || original.SourceQABodySHA != r.QABodySHA || r.VersionPRURL != "" && original.PayloadSHA != r.SourceBundleSHA {
		return false
	}
	if p.TaskURL != t.Task.URL || p.MasterGoal == nil || *p.MasterGoal != t.Binding.Goal || p.AssignmentSHA != in.AssignmentSHA || p.Author.Kind != "Bot" || p.Author.ID != t.Binding.AuthorID || p.HeadSHA != in.HeadSHA || p.ScopeRevision != in.PRScopeRevision || p.BaseBranch != in.BaseBranch || p.QAArtifact == nil || p.QAArtifact.HeadSHA != p.HeadSHA || p.QAArtifact.SHA256 != in.ArtifactSHA || !p.ReviewsComplete || !p.ContributorsComplete || p.ChecksState != "success" && p.ChecksState != "none" {
		return false
	}
	if a.Actors.Author.GitHubBotID != t.Binding.AuthorID || a.Actors.Peer.GitHubBotID != t.Binding.PeerID || a.Actors.QA.GitHubBotID != t.Binding.QAID {
		return false
	}
	peer, clear := nativePeerCleared(t.Binding, p)
	if !clear || peer.ID != in.PeerReviewID {
		return false
	}
	qa, clear := nativeQAState(t.Binding, a, p, cat.Reports, cat.Handoffs...)
	var result NativeQAResult
	if !clear || qa.ID != in.QAReviewID || nativeMetadata(qa.Body, "qa", &result) != nil || result.EvidenceMessageID != in.QAEvidenceMessageID || result.EvidenceBodySHA != in.QAEvidenceBodySHA || result.Environment != original.SourceEnvironment || result.EnvironmentRevision != original.SourceEnvironmentRevision {
		return false
	}
	if !nativeRetainedCurrentEnvironment(t.Binding, a, p, in, cat.Handoffs) {
		return false
	}
	for _, check := range []struct{ id, sha, author string }{{source.Input.EvidenceMessageID, "", ""}, {original.EngineeringReportID, original.EngineeringReportBodySHA, "hawk"}, {original.QAReportID, original.QAReportBodySHA, "wren"}} {
		m := cat.Reports[check.id]
		if m.ID != check.id || m.Imported || m.ThreadID != a.Revision.Proposal.ThreadID && m.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID {
			return false
		}
		if check.author == "" {
			if m.AuthorKind != "owner" {
				return false
			}
		} else if m.AuthorKind != "agent" || m.AuthorID != check.author || BodyHash(m.Body) != check.sha {
			return false
		}
	}
	return true
}

func nativeTagOnlyVersion(cfg Config, cat NativeCatalog, tag c.Handoff) bool {
	r := tag.Input.TagOnly
	repo, _, err := parseRef(tag.Input.PRURL)
	if err != nil {
		return false
	}
	for _, t := range cat.Tasks {
		if t.Task.URL != r.VersionTaskURL || t.Binding.Goal != r.VersionGoal || t.Task.Repo != repo || !t.Task.Completed || !t.Task.Closed || !nativeCacheTaskReady(cat, t.Task.URL, time.Now()) || cat.Cache != nil && cat.Cache.OutOfScope[r.VersionGoal] {
			continue
		}
		for _, p := range t.PRs {
			if p.URL != r.VersionPRURL || !p.Merged || p.HeadSHA != r.VersionHeadSHA || p.QAArtifact == nil || p.QAArtifact.Kind != "source" || p.QAArtifact.Revision != "source-bundle-v1" || p.QAArtifact.SHA256 != r.VersionBundleSHA {
				continue
			}
			if !nativeTagOnlyGateRoster(cfg, p, t.Binding.AuthorID) {
				return false
			}
			for _, a := range cat.Goals {
				if a.Revision.Ref != r.VersionGoal {
					continue
				}
				h := nativeDeliveryHandoff(cfg, cat, t, a, p)
				if h != nil && h.ID == r.VersionDeliveryHandoffID && tagOnlyInputSHA(h.Input) == r.VersionDeliveryInputSHA && h.Input.BaseSHA == r.VersionBaseSHA && p.BaseBranch == tag.Input.BaseBranch && p.URL != tag.Input.PRURL {
					return true
				}
			}
		}
	}
	return false
}

// Preload before any historical ReadNativeTask. No task body or arbitrary local
// JSON can create this bridge: all references come from authenticated handoffs.
func loadNativeTagOnlyBridgeEvidence(ctx context.Context, cfg Config, m NativeMaster, cat *NativeCatalog) {
	if !nativeDeliveryActor(cfg) || cfg.DeliveryHold {
		return
	}
	if cat.Reports == nil {
		cat.Reports = map[string]c.Message{}
	}
	for _, h := range cat.Handoffs {
		if h.Input.Kind != "tag-only" {
			continue
		}
		in := h.Input
		latest := nativeTagOnlyDecision(*cat, NativeTask{Task: Task{URL: in.TaskURL}, Binding: NativeTaskBinding{Goal: in.Goal}}, PR{URL: in.PRURL})
		if latest == nil || latest.ID != h.ID {
			continue
		}
		ids := []string{in.EvidenceMessageID, in.QAEvidenceMessageID, in.TagOnly.EngineeringReportID, in.TagOnly.QAReportID}
		if source := nativeTagOnlySourceProof(*cat, in); source != nil {
			ids = append(ids, source.Input.EvidenceMessageID, source.Input.Retained.EngineeringReportID, source.Input.Retained.QAReportID)
		}
		for _, id := range ids {
			if id == "" {
				continue
			}
			delete(cat.Reports, id)
			if msg, err := m.Message(ctx, id); err == nil && msg.ID == id {
				cat.Reports[id] = msg
			}
		}
	}
}

func nativeTagOnlySnapshot(ctx context.Context, cfg Config, m NativeMaster, reader NativeCatalogReader, job Job, extra ...string) (NativeCatalog, error) {
	if !nativeTagOnlyJob(cfg, job) || job.Master.Goal == nil {
		return NativeCatalog{}, c.ErrForbidden
	}
	cat := NativeCatalog{Goals: []c.GoalAuthority{{Revision: c.GoalRevision{Ref: *job.Master.Goal}}}}
	if err := loadCatalogHandoffs(ctx, m, &cat); err != nil {
		return NativeCatalog{}, err
	}
	for _, h := range cat.Handoffs {
		if h.ID == job.Master.DeliveryHandoffID && h.Input.Kind == "tag-only" && h.Input.TagOnly.VersionPRURL != "" {
			extra = append(extra, h.Input.TagOnly.VersionTaskURL)
		}
	}
	return reader.NativeTaskSnapshot(ctx, cfg, m, job, extra...)
}
