package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestQueueMasterReplyRetriesProducerBusy(t *testing.T) {
	cfg, comms, store, owner, bird, thread, code := commsFixture(t)
	ctx := context.Background()
	if _, err := store.Send(ctx, owner, c.SendMessage{ThreadID: thread.ID, Recipients: []string{bird.ID}, Body: "question", RequestID: "reply-lock-contention"}); err != nil {
		t.Fatal(err)
	}
	if err := comms.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := comms.Next(ctx)
	if err != nil || turn == nil {
		t.Fatalf("missing turn: %v", err)
	}
	if err = comms.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	release, err := lockMasterProducer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	const body = "An answer prepared before producer contention."
	done := make(chan error, 1)
	go func() { done <- QueueMasterReply(cfg, turn.Delivery.ID, body) }()
	select {
	case err := <-done:
		t.Fatalf("reply returned before the busy producer released: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if entries, err := os.ReadDir(filepath.Join(masterDir(cfg), "outbox")); !os.IsNotExist(err) && (err != nil || len(entries) != 0) {
		t.Fatalf("outbox mutated while producer was busy: %v, %v", entries, err)
	}
	release()
	release = nil
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reply did not resume after producer released")
	}

	hash := sha256.Sum256([]byte("master-reply\x00" + turn.MasterID + "\x00" + turn.Delivery.ID + "\x00" + turn.Job.MessageID))
	id := hex.EncodeToString(hash[:])
	dir := filepath.Join(masterDir(cfg), "outbox")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != id+".json" {
		t.Fatalf("wrong reply identity: %v, %v", entries, err)
	}
	path := filepath.Join(dir, id+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got masterOutbox
	if err = json.Unmarshal(before, &got); err != nil {
		t.Fatal(err)
	}
	want := masterOutbox{Version: 1, MasterID: turn.MasterID, InstanceID: turn.InstanceID, Epoch: turn.RestoreEpoch, Reply: c.PublishReply{RestoreEpoch: turn.RestoreEpoch, DeliveryID: turn.Delivery.ID, InstanceID: turn.InstanceID, Generation: turn.Generation, Intent: c.NativeIntent{SessionID: turn.Job.SessionID, MessageID: turn.Job.MessageID}, Body: body, RequestID: id}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reply identity or body changed: got %+v, want %+v", got, want)
	}
	if err = QueueMasterReply(cfg, turn.Delivery.ID, body); err != nil {
		t.Fatal("identical reply retry failed", err)
	}
	started := time.Now()
	if err = QueueMasterReply(cfg, turn.Delivery.ID, "A conflicting answer."); !errors.Is(err, c.ErrConflict) || errors.Is(err, errNativeProducerBusy) {
		t.Fatalf("conflicting reply error changed: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("retried a reply conflict that was not producer contention")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("retry changed the durable request: %v", err)
	}
	for range 2 {
		if err = comms.FlushOutbox(ctx); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Messages(ctx, owner, thread.ID, 0)
	if err != nil || len(page.Messages) != 2 {
		t.Fatalf("reply missing or duplicated: %+v, %v", page, err)
	}
	if code.calls != 1 {
		t.Fatalf("native turn replayed: %d submissions", code.calls)
	}
}

func TestQueueMasterReplyProducerBusyTimeout(t *testing.T) {
	cfg, _, _ := masterMigrationFixture(t)
	release, err := lockMasterProducer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	started := time.Now()
	err = QueueMasterReply(cfg, "delivery", "Prepared answer.")
	elapsed := time.Since(started)
	if !errors.Is(err, errNativeProducerBusy) || !errors.Is(err, c.ErrConflict) {
		t.Fatalf("exhausted contention error changed: %v", err)
	}
	if elapsed < 4*time.Second || elapsed > 8*time.Second {
		t.Fatalf("producer wait was not bounded near five seconds: %v", elapsed)
	}
	if _, err = os.Stat(filepath.Join(masterDir(cfg), "outbox")); !os.IsNotExist(err) {
		t.Fatalf("timed out reply mutated the outbox: %v", err)
	}
}

func TestQueueMasterReplyDoesNotRetryProducerIOError(t *testing.T) {
	cfg, _, _ := masterMigrationFixture(t)
	dir := filepath.Join(masterDir(cfg), "actions")
	if err := ensurePrivateDir(dir, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "producer.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err := QueueMasterReply(cfg, "delivery", "Prepared answer.")
	if err == nil || errors.Is(err, errNativeProducerBusy) {
		t.Fatalf("producer IO error changed: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("retried a producer IO error")
	}
}

func TestFlushOutboxRemainsNonblockingWithBusyProducer(t *testing.T) {
	cfg, _, _ := masterMigrationFixture(t)
	release, err := lockMasterProducer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	comms := &MasterComms{Config: cfg}
	started := time.Now()
	if err = comms.FlushOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("outbox flush waited for the busy producer")
	}
}
