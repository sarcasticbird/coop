package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/sarcasticbird/coop/internal/hostcontrol"
	"github.com/sarcasticbird/coop/internal/runtime"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestMCPServerStopsWhenEngineFails(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", root)
	config := filepath.Join(root, "server.toml")
	verifiers := filepath.Join(root, "verifiers.json")
	if err = os.WriteFile(verifiers, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprintf("version=1\nlabel='test'\nlisten='127.0.0.1:0'\nstate_dir='%s/state'\nverifier_file='%s'\nallowed_hosts=['127.0.0.1']\n", root, verifiers)
	if err = os.WriteFile(config, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	old := runMCPEngine
	defer func() { runMCPEngine = old }()
	expected := errors.New("engine persistence failed")
	runMCPEngine = func(context.Context, *hostcontrol.Engine) error { return expected }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveMCP(ctx, config) }()
	select {
	case err := <-done:
		if !errors.Is(err, expected) {
			t.Fatalf("engine error lost: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("HTTP service outlived failed operation engine")
	}
}
