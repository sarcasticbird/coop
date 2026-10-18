package master

import (
	"context"
	"errors"
	"fmt"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestPendingCapacityPreservesExisting(t *testing.T) {
	s, owner, bot, th := storeFixture(t)
	ctx := context.Background()
	for i := range c.MaxPending {
		if _, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Recipients: []string{bot.ID}, Body: "queued", RequestID: fmt.Sprint("capacity-", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Recipients: []string{bot.ID}, Body: "overflow", RequestID: "overflow"}); !errors.Is(err, c.ErrCapacity) {
		t.Fatalf("capacity not enforced: %v", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM deliveries").Scan(&count); err != nil || count != c.MaxPending {
		t.Fatalf("discarded deliveries: %d %v", count, err)
	}
}
func TestRetainedMessageCapacity(t *testing.T) {
	s, owner, _, th := storeFixture(t)
	ctx := context.Background()
	_, err := s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<?) INSERT INTO messages(id,thread_id,author_id,body) SELECT 'retained-'||x,?,?, '{}' FROM n`, c.MaxMessages, th.ID, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Body: "overflow", RequestID: "overflow"}); !errors.Is(err, c.ErrCapacity) {
		t.Fatalf("retained capacity not enforced: %v", err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM messages").Scan(&count); err != nil || count != c.MaxMessages {
		t.Fatalf("deleted history: %d %v", count, err)
	}
}
