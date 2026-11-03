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
	"sync/atomic"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/runtime"
)

func TestMemoryCommandsUseMasterWithoutVM(t *testing.T) {
	previous := newRuntime
	t.Cleanup(func() { newRuntime = previous })
	newRuntime = func() runtime.Runtime { t.Fatal("memory started a VM"); return nil }
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer memory-test-token" || r.Header.Get("X-Coop-Expected-Master-ID") != "memory-test" {
			t.Error("memory request lost identity pin")
		}
		var data any
		switch r.URL.Path {
		case "/api/v1/memory/list":
			var in c.MemoryList
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.AgentID != "rook" || in.Prefix != "projects/" {
				t.Error(in)
			}
			data = c.MemoryPage{Entries: []c.MemorySummary{{AgentID: "rook", Key: "projects/coop.md", Revision: 1}}}
		case "/api/v1/memory/read":
			var in c.MemoryQuery
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.AgentID != "rook" || in.Key != "projects/coop.md" {
				t.Error(in)
			}
			data = c.Memory{MemorySummary: c.MemorySummary{AgentID: "rook", Key: in.Key, Revision: 1}, Body: "knowledge"}
		case "/api/v1/memory/write":
			var in c.MemoryWrite
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.AgentID != "rook" || in.Key != "projects/coop.md" || in.Body != "knowledge" || in.ExpectedRevision != 1 {
				t.Error(in)
			}
			data = c.Memory{MemorySummary: c.MemorySummary{AgentID: "rook", Key: in.Key, Revision: 2}, Body: in.Body}
		default:
			t.Error("unexpected endpoint", r.URL.Path)
		}
		w.Header().Set("X-Coop-Master-ID", "memory-test")
		_ = json.NewEncoder(w).Encode(map[string]any{"master_id": "memory-test", "restore_epoch": 1, "data": data})
	}))
	defer server.Close()
	dir := t.TempDir()
	token, file, config := filepath.Join(dir, "token"), filepath.Join(dir, "body"), filepath.Join(dir, "client.toml")
	for name, body := range map[string]string{token: "memory-test-token", file: "knowledge", config: fmt.Sprintf("url = %q\nmaster_id = \"memory-test\"\ntoken_file = %q\nallow_loopback_http = true\n", server.URL, token)} {
		if err := os.WriteFile(name, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"list", "--prefix", "projects/"}, {"read", "projects/coop.md"}, {"write", "projects/coop.md", "--file", file, "--revision", "1"}} {
		cmd := root()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append([]string{"--master-config", config, "memory", "--bird", "rook"}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatal(args, err)
		}
		if out.Len() == 0 {
			t.Fatal("missing JSON output")
		}
	}
	if calls.Load() != 3 {
		t.Fatal(calls.Load())
	}
	for _, args := range [][]string{{"write", "key", "--file", file}, {"write", "../invalid", "--file", file, "--revision", "0"}, {"read"}, {"read", "../invalid"}} {
		cmd := root()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append([]string{"--master-config", config, "memory"}, args...))
		if err := cmd.Execute(); err == nil {
			t.Fatal("accepted invalid command", args)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("invalid command contacted master", calls.Load())
	}
}
