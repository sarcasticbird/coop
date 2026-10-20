package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestDiagnosticsSafeFieldsAndFreshness(t *testing.T) {
	cfg, m, _, _, _, _, _ := commsFixture(t)
	cfg.CodeOwners, cfg.Repositories = []string{"example"}, nil
	m.connection.Epoch = 1
	now := time.Now().UTC()
	b := newDiagnosticBuilder(now)
	status := Status{State: "working", ServerHealthy: false, SessionID: "ses_owned", Native: &NativeProgress{Reason: "native_dependency_wait", Current: &CurrentWork{Stage: "review", Phase: "selected", TaskURL: "https://github.com/example/repo/issues/1"}, Blocked: []NativeBlocker{{Dependencies: []string{"https://github.com/example/repo/issues/2"}}}}}
	status.Current = status.Native.Current
	status.WaitReason = status.Native.Reason
	status.Current.HeadSHA = strings.Repeat("a", 40)
	d := b.snapshot(cfg, m.connection.Epoch, status, errors.New("secret /Users/private prompt body"), c.DiagnosticService{}, c.DiagnosticService{}, now)
	if d.Wait.Handling != "waiting" || len(d.Wait.Dependencies) != 1 || d.OpenCode.Available {
		t.Fatalf("bad evidence %+v", d)
	}
	if err := c.ValidateDiagnostics(d); err != nil {
		t.Fatal(err, d)
	}
	if d.Work.HeadSHA != status.Current.HeadSHA {
		t.Fatal("Git head was discarded", d.Work)
	}
	raw, _ := json.Marshal(d)
	for _, secret := range []string{"secret", "/Users/private", "prompt body", "server_healthy"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal(string(raw))
		}
	}
	for range 40 {
		now = now.Add(time.Second)
		status.Current.Phase = "active"
		b.snapshot(cfg, 1, status, &GitHubError{Category: "transport_failed"}, c.DiagnosticService{}, c.DiagnosticService{}, now)
		status.Current.Phase = "selected"
		b.snapshot(cfg, 1, status, &OpenCodeError{Category: "transport_failed"}, c.DiagnosticService{}, c.DiagnosticService{}, now)
	}
	d = b.snapshot(cfg, 1, status, nil, c.DiagnosticService{}, c.DiagnosticService{}, now)
	if len(d.Transitions) != 32 {
		t.Fatal("timeline not bounded", len(d.Transitions))
	}
	restarted := newDiagnosticBuilder(now).snapshot(cfg, 1, status, nil, c.DiagnosticService{}, c.DiagnosticService{}, now)
	if len(restarted.Transitions) != 1 || !restarted.StartedAt.Equal(now) {
		t.Fatal("restart claims old history", restarted)
	}
	origin, category := diagnosticError(&GitHubError{Category: "transport_failed"})
	if origin != "github" || category != "transport_failed" {
		t.Fatal(origin, category)
	}
	origin, category = diagnosticError(&OpenCodeError{Category: "transport_failed"})
	if origin != "opencode" || category != "transport_failed" {
		t.Fatal(origin, category)
	}
	_, category = diagnosticError(&GitHubError{Category: "secret-token"})
	if category != "operation_failed" {
		t.Fatal("untrusted category exposed", category)
	}
}

func TestDiagnosticsHandling(t *testing.T) {
	for reason, want := range map[string]string{"native_dependency_wait": "waiting", "native_dependency_retry": "auto_retry", "native_recovery_required": "action_required", "native_admission_stalled": "action_required", "native_catalog_unavailable": "auto_retry", "unrecognized_reason": "unknown", "local_pause": "waiting"} {
		if got := diagnosticHandling(Status{WaitReason: reason}); got != want {
			t.Fatalf("%s: %s want %s", reason, got, want)
		}
	}
}

func TestDiagnosticsMissingModelKeepsOtherSections(t *testing.T) {
	cfg, _, _, _, _, _, _ := commsFixture(t)
	now := time.Now().UTC()
	for _, model := range []*ModelIdentity{{}, {ProviderID: "openai", ModelID: "/Users/private"}} {
		s := Status{State: "working", Current: &CurrentWork{Stage: "review", Phase: "active", ExecutedModel: model}}
		d := newDiagnosticBuilder(now).snapshot(cfg, 1, s, nil, c.DiagnosticService{Available: true, ObservedAt: now}, c.DiagnosticService{Available: true, ObservedAt: now}, now)
		if err := c.ValidateDiagnostics(d); err != nil || d.Work.ExecutedModel != nil || !d.GitHub.Available || d.Work.Stage != "review" {
			t.Fatalf("optional model hid other sections: %+v %v", d, err)
		}
	}
}

func TestDiagnosticsPausePrecedesRetry(t *testing.T) {
	cfg, _, _, _, _, _, _ := commsFixture(t)
	now := time.Now().UTC()
	for _, reason := range []string{"native_catalog_unavailable", "native_dependency_retry", "native_recovery_required"} {
		for _, status := range []Status{{State: "paused", WaitReason: reason, NextRetry: now.Add(time.Minute)}, {Paused: true, State: "working", WaitReason: reason}} {
			d := newDiagnosticBuilder(now).snapshot(cfg, 1, status, nil, c.DiagnosticService{}, c.DiagnosticService{}, now)
			if d.Wait.Handling != "waiting" || d.Wait.Reason != "local_pause" || !d.Wait.NextRetry.IsZero() {
				t.Fatal("paused worker advertised automatic recovery", d.Wait)
			}
		}
	}
}

type diagnosticSenderFunc func(context.Context, c.WorkerDiagnostics) error

func (f diagnosticSenderFunc) ReportDiagnostics(ctx context.Context, d c.WorkerDiagnostics) error {
	return f(ctx, d)
}

func TestDiagnosticsDoesNotBlockWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, done := make(chan struct{}), make(chan struct{})
	sender := diagnosticSenderFunc(func(ctx context.Context, _ c.WorkerDiagnostics) error {
		close(started)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Error("unbounded report")
		}
		<-ctx.Done()
		close(done)
		return ctx.Err()
	})
	r := diagnosticReporter{}
	now := time.Now()
	r.tick(ctx, sender, c.WorkerDiagnostics{}, now)
	<-started
	for range 20 {
		r.tick(ctx, sender, c.WorkerDiagnostics{}, now.Add(time.Minute))
	}
	cancel()
	<-done
}

func TestDiagnosticsOlderMaster(t *testing.T) {
	called := make(chan struct{}, 2)
	sender := diagnosticSenderFunc(func(context.Context, c.WorkerDiagnostics) error { called <- struct{}{}; return c.ErrUnsupported })
	r := diagnosticReporter{}
	now := time.Now()
	r.tick(t.Context(), sender, c.WorkerDiagnostics{}, now)
	<-called
	<-r.done // Observe completion deterministically, then let tick consume it.
	r.done <- c.ErrUnsupported
	r.tick(t.Context(), sender, c.WorkerDiagnostics{}, now.Add(time.Minute))
	r.tick(t.Context(), sender, c.WorkerDiagnostics{}, now.Add(2*time.Minute))
	select {
	case <-called:
		t.Fatal("old master reporting not disabled")
	default:
	}
}
