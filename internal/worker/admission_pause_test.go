package worker

import (
	"context"
	"testing"
)

type admissionStore struct {
	*memoryStore
	draining bool
}

func (s *admissionStore) AdmissionPaused() (bool, error) { return s.draining, nil }
func TestAdmissionPauseLeavesActiveTurnAndBlocksNewWork(t *testing.T) {
	e, _, code, clock, store := engineFixture(t)
	wrapped := &admissionStore{memoryStore: store}
	e.Store = wrapped
	code.running = true
	tick(t, e, clock)
	wrapped.draining = true
	tick(t, e, clock)
	if code.aborts != 0 || code.calls != 1 {
		t.Fatal("admission pause aborted or resubmitted active work")
	}
	idle, _, idleCode, idleClock, idleStore := engineFixture(t)
	idle.Store = &admissionStore{memoryStore: idleStore, draining: true}
	tick(t, idle, idleClock)
	if idleCode.calls != 0 {
		t.Fatal("admission pause allowed new work")
	}
}

type admissionPausingCheckout struct {
	fakeCheckouts
	store *admissionStore
}

func (c *admissionPausingCheckout) Prepare(ctx context.Context, j Job) (Job, error) {
	c.store.draining = true
	return c.fakeCheckouts.Prepare(ctx, j)
}
func TestAdmissionPauseRecheckedBeforeSubmission(t *testing.T) {
	e, _, code, clock, store := engineFixture(t)
	wrapped := &admissionStore{memoryStore: store}
	e.Store = wrapped
	e.Checkouts = &admissionPausingCheckout{store: wrapped}
	tick(t, e, clock)
	if code.calls != 0 {
		t.Fatal("preparation race crossed admission pause")
	}
}
func TestAdmissionPausePersistsWithoutClearingLegacyPause(t *testing.T) {
	s := storeFixture(t)
	if err := s.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAdmissionPaused(true); err != nil {
		t.Fatal(err)
	}
	if paused, err := NewFileStore(s.Dir).AdmissionPaused(); err != nil || !paused {
		t.Fatal("admission pause not durable")
	}
	if err := s.SetAdmissionPaused(false); err != nil {
		t.Fatal(err)
	}
	if paused, err := s.Paused(); err != nil || !paused {
		t.Fatal("admission resume cleared legacy interrupting pause")
	}
}
