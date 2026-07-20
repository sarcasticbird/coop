package runtime

import "testing"

// Captured from `container list --all --format json` (container 1.1.0,
// macOS 26.5.2) — guards against Apple schema drift. Mount ordering is
// real: agent state volumes precede the project bind mount, and volume
// mounts also land under HOME (why ProjectMount can't use destination
// alone).
const listFixture = `[{"configuration":{"id":"coop-tui-test","resources":{"cpus":4,"memoryInBytes":8589934592},"mounts":[{"type":{"volume":{"format":"ext4","name":"coop-tui-test--claude"}},"source":"/Users/u/Library/Application Support/com.apple.container/volumes/coop-tui-test--claude/volume.img","destination":"/Users/u/.claude"},{"type":{"volume":{"format":"ext4","name":"coop-tui-test--codex"}},"source":"/Users/u/Library/Application Support/com.apple.container/volumes/coop-tui-test--codex/volume.img","destination":"/Users/u/.codex"},{"type":{"volume":{"format":"ext4","name":"coop-tui-test--opencode"}},"source":"/Users/u/Library/Application Support/com.apple.container/volumes/coop-tui-test--opencode/volume.img","destination":"/Users/u/.local/share/opencode"},{"type":{"virtiofs":{}},"source":"/Users/u/Projects/proj","destination":"/Users/u/Projects/proj"}]},"status":{"state":"running","startedDate":"2026-07-19T20:53:53Z","networks":[{"ipv4Address":"192.168.64.24/24"}]}},{"configuration":{"id":"buildkit","resources":{"cpus":2,"memoryInBytes":2147483648}},"status":{"state":"running"}}]`

func TestParseListFiltersAndExtracts(t *testing.T) {
	infos, err := parseList([]byte(listFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("non-coop containers must be filtered, got %d entries", len(infos))
	}
	c := infos[0]
	if c.Name != "coop-tui-test" || c.State != "running" {
		t.Errorf("identity: %+v", c)
	}
	if c.IP != "192.168.64.24" {
		t.Errorf("IP CIDR suffix not stripped: %q", c.IP)
	}
	if c.CPUs != 4 || c.Memory != 8589934592 {
		t.Errorf("resources: %+v", c)
	}
	if c.Started.IsZero() {
		t.Errorf("startedDate not parsed")
	}
	if len(c.Mounts) != 4 {
		t.Errorf("mounts: %v", c.Mounts)
	}
	// state volumes come first in real output — ProjectMount must still
	// find the bind mount, not an agent volume under HOME
	if got := c.ProjectMount(); got != "/Users/u/Projects/proj" {
		t.Errorf("ProjectMount = %q (picked an agent volume?)", got)
	}
}

func TestParseContainerImage(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"valid", `[{"configuration":{"image":{"reference":"coop:latest"}}}]`, "coop:latest", false},
		{"empty reference", `[{"configuration":{"image":{"reference":""}}}]`, "", true},
		{"missing image key", `[{"configuration":{}}]`, "", true},
		{"zero entries", `[]`, "", true},
		{"malformed", `{not json`, "", true},
	}
	for _, tc := range cases {
		got, err := parseContainerImage([]byte(tc.in))
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%s: got (%q, %v), want (%q, err=%v)", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestStateFromList(t *testing.T) {
	mk := func(id, state string) string {
		return `[{"configuration":{"id":"` + id + `"},"status":{"state":"` + state + `"}}]`
	}
	cases := []struct {
		name    string
		json    string
		want    State
		wantErr bool
	}{
		{"running", mk("c1", "running"), StateRunning, false},
		{"stopped", mk("c1", "stopped"), StateStopped, false},
		{"other state maps to stopped", mk("c1", "stopping"), StateStopped, false},
		{"absent target", mk("other", "running"), StateAbsent, false},
		{"empty list", `[]`, StateAbsent, false},
		{"missing id is drift", mk("", "running"), StateAbsent, true},
		{"empty state is drift", mk("c1", ""), StateAbsent, true},
		{"malformed", `{nope`, StateAbsent, true},
	}
	for _, tc := range cases {
		got, err := stateFromList("c1", []byte(tc.json))
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%s: got (%v, %v), want (%v, err=%v)", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestParseListEmpty(t *testing.T) {
	infos, err := parseList([]byte(`[]`))
	if err != nil || len(infos) != 0 {
		t.Errorf("empty list should parse cleanly: %v %v", infos, err)
	}
}
