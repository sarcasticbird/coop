package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func indexedTurn(cfg Config, id, phase string) MasterTurn {
	return MasterTurn{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, RestoreEpoch: 1, Delivery: c.Delivery{ID: id, MessageID: "message-" + id}, Message: c.Message{ID: "message-" + id, Body: "body"}, BodySHA: BodyHash("body"), Phase: phase}
}

func TestInboxIndexSkipsHistoryAndTracksNewWork(t *testing.T) {
	cfg, m, _, _, _, _, _ := commsFixture(t)
	old := indexedTurn(cfg, "old", "completed")
	if err := saveMasterTurn(cfg, &old); err != nil {
		t.Fatal(err)
	}
	if active, err := m.Active(); err != nil || active != nil {
		t.Fatalf("terminal turn active: %+v %v", active, err)
	}
	// Cold scans validate history; steady-state scheduling must not reread it.
	if err := atomicPrivate(masterRecordPath(cfg, old.Delivery.ID), []byte(`invalid`)); err != nil {
		t.Fatal(err)
	}
	current := indexedTurn(cfg, "new", "prepared")
	if err := saveMasterTurn(cfg, &current); err != nil {
		t.Fatal(err)
	}
	if active, err := m.Active(); err != nil || active == nil || active.Delivery.ID != current.Delivery.ID {
		t.Fatalf("new indexed work missing: %+v %v", active, err)
	}
	current.Phase = "completed"
	if err := saveMasterTurn(cfg, &current); err != nil {
		t.Fatal(err)
	}
	if active, err := m.Active(); err != nil || active != nil {
		t.Fatalf("completed work retained: %+v %v", active, err)
	}
	if _, err := (&MasterComms{Config: cfg}).Active(); err == nil {
		t.Fatal("restart failed to validate canonical history")
	}
}

func TestInboxIndexRebuildRecoversMissingAndStaleHints(t *testing.T) {
	cfg, m, _, _, _, _, _ := commsFixture(t)
	turn := indexedTurn(cfg, "recover", "prepared")
	if err := saveMasterTurn(cfg, &turn); err != nil {
		t.Fatal(err)
	}
	if err := clearMasterIndex(cfg, "inbox"); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after a hint was written, before its canonical record.
	if err := markMasterPending(cfg, "inbox", BodyHash("orphan")+".json", "orphan"); err != nil {
		t.Fatal(err)
	}
	active, err := m.Active()
	if err != nil || active == nil || active.Delivery.ID != turn.Delivery.ID {
		t.Fatalf("rebuild lost canonical active turn: %+v %v", active, err)
	}
	// Simulate a terminal write committed before removal of its hint.
	turn.Phase = "completed"
	if err := atomicPrivate(masterRecordPath(cfg, turn.Delivery.ID), mustRecoveryJSON(turn)); err != nil {
		t.Fatal(err)
	}
	if active, err := m.Active(); err != nil || active != nil {
		t.Fatalf("stale hint replayed a terminal turn: %+v %v", active, err)
	}
}

func TestOutboxIndexSkipsSettledHistory(t *testing.T) {
	cfg, _, _ := masterMigrationFixture(t)
	retainedOutboxHistory(t, cfg, 20)
	m := &MasterComms{Config: cfg, connection: masterConnectionState{Epoch: 1}}
	if err := m.FlushOutbox(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(masterDir(cfg), "outbox"))
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicPrivate(filepath.Join(masterDir(cfg), "outbox", entries[0].Name()), []byte(`invalid`)); err != nil {
		t.Fatal(err)
	}
	if err := m.FlushOutbox(t.Context()); err != nil {
		t.Fatalf("idle flush reread settled history: %v", err)
	}
	restarted := &MasterComms{Config: cfg, connection: m.connection}
	if err := restarted.FlushOutbox(t.Context()); err == nil {
		t.Fatal("restart skipped canonical history validation")
	}
}

func TestNativeActionWaitsForProducerBeforeRevalidating(t *testing.T) {
	cfg, master, catalog, _ := dispatchFixture(t)
	v := NativeCandidates(cfg, catalog.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: 1, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "active"}
	if err := saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	release, err := lockMasterProducer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	master.authority.State = "paused"
	done := make(chan error, 1)
	go func() {
		_, err := RunNativeAction(ctx, cfg, master, NewGitHubClient(nil), x.Job.ID, NativeAction{Operation: "publish-pr", RequestID: "contended"})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("transient contention rejected action: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := os.Stat(nativeActionPath(cfg, x.Job.ID, "contended")); !os.IsNotExist(err) {
		t.Fatalf("action wrote before obtaining producer: %v", err)
	}
	release()
	if err := <-done; !errors.Is(err, c.ErrForbidden) {
		t.Fatalf("authority was not revalidated after contention: %v", err)
	}
}

func TestProducerWaitHonorsCancellation(t *testing.T) {
	cfg, _, _ := masterMigrationFixture(t)
	release, err := lockMasterProducer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := waitMasterProducer(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled action waited for producer: %v", err)
	}
}

func TestInterruptedHelperPreservesLiveOutbox(t *testing.T) {
	cfg, live, store, owner, bird, thread, _ := commsFixture(t)
	ctx := t.Context()
	if _, err := store.Send(ctx, owner, c.SendMessage{ThreadID: thread.ID, Recipients: []string{bird.ID}, Body: "question", RequestID: "helper-rebuild"}); err != nil {
		t.Fatal(err)
	}
	if err := live.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := live.Next(ctx)
	if err != nil || turn == nil {
		t.Fatalf("missing turn: %v", err)
	}
	if err := live.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err := QueueMasterReply(cfg, turn.Delivery.ID, "answer"); err != nil {
		t.Fatal(err)
	}
	// A helper is interrupted at the first transport call. The live worker
	// must still discover every reply, including one beyond that call.
	id := strings.Repeat("0", 64)
	record := masterOutbox{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Epoch: live.connection.Epoch, Reply: c.PublishReply{RequestID: id, RestoreEpoch: live.connection.Epoch}}
	if err := writeWakeImmutable(filepath.Join(masterDir(cfg), "outbox", id+".json"), mustRecoveryJSON(record)); err != nil {
		t.Fatal(err)
	}
	gate := &gatedHistoryReply{MasterTransport: live.Client, entered: make(chan struct{}), resume: make(chan struct{})}
	helper := &MasterComms{Config: cfg, Client: gate, connection: live.connection}
	interrupted, cancel := context.WithCancel(ctx)
	cancel()
	if err := helper.FlushOutbox(interrupted); !errors.Is(err, context.Canceled) {
		t.Fatalf("helper interruption: %v", err)
	}
	gate = &gatedHistoryReply{MasterTransport: live.Client, entered: make(chan struct{}), resume: make(chan struct{})}
	close(gate.resume)
	live.Client = gate
	if err := live.FlushOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := store.Messages(ctx, owner, thread.ID, 0)
	if err != nil || len(page.Messages) != 2 {
		t.Fatalf("pending reply stranded: messages=%d err=%v", len(page.Messages), err)
	}
}

func TestIndexRebuildDiscardsAbandonedStagingAndOrphans(t *testing.T) {
	cfg, m, _, _, _, _, _ := commsFixture(t)
	for _, kind := range []string{"inbox", "outbox"} {
		if err := atomicPrivate(filepath.Join(masterIndexDir(cfg, kind), ".worker-abandoned"), []byte("partial")); err != nil {
			t.Fatal(err)
		}
	}
	id := BodyHash("uncommitted")
	if err := markMasterPending(cfg, "outbox", id+".json", id); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Active(); err != nil {
		t.Fatal(err)
	}
	if err := m.FlushOutbox(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"inbox", "outbox"} {
		entries, err := os.ReadDir(masterIndexDir(cfg, kind))
		if err != nil || len(entries) != 0 {
			t.Fatalf("%s rebuild left abandoned hints: %v %v", kind, entries, err)
		}
	}
}

func TestWarmOutboxRecoversHelperCrash(t *testing.T) {
	cfg, m, store, owner, bird, thread, _ := commsFixture(t)
	ctx := t.Context()
	if _, err := store.Send(ctx, owner, c.SendMessage{ThreadID: thread.ID, Recipients: []string{bird.ID}, Body: "question", RequestID: "warm-helper-crash"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := m.Next(ctx)
	if err != nil || turn == nil {
		t.Fatalf("missing turn: %v", err)
	}
	if err := m.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if !m.outboxIndexed {
		t.Fatal("worker must already be indexed")
	}
	id := BodyHash("uncommitted-helper-reply")
	if err := markMasterPending(cfg, "outbox", id+".json", id); err != nil {
		t.Fatal(err)
	}
	if err := atomicPrivate(filepath.Join(masterIndexDir(cfg, "outbox"), ".worker-abandoned"), []byte("partial")); err != nil {
		t.Fatal(err)
	}
	if err := QueueMasterReply(cfg, turn.Delivery.ID, "surviving reply"); err != nil {
		t.Fatal(err)
	}
	if err := m.FlushOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := store.Messages(ctx, owner, thread.ID, 0)
	if err != nil || len(page.Messages) != 2 {
		t.Fatalf("reply blocked after helper crash: %v %v", page, err)
	}
	entries, err := os.ReadDir(masterIndexDir(cfg, "outbox"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("abandoned hints remain: %v %v", entries, err)
	}
}

func TestColdHelperDoesNotRewriteSettledHints(t *testing.T) {
	cfg, _, _ := masterMigrationFixture(t)
	retainedOutboxHistory(t, cfg, 20)
	m := &MasterComms{Config: cfg, connection: masterConnectionState{Epoch: 1}}
	if err := m.FlushOutbox(t.Context()); err != nil {
		t.Fatal(err)
	}
	dir := masterIndexDir(cfg, "outbox")
	// Pin an old directory timestamp: creating/removing any historical hint
	// changes it, without relying on timing or filesystem timestamp precision.
	stamp := time.Unix(1000000000, 0)
	if err := os.Chtimes(dir, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	helper := &MasterComms{Config: cfg, connection: m.connection}
	if err := helper.FlushOutbox(t.Context()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(stamp) {
		t.Fatal("helper rewrote settled history hints")
	}
}

func TestWarmInboxRetriesFailedInitialWrite(t *testing.T) {
	cfg, m, store, owner, bird, thread, code := commsFixture(t)
	ctx := t.Context()
	if _, err := m.Active(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Send(ctx, owner, c.SendMessage{ThreadID: thread.ID, Recipients: []string{bird.ID}, Body: "retry admission", RequestID: "failed-inbox-save"}); err != nil {
		t.Fatal(err)
	}
	page, err := m.Client.Pending(ctx, c.PendingQuery{})
	if err != nil || len(page.Deliveries) != 1 {
		t.Fatalf("pending: %+v %v", page, err)
	}
	d := page.Deliveries[0]
	msg, err := m.Client.Message(ctx, d.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	turn := MasterTurn{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, RestoreEpoch: page.RestoreEpoch, Delivery: d, Message: msg, BodySHA: BodyHash(msg.Body), Phase: "received"}
	path := masterRecordPath(cfg, d.ID)
	// A directory at the destination deterministically rejects the canonical
	// write after the pending hint has succeeded, without a disk-full fixture.
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveMasterTurn(cfg, &turn); err == nil {
		t.Fatal("canonical write unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(masterIndexDir(cfg, "inbox"), BodyHash(d.ID)+".json")); err != nil {
		t.Fatal("missing orphan hint", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if active, err := m.Active(); err != nil || active != nil {
		t.Fatalf("orphan blocked running worker: %+v %v", active, err)
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	next, err := m.Next(ctx)
	if err != nil || next == nil || next.Delivery.ID != d.ID {
		t.Fatalf("delivery not retried: %+v %v", next, err)
	}
	if err := m.Advance(ctx, next); err != nil {
		t.Fatal(err)
	}
	if code.calls != 1 {
		t.Fatalf("model calls=%d", code.calls)
	}
}
