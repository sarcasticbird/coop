package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestFleetTUICommandCoverageInventory(t *testing.T) {
	doc, err := os.ReadFile("../../docs/fleet-tui.md")
	if err != nil {
		t.Fatal(err)
	}
	const start = "<!-- fleet-tui-command-inventory:start -->"
	const end = "<!-- fleet-tui-command-inventory:end -->"
	_, table, ok := strings.Cut(string(doc), start)
	if !ok || strings.Count(string(doc), start) != 1 || strings.Count(string(doc), end) != 1 {
		t.Fatal("expected exactly one marked command inventory")
	}
	table, _, ok = strings.Cut(table, end)
	if !ok {
		t.Fatal("command inventory end must follow start")
	}
	rows := make(map[string]bool)
	for _, line := range strings.Split(table, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 5 {
			t.Fatalf("expected three inventory columns: %s", line)
		}
		name := strings.TrimSpace(fields[1])
		if len(name) < 3 || name[0] != '`' || name[len(name)-1] != '`' || strings.Count(name, "`") != 2 {
			t.Fatalf("expected one exact command per row: %s", line)
		}
		name = strings.Trim(name, "`")
		disposition := strings.TrimSpace(fields[2])
		if (disposition != "UI" && disposition != "CLI/runbook") || strings.TrimSpace(fields[3]) == "" {
			t.Errorf("command %q needs an explicit disposition and explanation", name)
		}
		if rows[name] {
			t.Errorf("duplicate inventory command %q", name)
		}
		rows[name] = true
	}
	cmd := root()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--help"})
	// Execute help only to install Cobra's generated help/completion commands.
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden {
			return
		}
		if c.Run != nil || c.RunE != nil {
			name := strings.Join(commandParts(c), " ")
			if c == cmd {
				name = "coop"
			}
			if !rows[name] {
				t.Errorf("command %q missing from docs/fleet-tui.md inventory", name)
			}
			delete(rows, name)
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(cmd)
	for name := range rows {
		t.Errorf("stale inventory command %q is not a visible executable command", name)
	}
}
