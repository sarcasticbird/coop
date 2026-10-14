package worker

import (
	"testing"
	"time"
)

func TestWakeAcceptanceRequiresHeldWorkerLockAndFreshHeartbeat(t *testing.T) {
	c, _ := wakeFixture(t)
	store := NewFileStore(c.StateDir)
	release, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	now := time.Now().UTC()
	status := Status{Enabled: true, WakeEnabled: true, State: "idle", Heartbeat: now}
	if err := store.WriteStatus(status); err != nil {
		t.Fatal(err)
	}
	if err := WorkerAcceptingWake(c.StateDir, now); err != nil {
		t.Fatal(err)
	}
	status.Heartbeat = now.Add(-3 * time.Minute)
	if err := store.WriteStatus(status); err != nil {
		t.Fatal(err)
	}
	if err := WorkerAcceptingWake(c.StateDir, now); err == nil {
		t.Fatal("stale worker accepted")
	}
	status.Heartbeat = now
	if err := store.WriteStatus(status); err != nil {
		t.Fatal(err)
	}
	release()
	if err := WorkerAcceptingWake(c.StateDir, now); err == nil {
		t.Fatal("stopped worker with fresh status accepted")
	}
}
