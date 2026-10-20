package master

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func diagnosticFixture() c.WorkerDiagnostics {
	return c.WorkerDiagnostics{Version: 1, Placement: c.Placement{AgentID: "agent-one", HostID: "host", InstanceID: "instance", Generation: 1}, RestoreEpoch: 1,
		ObservedAt: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), StartedAt: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC),
		Wait:   c.DiagnosticWait{Handling: "waiting", Reason: "native_dependency_wait"},
		GitHub: c.DiagnosticService{Available: true}, OpenCode: c.DiagnosticService{Category: "unavailable"}}
}

func TestDiagnosticsPlacementAndEpoch(t *testing.T) {
	s, owner, bot, _ := storeFixture(t)
	ctx := context.Background()
	in := diagnosticFixture()
	if err := s.ReportDiagnostics(ctx, bot, in); err != nil {
		t.Fatal(err)
	}
	for _, alter := range []func(*c.WorkerDiagnostics){
		func(d *c.WorkerDiagnostics) { d.AgentID = "agent-two" },
		func(d *c.WorkerDiagnostics) { d.InstanceID = "retired" },
		func(d *c.WorkerDiagnostics) { d.HostID = "other" },
		func(d *c.WorkerDiagnostics) { d.Generation = 2 },
		func(d *c.WorkerDiagnostics) { d.RestoreEpoch = 2 },
	} {
		d := in
		alter(&d)
		if err := s.ReportDiagnostics(ctx, bot, d); err == nil {
			t.Fatalf("accepted wrong placement/epoch: %+v", d)
		}
	}
	if err := s.ReportDiagnostics(ctx, owner, in); !errors.Is(err, c.ErrForbidden) {
		t.Fatal(err)
	}
	// A placement move must make previously recorded diagnostics unavailable.
	newPlacement := in.Placement
	newPlacement.Generation++
	newPlacement.InstanceID = "new-instance"
	if _, err := s.db.Exec("UPDATE placements SET body=? WHERE agent_id=?", encode(newPlacement), bot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AgentDiagnostics(ctx, owner, bot.ID); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("old placement visible", err)
	}
	if err := s.ReportDiagnostics(ctx, bot, in); err == nil {
		t.Fatal("late report from retired placement accepted")
	}
	if _, err := s.db.Exec("UPDATE placements SET body=? WHERE agent_id=?", encode(in.Placement), bot.ID); err != nil {
		t.Fatal(err)
	}
	s.epoch++
	if _, err := s.AgentDiagnostics(ctx, owner, bot.ID); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("old epoch visible", err)
	}
}

func TestDiagnosticsEngagementVisibility(t *testing.T) {
	s, owner, bot, _ := storeFixture(t)
	ctx := context.Background()
	other := c.Principal{ID: "outside", Alias: "outside", Kind: "service", Engagements: []string{"customer"}, Capabilities: []string{"read"}}
	if err := s.Configure(ctx, []c.Principal{other}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportDiagnostics(ctx, bot, diagnosticFixture()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AgentDiagnostics(ctx, owner, bot.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{bot.ID, "missing"} {
		if _, err := s.AgentDiagnostics(ctx, other, id); !errors.Is(err, c.ErrNotFound) {
			t.Fatalf("visibility leak for %s: %v", id, err)
		}
	}
}

func TestDiagnosticsLimits(t *testing.T) {
	s, _, bot, _ := storeFixture(t)
	for _, alter := range []func(*c.WorkerDiagnostics){
		func(d *c.WorkerDiagnostics) { d.Version = 2 },
		func(d *c.WorkerDiagnostics) { d.Transitions = make([]c.DiagnosticTransition, 33) },
		func(d *c.WorkerDiagnostics) { d.Wait.Reason = "raw secret /Users/private" },
		func(d *c.WorkerDiagnostics) { d.Work.TaskURL = "https://secret@github.com/owner/repo/issues/1" },
		func(d *c.WorkerDiagnostics) { d.Work.TaskURL = "https://github.com/owner/repo/issues/1?token=secret" },
		func(d *c.WorkerDiagnostics) { d.Work.HeadSHA = strings.Repeat("a", c.MaxDiagnosticsBytes) },
		func(d *c.WorkerDiagnostics) { d.GitHub.Category = "transport_failed\nsecret" },
	} {
		d := diagnosticFixture()
		alter(&d)
		if err := s.ReportDiagnostics(context.Background(), bot, d); err == nil {
			t.Fatal("accepted invalid diagnostic report")
		}
	}
}

func TestDiagnosticsSafeFieldsAndFreshness(t *testing.T) {
	s, owner, bot, _ := storeFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 19, 2, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	in := diagnosticFixture()
	in.GitHub.ObservedAt = in.ObservedAt
	if err := s.ReportDiagnostics(ctx, bot, in); err != nil {
		t.Fatal(err)
	}
	out, err := s.AgentDiagnostics(ctx, owner, bot.ID)
	if err != nil || out.Stale || !out.ReceivedAt.Equal(now) || !out.Report.ObservedAt.Equal(in.ObservedAt) || !out.Report.GitHub.Available || out.Report.OpenCode.Available {
		t.Fatalf("bad independent sections: %+v %v", out, err)
	}
	now = now.Add(c.PresenceStale)
	out, err = s.AgentDiagnostics(ctx, owner, bot.ID)
	if err != nil || !out.Stale {
		t.Fatal("guest clock changed receipt freshness", out, err)
	}
	var count int
	if err = s.ReportDiagnostics(ctx, bot, in); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM agent_diagnostics").Scan(&count); err != nil || count != 1 {
		t.Fatalf("unbounded storage: %d %v", count, err)
	}
}

func TestDiagnosticsHTTPBoundaries(t *testing.T) {
	s, owner, bot, _ := storeFixture(t)
	h := NewHandler(&Service{Store: s}, policyFixture(owner, bot))
	raw, err := json.Marshal(diagnosticFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, token, body string
		code                      int
	}{
		{"POST", "/api/v1/diagnostics", "agent-test-token", string(raw), 200},
		{"GET", "/api/v1/team/agent-one/diagnostics", "owner-test-token", "", 200},
		{"POST", "/api/v1/diagnostics", "owner-test-token", string(raw), 403},
		{"POST", "/api/v1/diagnostics", "agent-test-token", strings.Repeat(" ", c.MaxDiagnosticsBytes+1), 413},
		{"POST", "/api/v1/diagnostics", "agent-test-token", strings.TrimSuffix(string(raw), "}") + `,"prompt":"secret"}`, 400},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("X-Coop-Expected-Master-ID", "test-master")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}
