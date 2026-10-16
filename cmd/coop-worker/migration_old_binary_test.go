package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/internal/worker"
)

// The compatibility lane supplies a binary built from the pre-migration source.
func TestMigrationSavedOldBinaryRefusesV2(t *testing.T) {
	binary := os.Getenv("COOP_TEST_OLD_WORKER")
	if binary == "" {
		t.Skip("set COOP_TEST_OLD_WORKER to a saved v1 binary")
	}
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprintf("config-v%d", version), func(t *testing.T) {
			path, c, state := cliMigrationFixture(t, version)
			c.DiscussionRepositories = []string{"planning/board"}
			c.Wake = &worker.WakeConfig{Enabled: true, CoordinationURL: "https://github.com/planning/board/discussions/1", PeerHopLimit: 2}
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := worker.LoadConfig(path); err != nil {
				t.Fatal(err)
			}
			live := func(ledgerVersion int) func() {
				store := worker.NewVersionedFileStore(c.StateDir, ledgerVersion)
				release, err := store.Lock()
				if err != nil {
					t.Fatal(err)
				}
				if err := store.WriteStatus(worker.Status{Enabled: true, WakeEnabled: true, State: "idle", Heartbeat: time.Now().UTC()}); err != nil {
					release()
					t.Fatal(err)
				}
				// Model the saved generation's status writer, not a new status
				// field rejection that would hide whether the inbox fence works.
				statusPath := filepath.Join(c.StateDir, "status.json")
				raw, err := os.ReadFile(statusPath)
				if err != nil {
					t.Fatal(err)
				}
				var status map[string]json.RawMessage
				if err := json.Unmarshal(raw, &status); err != nil {
					t.Fatal(err)
				}
				delete(status, "actor")
				raw, err = json.Marshal(status)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(statusPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := worker.WorkerAcceptingWake(c.StateDir, time.Now().UTC()); err != nil {
					release()
					t.Fatal(err)
				}
				return release
			}
			ping := func(id string) ([]byte, error) {
				cmd := exec.Command(binary, "ping", "--config", path)
				cmd.Stdin = strings.NewReader(`{"request_id":"` + id + `","prompt":"compatibility fixture"}`)
				return cmd.CombinedOutput()
			}
			// Positive control: the same old command/config accepts a ping while
			// the original v1 ledger and a live lock/status are present.
			release := live(1)
			output, err := ping(worker.BodyHash("before conversion"))
			release()
			if err != nil {
				t.Fatalf("old ping positive control failed: %v %s", err, output)
			}
			c.Version = 2
			if err := worker.MigrateState(c, 1, 2, state.InstanceID); err != nil {
				t.Fatal(err)
			}
			output, err = exec.Command(binary, "dry-run", "--config", path).CombinedOutput()
			if err == nil || !strings.Contains(string(output), "invalid worker state") {
				t.Fatalf("saved binary did not reject v2 ledger: %v %s", err, output)
			}
			inbox := filepath.Join(c.StateDir, "wake", "v1", "inbox")
			snapshot := func() map[string]string {
				entries, err := os.ReadDir(inbox)
				if err != nil {
					t.Fatal(err)
				}
				result := map[string]string{}
				for _, entry := range entries {
					raw, err := os.ReadFile(filepath.Join(inbox, entry.Name()))
					if err != nil {
						t.Fatal(err)
					}
					result[entry.Name()] = string(raw)
				}
				return result
			}
			release = live(2)
			defer release()
			before := snapshot()
			output, err = ping(worker.BodyHash("after conversion"))
			if err == nil || !strings.Contains(string(output), "invalid wake inbox entry") {
				t.Errorf("saved binary did not reject v2 wake intake: %v %s", err, output)
			}
			if !reflect.DeepEqual(before, snapshot()) {
				t.Error("old ping changed migrated inbox")
			}
			if version == 2 {
				var out bytes.Buffer
				input := `{"request_id":"` + worker.BodyHash("v2 producer") + `","prompt":"supported producer"}`
				if err := runInput([]string{"ping", "--config", path}, strings.NewReader(input), &out); err != nil {
					t.Fatalf("new v2 producer rejected: %v", err)
				}
				if !strings.Contains(out.String(), "queued") {
					t.Fatalf("new producer missing receipt: %s", out.String())
				}
			}
		})
	}
}

func TestMigrationMissingWakeTreeBlocksOldProducerHandoff(t *testing.T) {
	binary := os.Getenv("COOP_TEST_OLD_WORKER")
	if binary == "" {
		t.Skip("set COOP_TEST_OLD_WORKER to a saved v1 binary")
	}
	_, c, state := cliMigrationFixture(t, 2)
	c.DiscussionRepositories = []string{"planning/board"}
	c.Wake = &worker.WakeConfig{Enabled: true, CoordinationURL: "https://github.com/planning/board/discussions/1", PeerHopLimit: 2}
	path := installConfigCopy(t, c)
	old := c
	old.Version, old.Team, old.Role = 1, nil, "implementer"
	old.Implementer, old.Reviewer = c.Self, worker.Actor{ID: 3, Login: "reviewer[bot]", Kind: "Bot"}
	oldPath := installConfigCopy(t, old)
	if err := worker.MigrateState(c, 1, 2, state.InstanceID); err != nil {
		t.Fatal(err)
	}
	// A partial restore must not pass the prerequisite for any producer
	// handoff. Old binaries cannot protect themselves once their fence is lost.
	wake := filepath.Join(c.StateDir, "wake")
	if err := os.RemoveAll(wake); err != nil {
		t.Fatal(err)
	}
	// Keep status in the saved generation's wire shape so it cannot mask a
	// missing-fence bypass if the installation check incorrectly succeeds.
	raw, err := json.Marshal(map[string]any{"enabled": true, "wake_enabled": true, "state": "idle", "heartbeat": time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.StateDir, "status.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	before := installLedgerBytes(t, c.StateDir)
	var out bytes.Buffer
	gateErr := run([]string{"check-install", "--config", path}, &out)
	if gateErr == nil {
		// Exercise the real saved producer only if the prerequisite permits
		// it, reproducing the unsafe handoff on the unfixed implementation.
		release, err := worker.NewVersionedFileStore(c.StateDir, 2).Lock()
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		cmd := exec.Command(binary, "ping", "--config", oldPath)
		cmd.Stdin = strings.NewReader(`{"request_id":"` + worker.BodyHash("partial restore") + `","prompt":"compatibility fixture"}`)
		output, err := cmd.CombinedOutput()
		t.Errorf("installation gate permitted old producer: err=%v output=%s", err, output)
	} else if !strings.Contains(gateErr.Error(), "requires complete fenced wake storage") {
		t.Fatalf("unexpected handoff refusal: %v", gateErr)
	}
	if out.Len() != 0 || !reflect.DeepEqual(before, installLedgerBytes(t, c.StateDir)) {
		t.Error("refused handoff emitted success or changed ledger/inbox bytes")
	}
	if _, err := os.Lstat(wake); !os.IsNotExist(err) {
		t.Errorf("old producer recreated missing wake tree: %v", err)
	}
}
