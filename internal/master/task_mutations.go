package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// A durable reservation spans the provider CAS and registry commit. Registry
// writes cannot change a task or publish an old-assignment PR during that gap,
// including after a process exit or an uncertain provider response.
type taskBindReservation struct {
	Actor          string           `json:"actor"`
	Input          c.TaskBindInput  `json:"input"`
	AdoptionOrigin *c.TaskBindInput `json:"adoption_origin,omitempty"`
}

func taskMutationKey(ref string) string { return "task-bind:" + digest(ref) }
func taskMutation(ctx context.Context, q queryer, ref string) (taskBindReservation, bool, error) {
	var out taskBindReservation
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", taskMutationKey(ref)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, false, fmt.Errorf("decode task mutation reservation: %w", err)
	}
	return out, true, nil
}
func sameTaskMutation(a, b taskBindReservation) bool {
	return a.Actor == b.Actor && digest(a.Input) == digest(b.Input)
}
func (svc *Service) reserveTaskBinding(ctx context.Context, actor string, in c.TaskBindInput, providerSame bool, observed ...c.ProviderTask) (taskBindReservation, error) {
	out := taskBindReservation{Actor: actor, Input: in}
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	var accepted bool
	found, err := request(ctx, tx, actor, in.RequestID, "task-bind", in, &accepted)
	if err != nil {
		return out, err
	}
	pending, exists, err := taskMutation(ctx, tx, in.Binding.TaskURL)
	if err != nil {
		return out, err
	}
	if exists {
		out.AdoptionOrigin = pending.AdoptionOrigin
	}
	replacing := exists && !sameTaskMutation(pending, out)
	if replacing {
		if len(observed) != 1 {
			return out, c.ErrConflict
		}
		if err = svc.appliedAdoptionRecovery(ctx, tx, pending, &out, observed[0]); err != nil {
			return out, err
		}
	}
	var registered string
	registryErr := tx.QueryRowContext(ctx, "SELECT assignment_sha FROM artifacts WHERE goal_id=? AND revision=? AND url=? AND kind='task'", in.Binding.Goal.GoalID, in.Binding.Goal.RevisionSHA256, in.Binding.TaskURL).Scan(&registered)
	if registryErr != nil && !errors.Is(registryErr, sql.ErrNoRows) {
		return out, registryErr
	}
	sha := digest(in.Binding)
	if registryErr == nil && registered != sha && registered != in.ExpectedAssignmentSHA {
		return out, c.ErrConflict
	}
	if !providerSame || (registryErr == nil && registered != sha) {
		published, err := taskPublished(ctx, tx, in.Binding.Goal, in.Binding.TaskURL)
		if err != nil {
			return out, err
		}
		if published {
			return out, c.ErrConflict
		}
	}
	if !found {
		if err = saveRequest(ctx, tx, actor, in.RequestID, "task-bind", in, true); err != nil {
			return out, err
		}
	}
	if !exists {
		if _, err = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?)", taskMutationKey(in.Binding.TaskURL), string(encode(out))); err != nil {
			return out, err
		}
	}
	if replacing {
		if _, err = tx.ExecContext(ctx, "UPDATE meta SET value=? WHERE key=?", string(encode(out)), taskMutationKey(in.Binding.TaskURL)); err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}
func finishTaskMutation(ctx context.Context, q queryer, ref string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM meta WHERE key=?", taskMutationKey(ref))
	return err
}

// A newer provider revision makes the reserved CAS impossible to apply later.
// Release it only when both provider and registry still hold the old assignment.
// The immutable request journal remains, so advancing the original request's
// revision is forbidden. Same-revision uncertainty stays reserved for exact retry.
func (svc *Service) reconcileTaskMutation(ctx context.Context, task c.ProviderTask) error {
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	pending, exists, err := taskMutation(ctx, tx, task.Reference)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	in := pending.Input
	current := ""
	if task.Binding != nil {
		current = digest(task.Binding)
	}
	if task.Goal != in.Binding.Goal || task.Revision <= in.ExpectedRevision || current == digest(in.Binding) || current != in.ExpectedAssignmentSHA {
		return nil
	}
	var registered string
	err = tx.QueryRowContext(ctx, "SELECT assignment_sha FROM artifacts WHERE goal_id=? AND revision=? AND url=? AND kind='task'", task.Goal.GoalID, task.Goal.RevisionSHA256, task.Reference).Scan(&registered)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && registered != in.ExpectedAssignmentSHA {
		return nil
	}
	if err = finishTaskMutation(ctx, tx, task.Reference); err != nil {
		return err
	}
	return tx.Commit()
}

// Only a fresh, currently authorized TaskBind may replace an applied adoption
// that never reached the registry. The original journals remain immutable.
func (svc *Service) appliedAdoptionRecovery(ctx context.Context, tx *sql.Tx, pending taskBindReservation, next *taskBindReservation, task c.ProviderTask) error {
	old, in := pending.Input, next.Input
	if pending.Actor != next.Actor || task.Binding == nil || task.BindingActor.ID != pending.Actor || task.State != "open" || task.Reference != in.Binding.TaskURL || task.Goal != in.Binding.Goal || old.Binding.Goal != in.Binding.Goal || digest(task.Binding) != digest(old.Binding) || task.Revision <= old.ExpectedRevision || in.ExpectedRevision != task.Revision || in.ExpectedAssignmentSHA != digest(old.Binding) {
		return c.ErrConflict
	}
	project, uid, repo, err := c.ParseKataTaskReference(task.Reference)
	if err != nil || task.ProjectUID != project || task.IssueUID != uid || task.Repository != repo || project != svc.Tasks.Capabilities().ProjectUID {
		return c.ErrConflict
	}
	// Carry the original adoption through further failed recovery writes. Both
	// the origin and current pending bind must still match immutable journals.
	origin := old
	if pending.AdoptionOrigin != nil {
		origin = *pending.AdoptionOrigin
	} else if old.ExpectedAssignmentSHA != "" {
		return c.ErrConflict
	}
	if origin.ExpectedAssignmentSHA != "" || origin.Binding.TaskURL != task.Reference || origin.Binding.Goal != in.Binding.Goal {
		return c.ErrConflict
	}
	for _, input := range []c.TaskBindInput{origin, old} {
		var accepted bool
		found, e := request(ctx, tx, pending.Actor, input.RequestID, "task-bind", input, &accepted)
		if e != nil {
			return e
		}
		if !found || !accepted {
			return c.ErrConflict
		}
	}
	// The reservation alone cannot turn arbitrary native metadata into authority.
	// Require its exact originating authenticated adoption journal as well.
	rows, err := tx.QueryContext(ctx, "SELECT request_id FROM requests WHERE actor=? AND kind='task-adopt' AND result=?", pending.Actor, encode(taskAdoptIntent{Binding: origin.Binding}))
	if err != nil {
		return err
	}
	proven := false
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if origin.RequestID == "adopt-bind-"+digest(struct{ Actor, Request string }{pending.Actor, id}) {
			proven = true
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if !proven {
		return c.ErrConflict
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM artifacts WHERE kind='task' AND url LIKE ?", "kata://"+strings.ToLower(project)+"/"+uid+"/%").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return c.ErrConflict
	}
	p, err := authorize(ctx, tx, c.Principal{ID: next.Actor}, "read")
	if err != nil {
		return err
	}
	if !planner(p) {
		return c.ErrForbidden
	}
	if err = admission(ctx, tx); err != nil {
		return err
	}
	if err = svc.taskBindingAuthority(ctx, tx, p, in.Binding); err != nil {
		return err
	}
	var goalThread string
	if err = tx.QueryRowContext(ctx, "SELECT thread_id FROM goals WHERE id=?", in.Binding.Goal.GoalID).Scan(&goalThread); err != nil {
		return err
	}
	if err = svc.taskAdoptionConversation(ctx, tx, p, goalThread, uid); err != nil {
		return err
	}
	next.AdoptionOrigin = &origin
	return nil
}

func (svc *Service) taskBindingAuthority(ctx context.Context, q queryer, p c.Principal, binding c.NativeTaskBinding) error {
	current, err := svc.Store.resolveGoal(ctx, q, p, binding.Goal)
	if err != nil {
		return err
	}
	if !current.Valid || current.ApprovalSequence != binding.ApprovalSequence || current.Actors.Author.GitHubBotID != binding.AuthorID || current.Actors.Peer.GitHubBotID != binding.PeerID || current.Actors.QA.GitHubBotID != binding.QAID || svc.Tasks.Capabilities().PolicySHA != binding.PolicySHA {
		return c.ErrConflict
	}
	return nil
}
