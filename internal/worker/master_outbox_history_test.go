package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

// Synthetic retained records only: no captured journals or credentials.
func TestInboxRetirementViewIsOperationLocal(t *testing.T) {
	cfg, _, _, _, _, _, _ := commsFixture(t)
	turn := MasterTurn{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Delivery: c.Delivery{ID: "retained"}, Phase: "completed"}
	raw, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalRecoveryRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := masterRecoverySnapshot{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Epoch: 1, Records: []c.RecoveryEvidence{{Kind: "inbox", RecordKey: "inbox/" + BodyHash(turn.Delivery.ID) + ".json", Data: canonical}}}
	raw, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(masterDir(cfg), "recovery", "accepted.json")
	if err := atomicPrivate(path, raw); err != nil {
		t.Fatal(err)
	}
	view := masterRecoveryView{cfg: cfg}
	if retired, err := view.retiredTurn(turn); err != nil || !retired {
		t.Fatalf("retirement missing: %v %v", retired, err)
	}
	// Detect repeated decoding without a timing-dependent performance test.
	if err := atomicPrivate(path, []byte(`{"Version":99}`)); err != nil {
		t.Fatal(err)
	}
	if retired, err := view.retiredTurn(turn); err != nil || !retired {
		t.Fatalf("operation reread its recovery evidence: %v %v", retired, err)
	}
	if _, err := retiredTurn(cfg, turn); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("next operation reused stale evidence: %v", err)
	}
}

func retainedOutboxHistory(t *testing.T, cfg Config, count int) int {
	t.Helper()
	snapshot := masterRecoverySnapshot{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Epoch: 1}
	dir := filepath.Join(masterDir(cfg), "outbox")
	if err := ensurePrivateDir(dir, true); err != nil {
		t.Fatal(err)
	}
	for i := range count {
		id := BodyHash(fmt.Sprintf("retained-reply-%d", i))
		in := masterOutbox{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Epoch: 1, Reply: c.PublishReply{RequestID: id, Body: strings.Repeat("x", 16000)}}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if err = writeWakeImmutable(filepath.Join(dir, id+".json"), raw); err != nil {
			t.Fatal(err)
		}
		canonical, err := canonicalRecoveryRaw(raw)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Records = append(snapshot.Records, c.RecoveryEvidence{Kind: "outbox", RecordKey: "outbox/" + id + ".json", Data: canonical})
	}
	raw := mustRecoveryJSON(snapshot)
	path := filepath.Join(masterDir(cfg), "recovery", "accepted.json")
	if err := ensurePrivateDir(filepath.Dir(path), true); err != nil {
		t.Fatal(err)
	}
	if err := atomicPrivate(path, raw); err != nil {
		t.Fatal(err)
	}
	return len(raw)
}

func TestFlushOutboxRetainedHistoryCost(t *testing.T) {
	cfg, _, _ := masterMigrationFixture(t)
	size := retainedOutboxHistory(t, cfg, 220)
	m := &MasterComms{Config: cfg, connection: masterConnectionState{Epoch: 1}}
	started := time.Now()
	if err := m.FlushOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Logf("retained=220 accepted_bytes=%d flush_producer_hold=%s", size, time.Since(started))
	started = time.Now()
	for range 100 {
		if err := m.FlushOutbox(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("indexed_idle_flush_mean=%s", time.Since(started)/100)
}

type gatedHistoryReply struct {
	MasterTransport
	entered chan struct{}
	resume  chan struct{}
}

func (g *gatedHistoryReply) PublishReply(ctx context.Context, in c.PublishReply) (c.MessageReceipt, error) {
	if in.RequestID == strings.Repeat("0", 64) {
		close(g.entered)
		select {
		case <-g.resume:
			return c.MessageReceipt{MasterID: "master", MessageID: "synthetic-history-gate", RestoreEpoch: in.RestoreEpoch}, nil
		case <-ctx.Done():
			return c.MessageReceipt{}, ctx.Err()
		}
	}
	return g.MasterTransport.PublishReply(ctx, in)
}

func TestQueueMasterReplyDuringFlushWithRetainedHistory(t *testing.T) {
	cfg, comms, store, owner, bird, thread, code := commsFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := store.Send(ctx, owner, c.SendMessage{ThreadID: thread.ID, Recipients: []string{bird.ID}, Body: "question", RequestID: "history-contention"}); err != nil {
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
	size := retainedOutboxHistory(t, cfg, 220)
	// Seeded historical files represent a cold start, not live producer writes.
	comms.outboxIndexed = false
	// This synthetic pending record sorts first. Its transport barrier ensures
	// the reply is queued while flushing owns the producer, before history work.
	id := strings.Repeat("0", 64)
	gateRecord := masterOutbox{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Epoch: comms.connection.Epoch, Reply: c.PublishReply{RequestID: id, RestoreEpoch: comms.connection.Epoch}}
	if err = writeWakeImmutable(filepath.Join(masterDir(cfg), "outbox", id+".json"), mustRecoveryJSON(gateRecord)); err != nil {
		t.Fatal(err)
	}
	gate := &gatedHistoryReply{MasterTransport: comms.Client, entered: make(chan struct{}), resume: make(chan struct{})}
	comms.Client = gate
	flushed := make(chan error, 1)
	go func() { flushed <- comms.FlushOutbox(ctx) }()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("flush did not reach transport barrier")
	}
	queued := make(chan error, 1)
	started := time.Now()
	go func() { queued <- QueueMasterReply(cfg, turn.Delivery.ID, "Prepared during history flush.") }()
	select {
	case err = <-queued:
		t.Fatalf("reply did not wait for producer: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(gate.resume)
	queueErr := <-queued
	t.Logf("accepted_bytes=%d concurrent_reply_wait=%s", size, time.Since(started))
	if err = <-flushed; err != nil {
		t.Fatal(err)
	}
	if queueErr != nil {
		t.Fatalf("reply failed during retained-history flush: %v", queueErr)
	}
	for range 2 {
		if err = comms.FlushOutbox(ctx); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Messages(ctx, owner, thread.ID, 0)
	if err != nil || len(page.Messages) != 2 || code.calls != 1 {
		t.Fatalf("reply missing, duplicated or model replayed: messages=%d calls=%d err=%v", len(page.Messages), code.calls, err)
	}
	if err = QueueMasterReply(cfg, turn.Delivery.ID, "Conflicting answer."); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("immutable reply changed: %v", err)
	}
	// Restart rebuilds the derived pending index from fresh recovery evidence.
	if err = atomicPrivate(filepath.Join(masterDir(cfg), "recovery", "accepted.json"), []byte(`{"Version":99}`)); err != nil {
		t.Fatal(err)
	}
	restarted := &MasterComms{Config: cfg, Client: comms.Client, connection: comms.connection}
	if err = restarted.FlushOutbox(ctx); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("changed recovery evidence ignored: %v", err)
	}
}

func TestMasterRecoveryViewLoadsOnceWithinOperation(t *testing.T) {
	cfg, _, _ := masterMigrationFixture(t)
	retainedOutboxHistory(t, cfg, 2)
	release, err := lockMasterProducer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	view := masterRecoveryView{cfg: cfg}
	dir := filepath.Join(masterDir(cfg), "outbox")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := readPrivate(filepath.Join(dir, entry.Name()), c.MaxJSON)
		if err != nil {
			t.Fatal(err)
		}
		if retired, err := view.retired("outbox", entry.Name(), raw); err != nil || !retired {
			t.Fatalf("retirement lost: %v %v", retired, err)
		}
		if !view.acceptedLoaded || !view.adoptionLoaded || len(view.accepted.Records) != 2 {
			t.Fatal("evidence was not retained in operation-local view")
		}
		// Deliberately poison the fixture behind the locked view to detect a
		// second decode deterministically. Real writers must own this lock.
		if err = atomicPrivate(filepath.Join(masterDir(cfg), "recovery", "accepted.json"), []byte(`{"Version":99}`)); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := readPrivate(filepath.Join(dir, entries[0].Name()), c.MaxJSON)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&masterRecoveryView{cfg: cfg}).retired("outbox", entries[0].Name(), raw); !errors.Is(err, c.ErrConflict) {
		t.Fatalf("new view reused old evidence: %v", err)
	}
}
