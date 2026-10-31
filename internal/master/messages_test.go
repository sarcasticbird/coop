package master

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func storeFixture(t *testing.T) (*Store, c.Principal, c.Principal, c.Thread) {
	t.Helper()
	s, err := OpenStore(context.Background(), StoreConfig{Path: filepath.Join(t.TempDir(), "master.sqlite"), MasterID: "test-master"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	owner := c.Principal{ID: "owner", Kind: "owner", Alias: "owner", Engagements: []string{"coop"}, Capabilities: []string{"read", "send", "manage", "decide"}}
	bot := c.Principal{ID: "agent-one", Kind: "agent", Alias: "one", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}, GitHubBotID: 10}
	other := c.Principal{ID: "agent-two", Kind: "agent", Alias: "two", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}, GitHubBotID: 11}
	if err := s.Configure(context.Background(), []c.Principal{owner, bot, other}, []c.Placement{{AgentID: bot.ID, HostID: "host", InstanceID: "instance", Generation: 1}, {AgentID: other.ID, HostID: "host", InstanceID: "instance-two", Generation: 1}}); err != nil {
		t.Fatal(err)
	}
	thread, err := s.CreateThread(context.Background(), owner, c.CreateThread{Title: "Coop work", Engagement: "coop", Members: []string{owner.ID, bot.ID, other.ID}, RequestID: "create-thread"})
	if err != nil {
		t.Fatal(err)
	}
	return s, owner, bot, thread
}

func TestSendAtomicIdempotency(t *testing.T) {
	s, owner, bot, th := storeFixture(t)
	ctx := context.Background()
	in := c.SendMessage{ThreadID: th.ID, Recipients: []string{bot.ID}, Body: "hello", RequestID: "request-1"}
	a, err := s.Send(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Send(ctx, owner, in)
	if err != nil || a.MessageID != b.MessageID {
		t.Fatalf("retry changed identity: %+v %v", b, err)
	}
	in.Body = "changed"
	if _, err = s.Send(ctx, owner, in); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("changed input accepted: %v", err)
	}
	var messages, deliveries int
	if err = s.db.QueryRow("SELECT count(*) FROM messages").Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM deliveries").Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || deliveries != 1 {
		t.Fatalf("duplicates: %d/%d", messages, deliveries)
	}
}

func TestReplyAtomicLinkage(t *testing.T) {
	s, owner, bot, th := storeFixture(t)
	ctx := context.Background()
	r, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Recipients: []string{bot.ID}, Body: "question", RequestID: "question"})
	if err != nil {
		t.Fatal(err)
	}
	intent := &c.NativeIntent{SessionID: "session", MessageID: "native-message"}
	u := c.DeliveryUpdate{RestoreEpoch: 1, DeliveryID: r.Deliveries[0].ID, InstanceID: "instance", Generation: 1, State: "received"}
	if err = s.SetDelivery(ctx, bot, u); err != nil {
		t.Fatal(err)
	}
	u.State = "running"
	u.Intent = intent
	if err = s.SetDelivery(ctx, bot, u); err != nil {
		t.Fatal(err)
	}
	in := c.PublishReply{RestoreEpoch: 1, DeliveryID: u.DeliveryID, InstanceID: "instance", Generation: 1, Intent: *intent, Body: "substantive answer", RequestID: "reply-one"}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { result, e := s.PublishReply(ctx, bot, in); ids <- result.MessageID; errs <- e })
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate reply")
		}
	}
	status, err := s.MessageStatus(ctx, owner, r.MessageID)
	if err != nil || status.Deliveries[0].State != "replied" {
		t.Fatalf("unlinked reply: %+v %v", status, err)
	}
	in.RequestID = "different-request"
	if _, err = s.PublishReply(ctx, bot, in); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("second reply accepted: %v", err)
	}
}

func TestExplicitHandoffsAndQuietReplies(t *testing.T) {
	s, owner, bot, th := storeFixture(t)
	ctx := context.Background()
	r, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Recipients: []string{bot.ID}, Body: "start", RequestID: "start"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(p.Deliveries) != 1 {
		t.Fatalf("pending %+v %v", p, err)
	}
	other, err := s.Principal(ctx, "agent-two")
	if err != nil {
		t.Fatal(err)
	}
	watch, err := s.Pending(ctx, other, c.PendingQuery{})
	if err != nil || len(watch.Deliveries) != 0 {
		t.Fatal("watcher awakened")
	}
	for hop := 1; hop <= 6; hop++ {
		actor, target := bot, other
		if hop%2 == 0 {
			actor, target = other, bot
		}
		r, err = s.Send(ctx, actor, c.SendMessage{ThreadID: th.ID, ParentMessageID: r.MessageID, Recipients: []string{target.ID, actor.ID}, Body: "peer question", RequestID: strings.Repeat("x", hop)})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Deliveries) != 1 {
			t.Fatalf("self delivery %+v", r)
		}
		if r.Deliveries[0].State != "queued" {
			t.Fatal("explicit handoff suppressed by conversation depth")
		}
	}
	reply, err := s.Send(ctx, bot, c.SendMessage{ThreadID: th.ID, ParentMessageID: r.MessageID, Body: "default reply", RequestID: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.Deliveries) != 0 {
		t.Fatal("agent result triggered a reply loop")
	}
	human, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, ParentMessageID: reply.MessageID, Body: "next question", RequestID: "human-followup"})
	if err != nil || len(human.Deliveries) != 1 || human.Deliveries[0].AgentID != bot.ID {
		t.Fatalf("human follow-up not delivered: %+v %v", human, err)
	}
}

func TestCapacityAndFailedCommit(t *testing.T) {
	s, owner, bot, th := storeFixture(t)
	ctx := context.Background()
	in := c.SendMessage{ThreadID: th.ID, Recipients: []string{bot.ID}, Body: strings.Repeat("x", 32<<10), RequestID: "at-limit"}
	if _, err := s.Send(ctx, owner, in); err != nil {
		t.Fatal(err)
	}
	in.Body += "x"
	in.RequestID = "too-large"
	if _, err := s.Send(ctx, owner, in); !errors.Is(err, c.ErrSize) {
		t.Fatalf("oversize %v", err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER fail_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'disk full'); END;"); err != nil {
		t.Fatal(err)
	}
	in.Body = "message"
	in.RequestID = "failed-commit"
	if _, err := s.Send(ctx, owner, in); err == nil {
		t.Fatal("failure ignored")
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM messages").Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial commit: %d %v", count, err)
	}
}
