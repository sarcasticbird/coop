package tui

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	textansi "github.com/charmbracelet/x/ansi"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

// Diagnostic formatting must never dump private reader or draft buffers.
func (m fleetModel) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "Coop TUI [private buffers omitted]")
}
func (q coordState) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "Coordination [private buffers omitted]")
}
func (f coordForm) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "Form [private buffer omitted]")
}

func (m fleetModel) composePage() bool {
	q := m.coordination
	return m.workspace == 1 && (q.form != nil || q.receipt != nil || q.leaving != "")
}

func (m *fleetModel) revealComposeCursor() {
	f := *m.coordination.form
	f.positions = slices.Clone(f.positions)
	line := 0
	for _, text := range m.composeLines() {
		selected := strings.HasPrefix(text, "> ")
		if selected && !m.coordination.failed && (f.reveal || (f.kind == "send" || f.kind == "reply") && f.field == 0) {
			pos := f.position()
			text = "> " + f.labels[f.field] + ": " + string([]rune(f.values[f.field])[:pos]) + "|"
		}
		if m.width > 0 {
			text = textansi.Hardwrap(text, m.width, true)
		}
		rows := strings.Count(text, "\n") + 1
		if selected {
			line += rows - 1
			line = max(0, line-conversationHeader)
			limit := max(1, m.height-conversationHeader-2)
			if line < m.scroll {
				m.scroll = line
			} else if line >= m.scroll+limit {
				m.scroll = line - limit + 1
			}
			return
		}
		line += rows
	}
}

func (m *fleetModel) coordCloseForm(keep bool) {
	q := &m.coordination
	if keep && q.form != nil {
		f := *q.form
		f.scroll = q.leaveScroll
		q.drafts = append(slices.Clone(q.drafts), &f)
	}
	q.form, q.leaving = nil, ""
	m.scroll = q.readerScroll
	m.restoreConversationAnchor(q.readerAnchor, q.readerOffset)
	q.status = "Buffer discarded; this cannot undo a submitted operation"
	if keep {
		q.status = "Draft kept in process memory only; reopen the same intent to resume"
	}
}

func (m fleetModel) coordLeaveKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	q := &m.coordination
	switch k.String() {
	case "esc", "r":
		q.leaving = ""
		m.scroll = q.leaveScroll
	case "k":
		if q.leaving == "back" {
			m.coordCloseForm(true)
		}
	case "d":
		if q.leaving == "quit" {
			if q.cancel != nil {
				q.cancel()
			}
			q.form, q.drafts, q.leaving = nil, nil, ""
			if m.cancel != nil {
				m.cancel()
			}
			if m.actionCancel != nil {
				m.actionCancel()
			}
			return m, tea.Quit
		}
		m.coordCloseForm(false)
	}
	return m, nil
}

func (f *coordForm) position() int {
	if len(f.positions) != len(f.values) {
		f.positions = make([]int, len(f.values))
		for i := range f.positions {
			f.positions[i] = -1
		}
	}
	n := len([]rune(f.values[f.field]))
	if f.positions[f.field] < 0 {
		return n
	}
	return min(n, f.positions[f.field])
}

// Editor offsets are rune indices; rendering never changes the literal payload.
func (f *coordForm) edit(k tea.KeyMsg) bool {
	v, pos := []rune(f.values[f.field]), f.position()
	key, text := k.String(), ""
	switch key {
	case "left":
		pos = max(0, pos-1)
	case "right":
		pos = min(len(v), pos+1)
	case "home":
		for pos > 0 && v[pos-1] != '\n' {
			pos--
		}
	case "end":
		for pos < len(v) && v[pos] != '\n' {
			pos++
		}
	case "up", "down":
		start := pos
		for start > 0 && v[start-1] != '\n' {
			start--
		}
		col := pos - start
		end := pos
		for end < len(v) && v[end] != '\n' {
			end++
		}
		if key == "up" && start > 0 {
			end = start - 1
			start = end
			for start > 0 && v[start-1] != '\n' {
				start--
			}
			pos = min(end, start+col)
		} else if key == "down" && end < len(v) {
			start = end + 1
			end = start
			for end < len(v) && v[end] != '\n' {
				end++
			}
			pos = min(end, start+col)
		}
	case "backspace", "ctrl+h":
		if pos > 0 {
			v = slices.Delete(v, pos-1, pos)
			pos--
			f.preview = false
		}
	case "delete":
		if pos < len(v) {
			v = slices.Delete(v, pos, pos+1)
			f.preview = false
		}
	case "enter":
		text = "\n"
	default:
		switch k.Type {
		case tea.KeyRunes:
			text = string(k.Runes)
		case tea.KeySpace:
			text = " "
		}
	}
	limit := c.MaxText
	if f.kind == "proposal" {
		limit = c.MaxJSON - 1024
	}
	if text != "" {
		if len(string(v))+len(text) > limit || strings.IndexFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' }) >= 0 {
			return false
		}
		v = slices.Insert(v, pos, []rune(text)...)
		pos += len([]rune(text))
		f.preview = false
	}
	f.values[f.field], f.positions[f.field] = string(v), pos
	return true
}

func (m fleetModel) composeLines() (lines []string) {
	// Compact context stays pinned; full untruncated identity is also reviewable
	// in the scrolling body. No private input is copied into the fixed header.
	defer func() {
		if m.coordination.leaving == "" {
			for i := range min(conversationHeader, len(lines)) {
				lines[i] = strings.ReplaceAll(coordClean(lines[i]), "\n", " ")
				if m.width > 0 {
					lines[i] = textansi.Truncate(lines[i], m.width, "...")
				}
			}
		}
	}()
	q := m.coordination
	if q.leaving != "" {
		lines := []string{"Draft protection", "Private buffers exist only in this process."}
		if q.form != nil {
			lines = append(lines, "Master: "+q.form.master, "Principal: "+q.form.self.Principal.ID, "Thread: "+valueOr(q.form.thread.ID, "new"), "Request: "+q.form.requestID)
		}
		if q.leaving == "quit" {
			lines = append(lines, "Quit destroys all active and kept drafts.", "d Discard all and quit | r/Esc Return")
		} else {
			lines = append(lines, "k Keep in memory | d Discard | r/Esc Return")
		}
		if q.form != nil && q.form.frozen {
			lines = append(lines, "Outcome unknown; discarding cannot cancel or undo the operation.", "Original request: "+q.form.requestID, "Keep resumes ONLY the identical retry state.")
		}
		return lines
	}
	if q.form == nil {
		r := q.receipt
		lines := []string{"Threads > Delivery receipt", "Master: " + r.MasterID, "Principal: " + q.receiptSelf.Principal.ID, "Committed message: " + r.MessageID, fmt.Sprintf("Master: %s | epoch: %d", r.MasterID, r.RestoreEpoch), fmt.Sprintf("Backend: %s | sequence: %d", r.Backend, r.Sequence), "Commitment is not acknowledgment, execution or wake."}
		if len(r.Deliveries) == 0 {
			lines = append(lines, "No agent deliveries; commitment is not acknowledgment")
		}
		for _, d := range r.Deliveries {
			lines = append(lines, d.AgentID+" | "+d.State+" | "+d.Reason+" | reply: "+d.ReplyMessageID)
		}
		return lines
	}
	f := *q.form
	f.positions = slices.Clone(f.positions)
	canReveal := !q.failed && m.services.Coordination != nil && coordSameContext(q.self, f.self, m.services.Coordination.MasterID(), f.master)
	state := "Composer"
	if f.preview {
		state = "Preview"
	}
	if f.frozen {
		state = "Submitted / outcome unknown"
		if q.busy {
			state = "Submitting; awaiting receipt"
		}
	}
	lines = []string{"Threads > " + state + " (" + f.kind + ")", "Master: " + f.master, "Principal: " + f.self.Principal.ID}
	privacy := "masked"
	if canReveal && f.reveal {
		privacy = "revealed"
	}
	lines = append(lines, "Content: "+privacy)
	if f.kind == "create" && f.preview {
		members := append(coordIDs(f.values[2]), f.self.Principal.ID)
		slices.Sort(members)
		members = slices.Compact(members)
		for i, label := range []string{"Title", "Engagement"} {
			value := fmt.Sprintf("[hidden, %d bytes]", len(f.values[i]))
			if canReveal && f.reveal {
				value = valueOr(f.values[i], "none")
			}
			lines = append(lines, label+": "+value)
		}
		value := fmt.Sprintf("[hidden, %d members]", len(members))
		if canReveal && f.reveal {
			value = strings.Join(members, ",")
		}
		lines = append(lines, "Members (owner included): "+value, "Request: "+f.requestID, "Exact Master: "+f.master, fmt.Sprintf("Exact Principal: %s (%s) | epoch: %d", f.self.Principal.ID, f.self.Principal.Kind, f.self.RestoreEpoch), "Creates a thread only; no message or wake.")
		if f.frozen {
			lines = append(lines, "Outcome unknown; Ctrl+R retries ONLY this identical request.")
		} else {
			lines = append(lines, "Not created; review all pages before Ctrl+S creates.", "Esc edits; input changes require a new preview.")
		}
		return lines
	}
	if f.kind == "reply" {
		lines = append(lines, "Blank recipients use captured parent author: "+f.parentAuthor)
	}
	if f.kind == "send" {
		lines = append(lines, "Blank To is a note; wakes nobody.")
	}
	if f.kind == "decision" {
		lines = append(lines, fmt.Sprintf("Goal: %s\nExact revision: %s\nExpected decision sequence: %d\nState: %s", f.authority.Revision.Ref.GoalID, f.authority.Revision.Ref.RevisionSHA256, f.authority.DecisionSequence, f.authority.State))
	}
	for i, label := range f.labels {
		prefix := "  "
		if i == f.field && !f.preview {
			prefix = "> "
		}
		value := fmt.Sprintf("[hidden, %d bytes]", len(f.values[i]))
		if canReveal && (f.reveal || (f.kind == "send" || f.kind == "reply") && i == 0) {
			value = f.values[i]
			if i == f.field && !f.preview {
				runes := []rune(value)
				pos := f.position()
				value = string(runes[:pos]) + "|" + string(runes[pos:])
			}
		}
		lines = append(lines, prefix+label+": "+value)
	}
	lines = append(lines, "Thread: "+valueOr(f.thread.ID, "new")+" | parent: "+valueOr(f.parent, "none (root)"), "Request: "+f.requestID, "Captured master: "+f.master, fmt.Sprintf("Principal: %s (%s) | epoch: %d", f.self.Principal.ID, f.self.Principal.Kind, f.self.RestoreEpoch), "Engagement: "+f.thread.Engagement, "Captured members: "+strings.Join(f.thread.Members, ","))
	if f.kind == "create" {
		lines = append(lines, "Creates a thread only; no message or wake.")
	}
	if f.frozen {
		lines = append(lines, "Ctrl+R retries ONLY this identical request.", "No automatic retry; Esc draft options.")
	} else if f.preview {
		lines = append(lines, "Not submitted; review all pages before Ctrl+S submits.", "Esc edits; input changes require a new preview.")
	} else {
		lines = append(lines, "Enter inserts newline, never sends. Tab changes field.", "Not submitted; preview and review all before a second Ctrl+S submits.")
	}
	return lines
}
