package config

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestAgentExampleLoads(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	copyFile(t, filepath.Join(filepath.Dir(file), "..", "..", "examples", "coop.agent.toml"), filepath.Join(configHome(), "coop", "coop.toml"))
	cfg, profile, err := LoadProfile("robin")
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Orgs) != 1 || len(cfg.IncludeCredentials) != 1 || cfg.SSH || len(cfg.Mounts) != 0 {
		t.Fatal("agent example capabilities differ from documentation")
	}
}

func TestLoadProfileIgnoresCurrentProject(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), `
mount = [{ source = "/a/nonexistent/interactive/share" }]
ssh = true
[profiles.robin]
command = ["claude"]
orgs = ["GreatCircleIO"]
[profiles.robin.resources]
cpus = 6
memory = "12G"
`)
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, ".coop.toml"), "invalid toml")
	t.Chdir(cwd)
	cfg, profile, err := LoadProfile("robin")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SSH || len(cfg.Mounts)+len(cfg.Projects)+len(cfg.Agents)+len(cfg.Volumes) != 0 {
		t.Fatalf("inherited capabilities: %+v", cfg)
	}
	if !slices.Equal(profile.Command, []string{"claude"}) || cfg.Resources.CPUs != 6 || cfg.Resources.Memory != "12G" {
		t.Fatalf("wrong profile: %+v %+v", profile, cfg.Resources)
	}
}

func TestProfileValidation(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"Robin", `command = ["claude"]`},
		{"-robin", `command = ["claude"]`},
		{"robin-", `command = ["claude"]`},
		{strings.Repeat("r", 64), `command = ["claude"]`},
		{"robin", `command = []`},
		{"robin", `command = [""]`},
		{"robin", `command = ["claude"]
orgs = ["../escape"]`},
		{"robin", `command = ["claude"]
include_credentials = ["missing"]`},
		{"robin", `command = ["claude"]
[profiles.robin.resources]
cpus = -1`},
		{"robin", `command = ["claude"]
[profiles.robin.resources]
cpus = 0`},
		{"robin", `command = ["claude"]
[profiles.robin.resources]
memory = ""`},
	} {
		t.Run(tc.name+tc.body, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			mustWrite(t, filepath.Join(configHome(), "coop", "coop.toml"), "[profiles."+tc.name+"]\n"+tc.body)
			if _, _, err := LoadProfile(tc.name); err == nil {
				t.Fatal("invalid profile accepted")
			}
		})
	}
}

func TestProjectCannotDefineProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".coop.toml"), "[profiles.robin]\ncommand = [\"claude\"]\n")
	if _, err := Load(root); err == nil {
		t.Fatal("project defined host agent identity")
	}
}

func TestProfileSelectsOnlyItsCredentials(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mustWrite(t, filepath.Join(configHome(), "coop", "coop.toml"), `
include_credentials = ["other"]
[credentials.robin]
source = {type="file",path="~/robin.token"}
expose = [{type="environment",name="GH_TOKEN"}]
[credentials.other]
source = {type="file",path="~/other.token"}
expose = [{type="environment",name="OTHER_TOKEN"}]
[profiles.robin]
command = ["claude"]
include_credentials = ["robin"]
`)
	cfg, _, err := LoadProfile("robin")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.IncludeCredentials, []string{"robin"}) {
		t.Fatalf("grants = %q", cfg.IncludeCredentials)
	}
	if _, _, err := LoadProfile("unknown"); err == nil {
		t.Fatal("unknown profile accepted")
	}
}

func TestProfileSupportsServerAndSupervisedHelpers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mustWrite(t, filepath.Join(configHome(), "coop", "coop.toml"), `
[profiles.robin]
command = ["opencode", "serve", "--hostname", "0.0.0.0", "--port", "8788"]
restart = "on-failure"
[profiles.robin.services.tunnel]
command = ["cloudflared", "tunnel", "run", "robin"]
`)
	_, p, err := LoadProfile("robin")
	if err != nil {
		t.Fatal(err)
	}
	if p.Restart != "on-failure" || len(p.Services["tunnel"].Command) != 4 {
		t.Fatalf("profile=%+v", p)
	}
}
