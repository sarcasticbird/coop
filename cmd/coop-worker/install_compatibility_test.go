package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/worker"
	"golang.org/x/sys/unix"
)

func installConfigCopy(t *testing.T, c worker.Config) string {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func installLedgerBytes(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) && path == dir {
			return nil
		}
		if err != nil {
			return err
		}
		// Checking compatibility may create lock files, but never ledger data.
		if entry.IsDir() || entry.Name() == "worker.lock" || entry.Name() == "producer.lock" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		files[rel] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func checkInstallOffline(t *testing.T, path, previous, want string) {
	t.Helper()
	c, err := worker.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	before := installLedgerBytes(t, c.StateDir)
	for _, cli := range []bool{false, true} {
		var out bytes.Buffer
		if cli {
			args := []string{"check-install", "--config", path}
			if previous != "" {
				args = append(args, "--previous-config", previous)
			}
			err = run(args, &out)
		} else {
			err = worker.CheckInstall(c, previous)
		}
		if want == "" {
			if err != nil {
				t.Errorf("CLI=%v: unexpected refusal: %v", cli, err)
			}
			if cli && out.String() != "{\"compatible\":true}\n" {
				t.Errorf("unexpected check-install output: %q", out.String())
			}
		} else {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("CLI=%v: error = %v, want %q", cli, err, want)
			}
			if out.Len() != 0 {
				t.Errorf("refusal emitted output: %q", out.String())
			}
		}
		if after := installLedgerBytes(t, c.StateDir); !reflect.DeepEqual(before, after) {
			t.Errorf("CLI=%v: compatibility check changed ledger bytes or file inventory", cli)
		}
	}
}

func TestWorkerCheckInstallCompatibleOffline(t *testing.T) {
	for _, scenario := range []string{"v1", "proposed v2", "migrated v2", "fresh v1", "fresh v2", "empty directory v2"} {
		t.Run(scenario, func(t *testing.T) {
			version := 1
			if strings.Contains(scenario, "v2") {
				version = 2
			}
			path, c, state := cliMigrationFixture(t, version)
			previous := ""
			if strings.HasPrefix(scenario, "fresh") {
				if err := os.RemoveAll(c.StateDir); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "empty directory v2" {
				if err := os.Remove(filepath.Join(c.StateDir, "state.json")); err != nil {
					t.Fatal(err)
				}
			} else {
				old := c
				old.Version, old.Team, old.Role = 1, nil, "implementer"
				old.Implementer, old.Reviewer = c.Self, worker.Actor{ID: 3, Login: "reviewer[bot]", Kind: "Bot"}
				previous = installConfigCopy(t, old)
				old.DiscussionRepositories = []string{"planning/board"}
				old.Wake = &worker.WakeConfig{Enabled: true, CoordinationURL: "https://github.com/orgs/planning/discussions/1", PeerHopLimit: 2}
				w := worker.NewWakeStore(filepath.Join(c.StateDir, "wake", "v1"))
				if _, err := worker.QueuePing(old, w, worker.PingInput{RequestID: worker.BodyHash("queued install wake"), Prompt: "keep this question"}, time.Now()); err != nil {
					t.Fatal(err)
				}
				if scenario == "migrated v2" {
					if err := worker.MigrateState(c, 1, 2, state.InstanceID); err != nil {
						t.Fatal(err)
					}
				}
			}
			checkInstallOffline(t, path, previous, "")
			if strings.HasPrefix(scenario, "fresh") {
				if _, err := os.Stat(c.StateDir); !os.IsNotExist(err) {
					t.Fatalf("fresh install check created state directory: %v", err)
				}
			}
		})
	}
}

func TestWorkerCheckInstallRejectsMissingMigratedWakeTree(t *testing.T) {
	for _, missingState := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved ledger", true: "missing ledger"}[missingState], func(t *testing.T) {
			path, c, state := cliMigrationFixture(t, 2)
			if err := worker.MigrateState(c, 1, 2, state.InstanceID); err != nil {
				t.Fatal(err)
			}
			wake := filepath.Join(c.StateDir, "wake")
			if err := os.RemoveAll(wake); err != nil {
				t.Fatal(err)
			}
			if missingState {
				if err := os.Remove(filepath.Join(c.StateDir, "state.json")); err != nil {
					t.Fatal(err)
				}
			}
			checkInstallOffline(t, path, "", "requires complete fenced wake storage")
			if _, err := os.Lstat(wake); !os.IsNotExist(err) {
				t.Errorf("refused check recreated missing wake tree: %v", err)
			}
		})
	}
}

func TestWorkerCheckInstallV1RejectsV2WithoutPreviousConfig(t *testing.T) {
	for _, stripped := range []bool{false, true} {
		t.Run(map[bool]string{false: "migration evidence", true: "stripped migration evidence"}[stripped], func(t *testing.T) {
			_, c, state := cliMigrationFixture(t, 2)
			if err := worker.MigrateState(c, 1, 2, state.InstanceID); err != nil {
				t.Fatal(err)
			}
			if stripped {
				for _, name := range []string{"migration.json", "migration-backup.json"} {
					if err := os.Remove(filepath.Join(c.StateDir, name)); err != nil {
						t.Fatal(err)
					}
				}
			}
			c.Version, c.Team, c.Role = 1, nil, "implementer"
			c.Implementer, c.Reviewer = c.Self, worker.Actor{ID: 3, Login: "reviewer[bot]", Kind: "Bot"}
			want := "requires inspection"
			if stripped {
				want = "version mismatch"
			}
			checkInstallOffline(t, installConfigCopy(t, c), "", want)
		})
	}
}

func TestWorkerCheckInstallPreviousConfigRefusals(t *testing.T) {
	for _, scenario := range []string{"name", "actor id", "state path", "downgrade", "missing", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			path, c, _ := cliMigrationFixture(t, 1)
			old := c
			switch scenario {
			case "name":
				old.Name = "other"
			case "actor id":
				old.Self.ID = 20
				old.Implementer = old.Self
			case "state path":
				old.StateDir = filepath.Join(t.TempDir(), "other-state")
			case "downgrade":
				_, old, _ = cliMigrationFixture(t, 2)
				old.StateDir = c.StateDir
			}
			previous := installConfigCopy(t, old)
			want := "installation cannot change"
			if scenario == "missing" {
				previous += ".missing"
				want = "cannot read private worker config"
			} else if scenario == "malformed" {
				if err := os.WriteFile(previous, []byte(`{`), 0600); err != nil {
					t.Fatal(err)
				}
				want = "invalid worker configuration envelope"
			} else if _, err := worker.LoadConfig(previous); err != nil {
				t.Fatalf("previous config must be valid before compatibility check: %v", err)
			}
			checkInstallOffline(t, path, previous, want)
		})
	}
}

func TestWorkerCheckInstallBusyLocks(t *testing.T) {
	for _, kind := range []string{"worker", "producer"} {
		t.Run(kind, func(t *testing.T) {
			path, c, _ := cliMigrationFixture(t, 1)
			want := "another worker owns this state"
			if kind == "worker" {
				release, err := worker.NewFileStore(c.StateDir).Lock()
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			} else {
				dir := filepath.Join(c.StateDir, "wake", "v1")
				for _, name := range []string{"inbox", "receipts"} {
					if err := os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
						t.Fatal(err)
					}
				}
				f, err := os.OpenFile(filepath.Join(dir, "producer.lock"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = f.Close() }()
				if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()
				want = "cannot acquire wake producer lock"
			}
			checkInstallOffline(t, path, "", want)
		})
	}
}

func TestWorkerCheckInstallRejectsPartialWakeTree(t *testing.T) {
	for _, missing := range []string{"inbox", "receipts", "v1"} {
		for _, cli := range []bool{false, true} {
			t.Run(missing+map[bool]string{false: "/function", true: "/cli"}[cli], func(t *testing.T) {
				path, c, _ := cliMigrationFixture(t, 1)
				dir := filepath.Join(c.StateDir, "wake", "v1")
				for _, name := range []string{"inbox", "receipts"} {
					if err := os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
						t.Fatal(err)
					}
				}
				missingPath := filepath.Join(dir, missing)
				if missing == "v1" {
					missingPath = dir
				}
				if err := os.RemoveAll(missingPath); err != nil {
					t.Fatal(err)
				}
				before := installLedgerBytes(t, c.StateDir)
				var out bytes.Buffer
				var err error
				if cli {
					err = run([]string{"check-install", "--config", path}, &out)
				} else {
					err = worker.CheckInstall(c, "")
				}
				if err == nil {
					t.Error("partial wake tree accepted")
				}
				if out.Len() != 0 {
					t.Errorf("refusal emitted output: %q", out.String())
				}
				if _, err := os.Lstat(missingPath); !os.IsNotExist(err) {
					t.Errorf("compatibility check recreated missing %s: %v", missing, err)
				}
				if !reflect.DeepEqual(before, installLedgerBytes(t, c.StateDir)) {
					t.Error("compatibility check changed ledger bytes or file inventory")
				}
			})
		}
	}
}

func TestWorkerCheckInstallMigrationHoldWithoutNewState(t *testing.T) {
	for _, version := range []int{1, 2} {
		for _, missingState := range []bool{false, true} {
			for _, evidence := range []string{"pending", "malformed"} {
				t.Run(strings.Join([]string{map[int]string{1: "v1", 2: "v2"}[version], map[bool]string{false: "existing state", true: "no state"}[missingState], evidence}, "/"), func(t *testing.T) {
					path, c, state := cliMigrationFixture(t, version)
					migrationConfig := c
					migrationConfig.Version = 2
					if err := worker.MigrateState(migrationConfig, 1, 2, state.InstanceID); err != nil {
						t.Fatal(err)
					}
					if missingState {
						if err := os.Remove(filepath.Join(c.StateDir, "state.json")); err != nil {
							t.Fatal(err)
						}
					}
					migrationPath := filepath.Join(c.StateDir, "migration.json")
					raw, err := os.ReadFile(migrationPath)
					if err != nil {
						t.Fatal(err)
					}
					raw = bytes.Replace(raw, []byte(`"Phase":"verified"`), []byte(`"Phase":"pending"`), 1)
					if evidence == "malformed" {
						raw = []byte(`{`)
					}
					if err := os.WriteFile(migrationPath, raw, 0600); err != nil {
						t.Fatal(err)
					}
					checkInstallOffline(t, path, "", "requires inspection")
				})
			}
		}
	}
}

func TestWorkerCheckInstallCLIArgumentIsolation(t *testing.T) {
	path, c, state := cliMigrationFixture(t, 1)
	before := installLedgerBytes(t, c.StateDir)
	for _, tc := range []struct {
		flags []string
		want  string
	}{
		{[]string{"--task", "https://github.com/code/repo/issues/1"}, "task/goal flags require task-metadata"},
		{[]string{"--goal", "https://github.com/orgs/planning/discussions/1"}, "task/goal flags require task-metadata"},
		{[]string{"--request-id", "request"}, "request-id requires requests"},
		{[]string{"--job-id", "job"}, "intent flags require recover"},
		{[]string{"--session-id", "ses_saved"}, "intent flags require recover"},
		{[]string{"--message-id", "msg_saved"}, "intent flags require recover"},
		{[]string{"--from", "1"}, "migration flags require migrate-state"},
		{[]string{"--to", "2"}, "migration flags require migrate-state"},
		{[]string{"--expected-instance", state.InstanceID}, "migration flags require migrate-state"},
		{[]string{"--force"}, "requires --config"},
		{[]string{"extra"}, "requires --config"},
	} {
		t.Run(tc.flags[0], func(t *testing.T) {
			var out bytes.Buffer
			err := run(append([]string{"check-install", "--config", path}, tc.flags...), &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) || out.Len() != 0 {
				t.Fatalf("error=%v output=%q, want %q and no output", err, out.String(), tc.want)
			}
		})
	}
	for _, command := range []string{"run", "validate", "preflight", "dry-run", "status", "pause", "resume", "task-metadata", "ping", "requests", "recover", "migrate-state"} {
		t.Run(command, func(t *testing.T) {
			var out bytes.Buffer
			err := run([]string{command, "--config", path, "--previous-config", path}, &out)
			if err == nil || !strings.Contains(err.Error(), "previous-config requires check-install") || out.Len() != 0 {
				t.Fatalf("error=%v output=%q, want previous-config isolation", err, out.String())
			}
		})
	}
	if !reflect.DeepEqual(before, installLedgerBytes(t, c.StateDir)) {
		t.Fatal("invalid arguments changed ledger")
	}
}

func TestWorkerCheckInstallExamplesOffline(t *testing.T) {
	_, _, _ = cliMigrationFixture(t, 1) // Install the offline client-construction trap.
	for _, name := range []string{"worker.json", "worker-team.json"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "examples", name))
			if err != nil {
				t.Fatal(err)
			}
			var c worker.Config
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			// Never inspect the example's live-looking installation paths.
			c.StateDir, c.WorkRoot = filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "work")
			c.OpenCode.PasswordFile = filepath.Join(t.TempDir(), "nonexistent-password")
			checkInstallOffline(t, installConfigCopy(t, c), "", "")
		})
	}
}
