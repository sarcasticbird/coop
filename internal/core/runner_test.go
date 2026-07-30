package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sarcasticbird/coop/image"
)

func TestContainerRunnerUsesPinnedImageAndSafeMount(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "args")
	fake := filepath.Join(binDir, "container")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", marker)
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	if err := runContainer(context.Background(), fake, workdir, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{
		"run",
		"--rm",
		"--cpus", "4",
		"--memory", "8G",
		"--env", "FLOX_DISABLE_METRICS=true",
		"--mount", "type=virtiofs,source=" + workdir + ",target=/work",
		"--workdir", "/work",
		image.FloxBaseImage,
		"flox", "upgrade", "--dir", "/work",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("container argv:\n got: %q\nwant: %q", got, want)
	}
}

func TestContainerRunnerRejectsMountGrammarCharacters(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "bad,dir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	err := runContainer(context.Background(), filepath.Join(t.TempDir(), "missing"), workdir, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "mount") {
		t.Fatalf("error = %v", err)
	}
}

func TestContainerRunnerPropagatesExitFailure(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "container")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := runContainer(context.Background(), fake, t.TempDir(), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "core upgrader") {
		t.Fatalf("error = %v", err)
	}
}

func TestContainerRunnerHonorsContextCancellation(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "container")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := runContainer(ctx, fake, t.TempDir(), io.Discard, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestUpgradeLeavesActiveLockUntouchedWhenRunnerFails(t *testing.T) {
	stateDir := t.TempDir()
	before := changedPackageVersion(t, image.EmbeddedCoreLock(), "codex", "98.0.0")
	if err := Install(stateDir, before); err != nil {
		t.Fatal(err)
	}
	upgrader := Upgrader{
		StateDir: stateDir,
		Run: func(context.Context, string, io.Writer, io.Writer) error {
			return errors.New("resolver unavailable")
		},
	}
	_, err := upgrader.Upgrade(context.Background(), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "resolver unavailable") {
		t.Fatalf("error = %v", err)
	}
	got, readErr := os.ReadFile(lockPath(stateDir))
	if readErr != nil {
		t.Fatal(readErr)
	}
	assertJSONEqual(t, got, before)
}

func TestUpgradeRejectsInvalidCandidateWithoutInstallingIt(t *testing.T) {
	stateDir := t.TempDir()
	before := changedPackageVersion(t, image.EmbeddedCoreLock(), "codex", "98.0.0")
	if err := Install(stateDir, before); err != nil {
		t.Fatal(err)
	}
	upgrader := Upgrader{
		StateDir: stateDir,
		Run: func(_ context.Context, workdir string, _, _ io.Writer) error {
			return os.WriteFile(filepath.Join(workdir, ".flox", "env", "manifest.lock"), []byte("{broken"), 0o600)
		},
	}
	_, err := upgrader.Upgrade(context.Background(), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "validate upgraded core lock") {
		t.Fatalf("error = %v", err)
	}
	got, readErr := os.ReadFile(lockPath(stateDir))
	if readErr != nil {
		t.Fatal(readErr)
	}
	assertJSONEqual(t, got, before)
}

func TestUpgradeReturnsUnchangedWithoutInstalling(t *testing.T) {
	stateDir := t.TempDir()
	upgrader := Upgrader{
		StateDir: stateDir,
		Run: func(_ context.Context, workdir string, _, _ io.Writer) error {
			if _, err := os.Stat(filepath.Join(workdir, ".flox", "env.json")); err != nil {
				return fmt.Errorf("missing Flox environment metadata: %w", err)
			}
			if _, err := os.Stat(filepath.Join(workdir, ".flox", "env", "manifest.toml")); err != nil {
				return fmt.Errorf("missing Flox manifest: %w", err)
			}
			return nil
		},
	}
	result, err := upgrader.Upgrade(context.Background(), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || len(result.Changes) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(lockPath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("unchanged upgrade installed state: %v", err)
	}
}

func TestUpgradeInstallsChangedCandidateAndReportsVersions(t *testing.T) {
	stateDir := t.TempDir()
	before := image.EmbeddedCoreLock()
	after := changedPackageVersion(t, before, "codex", "99.0.0")
	upgrader := Upgrader{
		StateDir: stateDir,
		Run: func(_ context.Context, workdir string, _, _ io.Writer) error {
			return os.WriteFile(filepath.Join(workdir, ".flox", "env", "manifest.lock"), after, 0o600)
		},
	}
	var stderr bytes.Buffer
	result, err := upgrader.Upgrade(context.Background(), io.Discard, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || len(result.Changes) != 1 || result.Changes[0].Name != "codex" {
		t.Fatalf("result = %+v", result)
	}
	got, err := os.ReadFile(lockPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, got, after)
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}
