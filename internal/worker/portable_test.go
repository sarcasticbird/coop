package worker

import (
	"runtime"
	"testing"
)

func TestPortableBindingRequiresStateAndWorkOnDurableVolumes(t *testing.T) {
	for _, test := range []struct {
		state, work string
		valid       bool
	}{
		{"/var/lib/coop/worker/state", "/workspace/jobs", true},
		{"/var/lib/coop/state", "/workspace/jobs/../lost", false},
		{"/var/lib/coop/state", "/tmp/jobs", false},
		{"/tmp/state", "/workspace/jobs", false},
		{"/var/lib/coop-other/state", "/workspace/jobs", false},
	} {
		cfg := Config{Version: 3, StateDir: test.state, WorkRoot: test.work, Master: &MasterConfig{MasterID: "master", AgentID: "robin", HostID: "local", InstanceID: "instance", Generation: 2}}
		if got := portableLocations(cfg); got != test.valid {
			t.Fatal(test, got)
		}
		// /var is intentionally a symlink on macOS. Filesystem validation runs
		// against the guest layout on Linux CI, never by relaxing the guard.
		if test.valid && runtime.GOOS != "linux" {
			continue
		}
		binding, err := PortableBinding(cfg)
		if test.valid && (err != nil || binding.Generation != 2) || !test.valid && err == nil {
			t.Fatal(test, binding, err)
		}
	}
}
