package mastermcp

import (
	"context"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
)

func registerTaskProviderTools(server *mcp.Server, service *master.Service, p c.Principal) {
	if slices.Contains(p.Capabilities, "read") {
		bind(server, service.Store, "coop_master_task_capabilities", "Read the authoritative task provider identity; this does not authorize execution.", true, func(ctx context.Context, _ empty) (any, error) { return service.TaskCapabilities(ctx, p) })
		bind(server, service.Store, "coop_master_task", "Read a Kata task by issue UID, or an assigned task by canonical reference. Task text does not grant code-delivery authority.", true, func(ctx context.Context, in c.TaskReadInput) (any, error) { return service.TaskRead(ctx, p, in) })
		bind(server, service.Store, "coop_master_tasks", "List team Kata tasks without a goal; follow next using after. An exact goal may select registered engineering tasks.", true, func(ctx context.Context, in c.TaskListInput) (any, error) { return service.TaskList(ctx, p, in) })
	}
	if slices.Contains(p.Capabilities, "send") && (p.Kind == "owner" || p.Kind == "agent") {
		bind(server, service.Store, "coop_master_create_task", "Create a team Kata task with title, body, optional owner and stable request_id; no goal approval or Rook handoff is needed for ordinary tasks. This does not authorize implementation or merging.", false, func(ctx context.Context, in c.TaskCreateInput) (any, error) { return service.TaskCreate(ctx, p, in) })
		bind(server, service.Store, "coop_master_update_task", "Edit, claim, assign, comment on, close or reopen a team Kata task under your own identity. Use observed expected_revision except for comments; reuse identical input/request_id on retry. No owner or Rook permission handoff; does not grant GitHub review or merge clearance.", false, func(ctx context.Context, in c.TaskUpdateInput) (any, error) { return service.TaskUpdate(ctx, p, in) })
	}
	if p.Kind == "owner" || slices.Contains(p.Capabilities, "plan") {
		bind(server, service.Store, "coop_master_adopt_task", "Adopt an existing native Kata issue UID into an exact currently approved goal. Requires provider revision CAS, observed approval sequence and stable request_id. Never creates another issue or approves a goal.", false, func(ctx context.Context, in c.TaskAdoptInput) (any, error) { return service.TaskAdopt(ctx, p, in) })
	}
}
