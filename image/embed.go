// Package image embeds the sandbox image definition so `coop rebuild`
// works from the installed binary alone.
package image

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sarcasticbird/coop/internal/config"
)

//go:embed Containerfile coop-user-env zshrc core/.flox/env.json core/.flox/env/manifest.toml core/.flox/env/manifest.lock
var files embed.FS

// NixpkgsRef is the immutable package source shared by the embedded image and
// local extra-package builds.
const NixpkgsRef = "github:flox/nixpkgs/d407951447dcd00442e97087bf374aad70c04cea"

// FloxBaseImage is shared by sandbox builds and the disposable core upgrader.
const FloxBaseImage = "ghcr.io/flox/flox:latest@sha256:04f2254909363974b049b6930a425f179bb1eaa194b1949198a44e4716a25782"

const coreLockPath = "core/.flox/env/manifest.lock"

var embeddedFiles = []string{
	"Containerfile",
	"coop-user-env",
	"zshrc",
	"core/.flox/env.json",
	"core/.flox/env/manifest.toml",
	coreLockPath,
}

var coreDefinitionFiles = []string{
	"core/.flox/env.json",
	"core/.flox/env/manifest.toml",
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

// EmbeddedCoreLock returns a copy of the release-owned fallback lock.
func EmbeddedCoreLock() []byte {
	return embeddedCoreFile(coreLockPath)
}

// EmbeddedCoreEnvJSON returns the release-owned Flox environment metadata.
func EmbeddedCoreEnvJSON() []byte {
	return embeddedCoreFile("core/.flox/env.json")
}

// EmbeddedCoreManifest returns the immutable release-owned Flox manifest.
func EmbeddedCoreManifest() []byte {
	return embeddedCoreFile("core/.flox/env/manifest.toml")
}

func embeddedCoreFile(name string) []byte {
	data, err := files.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("embedded %s: %v", name, err))
	}
	return append([]byte(nil), data...)
}

// CoreDefinitionFingerprint identifies the immutable manifest generation that
// an upgraded core lock must match. It deliberately excludes the mutable lock.
func CoreDefinitionFingerprint() string {
	return fingerprintFSFull(files, coreDefinitionFiles, "core-definition-v1", nil)
}

// Fingerprint identifies the embedded build inputs with the release-owned
// fallback lock.
func Fingerprint() string {
	return FingerprintWithCoreLock(EmbeddedCoreLock())
}

// FingerprintWithCoreLock identifies all build inputs with lock as the chosen
// core lock. Local image tags include it so upgraded cores cannot silently
// reuse an older image under the same configuration.
func FingerprintWithCoreLock(lock []byte) string {
	return fingerprintFSFull(files, embeddedFiles, NixpkgsRef, map[string][]byte{
		coreLockPath: lock,
	})[:12]
}

func fingerprintFS(fsys fs.FS, names []string, packageSource string) string {
	return fingerprintFSFull(fsys, names, packageSource, nil)[:12]
}

func fingerprintFSFull(fsys fs.FS, names []string, packageSource string, overrides map[string][]byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte("nixpkgs-ref\x00" + packageSource + "\x00"))
	for _, name := range names {
		data, ok := overrides[name]
		if !ok {
			var err error
			data, err = fs.ReadFile(fsys, name)
			if err != nil {
				panic(fmt.Sprintf("embedded %s: %v", name, err))
			}
		}
		_, _ = h.Write([]byte(name))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Materialize writes the build context to a temp directory and returns
// its path. Caller removes it.
func Materialize(packages []string, releases []config.ResolvedReleaseTool) (dir string, retErr error) {
	return MaterializeWithCoreLock(packages, releases, EmbeddedCoreLock())
}

// MaterializeWithCoreLock writes the build context using lock as the selected
// core environment lock. Caller removes the returned directory.
func MaterializeWithCoreLock(packages []string, releases []config.ResolvedReleaseTool, lock []byte) (dir string, retErr error) {
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
		data := lock
		if name != coreLockPath {
			data, err = files.ReadFile(name)
			if err != nil {
				return "", fmt.Errorf("embedded %s: %w", name, err)
			}
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
	canonicalReleases := append([]config.ResolvedReleaseTool(nil), releases...)
	sort.Slice(canonicalReleases, func(i, j int) bool {
		return canonicalReleases[i].Name < canonicalReleases[j].Name
	})
	archivePath := filepath.Join(dir, "release-tools.tar.gz")
	archive, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("create release tool archive %q: %w", archivePath, err)
	}
	gz := gzip.NewWriter(archive)
	tw := tar.NewWriter(gz)
	closeArchive := func() error {
		if err := tw.Close(); err != nil {
			return err
		}
		if err := gz.Close(); err != nil {
			return err
		}
		return archive.Close()
	}
	for _, release := range canonicalReleases {
		info, err := os.Stat(release.CachePath)
		if err != nil {
			_ = closeArchive()
			return "", fmt.Errorf("inspect cached release tool %q: %w", release.Name, err)
		}
		if !info.Mode().IsRegular() {
			_ = closeArchive()
			return "", fmt.Errorf("cached release tool %q is not a regular file", release.Name)
		}
		source, err := os.Open(release.CachePath)
		if err != nil {
			_ = closeArchive()
			return "", fmt.Errorf("read cached release tool %q: %w", release.Name, err)
		}
		header := &tar.Header{Name: release.Name, Mode: 0o755, Size: info.Size(), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			_ = source.Close()
			_ = closeArchive()
			return "", fmt.Errorf("archive release tool %q: %w", release.Name, err)
		}
		if _, err := io.Copy(tw, source); err != nil {
			_ = source.Close()
			_ = closeArchive()
			return "", fmt.Errorf("archive release tool %q: %w", release.Name, err)
		}
		if err := source.Close(); err != nil {
			_ = closeArchive()
			return "", fmt.Errorf("close cached release tool %q: %w", release.Name, err)
		}
	}
	if err := closeArchive(); err != nil {
		return "", fmt.Errorf("close release tool archive %q: %w", archivePath, err)
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
