package master

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func (s *Store) Events(ctx context.Context, actor c.Principal, after c.Cursor) ([]c.Event, error) {
	if after.RestoreEpoch != s.epoch {
		return nil, c.ErrEpoch
	}
	if after.Sequence < 0 {
		return nil, c.ErrInvalid
	}
	p, err := authorize(ctx, s.db, actor, "read")
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT e.seq,e.kind,e.thread_id,e.message_id,t.engagement FROM events e JOIN members m ON m.thread_id=e.thread_id JOIN threads t ON t.id=e.thread_id WHERE m.principal_id=? AND e.seq>? ORDER BY e.seq", p.ID, after.Sequence)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []c.Event{}
	for rows.Next() {
		var e c.Event
		var engagement string
		e.RestoreEpoch = s.epoch
		if err = rows.Scan(&e.Sequence, &e.Kind, &e.ThreadID, &e.MessageID, &engagement); err != nil {
			return nil, err
		}
		if slices.Contains(p.Engagements, engagement) {
			out = append(out, e)
			if len(out) == c.MaxPageRecords {
				break
			}
		}
	}
	return out, rows.Err()
}
func parseCursor(v string, epoch int64) (c.Cursor, error) {
	if v == "" {
		return c.Cursor{RestoreEpoch: epoch}, nil
	}
	a, b, ok := strings.Cut(v, ":")
	if !ok {
		return c.Cursor{}, c.ErrInvalid
	}
	ep, e := strconv.ParseInt(a, 10, 64)
	seq, e2 := strconv.ParseInt(b, 10, 64)
	if e != nil || e2 != nil || seq < 0 {
		return c.Cursor{}, c.ErrInvalid
	}
	return c.Cursor{RestoreEpoch: ep, Sequence: seq}, nil
}
func serveEvents(w http.ResponseWriter, r *http.Request, s *Store, p c.Principal, reauthorize func() error) {
	cursor, err := parseCursor(r.Header.Get("Last-Event-ID"), s.epoch)
	if err != nil {
		writeError(w, err)
		return
	}
	events, err := s.Events(r.Context(), p, cursor)
	if err != nil {
		writeError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, c.ErrUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err = reauthorize(); err != nil {
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
		for _, e := range events {
			if _, err = fmt.Fprintf(w, "id: %d:%d\nevent: coop\ndata: %s\n\n", e.RestoreEpoch, e.Sequence, encode(e)); err != nil {
				return
			}
			cursor = e.Cursor
		}
		if _, err = fmt.Fprint(w, ": keepalive\n\n"); err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		events, err = s.Events(r.Context(), p, cursor)
		if err != nil {
			return
		}
	}
}
