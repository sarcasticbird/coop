package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

func TestRestoreEpochGuestReceipts(t *testing.T) {
	cfg, comms, s, o, b, th, code := commsFixture(t)
	ctx := context.Background()
	sent, e := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "answer this", RequestID: "restore"})
	if e != nil {
		t.Fatal(e)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, e = s.Backup(ctx, backup, nil); e != nil {
		t.Fatal(e)
	}
	if e = comms.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	turn, e := comms.Next(ctx)
	if e != nil || turn == nil {
		t.Fatal(e)
	}
	if e = comms.Advance(ctx, turn); e != nil {
		t.Fatal(e)
	}
	if e = QueueMasterReply(cfg, turn.Delivery.ID, "published after snapshot"); e != nil {
		t.Fatal(e)
	}
	if e = comms.FlushOutbox(ctx); e != nil {
		t.Fatal(e)
	}
	// Recovery also inventories native action journals. Unknown effects remain a
	// separate blocker even after the native model is terminal.
	action := nativeActionRecord{Version: 3, JobID: BodyHash("job"), RequestID: "action", State: "uncertain"}
	if e = saveNativeAction(cfg, action); e != nil {
		t.Fatal(e)
	}
	if e = s.Quiesce(ctx, o, 1); e != nil {
		t.Fatal(e)
	}
	client := comms.Client.(*masterclient.Client)
	report, e := ReconcileMaster(ctx, cfg, client, code, false)
	if e != nil {
		t.Fatal(e)
	}
	if report.Unresolved < 4 {
		t.Fatalf("missing receipts %+v", report)
	}
	evidence, e := s.RecoveryEvidence(ctx, o, b.ID, "")
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range evidence {
		if r.Uncertain {
			if e = s.ResolveRecovery(ctx, o, c.RecoveryResolution{RestoreEpoch: 1, AgentID: b.ID, EvidenceID: r.ID, Reason: "cannot waive uncertainty"}); !errors.Is(e, c.ErrConflict) {
				t.Fatalf("uncertainty waived %v", e)
			}
		}
	}
	if _, e = ReconcileMaster(ctx, cfg, client, code, true); !errors.Is(e, c.ErrEpoch) {
		t.Fatalf("accepted unresolved epoch %v", e)
	}
	// Remove only this test-created action fixture before creating an independent
	// restore from the earlier snapshot. Real action evidence is never removed.
	if e = os.Remove(nativeActionPath(cfg, action.JobID, action.RequestID)); e != nil {
		t.Fatal(e)
	}
	// Source database location is private to Store; a dedicated independent
	// lineage fixture starts at its current epoch and is kept outside restore.
	lineage := filepath.Join(t.TempDir(), "epoch")
	if e = os.WriteFile(lineage, []byte(`{"master_id":"master","epoch":1}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(t.TempDir(), "restore")
	if _, e = master.Restore(ctx, backup, target, lineage, false); e != nil {
		t.Fatal(e)
	}
	restored, e := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(target, "master.sqlite"), MasterID: "master", EpochPath: lineage})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = restored.Close() }()
	hash := sha256.Sum256([]byte("agent-test-token"))
	policy := &master.Policy{AllowedHosts: []string{"127.0.0.1"}, Tokens: []master.Token{{PrincipalID: b.ID, SHA256: hex.EncodeToString(hash[:]), InstanceID: cfg.Master.InstanceID, Generation: 1}}}
	server := httptest.NewTLSServer(master.NewHandler(&master.Service{Store: restored}, policy))
	defer server.Close()
	client, e = masterclient.New(masterclient.ClientConfig{URL: server.URL, MasterID: "master", Token: "agent-test-token", HTTPClient: server.Client()})
	if e != nil {
		t.Fatal(e)
	}
	comms.Client = client
	if e = comms.Tick(ctx); !errors.Is(e, c.ErrEpoch) {
		t.Fatalf("epoch not detected: %v", e)
	}
	code.running = false
	status, e := ReconcileMaster(ctx, cfg, client, code, false)
	if e != nil {
		t.Fatal(e)
	}
	if status.Unresolved != 3 {
		t.Fatalf("inbox/outbox/published not enumerated: %+v", status)
	}
	evidence, e = restored.RecoveryEvidence(ctx, o, b.ID, "")
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range evidence {
		if e = restored.ResolveRecovery(ctx, o, c.RecoveryResolution{RestoreEpoch: 2, AgentID: b.ID, EvidenceID: r.ID, Reason: "retain missing reply, retire completed native turn"}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = ReconcileMaster(ctx, cfg, client, code, true); e != nil {
		t.Fatal(e)
	}
	if e = restored.Resume(ctx, o, 2); e != nil {
		t.Fatal(e)
	}
	restarted, e := NewMasterComms(cfg, client, code, RealClock{})
	if e != nil {
		t.Fatal(e)
	}
	if e = restarted.Tick(ctx); e != nil {
		t.Fatal(e)
	}
	next, e := restarted.Next(ctx)
	if e != nil || next != nil {
		t.Fatalf("completed native work reopened: %+v %v", next, e)
	}
	page, e := client.Messages(ctx, th.ID, 0)
	if e != nil || len(page.Messages) != 1 || page.Messages[0].ID != sent.MessageID {
		t.Fatalf("invented/replayed reply: %+v %v", page, e)
	}
	if code.calls != 1 {
		t.Fatalf("native resubmitted %d times", code.calls)
	}
}
