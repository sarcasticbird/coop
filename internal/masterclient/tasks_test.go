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
