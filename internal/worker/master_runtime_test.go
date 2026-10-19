package worker

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"testing"
)

type busyProducerEngineering struct {
	ticks, observations int
}

func (*busyProducerEngineering) HasActive() bool { return true }
func (*busyProducerEngineering) Ready() bool     { return true }
func (e *busyProducerEngineering) Tick(context.Context) (bool, error) {
	e.ticks++
	return false, nil
}
func (e *busyProducerEngineering) ObserveOnly(context.Context) (bool, error) {
	e.observations++
	return false, nil
}

func TestMasterRuntimeNativeProducerContentionDoesNotCancelContinuation(t *testing.T) {
	cfg, comms, store, owner, bird, thread, code := commsFixture(t)
	receipt, err := store.Send(t.Context(), owner, c.SendMessage{ThreadID: thread.ID, Recipients: []string{bird.ID}, Body: "status?", RequestID: "producer-contention"})
	if err != nil {
		t.Fatal(err)
	}
	// Continuation holds this same producer lock while revalidating GitHub.
	release, err := lockNativeProducer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	engineering := &busyProducerEngineering{}
	e := &Engine{Config: cfg, Store: NewVersionedFileStore(cfg.StateDir, 3), OpenCode: code, MasterRuntime: &MasterRuntime{Comms: comms, Engineering: engineering}}
	if err = e.Tick(t.Context()); err != nil {
		t.Fatal("local producer contention was mistaken for a transport outage", err)
	}
	status, err := store.MessageStatus(t.Context(), owner, receipt.MessageID)
	if err != nil || status.Deliveries[0].State != "received" || engineering.ticks != 1 || engineering.observations != 0 || code.calls != 0 {
		t.Fatal("contention canceled native progress or stopped durable receipts", status, err)
	}
}

func TestMasterRuntimeReceiptsBypassLegacyCatalog(t *testing.T) {
	cfg, comms, s, o, b, th, code := commsFixture(t)
	ctx := context.Background()
	r, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "reply please", RequestID: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Config: cfg, Store: NewVersionedFileStore(cfg.StateDir, 3), OpenCode: code, MasterRuntime: &MasterRuntime{Comms: comms}}
	if err = e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := s.MessageStatus(ctx, o, r.MessageID)
	if err != nil || status.Deliveries[0].State != "running" {
		t.Fatalf("conversation blocked on legacy catalog: %+v %v", status, err)
	}
	if code.calls != 1 {
		t.Fatal("no native conversation")
	}
	if err = e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if code.calls != 1 {
		t.Fatal("active conversation submitted twice")
	}
}
