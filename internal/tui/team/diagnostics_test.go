package team

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

type detailSource struct {
	sourceFunc
	read func(context.Context, string) (c.AgentDiagnostics, error)
}

func (s detailSource) AgentDiagnostics(ctx context.Context, agent string) (c.AgentDiagnostics, error) {
	return s.read(ctx, agent)
}

func teamDetailFixture(agent string) c.AgentDiagnostics {
	now := time.Now().UTC()
	return c.AgentDiagnostics{Report: c.WorkerDiagnostics{Placement: c.Placement{AgentID: agent, HostID: "rack-mini", InstanceID: "worker", Generation: 1}, Version: 1, RestoreEpoch: 1, StartedAt: now, ObservedAt: now, Wait: c.DiagnosticWait{Handling: "auto_retry", Reason: "native_dependency_retry"}, GitHub: c.DiagnosticService{Category: "rate_limited", Origin: "github"}}, ReceivedAt: now}
}

func TestTeamDiagnosticsDetail(t *testing.T) {
	s := detailSource{read: func(_ context.Context, agent string) (c.AgentDiagnostics, error) {
		return teamDetailFixture(agent), nil
	}}
	m := New(t.Context(), s, "fixture")
	defer m.Close()
	m.height = 60
	m.Update(resultMsg{rows: []c.Presence{{Placement: teamDetailFixture("hawk").Report.Placement, Alias: "Hawk"}}, at: time.Now()})
	_, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("no selected-agent read")
	}
	if _, duplicate := m.Update(key("r")); duplicate != nil {
		t.Fatal("overlapping detail read")
	}
	m.Update(cmd())
	for _, want := range []string{"hawk", "auto_retry", "native_dependency_retry", "rate_limited", "OpenCode", "unknown"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("missing %q: %s", want, m.View())
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 15, Height: 8})
	if len(strings.Split(m.View(), "\n")) > 8 {
		t.Fatal(m.View())
	}
	m.Update(key("esc"))
	if m.closed || m.detailOpen {
		t.Fatal("escape quit instead of returning to list")
	}
}

func TestTeamDiagnosticsCancellationAndLateResult(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	s := detailSource{read: func(ctx context.Context, agent string) (c.AgentDiagnostics, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return teamDetailFixture(agent), nil
	}}
	m := New(t.Context(), s, "fixture")
	defer m.Close()
	m.Update(resultMsg{rows: []c.Presence{{Placement: c.Placement{AgentID: "hawk"}}}, at: time.Now()})
	_, cmd := m.Update(key("enter"))
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	<-started
	m.Update(key("esc"))
	<-cancelled
	m.Update(<-done)
	if m.detailOpen || m.detail != nil {
		t.Fatal("late detail escaped cancellation")
	}
	legacy := New(t.Context(), sourceFunc(func(context.Context) ([]c.Presence, error) { return nil, nil }), "fixture")
	defer legacy.Close()
	legacy.Update(resultMsg{rows: []c.Presence{{Placement: c.Placement{AgentID: "hawk"}}}, at: time.Now()})
	_, cmd = legacy.Update(key("enter"))
	if cmd != nil || !strings.Contains(legacy.View(), "unsupported") {
		t.Fatal("legacy Source.Team lost compatibility", legacy.View())
	}
}
