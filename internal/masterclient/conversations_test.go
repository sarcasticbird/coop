package masterclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sarcasticbird/coop/internal/mastermcp"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/taskstore"
)

func TestNativeConversationAuthenticatedClient(t *testing.T) {
	home := os.Getenv("COOP_KATA_TEST_HOME")
	if home == "" {
		t.Skip("requires explicitly selected disposable native profile")
	}
	ctx := context.Background()
	provider, err := taskstore.New(taskstore.Config{Provider: "kata", Binary: "/opt/homebrew/bin/kata", Home: home, ProjectName: "coop-consolidation-acceptance", ProjectUID: "01M5N8JN3PA04EJRZSHHKHY4TJ", InstanceUID: "01M5N8JCKTYDTF0CE5FXR9AKX8", PolicySHA: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	work := provider.(taskstore.WorkBackend)
	cursor := taskstore.EventCursor{InstanceUID: "01M5N8JCKTYDTF0CE5FXR9AKX8", ProjectUID: "01M5N8JN3PA04EJRZSHHKHY4TJ"}
	for {
		page, e := work.Events(ctx, cursor, 100)
		if e != nil {
			t.Fatal(e)
		}
		if page.Next.AfterID == cursor.AfterID {
			break
		}
		cursor = page.Next
	}
	s, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.db"), MasterID: "native-client-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	owner := c.Principal{ID: "owner", Alias: "owner", Kind: "owner", Engagements: []string{"acceptance"}, Capabilities: []string{"read", "send", "manage"}}
	bridge := c.Principal{ID: "native-bridge", Alias: "native-bridge", Kind: "service", Engagements: []string{"acceptance"}, Capabilities: []string{"read", "send"}}
	bird := c.Principal{ID: "rook", Alias: "rook", Kind: "agent", Engagements: []string{"acceptance"}, Capabilities: []string{"read", "send"}}
	hark := c.Principal{ID: "hark", Alias: "hark", Kind: "service", Engagements: []string{"acceptance"}, Capabilities: []string{"read", "send"}}
	if err = s.Configure(ctx, []c.Principal{owner, bridge, bird, hark}, nil); err != nil {
		t.Fatal(err)
	}
	th, err := s.CreateThread(ctx, owner, c.CreateThread{Title: "Native client acceptance", Engagement: "acceptance", Members: []string{owner.ID, bridge.ID, bird.ID, hark.ID}, RequestID: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := master.ConversationConfig{InstanceUID: cursor.InstanceUID, ProjectUID: cursor.ProjectUID, BridgePrincipal: bridge.ID, IntakePrincipal: bird.ID, TeamThreadID: th.ID, TeamIssueUID: "01M5N8JN47ME8RRF4482K89JDJ"}
	if err = s.ConfigureConversations(ctx, work, cfg); err != nil {
		t.Fatal(err)
	}
	token := "isolated-native-client-token"
	hash := sha256.Sum256([]byte(token))
	policy := &master.Policy{AllowedHosts: []string{"127.0.0.1"}, Tokens: []master.Token{{PrincipalID: owner.ID, SHA256: hex.EncodeToString(hash[:])}}}
	harkHash := sha256.Sum256([]byte("native-hark-fixture-token"))
	policy.Tokens = append(policy.Tokens, master.Token{PrincipalID: hark.ID, SHA256: hex.EncodeToString(harkHash[:])})
	service := &master.Service{Store: s, Tasks: provider}
	service.MCPHandler = func(p c.Principal) http.Handler {
		return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mastermcp.NewServer(service, p) }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: c.MaxJSON, DisableLocalhostProtection: true})
	}
	ts := httptest.NewTLSServer(master.NewHandler(service, policy))
	defer ts.Close()
	client, err := New(ClientConfig{URL: ts.URL, MasterID: "native-client-test", Token: token, HTTPClient: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.MapConversation(ctx, c.MapConversation{ThreadID: th.ID, Source: c.ConversationSource{Provider: "kata", InstanceUID: cursor.InstanceUID, ProjectUID: cursor.ProjectUID, IssueUID: cfg.TeamIssueUID}, AfterEventID: cursor.AfterID, RequestID: "map"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Send(ctx, c.SendMessage{ThreadID: th.ID, Recipients: []string{bird.ID}, Body: "Authenticated client canonical native delivery", RequestID: "native-send"})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Source == nil || receipt.Source.CommentUID == "" || receipt.Source.VerifiedSenderID != owner.ID {
		t.Fatalf("source receipt %+v", receipt)
	}
	if err = s.PollConversations(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending(ctx, bird, c.PendingQuery{})
	if err != nil || len(pending.Deliveries) != 1 {
		t.Fatalf("native echo duplicate %+v %v", pending, err)
	}
	messages, err := client.Messages(ctx, th.ID, cursor.AfterID)
	if err != nil || len(messages.Messages) != 1 || messages.Messages[0].ID != receipt.MessageID {
		t.Fatalf("canonical read %+v %v", messages, err)
	}
	harkHTTP := ts.Client()
	harkHTTP.Transport = nativeHarkAuth{base: harkHTTP.Transport}
	harkClient := mcp.NewClient(&mcp.Implementation{Name: "native-hark-acceptance", Version: "1"}, nil)
	session, err := harkClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: harkHTTP}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	read, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "coop_master_read_thread", Arguments: map[string]any{"thread_id": th.ID, "after": cursor.AfterID}})
	if err != nil || read.IsError {
		t.Fatalf("Hark native read failed %+v %v", read, err)
	}
	sent, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "coop_master_send", Arguments: map[string]any{"thread_id": th.ID, "body": "Hark replies through existing authenticated Coop MCP", "parent_message_id": receipt.MessageID, "request_id": "hark-native-reply"}})
	if err != nil || sent.IsError {
		t.Fatalf("Hark native send failed %+v %v", sent, err)
	}
	messages, err = client.Messages(ctx, th.ID, cursor.AfterID)
	if err != nil || len(messages.Messages) != 2 || messages.Messages[1].Source.VerifiedSenderID != hark.ID {
		t.Fatalf("Hark source authentication lost %+v %v", messages, err)
	}
	rawComment, err := work.AppendComment(ctx, c.Principal{ID: bird.ID}, taskstore.CommentWrite{IssueUID: cfg.TeamIssueUID, Body: "Native bot context without a gateway delivery", RequestID: "raw-bot-" + receipt.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	replyToRaw, err := client.Send(ctx, c.SendMessage{ThreadID: th.ID, ParentMessageID: rawComment.UID, Body: "Owner replies to unaddressed native context", RequestID: "reply-native-context"})
	if err != nil || replyToRaw.Source == nil {
		t.Fatalf("canonical untracked reply %+v %v", replyToRaw, err)
	}
	canonicalReply, err := work.Comments(ctx, taskstore.CommentQuery{IssueUID: cfg.TeamIssueUID, CommentUID: replyToRaw.Source.CommentUID, Limit: 1})
	if err != nil || len(canonicalReply.Comments) != 1 || canonicalReply.Comments[0].ReplyToUID != rawComment.UID {
		t.Fatalf("native reply ancestry %+v %v", canonicalReply, err)
	}

}

type nativeHarkAuth struct{ base http.RoundTripper }

func (a nativeHarkAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer native-hark-fixture-token")
	r.Header.Set("X-Coop-Expected-Master-ID", "native-client-test")
	return a.base.RoundTrip(r)
}
