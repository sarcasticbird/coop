package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sarcasticbird/coop/image"
)

func TestLoadFallsBackToEmbeddedWhenStateMissing(t *testing.T) {
	lock, warning, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	assertJSONEqual(t, lock, image.EmbeddedCoreLock())
}

func TestInstallAndLoadCompatibleLock(t *testing.T) {
	stateDir := t.TempDir()
	candidate := changedPackageVersion(t, image.EmbeddedCoreLock(), "codex", "99.0.0")
	if err := Install(stateDir, candidate); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "core", image.CoreDefinitionFingerprint(), "manifest.lock")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %o, want 600", info.Mode().Perm())
	}
	lock, warning, err := Load(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	assertJSONEqual(t, lock, candidate)
}

func TestDifferentCoreDefinitionCannotReuseState(t *testing.T) {
	stateDir := t.TempDir()
	other := filepath.Join(stateDir, "core", "different-definition")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	candidate := changedPackageVersion(t, image.EmbeddedCoreLock(), "codex", "99.0.0")
	if err := os.WriteFile(filepath.Join(other, "manifest.lock"), candidate, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, warning, err := Load(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	assertJSONEqual(t, lock, image.EmbeddedCoreLock())
}

func TestMalformedStateFallsBackWithWarning(t *testing.T) {
	stateDir := t.TempDir()
	path := lockPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, warning, err := Load(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warning, "invalid core upgrade state ignored") {
		t.Fatalf("warning = %q", warning)
	}
	assertJSONEqual(t, lock, image.EmbeddedCoreLock())
}

func TestUnreadableStateFallsBackWithWarning(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.MkdirAll(lockPath(stateDir), 0o700); err != nil {
		t.Fatal(err)
	}
	lock, warning, err := Load(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warning, "invalid core upgrade state ignored") ||
		!strings.Contains(warning, "read") {
		t.Fatalf("warning = %q", warning)
	}
	assertJSONEqual(t, lock, image.EmbeddedCoreLock())
}

func TestInstallRejectsWrongManifestOrPackageSet(t *testing.T) {
	t.Run("manifest", func(t *testing.T) {
		candidate := mutateLock(t, image.EmbeddedCoreLock(), func(lock map[string]any) {
			lock["manifest"].(map[string]any)["schema-version"] = "0"
		})
		if err := Install(t.TempDir(), candidate); err == nil || !strings.Contains(err.Error(), "manifest") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("package set", func(t *testing.T) {
		candidate := mutateLock(t, image.EmbeddedCoreLock(), func(lock map[string]any) {
			packages := lock["packages"].([]any)
			lock["packages"] = packages[:len(packages)-1]
		})
		if err := Install(t.TempDir(), candidate); err == nil || !strings.Contains(err.Error(), "packages") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("trailing JSON", func(t *testing.T) {
		candidate := append(append([]byte(nil), image.EmbeddedCoreLock()...), []byte("{}")...)
		if err := Install(t.TempDir(), candidate); err == nil || !strings.Contains(err.Error(), "trailing") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestInstallIsAtomicWhenRenameFails(t *testing.T) {
	stateDir := t.TempDir()
	before := changedPackageVersion(t, image.EmbeddedCoreLock(), "codex", "98.0.0")
	if err := Install(stateDir, before); err != nil {
		t.Fatal(err)
	}
	after := changedPackageVersion(t, image.EmbeddedCoreLock(), "codex", "99.0.0")
	err := installWithRename(stateDir, after, func(_, _ string) error {
		return errors.New("injected rename failure")
	})
	if err == nil || !strings.Contains(err.Error(), "install core lock") {
		t.Fatalf("error = %v", err)
	}
	got, readErr := os.ReadFile(lockPath(stateDir))
	if readErr != nil {
		t.Fatal(readErr)
	}
	assertJSONEqual(t, got, before)
}

func TestChangesAreSortedByInstallID(t *testing.T) {
	before := image.EmbeddedCoreLock()
	after := changedPackageVersion(t, before, "codex", "99.0.0")
	after = changedPackageVersion(t, after, "claude-code", "88.0.0")
	changes, err := Changes(before, after)
	if err != nil {
		t.Fatal(err)
	}
	want := []PackageChange{
		{Name: "claude-code", From: packageVersion(t, before, "claude-code"), To: "88.0.0"},
		{Name: "codex", From: packageVersion(t, before, "codex"), To: "99.0.0"},
	}
	if !slices.Equal(changes, want) {
		t.Fatalf("changes = %+v, want %+v", changes, want)
	}
}

func TestStateDirUsesXDGStateHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	got, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdg, "coop"); got != want {
		t.Fatalf("state dir = %q, want %q", got, want)
	}
}

func changedPackageVersion(t *testing.T, data []byte, installID, version string) []byte {
	t.Helper()
	return mutateLock(t, data, func(lock map[string]any) {
		for _, value := range lock["packages"].([]any) {
			pkg := value.(map[string]any)
			if pkg["install_id"] == installID {
				pkg["version"] = version
				pkg["rev"] = "changed-" + version
				return
			}
		}
		t.Fatalf("package %q not found", installID)
	})
}

func packageVersion(t *testing.T, data []byte, installID string) string {
	t.Helper()
	var lock map[string]any
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	for _, value := range lock["packages"].([]any) {
		pkg := value.(map[string]any)
		if pkg["install_id"] == installID {
			return pkg["version"].(string)
		}
	}
	t.Fatalf("package %q not found", installID)
	return ""
}

func mutateLock(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var lock map[string]any
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	mutate(lock)
	out, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotJSON, wantJSON any
	if err := json.Unmarshal(got, &gotJSON); err != nil {
		t.Fatalf("decode got: %v", err)
	}
	if err := json.Unmarshal(want, &wantJSON); err != nil {
		t.Fatalf("decode want: %v", err)
	}
	gotData, _ := json.Marshal(gotJSON)
	wantData, _ := json.Marshal(wantJSON)
	if !bytes.Equal(gotData, wantData) {
		t.Fatalf("JSON differs:\ngot:  %s\nwant: %s", got, want)
	}
}
