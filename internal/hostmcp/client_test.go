package hostmcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMCPClientRejectsUnsafeEndpoints(t *testing.T) {
	for _, endpoint := range []string{"http://rack-mini/mcp", "http://localhost/mcp", "https://user:secret@example.com/mcp", "https://example.com/mcp?token=secret"} {
		cfg := ClientConfig{Endpoint: endpoint, TokenEnv: "TEST_COOP_MCP_TOKEN", ExpectedHostID: "h1_00000000000000000000000000000000"}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
}
func TestMCPClientDoesNotForwardCredentialOnRedirect(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(200) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	client := newHTTPClient("test-only-token")
	req, err := http.NewRequestWithContext(context.Background(), "GET", redirect.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if calls != 0 {
		t.Fatal("credential followed redirect")
	}
}
