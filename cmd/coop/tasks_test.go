package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
)

func TestTaskCommandsNeverStartVMForInvalidInput(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	oldRuntime, oldSession := newRuntime, runSession
	t.Cleanup(func() { newRuntime, runSession = oldRuntime, oldSession })
	newRuntime = func() runtime.Runtime { t.Fatal("task command constructed VM runtime"); return nil }
	runSession = func(*session.Session, string, []string, []string) error {
		t.Fatal("task command launched session")
		return nil
	}
	for _, args := range [][]string{{"task"}, {"task", "unknown"}, {"task", "create"}, {"task", "read"}, {"task", "list"}, {"task", "capabilities"}} {
		cmd := root()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		e := cmd.Execute()
		if len(args) == 1 {
			if e != nil {
				t.Fatal(e)
			}
		} else if e == nil {
			t.Fatal("missing safe rejection", args)
		}
	}
}
func TestTaskCommandRegistered(t *testing.T) {
	cmd := root()
	if command, _, e := cmd.Find([]string{"task", "read"}); e != nil || command.Name() != "read" {
		t.Fatal("missing task read", command, e)
	}
}

func TestTaskAdoptCommandRegistered(t *testing.T) {
	cmd := root()
	command, _, err := cmd.Find([]string{"task", "adopt"})
	if err != nil || command.Name() != "adopt" {
		t.Fatal("missing authenticated task adoption command", command.Name(), err)
	}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"task", "adopt"})
	if err = cmd.Execute(); err == nil {
		t.Fatal("adoption accepted missing explicit input")
	}
}

func TestHandoffCommandsRequireExplicitInput(t *testing.T) {
	for _, name := range []string{"first-postmerge-qa", "reassess-qa", "request-release", "handoffs"} {
		cmd := root()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		found, _, err := cmd.Find([]string{"task", name})
		if err != nil || found.Name() != name {
			t.Fatal("missing handoff command", name, err)
		}
		cmd.SetArgs([]string{"task", name})
		if cmd.Execute() == nil {
			t.Fatal("missing exact handoff input accepted", name)
		}
	}
}

func TestFirstPostmergeQACLI(t *testing.T) {
	var received c.HandoffInput
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/handoffs" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("wrong handoff endpoint or identity", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.Header().Set("X-Coop-Master-ID", "fixture-master")
		_ = json.NewEncoder(w).Encode(map[string]any{"master_id": "fixture-master", "restore_epoch": 1, "data": c.Handoff{ID: "ho-fixture", Input: received}})
	}))
	defer server.Close()
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "client.toml")
	if err := os.WriteFile(config, []byte("url = \""+server.URL+"\"\nmaster_id = \"fixture-master\"\ntoken_file = \""+token+"\"\nallow_loopback_http = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	in := c.HandoffInput{Goal: c.GoalRef{Source: "master", MasterID: "fixture-master", GoalID: "goal", RevisionSHA256: strings.Repeat("d", 64)}, TaskURL: "https://github.com/example/coop/issues/7", PRURL: "https://github.com/example/coop/pull/8", AssignmentSHA: strings.Repeat("a", 64), HeadSHA: strings.Repeat("b", 40), ArtifactSHA: strings.Repeat("c", 64), Environment: "test", EnvironmentRevision: "v1", EvidenceMessageID: "msg-evidence", RequestID: "first-qa"}
	for _, test := range []string{"inferred-kind", "explicit-kind", "wrong-kind", "missing-environment", "review", "tag", "workflow", "unknown-field", "positional-argument"} {
		t.Run(test, func(t *testing.T) {
			payload := in
			switch test {
			case "explicit-kind":
				payload.Kind = "first-postmerge-qa"
			case "wrong-kind":
				payload.Kind = "reassess-qa"
			case "missing-environment":
				payload.Environment = ""
			case "review":
				payload.ReviewID = "review"
			case "tag":
				payload.Tag = "v1.2.3"
			case "workflow":
				payload.Workflow = "release.yml"
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if test == "unknown-field" {
				raw = append(raw[:len(raw)-1], []byte(`,"execute":"anything"}`)...)
			}
			file := filepath.Join(dir, "handoff.json")
			if err = os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := root()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			args := []string{"--master-config", config, "task", "first-postmerge-qa", "--file", file}
			if test == "positional-argument" {
				args = append(args, "unexpected")
			}
			cmd.SetArgs(args)
			before := calls
			err = cmd.Execute()
			if test == "inferred-kind" || test == "explicit-kind" {
				payload.Kind = "first-postmerge-qa"
				if err != nil || calls != before+1 || received != payload {
					t.Fatalf("wrong handoff: %+v, calls=%d, err=%v", received, calls, err)
				}
			} else if err == nil || calls != before {
				t.Fatalf("invalid input reached master: calls=%d, err=%v", calls, err)
			}
		})
	}
}
