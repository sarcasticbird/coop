package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/config"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
	"github.com/sarcasticbird/coop/internal/runtime"
	"github.com/sarcasticbird/coop/internal/worker"
	"github.com/sarcasticbird/coop/internal/workspace"
)

func TestPingBackendSelection(t *testing.T) {
	ctx := context.Background()
	store, e := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.sqlite"), MasterID: "master-test"})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = store.Close() }()
	owner := c.Principal{ID: "owner", Alias: "owner", Kind: "owner", Engagements: []string{"coop"}, Capabilities: []string{"read", "send", "manage"}}
	bot := c.Principal{ID: "bot", Alias: "test-bird", Kind: "agent", Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
	if e = store.Configure(ctx, []c.Principal{owner, bot}, []c.Placement{{AgentID: bot.ID, HostID: "host", InstanceID: "instance", Generation: 1}}); e != nil {
		t.Fatal(e)
	}
	token := "test-owner-token"
	h := sha256.Sum256([]byte(token))
	policy := &master.Policy{AllowedHosts: []string{"127.0.0.1"}, Tokens: []master.Token{{PrincipalID: owner.ID, SHA256: hex.EncodeToString(h[:])}}}
	ts := httptest.NewServer(master.NewHandler(&master.Service{Store: store}, policy))
	defer ts.Close()
	t.Setenv("COOP_TEST_MASTER_TOKEN", token)
	file := filepath.Join(t.TempDir(), "client.toml")
	if e = os.WriteFile(file, fmt.Appendf(nil, "url = %q\nmaster_id = \"master-test\"\ntoken_env = \"COOP_TEST_MASTER_TOKEN\"\nallow_loopback_http = true\n", ts.URL), 0600); e != nil {
		t.Fatal(e)
	}
	backend := &workerBackend{state: runtime.StateStopped}
	var out bytes.Buffer
	s := Session{Spec: workspace.Spec{Mode: workspace.Agent, Identity: "test-bird", Profile: config.Profile{Communication: &config.MasterCommunication{Backend: "master", ClientConfig: file, AgentID: bot.ID, Engagement: "coop"}}}, Controller: workspace.Controller{Backend: backend}, Runner: backend, Output: &out}
	if e = s.Ping(ctx, worker.PingInput{Prompt: "status?", RequestID: strings.Repeat("a", 64)}); e != nil {
		t.Fatal(e)
	}
	var receipt c.MessageReceipt
	if e = json.Unmarshal(out.Bytes(), &receipt); e != nil || receipt.Backend != "master" || len(receipt.Deliveries) != 1 || receipt.Deliveries[0].State != "queued" {
		t.Fatalf("bad receipt %s %v", out.String(), e)
	}
	if backend.starts != 0 || backend.execs != 0 {
		t.Fatal("native ping touched stopped guest")
	}
	ts.Close()
	if e = s.Ping(ctx, worker.PingInput{Prompt: "again", RequestID: strings.Repeat("b", 64)}); e == nil {
		t.Fatal("master outage hidden")
	}
	if backend.execs != 0 {
		t.Fatal("native ping fell back to guest")
	}
}
