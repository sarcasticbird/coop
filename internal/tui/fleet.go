package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	textansi "github.com/charmbracelet/x/ansi"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/tui/team"
)

// Registry edits configured references only; transport and credentials stay in
// the backend. ResolveTarget must reject a changed registration address.
type Registry interface {
	List() ([]hostrouter.Host, error)
	Change(operation, name, address string) error
	ResolveTarget(hostrouter.Target) (hostrouter.Host, error)
}

type Inventory interface {
	Watch(context.Context, []hostrouter.Host) <-chan hostrouter.HostInventory
}

type Services struct {
	Coordination Coordination
	Version      string
	Inventory    Inventory
	Registry     Registry
	Actions      hostrouter.ActionClient
	Terminal     hostrouter.TerminalClient
	// Foreground uses the existing CLI on the released controlling terminal.
	Foreground func(context.Context, hostrouter.ActionRequest) (*exec.Cmd, error)
	// PrepareAction captures configured references before the consent preview.
	PrepareAction func(hostrouter.ActionRequest) (hostrouter.ActionRequest, error)
}

type hostState struct {
	host      hostrouter.Host
	entries   []hostrouter.InventoryEntry
	updated   time.Time
	errorCode string
	loading   bool
}

type serverForm struct {
	operation       string
	original        hostrouter.Host
	name, address   string
	field           int
	reviewedThrough int
}

type pendingAction struct {
	context         [2]string
	entry           hostrouter.InventoryEntry
	operation       string
	args            []string
	directory       string
	foreground      bool
	reviewedThrough int
	returnScroll    int
}

type fleetModel struct {
	coordination          coordState
	diagnostics           fleetDiagnostics
	ledgerDiagnostics     ledgerDiagnostics
	services              Services
	ctx                   context.Context
	hosts                 []hostState
	selected              hostrouter.Target
	filter                hostrouter.Host
	filtering             bool
	filterIndex           int
	filterChoices         []hostrouter.Host
	ledgerScroll          int
	ledgerFocus           bool
	overlayScroll         int
	overlayLedgerFocus    bool
	overlayTeamFocus      bool
	detailEntry           *hostrouter.InventoryEntry
	detailInvalid         bool
	server                hostrouter.Host
	workspace             int
	detail, help          bool
	width, height         int
	epoch                 uint64
	stream                <-chan hostrouter.HostInventory
	cancel                context.CancelFunc
	actionCancel          context.CancelFunc
	scroll                int
	detailScroll          int
	refreshing, busy      bool
	registryError, status string
	form                  *serverForm
	formScroll            int
	pending               *pendingAction
	result                Result
	operations            *operationForm
	runbook               bool
	actionSequence        uint64
	activeTarget          hostrouter.Target
	active                bool
	output                string
	// legacy is the compatibility adapter for local lifecycle and CLI attachment.
	legacy runtime.Runtime
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
type fleetActionDone struct {
	target    hostrouter.Target
	operation string
	err       error
	request   uint64
	output    string
}
type registryDone struct{ err error }

func runFleet(rt runtime.Runtime) (Result, error) {
	r, err := hostrouter.DefaultRegistry()
	if err != nil {
		return Result{}, err
	}
	return runServices(Services{
		Registry:  r,
		Inventory: hostrouter.Router{Local: hostrouter.LocalClient{}, Remote: hostrouter.SSH{}},
		Actions:   hostrouter.SSH{},
		Terminal:  hostrouter.SSH{},
	}, rt)
}

// RunWithServices is the launch boundary for hosts, actions and terminal routing.
func RunWithServices(s Services) (Result, error) { return runServices(s, nil) }

func runServices(s Services, legacy runtime.Runtime) (Result, error) {
	if s.Inventory == nil || s.Registry == nil {
		return Result{}, fmt.Errorf("fleet inventory and registry are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, err := tea.NewProgram(fleetModel{services: s, ctx: ctx, legacy: legacy}, tea.WithAltScreen()).Run()
	if err != nil {
		return Result{}, err
	}
	return out.(fleetModel).result, nil
}

func (m fleetModel) Init() tea.Cmd { return func() tea.Msg { return fleetRefresh{} } }
func fleetClock() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return fleetTick(t) })
}

func (m *fleetModel) refresh() tea.Cmd {
	if m.refreshing || m.busy {
		return nil
	}
	m.refreshing = true
	m.epoch++
	epoch, registry := m.epoch, m.services.Registry
	return func() tea.Msg { hosts, err := registry.List(); return registryLoaded{hosts, err, epoch} }
}

func nextHost(ch <-chan hostrouter.HostInventory, epoch uint64) tea.Cmd {
	return func() tea.Msg { h, ok := <-ch; return hostLoaded{inventory: h, epoch: epoch, done: !ok} }
}

func (m fleetModel) rows() []hostrouter.InventoryEntry {
	var rows []hostrouter.InventoryEntry
	for _, h := range m.hosts {
		if m.filter != (hostrouter.Host{}) && h.host != m.filter {
			continue
		}
		rows = append(rows, h.entries...)
	}
	return rows
}

func (m fleetModel) entry(t hostrouter.Target) (hostrouter.InventoryEntry, bool) {
	if m.registryError != "" && t.Host != hostrouter.Local {
		return hostrouter.InventoryEntry{}, false
	}
	for _, h := range m.hosts {
		if h.host == t.Host && h.errorCode == "" && !h.updated.IsZero() {
			for _, e := range h.entries {
				if e.Target == t {
					return e, true
				}
			}
		}
	}
	return hostrouter.InventoryEntry{}, false
}

func (m *fleetModel) reconcile() {
	m.reconcileLedgerDiagnostics()
	found := false
	for _, e := range m.rows() {
		if e.Target == m.selected {
			found = true
		}
	}
	if !found {
		m.selected = hostrouter.Target{}
	}
	if m.detailEntry != nil {
		e, ok := m.entry(m.detailEntry.Target)
		// Disconnection is transient, but changed placement or identity requires
		// fresh explicit intent, even if the old target later reappears.
		present := false
		for _, row := range m.rows() {
			if row.Target == m.detailEntry.Target {
				present = true
			}
		}
		if !present || (ok && (e.Name != m.detailEntry.Name || e.Project != m.detailEntry.Project || !sameBinding(e.Diagnostics, m.detailEntry.Diagnostics))) {
			m.detailInvalid = true
			m.resetFleetDiagnostics()
			m.diagnostics.failure = "target or diagnostic binding changed; work unknown"
		} else if !ok {
			m.diagnostics.failure = "host inventory unavailable; current work unknown"
		}
	}
	if m.pending != nil && m.pending.entry.Target.Kind != "" {
		present := false
		for _, row := range m.rows() {
			if row.Target == m.pending.entry.Target && row.Name == m.pending.entry.Name && row.Project == m.pending.entry.Project && sameBinding(row.Diagnostics, m.pending.entry.Diagnostics) {
				present = true
			}
		}
		if !present {
			m.scroll = m.pending.returnScroll
			m.pending = nil
			m.status = "Captured target changed or removed; action cancelled; select again"
		}
	}
	if m.operations != nil && m.operations.entry.Target.Kind != "" {
		present := false
		for _, row := range m.rows() {
			if row.Target == m.operations.entry.Target && row.Name == m.operations.entry.Name && row.Project == m.operations.entry.Project && sameBinding(row.Diagnostics, m.operations.entry.Diagnostics) {
				present = true
			}
		}
		if !present {
			m.scroll = m.operations.returnScroll
			m.operations = nil
			m.status = "Captured target changed or removed; action cancelled; select again"
		}
	}
	found = false
	for _, h := range m.hosts {
		if h.host == m.server {
			found = true
		}
	}
	if !found {
		m.server = hostrouter.Local
	}
}

// Ledger coordinates exclude the pinned context and the feedback/footer rows.
func (m fleetModel) ledgerViewport() (line, start, limit, maxScroll int) {
	if m.splitFleet() {
		left, _ := m.splitWidths()
		probe := m
		probe.width = left
		lines, selected := probe.fleetPaneLines()
		scroll := m.scroll
		if m.detail {
			scroll = m.ledgerScroll
		}
		return selected - 1, min(scroll, max(0, len(lines)-(m.height-4))), max(1, m.height-5), max(0, len(lines)-(m.height-4))
	}
	if m.workspace != 0 || m.detail || m.filtering || m.help || m.runbook || m.form != nil || m.pending != nil || m.operations != nil || m.height <= 0 {
		return -1, 0, 0, 0
	}
	lines, _, line := m.content()
	limit = max(1, m.height-2)
	maxScroll = max(0, len(lines)-limit)
	if m.height >= 4 {
		if line >= 0 {
			line--
		}
		limit--
	}
	return line, min(m.scroll, maxScroll), limit, maxScroll
}

func (m fleetModel) ledgerSelectionVisible() bool {
	line, start, limit, _ := m.ledgerViewport()
	return line >= 0 && line >= start && line < start+limit
}

func (m *fleetModel) revealLedgerSelection() {
	line, start, limit, maxScroll := m.ledgerViewport()
	if line < 0 {
		return
	}
	if line >= start && line < start+limit {
		return
	}
	scroll := start
	if line < start || line >= start+limit {
		scroll = min(line, maxScroll)
	}
	if m.detail && m.splitFleet() {
		m.ledgerScroll = scroll
	} else {
		m.scroll = scroll
	}
}

func (m fleetModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case fleetDiagnosticResult:
		return m.fleetDiagnosticUpdate(msg)
	case ledgerDiagnosticResult:
		return m.ledgerDiagnosticUpdate(msg)
	case coordResult:
		return m.coordUpdate(msg)
	case operationStarted:
		if !m.active || msg.request != m.actionSequence || msg.target != m.activeTarget {
			return m, nil
		}
		m.status = identity(msg.target) + " " + msg.operation + " running; esc cancels; no automatic retries"
		return m, msg.run
	case fleetRefresh:
		cmd := m.refresh()
		return m, tea.Batch(cmd, fleetClock())
	case fleetTick:
		cmd := m.refresh()
		ledger := m.refreshLedgerDiagnostics()
		if m.workspace == 0 && m.detail && !time.Time(msg).Before(m.diagnostics.next) {
			diagnostic := m.refreshFleetDiagnostics()
			return m, tea.Batch(cmd, diagnostic, ledger, fleetClock())
		}
		if m.workspace == 1 && m.coordination.follow && m.coordination.form == nil && !m.coordination.busy {
			return m, tea.Batch(cmd, m.coordLoad(false), ledger, fleetClock())
		}
		return m, tea.Batch(cmd, ledger, fleetClock())
	case registryLoaded:
		if msg.epoch != m.epoch {
			return m, nil
		}
		visible := m.ledgerSelectionVisible()
		m.registryError = ""
		if msg.err != nil {
			m.registryError = "registry unavailable: " + msg.err.Error()
			msg.hosts = nil
			for _, old := range m.hosts {
				if old.host != hostrouter.Local {
					msg.hosts = append(msg.hosts, old.host)
				}
			}
		}
		hosts := append([]hostrouter.Host{hostrouter.Local}, msg.hosts...)
		sort.Slice(hosts, func(i, j int) bool {
			if hosts[i] == hostrouter.Local {
				return true
			}
			if hosts[j] == hostrouter.Local {
				return false
			}
			return hosts[i].Name < hosts[j].Name
		})
		states := make([]hostState, 0, len(hosts))
		for _, h := range hosts {
			s := hostState{host: h}
			for _, old := range m.hosts {
				if old.host == h {
					s = old
					break
				}
			}
			s.loading = true
			states = append(states, s)
		}
		m.hosts = states
		m.reconcile()
		if visible {
			m.revealLedgerSelection()
		}
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
			return m, m.refreshLedgerDiagnostics()
		}
		visible := m.ledgerSelectionVisible()
		for i := range m.hosts {
			h := &m.hosts[i]
			if h.host != msg.inventory.Host {
				continue
			}
			h.loading = false
			h.errorCode = msg.inventory.Error
			if h.errorCode == "" {
				if hostrouter.ValidateEntries(msg.inventory.Entries) != nil {
					h.errorCode = "invalid_inventory"
				} else {
					valid := true
					for _, e := range msg.inventory.Entries {
						if e.Target.Host != h.host {
							valid = false
						}
					}
					if !valid {
						h.errorCode = "invalid_inventory"
					} else {
						h.entries = append([]hostrouter.InventoryEntry(nil), msg.inventory.Entries...)
						h.updated = time.Now()
					}
				}
			}
		}
		m.reconcile()
		if visible {
			m.revealLedgerSelection()
		}
		return m, nextHost(m.stream, m.epoch)
	case registryDone:
		m.busy = false
		if msg.err != nil {
			m.status = "server change failed: " + msg.err.Error()
			return m, nil
		}
		m.form = nil
		m.scroll = m.formScroll
		m.status = "server references updated"
		m.refreshing = false
		return m, m.refresh()
	case fleetActionDone:
		if msg.request != 0 && (!m.active || msg.request != m.actionSequence) {
			return m, nil
		}
		if m.active && (msg.request != m.actionSequence || msg.target != m.activeTarget) {
			return m, nil
		}
		m.active = false
		m.busy = false
		m.output = msg.output
		if m.actionCancel != nil {
			m.actionCancel()
			m.actionCancel = nil
		}
		m.status = identity(msg.target) + " " + msg.operation + " succeeded"
		if msg.err != nil {
			m.status = identity(msg.target) + " " + msg.operation + " failed: " + msg.err.Error() + "; refresh and inspect before retrying"
		}
		if errors.Is(msg.err, context.Canceled) {
			m.status = identity(msg.target) + " " + msg.operation + " cancelled; outcome may be unknown; refresh before retrying"
		}
		m.refreshing = false
		return m, m.refresh()
	case tea.WindowSizeMsg:
		if m.form != nil && m.form.operation == "remove" && (m.width != msg.Width || m.height != msg.Height) {
			f := *m.form
			f.reviewedThrough = 0
			m.form, m.scroll = &f, 0
			if m.help {
				m.overlayScroll = 0
			}
		}
		if m.pending != nil && (m.width != msg.Width || m.height != msg.Height) {
			p := *m.pending
			p.reviewedThrough = 0
			m.pending, m.scroll = &p, 0
		}
		teamVisible := m.teamSelectionVisible()
		conversationVisible := m.conversationSelectionVisible()
		formVisible := false
		if f := m.coordination.form; f != nil && !f.preview && !f.frozen && !m.help && !m.runbook {
			probe := m
			probe.revealComposeCursor()
			formVisible = probe.scroll == m.scroll
		}
		anchor, offset := m.conversationAnchor()
		m.width, m.height = msg.Width, msg.Height
		m.restoreConversationAnchor(anchor, offset)
		if conversationVisible {
			m.revealConversationSelection()
		}
		if formVisible {
			m.revealComposeCursor()
		}
		m.revealLedgerSelection()
		if teamVisible {
			m.revealTeamSelection()
		}
	case tea.KeyMsg:
		return m.keyFleet(msg)
	}
	return m, nil
}

func identity(t hostrouter.Target) string { return t.Host.Name + " / " + string(t.Kind) + " / " + t.ID }

func (m fleetModel) keyFleet(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if m.coordination.leaving != "" {
		return m.coordLeaveKey(k)
	}
	if key == "ctrl+c" || key == "q" && m.coordination.form == nil {
		if m.coordination.form != nil || len(m.coordination.drafts) > 0 {
			m.coordination.leaving = "quit"
			m.coordination.leaveScroll = m.scroll
			m.scroll = 0
			return m, nil
		}
	}
	if key == "ctrl+c" {
		if m.cancel != nil {
			m.cancel()
		}
		if m.actionCancel != nil {
			m.actionCancel()
		}
		return m, tea.Quit
	}
	if key == "pgdown" {
		if m.pending != nil || m.form != nil && m.form.operation == "remove" {
			m = m.reviewConfirmation()
		}
		lines, _, _ := m.focusedContent()
		step := 1
		if m.pending != nil || m.form != nil && m.form.operation == "remove" {
			step = max(1, m.height-2-m.confirmationHeaderRows())
		}
		if m.conversationPage() && !m.help && !m.runbook {
			step = max(1, m.height-conversationHeader-2)
		}
		viewport := max(1, m.height-2)
		if m.splitFleet() {
			viewport = m.height - 4
			if m.detail {
				step = max(1, m.height-6)
			}
		}
		m.scroll = min(m.scroll+step, max(0, len(lines)-viewport))
		return m, nil
	}
	if key == "pgup" {
		step := 1
		if m.conversationPage() && !m.help && !m.runbook {
			step = max(1, m.height-conversationHeader-2)
		}
		if m.splitFleet() && m.detail {
			step = max(1, m.height-6)
		}
		m.scroll = max(0, m.scroll-step)
		return m, nil
	}
	if m.busy {
		if key == "esc" && m.actionCancel != nil {
			m.actionCancel()
			m.status = "cancelling; remote outcome may be unknown; refresh before retry"
		} else {
			m.status = "Operation in progress; control blocked, not queued; Esc cancels if supported"
		}
		return m, nil
	}
	if m.help {
		if key == "?" || key == "esc" || key == "f1" {
			m.help = false
			m.scroll = m.overlayScroll
			if m.overlayLedgerFocus {
				m.revealLedgerSelection()
			}
			if m.overlayTeamFocus {
				m.revealTeamSelection()
			}
		}
		return m, nil
	}
	if m.runbook {
		if key == "esc" || key == "v" {
			m.runbook = false
			m.scroll = m.overlayScroll
			if m.overlayLedgerFocus {
				m.revealLedgerSelection()
			}
			if m.overlayTeamFocus {
				m.revealTeamSelection()
			}
		}
		return m, nil
	}
	if m.operations != nil {
		if key == "f1" {
			m.help, m.overlayScroll, m.scroll = true, m.scroll, 0
			return m, nil
		}
		return m.keyOperation(k)
	}
	if m.pending != nil {
		p := *m.pending
		if key == "esc" || key == "n" {
			m.pending = nil
			m.scroll = p.returnScroll
			m.status = "action cancelled"
		}
		if key == "y" || key == "enter" && p.operation == "enter" {
			m = m.reviewConfirmation()
			lines, _, _ := m.content()
			if m.pending.reviewedThrough < len(lines) {
				m.status = "Review complete confirmation with PgDn before confirming"
				return m, nil
			}
			m.pending = nil
			m.scroll = p.returnScroll
			return m.dispatch(p)
		}
		return m, nil
	}
	if m.form != nil {
		if key == "f1" {
			m.help, m.overlayScroll, m.scroll = true, m.scroll, 0
			return m, nil
		}
		return m.keyForm(k)
	}
	if m.filtering {
		switch key {
		case "esc", "h":
			m.filtering = false
			m.scroll = m.ledgerScroll
		case "up", "k":
			m.filterIndex = max(0, m.filterIndex-1)
		case "down", "j":
			m.filterIndex = min(len(m.filterChoices), m.filterIndex+1)
		case "enter":
			m.filter = hostrouter.Host{}
			if m.filterIndex > 0 && m.filterIndex <= len(m.filterChoices) {
				m.filter = m.filterChoices[m.filterIndex-1]
			}
			m.filtering, m.detail = false, false
			m.selected = hostrouter.Target{}
			m.detailEntry = nil
			m.scroll = 0
			m.status = "filter changed; select a target explicitly"
		}
		if key == "up" || key == "k" || key == "down" || key == "j" {
			_, _, selectedLine := m.content()
			limit := max(1, m.height-3)
			selectedLine--
			if selectedLine < m.scroll {
				m.scroll = max(0, selectedLine)
			}
			if selectedLine >= m.scroll+limit {
				m.scroll = selectedLine - limit + 1
			}
		}
		return m, nil
	}
	if m.workspace == 0 && !m.detail {
		switch key {
		case "h":
			m.filtering, m.filterIndex, m.ledgerScroll = true, 0, m.scroll
			m.filterChoices = nil
			for _, h := range m.hosts {
				m.filterChoices = append(m.filterChoices, h.host)
			}
			m.scroll = 0
			return m, nil
		case "enter", "tab", "shift+tab", "u", "s", "d", "x":
			for _, e := range m.rows() {
				if e.Target == m.selected {
					if e.Diagnostics != nil {
						binding := *e.Diagnostics
						e.Diagnostics = &binding
					}
					m.ledgerFocus = m.ledgerSelectionVisible()
					if m.detailEntry == nil || m.detailEntry.Target != e.Target || m.detailInvalid || !sameBinding(m.detailEntry.Diagnostics, e.Diagnostics) {
						m.detailScroll = 0
					}
					m.detail, m.detailEntry, m.detailInvalid = true, &e, false
					m.resetFleetDiagnostics()
					m.ledgerScroll, m.scroll, m.status = m.scroll, m.detailScroll, ""
					if key != "enter" && key != "tab" && key != "shift+tab" {
						m.status = "Details opened; choose an action explicitly"
					}
					cmd := m.refreshFleetDiagnostics()
					return m, cmd
				}
			}
			m.status = "No target selected; use up/down to select, then Enter for Details"
			return m, nil
		}
	}
	if m.workspace == 0 && key == "enter" {
		m.status = "Details focused; c opens Connect preflight"
		return m, nil
	}
	if m.workspace == 0 && key == "c" && !m.detail {
		m.status = "Open Details before Connect"
		return m, nil
	}
	if m.workspace == 0 && m.detail && (key == "c" || key == "u" || key == "s" || key == "d" || key == "x") {
		if m.detailInvalid || m.detailEntry == nil {
			m.status = "Target changed or removed; return to Fleet and select again"
			return m, nil
		}
		e, ok := m.entry(m.detailEntry.Target)
		if !ok || e.Name != m.detailEntry.Name || e.Project != m.detailEntry.Project || !sameBinding(e.Diagnostics, m.detailEntry.Diagnostics) {
			m.status = "selected host inventory is not authoritative or target changed; refresh and select again"
			return m, nil
		}
		m.selected = m.detailEntry.Target
	}
	if m.workspace == 1 && (m.coordination.form != nil || (key != "1" && key != "2" && key != "3" && key != "?" && key != "q" && key != "v")) {
		out, cmd := m.coordKey(k)
		updated := out.(fleetModel)
		if updated.coordination.form != nil && !updated.coordination.form.preview && !updated.coordination.form.frozen && key != "esc" && key != "f1" {
			updated.revealComposeCursor()
		}
		if updated.coordination.form != nil || updated.coordination.leaving != "" {
			return updated, cmd
		}
		if updated.conversationPage() {
			if key == "up" || key == "down" || key == "k" || key == "j" {
				updated.revealConversationSelection()
			}
			return updated, cmd
		}
		if updated.teamList() && key == "esc" {
			return updated, cmd
		}
		if updated.teamList() && (key == "up" || key == "down" || key == "k" || key == "j") {
			updated.revealTeamSelection()
			return updated, cmd
		}
		if key == "up" || key == "down" || key == "k" || key == "j" || key == "tab" || key == "shift+tab" || key == "esc" {
			_, _, selected := updated.content()
			if selected >= 0 && updated.height > 0 {
				limit := max(1, updated.height-2)
				if selected < updated.scroll {
					updated.scroll = selected
				}
				if selected >= updated.scroll+limit {
					updated.scroll = selected - limit + 1
				}
			}
		}
		return updated, cmd
	}
	switch key {
	case "v":
		m.overlayTeamFocus = m.teamSelectionVisible()
		m.overlayScroll, m.overlayLedgerFocus = m.scroll, m.ledgerSelectionVisible()
		m.runbook, m.scroll = true, 0
	case "x":
		return m.openOperations()
	case "q":
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case "?":
		m.overlayTeamFocus = m.teamSelectionVisible()
		m.overlayScroll, m.overlayLedgerFocus = m.scroll, m.ledgerSelectionVisible()
		m.help = true
		m.scroll = 0
	case "1":
		m.workspace = 0
		m.scroll = 0
	case "2":
		m.workspace = 1
		m.scroll = 0
		if m.coordination.seen.IsZero() {
			return m, m.coordLoad(false)
		}
	case "3":
		m.workspace = 2
		m.scroll = 0
	case "tab", "shift+tab":
		if m.workspace == 0 && m.detail {
			m.resetFleetDiagnostics()
			m.detailScroll = m.scroll
			m.detail = false
			if !m.splitFleet() {
				m.detailEntry = nil
			}
			m.scroll = m.ledgerScroll
			if m.ledgerFocus {
				m.revealLedgerSelection()
			}
		}
	case "esc":
		returning := m.workspace == 0 && m.detail
		if returning {
			m.resetFleetDiagnostics()
			m.detailScroll = m.scroll
			m.scroll = m.ledgerScroll
		}
		m.detail = false
		if !m.splitFleet() {
			m.detailEntry = nil
		}
		if returning && m.ledgerFocus {
			m.revealLedgerSelection()
		}
		m.status = ""
	case "r":
		if m.workspace == 0 && m.detail {
			inventory, diagnostic := m.refresh(), m.refreshFleetDiagnostics()
			return m, tea.Batch(inventory, diagnostic)
		}
		return m, m.refresh()
	case "up", "k", "down", "j":
		previousTarget, previousHost := m.selected, m.server
		delta := 1
		if key == "up" || key == "k" {
			delta = -1
		}
		if m.workspace == 0 {
			if m.detail {
				lines, _, _ := m.focusedContent()
				viewport := m.height - 2
				if m.splitFleet() {
					viewport = m.height - 4
				}
				m.scroll = max(0, min(m.scroll+delta, max(0, len(lines)-viewport)))
				return m, nil
			}
			rows := m.rows()
			found := false
			for i, e := range rows {
				if e.Target == m.selected {
					found = true
					m.selected = rows[max(0, min(len(rows)-1, i+delta))].Target
					break
				}
			}
			if !found && len(rows) > 0 {
				index := 0
				if delta < 0 {
					index = len(rows) - 1
				}
				m.selected = rows[index].Target
			}
		} else if m.workspace == 2 {
			for i, h := range m.hosts {
				if h.host == m.server {
					m.server = m.hosts[max(0, min(len(m.hosts)-1, i+delta))].host
					break
				}
			}
		}
		if previousTarget != m.selected || previousHost != m.server {
			if m.workspace == 0 {
				m.detailScroll, m.detailInvalid = 0, false
				for _, e := range m.rows() {
					if e.Target == m.selected {
						if e.Diagnostics != nil {
							binding := *e.Diagnostics
							e.Diagnostics = &binding
						}
						m.detailEntry = &e
						break
					}
				}
			}
			if m.splitFleet() {
				m.revealLedgerSelection()
				return m, nil
			}
			lines, _, selectedLine := m.focusedContent()
			if selectedLine >= 0 && m.height > 0 {
				limit := max(1, m.height-2)
				if m.workspace == 0 {
					selectedLine-- // Fleet context occupies a pinned line.
					limit = max(1, m.height-3)
					if m.splitFleet() {
						limit = max(1, m.height-5)
					}
				}
				if selectedLine < m.scroll {
					m.scroll = selectedLine
				}
				if selectedLine >= m.scroll+limit {
					m.scroll = selectedLine - limit + 1
				}
				m.scroll = min(m.scroll, max(0, len(lines)-limit))
			}
		}
	case "a", "e", "d":
		if m.workspace == 2 {
			if key != "a" && m.server == hostrouter.Local {
				m.status = "local is reserved; select a registered server"
				return m, nil
			}
			op := map[string]string{"a": "add", "e": "update", "d": "remove"}[key]
			m.form = &serverForm{operation: op, original: m.server}
			m.formScroll = m.scroll
			m.scroll = 0
			if key != "a" {
				m.form.name, m.form.address = m.server.Name, m.server.Address
				m.form.field = 1
			}
			return m, nil
		}
		if key == "d" && m.workspace == 0 {
			if e, ok := m.entry(m.selected); ok {
				return m.confirmAction(pendingAction{entry: e, operation: "destroy"})
			} else {
				m.status = "selected host inventory is not authoritative; refresh"
			}
		}
	case "u", "s", "c":
		if m.workspace != 0 {
			return m, nil
		}
		e, ok := m.entry(m.selected)
		if !ok {
			m.status = "selected host inventory is not authoritative; refresh"
			return m, nil
		}
		op := map[string]string{"u": "up", "s": "down", "c": "enter"}[key]
		if (op == "enter" || op == "down") && e.State != "running" {
			m.status = identity(e.Target) + " is not running"
			return m, nil
		}
		return m.confirmAction(pendingAction{entry: e, operation: op})
	}
	return m, nil
}

func (m fleetModel) keyForm(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := *m.form
	m.form = &f
	if f.operation == "remove" && k.String() == "n" {
		m.form = nil
		m.scroll = m.formScroll
		m.status = "server change cancelled"
		return m, nil
	}
	switch k.String() {
	case "esc":
		m.form = nil
		m.scroll = m.formScroll
		m.status = "server change cancelled"
	case "tab", "shift+tab":
		if f.operation == "add" {
			m.form.field = 1 - f.field
		}
	case "backspace", "ctrl+h":
		v := &m.form.address
		if f.field == 0 {
			v = &m.form.name
		}
		r := []rune(*v)
		if len(r) > 0 {
			*v = string(r[:len(r)-1])
		}
	case "enter":
		if f.operation == "remove" {
			m.status = "press y to remove " + f.original.Name + " (" + f.original.Address + ")"
			return m, nil
		}
		return m.saveServer(f)
	case "y":
		if f.operation == "remove" {
			m = m.reviewConfirmation()
			lines, _, _ := m.content()
			if m.form.reviewedThrough < len(lines) {
				m.status = "Review complete removal with PgDn before confirming"
				return m, nil
			}
			return m.saveServer(f)
		}
		fallthrough
	default:
		if f.operation != "remove" && k.Type == tea.KeyRunes {
			for _, r := range k.Runes {
				if r < 32 || r == 127 {
					m.status = "server references must be a single line without control characters"
					return m, nil
				}
			}
			v := &m.form.address
			if f.field == 0 {
				v = &m.form.name
			}
			if len(*v)+len(string(k.Runes)) <= 255 {
				*v += string(k.Runes)
			}
		}
	}
	return m, nil
}

func (m fleetModel) saveServer(f serverForm) (tea.Model, tea.Cmd) {
	m.busy = true
	m.status = f.operation + " server " + f.name + "..."
	registry := m.services.Registry
	return m, func() tea.Msg {
		if f.operation != "add" {
			_, err := registry.ResolveTarget(hostrouter.Target{Host: f.original, Kind: hostrouter.Interactive, ID: "validation"})
			if err != nil {
				return registryDone{err}
			}
		}
		return registryDone{registry.Change(f.operation, f.name, f.address)}
	}
}

func (m fleetModel) dispatch(p pendingAction) (tea.Model, tea.Cmd) {
	if m.services.Foreground != nil || p.operation != "enter" && p.operation != "up" && p.operation != "down" && p.operation != "destroy" {
		return m.dispatchOperation(p)
	}
	e, ok := m.entry(p.entry.Target)
	if !ok || e.Name != p.entry.Name || e.Project != p.entry.Project || !sameBinding(e.Diagnostics, p.entry.Diagnostics) {
		m.status = identity(p.entry.Target) + " changed or became stale; action cancelled"
		return m, nil
	}
	if _, err := m.services.Registry.ResolveTarget(e.Target); err != nil {
		m.status = err.Error()
		return m, nil
	}
	if p.operation == "enter" {
		if e.State != "running" {
			m.status = identity(e.Target) + " is not running; connect cancelled"
			return m, nil
		}
		if m.services.Terminal != nil && (e.Target.Host != hostrouter.Local || m.legacy == nil) {
			m.busy = true
			m.epoch++
			m.refreshing = false
			if m.cancel != nil {
				m.cancel()
			}
			for i := range m.hosts {
				if m.hosts[i].host == e.Target.Host {
					m.hosts[i].errorCode = "terminal_pending"
				}
			}
			ctx := m.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			command := &terminalCommand{ctx: ctx, client: m.services.Terminal, target: e.Target, registry: m.services.Registry}
			return m, tea.Exec(command, func(err error) tea.Msg { return fleetActionDone{target: e.Target, operation: "terminal", err: err} })
		}
		if e.Target.Host != hostrouter.Local || m.legacy == nil {
			m.status = identity(e.Target) + ": terminal backend unavailable"
			return m, nil
		}
		m.result = Result{EnterName: e.Name, EnterWorkdir: e.Project}
		if e.Target.Kind == hostrouter.Agent {
			m.result.EnterAgent = e.Target.ID
			m.result.EnterWorkdir = ""
		}
		if m.result.EnterAgent == "" && e.Project == "" {
			m.status = "project mount unavailable"
			return m, nil
		}
		return m, tea.Quit
	}
	if m.services.Actions == nil && (e.Target.Host != hostrouter.Local || m.legacy == nil) {
		m.status = identity(e.Target) + ": action backend unavailable"
		return m, nil
	}
	m.busy = true
	m.epoch++ // discard any inventory started before this action
	m.refreshing = false
	if m.cancel != nil {
		m.cancel()
	}
	for i := range m.hosts {
		if m.hosts[i].host == e.Target.Host {
			m.hosts[i].errorCode = "action_pending"
		}
	}
	m.status = identity(e.Target) + " " + p.operation + "..."
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, m.actionCancel = context.WithCancel(ctx)
	services, rt := m.services, m.legacy
	return m, func() tea.Msg {
		if _, err := services.Registry.ResolveTarget(e.Target); err != nil {
			return fleetActionDone{target: e.Target, operation: p.operation, err: err}
		}
		var err error
		if services.Actions != nil && (e.Target.Host != hostrouter.Local || rt == nil) {
			args := p.args
			if args == nil {
				args = []string{p.operation}
			}
			if p.args == nil && e.Target.Kind == hostrouter.Agent {
				args = append([]string{"--agent", e.Target.ID}, args...)
			}
			var input io.Reader
			if p.operation == "destroy" {
				input = strings.NewReader("y\n")
			}
			directory := e.Project
			if e.Target.Kind == hostrouter.Agent {
				directory = ""
			}
			result, actionErr := services.Actions.Execute(ctx, hostrouter.ActionRequest{Target: e.Target, Operation: "command", Args: args, Directory: directory}, input, io.Discard, io.Discard)
			err = actionErr
			if result.Target != e.Target {
				err = fmt.Errorf("mismatched action response; refresh before retry")
			}
			if err == nil && result.ExitCode != 0 {
				err = fmt.Errorf("exit status %d", result.ExitCode)
			}
		} else {
			info := runtime.ContainerInfo{Name: e.Name, State: e.State}
			if e.Target.Kind == hostrouter.Agent {
				info.Mode, info.Profile = "agent", e.Target.ID
			}
			if e.Project != "" {
				info.Mounts = []runtime.MountInfo{{Source: e.Project, Destination: e.Project, Bind: true}}
			}
			local := model{rt: rt, infos: []runtime.ContainerInfo{info}, result: &Result{}}
			key := map[string]string{"up": "u", "down": "s", "destroy": "d"}[p.operation]
			out, cmd := local.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
			if p.operation == "destroy" && out.(model).confirmName != "" {
				out, cmd = out.(model).key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
			}
			if cmd != nil {
				err = cmd().(actionDoneMsg).err
			} else {
				err = out.(model).err
			}
		}
		return fleetActionDone{target: e.Target, operation: p.operation, err: err}
	}
}

// Keep every host row in the content. The viewport is the only clipping layer,
// so paging can reach hosts that have no selectable target entries.
func (m fleetModel) content() ([]string, string, int) {
	var lines []string
	selectedLine := -1
	lines = append(lines, "COOP   [1 Fleet]   [2 Coordination]   [3 Servers]", "")
	if m.workspace == 1 {
		if m.teamList() || m.conversationPage() || m.composePage() || m.coordination.teamDetail != nil && m.coordination.form == nil {
			lines = m.coordLines()
		} else {
			lines = append(lines, m.coordLines()...)
		}
		for i, line := range lines {
			if strings.HasPrefix(line, "> ") {
				selectedLine = i
				break
			}
		}
	} else {
		if m.workspace == 0 {
			scope := "All"
			hiddenHosts, hiddenTargets := 0, 0
			if m.filter != (hostrouter.Host{}) {
				scope = m.filter.Name + " (" + valueOr(m.filter.Address, "this machine") + ")"
				for _, h := range m.hosts {
					if h.host != m.filter {
						hiddenHosts++
						hiddenTargets += len(h.entries)
					}
				}
			}
			lines = []string{"Fleet Ledger | Filter: " + scope}
			if hiddenHosts > 0 || hiddenTargets > 0 {
				lines[0] += fmt.Sprintf(" | %dH/%dT hidden", hiddenHosts, hiddenTargets)
			}
			if m.width > 0 {
				lines[0] = textansi.Truncate(lines[0], m.width, "...")
			}
			if m.selected == (hostrouter.Target{}) && !m.detail {
				lines = append(lines, "No target selected")
			}
			master, principal := "not configured", "unavailable"
			if m.services.Coordination != nil {
				master = m.services.Coordination.MasterID()
				if m.ledgerDiagnostics.master == master {
					principal = valueOr(m.ledgerDiagnostics.self.Principal.ID, "unknown")
				}
			}
			lines = append(lines, "Master: "+coordClean(master)+" | As: "+coordClean(principal))
			lines = append(lines, ledgerRow(m.width, "  ", "NAME", "KIND", "VM", "ACTIVITY", "WORK / BLOCKER"))
		} else {
			lines = append(m.pageContext("Servers | SSH references"), "HOST   CONFIGURED SSH REFERENCE", "Master configuration is separate; removing a reference does not delete VMs.")
			if len(m.hosts) == 1 && m.hosts[0].host == hostrouter.Local {
				lines = append(lines, "SSH setup: no SSH servers registered. Press a to add an existing reference from ~/.ssh/config.", "SSH registration is separate from master setup. It does not provision a host, enter tokens, bypass trust, or start services.")
			}
		}
		var body []string
		selectedHealth := ""
		if len(m.hosts) == 0 {
			body = append(body, "Loading host registry and local inventory...")
		}
		for _, h := range m.hosts {
			state := "loading"
			if !h.updated.IsZero() {
				age := time.Since(h.updated)
				freshness := age.Round(time.Second).String()
				if age >= 0 && age < 5*time.Second {
					freshness = "<5s"
				}
				state = "ok; snapshot " + freshness + " old"
			}
			if h.errorCode != "" {
				state = h.errorCode
				if !h.updated.IsZero() {
					state += "; stale snapshot " + time.Since(h.updated).Round(time.Second).String() + " old"
				}
			}
			if m.registryError != "" && h.host != hostrouter.Local {
				state = "registry unavailable; cached destination; actions blocked"
			}
			if h.loading && h.updated.IsZero() && h.errorCode == "" {
				state += "; refreshing"
			}
			if h.host == m.selected.Host {
				selectedHealth = state
			}
			if m.workspace == 0 && m.filter != (hostrouter.Host{}) && h.host != m.filter {
				continue
			}
			prefix := "  "
			if m.workspace == 2 && h.host == m.server {
				prefix = "> "
				selectedLine = len(lines) + len(body)
			}
			hostLabel := coordClean(h.host.Name)
			if m.workspace == 0 {
				hostLabel = "Host: " + hostLabel
			}
			body = append(body, prefix+hostLabel+" ("+coordClean(valueOr(h.host.Address, "this machine"))+")")
			// Health must remain readable even when a long SSH reference is clipped.
			health := "    [" + state + "]"
			if m.width > 0 {
				health = textansi.Wrap(health, m.width, "")
			}
			body = append(body, strings.Split(health, "\n")...)
			if m.workspace != 0 {
				continue
			}
			if len(h.entries) == 0 && (!h.loading || !h.updated.IsZero()) && h.errorCode == "" {
				body = append(body, "  (no Coops)")
			}
			for _, e := range h.entries {
				prefix = "  "
				if e.Target == m.selected {
					prefix = "> "
					selectedLine = len(lines) + len(body)
				}
				vm := e.State
				activity, work := m.ledgerSummary(e)
				if _, ok := m.entry(e.Target); !ok {
					vm, work = "unknown", "[stale] last VM: "+e.State
				}
				body = append(body, ledgerRow(m.width, prefix, ledgerName(e), string(e.Target.Kind), vm, activity, work))
				if m.width > 0 && m.width < 65 && e.Target == m.selected {
					body = append(body, textansi.Truncate("  Activity: "+activity+" / "+work, m.width, "..."))
				}
			}
		}
		for _, line := range body {
			if m.width > 0 && m.workspace != 2 {
				line = textansi.Truncate(line, m.width, "...")
			}
			lines = append(lines, line)
		}
		if m.workspace == 0 && len(body) == 0 && m.filter != (hostrouter.Host{}) {
			lines = append(lines, "Filtered host removed or registration changed; h to choose scope")
		}
		if m.workspace == 0 && m.detail && m.detailEntry != nil {
			e := *m.detailEntry
			state := "unknown (cached " + e.State + ")"
			if current, ok := m.entry(e.Target); ok && !m.detailInvalid {
				state = current.State
			}
			lines = m.fleetDetailsLines(state, valueOr(selectedHealth, "unknown; target removed"))
			for i := range conversationHeader {
				if m.width > 0 {
					lines[i] = textansi.Truncate(coordClean(lines[i]), m.width, "...")
				}
			}
			selectedLine = -1
			if m.detailInvalid {
				lines = append(lines, "Target changed or removed; actions blocked. Return to Fleet and select again.")
			}
		}
	}
	if m.filtering {
		lines = []string{"Fleet > Host Filter", "Choose scope; selection clears on change"}
		choices := append([]hostrouter.Host{{}}, m.filterChoices...)
		for i, h := range choices {
			label := "All hosts"
			if i > 0 {
				label = h.Name + " (" + valueOr(h.Address, "this machine") + ")"
			}
			prefix := "  "
			if i == m.filterIndex {
				prefix, selectedLine = "> ", len(lines)
			}
			lines = append(lines, prefix+label)
		}
	}
	if m.help {
		selectedLine = -1
		lines = []string{"Keyboard Help", "1 Fleet | 2 Coordination | 3 Servers", "up/down or j/k: select | enter/tab: Details", "r: refresh | ?: help | esc: back/cancel | q: quit", "Fleet: h host filter | Enter Details (never Connect)", "Details: c Connect | u up | s stop | d destroy", "x: selected target/host operations | v: services/about", "Servers: a add | e update | d remove", "Forms: type | tab field | backspace edit | enter save", "Confirmation: y confirm | n or esc cancel", "Busy operations serialize changes; Ctrl+C exits.", "? or esc: return"}
		lines = append(lines, "Coordination: t team | l threads | i inbox | g goals | n create | c compose | p reply/prompt", "m members | s receipt | a authority | o propose JSON | d decision | f follow | ] next page", "Goals list: e next authorized engagement (resets page)", "Private forms: Tab field | Ctrl+V reveal | Ctrl+S preview then submit | Ctrl+R identical retry | Esc draft options")
		if m.workspace == 0 && m.detailEntry != nil && m.detail {
			lines = append(append(m.pageContext("Details Help"), "Target: "+identity(m.detailEntry.Target), "Host destination: "+valueOr(m.detailEntry.Target.Host.Address, "this machine"), "Runtime: "+m.detailEntry.Name, "Connect needs a running VM; Apply and Backup need a stopped VM.", "Observations do not grant action authority; every action revalidates the captured target."), lines[1:]...)
		}
		if m.workspace == 1 && m.coordination.section == "team" && m.coordination.form == nil {
			lines = []string{"Team Help", "Master Team and diagnostics are read-only observations, not VM state or action authority.", "List: up/down or j/k select agent; Enter inspects the captured agent/host/worker/generation.", "Detail: j/k or PgUp/PgDn scroll; Esc returns to the same Team selection and viewport.", "r refresh | f follow/poll | t/l/i/g sections | 1 Fleet | 3 Servers | q quit", "Missing, denied, unsupported and clock-skewed evidence is unknown. STALE is retained evidence, not current work.", "Heartbeat age is not work progress. Report, receipt, work and wait ages are separate.", "Placement changes invalidate reports; return and inspect again. Reads never wake agents or perform recovery.", "? or Esc returns without changing selection or dispatching an action."}
		}
		if m.conversationPage() {
			lines = []string{"Threads Help", "Enter opens a thread with private bodies masked; Ctrl+V explicitly reveals/masks this reader only.", "Up/down or j/k select a stable message. PgUp/PgDn scroll without changing the reply target.", "c composes a root message (no parent); p replies to the selected message; s inspects its receipt.", "Enter marks loaded messages read in this TUI session only; f toggles follow; r refreshes; ] loads next page.", "Esc returns to the retained thread-list viewport. Each newly opened reader starts masked.", "n creates a thread; m manages membership; t Team | l Threads | i Inbox | g Goals.", "a authority | o propose | d decide | 1 Fleet | 3 Servers | q quit", "Private forms: Tab field | Ctrl+V reveal | Ctrl+S preview/submit | Ctrl+R identical retry | Esc draft options", "? or Esc returns without changing selection or revealing private bodies."}
		}
		if m.coordination.form != nil {
			lines = []string{"Composer Help", "Tab/Shift+Tab changes field; arrows/Home/End move the rune cursor; Enter inserts a literal newline.", "Ctrl+V reveals/masks this buffer only. F1 opens this help; Esc returns without changing the buffer.", "Ctrl+S previews, then submits after the complete preview is reviewed with PgDn.", "Esc in the form offers Keep in memory, Discard, or Return. Reopen the same intent to resume.", "Ctrl+C warns before quitting: all active and kept drafts are lost when this process exits.", "Submitted requests freeze. Ctrl+R retries only the identical payload and request ID, never automatically.", "Ctrl+L rechecks original identity without sending or changing the buffer. Changed authority fails closed.", "A receipt reports server-supported delivery state, not proof of execution or wake.", "Creation sends no message and performs no wake. Master, principal, thread, parent and membership stay captured."}
			if kind := m.coordination.form.kind; kind != "create" && kind != "send" && kind != "reply" {
				lines[0] = "Coordination " + kind + " Help"
				lines[len(lines)-1] = "Review the captured request and its consequence. This form never switches master or bypasses principal permissions."
			}
		}
		if m.form != nil {
			lines = []string{"SSH Server Help", "Register an existing SSH reference from ~/.ssh/config; master coordination setup is separate.", "No token entry, provisioning, trust bypass or service startup. Paths and configuration stay on the selected host.", "Tab selects field; type literal text (including ?); Enter saves; Esc cancels. Removal needs full review and explicit consent.", "F1 or Esc returns to the same editor field and viewport without changing input."}
		}
		if m.operations != nil {
			lines = []string{"Operation Help", operationSpecs[m.operations.index].name, operationSpecs[m.operations.index].note, "Paths belong to the captured host. Use configured references, never secret values.", "Tab selects field; type literal text (including ?); Enter reviews/runs; Esc backs out.", "Writes require captured identity, complete confirmation review and fresh backend authorization. No rollback is promised.", "F1 or Esc returns without changing input, focus or viewport."}
		}
	}
	if m.operations != nil && !m.help {
		lines = m.operationLines()
		selectedLine = -1
	}
	if m.runbook {
		lines = append([]string{"Coop build: " + valueOr(m.services.Version, "unknown (embedded caller)")}, serviceRunbook()...)
		selectedLine = -1
	}
	if m.form != nil && !m.help {
		selectedLine = -1
		f := m.form
		lines = append(m.pageContext(strings.ToUpper(f.operation)+" SERVER"), "Name: "+f.name, "SSH reference: "+f.address)
		lines = append(lines, "Existing SSH reference only; not master setup. No provisioning, tokens or trust bypass.")
		if f.operation == "remove" {
			lines = append(lines, "Remove "+f.original.Name+" ("+f.original.Address+")? [y/N; esc cancels]")
		} else {
			lines = append(lines, fmt.Sprintf("Editing %s; tab field / enter save / esc cancel", []string{"name", "SSH reference"}[f.field]))
		}
	}
	if m.pending != nil {
		selectedLine = -1
		impact := "Run the selected operation using this host's configuration and paths?"
		for _, s := range operationSpecs {
			if s.name == m.pending.operation {
				impact = s.note
			}
		}
		if m.pending.operation == "destroy" {
			impact = "Remove this Coop and its persistent state volumes?"
		}
		if m.pending.operation == "enter" {
			impact = "Connect on the foreground terminal? Remote SSH: Ctrl+] detaches; Ctrl+D is application EOF and may end the guest shell. Ctrl+C reaches the foreground application. Local: exit the foreground application to return. Return to the same Details and filter; no automatic reconnect."
		}
		title := "CONFIRM " + strings.ToUpper(m.pending.operation)
		if m.pending.operation == "enter" {
			title = "Connect preflight"
		}
		lines = []string{title, m.pending.context[0], m.pending.context[1], "Target: " + identity(m.pending.entry.Target)}
		for i := range lines {
			if m.width > 0 {
				lines[i] = textansi.Truncate(coordClean(lines[i]), m.width, "...")
			}
		}
		consent := "No automatic retry or rollback. Review every page, then y confirms; n/Esc cancels."
		if m.pending.operation == "enter" {
			consent = "No automatic retry or rollback. Review every page, then Enter continues; Esc cancels."
		}
		lines = append(lines, "Target: "+identity(m.pending.entry.Target), "Name: "+m.pending.entry.Name, "Host destination: "+valueOr(m.pending.entry.Target.Host.Address, "this machine"), "Project: "+valueOr(m.pending.entry.Project, "-"), "Exact "+m.pending.context[0], "Exact "+m.pending.context[1], "Master context is not the lifecycle destination.", impact, consent)
		if m.pending.operation == "enter" {
			lines = append(lines, "VM lifecycle: "+m.pending.entry.State+"; Connect requires running. A stopped VM needs explicit Start first.", "Enter continues only after complete review; Esc returns to Details.")
		}
		if m.pending.directory != "" {
			lines = append(lines, "Captured host directory: "+m.pending.directory)
		}
		if m.pending.args != nil {
			lines = append(lines, "Exact CLI arguments: "+fmt.Sprintf("%q", m.pending.args))
		}
	}
	if m.registryError != "" && !m.help && m.pending == nil && (m.form == nil || m.form.operation != "remove") {
		lines = append(lines, m.registryError)
	}
	if m.status != "" && m.workspace != 1 && !m.help && m.pending == nil && (m.form == nil || m.form.operation != "remove") {
		lines = append(lines, m.status)
	}
	if m.output != "" && m.workspace != 1 && !m.help && m.pending == nil && (m.form == nil || m.form.operation != "remove") {
		lines = append(lines, strings.Split(m.output, "\n")...)
	}
	if m.workspace == 0 && !m.detail && !m.filtering && !m.help && !m.runbook && m.form == nil && m.pending == nil && m.operations == nil && m.coordination.leaving == "" {
		pane, selected := m.fleetPaneLines()
		context := m.pageContext(lines[0])
		if m.selected == (hostrouter.Target{}) {
			context = append(context, "No target selected")
		}
		selectedLine = -1
		if selected >= 0 {
			selectedLine = len(context) + selected
		}
		lines = append(context, pane...)
	}
	footer := "x actions | v services/about | 1/2/3 workspace | r refresh | ? help | q quit"
	if m.workspace == 1 {
		footer = "t/l/i/g sections | r refresh | f follow | ] next | ? help | esc back | 1/3 workspace | q quit"
		if m.conversationPage() {
			footer = "Enter open | n new | ? help | Esc"
			if m.coordination.thread != nil {
				footer = "Esc Threads | p reply | ^V reveal | ?"
				if m.coordination.readerReveal {
					footer = "Esc Threads | p reply | ^V mask | ?"
				}
				if m.coordination.readerBlocked {
					footer = "r refresh authority | ? help | Esc"
				}
			}
		}
		if m.teamList() || m.coordination.teamDetail != nil {
			footer = "Esc Fleet | Enter inspect | r | ?"
			if m.coordination.teamDetail != nil {
				footer = "Esc Team | r refresh | PgDn more | ?"
			}
		}
		if m.coordination.form != nil {
			action := "reveal"
			if m.coordination.form.reveal && !m.coordination.failed && m.services.Coordination != nil && coordSameContext(m.coordination.self, m.coordination.form.self, m.services.Coordination.MasterID(), m.coordination.form.master) {
				action = "mask"
			}
			footer = "Esc draft | ^S preview | ^V " + action
			if m.coordination.form.preview {
				footer = "Esc edit | ^S submit | ^V " + action
				if m.coordination.form.kind == "create" {
					footer = "Esc edit | ^S create | ^V " + action
				}
			}
			if m.coordination.form.frozen {
				footer = "^R identical retry | Esc draft options"
			}
			if m.width <= 0 || m.width >= 65 {
				footer += " | F1 help"
			} else if textansi.StringWidth(footer+" | F1") <= m.width {
				footer += " | F1"
			}
		}
		if m.coordination.receipt != nil {
			footer = "r status | f poll | Esc reader"
		}
		if m.coordination.busy && !m.coordination.backgroundRead {
			footer = "Coordination request in progress; no automatic mutation retries | Ctrl+C quit"
		}
	}
	if m.workspace == 0 {
		footer = "Enter Details | h filter | ? help"
		if m.detail {
			footer = "Esc Fleet | c Connect | x | ?"
		}
		if m.filtering {
			footer = "Enter apply | Esc cancel | arrows select"
		}
	}
	if m.workspace == 2 {
		footer = "a add | e edit | d remove | v | ?"
	}
	if m.workspace == 1 && m.services.Coordination == nil {
		footer = "1 Fleet | 3 Servers | v runbook | ?"
	}
	if m.form != nil {
		footer = "Tab field | Enter save | F1 help | Esc"
	}
	if m.operations != nil {
		footer = "Enter review | Tab field | F1 help | Esc"
	}
	if m.pending != nil || (m.form != nil && m.form.operation == "remove") {
		footer = "Esc cancel | y confirm after review"
		if m.pending != nil && m.pending.operation == "enter" {
			footer = "Esc cancel | Enter after full review"
		}
	}
	if m.busy {
		footer = "Operation in progress | esc cancel if supported | Ctrl+C quit"
	}
	if m.help {
		footer = "F1/Esc return | PgUp/PgDn scroll"
	}
	// Draft protection owns both the visible page and its keys, without changing
	// the underlying workspace or overlay that Return must restore.
	if m.coordination.leaving != "" {
		lines, selectedLine = m.composeLines(), -1
		footer = "k Keep | d Discard | r/Esc Return"
		if m.coordination.leaving == "quit" {
			footer = "d Discard all and quit | r/Esc Return"
		}
	}
	var content []string
	selectedPhysicalLine := -1
	for i, line := range lines {
		if i == selectedLine {
			selectedPhysicalLine = len(content)
		}
		if m.coordination.leaving == "" && (m.form != nil || m.operations != nil || m.coordination.form != nil && !m.coordination.form.preview) {
			if m.width > 0 {
				line = textansi.Hardwrap(line, m.width, true)
			}
		} else {
			line = readingWrap(line, m.width)
		}
		content = append(content, strings.Split(line, "\n")...)
	}
	return content, footer, selectedPhysicalLine
}

func (m fleetModel) pageContext(title string) []string {
	master, principal := "not configured", "unavailable"
	if m.services.Coordination != nil {
		master = m.services.Coordination.MasterID()
		principal = valueOr(m.coordination.self.Principal.ID, "unknown")
		if m.ledgerDiagnostics.master == master {
			principal = valueOr(m.ledgerDiagnostics.self.Principal.ID, principal)
		}
	}
	return []string{title, "Master: " + master, "Principal: " + principal}
}

func (m fleetModel) View() string {
	if m.splitFleet() {
		return m.splitView()
	}
	return m.pageView()
}

func (m fleetModel) pageView() string {
	content, footer, _ := m.content()
	if m.pending != nil && m.pending.reviewedThrough < len(content) {
		footer = "Esc cancel | PgDn review | disabled"
	}
	if m.height > 0 {
		limit := max(1, m.height-2)
		total := len(content)
		if header := m.confirmationHeaderRows(); header > 0 {
			pinned := append([]string(nil), content[:header]...)
			content = content[header:]
			limit = max(1, limit-header)
			start := min(m.scroll, max(0, len(content)-limit))
			content = append(pinned, content[start:min(len(content), start+limit)]...)
		} else if (m.workspace == 0 && !m.detail || m.coordination.leaving != "") && !m.help && !m.runbook && m.form == nil && m.pending == nil && m.operations == nil && len(content) > 0 && m.height >= 4 {
			pinned := content[0]
			content = content[1:]
			limit--
			start := min(m.scroll, max(0, len(content)-limit))
			content = append([]string{pinned}, content[start:min(len(content), start+limit)]...)
		} else if (m.conversationPage() || m.composePage() || m.workspace == 0 && m.detail || m.teamList() || m.coordination.teamDetail != nil && m.workspace == 1) && m.pending == nil && m.operations == nil && m.form == nil && !m.help && !m.runbook && m.height >= conversationHeader+3 && len(content) >= conversationHeader {
			pinned := append([]string(nil), content[:conversationHeader]...)
			content = content[conversationHeader:]
			limit = max(1, limit-conversationHeader)
			start := min(m.scroll, max(0, len(content)-limit))
			content = append(pinned, content[start:min(len(content), start+limit)]...)
		} else {
			start := min(m.scroll, max(0, len(content)-limit))
			content = content[start:min(len(content), start+limit)]
		}
		if total > m.height-2 && !strings.Contains(footer, "PgUp/PgDn") {
			hint := " | PgUp/PgDn scroll"
			if m.width <= 0 || textansi.StringWidth(footer+hint) <= m.width {
				footer += hint
			} else if textansi.StringWidth(footer+" | PgDn") <= m.width {
				footer += " | PgDn"
			}
		}
		for len(content) < m.height-2 {
			content = append(content, "")
		}
	}
	feedback := m.status
	if m.workspace == 1 && !m.help && !m.runbook {
		q := m.coordination
		feedback = q.status
		if q.failed {
			feedback = "STALE/UNKNOWN | " + feedback
		}
		if m.conversationPage() && !q.failed && !q.readerBlocked && q.thread != nil && q.cursor >= 0 && q.cursor < len(q.messages) && !m.conversationSelectionVisible() {
			feedback = "Reply target offscreen: " + q.messages[q.cursor].ID
		}
	}
	if m.workspace == 0 && m.detail && m.pending == nil && m.operations == nil && !m.help && !m.runbook && m.coordination.leaving == "" {
		diagnostic := team.DiagnosticFeedback(time.Now(), m.diagnostics.report, m.diagnostics.failure)
		if m.diagnostics.busy && m.diagnostics.report == nil && m.diagnostics.failure == "" {
			diagnostic = "Loading diagnostics..."
		}
		if feedback == "" {
			feedback = diagnostic
		} else if diagnostic != "" {
			if strings.Contains(diagnostic, "denied") {
				diagnostic = "STALE/UNKNOWN denied"
			} else if strings.Contains(diagnostic, "STALE") {
				diagnostic = "STALE"
			} else if m.diagnostics.busy && m.diagnostics.report == nil {
				diagnostic = "Loading diagnostics"
			}
			// Keep both control and observation outcomes visible without replacing
			// the contextual return/actions row. Full evidence remains in the body.
			feedback += " | " + diagnostic
		}
	}
	if m.workspace == 1 && m.coordination.teamDetail != nil && m.coordination.form == nil && !m.help && !m.runbook && m.coordination.leaving == "" {
		q := m.coordination
		if q.failed {
			feedback = "STALE/UNKNOWN | Team: " + q.status
			if q.status == "forbidden" {
				feedback = "STALE/UNKNOWN | Team access denied"
			}
		} else if diagnostic := team.DiagnosticFeedback(time.Now(), q.teamReport, q.teamFailure); diagnostic != "" {
			feedback = diagnostic
		}
	}
	if m.registryError != "" {
		feedback = m.registryError
	}
	if m.help {
		feedback = ""
	}
	if m.coordination.leaving != "" {
		feedback = "Keep in memory, discard, or return"
		if m.coordination.leaving == "quit" {
			feedback = "Quitting loses all in-memory drafts"
		}
	}
	feedback = strings.Join(strings.Fields(coordClean(feedback)), " ")
	if m.width > 0 {
		feedback = textansi.Truncate(feedback, m.width, "...")
		footer = textansi.Truncate(footer, m.width, "...")
	}
	return strings.Join(append(content, feedback, footer), "\n")
}
