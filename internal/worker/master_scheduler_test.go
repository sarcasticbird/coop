package worker

import (
	"context"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"testing"
)

type admittedEngineering struct{ calls int }

func (*admittedEngineering) HasActive() bool                      { return false }
func (*admittedEngineering) Ready() bool                          { return true }
func (e *admittedEngineering) Tick(context.Context) (bool, error) { e.calls++; return true, nil }

func TestMasterConversationFairness(t *testing.T) {
	cfg, m, s, o, b, th, code := commsFixture(t)
	ctx := context.Background()
	eng := &admittedEngineering{}
	e := &Engine{Config: cfg, Store: NewVersionedFileStore(cfg.StateDir, 3), OpenCode: code, MasterRuntime: &MasterRuntime{Comms: m, Engineering: eng}}
	for _, id := range []string{"one", "two", "three"} {
		if _, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "please answer", RequestID: id}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		code.running = true
		if err := e.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		turn, err := m.Active()
		if err != nil || turn == nil {
			t.Fatal("conversation was starved", err)
		}
		if eng.calls != 0 {
			t.Fatal("engineering ran before two eligible conversations")
		}
		if err = QueueMasterReply(cfg, turn.Delivery.ID, "A substantive reply."); err != nil {
			t.Fatal(err)
		}
		if err = m.FlushOutbox(ctx); err != nil {
			t.Fatal(err)
		}
		code.running = false
		if err = e.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if eng.calls != 1 || code.calls != 2 {
		t.Fatal("engineering starved after two completed conversations")
	}
	code.running = true
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if code.calls != 3 || eng.calls != 1 {
		t.Fatal("conversation did not resume after engineering service")
	}
}

func TestMasterStatusReportsAdmissionHold(t *testing.T) {
	cfg, m, _, _, _, _, code := commsFixture(t)
	store := NewVersionedFileStore(cfg.StateDir, 3)
	e := &Engine{Config: cfg, Store: store, OpenCode: code, MasterRuntime: &MasterRuntime{Comms: m, Engineering: &admittedEngineering{}}}
	for _, hold := range []struct{ hard, admission bool }{{false, true}, {true, false}, {true, true}, {false, false}} {
		if err := store.SetPaused(hold.hard); err != nil {
			t.Fatal(err)
		}
		if err := store.SetAdmissionPaused(hold.admission); err != nil {
			t.Fatal(err)
		}
		if err := e.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		status, err := ReadStatus(cfg.StateDir, m.Clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		paused := hold.hard || hold.admission
		if !status.AdmissionPauseSupported || status.AdmissionPaused != hold.admission || status.Paused != paused || (status.State == "paused") != paused {
			t.Fatalf("hold %+v reported inconsistently: %+v", hold, status)
		}
	}
}
