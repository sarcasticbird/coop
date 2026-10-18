package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/masterclient"
)

func commsFixture(t *testing.T) (Config, *MasterComms, *master.Store, c.Principal, c.Principal, c.Thread, *fakeOpenCode) {
	return commsFixtureWithRetiredHistory(t, false)
}

func commsFixtureWithRetiredHistory(t *testing.T, retired bool) (Config, *MasterComms, *master.Store, c.Principal, c.Principal, c.Thread, *fakeOpenCode) {
	t.Helper()
	cfg, old, before := masterMigrationFixture(t)
	var retiredJobs []string
	if retired {
		j := Job{Kind: "plan", TaskURL: "https://github.com/code/repo/issues/99", Phase: "blocked", WaitReason: "scope"}
		j.ID = JobKey(j)
		before.Jobs[j.ID] = j
		if err := old.Save(before); err != nil {
			t.Fatal(err)
		}
		retiredJobs = []string{j.ID}
	}
	if err := MigrateMasterStateWithRetirements(cfg, before.InstanceID, retiredJobs, nil); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.db"), MasterID: "master"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	o := c.Principal{ID: "owner", Alias: "owner", Kind: "owner", Engagements: []string{"coop"}, Capabilities: []string{"read", "send", "manage", "decide"}}
	b := c.Principal{ID: cfg.Master.AgentID, Alias: "bird", Kind: "agent", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}, GitHubBotID: cfg.Self.ID}
	if err = s.Configure(ctx, []c.Principal{o, b}, []c.Placement{{AgentID: b.ID, HostID: cfg.Master.HostID, InstanceID: cfg.Master.InstanceID, Generation: 1}}); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("agent-test-token"))
	policy := &master.Policy{AllowedHosts: []string{"127.0.0.1"}, Tokens: []master.Token{{PrincipalID: b.ID, SHA256: hex.EncodeToString(hash[:]), InstanceID: cfg.Master.InstanceID, Generation: 1}}}
	ts := httptest.NewTLSServer(master.NewHandler(&master.Service{Store: s}, policy))
	t.Cleanup(ts.Close)
	cl, err := masterclient.New(masterclient.ClientConfig{URL: ts.URL, MasterID: "master", Token: "agent-test-token", HTTPClient: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}
	th, err := s.CreateThread(ctx, o, c.CreateThread{Title: "Coop test", Engagement: "coop", Members: []string{o.ID, b.ID}, RequestID: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	code := &fakeOpenCode{running: true}
	comms, err := NewMasterComms(cfg, cl, code, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	return cfg, comms, s, o, b, th, code
}
func TestMasterConversationWhileCatalogBlocked(t *testing.T) {
	_, comms, s, o, b, th, code := commsFixture(t)
	ctx := context.Background()
	started := time.Now()
	r, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "status?", RequestID: "question"})
	if err != nil {
		t.Fatal(err)
	}
	if err = comms.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := s.MessageStatus(ctx, o, r.MessageID)
	if err != nil || status.Deliveries[0].State != "received" || time.Since(started) > 5*time.Second {
		t.Fatalf("slow/missing receipt %+v %v", status, err)
	}
	turn, err := comms.Next(ctx)
	if err != nil || turn == nil {
		t.Fatalf("no turn %v", err)
	}
	if err = comms.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if code.calls != 1 {
		t.Fatal("conversation required GitHub snapshot")
	}
}
func TestMasterNativeUnknownSubmission(t *testing.T) {
	cfg, comms, s, o, b, th, code := commsFixture(t)
	ctx := context.Background()
	code.lost = true
	if _, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "hello", RequestID: "unknown"}); err != nil {
		t.Fatal(err)
	}
	if err := comms.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := comms.Next(ctx)
	if err != nil || turn == nil {
		t.Fatal(err)
	}
	_ = comms.Advance(ctx, turn)
	restarted, err := NewMasterComms(cfg, comms.Client, code, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := restarted.Active()
	if err != nil || saved == nil {
		t.Fatal("lost saved native intent", err)
	}
	if err = restarted.Advance(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if code.calls != 1 {
		t.Fatal("uncertain native prompt replayed")
	}
}
func TestMasterReplyOutboxRecovery(t *testing.T) {
	cfg, comms, s, o, b, th, _ := commsFixture(t)
	ctx := context.Background()
	r, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "question", RequestID: "reply-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err = comms.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := comms.Next(ctx)
	if err != nil || turn == nil {
		t.Fatal(err)
	}
	if err = comms.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err = QueueMasterReply(cfg, turn.Delivery.ID, "An actual answer."); err != nil {
		t.Fatal(err)
	}
	if err = comms.FlushOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	if err = comms.FlushOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := s.MessageStatus(ctx, o, r.MessageID)
	if err != nil || status.Deliveries[0].State != "replied" {
		t.Fatalf("reply not linked %+v %v", status, err)
	}
	page, err := s.Messages(ctx, o, th.ID, 0)
	if err != nil || len(page.Messages) != 2 {
		t.Fatal("duplicate reply", err)
	}
}

type lostMasterReply struct {
	MasterTransport
	lost bool
}

func (l *lostMasterReply) PublishReply(ctx context.Context, in c.PublishReply) (c.MessageReceipt, error) {
	r, err := l.MasterTransport.PublishReply(ctx, in)
	if err == nil && !l.lost {
		l.lost = true
		return c.MessageReceipt{}, errors.New("response lost after commit")
	}
	return r, err
}

func TestMasterReplyCommittedResponseLost(t *testing.T) {
	cfg, m, s, o, b, th, code := commsFixture(t)
	ctx := context.Background()
	_, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "question", RequestID: "lost-reply"})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := m.Next(ctx)
	if err != nil || turn == nil {
		t.Fatal(err)
	}
	if err = m.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err = QueueMasterReply(cfg, turn.Delivery.ID, "The answer."); err != nil {
		t.Fatal(err)
	}
	lost := &lostMasterReply{MasterTransport: m.Client}
	m.Client = lost
	if err = m.FlushOutbox(ctx); err == nil {
		t.Fatal("expected lost response")
	}
	restarted, err := NewMasterComms(cfg, lost, code, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.FlushOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := s.Messages(ctx, o, th.ID, 0)
	if err != nil || len(page.Messages) != 2 {
		t.Fatalf("duplicate/missing reply: %+v %v", page, err)
	}
	if code.calls != 1 {
		t.Fatal("reply retry replayed native prompt")
	}
}

func TestMasterPreparedPausePreventsSubmit(t *testing.T) {
	cfg, m, s, o, b, th, code := commsFixture(t)
	ctx := context.Background()
	_, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "hello", RequestID: "prepared-pause"})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := m.Next(ctx)
	if err != nil || turn == nil {
		t.Fatal(err)
	}
	turn.Phase = "prepared"
	turn.Job = Job{SessionID: "ses_saved", MessageID: "msg_saved", SubmissionState: "prepared"}
	if err = saveMasterTurn(cfg, turn); err != nil {
		t.Fatal(err)
	}
	m.Paused = true
	if err = m.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if code.calls != 0 {
		t.Fatal("prepared prompt submitted while paused")
	}
}

func TestMasterTerminalWithoutReplyBlocksDelivery(t *testing.T) {
	_, m, s, o, b, th, code := commsFixture(t)
	ctx := context.Background()
	r, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "answer me", RequestID: "no-answer"})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := m.Next(ctx)
	if err != nil || turn == nil {
		t.Fatal(err)
	}
	if err = m.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	code.running = false
	if err = m.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	status, err := s.MessageStatus(ctx, o, r.MessageID)
	if err != nil || status.Deliveries[0].State != "blocked" {
		t.Fatalf("false running delivery: %+v %v", status, err)
	}
}

func TestMasterConversationRecoveryRequiresTerminalAndExactIntent(t *testing.T) {
	cfg, m, s, o, b, th, code := commsFixture(t)
	ctx := context.Background()
	_, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "question", RequestID: "recovery"})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := m.Next(ctx)
	if err != nil || turn == nil {
		t.Fatal(err)
	}
	if err = m.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if _, err = ResolveMasterConversation(ctx, cfg, m.Client, code, turn.Delivery.ID, turn.Job.SessionID, "msg_wrong", "cancel"); err == nil {
		t.Fatal("mismatched native intent resolved")
	}
	if _, err = ResolveMasterConversation(ctx, cfg, m.Client, code, turn.Delivery.ID, turn.Job.SessionID, turn.Job.MessageID, "cancel"); err == nil {
		t.Fatal("running native intent resolved")
	}
	code.running = false
	if err = m.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if _, err = ResolveMasterConversation(ctx, cfg, m.Client, code, turn.Delivery.ID, turn.Job.SessionID, turn.Job.MessageID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if code.calls != 1 {
		t.Fatal("resolution resubmitted native intent")
	}
}

func TestMasterConversationCompletionAccountingAfterRestart(t *testing.T) {
	cfg, m, s, o, b, th, code := commsFixture(t)
	ctx := context.Background()
	_, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "hello", RequestID: "accounting"})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := m.Next(ctx)
	if err != nil || turn == nil {
		t.Fatal(err)
	}
	if err = m.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err = QueueMasterReply(cfg, turn.Delivery.ID, "An actual reply."); err != nil {
		t.Fatal(err)
	}
	if err = m.FlushOutbox(ctx); err != nil {
		t.Fatal(err)
	}
	// Crash after fairness publication but before the turn completion record.
	m.connection.Conversations = 1
	m.connection.LastConversationID = turn.Delivery.ID
	if err = m.saveConnection(); err != nil {
		t.Fatal(err)
	}
	code.running = false
	restarted, err := NewMasterComms(cfg, m.Client, code, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	active, err := restarted.Active()
	if err != nil || active == nil {
		t.Fatal(err)
	}
	if err = restarted.Advance(ctx, active); err != nil {
		t.Fatal(err)
	}
	if restarted.connection.Conversations != 1 || code.calls != 1 {
		t.Fatal("completion was accounted or submitted twice")
	}
}

type failingObservation struct {
	OpenCode
	fail bool
}

func (f *failingObservation) Observe(ctx context.Context, j Job) (Turn, error) {
	if f.fail {
		return Turn{}, errors.New("native observation unavailable")
	}
	return f.OpenCode.Observe(ctx, j)
}

func TestMasterObservationRecoveryResumesOnlySavedIntent(t *testing.T) {
	cfg, m, s, o, b, th, code := commsFixture(t)
	ctx := context.Background()
	f := &failingObservation{OpenCode: code, fail: true}
	m.Code = f
	_, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "question", RequestID: "observation-recovery"})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	turn, err := m.Next(ctx)
	if err != nil || turn == nil {
		t.Fatal(err)
	}
	if err = m.Advance(ctx, turn); err != nil {
		t.Fatal(err)
	}
	for range Defaults(cfg).Limits.MaxAttempts {
		if err = m.Advance(ctx, turn); err == nil {
			t.Fatal("observation failure hidden")
		}
	}
	if turn.Phase != "blocked" {
		t.Fatal("observation failures never enter durable recovery")
	}
	f.fail = false
	resolved, err := ResolveMasterConversation(ctx, cfg, m.Client, f, turn.Delivery.ID, turn.Job.SessionID, turn.Job.MessageID, "observe")
	if err != nil || resolved.Phase != "active" {
		t.Fatal("owned observation recovery failed", err)
	}
	if err = m.Advance(ctx, &resolved); err != nil {
		t.Fatal(err)
	}
	if code.calls != 1 || code.aborts != 0 {
		t.Fatal("observation recovery replayed or aborted the saved native turn")
	}
}
