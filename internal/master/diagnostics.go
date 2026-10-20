package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (s *Store) ReportDiagnostics(ctx context.Context, actor c.Principal, report c.WorkerDiagnostics) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := authorize(ctx, tx, actor, "send")
	if err != nil {
		return err
	}
	if p.Kind != "agent" || report.AgentID != p.ID {
		return c.ErrForbidden
	}
	if report.RestoreEpoch != s.epoch {
		return c.ErrEpoch
	}
	if err = c.ValidateDiagnostics(report); err != nil {
		return err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT body FROM placements WHERE agent_id=?", p.ID).Scan(&raw); err != nil {
		return c.ErrForbidden
	}
	var current c.Placement
	if json.Unmarshal(raw, &current) != nil || report.Placement != current {
		return c.ErrConflict
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO agent_diagnostics(agent_id,body,received_at) VALUES(?,?,?) ON CONFLICT(agent_id) DO UPDATE SET body=excluded.body,received_at=excluded.received_at", p.ID, encode(report), s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AgentDiagnostics(ctx context.Context, actor c.Principal, agentID string) (c.AgentDiagnostics, error) {
	var out c.AgentDiagnostics
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := authorize(ctx, tx, actor, "read")
	if err != nil {
		return out, err
	}
	var principalRaw, placementRaw, reportRaw []byte
	var received string
	err = tx.QueryRowContext(ctx, "SELECT r.body,p.body,d.body,d.received_at FROM principals r JOIN placements p ON p.agent_id=r.id JOIN agent_diagnostics d ON d.agent_id=r.id WHERE r.id=?", agentID).Scan(&principalRaw, &placementRaw, &reportRaw, &received)
	if err == sql.ErrNoRows {
		return out, c.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	var target c.Principal
	var placement c.Placement
	if err = json.Unmarshal(principalRaw, &target); err != nil {
		return out, err
	}
	shared := false
	for _, e := range target.Engagements {
		if slices.Contains(p.Engagements, e) {
			shared = true
		}
	}
	if !shared {
		return out, c.ErrNotFound
	}
	if err = json.Unmarshal(placementRaw, &placement); err != nil {
		return out, err
	}
	if err = json.Unmarshal(reportRaw, &out.Report); err != nil {
		return out, err
	}
	if out.Report.RestoreEpoch != s.epoch || out.Report.Placement != placement {
		return c.AgentDiagnostics{}, c.ErrNotFound
	}
	if err = c.ValidateDiagnostics(out.Report); err != nil {
		return c.AgentDiagnostics{}, err
	}
	out.ReceivedAt, err = time.Parse(time.RFC3339Nano, received)
	if err != nil {
		return c.AgentDiagnostics{}, err
	}
	out.Stale = s.now().Sub(out.ReceivedAt) >= c.PresenceStale
	return out, nil
}
