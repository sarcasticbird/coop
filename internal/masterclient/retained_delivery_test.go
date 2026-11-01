package masterclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestRetainedDeliveryValidationClient(t *testing.T) {
	in := c.RetainedDeliveryValidation{Goal: c.GoalRef{Source: "master", MasterID: "test-master", GoalID: "goal", RevisionSHA256: "revision"}, HandoffID: "exact-grant"}
	want := c.Handoff{ID: in.HandoffID, Sequence: 42, Input: c.HandoffInput{Goal: in.Goal, Kind: "retained-deliver"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/handoffs/retained-delivery/validate" || r.Header.Get("Authorization") != "Bearer test-only-token" || r.Header.Get("X-Coop-Expected-Master-ID") != "test-master" {
			t.Error("validation lost authentication or route")
		}
		var actual c.RetainedDeliveryValidation
		if err := json.NewDecoder(r.Body).Decode(&actual); err != nil || actual != in {
			t.Error("validation changed exact intent", actual, err)
		}
		w.Header().Set("X-Coop-Master-ID", "test-master")
		_ = json.NewEncoder(w).Encode(map[string]any{"master_id": "test-master", "restore_epoch": 1, "data": want})
	}))
	defer srv.Close()
	cl, err := New(ClientConfig{URL: srv.URL, MasterID: "test-master", Token: "test-only-token", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, e := cl.ValidateRetainedDeliveryHandoff(t.Context(), in); e != nil || got != want {
		t.Fatalf("validation response: %+v %v", got, e)
	}
}
