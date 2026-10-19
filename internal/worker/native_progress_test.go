package worker

import (
	c "github.com/sarcasticbird/coop/internal/coordination"
	"strings"
	"testing"
	"time"
)

func TestNativeProgressDistinguishesRecoveryAndTaskWait(t *testing.T) {
	cfg, m, g, code, x := nativeTerminalWaitFixture(t)
	clock := &fakeClock{now: time.Now()}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	p := d.Progress()
	if p.Reason != "native_task_wait" || len(p.Blocked) != 1 || p.Blocked[0].JobID != x.Job.ID {
		t.Fatal("task wait hidden", p)
	}
	x.Reason = "native_observation_unavailable"
	d.history[x.Job.ID] = x
	if err = d.updateRecoveryHold(); err != nil {
		t.Fatal(err)
	}
	p = d.Progress()
	if p.Reason != "native_recovery_required" {
		t.Fatal("global recovery hidden", p)
	}
}
func TestNativeProgressReportsAdmissionStallWithoutAbortingCoding(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	clock := &fakeClock{now: time.Now()}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	v := NativeCandidates(cfg, g.catalog, nil, clock.Now())[0]
	x := NativeExecution{Job: v.Job, Phase: "selected", SelectedAt: clock.Now().Add(-6 * time.Minute)}
	d.active = &x
	if p := d.Progress(); p.Reason != "native_admission_stalled" || p.Current == nil {
		t.Fatal("old preparation hidden", p)
	}
	x.Phase = "active"
	x.Job.Deadline = clock.Now().Add(time.Hour)
	if p := d.Progress(); p.Reason != "" || p.Deadline != x.Job.Deadline || code.aborts != 0 {
		t.Fatal("long running code treated as stall", p)
	}
	x.Job.Deadline = clock.Now().Add(-time.Minute)
	if p := d.Progress(); p.Reason != "native_turn_overdue" || code.aborts != 0 {
		t.Fatal("overdue turn not reported separately", p)
	}
}
func TestNativeRecoveryReachesMasterTeamAndWorkerStatus(t *testing.T) {
	cfg, comms, s, owner, _, _, code := commsFixture(t)
	dc, m, g, engineeringCode, x := nativeTerminalWaitFixture(t)
	x.Reason = "native_observation_unavailable"
	if err := saveNativeExecution(dc, x); err != nil {
		t.Fatal(err)
	}
	d, err := NewNativeDispatcher(dc, m, g, engineeringCode, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Config: cfg, Store: NewVersionedFileStore(cfg.StateDir, 3), OpenCode: code, MasterRuntime: &MasterRuntime{Comms: comms, Engineering: d}}
	if err = e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	team, err := s.Team(t.Context(), owner)
	if err != nil || len(team) != 1 || team[0].State != "recovering" || !strings.Contains(team[0].Reason, "native_observation_unavailable") {
		t.Fatal("heartbeat falsely reported idle", team, err)
	}
	status, err := ReadStatus(cfg.StateDir, time.Now())
	if err != nil || status.Native == nil || status.State != "blocked" || len(status.Native.Blocked) != 1 {
		t.Fatal("worker status hid recovery", status, err)
	}
}
func TestNativePendingMessageStallIgnoresActiveAndPausedWork(t *testing.T) {
	cfg, comms, _, _, _, _, _ := commsFixture(t)
	now := time.Now()
	turn := MasterTurn{Version: 1, MasterID: cfg.Master.MasterID, InstanceID: cfg.Master.InstanceID, Generation: cfg.Master.Generation, Delivery: c.Delivery{ID: "waiting", MessageID: "msg-waiting"}, Message: c.Message{ID: "msg-waiting", Body: "question", CreatedAt: now.Add(-6 * time.Minute)}, BodySHA: BodyHash("question"), Phase: "received"}
	if err := saveMasterTurn(cfg, &turn); err != nil {
		t.Fatal(err)
	}
	if reason, err := comms.pendingStall(now); err != nil || reason != "native_message_stalled" {
		t.Fatal("old pending message not reported", reason, err)
	}
	comms.Busy = true
	if reason, err := comms.pendingStall(now); err != nil || reason != "" {
		t.Fatal("active work falsely stalled", reason, err)
	}
	comms.Busy = false
	comms.Paused = true
	if reason, err := comms.pendingStall(now); err != nil || reason != "" {
		t.Fatal("pause falsely stalled", reason, err)
	}
}

func TestNativeProgressPrioritizesRecoveryBeforeTruncation(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		x := NativeExecution{Job: Job{ID: stringID(i)}, Phase: "blocked", Reason: "native_dependency_wait"}
		d.history[x.Job.ID] = x
	}
	x := d.history["19"]
	x.Job.ReconcileFailures = Defaults(cfg).Limits.MaxAttempts
	d.history["19"] = x
	p := d.Progress()
	if p.Reason != "native_dependency_recovery_required" || p.PrimaryJobID != "19" || p.Blocked[0].JobID != "19" || len(p.Blocked) != 16 {
		t.Fatal("healthy waits hid required recovery", p)
	}
}
