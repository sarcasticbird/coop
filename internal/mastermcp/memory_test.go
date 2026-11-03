package mastermcp

import (
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
)

func TestMemoryToolsKeepKnowledgeSeparateFromAuthority(t *testing.T) {
	ctx := t.Context()
	s, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.db"), MasterID: "memory-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	bird := c.Principal{ID: "bird", Alias: "bird", Kind: "agent", GitHubBotID: 1, Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
	other := c.Principal{ID: "other", Alias: "other", Kind: "agent", GitHubBotID: 2, Engagements: []string{"coop"}, Capabilities: []string{"read", "send"}}
	if err = s.Configure(ctx, []c.Principal{bird, other}, nil); err != nil {
		t.Fatal(err)
	}
	a, b := mcp.NewInMemoryTransports()
	ss, err := NewServer(&master.Service{Store: s}, bird).Connect(ctx, a, nil)
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
	for _, tc := range []struct {
		name   string
		in     map[string]any
		denied bool
	}{
		{"coop_master_memory_write", map[string]any{"key": "notes", "body": "Useful knowledge", "expected_revision": 0}, false},
		{"coop_master_memory_read", map[string]any{"key": "notes"}, false},
		{"coop_master_memory_list", map[string]any{}, false},
		{"coop_master_memory_write", map[string]any{"agent_id": "other", "key": "notes", "body": "overwrite", "expected_revision": 0}, true},
		{"coop_master_memory_read", map[string]any{"agent_id": "other", "key": "notes"}, true},
	} {
		out, e := session.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.in})
		if e != nil || out.IsError != tc.denied {
			t.Fatal(tc.name, out, e)
		}
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "coop_master_decide_goal" {
			t.Fatal("memory writer gained goal authority")
		}
	}
}
