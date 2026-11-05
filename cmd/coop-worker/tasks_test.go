package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/worker"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTasksCommandRequiresStructuredExplicitInput(t *testing.T) {
	for _, args := range [][]string{nil, {"approve"}, {"create"}, {"create", "--config", "/private/config"}, {"read", "--config", "/private/config", "--task", "kata://bogus"}, {"capabilities", "--config", "/private/config", "--input", "/private/input"}} {
		if err := tasksCommand(args, io.Discard); err == nil {
			t.Fatalf("accepted invalid tasks command %v", args)
		}
	}
}

func TestTasksPlanningHelperUsesMasterPolicyForCoordinator(t *testing.T) {
	cfg, _ := teamCLIConfig(t, "qa")
	canonicalPolicy, err := worker.TeamPolicySHA(*cfg.Team)
	if err != nil {
		t.Fatal(err)
	}
	actor := worker.Actor{ID: 99, Login: "rook[bot]", Kind: "Bot"}
	cfg.Team.Members = append(cfg.Team.Members, worker.TeamMember{Name: "rook", Actor: actor})
	cfg.Name, cfg.Self, cfg.Version = "rook", actor, 3
	project := "01K00000000000000000000000"
	cfg.TaskSource = &worker.TaskSourceConfig{Provider: "kata", ProjectUID: project}
	cfg.Master = &worker.MasterConfig{Backend: "master", MasterID: "master", Engagement: "coop", AgentID: "rook", HostID: "host", InstanceID: strings.Repeat("a", 64), Generation: 1}
	creates := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer planning-test-token" || r.Header.Get("X-Coop-Expected-Master-ID") != "master" {
			t.Error("planning helper lost authenticated master pin")
		}
		w.Header().Set("X-Coop-Master-ID", "master")
		var data any
		switch r.URL.Path {
		case "/api/v1/tasks/capabilities":
			data = c.TaskCapabilities{Provider: "kata", ProjectUID: project, PolicySHA: canonicalPolicy}
		case "/api/v1/tasks/adopt":
			var in c.TaskAdoptInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.Reference != "native-reference" || in.ExpectedRevision != 11 || in.ExpectedApprovalSequence != 7 || in.RequestID != "rook-adopt-task" {
				t.Error("adopt helper changed observed intent")
			}
			creates++
			data = c.ProviderTask{Reference: in.Reference, Goal: in.Goal}
		case "/api/v1/tasks/create":
			var in c.TaskCreateInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.RequestID != "rook-plan-task" || in.ExpectedApprovalSequence != 7 {
				t.Error("planning helper changed stable request or approval sequence")
			}
			creates++
			data = c.ProviderTask{Title: in.Title, Goal: in.Goal}
		default:
			t.Errorf("unexpected helper call %s", r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"master_id": "master", "restore_epoch": 1, "data": data}); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	clientPath := filepath.Join(dir, "client.toml")
	tokenPath := filepath.Join(dir, "token")
	inputPath := filepath.Join(dir, "create.json")
	if err = os.WriteFile(tokenPath, []byte("planning-test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(clientPath, []byte(fmt.Sprintf("url = %q\nmaster_id = %q\ntoken_file = %q\nallow_loopback_http = true\n", srv.URL, "master", tokenPath)), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Master.ClientConfig = clientPath
	configPath := installConfigCopy(t, cfg)
	in := c.TaskCreateInput{Goal: c.GoalRef{Source: "master", MasterID: "master", GoalID: "goal", RevisionSHA256: strings.Repeat("b", 64)}, ExpectedApprovalSequence: 7, Repository: "code/repo", BaseBranch: "main", Title: "Planned task", Body: "Approved scope", RequestID: "rook-plan-task"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(inputPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = run([]string{"tasks", "create", "--config", configPath, "--input", inputPath}, &out); err != nil {
		t.Fatal("coordinator planning required engineering roster", err)
	}
	if creates != 1 || !bytes.Contains(out.Bytes(), []byte("Planned task")) {
		t.Fatalf("planning response missing: %s", out.String())
	}
	adopt := c.TaskAdoptInput{Reference: "native-reference", Goal: in.Goal, Repository: in.Repository, BaseBranch: in.BaseBranch, ExpectedRevision: 11, ExpectedApprovalSequence: 7, RequestID: "rook-adopt-task"}
	raw, _ = json.Marshal(adopt)
	if err = os.WriteFile(inputPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = run([]string{"tasks", "adopt", "--config", configPath, "--input", inputPath}, &out); err != nil {
		t.Fatal(err)
	}
	if creates != 2 || !bytes.Contains(out.Bytes(), []byte("native-reference")) {
		t.Fatalf("adopt response %s", out.String())
	}
	if len(cfg.Team.Members[len(cfg.Team.Members)-1].Capabilities) != 0 {
		t.Fatal("helper assigned coordinator engineering capabilities")
	}
}

func TestTasksHelperDoesNotRequireAJobForRoutineKata(t *testing.T) {
	cfg, _ := teamCLIConfig(t, "qa")
	cfg.Version = 3
	project := "01K00000000000000000000000"
	cfg.TaskSource = &worker.TaskSourceConfig{Provider: "kata", ProjectUID: project}
	cfg.Master = &worker.MasterConfig{Backend: "master", MasterID: "master", Engagement: "coop", AgentID: cfg.Name, HostID: "host", InstanceID: strings.Repeat("a", 64), Generation: 1}
	calls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer routine-task-token" || r.Header.Get("X-Coop-Expected-Master-ID") != "master" {
			t.Error("lost worker/master authentication")
		}
		w.Header().Set("X-Coop-Master-ID", "master")
		var data any
		switch r.URL.Path {
		case "/api/v1/tasks/capabilities":
			data = c.TaskCapabilities{Provider: "kata", ProjectUID: project, PolicySHA: strings.Repeat("b", 64)}
		case "/api/v1/tasks/create":
			var in c.TaskCreateInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.Goal != (c.GoalRef{}) || in.ExpectedApprovalSequence != 0 || in.Title != "Ordinary task" {
				t.Error("created an engineering approval request", in)
			}
			calls = append(calls, "create")
			data = c.ProviderTask{Reference: project, Title: in.Title}
		case "/api/v1/tasks/update":
			var in c.TaskUpdateInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.Reference != project || in.RequestID != "stable-"+in.Operation || in.ExpectedRevision != 9 && in.Operation != "comment" {
				t.Error("helper changed task operation", in)
			}
			calls = append(calls, in.Operation)
			data = c.ProviderTask{Reference: project, Revision: 10}
		default:
			t.Error("unexpected helper call", r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"master_id": "master", "restore_epoch": 1, "data": data}); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	token, client := filepath.Join(dir, "token"), filepath.Join(dir, "client.toml")
	if err := os.WriteFile(token, []byte("routine-task-token"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(client, fmt.Appendf(nil, "url = %q\nmaster_id = 'master'\ntoken_file = %q\nallow_loopback_http = true\n", srv.URL, token), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Master.ClientConfig = client
	config, input := installConfigCopy(t, cfg), filepath.Join(dir, "input.json")
	invoke := func(op string, payload any) error {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(input, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return run([]string{"tasks", op, "--config", config, "--input", input}, io.Discard)
	}
	if err := invoke("create", map[string]any{"title": "Ordinary task", "body": "Context", "request_id": "ordinary-create"}); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"edit", "claim", "assign", "comment", "close", "reopen"} {
		in := map[string]any{"reference": project, "request_id": "stable-" + op, "expected_revision": 9}
		switch op {
		case "edit":
			in["title"] = "Updated"
		case "assign":
			in["owner"] = "peer"
		case "comment":
			delete(in, "expected_revision")
			in["message"] = "Response"
		case "close":
			in["reason"], in["message"] = "wontfix", "No longer needed"
		}
		if err := invoke(op, in); err != nil {
			t.Fatal(op, err)
		}
	}
	if len(calls) != 7 {
		t.Fatal("routine operations did not reach existing provider", calls)
	}
	for _, in := range []map[string]any{
		{"reference": project, "request_id": "forged", "message": "No", "actor": "owner"},
		{"reference": project, "request_id": "forged", "message": "No", "operation": "reopen"},
		{"reference": project, "request_id": "forged", "message": "No", "expected_revision": 9},
	} {
		if err := invoke("comment", in); err == nil {
			t.Fatal("accepted mixed or caller-chosen authority", in)
		}
	}
	if len(calls) != 7 {
		t.Fatal("malformed inputs reached task mutation endpoint")
	}
}
