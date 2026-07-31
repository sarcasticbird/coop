package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

func TestFlakeToolPackageExplainsPinnedSourceMigration(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), `[tools]
packages = ["github:owner/repo#tool"]
`)
	_, err := Load("")
	if err == nil {
		t.Fatal("flake tool package accepted")
	}
	for _, want := range []string{"plain Nixpkgs attribute path", "flake references", "pinned Nixpkgs source"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("flake migration error missing %q: %v", want, err)
		}
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

func TestGitHubReleaseToolsLoadCanonicallyFromTrustedConfig(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), `
[[tools.github_release]]
name = "roborev"
repo = "kenn-io/roborev"
tag = "v0.63.0"
asset = "roborev_{version}_linux_arm64.tar.gz"
binary = "roborev"

[[tools.github_release]]
name = "kata"
repo = "kenn-io/kata"
tag = "latest"
asset = "kata_{version}_linux_arm64.tar.gz"
binary = "kata"
`)

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{cfg.Tools.GitHubReleases[0].Name, cfg.Tools.GitHubReleases[1].Name}; !slices.Equal(got, []string{"kata", "roborev"}) {
		t.Fatalf("release tools = %v, want canonical name order", got)
	}
	if cfg.Tools.GitHubReleases[0].Tag != "latest" {
		t.Fatalf("latest tag lost: %+v", cfg.Tools.GitHubReleases[0])
	}
	if a, b := ReleaseSpecFingerprint(cfg.Tools.GitHubReleases), ReleaseSpecFingerprint(slices.Clone(cfg.Tools.GitHubReleases)); a == "" || a != b {
		t.Fatalf("release spec fingerprint unstable: %q != %q", a, b)
	}
}

func TestProjectGitHubReleaseToolsRejected(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	mustWrite(t, filepath.Join(project, "coop.toml"), `
[[tools.github_release]]
name = "kata"
repo = "kenn-io/kata"
tag = "latest"
asset = "kata_{version}_linux_arm64.tar.gz"
binary = "kata"
`)

	if _, err := Load(project); err == nil || !strings.Contains(err.Error(), "trusted user") {
		t.Fatalf("project GitHub release tool accepted: %v", err)
	}
}

func TestGitHubReleaseToolValidation(t *testing.T) {
	valid := GitHubReleaseTool{
		Name: "kata", Repo: "kenn-io/kata", Tag: "latest",
		Asset: "kata_{version}_linux_arm64.tar.gz", Binary: "kata",
	}
	tests := map[string]GitHubReleaseTool{
		"empty name":           withReleaseField(valid, "name", ""),
		"uppercase name":       withReleaseField(valid, "name", "Kata"),
		"invalid repo":         withReleaseField(valid, "repo", "https://github.com/kenn-io/kata"),
		"dot repo component":   withReleaseField(valid, "repo", "../kata"),
		"empty tag":            withReleaseField(valid, "tag", ""),
		"unknown placeholder":  withReleaseField(valid, "asset", "kata_{arch}.tar.gz"),
		"unsupported archive":  withReleaseField(valid, "asset", "kata_{version}.zip"),
		"absolute binary":      withReleaseField(valid, "binary", "/kata"),
		"escaping binary":      withReleaseField(valid, "binary", "../kata"),
		"duplicate executable": valid,
	}

	for name, tool := range tests {
		t.Run(name, func(t *testing.T) {
			xdg := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", xdg)
			entries := []GitHubReleaseTool{tool}
			if name == "duplicate executable" {
				entries = append(entries, valid)
			}
			mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), githubReleaseToolTOML(entries))
			if _, err := Load(""); err == nil {
				t.Fatalf("invalid release tool accepted: %+v", tool)
			}
		})
	}
}

func TestGitHubReleaseToolsBounded(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	tools := make([]GitHubReleaseTool, MaxGitHubReleaseTools+1)
	for i := range tools {
		tools[i] = GitHubReleaseTool{
			Name: fmt.Sprintf("tool-%d", i), Repo: "owner/repo", Tag: "latest",
			Asset:  fmt.Sprintf("tool-%d_{version}_linux_arm64.tar.gz", i),
			Binary: fmt.Sprintf("tool-%d", i),
		}
	}
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), githubReleaseToolTOML(tools))
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "32") {
		t.Fatalf("excessive release tools accepted: %v", err)
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

func TestCredentialExposeAcceptsGitCredentialForGitAndGitHubCLI(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), `
[credentials.github]
source = { type = "git-credential", url = "https://github.com/sarcasticbird" }
expose = [
  { type = "git-credential-store" },
  { type = "environment", name = "GH_TOKEN", field = "password" },
]
`)

	if _, err := Load(""); err != nil {
		t.Fatalf("valid multi-exposure credential rejected: %v", err)
	}
}

func TestCredentialExposeValidation(t *testing.T) {
	cases := map[string]string{
		"both inject and expose": `
[credentials.github]
source = { type = "git-credential", url = "https://github.com/acme" }
inject = { type = "git-credential-store" }
expose = [{ type = "environment", name = "GH_TOKEN", field = "password" }]
`,
		"empty inject and expose": `
[credentials.github]
source = { type = "git-credential", url = "https://github.com/acme" }
inject = {}
expose = [{ type = "git-credential-store" }]
`,
		"empty expose": `
[credentials.github]
source = { type = "git-credential", url = "https://github.com/acme" }
expose = []
`,
		"missing exposure": `
[credentials.github]
source = { type = "git-credential", url = "https://github.com/acme" }
`,
		"non https url": `
[credentials.github]
source = { type = "git-credential", url = "http://github.com/acme" }
expose = [{ type = "git-credential-store" }]
`,
		"url userinfo": `
[credentials.github]
source = { type = "git-credential", url = "https://user@github.com/acme" }
expose = [{ type = "git-credential-store" }]
`,
		"url query": `
[credentials.github]
source = { type = "git-credential", url = "https://github.com/acme?owner=other" }
expose = [{ type = "git-credential-store" }]
`,
		"url fragment": `
[credentials.github]
source = { type = "git-credential", url = "https://github.com/acme#other" }
expose = [{ type = "git-credential-store" }]
`,
		"encoded line break": `
[credentials.github]
source = { type = "git-credential", url = "https://github.com/acme%0Ahost=evil.example" }
expose = [{ type = "git-credential-store" }]
`,
		"missing url host": `
[credentials.github]
source = { type = "git-credential", url = "https:///acme" }
expose = [{ type = "git-credential-store" }]
`,
		"git source with unrelated field": `
[credentials.github]
source = { type = "git-credential", url = "https://github.com/acme", path = "~/.secret" }
expose = [{ type = "git-credential-store" }]
`,
		"opaque environment selects field": `
[credentials.token]
source = { type = "command", argv = ["token"] }
expose = [{ type = "environment", name = "TOKEN", field = "password" }]
`,
		"structured environment omits field": `
[credentials.github]
source = { type = "git-credential", url = "https://github.com/acme" }
expose = [{ type = "environment", name = "GH_TOKEN" }]
`,
		"unknown structured field": `
[credentials.github]
source = { type = "git-credential", url = "https://github.com/acme" }
expose = [{ type = "environment", name = "GH_TOKEN", field = "token" }]
`,
		"structured file exposure": `
[credentials.github]
source = { type = "git-credential", url = "https://github.com/acme" }
expose = [{ type = "file", path_env = "TOKEN_FILE" }]
`,
		"git store with command source": `
[credentials.github]
source = { type = "command", argv = ["token"] }
expose = [{ type = "git-credential-store" }]
`,
		"aws exposure with git source": `
[credentials.github]
source = { type = "git-credential", url = "https://github.com/acme" }
expose = [{ type = "aws" }]
`,
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			xdg := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", xdg)
			mustWrite(t, filepath.Join(xdg, "coop", "coop.toml"), content)
			if _, err := Load(""); err == nil {
				t.Fatal("invalid credential exposure accepted")
			}
		})
	}
}

func TestExposuresPreservesLegacyInjectAndReturnsCopy(t *testing.T) {
	legacy := Credential{Inject: CredentialInjection{Type: "environment", Name: "TOKEN"}}
	if got := Exposures(legacy); len(got) != 1 || got[0] != legacy.Inject {
		t.Fatalf("legacy exposure = %+v", got)
	}

	spec := Credential{Expose: []CredentialInjection{
		{Type: "git-credential-store"},
		{Type: "environment", Name: "GH_TOKEN", Field: "password"},
	}}
	got := Exposures(spec)
	got[0].Type = "changed"
	if spec.Expose[0].Type != "git-credential-store" {
		t.Fatal("Exposures returned mutable configuration storage")
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

func TestExamplesLoad(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate config test")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	xdg := t.TempDir()
	project := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	copyFile(t, filepath.Join(root, "examples", "coop.user.toml"), filepath.Join(xdg, "coop", "coop.toml"))
	copyFile(t, filepath.Join(root, "examples", "coop.project.toml"), filepath.Join(project, "coop.toml"))

	cfg, err := Load(project)
	if err != nil {
		t.Fatalf("load public examples: %v", err)
	}
	if cfg.Image.Name == "" || len(cfg.Seeds) == 0 || len(cfg.Tools.Packages) == 0 ||
		len(cfg.Tools.GitHubReleases) == 0 || len(cfg.Credentials) == 0 {
		t.Fatalf("public examples did not exercise image, seed, package, release tool, and credential configuration: %+v", cfg)
	}
	if cfg.Resources.CPUs != 6 {
		t.Fatalf("project resource override = %d, want 6", cfg.Resources.CPUs)
	}
	var hasIfAbsent bool
	for _, seed := range cfg.Seeds {
		if seed.Policy == PolicyIfAbsent {
			hasIfAbsent = true
			break
		}
	}
	if !hasIfAbsent {
		t.Fatal("trusted example does not exercise one-time bootstrap seeds")
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

func copyFile(t *testing.T, src, dest string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, dest, string(data))
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

func githubReleaseToolTOML(tools []GitHubReleaseTool) string {
	var content strings.Builder
	for _, tool := range tools {
		fmt.Fprintf(&content, `[[tools.github_release]]
name = %s
repo = %s
tag = %s
asset = %s
binary = %s
`, strconv.Quote(tool.Name), strconv.Quote(tool.Repo), strconv.Quote(tool.Tag),
			strconv.Quote(tool.Asset), strconv.Quote(tool.Binary))
	}
	return content.String()
}

func withReleaseField(tool GitHubReleaseTool, field, value string) GitHubReleaseTool {
	switch field {
	case "name":
		tool.Name = value
	case "repo":
		tool.Repo = value
	case "tag":
		tool.Tag = value
	case "asset":
		tool.Asset = value
	case "binary":
		tool.Binary = value
	}
	return tool
}

func writeConfigFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProjectScopesAreTrustedOnly(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "coop.toml")
	writeConfigFile(t, global, `
[[projects]]
match = "/tmp/allowed"
include_credentials = ["tok"]

[credentials.tok]
source = { type = "command", argv = ["true"] }
inject = { type = "environment", name = "TOK" }
`)
	projectFile := filepath.Join(dir, "project", "coop.toml")
	writeConfigFile(t, projectFile, `
[[projects]]
match = "/tmp/hostile"
include_credentials = ["tok"]
`)

	var cfg Config
	if err := mergeFile(&cfg, global, true); err != nil {
		t.Fatalf("global merge: %v", err)
	}
	if err := mergeFile(&cfg, projectFile, false); err != nil {
		t.Fatalf("project merge: %v", err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("got %d project scopes, want 1 (project layer must be discarded)", len(cfg.Projects))
	}
	if cfg.Projects[0].Match != "/tmp/allowed" {
		t.Fatalf("got match %q, want /tmp/allowed", cfg.Projects[0].Match)
	}
}

func TestProjectScopeRequiresMatch(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "coop.toml")
	writeConfigFile(t, global, "[[projects]]\ninclude_credentials = []\n")
	var cfg Config
	err := mergeFile(&cfg, global, true)
	if err == nil || !strings.Contains(err.Error(), "match is required") {
		t.Fatalf("got %v, want an error mentioning \"match is required\"", err)
	}
}

func TestProjectScopeMatchMustBeAbsoluteOrHome(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "coop.toml")
	writeConfigFile(t, global, "[[projects]]\nmatch = \"relative/path\"\n")
	var cfg Config
	err := mergeFile(&cfg, global, true)
	if err == nil || !strings.Contains(err.Error(), "absolute or start with ~/") {
		t.Fatalf("got %v, want an error about absolute or ~/ paths", err)
	}
}

func TestProjectScopeValidatesSeedPoliciesInEveryLayer(t *testing.T) {
	for _, trusted := range []bool{true, false} {
		t.Run(fmt.Sprintf("trusted=%t", trusted), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "coop.toml")
			writeConfigFile(t, path, `
[[projects]]
match = "/tmp/work"
seed = [{ src = "~/.codex/rules", policy = "sometimes" }]
`)
			var cfg Config
			err := mergeFile(&cfg, path, trusted)
			if err == nil || !strings.Contains(err.Error(), `unknown policy "sometimes"`) {
				t.Fatalf("scoped seed policy error = %v", err)
			}
		})
	}
}

func TestLoadFoldsMatchingProjectScopes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	matched := filepath.Join(home, "Projects", "sarcasticbird", "coop")
	other := filepath.Join(home, "Projects", "elsewhere", "app")
	for _, d := range []string{matched, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	seedSrc := filepath.Join(home, "settings.toml")
	if err := os.WriteFile(seedSrc, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, filepath.Join(home, ".config", "coop", "coop.toml"), `
include_credentials = ["claude"]

[credentials.claude]
source = { type = "command", argv = ["true"] }
inject = { type = "environment", name = "CLAUDE" }

[credentials.git-sb]
source = { type = "command", argv = ["true"] }
inject = { type = "environment", name = "GH_TOKEN" }

[[projects]]
match = "`+filepath.Join(home, "Projects", "sarcasticbird")+`"
include_credentials = ["git-sb"]
seed = [{ src = "`+seedSrc+`", policy = "always" }]
`)

	cfg, err := Load(matched)
	if err != nil {
		t.Fatalf("load matched: %v", err)
	}
	if !slices.Contains(cfg.IncludeCredentials, "claude") {
		t.Error("account-level grant must apply to a matched project")
	}
	if !slices.Contains(cfg.IncludeCredentials, "git-sb") {
		t.Error("matched scope must contribute its grant")
	}
	if len(cfg.Seeds) != 1 {
		t.Fatalf("got %d seeds, want 1 from the matched scope", len(cfg.Seeds))
	}
	// Scoped seeds must receive the same Dest/Policy defaulting as top-level ones.
	if cfg.Seeds[0].Dest != seedSrc || cfg.Seeds[0].Policy != PolicyAlways {
		t.Errorf("scoped seed missed defaulting: %+v", cfg.Seeds[0])
	}

	cfg, err = Load(other)
	if err != nil {
		t.Fatalf("load unmatched: %v", err)
	}
	if !slices.Contains(cfg.IncludeCredentials, "claude") {
		t.Error("account-level grant must still apply to an unmatched project")
	}
	if slices.Contains(cfg.IncludeCredentials, "git-sb") {
		t.Error("an unmatched project must not receive a scoped grant")
	}
	if len(cfg.Seeds) != 0 {
		t.Fatalf("got %d seeds, want 0 for an unmatched project", len(cfg.Seeds))
	}
}

func TestLoadRejectsUnknownScopedGrant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	unmatched := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(unmatched, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, filepath.Join(home, ".config", "coop", "coop.toml"), `
[[projects]]
match = "`+filepath.Join(home, "Projects")+`"
include_credentials = ["missing"]
`)
	// A typo must surface in every directory, not only inside the project the
	// scope targets.
	_, err := Load(unmatched)
	if err == nil || !strings.Contains(err.Error(), "unknown credential grant") {
		t.Fatalf("got %v, want an error naming the unknown grant", err)
	}
}

func TestLoadOverlappingScopesUnion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	project := filepath.Join(home, "Projects", "org", "repo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, filepath.Join(home, ".config", "coop", "coop.toml"), `
[credentials.broad]
source = { type = "command", argv = ["true"] }
inject = { type = "environment", name = "BROAD" }

[credentials.narrow]
source = { type = "command", argv = ["true"] }
inject = { type = "environment", name = "NARROW" }

[[projects]]
match = "`+filepath.Join(home, "Projects")+`"
include_credentials = ["broad"]

[[projects]]
match = "`+filepath.Join(home, "Projects", "org")+`"
include_credentials = ["narrow"]
`)
	cfg, err := Load(project)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, want := range []string{"broad", "narrow"} {
		if !slices.Contains(cfg.IncludeCredentials, want) {
			t.Errorf("overlapping scopes must union; missing %q", want)
		}
	}
}

func TestAuthNamespaceIsRejectedAsUnknown(t *testing.T) {
	tests := map[string]string{
		"top level": `
[auth.codex]
type = "oauth"
scope = "machine"
`,
		"project scope": `
[[projects]]
match = "/tmp/work"
[projects.auth.codex]
type = "credential"
name = "work"
`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "coop.toml")
			writeConfigFile(t, path, contents)
			var cfg Config
			err := mergeFile(&cfg, path, true)
			if err == nil || !strings.Contains(err.Error(), "unknown keys") {
				t.Fatalf("auth namespace error = %v", err)
			}
		})
	}
}
