package master

import (
	"context"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"path/filepath"
	"testing"
)

func TestCommitReviewAuthorizedPagesAfterHiddenRows(t *testing.T) {
	s, o, b, _ := storeFixture(t)
	ctx := context.Background()
	o.Engagements = []string{"coop", "hidden"}
	b.Engagements = o.Engagements
	if e := s.Configure(ctx, []c.Principal{o, b}, nil); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 101; i++ {
		if _, e := s.CreateThread(ctx, o, c.CreateThread{Title: "hidden", Engagement: "hidden", Members: []string{o.ID, b.ID}, RequestID: fmt.Sprint("hidden", i)}); e != nil {
			t.Fatal(e)
		}
	}
	th, e := s.CreateThread(ctx, o, c.CreateThread{Title: "visible", Engagement: "coop", Members: []string{o.ID, b.ID}, RequestID: "visible"})
	if e != nil {
		t.Fatal(e)
	}
	b.Engagements = []string{"coop"}
	if e = s.Configure(ctx, []c.Principal{b}, nil); e != nil {
		t.Fatal(e)
	}
	events, e := s.Events(ctx, b, c.Cursor{RestoreEpoch: 1, Sequence: 1})
	if e != nil || len(events) != 1 || events[0].ThreadID != th.ID {
		t.Fatalf("hidden rows masked events: %+v %v", events, e)
	}
	var ps []c.Principal
	var placements []c.Placement
	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("a%03d", i)
		ps = append(ps, c.Principal{ID: id, Alias: id, Kind: "agent", Engagements: []string{"hidden"}})
		placements = append(placements, c.Placement{AgentID: id, HostID: "host", InstanceID: id, Generation: 1})
	}
	if e = s.Configure(ctx, ps, placements); e != nil {
		t.Fatal(e)
	}
	team, e := s.Team(ctx, b)
	if e != nil || len(team) != 2 || team[0].AgentID != b.ID {
		t.Fatalf("hidden rows masked presence: %+v %v", team, e)
	}
}
func TestCommitReviewRestoreAliasReassignment(t *testing.T) {
	s, _, _, _ := storeFixture(t)
	ctx := context.Background()
	a := c.Principal{ID: "agent-a", Alias: "alpha", Kind: "agent", Engagements: []string{"coop"}}
	z := a
	z.ID = "agent-z"
	z.Alias = "zeta"
	if e := s.Configure(ctx, []c.Principal{a, z}, nil); e != nil {
		t.Fatal(e)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, e := s.Backup(ctx, backup, nil); e != nil {
		t.Fatal(e)
	}
	z.Alias = "omega"
	a.Alias = "zeta"
	if e := s.Configure(ctx, []c.Principal{z, a}, nil); e != nil {
		t.Fatal(e)
	}
	lineage := s.epochPath
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(t.TempDir(), "restore")
	if _, e := Restore(ctx, backup, target, lineage, false); e != nil {
		t.Fatalf("valid alias reassignment blocks restore: %v", e)
	}
	r, e := OpenStore(ctx, StoreConfig{Path: filepath.Join(target, "master.sqlite"), MasterID: s.id, EpochPath: lineage})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	p, e := r.Principal(ctx, a.ID)
	if e != nil || p.Alias != "zeta" {
		t.Fatal(p, e)
	}
}
func TestCommitReviewRegistryAheadOfflineRestore(t *testing.T) {
	s, _, b, _ := storeFixture(t)
	ctx := context.Background()
	backup := filepath.Join(t.TempDir(), "backup")
	if _, e := s.Backup(ctx, backup, nil); e != nil {
		t.Fatal(e)
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	p := c.Placement{AgentID: b.ID, HostID: "new", InstanceID: "new", Generation: 2}
	if _, e = tx.ExecContext(ctx, "UPDATE placements SET body=? WHERE agent_id=?", encode(p), b.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.saveRegistry(ctx, tx); e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	path, lineage := s.path, s.epochPath
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if reopened, e := OpenStore(ctx, StoreConfig{Path: path, MasterID: s.id, EpochPath: lineage}); e == nil {
		_ = reopened.Close()
		t.Fatal("registry-ahead startup not fenced")
	}
	target := filepath.Join(t.TempDir(), "restore")
	if _, e = Restore(ctx, backup, target, lineage, false); e != nil {
		t.Fatal(e)
	}
	r, e := OpenStore(ctx, StoreConfig{Path: filepath.Join(target, "master.sqlite"), MasterID: s.id, EpochPath: lineage})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	if e = placement(ctx, r.db, b.ID, "new", 2); e != nil {
		t.Fatal(e)
	}
	if e = placement(ctx, r.db, b.ID, "instance", 1); e == nil {
		t.Fatal("old generation restored")
	}
	if admission(ctx, r.db) != c.ErrEpoch {
		t.Fatal("restore not held for reconciliation")
	}
}

func TestCommitReviewOwnDeliveryReceiptScope(t *testing.T) {
	s, o, b, _ := storeFixture(t)
	other, err := s.Principal(context.Background(), "agent-two")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	th, e := s.CreateThread(ctx, o, c.CreateThread{Title: "private", Engagement: "coop", Members: []string{o.ID, b.ID}, RequestID: "private"})
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "secret", RequestID: "secret"})
	if e != nil {
		t.Fatal(e)
	}
	page, e := s.Pending(ctx, b, c.PendingQuery{})
	if e != nil || len(page.Deliveries) != 1 {
		t.Fatal(page, e)
	}
	id := page.Deliveries[0].ID
	if _, e = s.SetMembers(ctx, o, c.SetMembers{ThreadID: th.ID, ExpectedMembers: th.Members, Members: []string{o.ID}, RequestID: "revoke"}); e != nil {
		t.Fatal(e)
	}
	own, e := s.OwnDelivery(ctx, b, id)
	if e != nil || own.State != "cancelled" {
		t.Fatal(own, e)
	}
	if _, e = s.OwnDelivery(ctx, other, id); e != c.ErrForbidden {
		t.Fatal("foreign receipt visible", e)
	}
	if _, e = s.Message(ctx, b, receipt.MessageID); e != c.ErrForbidden {
		t.Fatal("revoked body visible", e)
	}
}
