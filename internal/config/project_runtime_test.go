package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// This catches a loader that ignores or mis-defaults explicit project runtime
// isolation: the local config must produce one canonical volume and two exact
// loopback port mappings without depending on declaration order.
func TestLoadProjectRuntime(t *testing.T) {
	project := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.Mkdir(filepath.Join(project, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `
[[volume]]
path = "web/node_modules"

[[publish]]
guest_port = 8000
host_port = 18000

[[publish]]
guest_port = 5173
`
	if err := os.WriteFile(filepath.Join(project, ".coop.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Volume{{Path: "web/node_modules"}}; !reflect.DeepEqual(cfg.Volumes, want) {
		t.Fatalf("volumes = %+v, want %+v", cfg.Volumes, want)
	}
	if want := []Publish{
		{GuestPort: 5173, HostPort: 5173},
		{GuestPort: 8000, HostPort: 18000},
	}; !reflect.DeepEqual(cfg.Publishes, want) {
		t.Fatalf("publishes = %+v, want %+v", cfg.Publishes, want)
	}
}

// This catches a provenance regression that would apply one relative path or
// host port to every Coop on the machine. Runtime isolation is local-project
// authority in version one.
func TestProjectRuntimeRejectedFromGlobalConfig(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	globalDir := filepath.Join(configHome, "coop")
	if err := os.Mkdir(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	global := `
[[volume]]
path = "web/node_modules"
`
	if err := os.WriteFile(filepath.Join(globalDir, "coop.toml"), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(project)
	if err == nil || !strings.Contains(err.Error(), "only supported in project config") {
		t.Fatalf("global project runtime error = %v", err)
	}
}

// This catches removal of the confinement and collision checks that make local
// project runtime declarations safe and deterministic before container state
// can be mutated.
func TestProjectRuntimeValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		setup  func(*testing.T, string)
		want   string
	}{
		{name: "empty volume path", config: "[[volume]]\npath = \"\"\n", want: "project volume"},
		{name: "absolute volume path", config: "[[volume]]\npath = \"/tmp/node_modules\"\n", want: "relative"},
		{name: "noncanonical volume path", config: "[[volume]]\npath = \"web/../web/node_modules\"\n", want: "normalized"},
		{name: "runtime grammar", config: "[[volume]]\npath = \"web/node,modules\"\n", want: "runtime grammar"},
		{name: "control character", config: "[[volume]]\npath = \"web/\\tnode_modules\"\n", want: "control character"},
		{name: "missing parent", config: "[[volume]]\npath = \"missing/node_modules\"\n", want: "parent"},
		{
			name:   "symlink parent",
			config: "[[volume]]\npath = \"web/link/node_modules\"\n",
			setup: func(t *testing.T, project string) {
				t.Helper()
				if err := os.Symlink(t.TempDir(), filepath.Join(project, "web", "link")); err != nil {
					t.Fatal(err)
				}
			},
			want: "symlink",
		},
		{
			name:   "existing file target",
			config: "[[volume]]\npath = \"web/node_modules\"\n",
			setup: func(t *testing.T, project string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(project, "web", "node_modules"), []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "not a directory",
		},
		{
			name: "overlapping targets",
			config: `
[[volume]]
path = "web/node_modules"
[[volume]]
path = "web/node_modules/cache"
`,
			setup: func(t *testing.T, project string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(project, "web", "node_modules", "cache"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "overlaps",
		},
		{
			name: "overlapping targets separated by lexical sibling",
			config: `
[[volume]]
path = "a"
[[volume]]
path = "a-"
[[volume]]
path = "a/b"
`,
			setup: func(t *testing.T, project string) {
				t.Helper()
				for _, path := range []string{"a/b", "a-"} {
					if err := os.MkdirAll(filepath.Join(project, path), 0o755); err != nil {
						t.Fatal(err)
					}
				}
			},
			want: "overlaps",
		},
		{name: "zero guest port", config: "[[publish]]\nguest_port = 0\n", want: "guest_port"},
		{name: "large host port", config: "[[publish]]\nguest_port = 80\nhost_port = 70000\n", want: "host_port"},
		{
			name: "host conflict",
			config: `
[[publish]]
guest_port = 5173
host_port = 8080
[[publish]]
guest_port = 8000
host_port = 8080
`,
			want: "host port 8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			if err := os.Mkdir(filepath.Join(project, "web"), 0o755); err != nil {
				t.Fatal(err)
			}
			if tt.setup != nil {
				tt.setup(t, project)
			}
			if err := os.WriteFile(filepath.Join(project, ".coop.toml"), []byte(tt.config), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := Load(project)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

// This catches canonicalization that leaves repeated declarations in the
// container specification, where harmless config duplication would otherwise
// change identity or repeat runtime flags.
func TestProjectRuntimeDeduplicates(t *testing.T) {
	project := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.Mkdir(filepath.Join(project, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `
[[volume]]
path = "web/node_modules"
[[volume]]
path = "web/node_modules"
[[publish]]
guest_port = 5173
[[publish]]
guest_port = 5173
host_port = 5173
`
	if err := os.WriteFile(filepath.Join(project, ".coop.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Volumes) != 1 || cfg.Volumes[0].Path != "web/node_modules" {
		t.Fatalf("volumes = %+v, want one canonical declaration", cfg.Volumes)
	}
	if len(cfg.Publishes) != 1 || cfg.Publishes[0] != (Publish{GuestPort: 5173, HostPort: 5173}) {
		t.Fatalf("publishes = %+v, want one canonical declaration", cfg.Publishes)
	}
}

// This catches removal of the hard declaration bounds that keep config load
// and later container argv construction predictable.
func TestProjectRuntimeBoundsDeclarations(t *testing.T) {
	tests := []struct {
		name   string
		config func() string
	}{
		{
			name: "volumes",
			config: func() string {
				var b strings.Builder
				for i := 0; i < MaxProjectVolumes+1; i++ {
					fmt.Fprintf(&b, "[[volume]]\npath = \"web/cache-%d\"\n", i)
				}
				return b.String()
			},
		},
		{
			name: "published ports",
			config: func() string {
				var b strings.Builder
				for i := 0; i < MaxPublishedPorts+1; i++ {
					fmt.Fprintf(&b, "[[publish]]\nguest_port = %d\n", 10000+i)
				}
				return b.String()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			if err := os.Mkdir(filepath.Join(project, "web"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(project, ".coop.toml"), []byte(tt.config()), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := Load(project)
			if err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
				t.Fatalf("error = %v, want declaration limit", err)
			}
		})
	}
}
