package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

// Older masters and test adapters remain usable without native capabilities.
type workCapabilities interface {
	TaskCapabilities(context.Context) (c.TaskCapabilities, error)
}

type fleetWork struct {
	serial        uint64
	busy          bool
	checked, next time.Time
	master        string
	self          c.SelfIdentity
	capabilities  c.TaskCapabilities
}
type fleetWorkResult struct {
	serial       uint64
	master       string
	self         c.SelfIdentity
	capabilities c.TaskCapabilities
	err          error
}

func (m *fleetModel) refreshWorkCapabilities() tea.Cmd {
	source, ok := m.services.Coordination.(workCapabilities)
	if !ok || m.work.busy || time.Now().Before(m.work.next) {
		return nil
	}
	m.work.serial++
	m.work.busy = true
	m.work.next = time.Now().Add(30 * time.Second)
	serial, master, coord := m.work.serial, m.services.Coordination.MasterID(), m.services.Coordination
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		result := fleetWorkResult{serial: serial, master: master}
		result.self, result.err = coord.Self(ctx)
		if result.err == nil {
			result.capabilities, result.err = source.TaskCapabilities(ctx)
		}
		return result
	}
}

func (m fleetModel) canonicalComms() bool {
	w := m.work
	source := w.capabilities.Conversation
	if m.services.Coordination == nil || w.master != m.services.Coordination.MasterID() || w.checked.IsZero() || time.Since(w.checked) < 0 || time.Since(w.checked) >= c.PresenceStale || source == nil || source.Provider != "kata" || source.InstanceUID == "" || source.ProjectUID == "" || source.ProjectUID != w.capabilities.ProjectUID || source.IssueUID == "" {
		return false
	}
	if m.coordination.checked.After(w.checked) && m.coordination.failed || m.coordination.seen.After(w.checked) && !ledgerSameContext(m.coordination.self, w.self) {
		return false
	}
	return true
}

// The launcher resolves its own configured master. Bird selection supplies only
// a current verified canonical ticket reference, never a destination host.
func (m fleetModel) openWorkUI(kind string) (tea.Model, tea.Cmd) {
	if m.services.Foreground == nil {
		m.status = "Native work tools unavailable; configure the foreground launcher"
		return m, nil
	}
	args := []string{kind}
	if kind == "task" {
		p := m.previewDetails()
		if p.detailEntry != nil && !p.detailInvalid {
			activity, _ := diagnosticSummary(time.Now(), p.diagnostics.report, p.diagnostics.failure)
			if !m.detail {
				activity, _ = m.ledgerSummary(*p.detailEntry)
			}
			r := p.diagnostics.report
			if activity != "unknown" && activity != "stale" && r != nil {
				at := r.Report.Work.ObservedAt
				ref := r.Report.Work.TaskURL
				if _, _, _, err := c.ParseKataTaskReference(ref); err == nil && !at.IsZero() && !at.After(time.Now()) && time.Since(at) < c.PresenceStale {
					args = append(args, ref)
				}
			}
		}
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, m.actionCancel = context.WithCancel(ctx)
	m.actionSequence++
	sequence := m.actionSequence
	target := hostrouter.Target{}
	m.active, m.busy, m.activeTarget = true, true, target
	command, err := m.services.Foreground(ctx, hostrouter.ActionRequest{Operation: "work-ui", Args: args})
	done := func(err error) tea.Msg {
		return fleetActionDone{target: target, operation: kind, request: sequence, err: err}
	}
	if err != nil {
		return m, func() tea.Msg { return done(err) }
	}
	m.status = "Opening native " + kind
	return m, tea.Exec(&foregroundCommand{command: command}, done)
}

func (m fleetModel) directAttach() (tea.Model, tea.Cmd) {
	e, ok := m.entry(m.selected)
	if !ok {
		m.status = "No current target selected; refresh and select a bird"
		return m, nil
	}

	if m.detailEntry != nil && m.detailEntry.Target == e.Target && (m.detailInvalid || !sameBinding(m.detailEntry.Diagnostics, e.Diagnostics) || m.detailEntry.Name != e.Name || m.detailEntry.Project != e.Project) {
		m.status = "Target placement changed; refresh and select again"
		return m, nil
	}
	if e.State != "running" {
		m.status = "Selected VM is not running; use Tab then actions to start it"
		return m, nil
	}
	return m.dispatch(pendingAction{entry: e, operation: "enter"})
}
