// Package hostmcp adapts host-local Coop control to the official MCP transports.
package hostmcp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sarcasticbird/coop/internal/hostcontrol"
	"net"
	"net/http"
	"slices"
	"strings"
)

type envelope struct {
	hostcontrol.Host
	Data any `json:"data"`
}

func safeError(err error) *mcp.CallToolResult {
	code := "control_unavailable"
	var e *hostcontrol.Error
	if errors.As(err, &e) {
		code = e.Code
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: code}}}
}
func result(host hostcontrol.Host, value any) (*mcp.CallToolResult, any, error) {
	body := envelope{Host: host, Data: value}
	raw, err := json.Marshal(body)
	if err != nil {
		return safeError(err), nil, nil
	}
	return &mcp.CallToolResult{StructuredContent: body, Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil, nil
}
func bearer(header http.Header) string {
	v := header.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") || len(header.Values("Authorization")) != 1 {
		return ""
	}
	return strings.TrimPrefix(v, "Bearer ")
}
func requestCaller(req *mcp.CallToolRequest, policy *hostcontrol.Policy) (hostcontrol.Caller, error) {
	if req.Extra == nil {
		return hostcontrol.Caller{}, &hostcontrol.Error{Code: "unauthorized"}
	}
	return policy.Authenticate(bearer(req.Extra.Header))
}
func HTTPHandler(server *mcp.Server, cfg hostcontrol.Config, policy *hostcontrol.Policy) (http.Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// The explicit allowlist below replaces the SDK's loopback-only Host rule so
	// a private Tailscale Serve hostname can reach the loopback backend safely.
	sdk := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 1 << 20, DisableLocalhostProtection: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !slices.Contains(cfg.AllowedHosts, r.Host) && !slices.Contains(cfg.AllowedHosts, host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !slices.Contains(cfg.AllowedOrigins, origin) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if _, err := policy.Authenticate(bearer(r.Header)); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		sdk.ServeHTTP(w, r)
	}), nil
}
func bind[In any](server *mcp.Server, host hostcontrol.Host, name, description string, readOnly bool, handler func(context.Context, *mcp.CallToolRequest, In) (any, error)) {
	destructive := !readOnly
	open := name == "coop_exec"
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructive, IdempotentHint: !readOnly, OpenWorldHint: &open}}, func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, any, error) {
		out, err := handler(ctx, req, input)
		if err != nil {
			return safeError(err), nil, nil
		}
		return result(host, out)
	})
}
