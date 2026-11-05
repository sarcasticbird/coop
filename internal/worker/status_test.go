package worker

import (
	"testing"
	"time"
)

func TestStatusCannotCallStaleMonitoringHealthy(t *testing.T) {
	s := storeFixture(t)
	now := time.Now().UTC()
	if err := s.WriteStatus(Status{Name: "builder", Enabled: true, State: "idle", ServerHealthy: true, Heartbeat: now.Add(-3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStatus(s.Dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "stale" || status.ServerHealthy {
		t.Fatal("stale monitoring appeared healthy")
	}
}
