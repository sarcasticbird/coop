package master

import (
	"errors"
	"reflect"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestTaskBindingReapprovalAfterRejectOrReopen(t *testing.T) {
	for _, withdraw := range []string{"reject", "reopen"} {
		t.Run(withdraw, func(t *testing.T) {
			svc, owner, author, create, backend := taskFixture(t)
			task, err := svc.TaskCreate(t.Context(), owner, create)
			if err != nil {
				t.Fatal(err)
			}
			original := *task.Binding
			rook := enrollTaskRook(t, svc, owner, create.Goal)
			input := c.TaskCompleteInput{Reference: task.Reference, Goal: create.Goal, AssignmentSHA: digest(task.Binding), PullRequestURL: "https://github.com/example/coop/pull/2", RequestID: "reapproved-completion"}
			if _, err := svc.RegisterArtifact(t.Context(), author, c.ArtifactLink{Goal: input.Goal, Kind: "pull_request", URL: input.PullRequestURL, TaskURL: input.Reference, AssignmentSHA: input.AssignmentSHA, RequestID: "pr"}); err != nil {
				t.Fatal(err)
			}
			decide := func(action string) c.GoalAuthority {
				t.Helper()
				a, err := svc.ResolveGoal(t.Context(), owner, create.Goal)
				if err != nil {
					t.Fatal(err)
				}
				a, err = svc.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: create.Goal.GoalID, RevisionSHA256: create.Goal.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: action, RequestID: "reapproval-" + action})
				if err != nil {
					t.Fatal(err)
				}
				return a
			}
			if withdraw == "reopen" {
				decide("complete")
			}
			a := decide(withdraw)
			if a.Valid || a.ApprovalSequence != 0 {
				t.Fatal("withdrawal retained authority", a)
			}
			if _, err := svc.TaskComplete(t.Context(), rook, input); !errors.Is(err, c.ErrForbidden) || backend.closes != 0 {
				t.Fatal("withdrawn authority reached provider", err)
			}
			a = decide("approve")
			if !a.Valid || a.Revision.Ref != original.Goal || a.ApprovalSequence <= original.ApprovalSequence {
				t.Fatal("same-revision reapproval missing", a)
			}
			if _, err := svc.TaskComplete(t.Context(), rook, input); err != nil {
				t.Fatal("explicit reapproval did not restore unchanged binding", err)
			}
			if backend.closes != 1 || backend.binds != 1 || !reflect.DeepEqual(original, *backend.task.Binding) {
				t.Fatal("reapproval rewrote original assignment")
			}
		})
	}
}

func TestTaskCompletionKeepsBindingAfterSameScopeRenewal(t *testing.T) {
	for _, duringRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-read", true: "during-read"}[duringRead], func(t *testing.T) {
			svc, owner, author, input, backend := taskFixture(t)
			task, err := svc.TaskCreate(t.Context(), owner, input)
			if err != nil {
				t.Fatal(err)
			}
			original := *task.Binding
			rook := enrollTaskRook(t, svc, owner, input.Goal)
			complete := c.TaskCompleteInput{Reference: task.Reference, Goal: input.Goal, AssignmentSHA: digest(task.Binding), PullRequestURL: "https://github.com/example/coop/pull/2", RequestID: "complete-renewed"}
			if _, err := svc.RegisterArtifact(t.Context(), author, c.ArtifactLink{Goal: input.Goal, Kind: "pull_request", URL: complete.PullRequestURL, TaskURL: task.Reference, AssignmentSHA: complete.AssignmentSHA, RequestID: "pr"}); err != nil {
				t.Fatal(err)
			}
			renew := func() {
				a, err := svc.ResolveGoal(t.Context(), owner, input.Goal)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := svc.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: input.Goal.GoalID, RevisionSHA256: input.Goal.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: "approve", RequestID: "renew"}); err != nil {
					t.Fatal(err)
				}
			}
			if duringRead {
				backend.onRead = func() { backend.onRead = nil; renew() }
			} else {
				renew()
			}
			if _, err := svc.TaskComplete(t.Context(), rook, complete); err != nil {
				t.Fatal("unchanged published task required rebinding", err)
			}
			if backend.closes != 1 || backend.binds != 1 || !reflect.DeepEqual(original, *backend.task.Binding) {
				t.Fatal("completion rewrote or rebound task provenance")
			}
		})
	}
}

func TestTaskRenewalStillComparesEveryScopeField(t *testing.T) {
	svc, owner, _, input, _ := taskFixture(t)
	task, err := svc.TaskCreate(t.Context(), owner, input)
	if err != nil {
		t.Fatal(err)
	}
	current := *task.Binding
	current.ApprovalSequence += 4
	if !taskExecutionBindingMatches(current, *task.Binding) {
		t.Fatal("historical counter invalidated unchanged scope")
	}
	for name, mutate := range map[string]func(*c.NativeTaskBinding){
		"author":     func(b *c.NativeTaskBinding) { b.AuthorID++ },
		"peer":       func(b *c.NativeTaskBinding) { b.PeerID++ },
		"qa":         func(b *c.NativeTaskBinding) { b.QAID++ },
		"goal":       func(b *c.NativeTaskBinding) { b.Goal.RevisionSHA256 = digest("changed") },
		"task":       func(b *c.NativeTaskBinding) { b.TaskURL += "different" },
		"scope":      func(b *c.NativeTaskBinding) { b.TaskScopeSHA = digest("changed") },
		"policy":     func(b *c.NativeTaskBinding) { b.PolicySHA = digest("changed") },
		"base":       func(b *c.NativeTaskBinding) { b.BaseBranch = "outside" },
		"dependency": func(b *c.NativeTaskBinding) { b.DependsOn = []string{"different"} },
		"future":     func(b *c.NativeTaskBinding) { b.ApprovalSequence = current.ApprovalSequence + 1 },
		"invalid":    func(b *c.NativeTaskBinding) { b.ApprovalSequence = 0 },
	} {
		b := *task.Binding
		mutate(&b)
		if taskExecutionBindingMatches(current, b) {
			t.Fatal("renewal hid changed binding", name)
		}
	}
}

func TestTaskRenewalRetainsExplicitQAHandoffsWithoutRebinding(t *testing.T) {
	for _, kind := range []string{"reassess-qa", "first-postmerge-qa", "release"} {
		t.Run(kind, func(t *testing.T) {
			svc, owner, planner, _, in := qaHandoffFixture(t)
			backend := svc.Tasks.(*testTasks)
			original := *backend.task.Binding
			a, err := svc.ResolveGoal(t.Context(), owner, in.Goal)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: "approve", RequestID: "renew-handoff"}); err != nil {
				t.Fatal(err)
			}
			in.Kind = kind
			actor := planner
			if kind == "first-postmerge-qa" {
				in.ReviewID = ""
				in.Environment, in.EnvironmentRevision = a.Revision.Proposal.QA.Environment, a.Revision.Proposal.QA.EnvironmentRevision
				backend.task.State, backend.task.ClosedReason = "closed", "done"
			}
			if kind == "release" {
				actor = owner
				in.ReviewID, in.Tag, in.Workflow = "", "v1.2.3", "release.yml"
			}
			handoff, err := svc.CreateHandoff(t.Context(), actor, in)
			if err != nil || handoff.Input.AssignmentSHA != digest(original) {
				t.Fatal("unchanged task required rebind before handoff", kind, err)
			}
			again, err := svc.CreateHandoff(t.Context(), actor, in)
			if err != nil || again.ID != handoff.ID || backend.binds != 1 || !reflect.DeepEqual(original, *backend.task.Binding) {
				t.Fatal("handoff retry changed original binding or effect", err)
			}
		})
	}
}

func TestTaskRenewalCannotHideEditedScopeFromQAHandoff(t *testing.T) {
	svc, owner, planner, _, in := qaHandoffFixture(t)
	a, err := svc.ResolveGoal(t.Context(), owner, in.Goal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DecideGoal(t.Context(), owner, c.GoalDecisionInput{GoalID: in.Goal.GoalID, RevisionSHA256: in.Goal.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: "approve", RequestID: "renew-edited-task"}); err != nil {
		t.Fatal(err)
	}
	backend := svc.Tasks.(*testTasks)
	backend.task.Body += " Materially changed acceptance."
	if _, err := svc.CreateHandoff(t.Context(), planner, in); err != c.ErrConflict {
		t.Fatal("renewal concealed changed task scope", err)
	}
	page, err := svc.Handoffs(t.Context(), owner, c.HandoffQuery{Goal: in.Goal})
	if err != nil || len(page.Handoffs) != 0 || backend.binds != 1 {
		t.Fatal("rejected handoff created or rebound work", page, err)
	}
}
