# Public Documentation Accuracy Design

## Goal

Bring Coop's normative documentation into agreement with the public repository,
the published `v0.1.0-beta.3` release flow, and the behavior shipped on `main`.
The result should give a new user one accurate installation path, retain a clear
source-build path for contributors, and remove private-project assumptions.

## Scope

Audit and update the current user- and maintainer-facing documents:

- `README.md`
- `SECURITY.md`
- `docs/release.md`
- `THIRD_PARTY_NOTICES.md`
- explanatory comments in `.github` workflows

Historical plans and specifications under `docs/superpowers/` are records of
past decisions. They are not normative documentation and will not be rewritten
unless they are linked as current user guidance.

## README Changes

### Requirements and installation

- Describe the runtime requirements independently of installation method:
  Apple silicon, macOS 26 or later, and Apple's running `container` service.
- Keep Homebrew installation of Apple's `container` CLI as the concise setup
  path; it remains supported by the current Homebrew formula.
- Present the published Darwin arm64 archive as the primary Coop installation
  path.
- Replace the complex `gh api` release selector with the exact current release
  version. Keep that version current as part of the release checklist.
- State that an exact public asset download requires a current GitHub CLI, not
  an authenticated GitHub account. The public asset download is verified with
  an empty `GH_CONFIG_DIR` and no token environment variables; release listing
  and API commands are not claimed to work without authentication.
- Retain checksum verification and the warning that release binaries are not
  Developer ID signed or notarized.
- Move source compilation into a distinct `Build from source` subsection and
  state its Go 1.26.5 requirement there. It remains a supported contributor and
  fallback path, not the primary installation path.
- Preserve the `~/.local/bin` PATH guidance and explicit `coop --version` and
  `coop doctor` checks.

### Behavioral accuracy

- Check the command list against the Cobra command tree.
- Check configuration examples, trusted/project boundaries, resource caps,
  state-volume behavior, credential lifecycle, and PATH precedence against the
  implementation and tests.
- Check the promised core package list against the locked core manifest.
- Replace the misleading statement that Flox environments are "limited to
  aarch64-linux" with the actual contract: project Flox environments may be
  cross-platform but must include `aarch64-linux` for guest activation.
- Fix wording and list grammar discovered during the audit without changing
  product behavior.

## Security Policy Changes

- Keep the latest-beta-only support policy.
- Keep GitHub private vulnerability reporting as the disclosure route.
- Remove the false requirement that reporters have repository access. Private
  vulnerability reporting is enabled for this public repository and accepts
  reports from any GitHub user.
- Continue directing reporters away from public issues for undisclosed
  vulnerabilities.

## Release Guide Changes

- Remove private-repository and authenticated-download assumptions.
- Replace hard-coded `v0.1.0-beta.3` commands and filenames with a reusable
  version variable so the guide remains valid for later releases.
- Keep the annotated-tag workflow, prerelease classification, real-hardware
  smoke tests, checksum validation, and no-tag-reuse rule.
- Retain the signing, notarization, and browser-quarantine caveats, but replace
  "outside the beta audience" wording with public-distribution language.
- Keep the explicit Node.js migration note as a requirement for the release
  whose change introduced it, while avoiding wording that implies every future
  release must repeat that exact note.

## Documents Expected to Remain Unchanged

`THIRD_PARTY_NOTICES.md` already describes a public beta, local-only sandbox
image construction, and redistribution obligations accurately. CI and release
workflow comments also match the current hosted-runner and publication model.
They will change only if the final cross-check finds a concrete contradiction.

## Verification

- Search all normative Markdown for stale private-repository, authentication,
  and beta-audience language, while allowing the README's intentional current
  release version.
- Re-run the exact-tag README download against an isolated empty GitHub CLI
  configuration with token variables removed.
- Verify the downloaded checksum file is reachable without authentication.
- Compare the README command list and core-tool list to source manifests.
- Run `git diff --check` and the Go test suite; documentation changes must not
  alter generated or embedded runtime inputs.
- Review the final diff as a public first-run journey: requirements, install,
  verification, quick start, configuration, security limits, and recovery.

## Non-Goals

- Adding a curl installer, install script, Homebrew tap, signing, or
  notarization.
- Changing Coop runtime behavior or release artifacts.
- Rewriting historical implementation plans and specifications.
- Resuming the paused generic repository or GitHub-extension installation
  design.
