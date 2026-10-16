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
