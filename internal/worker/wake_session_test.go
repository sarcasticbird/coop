package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testWakeRequest(c Config) WakeRequest {
	n := time.Now().UTC()
	s := sourceFixture(c, "source", "@builder answer", c.Owner, n)
	s.Revision = ConversationRevision(s)
	return WakeRequest{Version: 1, ID: BodyHash("wake"), ChainID: BodyHash("chain"), Kind: "discussion", Target: c.Name, Source: s, CreatedAt: n}
}
func TestWakePromptPreservesOwnerTextAndIdentity(t *testing.T) {
	c, _ := wakeFixture(t)
	r := testWakeRequest(c)
	r.Kind = "ping"
	r.Prompt = "check `head`\nthen explain $(literal) café"
	prompt, err := BuildWakePrompt(c, r, r.Source, Snapshot{})
	if !strings.Contains(prompt, "public or unknown visibility") || !strings.Contains(prompt, "private source code") {
		t.Fatal("missing disclosure boundary")
	}
	if err != nil || !strings.Contains(prompt, "Trusted runtime identity") || !strings.Contains(prompt, WakeResultMarker(r)) {
		t.Fatalf("prompt missing trusted input: %v", err)
	}
	at := strings.Index(prompt, "Quoted wake input:\n")
	if at < 0 {
		t.Fatal("missing structured input")
	}
	var input struct{ Prompt string }
	if err := json.Unmarshal([]byte(prompt[at+len("Quoted wake input:\n"):]), &input); err != nil || input.Prompt != r.Prompt {
		t.Fatal("prompt changed")
	}
}

func TestWakeCarriesVerifiedApprovalAndMetadataRepairBoundary(t *testing.T) {
	c, s, _ := policyFixture(t)
	r := testWakeRequest(c)
	r.Source.DiscussionURL = s.Goals[0].URL
	r.Source.DiscussionID = "goal"
	r.Source.RootID = "root"
	prompt, err := BuildWakePrompt(c, r, r.Source, s)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Approval Approval
		Goal     *Goal
	}
	parts := strings.SplitN(prompt, "Quoted wake input:\n", 2)
	if len(parts) != 2 || json.Unmarshal([]byte(parts[1]), &input) != nil || input.Approval.State != "approved" || input.Goal == nil {
		t.Fatal("conversation cannot see actual admission approval")
	}
	if !strings.Contains(prompt, "Administrative metadata repair") || !strings.Contains(prompt, "must run through admitted task jobs") {
		t.Fatal("missing repair/action separation")
	}
	s.Complete = false
	prompt, err = BuildWakePrompt(c, r, r.Source, s)
	if err != nil {
		t.Fatal(err)
	}
	parts = strings.SplitN(prompt, "Quoted wake input:\n", 2)
	_ = json.Unmarshal([]byte(parts[1]), &input)
	if input.Approval.State == "approved" {
		t.Fatal("partial snapshot claimed approval")
	}
}
func TestWakeCoordinationOwnershipAndIsolation(t *testing.T) {
	c, _ := wakeFixture(t)
	r := testWakeRequest(c)
	j, err := PrepareWakeJob(c, r, WakeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(j.Checkout) != "conversation" || strings.Contains(j.Checkout, "repo") {
		t.Fatal("not coordination directory")
	}
	if _, err := PrepareWakeJob(c, r, WakeBinding{Directory: j.Checkout, SessionID: "ses_wake"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(j.Checkout), "owner.json"), []byte(`{"foreign":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareWakeJob(c, r, WakeBinding{}); err == nil {
		t.Fatal("foreign directory adopted")
	}
}
func TestWakeNativeBusyBindingDoesNotCreateOrSubmit(t *testing.T) {
	dir := ""
	mutations := 0
	c, code := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations++
		}
		if r.URL.Query().Get("directory") != dir {
			t.Error("wrong directory")
		}
		switch r.URL.Path {
		case "/session/ses_wake":
			writeJSON(w, map[string]any{"id": "ses_wake", "directory": dir})
		case "/session/status":
			writeJSON(w, map[string]any{"ses_wake": map[string]string{"type": "busy"}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	r := testWakeRequest(c)
	j, err := PrepareWakeJob(c, r, WakeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	dir = j.Checkout
	j.SessionID = "ses_wake"
	_, err = code.EnsureSession(context.Background(), c, j)
	if errorCategory(err) != "session_busy" || mutations != 0 {
		t.Fatal("busy session mutated")
	}
}
