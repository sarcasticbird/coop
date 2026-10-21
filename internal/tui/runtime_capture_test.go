package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

// Opt-in fixture launcher: the ordinary native model, renderer and key handler
// run on the controlling PTY. No customer configuration or transport is loaded.
func TestRuntimeCapture(t *testing.T) {
	if os.Getenv("COOP_TUI_CAPTURE") != "1" {
		t.Skip("fixture-only interactive capture")
	}
	m := runtimeBaselineFleet()
	coord, source := teamViewFixture()
	source.self.RestoreEpoch = 1
	m.coordination, m.services.Coordination = coord.coordination, source
	m.coordination.section = "threads"
	stress := os.Getenv("COOP_TUI_CAPTURE_STRESS") == "1"
	m.coordination.threads = []c.Thread{{ID: "fixture-thread", Title: "Design check", Members: []string{"owner", "hawk"}}}
	source.threads = func(c.ThreadQuery) (c.ThreadPage, error) {
		return c.ThreadPage{Threads: m.coordination.threads}, nil
	}
	source.thread = func(id string) (c.Thread, error) {
		return c.Thread{ID: id, Title: "Design check", Members: []string{"owner", "hawk"}}, nil
	}
	source.messages = func(id string, _ int64) (c.MessagePage, error) {
		page := c.MessagePage{Messages: []c.Message{{ID: "fixture-message", ThreadID: id, AuthorID: "hawk", Sequence: 1, Body: "Synthetic fixture: waiting for dependency review."}}}
		if stress {
			for i := 2; i <= 30; i++ {
				page.Messages = append(page.Messages, c.Message{ID: fmt.Sprintf("fixture-message-%02d", i), ThreadID: id, AuthorID: "hawk", Sequence: int64(i), Body: "Synthetic private stress body."})
			}
		}
		return page, nil
	}
	source.status = func(id string) (c.MessageReceipt, error) {
		if id != "fixture-message" {
			return c.MessageReceipt{}, c.ErrNotFound
		}
		return c.MessageReceipt{MasterID: "test-master", RestoreEpoch: 1, MessageID: id, Sequence: 1, Backend: "master", Deliveries: []c.Delivery{{AgentID: "hawk", State: "acknowledged"}}}, nil
	}
	source.send = func(c.SendMessage) (c.MessageReceipt, error) {
		return c.MessageReceipt{}, errors.New("capture fixture forbids sends")
	}
	for i := range m.hosts {
		for j := range m.hosts[i].entries {
			e := &m.hosts[i].entries[j]
			for _, p := range source.rows {
				if p.AgentID == e.Target.ID {
					e.Diagnostics = &c.DiagnosticBinding{MasterID: source.MasterID(), Placement: p.Placement}
				}
			}
		}
	}
	if stress {
		label := "\u754c e\u0301 " + strings.Repeat("long-label-", 5)
		for i := range source.rows {
			source.rows[i].Alias = label
		}
		for i := range m.hosts {
			for j := range m.hosts[i].entries {
				m.hosts[i].entries[j].Name = label
			}
		}
		for i := range 30 {
			e := m.hosts[0].entries[0]
			e.Target.ID, e.Diagnostics = fmt.Sprintf("stress-agent-%02d", i), nil
			m.hosts[0].entries = append(m.hosts[0].entries, e)
		}
	}
	m.selected = hostrouter.Target{}
	switch mode := os.Getenv("COOP_TUI_CAPTURE_MODE"); mode {
	case "", "destroy":
	case "setup":
		m.services.Coordination = nil
		m.workspace = 1
	case "first-use":
		m.hosts = m.hosts[:1]
		m.services.Registry = &fixtureRegistry{}
		m.workspace = 2
	default:
		t.Fatalf("unknown capture mode %q", mode)
	}
	m.width, m.height = 0, 0
	read := inventoryFunc(func(_ context.Context, h hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
		for _, s := range m.hosts {
			if s.host == h {
				if s.errorCode != "" {
					return nil, errors.New(s.errorCode)
				}
				return s.entries, nil
			}
		}
		return nil, nil
	})
	m.services.Inventory = hostrouter.Router{Local: read, Remote: read}
	// Preserve the prepared fixture while exercising normal reads and navigation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.ctx = ctx
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		t.Fatal(err)
	}
	if len(final.(fleetModel).services.Actions.(*fixtureActions).requests) != 0 {
		t.Fatal("capture dispatched an action")
	}
}
