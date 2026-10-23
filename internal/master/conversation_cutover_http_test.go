package master

import (
	"bytes"
	"net/http/httptest"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestConversationCutoverAuthenticatedRecoveryAndHistory(t *testing.T) {
	s, o, b, th, _, mapping := cutoverFixture(t)
	ctx := t.Context()
	if _, err := s.Send(ctx, o, c.SendMessage{ThreadID: th.ID, Body: "kept local history", RequestID: "history"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(&Service{Store: s}, policyFixture(o, b))
	invoke := func(method, path, token string, input any, want int) {
		t.Helper()
		var raw []byte
		if input != nil {
			raw = encode(input)
		}
		req := httptest.NewRequest(method, "http://example.com"+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Coop-Expected-Master-ID", s.id)
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, req)
		if recorder.Code != want {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, recorder.Code, want, recorder.Body.String())
		}
	}
	invoke("GET", "/api/v1/conversation-cursor", "agent-test-token", nil, 403)
	if err := s.Quiesce(ctx, o, 1); err != nil {
		t.Fatal(err)
	}
	invoke("POST", "/api/v1/conversation-sources", "agent-test-token", mapping, 403)
	invoke("POST", "/api/v1/conversation-sources", "owner-test-token", mapping, 200)
	observed, err := s.ConversationCursor(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	in := c.ReacceptConversationCursor{Observed: observed, Reason: "inspected fixture", RequestID: "reaccept"}
	invoke("POST", "/api/v1/conversation-cursor/reaccept", "agent-test-token", in, 403)
	invoke("POST", "/api/v1/conversation-cursor/reaccept", "owner-test-token", in, 200)
	invoke("GET", "/api/v1/conversation-cursor", "owner-test-token", nil, 200)
	s.work = nil
	invoke("GET", "/api/v1/threads/"+th.ID+"/legacy-messages?after=0", "agent-test-token", nil, 200)
	invoke("GET", "/api/v1/threads/"+th.ID+"/legacy-messages?after=-1", "agent-test-token", nil, 400)
	invoke("GET", "/api/v1/threads/not-member/legacy-messages", "agent-test-token", nil, 403)
	invoke("POST", "/api/v1/messages", "owner-test-token", c.SendMessage{ThreadID: th.ID, Body: "fenced", RequestID: "fenced"}, 409)
	// Hark-like service reads use membership and read capability, never manage.
	bridge, err := s.Principal(ctx, "native-bridge")
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.LegacyMessages(ctx, bridge, th.ID, 0)
	if err != nil || len(page.Messages) != 1 {
		t.Fatal(page, err)
	}
}
