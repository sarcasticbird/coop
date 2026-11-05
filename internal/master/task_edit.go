package master

import (
	"context"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

// Task access follows existing project/team membership, not engineering roles.
// Service principals and nonmembers cannot acquire access by supplying a UID.
func (svc *Service) taskEditAuthority(ctx context.Context, actor c.Principal, mutation bool) (c.Principal, taskstore.TaskEditor, error) {
	capability := "read"
	if mutation {
		capability = "send"
	}
	p, err := authorize(ctx, svc.Store.db, actor, capability)
	if err != nil {
		return p, nil, err
	}
	if p.Kind != "owner" && p.Kind != "agent" {
		return p, nil, c.ErrForbidden
	}
	if svc.Tasks == nil || svc.Tasks.Capabilities().Provider != "kata" || svc.Store.conversations.TeamThreadID == "" || svc.Store.conversations.ProjectUID != svc.Tasks.Capabilities().ProjectUID {
		return p, nil, c.ErrUnsupported
	}
	if err = member(ctx, svc.Store.db, p, svc.Store.conversations.TeamThreadID); err != nil {
		return p, nil, err
	}
	backend, ok := svc.Tasks.(taskstore.TaskEditor)
	if !ok {
		return p, nil, c.ErrUnsupported
	}
	return p, backend, nil
}
func (svc *Service) taskIssueUID(ref string) (string, error) {
	if _, err := c.KataTaskReference(ref, ref, "validation/repository"); err == nil {
		return ref, nil
	}
	project, uid, _, err := c.ParseKataTaskReference(ref)
	if err != nil {
		return "", err
	}
	if project != svc.Tasks.Capabilities().ProjectUID {
		return "", c.ErrForbidden
	}
	return uid, nil
}
func (svc *Service) taskIssueRead(ctx context.Context, actor c.Principal, ref string) (c.ProviderTask, error) {
	_, backend, err := svc.taskEditAuthority(ctx, actor, false)
	if err != nil {
		return c.ProviderTask{}, err
	}
	uid, err := svc.taskIssueUID(ref)
	if err != nil {
		return c.ProviderTask{}, err
	}
	issue, err := backend.InspectIssue(ctx, uid)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if _, _, err = svc.taskEditAuthority(ctx, actor, false); err != nil {
		return c.ProviderTask{}, err
	}
	if issue.UID != uid || issue.ProjectUID != svc.Tasks.Capabilities().ProjectUID || issue.Revision < 1 {
		return c.ProviderTask{}, c.ErrConflict
	}
	return c.ProviderTask{Reference: uid, IssueUID: uid, ProjectUID: issue.ProjectUID, Title: issue.Title, Body: issue.Body, State: issue.Status, ClosedReason: issue.ClosedReason, Owner: issue.Owner, Revision: issue.Revision}, nil
}
func (svc *Service) taskIssueList(ctx context.Context, actor c.Principal, in c.TaskListInput) (c.ProviderTaskPage, error) {
	_, backend, err := svc.taskEditAuthority(ctx, actor, false)
	if err != nil {
		return c.ProviderTaskPage{}, err
	}
	out, err := backend.ListIssues(ctx, in)
	if err != nil {
		return c.ProviderTaskPage{}, err
	}
	if _, _, err = svc.taskEditAuthority(ctx, actor, false); err != nil {
		return c.ProviderTaskPage{}, err
	}
	return out, nil
}

// Use the existing requests table and native Kata idempotency, not another
// approval ledger. A lost reply retains the exact input and revision on retry.
func (svc *Service) editTask(ctx context.Context, actor c.Principal, id, kind string, input any, write func(c.Principal, taskstore.TaskEditor) (c.ProviderTask, error)) (c.ProviderTask, error) {
	p, _, err := svc.taskEditAuthority(ctx, actor, true)
	if err != nil {
		return c.ProviderTask{}, err
	}
	var saved struct {
		Task      *c.ProviderTask `json:"task,omitempty"`
		CreatedAt time.Time       `json:"created_at"`
	}
	tx, err := svc.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return c.ProviderTask{}, err
	}
	defer func() { _ = tx.Rollback() }()
	found, err := request(ctx, tx, p.ID, id, kind, input, &saved)
	if err != nil {
		return c.ProviderTask{}, err
	}
	if !found {
		saved.CreatedAt = svc.Store.now().UTC()
		if err = saveRequest(ctx, tx, p.ID, id, kind, input, saved); err != nil {
			return c.ProviderTask{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return c.ProviderTask{}, err
	}
	if saved.Task != nil {
		return *saved.Task, nil
	}
	// Native Kata deduplicates keys for seven days. Beyond that window an
	// unresolved create/comment could be repeated even with identical input.
	if saved.CreatedAt.IsZero() || svc.Store.now().UTC().Sub(saved.CreatedAt) >= 7*24*time.Hour {
		return c.ProviderTask{}, c.ErrConflict
	}
	p, backend, err := svc.taskEditAuthority(ctx, actor, true)
	if err != nil {
		return c.ProviderTask{}, err
	}
	out, err := write(p, backend)
	if err != nil {
		return c.ProviderTask{}, err
	}
	saved.Task = &out
	_, err = svc.Store.db.ExecContext(ctx, "UPDATE requests SET result=? WHERE actor=? AND request_id=? AND kind=? AND digest=?", encode(saved), p.ID, id, kind, digest(input))
	return out, err
}
func (svc *Service) TaskUpdate(ctx context.Context, actor c.Principal, in c.TaskUpdateInput) (c.ProviderTask, error) {
	if err := in.Validate(); err != nil {
		return c.ProviderTask{}, err
	}
	if _, _, err := svc.taskEditAuthority(ctx, actor, true); err != nil {
		return c.ProviderTask{}, err
	}
	if _, err := svc.taskIssueUID(in.Reference); err != nil {
		return c.ProviderTask{}, err
	}
	return svc.editTask(ctx, actor, in.RequestID, "task-update", in, func(p c.Principal, backend taskstore.TaskEditor) (c.ProviderTask, error) {
		return backend.UpdateIssue(ctx, p, in)
	})
}
