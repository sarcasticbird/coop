package master

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (svc *Service) CreateHandoff(ctx context.Context, actor c.Principal, in c.HandoffInput) (c.Handoff, error) {
	var out c.Handoff
	if err := c.ValidateHandoff(in); err != nil {
		return out, err
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
	if !a.Valid {
		return out, c.ErrForbidden
	}
	if !planner(p) || in.Kind == "release" && p.Kind != "owner" {
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
		task, err := svc.Tasks.Read(ctx, in.TaskURL)
		if err != nil {
			return out, err
		}
		if task.Goal != in.Goal || task.Repository != taskRepo || task.Binding == nil || digest(task.Binding) != in.AssignmentSHA || task.Binding.ApprovalSequence != a.ApprovalSequence {
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
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	if in.Kind == "release" {
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
	if !fresh.Valid || fresh.ApprovalSequence != a.ApprovalSequence {
		return out, c.ErrConflict
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
	if in.Kind == "release" {
		releaseKey = repo + ":" + in.Tag
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM handoffs WHERE release_key=?", releaseKey).Scan(&count); err != nil {
			return out, err
		}
		if count != 0 {
			return out, c.ErrConflict
		}
	}
	out = c.Handoff{ID: newID("ho"), Input: in, ActorID: p.ID, CreatedAt: svc.Store.now().UTC()}
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
