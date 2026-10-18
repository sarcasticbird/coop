package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMasterConversationCLI(t *testing.T) {
	for _, args := range [][]string{{"master", "--help"}, {"thread", "--help"}, {"message", "--help"}, {"team", "--help"}, {"inbox", "--help"}} {
		cmd := root()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "master") {
			t.Fatalf("missing master help for %v: %s", args, out.String())
		}
	}
}

func TestMasterCLIAuthenticatedRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.db"), MasterID: "master-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	owner := c.Principal{ID: "owner", Kind: "owner", Alias: "owner", Engagements: []string{"test"}, Capabilities: []string{"read", "send", "manage"}}
	bot := c.Principal{ID: "agent", Kind: "agent", Alias: "bird", Engagements: []string{"test"}, Capabilities: []string{"read", "send"}, GitHubBotID: 2}
	if err = s.Configure(ctx, []c.Principal{owner, bot}, nil); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("local-owner-test-token"))
	ts := httptest.NewServer(master.NewHandler(&master.Service{Store: s}, &master.Policy{AllowedHosts: []string{"127.0.0.1"}, Tokens: []master.Token{{PrincipalID: owner.ID, SHA256: hex.EncodeToString(hash[:])}}}))
	defer ts.Close()
	cfg := filepath.Join(t.TempDir(), "client.toml")
	t.Setenv("COOP_TEST_TOKEN", "local-owner-test-token")
	if err = os.WriteFile(cfg, fmt.Appendf(nil, "url = %q\nmaster_id = %q\ntoken_env = %q\nallow_loopback_http = true\n", ts.URL, "master-test", "COOP_TEST_TOKEN"), 0600); err != nil {
		t.Fatal(err)
	}
	invoke := func(args ...string) []byte {
		t.Helper()
		cmd := root()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"--master-config", cfg}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out.String())
		}
		return out.Bytes()
	}
	var th c.Thread
	if err = json.Unmarshal(invoke("thread", "create", "--title", "Round trip", "--engagement", "test", "--member", "agent", "--request-id", "cli-thread"), &th); err != nil || th.ID == "" {
		t.Fatal("missing thread", err)
	}
	body := filepath.Join(t.TempDir(), "body.txt")
	if err = os.WriteFile(body, []byte("Hello via the CLI"), 0600); err != nil {
		t.Fatal(err)
	}
	var receipt c.MessageReceipt
	if err = json.Unmarshal(invoke("message", "send", th.ID, "--to", "bird", "--body-file", body, "--request-id", "cli-message"), &receipt); err != nil || len(receipt.Deliveries) != 1 || receipt.Deliveries[0].State != "queued" {
		t.Fatal("missing queued receipt", err)
	}
	invoke("message", "send", th.ID, "--to", "bird", "--body-file", body, "--request-id", "cli-message")
	page, err := s.Messages(ctx, owner, th.ID, 0)
	if err != nil || len(page.Messages) != 1 {
		t.Fatal("CLI retry duplicated message", err)
	}
}
