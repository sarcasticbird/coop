package mastermcp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
)

func TestTaskToolsRespectPlanningGrant(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   string
		caps   []string
		create bool
	}{
		{"reader", "agent", []string{"read"}, false},
		{"planner", "agent", []string{"read", "plan"}, true},
		{"owner", "owner", []string{"read", "manage", "decide"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := master.OpenStore(ctx, master.StoreConfig{Path: filepath.Join(t.TempDir(), "master.db"), MasterID: "tasks-test"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			p := c.Principal{ID: tc.name, Kind: tc.kind, Alias: tc.name, Engagements: []string{"coop"}, Capabilities: tc.caps}
			if err = store.Configure(ctx, []c.Principal{p}, nil); err != nil {
				t.Fatal(err)
			}
			server := NewServer(&master.Service{Store: store}, p)
			a, b := mcp.NewInMemoryTransports()
			ss, err := server.Connect(ctx, a, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ss.Close() })
			client := mcp.NewClient(&mcp.Implementation{Name: "task-test", Version: "1"}, nil)
			s, err := client.Connect(ctx, b, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			listed, err := s.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			tools := map[string]bool{}
			for _, tool := range listed.Tools {
				tools[tool.Name] = true
			}
			for _, name := range []string{"coop_master_task_capabilities", "coop_master_task", "coop_master_tasks"} {
				if !tools[name] {
					t.Fatalf("missing %s", name)
				}
			}
			if tools["coop_master_create_task"] != tc.create || tools["coop_master_adopt_task"] != tc.create {
				t.Fatal("planning grant mismatch")
			}
			if tc.kind != "owner" && tools["coop_master_decide_goal"] {
				t.Fatal("planning granted goal approval")
			}
			if tools["coop_master_bind_task"] || tools["coop_master_complete_task"] {
				t.Fatal("unguarded engineering mutation exposed")
			}
			result, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "coop_master_task_capabilities", Arguments: map[string]any{}})
			if err != nil || result == nil || !result.IsError {
				t.Fatal("disabled provider did not fail closed", result, err)
			}
		})
	}
}
