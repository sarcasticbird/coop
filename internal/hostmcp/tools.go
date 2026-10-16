package hostmcp

import (
	"context"
	"encoding/base64"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sarcasticbird/coop/internal/hostcontrol"
	"time"
)

type emptyInput struct{}
type workspaceInput struct {
	Workspace string `json:"workspace"`
}
type requestsInput struct {
	Workspace string `json:"workspace"`
	WakeID    string `json:"wake_id,omitempty"`
}
type mutationInput struct {
	Workspace string    `json:"workspace"`
	RequestID string    `json:"request_id"`
	CreatedAt time.Time `json:"created_at"`
}
type PingInput struct {
	mutationInput
	Prompt string `json:"prompt,omitempty"`
}
type WorkerControlInput struct {
	mutationInput
	Action string `json:"action" jsonschema:"pause or resume"`
}
type LifecycleInput struct {
	mutationInput
	Action          string `json:"action" jsonschema:"start, stop or restart"`
	InterruptActive bool   `json:"interrupt_active"`
}
type ExecInput struct {
	mutationInput
	Argv           []string `json:"argv"`
	Cwd            string   `json:"cwd,omitempty"`
	Stdin          string   `json:"stdin,omitempty"`
	StdinBase64    string   `json:"stdin_base64,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}
type operationInput struct {
	OperationID  string `json:"operation_id"`
	StdoutOffset int64  `json:"stdout_offset,omitempty"`
	StderrOffset int64  `json:"stderr_offset,omitempty"`
	PageBytes    int    `json:"page_bytes,omitempty"`
}

type CancelInput struct {
	OperationID string    `json:"operation_id"`
	RequestID   string    `json:"request_id"`
	CreatedAt   time.Time `json:"created_at"`
}

func (in mutationInput) mutation(action string) hostcontrol.Mutation {
	return hostcontrol.Mutation{Key: hostcontrol.RequestKey{ID: in.RequestID, CreatedAt: in.CreatedAt}, Workspace: in.Workspace, Action: action}
}
func NewServer(control *hostcontrol.Control, engine *hostcontrol.Engine, policy *hostcontrol.Policy) (*mcp.Server, error) {
	host := control.Host()
	server := mcp.NewServer(&mcp.Implementation{Name: "coop", Version: host.Version}, &mcp.ServerOptions{SetCacheable: func(_ context.Context, _ mcp.Request, c *mcp.Cacheable) { c.CacheScope = "private"; c.TTLMs = 0 }})
	auth := func(req *mcp.CallToolRequest, workspace, cap string) (hostcontrol.Caller, error) {
		caller, err := requestCaller(req, policy)
		if err != nil {
			return caller, err
		}
		if workspace != "" {
			selector, err := control.Selector(workspace)
			if err != nil {
				return caller, err
			}
			workspace = selector
		}
		return caller, policy.Authorize(caller, workspace, cap)
	}
	bind(server, host, "coop_host_info", "Inspect this host and runtime without starting services.", true, func(ctx context.Context, r *mcp.CallToolRequest, _ emptyInput) (any, error) {
		if _, err := auth(r, "", "read"); err != nil {
			return nil, err
		}
		return control.HostInfo(ctx)
	})
	bind(server, host, "coop_list", "List caller-visible configured agents and owned interactive Coops.", true, func(ctx context.Context, r *mcp.CallToolRequest, _ emptyInput) (any, error) {
		caller, err := auth(r, "", "read")
		if err != nil {
			return nil, err
		}
		rows, err := control.List(ctx)
		if err != nil {
			return nil, err
		}
		out := []hostcontrol.Workspace{}
		for _, row := range rows {
			selector, err := control.Selector(row.Ref)
			if err == nil && policy.Authorize(caller, selector, "read") == nil {
				out = append(out, row)
			}
		}
		return out, nil
	})
	bind(server, host, "coop_status", "Inspect an owned workspace and maintenance requirements.", true, func(ctx context.Context, r *mcp.CallToolRequest, in workspaceInput) (any, error) {
		if _, err := auth(r, in.Workspace, "read"); err != nil {
			return nil, err
		}
		return control.Status(ctx, in.Workspace)
	})
	bind(server, host, "coop_worker_status", "Read safe current worker state.", true, func(ctx context.Context, r *mcp.CallToolRequest, in workspaceInput) (any, error) {
		if _, err := auth(r, in.Workspace, "read"); err != nil {
			return nil, err
		}
		return control.WorkerStatus(ctx, in.Workspace)
	})
	bind(server, host, "coop_worker_requests", "Read wake summaries without prompt bodies.", true, func(ctx context.Context, r *mcp.CallToolRequest, in requestsInput) (any, error) {
		if _, err := auth(r, in.Workspace, "read"); err != nil {
			return nil, err
		}
		return control.WorkerRequests(ctx, in.Workspace, in.WakeID)
	})
	bind(server, host, "coop_ping", "Queue an optional prompt. host_pending and queued do not mean delivered.", false, func(ctx context.Context, r *mcp.CallToolRequest, in PingInput) (any, error) {
		caller, err := requestCaller(r, policy)
		if err != nil {
			return nil, err
		}
		m := in.mutation("ping")
		m.Prompt = in.Prompt
		return engine.Submit(ctx, caller, m)
	})
	bind(server, host, "coop_worker_control", "Request pause or resume; does not abort native work or clear recovery.", false, func(ctx context.Context, r *mcp.CallToolRequest, in WorkerControlInput) (any, error) {
		if in.Action != "pause" && in.Action != "resume" {
			return nil, &hostcontrol.Error{Code: "invalid_action"}
		}
		caller, err := requestCaller(r, policy)
		if err != nil {
			return nil, err
		}
		return engine.Submit(ctx, caller, in.mutation(in.Action))
	})
	bind(server, host, "coop_lifecycle", "Request start, stop or restart. Explicit interruption intent is required for active work.", false, func(ctx context.Context, r *mcp.CallToolRequest, in LifecycleInput) (any, error) {
		if in.Action != "start" && in.Action != "stop" && in.Action != "restart" {
			return nil, &hostcontrol.Error{Code: "invalid_action"}
		}
		caller, err := requestCaller(r, policy)
		if err != nil {
			return nil, err
		}
		m := in.mutation(in.Action)
		m.InterruptActive = in.InterruptActive
		return engine.Submit(ctx, caller, m)
	})
	bind(server, host, "coop_exec", "Run exact argv in an already running guest without a PTY or host shell.", false, func(ctx context.Context, r *mcp.CallToolRequest, in ExecInput) (any, error) {
		caller, err := requestCaller(r, policy)
		if err != nil {
			return nil, err
		}
		if in.Stdin != "" && in.StdinBase64 != "" || in.TimeoutSeconds < 1 || in.TimeoutSeconds > 600 {
			return nil, &hostcontrol.Error{Code: "invalid_exec"}
		}
		m := in.mutation("exec")
		m.Argv = in.Argv
		m.Cwd = in.Cwd
		m.Stdin = []byte(in.Stdin)
		if in.StdinBase64 != "" {
			m.Stdin, err = base64.StdEncoding.DecodeString(in.StdinBase64)
			if err != nil {
				return nil, &hostcontrol.Error{Code: "invalid_stdin"}
			}
		}
		m.Timeout = time.Duration(in.TimeoutSeconds) * time.Second
		return engine.Submit(ctx, caller, m)
	})
	bind(server, host, "coop_operation_status", "Read the creating caller's operation and bounded byte-offset output pages.", true, func(ctx context.Context, r *mcp.CallToolRequest, in operationInput) (any, error) {
		caller, err := requestCaller(r, policy)
		if err != nil {
			return nil, err
		}
		if in.PageBytes == 0 {
			in.PageBytes = 64 << 10
		}
		return engine.Status(ctx, caller, in.OperationID, in.StdoutOffset, in.StderrOffset, in.PageBytes)
	})
	bind(server, host, "coop_operation_cancel", "Request cancellation of this caller's exact operation; guest termination can remain unknown.", false, func(ctx context.Context, r *mcp.CallToolRequest, in CancelInput) (any, error) {
		caller, err := requestCaller(r, policy)
		if err != nil {
			return nil, err
		}
		return engine.CancelRequest(ctx, caller, in.OperationID, hostcontrol.RequestKey{ID: in.RequestID, CreatedAt: in.CreatedAt})
	})
	return server, nil
}
