package worker

import (
	"testing"
	"time"
)

func TestNativeScheduleRetainsEligibilityAcrossCatalogAndRestart(t *testing.T) {
	cfg, m, g, code := dispatchFixture(t)
	clock := &fakeClock{now: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}
	d, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	first, err := d.candidates(g.catalog)
	if err != nil || len(first) != 1 {
		t.Fatal(err)
	}
	eligible := first[0].Job.FirstEligibleAt
	clock.now = clock.now.Add(time.Hour)
	next, err := d.candidates(g.catalog)
	if err != nil || next[0].Job.FirstEligibleAt != eligible {
		t.Fatal("catalog refresh reset engineering age", err)
	}
	restarted, err := NewNativeDispatcher(cfg, m, g, code, &fakeCheckouts{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	next, err = restarted.candidates(g.catalog)
	if err != nil || next[0].Job.FirstEligibleAt != eligible {
		t.Fatal("restart reset engineering age", err)
	}
}
