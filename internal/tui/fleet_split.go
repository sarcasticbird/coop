package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	textansi "github.com/charmbracelet/x/ansi"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/tui/team"
)

func (m fleetModel) splitFleet() bool {
	return m.workspace == 0 && m.width >= 100 && m.height >= 10 && !m.filtering && !m.help && !m.runbook && m.form == nil && m.pending == nil && m.operations == nil && m.coordination.leaving == ""
}

func (m fleetModel) splitWidths() (int, int) {
	usable := m.width - 5 // Two thin frames and one separating cell.
	left := usable * 2 / 5
	return left, usable - left
}

// Rendering a preview never starts a read or grants action authority. Reuse only
// the exact-placement ledger cache after its ordinary access/freshness checks.
func (m fleetModel) previewDetails() fleetModel {
	p := m
	p.detail = true
	if m.detail {
		return p
	}
	if p.detailEntry == nil {
		for _, e := range m.rows() {
			if e.Target == m.selected {
				p.detailEntry = &e
				break
			}
		}
	}
	p.diagnostics = fleetDiagnostics{}
	if p.detailEntry == nil {
		return p
	}
	e := *p.detailEntry
	current, ok := m.entry(e.Target)
	if m.detailInvalid || !ok || current.Name != e.Name || current.Project != e.Project || !sameBinding(current.Diagnostics, e.Diagnostics) {
		p.detailInvalid = true
		p.diagnostics.failure = "target unavailable or changed; select again"
		return p
	}
	if cached, ok := m.ledgerDiagnostics.entries[e.Target]; ok && sameBinding(e.Diagnostics, &cached.binding) {
		p.diagnostics = cached.fleetDiagnostics
	}
	activity, reason := m.ledgerSummary(e)
	if activity == "unknown" {
		p.diagnostics.failure = reason
	}
	return p
}

func (m fleetModel) fleetPaneLines() ([]string, int) {
	lines := []string{"BIRD / HOST          REPORTED ACTIVITY"}
	if m.filter != (hostrouter.Host{}) {
		hosts, targets := 0, 0
		for _, h := range m.hosts {
			if h.host != m.filter {
				hosts++
				targets += len(h.entries)
			}
		}
		lines = append(lines, fmt.Sprintf("%dH/%dT hidden", hosts, targets))
	}
	selected := -1
	foundHost := false
	for _, h := range m.hosts {
		if m.filter != (hostrouter.Host{}) && h.host != m.filter {
			continue
		}
		foundHost = true
		state := fleetHostHealth(h)
		if m.registryError != "" && h.host != hostrouter.Local {
			state = "registry unavailable"
		}
		lines = append(lines, textansi.Truncate("Host: "+coordClean(h.host.Name), m.width, "..."))
		lines = append(lines, strings.Split(readingWrap("  ["+state+"]", m.width), "\n")...)
		if len(h.entries) == 0 && !h.updated.IsZero() && h.errorCode == "" {
			lines = append(lines, "  (no Coops)")
		}
		for _, e := range h.entries {
			prefix := "  "
			if e.Target == m.selected {
				prefix, selected = "> ", len(lines)
			}
			activity, work := m.ledgerSummary(e)
			if _, ok := m.entry(e.Target); !ok {
				work = "[stale] last VM: " + e.State
			}
			nameWidth := min(18, max(8, m.width/2-2))
			lines = append(lines, prefix+ledgerCell(ledgerName(e), nameWidth)+"  "+ledgerCell(activity, max(1, m.width-nameWidth-4)))
			lines = append(lines, textansi.Truncate("  "+coordClean(work), m.width, "..."))
			if cached, ok := m.ledgerDiagnostics.entries[e.Target]; ok && cached.report != nil {
				lines = append(lines, textansi.Truncate("  Report "+teamAge(cached.report.Report.ObservedAt)+" / receipt "+teamAge(cached.report.ReceivedAt), m.width, "..."))
			}
		}
	}
	if len(m.hosts) == 0 {
		lines = append(lines, "Loading host registry and inventory...")
	}
	if !foundHost && m.filter != (hostrouter.Host{}) {
		lines = append(lines, "Filtered host removed; h to choose scope")
	}
	return lines, selected
}

func fleetHostHealth(h hostState) string {
	state := "loading"
	if !h.updated.IsZero() {
		age := time.Since(h.updated)
		label := age.Round(time.Second).String()
		if age >= 0 && age < 5*time.Second {
			label = "<5s"
		}
		state = "ok; snapshot " + label + " old"
	}
	if h.errorCode != "" {
		state = h.errorCode
		if !h.updated.IsZero() {
			state += "; stale snapshot " + time.Since(h.updated).Round(time.Second).String() + " old"
		}
	}
	return state
}

func (m fleetModel) focusedContent() ([]string, string, int) {
	if !m.splitFleet() {
		return m.content()
	}
	left, right := m.splitWidths()
	if !m.detail {
		m.width = left
		lines, selected := m.fleetPaneLines()
		return lines, "", selected
	}
	m.width = right
	lines, footer, selected := m.content()
	return lines[1:], footer, selected // The frame supplies the Details title.
}

func (m fleetModel) splitView() string {
	leftWidth, rightWidth := m.splitWidths()
	height := m.height - 4
	left := m
	left.width = leftWidth
	leftLines, _ := left.fleetPaneLines()
	leftScroll := m.scroll
	if m.detail {
		leftScroll = m.ledgerScroll
	}
	leftStart := min(leftScroll, max(0, len(leftLines)-height))
	leftBody := append([]string{leftLines[0]}, leftLines[1+leftStart:min(len(leftLines), 1+leftStart+height-1)]...)
	right := m.previewDetails()
	right.width = rightWidth
	rightLines := right.pageContext("Details")[1:]
	feedback := m.status
	if right.detailEntry != nil {
		var health string
		for _, h := range m.hosts {
			if h.host == right.detailEntry.Target.Host {
				health = fleetHostHealth(h)
			}
		}
		state := "unknown"
		if e, ok := m.entry(right.detailEntry.Target); ok && !right.detailInvalid {
			state = e.State
		}
		logical := right.fleetDetailsLines(state, valueOr(health, "unavailable"))
		rightLines = nil
		for _, line := range logical[1:] {
			rightLines = append(rightLines, strings.Split(readingWrap(line, rightWidth), "\n")...)
		}
		if right.detailInvalid {
			feedback = "Target unavailable or changed; select again"
		}
		diagnostic := team.DiagnosticFeedback(time.Now(), right.diagnostics.report, right.diagnostics.failure)
		if right.diagnostics.busy && right.diagnostics.report == nil && right.diagnostics.failure == "" {
			diagnostic = "Loading diagnostics..."
		}
		if diagnostic != "" {
			if feedback == "" {
				feedback = diagnostic
			} else {
				feedback += " | " + diagnostic
			}
		}
	} else {
		rightLines = append(rightLines, "", "No target selected", "Up/Down previews a bird; Enter opens Details.")
	}
	// Both context rows stay pinned; full clipped IDs remain in the body.
	for i := range min(2, len(rightLines)) {
		rightLines[i] = textansi.Truncate(rightLines[i], rightWidth, "...")
	}
	scroll := 0
	if m.detail {
		scroll = m.scroll
	}
	start := min(scroll, max(0, len(rightLines)-height))
	rightBody := append(append([]string(nil), rightLines[:2]...), rightLines[2+start:min(len(rightLines), 2+start+height-2)]...)
	leftTitle, rightTitle := "FLEET", "DETAILS"
	if m.detail {
		rightTitle += " [focus]"
	} else {
		leftTitle += " [focus]"
	}
	scope := "All"
	if m.filter != (hostrouter.Host{}) {
		scope = m.filter.Name
	}
	leftTitle += " | Filter: " + coordClean(scope)
	frame := func(title string, width int) string {
		title = textansi.Truncate(title, max(1, width-4), "...")
		return "+- " + title + " " + strings.Repeat("-", max(0, width-textansi.StringWidth(title)-3)) + "+"
	}
	out := []string{frame(leftTitle, leftWidth) + " " + frame(rightTitle, rightWidth)}
	accent := func(s string) string {
		if _, noColor := os.LookupEnv("NO_COLOR"); noColor || os.Getenv("TERM") == "dumb" {
			return s
		}
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#155E63", Dark: "#80CBC4"}).Render(s)
	}
	if m.detail {
		out[0] = frame(leftTitle, leftWidth) + " " + accent(frame(rightTitle, rightWidth))
	} else {
		out[0] = accent(frame(leftTitle, leftWidth)) + " " + frame(rightTitle, rightWidth)
	}
	for i := range height {
		l, r := "", ""
		if i < len(leftBody) {
			l = leftBody[i]
		}
		if i < len(rightBody) {
			r = rightBody[i]
		}
		lp, rp := ledgerCell(l, leftWidth), ledgerCell(r, rightWidth)
		if strings.HasPrefix(l, "> ") {
			lp = accent(lp)
		}
		if r == "REPORTED WORK" || r == "OBSERVATION" || r == "TARGET" {
			rp = accent(rp)
		}
		out = append(out, "|"+lp+"| |"+rp+"|")
	}
	out = append(out, "+"+strings.Repeat("-", leftWidth)+"+ +"+strings.Repeat("-", rightWidth)+"+")
	footer := "Up/Down select | Tab/Enter Details | h filter | ? help | 2 Coordination | 3 Servers"
	if m.detail {
		footer = "Up/Down/PgDn scroll | Tab/Esc Fleet | c Connect | x actions | 2/t Team | ? help"
	}
	if m.registryError != "" {
		feedback = m.registryError + "; cached destination; actions blocked"
	}
	return strings.Join(append(out, textansi.Truncate(coordClean(feedback), m.width, "..."), textansi.Truncate(footer, m.width, "...")), "\n")
}
