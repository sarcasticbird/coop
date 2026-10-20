package master

import (
	"bytes"
	"context"
	"encoding/json"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubCacheHTTPAuthorization(t *testing.T) {
	svc, o, b, _ := cacheFixture(t)
	policy := policyFixture(o, b)
	h := NewHandler(svc, policy)
	for _, tc := range []struct {
		token, pin, body string
		want             int
	}{
		{"owner-test-token", "test-master", `{"engagement":"coop"}`, 200},
		{"owner-test-token", "other", `{"engagement":"coop"}`, 409},
		{"wrong", "test-master", `{}`, 401},
		{"owner-test-token", "test-master", `{"engagement":"else"}`, 403},
		{"owner-test-token", "test-master", strings.Repeat(" ", c.MaxJSON+1), 413},
	} {
		req := httptest.NewRequest("POST", "https://example.com/api/v1/github-cache/catalog", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("X-Coop-Expected-Master-ID", tc.pin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("status %d want %d: %s", w.Code, tc.want, w.Body.String())
		}
	}
	lease, e := svc.ClaimGitHubRefresh(context.Background(), b, c.GitHubClaim{RouteID: "route", RequestID: "claim"})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(c.GitHubUpload{LeaseID: lease.ID, Kind: "provider", Data: make([]byte, c.GitHubChunkBytes), Total: c.GitHubChunkBytes, SHA256: strings.Repeat("a", 64)})
	req := httptest.NewRequest("POST", "https://example.com/api/v1/github-cache/upload", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer agent-test-token")
	req.Header.Set("X-Coop-Expected-Master-ID", "test-master")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	policy.Tokens[1].InstanceID = "old"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("placement allowed", w.Code)
	}
}
