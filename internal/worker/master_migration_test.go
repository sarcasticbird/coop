package worker

import (
	"bytes"
	"encoding/json"
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

func TestMasterMigrationExplicitLegacyRetirement(t *testing.T) {
	for _, kind := range []string{"valid", "unnamed", "unknown", "duplicate", "active", "submitted", "bound"} {
		t.Run(kind, func(t *testing.T) {
			cfg, store, before := masterMigrationFixture(t)
			j := Job{Kind: "review", TaskURL: "https://github.com/code/repo/issues/1", Phase: "blocked", WaitReason: "scope"}
			j.ID = JobKey(j)
			before.Jobs[j.ID] = j
			if err := store.Save(before); err != nil {
				t.Fatal(err)
			}
			if kind == "active" || kind == "submitted" || kind == "bound" {
				// Even a drained bound job belongs to the current authority.
				// Its retirement must not be inferred from a legacy option.
				if kind == "bound" {
					j.Binding = &JobBinding{}
				}
				if kind == "active" {
					j.Phase = "active"
				}
				if kind == "submitted" {
					j.SessionID, j.MessageID, j.SubmissionState = "ses_saved", "msg_saved", "unknown"
				}
				before.Jobs[j.ID] = j
				raw, _ := json.Marshal(before)
				if err := os.WriteFile(filepath.Join(store.Dir, "state.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			beforeRaw, err := os.ReadFile(filepath.Join(store.Dir, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{j.ID}
			if kind == "unnamed" {
				ids = nil
			}
			if kind == "unknown" {
				ids = []string{BodyHash("unknown")}
			}
			if kind == "duplicate" {
				ids = append(ids, j.ID)
			}
			err = MigrateMasterStateWithRetirements(cfg, before.InstanceID, ids, nil)
			if kind != "valid" {
				if err == nil {
					t.Fatal("unsafe retirement accepted")
				}
				after, _ := os.ReadFile(filepath.Join(store.Dir, "state.json"))
				if !bytes.Equal(beforeRaw, after) {
					t.Fatal("rejection changed history")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			after, err := NewVersionedFileStore(store.Dir, 3).Load()
			if err != nil {
				t.Fatal(err)
			}
			before.Version = 3
			if !reflect.DeepEqual(before, after) {
				t.Fatal("retirement rewrote historical statuses")
			}
			if _, err := os.ReadFile(filepath.Join(store.Dir, "master-legacy-retirement.json")); err != nil {
				t.Fatal(err)
			}
			if err := RecoverMasterMigration(cfg, before.InstanceID); err != nil {
				t.Fatal(err)
			}
		})
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

func TestMasterMigrationRetriesBeforeBackupPublication(t *testing.T) {
	for _, mode := range []string{"identical", "audit-changed", "state-changed"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, before := masterMigrationFixture(t)
			j := Job{Kind: "plan", TaskURL: "https://github.com/code/repo/issues/1", Phase: "blocked", WaitReason: "scope"}
			j.ID = JobKey(j)
			before.Jobs[j.ID] = j
			if err := store.Save(before); err != nil {
				t.Fatal(err)
			}
			// An unreadable snapshot entry fails collection after audit publication.
			probe := filepath.Join(store.Dir, "snapshot-probe.json")
			if err := os.WriteFile(probe, []byte("{}"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := MigrateMasterStateWithRetirements(cfg, before.InstanceID, []string{j.ID}, nil); err == nil {
				t.Fatal("expected snapshot collection failure")
			}
			auditPath := filepath.Join(store.Dir, "master-legacy-retirement.json")
			audit, err := os.ReadFile(auditPath)
			if err != nil {
				t.Fatal(err)
			}
			backupPath := filepath.Join(store.Dir, "master-migration-backup.json")
			if _, err = os.Stat(backupPath); !os.IsNotExist(err) {
				t.Fatal("backup published before snapshot completed", err)
			}
			if err = os.Remove(probe); err != nil {
				t.Fatal(err)
			}
			if mode == "audit-changed" {
				if err = os.WriteFile(auditPath, append(bytes.Clone(audit), '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "state-changed" {
				j.WaitReason = "changed"
				before.Jobs[j.ID] = j
				if err = store.Save(before); err != nil {
					t.Fatal(err)
				}
			}
			err = MigrateMasterStateWithRetirements(cfg, before.InstanceID, []string{j.ID}, nil)
			if mode != "identical" {
				if err == nil {
					t.Fatal("retry accepted changed migration evidence")
				}
				if _, err = store.Load(); err != nil {
					t.Fatal("rejected retry changed legacy state", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("identical retry failed after audit publication: %v", err)
			}
			after, err := NewVersionedFileStore(store.Dir, 3).Load()
			if err != nil {
				t.Fatal(err)
			}
			before.Version = 3
			if !reflect.DeepEqual(before, after) {
				t.Fatal("retry changed historical records")
			}
			if err = validateMasterLegacyJobs(cfg, after); err != nil {
				t.Fatal(err)
			}
			preserved, err := os.ReadFile(auditPath)
			if err != nil || !bytes.Equal(audit, preserved) {
				t.Fatal("retry rewrote the immutable audit", err)
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

func TestMasterMigrationPreservesCompletedQA(t *testing.T) {
	cfg, old, before := masterMigrationFixture(t)
	j := qaRuntimeJob()
	j.Phase, j.SubmissionState = "completed", "completed"
	j.SessionID, j.MessageID, j.Checkout = "ses_qahistory", "msg_qahistory", filepath.Join(cfg.WorkRoot, "qa-history")
	j.TerminalAt = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	j.SubmittedModel = &ModelIdentity{ProviderID: "provider", ModelID: "qa-model", Variant: "medium"}
	before.Jobs[j.ID] = j
	before.Sessions[BindingKey(cfg, j)] = SessionBinding{Binding: j.Binding, Role: j.Binding.Role, Kind: j.Kind, TaskURL: j.TaskURL, GoalURL: j.GoalURL, SessionID: j.SessionID, Checkout: j.Checkout, Branch: j.Branch}
	if err := old.Save(before); err != nil {
		t.Fatal(err)
	}
	if err := MigrateState(cfg, 2, 3, before.InstanceID); err != nil {
		t.Fatalf("completed bound QA history prevented conversion: %v", err)
	}
	after, err := NewVersionedFileStore(cfg.StateDir, 3).Load()
	if err != nil {
		t.Fatal(err)
	}
	before.Version = 3
	if !reflect.DeepEqual(before, after) {
		t.Fatal("QA history, identity or model evidence changed")
	}
}

func TestMasterMigrationRefusesUnresolvedQA(t *testing.T) {
	for _, phase := range []string{"active", "completed"} {
		t.Run(phase, func(t *testing.T) {
			cfg, old, before := masterMigrationFixture(t)
			j := qaRuntimeJob()
			j.Phase, j.SubmissionState = phase, "unknown"
			j.SessionID, j.MessageID, j.Checkout = "ses_qahold", "msg_qahold", filepath.Join(cfg.WorkRoot, "qa-held")
			before.Jobs[j.ID] = j
			before.Sessions[BindingKey(cfg, j)] = SessionBinding{Binding: j.Binding, Role: j.Binding.Role, Kind: j.Kind, TaskURL: j.TaskURL, GoalURL: j.GoalURL, SessionID: j.SessionID, Checkout: j.Checkout, Branch: j.Branch}
			if err := old.Save(before); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(cfg.StateDir, "state.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := MigrateState(cfg, 2, 3, before.InstanceID); err == nil {
				t.Fatal("unresolved QA turn migrated")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(raw, after) {
				t.Fatal("refused conversion changed legacy state", err)
			}
		})
	}
}

func TestTeamMigrationRefusesUnboundLegacyQA(t *testing.T) {
	cfg, old, before, _, _ := migrationFixture(t)
	j := Job{Kind: "qa", Phase: "completed", SubmissionState: "completed"}
	j.ID = JobKey(j)
	before.Jobs[j.ID] = j
	if err := old.Save(before); err != nil {
		t.Fatal(err)
	}
	if err := MigrateState(cfg, 1, 2, before.InstanceID); err == nil {
		t.Fatal("legacy unbound QA was promoted to team history")
	}
}

func TestMasterMigrationExplicitCompletedLegacyWake(t *testing.T) {
	for _, mode := range []string{"valid", "unnamed", "duplicate", "unknown", "active", "altered reply"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, before, wakes, request, receipt := legacyCompletedWakeFixture(t, "ping")
			if err := MigrateState(cfg, 1, 2, before.InstanceID); err != nil {
				t.Fatal(err)
			}
			cfg.Version = 3
			cfg.Master = &MasterConfig{Backend: "master", ClientConfig: filepath.Join(cfg.StateDir, "master-client.toml"), MasterID: "master", Engagement: "coop", AgentID: "agent-one", HostID: "host", InstanceID: before.InstanceID, Generation: 1}
			receiptPath := filepath.Join(wakes.Dir, "receipts", request.ID+".json")
			if mode == "active" {
				receipt.Phase, receipt.Job.Phase = "running", "active"
			}
			if mode == "altered reply" {
				receipt.ReplyURL = "https://github.com/other/repo/discussions/1#discussioncomment-2"
			}
			if mode == "active" || mode == "altered reply" {
				raw, _ := json.Marshal(receipt)
				if err := os.WriteFile(receiptPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(receiptPath)
			if err != nil {
				t.Fatal(err)
			}
			stateRaw, _ := os.ReadFile(filepath.Join(store.Dir, "state.json"))
			ids := []string{request.ID}
			if mode == "unnamed" {
				ids = nil
			}
			if mode == "duplicate" {
				ids = append(ids, request.ID)
			}
			if mode == "unknown" {
				ids = []string{BodyHash("unknown")}
			}
			err = MigrateMasterStateWithRetirements(cfg, before.InstanceID, nil, ids)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if err = RecoverMasterMigration(cfg, before.InstanceID); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("unsafe wake acceptance")
				}
				after, _ := os.ReadFile(filepath.Join(store.Dir, "state.json"))
				if !bytes.Equal(stateRaw, after) {
					t.Fatal("rejection changed state")
				}
			}
			after, _ := os.ReadFile(receiptPath)
			if !bytes.Equal(raw, after) {
				t.Fatal("legacy receipt rewritten")
			}
		})
	}
}

func TestMasterRetirementAuthorizesOnlyExactPreservedJobs(t *testing.T) {
	cfg, store, before := masterMigrationFixture(t)
	j := Job{Kind: "plan", TaskURL: "https://github.com/code/repo/issues/1", Phase: "blocked", WaitReason: "scope"}
	j.ID = JobKey(j)
	before.Jobs[j.ID] = j
	if err := store.Save(before); err != nil {
		t.Fatal(err)
	}
	if err := MigrateMasterStateWithRetirements(cfg, before.InstanceID, []string{j.ID}, nil); err != nil {
		t.Fatal(err)
	}
	state, err := NewVersionedFileStore(store.Dir, 3).Load()
	if err != nil {
		t.Fatal(err)
	}
	if err = validateMasterLegacyJobs(cfg, state); err != nil {
		t.Fatal(err)
	}
	changed := state.Jobs[j.ID]
	changed.WaitReason = "changed"
	state.Jobs[j.ID] = changed
	if validateMasterLegacyJobs(cfg, state) == nil {
		t.Fatal("changed retired job accepted")
	}
	state.Jobs[j.ID] = j
	path := filepath.Join(store.Dir, "master-legacy-retirement.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(raw, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if validateMasterLegacyJobs(cfg, state) == nil {
		t.Fatal("altered retirement audit accepted")
	}
}

func TestMasterRuntimeStartsWithExplicitRetiredHistory(t *testing.T) {
	cfg, _, _, _, _, _, code := commsFixtureWithRetiredHistory(t, true)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("test-agent-token"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf("url = %q\nmaster_id = %q\ntoken_file = %q\n", "https://127.0.0.1:443", cfg.Master.MasterID, token)
	if err := os.WriteFile(cfg.Master.ClientConfig, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Projects = nil
	cfg.ProjectOwners = nil
	engine := Engine{Config: cfg, Store: NewVersionedFileStore(cfg.StateDir, 3), GitHub: NewGitHubClient(ExecRunner{}), OpenCode: code, Checkouts: &fakeCheckouts{}}
	if err := engine.initMaster(); err != nil {
		t.Fatalf("retired history prevented runtime startup: %v", err)
	}
}
