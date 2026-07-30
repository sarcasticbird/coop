package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sarcasticbird/coop/image"
	"github.com/sarcasticbird/coop/internal/runtime"
)

const upgradeMount = "/work"

// ContainerRunner upgrades the Flox environment rooted at workdir.
type ContainerRunner func(ctx context.Context, workdir string, stdout, stderr io.Writer) error

// UpgradeResult reports whether desired machine-wide core state changed.
type UpgradeResult struct {
	Changed bool
	Changes []PackageChange
}

// Upgrader resolves and installs a candidate core lock.
type Upgrader struct {
	StateDir string
	Run      ContainerRunner
}

// Upgrade resolves all packages in Coop's core manifest. It never builds an
// image or touches a project container.
func (u Upgrader) Upgrade(ctx context.Context, stdout, stderr io.Writer) (UpgradeResult, error) {
	stateDir := u.StateDir
	if stateDir == "" {
		var err error
		stateDir, err = StateDir()
		if err != nil {
			return UpgradeResult{}, err
		}
	}
	before, warning, err := Load(stateDir)
	if err != nil {
		return UpgradeResult{}, err
	}
	if warning != "" {
		_, _ = fmt.Fprintf(stderr, "coop: warning: %s\n", warning)
	}

	workdir, err := os.MkdirTemp("", "coop-core-upgrade-")
	if err != nil {
		return UpgradeResult{}, fmt.Errorf("create core upgrade workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(workdir) }()
	floxDir := filepath.Join(workdir, ".flox")
	envDir := filepath.Join(floxDir, "env")
	if err := os.MkdirAll(envDir, 0o700); err != nil {
		return UpgradeResult{}, fmt.Errorf("create core upgrade environment: %w", err)
	}
	for _, file := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{path: filepath.Join(floxDir, "env.json"), data: image.EmbeddedCoreEnvJSON(), mode: 0o644},
		{path: filepath.Join(envDir, "manifest.toml"), data: image.EmbeddedCoreManifest(), mode: 0o644},
		{path: filepath.Join(envDir, lockFilename), data: before, mode: 0o600},
	} {
		if err := os.WriteFile(file.path, file.data, file.mode); err != nil {
			return UpgradeResult{}, fmt.Errorf("write core upgrade %s: %w", filepath.Base(file.path), err)
		}
	}

	run := u.Run
	if run == nil {
		run = DefaultContainerRunner
	}
	if err := run(ctx, workdir, stdout, stderr); err != nil {
		return UpgradeResult{}, fmt.Errorf("resolve core upgrade: %w", err)
	}
	candidate, err := os.ReadFile(filepath.Join(envDir, lockFilename))
	if err != nil {
		return UpgradeResult{}, fmt.Errorf("read upgraded core lock: %w", err)
	}
	if err := Validate(candidate); err != nil {
		return UpgradeResult{}, fmt.Errorf("validate upgraded core lock: %w", err)
	}
	normalizedBefore, err := normalize(before)
	if err != nil {
		return UpgradeResult{}, fmt.Errorf("normalize active core lock: %w", err)
	}
	normalizedCandidate, err := normalize(candidate)
	if err != nil {
		return UpgradeResult{}, fmt.Errorf("normalize upgraded core lock: %w", err)
	}
	if bytes.Equal(normalizedBefore, normalizedCandidate) {
		return UpgradeResult{}, nil
	}
	changes, err := Changes(normalizedBefore, normalizedCandidate)
	if err != nil {
		return UpgradeResult{}, err
	}
	if err := Install(stateDir, normalizedCandidate); err != nil {
		return UpgradeResult{}, err
	}
	return UpgradeResult{Changed: true, Changes: changes}, nil
}

// DefaultContainerRunner invokes Flox inside a disposable digest-pinned Apple
// container. Flox is never required on the host.
func DefaultContainerRunner(ctx context.Context, workdir string, stdout, stderr io.Writer) error {
	return runContainer(ctx, "container", workdir, stdout, stderr)
}

func runContainer(ctx context.Context, bin, workdir string, stdout, stderr io.Writer) error {
	if err := runtime.ValidateMountField(workdir); err != nil {
		return fmt.Errorf("validate core upgrade mount: %w", err)
	}
	if err := runtime.ValidateMountField(upgradeMount); err != nil {
		return fmt.Errorf("validate core upgrade mount target: %w", err)
	}
	args := []string{
		"run", "--rm",
		"--cpus", "4",
		"--memory", "8G",
		"--env", "FLOX_DISABLE_METRICS=true",
		"--mount", "type=virtiofs,source=" + workdir + ",target=" + upgradeMount,
		"--workdir", upgradeMount,
		image.FloxBaseImage,
		"flox", "upgrade", "--dir", upgradeMount,
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return fmt.Errorf("run core upgrader container: %w", err)
	}
	return nil
}
