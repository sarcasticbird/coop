package hostmcp

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sarcasticbird/coop/internal/hostcontrol"
	"io"
)

func matchesHost(result *mcp.CallToolResult, id string) bool {
	if result == nil || result.IsError {
		return false
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return false
	}
	var h struct {
		ID string `json:"host_id"`
	}
	return json.Unmarshal(raw, &h) == nil && h.ID == id
}

type writeCloser struct{ io.Writer }

func (writeCloser) Close() error { return nil }
func RunStdio(ctx context.Context, cfg ClientConfig, in io.Reader, out, diagnostics io.Writer) error {
	session, err := connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()
	list, err := session.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 11 {
		return &hostcontrol.Error{Code: "incompatible_host_tools"}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "coop-adapter", Version: "1"}, nil)
	for _, tool := range list.Tools {
		server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: json.RawMessage(req.Params.Arguments)})
			if err != nil {
				return safeError(&hostcontrol.Error{Code: "host_unavailable"}), nil
			}
			if !result.IsError && !matchesHost(result, cfg.ExpectedHostID) {
				return safeError(&hostcontrol.Error{Code: "unexpected_host"}), nil
			}
			return result, nil
		})
	}
	reader, ok := in.(io.ReadCloser)
	if !ok {
		reader = io.NopCloser(in)
	}
	return server.Run(ctx, &mcp.IOTransport{Reader: reader, Writer: writeCloser{out}, MaxLineLength: 1 << 20})
}
