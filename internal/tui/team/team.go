// Package team provides a read-only Team screen, independent of fleet lifecycle controls.
package team

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

const refreshInterval = 5 * time.Second
const requestTimeout = 10 * time.Second

// Source exposes only the authenticated read used by this screen.
// Implementations must honor context cancellation.
type Source interface {
	Team(context.Context) ([]c.Presence, error)
}

// DiagnosticSource is optional; existing presence-only sources remain usable.
type DiagnosticSource interface {
	AgentDiagnostics(context.Context, string) (c.AgentDiagnostics, error)
}

type tickMsg time.Time
type resultMsg struct {
	rows []c.Presence
	err  error
	at   time.Time
}
type diagnosticResultMsg struct {
	id    uint64
	value c.AgentDiagnostics
	err   error
	at    time.Time
}

// Model can be embedded in a later unified TUI. Call Close when unmounting it.
type Model struct {
	source                           Source
	master                           string
	ctx                              context.Context
	cancel                           context.CancelFunc
	rows                             []c.Presence
	lastSuccess                      time.Time
	nextRefresh                      time.Time
	now                              time.Time
	refreshing, failed, help, closed bool
	failure                          string
	width, height, offset            int
	selected                         int
	detailOpen, detailBusy           bool
	detailAgent, detailFailure       string
	detail                           *c.AgentDiagnostics
	detailID                         uint64
	detailCancel                     context.CancelFunc
	detailNext                       time.Time
}

func New(ctx context.Context, source Source, masterID string) *Model {
	ctx, cancel := context.WithCancel(ctx)
	return &Model{source: source, master: masterID, ctx: ctx, cancel: cancel, now: time.Now(), width: 80, height: 24}
}

// Close cancels any outstanding read. It does not mutate the master.
func (m *Model) Close() { m.closed = true; m.cancel() }

func Run(ctx context.Context, source Source, masterID string, input io.Reader, output io.Writer) error {
	m := New(ctx, source, masterID)
	defer m.Close()
	_, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output), tea.WithAltScreen()).Run()
	return err
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) refresh() tea.Cmd {
	if m.refreshing || m.closed {
		return nil
	}
	m.refreshing = true
	ctx, source := m.ctx, m.source
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		rows, err := source.Team(ctx)
		return resultMsg{rows: rows, err: err, at: time.Now()}
	}
}

func (m *Model) Init() tea.Cmd { return tea.Batch(m.refresh(), tick()) }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.closed {
		return m, nil
	}
	switch msg := msg.(type) {
	case diagnosticResultMsg:
		if !m.detailOpen || msg.id != m.detailID {
			return m, nil
		}
		m.detailBusy = false
		if m.detailCancel != nil {
			m.detailCancel()
			m.detailCancel = nil
		}
		m.detailNext = msg.at.Add(refreshInterval)
		m.detailFailure = ""
		if msg.err == nil && (msg.value.Report.AgentID != m.detailAgent || c.ValidateDiagnostics(msg.value.Report) != nil) {
			msg.err = c.ErrInvalid
		}
		if msg.err == nil {
			m.detail = &msg.value
		} else {
			m.detailFailure = "diagnostics unavailable"
			if errors.Is(msg.err, c.ErrUnsupported) {
				m.detailFailure = "diagnostics unsupported by master"
			}
			if errors.Is(msg.err, c.ErrNotFound) {
				m.detailFailure = "no visible diagnostic report"
			}
		}
	case resultMsg:
		m.refreshing = false
		m.now = msg.at
		m.nextRefresh = msg.at.Add(refreshInterval)
		m.failed = msg.err != nil
		if msg.err == nil {
			m.rows, m.lastSuccess, m.failure = msg.rows, msg.at, ""
			m.selected = max(0, min(m.selected, len(m.rows)-1))
		} else {
			// Never render arbitrary transport errors (URLs, headers or response bodies).
			m.failure = "master unavailable or invalid response"
			if errors.Is(msg.err, c.ErrForbidden) {
				m.failure = "master access denied"
			}
			if errors.Is(msg.err, c.ErrConflict) {
				m.failure = "master identity/epoch mismatch"
			}
		}
	case tickMsg:
		m.now = time.Time(msg)
		var detailCmd tea.Cmd
		if m.detailOpen && !m.now.Before(m.detailNext) {
			detailCmd = m.refreshDetail()
		}
		if m.now.Before(m.nextRefresh) {
			return m, tea.Batch(detailCmd, tick())
		}
		return m, tea.Batch(m.refresh(), detailCmd, tick())
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			if m.detailOpen {
				m.closeDetail()
				return m, nil
			}
			m.Close()
			return m, tea.Quit
		case "q", "ctrl+c":
			m.Close()
			return m, tea.Quit
		case "enter":
			if m.detailOpen {
				return m, nil
			}
			return m, m.openDetail()
		case "tab", "shift+tab":
			if len(m.rows) > 0 && !m.detailOpen {
				delta := 1
				if msg.String() == "shift+tab" {
					delta = len(m.rows) - 1
				}
				m.selected = (m.selected + delta) % len(m.rows)
			}
		case "r":
			if m.detailOpen {
				return m, m.refreshDetail()
			}
			return m, m.refresh()
		case "?":
			m.help = !m.help
			m.offset = 0
		case "up", "k":
			m.offset--
		case "down", "j":
			m.offset++
		case "pgup":
			m.offset -= max(1, m.height-4)
		case "pgdown", " ":
			m.offset += max(1, m.height-4)
		case "home", "g":
			m.offset = 0
		case "end", "G":
			m.offset = len(m.lines())
		}
	}
	m.offset = max(0, min(m.offset, max(0, len(m.lines())-m.bodyHeight())))
	return m, nil
}

func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, s)
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

func age(now, then time.Time) string {
	if then.IsZero() {
		return "unknown"
	}
	if then.After(now) {
		return "unknown (clock ahead)"
	}
	return now.Sub(then).Truncate(time.Second).String() + " ago"
}

func (m *Model) lines() []string {
	if m.detailOpen {
		return m.detailLines()
	}
	var text strings.Builder
	if m.help {
		text.WriteString("READ ONLY: Team and selected-agent diagnostics. No messages, pings, model wakes or fleet changes.\nAutomatic refresh: every 5s after a completed read; timeout: 10s.\nr refresh | Tab select agent | Enter inspect | up/down or j/k scroll\nPgUp/PgDn page | g/G top/end | ? help | q / Ctrl-C quit\nEsc returns from detail or quits the list and cancels reads.\nUnknown means not supplied. Availability is returned by master, not inferred from VM state.\nSTALE retains the last successful snapshot after a read error. Last seen is heartbeat age, not progress.\n\n")
	}
	if m.lastSuccess.IsZero() {
		text.WriteString("No successful Team snapshot yet.\n")
	} else if len(m.rows) == 0 {
		text.WriteString("No visible agents returned by master.\n")
	}
	for i, row := range m.rows {
		if i == m.selected {
			text.WriteString("> ")
		}
		fmt.Fprintf(&text, "%s [%s]\n  Host: %s | Worker: %s | Generation: ", clean(row.Alias), clean(row.AgentID), clean(row.HostID), clean(row.InstanceID))
		if row.Generation == 0 {
			text.WriteString("unknown\n")
		} else {
			fmt.Fprintf(&text, "%d\n", row.Generation)
		}
		fmt.Fprintf(&text, "  Availability (returned): %s | Last seen: %s\n", clean(row.State), age(m.now, row.LastSeen))
		// Heartbeat diagnostics are whitespace-delimited key=value fields. Preserve
		// repeated task/blocker keys rather than implying they describe one job.
		details := map[string][]string{}
		for _, field := range strings.Fields(row.Reason) {
			key, value, ok := strings.Cut(field, "=")
			if ok {
				details[key] = append(details[key], clean(value))
			}
		}
		for _, field := range []struct{ key, label string }{{"task", "Task(s)"}, {"stage", "Stage"}, {"phase", "Phase"}, {"blocker", "Blocker(s)"}, {"depends_on", "Prerequisites"}} {
			fmt.Fprintf(&text, "  %s: %s\n", field.label, clean(strings.Join(details[field.key], ", ")))
		}
		fmt.Fprintf(&text, "  Diagnostic: %s\n\n", clean(row.Reason))
	}
	return strings.Split(ansi.Hardwrap(text.String(), max(1, m.width), true), "\n")
}

func (m *Model) bodyHeight() int { return max(0, m.height-3) }

func (m *Model) View() string {
	status := "Connecting; snapshot unknown"
	if !m.lastSuccess.IsZero() {
		status = "Snapshot " + age(m.now, m.lastSuccess)
	}
	if m.failed {
		if m.lastSuccess.IsZero() {
			status = "OFFLINE; no snapshot | " + m.failure
		} else {
			status = "STALE | " + status + " | " + m.failure
		}
	}
	if m.refreshing {
		status += " | refreshing"
	}
	lines := m.lines()
	start := max(0, min(m.offset, max(0, len(lines)-m.bodyHeight())))
	end := min(len(lines), start+m.bodyHeight())
	out := []string{"Team | read only | Master: " + clean(m.master), status}
	if m.height > 3 {
		out = append(out, lines[start:end]...)
	}
	out = append(out, "r refresh | Tab select | Enter inspect | j/k scroll | Esc back | q quit")
	if len(out) > m.height {
		out = out[:m.height]
	}
	for i := range out {
		out[i] = ansi.Truncate(out[i], m.width, "")
	}
	return strings.Join(out, "\n")
}
