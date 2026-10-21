package master

import (
	"errors"
	"reflect"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestSendStaleMembership(t *testing.T) {
	for _, empty := range []bool{false, true} {
		name := "stale"
		if empty {
			name = "explicit-empty"
		}
		t.Run(name, func(t *testing.T) {
			s, owner, bot, th := storeFixture(t)
			ctx := t.Context()
			if _, err := s.SetMembers(ctx, owner, c.SetMembers{
				ThreadID: th.ID, ExpectedMembers: th.Members,
				Members: []string{owner.ID, bot.ID}, RequestID: "change-members",
			}); err != nil {
				t.Fatal(err)
			}
			expected := th.Members
			if empty {
				expected = []string{}
			}
			_, err := s.Send(ctx, owner, c.SendMessage{
				ThreadID: th.ID, ExpectedMembers: expected, Recipients: []string{bot.ID},
				Body: "synthetic stale send", RequestID: "stale-send",
			})
			if !errors.Is(err, c.ErrConflict) {
				t.Fatalf("stale membership: got %v, want conflict", err)
			}
			var messages, deliveries int
			if err := s.db.QueryRow("SELECT (SELECT count(*) FROM messages), (SELECT count(*) FROM deliveries)").Scan(&messages, &deliveries); err != nil {
				t.Fatal(err)
			}
			if messages != 0 || deliveries != 0 {
				t.Fatalf("stale send persisted messages/deliveries: %d/%d", messages, deliveries)
			}
		})
	}
}

func TestSendMembershipCheckedBeforeReplay(t *testing.T) {
	s, owner, bot, th := storeFixture(t)
	ctx := t.Context()
	// Reordering and duplicates must normalize to the original membership.
	in := c.SendMessage{
		ThreadID: th.ID, ExpectedMembers: []string{owner.ID, "agent-two", bot.ID, owner.ID},
		Recipients: []string{bot.ID}, Body: "synthetic guarded send", RequestID: "guarded-send",
	}
	original, err := s.Send(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if original.MessageID == "" || len(original.Deliveries) != 1 {
		t.Fatalf("missing committed message or delivery: %+v", original)
	}
	changed, err := s.SetMembers(ctx, owner, c.SetMembers{
		ThreadID: th.ID, ExpectedMembers: th.Members,
		Members: []string{owner.ID, bot.ID}, RequestID: "change-members",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, owner, in); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("replay with stale membership: got %v, want conflict", err)
	}
	var messages, deliveries int
	if err := s.db.QueryRow("SELECT (SELECT count(*) FROM messages), (SELECT count(*) FROM deliveries)").Scan(&messages, &deliveries); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || deliveries != 1 {
		t.Fatalf("blocked replay changed messages/deliveries: %d/%d", messages, deliveries)
	}
	if _, err := s.SetMembers(ctx, owner, c.SetMembers{
		ThreadID: th.ID, ExpectedMembers: changed.Members,
		Members: th.Members, RequestID: "restore-members",
	}); err != nil {
		t.Fatal(err)
	}
	replayed, err := s.Send(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed, original) {
		t.Fatalf("restored membership changed receipt: got %+v, want %+v", replayed, original)
	}
	if err := s.db.QueryRow("SELECT (SELECT count(*) FROM messages), (SELECT count(*) FROM deliveries)").Scan(&messages, &deliveries); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || deliveries != 1 {
		t.Fatalf("restored replay duplicated messages/deliveries: %d/%d", messages, deliveries)
	}
}

func TestSendLegacyNilMembership(t *testing.T) {
	s, owner, bot, th := storeFixture(t)
	ctx := t.Context()
	in := c.SendMessage{
		ThreadID: th.ID, Recipients: []string{bot.ID},
		Body: "synthetic legacy send", RequestID: "legacy-send",
	}
	original, err := s.Send(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if original.MessageID == "" || len(original.Deliveries) != 1 {
		t.Fatalf("missing committed message or delivery: %+v", original)
	}
	if _, err := s.SetMembers(ctx, owner, c.SetMembers{
		ThreadID: th.ID, ExpectedMembers: th.Members,
		Members: []string{owner.ID, bot.ID}, RequestID: "change-members",
	}); err != nil {
		t.Fatal(err)
	}
	replayed, err := s.Send(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed, original) {
		t.Fatalf("legacy replay changed receipt: got %+v, want %+v", replayed, original)
	}
	in.RequestID = "legacy-send-after-change"
	fresh, err := s.Send(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.MessageID == original.MessageID || len(fresh.Deliveries) != 1 {
		t.Fatalf("legacy send after membership change: %+v", fresh)
	}
	var messages, deliveries int
	if err := s.db.QueryRow("SELECT (SELECT count(*) FROM messages), (SELECT count(*) FROM deliveries)").Scan(&messages, &deliveries); err != nil {
		t.Fatal(err)
	}
	if messages != 2 || deliveries != 2 {
		t.Fatalf("legacy messages/deliveries: got %d/%d, want 2/2", messages, deliveries)
	}
}
