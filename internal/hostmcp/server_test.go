package hostmcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sarcasticbird/coop/internal/hostcontrol"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureServer(t *testing.T) (http.Handler, hostcontrol.Host) {
	t.Helper()
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256([]byte("test-only-token"))
	raw, e := json.Marshal(map[string]string{"chief": hex.EncodeToString(digest[:])})
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, "verifiers.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	cfg := hostcontrol.Config{Version: 1, Label: "fixture", Listen: "127.0.0.1:0", StateDir: filepath.Join(root, "state"), VerifierFile: path, AllowedHosts: []string{"127.0.0.1"}, Callers: []hostcontrol.CallerConfig{{ID: "chief", Capabilities: []string{"read"}}}}
	h, close, e := hostcontrol.OpenHost(cfg.StateDir, cfg.Label)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = close() })
	control, e := hostcontrol.NewControl(cfg)
	if e != nil {
		t.Fatal(e)
	}
	policy, e := hostcontrol.NewPolicy(cfg)
	if e != nil {
		t.Fatal(e)
	}
	ledger, e := hostcontrol.OpenLedger(filepath.Join(cfg.StateDir, "operations"), h, cfg.Limits)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	engine, e := hostcontrol.NewEngine(h, ledger, policy, control)
	if e != nil {
		t.Fatal(e)
	}
	server, e := NewServer(control, engine, policy)
	if e != nil {
		t.Fatal(e)
	}
	handler, e := HTTPHandler(server, cfg, policy)
	if e != nil {
		t.Fatal(e)
	}
	return handler, h
}
func TestMCPHTTPAuthenticatesEveryMethodAndOrigin(t *testing.T) {
	handler, _ := fixtureServer(t)
	for _, method := range []string{"GET", "POST", "DELETE"} {
		r := httptest.NewRequest(method, "http://127.0.0.1/mcp", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("%s unauthenticated: %d", method, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader("{}"))
	r.Header.Set("Authorization", "Bearer test-only-token")
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("untrusted Origin accepted")
	}
}
func TestMCPHTTPRealSDKDiscoveryAndHostIdentity(t *testing.T) {
	handler, host := fixtureServer(t)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "fixture-client"}, nil)
	httpClient := &http.Client{Transport: bearerTransport{base: http.DefaultTransport, token: "test-only-token"}}
	session, e := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", HTTPClient: httpClient}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = session.Close() }()
	tools, e := session.ListTools(context.Background(), nil)
	if e != nil || len(tools.Tools) != 11 {
		t.Fatalf("tool contract: %v %d", e, len(tools.Tools))
	}
	result, e := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "coop_host_info", Arguments: map[string]any{}})
	if e != nil || result.IsError {
		t.Fatalf("host call: %v %+v", e, result)
	}
	raw, e := json.Marshal(result.StructuredContent)
	if e != nil || !strings.Contains(string(raw), host.ID) {
		t.Fatal("host identity absent")
	}
}

var _ io.Reader

func TestMCPStdioForwardsToCanonicalHTTPService(t *testing.T) {
	handler, host := fixtureServer(t)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	t.Setenv("COOP_TEST_MCP_TOKEN", "test-only-token")
	inputR, inputW := io.Pipe()
	outputR, outputW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunStdio(ctx, ClientConfig{Endpoint: httpServer.URL + "/mcp", TokenEnv: "COOP_TEST_MCP_TOKEN", ExpectedHostID: host.ID}, inputR, outputW, io.Discard)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-fixture"}, nil)
	session, e := client.Connect(ctx, &mcp.IOTransport{Reader: outputR, Writer: inputW, MaxLineLength: 1 << 20}, nil)
	if e != nil {
		t.Fatal(e)
	}
	result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: "coop_host_info", Arguments: map[string]any{}})
	if e != nil || !matchesHost(result, host.ID) {
		t.Fatal("stdio did not preserve host identity")
	}
	_ = session.Close()
	_ = inputR.Close()
	_ = outputW.Close()
	select {
	case e := <-done:
		if e != nil && ctx.Err() == nil {
			t.Log(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stdio did not shut down")
	}
}
func TestMCPLegacyNegotiationAndAuthRevocation(t *testing.T) {
	handler, _ := fixtureServer(t)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "legacy-fixture"}, nil)
	session, e := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", HTTPClient: newHTTPClient("test-only-token")}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = session.Close() }()
	result, e := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "coop_host_info", Arguments: map[string]any{}})
	if e != nil || result.IsError {
		t.Fatal("legacy tool call failed")
	}
	req, e := http.NewRequest("POST", httpServer.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if e != nil {
		t.Fatal(e)
	}
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	req.Header.Set("Tailscale-User-Login", "chief")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("network identity bypassed bearer authentication")
	}
}
