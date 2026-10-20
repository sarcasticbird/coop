package team

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (m *Model) closeDetail() {
	if m.detailCancel != nil {
		m.detailCancel()
		m.detailCancel = nil
	}
	m.detailID++
	m.detailOpen, m.detailBusy = false, false
	m.detail, m.detailFailure, m.offset = nil, "", 0
}

func (m *Model) openDetail() tea.Cmd {
	if len(m.rows) == 0 {
		return nil
	}
	m.detailOpen, m.detailAgent, m.offset = true, m.rows[m.selected].AgentID, 0
	return m.refreshDetail()
}

func (m *Model) refreshDetail() tea.Cmd {
	if m.detailBusy || m.closed {
		return nil
	}
	source, ok := m.source.(DiagnosticSource)
	if !ok {
		m.detailFailure = "diagnostics unsupported by source"
		return nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, requestTimeout)
	m.detailCancel = cancel
	m.detailID++
	id, agent := m.detailID, m.detailAgent
	m.detailBusy = true
	return func() tea.Msg {
		defer cancel()
		v, err := source.AgentDiagnostics(ctx, agent)
		return diagnosticResultMsg{id: id, value: v, err: err, at: time.Now()}
	}
}

func diagnosticTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format(time.RFC3339)
}

func (m *Model) detailLines() []string {
	var text strings.Builder
	fmt.Fprintf(&text, "Agent: %s\n", clean(m.detailAgent))
	if m.detailBusy && m.detail == nil {
		text.WriteString("Refreshing diagnostics...\n")
	}
	if m.detailFailure != "" {
		if m.detail != nil {
			text.WriteString("STALE retained snapshot | ")
		}
		fmt.Fprintln(&text, m.detailFailure)
	}
	if m.detail == nil {
		text.WriteString("No successful diagnostic snapshot yet.\n")
		return strings.Split(ansi.Hardwrap(text.String(), max(1, m.width), true), "\n")
	}
	v := m.detail
	d := v.Report
	freshness := "fresh receipt"
	if v.Stale || m.now.Sub(v.ReceivedAt) >= c.PresenceStale {
		freshness = "STALE receipt"
	}
	fmt.Fprintf(&text, "%s | received: %s\nObserved: %s | worker started: %s\n", freshness, diagnosticTime(v.ReceivedAt), diagnosticTime(d.ObservedAt), diagnosticTime(d.StartedAt))
	fmt.Fprintf(&text, "Handling: %s | reason: %s\nStage: %s | phase: %s\n", clean(d.Wait.Handling), clean(d.Wait.Reason), clean(d.Work.Stage), clean(d.Work.Phase))
	fmt.Fprintf(&text, "Task: %s\nPR: %s\nHead: %s\nJob: %s\nSession: %s | submission: %s\n", clean(d.Work.TaskURL), clean(d.Work.PRURL), clean(d.Work.HeadSHA), clean(d.Work.JobID), clean(d.Work.SessionID), clean(d.Work.SubmissionState))
	fmt.Fprintf(&text, "Retry: %s | deadline: %s\nDependencies: %s\n", diagnosticTime(d.Wait.NextRetry), diagnosticTime(d.Wait.Deadline), clean(strings.Join(d.Wait.Dependencies, ", ")))
	for _, item := range []struct {
		name  string
		value c.DiagnosticService
	}{{"GitHub", d.GitHub}, {"OpenCode", d.OpenCode}} {
		s := item.value
		state := "unknown"
		if !s.ObservedAt.IsZero() {
			state = "last contact failed"
			if s.Available {
				state = "last contact succeeded"
			}
		}
		fmt.Fprintf(&text, "%s: %s | origin: %s | category: %s\n  Observed: %s | last success: %s\n  Requests since start: %d | last duration: %dms | retry: %s\n", item.name, state, clean(s.Origin), clean(s.Category), diagnosticTime(s.ObservedAt), diagnosticTime(s.LastSuccess), s.Requests, s.DurationMS, diagnosticTime(s.RetryAt))
		if item.name == "GitHub" {
			fmt.Fprintf(&text, "  Last complete catalog: %s\n", diagnosticTime(s.LastRead))
		}
	}
	for _, model := range []struct {
		name  string
		value *c.DiagnosticModel
	}{{"Submitted model", d.Work.SubmittedModel}, {"Executed model", d.Work.ExecutedModel}} {
		value := "unknown"
		if model.value != nil {
			value = model.value.Provider + "/" + model.value.Model + " " + model.value.Variant
		}
		fmt.Fprintf(&text, "%s: %s\n", model.name, clean(value))
	}
	text.WriteString("Recent transitions since worker start:\n")
	for _, t := range d.Transitions {
		fmt.Fprintf(&text, "  %s %s/%s %s %s:%s\n", diagnosticTime(t.At), clean(t.Stage), clean(t.Phase), clean(t.Reason), clean(t.Origin), clean(t.Category))
	}
	text.WriteString("Heartbeat and successful contacts do not prove model or VM health.\n")
	return strings.Split(ansi.Hardwrap(text.String(), max(1, m.width), true), "\n")
}
