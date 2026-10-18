package master

import (
	"context"
	"errors"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func goalFixture(t *testing.T) (*Service, c.Principal, c.Principal, c.GoalProposal) {
	s, o, b, th := storeFixture(t)
	return &Service{Store: s}, o, b, c.GoalProposal{ThreadID: th.ID, Purpose: "Improve Coop", Scope: "bounded work", Repositories: []c.RepositoryScope{{Name: "example/coop", BaseBranches: []string{"main"}}}, Acceptance: []string{"tests pass"}, AuthorID: b.ID, PeerID: "agent-two", QA: c.QAPolicy{Required: false}, RequestID: "propose-1"}
}
func TestGoalRevisionAuthority(t *testing.T) {
	s, o, b, in := goalFixture(t)
	ctx := context.Background()
	v, err := s.ProposeGoal(ctx, b, in)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.DecideGoal(ctx, o, c.GoalDecisionInput{GoalID: v.Ref.GoalID, RevisionSHA256: v.Ref.RevisionSHA256, ExpectedDecisionSequence: 0, Action: "approve", RequestID: "approve-1"})
	if err != nil || !a.Valid {
		t.Fatalf("approval %+v %v", a, err)
	}
	in.GoalID = v.Ref.GoalID
	in.Scope = "new draft"
	in.RequestID = "propose-2"
	draft, err := s.ProposeGoal(ctx, b, in)
	if err != nil || draft.Ref.RevisionSHA256 == v.Ref.RevisionSHA256 {
		t.Fatal("draft did not change hash")
	}
	old, err := s.ResolveGoal(ctx, o, v.Ref)
	if err != nil || !old.Valid {
		t.Fatal("draft interrupted approved work")
	}
	if _, err = s.DecideGoal(ctx, o, c.GoalDecisionInput{GoalID: v.Ref.GoalID, RevisionSHA256: draft.Ref.RevisionSHA256, ExpectedDecisionSequence: a.DecisionSequence, Action: "approve", RequestID: "approve-2"}); err != nil {
		t.Fatal(err)
	}
	old, err = s.ResolveGoal(ctx, o, v.Ref)
	if err != nil || old.Valid {
		t.Fatal("superseded revision retained authority")
	}
}
func TestOwnerDecisionCAS(t *testing.T) {
	s, o, b, in := goalFixture(t)
	ctx := context.Background()
	v, err := s.ProposeGoal(ctx, b, in)
	if err != nil {
		t.Fatal(err)
	}
	d := c.GoalDecisionInput{GoalID: v.Ref.GoalID, RevisionSHA256: v.Ref.RevisionSHA256, Action: "approve", RequestID: "decision"}
	if _, err = s.DecideGoal(ctx, b, d); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("agent approved")
	}
	if _, err = s.DecideGoal(ctx, o, d); err != nil {
		t.Fatal(err)
	}
	d.RequestID = "stale"
	if _, err = s.DecideGoal(ctx, o, d); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale CAS accepted")
	}
}
func TestGoalLifecycle(t *testing.T) {
	s, o, b, in := goalFixture(t)
	ctx := context.Background()
	v, err := s.ProposeGoal(ctx, b, in)
	if err != nil {
		t.Fatal(err)
	}
	seq := int64(0)
	for i, action := range []string{"approve", "pause", "resume", "complete", "reopen", "approve", "reject"} {
		a, e := s.DecideGoal(ctx, o, c.GoalDecisionInput{GoalID: v.Ref.GoalID, RevisionSHA256: v.Ref.RevisionSHA256, ExpectedDecisionSequence: seq, Action: action, RequestID: action + string(rune('a'+i))})
		if e != nil {
			t.Fatalf("%s: %v", action, e)
		}
		seq = a.DecisionSequence
		if a.Valid != (action == "approve" || action == "resume") {
			t.Fatalf("%s validity %+v", action, a)
		}
		if action == "complete" {
			_, e = s.DecideGoal(ctx, o, c.GoalDecisionInput{GoalID: v.Ref.GoalID, RevisionSHA256: v.Ref.RevisionSHA256, ExpectedDecisionSequence: seq, Action: "approve", RequestID: "implicit-reopen"})
			if !errors.Is(e, c.ErrConflict) {
				t.Fatal("approval implicitly reopened goal")
			}
		}
	}
}
func TestQADestinationMembership(t *testing.T) {
	s, _, b, in := goalFixture(t)
	in.QA = c.QAPolicy{Required: true, ActorID: "outsider", Environment: "test", EnvironmentRevision: "test-v1"}
	if _, err := s.ProposeGoal(context.Background(), b, in); err == nil {
		t.Fatal("unreadable QA destination accepted")
	}
}
