package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func migrationFixture(t *testing.T) (Config, *FileStore, State, *FileWakeStore, string) {
	t.Helper()
	c, w := wakeFixture(t)
	s := NewFileStore(c.StateDir)
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	j := Job{Kind: "implement", GoalURL: "https://github.com/orgs/planning/discussions/1", TaskURL: "https://github.com/code/repo/issues/1", InputRevision: BodyHash("old scope"), Phase: "completed", SessionID: "ses_saved", MessageID: "msg_saved", SubmissionState: "completed", TerminalAt: time.Now().UTC(), Attempts: 2, ProgressHash: BodyHash("evidence")}
	j.ID = JobKey(j)
	state.Jobs[j.ID] = j
	state.Sessions["saved"] = SessionBinding{SessionID: j.SessionID, Checkout: c.WorkRoot, TaskURL: j.TaskURL, GoalURL: j.GoalURL, Kind: j.Kind, Role: c.Role}
	state.Seen["saved"] = "receipt"
	state.Cursor["saved"] = "cursor"
	state.ReviewCycles[j.TaskURL] = 2
	state.Attempts = map[string]int{j.ID: 2}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	id := BodyHash("queued wake")
	if _, err := QueuePing(c, w, PingInput{RequestID: id, Prompt: "retained question"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	done := BodyHash("completed wake")
	if _, err := QueuePing(c, w, PingInput{RequestID: done, Prompt: "answered question"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	receipt := WakeReceipt{Version: 1, RequestID: done, Phase: "completed", ReplyNodeID: "saved-reply", ReplyURL: "https://github.com/orgs/planning/discussions/1#discussioncomment-1", Job: Job{ID: done, Kind: "wake", Phase: "completed", SessionID: "ses_conversation", MessageID: "msg_reply", SubmissionState: "completed", TerminalAt: time.Now().UTC()}}
	if err := w.SaveReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	routing := newWakeRouting()
	routing.Bindings["saved"] = WakeBinding{SessionID: "ses_conversation", Directory: c.WorkRoot, DiscussionID: "discussion", ThreadID: "root"}
	routing.Seen["old"] = "revision"
	if err := w.SaveRouting(routing); err != nil {
		t.Fatal(err)
	}
	c.Version = 2
	return c, s, state, w, id
}

func TestTeamMigrationPreservesCompleteHistory(t *testing.T) {
	c, s, before, w, id := migrationFixture(t)
	if err := MigrateState(c, 1, 2, before.InstanceID); err != nil {
		t.Fatal(err)
	}
	after, err := NewVersionedFileStore(s.Dir, 2).Load()
	if err != nil {
		t.Fatal(err)
	}
	before.Version = 2
	if !reflect.DeepEqual(before, after) {
		t.Fatal("migration reinterpreted historical records")
	}
	w = NewVersionedWakeStore(w.Dir, 2)
	r, err := w.Receipt(id)
	if err != nil || r.Phase != "queued" {
		t.Fatalf("queued wake lost: %+v %v", r, err)
	}
	var backup map[string][]byte
	raw, err := readPrivate(filepath.Join(s.Dir, "migration-backup.json"), migrationLimit)
	if err != nil || json.Unmarshal(raw, &backup) != nil {
		t.Fatal("invalid backup", err)
	}
	for rel, original := range backup {
		if rel == "state.json" {
			continue
		}
		got, err := os.ReadFile(filepath.Join(s.Dir, rel))
		if err != nil || !bytes.Equal(original, got) {
			t.Fatalf("changed preserved file %s: %v", rel, err)
		}
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("v1 reader accepted v2 state")
	}
	before.Version = 1
	if err := s.Save(before); err == nil {
		t.Fatal("v1 writer rolled state back")
	}
	if err := MigrateState(c, 2, 1, before.InstanceID); err == nil {
		t.Fatal("unsafe rollback allowed")
	}
	if err := MigrateState(c, 1, 2, before.InstanceID); err == nil {
		t.Fatal("migration evidence was overwritten on retry")
	}
}

func TestTeamMigrationAcceptsV1DrainedEvidenceWithoutRewriting(t *testing.T) {
	for _, submission := range []string{"accepted", "running", "unknown", "submitting", "compacting", "blocked", "interrupted"} {
		t.Run(submission, func(t *testing.T) {
			c, s, state, w, _ := migrationFixture(t)
			// These are the records written by v1 wake completion and task stop:
			// completion retains its prior submission observation; confirmed abort
			// retains message identity but does not set TerminalAt.
			id := BodyHash("completed wake")
			r, err := w.Receipt(id)
			if err != nil {
				t.Fatal(err)
			}
			r.Job.SubmissionState = submission
			if err := w.SaveReceipt(r); err != nil {
				t.Fatal(err)
			}
			for key, j := range state.Jobs {
				j.Phase, j.SubmissionState, j.TerminalAt = "paused", "interrupted", time.Time{}
				state.Jobs[key] = j
			}
			if err := s.Save(state); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(w.Dir, "receipts", id+".json"))
			if err := MigrateState(c, 1, 2, state.InstanceID); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(filepath.Join(w.Dir, "receipts", id+".json"))
			if !bytes.Equal(before, after) {
				t.Fatal("terminal wake evidence rewritten")
			}
			loaded, err := NewVersionedFileStore(s.Dir, 2).Load()
			state.Version = 2
			if err != nil || !reflect.DeepEqual(loaded, state) {
				t.Fatal("confirmed-abort history changed", err)
			}
		})
	}
}

func TestTeamMigrationRefusesUndrainedOrInvalidRecords(t *testing.T) {
	for _, kind := range []string{"preparing", "active", "recovery", "submitting", "unknown", "accepted", "running", "compacting", "bad key", "bad kind", "bad phase", "bad session", "message without terminal", "foreign", "wake running", "wake recovery", "wake unknown", "orphan receipt", "duplicate state", "unknown state field", "bad routing", "bad control", "bad status", "unknown file", "symlink", "backup exists"} {
		t.Run(kind, func(t *testing.T) {
			c, s, state, w, id := migrationFixture(t)
			var j Job
			for _, value := range state.Jobs {
				j = value
			}
			switch kind {
			case "preparing", "active", "recovery":
				j.Phase = kind
			case "submitting", "unknown", "accepted", "running", "compacting":
				j.Phase = "blocked"
				j.SubmissionState = kind
			case "bad key":
				j.TaskURL += "other"
			case "bad kind":
				j.Kind = "outsider"
			case "bad phase":
				j.Phase = "unrecognized"
			case "bad session":
				j.SessionID = "invalid"
			case "message without terminal":
				j.TerminalAt = time.Time{}
			case "foreign":
				state.InstanceID = BodyHash("foreign")
			}
			state.Jobs[j.ID] = j
			if err := s.Save(state); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "wake running", "wake recovery", "wake unknown":
				r, _ := w.Receipt(id)
				r.Phase = "running"
				if kind == "wake recovery" {
					r.Phase = "recovery"
				}
				if kind == "wake unknown" {
					r.Phase = "blocked"
					r.Job = Job{ID: id, Kind: "wake", Phase: "blocked", SubmissionState: "unknown"}
				}
				if err := w.SaveReceipt(r); err != nil {
					t.Fatal(err)
				}
			case "orphan receipt":
				if err := atomicPrivate(filepath.Join(w.Dir, "receipts", BodyHash("orphan")+".json"), []byte(`{}`)); err != nil {
					t.Fatal(err)
				}
			case "duplicate state", "unknown state field":
				path := filepath.Join(s.Dir, "state.json")
				raw, _ := os.ReadFile(path)
				extra := `"Version":1,`
				if kind == "unknown state field" {
					extra = `"Unknown":true,`
				}
				if err := atomicPrivate(path, append([]byte("{"+extra), raw[1:]...)); err != nil {
					t.Fatal(err)
				}
			case "bad routing":
				_ = atomicPrivate(filepath.Join(w.Dir, "routing.json"), []byte(`{}`))
			case "bad control":
				_ = atomicPrivate(filepath.Join(s.Dir, "control.json"), []byte(`{"paused":"yes"}`))
			case "bad status":
				_ = atomicPrivate(filepath.Join(s.Dir, "status.json"), []byte(`{"heartbeat":"not-a-time"}`))
			case "unknown file":
				_ = atomicPrivate(filepath.Join(s.Dir, "unexpected.json"), []byte(`{}`))
			case "backup exists":
				_ = atomicPrivate(filepath.Join(s.Dir, "migration-backup.json"), []byte(`{}`))
			case "symlink":
				if err := os.Symlink(filepath.Join(s.Dir, "state.json"), filepath.Join(s.Dir, "link")); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(filepath.Join(s.Dir, "state.json"))
			expected := state.InstanceID
			if kind == "foreign" {
				expected = BodyHash("expected")
			}
			if err := MigrateState(c, 1, 2, expected); err == nil {
				t.Fatal("unsafe conversion accepted")
			}
			after, _ := os.ReadFile(filepath.Join(s.Dir, "state.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("refusal modified source")
			}
		})
	}
}

func TestTeamMigrationFaultsRemainInspectable(t *testing.T) {
	for _, boundary := range []string{"hold before", "hold after", "replace before", "replace after", "verify state", "verify wake", "verify backup", "complete before", "complete after"} {
		t.Run(boundary, func(t *testing.T) {
			c, s, state, _, _ := migrationFixture(t)
			calls := 0
			write := func(path string, raw []byte) error {
				calls++
				if boundary == "hold before" && calls == 1 || boundary == "replace before" && calls == 2 || boundary == "complete before" && calls == 3 {
					return errors.New("injected before replacement")
				}
				if err := atomicPrivate(path, raw); err != nil {
					return err
				}
				if boundary == "hold after" && calls == 1 || boundary == "replace after" && calls == 2 || boundary == "complete after" && calls == 3 {
					return errors.New("injected ambiguous replacement")
				}
				if calls == 2 {
					switch boundary {
					case "verify state":
						return atomicPrivate(path, []byte(`{}`))
					case "verify wake":
						return atomicPrivate(filepath.Join(s.Dir, "wake", "v1", "routing.json"), []byte(`{}`))
					case "verify backup":
						return atomicPrivate(filepath.Join(s.Dir, "migration-backup.json"), []byte(`{}`))
					}
				}
				return nil
			}
			if err := migrateState(c, 1, 2, state.InstanceID, write); err == nil {
				t.Fatal("injected failure accepted")
			}
			if _, err := os.Stat(filepath.Join(s.Dir, "migration-backup.json")); err != nil {
				t.Fatal("lost backup", err)
			}
			if boundary != "hold before" && boundary != "complete after" {
				for _, version := range []int{1, 2} {
					if _, err := NewVersionedFileStore(s.Dir, version).Load(); err == nil {
						t.Fatal("unverified migration runnable")
					}
				}
			}
			if err := MigrateState(c, 1, 2, state.InstanceID); err == nil {
				t.Fatal("ambiguous migration replayed")
			}
		})
	}
}

func TestTeamMigrationLocksAndBackupFailure(t *testing.T) {
	for _, kind := range []string{"worker", "producer", "backup sync", "backup verify"} {
		t.Run(kind, func(t *testing.T) {
			c, s, state, w, _ := migrationFixture(t)
			var release func()
			var err error
			switch kind {
			case "worker":
				release, err = s.Lock()
			case "producer":
				release, err = w.producerLock()
			case "backup sync", "backup verify":
				old := wakeDirectorySync
				wakeDirectorySync = func(string) error {
					if kind == "backup verify" {
						return atomicPrivate(filepath.Join(s.Dir, "migration-backup.json"), []byte(`{}`))
					}
					return errors.New("sync failed")
				}
				release = func() { wakeDirectorySync = old }
			}
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if err := MigrateState(c, 1, 2, state.InstanceID); err == nil {
				t.Fatal("unsafe migration accepted")
			}
			loaded, err := s.Load()
			if err != nil || !reflect.DeepEqual(loaded, state) {
				t.Fatal("source changed on lock/backup failure", err)
			}
		})
	}
}

func TestTeamMigrationWakeProducerRejectsConvertedLedger(t *testing.T) {
	c, s, state, w, id := migrationFixture(t)
	if err := MigrateState(c, 1, 2, state.InstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Receipt(id); err == nil {
		t.Fatal("v1 wake reader ignored v2 ledger")
	}
	if release, err := w.producerLock(); err == nil {
		release()
		t.Fatal("v1 wake producer ignored v2 ledger")
	}
	w = NewVersionedWakeStore(w.Dir, 2)
	if _, err := w.Receipt(id); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaused(false); err == nil {
		t.Fatal("v1 control writer ignored v2 ledger")
	}
}

func TestVersionedStoreNeverReinterpretsState(t *testing.T) {
	s := storeFixture(t)
	for _, version := range []int{1, 2} {
		store := NewVersionedFileStore(s.Dir, version)
		state, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if state.Version != version {
			t.Fatal("wrong new state version")
		}
		if err := store.Save(state); err != nil {
			t.Fatal(err)
		}
		other := NewVersionedFileStore(s.Dir, 3-version)
		if _, err := other.Load(); err == nil {
			t.Fatal("cross-version read accepted")
		}
		state.Version = 3 - version
		if err := other.Save(state); err == nil {
			t.Fatal("cross-version write accepted")
		}
		if err := os.Remove(filepath.Join(s.Dir, "state.json")); err != nil {
			t.Fatal(err)
		}
	}
}
