package taskstore

import (
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKataAdoptAtomicCASAndLostReply(t *testing.T) {
	cfg := testConfig(t)
	cfg.InstanceUID = testProject
	cfg.Binary = filepath.Join(t.TempDir(), "kata")
	socket := filepath.Join(t.TempDir(), "kata.sock")
	if err := os.WriteFile(cfg.Binary, []byte("#!/bin/sh\nprintf '%s' '{\"kind\":\"local\",\"source\":\"local_default\",\"network\":\"unix\",\"address\":\"unix://"+socket+"\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Home, "config.toml"), []byte("[auth]\ntoken = 'fixture-token'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	issue := Issue{UID: testIssue, ProjectUID: testProject, Title: "Native scope", Body: "Requested work", Revision: 4, Status: "open", Metadata: map[string]json.RawMessage{}}
	writes := 0
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing authenticated native session")
		}
		switch {
		case r.URL.Path == "/api/v1/instance":
			if _, err := fmt.Fprintf(w, `{"instance_uid":%q}`, testProject); err != nil {
				t.Error(err)
			}
		case r.URL.Path == "/api/v1/projects":
			if _, err := fmt.Fprintf(w, `{"projects":[{"id":1,"uid":%q,"name":%q}]}`, testProject, cfg.ProjectName); err != nil {
				t.Error(err)
			}
		case r.Method == "GET":
			if err := json.NewEncoder(w).Encode(map[string]any{"issue": issue}); err != nil {
				t.Error(err)
			}
		case r.Method == "POST":
			if r.Header.Get("If-Match") != fmt.Sprintf(`"rev-%d"`, issue.Revision) {
				w.WriteHeader(412)
				return
			}
			var input struct {
				Actor string                     `json:"actor"`
				Patch map[string]json.RawMessage `json:"patch"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				t.Error("invalid metadata patch")
			}
			if input.Actor != "planner" || len(input.Patch) != 2 {
				t.Errorf("non-atomic scope patch %+v", input)
			}
			for k, v := range input.Patch {
				issue.Metadata[k] = v
			}
			issue.Revision++
			writes++
			// The provider committed but its HTTP reply was lost.
			w.WriteHeader(503)
		}
	}))
	srv.Listener = listener
	srv.Start()
	defer srv.Close()
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := c.KataTaskReference(testProject, testIssue, "example/coop")
	in := c.TaskAdoptInput{Reference: ref, Repository: "example/coop", Goal: c.GoalRef{Source: "master", MasterID: "test", GoalID: "goal", RevisionSHA256: strings.Repeat("b", 64)}, BaseBranch: "main", ExpectedRevision: 4, ExpectedApprovalSequence: 1, RequestID: "adopt"}
	binding := c.NativeTaskBinding{Version: 3, TaskURL: ref, Goal: in.Goal, TaskScopeSHA: hash(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{issue.Title, issue.Body}), PolicySHA: cfg.PolicySHA, ApprovalSequence: 1, AuthorID: 1, PeerID: 2, BaseBranch: "main"}
	adopter := b.(AdoptionBackend)
	task, err := adopter.Adopt(t.Context(), in, binding, c.Principal{ID: "planner"})
	if err != nil || task.IssueUID != testIssue || task.Revision != 5 || writes != 1 {
		t.Fatalf("lost reply %+v %v writes=%d", task, err, writes)
	}
	if _, err = adopter.Adopt(t.Context(), in, binding, c.Principal{ID: "planner"}); err != nil || writes != 1 {
		t.Fatal("replay mutated", writes, err)
	}
	issue.Body = "edited"
	issue.Revision++
	if _, err = adopter.Adopt(t.Context(), in, binding, c.Principal{ID: "planner"}); !errors.Is(err, c.ErrConflict) {
		t.Fatal("scope edit accepted", err)
	}
}
