package hostmcp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// eofBody exercises readers that return the final bytes together with EOF.
type eofBody struct{ *bytes.Reader }

func (b eofBody) Close() error { return nil }
func (b eofBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if b.Len() == 0 {
		err = io.EOF
	}
	return n, err
}

type responseTransport struct{ body io.ReadCloser }

func (t responseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: t.body}, nil
}

func TestClientResponseExactLimit(t *testing.T) {
	for _, size := range []int{4194303, 4194304, 4194305, 4194306} {
		for _, finalEOF := range []bool{false, true} {
			body := io.ReadCloser(io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), size))))
			if finalEOF {
				body = eofBody{bytes.NewReader(bytes.Repeat([]byte("x"), size))}
			}
			req, err := http.NewRequest("GET", "http://127.0.0.1/mcp", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := (bearerTransport{base: responseTransport{body}, token: "fixture"}).RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if size <= 4194304 && (err != nil || len(got) != size) {
				t.Fatalf("size=%d finalEOF=%v: got %d bytes, %v", size, finalEOF, len(got), err)
			}
			if size > 4194304 && err == nil {
				t.Fatalf("oversized response accepted: size=%d finalEOF=%v", size, finalEOF)
			}
		}
	}
}

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
