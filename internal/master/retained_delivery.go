package master

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func retainedBodySHA(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

// ValidateRetainedDeliveryHandoff is an authenticated read, not a grant or a
// completion receipt. Workers must call it immediately before retained effects,
// including completion recovery, and still validate the live GitHub artifact.
func (svc *Service) ValidateRetainedDeliveryHandoff(ctx context.Context, actor c.Principal, in c.RetainedDeliveryValidation) (c.Handoff, error) {
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return c.Handoff{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := authorize(ctx, tx, actor, "read")
	if err != nil {
		return c.Handoff{}, err
	}
	if err = admission(ctx, tx); err != nil {
		return c.Handoff{}, err
	}
	a, err := svc.Store.resolveGoal(ctx, tx, p, in.Goal)
	if err != nil {
		return c.Handoff{}, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM handoffs WHERE id=? AND goal_id=? AND revision=?", in.HandoffID, in.Goal.GoalID, in.Goal.RevisionSHA256).Scan(&raw); err != nil {
		return c.Handoff{}, c.ErrConflict
	}
	var h c.Handoff
	if json.Unmarshal(raw, &h) != nil || h.ID != in.HandoffID || h.Input.Goal != in.Goal || h.Sequence < 1 || h.ActorKind != "owner" || h.Input.Revoked {
		return c.Handoff{}, c.ErrConflict
	}
	switch h.Input.Kind {
	case "retained-deliver":
		if p.ID != c.DeliveryPrincipalID || p.Kind != "agent" || p.GitHubBotID != c.DeliveryGitHubBotID {
			return c.Handoff{}, c.ErrForbidden
		}
	case "retained-gates":
		if (p.ID != "hawk" && p.ID != "wren") || p.Kind != "agent" || p.GitHubBotID == 0 {
			return c.Handoff{}, c.ErrForbidden
		}
	default:
		return c.Handoff{}, c.ErrConflict
	}
	var latest []byte
	// Match TaskComplete selection exactly: a newer ordinary delivery also
	// supersedes retained delivery, even when revoked or no longer applicable.
	kinds := "json_extract(body,'$.input.kind') IN ('deliver','retained-deliver')"
	if h.Input.Kind == "retained-gates" {
		kinds = "json_extract(body,'$.input.kind')='retained-gates'"
	}
	if err = tx.QueryRowContext(ctx, `SELECT body FROM handoffs WHERE goal_id=? AND revision=? AND `+kinds+` AND json_extract(body,'$.input.task_url')=? AND json_extract(body,'$.input.pr_url')=? ORDER BY json_extract(body,'$.sequence') DESC LIMIT 1`, in.Goal.GoalID, in.Goal.RevisionSHA256, h.Input.TaskURL, h.Input.PRURL).Scan(&latest); err != nil || string(latest) != string(raw) {
		return c.Handoff{}, c.ErrConflict
	}
	owner, err := authorize(ctx, tx, c.Principal{ID: h.ActorID}, "decide")
	if err != nil || owner.Kind != "owner" || member(ctx, tx, owner, a.Revision.Proposal.ThreadID) != nil || member(ctx, tx, owner, a.Revision.Proposal.QA.EvidenceDestination.ThreadID) != nil {
		return c.Handoff{}, c.ErrForbidden
	}
	if err = svc.retainedAuthority(ctx, tx, p, a, h.Input); err != nil {
		return c.Handoff{}, err
	}
	return h, tx.Commit()
}

// Retained execution deliberately does not regenerate a binding from today's
// roster. The registered source binding remains the identity of the work.
func (svc *Service) retainedAuthority(ctx context.Context, q queryer, actor c.Principal, a c.GoalAuthority, in c.HandoffInput) error {
	if err := c.ValidateHandoff(in); err != nil {
		return err
	}
	r := in.Retained
	if svc.Tasks == nil || svc.Tasks.Capabilities().PolicySHA != r.ExecutionPolicySHA || a.Revision.Ref != in.Goal || a.State != "approved" || a.ActiveRevisionSHA256 != in.Goal.RevisionSHA256 || a.ApprovalSequence != in.ApprovalSequence || in.AcceptanceSHA != digest(a.Revision.Proposal.Acceptance) {
		return c.ErrConflict
	}
	if _, err := deliveryPrincipal(ctx, q, a); err != nil {
		return err
	}
	task, err := svc.Tasks.Read(ctx, in.TaskURL)
	if err != nil {
		return err
	}
	b := task.Binding
	if b == nil || task.Reference != in.TaskURL || task.Goal != in.Goal || b.Goal != in.Goal || b.TaskURL != in.TaskURL || digest(b) != in.AssignmentSHA || b.PolicySHA != r.SourcePolicySHA || b.TaskScopeSHA != in.TaskScopeSHA || b.BaseBranch != in.BaseBranch || b.ApprovalSequence != in.ApprovalSequence || b.TaskScopeSHA != digest(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{task.Title, task.Body}) {
		return c.ErrConflict
	}
	allowed := false
	for _, repo := range a.Revision.Proposal.Repositories {
		for _, base := range repo.BaseBranches {
			if repo.Name == task.Repository && base == b.BaseBranch {
				allowed = true
			}
		}
	}
	if !allowed || r.SourcePeerReviewID != in.PeerReviewID || r.SourceQAReviewID != in.QAReviewID || r.SourceReportID != in.QAEvidenceMessageID || r.SourceReportBodySHA != in.QAEvidenceBodySHA {
		return c.ErrConflict
	}
	for _, link := range []struct{ url, kind string }{{in.TaskURL, "task"}, {in.PRURL, "pull_request"}} {
		pathKind := "issues"
		if link.kind == "pull_request" {
			pathKind = "pull"
		}
		repo, e := svc.artifactRepository(link.url, pathKind)
		if e != nil || repo != task.Repository {
			return c.ErrConflict
		}
		var assignment string
		var raw []byte
		if err = q.QueryRowContext(ctx, "SELECT assignment_sha,body FROM artifacts WHERE goal_id=? AND revision=? AND url=? AND kind=?", in.Goal.GoalID, in.Goal.RevisionSHA256, link.url, link.kind).Scan(&assignment, &raw); err != nil || assignment != in.AssignmentSHA {
			return c.ErrConflict
		}
		var registered c.ArtifactLink
		if json.Unmarshal(raw, &registered) != nil || registered.Goal != in.Goal || registered.URL != link.url || registered.Kind != link.kind || registered.AssignmentSHA != assignment || link.kind == "pull_request" && registered.TaskURL != in.TaskURL {
			return c.ErrConflict
		}
	}
	ownerEvidence, err := loadMessage(ctx, q, in.EvidenceMessageID)
	if err != nil {
		return err
	}
	owner, err := authorize(ctx, q, c.Principal{ID: ownerEvidence.AuthorID}, "decide")
	if err != nil || owner.Kind != "owner" || ownerEvidence.AuthorKind != "owner" || ownerEvidence.Imported || (ownerEvidence.ThreadID != a.Revision.Proposal.ThreadID && ownerEvidence.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID) || member(ctx, q, owner, ownerEvidence.ThreadID) != nil || member(ctx, q, actor, ownerEvidence.ThreadID) != nil {
		return c.ErrForbidden
	}
	source, err := loadMessage(ctx, q, r.SourceReportID)
	if err != nil {
		return err
	}
	if source.Imported || source.AuthorKind != "agent" || source.AuthorID != a.Revision.Proposal.QA.ActorID || source.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID || retainedBodySHA(source.Body) != r.SourceReportBodySHA {
		return c.ErrForbidden
	}
	if r.SourceEnvironment != a.Revision.Proposal.QA.Environment || r.SourceEnvironmentRevision != a.Revision.Proposal.QA.EnvironmentRevision {
		sourceEnvironment := in
		sourceEnvironment.Environment, sourceEnvironment.EnvironmentRevision = r.SourceEnvironment, r.SourceEnvironmentRevision
		if err = deliveryQAEnvironment(ctx, q, actor, a, sourceEnvironment); err != nil {
			return err
		}
	}
	// Current execution scope and historical QA scope are separate assertions.
	// Approval of one must never silently relabel the other.
	if in.Environment != a.Revision.Proposal.QA.Environment || in.EnvironmentRevision != a.Revision.Proposal.QA.EnvironmentRevision {
		if err = deliveryQAEnvironment(ctx, q, actor, a, in); err != nil {
			return err
		}
	}
	var gateBotID int64
	for _, id := range []string{"hawk", "wren"} {
		p, e := authorize(ctx, q, c.Principal{ID: id}, "read")
		if e != nil || p.Kind != "agent" || p.GitHubBotID == 0 || p.GitHubBotID == gateBotID || p.GitHubBotID == c.DeliveryGitHubBotID || id == a.Revision.Proposal.AuthorID || p.GitHubBotID == b.AuthorID || member(ctx, q, p, a.Revision.Proposal.ThreadID) != nil || member(ctx, q, p, a.Revision.Proposal.QA.EvidenceDestination.ThreadID) != nil {
			return c.ErrForbidden
		}
		gateBotID = p.GitHubBotID
	}
	if in.Kind == "retained-gates" {
		return nil
	}
	// Select first, then validate: revoked or drifted latest gates never fall back.
	var raw []byte
	if err = q.QueryRowContext(ctx, `SELECT body FROM handoffs WHERE goal_id=? AND revision=? AND json_extract(body,'$.input.kind')='retained-gates' AND json_extract(body,'$.input.task_url')=? AND json_extract(body,'$.input.pr_url')=? ORDER BY json_extract(body,'$.sequence') DESC LIMIT 1`, in.Goal.GoalID, in.Goal.RevisionSHA256, in.TaskURL, in.PRURL).Scan(&raw); err != nil {
		return c.ErrConflict
	}
	var gates c.Handoff
	if json.Unmarshal(raw, &gates) != nil || gates.Sequence < 1 || gates.Input.Revoked || gates.ActorKind != "owner" {
		return c.ErrConflict
	}
	expected := in
	expected.Kind, expected.RequestID, expected.EvidenceMessageID = "retained-gates", gates.Input.RequestID, gates.Input.EvidenceMessageID
	expected.Retained.EngineeringReportID, expected.Retained.EngineeringReportBodySHA, expected.Retained.QAReportID, expected.Retained.QAReportBodySHA = "", "", "", ""
	if gates.Input != expected {
		return c.ErrConflict
	}
	gateOwner, err := authorize(ctx, q, c.Principal{ID: gates.ActorID}, "decide")
	if err != nil || gateOwner.Kind != "owner" || member(ctx, q, gateOwner, a.Revision.Proposal.ThreadID) != nil || member(ctx, q, gateOwner, a.Revision.Proposal.QA.EvidenceDestination.ThreadID) != nil {
		return c.ErrForbidden
	}
	if err = svc.retainedAuthority(ctx, q, actor, a, gates.Input); err != nil {
		return err
	}
	for _, check := range []struct{ id, sha, author string }{{r.EngineeringReportID, r.EngineeringReportBodySHA, "hawk"}, {r.QAReportID, r.QAReportBodySHA, "wren"}} {
		m, e := loadMessage(ctx, q, check.id)
		if e != nil {
			return e
		}
		var report c.RetainedGateReport
		if m.Imported || m.AuthorKind != "agent" || m.AuthorID != check.author || m.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID || retainedBodySHA(m.Body) != check.sha || c.DecodeJSON([]byte(m.Body), &report) != nil || report.Result != "ready" || report.GateHandoffID != gates.ID || report.AssignmentSHA != in.AssignmentSHA || report.Binding != gates.Input || member(ctx, q, actor, m.ThreadID) != nil {
			return c.ErrForbidden
		}
	}
	return nil
}
