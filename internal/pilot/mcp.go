package pilot

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer exposes the same fixture coordinator as the CLI, over private stdio.
func NewServer(p *Coordinator) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "coop-maestro-fixture", Version: "0"}, nil)
	result := func(v any, e error) (*mcp.CallToolResult, any, error) {
		if e != nil {
			slog.Error("fixture pilot operation failed", "error", e)
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "Fixture pilot did not complete; inspect its private receipt."}}}, nil, nil
		}
		b, e := json.Marshal(v)
		if e != nil {
			return nil, nil, e
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, v, nil
	}
	mcp.AddTool(s, &mcp.Tool{Name: "coop_pilot_run", Description: "Run one approved disposable Kata audit through Coop. Disposable only; configured native mode runs a bounded read or coding turn. Never selects a live bird."}, func(ctx context.Context, _ *mcp.CallToolRequest, in Request) (*mcp.CallToolResult, any, error) {
		v, e := p.Run(ctx, in)
		return result(v, e)
	})
	mcp.AddTool(s, &mcp.Tool{Name: "coop_pilot_status", Description: "Read a saved fixture receipt, without dispatching.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		RequestID string `json:"request_id"`
	}) (*mcp.CallToolResult, any, error) {
		v, e := p.Status(in.RequestID)
		return result(v, e)
	})
	return s
}
