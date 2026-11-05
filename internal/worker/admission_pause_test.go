package worker

import "testing"

type admissionStore struct {
	*memoryStore
	draining bool
}

func (s *admissionStore) AdmissionPaused() (bool, error) { return s.draining, nil }

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
