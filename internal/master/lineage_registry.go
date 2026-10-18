package master

import (
	"context"
	"encoding/json"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

// The independent lineage retains enrollment and placement fences made after
// an older backup. Restoring that backup cannot forget a guest or lower its
// placement generation. This is recovery metadata, never a second coordinator.
func registry(ctx context.Context, q queryer) ([]c.Principal, []c.Placement, error) {
	ps := []c.Principal{}
	placements := []c.Placement{}
	rows, e := q.QueryContext(ctx, "SELECT body FROM principals ORDER BY id")
	if e != nil {
		return nil, nil, e
	}
	for rows.Next() {
		var raw []byte
		var p c.Principal
		if e = rows.Scan(&raw); e != nil {
			_ = rows.Close()
			return nil, nil, e
		}
		if e = json.Unmarshal(raw, &p); e != nil {
			_ = rows.Close()
			return nil, nil, e
		}
		ps = append(ps, p)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return nil, nil, e
	}
	rows, e = q.QueryContext(ctx, "SELECT body FROM placements ORDER BY agent_id")
	if e != nil {
		return nil, nil, e
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		var p c.Placement
		if e = rows.Scan(&raw); e != nil {
			return nil, nil, e
		}
		if e = json.Unmarshal(raw, &p); e != nil {
			return nil, nil, e
		}
		placements = append(placements, p)
	}
	return ps, placements, rows.Err()
}
func (s *Store) saveRegistry(ctx context.Context, q queryer) error {
	ps, placements, e := registry(ctx, q)
	if e != nil {
		return e
	}
	v := epochLineage{MasterID: s.id, Epoch: s.epoch, Principals: ps, Placements: placements}
	if len(encode(v)) > 16<<20 {
		return c.ErrSize
	}
	return writeLineage(s.epochPath, v)
}
func restoreRegistry(ctx context.Context, q queryer, v epochLineage) error {
	// Private temporary values cannot collide with a valid configured alias.
	// Release all old aliases before applying the final unique assignment.
	if _, e := q.ExecContext(ctx, "UPDATE principals SET alias=char(0)||id"); e != nil {
		return e
	}

	for _, p := range v.Principals {
		var n int
		if e := q.QueryRowContext(ctx, "SELECT count(*) FROM principals WHERE id=?", p.ID).Scan(&n); e != nil {
			return e
		}
		if n > 0 {
			old, e := principal(ctx, q, p.ID)
			if e != nil {
				return e
			}
			if old.Kind != p.Kind || old.GitHubBotID != p.GitHubBotID {
				return c.ErrConflict
			}
		}
		if _, e := q.ExecContext(ctx, "INSERT INTO principals(id,kind,alias,body) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET alias=excluded.alias,body=excluded.body", p.ID, p.Kind, p.Alias, encode(p)); e != nil {
			return e
		}
	}
	for _, p := range v.Placements {
		if _, e := q.ExecContext(ctx, "INSERT INTO placements(agent_id,body) VALUES(?,?) ON CONFLICT(agent_id) DO UPDATE SET body=excluded.body,last_seen='',state='offline',reason='restored'", p.AgentID, encode(p)); e != nil {
			return e
		}
	}
	return nil
}
