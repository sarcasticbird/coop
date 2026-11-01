package master

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// Public CI uses synthetic records, never original private PR9 evidence.
type retainedDependencyTasks struct {
	*testTasks
	tasks  map[string]c.ProviderTask
	policy string
}

func (b *retainedDependencyTasks) Capabilities() c.TaskCapabilities {
	out := b.testTasks.Capabilities()
	out.PolicySHA = b.policy
	return out
}
func (b *retainedDependencyTasks) Read(_ context.Context, ref string) (c.ProviderTask, error) {
	task, ok := b.tasks[ref]
	if !ok {
		return c.ProviderTask{}, c.ErrNotFound
	}
	return task, nil
}
func (b *retainedDependencyTasks) Create(ctx context.Context, in c.TaskCreateInput, actor c.Principal) (c.ProviderTask, error) {
	uid := fmt.Sprintf("01M5MSA8FZCCD7DWMTP6RWM3%02d", b.creates+1)
	ref, err := c.KataTaskReference(b.Capabilities().ProjectUID, uid, in.Repository)
	if err != nil {
		return c.ProviderTask{}, err
	}
	b.task = c.ProviderTask{Reference: ref, ProjectUID: b.Capabilities().ProjectUID, IssueUID: uid, Repository: in.Repository, Title: in.Title, Body: in.Body, Goal: in.Goal, State: "open", Revision: 1}
	task, err := b.testTasks.Create(ctx, in, actor)
	if err == nil {
		b.tasks[task.Reference] = task
	}
	return task, err
}
func (b *retainedDependencyTasks) Bind(ctx context.Context, in c.TaskBindInput, actor c.Principal) (c.ProviderTask, error) {
	b.task = b.tasks[in.Binding.TaskURL]
	task, err := b.testTasks.Bind(ctx, in, actor)
	if err == nil {
		b.tasks[task.Reference] = task
	}
	return task, err
}

func TestRetainedSyntheticIncrementalDependenciesStayRevisionBound(t *testing.T) {
	s, owner, _, in, _ := taskFixture(t)
	backend := &retainedDependencyTasks{testTasks: &testTasks{}, tasks: map[string]c.ProviderTask{}, policy: strings.Repeat("a", 64)}
	s.Tasks = backend
	predecessor, err := s.TaskCreate(t.Context(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	originalSHA := digest(predecessor.Binding)
	backend.policy = strings.Repeat("b", 64)
	successor := in
	successor.Title, successor.Body, successor.RequestID = "Synthetic successor", "Harmless synthetic dependency fixture", "synthetic-successor"
	successor.DependsOn = []string{predecessor.Reference}
	created, err := s.TaskCreate(t.Context(), owner, successor)
	if err != nil || created.Binding == nil || created.Binding.Goal != predecessor.Goal || created.Binding.PolicySHA != backend.policy || len(created.Binding.DependsOn) != 1 || created.Binding.DependsOn[0] != predecessor.Reference {
		t.Fatal("same-revision incremental dependency failed", err)
	}
	a, err := s.ResolveGoal(t.Context(), owner, in.Goal)
	if err != nil {
		t.Fatal(err)
	}
	proposal := a.Revision.Proposal
	proposal.Scope += " synthetic next revision"
	proposal.RequestID = "synthetic-next-revision"
	next, err := s.ProposeGoal(t.Context(), owner, proposal)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := s.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: next.Ref.GoalID, RevisionSHA256: next.Ref.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: "approve", RequestID: "synthetic-next-approval"})
	if err != nil {
		t.Fatal(err)
	}
	successor.Goal, successor.ExpectedApprovalSequence, successor.RequestID = next.Ref, approval.ApprovalSequence, "synthetic-cross-revision"
	if _, err = s.TaskCreate(t.Context(), owner, successor); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("cross-revision predecessor was not refused", err)
	}
	if backend.creates != 2 || backend.binds != 2 || backend.closes != 0 || digest(backend.tasks[predecessor.Reference].Binding) != originalSHA {
		t.Fatal("dependency handling replaced predecessor or reached an extra effect")
	}
}
