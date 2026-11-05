package master

import (
	"context"
	"encoding/json"
	"fmt"
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
		task, err := svc.Tasks.Read(ctx, in.TaskURL)
		if err != nil {
			return out, err
		}
		if task.Goal != in.Goal || task.Repository != taskRepo || task.Binding == nil || digest(task.Binding) != in.AssignmentSHA {
			return out, c.ErrConflict
		}
		expected, err := svc.taskBinding(ctx, p, a, task, task.Binding.BaseBranch, task.Binding.DependsOn)
		if err != nil {
			return out, err
		}
		if !taskExecutionBindingMatches(expected, *task.Binding) {
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
	if in.Kind == "release" || environmentDecision {
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
		if e != nil {
			return out, e
		}
		_ = tx.Rollback()
		return out, svc.notifyReleaseAuthor(ctx, p, a, out)
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
	out = c.Handoff{ID: newID("ho"), Input: in, ActorID: p.ID, ActorKind: p.Kind, CreatedAt: svc.Store.now().UTC()}
	if _, err = tx.ExecContext(ctx, "INSERT INTO handoffs(id,goal_id,revision,release_key,body) VALUES(?,?,?,?,?)", out.ID, in.Goal.GoalID, in.Goal.RevisionSHA256, releaseKey, encode(out)); err != nil {
		return out, err
	}
	if in.Kind == "release" {
		notice := releaseAuthorMessage(a, out)
		if _, err = authorize(ctx, tx, p, "send"); err != nil {
			return out, err
		}
		if err = member(ctx, tx, p, notice.ThreadID); err != nil {
			return out, err
		}
		receipt, err := svc.Store.insertMessage(ctx, tx, p, notice)
		if err != nil {
			return out, err
		}
		if err = svc.Store.stageSource(ctx, tx, p, &receipt, ""); err != nil {
			return out, err
		}
		if err = saveRequest(ctx, tx, p.ID, notice.RequestID, "send", notice, receipt); err != nil {
			return out, err
		}
	}
	if err = saveRequest(ctx, tx, p.ID, in.RequestID, "handoff", in, out); err != nil {
		return out, err
	}
	if err = event(ctx, tx, "handoff", a.Revision.Proposal.ThreadID, ""); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return out, svc.notifyReleaseAuthor(ctx, p, a, out)
}

// Reuse durable message delivery rather than scheduling author merge work.
// A retry repairs a failed notification without creating another handoff/wake.
func (svc *Service) notifyReleaseAuthor(ctx context.Context, owner c.Principal, a c.GoalAuthority, h c.Handoff) error {
	if h.Input.Kind != "release" {
		return nil
	}
	_, err := svc.Store.Send(ctx, owner, releaseAuthorMessage(a, h))
	return err
}

func releaseAuthorMessage(a c.GoalAuthority, h c.Handoff) c.SendMessage {
	body := fmt.Sprintf("Owner-approved release handoff %s for %s. Read this exact handoff through the authenticated master and execute the guarded release follow-up:\n/usr/local/libexec/coop-worker native release --config /var/lib/coop/worker/config.json --goal %s --revision %s --handoff %s\nThis is release-only authority. Verify the merged source and current release gates; do not merge, reopen the task, modify source, or install/deploy. Report the result, preserving the same handoff and any uncertain journal.", h.ID, h.Input.PRURL, h.Input.Goal.GoalID, h.Input.Goal.RevisionSHA256, h.ID)
	return c.SendMessage{ThreadID: a.Revision.Proposal.ThreadID, Recipients: []string{a.Actors.Author.PrincipalID}, Body: body, RequestID: "release-handoff-" + h.ID}
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
