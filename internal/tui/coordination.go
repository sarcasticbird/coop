package tui

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

// Coordination is implemented by the identity-pinned master client. No fleet
// selection can redirect this connection or confer coordination authority.
type Coordination interface {
	MasterID() string
	Self(context.Context) (c.SelfIdentity, error)
	Team(context.Context) ([]c.Presence, error)
	Threads(context.Context, c.ThreadQuery) (c.ThreadPage, error)
	Thread(context.Context, string) (c.Thread, error)
	Messages(context.Context, string, int64) (c.MessagePage, error)
	Message(context.Context, string) (c.Message, error)
	Pending(context.Context, c.PendingQuery) (c.DeliveryPage, error)
	MessageStatus(context.Context, string) (c.MessageReceipt, error)
	ApprovedGoals(context.Context, string, string) (c.GoalPage, error)
	ResolveGoal(context.Context, c.GoalRef) (c.GoalAuthority, error)
	CreateThread(context.Context, c.CreateThread) (c.Thread, error)
	SetMembers(context.Context, c.SetMembers) (c.Thread, error)
	Send(context.Context, c.SendMessage) (c.MessageReceipt, error)
	Ping(context.Context, c.Ping) (c.MessageReceipt, error)
	ProposeGoal(context.Context, c.GoalProposal) (c.GoalRevision, error)
	DecideGoal(context.Context, c.GoalDecisionInput) (c.GoalAuthority, error)
}

type coordState struct {
	section              string
	self                 c.SelfIdentity
	team                 []c.Presence
	threads              []c.Thread
	inbox                []c.Delivery
	goals                []c.GoalAuthority
	thread               *c.Thread
	messages             []c.Message
	readThrough          map[string]int64
	authority            *c.GoalAuthority
	receipt              *c.MessageReceipt
	next                 string
	after                int64
	cursor               int
	follow, busy, failed bool
	seen                 time.Time
	status               string
	serial               uint64
	form                 *coordForm
}

// Forms own immutable snapshots and freeze on submission, including after an
// uncertain outcome. A retry reuses both input and request ID verbatim.
type coordForm struct {
	kind                    string
	labels, values          []string
	field                   int
	requestID               string
	thread                  c.Thread
	parent                  string
	authority               c.GoalAuthority
	preview, frozen, reveal bool
	run                     func(context.Context, Coordination) (any, error)
}

type coordResult struct {
	serial uint64
	kind   string
	self   c.SelfIdentity
	data   any
	err    error
}

func coordError(err error) string {
	for _, known := range []error{c.ErrInvalid, c.ErrForbidden, c.ErrNotFound, c.ErrConflict, c.ErrCapacity, c.ErrSize, c.ErrEpoch, c.ErrUnavailable} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "transport unavailable; outcome unknown for writes"
}

func coordClean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, s)
}

func (m *fleetModel) coordCall(kind string, run func(context.Context, Coordination) (any, error)) tea.Cmd {
	q := &m.coordination
	if q.busy {
		return nil
	}
	q.serial++
	q.busy, q.status = true, "Loading "+kind+"..."
	serial, source, ctx := q.serial, m.services.Coordination, m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out := coordResult{serial: serial, kind: kind}
		if source == nil {
			out.err = c.ErrUnavailable
			return out
		}
		out.self, out.err = source.Self(ctx)
		if out.err == nil {
			out.data, out.err = run(ctx, source)
		}
		return out
	}
}

func (m *fleetModel) coordLoad(next bool) tea.Cmd {
	q := &m.coordination
	if q.section == "" {
		q.section = "team"
	}
	section, cursor := q.section, ""
	if next && q.thread == nil && q.receipt == nil && q.authority == nil {
		cursor = q.next
		if cursor == "" {
			return nil
		}
	}
	thread, after, receipt, authority := q.thread, q.after, q.receipt, q.authority
	return m.coordCall("read", func(ctx context.Context, s Coordination) (any, error) {
		switch {
		case authority != nil:
			return s.ResolveGoal(ctx, authority.Revision.Ref)
		case receipt != nil:
			return s.MessageStatus(ctx, receipt.MessageID)
		case thread != nil:
			return s.Messages(ctx, thread.ID, after)
		case section == "team":
			return s.Team(ctx)
		case section == "threads":
			return s.Threads(ctx, c.ThreadQuery{After: cursor})
		case section == "inbox":
			return s.Pending(ctx, c.PendingQuery{After: cursor})
		default:
			return s.ApprovedGoals(ctx, "", cursor)
		}
	})
}

func (m fleetModel) coordUpdate(r coordResult) (tea.Model, tea.Cmd) {
	q := &m.coordination
	if r.serial != q.serial {
		return m, nil
	}
	q.busy = false
	q.self = r.self
	q.failed = r.err != nil
	if r.err != nil {
		q.status = coordError(r.err)
		if q.form != nil && q.form.frozen {
			q.status += "; request " + q.form.requestID + "; Ctrl+R retries ONLY this identical request; Esc discards buffer, not the operation"
		}
		return m, nil
	}
	q.seen, q.status = time.Now(), "Snapshot refreshed"
	if r.kind == "write" || r.kind == "inspect" {
		q.form = nil
		q.status = "Completed; refresh to inspect current state"
	}
	switch data := r.data.(type) {
	case []c.Presence:
		q.team = data
		q.cursor = max(0, min(q.cursor, len(data)-1))
	case c.ThreadPage:
		q.threads, q.next, q.cursor = data.Threads, data.Next, 0
	case c.DeliveryPage:
		q.inbox, q.next, q.cursor = data.Deliveries, data.Next, 0
		if data.Quiescent {
			q.status = "Restore reconciliation; writes unavailable"
		}
	case c.GoalPage:
		q.goals, q.next, q.cursor = data.Goals, data.Next, 0
	case c.Thread:
		q.thread, q.messages, q.after, q.cursor, q.receipt = &data, nil, 0, 0, nil
		return m, m.coordLoad(false)
	case c.Message:
		q.section = "threads"
		return m, m.coordCall("thread", func(ctx context.Context, s Coordination) (any, error) { return s.Thread(ctx, data.ThreadID) })
	case c.MessagePage:
		for _, message := range data.Messages {
			if !slices.ContainsFunc(q.messages, func(old c.Message) bool { return old.ID == message.ID }) {
				q.messages = append(q.messages, message)
			}
			q.after = max(q.after, message.Sequence)
		}
		// Bound the reader's retained private history, independently of API pages.
		if len(q.messages) > 500 {
			q.messages = q.messages[len(q.messages)-500:]
		}
		q.cursor = min(q.cursor, max(0, len(q.messages)-1))
		if data.Next > q.after {
			q.after = data.Next
		}
	case c.MessageReceipt:
		q.receipt, q.authority = &data, nil
	case c.GoalAuthority:
		q.authority, q.receipt = &data, nil
	case c.GoalRevision:
		q.authority = &c.GoalAuthority{Revision: data, State: "proposed; authority not yet refreshed"}
		q.receipt = nil
		return m, m.coordCall("authority", func(ctx context.Context, s Coordination) (any, error) { return s.ResolveGoal(ctx, data.Ref) })
	}
	return m, nil
}

func (q coordState) allowed(capability string) bool {
	return !q.failed && !q.seen.IsZero() && slices.Contains(q.self.Principal.Capabilities, capability)
}

func (m *fleetModel) coordOpen(kind string) {
	q := &m.coordination
	capability := "send"
	switch kind {
	case "create", "members":
		capability = "manage"
	case "decision":
		capability = "decide"
	case "authority", "status":
		capability = "read"
	}
	if !q.allowed(capability) {
		q.status = "Authenticated " + capability + " capability required; refresh identity"
		return
	}
	f := &coordForm{kind: kind, requestID: rand.Text()}
	if q.thread != nil {
		f.thread = *q.thread
		f.thread.Members = slices.Clone(q.thread.Members)
	}
	switch kind {
	case "create":
		if q.self.Principal.Kind != "owner" || !q.allowed("manage") {
			q.status = "Owner manage capability required"
			return
		}
		f.labels = []string{"Title", "Engagement", "Stable member IDs (comma separated)"}
	case "members":
		if q.thread == nil || q.self.Principal.Kind != "owner" || !q.allowed("manage") {
			q.status = "Open a thread; owner manage capability required"
			return
		}
		f.labels = []string{"Replacement member IDs (comma separated)"}
	case "send", "reply":
		if q.thread == nil {
			q.status = "Open a thread first"
			return
		}
		f.labels = []string{"Recipient IDs (comma separated; blank reply defaults to parent author)", "Private body"}
		if kind == "reply" {
			if len(q.messages) == 0 {
				q.status = "Select a message first"
				return
			}
			f.parent = q.messages[q.cursor].ID
		}
	case "ping":
		if len(q.team) == 0 {
			return
		}
		f.labels = []string{"Agent ID", "Engagement", "Private prompt (distinct from durable messaging)"}
	case "proposal":
		f.labels = []string{"Private structured GoalProposal JSON (explicit repositories required)"}
	case "authority":
		f.labels = []string{"Goal ID", "Exact revision SHA-256"}
	case "status":
		f.labels = []string{"Committed message ID"}
	case "decision":
		if q.authority == nil || q.self.Principal.Kind != "owner" || !q.allowed("decide") {
			q.status = "Inspect exact authority first; owner decide capability required"
			return
		}
		f.authority = *q.authority
		f.labels = []string{"Action: approve/reject/pause/resume/complete/reopen"}
	}
	f.values = make([]string, len(f.labels))
	if kind == "members" {
		f.values[0] = strings.Join(f.thread.Members, ",")
	}
	if kind == "ping" {
		f.values[0] = q.team[q.cursor].AgentID
	}
	q.form, q.status = f, ""
	m.scroll = 0
}

func coordIDs(value string) []string {
	var ids []string
	for _, id := range strings.Split(value, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (f coordForm) build() (func(context.Context, Coordination) (any, error), error) {
	for _, value := range f.values {
		if len(value) > c.MaxText && f.kind != "proposal" {
			return nil, c.ErrSize
		}
	}
	switch f.kind {
	case "create":
		if strings.TrimSpace(f.values[0]) == "" || strings.TrimSpace(f.values[1]) == "" {
			return nil, c.ErrInvalid
		}
		in := c.CreateThread{Title: f.values[0], Engagement: f.values[1], Members: coordIDs(f.values[2]), RequestID: f.requestID}
		return func(ctx context.Context, s Coordination) (any, error) { return s.CreateThread(ctx, in) }, nil
	case "members":
		in := c.SetMembers{ThreadID: f.thread.ID, ExpectedMembers: slices.Clone(f.thread.Members), Members: coordIDs(f.values[0]), RequestID: f.requestID}
		return func(ctx context.Context, s Coordination) (any, error) { return s.SetMembers(ctx, in) }, nil
	case "send", "reply":
		if strings.TrimSpace(f.values[1]) == "" {
			return nil, c.ErrInvalid
		}
		in := c.SendMessage{ThreadID: f.thread.ID, ParentMessageID: f.parent, Recipients: coordIDs(f.values[0]), Body: f.values[1], RequestID: f.requestID}
		return func(ctx context.Context, s Coordination) (any, error) { return s.Send(ctx, in) }, nil
	case "ping":
		if f.values[0] == "" || f.values[1] == "" || strings.TrimSpace(f.values[2]) == "" {
			return nil, c.ErrInvalid
		}
		in := c.Ping{AgentID: f.values[0], Engagement: f.values[1], Body: f.values[2], RequestID: f.requestID}
		return func(ctx context.Context, s Coordination) (any, error) { return s.Ping(ctx, in) }, nil
	case "proposal":
		var in c.GoalProposal
		dec := json.NewDecoder(strings.NewReader(f.values[0]))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return nil, c.ErrInvalid
		}
		var extra any
		if dec.Decode(&extra) != io.EOF || in.ThreadID == "" || in.Purpose == "" || in.Scope == "" || len(in.Repositories) == 0 || len(in.Acceptance) == 0 || in.AuthorID == "" || in.PeerID == "" || in.AuthorID == in.PeerID {
			return nil, c.ErrInvalid
		}
		for _, repo := range in.Repositories {
			if repo.Name == "" || len(repo.BaseBranches) == 0 {
				return nil, c.ErrInvalid
			}
		}
		in.RequestID = f.requestID
		return func(ctx context.Context, s Coordination) (any, error) { return s.ProposeGoal(ctx, in) }, nil
	case "authority":
		if f.values[0] == "" || len(f.values[1]) != 64 {
			return nil, c.ErrInvalid
		}
		return func(ctx context.Context, s Coordination) (any, error) {
			return s.ResolveGoal(ctx, c.GoalRef{Source: "master", MasterID: s.MasterID(), GoalID: f.values[0], RevisionSHA256: f.values[1]})
		}, nil
	case "status":
		if f.values[0] == "" {
			return nil, c.ErrInvalid
		}
		return func(ctx context.Context, s Coordination) (any, error) { return s.MessageStatus(ctx, f.values[0]) }, nil
	case "decision":
		if !slices.Contains([]string{"approve", "reject", "pause", "resume", "complete", "reopen"}, f.values[0]) {
			return nil, c.ErrInvalid
		}
		in := c.GoalDecisionInput{GoalID: f.authority.Revision.Ref.GoalID, RevisionSHA256: f.authority.Revision.Ref.RevisionSHA256, ExpectedDecisionSequence: f.authority.DecisionSequence, Action: f.values[0], RequestID: f.requestID}
		return func(ctx context.Context, s Coordination) (any, error) { return s.DecideGoal(ctx, in) }, nil
	}
	return nil, c.ErrInvalid
}

func (m fleetModel) coordKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	q := &m.coordination
	key := k.String()
	if q.busy {
		return m, nil
	}
	if q.form != nil {
		f := *q.form
		f.values = slices.Clone(f.values)
		q.form = &f
		switch key {
		case "esc":
			q.form = nil
			q.status = "Buffer cleared; cancellation cannot undo an already submitted operation"
			if f.frozen {
				q.status += "; inspect original request " + f.requestID + " before creating another intent"
			}
		case "ctrl+v":
			f.reveal = !f.reveal
		case "ctrl+r":
			if f.frozen && f.run != nil {
				kind := "write"
				if f.kind == "authority" || f.kind == "status" {
					kind = "inspect"
				}
				return m, m.coordCall(kind, f.run)
			}
		case "ctrl+s":
			if f.frozen {
				return m, nil
			}
			if !f.preview {
				run, err := f.build()
				if err != nil {
					q.status = "Invalid or oversized input; check the form (private details suppressed)"
					return m, nil
				}
				f.run, f.preview = run, true
				q.status = "Review exact target/input; Ctrl+S submits; Esc cancels"
			} else {
				f.frozen = true
				kind := "write"
				if f.kind == "authority" || f.kind == "status" {
					kind = "inspect"
				}
				return m, m.coordCall(kind, f.run)
			}
		case "tab", "shift+tab":
			if !f.frozen {
				f.preview = false
				delta := 1
				if key == "shift+tab" {
					delta = -1
				}
				f.field = (f.field + delta + len(f.values)) % len(f.values)
			}
		case "backspace", "ctrl+h":
			if !f.frozen {
				f.preview = false
				value := []rune(f.values[f.field])
				if len(value) > 0 {
					f.values[f.field] = string(value[:len(value)-1])
				}
			}
		case "enter":
			if !f.frozen {
				f.preview = false
				f.values[f.field] += "\n"
			}
		default:
			if !f.frozen && (k.Type == tea.KeyRunes || k.Type == tea.KeySpace) {
				text := string(k.Runes)
				if k.Type == tea.KeySpace {
					text = " "
				}
				if strings.IndexFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' }) >= 0 {
					return m, nil
				}
				limit := c.MaxText
				if f.kind == "proposal" {
					limit = c.MaxJSON - 1024
				}
				if len(f.values[f.field])+len(text) <= limit {
					f.preview = false
					f.values[f.field] += text
				} else {
					q.status = "Input exceeds the form byte limit; nothing from this paste was added"
				}
			}
		}
		return m, nil
	}
	switch key {
	case "t", "l", "i", "g":
		section := map[string]string{"t": "team", "l": "threads", "i": "inbox", "g": "goals"}[key]
		q.section, q.thread, q.receipt, q.authority, q.messages, q.after, q.next, q.cursor, q.follow = section, nil, nil, nil, nil, 0, "", 0, false
		m.scroll = 0
		return m, m.coordLoad(false)
	case "esc":
		if q.receipt != nil {
			q.receipt = nil
		} else if q.authority != nil {
			q.authority = nil
		} else if q.thread != nil {
			q.thread, q.messages, q.after, q.cursor, q.follow = nil, nil, 0, 0, false
		} else {
			m.workspace = 0
		}
		m.scroll = 0
	case "r":
		return m, m.coordLoad(false)
	case "]":
		return m, m.coordLoad(true)
	case "f":
		q.follow = !q.follow
		q.status = fmt.Sprintf("Follow/receipt polling: %t", q.follow)
	case "n":
		m.coordOpen("create")
	case "c":
		m.coordOpen("send")
	case "p":
		if q.section == "team" && q.thread == nil {
			m.coordOpen("ping")
		} else {
			m.coordOpen("reply")
		}
	case "m":
		m.coordOpen("members")
	case "o":
		m.coordOpen("proposal")
	case "a":
		m.coordOpen("authority")
	case "d":
		m.coordOpen("decision")
	case "s":
		if q.thread != nil && len(q.messages) > 0 {
			id := q.messages[q.cursor].ID
			return m, m.coordCall("receipt", func(ctx context.Context, s Coordination) (any, error) { return s.MessageStatus(ctx, id) })
		}
		m.coordOpen("status")
	case "enter":
		if q.thread != nil && q.receipt == nil {
			q.readThrough = maps.Clone(q.readThrough)
			if q.readThrough == nil {
				q.readThrough = make(map[string]int64)
			}
			q.readThrough[q.thread.ID] = q.after
			q.status = "Marked loaded messages read in this TUI session only"
		}
		if q.thread == nil && q.receipt == nil {
			switch q.section {
			case "threads":
				if len(q.threads) > 0 {
					id := q.threads[q.cursor].ID
					return m, m.coordCall("thread", func(ctx context.Context, s Coordination) (any, error) { return s.Thread(ctx, id) })
				}
			case "inbox":
				if len(q.inbox) > 0 {
					id := q.inbox[q.cursor].MessageID
					return m, m.coordCall("message", func(ctx context.Context, s Coordination) (any, error) { return s.Message(ctx, id) })
				}
			case "goals":
				if len(q.goals) > 0 {
					ref := q.goals[q.cursor].Revision.Ref
					return m, m.coordCall("authority", func(ctx context.Context, s Coordination) (any, error) { return s.ResolveGoal(ctx, ref) })
				}
			}
		}
	case "up", "k", "down", "j":
		count := len(q.team)
		if q.thread != nil {
			count = len(q.messages)
		} else {
			switch q.section {
			case "threads":
				count = len(q.threads)
			case "inbox":
				count = len(q.inbox)
			case "goals":
				count = len(q.goals)
			}
		}
		delta := 1
		if key == "up" || key == "k" {
			delta = -1
		}
		q.cursor = max(0, min(count-1, q.cursor+delta))
	}
	return m, nil
}

func (m fleetModel) coordLines() (lines []string) {
	defer func() {
		for i := range lines {
			lines[i] = coordClean(lines[i])
		}
	}()
	q := m.coordination
	master := "unconfigured"
	if m.services.Coordination != nil {
		master = m.services.Coordination.MasterID()
	}
	lines = []string{"Coordination | Master: " + master + " | Principal: " + q.self.Principal.ID + " (" + q.self.Principal.Kind + ")", "t Team | l Threads | i Inbox (agent only) | g Active approved goals", "n create | c compose | p reply/prompt | m members | s status | a authority | o propose | d decide", "r refresh | f follow/poll | ] next page | Enter open | Esc back/Fleet"}
	if m.services.Coordination == nil {
		return append(lines, "Master client unavailable: configure --master-config with a private master-client.toml; Fleet remains available")
	}
	health := "No successful snapshot"
	if !q.seen.IsZero() {
		health = "Snapshot age " + time.Since(q.seen).Round(time.Second).String()
	}
	if q.failed {
		health = "STALE/OFFLINE | " + health
	}
	if q.busy {
		health += " | in progress"
	}
	lines = append(lines, health, q.status)
	if q.form != nil {
		f := q.form
		lines = append(lines, "Form: "+f.kind, "Request: "+f.requestID, "Thread: "+f.thread.ID+" | parent: "+f.parent)
		if f.kind == "members" {
			lines = append(lines, "Expected members: "+strings.Join(f.thread.Members, ","))
		}
		if f.kind == "decision" {
			lines = append(lines, fmt.Sprintf("Goal: %s\nExact revision: %s\nExpected decision sequence: %d\nState: %s\nAction: %s", f.authority.Revision.Ref.GoalID, f.authority.Revision.Ref.RevisionSHA256, f.authority.DecisionSequence, f.authority.State, f.values[0]))
		}
		for i, label := range f.labels {
			prefix := "  "
			if i == f.field {
				prefix = "> "
			}
			value := fmt.Sprintf("[hidden, %d bytes]", len(f.values[i]))
			if f.reveal {
				value = f.values[i]
			}
			lines = append(lines, prefix+label+": "+value)
		}
		return append(lines, fmt.Sprintf("Preview: %t | submitted/frozen: %t", f.preview, f.frozen), "Tab field | Enter newline | Ctrl+V reveal private input | Ctrl+S preview/submit | Ctrl+R identical retry | Esc clear")
	}
	if q.receipt != nil {
		lines = append(lines, "Committed message: "+q.receipt.MessageID+" | Backend: "+q.receipt.Backend)
		if len(q.receipt.Deliveries) == 0 {
			lines = append(lines, "No agent deliveries; commitment is not acknowledgment")
		}
		for _, d := range q.receipt.Deliveries {
			lines = append(lines, d.AgentID+" | "+d.State+" | "+d.Reason+" | reply: "+d.ReplyMessageID)
		}
		return lines
	}
	if q.authority != nil {
		a := q.authority
		lines = append(lines, fmt.Sprintf("Goal: %s\nRevision: %s\nActive revision: %s\nState: %s | valid: %t\nDecision sequence: %d | Approval sequence: %d\nAuthor: %s | Peer: %s | QA: %s", a.Revision.Ref.GoalID, a.Revision.Ref.RevisionSHA256, a.ActiveRevisionSHA256, a.State, a.Valid, a.DecisionSequence, a.ApprovalSequence, a.Actors.Author.PrincipalID, a.Actors.Peer.PrincipalID, a.Actors.QA.PrincipalID))
		proposal := a.Revision.Proposal
		lines = append(lines, "Thread: "+proposal.ThreadID, "Purpose: "+proposal.Purpose, "Scope: "+proposal.Scope)
		for _, repository := range proposal.Repositories {
			lines = append(lines, "Repository: "+repository.Name+" | branches: "+strings.Join(repository.BaseBranches, ","))
		}
		for _, criterion := range proposal.Acceptance {
			lines = append(lines, "Acceptance: "+criterion)
		}
		return append(lines, fmt.Sprintf("QA required: %t | Actor: %s | Environment: %s | Revision: %s\nEvidence: %s / %s / %s", proposal.QA.Required, proposal.QA.ActorID, proposal.QA.Environment, proposal.QA.EnvironmentRevision, proposal.QA.EvidenceDestination.MasterID, proposal.QA.EvidenceDestination.ThreadID, proposal.QA.EvidenceDestination.Engagement))
	}
	row := func(i int, text string) {
		prefix := "  "
		if i == q.cursor {
			prefix = "> "
		}
		lines = append(lines, prefix+text)
	}
	if q.thread != nil {
		lines = append(lines, "Thread: "+q.thread.ID+" | "+q.thread.Title, "Members: "+strings.Join(q.thread.Members, ","), "Private reader; no screenshots. Enter marks loaded messages read (session only). Follow: "+fmt.Sprint(q.follow))
		for i, v := range q.messages {
			unread := ""
			if v.Sequence > q.readThrough[q.thread.ID] {
				unread = "[unread this session] "
			}
			row(i, unread+fmt.Sprintf("%s | %s | parent %s\n%s", v.ID, v.AuthorID, v.ParentMessageID, v.Body))
		}
		return lines
	}
	switch q.section {
	case "threads":
		for i, v := range q.threads {
			row(i, v.ID+" | "+v.Title+" | "+v.Engagement)
		}
	case "inbox":
		for i, v := range q.inbox {
			row(i, v.MessageID+" | "+v.State+" | "+v.ID)
		}
	case "goals":
		for i, v := range q.goals {
			row(i, v.Revision.Ref.GoalID+" | "+v.State+" | "+v.Revision.Ref.RevisionSHA256)
		}
	default:
		for i, v := range q.team {
			row(i, fmt.Sprintf("%s (%s) | %s / %s | %s | last seen %s", v.AgentID, v.Alias, v.HostID, v.InstanceID, v.State, v.LastSeen.UTC().Format(time.RFC3339)))
		}
	}
	return append(lines, "Next page: "+valueOr(q.next, "none"))
}
