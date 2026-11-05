package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestTaskCLIRoutineOperationsUseExistingMaster(t *testing.T) {
	const uid = "01K00000000000000000000000"
	var operations []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer task-test-token" || r.Header.Get("X-Coop-Expected-Master-ID") != "task-test" {
			t.Error("task command lost authenticated master")
		}
		w.Header().Set("X-Coop-Master-ID", "task-test")
		var data any
		switch r.URL.Path {
		case "/api/v1/tasks/list":
			var in c.TaskListInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.Goal != (c.GoalRef{}) || in.After != "next-page" || in.Status != "open" {
				t.Error("list required or invented a goal", in)
			}
			operations = append(operations, "list")
			data = c.ProviderTaskPage{Tasks: []c.ProviderTask{}, Next: "following-page"}
		case "/api/v1/tasks/create":
			var in c.TaskCreateInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.Goal != (c.GoalRef{}) || in.ExpectedApprovalSequence != 0 {
				t.Error("routine create acquired goal authority")
			}
			operations = append(operations, "create")
			data = c.ProviderTask{Reference: uid, Title: in.Title}
		case "/api/v1/tasks/update":
			var in c.TaskUpdateInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.Reference != uid || in.Operation != "comment" || in.Message != "Response" {
				t.Error(in)
			}
			operations = append(operations, in.Operation)
			data = c.ProviderTask{Reference: uid}
		default:
			t.Error("unexpected operation", r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"master_id": "task-test", "restore_epoch": 1, "data": data}); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	config := filepath.Join(dir, "client.toml")
	input := filepath.Join(dir, "input.json")
	t.Setenv("TASK_TEST_TOKEN", "task-test-token")
	if err := os.WriteFile(config, fmt.Appendf(nil, "url = %q\nmaster_id = 'task-test'\ntoken_env = 'TASK_TEST_TOKEN'\nallow_loopback_http = true\n", srv.URL), 0600); err != nil {
		t.Fatal(err)
	}
	invoke := func(args ...string) error {
		cmd := root()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append([]string{"--master-config", config, "task"}, args...))
		return cmd.Execute()
	}
	if err := invoke("list", "--status", "open", "--after", "next-page"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte(`{"title":"Ordinary task","body":"Context","request_id":"task-create"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := invoke("create", "--file", input); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, fmt.Appendf(nil, `{"reference":%q,"message":"Response","request_id":"task-comment"}`, uid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := invoke("comment", "--file", input); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 3 {
		t.Fatal(operations)
	}
	if err := invoke("reopen", "--file", input); err == nil {
		t.Fatal("reopen accepted without observed revision")
	}
	if len(operations) != 3 {
		t.Fatal("invalid write reached provider")
	}
}
