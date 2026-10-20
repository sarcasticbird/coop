package team

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/tui/tuifixture"
)

func TestRuntimeBaselineDiagnosticInputs(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for i, want := range []string{"fresh receipt", "STALE receipt", "no visible diagnostic report"} {
			t.Run(fmt.Sprintf("%dx%d/%d", size.Width, size.Height, i), func(t *testing.T) {
				f := tuifixture.New(time.Now())
				reads := 0
				s := detailSource{read: func(_ context.Context, agent string) (c.AgentDiagnostics, error) {
					reads++
					if d, ok := f.Diagnostics[agent]; ok {
						return d, nil
					}
					return c.AgentDiagnostics{}, c.ErrNotFound
				}}
				m := New(t.Context(), s, "fixture-master")
				defer m.Close()
				m.Update(tea.WindowSizeMsg{Width: size.Width, Height: size.Height})
				m.Update(resultMsg{at: f.At, rows: f.Presence})
				m.selected = i
				_, cmd := m.Update(key("enter"))
				if cmd == nil {
					t.Fatal("explicit diagnostic navigation did not schedule a read")
				}
				m.Update(cmd())
				lines := strings.Join(m.detailLines(), "\n")
				if !strings.Contains(lines, want) {
					t.Fatalf("missing %q: %s", want, lines)
				}
				if i < 2 && (!strings.Contains(lines, "Handling: waiting") || !strings.Contains(lines, "phase: unknown")) {
					t.Fatal("work observation was inferred from VM lifecycle or presence", lines)
				}
				view := m.View()
				if len(strings.Split(view, "\n")) > size.Height {
					t.Fatal("Team viewport exceeds terminal height")
				}
				for _, line := range strings.Split(view, "\n") {
					if ansi.StringWidth(line) > size.Width {
						t.Fatal("Team viewport exceeds terminal width")
					}
				}
				if reads != 1 {
					t.Fatalf("unexpected read count: %d", reads)
				}
			})
		}
	}
}
