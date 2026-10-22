package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/tui/tuifixture"
)

func TestCreationPreviewRequiresExplicitReveal(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m, _ := coordinationFixture()
			m.width, m.height = size.Width, size.Height
			m, _ = coordinationKey(m, "n")
			values := []string{"synthetic-private-title-sentinel", "coop", "synthetic-private-member"}
			for i, value := range values {
				if i > 0 {
					m, _ = coordinationKey(m, "tab")
				}
				m, _ = coordinationKey(m, value)
			}
			m, cmd := coordinationKey(m, "ctrl+s")
			if cmd != nil || !m.coordination.form.preview || m.coordination.form.reveal {
				t.Fatal("preview submitted or changed reveal state")
			}
			for _, reveal := range []bool{false, true, false} {
				if m.coordination.form.reveal != reveal {
					m, _ = coordinationKey(m, "ctrl+v")
				}
				lines, _, _ := m.content()
				all := strings.Join(lines, "")
				for _, value := range []string{values[0], values[2]} {
					if strings.Contains(all, value) != reveal {
						t.Fatalf("explicit reveal=%t not respected by rendered preview", reveal)
					}
				}
				assertComposeBounds(t, m)
			}
		})
	}
}

func TestConfirmationKeepsCapturedContextPinned(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, operation := range []string{"enter", "d"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.Width, size.Height, operation), func(t *testing.T) {
				m := runtimeBaselineFleet()
				coord, source := coordinationFixture()
				master := "master-" + strings.Repeat("exact-", 14) + "suffix"
				principal := "principal-" + strings.Repeat("exact-", 14) + "suffix"
				source.master = master
				coord.coordination.self.Principal.ID = principal
				m.services.Coordination, m.coordination = source, coord.coordination
				m.width, m.height = size.Width, size.Height
				m, _ = fleetKey(m, "enter")
				key := operation
				if operation == "enter" {
					key = "c"
				}
				m, _ = fleetKey(m, key)
				if m.pending == nil {
					t.Fatal("confirmation missing")
				}
				lines, _, _ := m.content()
				for _, value := range []string{master, principal, identity(m.pending.entry.Target)} {
					if !strings.Contains(strings.Join(lines, ""), value) {
						t.Fatal("confirmation lost full captured identity")
					}
				}
				header := strings.Join(strings.Split(m.View(), "\n")[:4], "\n")
				if !strings.Contains(header, "Master:") || !strings.Contains(header, "Principal:") || !strings.Contains(header, "Target:") {
					t.Fatal("confirmation context is not pinned")
				}
				source.master = "different-master"
				m.coordination.self.Principal.ID = "different-principal"
				for range 20 {
					m, _ = fleetKey(m, "pgdown")
					view := m.View()
					if !strings.HasPrefix(view, header) || maxLineWidth(view) > size.Width || len(strings.Split(view, "\n")) != size.Height {
						t.Fatal("paging changed captured context or geometry")
					}
				}
				lines, _, _ = m.content()
				all := strings.Join(lines, "")
				if !strings.Contains(all, master) || !strings.Contains(all, principal) || strings.Contains(all, "different-principal") || strings.Contains(all, "different-master") {
					t.Fatal("confirmation replaced captured context")
				}
				m, _ = fleetKey(m, "esc")
				if m.pending != nil || !m.detail {
					t.Fatal("cancel did not return to Details")
				}
			})
		}
	}
}
