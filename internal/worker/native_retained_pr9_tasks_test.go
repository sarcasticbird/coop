package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type pr9IncrementalProvider struct {
	*pr9Provider
	successor      c.ProviderTask
	creates, binds int
}

func (p *pr9IncrementalProvider) Read(ctx context.Context, ref string) (c.ProviderTask, error) {
	if ref == p.successor.Reference && ref != "" {
		return p.successor, nil
	}
	return p.pr9Provider.Read(ctx, ref)
}
func (p *pr9IncrementalProvider) Create(_ context.Context, in c.TaskCreateInput, _ c.Principal) (c.ProviderTask, error) {
	if p.successor.Reference != "" {
		return p.successor, nil
	}
	p.creates++
	ref, err := c.KataTaskReference(p.task.ProjectUID, "01M69NJWQXXBT2PQWX307ZFPFJ", in.Repository)
	if err != nil {
		return c.ProviderTask{}, err
	}
	p.successor = c.ProviderTask{Reference: ref, ProjectUID: p.task.ProjectUID, IssueUID: "01M69NJWQXXBT2PQWX307ZFPFJ", Repository: in.Repository, Title: in.Title, Body: in.Body, State: "open", Revision: 1, Goal: in.Goal}
	return p.successor, nil
}
func (p *pr9IncrementalProvider) Bind(_ context.Context, in c.TaskBindInput, actor c.Principal) (c.ProviderTask, error) {
	if p.successor.Binding != nil && NativeAssignmentSHA(*p.successor.Binding) == NativeAssignmentSHA(in.Binding) {
		return p.successor, nil
	}
	if in.Binding.TaskURL != p.successor.Reference || in.ExpectedRevision != p.successor.Revision {
		return c.ProviderTask{}, c.ErrConflict
	}
	p.binds++
	p.successor.Binding, p.successor.BindingActor = &in.Binding, actor
	p.successor.Revision++
	return p.successor, nil
}

func TestRetainedPR9IncrementalTaskCreatePinsOriginalPredecessor(t *testing.T) {
	f := newPR9Journey(t)
	backend := &pr9IncrementalProvider{pr9Provider: f.provider}
	f.service.Tasks = backend
	before, _ := json.Marshal(backend.task.Binding)
	in := c.TaskCreateInput{Goal: f.data.Task.Goal, ExpectedApprovalSequence: 1, Repository: "sarcasticbird/gh-sweep", BaseBranch: "main", Title: "Synthetic same-goal successor", Body: "Test-only successor pinned to original PR9 predecessor " + pr9Task + " with immutable assignment " + pr9Assignment, DependsOn: []string{pr9Task}, RequestID: "fixture-incremental-successor"}
	created, err := f.clients["rook"].TaskCreate(t.Context(), in)
	if err != nil {
		t.Fatal("incremental same-goal creation", err)
	}
	if created.Binding == nil || created.Binding.Goal != f.data.Task.Goal || created.Binding.PolicySHA != f.provider.policy || created.Binding.ApprovalSequence != 1 || len(created.Binding.DependsOn) != 1 || created.Binding.DependsOn[0] != pr9Task || created.Binding.AuthorID != 342161151 || created.Binding.PeerID != 342159661 || created.Binding.QAID != 343005406 {
		t.Fatal("successor did not retain its exact same-revision predecessor", created.Binding)
	}
	if _, err = f.clients["rook"].TaskCreate(t.Context(), in); err != nil {
		t.Fatal("original request replay", err)
	}
	list, err := f.clients["rook"].TaskList(t.Context(), c.TaskListInput{Goal: in.Goal})
	if err != nil || len(list.Tasks) != 2 || backend.creates != 1 || backend.binds != 1 {
		t.Fatal("incremental creation duplicated or replaced the existing task", len(list.Tasks), backend.creates, backend.binds, err)
	}
	proposal := f.data.Authority.Revision.Proposal
	proposal.Scope += "\nSynthetic cross-revision dependency refusal fixture."
	proposal.RequestID = "fixture-next-revision"
	next, err := f.clients["rook"].ProposeGoal(t.Context(), proposal)
	if err != nil || next.Ref == in.Goal {
		t.Fatal("new revision", err)
	}
	approval, err := f.clients["owner"].DecideGoal(t.Context(), c.GoalDecisionInput{GoalID: next.Ref.GoalID, RevisionSHA256: next.Ref.RevisionSHA256, ExpectedDecisionSequence: 1, Action: "approve", RequestID: "fixture-approve-next-revision"})
	if err != nil {
		t.Fatal(err)
	}
	in.Goal, in.ExpectedApprovalSequence, in.RequestID = next.Ref, approval.ApprovalSequence, "fixture-cross-revision-denied"
	if _, err = f.clients["rook"].TaskCreate(t.Context(), in); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("cross-revision dependency must be refused before creation", err)
	}
	after, _ := json.Marshal(backend.task.Binding)
	if !bytes.Equal(before, after) || NativeAssignmentSHA(*backend.task.Binding) != pr9Assignment || backend.creates != 1 || backend.binds != 1 || f.provider.completes != 0 || len(f.replay.writes) != 0 {
		t.Fatal("dependency refusal rewrote predecessor or reached an effect")
	}
}
