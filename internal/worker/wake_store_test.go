package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func wakeFixture(t *testing.T) (Config, *FileWakeStore) {
	t.Helper()
	c := Defaults(configFixture(t))
	c.Wake = &WakeConfig{Enabled: true, CoordinationURL: "https://github.com/orgs/planning/discussions/1", PeerHopLimit: 2}
	return c, NewWakeStore(filepath.Join(c.StateDir, "wake", "v1"))
}

func TestConcurrentDistinctPingPromptsAreNotLost(t *testing.T) {
	c, store := wakeFixture(t)
	var group sync.WaitGroup
	for i := range 8 {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			_, err := QueuePing(c, store, PingInput{RequestID: BodyHash(fmt.Sprintf("request-%d", i)), Prompt: fmt.Sprintf("question %d", i)}, time.Now())
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	group.Wait()
	requests, err := store.Requests()
	if err != nil || len(requests) != 8 {
		t.Fatalf("requests=%d err=%v", len(requests), err)
	}
}

func TestWakePingIdempotencyCoalescingAndConflict(t *testing.T) {
	c, s := wakeFixture(t)
	now := time.Now()
	a, err := QueuePing(c, s, PingInput{Prompt: "literal `head`\n$(untouched)"}, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := QueuePing(c, s, PingInput{Prompt: "literal `head`\n$(untouched)"}, now.Add(time.Second))
	if err != nil || a.ID != b.ID || !b.Coalesced {
		t.Fatal("queued identical ping was not coalesced")
	}
	if _, err := QueuePing(c, s, PingInput{RequestID: a.ID, Prompt: "literal `head`\n$(untouched)"}, now.Add(time.Hour)); err != nil {
		t.Fatal("retry timestamp changed request identity")
	}
	if _, err := QueuePing(c, s, PingInput{RequestID: a.ID, Prompt: "sentinel-private-prompt"}, now); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("conflicting ID accepted or private text exposed")
	}
	r, err := s.Receipt(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.Phase = "completed"
	if err := s.SaveReceipt(r); err != nil {
		t.Fatal(err)
	}
	d, err := QueuePing(c, s, PingInput{Prompt: "literal `head`\n$(untouched)"}, now.Add(time.Hour))
	if err != nil || d.ID == a.ID {
		t.Fatal("completed ping swallowed new check-in")
	}
	raw, _ := json.Marshal(d)
	if bytes.Contains(raw, []byte("untouched")) {
		t.Fatal("summary exposed prompt")
	}
}

func TestWakeIntakeDoesNotChangeV1StateOrPause(t *testing.T) {
	c, wakes := wakeFixture(t)
	v1 := NewFileStore(c.StateDir)
	release, err := v1.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := v1.Save(newState()); err != nil {
		t.Fatal(err)
	}
	if err := v1.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(c.StateDir, "state.json"))
	if _, err := QueuePing(c, wakes, PingInput{Prompt: "keep this intact"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(c.StateDir, "state.json"))
	paused, err := v1.Paused()
	if err != nil || !paused || !bytes.Equal(before, after) {
		t.Fatal("producer changed v1 state or pause")
	}
}

func TestWakeConfigurationBoundsAndDisabledSideEffects(t *testing.T) {
	c, s := wakeFixture(t)
	c.Wake = nil
	if _, err := QueuePing(c, s, PingInput{}, time.Now()); err == nil {
		t.Fatal("disabled wake accepted")
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Fatal("disabled intake created state")
	}
	c.Wake = &WakeConfig{Enabled: true, CoordinationURL: "https://github.com/orgs/planning/discussions/1"}
	if Defaults(c).Wake.PeerHopLimit != 2 || ValidateConfig(c) != nil {
		t.Fatal("default wake policy invalid")
	}
	for _, url := range []string{"https://github.com/orgs/outsider/discussions/1", "https://github.com/code/repo/issues/1", "http://github.com/orgs/planning/discussions/1"} {
		c.Wake.CoordinationURL = url
		if ValidateConfig(c) == nil {
			t.Fatal("invalid coordination scope accepted")
		}
	}
	c.Wake.CoordinationURL = "https://github.com/orgs/planning/discussions/1"
	for _, hop := range []int{-1, 9} {
		c.Wake.PeerHopLimit = hop
		if ValidateConfig(c) == nil {
			t.Fatal("unbounded peer hops accepted")
		}
	}
	c.Wake.PeerHopLimit = 2
	for _, prompt := range []string{strings.Repeat("x", 8193), "contains\x00nul", string([]byte{0xff})} {
		if _, err := QueuePing(c, s, PingInput{Prompt: prompt}, time.Now()); err == nil {
			t.Fatal("invalid prompt accepted")
		}
	}
}

func TestWakeStoreRefusesUnsafeManagedPaths(t *testing.T) {
	for _, kind := range []string{"root", "inbox", "receipt", "routing", "lock"} {
		t.Run(kind, func(t *testing.T) {
			c, s := wakeFixture(t)
			a, err := QueuePing(c, s, PingInput{Prompt: "first"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			path := s.Dir
			switch kind {
			case "inbox":
				path = filepath.Join(s.Dir, "inbox")
			case "receipt":
				path = filepath.Join(s.Dir, "receipts", a.ID+".json")
			case "routing":
				path = filepath.Join(s.Dir, "routing.json")
			case "lock":
				path = filepath.Join(s.Dir, "producer.lock")
			}
			if _, err := os.Lstat(path); err == nil {
				if err := os.Rename(path, path+".before"); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Join(filepath.Dir(s.Dir), "outside"), path); err != nil {
				t.Fatal(err)
			}
			var check error
			switch kind {
			case "receipt":
				_, check = s.Receipt(a.ID)
			case "routing":
				_, check = s.Routing()
			default:
				_, check = QueuePing(c, s, PingInput{Prompt: "second"}, time.Now())
			}
			if check == nil {
				t.Fatal("symlink accepted")
			}
		})
	}
}

func TestWakeSnapshotAdditionsDoNotChangeV1JSON(t *testing.T) {
	s := Snapshot{Complete: true}
	before, _ := json.Marshal(s)
	s.WakeSources = []ConversationSource{{NodeID: "new-comment"}}
	after, _ := json.Marshal(s)
	if !bytes.Equal(before, after) {
		t.Fatal("ephemeral reply context changed v1 state schema")
	}
}

func TestWakeStoreRejectsPublicRootsOnRead(t *testing.T) {
	c, s := wakeFixture(t)
	a, err := QueuePing(c, s, PingInput{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Receipt(a.ID); err == nil {
		t.Fatal("public wake root accepted by receipt read")
	}
}

func TestWakeProducerProcess(t *testing.T) {
	if os.Getenv("COOP_WAKE_HELPER") != "1" {
		return
	}
	c := Defaults(configFixture(t))
	c.Wake = &WakeConfig{Enabled: true, CoordinationURL: "https://github.com/orgs/planning/discussions/1", PeerHopLimit: 2}
	_, err := QueuePing(c, NewWakeStore(os.Getenv("COOP_WAKE_HELPER_DIR")), PingInput{RequestID: os.Getenv("COOP_WAKE_HELPER_ID"), Prompt: "process payload"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
}

func TestWakeConcurrentProcessesAndPausePreserveRequests(t *testing.T) {
	c, s := wakeFixture(t)
	v1 := NewFileStore(c.StateDir)
	if err := v1.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	var commands []*exec.Cmd
	for i := range 4 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestWakeProducerProcess$")
		cmd.Env = []string{"COOP_WAKE_HELPER=1", "COOP_WAKE_HELPER_DIR=" + s.Dir, "COOP_WAKE_HELPER_ID=" + BodyHash(fmt.Sprint(i))}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	if err := v1.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	requests, err := s.Requests()
	if err != nil || len(requests) != 4 {
		t.Fatalf("requests=%d err=%v", len(requests), err)
	}
	paused, err := v1.Paused()
	if err != nil || !paused {
		t.Fatal("producer overwrote pause")
	}
}

func TestWakeCapacityAndCorruptRecordsFailClosed(t *testing.T) {
	c, s := wakeFixture(t)
	for i := range MaxWakeQueued {
		if _, err := QueuePing(c, s, PingInput{RequestID: BodyHash(fmt.Sprint(i)), Prompt: fmt.Sprint(i)}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := QueuePing(c, s, PingInput{Prompt: "capacity-overflow"}, time.Now()); err == nil {
		t.Fatal("queue capacity ignored")
	}
	requests, err := s.Requests()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, "inbox", requests[0].ID+".json")
	if err := os.WriteFile(path, []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Requests(); err == nil {
		t.Fatal("corrupt request ignored")
	}
}

func TestWakeImmutablePublishNeverOverwrites(t *testing.T) {
	_, s := wakeFixture(t)
	if err := s.ensure(true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, "inbox", BodyHash("record")+".json")
	if err := writeWakeImmutable(path, []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := writeWakeImmutable(path, []byte("replacement")); err == nil {
		t.Fatal("immutable destination overwritten")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "original" {
		t.Fatal("original input changed")
	}
}

func TestWakeSyncFailureDoesNotAcknowledgeAndRetryReconciles(t *testing.T) {
	c, s := wakeFixture(t)
	old := wakeDirectorySync
	t.Cleanup(func() { wakeDirectorySync = old })
	wakeDirectorySync = func(string) error { return errors.New("forced directory sync failure") }
	input := PingInput{RequestID: BodyHash("sync-retry"), Prompt: "preserve this"}
	if _, err := QueuePing(c, s, input, time.Now()); err == nil {
		t.Fatal("acknowledged a request before durable publication")
	}
	wakeDirectorySync = old
	if _, err := QueuePing(c, s, input, time.Now()); err != nil {
		t.Fatal(err)
	}
	requests, err := s.Requests()
	if err != nil || len(requests) != 1 {
		t.Fatal("ambiguous publication retry duplicated request")
	}
}
