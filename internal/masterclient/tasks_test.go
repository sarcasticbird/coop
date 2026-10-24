package masterclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestTaskClientReportsDisabledProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Coop-Master-ID", "task-master")
		w.WriteHeader(http.StatusNotImplemented)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": c.ErrUnsupported.Error()})
	}))
	defer srv.Close()
	cl, err := New(ClientConfig{URL: srv.URL, MasterID: "task-master", Token: "test-only-token", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cl.TaskCapabilities(context.Background()); !errors.Is(err, c.ErrUnsupported) {
		t.Fatal("disabled provider misreported", err)
	}
}

func TestTaskAdoptClientPreservesAuthenticatedIntent(t *testing.T) {
	in := c.TaskAdoptInput{Reference: "kata-reference", ExpectedRevision: 7, ExpectedApprovalSequence: 3, RequestID: "stable-adoption"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/tasks/adopt" || r.Header.Get("Authorization") != "Bearer test-only-token" || r.Header.Get("X-Coop-Expected-Master-ID") != "task-master" {
			t.Error("adoption lost authentication or route")
		}
		var actual c.TaskAdoptInput
		if err := json.NewDecoder(r.Body).Decode(&actual); err != nil || actual.Reference != in.Reference || actual.RequestID != in.RequestID || actual.ExpectedRevision != 7 || actual.ExpectedApprovalSequence != 3 {
			t.Fatal("adoption changed intent", actual, err)
		}
		w.Header().Set("X-Coop-Master-ID", "task-master")
		_ = json.NewEncoder(w).Encode(map[string]any{"master_id": "task-master", "restore_epoch": 1, "data": c.ProviderTask{Reference: in.Reference, IssueUID: "same-existing-uid"}})
	}))
	defer srv.Close()
	cl, err := New(ClientConfig{URL: srv.URL, MasterID: "task-master", Token: "test-only-token", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	out, err := cl.TaskAdopt(t.Context(), in)
	if err != nil || out.IssueUID != "same-existing-uid" {
		t.Fatal(out, err)
	}
}
