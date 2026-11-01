package worker

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func nativeTagOnlyJob(cfg Config, j Job) bool {
	return nativeDeliveryActor(cfg) && j.Kind == "tag-only" && j.Master != nil && j.Master.Role == "delivery" && j.Master.AgentID == c.DeliveryPrincipalID && j.Master.ExecutorID == c.DeliveryGitHubBotID && j.Master.DeliveryHandoffID != ""
}

func nativeTagOnlyScopeChanged(x NativeExecution, cat NativeCatalog) bool {
	if x.Binding == nil || x.Job.Master == nil {
		return true
	}
	for _, t := range cat.Tasks {
		if t.Task.URL != x.Job.TaskURL {
			continue
		}
		if !t.Task.Completed || !t.Task.Closed || NativeAssignmentSHA(t.Binding) != NativeAssignmentSHA(*x.Binding) || t.Task.BodySHA != x.Job.ScopeSHA {
			return true
		}
		for _, p := range t.PRs {
			if p.URL != x.Job.PRURL {
				continue
			}
			h := nativeTagOnlyDecision(cat, t, p)
			return h == nil || h.ID != x.Job.Master.DeliveryHandoffID || !p.Merged || p.HeadSHA != x.Job.HeadSHA || p.ScopeRevision != x.Job.PRScopeRevision || p.AssignmentSHA != x.Job.Master.AssignmentSHA || !qaArtifactEqual(p.QAArtifact, x.Job.QAArtifact)
		}
	}
	return true
}

// Order decisions before validating scope. A revoked or drifted newest grant
// must never expose an older decision for the same source PR.
func nativeTagOnlyDecision(cat NativeCatalog, t NativeTask, p PR) *c.Handoff {
	var chosen *c.Handoff
	for i := range cat.Handoffs {
		h := &cat.Handoffs[i]
		in := h.Input
		if in.Kind != "tag-only" || in.Goal != t.Binding.Goal || in.TaskURL != t.Task.URL || in.PRURL != p.URL {
			continue
		}
		if chosen == nil || h.Sequence > chosen.Sequence {
			chosen = h
		}
	}
	if chosen == nil {
		return nil
	}
	for _, h := range cat.Handoffs {
		if h.Input.Kind == "tag-only" && h.Input.Goal == t.Binding.Goal && h.Input.TaskURL == t.Task.URL && h.Input.PRURL == p.URL && h.Sequence == chosen.Sequence && h.ID != chosen.ID {
			return nil
		}
	}
	if chosen.ID == "" || chosen.Sequence < 1 || chosen.ActorKind != "owner" || chosen.ActorID == "" || chosen.CreatedAt.IsZero() || chosen.Input.Revoked || c.ValidateHandoff(chosen.Input) != nil {
		return nil
	}
	return chosen
}

func nativeTagOnlyHandoff(cfg Config, cat NativeCatalog, t NativeTask, a c.GoalAuthority, p PR) *c.Handoff {
	if !nativeDeliveryActor(cfg) || cfg.DeliveryHold || !cat.Complete || !t.Task.Completed || !t.Task.Closed || !p.Merged || a.Revision.Ref.MasterID != cfg.Master.MasterID {
		return nil
	}
	h := nativeTagOnlyDecision(cat, t, p)
	if h == nil {
		return nil
	}
	in, r := h.Input, h.Input.TagOnly
	if !nativeTagOnlyGateRoster(cfg, p, t.Binding.AuthorID) {
		return nil
	}
	policy, err := TeamPolicySHA(*cfg.Team)
	if err != nil || policy != r.ExecutionPolicySHA || t.Binding.PolicySHA != r.SourcePolicySHA || r.SourceCommitSHA != in.HeadSHA || r.SourceTreeSHA != r.IntegratedTreeSHA {
		return nil
	}
	source := nativeTagOnlySourceProof(cat, in)
	if source == nil {
		return nil
	}
	// Reuse the strict current-policy delivery evidence checks without changing
	// the registered binding or teaching ordinary delivery about completed tasks.
	base := *h
	base.Input.Kind, base.Input.Tag, base.Input.TagOnly = "deliver", "", c.TagOnlyAuthority{}
	view := cat
	view.Handoffs = nil
	for _, prior := range cat.Handoffs {
		if prior.Input.Kind != "deliver" {
			view.Handoffs = append(view.Handoffs, prior)
		}
	}
	view.Handoffs = append(view.Handoffs, base)
	if source.Input.Kind == "retained-deliver" {
		if !nativeTagOnlyRetainedSource(cfg, cat, t, a, p, *h, *source) {
			return nil
		}
	} else if nativeDeliveryHandoff(cfg, view, t, a, p) == nil {
		return nil
	}
	if r.VersionPRURL != "" && source.Input.Kind == "deliver" && (p.QAArtifact.Kind != "source" || p.QAArtifact.Revision != "source-bundle-v1" || p.QAArtifact.SHA256 != r.SourceBundleSHA) {
		return nil
	}
	if _, cleared := nativeQAState(t.Binding, a, p, cat.Reports, cat.Handoffs...); !cleared {
		return nil
	}
	owner := cat.Reports[in.EvidenceMessageID]
	peer := nativeLatestReview(p, t.Binding.PeerID)
	qa := nativeLatestReview(p, t.Binding.QAID)
	if BodyHash(owner.Body) != r.OwnerBodySHA || BodyHash(peer.Body) != r.PeerBodySHA || BodyHash(qa.Body) != r.QABodySHA || qa.ID != in.QAReviewID {
		return nil
	}
	for _, check := range []struct{ id, sha, actor string }{{r.EngineeringReportID, r.EngineeringReportBodySHA, "hawk"}, {r.QAReportID, r.QAReportBodySHA, "wren"}} {
		m, ok := cat.Reports[check.id]
		var report c.TagOnlyReport
		if !ok || m.ID != check.id || m.Imported || m.AuthorKind != "agent" || m.AuthorID != check.actor || m.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID || BodyHash(m.Body) != check.sha || c.DecodeJSON([]byte(m.Body), &report) != nil || report.Result != "ready" || report.Binding != c.TagOnlyReportBinding(in) {
			return nil
		}
	}
	if r.VersionPRURL != "" && !nativeTagOnlyVersion(cfg, cat, *h) {
		return nil
	}
	return h
}

// NativeTagOnlyCandidates is intentionally separate from ordinary closed-task
// admission. The central selector can append these candidates before sorting.
func NativeTagOnlyCandidates(cfg Config, cat NativeCatalog, history map[string]NativeExecution, now time.Time) []NativeCandidate {
	var out []NativeCandidate
	if !nativeDeliveryActor(cfg) || !cat.Complete {
		return nil
	}
	for _, task := range cat.Tasks {
		t := task
		if !nativeCacheTaskReady(cat, t.Task.URL, now) {
			continue
		}
		for _, a := range cat.Goals {
			if a.Revision.Ref != t.Binding.Goal || cat.Cache != nil && cat.Cache.OutOfScope[a.Revision.Ref] {
				continue
			}
			for _, pr := range t.PRs {
				p := pr
				h := nativeTagOnlyHandoff(cfg, cat, t, a, p)
				if h == nil {
					continue
				}
				ref := a.Revision.Ref
				mb := &MasterJobBinding{MasterID: cfg.Master.MasterID, AgentID: cfg.Master.AgentID, ThreadID: a.Revision.Proposal.ThreadID, Goal: &ref, AssignmentSHA: NativeAssignmentSHA(t.Binding), ExecutorID: cfg.Self.ID, Role: "delivery", BaseBranch: t.Binding.BaseBranch, DeliveryHandoffID: h.ID}
				j := Job{Kind: "tag-only", TaskURL: t.Task.URL, PRURL: p.URL, HeadSHA: p.HeadSHA, ScopeSHA: t.Task.BodySHA, PRScopeRevision: p.ScopeRevision, QAArtifact: p.QAArtifact, InputRevision: "tag-only:" + h.ID, Master: mb, FirstEligibleAt: now, SchedulePriority: t.Task.Priority, ScheduleOrdinal: t.Task.Ordinal}
				raw, _ := json.Marshal(struct {
					Master                         *MasterJobBinding
					Kind, Task, PR, Head, Revision string
				}{mb, j.Kind, j.TaskURL, j.PRURL, j.HeadSHA, j.InputRevision})
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
					out = append(out, NativeCandidate{Job: j, Goal: a, Task: &t, PR: &p, Handoff: h})
				}
			}
		}
	}
	return out
}

// Call after loading handoffs and before candidate selection, including task
// validation snapshots and cache snapshots. Missing evidence denies admission.
func loadNativeTagOnlyEvidence(ctx context.Context, cfg Config, m NativeMaster, cat *NativeCatalog) {
	if !nativeDeliveryActor(cfg) || cfg.DeliveryHold {
		return
	}
	if cat.Reports == nil {
		cat.Reports = map[string]c.Message{}
	}
	loadNativeTagOnlyBridgeEvidence(ctx, cfg, m, cat)
	for _, t := range cat.Tasks {
		for _, p := range t.PRs {
			h := nativeTagOnlyDecision(*cat, t, p)
			if h == nil {
				continue
			}
			for _, id := range []string{h.Input.EvidenceMessageID, h.Input.QAEvidenceMessageID, h.Input.TagOnly.EngineeringReportID, h.Input.TagOnly.QAReportID} {
				delete(cat.Reports, id)
				if msg, err := m.Message(ctx, id); err == nil && msg.ID == id {
					cat.Reports[id] = msg
				}
			}
		}
	}
}

// Discovery may retain only this explicitly authorized completed source. Final
// admission still validates the newest decision, reports and exact artifact.
func nativeTagOnlyFollowup(cfg Config, cat NativeCatalog, t NativeTask, prURL string) bool {
	if !nativeDeliveryActor(cfg) || cfg.DeliveryHold || !t.Task.Completed {
		return false
	}
	for _, h := range cat.Handoffs {
		if h.Input.Kind != "tag-only" || h.Input.Revoked || c.ValidateHandoff(h.Input) != nil {
			continue
		}
		if h.Input.Goal == t.Binding.Goal && h.Input.TaskURL == t.Task.URL && h.Input.AssignmentSHA == NativeAssignmentSHA(t.Binding) && (prURL == "" || h.Input.PRURL == prURL) {
			return true
		}
		r := h.Input.TagOnly
		if r.VersionGoal == t.Binding.Goal && r.VersionTaskURL == t.Task.URL && (prURL == "" || r.VersionPRURL == prURL) {
			for _, delivery := range cat.Handoffs {
				if delivery.ID == r.VersionDeliveryHandoffID && delivery.Input.Kind == "deliver" && delivery.Input.AssignmentSHA == NativeAssignmentSHA(t.Binding) && tagOnlyInputSHA(delivery.Input) == r.VersionDeliveryInputSHA {
					return true
				}
			}
		}
	}
	return false
}

// Original source integration remains exact. A subsequent native version PR
// has its own reviewed source, integration, registration and payload contract.
func nativeTagOnlyTarget(ctx context.Context, api releaseAPI, repo string, h c.Handoff) (releaseTarget, error) {
	r := h.Input.TagOnly
	if r.SourceCommitSHA != h.Input.HeadSHA || r.SourceTreeSHA != r.IntegratedTreeSHA {
		return releaseTarget{}, c.ErrUnsupported
	}
	owner, _, ok := strings.Cut(repo, "/")
	prRepo, n, err := parseRef(h.Input.PRURL)
	if !ok || err != nil || prRepo != repo {
		return releaseTarget{}, c.ErrForbidden
	}
	base := "repos/" + repo
	raw, _, err := api.api(ctx, owner, "GET", base+"/pulls/"+stringID(n), nil)
	if err != nil {
		return releaseTarget{}, err
	}
	var pr struct {
		Merged bool
		SHA    string `json:"merge_commit_sha"`
		Head   struct{ SHA string }
		Base   struct {
			Ref  string
			Repo struct {
				FullName string `json:"full_name"`
			}
		}
	}
	if json.Unmarshal(raw, &pr) != nil || !pr.Merged || pr.Head.SHA != h.Input.HeadSHA || pr.SHA != r.IntegratedCommitSHA || pr.Base.Ref != h.Input.BaseBranch || pr.Base.Repo.FullName != repo {
		return releaseTarget{}, c.ErrConflict
	}
	for _, commit := range []struct{ sha, tree string }{{r.SourceCommitSHA, r.SourceTreeSHA}, {r.IntegratedCommitSHA, r.IntegratedTreeSHA}} {
		raw, _, err = api.api(ctx, owner, "GET", base+"/git/commits/"+commit.sha, nil)
		if err != nil {
			return releaseTarget{}, err
		}
		var got struct {
			SHA     string
			Tree    struct{ SHA string }
			Parents []struct{ SHA string }
		}
		if json.Unmarshal(raw, &got) != nil || got.SHA != commit.sha || got.Tree.SHA != commit.tree {
			return releaseTarget{}, c.ErrConflict
		}
		if commit.sha == r.IntegratedCommitSHA && (len(got.Parents) != 1 || got.Parents[0].SHA != h.Input.BaseSHA) {
			return releaseTarget{}, c.ErrConflict
		}
	}
	raw, _, err = api.api(ctx, owner, "GET", base+"/compare/"+r.IntegratedCommitSHA+"..."+h.Input.BaseBranch, nil)
	if err != nil {
		return releaseTarget{}, err
	}
	var ancestry struct {
		Status string
		Behind int `json:"behind_by"`
	}
	if json.Unmarshal(raw, &ancestry) != nil || ancestry.Behind != 0 || ancestry.Status != "ahead" && ancestry.Status != "identical" {
		return releaseTarget{}, c.ErrConflict
	}
	if r.VersionPRURL != "" {
		return nativeTagOnlyVersionTarget(ctx, api, repo, h)
	}
	return releaseTarget{Repository: repo, SHA: r.IntegratedCommitSHA}, nil
}
