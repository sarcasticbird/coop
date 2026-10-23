package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

// conversationOwner is also available while quiescent. Mapping and cursor
// recovery cannot grant send, goal approval, or worker admission authority.
func conversationOwner(ctx context.Context, q queryer, actor c.Principal) (c.Principal, error) {
	p, err := principal(ctx, q, actor.ID)
	if err != nil || p.Kind != "owner" || !slices.Contains(p.Capabilities, "manage") {
		return p, c.ErrForbidden
	}
	return p, nil
}

func (s *Store) ConversationCursor(ctx context.Context, actor c.Principal) (c.ConversationCursor, error) {
	if _, err := conversationOwner(ctx, s.db, actor); err != nil {
		return c.ConversationCursor{}, err
	}
	return s.conversationCursor(ctx, s.db)
}
func (s *Store) conversationCursor(ctx context.Context, q queryer) (c.ConversationCursor, error) {
	out := c.ConversationCursor{RestoreEpoch: s.epoch, InstanceUID: s.conversations.InstanceUID, ProjectUID: s.conversations.ProjectUID}
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT epoch,body FROM conversation_cursor WHERE singleton=1").Scan(&out.AcceptedEpoch, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var cursor taskstore.EventCursor
	if err = json.Unmarshal(raw, &cursor); err != nil {
		return out, err
	}
	out.InstanceUID, out.ProjectUID, out.AfterEventID = cursor.InstanceUID, cursor.ProjectUID, cursor.AfterID
	out.AfterEventUID = cursor.AfterUID
	return out, nil
}

// verifyConversationCursor checks one immutable checkpoint, never a history scan.
// A numeric position alone cannot distinguish a provider rollback and ID reuse.
func (s *Store) verifyConversationCursor(ctx context.Context, cur taskstore.EventCursor) error {
	if cur.InstanceUID != s.conversations.InstanceUID || cur.ProjectUID != s.conversations.ProjectUID || cur.AfterID < 0 {
		return c.ErrEpoch
	}
	if (cur.AfterID == 0) != (cur.AfterUID == "") {
		return c.ErrEpoch
	}
	probe := cur
	if probe.AfterID > 0 {
		probe.AfterID--
	}
	probe.AfterUID = ""
	page, err := s.work.Events(ctx, probe, 1)
	if err != nil {
		return err
	}
	if page.Next.InstanceUID != cur.InstanceUID || page.Next.ProjectUID != cur.ProjectUID || page.Next.AfterID < cur.AfterID {
		return c.ErrEpoch
	}
	if cur.AfterID > 0 && (len(page.Events) != 1 || page.Events[0].ID != cur.AfterID || page.Events[0].UID != cur.AfterUID || page.Events[0].ProjectUID != cur.ProjectUID || page.Events[0].OriginInstanceUID == "") {
		return c.ErrEpoch
	}
	return nil
}

// ReacceptConversationCursor retains the exact cursor and immutable receipts.
// It only acknowledges the new master restore epoch after owner reconciliation.
func (s *Store) ReacceptConversationCursor(ctx context.Context, actor c.Principal, in c.ReacceptConversationCursor) (c.ConversationCursor, error) {
	var out c.ConversationCursor
	if _, err := conversationOwner(ctx, s.db, actor); err != nil {
		return out, err
	}
	if s.work == nil {
		return out, c.ErrUnavailable
	}
	if !validID(in.RequestID) || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 512 {
		return out, c.ErrInvalid
	}
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	// A bounded read validates the actual provider identity. It never consumes
	// events or changes the requested cursor during recovery.
	cur := taskstore.EventCursor{InstanceUID: in.Observed.InstanceUID, ProjectUID: in.Observed.ProjectUID, AfterID: in.Observed.AfterEventID, AfterUID: in.Observed.AfterEventUID}
	if in.ReconciledEventUID != "" {
		if cur.AfterID == 0 || cur.AfterUID != "" || !validID(in.ReconciledEventUID) {
			return out, c.ErrInvalid
		}
		cur.AfterUID = in.ReconciledEventUID
	}
	if err := s.verifyConversationCursor(ctx, cur); err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := conversationOwner(ctx, tx, actor)
	if err != nil {
		return out, err
	}
	if err = quiet(ctx, tx, in.Observed.RestoreEpoch, s.epoch); err != nil {
		return out, err
	}
	if yes, e := request(ctx, tx, p.ID, in.RequestID, "reaccept-conversation-cursor", in, &out); yes || e != nil {
		return out, e
	}
	observed, err := s.conversationCursor(ctx, tx)
	if err != nil {
		return out, err
	}
	if observed != in.Observed || observed.AcceptedEpoch == 0 {
		return out, c.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE conversation_cursor SET epoch=?,body=? WHERE singleton=1", s.epoch, encode(cur)); err != nil {
		return out, err
	}
	out = observed
	out.AcceptedEpoch = s.epoch
	out.AfterEventUID = cur.AfterUID
	if err = saveRequest(ctx, tx, p.ID, in.RequestID, "reaccept-conversation-cursor", in, out); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// LegacyMessages reads only the original, immutable local history. It remains
// available with the provider offline and never polls, publishes, or dispatches.
func (s *Store) LegacyMessages(ctx context.Context, actor c.Principal, thread string, after int64) (c.MessagePage, error) {
	out := c.MessagePage{Messages: []c.Message{}}
	if after < 0 {
		return out, c.ErrInvalid
	}
	p, err := authorize(ctx, s.db, actor, "read")
	if err != nil {
		return out, err
	}
	if err = member(ctx, s.db, p, thread); err != nil {
		return out, err
	}
	return s.storedMessages(ctx, thread, after, true)
}
