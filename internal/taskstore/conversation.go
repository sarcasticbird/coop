package taskstore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	c "github.com/sarcasticbird/coop/internal/coordination"
)

type Issue struct {
	Owner        string                     `json:"owner"`
	ClosedReason string                     `json:"closed_reason"`
	UID          string                     `json:"uid"`
	ProjectUID   string                     `json:"project_uid"`
	Title        string                     `json:"title"`
	Body         string                     `json:"body"`
	Status       string                     `json:"status"`
	Revision     int64                      `json:"revision"`
	Metadata     map[string]json.RawMessage `json:"metadata"`
}
type Comment struct {
	EditedAt   *time.Time `json:"edited_at,omitempty"`
	UID        string     `json:"uid"`
	IssueUID   string     `json:"issue_uid"`
	Author     string     `json:"author"`
	Body       string     `json:"body"`
	ReplyToUID string     `json:"reply_to_uid,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	Revision   string     `json:"revision"`
	EventID    int64      `json:"event_id"`
}
type CommentQuery struct {
	CommentUID string
	IssueUID   string
	Cursor     EventCursor
	Limit      int
}
type CommentPage struct {
	Comments []Comment
	Next     EventCursor
}
type CommentWrite struct {
	IssueUID   string `json:"issue_uid"`
	Body       string `json:"body"`
	ReplyToUID string `json:"reply_to_uid,omitempty"`
	RequestID  string `json:"request_id"`
}
type NotificationWrite struct {
	Correlation string
	IssueUID    string
	Recipient   string
	Message     string
}
type Notification struct {
	IssueUID  string
	Recipient string
	Message   string
	EventID   int64
	Revision  string
}
type EventCursor struct {
	InstanceUID string `json:"instance_uid"`
	ProjectUID  string `json:"project_uid"`
	AfterID     int64  `json:"after_id"`
	AfterUID    string `json:"after_uid,omitempty"`
}
type WorkEvent struct {
	ID                int64           `json:"event_id"`
	UID               string          `json:"event_uid"`
	OriginInstanceUID string          `json:"origin_instance_uid"`
	ProjectUID        string          `json:"project_uid"`
	IssueUID          string          `json:"issue_uid"`
	Type              string          `json:"type"`
	Actor             string          `json:"actor"`
	Payload           json.RawMessage `json:"payload"`
	CreatedAt         time.Time       `json:"created_at"`
}
type EventPage struct {
	Events []WorkEvent
	Next   EventCursor
}
type WorkBackend interface {
	InspectIssue(context.Context, string) (Issue, error)
	Comments(context.Context, CommentQuery) (CommentPage, error)
	AppendComment(context.Context, c.Principal, CommentWrite) (Comment, error)
	Notify(context.Context, c.Principal, NotificationWrite) (Notification, error)
	Events(context.Context, EventCursor, int) (EventPage, error)
}

// nativeHTTP uses the supported local daemon locator and native profile token.
// Redirects and remote endpoints are refused before attaching any credential.
func (k *kata) nativeHTTP(ctx context.Context) (*http.Client, string, error) {
	base := []string{"--workspace", k.cfg.Home, "--project", k.cfg.ProjectName, "--json"}
	raw, err := k.execute(ctx, base, "daemon", "locate")
	if err != nil {
		return nil, "", err
	}
	var endpoint struct{ Kind, Source, Network, Address string }
	if json.Unmarshal(raw, &endpoint) != nil || endpoint.Kind != "local" || endpoint.Source != "local_default" || endpoint.Network != "unix" {
		return nil, "", c.ErrForbidden
	}
	u, err := url.Parse(endpoint.Address)
	if err != nil || u.Scheme != "unix" || u.Host != "" || !filepath.IsAbs(u.Path) || u.RawQuery != "" || u.Fragment != "" {
		return nil, "", c.ErrForbidden
	}
	path := filepath.Join(k.cfg.Home, "config.toml")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", fmt.Errorf("read native auth profile: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ok || stat.Uid != uint32(os.Getuid()) {
		return nil, "", c.ErrForbidden
	}
	var profile struct {
		Auth struct {
			Token string `toml:"token"`
		} `toml:"auth"`
	}
	if _, err = toml.DecodeFile(path, &profile); err != nil {
		return nil, "", c.ErrInvalid
	}
	token := strings.TrimSpace(profile.Auth.Token)
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, "", c.ErrForbidden
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", u.Path)
	}, DisableKeepAlives: true}
	return &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return c.ErrForbidden }}, token, nil
}
func nativeCall(ctx context.Context, client *http.Client, token, method, path, key string, input, out any) error {
	return nativeCallIfMatch(ctx, client, token, method, path, key, input, out, 0)
}
func nativeCallIfMatch(ctx context.Context, client *http.Client, token, method, path, key string, input, out any, revision int64) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://kata.local"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if revision > 0 {
		req.Header.Set("If-Match", fmt.Sprintf(`"rev-%d"`, revision))
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("native Kata request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case 200, 201:
	case 400, 422:
		return c.ErrInvalid
	case 401, 403:
		return c.ErrForbidden
	case 404:
		return c.ErrNotFound
	case 409, 412:
		return c.ErrConflict
	default:
		return c.ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.MaxJSON+1))
	if err != nil {
		return err
	}
	if len(raw) > c.MaxJSON {
		return c.ErrSize
	}
	if json.Unmarshal(raw, out) != nil {
		return c.ErrInvalid
	}
	return nil
}
func (k *kata) nativeSession(ctx context.Context) (*http.Client, string, int64, error) {
	cl, token, err := k.nativeHTTP(ctx)
	if err != nil {
		return nil, "", 0, err
	}
	var instance struct {
		UID string `json:"instance_uid"`
	}
	if err = nativeCall(ctx, cl, token, "GET", "/api/v1/instance", "", nil, &instance); err != nil {
		return nil, "", 0, err
	}
	if k.cfg.InstanceUID == "" || instance.UID != k.cfg.InstanceUID {
		return nil, "", 0, c.ErrConflict
	}
	k.identityMu.Lock()
	defer k.identityMu.Unlock()
	if k.projectID > 0 {
		return cl, token, k.projectID, nil
	}
	var projects struct {
		Projects []struct {
			ID   int64  `json:"id"`
			UID  string `json:"uid"`
			Name string `json:"name"`
		} `json:"projects"`
	}
	if err = nativeCall(ctx, cl, token, "GET", "/api/v1/projects", "", nil, &projects); err != nil {
		return nil, "", 0, err
	}
	for _, p := range projects.Projects {
		if p.UID == k.cfg.ProjectUID && p.Name == k.cfg.ProjectName && p.ID > 0 {
			k.projectID = p.ID
			return cl, token, p.ID, nil
		}
	}
	return nil, "", 0, c.ErrConflict
}
func validUID(uid string) bool {
	_, err := c.KataTaskReference(uid, uid, "validation/repository")
	return err == nil
}
func (k *kata) InspectIssue(ctx context.Context, uid string) (Issue, error) {
	var out Issue
	if !validUID(uid) {
		return out, c.ErrInvalid
	}
	cl, token, pid, err := k.nativeSession(ctx)
	if err != nil {
		return out, err
	}
	// Native inbound selection excludes full comment history from issue inspection.
	// The bridge reads comments from bounded native event pages, never show history.
	var raw struct {
		Issue Issue `json:"issue"`
	}
	err = nativeCall(ctx, cl, token, "GET", fmt.Sprintf("/api/v1/projects/%d/issues/%s?inbound=coop-inspect-%s", pid, uid, rand.Text()), "", nil, &raw)
	if err == nil && (raw.Issue.UID != uid || raw.Issue.ProjectUID != k.cfg.ProjectUID || raw.Issue.Revision < 1) {
		err = c.ErrConflict
	}
	return raw.Issue, err
}
func (k *kata) AppendComment(ctx context.Context, actor c.Principal, in CommentWrite) (Comment, error) {
	var out Comment
	if !validUID(in.IssueUID) || !validRequest(in.RequestID) || actor.ID == "" || strings.TrimSpace(in.Body) == "" || len(in.Body) > c.MaxText || (in.ReplyToUID != "" && !validUID(in.ReplyToUID)) {
		return out, c.ErrInvalid
	}
	if _, err := k.InspectIssue(ctx, in.IssueUID); err != nil {
		return out, err
	}
	cl, token, pid, err := k.nativeSession(ctx)
	if err != nil {
		return out, err
	}
	input := map[string]any{"actor": actor.ID, "body": in.Body}
	if in.ReplyToUID != "" {
		input["reply_to"] = in.ReplyToUID
		input["kind"] = "reply"
		// Distinct Coop messages may share a parent. Kata otherwise allows
		// only one reply per author/kind; the immutable native key still
		// deduplicates retries and rejects changed payloads.
		input["force"] = true
	}
	var raw struct {
		Issue   Issue   `json:"issue"`
		Comment Comment `json:"comment"`
		Event   struct {
			ID  int64  `json:"id"`
			UID string `json:"uid"`
		} `json:"event"`
	}
	err = nativeCall(ctx, cl, token, "POST", fmt.Sprintf("/api/v1/projects/%d/issues/%s/comments", pid, in.IssueUID), CommentKey(in.RequestID), input, &raw)
	if err != nil {
		return out, err
	}
	if raw.Issue.UID != in.IssueUID || raw.Issue.ProjectUID != k.cfg.ProjectUID || !validUID(raw.Comment.UID) || raw.Comment.Body != in.Body || raw.Comment.ReplyToUID != in.ReplyToUID {
		return out, c.ErrConflict
	}
	raw.Comment.IssueUID = in.IssueUID
	raw.Comment.EventID = raw.Event.ID
	raw.Comment.Revision = raw.Event.UID
	return raw.Comment, nil
}
func (k *kata) Notify(ctx context.Context, actor c.Principal, in NotificationWrite) (Notification, error) {
	out := Notification{IssueUID: in.IssueUID, Recipient: in.Recipient, Message: in.Message}
	if !validUID(in.IssueUID) || !validRequest(in.Recipient) || len(in.Recipient) > 128 || strings.TrimSpace(in.Message) == "" || len(in.Message) > 1024 || actor.ID == "" {
		return out, c.ErrInvalid
	}
	issue, err := k.InspectIssue(ctx, in.IssueUID)
	if err != nil {
		return out, err
	}
	if issue.Status != "open" {
		return out, c.ErrConflict
	}
	cl, token, pid, err := k.nativeSession(ctx)
	if err != nil {
		return out, err
	}
	input := map[string]any{"actor": actor.ID, "patch": map[string]any{"notify." + base64.RawURLEncoding.EncodeToString([]byte(in.Recipient)): map[string]any{"from": actor.ID, "message": in.Message, "teammate": in.Correlation}}}
	var raw struct {
		Event *struct {
			ID  int64  `json:"id"`
			UID string `json:"uid"`
		} `json:"event"`
	}
	err = nativeCall(ctx, cl, token, "POST", fmt.Sprintf("/api/v1/projects/%d/issues/%s/metadata", pid, in.IssueUID), "", input, &raw)
	if raw.Event != nil {
		out.EventID = raw.Event.ID
		out.Revision = raw.Event.UID
	}
	return out, err
}
func (k *kata) Events(ctx context.Context, cursor EventCursor, limit int) (EventPage, error) {
	out := EventPage{Events: []WorkEvent{}, Next: cursor}
	if limit < 1 || limit > c.MaxPageRecords || cursor.AfterID < 0 {
		return out, c.ErrInvalid
	}
	if cursor.InstanceUID != k.cfg.InstanceUID || cursor.ProjectUID != k.cfg.ProjectUID {
		return out, c.ErrConflict
	}
	cl, token, pid, err := k.nativeSession(ctx)
	if err != nil {
		return out, err
	}
	var raw struct {
		Events []WorkEvent `json:"events"`
		Next   int64       `json:"next_after_id"`
		Reset  bool        `json:"reset_required"`
	}
	err = nativeCall(ctx, cl, token, "GET", fmt.Sprintf("/api/v1/projects/%d/events?after_id=%d&limit=%d", pid, cursor.AfterID, limit), "", nil, &raw)
	if errors.Is(err, c.ErrSize) && limit > 1 {
		return k.Events(ctx, cursor, limit/2)
	}
	if err != nil {
		return out, err
	}
	if raw.Reset || len(raw.Events) > limit || raw.Next < cursor.AfterID {
		return out, c.ErrConflict
	}
	last := cursor.AfterID
	for _, e := range raw.Events {
		if e.ID <= last || e.ID > raw.Next || e.ProjectUID != k.cfg.ProjectUID || e.UID == "" || e.OriginInstanceUID == "" {
			return out, c.ErrConflict
		}
		last = e.ID
	}
	if raw.Next != last {
		return out, c.ErrConflict
	}
	out.Events = raw.Events
	out.Next.AfterID = raw.Next
	if len(raw.Events) > 0 {
		out.Next.AfterUID = raw.Events[len(raw.Events)-1].UID
	}
	return out, nil
}
func (k *kata) Comments(ctx context.Context, in CommentQuery) (CommentPage, error) {
	out := CommentPage{Comments: []Comment{}}
	if !validUID(in.IssueUID) {
		return out, c.ErrInvalid
	}
	if in.CommentUID != "" {
		if !validUID(in.CommentUID) {
			return out, c.ErrInvalid
		}
		cl, token, pid, err := k.nativeSession(ctx)
		if err != nil {
			return out, err
		}
		comment, err := k.currentComment(ctx, cl, token, pid, in.IssueUID, in.CommentUID)
		if err != nil {
			return out, err
		}
		out.Comments = append(out.Comments, comment)
		return out, nil
	}
	page, err := k.Events(ctx, in.Cursor, in.Limit)
	out.Next = page.Next
	if err != nil {
		return out, err
	}
	var cl *http.Client
	var token string
	var pid int64
	seen := map[string]int{}
	for _, e := range page.Events {
		if e.IssueUID != in.IssueUID {
			continue
		}
		snapshot, ok := EventComment(e)
		if !ok {
			continue
		}
		if cl == nil {
			cl, token, pid, err = k.nativeSession(ctx)
			if err != nil {
				return out, err
			}
		}
		comment, err := k.currentComment(ctx, cl, token, pid, in.IssueUID, snapshot.UID)
		if err != nil {
			return out, err
		}
		comment.EventID = e.ID
		comment.Revision = e.UID
		if i, exists := seen[comment.UID]; exists {
			out.Comments[i] = comment
		} else {
			seen[comment.UID] = len(out.Comments)
			out.Comments = append(out.Comments, comment)
		}
	}
	return out, nil
}

// EventComment decodes immutable comment snapshots from native journal events.
func EventComment(e WorkEvent) (Comment, bool) {
	var comment Comment
	if e.Type != "issue.commented" && e.Type != "comment.edited" && e.Type != "issue.comment_edited" {
		return comment, false
	}
	var payload struct {
		UID    string `json:"comment_uid"`
		Body   string `json:"body"`
		Author string `json:"author"`
		Reply  string `json:"reply_to_uid"`
	}
	raw := e.Payload
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = []byte(encoded)
	}
	if json.Unmarshal(raw, &payload) != nil || !validUID(payload.UID) {
		return comment, false
	}
	comment = Comment{UID: payload.UID, IssueUID: e.IssueUID, Body: payload.Body, Author: payload.Author, ReplyToUID: payload.Reply, CreatedAt: e.CreatedAt, EventID: e.ID, Revision: e.UID}
	if comment.Author == "" {
		comment.Author = e.Actor
	}
	return comment, true
}

// CommentKey is the stable native publication identity carried by event receipts.
func CommentKey(requestID string) string { return "coop-comment-" + hash(requestID) }
