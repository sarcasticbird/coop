# Flox-Backed Tool Environments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Coop's stock guest tools a locked internal Flox environment, allow additive global and repository `[tools].packages`, and expose deterministic image/rebuild state without making Flox a host or repository prerequisite.

**Architecture:** The embedded image realizes a release-owned `aarch64-linux` Flox environment for Coop's maintenance and baseline tools. Validated configured packages are installed from Coop's immutable Nixpkgs revision into a separate Nix profile. User entry activates the core environment, prepends the configured profile, and then optionally activates the nearest project Flox environment; Coop maintenance never inherits the configured or project layers.

**Tech Stack:** Go 1.26.5, BurntSushi TOML, Cobra, Flox 1.13.x, Nix 2.x profiles, Apple `container` 1.x, OCI Containerfile builds.

## Execution Record

- [x] Prerequisite `origin/main` credential fix merged in `f3603a2`.
- [x] Additive tool configuration committed in `e6f9614`.
- [x] Locked core Flox environment committed in `b4a4974`.
- [x] Image construction and runtime layering committed together in `725e484`
  so no intermediate commit produced an image without its required entry
  wrapper.
- [x] Image drift and rebuild status committed in `cee0a08`.
- [x] Real Apple builds validated the stock image and a repository-defined
  package; ephemeral VMs validated core commands, maintenance isolation, and
  project Flox precedence.
- [x] Documentation and final branch verification.

## Global Constraints

- Incorporate `origin/main` before implementation; it contains the guest credential shell-builtin fix required by the new execution layering.
- Preserve the existing unrelated `README.md` worktree edit and never stage it with this feature.
- Flox remains internal to the guest image. The host requirement remains Apple's `container` runtime, and repositories without `.flox` remain fully supported.
- Core packages are immutable for a Coop release and locked by the embedded Flox manifest and `aarch64-linux` lock.
- `[tools].packages` is additive across global and repository config. It cannot remove or replace core packages or select another package source.
- Configured package values are Nixpkgs attribute paths only. Reject whitespace, control characters, `/`, `\\`, `:`, `#`, URL syntax, flake syntax, empty segments, values longer than 128 bytes, and more than 64 effective configured packages.
- Keep configured package input out of shell source. Materialize one validated full installable per line and read it as a quoted shell variable during the image build.
- No read-only command (`status`, project discovery, entry) may resolve packages or start a build. Only `coop rebuild` builds.
- A failed rebuild must not remove or replace the existing image or container.
- User precedence is core < configured profile < nearest project `.flox`. Maintenance uses only the controlled core PATH.
- Preserve argv boundaries through every activation wrapper. Do not use `flox activate -c` or join user argv into shell source.
- Do not pass `--start-services`; project Flox automatic-service settings remain authoritative.
- Manual rootfs changes survive down/up and are discarded on recreation/destroy. Named agent-state volumes survive image-driven recreation.
- After every Go change, run `gofmt`, focused tests, `go test ./...`, and `go vet ./...` before the corresponding commit.
- Every commit below is authorized by the user's request to build this work, but no push or PR is authorized.

## File Map

- Modify `internal/config/config.go`: tools schema, provenance, validation, canonical merge, compatibility warning.
- Modify `internal/config/config_test.go`: global/project merge, validation, count limit, and legacy alias tests.
- Create `image/core/.flox/env.json`: embedded core environment identity.
- Create `image/core/.flox/env/manifest.toml`: promised core packages and Linux-only target.
- Create `image/core/.flox/env/manifest.lock`: exact `aarch64-linux` release lock.
- Create `image/coop-user-env`: argv-preserving configured/project layer wrapper.
- Modify `image/embed.go`: embed all image inputs, write configured installables, and fingerprint source/core files.
- Modify `image/embed_test.go`: materialization, pin consistency, package input, and fingerprint tests.
- Modify `image/Containerfile`: realize core Flox environment and isolated configured Nix profile.
- Modify `image/zshrc`: remove advisory activation and rely on entry-time activation.
- Modify `internal/session/session.go`: desired image identity, maintenance/user execution boundaries, and recreation warning.
- Modify `internal/session/session_test.go`: identity, activation precedence, argv preservation, and lifecycle safety tests.
- Modify `cmd/coop/main.go`: rebuild input summary and desired/running image status.
- Modify `cmd/coop/main_test.go`: rebuild argv/input and status reporting tests.
- Modify `internal/doctor/doctor.go` and `internal/doctor/doctor_test.go`: desired image calculation after the config model changes.
- Modify `README.md`: dependency boundary, package policy, migration, precedence, and mutability.
- Modify `docs/release.md`: real Apple VM smoke procedure.

---

### Task 0: Reconcile the branch and preserve local work

**Files:**
- Integrate: `internal/credential/guest.go`
- Integrate: `internal/credential/guest_test.go`
- Preserve unstaged: `README.md`

- [ ] **Step 1: Record the current tree and divergence**

Run:

```sh
flox activate -- git status --short --branch
flox activate -- git log --oneline --left-right HEAD...origin/main
```

Expected: only `README.md` is modified; `origin/main` contributes `31976df` through merge `4cbe389`.

- [ ] **Step 2: Merge `origin/main` without staging the README edit**

Run:

```sh
flox activate -- git merge --no-edit origin/main
```

Expected: a clean merge touching only credential guest code/tests; `README.md` remains unstaged.

- [ ] **Step 3: Verify the prerequisite**

Run:

```sh
flox activate -- go test ./internal/credential ./internal/session
flox activate -- git status --short --branch
```

Expected: tests pass and only the pre-existing README edit remains outside the merge commit.

### Task 1: Add additive tool configuration and compatibility

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Add `Config.Tools Tools` with canonical `Packages`, plus non-TOML `GlobalPackages` and `ProjectPackages` provenance used by rebuild output.
- Add `Config.Warnings []string` for the one-beta legacy alias warning.
- Keep `Image.ExtraPackages` only as the decoded compatibility field; all consumers move to `Config.Tools`.

- [ ] **Step 1: Write failing merge and provenance tests**

Cover global-plus-project merge, stable sorting, deduplication within and across layers, and absence of implicit core package entries:

```go
func TestToolPackagesMergeCanonically(t *testing.T) {
	// global: ["bat", "shellcheck"]
	// project: ["actionlint", "bat"]
	// effective: ["actionlint", "bat", "shellcheck"]
	// provenance remains global ["bat", "shellcheck"], project ["actionlint", "bat"].
}
```

- [ ] **Step 2: Write failing package grammar and limit tests**

Accept `gh`, `nodePackages.prettier`, and `python313Packages.ruff`. Reject empty strings, leading/trailing or repeated dots, whitespace/control bytes, path separators, `:`, `#`, `github:owner/repo`, shell punctuation, values over 128 bytes, and 65 effective unique packages.

Run: `flox activate -- go test ./internal/config -run 'Tool|Package' -v`

Expected: FAIL because the schema does not exist.

- [ ] **Step 3: Implement the schema and canonical merge**

Use a decoded and returned structure that keeps TOML input narrow:

```go
const MaxToolPackages = 64

type Tools struct {
	Packages        []string `toml:"packages"`
	GlobalPackages  []string `toml:"-"`
	ProjectPackages []string `toml:"-"`
}

type Config struct {
	// existing fields...
	Tools    Tools    `toml:"tools"`
	Warnings []string `toml:"-"`
}
```

Validate each layer before merging. Canonicalize only after both layers load. Keep per-layer provenance canonical as well.

- [ ] **Step 4: Add the one-beta legacy alias**

Global `[image].extra_packages` populates the global tool layer and appends a deprecation warning. Error when the same global file defines both `[tools].packages` and `[image].extra_packages`. Continue ignoring project image configuration, but explicitly error if a project uses `image.extra_packages` so the trusted-only rule cannot conceal a migration mistake.

- [ ] **Step 5: Verify and commit configuration**

Run:

```sh
flox activate -- gofmt -w internal/config/config.go internal/config/config_test.go
flox activate -- go test ./internal/config -v
flox activate -- go test ./...
flox activate -- go vet ./...
flox activate -- git diff --check
```

Expected: all pass.

Commit only these files:

```sh
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add project-configurable tool packages"
```

### Task 2: Embed and lock the Coop core Flox environment

**Files:**
- Create: `image/core/.flox/env.json`
- Create: `image/core/.flox/env/manifest.toml`
- Create: `image/core/.flox/env/manifest.lock`
- Modify: `image/embed.go`
- Test: `image/embed_test.go`

**Interfaces:**
- `image.CorePackages() []string` returns the sorted promised command package IDs for display/tests.
- `image.Fingerprint()` hashes `Containerfile`, `zshrc`, `coop-user-env`, core `env.json`, manifest, lock, and `NixpkgsRef`.
- `image.Materialize(packages []string)` writes all embedded files plus `configured-packages.txt`.

- [ ] **Step 1: Add failing embedded-environment tests**

Assert that materialization includes the complete `.flox` tree, that the lock contains only `aarch64-linux` resolutions, that the manifest declares exactly the approved 26 core packages, and that `gh` is present while standalone Node/Go/Python are absent.

Add a fingerprint helper test using an internal `fingerprint(fs.FS)` or explicit byte-list helper so changing manifest or lock bytes changes identity.

- [ ] **Step 2: Create and lock the core manifest**

Create a disposable Flox environment under `/Users/cdolan/tmp`, target only `aarch64-linux`, declare these package paths, and let Flox produce the lock:

```text
bashInteractive zsh coreutils gnugrep gnused findutils gawk gnutar gzip cacert
git gh openssh curl ripgrep jq diffutils patch file less procps tmux unzip
codex claude-code opencode
```

Copy only release artifacts (`env.json`, `manifest.toml`, `manifest.lock`) into `image/core/.flox`; do not copy cache, run, log, or host store links.

- [ ] **Step 3: Expand embedding and materialization**

Embed explicit paths so changes cannot be omitted accidentally:

```go
//go:embed Containerfile zshrc coop-user-env core/.flox/env.json core/.flox/env/manifest.toml core/.flox/env/manifest.lock
var files embed.FS
```

Return copies from `CorePackages`; never expose mutable package state.

- [ ] **Step 4: Verify and commit the core environment**

Run:

```sh
flox activate -- gofmt -w image/embed.go image/embed_test.go
flox activate -- go test ./image -v
flox activate -- go test ./...
flox activate -- go vet ./...
flox activate -- git diff --check
```

Commit:

```sh
git add image/core image/embed.go image/embed_test.go
git commit -m "feat: embed the locked Coop core environment"
```

### Task 3: Build the core and configured tool layers safely

**Files:**
- Create: `image/coop-user-env`
- Modify: `image/Containerfile`
- Modify: `image/embed.go`
- Test: `image/embed_test.go`
- Modify: `internal/session/session.go`
- Test: `internal/session/session_test.go`
- Modify: `cmd/coop/main.go`
- Test: `cmd/coop/main_test.go`
- Modify: `internal/doctor/doctor.go`
- Test: `internal/doctor/doctor_test.go`

**Interfaces:**
- Change image identity to `session.EffectiveImageName(config.Config)` so image name, core fingerprint, configured source ref, and canonical configured tools are one input.
- `image.Materialize(packages)` emits full installables as `${NixpkgsRef}#${validatedAttr}` one per line.
- Add an injectable CLI build runner so command construction and failures are testable without Apple virtualization.

- [ ] **Step 1: Write failing identity and materialization tests**

Prove package declaration order and duplicates do not change the tag, while any package, core fingerprint, base image name, or Nixpkgs source change does. Verify registry-port and digest normalization remains correct.

Assert `configured-packages.txt` contains exactly:

```text
github:flox/nixpkgs/<pinned-revision>#actionlint
github:flox/nixpkgs/<pinned-revision>#shellcheck
```

with no quoting, continuations, or shell interpolation.

- [ ] **Step 2: Replace stock `nix profile install` layers with the core Flox environment**

The Containerfile copies `core/.flox` to `/opt/coop-core/.flox`, realizes it during build with `flox activate --dir /opt/coop-core -- true`, and sets the maintenance PATH to the realized `aarch64-linux.coop-core-run/bin` followed only by fixed system fallbacks.

Install configured packages into `/opt/coop-tools/profile` using a quoted loop:

```sh
while IFS= read -r installable; do
  [ -n "$installable" ] || continue
  NIXPKGS_ALLOW_UNFREE=1 nix profile install \
    --profile /opt/coop-tools/profile \
    --extra-experimental-features "nix-command flakes" \
    --impure --priority 2 "$installable"
done < /tmp/configured-packages.txt
```

The file contents are already grammar-validated; the quoted variable is still required. Do not expand a space-separated build argument.

- [ ] **Step 3: Add the argv-preserving user wrapper**

`image/coop-user-env` accepts optional `--project-flox DIR`, then `--`, prepends `/opt/coop-tools/profile/bin` to the active core PATH, and either directly `exec`s argv or runs `flox activate --dir "$dir" -- "$@"`. It contains no eval and no user-derived shell source.

- [ ] **Step 4: Make rebuild deterministic and observable**

Before calling `container build`, print canonical inputs:

```text
core tools:     26 packages
global tools:   bat
project tools:  actionlint, shellcheck
image:          coop:local-<fingerprint>
```

Pass only `GUEST_HOME` as a build argument; configured installables come from the materialized file. An injected build-runner failure must return unchanged and must not call runtime remove/stop methods.

- [ ] **Step 5: Update all desired-image consumers**

Move session, doctor, and tests from `Cfg.Image.ExtraPackages` to the effective canonical `Cfg.Tools.Packages`. Ensure `EnsureImage` still refuses stale fallback images.

- [ ] **Step 6: Verify and commit image construction**

Run:

```sh
flox activate -- gofmt -w image/embed.go image/embed_test.go internal/session/session.go internal/session/session_test.go cmd/coop/main.go cmd/coop/main_test.go internal/doctor/doctor.go internal/doctor/doctor_test.go
flox activate -- go test ./image ./internal/session ./internal/doctor ./cmd/coop -v
flox activate -- go test ./...
flox activate -- go vet ./...
flox activate -- git diff --check
```

Commit:

```sh
git add image/Containerfile image/coop-user-env image/embed.go image/embed_test.go internal/session/session.go internal/session/session_test.go cmd/coop/main.go cmd/coop/main_test.go internal/doctor/doctor.go internal/doctor/doctor_test.go
git commit -m "feat: build Flox-backed Coop tool images"
```

### Task 4: Enforce runtime layer precedence

**Files:**
- Modify: `internal/session/session.go`
- Modify: `internal/session/entry.go`
- Test: `internal/session/session_test.go`
- Modify: `image/zshrc`
- Test: `image/embed_test.go`

**Interfaces:**
- `entryArgv` always returns `flox activate --dir /opt/coop-core -- /usr/local/bin/coop-user-env ...`.
- It adds `--project-flox <nearest-dir>` only when a governing `.flox` exists.
- Non-interactive runtime calls remain unwrapped and therefore use the image's controlled core PATH; configured and project paths appear only inside `coop-user-env`.

- [ ] **Step 1: Write failing user-plane tests**

Cover all four entry shapes:

```text
command, no project Flox
command, nearest project Flox
bare shell, no project Flox
bare shell, nearest project Flox
```

Assert exact argv vectors, `zsh -l` for a bare shell, nested-directory lookup, sibling-boundary rejection, and preservation of arguments containing spaces, quotes, dollar signs, and semicolons.

- [ ] **Step 2: Write a maintenance-plane isolation test**

Create a session with configured packages and a project `.flox`, call `Up`, seeding, and credential scrub/stage paths, and assert the non-interactive calls do not contain `/opt/coop-tools`, the project path, or project activation. Assert the user interactive call does.

- [ ] **Step 3: Implement consistent entry activation**

Use constants for `/opt/coop-core` and `/usr/local/bin/coop-user-env`. Build argv with append/copy only. Do not pass `--start-services` or `--no-start-services`.

- [ ] **Step 4: Simplify zsh startup**

Remove the advisory `chpwd` Flox detector because entry-time activation is now authoritative. Retain prompt, history, locale, and the controlled fallback PATH established by the image.

- [ ] **Step 5: Improve recreation warning**

When a current container's spec mismatches a ready desired image, state explicitly that named state volumes are preserved and undeclared root-filesystem changes will be discarded before stop/remove occurs.

- [ ] **Step 6: Verify and commit runtime layering**

Run:

```sh
flox activate -- gofmt -w internal/session/session.go internal/session/session_test.go
flox activate -- go test ./internal/session -v
flox activate -- go test ./...
flox activate -- go vet ./...
flox activate -- git diff --check
```

Commit:

```sh
git add internal/session/session.go internal/session/session_test.go image/zshrc image/embed_test.go
git commit -m "feat: layer Coop and project Flox environments"
```

### Task 5: Report desired and running image state

**Files:**
- Modify: `cmd/coop/main.go`
- Test: `cmd/coop/main_test.go`
- Modify: `internal/runtime/mock.go` only if an error hook is missing.

- [ ] **Step 1: Write failing status tests**

For absent, stopped-current, running-current, and running-stale containers, assert output includes:

```text
project:
container:
state:
running image:
desired image:
rebuild required:
recreation pending:
```

`rebuild required` is `yes` only when the desired image is absent. `recreation pending` is `yes` only when a container exists but its recorded image/spec differs. Runtime inspection errors remain errors, never guessed state.

- [ ] **Step 2: Add a session status snapshot**

Prefer a small `Session.ImageStatus()` value over duplicating runtime calls in Cobra. It reads state, desired image existence, and current container image/label without mutation.

- [ ] **Step 3: Surface configuration warnings once**

Print `Config.Warnings` to stderr from the current-project command path. `ls` remains independent of project config; `doctor` reports the legacy warning in its normal check output or stderr once.

- [ ] **Step 4: Verify and commit operational UX**

Run:

```sh
flox activate -- gofmt -w cmd/coop/main.go cmd/coop/main_test.go internal/session/session.go internal/session/session_test.go internal/runtime/mock.go
flox activate -- go test ./cmd/coop ./internal/session ./internal/doctor -v
flox activate -- go test ./...
flox activate -- go vet ./...
flox activate -- git diff --check
```

Commit:

```sh
git add cmd/coop/main.go cmd/coop/main_test.go internal/session/session.go internal/session/session_test.go internal/runtime/mock.go
git commit -m "feat: report Coop image drift and rebuild state"
```

### Task 6: Document the boundary and verify on real hardware

**Files:**
- Modify: `README.md` without overwriting or staging unrelated user-authored hunks.
- Modify: `docs/release.md`
- Modify: `docs/superpowers/plans/2026-07-22-flox-backed-tools.md` checkbox state only.

- [ ] **Step 1: Update public documentation**

Document:

- Flox is required inside the Coop image but not on the host or in a repository.
- the exact 26-package core set and why language runtimes are excluded;
- `[tools].packages` in global and project config, additive merge, pinned-source behavior, and the 64-package limit;
- the `image.extra_packages` compatibility window and conflict rule;
- core/configured/project precedence;
- `try manually -> declare in coop.toml -> move to .flox when portability matters`;
- manual changes surviving down/up but not recreation/destroy; and
- `coop rebuild`/`coop status` behavior.

Use patch hunks narrow enough to preserve the existing credential-related README changes. Inspect the final README diff before staging and stage only intended hunks if necessary.

- [ ] **Step 2: Expand the release smoke checklist**

On Apple silicon with the real runtime:

```sh
flox activate -- go build -o /Users/cdolan/tmp/coop-smoke ./cmd/coop
/Users/cdolan/tmp/coop-smoke rebuild
/Users/cdolan/tmp/coop-smoke up
```

Verify all 26 promised packages with `command -v`; verify `node`, `go`, and `python` are not promised by checking they are absent unless supplied transitively and documenting that any transitive executable is unsupported. Then verify:

1. a repository `[tools].packages = ["shellcheck"]` rebuild and entry;
2. no-project-Flox and project-Flox entry;
3. a project Flox executable shadows a lower-layer executable;
4. command argv with spaces and shell punctuation is preserved;
5. maintenance commands remain successful when the configured/project layer contains a same-named tool;
6. failed configured-package build leaves the old image/container usable;
7. state volumes survive recreation; and
8. an undeclared rootfs marker disappears after recreation.

- [ ] **Step 3: Run the full automated gate**

Run:

```sh
flox activate -- gofmt -w $(rg --files -g '*.go')
flox activate -- go test ./...
flox activate -- go vet ./...
flox activate -- git diff --check
flox activate -- git status --short --branch
```

Expected: all checks pass; status contains only this feature plus the clearly separated pre-existing README hunk.

- [ ] **Step 4: Run RoboRev before final handoff**

Run:

```sh
flox activate -- roborev list --open
```

Inspect and resolve only current, actionable findings. Do not treat canceled or stale jobs as blockers.

- [ ] **Step 5: Commit documentation and smoke coverage**

Stage only feature documentation hunks and the release checklist:

```sh
git add -p README.md
git add docs/release.md docs/superpowers/plans/2026-07-22-flox-backed-tools.md
git commit -m "docs: explain Coop tool environments"
```

Do not push. Report any real-hardware step that could not be completed, with the exact reason and the remaining command.

## Plan Self-Review Checklist

- [x] Every approved design goal and non-goal maps to a task or global constraint.
- [x] Every repository-controlled value is validated before materialization or build.
- [x] No package or user argv is interpolated into shell source.
- [x] Core lock, configured source pin, and canonical package set all affect image identity.
- [x] Maintenance and user execution planes have separate tests.
- [x] Rebuild failure and pre-teardown image validation preserve current state.
- [x] Legacy alias behavior is limited to global config and produces one warning.
- [x] Existing README work is preserved and separately staged.
- [x] Each implementation commit is independently testable.
