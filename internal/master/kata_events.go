package master

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

// PollConversations observes one bounded native event page. Immutable dispatch
// snapshots, recipient reservations and cursor advancement share one transaction.
func (s *Store) PollConversations(ctx context.Context) error {
	if s.work == nil {
		return nil
	}
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	if err := admission(ctx, s.db); err != nil {
		return err
	}
	var raw []byte
	var epoch int64
	err := s.db.QueryRowContext(ctx, "SELECT epoch,body FROM conversation_cursor WHERE singleton=1").Scan(&epoch, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if epoch != s.epoch {
		return c.ErrEpoch
	}
	var cursor taskstore.EventCursor
	if json.Unmarshal(raw, &cursor) != nil {
		return c.ErrInvalid
	}
	if cursor.AfterID > 0 || cursor.AfterUID != "" {
		if err = s.verifyConversationCursor(ctx, cursor); err != nil {
			return err
		}
	}
	page, err := s.work.Events(ctx, cursor, c.MaxPageRecords)
	if err != nil {
		return err
	}
	if page.Next.InstanceUID != cursor.InstanceUID || page.Next.ProjectUID != cursor.ProjectUID || page.Next.AfterID < cursor.AfterID || len(page.Events) > c.MaxPageRecords {
		return c.ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	bridge, err := authorize(ctx, tx, c.Principal{ID: s.conversations.BridgePrincipal}, "send")
	if err != nil || bridge.Kind != "service" {
		return c.ErrForbidden
	}
	last := cursor.AfterID
	for _, e := range page.Events {
		if e.ID <= last || e.ID > page.Next.AfterID || e.ProjectUID != cursor.ProjectUID || e.OriginInstanceUID == "" || e.UID == "" {
			return c.ErrConflict
		}
		last = e.ID
		page.Next.AfterUID = e.UID
		var observed int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM source_observations WHERE event_uid=?", e.UID).Scan(&observed); err != nil {
			return err
		}
		if observed > 0 {
			continue
		}
		var thread string
		var mappingRaw []byte
		var after int64
		err = tx.QueryRowContext(ctx, "SELECT thread_id,after_id,body FROM conversation_sources WHERE issue_uid=?", e.IssueUID).Scan(&thread, &after, &mappingRaw)
		if errors.Is(err, sql.ErrNoRows) {
			if e.IssueUID != "" && (e.Type == "issue.created" || e.Type == "issue.commented" || e.Type == "issue.metadata_updated") {
				if err = s.observeTaskCreation(ctx, tx, e); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "INSERT INTO source_observations(event_uid,issue_uid,state,body) VALUES(?,?,?,?)", e.UID, e.IssueUID, "pending_intake", encode(e)); err != nil {
					return err
				}
			}
			continue
		}
		if err != nil {
			return err
		}
		if e.ID <= after {
			continue
		}
		var source c.ConversationSource
		if json.Unmarshal(mappingRaw, &source) != nil {
			return c.ErrInvalid
		}
		source.Revision = e.UID
		source.ClaimedAuthor = e.Actor
		source.VerifiedSenderID = ""
		if _, err = tx.ExecContext(ctx, "SAVEPOINT source_event"); err != nil {
			return err
		}
		// Thread revocation is scoped to this observation, like recipient
		// revocation. It must not prevent other mapped issues from progressing.
		messageID := ""
		observeErr := member(ctx, tx, bridge, thread)
		if observeErr == nil {
			messageID, observeErr = s.observeSourceEvent(ctx, tx, bridge, thread, source, e)
		}
		state, reason := "observed", ""
		if observeErr != nil {
			if !errors.Is(observeErr, c.ErrInvalid) && !errors.Is(observeErr, c.ErrForbidden) && !errors.Is(observeErr, c.ErrNotFound) && !errors.Is(observeErr, c.ErrSize) {
				return observeErr
			}
			if _, err = tx.ExecContext(ctx, "ROLLBACK TO source_event"); err != nil {
				return err
			}
			state = "rejected"
			reason = ErrorCode(observeErr)
			messageID = ""
		}
		if _, err = tx.ExecContext(ctx, "RELEASE source_event"); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO source_observations(event_uid,issue_uid,message_id,state,reason,body) VALUES(?,?,?,?,?,?)", e.UID, e.IssueUID, messageID, state, reason, encode(e)); err != nil {
			return err
		}
	}
	if page.Next.AfterID != last {
		return c.ErrConflict
	}
	if len(page.Events) == 0 {
		page.Next.AfterUID = cursor.AfterUID
	}
	if _, err = tx.ExecContext(ctx, "UPDATE conversation_cursor SET body=? WHERE singleton=1 AND epoch=?", encode(page.Next), s.epoch); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.routePendingIntakeLocked(ctx)
}
func (s *Store) observeSourceEvent(ctx context.Context, tx *sql.Tx, bridge c.Principal, thread string, source c.ConversationSource, e taskstore.WorkEvent) (string, error) {
	comment, isComment := taskstore.EventComment(e)
	if e.Type == "issue.comment_edited" || e.Type == "comment.edited" {
		return "", nil
	}
	if e.Type == "issue.commented" && !isComment {
		return "", c.ErrInvalid
	}
	var recipients []string
	body := ""
	parentID := ""
	if isComment {
		// Echoes of a known publication cannot reserve another model turn.
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM source_publications WHERE comment_uid=?", comment.UID).Scan(&count); err != nil {
			return "", err
		}
		if count > 0 {
			return "", nil
		}
		// An uncertain POST is also correlated by its stable native idempotency key,
		// never its body or claimed actor. The normal outbox retry reconciles it.
		var payload struct {
			Key string `json:"idempotency_key"`
		}
		if err := json.Unmarshal(e.Payload, &payload); err == nil && payload.Key != "" {
			var messageID string
			err := tx.QueryRowContext(ctx, "SELECT message_id FROM source_publication_keys WHERE issue_uid=? AND native_key=?", e.IssueUID, payload.Key).Scan(&messageID)
			if err == nil {
				if _, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO source_publication_evidence(message_id,body) VALUES(?,?)", messageID, encode(comment)); err != nil {
					return "", err
				}
				return "", nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
		}
		source.CommentUID = comment.UID
		body = comment.Body
		if comment.ReplyToUID != "" {
			var raw []byte
			err := tx.QueryRowContext(ctx, "SELECT body FROM messages WHERE thread_id=? AND json_extract(body,'$.source.comment_uid')=?", thread, comment.ReplyToUID).Scan(&raw)
			if err == nil {
				var parent c.Message
				if json.Unmarshal(raw, &parent) != nil {
					return "", c.ErrInvalid
				}
				parentID = parent.ID
				if parent.AuthorKind == "agent" {
					recipients = []string{parent.AuthorID}
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
		}
		if len(recipients) == 0 {
			claimed, err := principal(ctx, tx, e.Actor)
			// Native author labels are external data. Known bot labels do not fan out
			// unaddressed comments, and no label grants goal/engineering authority.
			if err == nil && claimed.Kind == "agent" {
				return "", nil
			}
			if err != nil && !errors.Is(err, c.ErrNotFound) && !errors.Is(err, c.ErrForbidden) {
				return "", err
			}
			recipients = []string{s.conversations.IntakePrincipal}
		}
	} else if e.Type == "issue.metadata_updated" {
		var payload struct {
			Diff map[string]struct {
				To *struct {
					From     string `json:"from"`
					Message  string `json:"message"`
					Teammate string `json:"teammate"`
				} `json:"to"`
			} `json:"diff"`
		}
		if json.Unmarshal(e.Payload, &payload) != nil {
			return "", c.ErrInvalid
		}
		for key, change := range payload.Diff {
			if !strings.HasPrefix(key, "notify.") || change.To == nil {
				continue
			}
			decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "notify."))
			if err != nil {
				return "", c.ErrInvalid
			}
			id := string(decoded)
			recipient, err := principal(ctx, tx, id)
			if err != nil {
				return "", err
			}
			if recipient.Kind != "agent" {
				continue
			}
			if err = member(ctx, tx, recipient, thread); err != nil {
				return "", err
			}
			var correlated int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM source_notifications WHERE event_id=?", e.ID).Scan(&correlated); err != nil {
				return "", err
			}
			if change.To.Teammate != "" {
				var messageID string
				err = tx.QueryRowContext(ctx, "SELECT i.message_id FROM source_notification_intents i JOIN messages m ON m.id=i.message_id WHERE i.marker=? AND m.thread_id=? AND i.actor_id=? AND i.recipient=? AND i.state='attempting'", change.To.Teammate, thread, change.To.From, recipient.ID).Scan(&messageID)
				if err == nil {
					correlated++
					if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO source_notifications(event_id,message_id) VALUES(?,?)", e.ID, messageID); err != nil {
						return "", err
					}
					if _, err = tx.ExecContext(ctx, "UPDATE source_notification_intents SET state='observed' WHERE message_id=? AND recipient=?", messageID, recipient.ID); err != nil {
						return "", err
					}
				} else if !errors.Is(err, sql.ErrNoRows) {
					return "", err
				}
			}
			if correlated > 0 {
				continue
			}
			recipients = append(recipients, recipient.ID)
			if body != "" {
				body += "\n"
			}
			body += change.To.Message
		}
	} else {
		return "", nil
	}
	if len(recipients) == 0 || strings.TrimSpace(body) == "" {
		return "", nil
	}
	// A source author must never wake itself through explicit native labels.
	filtered := recipients[:0]
	for _, id := range recipients {
		if id != e.Actor {
			filtered = append(filtered, id)
		}
	}
	recipients = filtered
	if len(recipients) == 0 {
		return "", nil
	}
	out, err := s.insertMessage(ctx, tx, bridge, c.SendMessage{ThreadID: thread, Body: body, Recipients: recipients, ParentMessageID: parentID, RequestID: "source-" + e.UID})
	if err != nil {
		return "", err
	}
	m, err := loadMessage(ctx, tx, out.MessageID)
	if err != nil {
		return "", err
	}
	m.Source = &source
	if _, err = tx.ExecContext(ctx, "UPDATE messages SET body=? WHERE id=?", encode(m), m.ID); err != nil {
		return "", err
	}
	return m.ID, nil
}

// PendingConversationIntake exposes only bounded immutable native observations
// to the master's later planner intake step. It grants no assignment authority.
func (s *Store) PendingConversationIntake(ctx context.Context, after string, limit int) ([]taskstore.WorkEvent, error) {
	if limit < 1 || limit > c.MaxPageRecords {
		return nil, c.ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, "SELECT body FROM source_observations WHERE state='pending_intake' AND event_uid>? ORDER BY event_uid LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []taskstore.WorkEvent{}
	for rows.Next() {
		var raw []byte
		var e taskstore.WorkEvent
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ConversationObservations provides scoped diagnostics for rejected native events.
func (s *Store) ConversationObservations(ctx context.Context, actor c.Principal, after string) (c.ConversationObservationPage, error) {
	out := c.ConversationObservationPage{Observations: []c.ConversationObservation{}}
	p, err := authorize(ctx, s.db, actor, "read")
	if err != nil {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT o.event_uid,o.issue_uid,o.state,o.reason,o.body,t.id,t.engagement FROM source_observations o JOIN conversation_sources src ON src.issue_uid=o.issue_uid JOIN threads t ON t.id=src.thread_id JOIN members m ON m.thread_id=t.id AND m.principal_id=? WHERE o.event_uid>? AND o.state='rejected' ORDER BY o.event_uid LIMIT ?", p.ID, after, c.MaxPageRecords)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var observation c.ConversationObservation
		var raw []byte
		var engagement string
		if err = rows.Scan(&observation.EventUID, &observation.IssueUID, &observation.State, &observation.Reason, &raw, &observation.ThreadID, &engagement); err != nil {
			return out, err
		}
		out.Next = observation.EventUID
		if !slices.Contains(p.Engagements, engagement) {
			continue
		}
		var e taskstore.WorkEvent
		if err = json.Unmarshal(raw, &e); err != nil {
			return out, err
		}
		observation.Kind = e.Type
		out.Observations = append(out.Observations, observation)
	}
	return out, rows.Err()
}
