package worker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func apiFixture(t *testing.T, handler http.HandlerFunc) (Config, *OpenCodeClient) {
	t.Helper()
	cfg := Defaults(configFixture(t))
	if err := os.WriteFile(cfg.OpenCode.PasswordFile, []byte("fixture-password"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "opencode" || p != "fixture-password" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/agent" {
			writeJSON(w, []any{map[string]any{"name": "build", "mode": "primary", "permission": []any{}}})
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client, err := NewOpenCodeClient(cfg.OpenCode, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return cfg, client
}
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestPreflightUsesConfiguredProvidersWithoutFullCatalog(t *testing.T) {
	cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/global/health":
			writeJSON(w, ServerHealth{Healthy: true, Version: "1.18.18"})
		case "/doc":
			paths := map[string]any{}
			for _, path := range []string{"/session", "/session/status", "/session/{sessionID}/prompt_async", "/session/{sessionID}/message/{messageID}", "/session/{sessionID}/abort", "/permission", "/question", "/agent"} {
				paths[path] = map[string]any{"get": map[string]any{}}
			}
			writeJSON(w, map[string]any{"paths": paths})
		case "/config/providers":
			writeJSON(w, map[string]any{"providers": []any{map[string]any{"id": "example", "models": map[string]any{"model": map[string]any{}}}}})
		default:
			t.Errorf("unexpected preflight endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	if err := client.Preflight(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}
func TestOpenCodeResumesExactSessionWithExplicitDirectory(t *testing.T) {
	dir := ""
	created := 0
	c, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("directory") != dir {
			t.Error("directory missing")
		}
		switch r.URL.Path {
		case "/session":
			if r.Method == "POST" {
				created++
				writeJSON(w, map[string]any{"id": "ses_task", "directory": dir})
			} else {
				writeJSON(w, []any{})
			}
		case "/session/ses_task":
			writeJSON(w, map[string]any{"id": "ses_task", "directory": dir})
		case "/session/status":
			writeJSON(w, map[string]any{})
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	dir = filepath.Join(c.WorkRoot, "task")
	j := Job{Kind: "implement", TaskURL: "https://github.com/code/repo/issues/4", Checkout: dir}
	first, err := client.EnsureSession(context.Background(), c, j)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.EnsureSession(context.Background(), c, first)
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 || second.SessionID != "ses_task" {
		t.Fatal("session recreated or unrelated session adopted")
	}
}
func TestOpenCodeAmbiguousSubmissionReconcilesExactMessage(t *testing.T) {
	posts := 0
	messageID := "msg_fixture"
	dir := ""
	accepted := false
	c, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("directory") != dir {
			t.Error("directory missing")
		}
		switch r.URL.Path {
		case "/session/ses_task/prompt_async":
			posts++
			var request struct {
				MessageID string `json:"messageID"`
				Model     struct{ ProviderID, ModelID string }
				Parts     []struct{ Type, Text string }
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.MessageID != messageID || request.Model.ProviderID != "example" || request.Model.ModelID != "model" {
				t.Error("submission omitted exact identity or model")
			}
			accepted = true
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
		case "/session/status":
			if accepted {
				writeJSON(w, map[string]any{"ses_task": map[string]string{"type": "busy"}})
			} else {
				writeJSON(w, map[string]any{})
			}
		case "/session/ses_task":
			writeJSON(w, map[string]string{"id": "ses_task", "directory": dir})
		case "/session/ses_task/message/" + messageID:
			if !accepted {
				w.WriteHeader(404)
			} else {
				writeJSON(w, map[string]any{"info": map[string]string{"id": messageID, "sessionID": "ses_task", "role": "user"}, "parts": []any{}})
			}
		case "/session/ses_task/message":
			writeJSON(w, []any{map[string]any{"info": map[string]string{"id": messageID, "sessionID": "ses_task", "role": "user"}}})
		case "/permission", "/question":
			writeJSON(w, []any{})
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	dir = filepath.Join(c.WorkRoot, "task")
	j := Job{SessionID: "ses_task", MessageID: messageID, Checkout: dir, Deadline: time.Now().Add(time.Minute)}
	turn, err := client.Submit(context.Background(), c, j, "Continue approved work")
	if err == nil || turn.State != "unknown" {
		t.Fatal("lost response treated as rejected or completed")
	}
	observed, err := client.Observe(context.Background(), j)
	if err != nil || !observed.Accepted || observed.State != "running" || posts != 1 {
		t.Fatal("accepted message not recovered without resubmission")
	}
}

func TestNativeBusySessionRefusesPolicyMutationAndSubmission(t *testing.T) {
	dir := ""
	mutations := 0
	cfg, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations++
			w.WriteHeader(500)
			return
		}
		switch r.URL.Path {
		case "/session/ses_manual":
			writeJSON(w, map[string]string{"id": "ses_manual", "directory": dir})
		case "/session/status":
			writeJSON(w, map[string]any{"ses_manual": map[string]string{"type": "busy"}})
		default:
			t.Errorf("unexpected busy-session request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	dir = filepath.Join(cfg.WorkRoot, "task")
	j := Job{TaskURL: "https://github.com/code/repo/issues/4", Kind: "implement", SessionID: "ses_manual", MessageID: "msg_next", Checkout: dir, Deadline: time.Now().Add(time.Minute)}
	if _, err := client.EnsureSession(context.Background(), cfg, j); errorCategory(err) != "session_busy" {
		t.Fatalf("busy session prepared: %v", err)
	}
	if _, err := client.Submit(context.Background(), cfg, j, "Continue approved work"); errorCategory(err) != "session_busy" {
		t.Fatalf("busy session submitted: %v", err)
	}
	if mutations != 0 {
		t.Fatal("busy native session was mutated")
	}
}
func TestOpenCodeRejectsForeignDirectoryAndRedactsErrors(t *testing.T) {
	c, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_foreign" {
			writeJSON(w, map[string]string{"id": "ses_foreign", "directory": "/workspace/manual"})
			return
		}
		w.WriteHeader(500)
		_, _ = io.WriteString(w, "fixture-password sentinel-secret")
	})
	_, err := client.EnsureSession(context.Background(), c, Job{SessionID: "ses_foreign", Checkout: filepath.Join(c.WorkRoot, "owned")})
	if err == nil {
		t.Fatal("foreign directory accepted")
	}
	if _, err := client.Health(context.Background()); err == nil || strings.Contains(err.Error(), "fixture-password") || strings.Contains(err.Error(), "sentinel-secret") {
		t.Fatal("server error exposed secret response")
	}
}
func TestOpenCodeAbortOnlyOwnedSession(t *testing.T) {
	aborted := ""
	dir := ""
	c, client := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/ses_task":
			writeJSON(w, map[string]string{"id": "ses_task", "directory": dir})
		case "/session/ses_task/message/msg_worker":
			writeJSON(w, map[string]any{"info": map[string]string{"id": "msg_worker", "sessionID": "ses_task", "role": "user"}})
		case "/session/ses_task/message":
			writeJSON(w, []any{map[string]any{"info": map[string]string{"id": "msg_worker", "sessionID": "ses_task", "role": "user"}}})
		case "/session/ses_task/abort":
			aborted = "ses_task"
			writeJSON(w, true)
		case "/session/status":
			writeJSON(w, map[string]any{"ses_manual": map[string]string{"type": "busy"}})
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	dir = filepath.Join(c.WorkRoot, "task")
	if err := client.Abort(context.Background(), Job{SessionID: "ses_task", MessageID: "msg_worker", Checkout: dir}); err != nil {
		t.Fatal(err)
	}
	if aborted != "ses_task" {
		t.Fatal("wrong session aborted")
	}
}
func TestWorkerPromptIncludesScopeAndCurrentHead(t *testing.T) {
	c, s, n := policyFixture(t)
	j := Select(c, s, State{}, n)[0]
	prompt, err := BuildPrompt(c, j, s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, s.Tasks[0].URL) || !strings.Contains(prompt, s.Goals[0].BodySHA) || !strings.Contains(prompt, "AUTOMATED COOP") {
		t.Fatal("prompt omitted verified scope or source")
	}
	policy, err := WorkerToolPolicy([]string{"/root/.local/share/opencode/auth.json"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(policy), `"question":"deny"`) || !strings.Contains(string(policy), "auth.json") {
		t.Fatal("unattended credential/question policy missing")
	}
}

func TestOpenCodeEventsAreBoundedHintsForExactSession(t *testing.T) {
	wake := make(chan struct{}, 1)
	stream := `data: {"type":"session.idle","properties":{"sessionID":"ses_manual"}}` + "\n\n" + `data: {"type":"session.idle","properties":{"sessionID":"ses_task"}}` + "\n\n"
	if err := ReadOpenCodeEvents(context.Background(), strings.NewReader(stream), "ses_task", wake); err != nil {
		t.Fatal(err)
	}
	if len(wake) != 1 {
		t.Fatal("owned session was not signaled")
	}
	if err := ReadOpenCodeEvents(context.Background(), strings.NewReader("data: "+strings.Repeat("x", (256<<10)+1)), "ses_task", wake); err == nil {
		t.Fatal("oversized event accepted")
	}
	if err := ReadOpenCodeEvents(context.Background(), strings.NewReader("data: invalid-json\n\n"), "ses_task", wake); err == nil {
		t.Fatal("malformed event accepted")
	}
}
func TestUnattendedRulesPreserveExplicitDenials(t *testing.T) {
	agent := PermissionRule{Permission: "read", Pattern: "/private/source/*", Action: "deny"}
	existing := PermissionRule{Permission: "bash", Pattern: "rm *", Action: "deny"}
	rules := guardedRules([]PermissionRule{existing}, []PermissionRule{agent})
	found := map[PermissionRule]bool{}
	for _, r := range rules {
		found[r] = true
	}
	if !found[agent] || !found[existing] || !found[PermissionRule{Permission: "question", Pattern: "*", Action: "deny"}] {
		t.Fatal("explicit denial erased by unattended defaults")
	}
}
