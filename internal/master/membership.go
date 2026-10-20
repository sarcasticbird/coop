package master

import (
	"context"
	"encoding/json"
	"slices"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (s *Store) SetMembers(ctx context.Context, actor c.Principal, in c.SetMembers) (c.Thread, error) {
	var out c.Thread
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := authorize(ctx, tx, actor, "manage")
	if err != nil || p.Kind != "owner" {
		return out, c.ErrForbidden
	}
	if err = member(ctx, tx, p, in.ThreadID); err != nil {
		return out, err
	}
	if yes, e := request(ctx, tx, p.ID, in.RequestID, "members", in, &out); yes || e != nil {
		return out, e
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM threads WHERE id=?", in.ThreadID).Scan(&raw); err != nil {
		return out, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	if !slices.Equal(normalized(in.ExpectedMembers), out.Members) {
		return out, c.ErrConflict
	}
	next := normalized(in.Members)
	if len(next) > 100 || !slices.Contains(next, p.ID) {
		return out, c.ErrInvalid
	}
	for _, id := range next {
		v, err := principal(ctx, tx, id)
		if err != nil || v.ID != id || !slices.Contains(v.Engagements, out.Engagement) {
			return out, c.ErrForbidden
		}
	}
	// Revoke access and unstarted deliveries in the same transaction. A running
	// intent is held with its evidence; this does not claim the model was stopped.
	for _, id := range out.Members {
		if slices.Contains(next, id) {
			continue
		}
		rows, err := tx.QueryContext(ctx, "SELECT d.body FROM deliveries d JOIN messages m ON m.id=d.message_id WHERE m.thread_id=? AND d.agent_id=? AND d.state IN ('queued','received','running','admitted')", out.ID, id)
		if err != nil {
			return out, err
		}
		var ds []c.Delivery
		for rows.Next() {
			var b []byte
			var d c.Delivery
			if err = rows.Scan(&b); err != nil {
				_ = rows.Close()
				return out, err
			}
			if err = json.Unmarshal(b, &d); err != nil {
				_ = rows.Close()
				return out, err
			}
			ds = append(ds, d)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return out, err
		}
		for _, d := range ds {
			if d.Intent != nil {
				d.State = "blocked"
			} else {
				d.State = "cancelled"
			}
			d.Reason = "membership_revoked"
			if err = saveDelivery(ctx, tx, d); err != nil {
				return out, err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM members WHERE thread_id=?", out.ID); err != nil {
		return out, err
	}
	for _, id := range next {
		if _, err = tx.ExecContext(ctx, "INSERT INTO members(thread_id,principal_id) VALUES(?,?)", out.ID, id); err != nil {
			return out, err
		}
	}
	out.Members = next
	if _, err = tx.ExecContext(ctx, "UPDATE threads SET body=? WHERE id=?", encode(out), out.ID); err != nil {
		return out, err
	}
	if err = event(ctx, tx, "membership", out.ID, ""); err != nil {
		return out, err
	}
	if err = saveRequest(ctx, tx, p.ID, in.RequestID, "members", in, out); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
