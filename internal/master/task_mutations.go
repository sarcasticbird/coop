package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// A durable reservation spans the provider CAS and registry commit. Registry
// writes cannot change a task or publish an old-assignment PR during that gap,
// including after a process exit or an uncertain provider response.
type taskBindReservation struct {
	Actor string          `json:"actor"`
	Input c.TaskBindInput `json:"input"`
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
func (svc *Service) reserveTaskBinding(ctx context.Context, actor string, in c.TaskBindInput, providerSame bool) (taskBindReservation, error) {
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
	if exists && !sameTaskMutation(pending, out) {
		return out, c.ErrConflict
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
