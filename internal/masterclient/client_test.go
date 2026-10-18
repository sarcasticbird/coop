package masterclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"path/filepath"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
)

func TestClientIdentityAndRetry(t *testing.T) {
	ctx := context.Background()
	s, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.db"), MasterID: "client-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	p := c.Principal{ID: "owner", Kind: "owner", Alias: "owner", Engagements: []string{"coop"}, Capabilities: []string{"read", "send", "manage"}}
	if err = s.Configure(ctx, []c.Principal{p}, nil); err != nil {
		t.Fatal(err)
	}
	token := "a-test-only-token"
	hash := sha256.Sum256([]byte(token))
	policy := &master.Policy{AllowedHosts: []string{"127.0.0.1"}, Tokens: []master.Token{{PrincipalID: p.ID, SHA256: hex.EncodeToString(hash[:])}}}
	ts := httptest.NewTLSServer(master.NewHandler(&master.Service{Store: s}, policy))
	defer ts.Close()
	cl, err := New(ClientConfig{URL: ts.URL, MasterID: "client-test", Token: token, HTTPClient: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}
	th, err := cl.CreateThread(ctx, c.CreateThread{Title: "native", Engagement: "coop", RequestID: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	in := c.SendMessage{ThreadID: th.ID, Body: "question", RequestID: "same-request"}
	a, err := cl.Send(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := cl.Send(ctx, in)
	if err != nil || a.MessageID != b.MessageID {
		t.Fatalf("retry identity %+v %v", b, err)
	}
	wrong, err := New(ClientConfig{URL: ts.URL, MasterID: "other-master", Token: token, HTTPClient: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wrong.Threads(ctx, c.ThreadQuery{}); err == nil {
		t.Fatal("wrong master accepted")
	}
	if stream, err := wrong.Events(ctx, c.Cursor{}); err == nil {
		_ = stream.Close()
		t.Fatal("wrong master SSE accepted")
	}
	if _, err = New(ClientConfig{URL: "http://remote-host:9000", MasterID: "client-test", Token: token, AllowHTTP: true}); err == nil {
		t.Fatal("plaintext remote accepted")
	}
}
