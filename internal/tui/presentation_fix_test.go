package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/tui/tuifixture"
)

func TestPresentationCreationPreviewAndGuard(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			m, _ := coordinationFixture()
			m.width, m.height = size.Width, size.Height
			m, _ = coordinationKey(m, "n")
			for i, value := range []string{"Synthetic private title", "coop", "hawk"} {
				if i > 0 {
					m, _ = coordinationKey(m, "tab")
				}
				m, _ = coordinationKey(m, value)
			}
			m, cmd := coordinationKey(m, "ctrl+s")
			if cmd != nil {
				t.Fatal("preview dispatched")
			}
			request := m.coordination.form.requestID
			for _, revealed := range []bool{false, true, false} {
				if m.coordination.form.reveal != revealed {
					m, _ = coordinationKey(m, "ctrl+v")
				}
				state, action := "masked", "reveal"
				if revealed {
					state, action = "revealed", "mask"
				}
				lines := strings.Join(m.composeLines(), "\n")
				view := m.View()
				if !strings.Contains(lines, "Content: "+state) || !strings.Contains(view, "^V "+action) {
					t.Fatalf("privacy labels disagree: %s", view)
				}
				for _, bad := range []string{"Captured members:", "Engagement: \n", "parent: none", "Enter inserts", "> Stable"} {
					if strings.Contains(lines, bad) {
						t.Fatalf("creation preview retains irrelevant context %q", bad)
					}
				}
				if strings.Contains(lines, "Synthetic private title") != revealed {
					t.Fatal("preview privacy changed")
				}
				if revealed && (!strings.Contains(lines, "Members (owner included): hawk,owner") || !strings.Contains(lines, "Engagement: coop")) {
					t.Fatal("creation review lost captured input")
				}
				footer := strings.Split(view, "\n")[size.Height-1]
				if !strings.Contains(footer, "Esc edit") || !strings.Contains(footer, "^S create") || strings.Contains(footer, "...") {
					t.Fatal("preview controls clipped", footer)
				}
				if m.coordination.form.requestID != request {
					t.Fatal("preview retargeted request")
				}
				assertComposeBounds(t, m)
			}
			m, _ = coordinationKey(m, "esc")
			if m.coordination.leaving != "" || m.coordination.form.preview {
				t.Fatal("Esc preview did not edit")
			}
			m, _ = coordinationKey(m, "esc")
			if m.coordination.leaving == "" {
				t.Fatal("nonempty edit lost draft guard")
			}
			view := strings.Split(m.View(), "\n")
			if strings.Contains(m.View(), "proce\nss") || strings.Contains(m.View(), "Ret\nurn") {
				t.Fatal("guard breaks ordinary words", m.View())
			}
			if strings.Contains(view[size.Height-2], "Review captured") || !strings.Contains(view[size.Height-2], "memory") {
				t.Fatal("guard retained preview feedback", view)
			}
		})
	}
}

func TestPresentationConnectAndDetailsHelp(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		m := runtimeBaselineFleet()
		coord, source := coordinationFixture()
		m.services.Coordination, m.coordination = source, coord.coordination
		m.width, m.height = size.Width, size.Height
		m, _ = fleetKey(m, "tab")
		m, _ = fleetKey(m, "?")
		lines, _, _ := m.content()
		all := strings.Join(lines, " ")
		if !strings.Contains(all, "Master: test-master") || !strings.Contains(all, "Principal: owner") {
			t.Fatal("Details Help lost identity", all)
		}
		if !strings.Contains(all, "c Connect") || strings.Contains(all, "Enter Connect") {
			t.Fatal("Details Help advertises the wrong Connect key", all)
		}
		m, _ = fleetKey(m, "esc")
		m, _ = fleetKey(m, "c")
		lines, _, _ = m.content()
		all = strings.Join(lines, " ")
		if strings.Contains(all, "y confirms") || !strings.Contains(all, "Enter continues") {
			t.Fatal("Connect has mixed consent vocabulary", all)
		}
		for range 20 {
			m, _ = fleetKey(m, "pgdown")
		}
		footer := strings.Split(m.View(), "\n")[size.Height-1]
		if strings.Contains(footer, "...") || !strings.Contains(footer, "Enter") || !strings.Contains(footer, "Esc") {
			t.Fatal("Connect controls clipped", footer)
		}
	}
}
