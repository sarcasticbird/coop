package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/worker"
)

func cliMigrationFixture(t *testing.T, version int) (string, worker.Config, worker.State) {
	t.Helper()
	path := cliConfig(t)
	c, err := worker.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if version == 2 {
		c.Version, c.Role = 2, ""
		c.Team = &worker.TeamPolicy{Version: 2, Planner: "planner", Members: []worker.TeamMember{
			{Name: c.Name, Actor: c.Self, Capabilities: []string{"implement"}},
			{Name: "planner", Actor: c.Reviewer, Capabilities: []string{"plan", "engineering-review"}},
		}, EngineeringPeers: map[string]string{c.Name: "planner"}}
		c.Implementer, c.Reviewer = worker.Actor{}, worker.Actor{}
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		c, err = worker.LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	store := worker.NewFileStore(c.StateDir)
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	old := clients
	t.Cleanup(func() { clients = old })
	clients = func(worker.Config) (worker.GitHub, *worker.OpenCodeClient, error) {
		t.Fatal("offline migration constructed GitHub/OpenCode clients")
		return nil, nil, nil
	}
	return path, c, state
}

func TestWorkerCLIMigrateStateOffline(t *testing.T) {
	path, c, before := cliMigrationFixture(t, 2)
	var out bytes.Buffer
	if err := run([]string{"migrate-state", "--config", path, "--from", "1", "--to", "2", "--expected-instance", before.InstanceID}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\"state\":\"migration_verified\"}\n" {
		t.Fatalf("unexpected migration result: %q", out.String())
	}
	after, err := worker.NewVersionedFileStore(c.StateDir, 2).Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != 2 || after.InstanceID != before.InstanceID {
		t.Fatalf("migration did not preserve worker identity: version=%d instance=%q", after.Version, after.InstanceID)
	}
	for _, name := range []string{"migration.json", "migration-backup.json"} {
		info, err := os.Stat(filepath.Join(c.StateDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s permissions = %o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestWorkerCLIMigrateStateRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		flags   []string
		want    string
	}{
		{"v1 config", 1, []string{"--from", "1", "--to", "2", "--expected-instance", "INSTANCE"}, "migration requires v2 config"},
		{"missing from", 2, []string{"--to", "2", "--expected-instance", "INSTANCE"}, "migration requires"},
		{"missing to", 2, []string{"--from", "1", "--expected-instance", "INSTANCE"}, "migration requires"},
		{"missing instance", 2, []string{"--from", "1", "--to", "2"}, "migration requires"},
		{"malformed instance", 2, []string{"--from", "1", "--to", "2", "--expected-instance", "not-an-instance"}, "migration requires"},
		{"foreign instance", 2, []string{"--from", "1", "--to", "2", "--expected-instance", worker.BodyHash("foreign worker")}, "instance mismatch"},
		{"reverse", 2, []string{"--from", "2", "--to", "1", "--expected-instance", "INSTANCE"}, "rollback is unsupported"},
		{"wrong source", 2, []string{"--from", "3", "--to", "2", "--expected-instance", "INSTANCE"}, "migration requires"},
		{"wrong target", 2, []string{"--from", "1", "--to", "3", "--expected-instance", "INSTANCE"}, "migration requires"},
		{"task", 2, []string{"--task", "https://github.com/code/repo/issues/1"}, "task/goal flags require task-metadata"},
		{"goal", 2, []string{"--goal", "https://github.com/planning/board/discussions/1"}, "task/goal flags require task-metadata"},
		{"request", 2, []string{"--request-id", "request"}, "request-id requires requests"},
		{"job", 2, []string{"--job-id", "job"}, "intent flags require recover"},
		{"session", 2, []string{"--session-id", "ses_saved"}, "intent flags require recover"},
		{"message", 2, []string{"--message-id", "msg_saved"}, "intent flags require recover"},
		{"unknown flag", 2, []string{"--force"}, "requires --config"},
		{"positional argument", 2, []string{"extra"}, "requires --config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, c, state := cliMigrationFixture(t, tc.version)
			statePath := filepath.Join(c.StateDir, "state.json")
			before, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"migrate-state", "--config", path}
			// Unrelated flags must fail even with otherwise valid migration arguments.
			switch tc.name {
			case "task", "goal", "request", "job", "session", "message", "unknown flag", "positional argument":
				args = append(args, "--from", "1", "--to", "2", "--expected-instance", state.InstanceID)
			}
			for _, arg := range tc.flags {
				if arg == "INSTANCE" {
					arg = state.InstanceID
				}
				args = append(args, arg)
			}
			var out bytes.Buffer
			if err := run(args, &out); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if out.Len() != 0 {
				t.Fatalf("refused migration emitted success output: %q", out.String())
			}
			after, err := os.ReadFile(statePath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("refused migration changed saved state: %v", err)
			}
			for _, name := range []string{"migration.json", "migration-backup.json"} {
				if _, err := os.Stat(filepath.Join(c.StateDir, name)); !os.IsNotExist(err) {
					t.Fatalf("refused migration created %s: %v", name, err)
				}
			}
		})
	}
}

func TestWorkerCLIMigrationFlagsRequireMigrationCommand(t *testing.T) {
	path, _, state := cliMigrationFixture(t, 2)
	for _, flags := range [][]string{{"--from", "1"}, {"--to", "2"}, {"--expected-instance", state.InstanceID}} {
		var out bytes.Buffer
		args := append([]string{"validate", "--config", path}, flags...)
		if err := run(args, &out); err == nil || !strings.Contains(err.Error(), "migration flags require migrate-state") {
			t.Fatalf("flags %v: error = %v", flags, err)
		}
		if out.Len() != 0 {
			t.Fatalf("invalid flags emitted output: %q", out.String())
		}
	}
}
