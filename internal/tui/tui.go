// Package tui provides a Coop table, session attachment, Kata and messaging.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	textansi "github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

// Result retains the CLI launch contract. Attachments now return to the table.
type Result struct{ EnterAgent, EnterName, EnterWorkdir string }

type Registry interface {
	List() ([]hostrouter.Host, error)
	ResolveTarget(hostrouter.Target) (hostrouter.Host, error)
}
type Inventory interface {
	Watch(context.Context, []hostrouter.Host) <-chan hostrouter.HostInventory
}
type Services struct {
	Inventory     Inventory
	Registry      Registry
	Coordination  Coordination
	Foreground    func(context.Context, hostrouter.ActionRequest) (*exec.Cmd, error)
	PrepareAction func(hostrouter.ActionRequest) (hostrouter.ActionRequest, error)
}

type hostState struct {
	host      hostrouter.Host
	entries   []hostrouter.InventoryEntry
	updated   time.Time
	errorCode string
}
type fleetModel struct {
	services       Services
	ctx            context.Context
	hosts          []hostState
	selected       hostrouter.Target
	width, height  int
	scroll         int
	status         string
	registryFailed bool
	refreshing     bool
	epoch          uint64
	stream         <-chan hostrouter.HostInventory
	cancel         context.CancelFunc
	busy           bool
	workBusy       bool
	work           map[hostrouter.Target]c.AgentDiagnostics
	draft          *messageDraft
}
type fleetRefresh struct{}
type fleetTick time.Time
type registryLoaded struct {
	hosts []hostrouter.Host
	err   error
	epoch uint64
}
type hostLoaded struct {
	inventory hostrouter.HostInventory
	epoch     uint64
	done      bool
}
type fleetActionDone struct{ err error }

func RunWithServices(s Services) (Result, error) {
	if s.Inventory == nil || s.Registry == nil {
		return Result{}, fmt.Errorf("Coop inventory and registry are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := tea.NewProgram(fleetModel{services: s, ctx: ctx}, tea.WithAltScreen()).Run()
	return Result{}, err
}
func (m fleetModel) Init() tea.Cmd { return func() tea.Msg { return fleetRefresh{} } }
func fleetClock() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return fleetTick(t) })
}
func (m *fleetModel) refresh() tea.Cmd {
	if m.refreshing || m.busy {
		return nil
	}
	m.refreshing = true
	m.epoch++
	epoch, registry := m.epoch, m.services.Registry
	return func() tea.Msg {
		hosts, err := registry.List()
		return registryLoaded{hosts: hosts, err: err, epoch: epoch}
	}
}
func nextHost(ch <-chan hostrouter.HostInventory, epoch uint64) tea.Cmd {
	return func() tea.Msg {
		h, ok := <-ch
		return hostLoaded{inventory: h, epoch: epoch, done: !ok}
	}
}
func (m fleetModel) rows() []hostrouter.InventoryEntry {
	var rows []hostrouter.InventoryEntry
	for _, h := range m.hosts {
		rows = append(rows, h.entries...)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		ar, br := m.current(a.Target) && a.State == "running", m.current(b.Target) && b.State == "running"
		if ar != br {
			return ar
		}
		if a.Target.Host.Name != b.Target.Host.Name {
			return a.Target.Host.Name < b.Target.Host.Name
		}
		if a.Target.Kind != b.Target.Kind {
			return a.Target.Kind < b.Target.Kind
		}
		return a.Target.ID < b.Target.ID
	})
	return rows
}
func (m fleetModel) current(t hostrouter.Target) bool {
	if m.registryFailed && t.Host != hostrouter.Local {
		return false
	}
	for _, h := range m.hosts {
		if h.host == t.Host {
			return h.errorCode == "" && fresh(h.updated)
		}
	}
	return false
}
func fresh(at time.Time) bool {
	return !at.IsZero() && !at.After(time.Now()) && time.Since(at) < c.PresenceStale
}
func (m fleetModel) entry(t hostrouter.Target) (hostrouter.InventoryEntry, bool) {
	if m.current(t) {
		for _, h := range m.hosts {
			for _, e := range h.entries {
				if e.Target == t {
					return e, true
				}
			}
		}
	}
	return hostrouter.InventoryEntry{}, false
}
func (m *fleetModel) selectVisible() {
	rows := m.rows()
	if len(rows) == 0 {
		m.selected, m.scroll = hostrouter.Target{}, 0
		return
	}
	index := -1
	for i, e := range rows {
		if e.Target == m.selected {
			index = i
		}
	}
	if index < 0 {
		index, m.selected = 0, rows[0].Target
	}
	limit := m.rowLimit()
	if index < m.scroll {
		m.scroll = index
	}
	if index >= m.scroll+limit {
		m.scroll = index - limit + 1
	}
	m.scroll = max(0, min(m.scroll, len(rows)-limit))
}
func (m fleetModel) rowLimit() int {
	return max(1, m.screenHeight()-m.headerRows()-len(m.footerLines()))
}
func (m fleetModel) screenHeight() int {
	if m.height <= 0 {
		return 24
	}
	return m.height
}
func (m fleetModel) headerRows() int {
	return min(2, max(0, m.screenHeight()-2))
}

func (m fleetModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case fleetRefresh:
		cmd := m.refresh()
		return m, tea.Batch(cmd, fleetClock())
	case fleetTick:
		inventory, work := m.refresh(), m.refreshWork()
		return m, tea.Batch(inventory, work, fleetClock())
	case registryLoaded:
		if msg.epoch != m.epoch {
			return m, nil
		}
		m.registryFailed = msg.err != nil
		if msg.err != nil {
			msg.hosts = nil
			for _, h := range m.hosts {
				if h.host != hostrouter.Local {
					msg.hosts = append(msg.hosts, h.host)
				}
			}
		}
		hosts := append([]hostrouter.Host{hostrouter.Local}, msg.hosts...)
		var states []hostState
		for _, host := range hosts {
			state := hostState{host: host}
			for _, old := range m.hosts {
				if old.host == host {
					state = old
				}
			}
			states = append(states, state)
		}
		m.hosts = states
		m.selectVisible()
		if m.cancel != nil {
			m.cancel()
		}
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, m.cancel = context.WithCancel(ctx)
		m.stream = m.services.Inventory.Watch(ctx, hosts)
		return m, nextHost(m.stream, m.epoch)
	case hostLoaded:
		if msg.epoch != m.epoch {
			return m, nil
		}
		if msg.done {
			m.refreshing = false
			cmd := m.refreshWork()
			return m, cmd
		}
		for i := range m.hosts {
			h := &m.hosts[i]
			if h.host != msg.inventory.Host {
				continue
			}
			h.errorCode = msg.inventory.Error
			if h.errorCode == "" {
				if hostrouter.ValidateEntries(msg.inventory.Entries) != nil {
					h.errorCode = "invalid_inventory"
				}
				for _, e := range msg.inventory.Entries {
					if e.Target.Host != h.host {
						h.errorCode = "invalid_inventory"
					}
				}
				if h.errorCode == "" {
					h.entries = append([]hostrouter.InventoryEntry(nil), msg.inventory.Entries...)
					h.updated = time.Now()
				}
			}
		}
		m.selectVisible()
		return m, nextHost(m.stream, m.epoch)
	case workLoaded:
		m.workBusy = false
		m.work = msg.reports
		return m, nil
	case messageReady:
		if m.draft == nil || m.draft.requestID != msg.requestID {
			return m, nil
		}
		m.draft.loading = false
		if msg.err != nil {
			m.status = "Cannot message this Coop: " + safeError(msg.err)
			m.draft = nil
		} else {
			m.draft.self, m.draft.thread = msg.self, msg.thread
		}
		return m, nil
	case messageSent:
		if m.draft == nil || m.draft.requestID != msg.requestID {
			return m, nil
		}
		m.draft.sending = false
		if msg.err != nil {
			m.draft.frozen = m.draft.frozen || msg.uncertain
			m.status = "Message not confirmed: " + safeError(msg.err)
		} else {
			m.status = "Message queued for " + m.draft.binding.AgentID + " in Kata"
			m.draft = nil
		}
		return m, nil
	case fleetActionDone:
		m.busy, m.refreshing = false, false
		m.status = "Returned to Coops"
		if msg.err != nil {
			m.status = "Could not open: " + safeError(msg.err)
		}
		cmd := m.refresh()
		return m, cmd
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.selectVisible()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m fleetModel) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" || m.draft == nil && k.String() == "q" {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}
	if m.draft != nil {
		return m.messageKey(k)
	}
	if m.busy {
		return m, nil
	}
	switch k.String() {
	case "up", "down", "pgup", "pgdown", "home", "end":
		rows := m.rows()
		for i, e := range rows {
			if e.Target != m.selected {
				continue
			}
			index := i
			switch k.String() {
			case "up":
				index--
			case "down":
				index++
			case "pgup":
				index -= m.rowLimit()
			case "pgdown":
				index += m.rowLimit()
			case "home":
				index = 0
			case "end":
				index = len(rows) - 1
			}
			m.selected = rows[max(0, min(index, len(rows)-1))].Target
			break
		}
		m.selectVisible()
	case "r":
		inventory, work := m.refresh(), m.refreshWork()
		return m, tea.Batch(inventory, work)
	case "enter":
		e, ok := m.entry(m.selected)
		if !ok {
			m.status = "Select a reachable Coop first"
			return m, nil
		}
		if e.State != "running" {
			m.status = "This Coop is not running"
			return m, nil
		}
		args := []string{"--"}
		directory := e.Project
		if e.Target.Kind == hostrouter.Agent {
			args, directory = []string{"--agent", e.Target.ID, "attach"}, ""
		}
		if e.Target.Kind == hostrouter.Interactive && directory == "" {
			m.status = "This Coop's project directory is unavailable"
			return m, nil
		}
		return m.open(hostrouter.ActionRequest{Target: e.Target, Operation: "command", Directory: directory, Args: args})
	case "k":
		args := []string{"task"}
		if r, ok := m.task(m.selected); ok && r.Report.Work.TaskURL != "" {
			if _, _, _, err := c.ParseKataTaskReference(r.Report.Work.TaskURL); err == nil {
				args = append(args, r.Report.Work.TaskURL)
			}
		}
		return m.open(hostrouter.ActionRequest{Operation: "work-ui", Args: args})
	case "m":
		return m.beginMessage()
	}
	return m, nil
}

func (m fleetModel) open(req hostrouter.ActionRequest) (tea.Model, tea.Cmd) {
	if m.services.Foreground == nil {
		m.status = "Terminal launcher unavailable"
		return m, nil
	}
	if req.Target.Kind != "" {
		if _, err := m.services.Registry.ResolveTarget(req.Target); err != nil {
			m.status = "Coop destination changed; refresh and select again"
			return m, nil
		}
	}
	if m.services.PrepareAction != nil {
		var err error
		req, err = m.services.PrepareAction(req)
		if err != nil {
			m.status = "Could not prepare session"
			return m, nil
		}
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	command, err := m.services.Foreground(ctx, req)
	if err != nil {
		m.status = "Could not prepare terminal: " + safeError(err)
		return m, nil
	}
	registry := m.services.Registry
	before := func() error {
		if req.Target.Kind != "" {
			_, err := registry.ResolveTarget(req.Target)
			return err
		}
		return nil
	}
	m.busy, m.refreshing = true, false
	m.epoch++
	if m.cancel != nil {
		m.cancel()
	}
	return m, tea.Exec(&foregroundCommand{command: command, before: before}, func(err error) tea.Msg { return fleetActionDone{err: err} })
}

func coopName(e hostrouter.InventoryEntry) string {
	if e.Target.Kind == hostrouter.Agent {
		return e.Target.ID
	}
	if e.Project != "" {
		id := e.Target.ID
		return filepath.Base(e.Project) + " (" + id[max(0, len(id)-6):] + ")"
	}
	return e.Name
}
func cell(s string, width int) string {
	s = strings.ReplaceAll(coordClean(s), "\n", " ")
	if width < 1 {
		return ""
	}
	s = textansi.Truncate(s, width, "…")
	return s + strings.Repeat(" ", max(0, width-textansi.StringWidth(s)))
}
func coordClean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, textansi.Strip(s))
}
func valueOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
func safeError(err error) string {
	for _, known := range []error{c.ErrForbidden, c.ErrConflict, c.ErrNotFound, c.ErrInvalid, c.ErrUnavailable, c.ErrEpoch, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "connection unavailable; inspect before retrying"
}

func (m fleetModel) View() string {
	// Feedback changes the available row count. Reveal selection using that
	// exact budget before rendering; never crop an actionable row afterward.
	m.selectVisible()
	width := m.width
	if width <= 0 {
		width = 80
	}
	nameWidth, hostWidth, stateWidth := 18, 12, 11
	if width < 60 {
		nameWidth, hostWidth = max(4, width-29), 10
	}
	row := func(prefix, name, host, state, task string) string {
		s := prefix + cell(name, nameWidth) + "  " + cell(host, hostWidth) + "  " + cell(state, stateWidth)
		if width >= 60 {
			s += "  " + cell(task, width-textansi.StringWidth(s)-2)
		}
		return textansi.Truncate(s, width, "")
	}
	lines := []string{"COOPS", row("  ", "COOP", "HOST", "STATE", "KATA")}
	lines = lines[:m.headerRows()]
	rows := m.rows()
	if len(rows) == 0 {
		text := "No Coops found"
		if m.registryFailed || hasHostError(m.hosts) {
			text = "Coop inventory unavailable"
		}
		if m.refreshing || len(m.hosts) == 0 {
			text = "Loading Coops…"
		}
		lines = append(lines, text)
	}
	for i := m.scroll; i < min(len(rows), m.scroll+m.rowLimit()); i++ {
		e := rows[i]
		prefix := "  "
		if e.Target == m.selected {
			prefix = "> "
		}
		state := e.State
		if !m.current(e.Target) {
			state = "unavailable"
		}
		task := "—"
		if e.Target.Kind == hostrouter.Agent {
			task = "unknown"
			if report, ok := m.task(e.Target); ok {
				task = valueOr(report.Report.Work.WorkTitle, "—")
				if task == "—" && report.Report.Work.TaskURL != "" {
					task = "Assigned task"
				}
			}
		}
		lines = append(lines, row(prefix, coopName(e), e.Target.Host.Name, state, task))
	}
	lines = append(lines, m.footerLines()...)
	for i := range lines {
		lines[i] = textansi.Truncate(coordClean(lines[i]), width, "")
	}
	return strings.Join(lines, "\n")
}

func (m fleetModel) footerLines() []string {
	limit := max(0, m.screenHeight()-m.headerRows()-1)
	var lines []string
	for _, h := range m.hosts {
		if h.errorCode != "" {
			lines = append(lines, h.host.Name+": unavailable (cached rows shown)")
		}
	}
	if m.registryFailed {
		lines = append(lines, "Server list unavailable; cached remote Coops cannot be opened")
	}
	if m.status != "" {
		lines = append(lines, m.status)
	}
	if m.draft != nil {
		// Keep the recipient, cursor and send/cancel keys ahead of diagnostics.
		lines = lines[:min(len(lines), max(0, limit-3))]
		width := m.width
		if width <= 0 {
			width = 80
		}
		return append(lines, m.messageView(width, limit-len(lines))...)
	} else {
		lines = append(lines, "↑/↓ select · Enter session · k Kata · m message · r refresh · q quit")
	}
	return lines[max(0, len(lines)-limit):]
}

func hasHostError(hosts []hostState) bool {
	for _, host := range hosts {
		if host.errorCode != "" {
			return true
		}
	}
	return false
}
