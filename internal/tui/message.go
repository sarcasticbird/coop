package tui

import (
	"context"
	"crypto/rand"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
)

type messageDraft struct {
	entry                    hostrouter.InventoryEntry
	binding                  c.DiagnosticBinding
	self                     c.SelfIdentity
	thread                   c.Thread
	requestID                string
	body                     []rune
	cursor                   int
	loading, sending, frozen bool
}
type messageReady struct {
	requestID string
	self      c.SelfIdentity
	thread    c.Thread
	err       error
}
type messageSent struct {
	requestID string
	uncertain bool
	err       error
}

func (m fleetModel) beginMessage() (tea.Model, tea.Cmd) {
	e, ok := m.entry(m.selected)
	if !ok || e.Target.Kind != hostrouter.Agent || e.Diagnostics == nil || !m.uniqueBird(e) || m.services.Coordination == nil {
		m.status = "Select a reachable named bird to message; Enter opens other sessions"
		return m, nil
	}
	if _, err := m.services.Registry.ResolveTarget(e.Target); err != nil {
		m.status = "Coop destination changed; refresh and select again"
		return m, nil
	}
	binding := *e.Diagnostics
	e.Diagnostics = &binding
	m.draft = &messageDraft{entry: e, binding: binding, requestID: rand.Text(), loading: true}
	source, id, ctx := m.services.Coordination, m.draft.requestID, m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		result := messageReady{requestID: id}
		result.self, result.err = source.Self(ctx)
		if result.err == nil && !slices.Contains(result.self.Principal.Capabilities, "send") {
			result.err = c.ErrForbidden
		}
		if result.err == nil {
			var team []c.Presence
			team, result.err = source.Team(ctx)
			if result.err == nil {
				_, result.err = birdPlacement(source, e, team)
			}
		}
		if result.err == nil {
			result.thread, result.err = teamThread(ctx, source, result.self.Principal.ID, binding.AgentID)
		}
		return result
	}
}

func teamThread(ctx context.Context, source Coordination, sender, recipient string) (c.Thread, error) {
	caps, err := source.TaskCapabilities(ctx)
	if err != nil {
		return c.Thread{}, err
	}
	canonical := caps.Conversation
	if canonical == nil || canonical.Provider != "kata" || canonical.InstanceUID == "" || canonical.ProjectUID != caps.ProjectUID || canonical.IssueUID == "" {
		return c.Thread{}, c.ErrUnavailable
	}
	seen, after := map[string]bool{}, ""
	for {
		page, err := source.Threads(ctx, c.ThreadQuery{After: after})
		if err != nil {
			return c.Thread{}, err
		}
		for _, t := range page.Threads {
			s := t.Source
			if s != nil && s.Provider == canonical.Provider && s.InstanceUID == canonical.InstanceUID && s.ProjectUID == canonical.ProjectUID && s.IssueUID == canonical.IssueUID {
				if !slices.Contains(t.Members, sender) || !slices.Contains(t.Members, recipient) {
					return c.Thread{}, c.ErrForbidden
				}
				return t, nil
			}
		}
		if page.Next == "" {
			return c.Thread{}, c.ErrNotFound
		}
		if seen[page.Next] {
			return c.Thread{}, c.ErrConflict
		}
		after, seen[page.Next] = page.Next, true
	}
}

func (m fleetModel) messageKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := *m.draft
	d.body = slices.Clone(d.body)
	m.draft = &d
	if k.String() == "esc" {
		if !d.sending {
			m.draft = nil
			if d.frozen {
				m.status = "Unconfirmed send: check Kata before sending it again (request " + d.requestID + ")"
			}
		}
		return m, nil
	}
	if d.loading || d.sending {
		return m, nil
	}
	if k.String() == "ctrl+s" {
		if strings.TrimSpace(string(d.body)) == "" {
			m.status = "Write a message first"
			return m, nil
		}
		return m.sendMessage()
	}
	if d.frozen {
		return m, nil // An uncertain send can retry only its original payload and ID.
	}
	switch k.String() {
	case "left":
		d.cursor = max(0, d.cursor-1)
	case "right":
		d.cursor = min(len(d.body), d.cursor+1)
	case "home", "ctrl+a":
		d.cursor = 0
	case "end", "ctrl+e":
		d.cursor = len(d.body)
	case "backspace", "ctrl+h":
		if d.cursor > 0 {
			d.body = slices.Delete(d.body, d.cursor-1, d.cursor)
			d.cursor--
		}
	case "delete":
		if d.cursor < len(d.body) {
			d.body = slices.Delete(d.body, d.cursor, d.cursor+1)
		}
	default:
		var insert []rune
		switch k.Type {
		case tea.KeyRunes:
			for _, r := range k.Runes {
				if !unicode.IsControl(r) || r == '\n' {
					insert = append(insert, r)
				}
			}
		case tea.KeyEnter:
			insert = []rune{'\n'}
		case tea.KeySpace:
			insert = []rune{' '}
		}
		if len(string(d.body))+len(string(insert)) <= c.MaxText {
			d.body = slices.Insert(d.body, d.cursor, insert...)
			d.cursor += len(insert)
		} else {
			m.status = "Message is too long"
		}
	}
	return m, nil
}

func (m fleetModel) sendMessage() (tea.Model, tea.Cmd) {
	d := *m.draft
	e, ok := m.entry(d.entry.Target)
	if !ok || !m.uniqueBird(e) || e.Diagnostics == nil || e.Diagnostics.MasterID != d.binding.MasterID || e.Diagnostics.AgentID != d.binding.AgentID || e.Diagnostics.HostID != d.binding.HostID {
		m.status = "Message destination changed or is unavailable; refresh first"
		return m, nil
	}
	if _, err := m.services.Registry.ResolveTarget(e.Target); err != nil {
		m.status = "Message destination changed; refresh first"
		return m, nil
	}
	d.sending = true // In-flight editing is already disabled; uncertainty is sticky.
	m.draft = &d
	source, registry, ctx := m.services.Coordination, m.services.Registry, m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	input := c.SendMessage{ThreadID: d.thread.ID, ExpectedMembers: slices.Clone(d.thread.Members), Recipients: []string{d.binding.AgentID}, Body: string(d.body), RequestID: d.requestID}
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out := messageSent{requestID: d.requestID}
		_, err := registry.ResolveTarget(d.entry.Target)
		var self c.SelfIdentity
		if err == nil {
			self, err = source.Self(ctx)
		}
		if err == nil && (self.Principal.ID != d.self.Principal.ID || self.Principal.Kind != d.self.Principal.Kind || self.RestoreEpoch != d.self.RestoreEpoch || !slices.Contains(self.Principal.Capabilities, "send")) {
			err = c.ErrForbidden
		}
		if err == nil {
			var team []c.Presence
			team, err = source.Team(ctx)
			if err == nil {
				_, err = birdPlacement(source, d.entry, team)
			}
		}
		if err == nil {
			var thread c.Thread
			thread, err = source.Thread(ctx, d.thread.ID)
			if err == nil && (thread.ID != d.thread.ID || thread.Engagement != d.thread.Engagement || !slices.Contains(thread.Members, self.Principal.ID) || !slices.Contains(thread.Members, d.binding.AgentID)) {
				err = c.ErrForbidden
			}
		}
		if err == nil {
			out.uncertain = true
			var receipt c.MessageReceipt
			receipt, err = source.Send(ctx, input)
			if err == nil && (receipt.MessageID == "" || receipt.MasterID != source.MasterID() || receipt.RestoreEpoch != self.RestoreEpoch) {
				err = c.ErrConflict
			}
			if err == nil && !slices.ContainsFunc(receipt.Deliveries, func(delivery c.Delivery) bool {
				return delivery.AgentID == d.binding.AgentID && delivery.MessageID == receipt.MessageID
			}) {
				err = c.ErrConflict
			}
		}
		out.err = err
		return out
	}
}

func (m fleetModel) messageView(width, limit int) []string {
	d := m.draft
	if limit <= 0 {
		return nil
	}
	lines := []string{"Message to " + d.binding.AgentID + " @ " + d.entry.Target.Host.Name + " · team Kata"}
	if limit == 1 {
		return lines
	}
	if d.loading {
		if limit > 2 {
			lines = append(lines, "Opening conversation…")
		}
		return append(lines, "Esc cancel")
	}
	before := string(d.body[:d.cursor])
	body := strings.Split(readingWrap(before+"▏"+string(d.body[d.cursor:]), max(1, width)), "\n")
	position, markers := 0, strings.Count(before, "▏")
	for i, line := range body {
		count := strings.Count(line, "▏")
		if markers < count {
			position = i
			break
		}
		markers -= count
	}
	count := min(4, max(0, limit-2))
	start := max(0, min(position-count+1, len(body)-count))
	lines = append(lines, body[start:min(len(body), start+count)]...)
	footer := "Ctrl+S send · Enter newline · Esc cancel"
	if d.sending {
		footer = "Sending…"
	} else if d.frozen {
		footer = "Unconfirmed · Ctrl+S retry same message · Esc close"
	}
	return append(lines, footer)
}
