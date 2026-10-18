package master

import (
	"context"
	"errors"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestPrincipalAliasCannotShadowIdentity(t *testing.T) {
	s, owner, bot, _ := storeFixture(t)
	owner.Alias = bot.ID
	if err := s.Configure(context.Background(), []c.Principal{owner}, nil); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("alias shadowed agent identity: %v", err)
	}
}
func TestGoalProposalRetryPreservesOriginalInput(t *testing.T) {
	s, _, bot, in := goalFixture(t)
	ctx := context.Background()
	in.Repositories = []c.RepositoryScope{{Name: "example/z", BaseBranches: []string{"main"}}, {Name: "example/a", BaseBranches: []string{"main"}}}
	a, err := s.ProposeGoal(ctx, bot, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Repositories = []c.RepositoryScope{{Name: "example/z", BaseBranches: []string{"main"}}, {Name: "example/a", BaseBranches: []string{"main"}}}
	b, err := s.ProposeGoal(ctx, bot, in)
	if err != nil || a.Ref != b.Ref {
		t.Fatalf("same proposal request conflicted: %v", err)
	}
}

func TestGoalActorsRequireStableIDs(t *testing.T) {
	s, _, b, in := goalFixture(t)
	in.AuthorID = b.Alias
	if _, err := s.ProposeGoal(context.Background(), b, in); err == nil {
		t.Fatal("mutable alias became approval identity")
	}
}

func TestPingSurvivesAliasRename(t *testing.T) {
	s, o, b, _ := storeFixture(t)
	ctx := context.Background()
	first, err := s.Ping(ctx, o, c.Ping{AgentID: b.Alias, Engagement: "coop", Body: "hello", RequestID: "before-rename"})
	if err != nil {
		t.Fatal(err)
	}
	b.Alias = "renamed"
	if err = s.Configure(ctx, []c.Principal{b}, nil); err != nil {
		t.Fatal(err)
	}
	second, err := s.Ping(ctx, o, c.Ping{AgentID: b.Alias, Engagement: "coop", Body: "hello again", RequestID: "after-rename"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Message(ctx, o, first.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	z, err := s.Message(ctx, o, second.MessageID)
	if err != nil || a.ThreadID != z.ThreadID {
		t.Fatal("alias rename split or blocked direct thread", err)
	}
}
