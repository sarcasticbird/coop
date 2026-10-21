package tui

import (
	"context"
	"errors"
	"os"
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
	m.coordination.threads = []c.Thread{{ID: "fixture-thread", Title: "Design check", Members: []string{"owner", "hawk"}}}
	source.threads = func(c.ThreadQuery) (c.ThreadPage, error) {
		return c.ThreadPage{Threads: m.coordination.threads}, nil
	}
	source.thread = func(id string) (c.Thread, error) {
		return c.Thread{ID: id, Title: "Design check", Members: []string{"owner", "hawk"}}, nil
	}
	source.messages = func(id string, _ int64) (c.MessagePage, error) {
		return c.MessagePage{Messages: []c.Message{{ID: "fixture-message", ThreadID: id, AuthorID: "hawk", Sequence: 1, Body: "Synthetic fixture: waiting for dependency review."}}}, nil
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
	m.selected = hostrouter.Target{}
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
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		t.Fatal(err)
	}
}
