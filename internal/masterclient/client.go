// Package masterclient provides the identity-pinned master protocol client.
package masterclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

type ClientConfig struct {
	URL, MasterID, Token string
	AllowHTTP            bool
	HTTPClient           *http.Client
}
type Client struct {
	cfg  ClientConfig
	http *http.Client
}

func (cl *Client) MasterID() string { return cl.cfg.MasterID }
func (cl *Client) Self(ctx context.Context) (out c.SelfIdentity, err error) {
	err = cl.call(ctx, "GET", "/api/v1/identity", nil, &out)
	return
}

func (cl *Client) ApprovedGoals(ctx context.Context, engagement, after string) (out c.GoalPage, err error) {
	err = cl.call(ctx, "GET", "/api/v1/goals?engagement="+url.QueryEscape(engagement)+"&after="+url.QueryEscape(after), nil, &out)
	return
}
func (cl *Client) RegisterArtifact(ctx context.Context, in c.ArtifactLink) (out c.ArtifactLink, err error) {
	err = cl.call(ctx, "POST", "/api/v1/artifacts", in, &out)
	return
}
func (cl *Client) Artifacts(ctx context.Context, in c.ArtifactQuery) (out c.ArtifactPage, err error) {
	err = cl.call(ctx, "POST", "/api/v1/artifacts/list", in, &out)
	return
}

func New(cfg ClientConfig) (*Client, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || cfg.MasterID == "" || len(cfg.Token) < 12 {
		return nil, c.ErrInvalid
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !cfg.AllowHTTP || ip == nil || !ip.IsLoopback() {
			return nil, c.ErrInvalid
		}
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	cl := cfg.HTTPClient
	if cl == nil {
		cl = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second}}
	}
	copyClient := *cl
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{cfg: cfg, http: &copyClient}, nil
}
func (cl *Client) pinRequest(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+cl.cfg.Token)
	req.Header.Set("X-Coop-Expected-Master-ID", cl.cfg.MasterID)
}
func (cl *Client) open(ctx context.Context, method, path string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil || len(b) > c.MaxJSON {
			return nil, c.ErrSize
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, cl.cfg.URL+path, body)
	if err != nil {
		return nil, c.ErrInvalid
	}
	cl.pinRequest(req)
	req.Header.Set("Content-Type", "application/json")
	r, err := cl.http.Do(req)
	if err != nil {
		return nil, c.ErrUnavailable
	}
	if r.Header.Get("X-Coop-Master-ID") != cl.cfg.MasterID {
		_ = r.Body.Close()
		if r.StatusCode == http.StatusNotFound && r.Header.Get("X-Coop-Master-ID") == "" && strings.HasPrefix(path, "/api/v1/github-cache/") {
			return nil, errGitHubCacheRoute
		}
		if r.StatusCode == http.StatusNotFound && r.Header.Get("X-Coop-Master-ID") == "" && (path == "/api/v1/diagnostics" || strings.HasPrefix(path, "/api/v1/team/") && strings.HasSuffix(path, "/diagnostics")) {
			return nil, errDiagnosticRoute
		}
		return nil, c.ErrConflict
	}
	return r, nil
}

var errDiagnosticRoute = errors.New("diagnostic_route_missing")

func (cl *Client) call(ctx context.Context, method, path string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r, err := cl.open(ctx, method, path, in)
	if err != nil {
		return err
	}
	defer func() { _ = r.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(r.Body, c.MaxPageBytes+1))
	if err != nil {
		return c.ErrUnavailable
	}
	if len(raw) > c.MaxPageBytes {
		return c.ErrSize
	}
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		for _, known := range []error{c.ErrInvalid, c.ErrForbidden, c.ErrNotFound, c.ErrConflict, c.ErrCapacity, c.ErrSize, c.ErrEpoch, c.ErrUnsupported} {
			if e.Error == known.Error() {
				return known
			}
		}
		if r.StatusCode == 401 {
			return c.ErrForbidden
		}
		return c.ErrUnavailable
	}
	var env struct {
		MasterID     string          `json:"master_id"`
		RestoreEpoch int64           `json:"restore_epoch"`
		Data         json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil || env.MasterID != cl.cfg.MasterID || env.RestoreEpoch < 1 {
		return c.ErrConflict
	}
	if out != nil && json.Unmarshal(env.Data, out) != nil {
		return c.ErrUnavailable
	}
	return nil
}
func (cl *Client) Send(ctx context.Context, in c.SendMessage) (out c.MessageReceipt, err error) {
	err = cl.call(ctx, "POST", "/api/v1/messages", in, &out)
	return
}
func (cl *Client) PublishReply(ctx context.Context, in c.PublishReply) (out c.MessageReceipt, err error) {
	err = cl.call(ctx, "POST", "/api/v1/replies", in, &out)
	return
}
func (cl *Client) Pending(ctx context.Context, in c.PendingQuery) (out c.DeliveryPage, err error) {
	err = cl.call(ctx, "GET", "/api/v1/pending?after="+url.QueryEscape(in.After), nil, &out)
	return
}
func (cl *Client) SetDelivery(ctx context.Context, in c.DeliveryUpdate) error {
	return cl.call(ctx, "POST", "/api/v1/deliveries", in, nil)
}
func (cl *Client) Heartbeat(ctx context.Context, in c.Heartbeat) error {
	return cl.call(ctx, "POST", "/api/v1/heartbeat", in, nil)
}
func (cl *Client) CreateThread(ctx context.Context, in c.CreateThread) (out c.Thread, err error) {
	err = cl.call(ctx, "POST", "/api/v1/threads", in, &out)
	return
}
func (cl *Client) Threads(ctx context.Context, query c.ThreadQuery) (out c.ThreadPage, err error) {
	err = cl.call(ctx, "GET", "/api/v1/threads?after="+url.QueryEscape(query.After), nil, &out)
	return
}
func (cl *Client) Team(ctx context.Context) (out []c.Presence, err error) {
	err = cl.call(ctx, "GET", "/api/v1/team", nil, &out)
	return
}
func (cl *Client) Messages(ctx context.Context, thread string, after int64) (out c.MessagePage, err error) {
	err = cl.call(ctx, "GET", "/api/v1/threads/"+url.PathEscape(thread)+"/messages?after="+jsonNumber(after), nil, &out)
	return
}

func (cl *Client) SetMembers(ctx context.Context, in c.SetMembers) (out c.Thread, err error) {
	err = cl.call(ctx, "POST", "/api/v1/thread-members", in, &out)
	return
}
func (cl *Client) Message(ctx context.Context, id string) (out c.Message, err error) {
	err = cl.call(ctx, "GET", "/api/v1/messages/"+url.PathEscape(id), nil, &out)
	return
}
func (cl *Client) MessageStatus(ctx context.Context, id string) (out c.MessageReceipt, err error) {
	err = cl.call(ctx, "GET", "/api/v1/messages/"+url.PathEscape(id)+"/status", nil, &out)
	return
}
func jsonNumber(v int64) string { b, _ := json.Marshal(v); return string(b) }
func (cl *Client) Ping(ctx context.Context, in c.Ping) (out c.MessageReceipt, err error) {
	err = cl.call(ctx, "POST", "/api/v1/ping", in, &out)
	return
}
func (cl *Client) ProposeGoal(ctx context.Context, in c.GoalProposal) (out c.GoalRevision, err error) {
	err = cl.call(ctx, "POST", "/api/v1/goals", in, &out)
	return
}
func (cl *Client) DecideGoal(ctx context.Context, in c.GoalDecisionInput) (out c.GoalAuthority, err error) {
	err = cl.call(ctx, "POST", "/api/v1/goal-decisions", in, &out)
	return
}
func (cl *Client) ResolveGoal(ctx context.Context, in c.GoalRef) (out c.GoalAuthority, err error) {
	err = cl.call(ctx, "POST", "/api/v1/goal-authority", in, &out)
	return
}

func (cl *Client) RecoveryStatus(ctx context.Context) (out c.RecoveryStatus, err error) {
	err = cl.call(ctx, "GET", "/api/v1/recovery", nil, &out)
	return
}
func (cl *Client) ReconcileGuest(ctx context.Context, in c.GuestRecovery) (out c.RecoveryStatus, err error) {
	err = cl.call(ctx, "POST", "/api/v1/recovery/report", in, &out)
	return
}
func (cl *Client) RecoveryEvidence(ctx context.Context, agent, after string) (out []c.RecoveryEvidence, err error) {
	err = cl.call(ctx, "GET", "/api/v1/recovery/evidence?agent="+url.QueryEscape(agent)+"&after="+url.QueryEscape(after), nil, &out)
	return
}
func (cl *Client) ResolveRecovery(ctx context.Context, in c.RecoveryResolution) error {
	return cl.call(ctx, "POST", "/api/v1/recovery/resolve", in, nil)
}
func (cl *Client) RecoveryControl(ctx context.Context, op string, epoch int64) error {
	if op != "pause" && op != "resume" {
		return c.ErrInvalid
	}
	return cl.call(ctx, "POST", "/api/v1/recovery/"+op, map[string]int64{"restore_epoch": epoch}, nil)
}
func (cl *Client) SetPlacement(ctx context.Context, in c.PlacementChange) (out c.Placement, err error) {
	err = cl.call(ctx, "POST", "/api/v1/placement", in, &out)
	return
}
func (cl *Client) ImportHistory(ctx context.Context, in c.HistoryImport) (out c.HistoryRecord, err error) {
	err = cl.call(ctx, "POST", "/api/v1/history", in, &out)
	return
}
func (cl *Client) History(ctx context.Context, thread, after string) (out []c.HistoryRecord, err error) {
	err = cl.call(ctx, "GET", "/api/v1/history?thread="+url.QueryEscape(thread)+"&after="+url.QueryEscape(after), nil, &out)
	return
}
func (cl *Client) Cutover(ctx context.Context, in c.SourceCutover) (out c.SourceCutover, err error) {
	err = cl.call(ctx, "POST", "/api/v1/cutover", in, &out)
	return
}

func (cl *Client) Backup(ctx context.Context, directory string) (out c.BackupManifest, err error) {
	err = cl.call(ctx, "POST", "/api/v1/backup", map[string]string{"directory": directory}, &out)
	return
}

func (cl *Client) Thread(ctx context.Context, id string) (out c.Thread, err error) {
	err = cl.call(ctx, "GET", "/api/v1/threads/"+url.PathEscape(id), nil, &out)
	return
}

func (cl *Client) PlacementProof(ctx context.Context) (out c.PlacementProof, err error) {
	err = cl.call(ctx, "GET", "/api/v1/placement/transition", nil, &out)
	return
}

func (cl *Client) OwnDelivery(ctx context.Context, id string) (out c.Delivery, err error) {
	err = cl.call(ctx, "GET", "/api/v1/deliveries/"+url.PathEscape(id), nil, &out)
	return
}

// MapConversation pins a private thread to its canonical native issue.
func (cl *Client) MapConversation(ctx context.Context, in c.MapConversation) (out c.Thread, err error) {
	err = cl.call(ctx, "POST", "/api/v1/conversation-sources", in, &out)
	return
}

func (cl *Client) ConversationObservations(ctx context.Context, after string) (out c.ConversationObservationPage, err error) {
	err = cl.call(ctx, "GET", "/api/v1/conversation-observations?after="+url.QueryEscape(after), nil, &out)
	return
}

func (cl *Client) ConversationCursor(ctx context.Context) (out c.ConversationCursor, err error) {
	err = cl.call(ctx, "GET", "/api/v1/conversation-cursor", nil, &out)
	return
}
func (cl *Client) ReacceptConversationCursor(ctx context.Context, in c.ReacceptConversationCursor) (out c.ConversationCursor, err error) {
	err = cl.call(ctx, "POST", "/api/v1/conversation-cursor/reaccept", in, &out)
	return
}
func (cl *Client) LegacyMessages(ctx context.Context, thread string, after int64) (out c.MessagePage, err error) {
	err = cl.call(ctx, "GET", "/api/v1/threads/"+url.PathEscape(thread)+"/legacy-messages?after="+strconv.FormatInt(after, 10), nil, &out)
	return
}
