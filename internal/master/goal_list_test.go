package master

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"testing"
)

func TestApprovedGoalCatalogHasNoDiscussionDependency(t *testing.T) {
	s, o, b, in := goalFixture(t)
	ctx := context.Background()
	v, err := s.ProposeGoal(ctx, b, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecideGoal(ctx, o, c.GoalDecisionInput{GoalID: v.Ref.GoalID, RevisionSHA256: v.Ref.RevisionSHA256, ExpectedDecisionSequence: 0, Action: "approve", RequestID: "approve"}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ApprovedGoals(ctx, b, "coop", "")
	if err != nil || len(page.Goals) != 1 || !page.Goals[0].Valid {
		t.Fatalf("missing native catalog: %+v %v", page, err)
	}
	if _, err = s.ApprovedGoals(ctx, b, "another-customer", ""); err == nil {
		t.Fatal("engagement boundary ignored")
	}
}
