package masterclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestDiagnosticsOlderMaster(t *testing.T) {
	for _, pin := range []string{"client-test", "wrong-master"} {
		t.Run(pin, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/identity" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("X-Coop-Master-ID", pin)
				_ = json.NewEncoder(w).Encode(map[string]any{"master_id": pin, "restore_epoch": 1, "data": c.SelfIdentity{RestoreEpoch: 1}})
			}))
			defer ts.Close()
			cl, err := New(ClientConfig{URL: ts.URL, MasterID: "client-test", Token: "test-token-long-enough", AllowHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			_, err = cl.AgentDiagnostics(t.Context(), "hawk")
			if pin == "client-test" && !errors.Is(err, c.ErrUnsupported) {
				t.Fatal("older master not distinguished", err)
			}
			if pin != "client-test" && !errors.Is(err, c.ErrConflict) {
				t.Fatal("wrong identity treated as unsupported", err)
			}
		})
	}
}

func TestDiagnosticsClient(t *testing.T) {
	now := time.Now().UTC()
	d := c.WorkerDiagnostics{Placement: c.Placement{AgentID: "hawk", HostID: "rack-mini", InstanceID: "instance", Generation: 1}, Version: 1, RestoreEpoch: 1, StartedAt: now, ObservedAt: now, Wait: c.DiagnosticWait{Handling: "waiting"}}
	posts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token-long-enough" || r.Header.Get("X-Coop-Expected-Master-ID") != "client-test" {
			t.Error("missing authentication/pin")
		}
		w.Header().Set("X-Coop-Master-ID", "client-test")
		var data any = c.AgentDiagnostics{Report: d, ReceivedAt: now}
		if r.Method == "POST" {
			var report c.WorkerDiagnostics
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil || report.AgentID != "hawk" {
				t.Error(err)
			}
			posts++
			data = map[string]bool{"ok": true}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"master_id": "client-test", "restore_epoch": 1, "data": data})
	}))
	defer ts.Close()
	cl, err := New(ClientConfig{URL: ts.URL, MasterID: "client-test", Token: "test-token-long-enough", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = cl.ReportDiagnostics(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	out, err := cl.AgentDiagnostics(t.Context(), "hawk")
	if err != nil || out.Report.AgentID != "hawk" || posts != 1 {
		t.Fatal(out, err, posts)
	}
	_, err = cl.AgentDiagnostics(t.Context(), "robin")
	if !errors.Is(err, c.ErrConflict) {
		t.Fatal("misrouted report accepted", err)
	}
}
