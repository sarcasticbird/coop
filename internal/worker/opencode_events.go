package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ReadOpenCodeEvents emits hints only. Callers reread authoritative session state.
func ReadOpenCodeEvents(ctx context.Context, input io.Reader, sessionID string, wake chan<- struct{}) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	var data strings.Builder
	dispatch := func() error {
		if data.Len() == 0 {
			return nil
		}
		var event struct {
			Type       string
			Properties struct {
				SessionID string
				Info      struct{ SessionID string }
			}
		}
		if json.Unmarshal([]byte(data.String()), &event) != nil {
			return &OpenCodeError{Category: "invalid_event"}
		}
		data.Reset()
		id := event.Properties.SessionID
		if id == "" {
			id = event.Properties.Info.SessionID
		}
		if id != sessionID {
			return nil
		}
		switch event.Type {
		case "session.idle", "session.error", "session.status", "message.updated", "permission.asked", "question.asked":
			select {
			case wake <- struct{}{}:
			default:
			}
		}
		return nil
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			if data.Len() > 256<<10 {
				return &OpenCodeError{Category: "event_too_large"}
			}
		}
	}
	if scanner.Err() != nil {
		return &OpenCodeError{Category: "event_stream_failed"}
	}
	return dispatch()
}
func (c *OpenCodeClient) Watch(ctx context.Context, j Job, wake chan<- struct{}) error {
	if !validSession(j.SessionID) {
		return &OpenCodeError{Category: "invalid_session_id"}
	}
	u := c.config.BaseURL + "/event?directory=" + url.QueryEscape(j.Checkout)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return &OpenCodeError{Category: "invalid_request"}
	}
	password, err := readPrivate(c.config.PasswordFile, 4096)
	if err != nil {
		return &OpenCodeError{Category: "server_auth_unavailable"}
	}
	req.SetBasicAuth("opencode", strings.TrimSpace(string(password)))
	client := *c.http
	client.Timeout = 0
	res, err := client.Do(req)
	if err != nil {
		return &OpenCodeError{Category: "event_stream_failed"}
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		return &OpenCodeError{Category: "event_stream_failed", Status: res.StatusCode}
	}
	return ReadOpenCodeEvents(ctx, res.Body, j.SessionID, wake)
}
