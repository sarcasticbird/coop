package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

type ConversationConfig struct {
	InstanceUID     string `toml:"instance_uid"`
	ProjectUID      string `toml:"project_uid"`
	BridgePrincipal string `toml:"bridge_principal"`
	IntakePrincipal string `toml:"intake_principal"`
	TeamThreadID    string `toml:"team_thread_id"`
	TeamIssueUID    string `toml:"team_issue_uid"`
}

const conversationSchema = `
CREATE TABLE IF NOT EXISTS conversation_sources(thread_id TEXT PRIMARY KEY REFERENCES threads(id),issue_uid TEXT UNIQUE NOT NULL,after_id INTEGER NOT NULL,body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS conversation_cursor(singleton INTEGER PRIMARY KEY CHECK(singleton=1),epoch INTEGER NOT NULL,body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS source_publications(message_id TEXT PRIMARY KEY REFERENCES messages(id),actor_id TEXT NOT NULL,reply_delivery TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,comment_uid TEXT NOT NULL DEFAULT '',instance_id TEXT NOT NULL DEFAULT '',generation INTEGER NOT NULL DEFAULT 0,body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS source_publication_keys(issue_uid TEXT NOT NULL,native_key TEXT NOT NULL,message_id TEXT UNIQUE NOT NULL REFERENCES source_publications(message_id),PRIMARY KEY(issue_uid,native_key));
CREATE INDEX IF NOT EXISTS source_publications_pending ON source_publications(comment_uid,created_at,message_id);
CREATE TABLE IF NOT EXISTS source_observations(event_uid TEXT PRIMARY KEY,issue_uid TEXT NOT NULL,message_id TEXT NOT NULL DEFAULT '',state TEXT NOT NULL,reason TEXT NOT NULL DEFAULT '',body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS source_publication_evidence(message_id TEXT PRIMARY KEY REFERENCES messages(id),body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS source_notification_intents(message_id TEXT NOT NULL REFERENCES messages(id),recipient TEXT NOT NULL,actor_id TEXT NOT NULL,marker TEXT UNIQUE NOT NULL,state TEXT NOT NULL,PRIMARY KEY(message_id,recipient));
CREATE TABLE IF NOT EXISTS source_notifications(event_id INTEGER PRIMARY KEY,message_id TEXT NOT NULL REFERENCES messages(id));
`

func (s *Store) ConfigureConversations(ctx context.Context, backend taskstore.WorkBackend, cfg ConversationConfig) error {
	if backend == nil || cfg.InstanceUID == "" || cfg.ProjectUID == "" || cfg.BridgePrincipal == "" || cfg.IntakePrincipal == "" {
		return c.ErrInvalid
	}
	bridge, err := s.Principal(ctx, cfg.BridgePrincipal)
	if err != nil || bridge.Kind != "service" || !slices.Contains(bridge.Capabilities, "send") || slices.Contains(bridge.Capabilities, "decide") || slices.Contains(bridge.Capabilities, "manage") {
		return c.ErrForbidden
	}
	for _, capability := range bridge.Capabilities {
		if capability != "read" && capability != "send" {
			return c.ErrForbidden
		}
	}
	intake, err := s.Principal(ctx, cfg.IntakePrincipal)
	if err != nil || intake.Kind != "agent" {
		return c.ErrForbidden
	}
	var cursor taskstore.EventCursor
	var epoch int64
	var raw []byte
	err = s.db.QueryRowContext(ctx, "SELECT epoch,body FROM conversation_cursor WHERE singleton=1").Scan(&epoch, &raw)
	if err == nil {
		if json.Unmarshal(raw, &cursor) != nil || cursor.InstanceUID != cfg.InstanceUID || cursor.ProjectUID != cfg.ProjectUID {
			return c.ErrEpoch
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = s.initializeTaskCreationCorrelation(ctx, cfg.ProjectUID); err != nil {
		return err
	}
	s.work = backend
	s.conversations = cfg
	return nil
}
func (s *Store) conversationSource(ctx context.Context, q queryer, thread string) (*c.ConversationSource, error) {
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT body FROM conversation_sources WHERE thread_id=?", thread).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var source c.ConversationSource
	if json.Unmarshal(raw, &source) != nil {
		return nil, c.ErrInvalid
	}
	if s.work == nil {
		return nil, c.ErrUnavailable
	}
	return &source, nil
}
func (s *Store) MapConversation(ctx context.Context, actor c.Principal, in c.MapConversation) (c.Thread, error) {
	var out c.Thread
	if s.work == nil {
		return out, c.ErrUnsupported
	}
	source := in.Source
	if source.Provider != "kata" || source.InstanceUID != s.conversations.InstanceUID || source.ProjectUID != s.conversations.ProjectUID || source.IssueUID == "" || source.CommentUID != "" || source.VerifiedSenderID != "" || source.ClaimedAuthor != "" || in.AfterEventID < 0 {
		return out, c.ErrInvalid
	}
	// Authorize before even inspecting a native issue so mapping is not a read oracle.
	p, err := conversationOwner(ctx, s.db, actor)
	if err != nil || p.Kind != "owner" {
		return out, c.ErrForbidden
	}
	if err = member(ctx, s.db, p, in.ThreadID); err != nil {
		return out, err
	}
	issue, err := s.work.InspectIssue(ctx, source.IssueUID)
	if err != nil {
		return out, err
	}
	if issue.UID != source.IssueUID || issue.ProjectUID != source.ProjectUID {
		return out, c.ErrForbidden
	}
	cursor := taskstore.EventCursor{InstanceUID: source.InstanceUID, ProjectUID: source.ProjectUID, AfterID: in.AfterEventID}
	if in.AfterEventID > 0 {
		page, e := s.work.Events(ctx, taskstore.EventCursor{InstanceUID: source.InstanceUID, ProjectUID: source.ProjectUID, AfterID: in.AfterEventID - 1}, 1)
		if e != nil {
			return out, e
		}
		if page.Next.InstanceUID != source.InstanceUID || page.Next.ProjectUID != source.ProjectUID || len(page.Events) != 1 || page.Events[0].ID != in.AfterEventID || page.Events[0].ProjectUID != source.ProjectUID || page.Events[0].UID == "" || page.Events[0].OriginInstanceUID == "" {
			return out, c.ErrConflict
		}
		cursor.AfterUID = page.Events[0].UID
	}
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err = conversationOwner(ctx, tx, actor)
	if err != nil || p.Kind != "owner" {
		return out, c.ErrForbidden
	}
	for _, id := range []string{p.ID, s.conversations.BridgePrincipal, s.conversations.IntakePrincipal} {
		v, e := principal(ctx, tx, id)
		if e != nil {
			return out, e
		}
		if e = member(ctx, tx, v, in.ThreadID); e != nil {
			return out, e
		}
	}
	if yes, e := request(ctx, tx, p.ID, in.RequestID, "map-conversation", in, &out); yes || e != nil {
		return out, e
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM threads WHERE id=?", in.ThreadID).Scan(&raw); err != nil {
		return out, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	if existing, e := s.conversationSource(ctx, tx, in.ThreadID); e != nil {
		return out, e
	} else if existing != nil {
		return out, c.ErrConflict
	}
	// A mapping is atomic with legacy sends. An accepted legacy delivery must
	// finish first; no cancelled turn, new thread identity, or history replay.
	var pending int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM deliveries d JOIN messages m ON m.id=d.message_id WHERE m.thread_id=? AND json_extract(m.body,'$.source') IS NULL AND d.state NOT IN ('replied','cancelled','suppressed','resumed','refused')", in.ThreadID).Scan(&pending); err != nil {
		return out, err
	}
	if pending > 0 {
		return out, c.ErrConflict
	}
	observed, e := s.conversationCursor(ctx, tx)
	if e != nil {
		return out, e
	}
	if observed.AcceptedEpoch != 0 && (observed.AcceptedEpoch != s.epoch || in.AfterEventID < observed.AfterEventID) {
		return out, c.ErrEpoch
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO conversation_sources(thread_id,issue_uid,after_id,body) VALUES(?,?,?,?)", in.ThreadID, source.IssueUID, in.AfterEventID, encode(source)); err != nil {
		return out, c.ErrConflict
	}
	out.Source = &source
	if _, err = tx.ExecContext(ctx, "UPDATE threads SET body=? WHERE id=?", encode(out), out.ID); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO conversation_cursor(singleton,epoch,body) VALUES(1,?,?)", s.epoch, encode(cursor)); err != nil {
		return out, err
	}
	if err = saveRequest(ctx, tx, p.ID, in.RequestID, "map-conversation", in, out); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (s *Store) stageSource(ctx context.Context, tx *sql.Tx, p c.Principal, out *c.MessageReceipt, replyDelivery string) error {
	m, err := loadMessage(ctx, tx, out.MessageID)
	if err != nil {
		return err
	}
	source, err := s.conversationSource(ctx, tx, m.ThreadID)
	if err != nil || source == nil {
		return err
	}
	if err = s.sourceEpoch(ctx, tx); err != nil {
		return err
	}
	source.VerifiedSenderID = p.ID
	m.Source = source
	out.Source = source
	out.Backend = "kata"
	if _, err = tx.ExecContext(ctx, "UPDATE messages SET body=? WHERE id=?", encode(m), m.ID); err != nil {
		return err
	}
	write := taskstore.CommentWrite{IssueUID: source.IssueUID, Body: m.Body, RequestID: s.id + ":" + m.ID}
	if m.ParentMessageID != "" {
		parent, e := loadMessage(ctx, tx, m.ParentMessageID)
		if e != nil {
			return e
		}
		if parent.Source != nil {
			if parent.Source.IssueUID != source.IssueUID {
				return c.ErrConflict
			}
			write.ReplyToUID = parent.Source.CommentUID
		}
	}
	for i, d := range out.Deliveries {
		if d.State == "queued" {
			d.State = "publishing"
			d.Reason = "source_publication_pending"
			if err = saveDelivery(ctx, tx, d); err != nil {
				return err
			}
			out.Deliveries[i] = d
		}
	}
	var frozen c.Placement
	if p.Kind == "agent" {
		var raw []byte
		if err = tx.QueryRowContext(ctx, "SELECT body FROM placements WHERE agent_id=?", p.ID).Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &frozen); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO source_publications(message_id,actor_id,reply_delivery,created_at,instance_id,generation,body) VALUES(?,?,?,?,?,?,?)", m.ID, p.ID, replyDelivery, s.now().UTC().Format(time.RFC3339Nano), frozen.InstanceID, frozen.Generation, encode(write))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO source_publication_keys(issue_uid,native_key,message_id) VALUES(?,?,?)", write.IssueUID, taskstore.CommentKey(write.RequestID), m.ID)
	return err
}
func (s *Store) publishSource(ctx context.Context, actor c.Principal, out c.MessageReceipt) (c.MessageReceipt, error) {
	if out.Source == nil {
		return out, nil
	}
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	return s.publishSourceLocked(ctx, actor, out)
}
func (s *Store) publishSourceLocked(ctx context.Context, actor c.Principal, out c.MessageReceipt) (c.MessageReceipt, error) {
	if s.work == nil {
		return out, c.ErrUnavailable
	}
	if err := s.sourceEpoch(ctx, s.db); err != nil {
		return out, err
	}
	p, err := authorize(ctx, s.db, actor, "send")
	if err != nil {
		return out, err
	}
	m, err := loadMessage(ctx, s.db, out.MessageID)
	if err != nil {
		return out, err
	}
	if err = member(ctx, s.db, p, m.ThreadID); err != nil {
		return out, err
	}
	var raw []byte
	var reply, created, known, instance string
	var generation int64
	if err = s.db.QueryRowContext(ctx, "SELECT body,reply_delivery,created_at,comment_uid,instance_id,generation FROM source_publications WHERE message_id=? AND actor_id=?", m.ID, p.ID).Scan(&raw, &reply, &created, &known, &instance, &generation); err != nil {
		return out, err
	}
	if known != "" {
		return s.MessageStatus(ctx, p, m.ID)
	}
	if err = s.checkSourceReply(ctx, m, reply); err != nil {
		return out, err
	}
	if instance != "" {
		if err = placement(ctx, s.db, p.ID, instance, generation); err != nil {
			return out, err
		}
	}
	for _, id := range m.Recipients {
		recipient, e := s.Principal(ctx, id)
		if e != nil {
			return out, e
		}
		if e = member(ctx, s.db, recipient, m.ThreadID); e != nil {
			return out, e
		}
	}
	// Native idempotency retention is finite. Keep old uncertain intents explicit;
	// they require source-event reconciliation instead of a fresh blind POST.
	var evidence []byte
	evidenceErr := s.db.QueryRowContext(ctx, "SELECT body FROM source_publication_evidence WHERE message_id=?", m.ID).Scan(&evidence)
	if evidenceErr != nil && !errors.Is(evidenceErr, sql.ErrNoRows) {
		return out, evidenceErr
	}
	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return out, err
	}
	if s.now().Sub(at) > 23*time.Hour && len(evidence) == 0 {
		return out, c.ErrConflict
	}
	var write taskstore.CommentWrite
	if err = json.Unmarshal(raw, &write); err != nil {
		return out, err
	}
	var comment taskstore.Comment
	if len(evidence) > 0 {
		err = json.Unmarshal(evidence, &comment)
	} else {
		if err = s.checkSourceReply(ctx, m, reply); err != nil {
			return out, err
		}
		comment, err = s.work.AppendComment(ctx, p, write)
	}
	if err != nil {
		return out, err
	}
	if comment.IssueUID != m.Source.IssueUID || comment.UID == "" {
		return out, c.ErrConflict
	}
	// A successful append is durable evidence even if cancellation or another
	// local guard prevents finalization. Never depend on another native POST.
	if _, err = s.db.ExecContext(ctx, "INSERT OR REPLACE INTO source_publication_evidence(message_id,body) VALUES(?,?)", m.ID, encode(comment)); err != nil {
		return out, err
	}
	if err = s.checkSourceReply(ctx, m, reply); err != nil {
		return out, err
	}
	for _, id := range m.Recipients {
		r, e := s.Principal(ctx, id)
		if e != nil {
			return out, e
		}
		if e = member(ctx, s.db, r, m.ThreadID); e != nil {
			return out, e
		}
		if r.Kind != "agent" {
			continue
		}
		if _, e = s.db.ExecContext(ctx, "INSERT OR IGNORE INTO source_notification_intents(message_id,recipient,actor_id,marker,state) VALUES(?,?,?,?,?)", m.ID, r.ID, p.ID, newID("notify"), "attempting"); e != nil {
			return out, e
		}
		var marker string
		if e = s.db.QueryRowContext(ctx, "SELECT marker FROM source_notification_intents WHERE message_id=? AND recipient=?", m.ID, r.ID).Scan(&marker); e != nil {
			return out, e
		}
		notification, e := s.work.Notify(ctx, p, taskstore.NotificationWrite{IssueUID: m.Source.IssueUID, Recipient: r.ID, Message: "Coop message " + m.ID, Correlation: marker})
		if e != nil {
			return out, e
		}
		if notification.EventID > 0 {
			if _, e = s.db.ExecContext(ctx, "INSERT OR IGNORE INTO source_notifications(event_id,message_id) VALUES(?,?)", notification.EventID, m.ID); e != nil {
				return out, e
			}
			if _, e = s.db.ExecContext(ctx, "UPDATE source_notification_intents SET state='succeeded' WHERE message_id=? AND recipient=?", m.ID, r.ID); e != nil {
				return out, e
			}
		}
	}
	return s.finalizeSource(ctx, p, m, comment, reply)
}

// Retiring an intent preserves its request, native key, snapshot and any
// uncertain-write evidence. It only cancels dispatch and removes blind retries.
func sourceReplyActive(ctx context.Context, tx *sql.Tx, m c.Message, reply string) (bool, error) {
	if reply == "" {
		return true, nil
	}
	var retired int
	key := "source-publication-retired:" + m.ID
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM meta WHERE key=?", key).Scan(&retired); err != nil {
		return false, err
	}
	d, err := loadDelivery(ctx, tx, reply)
	if err != nil {
		return false, err
	}
	if retired == 0 && d.State == "running" && d.ReplyMessageID == m.ID {
		return true, nil
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO meta(key,value) VALUES(?,?)", key, string(encode(d))); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE deliveries SET state='cancelled',body=json_set(body,'$.state','cancelled','$.reason','source_reply_retired') WHERE message_id=? AND state='publishing'", m.ID); err != nil {
		return false, err
	}
	return false, nil
}
func (s *Store) checkSourceReply(ctx context.Context, m c.Message, reply string) error {
	if reply == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	active, err := sourceReplyActive(ctx, tx, m, reply)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if !active {
		return c.ErrConflict
	}
	return nil
}
func (s *Store) finalizeSource(ctx context.Context, p c.Principal, m c.Message, comment taskstore.Comment, reply string) (c.MessageReceipt, error) {
	var out c.MessageReceipt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	active, err := sourceReplyActive(ctx, tx, m, reply)
	if err != nil {
		return out, err
	}
	if !active {
		if err = tx.Commit(); err != nil {
			return out, err
		}
		return out, c.ErrConflict
	}
	current, err := authorize(ctx, tx, p, "send")
	if err != nil {
		return out, err
	}
	if err = member(ctx, tx, current, m.ThreadID); err != nil {
		return out, err
	}
	m.Source.CommentUID = comment.UID
	m.Source.Revision = comment.Revision
	m.Source.ClaimedAuthor = comment.Author
	if _, err = tx.ExecContext(ctx, "UPDATE messages SET body=? WHERE id=?", encode(m), m.ID); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE source_publications SET comment_uid=? WHERE message_id=?", comment.UID, m.ID); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT body FROM deliveries WHERE message_id=?", m.ID)
	if err != nil {
		return out, err
	}
	var ds []c.Delivery
	for rows.Next() {
		var raw []byte
		var d c.Delivery
		if err = rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return out, err
		}
		if err = json.Unmarshal(raw, &d); err != nil {
			_ = rows.Close()
			return out, err
		}
		ds = append(ds, d)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return out, err
	}
	if closeErr != nil {
		return out, closeErr
	}
	for i, d := range ds {
		if d.State == "publishing" {
			r, e := principal(ctx, tx, d.AgentID)
			if e != nil {
				return out, e
			}
			if e = member(ctx, tx, r, m.ThreadID); e != nil {
				d.State = "cancelled"
				d.Reason = "membership_revoked"
			} else {
				d.State = "queued"
				d.Reason = ""
			}
			if err = saveDelivery(ctx, tx, d); err != nil {
				return out, err
			}
			ds[i] = d
		}
	}
	if reply != "" {
		d, e := loadDelivery(ctx, tx, reply)
		if e != nil {
			return out, e
		}
		if d.ReplyMessageID != m.ID {
			return out, c.ErrConflict
		}
		if d.State != "running" {
			return out, c.ErrConflict
		}
		d.State = "replied"
		d.Reason = ""
		if err = saveDelivery(ctx, tx, d); err != nil {
			return out, err
		}
	}
	out = c.MessageReceipt{MasterID: s.id, RestoreEpoch: s.epoch, Backend: "kata", MessageID: m.ID, Sequence: m.Sequence, Deliveries: ds, Source: m.Source}
	if _, err = tx.ExecContext(ctx, "UPDATE requests SET result=? WHERE actor=? AND request_id=?", encode(out), p.ID, m.RequestID); err != nil {
		return out, err
	}
	if err = event(ctx, tx, "message", m.ThreadID, m.ID); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (s *Store) sourceMessages(ctx context.Context, p c.Principal, thread string, source c.ConversationSource, after int64) (c.MessagePage, error) {
	return s.sourceMessagePage(ctx, p, thread, source, after, c.MaxPageRecords)
}

func (s *Store) sourceMessagePage(ctx context.Context, p c.Principal, thread string, source c.ConversationSource, after int64, limit int) (c.MessagePage, error) {
	out := c.MessagePage{Messages: []c.Message{}}
	if after < 0 {
		return out, c.ErrInvalid
	}
	if s.work == nil {
		return out, c.ErrUnavailable
	}
	page, err := s.work.Comments(ctx, taskstore.CommentQuery{IssueUID: source.IssueUID, Cursor: taskstore.EventCursor{InstanceUID: source.InstanceUID, ProjectUID: source.ProjectUID, AfterID: after}, Limit: limit})
	if err != nil {
		return out, err
	}
	// Validate again after the provider read; revocation during I/O must fail closed.
	if page.Next.InstanceUID != source.InstanceUID || page.Next.ProjectUID != source.ProjectUID || page.Next.AfterID < after || len(page.Comments) > limit || (len(page.Comments) > 0 && page.Next.AfterID == after) {
		return out, c.ErrConflict
	}
	current, err := authorize(ctx, s.db, p, "read")
	if err != nil {
		return out, err
	}
	if err = member(ctx, s.db, current, thread); err != nil {
		return out, err
	}
	for _, x := range page.Comments {
		if x.IssueUID != source.IssueUID || x.EventID <= after || x.EventID > page.Next.AfterID {
			return out, c.ErrConflict
		}
		src := source
		src.CommentUID = x.UID
		src.Revision = x.Revision
		src.ClaimedAuthor = x.Author
		m := c.Message{ID: x.UID, ThreadID: thread, AuthorKind: "external", AuthorID: x.Author, Body: x.Body, Sequence: x.EventID, CreatedAt: x.CreatedAt, Source: &src, Recipients: []string{}}
		var raw []byte
		e := s.db.QueryRowContext(ctx, "SELECT m.body FROM messages m WHERE m.thread_id=? AND json_extract(m.body,'$.source.comment_uid')=?", thread, x.UID).Scan(&raw)
		if e == nil {
			var stored c.Message
			if json.Unmarshal(raw, &stored) != nil {
				return out, c.ErrInvalid
			}
			m.ID = stored.ID
			if x.Body == stored.Body && x.EditedAt == nil && x.Revision == stored.Source.Revision {
				m.AuthorID = stored.AuthorID
				m.AuthorKind = stored.AuthorKind
				m.Source.VerifiedSenderID = stored.Source.VerifiedSenderID
			}
			m.Recipients = stored.Recipients
			m.ParentMessageID = stored.ParentMessageID
		} else if !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
		out.Messages = append(out.Messages, m)
	}
	if page.Next.AfterID > after {
		out.Next = page.Next.AfterID
	}
	// Include the real HTTP envelope and encoder newline. If a provider page
	// exceeds the budget, reread a smaller event window at the SAME cursor.
	// Returning only a comment prefix is unsafe: provider deduplication can move
	// a comment's event ID past an unsent comment or a later edit.
	raw, err := json.Marshal(Envelope{MasterID: s.id, RestoreEpoch: s.epoch, Data: out})
	if err != nil {
		return c.MessagePage{}, err
	}
	if len(raw)+1 > c.MaxPageBytes {
		if limit == 1 {
			return c.MessagePage{}, c.ErrSize
		}
		return s.sourceMessagePage(ctx, p, thread, source, after, limit/2)
	}
	return out, nil
}

// RecoverConversationPublications reuses existing immutable publication intents.
// A failed provider response leaves the same intent for the next serving tick.
func (s *Store) RecoverConversationPublications(ctx context.Context) error {
	if s.work == nil {
		return nil
	}
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	rows, err := s.pendingSourcePublications(ctx, s.sourceRecoveryAfter)
	if err != nil {
		return err
	}
	entries, err := readPendingSourcePublications(rows)
	if err != nil {
		return err
	}
	if len(entries) == 0 && s.sourceRecoveryAfter.messageID != "" {
		s.sourceRecoveryAfter = publicationRecoveryCursor{}
		rows, err = s.pendingSourcePublications(ctx, s.sourceRecoveryAfter)
		if err != nil {
			return err
		}
		entries, err = readPendingSourcePublications(rows)
		if err != nil {
			return err
		}
	}
	var result error
	for _, entry := range entries {
		// Advance the attempt page even on failure; failed old intents stay durable
		// and are retried after wrapping, without starving the rest of the backlog.
		s.sourceRecoveryAfter = publicationRecoveryCursor{createdAt: entry.createdAt, messageID: entry.id}
		m, e := loadMessage(ctx, s.db, entry.id)
		if e != nil {
			return e
		}
		_, e = s.publishSourceLocked(ctx, c.Principal{ID: entry.actor}, c.MessageReceipt{MessageID: m.ID, Source: m.Source})
		result = errors.Join(result, e)
	}
	return result
}

type publicationRecoveryCursor struct{ createdAt, messageID string }
type pendingSourcePublication struct{ id, actor, createdAt string }

func (s *Store) pendingSourcePublications(ctx context.Context, after publicationRecoveryCursor) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, "SELECT message_id,actor_id,created_at FROM source_publications WHERE comment_uid='' AND NOT EXISTS(SELECT 1 FROM meta WHERE key='source-publication-retired:'||message_id) AND (created_at,message_id)>(?,?) ORDER BY created_at,message_id LIMIT ?", after.createdAt, after.messageID, c.MaxPending)
}
func readPendingSourcePublications(rows *sql.Rows) ([]pendingSourcePublication, error) {
	defer func() { _ = rows.Close() }()
	var entries []pendingSourcePublication
	for rows.Next() {
		var p pendingSourcePublication
		if err := rows.Scan(&p.id, &p.actor, &p.createdAt); err != nil {
			return nil, err
		}
		entries = append(entries, p)
	}
	return entries, rows.Err()
}

func (s *Store) initializeConversationPublications(ctx context.Context) error {
	// Existing immutable intents predate the exact lookup table. Backfill once
	// at store initialization in bounded batches, before any event can dispatch.
	after := ""
	for {
		rows, err := s.db.QueryContext(ctx, "SELECT p.message_id,p.body FROM source_publications p LEFT JOIN source_publication_keys k ON k.message_id=p.message_id WHERE p.message_id>? AND k.message_id IS NULL ORDER BY p.message_id LIMIT ?", after, c.MaxPageRecords)
		if err != nil {
			return err
		}
		type entry struct {
			id    string
			write taskstore.CommentWrite
		}
		var entries []entry
		for rows.Next() {
			var e entry
			var raw []byte
			if err = rows.Scan(&e.id, &raw); err != nil {
				_ = rows.Close()
				return err
			}
			if err = json.Unmarshal(raw, &e.write); err != nil {
				_ = rows.Close()
				return err
			}
			entries = append(entries, e)
		}
		err = rows.Err()
		err = errors.Join(err, rows.Close())
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if _, err = tx.ExecContext(ctx, "INSERT INTO source_publication_keys(issue_uid,native_key,message_id) VALUES(?,?,?)", e.write.IssueUID, taskstore.CommentKey(e.write.RequestID), e.id); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		after = entries[len(entries)-1].id
	}
}

// ConversationActive excludes unreconciled restores from clients' entry routing.
func (s *Store) ConversationActive(ctx context.Context) bool {
	return s.work != nil && s.sourceEpoch(ctx, s.db) == nil && admission(ctx, s.db) == nil
}

func (s *Store) sourceEpoch(ctx context.Context, q queryer) error {
	var epoch int64
	var raw []byte
	if err := q.QueryRowContext(ctx, "SELECT epoch,body FROM conversation_cursor WHERE singleton=1").Scan(&epoch, &raw); err != nil {
		return err
	}
	var cursor taskstore.EventCursor
	if epoch != s.epoch || json.Unmarshal(raw, &cursor) != nil || cursor.AfterID < 0 || (cursor.AfterID == 0) != (cursor.AfterUID == "") || cursor.InstanceUID != s.conversations.InstanceUID || cursor.ProjectUID != s.conversations.ProjectUID {
		return c.ErrEpoch
	}
	return nil
}

// sourceParent retains a canonical comment snapshot only when a gateway reply
// needs ancestry. Reading a native bot comment does not create a delivery.
func (s *Store) sourceParent(ctx context.Context, tx *sql.Tx, p c.Principal, thread, id string) (c.Message, error) {
	parent, err := loadMessage(ctx, tx, id)
	if !errors.Is(err, c.ErrNotFound) {
		return parent, err
	}
	source, err := s.conversationSource(ctx, tx, thread)
	if err != nil {
		return parent, err
	}
	if source == nil {
		return parent, c.ErrNotFound
	}
	if err = member(ctx, tx, p, thread); err != nil {
		return parent, err
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM messages WHERE thread_id=? AND json_extract(body,'$.source.comment_uid')=?", thread, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &parent)
		return parent, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return parent, err
	}
	page, err := s.work.Comments(ctx, taskstore.CommentQuery{IssueUID: source.IssueUID, CommentUID: id, Limit: 1})
	if err != nil {
		return parent, err
	}
	if len(page.Comments) != 1 || page.Comments[0].UID != id || page.Comments[0].IssueUID != source.IssueUID {
		return parent, c.ErrNotFound
	}
	comment := page.Comments[0]
	bridge, err := authorize(ctx, tx, c.Principal{ID: s.conversations.BridgePrincipal}, "send")
	if err != nil {
		return parent, err
	}
	if err = member(ctx, tx, bridge, thread); err != nil {
		return parent, err
	}
	out, err := s.insertMessage(ctx, tx, bridge, c.SendMessage{ThreadID: thread, Body: comment.Body, RequestID: "source-context-" + id})
	if err != nil {
		return parent, err
	}
	parent, err = loadMessage(ctx, tx, out.MessageID)
	if err != nil {
		return parent, err
	}
	source.CommentUID = id
	source.ClaimedAuthor = comment.Author
	source.Revision = comment.Revision
	source.VerifiedSenderID = ""
	parent.Source = source
	_, err = tx.ExecContext(ctx, "UPDATE messages SET body=? WHERE id=?", encode(parent), parent.ID)
	return parent, err
}
