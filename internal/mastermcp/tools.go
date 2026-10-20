// Package mastermcp exposes coordination operations without VM control privileges.
package mastermcp

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/master"
)

type empty struct{}
type threadInput struct {
	ThreadID string `json:"thread_id"`
	After    int64  `json:"after,omitempty"`
}
type messageInput struct {
	MessageID string `json:"message_id"`
}
type agentStatusInput struct {
	AgentID string `json:"agent_id"`
}
type goalListInput struct {
	Engagement string `json:"engagement"`
	After      string `json:"after,omitempty"`
}

func bind[In any](server *mcp.Server, s *master.Store, name, description string, read bool, fn func(context.Context, In) (any, error)) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: read, IdempotentHint: true}}, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		out, err := fn(ctx, in)
		if err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: master.ErrorCode(err)}}}, nil, nil
		}
		id, epoch := s.Identity()
		env := master.Envelope{MasterID: id, RestoreEpoch: epoch, Data: out}
		raw, err := json.Marshal(env)
		if err != nil {
			return nil, nil, err
		}
		if len(raw) > c.MaxPageBytes {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: c.ErrSize.Error()}}}, nil, nil
		}
		return &mcp.CallToolResult{StructuredContent: env, Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil, nil
	})
}
func NewServer(service *master.Service, p c.Principal) *mcp.Server {
	s := service.Store
	server := mcp.NewServer(&mcp.Implementation{Name: "coop-master", Version: "1"}, nil)
	if slices.Contains(p.Capabilities, "read") {
		bind(server, s, "coop_master_team", "Read scoped agent presence and placement.", true, func(ctx context.Context, _ empty) (any, error) { return s.Team(ctx, p) })
		bind(server, s, "coop_master_agent_status", "Read one visible agent's bounded diagnostic snapshot and freshness. Does not wake it or authorize work.", true, func(ctx context.Context, in agentStatusInput) (any, error) {
			return s.AgentDiagnostics(ctx, p, in.AgentID)
		})
		bind(server, s, "coop_master_threads", "Read member threads.", true, func(ctx context.Context, in c.ThreadQuery) (any, error) { return s.Threads(ctx, p, in) })
		bind(server, s, "coop_master_read_thread", "Read a bounded message page.", true, func(ctx context.Context, in threadInput) (any, error) {
			return s.Messages(ctx, p, in.ThreadID, in.After)
		})
		bind(server, s, "coop_master_message_status", "Read durable receipt states, separate from presence.", true, func(ctx context.Context, in messageInput) (any, error) { return s.MessageStatus(ctx, p, in.MessageID) })
		if p.Kind == "agent" {
			bind(server, s, "coop_master_pending", "Read this agent's pending deliveries; does not acknowledge them.", true, func(ctx context.Context, in c.PendingQuery) (any, error) { return s.Pending(ctx, p, in) })
		}
	}
	if slices.Contains(p.Capabilities, "send") {
		bind(server, s, "coop_master_send", "Send an immutable addressed message using a stable request ID.", false, func(ctx context.Context, in c.SendMessage) (any, error) { return s.Send(ctx, p, in) })
	}
	if p.Kind == "owner" && slices.Contains(p.Capabilities, "manage") {
		bind(server, s, "coop_master_set_members", "Replace thread membership using an exact observed member set.", false, func(ctx context.Context, in c.SetMembers) (any, error) { return s.SetMembers(ctx, p, in) })
		bind(server, s, "coop_master_create_thread", "Create a private engagement thread with existing principals.", false, func(ctx context.Context, in c.CreateThread) (any, error) { return s.CreateThread(ctx, p, in) })
	}
	if slices.Contains(p.Capabilities, "read") {
		bind(server, s, "coop_master_resolve_goal", "Read current exact-revision goal authority.", true, func(ctx context.Context, in c.GoalRef) (any, error) { return service.ResolveGoal(ctx, p, in) })
		bind(server, s, "coop_master_goals", "Read active approved goals for an engagement.", true, func(ctx context.Context, in goalListInput) (any, error) {
			return service.ApprovedGoals(ctx, p, in.Engagement, in.After)
		})
		bind(server, s, "coop_master_artifacts", "Read GitHub task and PR links for an exact goal revision.", true, func(ctx context.Context, in c.ArtifactQuery) (any, error) { return service.Artifacts(ctx, p, in) })
	}
	if slices.Contains(p.Capabilities, "send") {
		bind(server, s, "coop_master_propose_goal", "Propose an immutable goal revision without approving work.", false, func(ctx context.Context, in c.GoalProposal) (any, error) { return service.ProposeGoal(ctx, p, in) })
	}
	if p.Kind == "owner" && slices.Contains(p.Capabilities, "decide") {
		bind(server, s, "coop_master_decide_goal", "Owner decision on an exact revision and expected decision sequence.", false, func(ctx context.Context, in c.GoalDecisionInput) (any, error) { return service.DecideGoal(ctx, p, in) })
	}
	return server
}
