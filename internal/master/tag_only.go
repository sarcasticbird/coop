package master

import (
	"context"
	"encoding/json"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// ValidateTagOnlyHandoff is an authenticated read, not a new grant or receipt.
// It rechecks the latest decision and current native evidence before each effect.
func (svc *Service) ValidateTagOnlyHandoff(ctx context.Context, actor c.Principal, in c.TagOnlyValidation) (c.Handoff, error) {
	var h c.Handoff
	p, err := authorize(ctx, svc.Store.db, actor, "read")
	if err != nil {
		return h, err
	}
	if p.ID != c.DeliveryPrincipalID || p.Kind != "agent" || p.GitHubBotID != c.DeliveryGitHubBotID {
		return h, c.ErrForbidden
	}
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return h, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = admission(ctx, tx); err != nil {
		return h, err
	}
	a, err := svc.Store.resolveGoal(ctx, tx, p, in.Goal)
	if err != nil {
		return h, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM handoffs WHERE id=? AND goal_id=? AND revision=?", in.HandoffID, in.Goal.GoalID, in.Goal.RevisionSHA256).Scan(&raw); err != nil {
		return h, c.ErrConflict
	}
	if json.Unmarshal(raw, &h) != nil || h.ID != in.HandoffID || h.Input.Goal != in.Goal || h.Input.Kind != "tag-only" || h.Input.Revoked || h.Sequence < 1 || h.ActorKind != "owner" {
		return c.Handoff{}, c.ErrConflict
	}
	var latestRaw []byte
	if err = tx.QueryRowContext(ctx, `SELECT body FROM handoffs WHERE goal_id=? AND revision=? AND json_extract(body,'$.input.kind')='tag-only' AND json_extract(body,'$.input.task_url')=? AND json_extract(body,'$.input.pr_url')=? ORDER BY json_extract(body,'$.sequence') DESC LIMIT 1`, in.Goal.GoalID, in.Goal.RevisionSHA256, h.Input.TaskURL, h.Input.PRURL).Scan(&latestRaw); err != nil || string(latestRaw) != string(raw) {
		return c.Handoff{}, c.ErrConflict
	}
	owner, err := authorize(ctx, tx, c.Principal{ID: h.ActorID}, "decide")
	if err != nil || owner.Kind != "owner" {
		return c.Handoff{}, c.ErrForbidden
	}
	for _, thread := range []string{a.Revision.Proposal.ThreadID, a.Revision.Proposal.QA.EvidenceDestination.ThreadID} {
		if thread != "" {
			if err = member(ctx, tx, owner, thread); err != nil {
				return c.Handoff{}, err
			}
		}
	}
	if err = svc.tagOnlyAuthority(ctx, tx, p, a, h.Input); err != nil {
		return c.Handoff{}, err
	}
	return h, tx.Commit()
}

func (svc *Service) tagOnlyAuthority(ctx context.Context, q queryer, actor c.Principal, a c.GoalAuthority, in c.HandoffInput) error {
	if err := c.ValidateHandoff(in); err != nil {
		return err
	}
	r := in.TagOnly
	source, err := svc.tagOnlyDeliveryProof(ctx, q, actor, in.Goal, r.SourceDeliveryHandoffID, r.SourceDeliveryInputSHA)
	if err != nil {
		return err
	}
	if err = tagOnlyCompletionProof(ctx, q, source, r.SourceCompletionRequestID); err != nil {
		return err
	}
	if !c.TagOnlySourceMatches(in, source.Input) || r.SourceCommitSHA != in.HeadSHA || r.SourceTreeSHA != r.IntegratedTreeSHA {
		return c.ErrConflict
	}
	base := in
	base.Kind, base.Tag, base.TagOnly = "deliver", "", c.TagOnlyAuthority{}
	if source.Input.Kind == "retained-deliver" {
		historical := source.Input.Retained
		if a.State != "approved" || a.ActiveRevisionSHA256 != in.Goal.RevisionSHA256 || a.ApprovalSequence != in.ApprovalSequence || in.AcceptanceSHA != digest(a.Revision.Proposal.Acceptance) || historical.SourcePolicySHA != r.SourcePolicySHA || historical.SourceTreeSHA != r.SourceTreeSHA || historical.SourcePeerBodySHA != r.PeerBodySHA || historical.SourceQABodySHA != r.QABodySHA || r.VersionPRURL != "" && historical.PayloadSHA != r.SourceBundleSHA {
			return c.ErrConflict
		}
		if _, err := deliveryPrincipal(ctx, q, a); err != nil {
			return err
		}
		for _, check := range []struct{ id, sha, author string }{{historical.SourceReportID, historical.SourceReportBodySHA, a.Revision.Proposal.QA.ActorID}, {historical.EngineeringReportID, historical.EngineeringReportBodySHA, "hawk"}, {historical.QAReportID, historical.QAReportBodySHA, "wren"}} {
			m, e := loadMessage(ctx, q, check.id)
			if e != nil {
				return e
			}
			if m.Imported || m.AuthorKind != "agent" || m.AuthorID != check.author || m.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID || retainedBodySHA(m.Body) != check.sha || member(ctx, q, actor, m.ThreadID) != nil {
				return c.ErrForbidden
			}
		}
	} else {
		if err := svc.deliveryAuthority(ctx, q, actor, a, base); err != nil {
			return err
		}
		if r.VersionPRURL != "" && r.SourceBundleSHA != in.ArtifactSHA {
			return c.ErrConflict
		}
	}
	if svc.Tasks == nil || svc.Tasks.Capabilities().PolicySHA != r.ExecutionPolicySHA {
		return c.ErrConflict
	}
	task, err := svc.Tasks.Read(ctx, in.TaskURL)
	if err != nil {
		return err
	}
	if task.Binding == nil || task.Goal != in.Goal || task.State != "closed" || task.ClosedReason != "done" || digest(task.Binding) != in.AssignmentSHA || task.Binding.PolicySHA != r.SourcePolicySHA || task.Binding.TaskScopeSHA != in.TaskScopeSHA || task.Binding.BaseBranch != in.BaseBranch || task.Binding.TaskScopeSHA != digest(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{task.Title, task.Body}) {
		return c.ErrConflict
	}
	ownerEvidence, err := loadMessage(ctx, q, in.EvidenceMessageID)
	if err != nil {
		return err
	}
	owner, err := authorize(ctx, q, c.Principal{ID: ownerEvidence.AuthorID}, "decide")
	if err != nil || owner.Kind != "owner" || ownerEvidence.AuthorKind != "owner" || ownerEvidence.Imported || (ownerEvidence.ThreadID != a.Revision.Proposal.ThreadID && ownerEvidence.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID) || retainedBodySHA(ownerEvidence.Body) != r.OwnerBodySHA || member(ctx, q, owner, ownerEvidence.ThreadID) != nil || member(ctx, q, actor, ownerEvidence.ThreadID) != nil {
		return c.ErrForbidden
	}
	var previousBot int64
	for _, check := range []struct{ id, sha, author string }{{r.EngineeringReportID, r.EngineeringReportBodySHA, "hawk"}, {r.QAReportID, r.QAReportBodySHA, "wren"}} {
		p, e := authorize(ctx, q, c.Principal{ID: check.author}, "read")
		if e != nil || p.Kind != "agent" || p.GitHubBotID == 0 || p.GitHubBotID == previousBot || p.GitHubBotID == c.DeliveryGitHubBotID || p.GitHubBotID == task.Binding.AuthorID || member(ctx, q, p, a.Revision.Proposal.QA.EvidenceDestination.ThreadID) != nil {
			return c.ErrForbidden
		}
		previousBot = p.GitHubBotID
		m, e := loadMessage(ctx, q, check.id)
		if e != nil {
			return e
		}
		var report c.TagOnlyReport
		if m.Imported || m.AuthorKind != "agent" || m.AuthorID != check.author || m.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID || retainedBodySHA(m.Body) != check.sha || c.DecodeJSON([]byte(m.Body), &report) != nil || report.Result != "ready" || report.Binding != c.TagOnlyReportBinding(in) || member(ctx, q, actor, m.ThreadID) != nil {
			return c.ErrForbidden
		}
	}
	if r.VersionPRURL != "" {
		version, e := svc.tagOnlyDeliveryProof(ctx, q, actor, r.VersionGoal, r.VersionDeliveryHandoffID, r.VersionDeliveryInputSHA)
		if e != nil {
			return e
		}
		if e = tagOnlyCompletionProof(ctx, q, version, r.VersionCompletionRequestID); e != nil {
			return e
		}
		v := version.Input
		if v.Kind != "deliver" || v.TaskURL != r.VersionTaskURL || v.PRURL != r.VersionPRURL || v.HeadSHA != r.VersionHeadSHA || v.ArtifactSHA != r.VersionBundleSHA || v.PRURL == in.PRURL || v.BaseBranch != in.BaseBranch || v.BaseSHA != r.VersionBaseSHA {
			return c.ErrConflict
		}
		va, e := svc.Store.resolveGoal(ctx, q, actor, r.VersionGoal)
		if e != nil {
			return e
		}
		if e = svc.deliveryAuthority(ctx, q, actor, va, v); e != nil {
			return e
		}
		vt, e := svc.Tasks.Read(ctx, v.TaskURL)
		if e != nil {
			return e
		}
		if vt.Binding == nil || vt.Binding.PolicySHA != r.ExecutionPolicySHA || digest(vt.Binding) != v.AssignmentSHA || vt.State != "closed" || vt.ClosedReason != "done" || vt.Repository != task.Repository || vt.Goal != r.VersionGoal || vt.Binding.TaskScopeSHA != digest(struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}{vt.Title, vt.Body}) {
			return c.ErrConflict
		}
		if vt.Reference != v.TaskURL || vt.Binding.TaskURL != v.TaskURL || vt.Binding.Goal != v.Goal || vt.Binding.TaskScopeSHA != v.TaskScopeSHA || vt.Binding.BaseBranch != v.BaseBranch {
			return c.ErrConflict
		}
		if e = svc.taskBindingAuthority(ctx, q, actor, *vt.Binding); e != nil {
			return e
		}
		for _, id := range []string{"hawk", "wren"} {
			p, e := authorize(ctx, q, c.Principal{ID: id}, "read")
			if e != nil || p.GitHubBotID == vt.Binding.AuthorID {
				return c.ErrForbidden
			}
		}
	}
	return nil
}

func tagOnlyCompletionProof(ctx context.Context, q queryer, delivery c.Handoff, id string) error {
	in := delivery.Input
	expected := c.TaskCompleteInput{Goal: in.Goal, Reference: in.TaskURL, AssignmentSHA: in.AssignmentSHA, PullRequestURL: in.PRURL, DeliveryHandoffID: delivery.ID, RequestID: id}
	var accepted bool
	found, err := request(ctx, q, c.DeliveryPrincipalID, id, "task-complete", expected, &accepted)
	if err != nil {
		return err
	}
	if !found || !accepted {
		return c.ErrConflict
	}
	return nil
}

// A historical source proof is not regenerated under today's roster. Native
// storage, exact digest, original owner, registration and no later withdrawal
// are required; completed provider state and the exact merge are checked too.
func (svc *Service) tagOnlyDeliveryProof(ctx context.Context, q queryer, actor c.Principal, goal c.GoalRef, id, sha string) (c.Handoff, error) {
	var h c.Handoff
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT body FROM handoffs WHERE id=? AND goal_id=? AND revision=?", id, goal.GoalID, goal.RevisionSHA256).Scan(&raw); err != nil {
		return h, c.ErrConflict
	}
	if json.Unmarshal(raw, &h) != nil || h.ID != id || h.ActorKind != "owner" || h.Sequence < 1 || h.Input.Goal != goal || h.Input.Revoked || (h.Input.Kind != "deliver" && h.Input.Kind != "retained-deliver") || c.ValidateHandoff(h.Input) != nil || digest(h.Input) != sha {
		return c.Handoff{}, c.ErrConflict
	}
	var latest []byte
	if err := q.QueryRowContext(ctx, `SELECT body FROM handoffs WHERE goal_id=? AND revision=? AND json_extract(body,'$.input.kind')=? AND json_extract(body,'$.input.task_url')=? AND json_extract(body,'$.input.pr_url')=? ORDER BY json_extract(body,'$.sequence') DESC LIMIT 1`, goal.GoalID, goal.RevisionSHA256, h.Input.Kind, h.Input.TaskURL, h.Input.PRURL).Scan(&latest); err != nil || string(latest) != string(raw) {
		return c.Handoff{}, c.ErrConflict
	}
	for _, link := range []struct{ url, kind string }{{h.Input.TaskURL, "task"}, {h.Input.PRURL, "pull_request"}} {
		var assignment string
		var body []byte
		if err := q.QueryRowContext(ctx, "SELECT assignment_sha,body FROM artifacts WHERE goal_id=? AND revision=? AND url=? AND kind=?", goal.GoalID, goal.RevisionSHA256, link.url, link.kind).Scan(&assignment, &body); err != nil || assignment != h.Input.AssignmentSHA {
			return c.Handoff{}, c.ErrConflict
		}
		var registered c.ArtifactLink
		if json.Unmarshal(body, &registered) != nil || registered.Goal != goal || registered.URL != link.url || registered.Kind != link.kind || registered.AssignmentSHA != assignment || link.kind == "pull_request" && registered.TaskURL != h.Input.TaskURL {
			return c.Handoff{}, c.ErrConflict
		}
	}
	owner, err := authorize(ctx, q, c.Principal{ID: h.ActorID}, "decide")
	if err != nil || owner.Kind != "owner" {
		return c.Handoff{}, c.ErrForbidden
	}
	m, err := loadMessage(ctx, q, h.Input.EvidenceMessageID)
	if err != nil {
		return c.Handoff{}, err
	}
	if m.Imported || m.AuthorKind != "owner" || member(ctx, q, owner, m.ThreadID) != nil || member(ctx, q, actor, m.ThreadID) != nil {
		return c.Handoff{}, c.ErrForbidden
	}
	return h, nil
}
