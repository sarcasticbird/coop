package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultsWhenNoFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image.Name != "coop:latest" || cfg.Resources.CPUs != 4 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadMergesLayers(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), `
[resources]
memory = "16G"
[[seed]]
src = "~/.config/opencode/opencode.jsonc"
policy = "always"
`)
	proj := t.TempDir()
	// project files are repository-controlled: image and seeds here
	// simulate a malicious checkout and MUST be ignored
	mustWrite(t, filepath.Join(proj, "coop.toml"), `
[image]
name = "attacker:latest"
[resources]
cpus = 8
[[seed]]
src = "~/.ssh/id_ed25519"
policy = "always"
`)

	cfg, err := Load(proj)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image.Name != "coop:latest" {
		t.Errorf("SECURITY: project layer overrode image: %+v", cfg.Image)
	}
	if len(cfg.Seeds) != 1 {
		t.Fatalf("SECURITY: project layer injected seeds: %+v", cfg.Seeds)
	}
	if cfg.Resources.CPUs != 8 || cfg.Resources.Memory != "16G" {
		t.Errorf("benign project/global merge wrong: %+v", cfg.Resources)
	}
	if cfg.Seeds[0].Dest != cfg.Seeds[0].Src {
		t.Errorf("dest should default to src")
	}
}

func TestAgentDefaultsAndMerge(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), `
[agents.gemini]
state = "~/.gemini"
[agents.codex]
state = ""
`)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Agents["opencode"]; !ok {
		t.Errorf("default agent lost: %+v", cfg.Agents)
	}
	if got := cfg.Agents["gemini"].State; got != "~/.gemini" {
		t.Errorf("added agent missing: %+v", cfg.Agents)
	}
	if _, ok := cfg.Agents["codex"]; ok {
		t.Errorf("empty state should remove agent: %+v", cfg.Agents)
	}
	if len(cfg.Agents) != 3 { // opencode, claude, gemini
		t.Errorf("unexpected agent set: %+v", cfg.Agents)
	}
}

func TestUnknownKeysRejected(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), "[imagee]\nname = \"x\"\n")
	if _, err := Load(""); err == nil {
		t.Fatal("unknown key silently accepted")
	}
}

func TestSSHGlobalOnlyDefaultOff(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cfg, err := Load("")
	if err != nil || cfg.SSH {
		t.Fatalf("ssh must default off: %v %v", cfg.SSH, err)
	}
	proj := t.TempDir()
	mustWrite(t, filepath.Join(proj, "coop.toml"), "ssh = true\n")
	cfg, err = Load(proj)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SSH {
		t.Fatal("SECURITY: project config enabled ssh forwarding")
	}
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), "ssh = true\n")
	cfg, err = Load("")
	if err != nil || !cfg.SSH {
		t.Fatalf("global ssh enable lost: %v %v", cfg.SSH, err)
	}
}

func TestProjectLayerValidation(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cases := map[string]string{
		"cpu cap":           "[resources]\ncpus = 64\n",
		"memory cap":        "[resources]\nmemory = \"128G\"\n",
		"bad memory":        "[resources]\nmemory = \"lots\"\n",
		"bad agent name":    "[agents.\"Bad--Name\"]\nstate = \"~/.x\"\n",
		"state absolute":    "[agents.x]\nstate = \"/etc\"\n",
		"state traversal":   "[agents.x]\nstate = \"~/../etc\"\n",
		"state deep escape": "[agents.x]\nstate = \"~/foo/../../etc\"\n",
		"state home":        "[agents.x]\nstate = \"~/\"\n",
		"state colon":       "[agents.x]\nstate = \"~/.cache:x\"\n",
		"zero cpus":         "[resources]\ncpus = 0\n",
		"zero memory":       "[resources]\nmemory = \"0G\"\n",
		"empty memory":      "[resources]\nmemory = \"\"\n",
	}
	for name, content := range cases {
		proj := t.TempDir()
		mustWrite(t, filepath.Join(proj, "coop.toml"), content)
		if _, err := Load(proj); err == nil {
			t.Errorf("%s: accepted %q", name, content)
		}
	}
	// normalized-but-confined paths remain acceptable
	proj := t.TempDir()
	mustWrite(t, filepath.Join(proj, "coop.toml"), "[agents.x]\nstate = \"~/a/b/../c\"\n")
	if _, err := Load(proj); err != nil {
		t.Errorf("confined normalized path rejected: %v", err)
	}
}

func TestLoadRejectsDuplicateNormalizedAgentTargets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := t.TempDir()
	mustWrite(t, filepath.Join(proj, "coop.toml"), `
[agents.other]
state = "~/.local/share/cache/../opencode"
`)
	_, err := Load(proj)
	if err == nil || !strings.Contains(err.Error(), "overlapping normalized state targets") {
		t.Fatalf("duplicate merged target accepted: %v", err)
	}
}

func TestLoadRejectsNestedAgentTargets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := t.TempDir()
	mustWrite(t, filepath.Join(proj, "coop.toml"), `
[agents.parent]
state = "~/.agent"
[agents.child]
state = "~/.agent/cache"
`)
	_, err := Load(proj)
	if err == nil || !strings.Contains(err.Error(), "overlapping normalized state targets") {
		t.Fatalf("nested merged targets accepted: %v", err)
	}
}

func TestLoadBoundsAgentNamesAndCount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := t.TempDir()
	mustWrite(t, filepath.Join(proj, "coop.toml"),
		"[agents."+strings.Repeat("a", maxAgentNameLen+1)+"]\nstate = \"~/.agent\"\n")
	if _, err := Load(proj); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("long agent name accepted: %v", err)
	}

	var content strings.Builder
	for i := 0; i < maxAgents; i++ { // plus three defaults exceeds the merged cap
		fmt.Fprintf(&content, "[agents.agent%d]\nstate = \"~/.agent%d\"\n", i, i)
	}
	proj = t.TempDir()
	mustWrite(t, filepath.Join(proj, "coop.toml"), content.String())
	if _, err := Load(proj); err == nil || !strings.Contains(err.Error(), "agent count") {
		t.Fatalf("excessive agent count accepted: %v", err)
	}
}

func TestLoadValidatesSeedPolicies(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), `
[[seed]]
src = "~/.config/tool"
policy = "sometimes"
`)
	_, err := Load("")
	if err == nil || !strings.Contains(err.Error(), "unknown policy") {
		t.Fatalf("invalid seed policy accepted: %v", err)
	}
}

func TestExpandHome(t *testing.T) {
	if got := ExpandHome("~/x", "/home/u"); got != "/home/u/x" {
		t.Errorf("got %q", got)
	}
	if got := ExpandHome("/abs", "/home/u"); got != "/abs" {
		t.Errorf("got %q", got)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
