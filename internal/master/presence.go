package master

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (s *Store) Heartbeat(ctx context.Context, actor c.Principal, in c.Heartbeat) error {
	p, err := authorize(ctx, s.db, actor, "send")
	if err != nil || p.Kind != "agent" || in.AgentID != p.ID {
		return c.ErrForbidden
	}
	if err = placement(ctx, s.db, p.ID, in.InstanceID, in.Generation); err != nil {
		return err
	}
	var raw []byte
	if err = s.db.QueryRowContext(ctx, "SELECT body FROM placements WHERE agent_id=?", p.ID).Scan(&raw); err != nil {
		return err
	}
	var current c.Placement
	if json.Unmarshal(raw, &current) != nil || current.HostID != in.HostID {
		return c.ErrConflict
	}
	if !slices.Contains([]string{"idle", "busy", "paused", "recovering"}, in.State) || len(in.Reason) > 512 {
		return c.ErrInvalid
	}
	_, err = s.db.ExecContext(ctx, "UPDATE placements SET last_seen=?,state=?,reason=? WHERE agent_id=?", s.now().UTC().Format(time.RFC3339Nano), in.State, in.Reason, p.ID)
	return err
}
func (s *Store) Team(ctx context.Context, actor c.Principal) ([]c.Presence, error) {
	p, err := authorize(ctx, s.db, actor, "read")
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT p.body,r.body,p.last_seen,p.state,p.reason FROM placements p JOIN principals r ON r.id=p.agent_id ORDER BY p.agent_id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []c.Presence{}
	for rows.Next() {
		var place, pr []byte
		var seen string
		var v c.Presence
		if err = rows.Scan(&place, &pr, &seen, &v.State, &v.Reason); err != nil {
			return nil, err
		}
		var a c.Principal
		if err = json.Unmarshal(pr, &a); err != nil {
			return nil, err
		}
		shared := false
		for _, e := range a.Engagements {
			if slices.Contains(p.Engagements, e) {
				shared = true
			}
		}
		if !shared {
			continue
		}
		if err = json.Unmarshal(place, &v.Placement); err != nil {
			return nil, err
		}
		v.Alias = a.Alias
		if seen != "" {
			v.LastSeen, err = time.Parse(time.RFC3339Nano, seen)
			if err != nil {
				return nil, err
			}
			if s.now().Sub(v.LastSeen) >= c.PresenceStale {
				v.State = "stale"
			}
		}
		out = append(out, v)
		if len(out) > c.MaxPageRecords {
			return nil, c.ErrSize
		}
	}
	return out, rows.Err()
}
