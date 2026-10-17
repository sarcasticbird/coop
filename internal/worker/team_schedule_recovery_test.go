package worker

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Load directly because the v2 runtime initialization gate is still closed.
func reloadTeamSchedule(t *testing.T, c Config, dir string) *Engine {
	t.Helper()
	store := NewVersionedFileStore(dir, 2)
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{Config: c, Store: store, state: state}
}

func TestTeamScheduleRecoverySaveJobAccounting(t *testing.T) {
	for _, kind := range []string{"review", "fix", "deliver", "implement", "plan", "qa"} {
		for _, condition := range []string{"completed", "unsubmitted", "unbound", "v1", "blocked", "paused", "waiting", "active"} {
			// QA has no historical v1 job representation.
			if kind == "qa" && (condition == "unbound" || condition == "v1") {
				continue
			}
			t.Run(kind+"/"+condition, func(t *testing.T) {
				e, _, _, j := teamEngineFixture(t, false, "review")
				j.Kind = kind
				binding := *j.Binding
				j.Binding = &binding
				switch kind {
				case "implement", "fix", "deliver":
					j.Binding.Role = "implementer"
				case "plan":
					j.Binding.Role = "planner"
				case "qa":
					j.Binding.Role = "customer-qa"
				}
				j.ID = V2JobKey(j, j.Binding.AssignmentSHA, j.Binding.ExecutorID)
				j.Phase, j.SubmittedAt = "completed", e.Clock.Now()
				before := ScheduleCounters{1, 1}
				e.teamScheduleState().Counters = before
				switch condition {
				case "unsubmitted":
					j.SubmittedAt = time.Time{}
				case "unbound", "v1":
					j.Binding = nil
					j.ID = JobKey(j)
					if condition == "v1" {
						e.Config.Version, e.state.Version = 1, 1
						e.state.Schedule = nil
					}
				case "blocked", "paused", "waiting", "active":
					j.Phase = condition
				}
				dir := filepath.Join(t.TempDir(), "ledger")
				e.Store = NewVersionedFileStore(dir, e.state.Version)
				if err := e.saveJob(j); err != nil {
					t.Fatal(err)
				}
				if condition == "v1" {
					if e.state.Schedule != nil {
						t.Fatal("v1 completion created schedule accounting")
					}
					return
				}
				want := before
				if condition == "completed" {
					switch kind {
					case "review", "fix", "deliver", "qa":
						want = ScheduleCounters{2, 2}
					case "plan":
						want = ScheduleCounters{0, 2}
					case "implement":
						want = ScheduleCounters{}
					}
				}
				for range 2 {
					e = reloadTeamSchedule(t, e.Config, dir)
					if e.state.Schedule.Counters != want || !reflect.DeepEqual(e.state.Jobs[j.ID], j) {
						t.Fatalf("reloaded job/counters = %+v / %+v, want %+v / %+v", e.state.Jobs[j.ID], e.state.Schedule.Counters, j, want)
					}
					if err := e.saveJob(j); err != nil {
						t.Fatal(err)
					}
					if e.state.Schedule.Counters != want {
						t.Fatal("duplicate completion consumed service")
					}
				}
			})
		}
	}
}

func TestTeamScheduleRecoveryJobCheckpoint(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-rename=%t", commit), func(t *testing.T) {
			e, _, _, j := teamEngineFixture(t, false, "review")
			dir := filepath.Join(t.TempDir(), "ledger")
			store := &checkpointStore{Store: NewVersionedFileStore(dir, 2), commit: commit}
			e.Store = store
			j.Phase, j.SubmittedAt = "active", e.Clock.Now()
			e.teamScheduleState().Counters = ScheduleCounters{1, 1}
			if err := e.saveJob(j); err != nil {
				t.Fatal(err)
			}
			store.fail = func(s State) bool { return s.Jobs[j.ID].Phase == "completed" }
			j.Phase = "completed"
			if err := e.saveJob(j); !errors.Is(err, errCheckpoint) {
				t.Fatalf("completion error = %v", err)
			}
			restarted := reloadTeamSchedule(t, e.Config, dir)
			wantPhase, wantCounters := "active", (ScheduleCounters{1, 1})
			if commit {
				wantPhase, wantCounters = "completed", ScheduleCounters{2, 2}
			}
			if restarted.state.Jobs[j.ID].Phase != wantPhase || restarted.state.Schedule.Counters != wantCounters {
				t.Fatalf("non-atomic job/counter checkpoint: %+v", restarted.state)
			}
			if !reflect.DeepEqual(e.state, restarted.state) {
				t.Fatal("failed engine did not restore durable ledger")
			}
			for range 2 {
				if err := e.saveJob(j); !errors.Is(err, errCheckpoint) {
					t.Fatalf("failed engine accepted saveJob: %v", err)
				}
				if err := e.save(); !errors.Is(err, errCheckpoint) {
					t.Fatalf("failed engine accepted save: %v", err)
				}
			}
			durable := reloadTeamSchedule(t, e.Config, dir)
			if !reflect.DeepEqual(e.state, durable.state) || !reflect.DeepEqual(restarted.state, durable.state) {
				t.Fatal("fenced save changed live or durable state")
			}
			for range 2 {
				if err := restarted.saveJob(j); err != nil {
					t.Fatal(err)
				}
				restarted = reloadTeamSchedule(t, e.Config, dir)
				if restarted.state.Schedule.Counters != (ScheduleCounters{2, 2}) || restarted.state.Jobs[j.ID].Phase != "completed" {
					t.Fatal("restart lost or duplicated completion accounting")
				}
			}
		})
	}
}

func TestTeamScheduleRecoveryWakeEligibility(t *testing.T) {
	c, _ := wakeFixture(t)
	wakes := NewWakeStore(filepath.Join(t.TempDir(), "wakes"))
	e, _, _, _ := teamEngineFixture(t, false, "review")
	dir := filepath.Join(t.TempDir(), "ledger")
	e.Store = NewVersionedFileStore(dir, 2)
	e.Wakes = &WakeCoordinator{Store: wakes}
	e.teamScheduleState().Counters = ScheduleCounters{2, 0}
	var counted []string
	for i, name := range []string{"completed-one", "completed-two", "blocked", "queued", "unsubmitted", "historical"} {
		q, err := QueuePing(c, wakes, PingInput{Prompt: name}, e.Clock.Now().Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		r, err := wakes.Receipt(q.ID)
		if err != nil {
			t.Fatal(err)
		}
		r.Phase, r.Job.SubmittedAt = "completed", e.Clock.Now()
		switch name {
		case "blocked", "queued":
			r.Phase = name
		case "unsubmitted":
			r.Job.SubmittedAt = time.Time{}
		case "completed-one", "completed-two":
			counted = append(counted, q.ID)
		}
		if name != "historical" {
			e.state.Schedule.Eligible[q.ID] = e.Clock.Now()
		}
		if err := wakes.SaveReceipt(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		e = reloadTeamSchedule(t, e.Config, dir)
		e.Wakes = &WakeCoordinator{Store: NewWakeStore(wakes.Dir)}
		if err := e.accountCompletedWakes(); err != nil {
			t.Fatal(err)
		}
		if e.state.Schedule.Counters != (ScheduleCounters{0, 2}) || len(e.state.Schedule.CompletedWakes) != 2 {
			t.Fatalf("incorrect wake accounting: %+v", e.state.Schedule)
		}
		for _, id := range counted {
			if !e.state.Schedule.CompletedWakes[id] {
				t.Fatalf("eligible submitted completion %s not counted", id)
			}
		}
	}
}

type scheduleReceiptCheckpoint struct {
	WakeStore
	commit bool
}

func (s scheduleReceiptCheckpoint) SaveReceipt(r WakeReceipt) error {
	if s.commit {
		if err := s.WakeStore.SaveReceipt(r); err != nil {
			return err
		}
	}
	return errors.New("injected completion receipt checkpoint failure")
}

func TestTeamScheduleRecoveryWakeCheckpoint(t *testing.T) {
	for _, receiptCommit := range []bool{false, true} {
		for _, ledgerCommit := range []bool{false, true} {
			t.Run(fmt.Sprintf("receipt-after-rename=%t/ledger-after-rename=%t", receiptCommit, ledgerCommit), func(t *testing.T) {
				c, _ := wakeFixture(t)
				wakes := NewWakeStore(filepath.Join(t.TempDir(), "wakes"))
				e, _, _, _ := teamEngineFixture(t, false, "review")
				dir := filepath.Join(t.TempDir(), "ledger")
				e.Store = NewVersionedFileStore(dir, 2)
				q, err := QueuePing(c, wakes, PingInput{Prompt: "recover completed wake"}, e.Clock.Now())
				if err != nil {
					t.Fatal(err)
				}
				e.teamScheduleState().Counters = ScheduleCounters{1, 1}
				e.state.Schedule.Eligible[q.ID] = e.Clock.Now()
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
				r, err := wakes.Receipt(q.ID)
				if err != nil {
					t.Fatal(err)
				}
				r.Phase, r.Job.SubmittedAt = "completed", e.Clock.Now()
				fault := scheduleReceiptCheckpoint{WakeStore: wakes, commit: receiptCommit}
				if err := fault.SaveReceipt(r); err == nil {
					t.Fatal("receipt checkpoint error hidden")
				}
				// Simulate a crash after receipt persistence, before ledger accounting.
				e = reloadTeamSchedule(t, e.Config, dir)
				wakes = NewWakeStore(wakes.Dir)
				e.Wakes = &WakeCoordinator{Store: wakes}
				saved, err := wakes.Receipt(q.ID)
				if err != nil {
					t.Fatal(err)
				}
				if (saved.Phase == "completed") != receiptCommit {
					t.Fatalf("receipt durability = %s, committed=%t", saved.Phase, receiptCommit)
				}
				if !receiptCommit {
					if err := e.accountCompletedWakes(); err != nil {
						t.Fatal(err)
					}
					if e.state.Schedule.Counters != (ScheduleCounters{1, 1}) || len(e.state.Schedule.CompletedWakes) != 0 {
						t.Fatal("uncommitted receipt consumed service")
					}
					if err := wakes.SaveReceipt(r); err != nil {
						t.Fatal(err)
					}
				}
				e.Store = &checkpointStore{Store: e.Store, commit: ledgerCommit, fail: func(s State) bool { return s.Schedule.CompletedWakes[q.ID] }}
				if err := e.accountCompletedWakes(); !errors.Is(err, errCheckpoint) {
					t.Fatalf("ledger checkpoint error = %v", err)
				}
				restarted := reloadTeamSchedule(t, e.Config, dir)
				want := ScheduleCounters{1, 1}
				if ledgerCommit {
					want = ScheduleCounters{0, 2}
				}
				if restarted.state.Schedule.Counters != want || restarted.state.Schedule.CompletedWakes[q.ID] != ledgerCommit || !reflect.DeepEqual(e.state, restarted.state) {
					t.Fatal("wake marker/counters were not recovered atomically")
				}
				if err := e.save(); !errors.Is(err, errCheckpoint) {
					t.Fatalf("failed ledger accepted another save: %v", err)
				}
				if durable := reloadTeamSchedule(t, e.Config, dir); !reflect.DeepEqual(durable.state, restarted.state) {
					t.Fatal("fenced save changed durable accounting")
				}
				for range 3 {
					restarted.Wakes = &WakeCoordinator{Store: NewWakeStore(wakes.Dir)}
					if err := restarted.accountCompletedWakes(); err != nil {
						t.Fatal(err)
					}
					restarted = reloadTeamSchedule(t, e.Config, dir)
					if restarted.state.Schedule.Counters != (ScheduleCounters{0, 2}) || !restarted.state.Schedule.CompletedWakes[q.ID] || len(restarted.state.Schedule.CompletedWakes) != 1 {
						t.Fatal("restart lost or duplicated wake accounting")
					}
				}
			})
		}
	}
}
