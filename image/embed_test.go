package image

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/BurntSushi/toml"
)

func TestEmbeddedBuildContext(t *testing.T) {
	if len(Fingerprint()) != 12 {
		t.Fatalf("unexpected image fingerprint %q", Fingerprint())
	}
	dir, err := Materialize(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, name := range []string{
		"Containerfile",
		"coop-user-env",
		"zshrc",
		"core/.flox/env.json",
		"core/.flox/env/manifest.toml",
		"core/.flox/env/manifest.lock",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("materialized %s: %v", name, err)
		}
	}
	containerfile, err := files.ReadFile("Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(containerfile), "ARG NIXPKGS_REF="+NixpkgsRef) {
		t.Fatal("Go and Containerfile nixpkgs pins differ")
	}
}

func TestMaterializeWritesCanonicalConfiguredInstallables(t *testing.T) {
	dir, err := Materialize([]string{"shellcheck", "actionlint", "shellcheck"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	data, err := os.ReadFile(filepath.Join(dir, "configured-packages.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := NixpkgsRef + "#actionlint\n" + NixpkgsRef + "#shellcheck\n"
	if string(data) != want {
		t.Fatalf("configured package input = %q, want %q", data, want)
	}
}

func TestMaterializeWrapsBuildContextCreationError(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	_, err := Materialize(nil)
	if err == nil || !strings.Contains(err.Error(), "create image build context") {
		t.Fatalf("materialize error = %v", err)
	}
}

func TestCanonicalPackagesSortsAndDeduplicates(t *testing.T) {
	got := CanonicalPackages([]string{"shellcheck", "actionlint", "shellcheck"})
	want := []string{"actionlint", "shellcheck"}
	if !slices.Equal(got, want) {
		t.Fatalf("canonical packages = %v, want %v", got, want)
	}
}

func TestContainerfileBuildsSeparatedToolLayers(t *testing.T) {
	data, err := files.ReadFile("Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, required := range []string{
		"COPY core/.flox/env.json /opt/coop-core/.flox/env.json",
		"COPY core/.flox/env/manifest.toml /opt/coop-core/.flox/env/manifest.toml",
		"COPY core/.flox/env/manifest.lock /opt/coop-core/.flox/env/manifest.lock",
		"flox activate --dir /opt/coop-core -- true",
		"COPY configured-packages.txt",
		`--profile /opt/coop-tools/profile`,
		`"$installable"`,
		`--impure --priority 2 "$installable" || exit 1;`,
		"COPY coop-user-env /usr/local/bin/coop-user-env",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("Containerfile missing %q", required)
		}
	}
	if strings.Contains(content, "COPY core/.flox /opt/coop-core/.flox") {
		t.Fatal("Apple container drops files from recursive hidden-directory COPY")
	}
	if strings.Contains(content, "#nodejs_22") || strings.Contains(content, "EXTRA_PKGS") {
		t.Fatal("Containerfile still exposes the old runtime or shell-expanded package input")
	}
}

func TestUserEnvironmentOrdersCoreConfiguredAndFallbackTools(t *testing.T) {
	root := t.TempDir()
	coreRoot := filepath.Join(root, "core")
	coreEnv := filepath.Join(coreRoot, ".flox", "run", "core")
	coreBin := filepath.Join(coreEnv, "bin")
	coreSbin := filepath.Join(coreEnv, "sbin")
	toolsBin := filepath.Join(root, "tools", "bin")
	fallbackBin := filepath.Join(root, "fallback", "bin")
	for _, dir := range []string{coreBin, coreSbin, toolsBin, fallbackBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		filepath.Join(coreSbin, "shared-tool"),
		filepath.Join(toolsBin, "shared-tool"),
		filepath.Join(toolsBin, "configured-tool"),
		filepath.Join(fallbackBin, "configured-tool"),
	} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command("bash", "coop-user-env", "--", "/bin/sh", "-c", "command -v shared-tool; command -v configured-tool")
	cmd.Env = []string{
		"COOP_CORE=" + coreRoot,
		"FLOX_ENV=" + coreEnv,
		"COOP_TOOLS_PROFILE=" + filepath.Dir(toolsBin),
		"PATH=" + strings.Join([]string{coreBin, coreSbin, fallbackBin, "/usr/bin", "/bin"}, string(os.PathListSeparator)),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run wrapper: %v\n%s", err, out)
	}
	want := filepath.Join(coreSbin, "shared-tool") + "\n" + filepath.Join(toolsBin, "configured-tool")
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("tool resolution = %q, want %q", got, want)
	}
}

func TestUserEnvironmentDropsUnsafePathEntries(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	projectBin := filepath.Join(project, "bin")
	toolsProfile := filepath.Join(root, "tools")
	toolsBin := filepath.Join(toolsProfile, "bin")
	for _, dir := range []string{projectBin, toolsBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		filepath.Join(project, "repo-tool"),
		filepath.Join(projectBin, "repo-bin-tool"),
		filepath.Join(toolsBin, "configured-tool"),
	} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	wrapper, err := filepath.Abs("coop-user-env")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", wrapper, "--", "/bin/sh", "-c", `printf '%s\n' "$PATH"; command -v configured-tool; ! command -v repo-tool; ! command -v repo-bin-tool`)
	cmd.Dir = project
	cmd.Env = []string{
		"COOP_CORE=" + filepath.Join(root, "core"),
		"COOP_TOOLS_PROFILE=" + toolsProfile,
		"FLOX_ENV=" + filepath.Join(root, "core-env"),
		"PATH=:.:bin",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run wrapper: %v\n%s", err, out)
	}
	want := toolsBin + "\n" + filepath.Join(toolsBin, "configured-tool")
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("empty PATH handling = %q, want %q", got, want)
	}
}

func TestUserEnvironmentWrapperPreservesArgv(t *testing.T) {
	data, err := files.ReadFile("coop-user-env")
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, "eval") || strings.Contains(content, "flox activate -c") {
		t.Fatal("user wrapper evaluates command text")
	}
	for _, required := range []string{`$tools_profile/bin`, `exec flox activate --dir "$project_flox" -- "$@"`, `exec "$@"`} {
		if !strings.Contains(content, required) {
			t.Errorf("user wrapper missing %q", required)
		}
	}
}

func TestZshrcDoesNotReplaceLayeredPath(t *testing.T) {
	data, err := files.ReadFile("zshrc")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "export PATH=") {
		t.Fatal("zshrc replaces the core/configured/project PATH assembled at entry")
	}
}

func TestCoreEnvironmentPackageContract(t *testing.T) {
	want := []string{
		"bashInteractive", "cacert", "claude-code", "codex", "coreutils", "curl",
		"diffutils", "file", "findutils", "gawk", "gh", "git", "gnugrep", "gnused",
		"gnutar", "gzip", "jq", "less", "opencode", "openssh", "patch", "procps",
		"ripgrep", "tmux", "unzip", "zsh",
	}
	if got := CorePackages(); !slices.Equal(got, want) {
		t.Fatalf("core packages = %v, want %v", got, want)
	}
	for _, excluded := range []string{"go", "nodejs", "nodejs_22", "python", "rustc"} {
		if slices.Contains(CorePackages(), excluded) {
			t.Fatalf("application runtime %q is part of the core contract", excluded)
		}
	}
}

func TestCorePackagesMatchManifest(t *testing.T) {
	data, err := files.ReadFile("core/.flox/env/manifest.toml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Install map[string]struct {
			PkgPath string `toml:"pkg-path"`
		} `toml:"install"`
	}
	if err := toml.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(manifest.Install))
	for _, pkg := range manifest.Install {
		got = append(got, pkg.PkgPath)
	}
	sort.Strings(got)
	if !slices.Equal(got, CorePackages()) {
		t.Fatalf("manifest packages = %v, core package contract = %v", got, CorePackages())
	}
}

func TestCoreLockTargetsAarch64LinuxOnly(t *testing.T) {
	data, err := files.ReadFile("core/.flox/env/manifest.lock")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Packages []struct {
			AttrPath string `json:"attr_path"`
			Priority int    `json:"priority"`
			System   string `json:"system"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	if len(lock.Packages) != len(CorePackages()) {
		t.Fatalf("locked packages = %d, want %d", len(lock.Packages), len(CorePackages()))
	}
	priorities := make(map[string]int, len(lock.Packages))
	for _, pkg := range lock.Packages {
		if pkg.System != "aarch64-linux" {
			t.Fatalf("package %s locked for %s", pkg.AttrPath, pkg.System)
		}
		if !slices.Contains(CorePackages(), pkg.AttrPath) {
			t.Fatalf("unexpected locked package %q", pkg.AttrPath)
		}
		priorities[pkg.AttrPath] = pkg.Priority
	}
	if priorities["coreutils"] >= priorities["procps"] {
		t.Fatalf("coreutils priority %d must win bin/kill over procps priority %d", priorities["coreutils"], priorities["procps"])
	}
}

func TestFingerprintIncludesCoreLockAndPackageSource(t *testing.T) {
	base := fstest.MapFS{
		"Containerfile":                {Data: []byte("container")},
		"core/.flox/env/manifest.lock": {Data: []byte("lock-v1")},
	}
	names := []string{"Containerfile", "core/.flox/env/manifest.lock"}
	original := fingerprintFS(base, names, "source-v1")
	base["core/.flox/env/manifest.lock"] = &fstest.MapFile{Data: []byte("lock-v2")}
	if changedLock := fingerprintFS(base, names, "source-v1"); changedLock == original {
		t.Fatal("core lock change did not change image fingerprint")
	}
	base["core/.flox/env/manifest.lock"] = &fstest.MapFile{Data: []byte("lock-v1")}
	if changedSource := fingerprintFS(base, names, "source-v2"); changedSource == original {
		t.Fatal("configured package source change did not change image fingerprint")
	}
}
