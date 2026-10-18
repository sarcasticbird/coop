package master

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (s *Store) ImportHistory(ctx context.Context, p c.Principal, in c.HistoryImport) (c.HistoryRecord, error) {
	out := c.HistoryRecord{HistoryImport: in}
	out.DryRun = false
	out.SHA256 = digest([]string{in.ThreadID, in.SourceURL, in.Cutoff, in.Body})
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer func() { _ = tx.Rollback() }()
	if e = operator(ctx, tx, p); e != nil {
		return out, e
	}
	if e = member(ctx, tx, p, in.ThreadID); e != nil {
		return out, e
	}
	u, e := url.Parse(in.SourceURL)
	if len(in.SourceURL) > 2048 || e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return out, c.ErrInvalid
	}
	if _, e = time.Parse(time.RFC3339, in.Cutoff); e != nil {
		return out, c.ErrInvalid
	}
	if len(in.Body) > c.MaxText || strings.TrimSpace(in.Body) == "" || !utf8.ValidString(in.Body) || !validID(in.RequestID) {
		return out, c.ErrInvalid
	}
	normalized := in
	normalized.DryRun = false
	if !in.DryRun {
		if yes, e := request(ctx, tx, p.ID, in.RequestID, "history-import", normalized, &out); yes || e != nil {
			return out, e
		}
	}
	var existing []byte
	e = tx.QueryRowContext(ctx, "SELECT body FROM history_imports WHERE thread_id=? AND digest=?", in.ThreadID, out.SHA256).Scan(&existing)
	if e == nil {
		if !in.DryRun {
			if e = saveRequest(ctx, tx, p.ID, in.RequestID, "history-import", normalized, out); e != nil {
				return out, e
			}
			return out, tx.Commit()
		}
		return out, nil
	}
	if e != sql.ErrNoRows {
		return out, e
	}
	var n int
	if e = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM history_imports)+(SELECT count(*) FROM messages)").Scan(&n); e != nil {
		return out, e
	}
	if n >= c.MaxMessages {
		return out, c.ErrCapacity
	}
	if in.DryRun {
		return out, nil
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO history_imports(thread_id,digest,body) VALUES(?,?,?)", in.ThreadID, out.SHA256, encode(out)); e != nil {
		return out, e
	}
	if e = saveRequest(ctx, tx, p.ID, in.RequestID, "history-import", normalized, out); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (s *Store) History(ctx context.Context, p c.Principal, thread, after string) ([]c.HistoryRecord, error) {
	if _, e := authorize(ctx, s.db, p, "read"); e != nil {
		return nil, e
	}
	if e := member(ctx, s.db, p, thread); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, "SELECT body FROM history_imports WHERE thread_id=? AND digest>? ORDER BY digest LIMIT ?", thread, after, c.MaxPageRecords)
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	out := []c.HistoryRecord{}
	size := 0
	for rows.Next() {
		var raw []byte
		var r c.HistoryRecord
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if size+len(raw) > c.MaxPageBytes-4096 {
			break
		}
		if e = json.Unmarshal(raw, &r); e != nil {
			return nil, e
		}
		out = append(out, r)
		size += len(raw)
	}
	return out, rows.Err()
}
func (s *Store) Cutover(ctx context.Context, p c.Principal, in c.SourceCutover) (c.SourceCutover, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return in, e
	}
	defer func() { _ = tx.Rollback() }()
	p, e = principal(ctx, tx, p.ID)
	if e != nil || p.Kind != "owner" {
		return in, c.ErrForbidden
	}
	if e = member(ctx, tx, p, in.ThreadID); e != nil {
		return in, e
	}
	if e = admission(ctx, tx); e != nil {
		return in, e
	}
	var raw []byte
	if e = tx.QueryRowContext(ctx, "SELECT body FROM history_imports WHERE thread_id=? AND digest=?", in.ThreadID, in.HistorySHA256).Scan(&raw); e != nil {
		return in, c.ErrConflict
	}
	var history c.HistoryRecord
	if json.Unmarshal(raw, &history) != nil || history.SourceURL != in.SourceURL {
		return in, c.ErrConflict
	}
	var old []byte
	e = tx.QueryRowContext(ctx, "SELECT body FROM source_cutovers WHERE thread_id=?", in.ThreadID).Scan(&old)
	want := in
	want.DryRun = false
	if e == nil {
		if string(old) == string(encode(want)) {
			return want, nil
		}
		return in, c.ErrConflict
	}
	if e != sql.ErrNoRows {
		return in, e
	}
	if in.DryRun {
		return want, nil
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO source_cutovers(thread_id,body) VALUES(?,?)", in.ThreadID, encode(want)); e != nil {
		return in, e
	}
	return want, tx.Commit()
}
