package config

import (
	"path/filepath"
	"strings"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestProfileDiagnosticBindingIsExplicitAndHostOwned(t *testing.T) {
	const body = `[profiles.bird]
command = ["tool"]
[profiles.bird.diagnostics]
master_id = "fixture-master"
agent_id = "different-agent-id"
host_id = "coordination-host-not-ssh-alias"
worker_instance_id = "fixture-instance"
generation = 2
`
	for _, remove := range []string{"", "master_id", "agent_id", "host_id", "worker_instance_id", "generation"} {
		t.Run(remove, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			var lines []string
			for _, line := range strings.Split(body, "\n") {
				if remove == "" || !strings.HasPrefix(line, remove+" =") {
					lines = append(lines, line)
				}
			}
			mustWrite(t, filepath.Join(configHome(), "coop", "coop.toml"), strings.Join(lines, "\n"))
			_, p, err := LoadProfile("bird")
			if remove != "" {
				if err == nil {
					t.Fatal("partial placement accepted")
				}
				return
			}
			want := c.DiagnosticBinding{MasterID: "fixture-master", Placement: c.Placement{AgentID: "different-agent-id", HostID: "coordination-host-not-ssh-alias", InstanceID: "fixture-instance", Generation: 2}}
			if err != nil || p.Diagnostics == nil || *p.Diagnostics != want {
				t.Fatalf("binding=%+v error=%v", p.Diagnostics, err)
			}
			root := t.TempDir()
			mustWrite(t, filepath.Join(root, ".coop.toml"), body)
			if _, err := Load(root); err == nil {
				t.Fatal("project supplied host diagnostic binding")
			}
		})
	}
}
