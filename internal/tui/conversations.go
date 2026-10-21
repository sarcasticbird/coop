package tui

import (
	"fmt"
	"strings"

	textansi "github.com/charmbracelet/x/ansi"
)

const conversationHeader = 3

func (m fleetModel) conversationPage() bool {
	q := m.coordination
	return m.workspace == 1 && (q.section == "threads" || q.thread != nil) && q.form == nil && q.receipt == nil && q.authority == nil
}

func (m fleetModel) conversationLines(master string) []string {
	q := m.coordination
	title := "Threads"
	principal := q.self.Principal
	if q.thread != nil {
		title = "Threads > " + q.thread.ID
		if q.readerMaster != "" {
			master, principal = q.readerMaster, q.readerSelf.Principal
		}
	}
	lines := []string{title, "Master: " + master, "Principal: " + principal.ID + " (" + principal.Kind + ")"}
	// Context stays pinned without consuming the narrow reader's entire viewport.
	for i := range lines {
		lines[i] = strings.ReplaceAll(coordClean(lines[i]), "\n", " ")
		if m.width > 0 {
			lines[i] = textansi.Truncate(lines[i], m.width, "...")
		}
	}
	row := func(i int, text string) {
		prefix := "  "
		if i == q.cursor {
			prefix = "> "
		}
		lines = append(lines, prefix+text)
	}
	if q.thread == nil {
		lines = append(lines, "Enter opens masked reader | n new thread", "t Team | i Inbox | g Goals | ] next page")
		for i, thread := range q.threads {
			row(i, thread.ID+" | "+thread.Title+" | "+thread.Engagement)
		}
		if len(q.threads) == 0 {
			lines = append(lines, "No threads in this snapshot")
		}
		return append(lines, "Next page: "+valueOr(q.next, "none"))
	}
	lines = append(lines, "Thread: "+q.thread.ID+" | "+q.thread.Title, "Engagement: "+q.thread.Engagement+" | Members: "+strings.Join(q.thread.Members, ","), "Ctrl+V reveal/mask | c root | p selected reply", fmt.Sprintf("Private reader; no screenshots. Follow: %t", q.follow))
	for i, message := range q.messages {
		unread := ""
		if message.Sequence > q.readThrough[q.thread.ID] {
			unread = "[unread this session] "
		}
		body := "[body masked; Ctrl+V to reveal]"
		if q.readerBlocked {
			body = "[body masked; r refresh authority]"
		}
		if q.readerReveal && !q.failed && !q.readerBlocked {
			body = message.Body
		}
		row(i, unread+fmt.Sprintf("%s | %s | parent %s\n%s", message.ID, message.AuthorID, message.ParentMessageID, body))
	}
	if len(q.messages) == 0 {
		lines = append(lines, "No messages loaded; r refresh")
	}
	return lines
}

// Anchor the viewport to a logical row, not a reply selection or a wrapped line
// number. This keeps follow, history eviction and width changes from retargeting it.
func (m fleetModel) conversationAnchor() (string, int) {
	if !m.conversationPage() || m.help || m.runbook || m.services.Coordination == nil {
		return "", 0
	}
	lines := m.coordLines()[conversationHeader:]
	position := 0
	for _, line := range lines {
		count := m.conversationLineHeight(line)
		if m.scroll < position+count {
			return strings.TrimPrefix(strings.TrimPrefix(strings.SplitN(line, "\n", 2)[0], "> "), "  "), m.scroll - position
		}
		position += count
	}
	return "", 0
}

func (m fleetModel) conversationLineHeight(line string) int {
	if m.width > 0 {
		line = textansi.Hardwrap(line, m.width, true)
	}
	return strings.Count(line, "\n") + 1
}

func (m *fleetModel) restoreConversationAnchor(anchor string, offset int) {
	if anchor == "" || !m.conversationPage() {
		return
	}
	position := 0
	for _, line := range m.coordLines()[conversationHeader:] {
		count := m.conversationLineHeight(line)
		if strings.TrimPrefix(strings.TrimPrefix(strings.SplitN(line, "\n", 2)[0], "> "), "  ") == anchor {
			m.scroll = position + min(offset, count-1)
			return
		}
		position += count
	}
	m.scroll = 0 // The retained-history bound removed the anchor; never jump to latest.
}

func (m *fleetModel) revealConversationSelection() {
	if !m.conversationPage() || m.help || m.runbook || m.height <= 0 {
		return
	}
	lines, _, line := m.content()
	if line < 0 {
		return
	}
	line -= conversationHeader
	limit := max(1, m.height-conversationHeader-2)
	m.scroll = min(m.scroll, max(0, len(lines)-conversationHeader-limit))
	if line < m.scroll || line >= m.scroll+limit {
		m.scroll = min(max(0, line), max(0, len(lines)-conversationHeader-limit))
	}
}

func (m fleetModel) conversationSelectionVisible() bool {
	if !m.conversationPage() || m.help || m.runbook || m.height <= 0 {
		return false
	}
	_, _, line := m.content()
	return line >= conversationHeader+m.scroll && line < m.scroll+max(conversationHeader+1, m.height-2)
}
