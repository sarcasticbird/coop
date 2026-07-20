// Package tui is the coop fleet dashboard: every coop on the machine,
// its state, and lifecycle controls. Launch with `coop tui`.
package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sarcasticbird/coop/internal/lock"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/session"
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
	headerStyle  = lipgloss.NewStyle().Faint(true)
	cursorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	runningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	stoppedStyle = lipgloss.NewStyle().Faint(true)
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	helpStyle    = lipgloss.NewStyle().Faint(true)
)

// Result reports what the user asked for on exit.
type Result struct {
	EnterName    string // container to shell into ("" = plain quit)
	EnterWorkdir string
}

type model struct {
	rt     runtime.Runtime
	infos  []runtime.ContainerInfo
	cursor int
	// confirmName is the immutable identity captured when destroy
	// confirmation begins — a background refresh can reorder rows
	// between 'd' and 'y', and the wrong coop must not die for it.
	confirmName    string
	confirmProject string
	status         string
	err            error
	result         *Result
	width          int
	// busy serializes lifecycle actions: while one is in flight, keys
	// that would launch another are ignored. refreshing prevents ticks
	// from stacking list subprocesses behind a slow runtime.
	busy       bool
	refreshing bool
	// gen increments on every lifecycle action; refresh results carry
	// the gen at launch and are dropped if an action started since.
	gen int
	// stale marks the listing non-authoritative (an action failed or
	// its post-action listing failed) — state-dependent operations like
	// enter stay gated until a successful current-generation refresh.
	stale bool
}

// refreshMsg carries periodic listing results; actionDoneMsg is the
// ONLY message that completes a lifecycle action — a background
// refresh finishing must never clear the busy gate while an action is
// still running.
// refreshMsg carries listing results tagged with the generation that
// launched them: results from a refresh that started before the latest
// lifecycle action are stale and must not overwrite the action's
// outcome (listing or error).
type refreshMsg struct {
	infos []runtime.ContainerInfo
	gen   int
}
type actionDoneMsg struct {
	infos []runtime.ContainerInfo
	err   error
}
type errMsg struct {
	err error
	gen int
}
type tickMsg time.Time

// Run blocks until the user quits; the Result says whether to exec.
func Run(rt runtime.Runtime) (Result, error) {
	m := model{rt: rt, result: &Result{}}
	p := tea.NewProgram(m, tea.WithAltScreen())
	out, err := p.Run()
	if err != nil {
		return Result{}, err
	}
	if fm, ok := out.(model); ok {
		return *fm.result, nil
	}
	return Result{}, nil
}

func (m model) Init() tea.Cmd {
	// note: Init cannot mutate the model; the first tickMsg establishes
	// the refreshing flag through the single startRefresh path.
	return tea.Batch(func() tea.Msg { return tickMsg(time.Now()) })
}

func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// startRefresh is the ONLY way a refresh launches: sets the in-flight
// flag and tags the result with the current generation.
func (m *model) startRefresh() tea.Cmd {
	m.refreshing = true
	rt, gen := m.rt, m.gen
	return func() tea.Msg {
		infos, err := rt.Containers()
		if err != nil {
			return errMsg{err: err, gen: gen}
		}
		return refreshMsg{infos: infos, gen: gen}
	}
}

func (m model) selected() *runtime.ContainerInfo {
	if m.cursor >= 0 && m.cursor < len(m.infos) {
		return &m.infos[m.cursor]
	}
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case refreshMsg:
		m.refreshing = false
		if msg.gen != m.gen {
			return m, nil // stale: an action started after this launched
		}
		m.infos = msg.infos
		m.err = nil     // current-generation success clears stale errors
		m.stale = false // listing is authoritative again
		if m.cursor >= len(m.infos) {
			m.cursor = max(0, len(m.infos)-1)
		}
		return m, nil
	case actionDoneMsg:
		m.busy = false
		m.status = ""
		if msg.err != nil {
			m.err = msg.err
			m.stale = true // rows may not reflect the partial outcome
			return m, nil
		}
		m.err = nil
		m.stale = false
		m.infos = msg.infos
		if m.cursor >= len(m.infos) {
			m.cursor = max(0, len(m.infos)-1)
		}
		return m, nil
	case errMsg:
		m.refreshing = false
		if msg.gen == m.gen {
			m.err = msg.err
		}
		return m, nil
	case tickMsg:
		if m.busy || m.refreshing {
			return m, tick() // don't stack subprocesses
		}
		// explicit sequencing: startRefresh mutates m and MUST run
		// before the model value is returned
		cmd := m.startRefresh()
		return m, tea.Batch(cmd, tick())
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sel := m.selected()

	if m.confirmName != "" {
		name := m.confirmName
		projectPath := m.confirmProject
		m.confirmName = ""
		m.confirmProject = ""
		if msg.String() == "y" {
			rt := m.rt
			m.status = "destroying " + name + "..."
			m.busy = true
			m.gen++ // invalidate in-flight refreshes
			return m, sessionAction(rt, name, projectPath, func(s *session.Session) error {
				return s.Destroy()
			})
		}
		m.status = "destroy cancelled"
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.infos)-1 {
			m.cursor++
		}
	case "r":
		if m.busy || m.refreshing {
			return m, nil
		}
		cmd := m.startRefresh()
		return m, cmd
	case "u":
		if m.busy {
			return m, nil
		}
		if sel != nil && sel.State != "running" {
			name := sel.Name
			projectPath := sel.ProjectMount()
			if projectPath == "" {
				m.err = fmt.Errorf("cannot start %s: project mount unavailable", name)
				return m, nil
			}
			rt := m.rt
			m.status = "starting " + name + "..."
			m.busy = true
			m.gen++ // invalidate in-flight refreshes
			return m, sessionUpAction(rt, name, projectPath)
		}
	case "s":
		if m.busy {
			return m, nil
		}
		if sel != nil && sel.State == "running" {
			name := sel.Name
			rt := m.rt
			m.status = "stopping " + name + "..."
			m.busy = true
			m.gen++ // invalidate in-flight refreshes
			return m, action(rt, name, func() error { return rt.Stop(name) })
		}
	case "d":
		if sel != nil && !m.busy {
			projectPath := sel.ProjectMount()
			if projectPath == "" {
				m.err = fmt.Errorf("cannot destroy %s: project mount unavailable", sel.Name)
				return m, nil
			}
			m.confirmName = sel.Name
			m.confirmProject = projectPath
		}
	case "enter":
		// gated while busy or while the listing is non-authoritative:
		// the row's "running" may not reflect reality
		if sel != nil && sel.State == "running" && !m.busy && !m.stale {
			m.result.EnterName = sel.Name
			m.result.EnterWorkdir = projectMount(sel)
			return m, tea.Quit
		}
	}
	return m, nil
}

// action runs a lifecycle operation under the same per-project lock
// the CLI uses (TUI must not be a lock bypass), then reports completion
// with a fresh listing — listing failures surface, never masquerade as
// an empty fleet.
func action(rt runtime.Runtime, name string, op func() error) tea.Cmd {
	return func() tea.Msg {
		release, err := lock.Acquire(name, 5*time.Second)
		if err != nil {
			return actionDoneMsg{err: err}
		}
		defer release()
		return completeAction(rt, op)
	}
}

// Session lifecycle methods own the per-project lock, so they must not run
// through action's lock wrapper. This also verifies that the selected row still
// resolves to the same container identity before making lifecycle changes.
func sessionAction(rt runtime.Runtime, name, projectPath string, op func(*session.Session) error) tea.Cmd {
	return func() tea.Msg {
		return completeAction(rt, func() error {
			s, err := session.New(rt, projectPath)
			if err != nil {
				return err
			}
			if s.Name != name {
				return fmt.Errorf("selected container %s resolves to %s", name, s.Name)
			}
			return op(s)
		})
	}
}

func sessionUpAction(rt runtime.Runtime, name, projectPath string) tea.Cmd {
	return sessionAction(rt, name, projectPath, func(s *session.Session) error {
		return s.Up()
	})
}

func completeAction(rt runtime.Runtime, op func() error) actionDoneMsg {
	if err := op(); err != nil {
		return actionDoneMsg{err: err}
	}
	infos, err := rt.Containers()
	if err != nil {
		return actionDoneMsg{err: fmt.Errorf("action done but listing failed: %w", err)}
	}
	return actionDoneMsg{infos: infos}
}

func projectMount(info *runtime.ContainerInfo) string {
	if p := info.ProjectMount(); p != "" {
		return p
	}
	return "/"
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("coop — fleet") + "\n\n")
	// PROJECT needs a wide terminal; drop it rather than wrap rows.
	wide := m.width == 0 || m.width >= 100
	header := fmt.Sprintf("  %-30s %-8s %-15s %4s %5s %7s", "NAME", "STATE", "IP", "CPU", "MEM", "UPTIME")
	if wide {
		header += "  PROJECT"
	}
	b.WriteString(headerStyle.Render(header) + "\n")

	if len(m.infos) == 0 {
		b.WriteString(stoppedStyle.Render("  (no coops)") + "\n")
	}
	running := 0
	for i, info := range m.infos {
		if info.State == "running" {
			running++
		}
		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render("> ")
		}
		// truncate variable-width fields: %-Ns pads but never clips.
		// Names truncate in the middle — the trailing hash is what
		// distinguishes same-basename projects, and destroy decisions
		// are made off this rendering.
		row := fmt.Sprintf("%-30s %-8s %-15s %4d %5s %7s",
			truncateMiddle(info.Name, 30), truncate(info.State, 8),
			valueOr(info.IP, "-"), info.CPUs,
			fmtMem(info.Memory), fmtUptime(info))
		if wide {
			// fixed prefix: 2 cursor + 30 name + 8 state + 15 ip + 4 cpu +
			// 5 mem + 7 uptime + 6 single spaces + 2 gap = 79 columns
			// (verified by TestViewFitsTerminalWidth, which counts what
			// actually renders rather than trusting this comment)
			row += "  " + truncate(shortenPath(projectMount(&info)), max(0, m.widthOr(120)-79))
		}
		if info.State == "running" {
			row = runningStyle.Render(row)
		} else {
			row = stoppedStyle.Render(row)
		}
		b.WriteString(cursor + row + "\n")
	}

	b.WriteString("\n")
	b.WriteString(headerStyle.Render(fmt.Sprintf("%d coops · %d running", len(m.infos), running)) + "\n")
	if m.confirmName != "" {
		b.WriteString(errStyle.Render(
			fmt.Sprintf("destroy %s and its state volumes? [y/N]", m.confirmName)) + "\n")
	} else if m.err != nil {
		b.WriteString(errStyle.Render("error: "+m.err.Error()) + "\n")
	} else if m.status != "" {
		b.WriteString(helpStyle.Render(m.status) + "\n")
	}
	b.WriteString(helpStyle.Render("enter shell · u up · s stop · d destroy · r refresh · q quit"))
	return b.String()
}

func fmtMem(bytes int64) string {
	if bytes <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dG", bytes>>30)
}

// fmtUptime renders compact human uptime: 45s, 12m, 3h02m, 2d5h.
func fmtUptime(info runtime.ContainerInfo) string {
	if info.State != "running" || info.Started.IsZero() {
		return "-"
	}
	d := time.Since(info.Started)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// shortenPath abbreviates the home prefix for display.
func shortenPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func valueOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func truncate(s string, n int) string {
	if n > 1 && len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

// truncateMiddle keeps head and tail (the tail carries the identity
// hash in container names).
func truncateMiddle(s string, n int) string {
	if n < 10 || len(s) <= n {
		return truncate(s, n)
	}
	tail := 10
	head := n - tail - 1
	return s[:head] + "…" + s[len(s)-tail:]
}

func (m model) widthOr(fallback int) int {
	if m.width > 0 {
		return m.width
	}
	return fallback
}
