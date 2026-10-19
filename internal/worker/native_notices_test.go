package worker

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func notifyNativeBlockers(t *testing.T, d *NativeDispatcher) error {
	t.Helper()
	n, ok := any(d).(interface{ NotifyBlockers(context.Context) error })
	if !ok {
		t.Fatal("blocked engineering cannot notify its owner")
	}
	return n.NotifyBlockers(t.Context())
}

func TestNativeBlockerNoticesAreDurableAndDoNotWakePeers(t *testing.T) {
	cfg, comms, store, owner, bot, thread, code := commsFixture(t)
	cfg.Master.BlockerRecipient = owner.ID
	if err := comms.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Now()}
	j := Job{ID: BodyHash("blocked-job"), Kind: "fix", TaskURL: "https://github.com/code/repo/issues/1", SubmissionState: "completed", Master: &MasterJobBinding{ThreadID: thread.ID}}
	d := &NativeDispatcher{config: cfg, master: comms.Client.(NativeEngineeringMaster), clock: clock, history: map[string]NativeExecution{j.ID: {Job: j, Phase: "blocked", Reason: "native_completed_without_result"}}}
	if err := notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	page, err := store.Messages(t.Context(), owner, thread.ID, 0)
	if err != nil || len(page.Messages) != 1 || len(page.Messages[0].Recipients) != 1 || page.Messages[0].Recipients[0] != owner.ID || !strings.Contains(page.Messages[0].Body, j.TaskURL) || !strings.Contains(page.Messages[0].Body, "Needs:") {
		t.Fatal("missing actionable owner-targeted blocker notice", page, err)
	}
	// A new dispatcher reads the same durable notification receipt.
	d = &NativeDispatcher{config: cfg, master: d.master, clock: clock, history: d.history}
	if err = notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	page, _ = store.Messages(t.Context(), owner, thread.ID, 0)
	if len(page.Messages) != 1 {
		t.Fatal("unchanged blocker spammed owner after restart", page)
	}
	d.history[j.ID] = NativeExecution{Job: j, Phase: "blocked", Reason: "native_observation_unavailable"}
	if err = notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	page, _ = store.Messages(t.Context(), owner, thread.ID, 0)
	if len(page.Messages) != 2 {
		t.Fatal("changed blocker not reported", page)
	}
	d.history[j.ID] = NativeExecution{Job: j, Phase: "completed"}
	if err = notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	page, _ = store.Messages(t.Context(), owner, thread.ID, 0)
	if len(page.Messages) != 3 || !strings.Contains(page.Messages[2].Body, "[COOP UNBLOCKED]") {
		t.Fatal("resolution not reported", page)
	}
	if err = notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	page, _ = store.Messages(t.Context(), owner, thread.ID, 0)
	pending, err := store.Pending(t.Context(), bot, c.PendingQuery{})
	if err != nil || len(page.Messages) != 3 || len(pending.Deliveries) != 0 || code.calls != 0 {
		t.Fatal("notices repeated or created agent conversation turns", page, pending, code.calls, err)
	}
}

type lostNoticeReply struct {
	NativeEngineeringMaster
	lost bool
	deny bool
}

func (m *lostNoticeReply) Send(ctx context.Context, in c.SendMessage) (c.MessageReceipt, error) {
	if m.deny {
		return c.MessageReceipt{}, c.ErrForbidden
	}
	r, err := m.NativeEngineeringMaster.Send(ctx, in)
	if err == nil && !m.lost {
		m.lost = true
		return c.MessageReceipt{}, c.ErrUnavailable
	}
	return r, err
}

func (m *lostNoticeReply) Team(ctx context.Context) ([]c.Presence, error) {
	return m.NativeEngineeringMaster.(interface {
		Team(context.Context) ([]c.Presence, error)
	}).Team(ctx)
}

func TestNativeBlockerNoticeLostReplyReusesRequestAfterRestart(t *testing.T) {
	cfg, comms, store, owner, _, thread, _ := commsFixture(t)
	cfg.Master.BlockerRecipient = owner.ID
	if err := comms.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	j := Job{ID: BodyHash("blocked"), Kind: "implement", TaskURL: "https://github.com/code/repo/issues/1", Master: &MasterJobBinding{ThreadID: thread.ID}}
	clock := &fakeClock{now: time.Now()}
	transport := &lostNoticeReply{NativeEngineeringMaster: comms.Client.(NativeEngineeringMaster)}
	d := &NativeDispatcher{config: cfg, master: transport, clock: clock, history: map[string]NativeExecution{j.ID: {Job: j, Phase: "blocked", Reason: "native_completed_without_result"}}}
	if err := notifyNativeBlockers(t, d); err == nil {
		t.Fatal("expected lost send response")
	}
	clock.now = clock.now.Add(2 * time.Minute)
	d = &NativeDispatcher{config: cfg, master: transport, clock: clock, history: d.history}
	transport.deny = true
	if err := notifyNativeBlockers(t, d); err == nil {
		t.Fatal("expected permission refusal after the uncertain response")
	}
	transport.deny = false
	clock.now = clock.now.Add(2 * time.Minute)
	if err := notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	page, err := store.Messages(t.Context(), owner, thread.ID, 0)
	if err != nil || len(page.Messages) != 1 {
		t.Fatal("unknown send response duplicated the owner notice", page, err)
	}
}

type noticeOnlyEngineering struct{ *NativeDispatcher }

func (d *noticeOnlyEngineering) Ready() bool { return false }

func TestNativeBlockerNoticeIsPublishedByWorkerTick(t *testing.T) {
	cfg, comms, store, owner, _, thread, _ := commsFixture(t)
	cfg.Master.BlockerRecipient = owner.ID
	j := Job{ID: BodyHash("held"), Kind: "fix", TaskURL: "https://github.com/code/repo/issues/1", Master: &MasterJobBinding{ThreadID: thread.ID}}
	d := &NativeDispatcher{config: cfg, master: comms.Client.(NativeEngineeringMaster), clock: RealClock{}, history: map[string]NativeExecution{j.ID: {Job: j, Phase: "blocked", Reason: "native_completed_without_result"}}}
	e := &Engine{Config: cfg, Store: NewVersionedFileStore(cfg.StateDir, 3), MasterRuntime: &MasterRuntime{Comms: comms, Engineering: &noticeOnlyEngineering{d}}}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d.noticeDone != nil {
		select {
		case err := <-d.noticeDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("background notice did not finish")
		}
	}
	page, err := store.Messages(t.Context(), owner, thread.ID, 0)
	if err != nil || len(page.Messages) != 1 {
		t.Fatal("worker tick did not publish blocker", page, err)
	}
}

func TestNativeBlockerNoticeWaitsForPersistentCatalogFailure(t *testing.T) {
	cfg, comms, store, owner, _, thread, _ := commsFixture(t)
	if err := comms.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	turn := MasterTurn{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, RestoreEpoch: 1,
		Delivery: c.Delivery{ID: "finished-delivery", MessageID: "finished-message"}, Message: c.Message{ID: "finished-message", ThreadID: thread.ID, Body: "done"}, BodySHA: BodyHash("done"), Phase: "completed"}
	if err := saveMasterTurn(cfg, &turn); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Now()}
	d := &NativeDispatcher{config: cfg, master: comms.Client.(NativeEngineeringMaster), clock: clock, catalogError: "unavailable", history: map[string]NativeExecution{}}
	if err := notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	page, _ := store.Messages(t.Context(), owner, thread.ID, 0)
	if len(page.Messages) != 0 {
		t.Fatal("transient catalog error spammed the thread", page)
	}
	clock.now = clock.now.Add(6 * time.Minute)
	if err := notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	page, _ = store.Messages(t.Context(), owner, thread.ID, 0)
	if len(page.Messages) != 1 || !strings.Contains(page.Messages[0].Body, "native_catalog_unavailable") {
		t.Fatal("persistent catalog outage was silent", page)
	}
	d.catalogError = ""
	if err := notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	page, _ = store.Messages(t.Context(), owner, thread.ID, 0)
	if len(page.Messages) != 2 || !strings.Contains(page.Messages[1].Body, "[COOP UNBLOCKED]") {
		t.Fatal("catalog recovery not reported", page)
	}
}

func TestNativeBlockerNoticeCoversHeldConversationsWithoutCopyingPrompts(t *testing.T) {
	cfg, comms, store, owner, _, thread, _ := commsFixture(t)
	cfg.Master.BlockerRecipient = owner.ID
	if err := comms.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	turn := MasterTurn{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, RestoreEpoch: 1,
		Delivery: c.Delivery{ID: "held-delivery", MessageID: "held-message"}, Message: c.Message{ID: "held-message", ThreadID: thread.ID, Body: "private-prompt-marker"}, BodySHA: BodyHash("private-prompt-marker"),
		Job: Job{Kind: "comms", SubmissionState: "completed"}, Phase: "blocked", Reason: "native_completed_without_reply"}
	if err := saveMasterTurn(cfg, &turn); err != nil {
		t.Fatal(err)
	}
	d := &NativeDispatcher{config: cfg, master: comms.Client.(NativeEngineeringMaster), clock: RealClock{}, history: map[string]NativeExecution{}}
	if err := notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	page, err := store.Messages(t.Context(), owner, thread.ID, 0)
	if err != nil || len(page.Messages) != 1 || !strings.Contains(page.Messages[0].Body, "native_completed_without_reply") || strings.Contains(page.Messages[0].Body, "private-prompt-marker") {
		t.Fatal("held conversation stayed silent or copied its prompt", page, err)
	}
}

type gatedNoticeMaster struct {
	NativeEngineeringMaster
	started chan struct{}
	release chan struct{}
}

func (m *gatedNoticeMaster) Send(ctx context.Context, in c.SendMessage) (c.MessageReceipt, error) {
	close(m.started)
	select {
	case <-m.release:
		return m.NativeEngineeringMaster.Send(ctx, in)
	case <-ctx.Done():
		return c.MessageReceipt{}, ctx.Err()
	}
}

func (m *gatedNoticeMaster) Team(ctx context.Context) ([]c.Presence, error) {
	return m.NativeEngineeringMaster.(interface {
		Team(context.Context) ([]c.Presence, error)
	}).Team(ctx)
}

func TestNativeBlockerNoticeNetworkingDoesNotHoldWorkerTick(t *testing.T) {
	cfg, comms, _, owner, _, thread, _ := commsFixture(t)
	cfg.Master.BlockerRecipient = owner.ID
	j := Job{ID: BodyHash("held"), Kind: "fix", TaskURL: "https://github.com/code/repo/issues/1", Master: &MasterJobBinding{ThreadID: thread.ID}}
	transport := &gatedNoticeMaster{NativeEngineeringMaster: comms.Client.(NativeEngineeringMaster), started: make(chan struct{}), release: make(chan struct{})}
	d := &NativeDispatcher{config: cfg, master: transport, clock: RealClock{}, history: map[string]NativeExecution{j.ID: {Job: j, Phase: "blocked", Reason: "native_completed_without_result"}}}
	e := &Engine{Config: cfg, Store: NewVersionedFileStore(cfg.StateDir, 3), MasterRuntime: &MasterRuntime{Comms: comms, Engineering: &noticeOnlyEngineering{d}}}
	done := make(chan error, 1)
	go func() { done <- e.Tick(t.Context()) }()
	select {
	case <-transport.started:
	case <-time.After(2 * time.Second):
		close(transport.release)
		t.Fatal("notice send did not start")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Error("notification network held the worker receipt loop")
	}
	close(transport.release)
	if d.noticeDone != nil {
		select {
		case err := <-d.noticeDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("background send did not finish")
		}
	} else {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("worker did not return")
		}
	}
}

func TestNativeBlockerNoticesRespectPausePlacementAndOwnerRouting(t *testing.T) {
	for _, scenario := range []string{"paused", "admission_paused", "epoch", "agent_recipient", "agent_alias"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, comms, store, owner, bot, thread, _ := commsFixture(t)
			cfg.Master.BlockerRecipient = owner.ID
			if err := comms.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			files := NewVersionedFileStore(cfg.StateDir, 3)
			switch scenario {
			case "paused":
				if err := files.SetPaused(true); err != nil {
					t.Fatal(err)
				}
			case "admission_paused":
				if err := files.SetAdmissionPaused(true); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				connection := comms.connection
				connection.Epoch++
				raw, _ := json.Marshal(connection)
				if err := atomicPrivate(filepath.Join(masterDir(cfg), "connection.json"), raw); err != nil {
					t.Fatal(err)
				}
			case "agent_recipient":
				cfg.Master.BlockerRecipient = bot.ID
			case "agent_alias":
				cfg.Master.BlockerRecipient = bot.Alias
			}
			j := Job{ID: BodyHash("blocked"), Kind: "fix", Master: &MasterJobBinding{ThreadID: thread.ID}}
			d := &NativeDispatcher{config: cfg, master: comms.Client.(NativeEngineeringMaster), clock: RealClock{}, history: map[string]NativeExecution{j.ID: {Job: j, Phase: "blocked", Reason: "native_completed_without_result"}}}
			err := notifyNativeBlockers(t, d)
			if (scenario == "epoch" || scenario == "agent_recipient" || scenario == "agent_alias") && err == nil {
				t.Fatal("unsafe notice was not rejected")
			}
			page, readErr := store.Messages(t.Context(), owner, thread.ID, 0)
			if readErr != nil || len(page.Messages) != 0 {
				t.Fatal("notice ignored pause/placement/owner routing", page, err, readErr)
			}
		})
	}
}

func TestNativeBlockerNoticeDefinitiveRejectionDoesNotHoldOtherThreads(t *testing.T) {
	cfg, comms, store, owner, _, thread, _ := commsFixture(t)
	cfg.Master.BlockerRecipient = owner.ID
	if err := comms.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Now()}
	bad := Job{ID: BodyHash("denied"), Kind: "fix", Master: &MasterJobBinding{ThreadID: "th_00000000"}}
	good := Job{ID: BodyHash("allowed"), Kind: "fix", Master: &MasterJobBinding{ThreadID: thread.ID}}
	history := map[string]NativeExecution{bad.ID: {Job: bad, Phase: "blocked", Reason: "native_completed_without_result"}, good.ID: {Job: good, Phase: "blocked", Reason: "native_completed_without_result"}}
	d := &NativeDispatcher{config: cfg, master: comms.Client.(NativeEngineeringMaster), clock: clock, history: history}
	if err := notifyNativeBlockers(t, d); err == nil {
		t.Fatal("expected rejected thread")
	}
	clock.now = clock.now.Add(2 * time.Minute)
	d = &NativeDispatcher{config: cfg, master: d.master, clock: clock, history: history}
	if err := notifyNativeBlockers(t, d); err != nil {
		t.Fatal("rejected thread starved the accessible thread", err)
	}
	page, err := store.Messages(t.Context(), owner, thread.ID, 0)
	if err != nil || len(page.Messages) != 1 {
		t.Fatal("accessible blocker notice missing", page, err)
	}
}

func TestNativeBlockerNoticeRejectedRecipientCanBeCorrected(t *testing.T) {
	cfg, comms, store, owner, _, thread, _ := commsFixture(t)
	cfg.Master.BlockerRecipient = "missing-owner"
	if err := comms.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Now()}
	j := Job{ID: BodyHash("blocked"), Kind: "fix", Master: &MasterJobBinding{ThreadID: thread.ID}}
	d := &NativeDispatcher{config: cfg, master: comms.Client.(NativeEngineeringMaster), clock: clock, history: map[string]NativeExecution{j.ID: {Job: j, Phase: "blocked", Reason: "native_completed_without_result"}}}
	if err := notifyNativeBlockers(t, d); err == nil {
		t.Fatal("expected rejected recipient")
	}
	cfg.Master.BlockerRecipient = owner.ID
	clock.now = clock.now.Add(2 * time.Minute)
	d = &NativeDispatcher{config: cfg, master: d.master, clock: clock, history: d.history}
	if err := notifyNativeBlockers(t, d); err != nil {
		t.Fatal("corrected recipient still held by rejected notice", err)
	}
	page, err := store.Messages(t.Context(), owner, thread.ID, 0)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Recipients[0] != owner.ID {
		t.Fatal("corrected owner did not receive notice", page, err)
	}
}

func TestNativeDependencyNoticeChangesWhenRecoveryNeedsAttention(t *testing.T) {
	cfg, comms, store, owner, _, thread, _ := commsFixture(t)
	cfg.Master.BlockerRecipient = owner.ID
	if err := comms.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	j := Job{ID: BodyHash("waiting"), Kind: "implement", TaskURL: "https://github.com/code/repo/issues/1", Master: &MasterJobBinding{ThreadID: thread.ID}}
	d := &NativeDispatcher{config: cfg, master: comms.Client.(NativeEngineeringMaster), clock: RealClock{}, history: map[string]NativeExecution{j.ID: {Job: j, Phase: "blocked", Reason: "native_dependency_wait"}}}
	if err := notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	x := d.history[j.ID]
	x.Job.ReconcileFailures = Defaults(cfg).Limits.MaxAttempts
	x.Job.ReconcileError = "transport_failed"
	d.history[j.ID] = x
	if err := notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	page, err := store.Messages(t.Context(), owner, thread.ID, 0)
	if err != nil || len(page.Messages) != 2 || !strings.Contains(page.Messages[1].Body, "native_dependency_retry") {
		t.Fatal("transport retry was hidden behind the original dependency wait", page, err)
	}
	x.Job.ReconcileError = "continuation_ownership_unknown"
	d.history[j.ID] = x
	if err := notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	page, err = store.Messages(t.Context(), owner, thread.ID, 0)
	if err != nil || len(page.Messages) != 3 || !strings.Contains(page.Messages[2].Body, "native_dependency_recovery_required") || strings.Contains(page.Messages[2].Body, "no owner action") {
		t.Fatal("exhausted recovery still advertised automatic continuation", page, err)
	}
	if err := notifyNativeBlockers(t, d); err != nil {
		t.Fatal(err)
	}
	page, _ = store.Messages(t.Context(), owner, thread.ID, 0)
	if len(page.Messages) != 3 {
		t.Fatal("unchanged recovery spammed owner")
	}
}
