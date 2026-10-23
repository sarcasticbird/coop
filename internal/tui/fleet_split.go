package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	textansi "github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
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
	p.preview = !m.detail
	if p.detailEntry == nil {
		for _, e := range m.rows() {
			if e.Target == m.selected {
				p.detailEntry = &e
				break
			}
		}
	}
	if !m.detail {
		p.diagnostics = fleetDiagnostics{}
	}
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
	if m.detail {
		if e.Diagnostics != nil && (m.services.Coordination == nil || m.services.Coordination.MasterID() != e.Diagnostics.MasterID || c.ValidateDiagnosticBinding(*e.Diagnostics) != nil || !m.diagnosticBindingUnique(*e.Diagnostics)) {
			p.diagnostics.failure = "bound master unavailable or mismatched; work unknown"
		}
		if p.diagnostics.report != nil && m.fleetDiagnosticContextChanged(p.diagnostics) {
			p.diagnostics.failure = "access context changed; work unknown"
		}
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

func (m fleetModel) fleetPaneLines() ([]string, int, int) {
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
	selected, selectedEnd := -1, -1
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
			vm := "VM: " + e.State
			if _, ok := m.entry(e.Target); !ok {
				vm = "VM: unknown; last " + e.State
			}
			lines = append(lines, textansi.Truncate("  "+coordClean(vm), m.width, "..."))
			if e.Target == m.selected {
				progress, blocker := "unknown", "unknown"
				if cached, ok := m.ledgerDiagnostics.entries[e.Target]; ok && activity != "unknown" && activity != "stale" && cached.report != nil {
					progress = diagnosticProgress(time.Now(), cached.report, cached.failure)
					blocker = diagnosticBlocker(cached.report.Report.Wait)
					at := cached.report.Report.Work.ObservedAt
					if !at.IsZero() && !at.After(time.Now()) && time.Since(at) < c.PresenceStale {
						work = diagnosticWorkName(cached.report.Report.Work)
						if stage := cached.report.Report.Work.Stage; stage != "" {
							work += " / " + strings.ReplaceAll(stage, "_", " ")
						}
					}
				}
				lines = append(lines, textansi.Truncate("  Task: "+coordClean(work), m.width, "..."), textansi.Truncate("  Progress: "+progress, m.width, "..."), textansi.Truncate("  Blocker: "+coordClean(blocker), m.width, "..."))
			} else {
				lines = append(lines, textansi.Truncate("  "+coordClean(work), m.width, "..."))
			}
			if cached, ok := m.ledgerDiagnostics.entries[e.Target]; ok && cached.report != nil {
				lines = append(lines, textansi.Truncate("  Report "+teamAge(cached.report.Report.ObservedAt)+" / receipt "+teamAge(cached.report.ReceivedAt), m.width, "..."))
			}
			if e.Target == m.selected {
				selectedEnd = len(lines) - 1
			}
		}
	}
	if len(m.hosts) == 0 {
		lines = append(lines, "Loading host registry and inventory...")
	}
	if !foundHost && m.filter != (hostrouter.Host{}) {
		lines = append(lines, "Filtered host removed; h to choose scope")
	}
	return lines, selected, selectedEnd
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

// A single physical projection drives both keyboard bounds and rendering. Clip
// the two pinned context rows before wrapping the body, which retains full IDs.
func (m fleetModel) detailsPaneLines() []string {
	logical := m.pageContext("Details")
	if m.detailEntry != nil {
		health := "unavailable"
		for _, h := range m.hosts {
			if h.host == m.detailEntry.Target.Host {
				health = fleetHostHealth(h)
			}
		}
		state := "unknown"
		if e, ok := m.entry(m.detailEntry.Target); ok && !m.detailInvalid {
			state = e.State
		}
		logical = m.fleetDetailsLines(state, health)
	} else {
		logical = append(logical, "", "No target selected", "Up/Down previews a bird; Enter attaches; Tab opens Details.")
	}
	lines := []string{
		textansi.Truncate(coordClean(logical[1]), m.width, "..."),
		textansi.Truncate(coordClean(logical[2]), m.width, "..."),
	}
	for _, line := range logical[conversationHeader:] {
		lines = append(lines, strings.Split(readingWrap(line, m.width), "\n")...)
	}
	return lines
}

func (m fleetModel) focusedContent() ([]string, string, int) {
	if !m.splitFleet() {
		return m.content()
	}
	left, right := m.splitWidths()
	if !m.detail {
		m.width = left
		lines, selected, _ := m.fleetPaneLines()
		return lines, "", selected
	}
	m = m.previewDetails()
	m.width = right
	return m.detailsPaneLines(), "", -1
}

func (m fleetModel) splitView() string {
	leftWidth, rightWidth := m.splitWidths()
	height := m.height - 4
	left := m
	left.width = leftWidth
	leftLines, _, _ := left.fleetPaneLines()
	leftScroll := m.scroll
	if m.detail {
		leftScroll = m.ledgerScroll
	}
	leftStart := min(leftScroll, max(0, len(leftLines)-height))
	leftBody := append([]string{leftLines[0]}, leftLines[1+leftStart:min(len(leftLines), 1+leftStart+height-1)]...)
	right := m.previewDetails()
	right.width = rightWidth
	rightLines := right.detailsPaneLines()
	feedback := m.status
	if right.detailEntry != nil {
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
	footer := "Enter attach | t Tasks | C Chat | Tab Details | h filter | ? help"
	if m.detail {
		footer = "Tab/Esc Fleet | c Connect | t Tasks | C Chat | x actions | ? help"
	}
	if !m.canonicalComms() {
		footer += " | 2 Coordination | 3 Servers"
	}
	if m.registryError != "" {
		feedback = m.registryError + "; cached destination; actions blocked"
	}
	return strings.Join(append(out, textansi.Truncate(coordClean(feedback), m.width, "..."), textansi.Truncate(footer, m.width, "...")), "\n")
}
