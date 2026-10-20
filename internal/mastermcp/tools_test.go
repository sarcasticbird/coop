package mastermcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
)

func TestMasterMCPIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.db"), MasterID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	p := c.Principal{ID: "agent", Kind: "agent", Alias: "agent", Engagements: []string{"coop"}, Capabilities: []string{"read"}}
	if err = store.Configure(ctx, []c.Principal{p}, nil); err != nil {
		t.Fatal(err)
	}
	server := NewServer(&master.Service{Store: store}, p)
	a, b := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "coop_master_threads" {
			found = true
		}
		if tool.Name == "coop_master_decide_goal" || tool.Name == "coop_exec" || tool.Name == "coop_lifecycle" {
			t.Fatalf("unauthorized tool %s", tool.Name)
		}
	}
	if !found {
		t.Fatal("missing conversation tool")
	}
	status, statusErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "coop_master_agent_status", Arguments: map[string]any{"agent_id": "unowned"}})
	if statusErr != nil || !status.IsError || status.Content[0].(*mcp.TextContent).Text != "not_found" {
		t.Fatal("missing safe status read", status, statusErr)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "coop_master_send", Arguments: map[string]any{"thread_id": "unowned", "body": "message", "request_id": "req"}})
	if err == nil && result != nil && !result.IsError {
		t.Fatal("read principal sent a message")
	}
}

type authenticated struct{ base http.RoundTripper }

func (a authenticated) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer service-test-token")
	r.Header.Set("X-Coop-Expected-Master-ID", "mcp-test")
	return a.base.RoundTrip(r)
}

func TestMasterMCPAuthenticatedHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.db"), MasterID: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	owner := c.Principal{ID: "owner", Kind: "owner", Alias: "owner", Engagements: []string{"coop"}, Capabilities: []string{"read", "send", "manage", "decide"}}
	chief := c.Principal{ID: "chief", Kind: "service", Alias: "chief", Engagements: []string{"coop"}, Capabilities: []string{"read", "send", "decide"}}
	if err = s.Configure(ctx, []c.Principal{owner, chief}, nil); err != nil {
		t.Fatal(err)
	}
	th, err := s.CreateThread(ctx, owner, c.CreateThread{Title: "Private", Engagement: "coop", Members: []string{chief.ID}, RequestID: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	service := &master.Service{Store: s}
	service.MCPHandler = func(p c.Principal) http.Handler {
		return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return NewServer(service, p) }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: c.MaxJSON, DisableLocalhostProtection: true})
	}
	hash := sha256.Sum256([]byte("service-test-token"))
	ts := httptest.NewTLSServer(master.NewHandler(service, &master.Policy{AllowedHosts: []string{"127.0.0.1"}, Tokens: []master.Token{{PrincipalID: chief.ID, SHA256: hex.EncodeToString(hash[:])}}}))
	defer ts.Close()
	httpClient := ts.Client()
	httpClient.Transport = authenticated{httpClient.Transport}
	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "coop_master_decide_goal" || tool.Name == "coop_master_create_thread" {
			t.Fatalf("service received owner tool %s", tool.Name)
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "coop_master_send", Arguments: map[string]any{"thread_id": th.ID, "body": "A status note through MCP.", "request_id": "mcp-note"}})
	if err != nil || result.IsError {
		t.Fatal("authenticated send failed", err)
	}
	page, err := s.Messages(ctx, owner, th.ID, 0)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].AuthorID != chief.ID {
		t.Fatal("MCP principal binding failed", err)
	}
	denied, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "coop_master_decide_goal", Arguments: map[string]any{}})
	if err == nil && denied != nil && !denied.IsError {
		t.Fatal("chief service approved a goal")
	}
}
