package hostmcp

import (
	"context"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sarcasticbird/coop/internal/hostcontrol"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDefaultOutputPagesSurviveWorstCaseJSONEscaping(t *testing.T) {
	host := hostcontrol.Host{ID: "h1_" + strings.Repeat("b", 32), Label: "fixture"}
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	bind(server, host, "coop_operation_status", "output fixture", true, func(context.Context, *mcp.CallToolRequest, emptyInput) (any, error) {
		page := hostcontrol.OutputPage{Encoding: "utf8", Data: strings.Repeat("\x00", 64<<10), TotalBytes: 64 << 10, NextOffset: 64 << 10, EOF: true}
		return hostcontrol.Operation{Host: host, State: "succeeded", Stdout: page, Stderr: page}, nil
	})
	bind(server, host, "coop_host_info", "host fixture", true, func(context.Context, *mcp.CallToolRequest, emptyInput) (any, error) { return host, nil })
	for i := range 9 {
		bind(server, host, fmt.Sprintf("unused_%d", i), "fixture", true, func(context.Context, *mcp.CallToolRequest, emptyInput) (any, error) { return host, nil })
	}
	endpoint := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer endpoint.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint.URL, HTTPClient: newHTTPClient("fixture-token")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "coop_operation_status", Arguments: map[string]any{}})
	if err != nil || result.IsError || !matchesHost(result, host.ID) {
		t.Fatalf("valid maximum pages failed transport: %v", err)
	}

	// Exercise the complete stdio -> authenticated HTTP client -> SDK server path.
	t.Setenv("COOP_TEST_OUTPUT_TOKEN", "fixture-token")
	inputR, inputW := io.Pipe()
	outputR, outputW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunStdio(ctx, ClientConfig{Endpoint: endpoint.URL + "/mcp", TokenEnv: "COOP_TEST_OUTPUT_TOKEN", ExpectedHostID: host.ID}, inputR, outputW, io.Discard)
	}()
	t.Cleanup(func() { cancel(); _ = inputR.Close(); _ = inputW.Close(); _ = outputR.Close(); _ = outputW.Close() })
	stdio := mcp.NewClient(&mcp.Implementation{Name: "stdio-client", Version: "1"}, nil)
	forwarded, err := stdio.Connect(ctx, &mcp.IOTransport{Reader: outputR, Writer: inputW, MaxLineLength: maxResponseBytes}, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := forwarded.CallTool(ctx, &mcp.CallToolParams{Name: "coop_operation_status", Arguments: map[string]any{}})
	if err != nil || response.IsError || !matchesHost(response, host.ID) {
		t.Fatalf("maximum output pages failed stdio: %v", err)
	}
	_ = forwarded.Close()
	_ = inputR.Close()
	_ = outputW.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stdio did not shut down")
	}
}
func TestClientRejectsOversizedResponse(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, strings.Repeat("z", 5<<20)) }))
	defer endpoint.Close()
	resp, err := newHTTPClient("fixture-token").Get(endpoint.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err = io.Copy(io.Discard, resp.Body); err == nil {
		t.Fatal("oversized body was unbounded")
	}
}
