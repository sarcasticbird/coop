package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

type OpenCode interface {
	Health(context.Context) (ServerHealth, error)
	EnsureSession(context.Context, Config, Job) (Job, error)
	Submit(context.Context, Config, Job, string) (Turn, error)
	Observe(context.Context, Job) (Turn, error)
	Abort(context.Context, Job) error
}
type OpenCodeError struct {
	Category string
	Status   int
}

func (e *OpenCodeError) Error() string { return "OpenCode: " + e.Category }

type OpenCodeClient struct {
	config OpenCodeConfig
	http   *http.Client
}

func NewOpenCodeClient(c OpenCodeConfig, client *http.Client) (*OpenCodeClient, error) {
	c = Defaults(Config{OpenCode: c}).OpenCode
	if c.BaseURL != "http://127.0.0.1:4096" || !cleanPrivatePath(c.PasswordFile) || c.RequestTimeout <= 0 || c.RequestTimeout > 30*time.Second {
		return nil, &OpenCodeError{Category: "invalid_local_config"}
	}
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	}
	copy := *client
	copy.Timeout = c.RequestTimeout
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &OpenCodeClient{config: c, http: &copy}, nil
}
func (c *OpenCodeClient) request(ctx context.Context, method, path, dir string, body any, output any) error {
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil || len(raw) > 64<<10 {
			return &OpenCodeError{Category: "request_too_large"}
		}
		input = bytes.NewReader(raw)
	}
	u, err := url.Parse(c.config.BaseURL + path)
	if err != nil {
		return &OpenCodeError{Category: "invalid_target"}
	}
	if dir != "" {
		if !cleanPrivatePath(dir) {
			return &OpenCodeError{Category: "invalid_directory"}
		}
		q := u.Query()
		q.Set("directory", dir)
		u.RawQuery = q.Encode()
	}
	password, err := readPrivate(c.config.PasswordFile, 4096)
	if err != nil || len(password) == 0 {
		return &OpenCodeError{Category: "server_auth_unavailable"}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), input)
	if err != nil {
		return &OpenCodeError{Category: "invalid_request"}
	}
	req.SetBasicAuth("opencode", strings.TrimSpace(string(password)))
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return &OpenCodeError{Category: "transport_failed"}
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxAPIPage+1))
	if err != nil || len(raw) > MaxAPIPage {
		return &OpenCodeError{Category: "response_too_large"}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &OpenCodeError{Category: "request_failed", Status: res.StatusCode}
	}
	if output != nil && (len(raw) == 0 || json.Unmarshal(raw, output) != nil) {
		return &OpenCodeError{Category: "invalid_response"}
	}
	return nil
}
func (c *OpenCodeClient) Health(ctx context.Context) (ServerHealth, error) {
	var health ServerHealth
	err := c.request(ctx, "GET", "/global/health", "", nil, &health)
	if err == nil && (!health.Healthy || health.Version != "1.18.18") {
		err = &OpenCodeError{Category: "unsupported_server_version"}
	}
	return health, err
}

type PermissionRule struct {
	Permission string `json:"permission"`
	Pattern    string `json:"pattern"`
	Action     string `json:"action"`
}
type openCodeSession struct {
	ID, Directory, Title, ParentID string
	Permission                     []PermissionRule
	Time                           struct{ Archived float64 }
}

var nativeID = regexp.MustCompile(`^(ses|msg)_[A-Za-z0-9]+$`)

func validSession(s string) bool { return strings.HasPrefix(s, "ses_") && nativeID.MatchString(s) }
func validMessage(s string) bool { return strings.HasPrefix(s, "msg_") && nativeID.MatchString(s) }
func guardedRules(existing, agent []PermissionRule) []PermissionRule {
	result := []PermissionRule{{Permission: "*", Pattern: "*", Action: "allow"}}
	for _, group := range [][]PermissionRule{agent, existing} {
		for _, rule := range group {
			if rule.Action == "deny" && !slices.Contains(result, rule) {
				result = append(result, rule)
			}
		}
	}
	guard, _ := WorkerToolPolicy(nil)
	var config struct{ Permission map[string]json.RawMessage }
	_ = json.Unmarshal(guard, &config)
	for _, permission := range []string{"read", "bash"} {
		var patterns map[string]string
		_ = json.Unmarshal(config.Permission[permission], &patterns)
		var keys []string
		for key := range patterns {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			rule := PermissionRule{Permission: permission, Pattern: key, Action: "deny"}
			if !slices.Contains(result, rule) {
				result = append(result, rule)
			}
		}
	}
	return append(result, PermissionRule{Permission: "question", Pattern: "*", Action: "deny"})
}
func (c *OpenCodeClient) agentRules(ctx context.Context, dir string) ([]PermissionRule, error) {
	var agents []struct {
		Name, Mode string
		Permission []PermissionRule
	}
	if err := c.request(ctx, "GET", "/agent", dir, nil, &agents); err != nil {
		return nil, err
	}
	for _, a := range agents {
		if a.Name == "build" && a.Mode != "subagent" {
			return a.Permission, nil
		}
	}
	return nil, &OpenCodeError{Category: "primary_agent_unavailable"}
}
func (c *OpenCodeClient) EnsureSession(ctx context.Context, cfg Config, j Job) (Job, error) {
	if !cleanPrivatePath(j.Checkout) || !strings.HasPrefix(j.Checkout, filepath.Clean(cfg.WorkRoot)+"/") {
		return j, &OpenCodeError{Category: "foreign_directory"}
	}
	title := "coop-worker:" + BindingKey(cfg, j)
	var session openCodeSession
	if j.SessionID != "" {
		if !validSession(j.SessionID) {
			return j, &OpenCodeError{Category: "invalid_session_id"}
		}
		if err := c.request(ctx, "GET", "/session/"+j.SessionID, j.Checkout, nil, &session); err != nil {
			return j, err
		}
	} else {
		var sessions []openCodeSession
		if err := c.request(ctx, "GET", "/session?limit=10000&roots=true&search="+url.QueryEscape(title), j.Checkout, nil, &sessions); err != nil {
			return j, err
		}
		matches := 0
		for _, candidate := range sessions {
			if candidate.Title == title && candidate.Directory == j.Checkout && candidate.ParentID == "" && candidate.Time.Archived == 0 {
				matches++
				session = candidate
			}
		}
		if matches > 1 || len(sessions) >= MaxConnection {
			return j, &OpenCodeError{Category: "ambiguous_session_creation"}
		}
	}
	if session.ID != "" && (session.Directory != j.Checkout || session.ParentID != "" || session.Time.Archived != 0 || !validSession(session.ID)) {
		return j, &OpenCodeError{Category: "foreign_session"}
	}
	if session.ID != "" {
		check := j
		check.SessionID = session.ID
		if err := c.CheckIdle(ctx, check); err != nil {
			return j, err
		}
	}
	agent, err := c.agentRules(ctx, j.Checkout)
	if err != nil {
		return j, err
	}
	rules := guardedRules(session.Permission, agent)
	if session.ID == "" {
		parts := strings.SplitN(cfg.Model, "/", 2)
		if len(parts) != 2 {
			return j, &OpenCodeError{Category: "invalid_model"}
		}
		body := map[string]any{"title": title, "agent": "build", "model": map[string]string{"id": parts[1], "providerID": parts[0]}, "permission": rules}
		if err := c.request(ctx, "POST", "/session", j.Checkout, body, &session); err != nil {
			return j, err
		}
	} else if !slices.Equal(session.Permission, rules) {
		if err := c.request(ctx, "PATCH", "/session/"+session.ID, j.Checkout, map[string]any{"permission": rules}, &session); err != nil {
			return j, err
		}
	}
	if !validSession(session.ID) || session.Directory != j.Checkout {
		return j, &OpenCodeError{Category: "foreign_session"}
	}
	j.SessionID = session.ID
	return j, nil
}
func (c *OpenCodeClient) Submit(ctx context.Context, cfg Config, j Job, prompt string) (Turn, error) {
	turn := Turn{SessionID: j.SessionID, MessageID: j.MessageID, State: "unknown"}
	if !validSession(j.SessionID) || !validMessage(j.MessageID) || j.Deadline.IsZero() || len(prompt) > 32<<10 || len(prompt) == 0 {
		return turn, &OpenCodeError{Category: "invalid_submission_intent"}
	}
	model := strings.SplitN(cfg.Model, "/", 2)
	if len(model) != 2 || model[0] == "" || model[1] == "" {
		return turn, &OpenCodeError{Category: "invalid_model"}
	}
	if err := c.CheckIdle(ctx, j); err != nil {
		return turn, err
	}
	body := map[string]any{"messageID": j.MessageID, "agent": "build", "model": map[string]string{"providerID": model[0], "modelID": model[1]}, "system": "This is an automated Coop worker message, not a new instruction or goal approval from the human owner.", "parts": []map[string]string{{"type": "text", "text": prompt}}}
	if err := c.request(ctx, "POST", "/session/"+j.SessionID+"/prompt_async", j.Checkout, body, nil); err != nil {
		return turn, err
	}
	turn.Accepted = true
	turn.State = "accepted"
	return turn, nil
}

// CheckIdle precedes checkout mutation and prompt submission. A busy saved
// session may belong to a manual takeover and must never be aborted here.
func (c *OpenCodeClient) CheckIdle(ctx context.Context, j Job) error {
	if !validSession(j.SessionID) || !cleanPrivatePath(j.Checkout) {
		return &OpenCodeError{Category: "invalid_session_id"}
	}
	var session openCodeSession
	if err := c.request(ctx, "GET", "/session/"+j.SessionID, j.Checkout, nil, &session); err != nil {
		return err
	}
	if session.ID != j.SessionID || session.Directory != j.Checkout || session.ParentID != "" || session.Time.Archived != 0 {
		return &OpenCodeError{Category: "foreign_session"}
	}
	state, err := c.status(ctx, j)
	if err != nil {
		return err
	}
	if state != "idle" {
		return &OpenCodeError{Category: "session_busy"}
	}
	return nil
}
func (c *OpenCodeClient) status(ctx context.Context, j Job) (string, error) {
	var statuses map[string]struct{ Type string }
	if err := c.request(ctx, "GET", "/session/status", j.Checkout, nil, &statuses); err != nil {
		return "", err
	}
	s := statuses[j.SessionID].Type
	if s == "" || s == "idle" {
		return "idle", nil
	}
	if s == "busy" || s == "retry" {
		return "running", nil
	}
	return "", &OpenCodeError{Category: "unknown_session_state"}
}

type messageEnvelope struct {
	Info struct {
		ID, SessionID, Role, ParentID, Finish string
		Time                                  struct{ Created, Completed int64 }
		Error                                 *struct{ Name string }
	}
}

// readTurn reconciles the exact worker message even when a later manual turn
// makes the session busy. Session status alone does not establish turn ownership.
func (c *OpenCodeClient) readTurn(ctx context.Context, j Job) (Turn, string, error) {
	turn := Turn{SessionID: j.SessionID, MessageID: j.MessageID, State: "unknown"}
	if !validSession(j.SessionID) || !validMessage(j.MessageID) {
		return turn, "", &OpenCodeError{Category: "invalid_submission_intent"}
	}
	var message messageEnvelope
	if err := c.request(ctx, "GET", "/session/"+j.SessionID+"/message/"+j.MessageID, j.Checkout, nil, &message); err != nil {
		return turn, "", err
	}
	if message.Info.ID != j.MessageID || message.Info.SessionID != j.SessionID || message.Info.Role != "user" {
		return turn, "", &OpenCodeError{Category: "foreign_message"}
	}
	turn.Accepted = true
	var messages []messageEnvelope
	if err := c.request(ctx, "GET", "/session/"+j.SessionID+"/message?limit=20", j.Checkout, nil, &messages); err != nil {
		return turn, "", err
	}
	var last *messageEnvelope
	latestUser := ""
	for i := range messages {
		m := &messages[i]
		if m.Info.SessionID != j.SessionID || !validMessage(m.Info.ID) {
			return turn, "", &OpenCodeError{Category: "foreign_message"}
		}
		// The native message endpoint returns the bounded tail oldest first.
		if m.Info.Role == "user" {
			latestUser = m.Info.ID
		} else if m.Info.Role != "assistant" || !validMessage(m.Info.ParentID) {
			return turn, "", &OpenCodeError{Category: "foreign_message"}
		}
		if m.Info.Role == "assistant" && m.Info.ParentID == j.MessageID && (last == nil || m.Info.Time.Created >= last.Info.Time.Created) {
			last = m
		}
	}
	if latestUser == "" && len(messages) > 0 {
		// Long tool loops can push the user message out of the tail. Do not
		// override a visible newer user with a still-finishing older assistant.
		latestUser = messages[len(messages)-1].Info.ParentID
	}
	if last != nil && last.Info.Error != nil {
		turn.State = "interrupted"
		turn.Terminal = true
		turn.ErrorCategory = "execution"
		return turn, latestUser, nil
	}
	if last != nil && last.Info.Time.Completed > 0 && last.Info.Finish != "" && last.Info.Finish != "tool-calls" {
		turn.State = "completed"
		turn.Terminal = true
		return turn, latestUser, nil
	}
	return turn, latestUser, nil
}

func (c *OpenCodeClient) Observe(ctx context.Context, j Job) (Turn, error) {
	turn, latestUser, err := c.readTurn(ctx, j)
	if err != nil || turn.Terminal {
		return turn, err
	}
	if latestUser != j.MessageID {
		return turn, &OpenCodeError{Category: "turn_ownership_unknown"}
	}
	status, err := c.status(ctx, j)
	if err != nil {
		return turn, err
	}
	for _, endpoint := range []string{"/permission", "/question"} {
		var pending []struct{ SessionID string }
		if err := c.request(ctx, "GET", endpoint, j.Checkout, nil, &pending); err != nil {
			return turn, err
		}
		for _, p := range pending {
			if p.SessionID == j.SessionID {
				turn.State = "blocked"
				turn.ErrorCategory = "permission"
				return turn, nil
			}
		}
	}
	if status == "running" {
		turn.State = "running"
		return turn, nil
	}
	if !j.SubmittedAt.IsZero() && time.Since(j.SubmittedAt) > 30*time.Second {
		turn.State = "interrupted"
		turn.Terminal = true
		turn.ErrorCategory = "execution"
	} else {
		turn.State = "accepted"
	}
	return turn, nil
}
func (c *OpenCodeClient) Abort(ctx context.Context, j Job) error {
	if !validSession(j.SessionID) || !validMessage(j.MessageID) || !cleanPrivatePath(j.Checkout) {
		return &OpenCodeError{Category: "invalid_session_id"}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var session openCodeSession
	if err := c.request(ctx, "GET", "/session/"+j.SessionID, j.Checkout, nil, &session); err != nil {
		return err
	}
	if session.ID != j.SessionID || session.Directory != j.Checkout || session.ParentID != "" || session.Time.Archived != 0 {
		return &OpenCodeError{Category: "foreign_session"}
	}
	turn, latestUser, err := c.readTurn(ctx, j)
	if err != nil {
		return err
	}
	if turn.Terminal {
		return nil
	}
	if latestUser != j.MessageID {
		return &OpenCodeError{Category: "turn_ownership_unknown"}
	}
	if err := c.request(ctx, "POST", "/session/"+j.SessionID+"/abort", j.Checkout, nil, nil); err != nil {
		return err
	}
	for {
		state, err := c.status(ctx, j)
		if err != nil {
			return err
		}
		if state == "idle" {
			return nil
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return &OpenCodeError{Category: "abort_unconfirmed"}
		case <-timer.C:
		}
	}
}

var _ OpenCode = (*OpenCodeClient)(nil)
