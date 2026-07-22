# Flox-Backed Tool Environments

**Date:** 2026-07-22
**Status:** Approved design
**Tracking:** kata `qxq1`

## Summary

Coop will make its existing Flox dependency explicit and use Flox as the
internal environment engine for the stock guest toolset. Projects do not need
Flox on the host and do not need to contain a `.flox` environment. When a
project does contain one, Coop layers it over the internal environment so the
same project toolchain works inside and outside Coop.

Global and repository `coop.toml` files may add packages to a project-specific
image through a simple additive `[tools]` table. Coop will not maintain a
package catalog or invent per-package version syntax. Configured additions are
resolved from the Nixpkgs revision pinned by the installed Coop release. Exact
project toolchain versions remain the responsibility of an optional project
Flox manifest and lockfile.

The resulting boundary is:

```text
Apple VM
  -> Coop core Flox environment
  -> configured global and repository tools
  -> optional project Flox environment
  -> shell, agent, or command
```

## Goals

- Give every Coop a reliable operational workbench, including `gh`.
- Keep application runtimes and build toolchains out of the stock image.
- Allow trusted global configuration and repository configuration to request
  additional guest tools.
- Make the initial environment reproducible while preserving the user's
  ability to modify a running guest.
- Keep project Flox environments optional and portable between host and guest.
- Use Flox and Nix for package resolution, dependencies, and locking instead
  of implementing those systems in Coop.
- Preserve working containers and state volumes when replacement image builds
  fail.
- Implement the change as logical commits that remain independently testable.

## Non-Goals

- Restricting what guest root may install after the container starts.
- Treating tool declarations as a security allowlist.
- Adding language-specific version syntax to `coop.toml`.
- Making Flox a host prerequisite.
- Requiring repositories to contain `.flox`.
- Replacing Apple's `container` build and runtime pipeline with
  `flox containerize`.
- Supporting arbitrary package sources, flakes, URLs, overlays, hooks, or
  services from repository `coop.toml`.
- Persisting undeclared root-filesystem changes across container recreation.

## Dependency Boundary

Flox is already an internal Coop dependency: the embedded image starts from a
pinned `ghcr.io/flox/flox` image, uses the Nix installation supplied by that
image, includes the `flox` executable, and activates project environments when
present. This design formalizes that dependency.

Flox remains optional at the user-facing boundaries:

- A released Coop binary requires Apple's `container` runtime on the host, not
  a host Flox installation.
- A repository without `.flox` remains fully usable.
- A repository with `.flox` gets automatic environment activation and the same
  declared toolchain inside and outside Coop.

## Package Layers

### Core environment

Coop embeds a path Flox environment containing a manifest and an
`aarch64-linux` lock. The environment contains no services, activation hooks,
or user-controlled shell code. Its manifest and lock are release artifacts and
are included in the embedded image fingerprint.

The Flox base image supplies the `flox` and `nix` platform foundation. The core
environment declares the packages Coop promises to users:

**Shell and runtime support**

- `bashInteractive`
- `zsh`
- `coreutils`
- `gnugrep`
- `gnused`
- `findutils`
- `gawk`
- `gnutar`
- `gzip`
- `cacert`

**Repository workbench**

- `git`
- `gh`
- `openssh`
- `curl`
- `ripgrep`
- `jq`
- `diffutils`
- `patch`
- `file`
- `less`
- `procps`
- `tmux`
- `unzip`

**Agent CLIs**

- `codex`
- `claude-code`
- `opencode`

Node.js is not an explicitly available project runtime. Agent package closures
may contain Node internally, but that is not part of Coop's command contract.
Go, Python, Rust, Java, Ruby, build systems, cloud CLIs, deployment tools, and
personal shell utilities are also excluded from the stock environment.

### Configured tool profile

The global and repository package sets are merged into a separate Nix profile
inside the image. These packages are resolved from an immutable Nixpkgs
revision owned by the Coop release. This is necessary because arbitrary future
repository package names cannot be present in the shipped core Flox lock.

Using the release-pinned Nixpkgs revision keeps a package name deterministic
for a given Coop release without creating a Coop lockfile format. The package
profile is visible to user commands but is not used for Coop maintenance
operations. The locked core precedes this configured profile on `PATH`, so
repository-selected packages can add commands but cannot silently replace a
core command. Coop does not use an attribute-name collision list because a
different Nixpkgs attribute may provide the same executable.

### Optional project environment

When the selected working directory is governed by a project `.flox`
environment, Coop activates it inside the core environment. The project
environment has highest precedence for user commands. Repositories without
`.flox` skip this layer.

## Configuration

Both configuration layers accept the same additive shape:

```toml
# ~/.config/coop/coop.toml or <project>/coop.toml
[tools]
packages = ["shellcheck", "actionlint"]
```

The effective set is:

```text
immutable core + global tools.packages + project tools.packages
```

Coop sorts and deduplicates the configured package paths before validation,
display, and fingerprinting. Neither configuration layer can remove core
packages or change the package source.

Package values are Nixpkgs attribute paths such as `gh`, `shellcheck`, or
`nodePackages.some-tool`. Values must be non-empty, contain no whitespace,
control characters, path separators, URL syntax, or flake delimiters, and fit
within a conservative identifier-length limit. The effective configured set is
bounded to 64 packages. Existence and `aarch64-linux` support are determined by
the package resolver rather than a Coop-owned allowlist.

Repository configuration cannot declare package versions, sources, hooks,
profiles, environment variables, services, or license policy. Users who need
those features use a project Flox environment.

## Versioning

Version ownership is intentionally split by concern:

- The core package versions are fixed by Coop's embedded Flox lock.
- Global and repository package names resolve from the Nixpkgs revision pinned
  by the installed Coop release.
- Upgrading Coop may update both package universes and therefore changes the
  derived image identity.
- Projects requiring exact, portable toolchain versions declare and lock them
  in `.flox`.

`coop.toml` does not grow version constraints, dependency groups, conflict
resolution, update commands, or a generated lockfile.

## Image Build and Identity

`coop rebuild` materializes a build context containing:

- the Containerfile and shell configuration;
- the embedded core Flox manifest and lock; and
- a validated, newline-delimited list of release-pinned Nix installables for
  the configured tool profile.

The build realizes the core Flox environment and installs configured packages
into a dedicated profile using argument-safe package-manager input. Package
values are never joined into shell source. Any configured package installation
failure fails the build, prevents the derived image tag from being committed,
and leaves the existing image and container untouched.

The derived image tag includes hashes of:

- the image name;
- the Containerfile and shell configuration;
- the core Flox manifest and lock;
- the release-pinned source reference for configured packages; and
- the canonical configured package set.

Declaration order does not affect the tag. Any effective input change does.
Two projects with identical effective inputs reuse the same image.

`coop rebuild` remains explicit. Reading a checkout, running `coop status`, or
attempting entry never downloads packages or starts an image build.

## Runtime Activation and Precedence

Coop has two execution planes.

### Maintenance plane

Seeding, credential staging, credential cleanup, guest-home creation, and
other Coop-owned operations execute with a controlled core environment and
PATH. Configured tool packages and project Flox environments cannot shadow the
commands used by these operations. Where practical, maintenance helpers use
validated absolute paths or shell builtins from the core environment.

### User-command plane

User commands see, in increasing precedence:

1. the Coop core Flox environment;
2. the configured global and repository tool profile; and
3. the nearest project Flox environment, when present.

Explicit commands use argument-preserving exec activation. A bare `coop` shell
starts inside the core environment and, when a project environment is present,
enters that environment before launching the interactive login shell. Coop
does not pass `--start-services`; a project environment's own automatic-service
setting remains authoritative, preserving the existing activation behavior.

## Mutable Guest Behavior

The declared environment is a reproducible starting state, not an enforcement
mechanism. Guest root may install or download additional software.

- Manual changes survive `coop down` followed by `coop up` because the
  container root filesystem is retained.
- Manual changes disappear when a configuration or image change recreates the
  container, or when the user runs `coop destroy`.
- Agent state volumes survive ordinary image-driven recreation.
- A durable Coop-only tool belongs in `[tools].packages`.
- A durable and host-portable project tool belongs in `.flox`.

The documentation should describe the progression as:

```text
try manually -> declare in coop.toml -> move to .flox when portability matters
```

## Rebuild and Status UX

Before building, `coop rebuild` prints the effective inputs:

```text
core tools:     26 packages
global tools:   bat
project tools:  actionlint, shellcheck
image:          coop:local-a12b34c56d78
```

Behavior is fail-safe:

- Configuration and package identifiers are validated before build work.
- Unknown packages, unsupported systems, license-policy failures, and package
  conflicts identify the relevant package and preserve the underlying resolver
  context.
- A failed build leaves the existing image and running or stopped project
  container untouched.
- `coop up` and entry do not substitute a different or stale image when the
  desired image is absent. They instruct the user to run `coop rebuild`.
- After a successful rebuild, the next entry recreates a mismatched container,
  warns that undeclared root-filesystem changes will be discarded, and
  preserves named state volumes.
- `coop status` reports the running image, desired image, and whether a rebuild
  is required.
- User-command precedence is project `.flox`, locked Coop core, configured
  tools, then the image's operating-system fallback paths.

## Configuration Migration

`[tools].packages` replaces `[image].extra_packages`.

For one beta release, global `[image].extra_packages` remains an alias for
`[tools].packages` and emits a deprecation warning. Defining both is an error
because silent merging would conceal migration mistakes. Project
`[image].extra_packages` remains invalid; repositories use `[tools].packages`.

Legacy flake installables such as `ref#attr` are intentionally unsupported in
both fields. Migration errors and documentation direct users to plain Nixpkgs
attribute paths resolved from Coop's pinned source.

After the compatibility window, `image.extra_packages` becomes an unknown-key
configuration error.

## Security Model

Repository tool declarations reduce ambient capability and make intended guest
contents auditable. They do not strengthen the VM isolation boundary: project
code already runs as guest root and may obtain additional software through the
network.

The repository-controlled surface is nevertheless constrained:

- only package attribute paths are accepted;
- the package source and revision are release-controlled;
- declarations cannot run host commands or inject shell syntax;
- builds execute through Apple's isolated container builder;
- maintenance operations do not inherit repository-controlled PATH entries;
  and
- package count and identifier size are bounded.

## Verification

Automated tests cover:

- global and project package merging, sorting, and deduplication;
- additive-only behavior and core immutability;
- package identifier validation and injection resistance;
- the effective package-count bound;
- legacy `image.extra_packages` migration and conflict errors;
- stable image identity regardless of declaration order;
- identity changes for core manifest, lock, source revision, or package changes;
- generated build input using argument-safe package-manager input;
- core precedence over configured packages;
- failure of the complete image build when any configured package fails;
- missing, unsupported, and conflicting package failures;
- no container teardown when the replacement image is unavailable;
- maintenance-plane PATH isolation;
- configured-tool visibility for user commands;
- project Flox precedence and argument preservation;
- bare-shell activation with and without project Flox;
- deprecation warnings on both direct and TUI-driven entry; and
- unchanged behavior for repositories without `.flox`.

The real-hardware release smoke test must:

1. build the stock image through Apple `container`;
2. verify every promised core command;
3. build and run an image with a repository-defined package;
4. enter both Flox and non-Flox projects;
5. confirm a project Flox tool overrides the lower layers for user commands;
6. confirm Coop maintenance remains on the controlled core PATH; and
7. confirm state-volume survival and manual-rootfs loss during recreation.

## Logical Commit Sequence

Every commit must pass the relevant Go checks and leave the branch usable.

1. **Design:** add this approved architecture and migration contract.
2. **Configuration:** add `[tools].packages`, validation, trusted/untrusted
   merging, and the temporary `image.extra_packages` alias with tests.
3. **Core environment:** embed the locked Flox environment, establish the core
   package set, and remove Node as a promised baseline runtime.
4. **Image construction:** generate the configured tool profile safely, include
   all effective inputs in image identity, and preserve build failure safety.
5. **Runtime layering:** separate maintenance and user-command environments,
   activate the core environment consistently, and layer optional project Flox.
6. **Operational UX:** report effective tools and desired/running image state,
   improve rebuild guidance, and add recreation warnings.
7. **Documentation and smoke coverage:** document the dependency boundary,
   configuration, mutability, migration, and release validation procedure.

Before implementation commits begin, the working branch must incorporate the
credential-wrapper fix already merged into `origin/main`. Existing unrelated
worktree changes must remain separate.
