package team

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

type sourceFunc func(context.Context) ([]c.Presence, error)

func (f sourceFunc) Team(ctx context.Context) ([]c.Presence, error) { return f(ctx) }

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestReturnedStatesAndMissingDetails(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, fixture := range []struct{ state, reason string }{
		{"busy", "native_work stage=implement phase=running task=https://github.com/example/repo/issues/1"},
		{"idle", ""},
		{"recovering", "native_dependency_wait blocker=waiting task=task1 depends_on=task2"},
		{"paused", "local_pause"},
		{"stale", "native_recovery_required"},
		{"", ""},
	} {
		t.Run(fixture.state, func(t *testing.T) {
			m := New(context.Background(), nil, "master-fixture")
			defer m.Close()
			m.height = 50
			m.Update(resultMsg{at: now, rows: []c.Presence{{Placement: c.Placement{AgentID: "hawk", HostID: "rack-mini", InstanceID: "worker1", Generation: 2}, Alias: "Hawk", State: fixture.state, Reason: fixture.reason, LastSeen: now.Add(-20 * time.Second)}}})
			view := m.View()
			for _, want := range []string{"master-fixture", "Hawk [hawk]", "rack-mini", "worker1", "20s ago", "Availability (returned): " + clean(fixture.state)} {
				if !strings.Contains(view, want) {
					t.Fatalf("missing %q: %s", want, view)
				}
			}
			if fixture.reason == "" && !strings.Contains(view, "Task(s): unknown") {
				t.Fatal(view)
			}
			if strings.Contains(fixture.reason, "depends_on=") {
				for _, want := range []string{"Blocker(s): waiting", "Prerequisites: task2", "Task(s): task1"} {
					if !strings.Contains(view, want) {
						t.Fatalf("missing %q: %s", want, view)
					}
				}
			}
		})
	}
	m := New(context.Background(), nil, "fixture")
	defer m.Close()
	m.height = 40
	m.Update(resultMsg{at: now, rows: []c.Presence{{Reason: "task=a task=b blocker=x blocker=y"}}})
	for _, want := range []string{"Last seen: unknown", "Host: unknown", "Task(s): a, b", "Blocker(s): x, y", "Stage: unknown"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("missing %s: %s", want, m.View())
		}
	}
}

func TestFailureStaleReconnect(t *testing.T) {
	m := New(context.Background(), nil, "fixture")
	defer m.Close()
	now := time.Now()
	m.Update(resultMsg{at: now, err: errors.New("secret-token private-message https://credentials@host")})
	if !strings.Contains(m.View(), "OFFLINE; no snapshot") || strings.Contains(m.View(), "secret-token") {
		t.Fatal(m.View())
	}
	rows := []c.Presence{{Alias: "Hawk", State: "idle"}}
	m.Update(resultMsg{at: now, rows: rows})
	m.Update(resultMsg{at: now.Add(time.Minute), err: c.ErrUnavailable})
	if !strings.Contains(m.View(), "STALE") || !strings.Contains(m.View(), "Hawk") || !strings.Contains(m.View(), "1m0s ago") {
		t.Fatal(m.View())
	}
	m.Update(resultMsg{at: now.Add(2 * time.Minute), rows: rows})
	if strings.Contains(m.View(), "STALE") || strings.Contains(m.View(), "unavailable") {
		t.Fatal(m.View())
	}
	m.Update(resultMsg{at: now.Add(3 * time.Minute), rows: []c.Presence{}})
	if !strings.Contains(m.View(), "No visible agents") {
		t.Fatal(m.View())
	}
}

func TestSlowReadBoundedRefreshResizeQuit(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	source := sourceFunc(func(ctx context.Context) ([]c.Presence, error) {
		close(started)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > requestTimeout {
			t.Error("missing bounded deadline")
		}
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	m := New(context.Background(), source, "fixture")
	done := make(chan tea.Msg, 1)
	cmd := m.refresh()
	go func() { done <- cmd() }()
	<-started
	for range 20 {
		if _, cmd := m.Update(key("r")); cmd != nil {
			t.Fatal("overlapping manual read")
		}
		m.Update(tickMsg(time.Now()))
		m.Update(tea.WindowSizeMsg{Width: 12, Height: 5})
		m.Update(key("j"))
	}
	if !m.refreshing {
		t.Fatal("lost in-flight gate")
	}
	_, quit := m.Update(key("q"))
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatal("not quit")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("request not cancelled")
	}
	m.Update(<-done)
	if cmd := m.refresh(); cmd != nil {
		t.Fatal("refresh after close")
	}
}

func TestRefreshCadence(t *testing.T) {
	calls := 0
	m := New(context.Background(), sourceFunc(func(context.Context) ([]c.Presence, error) { calls++; return nil, nil }), "fixture")
	defer m.Close()
	result := m.refresh()().(resultMsg)
	m.Update(result)
	for i := 1; i < 5; i++ {
		m.Update(tickMsg(result.at.Add(time.Duration(i) * time.Second)))
		if m.refreshing {
			t.Fatal("automatic refresh too early")
		}
	}
	m.Update(tickMsg(result.at.Add(refreshInterval)))
	if !m.refreshing || calls != 1 {
		t.Fatal("automatic refresh not scheduled")
	}
	if m.refresh() != nil {
		t.Fatal("overlap")
	}
	m.Update(resultMsg{at: result.at.Add(refreshInterval)})
	_, manual := m.Update(key("r"))
	if manual == nil {
		t.Fatal("manual refresh not scheduled")
	}
	m.Update(manual())
	if calls != 2 || m.refreshing {
		t.Fatal("manual refresh did not complete")
	}
}

func TestResizeScrollHelpAndSafeTerminalText(t *testing.T) {
	m := New(context.Background(), nil, "fixture")
	defer m.Close()
	m.Update(resultMsg{at: time.Now(), rows: []c.Presence{{Alias: "Hawk\x1b]52;c;payload\a\n", Reason: strings.Repeat("long", 100)}}})
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 12, Height: 6}, {Width: 1, Height: 1}, {Width: 100, Height: 40}} {
		m.Update(size)
		for _, k := range []string{"?", "j", "G", "g", "k", "?"} {
			m.Update(key(k))
			view := m.View()
			if strings.ContainsAny(view, "\x1b\a") {
				t.Fatal("terminal control leaked")
			}
			lines := strings.Split(view, "\n")
			if len(lines) > size.Height {
				t.Fatal("height overflow")
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > size.Width {
					t.Fatal("width overflow")
				}
			}
		}
	}
	m.Update(key("?"))
	m.Update(key("g"))
	if !strings.Contains(m.View(), "READ ONLY") {
		t.Fatal(m.View())
	}
}

func TestAgeUnknownAndFuture(t *testing.T) {
	now := time.Now()
	if age(now, time.Time{}) != "unknown" || age(now, now.Add(time.Second)) != "unknown (clock ahead)" {
		t.Fatal("fabricated timestamp age")
	}
}
