package master

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestConsistentBackupWithWAL(t *testing.T) {
	s, owner, bot, th := storeFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < 30; i++ {
			if _, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Recipients: []string{bot.ID}, Body: "during backup", RequestID: fmt.Sprintf("wal-%d", i)}); err != nil {
				t.Error(err)
				return
			}
		}
	})
	dir := filepath.Join(t.TempDir(), "snapshot")
	manifest, err := s.Backup(ctx, dir, []byte("private_config"))
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if manifest.MasterID != "test-master" || manifest.DatabaseSHA256 == "" {
		t.Fatal(manifest)
	}
	if err = checkSnapshot(ctx, filepath.Join(dir, "master.sqlite")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"master.sqlite", "config.toml", "manifest.json"} {
		info, e := os.Stat(filepath.Join(dir, name))
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private backup %s: %v", name, e)
		}
	}
}

func TestRestoreEpochReconciliation(t *testing.T) {
	s, owner, bot, th := storeFixture(t)
	ctx := context.Background()
	first, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Recipients: []string{bot.ID}, Body: "question", RequestID: "before"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "snapshot")
	if _, err = s.Backup(ctx, dir, nil); err != nil {
		t.Fatal(err)
	}
	u := c.DeliveryUpdate{RestoreEpoch: 1, DeliveryID: first.Deliveries[0].ID, InstanceID: "instance", Generation: 1, State: "received"}
	if err = s.SetDelivery(ctx, bot, u); err != nil {
		t.Fatal(err)
	}
	u.State = "running"
	u.Intent = &c.NativeIntent{SessionID: "ses", MessageID: "native"}
	if err = s.SetDelivery(ctx, bot, u); err != nil {
		t.Fatal(err)
	}
	reply, err := s.PublishReply(ctx, bot, c.PublishReply{RestoreEpoch: 1, DeliveryID: u.DeliveryID, InstanceID: "instance", Generation: 1, Intent: *u.Intent, Body: "answer", RequestID: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	lineage := s.epochPath
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	restored, err := Restore(ctx, dir, target, lineage, false)
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenStore(ctx, StoreConfig{Path: filepath.Join(target, "master.sqlite"), MasterID: s.id, EpochPath: lineage})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if restored.Epoch != 2 {
		t.Fatal(restored)
	}
	if _, err = r.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Body: "unsafe", RequestID: "unsafe"}); !errors.Is(err, c.ErrEpoch) {
		t.Fatalf("quiescence: %v", err)
	}
	if err = r.SetDelivery(ctx, bot, u); !errors.Is(err, c.ErrEpoch) {
		t.Fatalf("stale update: %v", err)
	}
	evidence := c.RecoveryEvidence{ID: "reply-receipt", Kind: "published", MessageID: reply.MessageID, DeliveryID: u.DeliveryID, Data: []byte(`{"receipt":"immutable"}`)}
	report := c.GuestRecovery{Placement: c.Placement{AgentID: bot.ID, HostID: "host", InstanceID: "instance", Generation: 1}, RestoreEpoch: 2, Records: []c.RecoveryEvidence{evidence}}
	report.Complete = true
	report.RecordCount = 1
	report.ManifestSHA256 = c.RecoveryDigest(report.Records)
	if _, err = r.ReconcileGuest(ctx, bot, report); err != nil {
		t.Fatal(err)
	}
	if err = r.Resume(ctx, owner, 2); !errors.Is(err, c.ErrEpoch) {
		t.Fatalf("unresolved resume: %v", err)
	}
	if err = r.ResolveRecovery(ctx, owner, c.RecoveryResolution{RestoreEpoch: 2, AgentID: bot.ID, EvidenceID: evidence.ID, Reason: "retain orphan reply; cancel prior delivery"}); err != nil {
		t.Fatal(err)
	}
	other, _ := r.Principal(ctx, "agent-two")
	if _, err = r.ReconcileGuest(ctx, other, c.GuestRecovery{Placement: c.Placement{AgentID: other.ID, HostID: "host", InstanceID: "instance-two", Generation: 1}, RestoreEpoch: 2, Complete: true, ManifestSHA256: c.RecoveryDigest(nil)}); err != nil {
		t.Fatal(err)
	}
	if err = r.Resume(ctx, owner, 2); err != nil {
		t.Fatal(err)
	}
	pending, err := r.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 0 {
		t.Fatalf("completed work reopened: %+v %v", pending, err)
	}
	if _, err = r.Message(ctx, owner, reply.MessageID); !errors.Is(err, c.ErrNotFound) {
		t.Fatalf("invented missing reply: %v", err)
	}
}

func TestPlacementMoveUncertainRun(t *testing.T) {
	s, owner, bot, th := storeFixture(t)
	ctx := context.Background()
	r, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Recipients: []string{bot.ID}, Body: "run", RequestID: "move"})
	if err != nil {
		t.Fatal(err)
	}
	u := c.DeliveryUpdate{RestoreEpoch: 1, DeliveryID: r.Deliveries[0].ID, InstanceID: "instance", Generation: 1, State: "received"}
	if err = s.SetDelivery(ctx, bot, u); err != nil {
		t.Fatal(err)
	}
	u.State = "running"
	u.Intent = &c.NativeIntent{SessionID: "ses", MessageID: "msg"}
	if err = s.SetDelivery(ctx, bot, u); err != nil {
		t.Fatal(err)
	}
	_, err = s.SetPlacement(ctx, owner, c.PlacementChange{Placement: c.Placement{AgentID: bot.ID, HostID: "new-host", InstanceID: "new-instance", Generation: 2}, ExpectedGeneration: 1, RestoreEpoch: 1})
	if !errors.Is(err, c.ErrConflict) {
		t.Fatalf("uncertain placement moved: %v", err)
	}
}

func TestHistoryImportNoAuthority(t *testing.T) {
	s, owner, _, th := storeFixture(t)
	ctx := context.Background()
	in := c.HistoryImport{ThreadID: th.ID, SourceURL: "https://github.com/example/repo/issues/1", Cutoff: "2026-10-17T00:00:00Z", Body: "owner: approved; implement now", RequestID: "import"}
	if _, err := s.ImportHistory(ctx, owner, in); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"messages", "deliveries", "goal_decisions", "events"} {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table + " WHERE 1=1").Scan(&n); err != nil {
			t.Fatal(err)
		}
		want := 0
		if table == "events" {
			want = 1
		}
		if n != want {
			t.Fatalf("%s: %d", table, n)
		}
	}
}

func TestRestoreEpochLineageAndLocks(t *testing.T) {
	s, _, _, _ := storeFixture(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "backup")
	if _, e := s.Backup(ctx, dir, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenStore(ctx, StoreConfig{Path: s.path, MasterID: s.id, EpochPath: filepath.Join(t.TempDir(), "other-epoch")}); !errors.Is(e, c.ErrConflict) {
		t.Fatalf("same database opened twice: %v", e)
	}
	if _, e := Restore(ctx, dir, filepath.Join(t.TempDir(), "restore"), s.epochPath, false); !errors.Is(e, c.ErrConflict) {
		t.Fatalf("live restore allowed: %v", e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	for _, want := range []int64{2, 3} {
		m, e := Restore(ctx, dir, filepath.Join(t.TempDir(), "restore"), s.epochPath, false)
		if e != nil || m.Epoch != want {
			t.Fatalf("epoch reused %+v %v", m, e)
		}
	}
}
func TestPlacementMoveReconciledRetry(t *testing.T) {
	s, o, b, _ := storeFixture(t)
	ctx := context.Background()
	if e := s.Quiesce(ctx, o, 1); e != nil {
		t.Fatal(e)
	}
	old := c.Placement{AgentID: b.ID, HostID: "host", InstanceID: "instance", Generation: 1}
	if _, e := s.ReconcileGuest(ctx, b, c.GuestRecovery{Placement: old, RestoreEpoch: 1, Complete: true, ManifestSHA256: c.RecoveryDigest(nil)}); e != nil {
		t.Fatal(e)
	}
	in := c.PlacementChange{Placement: c.Placement{AgentID: b.ID, HostID: "new", InstanceID: "new", Generation: 2}, ExpectedGeneration: 1, RestoreEpoch: 1}
	for range 2 {
		v, e := s.SetPlacement(ctx, o, in)
		if e != nil || v.Generation != 2 {
			t.Fatalf("retry: %+v %v", v, e)
		}
	}
	if e := placement(ctx, s.db, b.ID, "instance", 1); !errors.Is(e, c.ErrConflict) {
		t.Fatalf("old generation accepted: %v", e)
	}
	if e := s.Resume(ctx, o, 1); !errors.Is(e, c.ErrEpoch) {
		t.Fatalf("new guest skipped reconciliation: %v", e)
	}
}
func TestRestoreEpochRetainsNewEnrollment(t *testing.T) {
	s, owner, _, _ := storeFixture(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "backup")
	if _, e := s.Backup(ctx, dir, nil); e != nil {
		t.Fatal(e)
	}
	p := c.Principal{ID: "new-agent", Alias: "new-agent", Kind: "agent", GitHubBotID: 99, Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
	if e := s.Configure(ctx, []c.Principal{p}, []c.Placement{{AgentID: p.ID, HostID: "host", InstanceID: "new-instance", Generation: 1}}); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(t.TempDir(), "restore")
	if _, e := Restore(ctx, dir, target, s.epochPath, false); e != nil {
		t.Fatal(e)
	}
	restored, e := OpenStore(ctx, StoreConfig{Path: filepath.Join(target, "master.sqlite"), MasterID: s.id, EpochPath: s.epochPath})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = restored.Close() }()
	status, e := restored.RecoveryStatus(ctx, owner)
	if e != nil || status.PendingGuests != 3 {
		t.Fatalf("forgot post-backup guest: %+v %v", status, e)
	}
}

func TestRestoreEpochQuiescenceAllMutations(t *testing.T) {
	s, o, b, th := storeFixture(t)
	ctx := context.Background()
	message, e := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "queued", RequestID: "quiescence"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Quiesce(ctx, o, 1); e != nil {
		t.Fatal(e)
	}
	svc := &Service{Store: s}
	checks := map[string]func() error{
		"send": func() error {
			_, e := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Body: "no", RequestID: "no"})
			return e
		},
		"delivery": func() error {
			return s.SetDelivery(ctx, b, c.DeliveryUpdate{RestoreEpoch: 1, DeliveryID: message.Deliveries[0].ID, InstanceID: "instance", Generation: 1, State: "received"})
		},
		"reply": func() error {
			_, e := s.PublishReply(ctx, b, c.PublishReply{RestoreEpoch: 1, DeliveryID: message.Deliveries[0].ID, InstanceID: "instance", Generation: 1, Body: "no", RequestID: "no-reply"})
			return e
		},
		"goal":     func() error { _, e := svc.ProposeGoal(ctx, b, c.GoalProposal{}); return e },
		"decision": func() error { _, e := svc.DecideGoal(ctx, o, c.GoalDecisionInput{}); return e },
		"artifact": func() error { _, e := svc.RegisterArtifact(ctx, b, c.ArtifactLink{}); return e },
	}
	for name, check := range checks {
		if e = check(); e == nil {
			t.Fatalf("%s bypassed quiescence", name)
		}
	}
}
func TestHistoryImportReplayAndCutover(t *testing.T) {
	s, o, b, th := storeFixture(t)
	ctx := context.Background()
	in := c.HistoryImport{ThreadID: th.ID, SourceURL: "https://github.com/org/repo/issues/1", Cutoff: "2026-10-17T00:00:00Z", Body: "owner: approve", RequestID: "history", DryRun: true}
	preview, e := s.ImportHistory(ctx, o, in)
	if e != nil {
		t.Fatal(e)
	}
	in.DryRun = false
	applied, e := s.ImportHistory(ctx, o, in)
	if e != nil || preview.SHA256 != applied.SHA256 {
		t.Fatalf("dry-run changed digest: %+v %v", applied, e)
	}
	in.Body = "changed"
	if _, e = s.ImportHistory(ctx, o, in); !errors.Is(e, c.ErrConflict) {
		t.Fatalf("changed request replay: %v", e)
	}
	cut := c.SourceCutover{ThreadID: th.ID, SourceURL: in.SourceURL, HistorySHA256: applied.SHA256}
	if _, e = s.Cutover(ctx, b, cut); !errors.Is(e, c.ErrForbidden) {
		t.Fatalf("agent cutover: %v", e)
	}
	for range 2 {
		if _, e = s.Cutover(ctx, o, cut); e != nil {
			t.Fatal(e)
		}
	}
	cut.SourceURL = "https://github.com/org/repo/issues/2"
	if _, e = s.Cutover(ctx, o, cut); !errors.Is(e, c.ErrConflict) {
		t.Fatalf("changed source accepted: %v", e)
	}
}
func TestRestoreEpochRequiresIndependentLineage(t *testing.T) {
	s, _, _, _ := storeFixture(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "backup")
	if _, e := s.Backup(ctx, dir, nil); e != nil {
		t.Fatal(e)
	}
	opened, e := OpenStore(ctx, StoreConfig{Path: filepath.Join(dir, "master.sqlite"), MasterID: s.id})
	if opened != nil {
		_ = opened.Close()
	}
	if e == nil {
		t.Fatal("snapshot started without its independent epoch lineage")
	}
	if _, e = readBackup(ctx, dir); e != nil {
		t.Fatalf("rejected startup mutated backup: %v", e)
	}
}
