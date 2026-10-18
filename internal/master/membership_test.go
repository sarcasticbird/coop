package master

import (
	"context"
	"errors"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"testing"
)

func TestMembershipRevocationCancelsUnstartedAndDeniesHistory(t *testing.T) {
	s, o, b, th := storeFixture(t)
	ctx := context.Background()
	r, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "private", RequestID: "revoke"})
	if err != nil {
		t.Fatal(err)
	}
	in := c.SetMembers{ThreadID: th.ID, Members: []string{o.ID}, ExpectedMembers: th.Members, RequestID: "membership"}
	if _, err = s.SetMembers(ctx, b, in); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("agent changed membership", err)
	}
	if _, err = s.SetMembers(ctx, o, in); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Messages(ctx, b, th.ID, 0); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("revoked member read history", err)
	}
	status, err := s.MessageStatus(ctx, o, r.MessageID)
	if err != nil || status.Deliveries[0].State != "cancelled" {
		t.Fatalf("unstarted revocation %+v %v", status, err)
	}
	in.RequestID = "stale"
	if _, err = s.SetMembers(ctx, o, in); !errors.Is(err, c.ErrConflict) {
		t.Fatal("stale membership CAS accepted", err)
	}
}

func TestPendingDoesNotExposeRevokedEngagement(t *testing.T) {
	s, o, b, th := storeFixture(t)
	ctx := context.Background()
	if _, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "private", RequestID: "old-engagement"}); err != nil {
		t.Fatal(err)
	}
	b.Engagements = []string{"other"}
	if err := s.Configure(ctx, []c.Principal{b}, nil); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending(ctx, b, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 0 {
		t.Fatal("revoked engagement exposed delivery metadata", err)
	}
}
