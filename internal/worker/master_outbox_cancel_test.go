package worker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type cancellationProofTransport struct {
	MasterTransport
	change     func(*c.MessageReceipt)
	err        error
	publishErr error
}

func (cl cancellationProofTransport) PublishReply(ctx context.Context, in c.PublishReply) (c.MessageReceipt, error) {
	if cl.publishErr != nil {
		return c.MessageReceipt{}, cl.publishErr
	}
	return cl.MasterTransport.PublishReply(ctx, in)
}

func (cl cancellationProofTransport) MessageStatus(ctx context.Context, id string) (c.MessageReceipt, error) {
	if cl.err != nil {
		return c.MessageReceipt{}, cl.err
	}
	v, err := cl.MasterTransport.MessageStatus(ctx, id)
	if err == nil && cl.change != nil {
		cl.change(&v)
	}
	return v, err
}

func TestCanceledDurableReplyDoesNotBlockLaterConversation(t *testing.T) {
	cfg, comms, store, owner, bird, thread, code := commsFixture(t)
	ctx := t.Context()
	if _, err := store.Send(ctx, owner, c.SendMessage{ThreadID: thread.ID, Recipients: []string{bird.ID}, Body: "first question", RequestID: "canceled-outbox-first"}); err != nil {
		t.Fatal(err)
	}
	if err := comms.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := comms.Next(ctx)
	if err != nil || turn == nil {
		t.Fatal("missing first turn", err)
	}
	if err = comms.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err = QueueMasterReply(cfg, turn.Delivery.ID, "preserve this unsent reply"); err != nil {
		t.Fatal(err)
	}
	id := BodyHash("master-reply\x00" + turn.MasterID + "\x00" + turn.Delivery.ID + "\x00" + turn.Job.MessageID)
	path := filepath.Join(masterDir(cfg), "outbox", id+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	code.running = false
	resolved, err := ResolveMasterConversation(ctx, cfg, comms.Client, code, turn.Delivery.ID, turn.Job.SessionID, turn.Job.MessageID, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	if err = comms.FlushOutbox(ctx); err != nil {
		t.Fatal("explicitly canceled reply blocked the outbox", err)
	}

	original := comms.Client
	for _, test := range []struct {
		name   string
		change func(*c.MessageReceipt)
		err    error
	}{
		{name: "epoch", change: func(v *c.MessageReceipt) { v.RestoreEpoch++ }},
		{name: "master", change: func(v *c.MessageReceipt) { v.MasterID = "other" }},
		{name: "message", change: func(v *c.MessageReceipt) { v.MessageID = "other" }},
		{name: "delivery", change: func(v *c.MessageReceipt) { v.Deliveries[0].ID = "other" }},
		{name: "agent", change: func(v *c.MessageReceipt) { v.Deliveries[0].AgentID = "other" }},
		{name: "intent", change: func(v *c.MessageReceipt) { v.Deliveries[0].Intent.MessageID = "other" }},
		{name: "missing intent", change: func(v *c.MessageReceipt) { v.Deliveries[0].Intent = nil }},
		{name: "still running", change: func(v *c.MessageReceipt) { v.Deliveries[0].State = "running" }},
		{name: "unavailable", err: c.ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			comms.Client = cancellationProofTransport{MasterTransport: original, change: test.change, err: test.err}
			if err := comms.FlushOutbox(ctx); !errors.Is(err, c.ErrConflict) {
				t.Fatalf("unverified cancellation bypassed publication failure: %v", err)
			}
		})
	}
	comms.Client = cancellationProofTransport{MasterTransport: original, publishErr: c.ErrUnavailable}
	if err = comms.FlushOutbox(ctx); !errors.Is(err, c.ErrUnavailable) {
		t.Fatal("transport failure was mistaken for cancellation", err)
	}
	comms.Client = original
	resolved.Phase = "active"
	if err = saveMasterTurn(cfg, &resolved); err != nil {
		t.Fatal(err)
	}
	if err = comms.FlushOutbox(ctx); !errors.Is(err, c.ErrConflict) {
		t.Fatal("master cancellation bypassed incomplete local recovery", err)
	}
	resolved.Phase = "cancelled"
	if err = saveMasterTurn(cfg, &resolved); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Send(ctx, owner, c.SendMessage{ThreadID: thread.ID, Recipients: []string{bird.ID}, Body: "next question", RequestID: "canceled-outbox-next"}); err != nil {
		t.Fatal(err)
	}
	if err = comms.Tick(ctx); err != nil {
		t.Fatal("new conversation receipt was blocked", err)
	}
	code.running = true
	next, err := comms.Next(ctx)
	if err != nil || next == nil || next.Delivery.ID == turn.Delivery.ID {
		t.Fatal("new conversation did not progress", err)
	}
	if err = comms.Advance(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err = QueueMasterReply(cfg, next.Delivery.ID, "new answer"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = comms.FlushOutbox(ctx); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Messages(ctx, owner, thread.ID, 0)
	if err != nil || len(page.Messages) != 3 || code.calls != 2 {
		t.Fatalf("reply/model was replayed or new reply is missing: messages=%d calls=%d error=%v", len(page.Messages), code.calls, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("canceled outbox evidence changed", err)
	}
	if _, err = os.Stat(filepath.Join(masterDir(cfg), "published", id+".json")); !os.IsNotExist(err) {
		t.Fatal("canceled reply received a fabricated publication receipt", err)
	}
}
