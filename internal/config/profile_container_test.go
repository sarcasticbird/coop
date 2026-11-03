package config

import (
	"testing"

	"github.com/BurntSushi/toml"
)

func TestPortableProfileRequiresExplicitWorkerExecution(t *testing.T) {
	base := `[profiles.robin]
runtime = "container"
command = ["opencode"]
[profiles.robin.services.worker]
command = ["/usr/local/libexec/coop-worker", "run"]
`
	execution := `[profiles.robin.execution]
master_id = "master"
agent_id = "robin"
host_id = "local"
worker_instance_id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
generation = 1
client_config = "/private/robin-client.toml"
`
	for _, valid := range []bool{false, true} {
		cfg := Default()
		raw := base
		if valid {
			raw += execution
		}
		if _, err := toml.Decode(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		err := validateProfiles(&cfg)
		if valid && err != nil || !valid && err == nil {
			t.Fatal(valid, err)
		}
		if valid && cfg.Profiles["robin"].Execution.AgentID != "robin" {
			t.Fatal("embedded execution pin was not decoded")
		}
	}
}
