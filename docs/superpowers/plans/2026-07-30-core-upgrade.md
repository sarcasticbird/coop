# Coop Core Upgrade Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a self-contained, machine-wide `coop upgrade` command that advances Coop's complete locked core without rebuilding, stopping, or recreating any project coop.

**Architecture:** Coop continues shipping an embedded core manifest and fallback lock. A compatible upgraded lock is stored atomically under the user's XDG state directory, keyed by the embedded core-definition fingerprint; image materialization and image identity both consume that active lock. `coop upgrade` runs Flox in a short-lived, digest-pinned Apple container against a host temporary directory, validates the candidate lock, installs it only after success, and tells the user to rebuild stale coops explicitly.

**Tech Stack:** Go, Cobra, Apple Container CLI, Flox manifest locks, XDG state storage.

## Global Constraints

- `coop upgrade` upgrades the complete Coop-owned core manifest in version one.
- Targeted package upgrades are out of scope for version one and reserved for version two.
- `coop upgrade` must not rebuild an image or stop, remove, start, or recreate any project coop.
- Existing project Flox environments, configured Nix packages, and configured GitHub release tools are not upgrade inputs.
- The host must not need Flox installed; the only host runtime dependency is Coop's existing Apple Container CLI.
- A failed upgrade must leave the previously active lock untouched.
- A changed lock must change the desired image name so existing status and entry paths naturally require `coop rebuild`.
- State is machine-wide and user-local under `${XDG_STATE_HOME:-$HOME/.local/state}/coop/core/`.
- State for one embedded core manifest must never be consumed by an incompatible future embedded manifest.
- All project commands run through `flox activate --`.
- Go changes must pass `go fmt ./...`, `go vet ./...`, `go test ./...`, and `go test -race ./...`.
- Do not commit without explicit user approval; the commit steps below are approval checkpoints.

---

## File Structure

- `image/embed.go`: expose immutable core assets, compute fingerprints with an explicit core lock, and materialize an explicit core lock.
- `image/embed_test.go`: prove the chosen lock affects both the build context and image fingerprint without changing existing wrapper behavior.
- `internal/core/core.go`: load, validate, compare, and atomically install the machine-wide active core lock.
- `internal/core/core_test.go`: cover missing, compatible, incompatible, malformed, unchanged, changed, and failed-upgrade state.
- `internal/core/runner.go`: run `flox upgrade` in a disposable digest-pinned Apple container.
- `internal/core/runner_test.go`: verify the exact safe argv, bind mount, cancellation, and subprocess failure behavior.
- `internal/session/session.go`: carry the active lock in each session and use it for desired image identity.
- `internal/session/session_test.go`: prove an upgraded lock makes an existing image/container stale without mutating it.
- `internal/doctor/doctor.go`: inspect the image name derived from the active lock.
- `internal/doctor/doctor_test.go`: prove doctor points upgraded users toward `coop rebuild`.
- `cmd/coop/main.go`: register the global `upgrade` command and use the active lock during rebuild.
- `cmd/coop/main_test.go`: cover command output, failure atomicity delegation, global invocation, credential rejection, and the absence of rebuild/runtime lifecycle calls.
- `README.md`: document `upgrade` versus `rebuild` and the explicit adoption workflow.
- `docs/runtime.md`: document stale-coop recovery after a core upgrade.

---

### Task 1: Make the core lock an explicit image input

**Files:**
- Modify: `image/embed.go`
- Modify: `image/embed_test.go`

**Interfaces:**
- Produces: `EmbeddedCoreLock() []byte`
- Produces: `CoreDefinitionFingerprint() string`
- Produces: `FingerprintWithCoreLock(lock []byte) string`
- Produces: `MaterializeWithCoreLock(packages []string, releases []config.ResolvedReleaseTool, lock []byte) (string, error)`
- Preserves: `Fingerprint()` and `Materialize(...)` as embedded-lock wrappers

- [x] **Step 1: Write failing tests for explicit lock identity and materialization**

Add tests that use two valid lock byte slices and assert:

```go
func TestFingerprintWithCoreLockChangesOnlyForChosenLock(t *testing.T) {
	embedded := EmbeddedCoreLock()
	changed := append([]byte(nil), embedded...)
	changed = append(changed, '\n')
	if FingerprintWithCoreLock(embedded) == FingerprintWithCoreLock(changed) {
		t.Fatal("explicit core lock did not affect image fingerprint")
	}
}

func TestMaterializeWithCoreLockWritesChosenLock(t *testing.T) {
	chosen := []byte(`{"lockfile-version":1}`)
	dir, err := MaterializeWithCoreLock(nil, nil, chosen)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	got, err := os.ReadFile(filepath.Join(dir, "core/.flox/env/manifest.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, chosen) {
		t.Fatalf("materialized lock = %q, want %q", got, chosen)
	}
}
```

- [x] **Step 2: Run the focused tests and confirm they fail**

Run:

```bash
flox activate -- go test ./image -run 'Test(FingerprintWithCoreLock|MaterializeWithCoreLock)' -count=1
```

Expected: compilation fails because the explicit-lock functions do not exist.

- [x] **Step 3: Implement explicit-lock wrappers**

Refactor the embedded file loop so `core/.flox/env/manifest.lock` is fingerprinted and written from the passed `lock` bytes. Keep compatibility wrappers:

```go
func EmbeddedCoreLock() []byte {
	data, err := files.ReadFile(coreLockPath)
	if err != nil {
		panic(fmt.Sprintf("embedded %s: %v", coreLockPath, err))
	}
	return append([]byte(nil), data...)
}

func Fingerprint() string {
	return FingerprintWithCoreLock(EmbeddedCoreLock())
}

func Materialize(packages []string, releases []config.ResolvedReleaseTool) (string, error) {
	return MaterializeWithCoreLock(packages, releases, EmbeddedCoreLock())
}
```

`CoreDefinitionFingerprint` must hash `core/.flox/env.json` and `core/.flox/env/manifest.toml`, excluding the mutable lock.

- [x] **Step 4: Run image tests**

Run:

```bash
flox activate -- go test ./image -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit only after approval**

```bash
git add image/embed.go image/embed_test.go
git commit -m "refactor: make core lock an image input"
```

---

### Task 2: Add compatible machine-wide active-lock state

**Files:**
- Create: `internal/core/core.go`
- Create: `internal/core/core_test.go`

**Interfaces:**
- Consumes: `image.EmbeddedCoreLock()` and `image.CoreDefinitionFingerprint()`
- Produces: `Active() (lock []byte, warning string, err error)`
- Produces: `Load(stateDir string) (lock []byte, warning string, err error)`
- Produces: `Install(stateDir string, candidate []byte) error`
- Produces: `Validate(candidate []byte) error`
- Produces: `Changes(before, after []byte) ([]PackageChange, error)`
- Produces: `StateDir() (string, error)`
- Produces:

```go
type PackageChange struct {
	Name string
	From string
	To   string
}
```

- [x] **Step 1: Write failing state and validation tests**

Cover these cases:

```go
func TestLoadFallsBackToEmbeddedWhenStateMissing(t *testing.T)
func TestInstallAndLoadCompatibleLock(t *testing.T)
func TestDifferentCoreDefinitionCannotReuseState(t *testing.T)
func TestMalformedStateFallsBackWithWarning(t *testing.T)
func TestInstallRejectsWrongManifestOrPackageSet(t *testing.T)
func TestInstallIsAtomicWhenTemporaryWriteFails(t *testing.T)
func TestChangesAreSortedByInstallID(t *testing.T)
```

Tests must set `XDG_STATE_HOME` to `t.TempDir()` and verify the compatible path is:

```go
filepath.Join(stateDir, "core", image.CoreDefinitionFingerprint(), "manifest.lock")
```

Validation must require:

- JSON `lockfile-version` equal to `1`;
- candidate `manifest` deeply equal to the embedded lock's manifest;
- exactly one candidate package for every embedded `install_id`;
- no unknown or duplicate install IDs;
- every package system equal to `aarch64-linux`.

- [x] **Step 2: Run the package tests and confirm they fail**

Run:

```bash
flox activate -- go test ./internal/core -count=1
```

Expected: compilation fails because `internal/core` has not been implemented.

- [x] **Step 3: Implement normalized, compatible state**

Use a manifest-fingerprint directory rather than a sidecar metadata file. Normalize accepted upgraded JSON with `json.MarshalIndent` plus a trailing newline before comparing or saving. Missing state returns the byte-for-byte embedded lock so installing this feature alone does not invalidate existing images. Malformed state returns the embedded lock plus a warning containing:

```text
invalid core upgrade state ignored
```

Atomically install with a mode-`0600` temporary file in the destination directory, `Sync`, `Close`, and `Rename`. Create state directories with mode `0700`.

- [x] **Step 4: Run state tests**

Run:

```bash
flox activate -- go test ./internal/core -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit only after approval**

```bash
git add internal/core/core.go internal/core/core_test.go
git commit -m "feat: persist an upgraded core lock"
```

---

### Task 3: Resolve upgrades in a disposable Apple container

**Files:**
- Create: `internal/core/runner.go`
- Create: `internal/core/runner_test.go`
- Modify: `image/embed.go`
- Modify: `image/embed_test.go`

**Interfaces:**
- Produces: `image.FloxBaseImage`
- Produces:

```go
type ContainerRunner func(
	ctx context.Context,
	workdir string,
	stdout io.Writer,
	stderr io.Writer,
) error

type UpgradeResult struct {
	Changed bool
	Changes []PackageChange
}

type Upgrader struct {
	StateDir string
	Run      ContainerRunner
}

func (u Upgrader) Upgrade(
	ctx context.Context,
	stdout io.Writer,
	stderr io.Writer,
) (UpgradeResult, error)
```

- [x] **Step 1: Write failing runner tests**

Use a fake `container` executable and assert the command is equivalent to:

```text
container run --rm
  --env FLOX_DISABLE_METRICS=true
  --mount type=virtiofs,source=<validated-temp-dir>,target=/work
  --workdir /work
  ghcr.io/flox/flox:latest@sha256:04f2254909363974b049b6930a425f179bb1eaa194b1949198a44e4716a25782
  flox upgrade --dir /work
```

Cover:

```go
func TestContainerRunnerUsesPinnedImageAndSafeMount(t *testing.T)
func TestContainerRunnerRejectsMountGrammarCharacters(t *testing.T)
func TestContainerRunnerPropagatesExitFailure(t *testing.T)
func TestContainerRunnerHonorsContextCancellation(t *testing.T)
func TestUpgradeLeavesActiveLockUntouchedWhenRunnerFails(t *testing.T)
func TestUpgradeRejectsInvalidCandidateWithoutInstallingIt(t *testing.T)
func TestUpgradeReturnsUnchangedWithoutReinstalling(t *testing.T)
func TestUpgradeInstallsChangedCandidateAndReportsVersions(t *testing.T)
```

- [x] **Step 2: Run runner tests and confirm they fail**

Run:

```bash
flox activate -- go test ./internal/core -run 'Test(ContainerRunner|Upgrade)' -count=1
```

Expected: compilation fails because the runner and updater do not exist.

- [x] **Step 3: Implement the self-contained runner and updater**

The updater must:

1. Load the active lock.
2. Create a `coop-core-upgrade-*` temporary directory.
3. Write `.flox/env.json`, `.flox/env/manifest.toml`, and `.flox/env/manifest.lock`.
4. Invoke the injected runner.
5. Read and validate the candidate lock.
6. Compare normalized locks and package versions.
7. Return unchanged without writing state when semantically identical.
8. Atomically install only a valid changed lock.
9. Remove the temporary directory on every return path.

Use `runtime.ValidateMountField` before constructing the comma-delimited `--mount` argument. Set subprocess stdin to nil and attach the passed stdout and stderr.

- [x] **Step 4: Keep the pinned resolver image aligned with the build image**

Export:

```go
const FloxBaseImage = "ghcr.io/flox/flox:latest@sha256:04f2254909363974b049b6930a425f179bb1eaa194b1949198a44e4716a25782"
```

Add an image test asserting `Containerfile` contains `FROM ` followed by exactly `FloxBaseImage`.

- [x] **Step 5: Run core and image tests**

Run:

```bash
flox activate -- go test ./internal/core ./image -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit only after approval**

```bash
git add internal/core/runner.go internal/core/runner_test.go image/embed.go image/embed_test.go
git commit -m "feat: resolve core upgrades in a container"
```

---

### Task 4: Derive session and doctor staleness from the active lock

**Files:**
- Modify: `internal/session/session.go`
- Modify: `internal/session/session_test.go`
- Modify: `internal/doctor/doctor.go`
- Modify: `internal/doctor/doctor_test.go`

**Interfaces:**
- Consumes: `core.Active()`
- Consumes: `image.FingerprintWithCoreLock(lock)`
- Produces: `Session.CoreLock []byte`
- Produces: `Session.DesiredImageName() string`
- Produces: `EffectiveImageNameWithCoreLock(cfg config.Config, lock []byte) string`
- Changes: `doctor.Run(..., coreLock []byte) []Check`

- [x] **Step 1: Write failing stale-image tests**

Add:

```go
func TestDesiredImageChangesWithCoreLock(t *testing.T)
func TestImageStatusMarksUpgradedCoreAsRebuildRequiredWithoutMutation(t *testing.T)
func TestUpgradedCoreRefusesContainerReplacementUntilImageExists(t *testing.T)
func TestNewLoadsActiveCoreLockAndSurfacesInvalidStateWarning(t *testing.T)
func TestDoctorUsesActiveCoreLockForImageCheck(t *testing.T)
```

The mutation test must assert `Stopped` and `Removed` remain empty when the upgraded desired image is missing.

- [x] **Step 2: Run focused tests and confirm they fail**

Run:

```bash
flox activate -- go test ./internal/session ./internal/doctor -run 'Test(DesiredImage|ImageStatusMarksUpgraded|UpgradedCore|NewLoadsActive|DoctorUsesActive)' -count=1
```

Expected: compilation fails because sessions do not carry an active lock.

- [x] **Step 3: Wire the active lock through image identity**

`session.New` calls `core.Active`, appends a non-empty warning to `cfg.Warnings`, and stores the lock. Every session path uses `s.DesiredImageName()` rather than recomputing from the embedded lock:

```go
func (s *Session) DesiredImageName() string {
	return EffectiveImageNameWithCoreLock(s.Cfg, s.CoreLock)
}
```

An empty `Session.CoreLock` in existing unit fixtures must mean the embedded lock. Preserve `EffectiveImageName(cfg)` as an embedded-lock wrapper for callers and tests that do not model state.

Pass the active lock into doctor so its sandbox image check agrees with rebuild and session entry.

- [x] **Step 4: Run session and doctor tests**

Run:

```bash
flox activate -- go test ./internal/session ./internal/doctor -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit only after approval**

```bash
git add internal/session/session.go internal/session/session_test.go internal/doctor/doctor.go internal/doctor/doctor_test.go
git commit -m "feat: derive stale coops from the active core"
```

---

### Task 5: Add `coop upgrade` without project lifecycle side effects

**Files:**
- Modify: `cmd/coop/main.go`
- Modify: `cmd/coop/main_test.go`

**Interfaces:**
- Consumes: `core.Upgrader.Upgrade(...)`
- Consumes: `image.MaterializeWithCoreLock(...)`
- Consumes: `Session.DesiredImageName()`
- Produces: Cobra subcommand `upgrade`

- [x] **Step 1: Write failing command tests**

Cover:

```go
func TestUpgradeChangedLockPrintsVersionsAndRebuildGuidance(t *testing.T)
func TestUpgradeChangedRevisionWithoutVersionChangeStillMarksCoopsStale(t *testing.T)
func TestUpgradeUnchangedReportsCurrentWithoutStaleWarning(t *testing.T)
func TestUpgradeFailureDoesNotInvokeAnyProjectLifecycleOperation(t *testing.T)
func TestUpgradeRunsOutsideAProject(t *testing.T)
func TestRebuildMaterializesAndNamesImageFromActiveCoreLock(t *testing.T)
```

Also add `upgrade` to `TestCredentialsFlagRejectedByNonEntryCommands`.

Inject the upgrader in command tests:

```go
var upgradeCore = func(
	ctx context.Context,
	stdout io.Writer,
	stderr io.Writer,
) (core.UpgradeResult, error) {
	stateDir, err := core.StateDir()
	if err != nil {
		return core.UpgradeResult{}, err
	}
	return (core.Upgrader{StateDir: stateDir}).Upgrade(ctx, stdout, stderr)
}
```

- [x] **Step 2: Run focused command tests and confirm they fail**

Run:

```bash
flox activate -- go test ./cmd/coop -run 'Test(Upgrade|RebuildMaterializesAndNamesImageFromActive)' -count=1
```

Expected: FAIL because `upgrade` is not registered and rebuild still uses the embedded lock.

- [x] **Step 3: Implement the command**

Register:

```go
&cobra.Command{
	Use:   "upgrade",
	Args:  cobra.NoArgs,
	Short: "Upgrade Coop's machine-wide locked core",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := rejectCredentials(cmd); err != nil {
			return err
		}
		result, err := upgradeCore(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		if err != nil {
			return fmt.Errorf("upgrade core: %w", err)
		}
		if !result.Changed {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Core packages are already up to date.")
			return err
		}
		for _, change := range result.Changes {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s -> %s\n", change.Name, change.From, change.To); err != nil {
				return fmt.Errorf("write upgrade summary: %w", err)
			}
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(),
			"Core lock upgraded.\nExisting coops are stale; run `coop rebuild` in each project when ready.")
		return err
	},
}
```

When a changed lock has no version-string differences, print `Core package revisions changed.` before the common stale guidance.

Update rebuild to pass `s.CoreLock` to `image.MaterializeWithCoreLock` and use `s.DesiredImageName()` for the tag.

- [x] **Step 4: Run command tests**

Run:

```bash
flox activate -- go test ./cmd/coop -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit only after approval**

```bash
git add cmd/coop/main.go cmd/coop/main_test.go
git commit -m "feat: add self-contained core upgrades"
```

---

### Task 6: Document explicit upgrade and adoption semantics

**Files:**
- Modify: `README.md`
- Modify: `docs/runtime.md`

**Interfaces:**
- Documents: `coop upgrade` changes machine-wide desired core state
- Documents: `coop rebuild` applies that state to only the current project

- [x] **Step 1: Add the command to README**

Document this workflow:

```bash
coop upgrade
# Existing coops keep running unchanged.

cd ~/Projects/my-app
coop status
coop rebuild
```

State explicitly that:

- the resolver is self-contained and does not require host Flox;
- version one upgrades the complete core;
- it does not rebuild any coop;
- project tools and project Flox environments are not upgraded;
- targeted core package upgrades are not supported yet.

- [x] **Step 2: Align runtime recovery guidance**

Update `docs/runtime.md` so a missing desired image after `coop upgrade` is expected stale state, and `coop rebuild` is the explicit adoption action. Preserve the warning not to use `coop destroy` because agent-state volumes are unrelated.

- [x] **Step 3: Check documentation references**

Run:

```bash
rg -n "coop upgrade|coop rebuild|embedded lock|embedded definition|stale" README.md docs
```

Expected: command descriptions consistently distinguish upgrade from rebuild.

- [ ] **Step 4: Commit only after approval**

```bash
git add README.md docs/runtime.md
git commit -m "docs: explain core upgrade adoption"
```

---

### Task 7: Full verification and review

**Files:**
- Review all files changed by Tasks 1-6

**Interfaces:**
- Verifies the complete feature

- [x] **Step 1: Format and statically check**

Run:

```bash
flox activate -- go fmt ./...
flox activate -- go vet ./...
```

Expected: both exit successfully.

- [x] **Step 2: Run the full test suites**

Run:

```bash
flox activate -- go test ./...
flox activate -- go test -race ./...
```

Expected: both exit successfully.

- [x] **Step 3: Build and inspect CLI help**

Run:

```bash
flox activate -- go build -o /Users/cdolan/tmp/coop-core-upgrade-rc ./cmd/coop
/Users/cdolan/tmp/coop-core-upgrade-rc upgrade --help
```

Expected: the binary builds and help describes a machine-wide core upgrade without promising a rebuild.

- [x] **Step 4: Run isolated Apple Container UAT**

Set isolated XDG directories under `/private/tmp`, run the candidate binary's `upgrade`, and verify:

```text
1. A core lock is written under the expected manifest-fingerprint state path.
2. No project container is stopped, removed, created, or rebuilt.
3. A second upgrade reports current when no newer lock exists.
4. `coop status` reports rebuild required for a project whose prior desired image exists.
5. `coop rebuild` builds the new desired image.
6. Existing agent-state volumes remain present.
```

Remove only the explicitly named UAT container, image, volumes, project directory, and XDG directories after recording the result.

- [x] **Step 5: Inspect the final diff**

Run:

```bash
git diff --check
git diff --stat
git status --short --branch
```

Expected: no whitespace errors, only planned files changed, and no generated build artifact is tracked.

- [x] **Step 6: Request code review**

Use `superpowers:requesting-code-review`, address verified findings, and rerun affected checks.

- [x] **Step 7: Commit only after explicit approval**

If the user approves one final feature commit instead of the task commits:

```bash
git add image/embed.go image/embed_test.go internal/core/core.go internal/core/core_test.go internal/core/runner.go internal/core/runner_test.go internal/session/session.go internal/session/session_test.go internal/doctor/doctor.go internal/doctor/doctor_test.go cmd/coop/main.go cmd/coop/main_test.go README.md docs/runtime.md docs/superpowers/plans/2026-07-30-core-upgrade.md
git commit -m "feat: add self-contained core upgrades"
```
