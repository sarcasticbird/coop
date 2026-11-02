package master

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

const sourceJourneySchema = `
CREATE TABLE IF NOT EXISTS source_decisions (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 goal_id TEXT NOT NULL,
 revision TEXT NOT NULL,
 pr_url TEXT NOT NULL,
 kind TEXT NOT NULL,
 body BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS source_decisions_goal ON source_decisions(goal_id,revision,sequence);
CREATE INDEX IF NOT EXISTS source_decisions_pr ON source_decisions(pr_url,sequence);
CREATE TABLE IF NOT EXISTS source_external_registrations (
 repository_id INTEGER NOT NULL,
 pr_id TEXT NOT NULL,
 pr_url TEXT NOT NULL UNIQUE,
 decision_id TEXT NOT NULL UNIQUE,
 PRIMARY KEY(repository_id,pr_id)
);
CREATE TABLE IF NOT EXISTS source_integration_receipts (
 id TEXT PRIMARY KEY,
 decision_id TEXT NOT NULL UNIQUE,
 goal_id TEXT NOT NULL,
 revision TEXT NOT NULL,
 body BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS source_effect_receipts (
 decision_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 body BLOB NOT NULL,
 PRIMARY KEY(decision_id,kind)
);`

// sourceAccess intentionally does not resolve execution eligibility: owners must
// be able to withdraw recorded authority even after a pause or roster removal.
func (svc *Service) sourceAccess(ctx context.Context, q queryer, actor c.Principal, goal c.GoalRef, owner bool) (c.Principal, c.GoalRevision, error) {
	p, err := authorize(ctx, q, actor, "read")
	if err != nil {
		return p, c.GoalRevision{}, err
	}
	if owner {
		p, err = authorize(ctx, q, p, "decide")
		if err != nil || p.Kind != "owner" {
			return p, c.GoalRevision{}, c.ErrForbidden
		}
	}
	if goal.Source != "master" || goal.MasterID != svc.Store.id {
		return p, c.GoalRevision{}, c.ErrConflict
	}
	var raw []byte
	var revision c.GoalRevision
	if err = q.QueryRowContext(ctx, "SELECT body FROM goal_revisions WHERE goal_id=? AND sha=?", goal.GoalID, goal.RevisionSHA256).Scan(&raw); err != nil {
		return p, revision, c.ErrNotFound
	}
	if json.Unmarshal(raw, &revision) != nil || revision.Ref != goal {
		return p, revision, c.ErrConflict
	}
	for _, thread := range []string{revision.Proposal.ThreadID, revision.Proposal.QA.EvidenceDestination.ThreadID} {
		if thread == "" || member(ctx, q, p, thread) != nil {
			return p, revision, c.ErrForbidden
		}
	}
	return p, revision, nil
}

func (svc *Service) CreateSourceDecision(ctx context.Context, actor c.Principal, in c.SourceDecisionInput) (out c.SourceDecision, err error) {
	if err = c.ValidateSourceDecision(in); err != nil {
		return
	}
	if len(encode(in)) > c.MaxPageBytes/2 {
		return out, c.ErrSize
	}
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, _, err := svc.sourceAccess(ctx, tx, actor, in.Goal, true)
	if err != nil {
		return out, err
	}
	// A lost response is not permission to repeat an effect or re-admit a grant.
	if found, e := request(ctx, tx, p.ID, in.RequestID, "source-decision", in, &out); found || e != nil {
		return out, e
	}
	pr := in.Source.PRURL
	if in.Kind == "revoke" {
		target, e := sourceDecision(ctx, tx, in.Goal, in.RevokeID)
		if e != nil || target.Input.Kind == "revoke" {
			return out, c.ErrConflict
		}
		pr = target.Input.Source.PRURL
	} else if err = svc.sourceAuthority(ctx, tx, p, in, 0); err != nil {
		return out, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM source_decisions").Scan(&count); err != nil {
		return
	}
	if count >= 10000 {
		return out, c.ErrCapacity
	}
	out = c.SourceDecision{ID: newID("sd"), OwnerID: p.ID, CreatedAt: svc.Store.now().UTC(), Input: in}
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(sequence),0)+1 FROM source_decisions").Scan(&out.Sequence); err != nil {
		return
	}
	if in.Kind == "intake" {
		// This is an external PR identity, never a fabricated native artifact.
		if _, err = tx.ExecContext(ctx, "INSERT INTO source_external_registrations(repository_id,pr_id,pr_url,decision_id) VALUES(?,?,?,?)", in.Source.RepositoryID, in.Source.PRID, pr, out.ID); err != nil {
			return c.SourceDecision{}, c.ErrConflict
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO source_decisions(sequence,id,goal_id,revision,pr_url,kind,body) VALUES(?,?,?,?,?,?,?)", out.Sequence, out.ID, in.Goal.GoalID, in.Goal.RevisionSHA256, pr, in.Kind, encode(out)); err != nil {
		return
	}
	if err = saveRequest(ctx, tx, p.ID, in.RequestID, "source-decision", in, out); err != nil {
		return
	}
	err = tx.Commit()
	return
}

func sourceDecision(ctx context.Context, q queryer, goal c.GoalRef, id string) (out c.SourceDecision, err error) {
	var raw []byte
	if err = q.QueryRowContext(ctx, "SELECT body FROM source_decisions WHERE id=? AND goal_id=? AND revision=?", id, goal.GoalID, goal.RevisionSHA256).Scan(&raw); err != nil {
		return out, c.ErrConflict
	}
	if json.Unmarshal(raw, &out) != nil || out.ID != id || out.Input.Goal != goal || out.Sequence < 1 || c.ValidateSourceDecision(out.Input) != nil {
		return out, c.ErrConflict
	}
	return out, nil
}

func (svc *Service) SourceDecisions(ctx context.Context, actor c.Principal, in c.SourceDecisionQuery) (out c.SourceDecisionPage, err error) {
	out.Decisions = []c.SourceDecision{}
	if in.After < 0 {
		return out, c.ErrInvalid
	}
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, _, err = svc.sourceAccess(ctx, tx, actor, in.Goal, false); err != nil {
		return
	}
	rows, err := tx.QueryContext(ctx, "SELECT body FROM source_decisions WHERE goal_id=? AND revision=? AND sequence>? ORDER BY sequence LIMIT ?", in.Goal.GoalID, in.Goal.RevisionSHA256, in.After, c.MaxPageRecords+1)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	size := 128
	for rows.Next() {
		var raw []byte
		var d c.SourceDecision
		if err = rows.Scan(&raw); err != nil {
			return
		}
		if err = json.Unmarshal(raw, &d); err != nil {
			return
		}
		if len(out.Decisions) == c.MaxPageRecords || size+len(raw)+1 > c.MaxPageBytes-128 {
			if len(out.Decisions) == 0 {
				return out, c.ErrSize
			}
			out.Next = out.Decisions[len(out.Decisions)-1].Sequence
			break
		}
		size += len(raw) + 1
		out.Decisions = append(out.Decisions, d)
	}
	err = rows.Err()
	return
}

// ValidateSourceDecision is read-only. The caller must still compare the returned
// immutable source and target with live GitHub immediately before its effect.
func (svc *Service) ValidateSourceDecision(ctx context.Context, actor c.Principal, in c.SourceDecisionValidation) (out c.SourceDecision, err error) {
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, _, err := svc.sourceAccess(ctx, tx, actor, in.Goal, false)
	if err != nil {
		return out, err
	}
	out, err = sourceDecision(ctx, tx, in.Goal, in.ID)
	if err != nil {
		return
	}
	if out.Input.Kind == "retarget" || out.Input.Kind == "deliver" {
		if p.ID != c.DeliveryPrincipalID || p.Kind != "agent" || p.GitHubBotID != c.DeliveryGitHubBotID {
			return out, c.ErrForbidden
		}
	} else if p.Kind != "agent" || (p.ID != out.Input.ReviewerID && p.ID != out.Input.QAID) {
		return out, c.ErrForbidden
	}
	err = svc.validateSourceRecorded(ctx, tx, out, true, 0)
	return
}

func (svc *Service) validateSourceRecorded(ctx context.Context, q queryer, d c.SourceDecision, latest bool, depth int) error {
	if depth > 16 || d.Input.Kind == "revoke" {
		return c.ErrConflict
	}
	owner, _, err := svc.sourceAccess(ctx, q, c.Principal{ID: d.OwnerID}, d.Input.Goal, true)
	if err != nil {
		return err
	}
	var revoked int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM source_decisions WHERE kind='revoke' AND json_extract(body,'$.input.revoke_id')=?", d.ID).Scan(&revoked); err != nil {
		return err
	}
	if revoked != 0 {
		return c.ErrForbidden
	}
	if latest {
		stream := "kind IN ('intake','transition')"
		if d.Input.Kind == "retarget" || d.Input.Kind == "deliver" {
			stream = "kind='" + d.Input.Kind + "'"
		}
		var id string
		if err = q.QueryRowContext(ctx, "SELECT id FROM source_decisions WHERE pr_url=? AND "+stream+" ORDER BY sequence DESC LIMIT 1", d.Input.Source.PRURL).Scan(&id); err != nil || id != d.ID {
			return c.ErrConflict
		}
	}
	return svc.sourceAuthority(ctx, q, owner, d.Input, depth+1)
}

func (svc *Service) sourceAuthority(ctx context.Context, q queryer, actor c.Principal, in c.SourceDecisionInput, depth int) error {
	if depth > 16 || c.ValidateSourceDecision(in) != nil {
		return c.ErrInvalid
	}
	if err := admission(ctx, q); err != nil {
		return err
	}
	a, err := svc.Store.resolveGoal(ctx, q, actor, in.Goal)
	if err != nil {
		return err
	}
	if !a.Valid || a.State != "approved" || a.ActiveRevisionSHA256 != in.Goal.RevisionSHA256 || a.ApprovalSequence != in.ApprovalSequence || digest(a.Revision.Proposal.Acceptance) != in.AcceptanceSHA || !a.Revision.Proposal.QA.Required || a.Revision.Proposal.QA.Environment != in.Environment || a.Revision.Proposal.QA.EnvironmentRevision != in.EnvironmentRevision || svc.Tasks == nil || svc.Tasks.Capabilities().PolicySHA != in.PolicySHA {
		return c.ErrConflict
	}
	allowed := false
	for _, r := range a.Revision.Proposal.Repositories {
		if r.Name == in.Source.Repository {
			allowed = true
		}
	}
	if !allowed {
		return c.ErrForbidden
	}
	e, err := loadMessage(ctx, q, in.EvidenceMessageID)
	if err != nil || e.Imported || e.AuthorKind != "owner" || e.AuthorID != actor.ID || e.ThreadID != a.Revision.Proposal.ThreadID {
		return c.ErrForbidden
	}
	task, err := svc.Tasks.Read(ctx, in.GateTaskURL)
	if err != nil {
		return err
	}
	if task.Binding == nil || task.Repository != in.Source.Repository || digest(task.Binding) != in.GateAssignmentSHA {
		return c.ErrConflict
	}
	if in.Source.AuthorKind == "User" && task.Binding.Purpose != "source-gates" {
		return c.ErrForbidden
	}
	// The task is a supplemental gate container, never a new implementation of
	// the imported PR. Native transitions may retain the original source task.
	if task.Goal != in.Goal && (in.Source.OriginalGoal == nil || task.Goal != *in.Source.OriginalGoal || task.Reference != in.Source.OriginalTaskURL || in.GateAssignmentSHA != in.Source.OriginalAssignmentSHA) {
		return c.ErrConflict
	}
	if task.Binding.TaskScopeSHA != digest(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{task.Title, task.Body}) {
		return c.ErrConflict
	}
	var registered string
	if err = q.QueryRowContext(ctx, "SELECT assignment_sha FROM artifacts WHERE url=? AND kind='task' AND goal_id=? AND revision=?", task.Reference, task.Goal.GoalID, task.Goal.RevisionSHA256).Scan(&registered); err != nil || registered != in.GateAssignmentSHA {
		return c.ErrConflict
	}
	var ids []int64
	for _, id := range []string{in.ReviewerID, in.QAID, c.DeliveryPrincipalID} {
		p, e := authorize(ctx, q, c.Principal{ID: id}, "read")
		if e != nil || p.Kind != "agent" || p.GitHubBotID <= 0 || slices.Contains(ids, p.GitHubBotID) || slices.Contains(in.Source.Contributors, p.GitHubBotID) {
			return c.ErrForbidden
		}
		if member(ctx, q, p, a.Revision.Proposal.ThreadID) != nil || member(ctx, q, p, a.Revision.Proposal.QA.EvidenceDestination.ThreadID) != nil {
			return c.ErrForbidden
		}
		if id == c.DeliveryPrincipalID && p.GitHubBotID != c.DeliveryGitHubBotID {
			return c.ErrForbidden
		}
		ids = append(ids, p.GitHubBotID)
	}
	if in.ReviewerID != a.Revision.Proposal.PeerID || in.QAID != a.Revision.Proposal.QA.ActorID {
		return c.ErrForbidden
	}
	if in.Source.AuthorKind == "Bot" {
		var raw []byte
		g := in.Source.OriginalGoal
		if err = q.QueryRowContext(ctx, "SELECT body FROM artifacts WHERE url=? AND kind='pull_request' AND goal_id=? AND revision=?", in.Source.PRURL, g.GoalID, g.RevisionSHA256).Scan(&raw); err != nil {
			return c.ErrConflict
		}
		var link c.ArtifactLink
		if json.Unmarshal(raw, &link) != nil || link.TaskURL != in.Source.OriginalTaskURL || link.AssignmentSHA != in.Source.OriginalAssignmentSHA || digest(link) != in.Source.OriginalRegistration {
			return c.ErrConflict
		}
		original, e := svc.Tasks.Read(ctx, in.Source.OriginalTaskURL)
		if e != nil || original.Binding == nil || digest(original.Binding) != in.Source.OriginalAssignmentSHA || original.Goal != *g {
			return c.ErrConflict
		}
		if original.Binding.PolicySHA != in.Source.OriginalPolicySHA || original.Binding.AuthorID != in.Source.AuthorID || original.Binding.BaseBranch != in.Source.BaseBranch {
			return c.ErrConflict
		}
	}
	if in.Kind != "intake" && in.Source.AuthorKind == "User" {
		var raw []byte
		if err = q.QueryRowContext(ctx, `SELECT d.body FROM source_external_registrations r JOIN source_decisions d ON d.id=r.decision_id WHERE r.repository_id=? AND r.pr_id=? AND r.pr_url=?`, in.Source.RepositoryID, in.Source.PRID, in.Source.PRURL).Scan(&raw); err != nil {
			return c.ErrConflict
		}
		var intake c.SourceDecision
		if json.Unmarshal(raw, &intake) != nil || intake.OwnerID != actor.ID || c.SourceDigest(intake.Input.Source) != c.SourceDigest(in.Source) {
			return c.ErrConflict
		}
		if err = svc.validateSourceRecorded(ctx, q, intake, false, depth+1); err != nil {
			return err
		}
	}
	if in.Target.Predecessor != nil {
		r := in.Target.Predecessor
		var raw []byte
		if err = q.QueryRowContext(ctx, "SELECT body FROM source_integration_receipts WHERE id=?", r.ID).Scan(&raw); err != nil || string(raw) != string(encode(r)) {
			return c.ErrConflict
		}
		var priorRaw []byte
		if err = q.QueryRowContext(ctx, "SELECT body FROM source_decisions WHERE id=?", r.DecisionID).Scan(&priorRaw); err != nil {
			return c.ErrConflict
		}
		var prior c.SourceDecision
		if json.Unmarshal(priorRaw, &prior) != nil || prior.Input.Kind != "deliver" || prior.Input.Source.PRURL != r.PRURL || c.SourceDigest(prior.Input.Source) != r.SourceSHA || prior.Input.Source.Branch != in.Source.BaseBranch || prior.Input.Source.HeadSHA != in.Source.BaseSHA {
			return c.ErrConflict
		}
		if prior.Input.Goal != r.Goal {
			return c.ErrConflict
		}
		for _, id := range prior.Input.Source.Contributors {
			if !slices.Contains(in.Source.Contributors, id) {
				return c.ErrConflict
			}
		}
	}
	if in.ParentID != "" {
		parent, e := sourceDecision(ctx, q, in.Goal, in.ParentID)
		if e != nil || parent.OwnerID != actor.ID || (parent.Input.Kind != "intake" && parent.Input.Kind != "transition") || c.SourceDigest(parent.Input.Source) != c.SourceDigest(in.Source) {
			return c.ErrConflict
		}
		if err = svc.validateSourceRecorded(ctx, q, parent, in.Kind != "transition", depth+1); err != nil {
			return err
		}
		if in.Kind == "retarget" || in.Kind == "deliver" {
			expected := in
			expected.Kind, expected.ParentID, expected.ActorID = parent.Input.Kind, parent.Input.ParentID, ""
			expected.RequestID, expected.EvidenceMessageID = parent.Input.RequestID, parent.Input.EvidenceMessageID
			expected.EngineeringReportID, expected.EngineeringBodySHA, expected.QAReportID, expected.QABodySHA = "", "", "", ""
			if c.SourceDigest(expected) != c.SourceDigest(parent.Input) {
				return c.ErrConflict
			}
			for _, gate := range []struct{ id, sha, author, stage string }{{in.EngineeringReportID, in.EngineeringBodySHA, in.ReviewerID, "source-review"}, {in.QAReportID, in.QABodySHA, in.QAID, "source-qa"}} {
				m, e := loadMessage(ctx, q, gate.id)
				var report c.SourceGateReport
				if e != nil || m.Imported || m.AuthorKind != "agent" || m.AuthorID != gate.author || m.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID || retainedBodySHA(m.Body) != gate.sha || c.DecodeJSON([]byte(m.Body), &report) != nil || report.DecisionID != parent.ID || report.ScopeSHA != c.SourceDigest(parent.Input) || report.Stage != gate.stage || report.Verdict != "approved" || report.ReviewID == "" {
					return c.ErrForbidden
				}
			}
		}
	}
	return nil
}

func (svc *Service) RecordSourceEffect(ctx context.Context, actor c.Principal, in c.SourceEffectReceipt) (out c.SourceEffectReceipt, err error) {
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, _, err := svc.sourceAccess(ctx, tx, actor, in.Goal, false)
	if err != nil {
		return out, err
	}
	d, err := sourceDecision(ctx, tx, in.Goal, in.DecisionID)
	if err != nil {
		return out, err
	}
	if p.Kind != "agent" || in.RequestID == "" || (in.Kind != "retarget" && in.Kind != "deliver" && in.Kind != "source-review" && in.Kind != "source-qa") || in.Kind != d.Input.Kind && in.Kind != "source-review" && in.Kind != "source-qa" {
		return out, c.ErrForbidden
	}
	if in.Kind == "retarget" || in.Kind == "deliver" {
		if p.ID != c.DeliveryPrincipalID || p.GitHubBotID != c.DeliveryGitHubBotID {
			return out, c.ErrForbidden
		}
	} else if in.Kind == "source-review" && p.ID != d.Input.ReviewerID || in.Kind == "source-qa" && p.ID != d.Input.QAID {
		return out, c.ErrForbidden
	}
	// Exact durable retries return facts, not renewed authority or a second effect.
	if found, e := request(ctx, tx, p.ID, in.RequestID, "source-effect", in, &out); found || e != nil {
		return out, e
	}
	if err = svc.validateSourceRecorded(ctx, tx, d, true, 0); err != nil {
		return out, err
	}
	if in.Kind == "source-review" || in.Kind == "source-qa" {
		if d.Input.Kind != "intake" && d.Input.Kind != "transition" {
			return out, c.ErrForbidden
		}
		msg, e := loadMessage(ctx, tx, in.ReportID)
		var report c.SourceGateReport
		if e != nil || msg.Imported || msg.AuthorKind != "agent" || msg.AuthorID != p.ID || retainedBodySHA(msg.Body) != in.BodySHA || c.DecodeJSON([]byte(msg.Body), &report) != nil || report.DecisionID != d.ID || report.ScopeSHA != c.SourceDigest(d.Input) || report.Stage != in.Kind || report.ReviewID == "" {
			return out, c.ErrForbidden
		}
	}
	if in.Kind == "deliver" && (!handoffCommit(in.CommitSHA) || !handoffCommit(in.TreeSHA)) {
		return out, c.ErrInvalid
	}
	if in.Kind == "deliver" && in.TreeSHA != d.Input.Target.ResultTreeSHA {
		return out, c.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO source_effect_receipts(decision_id,kind,body) VALUES(?,?,?)", d.ID, in.Kind, encode(in)); err != nil {
		return out, c.ErrConflict
	}
	if in.Kind == "deliver" {
		r := c.SourceIntegrationReceipt{Goal: d.Input.Goal, ID: "si_" + d.ID, DecisionID: d.ID, PRURL: d.Input.Source.PRURL, SourceSHA: c.SourceDigest(d.Input.Source), TargetRepoID: d.Input.Target.RepositoryID, TargetBranch: d.Input.Target.Branch, BeforeSHA: d.Input.Target.SHA, CommitSHA: in.CommitSHA, TreeSHA: in.TreeSHA, RequestID: in.RequestID}
		if _, err = tx.ExecContext(ctx, "INSERT INTO source_integration_receipts(id,decision_id,goal_id,revision,body) VALUES(?,?,?,?,?)", r.ID, d.ID, in.Goal.GoalID, in.Goal.RevisionSHA256, encode(r)); err != nil {
			return out, err
		}
	}
	out = in
	if err = saveRequest(ctx, tx, p.ID, in.RequestID, "source-effect", in, out); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func handoffCommit(s string) bool { return len(s) == 40 && stringsLowerHex(s) }
func stringsLowerHex(s string) bool {
	for _, v := range s {
		if (v < '0' || v > '9') && (v < 'a' || v > 'f') {
			return false
		}
	}
	return true
}

func (svc *Service) SourceEffects(ctx context.Context, actor c.Principal, in c.SourceDecisionValidation) (out []c.SourceEffectReceipt, err error) {
	out = []c.SourceEffectReceipt{}
	if _, _, err = svc.sourceAccess(ctx, svc.Store.db, actor, in.Goal, false); err != nil {
		return
	}
	if _, err = sourceDecision(ctx, svc.Store.db, in.Goal, in.ID); err != nil {
		return
	}
	rows, err := svc.Store.db.QueryContext(ctx, "SELECT body FROM source_effect_receipts WHERE decision_id=? ORDER BY kind", in.ID)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		var e c.SourceEffectReceipt
		if err = rows.Scan(&raw); err != nil {
			return
		}
		if err = json.Unmarshal(raw, &e); err != nil {
			return
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (svc *Service) CompleteSourceTask(ctx context.Context, actor c.Principal, in c.SourceCompletionInput) (out c.ProviderTask, err error) {
	if in.RequestID == "" {
		return out, c.ErrInvalid
	}
	d, err := svc.ValidateSourceDecision(ctx, actor, c.SourceDecisionValidation{Goal: in.Goal, ID: in.DecisionID})
	if err != nil {
		return out, err
	}
	if d.Input.Kind != "deliver" {
		return out, c.ErrForbidden
	}
	effects, err := svc.SourceEffects(ctx, actor, c.SourceDecisionValidation{Goal: in.Goal, ID: in.DecisionID})
	if err != nil || len(effects) != 1 || effects[0].Kind != "deliver" {
		return out, c.ErrConflict
	}
	reference, assignment := d.Input.GateTaskURL, d.Input.GateAssignmentSHA
	if d.Input.Source.OriginalTaskURL != "" {
		reference, assignment = d.Input.Source.OriginalTaskURL, d.Input.Source.OriginalAssignmentSHA
	}
	task, err := svc.Tasks.Read(ctx, reference)
	if err != nil {
		return out, err
	}
	// Provider completion has its own stable key and read-after-write recovery.
	// Native history and the published assignment are never rebound to main.
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	var accepted bool
	if found, e := request(ctx, tx, actor.ID, in.RequestID, "source-complete", in, &accepted); e != nil {
		return out, e
	} else if !found {
		if err = saveRequest(ctx, tx, actor.ID, in.RequestID, "source-complete", in, true); err != nil {
			return out, err
		}
	}
	if err = svc.validateSourceRecorded(ctx, tx, d, true, 0); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	if task.State == "closed" {
		return task, nil
	}
	return svc.Tasks.Complete(ctx, c.TaskCompleteInput{Goal: task.Goal, Reference: task.Reference, AssignmentSHA: assignment, PullRequestURL: d.Input.Source.PRURL, DeliveryHandoffID: d.ID, RequestID: in.RequestID}, actor)
}

func (svc *Service) SourceTaskState(ctx context.Context, actor c.Principal, in c.SourceDecisionValidation) (out c.SourceTaskState, err error) {
	d, err := svc.ValidateSourceDecision(ctx, actor, in)
	if err != nil {
		return out, err
	}
	reference, assignment := d.Input.GateTaskURL, d.Input.GateAssignmentSHA
	if d.Input.Source.OriginalTaskURL != "" {
		reference, assignment = d.Input.Source.OriginalTaskURL, d.Input.Source.OriginalAssignmentSHA
	}
	task, err := svc.Tasks.Read(ctx, reference)
	if err != nil {
		return out, err
	}
	if task.Binding == nil || digest(task.Binding) != assignment {
		return out, c.ErrConflict
	}
	return c.SourceTaskState{Reference: reference, AssignmentSHA: assignment, State: task.State}, nil
}

func registerSourceJourney(mux *http.ServeMux, svc *Service, wrap func(func(http.ResponseWriter, *http.Request, c.Principal) (any, error)) http.HandlerFunc) {
	mux.HandleFunc("POST /api/v1/source-decisions", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.SourceDecisionInput
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.CreateSourceDecision(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/source-decisions/list", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.SourceDecisionQuery
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.SourceDecisions(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/source-decisions/validate", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.SourceDecisionValidation
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.ValidateSourceDecision(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/source-decisions/effect", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.SourceEffectReceipt
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.RecordSourceEffect(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/source-decisions/effects", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.SourceDecisionValidation
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.SourceEffects(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/source-decisions/complete", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.SourceCompletionInput
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.CompleteSourceTask(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/source-decisions/task-state", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.SourceDecisionValidation
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.SourceTaskState(r.Context(), p, in)
	}))
}
