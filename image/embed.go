// Package image embeds the sandbox image definition so `coop rebuild`
// works from the installed binary alone.
package image

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed Containerfile coop-user-env zshrc core/.flox/env.json core/.flox/env/manifest.toml core/.flox/env/manifest.lock
var files embed.FS

// NixpkgsRef is the immutable package source shared by the embedded image and
// local extra-package builds.
const NixpkgsRef = "github:flox/nixpkgs/d407951447dcd00442e97087bf374aad70c04cea"

var embeddedFiles = []string{
	"Containerfile",
	"coop-user-env",
	"zshrc",
	"core/.flox/env.json",
	"core/.flox/env/manifest.toml",
	"core/.flox/env/manifest.lock",
}

var corePackages = []string{
	"bashInteractive", "cacert", "claude-code", "codex", "coreutils", "curl",
	"diffutils", "file", "findutils", "gawk", "gh", "git", "gnugrep", "gnused",
	"gnutar", "gzip", "jq", "less", "opencode", "openssh", "patch", "procps",
	"ripgrep", "tmux", "unzip", "zsh",
}

// CorePackages returns the sorted package paths promised by Coop's locked core
// environment. The returned slice is safe for callers to modify.
func CorePackages() []string { return append([]string(nil), corePackages...) }

// Fingerprint identifies the embedded build inputs. Local image tags include
// it so a changed Containerfile or shell setup cannot silently reuse an older
// image under the same configuration.
func Fingerprint() string {
	return fingerprintFS(files, embeddedFiles, NixpkgsRef)
}

func fingerprintFS(fsys fs.FS, names []string, packageSource string) string {
	h := sha256.New()
	_, _ = h.Write([]byte("nixpkgs-ref\x00" + packageSource + "\x00"))
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			panic(fmt.Sprintf("embedded %s: %v", name, err))
		}
		_, _ = h.Write([]byte(name))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// Materialize writes the build context to a temp directory and returns
// its path. Caller removes it.
func Materialize(packages []string) (dir string, retErr error) {
	dir, err := os.MkdirTemp("", "coop-image-")
	if err != nil {
		return "", fmt.Errorf("create image build context: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, name := range embeddedFiles {
		data, err := files.ReadFile(name)
		if err != nil {
			return "", fmt.Errorf("embedded %s: %w", name, err)
		}
		path := filepath.Join(dir, name)
		parent := filepath.Dir(path)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return "", fmt.Errorf("create image build directory %q: %w", parent, err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return "", fmt.Errorf("write embedded build file %q: %w", path, err)
		}
	}
	var installables strings.Builder
	for _, pkg := range CanonicalPackages(packages) {
		fmt.Fprintf(&installables, "%s#%s\n", NixpkgsRef, pkg)
	}
	packagesPath := filepath.Join(dir, "configured-packages.txt")
	if err := os.WriteFile(packagesPath, []byte(installables.String()), 0o644); err != nil {
		return "", fmt.Errorf("write configured package list %q: %w", packagesPath, err)
	}
	return dir, nil
}

// CanonicalPackages returns a sorted, deduplicated package list. Image
// identity and build materialization share this boundary so they cannot drift.
func CanonicalPackages(packages []string) []string {
	unique := make(map[string]struct{}, len(packages))
	for _, pkg := range packages {
		unique[pkg] = struct{}{}
	}
	canonical := make([]string, 0, len(unique))
	for pkg := range unique {
		canonical = append(canonical, pkg)
	}
	sort.Strings(canonical)
	return canonical
}
