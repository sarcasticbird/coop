package tui

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	textansi "github.com/charmbracelet/x/ansi"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/runtime"
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
	Inventory Inventory
	Registry  Registry
	Actions   hostrouter.ActionClient
	Terminal  hostrouter.TerminalClient
}

type hostState struct {
	host      hostrouter.Host
	entries   []hostrouter.InventoryEntry
	updated   time.Time
	errorCode string
	loading   bool
}

type serverForm struct {
	operation     string
	original      hostrouter.Host
	name, address string
	field         int
}

type pendingAction struct {
	entry     hostrouter.InventoryEntry
	operation string
}

type fleetModel struct {
	services              Services
	ctx                   context.Context
	hosts                 []hostState
	selected              hostrouter.Target
	server                hostrouter.Host
	workspace             int
	detail, help          bool
	width, height         int
	epoch                 uint64
	stream                <-chan hostrouter.HostInventory
	cancel                context.CancelFunc
	refreshing, busy      bool
	registryError, status string
	form                  *serverForm
	pending               *pendingAction
	result                Result
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
}
type registryDone struct{ err error }

func runFleet(rt runtime.Runtime) (Result, error) {
	r, err := hostrouter.DefaultRegistry()
	if err != nil {
		return Result{}, err
	}
	return runServices(Services{
		Registry:  r,
		Inventory: hostrouter.Router{Local: runtimeInventory{rt}, Remote: hostrouter.SSH{}},
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

type runtimeInventory struct{ rt runtime.Runtime }

func (r runtimeInventory) Inventory(ctx context.Context, h hostrouter.Host) ([]hostrouter.InventoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	infos, err := r.rt.Containers()
	if err != nil {
		return nil, err
	}
	rows := make([]hostrouter.InventoryEntry, 0, len(infos))
	for _, info := range infos {
		t := hostrouter.Target{Host: h, Kind: hostrouter.Interactive, ID: info.Name}
		if info.Mode == "agent" {
			t.Kind, t.ID = hostrouter.Agent, info.Profile
		}
		rows = append(rows, hostrouter.InventoryEntry{Target: t, Name: info.Name, State: info.State, Project: info.ProjectMount()})
	}
	return rows, nil
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
		rows = append(rows, h.entries...)
	}
	return rows
}

func (m fleetModel) entry(t hostrouter.Target) (hostrouter.InventoryEntry, bool) {
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
	found := false
	for _, e := range m.rows() {
		if e.Target == m.selected {
			found = true
		}
	}
	if !found {
		m.selected = hostrouter.Target{}
		if rows := m.rows(); len(rows) > 0 {
			m.selected = rows[0].Target
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

func (m fleetModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case fleetRefresh:
		cmd := m.refresh()
		return m, tea.Batch(cmd, fleetClock())
	case fleetTick:
		cmd := m.refresh()
		return m, tea.Batch(cmd, fleetClock())
	case registryLoaded:
		if msg.epoch != m.epoch {
			return m, nil
		}
		m.registryError = ""
		if msg.err != nil {
			m.registryError = "registry unavailable: " + msg.err.Error()
			msg.hosts = nil
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
			return m, nil
		}
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
		return m, nextHost(m.stream, m.epoch)
	case registryDone:
		m.busy = false
		if msg.err != nil {
			m.status = "server change failed: " + msg.err.Error()
			return m, nil
		}
		m.form = nil
		m.status = "server references updated"
		m.refreshing = false
		return m, m.refresh()
	case fleetActionDone:
		m.busy = false
		m.status = identity(msg.target) + " " + msg.operation + " complete"
		if msg.err != nil {
			m.status = identity(msg.target) + " " + msg.operation + " failed: " + msg.err.Error()
		}
		m.refreshing = false
		return m, m.refresh()
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		return m.keyFleet(msg)
	}
	return m, nil
}

func identity(t hostrouter.Target) string { return t.Host.Name + " / " + string(t.Kind) + " / " + t.ID }

func (m fleetModel) keyFleet(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if key == "ctrl+c" {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}
	if m.busy {
		return m, nil
	}
	if m.help {
		if key == "?" || key == "esc" {
			m.help = false
		}
		return m, nil
	}
	if m.pending != nil {
		p := *m.pending
		if key == "esc" || key == "n" {
			m.pending = nil
			m.status = "action cancelled"
		}
		if key == "y" {
			m.pending = nil
			return m.dispatch(p)
		}
		return m, nil
	}
	if m.form != nil {
		return m.keyForm(k)
	}
	switch key {
	case "q":
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case "?":
		m.help = true
	case "1":
		m.workspace = 0
	case "2":
		m.workspace = 1
	case "3":
		m.workspace = 2
	case "tab", "shift+tab":
		m.detail = !m.detail
	case "esc":
		m.detail = false
		m.status = ""
	case "r":
		return m, m.refresh()
	case "up", "k", "down", "j":
		delta := 1
		if key == "up" || key == "k" {
			delta = -1
		}
		if m.workspace == 0 {
			rows := m.rows()
			for i, e := range rows {
				if e.Target == m.selected {
					m.selected = rows[max(0, min(len(rows)-1, i+delta))].Target
					break
				}
			}
		} else if m.workspace == 2 {
			for i, h := range m.hosts {
				if h.host == m.server {
					m.server = m.hosts[max(0, min(len(m.hosts)-1, i+delta))].host
					break
				}
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
			if key != "a" {
				m.form.name, m.form.address = m.server.Name, m.server.Address
				m.form.field = 1
			}
			return m, nil
		}
		if key == "d" && m.workspace == 0 {
			if e, ok := m.entry(m.selected); ok {
				m.pending = &pendingAction{e, "destroy"}
			}
		}
	case "u", "s", "enter":
		if m.workspace != 0 {
			return m, nil
		}
		e, ok := m.entry(m.selected)
		if !ok {
			m.status = "selected host inventory is not authoritative; refresh"
			return m, nil
		}
		op := map[string]string{"u": "up", "s": "down", "enter": "enter"}[key]
		if (op == "enter" || op == "down") && e.State != "running" {
			m.status = identity(e.Target) + " is not running"
			return m, nil
		}
		return m.dispatch(pendingAction{e, op})
	}
	return m, nil
}

func (m fleetModel) keyForm(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := *m.form
	m.form = &f
	switch k.String() {
	case "esc":
		m.form = nil
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
			return m.saveServer(f)
		}
		fallthrough
	default:
		if f.operation != "remove" && k.Type == tea.KeyRunes {
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
	e, ok := m.entry(p.entry.Target)
	if !ok || e.Name != p.entry.Name || e.Project != p.entry.Project {
		m.status = identity(p.entry.Target) + " changed or became stale; action cancelled"
		return m, nil
	}
	if _, err := m.services.Registry.ResolveTarget(e.Target); err != nil {
		m.status = err.Error()
		return m, nil
	}
	if p.operation == "enter" {
		if m.services.Terminal != nil {
			m.status = "terminal attachment is provided by the action workflow slice"
			return m, nil
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
	services, rt := m.services, m.legacy
	return m, func() tea.Msg {
		if _, err := services.Registry.ResolveTarget(e.Target); err != nil {
			return fleetActionDone{e.Target, p.operation, err}
		}
		var err error
		if services.Actions != nil {
			result, actionErr := services.Actions.Execute(context.Background(), hostrouter.ActionRequest{Target: e.Target, Operation: p.operation, RequestID: fmt.Sprintf("tui-%d", time.Now().UnixNano())}, nil, io.Discard, io.Discard)
			err = actionErr
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
		return fleetActionDone{e.Target, p.operation, err}
	}
}

func (m fleetModel) View() string {
	var lines []string
	lines = append(lines, "COOP   [1 Fleet]   [2 Coordination]   [3 Servers]", "")
	if m.workspace == 1 {
		lines = append(lines, "Coordination workflows are a separate implementation slice.")
	} else {
		if m.workspace == 0 {
			lines = append(lines, "HOST / KIND / TARGET   STATE")
		} else {
			lines = append(lines, "HOST   CONFIGURED SSH REFERENCE")
		}
		var body []string
		selectedLine := 0
		for _, h := range m.hosts {
			state := "loading"
			if !h.updated.IsZero() {
				state = "ok; snapshot " + time.Since(h.updated).Round(time.Second).String() + " old"
			}
			if h.errorCode != "" {
				state = h.errorCode
				if !h.updated.IsZero() {
					state += "; stale snapshot " + time.Since(h.updated).Round(time.Second).String() + " old"
				}
			}
			if h.loading {
				state += "; refreshing"
			}
			prefix := "  "
			if m.workspace == 2 && h.host == m.server {
				prefix = "> "
				selectedLine = len(body)
			}
			body = append(body, prefix+h.host.Name+" ("+valueOr(h.host.Address, "this machine")+") ["+state+"]")
			if m.workspace != 0 {
				continue
			}
			if len(h.entries) == 0 && !h.loading && h.errorCode == "" {
				body = append(body, "  (no Coops)")
			}
			for _, e := range h.entries {
				prefix = "  "
				if e.Target == m.selected {
					prefix = "> "
					selectedLine = len(body)
				}
				body = append(body, prefix+identity(e.Target)+"   "+e.State)
			}
		}
		limit := len(body)
		if m.height > 0 {
			limit = max(1, m.height-14)
		}
		start := max(0, selectedLine-limit+1)
		end := min(len(body), start+limit)
		lines = append(lines, body[start:end]...)
		if end < len(body) {
			lines = append(lines, "... more below")
		}
		if m.workspace == 0 {
			if e, ok := m.entry(m.selected); ok && (m.detail || m.width >= 100) {
				lines = append(lines, "", "Selected: "+identity(e.Target), "Name: "+e.Name, "Project: "+valueOr(e.Project, "-"))
			}
		}
	}
	if m.help {
		lines = []string{"Keyboard Help", "1 Fleet | 2 Coordination | 3 Servers", "up/down or j/k: select | tab: focus detail", "r: refresh | ?: help | esc: back/cancel | q: quit", "Fleet: u up | s stop | d destroy | enter terminal", "Servers: a add | e update | d remove", "Forms: type | tab field | backspace edit | enter save", "Confirmation: y confirm | n or esc cancel", "Busy operations serialize changes; Ctrl+C exits.", "? or esc: return"}
	}
	if m.form != nil {
		f := m.form
		lines = append(lines, "", strings.ToUpper(f.operation)+" SERVER", "Name: "+f.name, "SSH reference: "+f.address)
		if f.operation == "remove" {
			lines = append(lines, "Remove "+f.original.Name+" ("+f.original.Address+")? [y/N; esc cancels]")
		} else {
			lines = append(lines, fmt.Sprintf("Editing %s; tab field / enter save / esc cancel", []string{"name", "SSH reference"}[f.field]))
		}
	}
	if m.pending != nil {
		lines = append(lines, "", "Destroy "+identity(m.pending.entry.Target)+" ("+valueOr(m.pending.entry.Target.Host.Address, "this machine")+") and its state volumes? [y/N]")
	}
	if m.registryError != "" {
		lines = append(lines, m.registryError)
	}
	if m.status != "" {
		lines = append(lines, m.status)
	}
	lines = append(lines, "", "1/2/3 workspace | tab focus | r refresh | ? help | q quit")
	if m.width > 0 {
		for i, line := range lines {
			lines[i] = textansi.Hardwrap(line, m.width, true)
		}
	}
	return strings.Join(lines, "\n")
}
