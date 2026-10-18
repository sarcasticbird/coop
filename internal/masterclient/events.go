package masterclient

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type EventStream interface {
	Next(context.Context) (c.Event, error)
	Close() error
}
type eventStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
	cancel  context.CancelFunc
}

func (cl *Client) Events(ctx context.Context, cursor c.Cursor) (EventStream, error) {
	ctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(ctx, "GET", cl.cfg.URL+"/events", nil)
	if err != nil {
		cancel()
		return nil, c.ErrInvalid
	}
	cl.pinRequest(req)
	if cursor.RestoreEpoch > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(cursor.RestoreEpoch, 10)+":"+strconv.FormatInt(cursor.Sequence, 10))
	}
	r, err := cl.http.Do(req)
	if err != nil {
		cancel()
		return nil, c.ErrUnavailable
	}
	if r.Header.Get("X-Coop-Master-ID") != cl.cfg.MasterID || r.StatusCode != 200 {
		_ = r.Body.Close()
		cancel()
		if r.Header.Get("X-Coop-Master-ID") == cl.cfg.MasterID && r.StatusCode == 409 {
			return nil, c.ErrEpoch
		}
		return nil, c.ErrConflict
	}
	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 4096), c.MaxPageBytes)
	return &eventStream{body: r.Body, scanner: scanner, cancel: cancel}, nil
}
func (s *eventStream) Close() error { s.cancel(); return s.body.Close() }
func (s *eventStream) Next(ctx context.Context) (c.Event, error) {
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	for s.scanner.Scan() {
		line := s.scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			var e c.Event
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e) != nil {
				return e, c.ErrInvalid
			}
			return e, nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return c.Event{}, c.ErrUnavailable
	}
	return c.Event{}, io.EOF
}
