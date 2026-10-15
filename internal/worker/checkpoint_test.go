package worker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Model both a failure before replacement and an ambiguous error after it.
type checkpointStore struct {
	Store
	fail    func(State) bool
	commit  bool
	loadErr error
}

func (s *checkpointStore) Load() (State, error) {
	if s.loadErr != nil {
		return State{}, s.loadErr
	}
	return s.Store.Load()
}

func (s *checkpointStore) Save(state State) error {
	if s.fail == nil || !s.fail(state) {
		return s.Store.Save(state)
	}
	s.fail = nil
	if s.commit {
		if err := s.Store.Save(state); err != nil {
			return err
		}
	}
	return errors.New("injected checkpoint failure")
}

func TestCheckpointSubmissionFailures(t *testing.T) {
	for _, post := range []bool{false, true} {
		for _, commit := range []bool{false, true} {
			for _, lost := range []bool{false, true} {
				t.Run(fmt.Sprintf("post=%t/commit=%t/lost=%t", post, commit, lost), func(t *testing.T) {
					e, g, code, clock, _ := engineFixture(t)
					g.snapshot.Cursor = map[string]string{}
					code.running, code.lost = true, lost
					store := &checkpointStore{Store: NewFileStore(filepath.Join(t.TempDir(), "state")), commit: commit}
					store.fail = func(s State) bool {
						for _, j := range s.Jobs {
							if j.Phase == "active" && (j.SubmissionState != "submitting") == post {
								return true
							}
						}
						return false
					}
					e.Store = store
					if err := e.Tick(context.Background()); !errors.Is(err, errCheckpoint) {
						t.Fatalf("checkpoint failure = %v", err)
					}
					durable, err := store.Load()
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(e.state, durable) {
						t.Fatal("live state differs from reloaded checkpoint")
					}
					wantCalls := 0
					if post {
						wantCalls = 1
					}
					for range 3 {
						clock.now = clock.now.Add(e.Config.Limits.TurnLimit)
						if err := e.Tick(context.Background()); !errors.Is(err, errCheckpoint) {
							t.Fatalf("failed engine resumed: %v", err)
						}
					}
					if code.calls != wantCalls || code.aborts != 0 || e.status.WaitReason != "checkpoint_failed" {
						t.Fatalf("unsafe continuation: calls=%d aborts=%d status=%+v", code.calls, code.aborts, e.status)
					}
					// A restart must preserve durable intent, not blindly replay it.
					clock.now = clock.now.Add(-3 * e.Config.Limits.TurnLimit)
					restarted := &Engine{Config: e.Config, GitHub: g, Store: store, Checkouts: e.Checkouts, OpenCode: code, Clock: clock}
					tick(t, restarted, clock)
					if !post && !commit {
						wantCalls = 1
					}
					if code.calls != wantCalls || restarted.state.InstanceID != durable.InstanceID {
						t.Fatal("restart replayed intent or replaced worker identity")
					}
					if post {
						j, ok := restarted.active()
						if !ok || j.MessageID != code.last.MessageID || j.SessionID != code.last.SessionID {
							t.Fatal("restart lost accepted/unknown turn identity")
						}
					}
				})
			}
		}
	}
}

func TestCheckpointRestoresWholeLedger(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprint(commit), func(t *testing.T) {
			e, _, _, clock, _ := engineFixture(t)
			store := &checkpointStore{Store: NewFileStore(filepath.Join(t.TempDir(), "state")), commit: commit}
			e.Store = store
			if err := e.initialize(); err != nil {
				t.Fatal(err)
			}
			store.fail = func(State) bool { return true }
			e.state.Attempts["binding"] = 3
			e.state.ReviewCycles["task"] = 2
			e.state.Seen["review"] = "counted"
			e.state.Sessions["binding"] = SessionBinding{SessionID: "ses_saved"}
			e.state.Cursor["cursor"] = "new"
			e.state.SnapshotCache.Complete = true
			e.state.LastProposalAt = clock.Now()
			j := Job{ID: BodyHash("job"), Phase: "blocked"}
			if err := e.saveJob(j); !errors.Is(err, errCheckpoint) {
				t.Fatal(err)
			}
			durable, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(e.state, durable) {
				t.Fatal("partial ledger rollback")
			}
			j.Phase = "completed"
			if err := e.saveJob(j); !errors.Is(err, errCheckpoint) {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(e.state, durable) {
				t.Fatal("latched save mutated live state")
			}
			if err := e.Run(context.Background()); !errors.Is(err, errCheckpoint) {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckpointReloadFailureStopsExecution(t *testing.T) {
	e, _, code, clock, memory := engineFixture(t)
	store := &checkpointStore{Store: memory}
	e.Store = store
	if err := e.initialize(); err != nil {
		t.Fatal(err)
	}
	store.fail = func(State) bool { return true }
	store.loadErr = errors.New("unreadable checkpoint")
	if err := e.save(); !errors.Is(err, errCheckpoint) || !errors.Is(err, store.loadErr) {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(time.Hour)
	if err := e.Tick(context.Background()); !errors.Is(err, errCheckpoint) {
		t.Fatal(err)
	}
	if code.calls != 0 || code.aborts != 0 {
		t.Fatal("unreadable state permitted execution")
	}
}

func TestCheckpointInitializationFailureDoesNotSkipPersistence(t *testing.T) {
	e, _, code, _, memory := engineFixture(t)
	store := &checkpointStore{Store: memory, fail: func(State) bool { return true }}
	e.Store = store
	if err := e.Tick(context.Background()); !errors.Is(err, errCheckpoint) {
		t.Fatal(err)
	}
	if e.initialized {
		t.Fatal("failed initial checkpoint marked engine initialized")
	}
	if err := e.Tick(context.Background()); !errors.Is(err, errCheckpoint) {
		t.Fatal(err)
	}
	if code.calls != 0 {
		t.Fatal("failed initialization permitted dispatch")
	}
}

type checkpointWakeStore struct {
	WakeStore
	post, commit, failed bool
}

func (s *checkpointWakeStore) SaveReceipt(v WakeReceipt) error {
	if s.failed || v.Phase != "running" || (v.Job.SubmissionState != "submitting") != s.post {
		return s.WakeStore.SaveReceipt(v)
	}
	s.failed = true
	if s.commit {
		if err := s.WakeStore.SaveReceipt(v); err != nil {
			return err
		}
	}
	return errors.New("injected wake checkpoint failure")
}

func TestWakeCheckpointSubmissionFailures(t *testing.T) {
	for _, post := range []bool{false, true} {
		for _, commit := range []bool{false, true} {
			for _, restart := range []bool{false, true} {
				t.Run(fmt.Sprintf("post=%t/commit=%t/restart=%t", post, commit, restart), func(t *testing.T) {
					e, code, clock, store := wakeEngineFixture(t)
					q, err := QueuePing(e.Config, store, PingInput{Prompt: "checkpoint regression"}, clock.Now())
					if err != nil {
						t.Fatal(err)
					}
					code.running = true
					e.Wakes.Store = &checkpointWakeStore{WakeStore: store, post: post, commit: commit}
					if err := e.Wakes.Tick(context.Background(), Snapshot{Complete: true}); err == nil {
						t.Fatal("checkpoint error hidden")
					}
					wantCalls := 0
					if post {
						wantCalls = 1
					}
					if code.calls != wantCalls {
						t.Fatalf("calls=%d", code.calls)
					}
					before, err := store.Receipt(q.ID)
					if err != nil {
						t.Fatal(err)
					}
					w := e.Wakes
					if restart {
						w = NewWakeCoordinator(e.Config, NewWakeStore(store.Dir), e.GitHub, code, clock)
					}
					clock.now = clock.now.Add(e.Config.OpenCode.ReconcileInterval)
					if err := w.Tick(context.Background(), Snapshot{Complete: true}); err != nil {
						t.Fatal(err)
					}
					after, err := store.Receipt(q.ID)
					if err != nil {
						t.Fatal(err)
					}
					if !post && !commit {
						wantCalls = 1
					}
					if code.calls != wantCalls || code.aborts != 0 {
						t.Fatal("wake checkpoint replayed or aborted a turn")
					}
					if before.Phase == "running" && (after.Job.MessageID != before.Job.MessageID || after.Job.SessionID != before.Job.SessionID) {
						t.Fatal("wake lost durable intent identity")
					}
				})
			}
		}
	}
}
