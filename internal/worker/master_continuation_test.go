package worker

import (
	"context"
	"github.com/sarcasticbird/coop/internal/master"
	"path/filepath"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type continuationStoreClient struct {
	store *master.Store
	actor c.Principal
}

func (v continuationStoreClient) MasterID() string { id, _ := v.store.Identity(); return id }
func (v continuationStoreClient) Pending(ctx context.Context, q c.PendingQuery) (c.DeliveryPage, error) {
	return v.store.Pending(ctx, v.actor, q)
}
func (v continuationStoreClient) Message(ctx context.Context, id string) (c.Message, error) {
	return v.store.Message(ctx, v.actor, id)
}
func (v continuationStoreClient) Continuation(ctx context.Context, id string) (c.Message, error) {
	return v.store.Continuation(ctx, v.actor, id)
}
func (v continuationStoreClient) MessageStatus(ctx context.Context, id string) (c.MessageReceipt, error) {
	return v.store.MessageStatus(ctx, v.actor, id)
}
func (v continuationStoreClient) OwnDelivery(ctx context.Context, id string) (c.Delivery, error) {
	return v.store.OwnDelivery(ctx, v.actor, id)
}
func (v continuationStoreClient) SetDelivery(ctx context.Context, u c.DeliveryUpdate) error {
	return v.store.SetDelivery(ctx, v.actor, u)
}
func (v continuationStoreClient) Heartbeat(ctx context.Context, h c.Heartbeat) error {
	return v.store.Heartbeat(ctx, v.actor, h)
}
func (v continuationStoreClient) PublishReply(ctx context.Context, in c.PublishReply) (c.MessageReceipt, error) {
	return v.store.PublishReply(ctx, v.actor, in)
}
func continuationRuntimeFixture(t *testing.T) (*Engine, *master.Store, c.Principal, c.Principal, c.MessageReceipt, *fakeOpenCode) {
	t.Helper()
	cfg, native, g, code := dispatchFixture(t)
	cfg = Defaults(cfg)
	code.running = false
	s, err := master.OpenStore(t.Context(), master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.db"), MasterID: cfg.Master.MasterID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	o := c.Principal{ID: "owner", Alias: "owner", Kind: "owner", Engagements: []string{"coop"}, Capabilities: []string{"read", "send", "manage"}}
	b := c.Principal{ID: cfg.Master.AgentID, Alias: "bird", Kind: "agent", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}, GitHubBotID: cfg.Self.ID}
	if err = s.Configure(t.Context(), []c.Principal{o, b}, []c.Placement{{AgentID: b.ID, HostID: cfg.Master.HostID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation}}); err != nil {
		t.Fatal(err)
	}
	th, err := s.CreateThread(t.Context(), o, c.CreateThread{Title: "Work", Engagement: "coop", Members: []string{o.ID, b.ID}, RequestID: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	native.authority.Revision.Proposal.ThreadID = th.ID
	g.catalog.Goals[0] = native.authority
	native.threads = append(native.threads, c.Thread{ID: th.ID, Engagement: "coop", Members: []string{"author", "peer", "qa"}})
	v := NativeCandidates(cfg, g.catalog, nil, time.Now())[0]
	x := NativeExecution{Version: 3, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Goal: v.Goal.Revision.Ref, Job: v.Job, Binding: &v.Task.Binding, Phase: "blocked", Reason: "native_completed_without_result"}
	x.Job.Phase, x.Job.SubmissionState = "completed", "completed"
	x.Job.TerminalAt = time.Now()
	x.Job.SessionID, x.Job.MessageID = "ses_saved", "msg_saved"
	x.Job.Checkout = filepath.Join(cfg.WorkRoot, "saved")
	if err = saveNativeExecution(cfg, x); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(cfg, native, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	d.catalog, d.nextRefresh = g.catalog, time.Now().Add(time.Hour)
	d.nextNoticeCheck = time.Now().Add(time.Hour)
	source, err := s.Send(t.Context(), o, c.SendMessage{ThreadID: th.ID, Body: "Findings ready", RequestID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := s.ContinueTask(t.Context(), o, c.ContinueTaskRequest{ThreadID: th.ID, AgentID: b.ID, Target: coordinatorTarget(cfg, x), SourceMessageID: source.MessageID, Body: "Continue approved work", RequestID: "continue"})
	if err != nil {
		t.Fatal(err)
	}
	cl := continuationStoreClient{s, b}
	comms := &MasterComms{Config: cfg, Client: cl, Code: code, Clock: RealClock{}, connection: masterConnectionState{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Epoch: 1}}
	e := &Engine{Config: cfg, Store: NewVersionedFileStore(cfg.StateDir, 3), OpenCode: code, MasterRuntime: &MasterRuntime{Comms: comms, Engineering: d}}
	return e, s, o, b, receipt, code
}
func TestMasterContinuationUsesEngineeringSlotAndTruthfulReceipt(t *testing.T) {
	e, s, o, _, receipt, code := continuationRuntimeFixture(t)
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	finishContinuationCheckForTest(t, e)
	status, err := s.MessageStatus(t.Context(), o, receipt.MessageID)
	if err != nil || status.Deliveries[0].State != "admitted" || code.calls != 0 {
		t.Fatal("control became chat or claimed resume prematurely", status, code.calls, err)
	}
	if active, err := e.MasterRuntime.Comms.Active(); err != nil || active != nil {
		t.Fatal("control owns conversation slot", err)
	}
	d := e.MasterRuntime.Engineering.(*NativeDispatcher)
	// Simulate a worker crash after checkpoint, before model submission or receipt.
	restarted, err := NewNativeDispatcher(e.Config, d.master, d.github, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	restarted.catalog, restarted.nextRefresh = d.catalog, time.Now().Add(time.Hour)
	restarted.nextNoticeCheck = time.Now().Add(time.Hour)
	e.MasterRuntime.Engineering = restarted
	if err = e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = consumeNativeStep(t, restarted); err != nil {
		t.Fatal(err)
	}
	if err = e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, err = s.MessageStatus(t.Context(), o, receipt.MessageID)
	if err != nil || status.Deliveries[0].State != "resumed" || code.calls != 1 || status.Deliveries[0].Intent.MessageID == "msg_saved" {
		t.Fatal("control lost checkpoint or duplicated model", status, code.calls, err)
	}
}
func TestMasterContinuationRefusesRevokedControlWithoutFreezingConversation(t *testing.T) {
	e, s, o, b, receipt, code := continuationRuntimeFixture(t)
	// Revoking send invalidates even an owner's queued control.
	o.Capabilities = []string{"read", "manage"}
	if err := s.Configure(t.Context(), []c.Principal{o}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, err := s.MessageStatus(t.Context(), o, receipt.MessageID)
	if err != nil || status.Deliveries[0].State != "refused" || code.calls != 0 || e.MasterRuntime.Comms.Recovery {
		t.Fatal("revoked control ran or froze worker", status, err)
	}
	// Restore normal communication and prove the refused control cannot starve it.
	o.Capabilities = append(o.Capabilities, "send")
	if err = s.Configure(t.Context(), []c.Principal{o}, nil); err != nil {
		t.Fatal(err)
	}
	_, err = s.Send(t.Context(), o, c.SendMessage{ThreadID: e.MasterRuntime.Engineering.(*NativeDispatcher).history[receiptTargetID(e)].Job.Master.ThreadID, Recipients: []string{b.ID}, Body: "status?", RequestID: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Tick(t.Context()); err != nil || code.calls != 1 || code.last.Kind != "master-conversation" {
		t.Fatal("refused control blocked ordinary chat", err)
	}
}
func receiptTargetID(e *Engine) string {
	for id := range e.MasterRuntime.Engineering.(*NativeDispatcher).history {
		return id
	}
	return ""
}

func finishContinuationCheckForTest(t *testing.T, e *Engine) {
	t.Helper()
	r := e.MasterRuntime
	if r.continuationStep == nil {
		t.Fatal("continuation check was not scheduled")
	}
	select {
	case result := <-r.continuationStep:
		ready := make(chan continuationCheck, 1)
		ready <- result
		r.continuationStep = ready
		if _, err := r.tickContinuation(t.Context()); err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("continuation check did not finish")
	}
}

type slowContinuationCatalog struct {
	NativeCatalogReader
	entered chan struct{}
	release chan struct{}
}

func (g slowContinuationCatalog) NativeTaskSnapshot(ctx context.Context, cfg Config, m NativeMaster, j Job, refs ...string) (NativeCatalog, error) {
	close(g.entered)
	select {
	case <-g.release:
		return g.NativeCatalogReader.NativeTaskSnapshot(ctx, cfg, m, j, refs...)
	case <-ctx.Done():
		return NativeCatalog{}, ctx.Err()
	}
}
func TestMasterContinuationSlowValidationKeepsReceiptsResponsive(t *testing.T) {
	e, s, o, b, control, code := continuationRuntimeFixture(t)
	d := e.MasterRuntime.Engineering.(*NativeDispatcher)
	gate := slowContinuationCatalog{NativeCatalogReader: d.github, entered: make(chan struct{}), release: make(chan struct{})}
	d.github = gate
	released := false
	defer func() {
		if !released {
			close(gate.release)
		}
	}()
	started := time.Now()
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("continuation validation blocked receipt loop")
	}
	select {
	case <-gate.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("validation was not scheduled")
	}
	message, err := s.Message(t.Context(), o, control.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := s.Send(t.Context(), o, c.SendMessage{ThreadID: message.ThreadID, Recipients: []string{b.ID}, Body: "Can you give an update?", RequestID: "during-validation"})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, err := s.MessageStatus(t.Context(), o, chat.MessageID)
	if err != nil || status.Deliveries[0].State != "received" || code.calls != 0 {
		t.Fatal("slow validation starved receipt or used model slot", status, err)
	}
	close(gate.release)
	released = true
	finishContinuationCheckForTest(t, e)
}

func TestMasterContinuationRetiresCancelledControlAfterMembershipRevocation(t *testing.T) {
	e, s, o, b, receipt, _ := continuationRuntimeFixture(t)
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	finishContinuationCheckForTest(t, e)
	msg, err := s.Message(t.Context(), o, receipt.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetMembers(t.Context(), o, c.SetMembers{ThreadID: msg.ThreadID, ExpectedMembers: []string{o.ID, b.ID}, Members: []string{o.ID}, RequestID: "remove-bird"}); err != nil {
		t.Fatal(err)
	}
	if _, err = e.MasterRuntime.tickContinuation(t.Context()); err != nil {
		t.Fatal("cancelled control stalled worker after membership revocation", err)
	}
	if handled, err := e.MasterRuntime.tickContinuation(t.Context()); err != nil || handled {
		t.Fatal("cancelled control remained in worker queue", handled, err)
	}
	th, err := s.CreateThread(t.Context(), o, c.CreateThread{Title: "Other work", Engagement: "coop", Members: []string{o.ID, b.ID}, RequestID: "other-thread"})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := s.Send(t.Context(), o, c.SendMessage{ThreadID: th.ID, Recipients: []string{b.ID}, Body: "status?", RequestID: "other-chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.MasterRuntime.Comms.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, err := s.MessageStatus(t.Context(), o, chat.MessageID)
	if err != nil || status.Deliveries[0].State != "received" {
		t.Fatal("cancelled control starved unrelated receipts", status, err)
	}
}

func TestContinuationRetriesGitHubServerFailuresOnly(t *testing.T) {
	for _, tc := range []struct {
		status string
		retry  bool
	}{{"500 Internal Server Error", true}, {"502 Bad Gateway", true}, {"400 Bad Request", false}, {"403 Forbidden", false}, {"404 Not Found", false}, {"422 Unprocessable Entity", false}} {
		t.Run(tc.status, func(t *testing.T) {
			runner := &fakeCommands{respond: func(_, _, _ string, _ []byte) (CommandResult, error) {
				return CommandResult{Stdout: []byte("HTTP/2.0 " + tc.status + "\r\n\r\n{}"), ExitCode: 1}, nil
			}}
			_, _, err := NewGitHubClient(runner).api(t.Context(), "code", "GET", "repos/code/repo", nil)
			if err == nil || continuationRetryable(err) != tc.retry {
				t.Fatal("GitHub error classified incorrectly", err)
			}
		})
	}
}
