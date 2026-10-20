package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/spf13/cobra"
)

func TestTUICommandCoverageExhaustive(t *testing.T) {
	doc, err := os.ReadFile("../../docs/tui-operations.md")
	if err != nil {
		t.Fatal(err)
	}
	cmd := root()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden {
			return
		}
		if c.Parent() != nil && (c.Run != nil || c.RunE != nil) {
			name := strings.Join(commandParts(c), " ")
			if !strings.Contains(string(doc), "`"+name+"`") {
				t.Errorf("command %q missing from TUI coverage matrix", name)
			}
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(cmd)
}

func TestTUILaunchPreservesImmutableHostAndArgv(t *testing.T) {
	for _, h := range []hostrouter.Host{hostrouter.Local, {Name: "remote", Address: "trusted-alias"}} {
		req := hostrouter.ActionRequest{Target: hostrouter.Target{Host: h, Kind: hostrouter.Interactive, ID: "coop-project"}, Operation: "command", Directory: "/selected/project", Args: []string{"init", "--"}}
		cmd, err := tuiCommand(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(cmd.Args) != 3 || cmd.Args[1] != "tui-exec" {
			t.Fatal(cmd.Args)
		}
		b, err := base64.RawURLEncoding.DecodeString(cmd.Args[2])
		if err != nil {
			t.Fatal(err)
		}
		var v hostrouter.Invocation
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		if v.Target != req.Target || v.Directory != req.Directory || !reflect.DeepEqual(v.Args, req.Args) {
			t.Fatal("launch redirected", v)
		}
	}
}

func TestTUIReceiverRejectsChangedHostAndNestedRouting(t *testing.T) {
	r := serverTestRegistry(t)
	if err := r.Change("add", "remote", "replacement"); err != nil {
		t.Fatal(err)
	}
	v := hostrouter.Invocation{Version: 1, Target: hostrouter.Target{Host: hostrouter.Host{Name: "remote", Address: "original"}}, Args: []string{"doctor"}}
	b, _ := json.Marshal(v)
	if _, err := runHostCLI("tui-exec", base64.RawURLEncoding.EncodeToString(b)); err == nil || !strings.Contains(err.Error(), "captured host changed") {
		t.Fatal("stale host accepted", err)
	}
	v.Target.Host = hostrouter.Local
	v.Args = []string{"tui-exec", "anything"}
	b, _ = json.Marshal(v)
	if _, err := runHostCLI("tui-exec", base64.RawURLEncoding.EncodeToString(b)); err == nil || !strings.Contains(err.Error(), "not remotely routable") {
		t.Fatal("nested UI accepted", err)
	}
}

func TestTUIReceiverUsesGuardedLocalHandlers(t *testing.T) {
	serverTestRegistry(t)
	for _, args := range [][]string{{"--host=other", "doctor"}, {"--agent=other", "status"}, {"--target-id=replacement", "status"}} {
		v := hostrouter.Invocation{Version: 1, Target: hostrouter.Target{Host: hostrouter.Local, Kind: hostrouter.Agent, ID: "selected"}, Args: args}
		b, _ := json.Marshal(v)
		if _, err := runHostCLI("tui-exec", base64.RawURLEncoding.EncodeToString(b)); err == nil {
			t.Fatal("invalid selector accepted", args)
		}
	}
}
