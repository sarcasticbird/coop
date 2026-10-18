package worker

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func masterMigrationFixture(t *testing.T) (Config, *FileStore, State) {
	t.Helper()
	c, s, before := activationFixture(t)
	c.Version = 3
	c.Master = &MasterConfig{Backend: "master", ClientConfig: filepath.Join(c.StateDir, "master-client.toml"), MasterID: "master", Engagement: "coop", AgentID: "agent-one", HostID: "host", InstanceID: before.InstanceID, Generation: 1}
	return c, s, before
}
func TestMasterMigrationPreservesHistory(t *testing.T) {
	c, old, before := masterMigrationFixture(t)
	j := Job{Kind: "implement", TaskURL: "https://github.com/code/repo/issues/1", Phase: "completed", SessionID: "ses_legacy", MessageID: "msg_legacy", Checkout: filepath.Join(c.WorkRoot, "legacy"), SubmissionState: "completed", TerminalAt: time.Now().UTC(), SubmittedModel: &ModelIdentity{ProviderID: "provider", ModelID: "historic-model", Variant: "high"}}
	j.ID = JobKey(j)
	before.Jobs[j.ID] = j
	before.Sessions[BindingKey(c, j)] = SessionBinding{TaskURL: j.TaskURL, GoalURL: j.GoalURL, Kind: j.Kind, Checkout: j.Checkout, SessionID: j.SessionID}
	if err := old.Save(before); err != nil {
		t.Fatal(err)
	}
	if err := MigrateState(c, 2, 3, before.InstanceID); err != nil {
		t.Fatal(err)
	}
	after, err := NewVersionedFileStore(c.StateDir, 3).Load()
	if err != nil {
		t.Fatal(err)
	}
	before.Version = 3
	if !reflect.DeepEqual(before, after) {
		t.Fatal("history changed")
	}
	if _, err = old.Load(); err == nil {
		t.Fatal("old writer read v3")
	}
	if err = old.Save(before); err == nil {
		t.Fatal("old writer overwrote v3")
	}
	if err = MigrateState(c, 2, 3, before.InstanceID); err == nil {
		t.Fatal("migration replayed")
	}
}

func TestMasterMigrationRefusesQueuedLegacyWake(t *testing.T) {
	cfg, _, before := masterMigrationFixture(t)
	legacy := cfg
	legacy.Version = 2
	legacy.Master = nil
	w := NewVersionedWakeStore(filepath.Join(cfg.StateDir, "wake", "v1"), 2)
	q, err := QueuePing(legacy, w, PingInput{Prompt: "finish under original authority"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = MigrateState(cfg, 2, 3, before.InstanceID); err == nil {
		t.Fatal("queued old conversation was stranded")
	}
	if err = w.SaveReceipt(WakeReceipt{Version: 1, RequestID: q.ID, Phase: "cancelled", Reason: "owner_resolved_before_cutover", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(w.Dir, "receipts", q.ID+".json")
	raw, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = MigrateState(cfg, 2, 3, before.InstanceID); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(receiptPath)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("legacy receipt bytes changed", err)
	}
	if _, err = w.Enqueue(WakeRequest{Version: 1, ID: BodyHash("retired"), Target: cfg.Name, Kind: "ping", Prompt: "old writer", CreatedAt: time.Now().UTC(), Source: ConversationSource{URL: legacy.Wake.CoordinationURL, DiscussionURL: legacy.Wake.CoordinationURL, Author: legacy.Owner}}); err == nil {
		t.Fatal("old wake writer accepted v3")
	}
}
func TestMasterMigrationRefusesUnresolved(t *testing.T) {
	c, s, state := masterMigrationFixture(t)
	j := Job{Kind: "implement", TaskURL: "https://github.com/code/repo/issues/1", Phase: "active", SessionID: "ses_saved", MessageID: "msg_saved", SubmissionState: "unknown"}
	j.ID = JobKey(j)
	// Seed an otherwise valid v2 historical hold, then prove conversion refuses it.
	j.Phase = "blocked"
	j.MessageID = ""
	j.SessionID = ""
	j.SubmissionState = ""
	state.Jobs[j.ID] = j
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = MigrateState(c, 2, 3, state.InstanceID); err == nil {
		t.Fatal("unresolved engineering work migrated")
	}
	after, err := os.ReadFile(filepath.Join(s.Dir, "state.json"))
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("rejected migration mutated state")
	}
}
func TestV3InstallerFence(t *testing.T) {
	c, _, before := masterMigrationFixture(t)
	if err := MigrateState(c, 2, 3, before.InstanceID); err != nil {
		t.Fatal(err)
	}
	if err := CheckInstall(c, ""); err != nil {
		t.Fatal(err)
	}
	old := c
	old.Version = 2
	old.Master = nil
	if err := CheckInstall(old, ""); err == nil {
		t.Fatal("v2 reinstall accepted v3 state")
	}
}

func TestMasterMigrationExplicitInterruptedRecovery(t *testing.T) {
	for fail := 1; fail <= 3; fail++ {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			cfg, old, before := masterMigrationFixture(t)
			calls := 0
			err := migrateMasterState(cfg, before.InstanceID, func(path string, raw []byte) error {
				calls++
				if calls == fail {
					return errors.New("interrupted publication")
				}
				return atomicPrivate(path, raw)
			})
			if err == nil {
				t.Fatal("expected interruption")
			}
			if _, err = old.Load(); err == nil {
				t.Fatal("legacy writer ignored incomplete master conversion")
			}
			if err = RecoverMasterMigration(cfg, "wrong"); err == nil {
				t.Fatal("wrong instance recovered")
			}
			if err = RecoverMasterMigration(cfg, before.InstanceID); err != nil {
				t.Fatal(err)
			}
			after, err := NewVersionedFileStore(cfg.StateDir, 3).Load()
			if err != nil {
				t.Fatal(err)
			}
			before.Version = 3
			if !reflect.DeepEqual(before, after) {
				t.Fatal("history changed during recovery")
			}
		})
	}
}

func TestInitializeMasterStateUsesPinnedInstance(t *testing.T) {
	cfg, _, _ := masterMigrationFixture(t)
	cfg.StateDir = filepath.Join(t.TempDir(), "fresh")
	id, err := InitializeTeamState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if id != cfg.Master.InstanceID {
		t.Fatal("initialization replaced pinned instance")
	}
	if _, err = NewVersionedFileStore(cfg.StateDir, 3).Load(); err != nil {
		t.Fatal(err)
	}
}
