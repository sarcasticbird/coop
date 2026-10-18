package master

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"strings"
	"testing"
)

func TestArtifactIndexIsDiscoveryWithoutApprovalRights(t *testing.T) {
	s, o, b, in := goalFixture(t)
	ctx := context.Background()
	b.Capabilities = append(b.Capabilities, "plan")
	if err := s.Store.Configure(ctx, []c.Principal{b}, nil); err != nil {
		t.Fatal(err)
	}
	v, err := s.ProposeGoal(ctx, b, in)
	if err != nil {
		t.Fatal(err)
	}
	link := c.ArtifactLink{Goal: v.Ref, Kind: "task", URL: "https://github.com/example/coop/issues/1", AssignmentSHA: strings.Repeat("a", 64), RequestID: "register"}
	if _, err = s.RegisterArtifact(ctx, b, link); err == nil {
		t.Fatal("unapproved work registered")
	}
	if _, err = s.DecideGoal(ctx, o, c.GoalDecisionInput{GoalID: v.Ref.GoalID, RevisionSHA256: v.Ref.RevisionSHA256, Action: "approve", RequestID: "approve"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterArtifact(ctx, b, link); err != nil {
		t.Fatal(err)
	}
	page, err := s.Artifacts(ctx, b, c.ArtifactQuery{Goal: v.Ref})
	if err != nil || len(page.Links) != 1 {
		t.Fatalf("artifact index %+v %v", page, err)
	}
	link.RequestID = "outside"
	link.URL = "https://github.com/customer/unapproved/issues/1"
	if _, err = s.RegisterArtifact(ctx, b, link); err == nil {
		t.Fatal("artifact escaped approved repository")
	}
}
