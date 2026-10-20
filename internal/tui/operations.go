package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

type operationSpec struct {
	name, scope, state, note string
	fields                   []string
	confirm, foreground      bool
}

// This is deliberately a closed menu, not a shell/argv editor. CLI handlers
// remain the authority for ownership, permissions, locks and prerequisites.
var operationSpecs = []operationSpec{
	{name: "enter", scope: "target", state: "running", note: "Attach on a real terminal; return to Details", confirm: true, foreground: true},
	{name: "status", scope: "target", note: "Read selected Coop status"},
	{name: "up", scope: "target", note: "Start selected Coop; may acquire configured credentials", confirm: true, foreground: true},
	{name: "down", scope: "target", state: "running", note: "Stop selected Coop; persistent state remains", confirm: true},
	{name: "destroy", scope: "target", note: "Remove Coop AND persistent state volumes", confirm: true},
	{name: "rebuild", scope: "target", note: "Build desired image; does not replace existing guest", confirm: true},
	{name: "apply", scope: "target", state: "stopped", note: "Apply maintenance to stopped owned guest; recovery archive may be created", confirm: true},
	{name: "sync", scope: "agent", note: "Clone missing repositories; may start guest and acquire credentials", confirm: true, foreground: true},
	{name: "backup", scope: "target", state: "stopped", note: "Private but UNENCRYPTED archive includes guest secrets; host path only", fields: []string{"Archive destination on selected host"}, confirm: true},
	{name: "credentials refresh", scope: "agent", note: "Configured grants only; may start guest; never enter secret values", fields: []string{"Configured grant names (optional comma-separated references)"}, confirm: true, foreground: true},
	{name: "credentials watch", scope: "agent", state: "running", note: "Foreground watcher; Ctrl+C stops watcher, not the guest", confirm: true, foreground: true},
	{name: "worker status", scope: "agent", state: "running", note: "Read configured worker state; never starts guest"},
	{name: "worker requests", scope: "agent", state: "running", note: "Read worker request list"},
	{name: "worker pause", scope: "agent", state: "running", note: "Pause worker through existing ownership safeguards", confirm: true},
	{name: "worker resume", scope: "agent", state: "running", note: "Resume configured worker", confirm: true},
	{name: "ping", scope: "agent", note: "Queue a literal owner prompt; acceptance is not a reply or goal approval", fields: []string{"Prompt (up to 8192 bytes)"}, confirm: true},
	{name: "doctor", scope: "host", note: "Read host diagnostics; warnings and failures retain CLI labels"},
	{name: "upgrade", scope: "host", note: "HOST-WIDE locked core upgrade; existing guests become stale", confirm: true},
	{name: "init", scope: "host", note: "Interactive bootstrap; existing CLI asks before writing project config", fields: []string{"Absolute project directory on selected host"}, confirm: true, foreground: true},
	{name: "restore", scope: "host", note: "Destination must be ABSENT; configure profile first; paths belong to this host", fields: []string{"Agent profile (blank for project)", "Absolute project directory (blank for agent)", "Archive on selected host"}, confirm: true},
	{name: "mcp register", scope: "host", note: "Record ownership of an existing Coop without booting it", fields: []string{"Absolute private MCP config on selected host", "Absolute canonical project on selected host"}, confirm: true},
	{name: "mcp inspect", scope: "host", note: "Read structured safe operation metadata", fields: []string{"Absolute private MCP config on selected host", "Operation ID"}},
}

type operationForm struct {
	entry        hostrouter.InventoryEntry
	index, field int
	editing      bool
	values       []string
	requestID    string
}

type operationStarted struct {
	request   uint64
	target    hostrouter.Target
	operation string
	run       tea.Cmd
}

func queueOperation(request uint64, target hostrouter.Target, operation string, run tea.Cmd) tea.Cmd {
	return func() tea.Msg { return operationStarted{request, target, operation, run} }
}

func (m fleetModel) operationReason(s operationSpec, e hostrouter.InventoryEntry) string {
	if s.scope == "host" && e.Target.Kind != "" {
		return "host-wide: open actions in Servers workspace"
	}
	if s.scope != "host" && e.Target.Kind == "" {
		return "select a Coop in Fleet workspace"
	}
	if s.scope == "agent" && e.Target.Kind != hostrouter.Agent {
		return "requires a persistent agent"
	}
	if s.state != "" && s.state != e.State {
		return "requires " + s.state + " state"
	}
	if e.State == "absent" && (s.name == "destroy" || s.name == "down" || s.name == "apply" || s.name == "backup") {
		return "requires an existing Coop"
	}
	if e.Target.Kind != "" {
		if _, ok := m.entry(e.Target); !ok {
			return "inventory stale or disconnected; refresh"
		}
		if e.Target.Kind == hostrouter.Interactive && e.Project == "" {
			return "project path unavailable"
		}
	}
	if s.foreground && m.services.Foreground == nil {
		return "foreground CLI adapter unavailable"
	}
	if !s.foreground && m.services.Actions == nil {
		return "action backend unavailable"
	}
	return ""
}

func (m fleetModel) openOperations() (tea.Model, tea.Cmd) {
	var e hostrouter.InventoryEntry
	switch m.workspace {
	case 0:
		var ok bool
		e, ok = m.entry(m.selected)
		if !ok {
			m.status = "selected host inventory is not authoritative; refresh"
			return m, nil
		}
	case 2:
		e.Target.Host = m.server
	default:
		return m, nil
	}
	m.operations = &operationForm{entry: e}
	m.scroll, m.output, m.status = 0, "", ""
	return m, nil
}

func (m fleetModel) keyOperation(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := *m.operations
	f.values = append([]string(nil), f.values...)
	m.operations = &f
	s := operationSpecs[f.index]
	switch k.String() {
	case "esc":
		if f.editing {
			f.editing = false
		} else {
			m.operations = nil
		}
		m.status = "operation cancelled"
	case "up", "k", "down", "j":
		if !f.editing {
			delta := 1
			if k.String() == "up" || k.String() == "k" {
				delta = -1
			}
			f.index = max(0, min(len(operationSpecs)-1, f.index+delta))
			m.scroll = max(0, f.index-2)
			m.status = ""
			return m, nil
		}
		fallthrough
	default:
		if f.editing && (k.Type == tea.KeyRunes || k.Type == tea.KeySpace) && len(f.values) > 0 {
			text := string(k.Runes)
			if k.Type == tea.KeySpace {
				text = " "
			}
			if strings.IndexFunc(text, unicode.IsControl) >= 0 {
				m.status = "single-line text only; never enter secret values"
				return m, nil
			}
			if len(f.values[f.field])+len(text) <= 8192 {
				f.values[f.field] += text
			}
		}
	case "tab", "shift+tab":
		if f.editing && len(f.values) > 0 {
			f.field = (f.field + 1) % len(f.values)
		}
	case "backspace", "ctrl+h":
		if f.editing && len(f.values) > 0 {
			r := []rune(f.values[f.field])
			if len(r) > 0 {
				f.values[f.field] = string(r[:len(r)-1])
			}
		}
	case "enter":
		if reason := m.operationReason(s, f.entry); reason != "" {
			m.status = reason
			return m, nil
		}
		if !f.editing {
			f.editing, f.values, f.field = true, make([]string, len(s.fields)), 0
			if s.name == "ping" {
				b := make([]byte, 32)
				if _, err := rand.Read(b); err != nil {
					m.status = "request ID unavailable"
					return m, nil
				}
				f.requestID = hex.EncodeToString(b)
			}
			m.scroll = 0
			return m, nil
		}
		p, err := f.action()
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.operations = nil
		if s.confirm {
			m.pending = &p
			m.scroll = 0
			return m, nil
		}
		return m.dispatchOperation(p)
	}
	return m, nil
}

func (f operationForm) action() (pendingAction, error) {
	s := operationSpecs[f.index]
	p := pendingAction{entry: f.entry, operation: s.name, foreground: s.foreground, args: strings.Fields(s.name), directory: f.entry.Project}
	if f.entry.Target.Kind == hostrouter.Agent {
		p.directory = ""
	}
	for i, v := range f.values {
		optional := s.name == "restore" && i < 2 || s.name == "credentials refresh"
		if !optional && strings.TrimSpace(v) == "" {
			return p, fmt.Errorf("%s is required", s.fields[i])
		}
	}
	switch s.name {
	case "enter":
		p.args = []string{"--"}
	case "backup":
		p.args = append(p.args, "--", f.values[0])
	case "restore":
		if (f.values[0] == "") == (f.values[1] == "") {
			return p, errors.New("choose exactly one agent profile or project directory")
		}
		if f.values[0] != "" {
			p.args = append([]string{"--agent", f.values[0]}, p.args...)
		} else {
			p.directory = f.values[1]
		}
		p.args = append(p.args, "--", f.values[2])
	case "init":
		p.directory = f.values[0]
	case "credentials refresh":
		if f.values[0] != "" {
			p.args = append([]string{"--credentials=" + f.values[0]}, p.args...)
		}
	case "credentials watch":
		p.args = append(p.args, "--profile="+f.entry.Target.ID)
	case "ping":
		p.args = append(p.args, "--prompt="+f.values[0], "--request-id="+f.requestID)
	case "mcp register":
		p.args = append(p.args, "--config="+f.values[0], "--project="+f.values[1])
	case "mcp inspect":
		p.args = append(p.args, "--config="+f.values[0], "--operation="+f.values[1])
	}
	if p.directory != "" && !strings.HasPrefix(p.directory, "/") {
		return p, errors.New("project directory must be absolute on the selected host")
	}
	if f.entry.Target.Kind == hostrouter.Agent && s.name != "credentials watch" {
		p.args = append([]string{"--agent", f.entry.Target.ID}, p.args...)
	}
	return p, nil
}

func (m fleetModel) operationLines() []string {
	f := m.operations
	lines := []string{"OPERATIONS: " + identity(f.entry.Target), "Destination: " + valueOr(f.entry.Target.Host.Address, "this machine"), "Paths and configuration belong to this host; never enter secret values.", ""}
	if f.editing {
		s := operationSpecs[f.index]
		lines = append(lines, strings.ToUpper(s.name), s.note)
		for i, label := range s.fields {
			prefix := "  "
			if i == f.field {
				prefix = "> "
			}
			lines = append(lines, prefix+label+": "+f.values[i])
		}
		if f.requestID != "" {
			lines = append(lines, "Idempotency request: "+f.requestID)
		}
		return append(lines, "enter: review/run | tab: field | esc: back")
	}
	for i, s := range operationSpecs {
		prefix := "  "
		if i == f.index {
			prefix = "> "
		}
		line := prefix + s.name
		if reason := m.operationReason(s, f.entry); reason != "" {
			line += " [disabled: " + reason + "]"
		}
		lines = append(lines, line)
	}
	return append(lines, "up/down: select | enter: open | esc: back")
}

// Truncation consumes all writes without retaining unbounded command output.
type boundedOutput struct{ text strings.Builder }

func (b *boundedOutput) Write(p []byte) (int, error) {
	remaining := (32 << 10) - b.text.Len()
	if remaining > 0 {
		b.text.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}
func (b *boundedOutput) String() string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, b.text.String())
}

func (m fleetModel) dispatchOperation(p pendingAction) (tea.Model, tea.Cmd) {
	if p.entry.Target.Kind != "" {
		e, ok := m.entry(p.entry.Target)
		if !ok || e.Project != p.entry.Project || e.Name != p.entry.Name {
			m.status = "captured identity changed or disconnected; refresh"
			return m, nil
		}
		for _, s := range operationSpecs {
			if s.name == p.operation {
				if reason := m.operationReason(s, e); reason != "" {
					m.status = reason
					return m, nil
				}
			}
		}
	}
	if err := resolveOperation(m.services.Registry, p.entry.Target); err != nil {
		m.status = "host registration changed; refresh before retry"
		return m, nil
	}
	if p.args == nil {
		f := operationForm{entry: p.entry}
		for i, s := range operationSpecs {
			if s.name == p.operation {
				f.index = i
				break
			}
		}
		var err error
		p, err = f.action()
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
	}
	if p.foreground && m.services.Foreground == nil || !p.foreground && m.services.Actions == nil {
		m.status = "selected operation backend unavailable"
		return m, nil
	}
	m.actionSequence++
	sequence := m.actionSequence
	m.active, m.activeTarget, m.busy = true, p.entry.Target, true
	m.epoch++
	m.refreshing, m.output = false, ""
	if m.cancel != nil {
		m.cancel()
	}
	for i := range m.hosts {
		if m.hosts[i].host == p.entry.Target.Host {
			m.hosts[i].errorCode = "action_pending"
		}
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if p.foreground {
		ctx, m.actionCancel = context.WithCancel(ctx)
	} else {
		ctx, m.actionCancel = context.WithTimeout(ctx, 10*time.Minute)
	}
	req := hostrouter.ActionRequest{Target: p.entry.Target, Operation: "command", Directory: p.directory, Args: p.args}
	// watch must not pass --agent: the legacy CLI intentionally treats that as
	// single-agent refresh. Its explicit --profile still pins the captured agent.
	if p.operation == "credentials watch" {
		req.Target.Kind, req.Target.ID = "", ""
	}
	m.status = identity(p.entry.Target) + " " + p.operation + " queued; esc cancels; no automatic retries"
	done := func(err error) tea.Msg {
		return fleetActionDone{target: p.entry.Target, operation: p.operation, err: err, request: sequence}
	}
	if p.foreground {
		command, err := m.services.Foreground(ctx, req)
		if err != nil {
			return m, func() tea.Msg { return done(err) }
		}
		return m, queueOperation(sequence, p.entry.Target, p.operation, tea.Exec(&foregroundCommand{command: command}, done))
	}
	services := m.services
	run := func() tea.Msg {
		if err := resolveOperation(services.Registry, req.Target); err != nil {
			return done(errors.New("host registration changed; cancelled"))
		}
		var output boundedOutput
		var input io.Reader
		if p.operation == "destroy" {
			input = strings.NewReader("y\n")
		}
		result, err := services.Actions.Execute(ctx, req, input, &output, io.Discard)
		if result.Target != req.Target {
			return done(errors.New("mismatched action response; outcome unknown"))
		}
		if err == nil && result.ExitCode != 0 {
			err = fmt.Errorf("command failed (status %d)", result.ExitCode)
		}
		return fleetActionDone{target: p.entry.Target, operation: p.operation, request: sequence, err: err, output: output.String()}
	}
	return m, queueOperation(sequence, p.entry.Target, p.operation, run)
}

func resolveOperation(r Registry, t hostrouter.Target) error {
	if t.Kind != "" {
		_, err := r.ResolveTarget(t)
		return err
	}
	if t.Host == hostrouter.Local {
		return nil
	}
	hosts, err := r.List()
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if h == t.Host {
			return nil
		}
	}
	return errors.New("host registration changed")
}

func serviceRunbook() []string {
	return []string{"ADVANCED / SERVICES / ABOUT", "These commands are intentionally operator-managed, never daemonized by this UI.",
		"Run on the exact intended host using private host configuration:",
		"coop mcp serve --config /private/server.toml: foreground service; use the configured service manager.",
		"coop mcp stdio --config /private/server.toml: foreground MCP client transport, not a browser action.",
		"coop master serve --config /private/master.toml: foreground master service; preserve deployment policy.",
		"coop credentials watch --profile NAME: supported foreground action in the agent menu; Ctrl+C ends watch.",
		"Master recovery/offline administration: master backup/restore, reconcile status/pause/resume/evidence/resolve, placement set, history import/list, cutover remain explicit operator CLI runbooks (docs/master.md). Never run recovery against a live service from this UI.",
		"Coordination (2): team, inbox, threads, private compose/reply, receipts and exact-revision goal proposal/authority/owner decisions. Presence and receipts never imply goal approval.",
		"Legacy/profile approve remains an explicit CLI action (coop --agent NAME approve GOAL_URL); it is not a native goal decision.",
		"Fleet inventory represents ls, inventory and status; Servers represents server list/add/update/remove.",
		"Bootstrap: Servers > x > init invokes the existing interactive CLI on an explicit host project directory.",
		"Help: ? for keyboard help; coop --help for CLI flags; coop --version for installed build identity.",
		"Completion: coop completion bash|zsh|fish|powershell is shell integration, not a TUI action.",
		"esc: return. Operator-assisted live fleet UAT remains pending; no production restart or rollout is implied."}
}
