package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/worker"
)

func TestWorkerMemoryDoesNotDependOnKata(t *testing.T) {
	cfg, _ := teamCLIConfig(t, "qa")
	cfg.Version = 3
	cfg.Master = &worker.MasterConfig{Backend: "master", MasterID: "memory-test", Engagement: "coop", AgentID: cfg.Name, HostID: "host", InstanceID: strings.Repeat("a", 64), Generation: 1}
	cfg.TaskSource = nil
	var wrongPlacement atomic.Bool
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer memory-test-token" {
			t.Error("wrong identity")
		}
		w.Header().Set("X-Coop-Master-ID", "memory-test")
		var data any
		switch r.URL.Path {
		case "/api/v1/identity":
			m := cfg.Master
			placement := c.Placement{AgentID: m.AgentID, HostID: m.HostID, InstanceID: m.InstanceID, Generation: m.Generation}
			if wrongPlacement.Load() {
				placement.Generation++
			}
			data = c.SelfIdentity{Principal: c.Principal{ID: m.AgentID, Kind: "agent", GitHubBotID: cfg.Self.ID}, Placement: placement, RestoreEpoch: 1}
		case "/api/v1/memory/list":
			calls.Add(1)
			data = c.MemoryPage{Entries: []c.MemorySummary{{AgentID: cfg.Name, Key: "notes", Revision: 1}}}
		case "/api/v1/memory/read":
			calls.Add(1)
			data = c.Memory{MemorySummary: c.MemorySummary{AgentID: cfg.Name, Key: "notes", Revision: 1}, Body: "knowledge"}
		case "/api/v1/memory/write":
			var in c.MemoryWrite
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.Key != "notes" || in.Body != "knowledge" || in.ExpectedRevision != 1 {
				t.Error(in)
			}
			calls.Add(1)
			data = c.Memory{MemorySummary: c.MemorySummary{AgentID: cfg.Name, Key: in.Key, Revision: 2}, Body: in.Body}
		default:
			t.Error("memory contacted task/provider API", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"master_id": "memory-test", "restore_epoch": 1, "data": data})
	}))
	defer server.Close()
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	client := filepath.Join(dir, "client.toml")
	input := filepath.Join(dir, "input.json")
	for name, body := range map[string]string{token: "memory-test-token", client: fmt.Sprintf("url = %q\nmaster_id = \"memory-test\"\ntoken_file = %q\nallow_loopback_http = true\n", server.URL, token)} {
		if err := os.WriteFile(name, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Master.ClientConfig = client
	config := installConfigCopy(t, cfg)
	for _, tc := range []struct{ op, input string }{{"list", ""}, {"read", `{"key":"notes"}`}, {"write", `{"key":"notes","body":"knowledge","expected_revision":1}`}} {
		args := []string{"memory", tc.op, "--config", config}
		if tc.input != "" {
			if err := os.WriteFile(input, []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			args = append(args, "--input", input)
		}
		var out bytes.Buffer
		if err := run(args, &out); err != nil || out.Len() == 0 {
			t.Fatal(tc.op, out.String(), err)
		}
	}
	wrongPlacement.Store(true)
	if err := run([]string{"memory", "list", "--config", config}, io.Discard); err == nil || calls.Load() != 3 {
		t.Fatal("accepted another placement", err, calls.Load())
	}
}

func TestWorkerMemoryRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"read", "--config", "/private/config"}, {"write", "--config", "/private/config"}, {"list", "--config", "/private/config", "--path", "/private/memory"}, {"import-markdown", "--config", "/private/config"}} {
		if err := memoryCommand(args, io.Discard); err == nil {
			t.Fatal("accepted invalid memory arguments", args)
		}
	}
}
