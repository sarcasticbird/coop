package master

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func policyFixture(owner, bot c.Principal) *Policy {
	hash := func(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
	return &Policy{Tokens: []Token{{PrincipalID: owner.ID, SHA256: hash("owner-test-token")}, {PrincipalID: bot.ID, SHA256: hash("agent-test-token"), InstanceID: "instance", Generation: 1}}, AllowedHosts: []string{"example.com"}}
}
func TestPrincipalBoundaries(t *testing.T) {
	s, owner, bot, th := storeFixture(t)
	h := NewHandler(&Service{Store: s}, policyFixture(owner, bot))
	for _, tc := range []struct {
		path, body, token string
		want              int
	}{
		{"/api/v1/messages", `{"thread_id":"` + th.ID + `","body":"hi","request_id":"spoof","author_id":"owner"}`, "agent-test-token", 400},
		{"/api/v1/threads", `{"title":"new","engagement":"coop","members":["agent-one"],"request_id":"new"}`, "agent-test-token", 403},
		{"/api/v1/messages", `{"thread_id":"` + th.ID + `","body":"hi","request_id":"good","recipients":["one"]}`, "owner-test-token", 200},
		{"/api/v1/messages", `{}`, "wrong-token", 401},
	} {
		t.Run(tc.path+tc.token+tc.body[:2], func(t *testing.T) {
			r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+tc.token)
			r.Header.Set("X-Coop-Expected-Master-ID", "test-master")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}
func TestEventCursorIsNotAck(t *testing.T) {
	s, owner, bot, th := storeFixture(t)
	ctx := context.Background()
	r, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Recipients: []string{bot.ID}, Body: "event", RequestID: "event"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, bot, c.Cursor{RestoreEpoch: 1})
	if err != nil || len(events) == 0 {
		t.Fatalf("events %v %v", events, err)
	}
	p, err := s.Pending(ctx, bot, c.PendingQuery{})
	if err != nil || len(p.Deliveries) != 1 || p.Deliveries[0].State != "queued" || p.Deliveries[0].MessageID != r.MessageID {
		t.Fatalf("cursor acknowledged delivery: %+v %v", p, err)
	}
}
func TestPlacementFence(t *testing.T) {
	s, owner, bot, th := storeFixture(t)
	ctx := context.Background()
	r, err := s.Send(ctx, owner, c.SendMessage{ThreadID: th.ID, Recipients: []string{bot.ID}, Body: "hi", RequestID: "fence"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetDelivery(ctx, bot, c.DeliveryUpdate{RestoreEpoch: 1, DeliveryID: r.Deliveries[0].ID, InstanceID: "wrong", Generation: 1, State: "received"}); err == nil {
		t.Fatal("wrong instance accepted")
	}
	if err = s.Configure(ctx, []c.Principal{}, []c.Placement{{AgentID: bot.ID, HostID: "new-host", InstanceID: "new-instance", Generation: 2}}); err == nil {
		t.Fatal("startup silently adopted new placement")
	}
}

func TestAgentCredentialPinnedToPlacement(t *testing.T) {
	s, owner, bot, _ := storeFixture(t)
	policy := policyFixture(owner, bot)
	policy.Tokens[1].InstanceID = "retired-instance"
	policy.Tokens[1].Generation = 1
	h := NewHandler(&Service{Store: s}, policy)
	req := httptest.NewRequest("GET", "/api/v1/team", nil)
	req.Header.Set("Authorization", "Bearer agent-test-token")
	req.Header.Set("X-Coop-Expected-Master-ID", "test-master")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("retired placement token still read master data: %d", w.Code)
	}
}
func TestPresenceAndLimits(t *testing.T) {
	s, owner, bot, _ := storeFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	if err := s.Heartbeat(ctx, bot, c.Heartbeat{Placement: c.Placement{AgentID: bot.ID, HostID: "host", InstanceID: "instance", Generation: 1}, State: "idle"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(45 * time.Second)
	team, err := s.Team(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range team {
		if p.AgentID == bot.ID && p.State != "stale" {
			t.Fatalf("not stale %+v", p)
		}
	}
	h := NewHandler(&Service{Store: s}, policyFixture(owner, bot))
	req := httptest.NewRequest("POST", "/api/v1/messages", strings.NewReader(strings.Repeat(" ", c.MaxJSON+1)))
	req.Header.Set("Authorization", "Bearer owner-test-token")
	req.Header.Set("X-Coop-Expected-Master-ID", "test-master")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status %d", w.Code)
	}
	var safe map[string]any
	if json.Unmarshal(w.Body.Bytes(), &safe) != nil {
		t.Fatal("non-JSON safe error")
	}
}

func TestEventStreamStopsWhenTokenExpires(t *testing.T) {
	s, o, b, _ := storeFixture(t)
	p := policyFixture(o, b)
	p.AllowedHosts = []string{"127.0.0.1"}
	p.Tokens[0].ExpiresAt = time.Now().Add(750 * time.Millisecond)
	ts := httptest.NewServer(NewHandler(&Service{Store: s}, p))
	defer ts.Close()
	client := ts.Client()
	client.Timeout = 3 * time.Second
	r, _ := http.NewRequest("GET", ts.URL+"/events", nil)
	r.Header.Set("Authorization", "Bearer owner-test-token")
	r.Header.Set("X-Coop-Expected-Master-ID", "test-master")
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	if _, err = io.ReadAll(response.Body); err != nil {
		t.Fatalf("expired stream stayed open: %v", err)
	}
}

func TestMasterPinCheckedBeforeMutation(t *testing.T) {
	s, o, b, th := storeFixture(t)
	h := NewHandler(&Service{Store: s}, policyFixture(o, b))
	r := httptest.NewRequest("POST", "/api/v1/messages", strings.NewReader(`{"thread_id":"`+th.ID+`","body":"wrong target","request_id":"wrong-master"}`))
	r.Header.Set("Authorization", "Bearer owner-test-token")
	r.Header.Set("X-Coop-Expected-Master-ID", "different-master")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("wrong master accepted request: %d", w.Code)
	}
	page, err := s.Messages(context.Background(), o, th.ID, 0)
	if err != nil || len(page.Messages) != 0 {
		t.Fatal("wrong master pin mutated storage", err)
	}
}

func TestHeartbeatRejectsWrongHost(t *testing.T) {
	s, _, b, _ := storeFixture(t)
	if err := s.Heartbeat(context.Background(), b, c.Heartbeat{Placement: c.Placement{AgentID: b.ID, HostID: "wrong-host", InstanceID: "instance", Generation: 1}, State: "idle"}); err == nil {
		t.Fatal("wrong host heartbeat accepted")
	}
}
