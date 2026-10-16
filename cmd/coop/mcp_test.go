package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/sarcasticbird/coop/internal/runtime"
	"strings"
	"testing"
)

func TestMCPCommandRejectsAgentAndGrantsBeforeConfiguration(t *testing.T) {
	oldRuntime := newRuntime
	t.Cleanup(func() { newRuntime = oldRuntime })
	newRuntime = func() runtime.Runtime { return &runtime.Mock{} }
	old := runNamedAgent
	t.Cleanup(func() { runNamedAgent = old })
	called := false
	runNamedAgent = func(context.Context, string, []string, []string) error {
		called = true
		return fmt.Errorf("legacy entry reached")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{{"--agent", "robin", "mcp", "serve", "--config", "/missing"}, {"mcp", "stdio", "--config", "relative"}} {
		c := root()
		c.SetArgs(args)
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		err := c.Execute()
		if err == nil || called || strings.Contains(err.Error(), "unsupported named-agent") {
			t.Fatalf("MCP routed into agent command: %v", err)
		}
	}
}
func TestMCPCommandRegistered(t *testing.T) {
	c := root()
	cmd, _, err := c.Find([]string{"mcp", "serve"})
	if err != nil || cmd.Name() != "serve" {
		t.Fatal("MCP serve command not registered")
	}
}
