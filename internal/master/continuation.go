package master

import (
	"context"
	"database/sql"
	"slices"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func validateContinuation(ctx context.Context, q queryer, m c.Message) error {
	in := m.Continuation
	if in == nil || m.Imported || c.ValidateContinuationTarget(in.Target) != nil || !validID(in.SourceMessageID) || len(m.Body) > 1024 {
		return c.ErrInvalid
	}
	sender, err := principal(ctx, q, m.AuthorID)
	if err != nil {
		return err
	}
	if sender.Kind != "owner" && !slices.Contains(sender.Capabilities, "task_continue") {
		return c.ErrForbidden
	}
	if !slices.Contains(sender.Capabilities, "send") {
		return c.ErrForbidden
	}
	if err = member(ctx, q, sender, m.ThreadID); err != nil {
		return err
	}
	if len(m.Recipients) != 1 || m.Recipients[0] == sender.ID {
		return c.ErrInvalid
	}
	receiver, err := principal(ctx, q, m.Recipients[0])
	if err != nil {
		return err
	}
	if receiver.Kind != "agent" {
		return c.ErrForbidden
	}
	if err = member(ctx, q, receiver, m.ThreadID); err != nil {
		return err
	}
	if err = placement(ctx, q, receiver.ID, in.Target.InstanceID, in.Target.Generation); err != nil {
		return err
	}
	source, err := loadMessage(ctx, q, in.SourceMessageID)
	if err != nil {
		return err
	}
	if source.ThreadID != m.ThreadID || source.Imported || source.Continuation != nil {
		return c.ErrForbidden
	}
	return nil
}
func (s *Store) ContinueTask(ctx context.Context, p c.Principal, in c.ContinueTaskRequest) (c.MessageReceipt, error) {
	msg, err := c.ContinuationMessage(in)
	if err != nil {
		return c.MessageReceipt{}, err
	}
	return s.Send(ctx, p, msg)
}

// Continuation rechecks current sender authority, source membership and target placement.
func (s *Store) Continuation(ctx context.Context, actor c.Principal, id string) (c.Message, error) {
	m, err := s.Message(ctx, actor, id)
	if err != nil {
		return m, err
	}
	p, err := authorize(ctx, s.db, actor, "read")
	if err != nil {
		return m, err
	}
	if p.Kind != "agent" || len(m.Recipients) != 1 || m.Recipients[0] != p.ID {
		return m, c.ErrForbidden
	}
	if err = admission(ctx, s.db); err != nil {
		return m, err
	}
	return m, validateContinuation(ctx, s.db, m)
}
func (s *Store) setContinuationDelivery(ctx context.Context, tx *sql.Tx, m c.Message, d c.Delivery, in c.DeliveryUpdate) error {
	allowed := false
	switch in.State {
	case "received":
		allowed = d.State == "queued" && in.Intent == nil
	case "admitted":
		allowed = d.State == "received" && in.Intent == nil
		if allowed {
			if err := validateContinuation(ctx, tx, m); err != nil {
				return err
			}
		}
	case "resumed":
		allowed = d.State == "admitted"
		if in.Intent == nil || !c.ValidContinuationIntent(in.Intent.SessionID, in.Intent.MessageID) || in.Intent.SessionID != m.Continuation.Target.SessionID || in.Intent.MessageID == m.Continuation.Target.MessageID {
			return c.ErrInvalid
		}
	case "refused":
		allowed = (d.State == "received" || d.State == "admitted") && in.Intent == nil && in.Reason != "" && c.DiagnosticCode(in.Reason)
	case "cancelled":
		allowed = (d.State == "queued" || d.State == "received" || d.State == "admitted") && in.Intent == nil
	}
	if !allowed {
		return c.ErrConflict
	}
	d.State, d.Reason, d.Intent = in.State, in.Reason, in.Intent
	if err := saveDelivery(ctx, tx, d); err != nil {
		return err
	}
	if err := event(ctx, tx, "delivery", m.ThreadID, m.ID); err != nil {
		return err
	}
	return tx.Commit()
}
