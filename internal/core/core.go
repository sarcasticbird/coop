// Package core manages Coop's release-owned, machine-wide core environment
// lock. Upgraded state is compatible only with the embedded manifest generation.
package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/sarcasticbird/coop/image"
)

const lockFilename = "manifest.lock"

type lockFile struct {
	Version  int             `json:"lockfile-version"`
	Manifest any             `json:"manifest"`
	Packages []lockedPackage `json:"packages"`
}

type lockedPackage struct {
	InstallID string `json:"install_id"`
	AttrPath  string `json:"attr_path"`
	Version   string `json:"version"`
	System    string `json:"system"`
}

// PackageChange is a user-facing package version change.
type PackageChange struct {
	Name string
	From string
	To   string
}

// StateDir returns Coop's user-local derived state directory.
func StateDir() (string, error) {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "coop"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home for core upgrade state: %w", err)
	}
	return filepath.Join(home, ".local", "state", "coop"), nil
}

// Active loads the lock for the current embedded core definition.
func Active() (lock []byte, warning string, err error) {
	stateDir, err := StateDir()
	if err != nil {
		return nil, "", err
	}
	return Load(stateDir)
}

// Load returns compatible upgraded state or the embedded fallback. Invalid
// derived state is advisory: callers remain usable with the release lock.
func Load(stateDir string) (lock []byte, warning string, err error) {
	fallback := image.EmbeddedCoreLock()
	if err := Validate(fallback); err != nil {
		return nil, "", fmt.Errorf("embedded core lock is invalid: %w", err)
	}
	data, err := os.ReadFile(lockPath(stateDir))
	if os.IsNotExist(err) {
		return fallback, "", nil
	}
	if err != nil {
		return fallback, fmt.Sprintf("invalid core upgrade state ignored: read core lock: %v", err), nil
	}
	if err := Validate(data); err != nil {
		return fallback, fmt.Sprintf("invalid core upgrade state ignored: %v", err), nil
	}
	normalized, err := normalize(data)
	if err != nil {
		return fallback, fmt.Sprintf("invalid core upgrade state ignored: %v", err), nil
	}
	return normalized, "", nil
}

// Validate requires a Flox lock for exactly the embedded manifest and package
// set, targeting Coop's aarch64-linux guest.
func Validate(candidate []byte) error {
	got, err := decodeLock(candidate)
	if err != nil {
		return err
	}
	want, err := decodeLock(image.EmbeddedCoreLock())
	if err != nil {
		return fmt.Errorf("decode embedded core lock: %w", err)
	}
	if got.Version != 1 {
		return fmt.Errorf("lockfile-version = %d, want 1", got.Version)
	}
	if !reflect.DeepEqual(got.Manifest, want.Manifest) {
		return fmt.Errorf("manifest does not match embedded core definition")
	}
	if len(got.Packages) != len(want.Packages) {
		return fmt.Errorf("core lock contains %d packages, want %d", len(got.Packages), len(want.Packages))
	}
	expected := make(map[string]lockedPackage, len(want.Packages))
	for _, pkg := range want.Packages {
		expected[pkg.InstallID] = pkg
	}
	seen := make(map[string]struct{}, len(got.Packages))
	for _, pkg := range got.Packages {
		wantPkg, ok := expected[pkg.InstallID]
		if !ok {
			return fmt.Errorf("core lock contains unknown package %q", pkg.InstallID)
		}
		if _, duplicate := seen[pkg.InstallID]; duplicate {
			return fmt.Errorf("core lock contains duplicate package %q", pkg.InstallID)
		}
		seen[pkg.InstallID] = struct{}{}
		if pkg.AttrPath != wantPkg.AttrPath {
			return fmt.Errorf("core package %q has attr_path %q, want %q", pkg.InstallID, pkg.AttrPath, wantPkg.AttrPath)
		}
		if pkg.System != "aarch64-linux" {
			return fmt.Errorf("core package %q targets %q, want aarch64-linux", pkg.InstallID, pkg.System)
		}
	}
	return nil
}

// Changes returns version-string changes sorted by Flox install ID.
func Changes(before, after []byte) ([]PackageChange, error) {
	if err := Validate(before); err != nil {
		return nil, fmt.Errorf("validate previous core lock: %w", err)
	}
	if err := Validate(after); err != nil {
		return nil, fmt.Errorf("validate candidate core lock: %w", err)
	}
	oldLock, _ := decodeLock(before)
	newLock, _ := decodeLock(after)
	oldVersions := make(map[string]string, len(oldLock.Packages))
	for _, pkg := range oldLock.Packages {
		oldVersions[pkg.InstallID] = pkg.Version
	}
	var changes []PackageChange
	for _, pkg := range newLock.Packages {
		if oldVersions[pkg.InstallID] != pkg.Version {
			changes = append(changes, PackageChange{
				Name: pkg.InstallID,
				From: oldVersions[pkg.InstallID],
				To:   pkg.Version,
			})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Name < changes[j].Name })
	return changes, nil
}

// Install validates and atomically records a compatible upgraded lock.
func Install(stateDir string, candidate []byte) error {
	return installWithRename(stateDir, candidate, os.Rename)
}

func installWithRename(stateDir string, candidate []byte, rename func(string, string) error) error {
	if err := Validate(candidate); err != nil {
		return fmt.Errorf("validate core lock: %w", err)
	}
	normalized, err := normalize(candidate)
	if err != nil {
		return fmt.Errorf("normalize core lock: %w", err)
	}
	destination := lockPath(stateDir)
	dir := filepath.Dir(destination)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create core upgrade state: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".core-lock-*")
	if err != nil {
		return fmt.Errorf("create core lock temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("set core lock mode: %w", err)
	}
	if _, err := tmp.Write(normalized); err != nil {
		return fmt.Errorf("write core lock: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync core lock: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close core lock: %w", err)
	}
	if err := rename(tmpName, destination); err != nil {
		return fmt.Errorf("install core lock: %w", err)
	}
	return nil
}

func lockPath(stateDir string) string {
	return filepath.Join(stateDir, "core", image.CoreDefinitionFingerprint(), lockFilename)
}

func normalize(data []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode core lock: %w", err)
	}
	normalized, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode core lock: %w", err)
	}
	return append(normalized, '\n'), nil
}

func decodeLock(data []byte) (lockFile, error) {
	var lock lockFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&lock); err != nil {
		return lockFile{}, fmt.Errorf("decode core lock: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return lockFile{}, fmt.Errorf("decode core lock trailing data: %w", err)
		}
		return lockFile{}, fmt.Errorf("decode core lock: trailing JSON value")
	}
	return lock, nil
}
