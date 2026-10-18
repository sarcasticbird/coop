package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	CheckContinuation(context.Context, Job) error
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

// Native messages include diff summaries and tool output even when callers
// only need ownership metadata. Keep their wire budget separate from catalogs
// and GitHub pages; ancestry is still limited to three bounded history reads.
const maxNativeMessagePage = 16 << 20

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
	return c.requestBounded(ctx, method, path, dir, body, output, MaxAPIPage)
}

func (c *OpenCodeClient) requestBounded(ctx context.Context, method, path, dir string, body any, output any, responseLimit int) error {
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
	raw, err := io.ReadAll(io.LimitReader(res.Body, int64(responseLimit)+1))
	if err != nil || len(raw) > responseLimit {
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
		if cfg.ModelVariant != "" {
			body["model"].(map[string]string)["variant"] = cfg.ModelVariant
		}
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
	turn := Turn{SessionID: j.SessionID, MessageID: j.MessageID, State: "not_submitted"}
	if !validSession(j.SessionID) || !validMessage(j.MessageID) || j.Deadline.IsZero() || len(prompt) > 32<<10 || len(prompt) == 0 {
		return turn, &OpenCodeError{Category: "invalid_submission_intent"}
	}
	model, err := configuredModel(cfg)
	if err != nil {
		return turn, err
	}
	if err := c.CheckIdle(ctx, j); err != nil {
		return turn, err
	}
	if err := c.preflightModelVariantInDirectory(ctx, cfg, j.Checkout); err != nil {
		return turn, err
	}
	body := modelPrompt(model)
	body["messageID"] = j.MessageID
	body["system"] = "This is an automated Coop worker message, not a new instruction or goal approval from the human owner."
	body["parts"] = []map[string]string{{"type": "text", "text": prompt}}
	if cfg.ModelVariant != "" {
		// The directory-specific probe may outlive an idle manual session.
		if err := c.CheckIdle(ctx, j); err != nil {
			return turn, err
		}
	}
	if j.ContinuationMessageID != "" {
		previous := j
		previous.MessageID = j.ContinuationMessageID
		if err := c.CheckContinuation(ctx, previous); err != nil {
			return turn, err
		}
	}
	turn.SubmittedModel = model
	turn.State = "unknown"
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
		Model                                 *ModelIdentity
		ProviderID, ModelID, Variant          string
		Summary                               compactionSummary
		Time                                  struct{ Created, Completed int64 }
		Error                                 *struct{ Name string }
	}
	Parts []struct {
		Type                      string
		Auto, Overflow, Synthetic bool
		Metadata                  struct {
			CompactionContinue bool `json:"compaction_continue"`
		}
	}
}

// User-message summaries contain arbitrary diff data. Only the literal native
// boolean true marks a compaction assistant, so do not retain summary objects.
type compactionSummary bool

func (s *compactionSummary) UnmarshalJSON(raw []byte) error {
	*s = compactionSummary(bytes.Equal(raw, []byte("true")))
	return nil
}

func (m messageEnvelope) autoCompaction() bool {
	return len(m.Parts) == 1 && m.Parts[0].Type == "compaction" && m.Parts[0].Auto && !m.Parts[0].Overflow
}

func (m messageEnvelope) compactionContinuation() bool {
	// Native 1.18.18 marks its injected continuation explicitly. Synthetic text
	// alone is insufficient, and overflow replay has no equivalent lineage proof.
	return len(m.Parts) == 1 && m.Parts[0].Type == "text" && m.Parts[0].Synthetic && m.Parts[0].Metadata.CompactionContinue
}

func (m messageEnvelope) summary() bool { return bool(m.Info.Summary) }

// readTurn reconciles the exact worker message even when a later manual turn
// makes the session busy. Session status alone does not establish turn ownership.
func (c *OpenCodeClient) readTurn(ctx context.Context, j Job) (Turn, string, error) {
	turn := Turn{SessionID: j.SessionID, MessageID: j.MessageID, State: "unknown"}
	if !validSession(j.SessionID) || !validMessage(j.MessageID) {
		return turn, "", &OpenCodeError{Category: "invalid_submission_intent"}
	}
	var message messageEnvelope
	if err := c.requestBounded(ctx, "GET", "/session/"+j.SessionID+"/message/"+j.MessageID, j.Checkout, nil, &message, maxNativeMessagePage); err != nil {
		return turn, "", err
	}
	if message.Info.ID != j.MessageID || message.Info.SessionID != j.SessionID || message.Info.Role != "user" {
		return turn, "", &OpenCodeError{Category: "foreign_message"}
	}
	turn.Accepted = true
	turn.SubmittedModel = message.Info.Model
	var messages []messageEnvelope
	// Most observations need only the tail. Compaction can move the saved
	// parent further back, so expand within a fixed request and response budget.
	for limit := 20; ; limit *= 4 {
		// json.Unmarshal reuses slice entries and leaves omitted fields intact.
		// Each native response must supply its own complete observation.
		messages = nil
		if err := c.requestBounded(ctx, "GET", fmt.Sprintf("/session/%s/message?limit=%d", j.SessionID, limit), j.Checkout, nil, &messages, maxNativeMessagePage); err != nil {
			return turn, "", err
		}
		anchored := slices.ContainsFunc(messages, func(m messageEnvelope) bool {
			return m.Info.ID == j.MessageID || (m.Info.Role == "assistant" && m.Info.ParentID == j.MessageID)
		})
		if anchored || len(messages) < limit || limit >= 320 {
			break
		}
	}
	var last *messageEnvelope
	latestUser := ""
	visibleUser := false
	ownedUser := j.MessageID
	compaction, summary := "", ""
	compacted := false
	for i := range messages {
		m := &messages[i]
		if m.Info.SessionID != j.SessionID || !validMessage(m.Info.ID) {
			return turn, "", &OpenCodeError{Category: "foreign_message"}
		}
		// The native message endpoint returns the bounded tail oldest first.
		if m.Info.Role == "user" {
			switch {
			case m.Info.ID == j.MessageID:
				ownedUser, compaction, summary = j.MessageID, "", ""
			case m.autoCompaction() && latestUser == ownedUser && latestUser != "":
				compaction, summary = m.Info.ID, ""
				last, compacted = nil, true
				turn.ExecutedModel = nil
			case m.compactionContinuation() && compaction != "" && latestUser == compaction && summary != "" && i > 0 && messages[i-1].Info.ID == summary:
				ownedUser, compaction, summary = m.Info.ID, "", ""
				last = nil
				turn.ExecutedModel = nil
			default:
				compaction, summary = "", ""
			}
			latestUser = m.Info.ID
			visibleUser = true
		} else if m.Info.Role != "assistant" || !validMessage(m.Info.ParentID) {
			return turn, "", &OpenCodeError{Category: "foreign_message"}
		}
		// A visible user takes precedence over late assistant replies. Without
		// one, any foreign parent makes ownership ambiguous; a still-finishing
		// owned assistant must not hide that evidence of manual work.
		if m.Info.Role == "assistant" && !visibleUser && (latestUser == "" || m.Info.ParentID != ownedUser) {
			latestUser = m.Info.ParentID
		}
		if m.Info.Role == "assistant" && m.Info.ParentID == compaction && m.summary() && m.Info.Error == nil && m.Info.Time.Completed > 0 && m.Info.Finish == "stop" {
			summary = m.Info.ID
		}
		if m.Info.Role == "assistant" && compaction == "" && m.Info.ParentID == ownedUser && !m.summary() && (last == nil || m.Info.Time.Created >= last.Info.Time.Created) {
			turn.ExecutedModel = &ModelIdentity{ProviderID: m.Info.ProviderID, ModelID: m.Info.ModelID, Variant: m.Info.Variant}
			last = m
		}
	}
	if latestUser == ownedUser || (compaction != "" && latestUser == compaction) {
		latestUser = j.MessageID
	}
	if compacted {
		turn.State = "compacting"
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
	persistedModel := turn.SubmittedModel
	if j.SubmittedModel != nil {
		// Keep the durable submission intent authoritative on subsequent polls.
		turn.SubmittedModel = j.SubmittedModel
	}
	if err != nil {
		return turn, err
	}
	if !turn.Terminal && latestUser != j.MessageID {
		return turn, &OpenCodeError{Category: "turn_ownership_unknown"}
	}
	// Ownership reconciliation must finish before enforcing model policy. Abort
	// uses the same history scan and must still be able to stop an owned mismatch.
	if j.SubmittedModel != nil && (persistedModel == nil || *persistedModel != *j.SubmittedModel) {
		turn.State, turn.Terminal = "unknown", false
		return turn, &OpenCodeError{Category: "submitted_model_mismatch"}
	}
	if turn.SubmittedModel != nil && turn.SubmittedModel.Variant != "" && turn.ExecutedModel != nil && *turn.ExecutedModel != *turn.SubmittedModel {
		turn.State, turn.Terminal = "unknown", false
		return turn, &OpenCodeError{Category: "executed_model_mismatch"}
	}
	if turn.Terminal {
		return turn, nil
	}
	status, err := c.status(ctx, j)
	if err != nil {
		return turn, err
	}
	if turn.State == "compacting" && status == "idle" {
		return turn, &OpenCodeError{Category: "compaction_incomplete"}
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

// Terminal observation alone can reconcile an old turn after a manual takeover.
// Continuation additionally requires the idle session's latest owned lineage.
func (c *OpenCodeClient) CheckContinuation(ctx context.Context, j Job) error {
	if err := c.CheckIdle(ctx, j); err != nil {
		return err
	}
	turn, latestUser, err := c.readTurn(ctx, j)
	if err != nil {
		return err
	}
	if !turn.Terminal || latestUser != j.MessageID {
		return &OpenCodeError{Category: "continuation_ownership_unknown"}
	}
	return nil
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
