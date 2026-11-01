package worker

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type nativeRetainedReadKey struct{}

func nativeValidateRetainedDecision(ctx context.Context, m NativeMaster, h c.Handoff) error {
	validator, ok := m.(interface {
		ValidateRetainedDeliveryHandoff(context.Context, c.RetainedDeliveryValidation) (c.Handoff, error)
	})
	if !ok {
		return c.ErrUnsupported
	}
	current, err := validator.ValidateRetainedDeliveryHandoff(ctx, c.RetainedDeliveryValidation{Goal: h.Input.Goal, HandoffID: h.ID})
	if err != nil {
		return err
	}
	if current != h {
		return c.ErrForbidden
	}
	return nil
}

// Recorded references may suppress an inaccessible retained candidate, never
// grant it access. Registration still prevents replacement planning.
func nativeRetainedTask(cat NativeCatalog, cfg Config, goal c.GoalRef, task string) bool {
	for _, h := range cat.Handoffs {
		if (h.Input.Kind == "retained-gates" || h.Input.Kind == "retained-deliver") && h.Input.Goal == goal && h.Input.TaskURL == task && (nativeRetainedActor(cfg, h.Input.Kind) || nativeDeliveryActor(cfg)) {
			return true
		}
	}
	return false
}

func nativeRetainedGate(kind string) bool { return kind == "delivery-review" || kind == "delivery-qa" }

func nativeRetainedGateJob(cfg Config, j Job) bool {
	want, role := "hawk", "engineering-review"
	if j.Kind == "delivery-qa" {
		want, role = "wren", "customer-qa"
	}
	return nativeRetainedGate(j.Kind) && nativeRetainedActor(cfg, "retained-gates") && cfg.Name == want && j.Master != nil && j.Master.Role == role && j.Master.AgentID == want && j.Master.ExecutorID == cfg.Self.ID && j.Master.DeliveryHandoffID != "" && j.InputRevision == "retained:"+j.Master.DeliveryHandoffID
}

// Order before validating, including assignment drift and revocation. Never
// hydrate evidence from a superseded decision to rescue its replacement.
func nativeRetainedDecision(cat NativeCatalog, goal c.GoalRef, task, pr, kind string) *c.Handoff {
	// Delivery grants share one decision stream; retained gates do not.
	matches := func(h c.Handoff) bool {
		delivery := kind == "deliver" || kind == "retained-deliver"
		return (h.Input.Kind == kind || delivery && (h.Input.Kind == "deliver" || h.Input.Kind == "retained-deliver")) && h.Input.Goal == goal && h.Input.TaskURL == task && h.Input.PRURL == pr
	}
	var chosen *c.Handoff
	for i := range cat.Handoffs {
		h := &cat.Handoffs[i]
		if matches(*h) && (chosen == nil || h.Sequence > chosen.Sequence) {
			chosen = h
		}
	}
	if chosen == nil {
		return nil
	}
	for _, h := range cat.Handoffs {
		if matches(h) && h.Sequence == chosen.Sequence && h != *chosen {
			return nil
		}
	}
	if chosen.Input.Kind != kind || chosen.ID == "" || chosen.Sequence < 1 || chosen.CreatedAt.IsZero() || chosen.ActorKind != "owner" || chosen.ActorID == "" || chosen.Input.Revoked || c.ValidateHandoff(chosen.Input) != nil {
		return nil
	}
	return chosen
}

func nativeRetainedActor(cfg Config, kind string) bool {
	if cfg.Master == nil || cfg.Team == nil {
		return false
	}
	if kind == "retained-deliver" {
		return nativeDeliveryActor(cfg)
	}
	m, ok := TeamMemberForActor(*cfg.Team, cfg.Self)
	return ok && cfg.Self.Kind == "Bot" && cfg.Name == m.Name && cfg.Master.AgentID == m.Name && (m.Name == "hawk" && memberCapability(m, "engineering-review") || m.Name == "wren" && memberCapability(m, "customer-qa"))
}

func nativeRetainedRoster(cfg Config, cat NativeCatalog, b NativeTaskBinding, h *c.Handoff) bool {
	if cfg.Version != 3 || cfg.Master == nil || b.Goal.Source != "master" || b.Goal.MasterID != cfg.Master.MasterID || h == nil || !nativeRetainedActor(cfg, h.Input.Kind) || validateNativeActorRoster(cfg, b) != nil {
		return false
	}
	in, r := h.Input, h.Input.Retained
	policy, err := TeamPolicySHA(*cfg.Team)
	if err != nil || policy != r.ExecutionPolicySHA || b.PolicySHA != r.SourcePolicySHA || in.Goal != b.Goal || in.TaskURL != b.TaskURL || in.AssignmentSHA != NativeAssignmentSHA(b) || in.TaskScopeSHA != b.TaskScopeSHA || in.BaseBranch != b.BaseBranch || in.ApprovalSequence != b.ApprovalSequence {
		return false
	}
	for _, a := range cat.Goals {
		if a.Valid && a.State == "approved" && a.Revision.Ref == b.Goal && a.ActiveRevisionSHA256 == b.Goal.RevisionSHA256 && a.ApprovalSequence == b.ApprovalSequence && in.AcceptanceSHA == NativeAcceptanceSHA(a) {
			repo, err := nativeTaskRepo(b.TaskURL)
			allowed := false
			for _, scope := range a.Revision.Proposal.Repositories {
				if scope.Name == repo && containsExact(scope.BaseBranches, b.BaseBranch) {
					allowed = true
				}
			}
			if err != nil || !allowed {
				return false
			}
			m, ok := cat.Reports[in.EvidenceMessageID]
			return ok && m.ID == in.EvidenceMessageID && !m.Imported && m.AuthorKind == "owner" && (m.ThreadID == a.Revision.Proposal.ThreadID || m.ThreadID == a.Revision.Proposal.QA.EvidenceDestination.ThreadID)
		}
	}
	return false
}

func nativeReadRoster(ctx context.Context, cfg Config, b NativeTaskBinding) error {
	err := validateNativeRoster(cfg, b)
	if err == nil {
		return nil
	}
	cat, ok := ctx.Value(nativeRetainedReadKey{}).(*NativeCatalog)
	if !ok {
		return err
	}
	if nativeTagOnlyReadRoster(cfg, *cat, b) {
		return nil
	}
	for _, h := range cat.Handoffs {
		if h.Input.TaskURL != b.TaskURL || h.Input.Goal != b.Goal {
			continue
		}
		if nativeDeliveryActor(cfg) && h.Input.Kind == "retained-gates" {
			continue
		}
		latest := nativeRetainedDecision(*cat, b.Goal, b.TaskURL, h.Input.PRURL, h.Input.Kind)
		if (h.Input.Kind == "retained-gates" || h.Input.Kind == "retained-deliver") && nativeRetainedRoster(cfg, *cat, b, latest) {
			return nil
		}
	}
	return err
}

// Called only with freshly authenticated handoffs. Evidence failures remain
// local to that candidate; inaccessible revoked history is never read.
func loadNativeRetainedEvidence(ctx context.Context, cfg Config, m NativeMaster, cat *NativeCatalog) {
	if cat.Reports == nil {
		cat.Reports = map[string]c.Message{}
	}
	for _, h := range cat.Handoffs {
		in := h.Input
		if (in.Kind != "retained-gates" && in.Kind != "retained-deliver") || !nativeRetainedActor(cfg, in.Kind) && !nativeDeliveryActor(cfg) {
			continue
		}
		latest := nativeRetainedDecision(*cat, in.Goal, in.TaskURL, in.PRURL, in.Kind)
		if latest == nil || latest.ID != h.ID {
			continue
		}
		threads, ok := m.(interface {
			Thread(context.Context, string) (c.Thread, error)
		})
		if !ok {
			continue
		}
		members := false
		for _, a := range cat.Goals {
			if a.Revision.Ref != in.Goal {
				continue
			}
			members = true
			for _, id := range []string{a.Revision.Proposal.ThreadID, a.Revision.Proposal.QA.EvidenceDestination.ThreadID} {
				th, err := threads.Thread(ctx, id)
				if err != nil || th.ID != id || th.Engagement != cfg.Master.Engagement {
					members = false
					break
				}
				for _, principal := range []string{h.ActorID, cfg.Master.AgentID, "hawk", "wren", "rook", a.Actors.Author.PrincipalID, a.Actors.Peer.PrincipalID, a.Actors.QA.PrincipalID} {
					if !containsExact(th.Members, principal) {
						members = false
					}
				}
			}
		}
		if !members {
			continue
		}
		r := in.Retained
		for _, id := range []string{in.EvidenceMessageID, r.SourceReportID, r.EngineeringReportID, r.QAReportID} {
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

func nativeRetainedSource(cfg Config, cat NativeCatalog, t NativeTask, a c.GoalAuthority, p PR, h *c.Handoff) bool {
	if cfg.DeliveryHold || !cat.Complete || !nativeRetainedRoster(cfg, cat, t.Binding, h) || t.Task.BodySHA != t.Binding.TaskScopeSHA {
		return false
	}
	if a.Actors.Author.GitHubBotID != t.Binding.AuthorID || a.Actors.Peer.GitHubBotID != t.Binding.PeerID || a.Actors.QA.GitHubBotID != t.Binding.QAID || a.Actors.Author.PrincipalID != a.Revision.Proposal.AuthorID || a.Actors.Peer.PrincipalID != a.Revision.Proposal.PeerID || a.Actors.QA.PrincipalID != a.Revision.Proposal.QA.ActorID {
		return false
	}
	in, r := h.Input, h.Input.Retained
	if in.Goal != a.Revision.Ref || !a.Revision.Proposal.QA.Required || in.HeadSHA != p.HeadSHA || in.PRScopeRevision != p.ScopeRevision || in.BaseBranch != p.BaseBranch || !p.Merged && in.BaseSHA != p.BaseSHA || p.TaskURL != t.Task.URL || p.AssignmentSHA != in.AssignmentSHA || p.MasterGoal == nil || *p.MasterGoal != in.Goal || p.Author.ID != t.Binding.AuthorID || p.Author.Kind != "Bot" || !p.ReviewsComplete || !p.ContributorsComplete || p.QAArtifact == nil || p.QAArtifact.HeadSHA != p.HeadSHA || p.QAArtifact.SHA256 != in.ArtifactSHA || cat.RetainedPayloads[p.URL] != r.PayloadSHA || cat.RetainedTrees[p.URL] != r.SourceTreeSHA || p.ChecksState != "success" && p.ChecksState != "none" || !p.Merged && (p.Closed || p.MergeState != "CLEAN") {
		return false
	}
	peer, peerOK := nativePeerCleared(t.Binding, p)
	qa, qaOK := nativeQAState(t.Binding, a, p, cat.Reports, cat.Handoffs...)
	var result NativeQAResult
	if !peerOK || !qaOK || nativeMetadata(qa.Body, "qa", &result) != nil || peer.ID != in.PeerReviewID || peer.ID != r.SourcePeerReviewID || BodyHash(peer.Body) != r.SourcePeerBodySHA || qa.ID != in.QAReviewID || qa.ID != r.SourceQAReviewID || BodyHash(qa.Body) != r.SourceQABodySHA || result.EvidenceMessageID != r.SourceReportID || result.EvidenceBodySHA != r.SourceReportBodySHA || in.QAEvidenceMessageID != r.SourceReportID || in.QAEvidenceBodySHA != r.SourceReportBodySHA || result.Environment != r.SourceEnvironment || result.EnvironmentRevision != r.SourceEnvironmentRevision || r.DeliveryAcceptanceSHA != NativeAcceptanceSHA(a) {
		return false
	}
	if !nativeRetainedCurrentEnvironment(t.Binding, a, p, in, cat.Handoffs) {
		return false
	}
	for _, name := range []string{"hawk", "wren", "rook"} {
		found := false
		for _, m := range cfg.Team.Members {
			if m.Name == name {
				capability := map[string]string{"hawk": "engineering-review", "wren": "customer-qa", "rook": "deliver"}[name]
				found = m.Actor.Kind == "Bot" && memberCapability(m, capability) && m.Actor.ID != t.Binding.AuthorID && !nativeContributor(p, m.Actor.ID)
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Delivery and later tag execution retain current environment authority without
// substituting it for the environment of the immutable source QA assessment.
func nativeRetainedCurrentEnvironment(b NativeTaskBinding, a c.GoalAuthority, p PR, in c.HandoffInput, handoffs []c.Handoff) bool {
	env, err := resolveNativeQAEnvironment(b, a, p, nil, handoffs)
	if err != nil {
		return false
	}
	if in.Environment == env.Name && in.EnvironmentRevision == env.Revision {
		return true
	}
	for i := range handoffs {
		h := &handoffs[i]
		if h.Input.Kind != "reassess-qa" || h.ActorKind != "owner" || h.ActorID == "" || h.Input.Revoked || h.Input.Environment != in.Environment || h.Input.EnvironmentRevision != in.EnvironmentRevision {
			continue
		}
		resolved, err := resolveNativeQAEnvironment(b, a, p, h, handoffs)
		if err == nil && resolved.Name == in.Environment && resolved.Revision == in.EnvironmentRevision {
			return true
		}
	}
	return false
}

func nativeRetainedHandoff(cfg Config, cat NativeCatalog, t NativeTask, a c.GoalAuthority, p PR, kind string) *c.Handoff {
	h := nativeRetainedDecision(cat, t.Binding.Goal, t.Task.URL, p.URL, kind)
	if !nativeRetainedSource(cfg, cat, t, a, p, h) {
		return nil
	}
	if kind == "retained-gates" {
		return h
	}
	if cfg.DeliveryHold {
		return nil
	}
	gates := nativeRetainedDecision(cat, t.Binding.Goal, t.Task.URL, p.URL, "retained-gates")
	if gates == nil {
		return nil
	}
	expected := h.Input
	expected.Kind, expected.RequestID, expected.EvidenceMessageID = "retained-gates", gates.Input.RequestID, gates.Input.EvidenceMessageID
	expected.Retained.EngineeringReportID, expected.Retained.EngineeringReportBodySHA, expected.Retained.QAReportID, expected.Retained.QAReportBodySHA = "", "", "", ""
	if expected != gates.Input {
		return nil
	}
	// Gate evidence has the same immutable source, but a separate owner decision.
	owner := cat.Reports[gates.Input.EvidenceMessageID]
	if owner.ID != gates.Input.EvidenceMessageID || owner.Imported || owner.AuthorKind != "owner" || owner.ThreadID != a.Revision.Proposal.ThreadID && owner.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID {
		return nil
	}
	r := h.Input.Retained
	for _, check := range []struct{ id, sha, actor string }{{r.EngineeringReportID, r.EngineeringReportBodySHA, "hawk"}, {r.QAReportID, r.QAReportBodySHA, "wren"}} {
		m := cat.Reports[check.id]
		var report c.RetainedGateReport
		if m.ID != check.id || m.Imported || m.AuthorKind != "agent" || m.AuthorID != check.actor || m.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID || BodyHash(m.Body) != check.sha || c.DecodeJSON([]byte(m.Body), &report) != nil || report.GateHandoffID != gates.ID || report.AssignmentSHA != NativeAssignmentSHA(t.Binding) || report.Binding != gates.Input || report.Result != "ready" {
			return nil
		}
	}
	return h
}

func loadNativeRetainedTrees(ctx context.Context, cfg Config, g *GitHubClient, cat *NativeCatalog) {
	cat.RetainedTrees = map[string]string{}
	cat.RetainedPayloads = map[string]string{}
	for _, t := range cat.Tasks {
		for _, p := range t.PRs {
			var h *c.Handoff
			for _, kind := range []string{"retained-gates", "retained-deliver"} {
				v := nativeRetainedDecision(*cat, t.Binding.Goal, t.Task.URL, p.URL, kind)
				if nativeRetainedRoster(cfg, *cat, t.Binding, v) {
					h = v
				}
			}
			if h == nil {
				continue
			}
			raw, _, err := g.api(ctx, strings.Split(t.Task.Repo, "/")[0], "GET", "repos/"+t.Task.Repo+"/git/commits/"+p.HeadSHA, nil)
			var commit struct {
				SHA  string
				Tree struct{ SHA string }
			}
			if err == nil && json.Unmarshal(raw, &commit) == nil && commit.SHA == p.HeadSHA && qaHeadSHA.MatchString(commit.Tree.SHA) {
				cat.RetainedTrees[p.URL] = commit.Tree.SHA
			}
			if p.QAArtifact != nil && p.QAArtifact.Kind == "source" && p.QAArtifact.Revision == "source-bundle-v1" && p.QAArtifact.HeadSHA == p.HeadSHA {
				cat.RetainedPayloads[p.URL] = p.QAArtifact.SHA256
			} else if raw, _, err := g.api(ctx, strings.Split(t.Task.Repo, "/")[0], "GET", "repos/"+t.Task.Repo+"/tarball/"+p.HeadSHA, nil); err == nil {
				if sha, err := sourceBundleSHA(raw); err == nil {
					cat.RetainedPayloads[p.URL] = sha
				}
			}
		}
	}
}

func nativeRetainedCandidates(cfg Config, cat NativeCatalog, history map[string]NativeExecution, now time.Time) []NativeCandidate {
	var out []NativeCandidate
	if cfg.Version != 3 || cfg.Master == nil || cfg.Team == nil || !cat.Complete {
		return nil
	}
	for _, t := range cat.Tasks {
		if t.Task.Closed || t.Task.Completed || !nativeCacheTaskReady(cat, t.Task.URL, now) {
			continue
		}
		deps := true
		for _, ref := range t.Binding.DependsOn {
			done := false
			for _, d := range cat.Tasks {
				if d.Task.URL == ref && d.Binding.Goal == t.Binding.Goal && d.Task.Completed && nativeCacheTaskReady(cat, ref, now) {
					done = true
				}
			}
			deps = deps && done
		}
		if !deps {
			continue
		}
		live := 0
		for _, p := range t.PRs {
			if !p.Closed || p.Merged {
				live++
			}
		}
		if live != 1 {
			continue
		}
		for _, a := range cat.Goals {
			if a.Revision.Ref != t.Binding.Goal || cat.Cache != nil && cat.Cache.OutOfScope[a.Revision.Ref] {
				continue
			}
			for _, p := range t.PRs {
				if p.Merged {
					continue
				}
				kind, role, decision := "delivery-review", "engineering-review", "retained-gates"
				if cfg.Name == "wren" {
					kind, role = "delivery-qa", "customer-qa"
				}
				if nativeDeliveryActor(cfg) {
					kind, role, decision = "deliver", "delivery", "retained-deliver"
				}
				h := nativeRetainedHandoff(cfg, cat, t, a, p, decision)
				if h == nil {
					continue
				}
				ref := a.Revision.Ref
				mb := &MasterJobBinding{MasterID: cfg.Master.MasterID, AgentID: cfg.Master.AgentID, ThreadID: a.Revision.Proposal.ThreadID, Goal: &ref, AssignmentSHA: NativeAssignmentSHA(t.Binding), ExecutorID: cfg.Self.ID, Role: role, BaseBranch: t.Binding.BaseBranch, DeliveryHandoffID: h.ID}
				j := Job{Kind: kind, TaskURL: t.Task.URL, PRURL: p.URL, HeadSHA: p.HeadSHA, ScopeSHA: t.Task.BodySHA, PRScopeRevision: p.ScopeRevision, QAArtifact: p.QAArtifact, InputRevision: "retained:" + h.ID, Master: mb, FirstEligibleAt: now, SchedulePriority: t.Task.Priority, ScheduleOrdinal: t.Task.Ordinal}
				raw, _ := json.Marshal(struct {
					Master                         *MasterJobBinding
					Kind, Task, PR, Head, Revision string
				}{mb, kind, j.TaskURL, j.PRURL, j.HeadSHA, j.InputRevision})
				j.ID = BodyHash(string(raw))
				if _, seen := history[j.ID]; seen {
					continue
				}
				blocked := false
				for _, x := range history {
					if x.Phase == "blocked" && BindingKey(cfg, x.Job) == BindingKey(cfg, j) {
						blocked = true
					}
				}
				if !blocked {
					task, pr := t, p
					out = append(out, NativeCandidate{Job: j, Goal: a, Task: &task, PR: &pr, Handoff: h})
				}
			}
		}
	}
	return out
}
