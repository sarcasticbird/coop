package image

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
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

func TestCoreLockTargetsAarch64LinuxOnly(t *testing.T) {
	data, err := files.ReadFile("core/.flox/env/manifest.lock")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Packages []struct {
			AttrPath string `json:"attr_path"`
			System   string `json:"system"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	if len(lock.Packages) != len(CorePackages()) {
		t.Fatalf("locked packages = %d, want %d", len(lock.Packages), len(CorePackages()))
	}
	for _, pkg := range lock.Packages {
		if pkg.System != "aarch64-linux" {
			t.Fatalf("package %s locked for %s", pkg.AttrPath, pkg.System)
		}
		if !slices.Contains(CorePackages(), pkg.AttrPath) {
			t.Fatalf("unexpected locked package %q", pkg.AttrPath)
		}
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
