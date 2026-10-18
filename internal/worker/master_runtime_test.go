package worker

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"testing"
)

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
