package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func operator(ctx context.Context, q queryer, actor c.Principal) error {
	p, e := principal(ctx, q, actor.ID)
	if e != nil {
		return e
	}
	if p.Kind != "owner" && p.Kind != "operator" {
		return c.ErrForbidden
	}
	return nil
}
func quiet(ctx context.Context, q queryer, epoch, want int64) error {
	if epoch != want {
		return c.ErrEpoch
	}
	if admission(ctx, q) != c.ErrEpoch {
		return c.ErrConflict
	}
	return nil
}
func (s *Store) Quiesce(ctx context.Context, p c.Principal, epoch int64) error {
	if epoch != s.epoch {
		return c.ErrEpoch
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	if e = operator(ctx, tx, p); e != nil {
		return e
	}
	if admission(ctx, tx) == nil {
		if _, e = tx.ExecContext(ctx, "DELETE FROM recovery_guests WHERE epoch=?", epoch); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, "UPDATE meta SET value='1' WHERE key='quiescent'"); e != nil {
		return e
	}
	return tx.Commit()
}
func recoveryRecords(ctx context.Context, q queryer, epoch int64, agent string) ([]c.RecoveryEvidence, error) {
	rows, e := q.QueryContext(ctx, "SELECT body FROM recovery_evidence WHERE epoch=? AND agent=? ORDER BY id", epoch, agent)
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	out := []c.RecoveryEvidence{}
	for rows.Next() {
		var b []byte
		var r c.RecoveryEvidence
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) ReconcileGuest(ctx context.Context, p c.Principal, in c.GuestRecovery) (c.RecoveryStatus, error) {
	var out c.RecoveryStatus
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer func() { _ = tx.Rollback() }()
	if e = quiet(ctx, tx, in.RestoreEpoch, s.epoch); e != nil {
		return out, e
	}
	p, e = principal(ctx, tx, p.ID)
	if e != nil || p.Kind != "agent" || p.ID != in.AgentID {
		return out, c.ErrForbidden
	}
	if e = placement(ctx, tx, p.ID, in.InstanceID, in.Generation); e != nil {
		return out, e
	}
	var actual []byte
	if e = tx.QueryRowContext(ctx, "SELECT body FROM placements WHERE agent_id=?", p.ID).Scan(&actual); e != nil {
		return out, e
	}
	if string(actual) != string(encode(in.Placement)) {
		return out, c.ErrConflict
	}
	if len(encode(in)) > c.MaxJSON {
		return out, c.ErrSize
	}
	for _, r := range in.Records {
		if !validID(r.ID) || r.Kind == "" || len(r.Kind) > 32 || len(r.RecordKey) > 256 || !json.Valid(r.Data) || len(r.Data) > c.MaxJSON-4096 {
			return out, c.ErrInvalid
		}
		var old []byte
		e = tx.QueryRowContext(ctx, "SELECT body FROM recovery_evidence WHERE epoch=? AND agent=? AND id=?", s.epoch, p.ID, r.ID).Scan(&old)
		if e == nil {
			if string(old) != string(encode(r)) {
				return out, c.ErrConflict
			}
			continue
		}
		if e != sql.ErrNoRows {
			return out, e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO recovery_evidence(epoch,agent,id,body) VALUES(?,?,?,?)", s.epoch, p.ID, r.ID, encode(r)); e != nil {
			return out, e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM recovery_guests WHERE epoch=? AND agent=?", s.epoch, p.ID); e != nil {
			return out, e
		}
	}
	if in.Complete {
		records, e := recoveryRecords(ctx, tx, s.epoch, p.ID)
		if e != nil {
			return out, e
		}
		if len(records) != in.RecordCount || c.RecoveryDigest(records) != in.ManifestSHA256 {
			return out, c.ErrConflict
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO recovery_guests(epoch,agent,placement,digest) VALUES(?,?,?,?) ON CONFLICT(epoch,agent) DO UPDATE SET placement=excluded.placement,digest=excluded.digest", s.epoch, p.ID, actual, in.ManifestSHA256); e != nil {
			return out, e
		}
	}
	if e = tx.Commit(); e != nil {
		return out, e
	}
	return s.RecoveryStatus(ctx, p)
}
func (s *Store) RecoveryStatus(ctx context.Context, p c.Principal) (c.RecoveryStatus, error) {
	out := c.RecoveryStatus{RestoreEpoch: s.epoch}
	var e error
	p, e = principal(ctx, s.db, p.ID)
	if e != nil {
		return out, e
	}
	out.Quiescent = admission(ctx, s.db) == c.ErrEpoch
	if p.Kind == "agent" {
		e = s.db.QueryRowContext(ctx, "SELECT digest FROM recovery_guests g JOIN placements p ON p.agent_id=g.agent AND p.body=g.placement WHERE g.epoch=? AND g.agent=?", s.epoch, p.ID).Scan(&out.ManifestSHA256)
		if e != nil && e != sql.ErrNoRows {
			return out, e
		}
		if e = s.db.QueryRowContext(ctx, "SELECT count(*) FROM recovery_evidence WHERE epoch=? AND agent=? AND resolution IS NULL", s.epoch, p.ID).Scan(&out.Unresolved); e != nil {
			return out, e
		}
		out.Reconciled = out.ManifestSHA256 != "" && out.Unresolved == 0
		return out, nil
	}
	if e = operator(ctx, s.db, p); e != nil {
		return out, e
	}
	if e = s.db.QueryRowContext(ctx, "SELECT count(*) FROM placements p WHERE NOT EXISTS(SELECT 1 FROM recovery_guests g WHERE g.epoch=? AND g.agent=p.agent_id AND g.placement=p.body)", s.epoch).Scan(&out.PendingGuests); e != nil {
		return out, e
	}
	if e = s.db.QueryRowContext(ctx, "SELECT count(*) FROM recovery_evidence WHERE epoch=? AND resolution IS NULL", s.epoch).Scan(&out.Unresolved); e != nil {
		return out, e
	}
	out.Reconciled = out.PendingGuests == 0 && out.Unresolved == 0
	return out, nil
}
func (s *Store) RecoveryEvidence(ctx context.Context, p c.Principal, agent, after string) ([]c.RecoveryEvidence, error) {
	if p.Kind == "agent" {
		if agent != p.ID {
			return nil, c.ErrForbidden
		}
	} else if e := operator(ctx, s.db, p); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, "SELECT body FROM recovery_evidence WHERE epoch=? AND agent=? AND id>? ORDER BY id LIMIT ?", s.epoch, agent, after, c.MaxPageRecords)
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	out := []c.RecoveryEvidence{}
	size := 0
	for rows.Next() {
		var b []byte
		var v c.RecoveryEvidence
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if size+len(b) > c.MaxPageBytes-4096 {
			break
		}
		if e = json.Unmarshal(b, &v); e != nil {
			return nil, e
		}
		out = append(out, v)
		size += len(b)
	}
	return out, rows.Err()
}
func (s *Store) ResolveRecovery(ctx context.Context, p c.Principal, in c.RecoveryResolution) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	if e = operator(ctx, tx, p); e != nil {
		return e
	}
	if e = quiet(ctx, tx, in.RestoreEpoch, s.epoch); e != nil {
		return e
	}
	if strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 512 {
		return c.ErrInvalid
	}
	var raw, resolved []byte
	if e = tx.QueryRowContext(ctx, "SELECT body,resolution FROM recovery_evidence WHERE epoch=? AND agent=? AND id=?", s.epoch, in.AgentID, in.EvidenceID).Scan(&raw, &resolved); e != nil {
		return e
	}
	if resolved != nil {
		if string(resolved) == string(encode(in)) {
			return nil
		}
		return c.ErrConflict
	}
	var r c.RecoveryEvidence
	if e = json.Unmarshal(raw, &r); e != nil {
		return e
	}
	if r.Uncertain {
		var proofRaw []byte
		if e = tx.QueryRowContext(ctx, "SELECT body FROM recovery_evidence WHERE epoch=? AND agent=? AND id=?", s.epoch, in.AgentID, in.ProofID).Scan(&proofRaw); e != nil {
			return c.ErrConflict
		}
		var proof c.RecoveryEvidence
		if json.Unmarshal(proofRaw, &proof) != nil || proof.Uncertain || !proof.Terminal || r.RecordKey == "" || proof.RecordKey != r.RecordKey || proof.Kind != r.Kind || digest(proof.Intent) != digest(r.Intent) {
			return c.ErrConflict
		}
	}
	// Missing messages remain quarantine evidence. Existing work is retired,
	// never reopened or published from a rolled-back request receipt.
	if r.DeliveryID != "" {
		d, e := loadDelivery(ctx, tx, r.DeliveryID)
		if e != nil && e != c.ErrNotFound {
			return e
		}
		if e == nil {
			if d.AgentID != in.AgentID {
				return c.ErrForbidden
			}
			if d.State != "replied" && d.State != "cancelled" && d.State != "suppressed" {
				d.State = "cancelled"
				d.Reason = "operator_restore_resolution"
				if e = saveDelivery(ctx, tx, d); e != nil {
					return e
				}
			}
		}
	}
	if _, e = tx.ExecContext(ctx, "UPDATE recovery_evidence SET resolution=? WHERE epoch=? AND agent=? AND id=?", encode(in), s.epoch, in.AgentID, in.EvidenceID); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Resume(ctx context.Context, p c.Principal, epoch int64) error {
	if epoch != s.epoch {
		return c.ErrEpoch
	}
	if e := operator(ctx, s.db, p); e != nil {
		return e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM placements p WHERE NOT EXISTS(SELECT 1 FROM recovery_guests g WHERE g.epoch=? AND g.agent=p.agent_id AND g.placement=p.body)", s.epoch).Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		return c.ErrEpoch
	}
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM recovery_evidence WHERE epoch=? AND resolution IS NULL", s.epoch).Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		return c.ErrEpoch
	}
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE state IN ('received','running','blocked')").Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		return c.ErrEpoch
	}
	if _, e = tx.ExecContext(ctx, "UPDATE meta SET value='0' WHERE key='quiescent'"); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) SetPlacement(ctx context.Context, p c.Principal, in c.PlacementChange) (c.Placement, error) {
	out := in.Placement
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer func() { _ = tx.Rollback() }()
	if e = operator(ctx, tx, p); e != nil {
		return out, e
	}
	if in.RestoreEpoch != s.epoch {
		return out, c.ErrEpoch
	}
	var n int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE agent_id=? AND state IN ('received','running','blocked')", in.AgentID).Scan(&n); e != nil {
		return out, e
	}
	if n > 0 {
		return out, c.ErrConflict
	}
	if e = quiet(ctx, tx, in.RestoreEpoch, s.epoch); e != nil {
		return out, e
	}
	var raw []byte
	if e = tx.QueryRowContext(ctx, "SELECT body FROM placements WHERE agent_id=?", in.AgentID).Scan(&raw); e != nil {
		return out, e
	}
	var old c.Placement
	if json.Unmarshal(raw, &old) == nil && old == in.Placement && in.ExpectedGeneration+1 == in.Generation {
		return out, nil
	}
	if json.Unmarshal(raw, &old) != nil || old.Generation != in.ExpectedGeneration || in.Generation != old.Generation+1 || !validID(in.HostID) || !validID(in.InstanceID) {
		return out, c.ErrConflict
	}
	var reported []byte
	var inventoryDigest string
	if e = tx.QueryRowContext(ctx, "SELECT placement,digest FROM recovery_guests WHERE epoch=? AND agent=?", s.epoch, in.AgentID).Scan(&reported, &inventoryDigest); e != nil || string(reported) != string(raw) {
		return out, c.ErrEpoch
	}
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM recovery_evidence WHERE epoch=? AND agent=? AND resolution IS NULL", s.epoch, in.AgentID).Scan(&n); e != nil {
		return out, e
	}
	if n > 0 {
		return out, c.ErrEpoch
	}
	if in.DryRun {
		return out, nil
	}
	if _, e = tx.ExecContext(ctx, "UPDATE placements SET body=?,last_seen='',state='offline',reason='placement_changed' WHERE agent_id=?", encode(out), in.AgentID); e != nil {
		return out, e
	}
	proof := c.PlacementProof{MasterID: s.id, RestoreEpoch: s.epoch, From: old, To: out, ManifestSHA256: inventoryDigest}
	if _, e = tx.ExecContext(ctx, "INSERT INTO placement_transitions(agent,generation,body) VALUES(?,?,?)", in.AgentID, in.Generation, encode(proof)); e != nil {
		return out, e
	}
	if e = s.saveRegistry(ctx, tx); e != nil {
		return out, e
	}
	return out, tx.Commit()
}

func (s *Store) PlacementProof(ctx context.Context, actor c.Principal) (c.PlacementProof, error) {
	var out c.PlacementProof
	p, e := authorize(ctx, s.db, actor, "read")
	if e != nil {
		return out, e
	}
	if p.Kind != "agent" {
		return out, c.ErrForbidden
	}
	var placementRaw []byte
	if e = s.db.QueryRowContext(ctx, "SELECT body FROM placements WHERE agent_id=?", p.ID).Scan(&placementRaw); e != nil {
		return out, e
	}
	var current c.Placement
	if e = json.Unmarshal(placementRaw, &current); e != nil {
		return out, e
	}
	var raw []byte
	if e = s.db.QueryRowContext(ctx, "SELECT body FROM placement_transitions WHERE agent=? AND generation=?", p.ID, current.Generation).Scan(&raw); e != nil {
		if e == sql.ErrNoRows {
			return out, c.ErrNotFound
		}
		return out, e
	}
	if e = json.Unmarshal(raw, &out); e != nil {
		return out, e
	}
	if out.To != current || out.MasterID != s.id {
		return out, c.ErrConflict
	}
	return out, nil
}
