# Configuration

`coop.toml` is Coop's project interface. User configuration supplies trusted
host integrations; project configuration declares the resources, tools, and
agent state a repository needs.

Start from the maintained examples:

- [trusted user configuration](../examples/coop.user.toml)
- [repository-controlled project configuration](../examples/coop.project.toml)

Both examples are loaded by the production configuration parser in the test
suite.

## Loading and precedence

Coop loads configuration in this order:

1. built-in defaults;
2. `$XDG_CONFIG_HOME/coop/coop.toml`, when `XDG_CONFIG_HOME` is non-empty, otherwise
   `~/.config/coop/coop.toml`;
3. `<project-root>/coop.toml`.

Missing files are allowed. Unknown keys, malformed values, and invalid
combinations are errors. The project file also marks a
[project boundary](runtime.md#project-selection).

Later layers override or extend earlier layers according to the key-specific
merge rules below. This is not an unrestricted TOML overlay: sensitive
host-facing settings only take effect from trusted user configuration.

## Trust boundary

The user file is controlled by the person running Coop. The project file may
come from an untrusted checkout.

| Setting | User file | Project file |
| --- | --- | --- |
| `image.name` | Effective | Parsed but ignored |
| deprecated `image.extra_packages` | Effective with a warning | Rejected |
| `tools.packages` | Additive | Additive |
| `[[tools.github_release]]` | Additive | Rejected |
| `resources` | Effective | Effective within project caps |
| `agents` | Keyed merge | Keyed merge |
| `ssh` | Effective | Parsed but ignored |
| `[[seed]]` | Effective | Validated but ignored |
| `credentials` | Effective | Validated but ignored |
| `include_credentials` | Effective | Parsed but ignored |

“Ignored” means the project does not receive the capability. These keys are
still recognized TOML, and some are validated, so malformed input may still
fail configuration loading. Do not rely on ignored project settings as
documentation; put trusted settings in the user file.

## When changes take effect

| Change | Required action |
| --- | --- |
| `tools.packages`, `tools.github_release`, `image.name`, or Coop's embedded image inputs | Run `coop rebuild`; the next `coop up` or entry recreates the container |
| `resources`, `agents`, or `ssh` | The next `coop up` or interactive entry recreates the container |
| `seed` | Applied on the next `coop up` or interactive entry |
| `credentials` or `include_credentials` | Applied on the next interactive entry |
| project `.flox` | Activated on the next entry from a governed directory |

`coop status` reports whether the desired image needs a build and whether the
existing container needs recreation. Recreation preserves named agent-state
volumes and discards undeclared changes to the container root filesystem.

## Reference

### `image`

```toml
[image]
name = "coop:latest"
```

| Property | Value |
| --- | --- |
| Type | String |
| Default | `"coop:latest"` |
| Layer | User only; project values are ignored |
| Merge | A non-empty user value replaces the default |
| Effect | Image rebuild, then container recreation |

`image.name` supplies the base local image reference used to name Coop's
derived image. It does not replace the embedded Containerfile or locked core
environment. The derived `local-...` tag incorporates the image name, embedded
image fingerprint, pinned package source, and effective tool set.

For one beta compatibility window, user configuration may use:

```toml
[image]
extra_packages = ["shellcheck"]
```

`image.extra_packages` is deprecated. It prints a warning, cannot be combined
with `tools.packages`, and is rejected in project configuration. Migrate it to
`[tools].packages`.

### `tools`

```toml
[tools]
packages = ["actionlint", "nodePackages.prettier"]
```

| Property | Value |
| --- | --- |
| Type | Array of strings |
| Default | Empty |
| Layer | User and project |
| Merge | User and project lists are combined, deduplicated, and sorted |
| Effect | Image rebuild, then container recreation |

Each value is a plain attribute path from the immutable Nixpkgs revision pinned
by the installed Coop release. Valid examples include `gh`, `shellcheck`,
`nodePackages.prettier`, and `python313Packages.ruff`.

Values may contain alphanumeric characters, `_`, `+`, `-`, and dot-separated
attribute components. They may not contain URLs, filesystem paths, shell
syntax, or flake references such as `nixpkgs#gh`. Each value is limited to 128
bytes, and the effective unique set is limited to 64 packages.

Configured tools are additive. The command lookup order is:

1. an explicitly activated project `.flox`, when present;
2. Coop's locked core tools;
3. configured `tools.packages`;
4. trusted GitHub release tools;
5. operating-system fallback paths from the image.

The core wins collisions with every configured tool, and a Nix package wins a
collision with a GitHub release tool. Use a project `.flox` when the repository
deliberately needs its own runtime version to win command lookup or wants the
same environment outside Coop.

#### `tools.github_release`

```toml
[[tools.github_release]]
name = "kata"
repo = "kenn-io/kata"
tag = "latest"
asset = "kata_{version}_linux_arm64.tar.gz"
binary = "kata"

[[tools.github_release]]
name = "roborev"
repo = "kenn-io/roborev"
tag = "vX.Y.Z"
asset = "roborev_{version}_linux_arm64.tar.gz"
binary = "roborev"
```

| Key | Meaning |
| --- | --- |
| `name` | Lowercase command name installed on guest `PATH` |
| `repo` | Public GitHub repository as `owner/name` |
| `tag` | Exact release tag or `"latest"` |
| `asset` | Exact `.tar.gz` asset name after placeholder expansion |
| `binary` | Normalized relative path to the executable inside the archive |

This repeated table is accepted only in trusted user configuration. It is for
personal Linux commands that are not available from Coop's pinned Nixpkgs
revision. Project configuration cannot select downloadable executables.

`asset` may contain `{tag}` and `{version}`. `{tag}` expands to the resolved
tag verbatim; `{version}` removes one leading `v`. No other placeholder,
archive format, arbitrary URL, source build, authentication, or install hook is
supported. Names must be unique across at most 32 declarations.

`coop rebuild` queries the public GitHub Releases API, resolves `latest` or the
exact tag, requires exactly one matching asset with a GitHub-provided
`sha256:` digest, downloads only through GitHub HTTPS hosts, verifies the
digest, and safely extracts exactly one configured regular file. The binary is
cached by digest and copied into the locally built image.

The resolved tag and digest are stored in
`$XDG_STATE_HOME/coop/release-tools.lock`, or
`~/.local/state/coop/release-tools.lock` when that variable is unset. Cached
archives and binaries live below `$XDG_CACHE_HOME/coop/release-tools`, falling
back to the platform user cache directory. After a successful rebuild, Coop
prunes unreferenced digest entries that have been inactive for at least one
hour; the grace period protects concurrent rebuilds. Normal entry and
`coop status` use the lock without network access. Changing a declaration invalidates the lock;
run `coop rebuild` to resolve it again. With `"latest"`, only a later rebuild
checks for a newer release. Use an exact tag for repeatable resolution.
If the derived lock is malformed or inconsistent, Coop ignores it with a
warning and treats the tools as unresolved. Run `coop rebuild` to replace the
invalid lock; ordinary entry will not reuse a previously resolved image while
the lock is invalid.

### `resources`

```toml
[resources]
cpus = 6
memory = "12G"
```

| Key | Type | Default | Merge |
| --- | --- | --- | --- |
| `cpus` | Positive integer | `4` | Last non-zero configured value |
| `memory` | String | `"8G"` | Last non-empty configured value |

Resources may be set in either layer and cause container recreation. Memory is
a positive whole number followed by `G` or `M`, such as `"8G"` or `"512M"`.
Project configuration is capped at 8 CPUs and 16 GB (or 16384 MB). Trusted user
configuration must still be positive but is not subject to those project caps.

### `agents`

```toml
[agents.gemini]
state = "~/.gemini"
```

Each agent entry declares one persistent guest directory. Coop mounts a named
volume at that path, isolated by project and agent name.

Built-in defaults are:

| Agent | State |
| --- | --- |
| `opencode` | `~/.local/share/opencode` |
| `claude` | `~/.claude` |
| `codex` | `~/.codex` |

Agent tables merge by name across both layers. A later entry replaces the
state for that name. An empty state removes an inherited agent:

```toml
[agents.codex]
state = ""
```

Agent changes cause container recreation. Removing an agent stops mounting its
volume but does not immediately delete an older volume. `coop destroy` removes
every volume belonging to the project, including volumes created under older
agent configuration.

Names must:

- start with a lowercase letter or digit;
- contain only lowercase letters, digits, and single hyphens;
- contain no consecutive `--`;
- be at most 63 characters.

State must start with `~/`, name a directory below the guest home, contain no
`:` character, and stay confined after path normalization. Effective state
directories may not duplicate, contain, or be contained by another configured
agent state. At most 32 agents may be effective.

### `ssh`

```toml
ssh = true
```

| Property | Value |
| --- | --- |
| Type | Boolean |
| Default | `false` |
| Layer | User only; project values are ignored |
| Merge | Enabling it in user configuration turns it on |
| Effect | Container recreation |

This forwards the host SSH-agent socket, not private-key files. It lets guest
processes request signatures and authentication from the host agent while the
guest has network access. Enable it deliberately.

### `seed`

Seeds copy trusted host files or directories into the guest whenever Coop
starts or enters the project.

```toml
[[seed]]
src = "~/.config/example/config.toml"
dest = "~/.config/example/config.toml"
policy = "always"
```

| Key | Type | Default |
| --- | --- | --- |
| `src` | String | Required for a useful rule |
| `dest` | String | Same as `src` |
| `policy` | String | `"always"` |

Seeds only take effect from user configuration. A leading `~/` in `src`
expands against the host home; in `dest` it expands against the guest home.
The homes have the same absolute path inside and outside Coop. Absolute guest
destinations such as `/usr/local/bin/example-tool` are also supported.

Policies are:

| Policy | Behavior |
| --- | --- |
| `always` | Copy a file on every application, replacing the guest file |
| `if-absent` | Copy a file only when the guest destination does not exist |
| `overlay` | Merge a directory tree, adding and replacing without deleting guest-only files |

Missing file sources are skipped. Missing or non-directory overlay sources are
also skipped. Host-side symlinks are followed so Stow-managed sources work.
File destinations reject symlinks, non-regular files, and symlinked parent
paths. These checks reduce redirection into the mounted project, but a
concurrently running guest can still race a check and write.

Do not use `overlay` for credentials or other sensitive data. Overlay
extraction may follow symlinks already present inside the guest destination.
Use [session credentials](#credentials) for secrets.

### `credentials`

Credential grants are named, trusted user definitions. A grant separates
host-side acquisition (`source`) from guest-side exposure (`inject`):

```toml
[credentials.github]
source = { type = "command", argv = ["gh", "auth", "token"] }
inject = { type = "environment", name = "GH_TOKEN" }
```

Project credential tables are validated but ignored. Up to 32 grants may be
defined, and up to 16 unique grants may be selected for one entry. Grant names
follow the same 63-character lowercase naming grammar as agents.

#### Sources

| Source type | Fields | Behavior |
| --- | --- | --- |
| `file` | `path` | Reads a regular host file; path must be absolute or start with `~/` |
| `command` | `argv` | Executes argv directly on the host and treats stdout as secret |
| `aws-profile` | `profile` | Runs the host AWS CLI's credential export for the named profile |

A source must not include fields belonging to another source type. File paths
are resolved without following a final symlink through the open operation and
may not enter the project through lexical or resolved paths. Command paths that
contain `/` must be absolute; bare names resolve through a sanitized host
`PATH`. Commands run from the trusted host home with a restricted environment,
not from the project directory, and have a ten-minute timeout.

Each acquired payload is limited to 1 MiB; the complete guest bundle is limited
to 8 MiB.

#### Injections

| Injection type | Fields | Compatible source |
| --- | --- | --- |
| `environment` | `name` | `file` or `command` |
| `file` | `path_env` | `file` or `command` |
| `git-credential-store` | None | `file` |
| `aws` | None | `aws-profile` |

Environment names and `path_env` values must be valid shell environment
identifiers. Selected grants may not claim the same injected environment or
specialized interface. Direct environment values have one trailing LF or CRLF
removed, then must contain no NUL, carriage return, or newline. Each value is
limited to 64 KiB, and the serialized injected environment across selected
grants is limited to 256 KiB.

`require_expiration = true` is valid only for `aws-profile`, the current source
that reports expiration. It rejects credentials without valid, unexpired
expiration metadata.

Selected material is acquired before Coop changes VM state. Coop stages it in a
mode-0700 directory under the guest's `/dev/shm/coop-credentials`, exposes it
through the launched interactive command's configured interface, and removes
the lease when that command exits. Other guest-root processes can read or copy
staged material, and cleanup cannot revoke a copy they retain. Secrets are not
stored in container arguments, labels, mounts, project files, seeds, or named
volumes.

### `include_credentials`

```toml
include_credentials = ["git"]
```

This top-level user-only array selects grants for every interactive entry. A
project value is ignored. Names must refer to defined grants. Duplicates are
removed while preserving order.

The `--credentials` flag adds grants for one entry:

```sh
coop --credentials github codex
coop --credentials aws-dev,kubernetes opencode
```

Defaults are followed by explicit selections, then the combined list is
deduplicated. The flag may be repeated and also applies to `coop tui`. Commands
that do not enter the guest, such as `coop up`, reject it.

## Recipes

### Add a guest package

For a repository requirement:

```toml
# <project-root>/coop.toml
[tools]
packages = ["actionlint"]
```

For a personal tool used across projects, put the same table in the user file.
Then build and enter:

```sh
coop rebuild
coop
```

### Choose between tools and Flox

Use `[tools].packages` for additive Linux commands available from Coop's pinned
Nixpkgs. Use trusted `[[tools.github_release]]` for a personal, public,
prebuilt Linux arm64 command absent from that revision. Use a project `.flox`
when the project needs pinned runtime versions or the same environment inside
and outside Coop. Flox is optional; Coop detects and activates the nearest
`.flox` between the current directory and project root.

### Install a public GitHub release tool

Put this in trusted user configuration, not the repository:

```toml
[[tools.github_release]]
name = "roborev"
repo = "kenn-io/roborev"
tag = "latest"
asset = "roborev_{version}_linux_arm64.tar.gz"
binary = "roborev"
```

Then run `coop rebuild`. `latest` is convenient for a personal tool; replace it
with an exact release tag when reproducibility matters. The release must
publish a Linux arm64 `.tar.gz` asset and expose its SHA-256 digest through the
GitHub Releases API.

### Install a self-contained executable

```toml
# Trusted user configuration
[[seed]]
src = "~/bin/example-tool"
dest = "/usr/local/bin/example-tool"
policy = "always"
```

The source must be a script with a guest-available interpreter or a Linux
binary for the guest architecture. A macOS Mach-O executable will not run in
the Linux guest.

If a script needs another command, declare that dependency separately:

```toml
[tools]
packages = ["jq"]

[[seed]]
src = "~/bin/example-tool"
dest = "/usr/local/bin/example-tool"
policy = "always"
```

Changing only the seed applies on the next entry. Changing `tools.packages`
also requires `coop rebuild`.

### Install a GitHub CLI extension

GitHub CLI discovers extensions by filesystem layout. A self-contained
extension executable can be seeded without a Coop-specific installer:

```toml
[[seed]]
src = "~/bin/gh-example"
dest = "~/.local/share/gh/extensions/gh-example/gh-example"
policy = "always"
```

Add `gh` or other Linux dependencies through `[tools].packages` when they are
not already part of Coop's core environment. A command merely present on
`PATH` is not automatically registered as `gh <name>`; it must use GitHub
CLI's extension directory and naming convention.

### Share a directory without replacing guest-only files

```toml
[[seed]]
src = "~/.config/example/plugins"
policy = "overlay"
```

Use this only for non-sensitive content that the guest is allowed to read.

### Remove persistent agent state

```toml
[agents.codex]
state = ""
```

The next entry recreates the container without that mount. Run `coop destroy`
when the old volume should also be deleted.

### Enable SSH or temporary credentials

Use `ssh = true` when a tool specifically needs the host SSH agent. Prefer a
named credential grant when the tool can accept a temporary environment value,
file path, Git credential store, or AWS credentials file. This narrows exposure
to one interactive command.

See the [security model](security-model.md) for the boundary and remaining
risks.
