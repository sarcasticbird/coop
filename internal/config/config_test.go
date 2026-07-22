package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

func TestToolPackagesMergeCanonically(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), `
[tools]
packages = ["shellcheck", "bat", "shellcheck"]
`)
	project := t.TempDir()
	mustWrite(t, filepath.Join(project, "coop.toml"), `
[tools]
packages = ["bat", "actionlint"]
`)

	cfg, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"actionlint", "bat", "shellcheck"}; !slices.Equal(cfg.Tools.Packages, want) {
		t.Fatalf("effective tools = %v, want %v", cfg.Tools.Packages, want)
	}
	if want := []string{"bat", "shellcheck"}; !slices.Equal(cfg.Tools.GlobalPackages, want) {
		t.Fatalf("global tools = %v, want %v", cfg.Tools.GlobalPackages, want)
	}
	if want := []string{"actionlint", "bat"}; !slices.Equal(cfg.Tools.ProjectPackages, want) {
		t.Fatalf("project tools = %v, want %v", cfg.Tools.ProjectPackages, want)
	}
}

func TestToolPackageIdentifiers(t *testing.T) {
	valid := []string{"gh", "nodePackages.prettier", "python313Packages.ruff", "pkg-with_underscores+plus"}
	for _, pkg := range valid {
		t.Run("valid "+pkg, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			project := t.TempDir()
			mustWrite(t, filepath.Join(project, "coop.toml"), "[tools]\npackages = [\""+pkg+"\"]\n")
			if _, err := Load(project); err != nil {
				t.Fatalf("valid package %q rejected: %v", pkg, err)
			}
		})
	}

	invalid := []string{
		"", ".leading", "trailing.", "two..dots", "has space", "has\ttab",
		"has\nnewline", "path/pkg", `path\pkg`, "github:owner/repo", "nixpkgs#gh",
		"$(touch-pwned)", "semi;colon", "quote'pkg", `quote"pkg`, strings.Repeat("a", 129),
	}
	for i, pkg := range invalid {
		t.Run(fmt.Sprintf("invalid-%d", i), func(t *testing.T) {
			xdg := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", xdg)
			mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), "[tools]\npackages = ["+strconv.Quote(pkg)+"]\n")
			if _, err := Load(""); err == nil {
				t.Fatalf("invalid package %q accepted", pkg)
			}
		})
	}
}

func TestToolPackagesBoundEffectiveUniqueSet(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	global := make([]string, MaxToolPackages)
	for i := range global {
		global[i] = fmt.Sprintf("global%d", i)
	}
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), toolPackageTOML(global))
	project := t.TempDir()
	// A duplicate at the boundary is allowed because the effective set remains 64.
	mustWrite(t, filepath.Join(project, "coop.toml"), toolPackageTOML([]string{"global0"}))
	if _, err := Load(project); err != nil {
		t.Fatalf("64 unique packages rejected: %v", err)
	}
	mustWrite(t, filepath.Join(project, "coop.toml"), toolPackageTOML([]string{"extra"}))
	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), "64") {
		t.Fatalf("65 unique packages accepted: %v", err)
	}
}

func TestLegacyExtraPackagesAlias(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), `
[image]
extra_packages = ["shellcheck", "bat"]
`)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bat", "shellcheck"}; !slices.Equal(cfg.Tools.Packages, want) {
		t.Fatalf("legacy alias tools = %v, want %v", cfg.Tools.Packages, want)
	}
	if len(cfg.Image.ExtraPackages) != 0 {
		t.Fatalf("legacy input leaked into the merged image model: %v", cfg.Image.ExtraPackages)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "deprecated") || !strings.Contains(cfg.Warnings[0], "tools.packages") {
		t.Fatalf("legacy warning = %v", cfg.Warnings)
	}
}

func TestLegacyExtraPackagesConflictsWithTools(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), `
[image]
extra_packages = ["bat"]
[tools]
packages = ["shellcheck"]
`)
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("legacy and current fields accepted together: %v", err)
	}
}

func TestProjectLegacyExtraPackagesRejected(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	mustWrite(t, filepath.Join(project, "coop.toml"), `
[image]
extra_packages = ["shellcheck"]
`)
	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), "tools.packages") {
		t.Fatalf("project legacy package field accepted: %v", err)
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

func TestCredentialConfigGlobalOnly(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), `
include_credentials = ["git"]
[credentials.git]
source = { type = "file", path = "~/.git-credentials" }
inject = { type = "git-credential-store" }
`)
	project := t.TempDir()
	mustWrite(t, filepath.Join(project, "coop.toml"), `
include_credentials = ["aws-prod"]
[credentials.aws-prod]
source = { type = "command", argv = ["steal-host-secret"] }
inject = { type = "environment", name = "AWS_SECRET_ACCESS_KEY" }
`)

	cfg, err := Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.IncludeCredentials, ","); got != "git" {
		t.Fatalf("project changed included credentials: %q", got)
	}
	if _, ok := cfg.Credentials["aws-prod"]; ok {
		t.Fatal("SECURITY: project defined a host credential grant")
	}
	if got := cfg.Credentials["git"].Source.Path; got != "~/.git-credentials" {
		t.Fatalf("global grant missing: %q", got)
	}
}

func TestProjectCredentialConfigIsValidatedBeforeBeingIgnored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	mustWrite(t, filepath.Join(project, "coop.toml"), `
[credentials.bad]
source = { type = "command", argv = ["./project-helper"] }
inject = { type = "environment", name = "TOKEN" }
`)

	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), "executable path") {
		t.Fatalf("malformed project credential definition accepted: %v", err)
	}
}

func TestCredentialConfigDeduplicatesIncludes(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), `
include_credentials = ["git", "git"]
[credentials.git]
source = { type = "file", path = "~/.git-credentials" }
inject = { type = "git-credential-store" }
`)

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.IncludeCredentials, ","); got != "git" {
		t.Fatalf("included credentials = %q", got)
	}
}

func TestCredentialConfigValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	tooMany := strings.Builder{}
	for i := 0; i <= MaxCredentialGrants; i++ {
		fmt.Fprintf(&tooMany, "[credentials.grant-%d]\nsource = { type = \"command\", argv = [\"token\"] }\ninject = { type = \"environment\", name = \"TOKEN_%d\" }\n", i, i)
	}

	cases := map[string]string{
		"bad grant name": `[credentials."Bad--Name"]
source = { type = "command", argv = ["token"] }
inject = { type = "environment", name = "TOKEN" }
`,
		"too many grants": tooMany.String(),
		"missing source path": `[credentials.git]
source = { type = "file" }
inject = { type = "git-credential-store" }
`,
		"relative file source": `[credentials.git]
source = { type = "file", path = "project-secret" }
inject = { type = "git-credential-store" }
`,
		"relative command executable path": `[credentials.token]
source = { type = "command", argv = ["./credential-helper"] }
inject = { type = "environment", name = "TOKEN" }
`,
		"file source with argv": `[credentials.git]
source = { type = "file", path = "~/.git-credentials", argv = ["unused"] }
inject = { type = "git-credential-store" }
`,
		"missing command argv": `[credentials.token]
source = { type = "command" }
inject = { type = "environment", name = "TOKEN" }
`,
		"command source with path": `[credentials.token]
source = { type = "command", argv = ["token"], path = "unused" }
inject = { type = "environment", name = "TOKEN" }
`,
		"missing aws profile": `[credentials.aws-dev]
source = { type = "aws-profile" }
inject = { type = "aws" }
`,
		"aws profile with generic injection": `[credentials.aws-dev]
source = { type = "aws-profile", profile = "dev" }
inject = { type = "environment", name = "AWS_TOKEN" }
`,
		"missing injection type": `[credentials.token]
source = { type = "command", argv = ["token"] }
inject = { name = "TOKEN" }
`,
		"missing environment name": `[credentials.token]
source = { type = "command", argv = ["token"] }
inject = { type = "environment" }
`,
		"invalid environment name": `[credentials.token]
source = { type = "command", argv = ["token"] }
inject = { type = "environment", name = "BAD-NAME" }
`,
		"environment with path env": `[credentials.token]
source = { type = "command", argv = ["token"] }
inject = { type = "environment", name = "TOKEN", path_env = "TOKEN_FILE" }
`,
		"missing file path env": `[credentials.kubernetes]
source = { type = "file", path = "~/.kube/config" }
inject = { type = "file" }
`,
		"aws injection with command": `[credentials.aws-dev]
source = { type = "command", argv = ["aws-token"] }
inject = { type = "aws" }
`,
		"git injection with command": `[credentials.git]
source = { type = "command", argv = ["git-token"] }
inject = { type = "git-credential-store" }
`,
		"expiration with file": `[credentials.token]
source = { type = "file", path = "~/.token" }
inject = { type = "environment", name = "TOKEN" }
require_expiration = true
`,
		"unknown default": `include_credentials = ["missing"]
`,
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			xdg := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", xdg)
			mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), content)
			if _, err := Load(""); err == nil {
				t.Fatalf("invalid credential configuration accepted:\n%s", content)
			}
		})
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

func toolPackageTOML(packages []string) string {
	quoted := make([]string, len(packages))
	for i, pkg := range packages {
		quoted[i] = strconv.Quote(pkg)
	}
	return "[tools]\npackages = [" + strings.Join(quoted, ", ") + "]\n"
}
