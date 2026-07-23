# Public Documentation Accuracy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every normative Coop document accurately describe the public repository and the behavior shipped in `v0.1.0-beta.3`.

**Architecture:** Keep the current documentation structure and correct facts in place rather than introducing a new documentation system. `README.md` remains the user journey, `SECURITY.md` remains the disclosure policy, and `docs/release.md` remains the maintainer runbook; historical plans/specifications remain immutable records.

**Tech Stack:** Markdown, shell examples, GitHub Releases, GitHub CLI, Apple `container`, Go 1.26.5.

## Global Constraints

- Runtime support remains Apple silicon on macOS 26 or later.
- Published binaries remain Darwin arm64, checksum-protected, ad-hoc signed, and not notarized.
- The public release installer remains `gh`-based; no curl installer, Homebrew tap, or install script is added.
- Exact-tag public release downloads do not require Coop repository access or GitHub authentication when using a current GitHub CLI.
- Source builds remain supported but secondary and require Go 1.26.5 or later.
- Historical files under `docs/superpowers/` are not rewritten.
- No runtime code, image input, release artifact, or workflow behavior changes.

---

### Task 1: Correct the Public README Journey

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: public GitHub release assets, `coop_v<version>_darwin_arm64.tar.gz`, `checksums.txt`, and the CLI/config behavior shipped on `main`.
- Produces: the primary public installation, verification, quick-start, configuration, and limitations documentation.

- [x] **Step 1: Record the stale README assertions**

Run:

```sh
rg -n -i 'while (the repository|it) is private|authenticated GitHub CLI|Flox environments are limited' README.md
```

Expected before editing: matches in Requirements, Install, and Current Limits.

- [x] **Step 2: Separate runtime requirements from installation tooling**

Replace the requirements list with the runtime contract:

```markdown
- macOS 26 or later on Apple silicon
- Apple's `container` CLI and a running container service
```

Keep the existing explanation that Flox is a guest component and not a host
prerequisite.

- [x] **Step 3: Make the published release the primary public install path**

Replace the stable-first, prerelease-fallback API query with the exact current
release version and retain the checksum flow. Introduce it with:

```markdown
Install the current release with [`gh`](https://cli.github.com/). Public asset
downloads do not require authentication or repository access:
```

Retain `shasum -a 256 -c`, extraction, and `install -m 0755`. Add updating the
README's exact version to the release checklist so the user-facing example
stays simple and current.

- [x] **Step 4: Isolate the source-build path**

Rename the source fallback to a `### Build from source` subsection and add:

```markdown
Building from source requires Go 1.26.5 or later:
```

Keep the clone/build commands and shared `~/.local/bin` PATH guidance.

- [x] **Step 5: Correct behavioral wording found by the audit**

Make these focused edits:

- remove the duplicate `and` in the `coop doctor` checklist;
- state that project Flox environments may support multiple systems but must
  include `aarch64-linux` for use inside Coop;
- preserve the existing command list, trusted/project configuration boundary,
  core-tool list, rebuild behavior, security model, and current signing limits
  after checking them against source.

- [x] **Step 6: Verify the README assertions are gone**

Run:

```sh
rg -n -i 'while (the repository|it) is private|authenticated GitHub CLI|Flox environments are limited' README.md
```

Expected: no output, exit status 1.

---

### Task 2: Correct Security and Release Maintainer Documentation

**Files:**
- Modify: `SECURITY.md`
- Modify: `docs/release.md`
- Review unchanged: `THIRD_PARTY_NOTICES.md`
- Review unchanged: `.github/workflows/ci.yml`
- Review unchanged: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: enabled GitHub private vulnerability reporting, annotated version tags, and the current GitHub release workflow.
- Produces: an accurate disclosure policy and a version-neutral release runbook.

- [x] **Step 1: Correct reporter eligibility in `SECURITY.md`**

Replace the false repository-access sentence with:

```markdown
Private vulnerability reporting is enabled for this public repository and is
available to any GitHub user.
```

Keep the advisory URL, requested report contents, and warning against public
disclosure.

- [x] **Step 2: Make release examples reusable**

In `docs/release.md`, replace beta.3-specific tag commands with a clearly
replaceable variable:

```sh
version=vX.Y.Z-beta.N # replace with the version being released
git tag -a "$version"
git push origin "$version"
```

Use the same `$version` variable for download and archive commands so later
releases do not require editing the runbook.

- [x] **Step 3: Remove private-project release assumptions**

Replace the private/authenticated download introduction with:

```markdown
Public release assets can be downloaded without authentication using a current
GitHub CLI:
```

Replace "outside the beta audience" with wording about broader distribution,
while retaining the signing, notarization, and quarantine facts.

- [x] **Step 4: Generalize release-note guidance**

Require release annotations to cover user-visible changes, migration steps,
and known limitations. Preserve the Node.js removal as the concrete beta.3
example, but do not require unrelated future releases to repeat it.

- [x] **Step 5: Confirm unchanged documents remain accurate**

Check that `THIRD_PARTY_NOTICES.md` still says the beta builds images locally
and does not redistribute them. Check that workflow comments still distinguish
mocked hosted CI from required Apple-hardware release testing. Make no edit
without a concrete contradiction.

---

### Task 3: Verify and Commit the Documentation Set

**Files:**
- Modify: `README.md`
- Modify: `SECURITY.md`
- Modify: `docs/release.md`
- Create: `docs/superpowers/specs/2026-07-22-public-documentation-accuracy-design.md`
- Create: `docs/superpowers/plans/2026-07-22-public-documentation-accuracy.md`

**Interfaces:**
- Consumes: all documentation changes from Tasks 1 and 2.
- Produces: one reviewable documentation commit with verified public install and release guidance.

- [x] **Step 1: Search all normative docs for stale visibility/version language**

Run:

```sh
rg -n -i 'repository is private|while it is private|downloads require authentication|authenticated GitHub CLI|outside the beta audience' README.md SECURITY.md THIRD_PARTY_NOTICES.md docs/release.md
```

Expected: no stale visibility/authentication language.

- [x] **Step 2: Re-run the public download path without credentials**

Create a temporary directory under `/Users/cdolan/tmp`, set `GH_CONFIG_DIR` to
an empty child directory, remove `GH_TOKEN` and `GITHUB_TOKEN`, download the
README's exact-tag `checksums.txt` and archive, and run:

```sh
shasum -a 256 -c checksums.txt
```

Expected: the current archive reports `OK` without GitHub authentication.

- [x] **Step 3: Run repository verification**

Run:

```sh
flox activate -- git diff --check
flox activate -- go test ./...
```

Expected: both commands exit 0.

- [x] **Step 4: Review the complete diff and repository status**

Run:

```sh
flox activate -- git diff -- README.md SECURITY.md THIRD_PARTY_NOTICES.md docs/release.md .github/workflows/ci.yml .github/workflows/release.yml docs/superpowers/specs/2026-07-22-public-documentation-accuracy-design.md docs/superpowers/plans/2026-07-22-public-documentation-accuracy.md
flox activate -- git status --short --branch
```

Expected: only the three normative documents and the two approved planning
artifacts differ; the notice and workflows remain unchanged.

- [x] **Step 5: Commit the complete documentation update**

Run:

```sh
git add README.md SECURITY.md docs/release.md docs/superpowers/specs/2026-07-22-public-documentation-accuracy-design.md docs/superpowers/plans/2026-07-22-public-documentation-accuracy.md
git commit -m "docs: update public project guidance"
```

Expected: one conventional documentation commit on
`docs/public-project-accuracy`.

- [ ] **Step 6: Check and resolve RoboRev findings**

Run:

```sh
roborev list --open
```

Expected: no open findings for the new commit. If findings exist, inspect each
with `roborev show <id>`, amend the same commit after verified corrections, and
close only findings confirmed resolved.
