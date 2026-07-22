# Flox-Backed Tools Review Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the actionable PR review findings without weakening Coop's deterministic image identity or project Flox model.

**Architecture:** Keep project Flox as the explicit outer user-command environment, make the locked Coop core authoritative over the additive configured profile, and reject incomplete image builds. Preserve canonical package handling at public image/session boundaries and document intentional compatibility breaks.

**Tech Stack:** Go 1.26, Bash, POSIX Containerfile build steps, Flox, Nix profiles, BurntSushi TOML.

## Global Constraints

- Repository package declarations remain plain Nixpkgs attribute paths resolved from Coop's immutable pinned source.
- Maintenance commands never inherit repository-controlled tool paths.
- A failed replacement build never mutates the existing image or container.
- The unrelated local README credential-environment edit remains unstaged and outside this change.
- Production changes follow red-green-refactor and all repository commands run through `flox activate`.

---

### Task 1: Harden user PATH and image build completion

**Files:**
- Modify: `image/embed_test.go`
- Modify: `image/coop-user-env`
- Modify: `image/Containerfile`

**Interfaces:**
- Consumes: `COOP_CORE`, `COOP_TOOLS_PROFILE`, and the core-activated `PATH` passed to `coop-user-env`.
- Produces: user precedence `project .flox > Coop core > configured tools > OS fallback`, plus a nonzero build result for every failed package install.

- [x] Add failing embedded-context tests requiring the core path before the configured profile and `nix profile install ... || exit 1`.
- [x] Run `flox activate -- go test ./image -run 'Test(UserEnvironment|Containerfile)'` and confirm both new assertions fail.
- [x] Insert the configured profile immediately after the first core PATH entry in `coop-user-env`, and make each install failure terminate the Containerfile loop.
- [x] Re-run the focused image tests and confirm they pass.

### Task 2: Make migration failures and warnings actionable

**Files:**
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/config.go`
- Modify: `cmd/coop/main_test.go`
- Modify: `cmd/coop/main.go`

**Interfaces:**
- Consumes: `validateToolPackages([]string) error`, `config.Config.Warnings`, and TUI `EnterWorkdir` results.
- Produces: a specific flake-reference migration error and exactly one deprecation warning before TUI-driven entry.

- [x] Add a failing config test for `github:owner/repo#attr` requiring an error that mentions plain Nixpkgs attributes, flake references, and Coop's pinned source.
- [x] Add a failing TUI-entry test that loads global `image.extra_packages` and expects one warning.
- [x] Run the two focused tests and confirm their expected failures.
- [x] Special-case `#` in package validation and call `writeConfigWarnings` after the TUI session loads.
- [x] Re-run the focused tests and confirm they pass.

### Task 3: Bind normalization and core declarations to one source of truth

**Files:**
- Modify: `image/embed_test.go`
- Modify: `image/embed.go`
- Modify: `internal/session/session.go`

**Interfaces:**
- Produces: `image.CanonicalPackages([]string) []string`, used by both image materialization and session image identity.
- Preserves: defensive normalization for directly constructed `config.Config` values.

- [x] Add a failing test that parses `core/.flox/env/manifest.toml` and requires its `[install].*.pkg-path` set to equal `CorePackages()`.
- [x] Export the image package canonicalizer and use it from `EffectiveImageName`; remove the duplicated session normalization imports and implementation.
- [x] Run `flox activate -- go test ./image ./internal/session` and confirm the invariant and identity tests pass.

### Task 4: Document migration and release impact

**Files:**
- Modify selected hunks only: `README.md`
- Modify: `docs/release.md`
- Modify: `docs/superpowers/specs/2026-07-22-flox-backed-tools-design.md`

**Interfaces:**
- Produces: documentation that configured tools cannot replace core commands, flake installables are no longer accepted, Node.js is no longer bundled, and the annotated release tag must call out that removal.

- [x] Add the migration and precedence language to the existing tools section without staging the unrelated credential documentation hunk.
- [x] Add Node.js removal to the release smoke checklist and annotated-tag guidance.
- [x] Run `flox activate -- git diff --check`.

### Task 5: Full verification and review handoff

**Files:**
- Verify all modified files; do not commit or push without explicit authorization.

**Interfaces:**
- Produces: a review-ready working tree with command evidence.

- [x] Run `flox activate -- gofmt -w` on modified Go files.
- [x] Run `flox activate -- go test -race ./...`.
- [x] Run `flox activate -- go vet ./...`.
- [x] Run `flox activate -- go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`.
- [x] Run `flox activate -- git diff --check` and inspect the final diff and status.
- [x] Report accepted and declined review items, verification evidence, and the preserved unstaged README hunk.
