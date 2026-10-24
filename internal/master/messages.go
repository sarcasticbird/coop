package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (s *Store) Send(ctx context.Context, actor c.Principal, in c.SendMessage) (c.MessageReceipt, error) {
	var out c.MessageReceipt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := authorize(ctx, tx, actor, "send")
	if err != nil {
		return out, err
	}
	if err = member(ctx, tx, p, in.ThreadID); err != nil {
		return out, err
	}
	// Optional for existing CLI/MCP callers; captured TUI intents require an
	// exact membership check in the same transaction as insertion and replay.
	if in.ExpectedMembers != nil {
		var raw []byte
		var thread c.Thread
		if err = tx.QueryRowContext(ctx, "SELECT body FROM threads WHERE id=?", in.ThreadID).Scan(&raw); err != nil {
			return out, err
		}
		if err = json.Unmarshal(raw, &thread); err != nil {
			return out, err
		}
		if !slices.Equal(normalized(in.ExpectedMembers), thread.Members) {
			return out, c.ErrConflict
		}
	}
	if yes, e := request(ctx, tx, p.ID, in.RequestID, "send", in, &out); yes || e != nil {
		if e != nil {
			return out, e
		}
		_ = tx.Rollback()
		return s.publishSource(ctx, p, out)
	}
	out, err = s.insertMessage(ctx, tx, p, in)
	if err != nil {
		return out, err
	}
	if err = s.stageSource(ctx, tx, p, &out, ""); err != nil {
		return out, err
	}
	if err = saveRequest(ctx, tx, p.ID, in.RequestID, "send", in, out); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return s.publishSource(ctx, p, out)
}

func (s *Store) insertMessage(ctx context.Context, tx *sql.Tx, p c.Principal, in c.SendMessage) (c.MessageReceipt, error) {
	out := c.MessageReceipt{MasterID: s.id, RestoreEpoch: s.epoch, Backend: "master", Deliveries: []c.Delivery{}}
	if !validID(in.RequestID) || strings.TrimSpace(in.Body) == "" || !utf8.ValidString(in.Body) {
		return out, c.ErrInvalid
	}
	if len(in.Body) > c.MaxText {
		return out, c.ErrSize
	}
	var total int
	if err := tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM messages)+(SELECT count(*) FROM history_imports)").Scan(&total); err != nil {
		return out, err
	}
	if total >= c.MaxMessages {
		return out, c.ErrCapacity
	}
	m := c.Message{ID: newID("msg"), ThreadID: in.ThreadID, AuthorID: p.ID, AuthorKind: p.Kind, Body: in.Body, RequestID: in.RequestID, ParentMessageID: in.ParentMessageID, CreatedAt: s.now().UTC(), Recipients: []string{}}
	m.ChainID = m.ID
	if p.Kind == "agent" {
		m.Hop = 1
	}
	if in.ParentMessageID != "" {
		parent, err := s.sourceParent(ctx, tx, p, in.ThreadID, in.ParentMessageID)
		if err != nil {
			return out, err
		}
		m.ParentMessageID = parent.ID
		if parent.ThreadID != in.ThreadID {
			return out, c.ErrForbidden
		}
		if in.Recipients == nil {
			in.Recipients = []string{parent.AuthorID}
		}
		if p.Kind != "owner" {
			m.ChainID = parent.ChainID
			m.Hop = parent.Hop + 1
		}
	}
	for _, name := range normalized(in.Recipients) {
		recipient, err := principal(ctx, tx, name)
		if err != nil {
			return out, err
		}
		if recipient.ID == p.ID {
			continue
		}
		if err = member(ctx, tx, recipient, in.ThreadID); err != nil {
			return out, err
		}
		m.Recipients = append(m.Recipients, recipient.ID)
	}
	m.Recipients = normalized(m.Recipients)
	if in.Continuation != nil {
		m.Continuation = in.Continuation
		if in.ParentMessageID != "" {
			return out, c.ErrInvalid
		}
		if err := validateContinuation(ctx, tx, m); err != nil {
			return out, err
		}
	}
	for _, id := range m.Recipients {
		r, err := principal(ctx, tx, id)
		if err != nil {
			return out, err
		}
		if r.Kind != "agent" {
			continue
		}
		d := c.Delivery{ID: newID("d"), MessageID: m.ID, AgentID: id, State: "queued"}
		if m.Hop > 2 {
			d.State = "suppressed"
			d.Reason = "peer_hop_limit"
		} else {
			var pending int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE agent_id=? AND state IN ('publishing','queued','received','running','admitted')", id).Scan(&pending); err != nil {
				return out, err
			}
			if pending >= c.MaxPending {
				return out, c.ErrCapacity
			}
		}
		out.Deliveries = append(out.Deliveries, d)
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO messages(id,thread_id,author_id,body) VALUES(?,?,?,?)", m.ID, m.ThreadID, m.AuthorID, encode(m))
	if err != nil {
		return out, err
	}
	m.Sequence, err = result.LastInsertId()
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE messages SET body=? WHERE id=?", encode(m), m.ID); err != nil {
		return out, err
	}
	for _, d := range out.Deliveries {
		if _, err = tx.ExecContext(ctx, "INSERT INTO deliveries(id,message_id,agent_id,state,body) VALUES(?,?,?,?,?)", d.ID, d.MessageID, d.AgentID, d.State, encode(d)); err != nil {
			return out, err
		}
	}
	if err = event(ctx, tx, "message", m.ThreadID, m.ID); err != nil {
		return out, err
	}
	out.MessageID = m.ID
	out.Sequence = m.Sequence
	return out, nil
}
func loadMessage(ctx context.Context, q queryer, id string) (c.Message, error) {
	var raw []byte
	var m c.Message
	err := q.QueryRowContext(ctx, "SELECT body FROM messages WHERE id=?", id).Scan(&raw)
	if err == sql.ErrNoRows {
		return m, c.ErrNotFound
	}
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	return m, nil
}
func loadDelivery(ctx context.Context, q queryer, id string) (c.Delivery, error) {
	var raw []byte
	var d c.Delivery
	err := q.QueryRowContext(ctx, "SELECT body FROM deliveries WHERE id=?", id).Scan(&raw)
	if err == sql.ErrNoRows {
		return d, c.ErrNotFound
	}
	if err != nil {
		return d, err
	}
	if err = json.Unmarshal(raw, &d); err != nil {
		return d, err
	}
	return d, nil
}
func saveDelivery(ctx context.Context, q queryer, d c.Delivery) error {
	_, err := q.ExecContext(ctx, "UPDATE deliveries SET state=?,body=? WHERE id=?", d.State, encode(d), d.ID)
	return err
}

func (s *Store) Message(ctx context.Context, actor c.Principal, id string) (c.Message, error) {
	p, err := authorize(ctx, s.db, actor, "read")
	if err != nil {
		return c.Message{}, err
	}
	m, err := loadMessage(ctx, s.db, id)
	if err != nil {
		return m, err
	}
	return m, member(ctx, s.db, p, m.ThreadID)
}
func (s *Store) MessageStatus(ctx context.Context, actor c.Principal, id string) (c.MessageReceipt, error) {
	m, err := s.Message(ctx, actor, id)
	out := c.MessageReceipt{MasterID: s.id, RestoreEpoch: s.epoch, Backend: "master", MessageID: id, Sequence: m.Sequence, Deliveries: []c.Delivery{}, Source: m.Source}
	if err != nil {
		return out, err
	}
	if m.Source != nil {
		out.Backend = "kata"
	}
	rows, err := s.db.QueryContext(ctx, "SELECT body FROM deliveries WHERE message_id=? ORDER BY agent_id", id)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		var d c.Delivery
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		if err = json.Unmarshal(raw, &d); err != nil {
			return out, err
		}
		out.Deliveries = append(out.Deliveries, d)
	}
	return out, rows.Err()
}

func (s *Store) SetDelivery(ctx context.Context, actor c.Principal, in c.DeliveryUpdate) error {
	if in.RestoreEpoch != s.epoch {
		return c.ErrEpoch
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := authorize(ctx, tx, actor, "send")
	if err != nil || p.Kind != "agent" {
		return c.ErrForbidden
	}
	if err = placement(ctx, tx, p.ID, in.InstanceID, in.Generation); err != nil {
		return err
	}
	d, err := loadDelivery(ctx, tx, in.DeliveryID)
	if err != nil {
		return err
	}
	if d.AgentID != p.ID {
		return c.ErrForbidden
	}
	m, err := loadMessage(ctx, tx, d.MessageID)
	if err != nil {
		return err
	}
	if err = member(ctx, tx, p, m.ThreadID); err != nil {
		return err
	}
	if len(in.Reason) > 512 {
		return c.ErrSize
	}
	if d.State == in.State && digest(d.Intent) == digest(in.Intent) && d.Reason == in.Reason {
		return nil
	}
	if m.Continuation != nil {
		return s.setContinuationDelivery(ctx, tx, m, d, in)
	}
	allowed := d.State == "queued" && in.State == "received" || d.State == "received" && in.State == "running" || (d.State == "queued" || d.State == "received" || d.State == "running") && (in.State == "blocked" || in.State == "cancelled")
	allowed = allowed || d.State == "blocked" && in.State == "cancelled"
	allowed = allowed || d.State == "blocked" && d.Reason == "native_observation_unavailable" && in.State == "running" && d.Intent != nil
	if !allowed {
		return c.ErrConflict
	}
	if in.State == "running" && (in.Intent == nil || !validID(in.Intent.SessionID) || !validID(in.Intent.MessageID)) {
		return c.ErrInvalid
	}
	if d.Intent != nil && (in.Intent == nil || *d.Intent != *in.Intent) {
		return c.ErrConflict
	}
	d.State = in.State
	d.Reason = in.Reason
	if in.Intent != nil {
		d.Intent = in.Intent
	}
	if err = saveDelivery(ctx, tx, d); err != nil {
		return err
	}
	if err = event(ctx, tx, "delivery", m.ThreadID, m.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PublishReply(ctx context.Context, actor c.Principal, in c.PublishReply) (c.MessageReceipt, error) {
	var out c.MessageReceipt
	if in.RestoreEpoch != s.epoch {
		return out, c.ErrEpoch
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := authorize(ctx, tx, actor, "send")
	if err != nil || p.Kind != "agent" {
		return out, c.ErrForbidden
	}
	if err = placement(ctx, tx, p.ID, in.InstanceID, in.Generation); err != nil {
		return out, err
	}
	d, err := loadDelivery(ctx, tx, in.DeliveryID)
	if err != nil {
		return out, err
	}
	if d.AgentID != p.ID {
		return out, c.ErrForbidden
	}
	m, err := loadMessage(ctx, tx, d.MessageID)
	if err != nil {
		return out, err
	}
	if err = member(ctx, tx, p, m.ThreadID); err != nil {
		return out, err
	}
	if yes, e := request(ctx, tx, p.ID, in.RequestID, "reply", in, &out); yes || e != nil {
		if e != nil {
			return out, e
		}
		_ = tx.Rollback()
		return s.publishSource(ctx, p, out)
	}
	if d.State != "running" || d.Intent == nil || *d.Intent != in.Intent || d.ReplyMessageID != "" {
		return out, c.ErrConflict
	}
	out, err = s.insertMessage(ctx, tx, p, c.SendMessage{ThreadID: m.ThreadID, ParentMessageID: m.ID, Recipients: in.Recipients, Body: in.Body, RequestID: in.RequestID})
	if err != nil {
		return out, err
	}
	if err = s.stageSource(ctx, tx, p, &out, d.ID); err != nil {
		return out, err
	}
	if out.Source == nil {
		d.State = "replied"
	}
	d.ReplyMessageID = out.MessageID
	d.Reason = ""
	if err = saveDelivery(ctx, tx, d); err != nil {
		return out, err
	}
	if err = saveRequest(ctx, tx, p.ID, in.RequestID, "reply", in, out); err != nil {
		return out, err
	}
	if err = event(ctx, tx, "delivery", m.ThreadID, m.ID); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return s.publishSource(ctx, p, out)
}

func (s *Store) Pending(ctx context.Context, actor c.Principal, in c.PendingQuery) (c.DeliveryPage, error) {
	out := c.DeliveryPage{Deliveries: []c.Delivery{}, RestoreEpoch: s.epoch, Quiescent: admission(ctx, s.db) == c.ErrEpoch}
	p, err := authorize(ctx, s.db, actor, "read")
	if err != nil || p.Kind != "agent" {
		return out, c.ErrForbidden
	}
	// At most MaxPending active records exist per agent. Read that bounded set
	// before filtering engagements, so revoked scopes cannot leak receipts or
	// hide a later authorized delivery behind the page boundary.
	rows, err := s.db.QueryContext(ctx, "SELECT d.body,t.engagement FROM deliveries d JOIN messages m ON m.id=d.message_id JOIN threads t ON t.id=m.thread_id JOIN members x ON x.thread_id=m.thread_id AND x.principal_id=d.agent_id WHERE d.agent_id=? AND d.id>? AND d.state IN ('queued','received','running','admitted') ORDER BY d.id LIMIT ?", p.ID, in.After, c.MaxPending+1)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		var d c.Delivery
		var engagement string
		if err = rows.Scan(&raw, &engagement); err != nil {
			return out, err
		}
		if !slices.Contains(p.Engagements, engagement) {
			continue
		}
		if err = json.Unmarshal(raw, &d); err != nil {
			return out, err
		}
		if len(out.Deliveries) == c.MaxPageRecords {
			out.Next = out.Deliveries[len(out.Deliveries)-1].ID
			break
		}
		out.Deliveries = append(out.Deliveries, d)
	}
	return out, rows.Err()
}

func (s *Store) Messages(ctx context.Context, actor c.Principal, thread string, after int64) (c.MessagePage, error) {
	out := c.MessagePage{Messages: []c.Message{}}
	p, err := authorize(ctx, s.db, actor, "read")
	if err != nil {
		return out, err
	}
	if err = member(ctx, s.db, p, thread); err != nil {
		return out, err
	}
	if source, e := s.conversationSource(ctx, s.db, thread); e != nil {
		return out, e
	} else if source != nil {
		return s.sourceMessages(ctx, p, thread, *source, after)
	}
	return s.storedMessages(ctx, thread, after, false)
}
func (s *Store) storedMessages(ctx context.Context, thread string, after int64, legacy bool) (c.MessagePage, error) {
	out := c.MessagePage{Messages: []c.Message{}}
	query := "SELECT body FROM messages WHERE thread_id=? AND seq>?"
	if legacy {
		query += " AND json_extract(body,'$.source') IS NULL"
	}
	query += " ORDER BY seq LIMIT ?"
	rows, err := s.db.QueryContext(ctx, query, thread, after, c.MaxPageRecords+1)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	size := 128
	for rows.Next() {
		var raw []byte
		var m c.Message
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		if err = json.Unmarshal(raw, &m); err != nil {
			return out, err
		}
		if len(out.Messages) == c.MaxPageRecords || size+len(raw) > c.MaxPageBytes-128 {
			if len(out.Messages) == 0 {
				return out, c.ErrSize
			}
			out.Next = out.Messages[len(out.Messages)-1].Sequence
			break
		}
		size += len(raw) + 1
		out.Messages = append(out.Messages, m)
	}
	return out, rows.Err()
}

func (s *Store) ResolveRecipients(ctx context.Context, ids []string) ([]string, error) {
	out := []string{}
	for _, id := range ids {
		p, e := s.Principal(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, p.ID)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// OwnDelivery returns only the addressed agent's existing receipt, including
// terminal cancellation after thread membership is revoked. It exposes no body.
func (s *Store) OwnDelivery(ctx context.Context, actor c.Principal, id string) (c.Delivery, error) {
	p, e := authorize(ctx, s.db, actor, "read")
	if e != nil || p.Kind != "agent" {
		return c.Delivery{}, c.ErrForbidden
	}
	d, e := loadDelivery(ctx, s.db, id)
	if e != nil {
		return d, e
	}
	if d.AgentID != p.ID {
		return c.Delivery{}, c.ErrForbidden
	}
	return d, nil
}
