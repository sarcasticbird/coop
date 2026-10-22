package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/tui/tuifixture"
)

func TestPageParityExactContextSurvivesHeaderClipping(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		for _, page := range []string{"team-detail", "unbound-details"} {
			for _, suffix := range []string{"first", "second"} {
				t.Run(fmt.Sprintf("%dx%d/%s/%s", size.Width, size.Height, page, suffix), func(t *testing.T) {
					master := "master-" + strings.Repeat("shared-prefix-", 8) + suffix
					principal := "principal-" + strings.Repeat("shared-prefix-", 8) + suffix
					m, source := teamViewFixture()
					source.master = master
					m.coordination.self.Principal.ID = principal
					m.width, m.height = size.Width, size.Height
					if page == "team-detail" {
						placement := m.coordination.team[0].Placement
						m.coordination.teamDetail = &placement
					} else {
						fleet := runtimeBaselineFleet()
						fleet.width, fleet.height = size.Width, size.Height
						fleet.services.Coordination, fleet.coordination = source, m.coordination
						m, _ = fleetKey(fleet, "enter")
						if m.detailEntry == nil || m.detailEntry.Diagnostics != nil {
							t.Fatal("regression requires unbound Details")
						}
					}
					lines, _, _ := m.content()
					for _, id := range []string{master, principal} {
						if !strings.Contains(strings.Join(lines[conversationHeader:], ""), id) {
							t.Fatalf("full exact context lost behind clipped %s header", page)
						}
					}
					seenMaster, seenPrincipal := false, false
					for offset := 0; offset < len(lines); offset++ {
						m.scroll = offset
						view := m.View()
						if maxLineWidth(view) > size.Width || len(strings.Split(view, "\n")) != size.Height {
							t.Fatal("context exceeds viewport")
						}
						body := strings.Join(strings.Split(view, "\n")[conversationHeader:size.Height-2], "")
						if page == "unbound-details" && m.splitFleet() {
							body = strings.Join(actualSplitRightInterior(t, m)[2:], "")
						}
						seenMaster = seenMaster || strings.Contains(body, master)
						seenPrincipal = seenPrincipal || strings.Contains(body, principal)
					}
					if !seenMaster || !seenPrincipal {
						t.Fatal("exact identities are not reachable by scrolling")
					}
				})
			}
		}
	}
}

func TestPageParityDetailsPresenceAndContext(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		m, _ := boundFleetFixture()
		m.width, m.height = size.Width, size.Height
		m, cmd := fleetKey(m, "enter")
		m = runFleetDiagnostic(t, m, cmd)
		if m.diagnostics.presence == nil || !strings.Contains(m.View(), "Activity: waiting") || !strings.Contains(m.View(), "Presence:") {
			t.Fatal("bound presence or work-first summary absent", m.View())
		}
		for _, failure := range []string{"diagnostics access denied; work unknown", "placement changed"} {
			m.diagnostics.failure = failure
			if !strings.Contains(m.fleetPresenceLine(), "unknown") {
				t.Fatal("failed observation retained current presence")
			}
		}
		m.diagnostics.failure = ""
		m.diagnostics.presence.LastSeen = time.Now().Add(-time.Hour)
		if !strings.Contains(m.fleetPresenceLine(), "STALE") {
			t.Fatal("old heartbeat presented as current")
		}
		m.scroll = 100
		view := m.View()
		if len(strings.Split(view, "\n")) != size.Height {
			t.Fatal("footer is not fixed at terminal bottom", view)
		}
		pinned := strings.HasPrefix(view, "Fleet > Details\nMaster: test-master\nPrincipal:")
		if m.splitFleet() {
			pane := actualSplitRightInterior(t, m)
			pinned = strings.Contains(strings.Split(view, "\n")[0], "DETAILS [focus]") && pane[0] == "Master: test-master" && strings.HasPrefix(pane[1], "Principal:")
		}
		if !pinned || !strings.Contains(strings.Split(view, "\n")[len(strings.Split(view, "\n"))-1], "Esc Fleet") {
			t.Fatal("scroll lost pinned identity or return", view)
		}
	}
}

func TestPageParityReaderOffscreenAndMasking(t *testing.T) {
	for _, size := range tuifixture.Sizes() {
		m, _ := coordinationFixture()
		m.width, m.height = size.Width, size.Height
		m.coordination.thread = &c.Thread{ID: "exact-thread", Title: "Readable title", Members: []string{"owner", "hawk"}}
		m.coordination.messages = makeSyntheticMessages(1, 100)
		m.coordination.cursor = 0
		m.scroll = 100
		view := m.View()
		if !strings.Contains(view, "Reply target offscreen: 1") || !strings.Contains(view, "Esc Threads") || strings.Contains(view, m.coordination.messages[0].Body) {
			t.Fatal("paging lost stable reply context or masking", view)
		}
		if m.coordination.cursor != 0 {
			t.Fatal("render changed reply parent")
		}
	}
}

func TestPageParityComposerFieldsBeforeEvidence(t *testing.T) {
	m, _ := coordinationFixture()
	m, _ = coordinationKey(m, "n")
	all := strings.Join(m.composeLines(), "\n")
	if strings.Index(all, "> "+m.coordination.form.labels[0]) > strings.Index(all, "Captured master:") {
		t.Fatal("technical metadata buries fields")
	}
	for _, size := range tuifixture.Sizes() {
		m.coordination.form.kind = "send"
		m.width, m.height = size.Width, size.Height
		_, footer, _ := m.content()
		if !strings.Contains(footer, "^S preview") || !strings.HasPrefix(footer, "Esc") {
			t.Fatal("editing footer is not contextual", footer)
		}
		m.coordination.form.preview = true
		_, footer, _ = m.content()
		if !strings.Contains(footer, "^S submit") {
			t.Fatal("preview not distinguished from editing", footer)
		}
		m.coordination.form.preview = false
	}
}
