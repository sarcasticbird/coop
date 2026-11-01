package master

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (svc *Service) CreateHandoff(ctx context.Context, actor c.Principal, in c.HandoffInput) (c.Handoff, error) {
	var out c.Handoff
	if err := c.ValidateHandoff(in); err != nil {
		return out, err
	}
	retained := in.Kind == "retained-gates" || in.Kind == "retained-deliver"
	if (in.Kind == "deliver" || in.Kind == "tag-only" || retained) && in.Revoked {
		return svc.revokeDelivery(ctx, actor, in)
	}
	p, err := authorize(ctx, svc.Store.db, actor, "read")
	if err != nil {
		return out, err
	}
	a, err := svc.Store.resolveGoal(ctx, svc.Store.db, p, in.Goal)
	if err != nil {
		return out, err
	}
	if err = admission(ctx, svc.Store.db); err != nil {
		return out, err
	}
	if !a.Valid && !retained && in.Kind != "tag-only" {
		return out, c.ErrForbidden
	}
	if !planner(p) || (in.Kind == "release" || in.Kind == "deliver" || in.Kind == "tag-only" || retained) && p.Kind != "owner" {
		return out, c.ErrForbidden
	}
	environmentDecision := in.Kind == "reassess-qa" && in.Environment != ""
	if environmentDecision && p.Kind != "owner" {
		return out, c.ErrForbidden
	}
	if (in.Kind == "reassess-qa" || in.Kind == "first-postmerge-qa") && !a.Revision.Proposal.QA.Required {
		return out, c.ErrForbidden
	}
	if in.Kind == "first-postmerge-qa" && (in.Environment != a.Revision.Proposal.QA.Environment || in.EnvironmentRevision != a.Revision.Proposal.QA.EnvironmentRevision) {
		return out, c.ErrForbidden
	}
	taskRepo, err := svc.artifactRepository(in.TaskURL, "issues")
	if err != nil {
		return out, err
	}
	allowed := false
	for _, r := range a.Revision.Proposal.Repositories {
		if r.Name == taskRepo {
			allowed = true
		}
	}
	if !allowed {
		return out, c.ErrForbidden
	}
	repo, err := artifactRepo(in.PRURL, "pull")
	if err != nil {
		return out, err
	}
	if repo != taskRepo {
		return out, c.ErrForbidden
	}
	if strings.HasPrefix(in.TaskURL, "kata:") {
		if svc.Tasks == nil {
			return out, c.ErrUnsupported
		}
		task, err := svc.Tasks.Read(ctx, in.TaskURL)
		if err != nil {
			return out, err
		}
		if task.Goal != in.Goal || task.Repository != taskRepo || task.Binding == nil || digest(task.Binding) != in.AssignmentSHA || task.Binding.ApprovalSequence != a.ApprovalSequence {
			return out, c.ErrConflict
		}
		if (in.Kind == "deliver" || retained) && (task.Binding.TaskScopeSHA != in.TaskScopeSHA || task.Binding.BaseBranch != in.BaseBranch || task.Binding.TaskScopeSHA != digest(struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}{task.Title, task.Body})) {
			return out, c.ErrConflict
		}
	}
	// GitHub assignments are authorized by the exact registered task/PR pair
	// below. Workers validate their current binding, head and evidence before
	// execution; the master has no GitHub runner.
	evidence, err := svc.Store.Message(ctx, p, in.EvidenceMessageID)
	if err != nil {
		return out, err
	}
	if evidence.ThreadID != a.Revision.Proposal.ThreadID && evidence.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID || evidence.AuthorKind != "owner" && evidence.AuthorID != a.Actors.QA.PrincipalID {
		return out, c.ErrForbidden
	}
	if environmentDecision && evidence.AuthorKind != "owner" {
		return out, c.ErrForbidden
	}
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	if in.Kind == "release" || in.Kind == "deliver" || in.Kind == "tag-only" || retained || environmentDecision {
		current, e := authorize(ctx, tx, p, "decide")
		if e != nil || current.Kind != "owner" {
			return out, c.ErrForbidden
		}
		p = current
	}
	if err = admission(ctx, tx); err != nil {
		return out, err
	}
	fresh, err := svc.Store.resolveGoal(ctx, tx, p, in.Goal)
	if err != nil {
		return out, err
	}
	if (!fresh.Valid && !retained && in.Kind != "tag-only") || fresh.ApprovalSequence != a.ApprovalSequence {
		return out, c.ErrConflict
	}
	if in.Kind == "deliver" {
		if err = svc.deliveryAuthority(ctx, tx, p, fresh, in); err != nil {
			return out, err
		}
	}
	if retained {
		if err = svc.retainedAuthority(ctx, tx, p, fresh, in); err != nil {
			return out, err
		}
	}
	if in.Kind == "tag-only" {
		if err = svc.tagOnlyAuthority(ctx, tx, p, fresh, in); err != nil {
			return out, err
		}
	}
	var assignment string
	if err = tx.QueryRowContext(ctx, "SELECT assignment_sha FROM artifacts WHERE goal_id=? AND revision=? AND url=? AND kind='task'", in.Goal.GoalID, in.Goal.RevisionSHA256, in.TaskURL).Scan(&assignment); err != nil || assignment != in.AssignmentSHA {
		return out, c.ErrConflict
	}
	if err = tx.QueryRowContext(ctx, "SELECT assignment_sha FROM artifacts WHERE goal_id=? AND revision=? AND url=? AND kind='pull_request' AND json_extract(body,'$.task_url')=?", in.Goal.GoalID, in.Goal.RevisionSHA256, in.PRURL, in.TaskURL).Scan(&assignment); err != nil || assignment != in.AssignmentSHA {
		return out, c.ErrConflict
	}
	if found, e := request(ctx, tx, p.ID, in.RequestID, "handoff", in, &out); found || e != nil {
		return out, e
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM handoffs").Scan(&count); err != nil {
		return out, err
	}
	if count >= 10000 {
		return out, c.ErrCapacity
	}
	releaseKey := ""
	if in.Kind == "release" || in.Kind == "tag-only" {
		releaseKey = repo + ":" + in.Tag
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM handoffs WHERE release_key=?", releaseKey).Scan(&count); err != nil {
			return out, err
		}
		if count != 0 {
			return out, c.ErrConflict
		}
	}
	out = c.Handoff{ID: newID("ho"), Input: in, ActorID: p.ID, ActorKind: p.Kind, CreatedAt: svc.Store.now().UTC()}
	if in.Kind == "deliver" || in.Kind == "tag-only" || retained {
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(rowid),0)+1 FROM handoffs").Scan(&out.Sequence); err != nil {
			return c.Handoff{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO handoffs(id,goal_id,revision,release_key,body) VALUES(?,?,?,?,?)", out.ID, in.Goal.GoalID, in.Goal.RevisionSHA256, releaseKey, encode(out)); err != nil {
		return out, err
	}
	if err = saveRequest(ctx, tx, p.ID, in.RequestID, "handoff", in, out); err != nil {
		return out, err
	}
	if err = event(ctx, tx, "handoff", a.Revision.Proposal.ThreadID, ""); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// Withdrawal binds recorded authority, not current execution eligibility. A
// paused or superseded goal, stale assignment, or disabled executor cannot veto it.
func (svc *Service) revokeDelivery(ctx context.Context, actor c.Principal, in c.HandoffInput) (c.Handoff, error) {
	var out c.Handoff
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := authorize(ctx, tx, actor, "read")
	if err != nil {
		return out, err
	}
	p, err = authorize(ctx, tx, p, "decide")
	if err != nil {
		return out, err
	}
	if p.Kind != "owner" {
		return out, c.ErrForbidden
	}
	if in.Goal.Source != "master" || in.Goal.MasterID != svc.Store.id {
		return out, c.ErrConflict
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM goal_revisions WHERE goal_id=? AND sha=?", in.Goal.GoalID, in.Goal.RevisionSHA256).Scan(&raw); err != nil {
		return out, c.ErrConflict
	}
	var revision c.GoalRevision
	if json.Unmarshal(raw, &revision) != nil || revision.Ref != in.Goal {
		return out, c.ErrConflict
	}
	for _, thread := range []string{revision.Proposal.ThreadID, revision.Proposal.QA.EvidenceDestination.ThreadID} {
		if thread != "" {
			if err = member(ctx, tx, p, thread); err != nil {
				return out, err
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT body FROM handoffs WHERE goal_id=? AND revision=?
		AND json_extract(body,'$.input.kind')=?`, in.Goal.GoalID, in.Goal.RevisionSHA256, in.Kind)
	if err != nil {
		return out, err
	}
	matched := false
	for rows.Next() {
		var grant c.Handoff
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &grant); err != nil {
			break
		}
		if grant.ID == "" || grant.Sequence < 1 || grant.ActorKind != "owner" || grant.ActorID == "" || grant.Input.Revoked {
			continue
		}
		original := grant.Input
		original.RequestID, original.Revoked = in.RequestID, true
		if original == in {
			matched = true
			break
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	if !matched {
		return out, c.ErrConflict
	}
	if found, e := request(ctx, tx, p.ID, in.RequestID, "handoff", in, &out); found || e != nil {
		return out, e
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM handoffs").Scan(&count); err != nil {
		return out, err
	}
	if count >= 10000 {
		return out, c.ErrCapacity
	}
	out = c.Handoff{ID: newID("ho"), Input: in, ActorID: p.ID, ActorKind: p.Kind, CreatedAt: svc.Store.now().UTC()}
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(rowid),0)+1 FROM handoffs").Scan(&out.Sequence); err != nil {
		return c.Handoff{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO handoffs(id,goal_id,revision,release_key,body) VALUES(?,?,?,?,?)", out.ID, in.Goal.GoalID, in.Goal.RevisionSHA256, "", encode(out)); err != nil {
		return out, err
	}
	if err = saveRequest(ctx, tx, p.ID, in.RequestID, "handoff", in, out); err != nil {
		return out, err
	}
	if err = event(ctx, tx, "handoff", revision.Proposal.ThreadID, ""); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func deliveryPrincipal(ctx context.Context, q queryer, a c.GoalAuthority) (c.Principal, error) {
	p, err := authorize(ctx, q, c.Principal{ID: c.DeliveryPrincipalID}, "read")
	if err != nil || p.ID != c.DeliveryPrincipalID || p.Kind != "agent" || p.GitHubBotID != c.DeliveryGitHubBotID {
		return p, c.ErrForbidden
	}
	for _, thread := range []string{a.Revision.Proposal.ThreadID, a.Revision.Proposal.QA.EvidenceDestination.ThreadID} {
		if thread == "" {
			continue
		}
		if err = member(ctx, q, p, thread); err != nil {
			return p, err
		}
	}
	return p, nil
}

// Delivery adds authority alongside the immutable assignment, never to it.
func (svc *Service) deliveryAuthority(ctx context.Context, q queryer, actor c.Principal, a c.GoalAuthority, in c.HandoffInput) error {
	if err := c.ValidateHandoff(in); err != nil {
		return err
	}
	if !a.Valid || in.Goal != a.Revision.Ref || in.ApprovalSequence != a.ApprovalSequence || in.AcceptanceSHA != digest(a.Revision.Proposal.Acceptance) {
		return c.ErrConflict
	}
	if _, err := deliveryPrincipal(ctx, q, a); err != nil {
		return err
	}
	evidence, err := loadMessage(ctx, q, in.EvidenceMessageID)
	if err != nil {
		return err
	}
	if evidence.Imported || evidence.AuthorKind != "owner" || (evidence.ThreadID != a.Revision.Proposal.ThreadID && evidence.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID) {
		return c.ErrForbidden
	}
	if err = member(ctx, q, actor, evidence.ThreadID); err != nil {
		return err
	}
	allowed := false
	repo, err := svc.artifactRepository(in.TaskURL, "issues")
	if err != nil {
		return err
	}
	for _, scope := range a.Revision.Proposal.Repositories {
		for _, base := range scope.BaseBranches {
			if scope.Name == repo && base == in.BaseBranch {
				allowed = true
			}
		}
	}
	if !allowed {
		return c.ErrForbidden
	}
	qa := a.Revision.Proposal.QA
	if (qa.Required || in.Environment != "" || in.EnvironmentRevision != "") && (in.Environment != qa.Environment || in.EnvironmentRevision != qa.EnvironmentRevision) {
		if err = deliveryQAEnvironment(ctx, q, actor, a, in); err != nil {
			return err
		}
	}
	if qa.Required && (in.QAReviewID == "" || in.QAEvidenceMessageID == "" || in.QAEvidenceBodySHA == "" || in.Environment == "" || in.EnvironmentRevision == "") {
		return c.ErrForbidden
	}
	if in.QAEvidenceMessageID != "" {
		report, err := loadMessage(ctx, q, in.QAEvidenceMessageID)
		if err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(report.Body))
		if report.Imported || report.AuthorKind != "agent" || report.AuthorID != a.Actors.QA.PrincipalID || report.ThreadID != qa.EvidenceDestination.ThreadID || hex.EncodeToString(hash[:]) != in.QAEvidenceBodySHA {
			return c.ErrForbidden
		}
		if err = member(ctx, q, actor, report.ThreadID); err != nil {
			return err
		}
	}
	return nil
}

// The master verifies the owner decision and its private evidence. The worker
// must still resolve the prior GitHub review chain against the current artifact.
func deliveryQAEnvironment(ctx context.Context, q queryer, actor c.Principal, a c.GoalAuthority, in c.HandoffInput) error {
	rows, err := q.QueryContext(ctx, `SELECT body FROM handoffs WHERE goal_id=? AND revision=?
		AND json_extract(body,'$.input.kind')='reassess-qa'`, in.Goal.GoalID, in.Goal.RevisionSHA256)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		var h c.Handoff
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &h); err != nil {
			return err
		}
		x := h.Input
		if h.ID == "" || h.ActorKind != "owner" || h.ActorID == "" || h.CreatedAt.IsZero() || c.ValidateHandoff(x) != nil || x.Kind != "reassess-qa" || x.Goal != in.Goal || x.TaskURL != in.TaskURL || x.AssignmentSHA != in.AssignmentSHA || x.PRURL != in.PRURL || x.HeadSHA != in.HeadSHA || x.ArtifactSHA != in.ArtifactSHA || x.Environment != in.Environment || x.EnvironmentRevision != in.EnvironmentRevision || x.Environment == "" {
			continue
		}
		owner, e := principal(ctx, q, h.ActorID)
		if e != nil || owner.ID != h.ActorID || owner.Kind != "owner" || member(ctx, q, owner, a.Revision.Proposal.ThreadID) != nil {
			continue
		}
		evidence, e := loadMessage(ctx, q, x.EvidenceMessageID)
		if e != nil || evidence.Imported || evidence.AuthorKind != "owner" || (evidence.ThreadID != a.Revision.Proposal.ThreadID && evidence.ThreadID != a.Revision.Proposal.QA.EvidenceDestination.ThreadID) {
			continue
		}
		author, e := principal(ctx, q, evidence.AuthorID)
		if e != nil || author.ID != evidence.AuthorID || author.Kind != "owner" || member(ctx, q, author, evidence.ThreadID) != nil || member(ctx, q, owner, evidence.ThreadID) != nil || member(ctx, q, actor, evidence.ThreadID) != nil {
			continue
		}
		return nil
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return c.ErrForbidden
}

func (svc *Service) Handoffs(ctx context.Context, actor c.Principal, in c.HandoffQuery) (c.HandoffPage, error) {
	out := c.HandoffPage{Handoffs: []c.Handoff{}}
	p, err := authorize(ctx, svc.Store.db, actor, "read")
	if err != nil {
		return out, err
	}
	if _, err := svc.Store.resolveGoal(ctx, svc.Store.db, p, in.Goal); err != nil {
		return out, err
	}
	rows, err := svc.Store.db.QueryContext(ctx, "SELECT body FROM handoffs WHERE goal_id=? AND revision=? AND id>? ORDER BY id LIMIT ?", in.Goal.GoalID, in.Goal.RevisionSHA256, in.After, c.MaxPageRecords+1)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		var h c.Handoff
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		if err = json.Unmarshal(raw, &h); err != nil {
			return out, err
		}
		if len(out.Handoffs) == c.MaxPageRecords {
			out.Next = out.Handoffs[len(out.Handoffs)-1].ID
			break
		}
		out.Handoffs = append(out.Handoffs, h)
	}
	return out, rows.Err()
}
func registerHandoffs(mux *http.ServeMux, svc *Service, wrap func(func(http.ResponseWriter, *http.Request, c.Principal) (any, error)) http.HandlerFunc) {
	mux.HandleFunc("POST /api/v1/handoffs/tag-only/validate", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.TagOnlyValidation
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.ValidateTagOnlyHandoff(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/handoffs", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.HandoffInput
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.CreateHandoff(r.Context(), p, in)
	}))
	mux.HandleFunc("POST /api/v1/handoffs/list", wrap(func(w http.ResponseWriter, r *http.Request, p c.Principal) (any, error) {
		var in c.HandoffQuery
		if err := decode(w, r, &in); err != nil {
			return nil, err
		}
		return svc.Handoffs(r.Context(), p, in)
	}))
}
