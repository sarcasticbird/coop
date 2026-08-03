# Configuration

Coop has two machine-local configuration layers: a machine-wide `coop.toml`
under the user's config directory and an ignored `.coop.toml` in each project.

Start from the maintained examples:

- [machine-wide configuration](../examples/coop.user.toml)
- [machine-local project configuration](../examples/coop.project.toml)

Both examples are loaded by the production configuration parser in the test
suite.

## Loading and precedence

Coop loads configuration in this order:

1. built-in defaults;
2. `$XDG_CONFIG_HOME/coop/coop.toml`, when `XDG_CONFIG_HOME` is non-empty, otherwise
   `~/.config/coop/coop.toml`;
3. `<project-root>/.coop.toml` as the project override and expansion layer.

Missing files are allowed. Unknown keys, malformed values, and invalid
combinations are errors. `.coop.toml` also marks a
[project boundary](runtime.md#project-selection). Coop does not load a
committed `<project-root>/coop.toml`.

Later layers override or extend earlier layers according to the key-specific
merge rules below.

## Migrating from 0.1.x

Version 0.2 stops loading a committed project `coop.toml`. For each project
that used one:

1. rename `<project-root>/coop.toml` to `<project-root>/.coop.toml`;
2. add `/.coop.toml` to the project's `.gitignore`;
3. record the tracked `coop.toml` deletion without adding the ignored dotfile;
4. review the local file because every supported setting is now effective.

Coop refuses to load a `.coop.toml` that Git tracks. This is an enforced trust
boundary, not only a repository convention: remove the file from the index
and keep the local copy ignored before invoking Coop. When a checkout is
present but Git cannot verify tracking state, Coop fails closed and reports the
inspection error. The check compares filesystem identities, so case aliases on
the default macOS filesystem cannot bypass it.

Additional mounts also move to the project file. Replace a machine-wide scope
like this:

```toml
[[projects]]
match = "~/Projects/sarcasticbird/wrap"
mount = [
  { source = "~/Projects/sarcasticbird/homebrew-tap", access = "read-write" },
]
```

with this project-local file:

```toml
# ~/Projects/sarcasticbird/wrap/.coop.toml
mount = [
  { source = "~/Projects/sarcasticbird/homebrew-tap", access = "read-write" },
]
```

Machine-wide `[[projects]]` scopes remain available for credentials and seeds
that intentionally apply across several projects. The next `coop up` or entry
recreates an existing container when the effective mount set changes; named
agent-state volumes are preserved.

## Trust boundary

Both files are user-owned, machine-local configuration and have the same
authority. Keep every project `.coop.toml` Git-ignored: it may contain private
paths, credential selectors, executable sources, and other host-specific
configuration that must not enter a repository. Coop rejects the project file
when it is tracked by the repository containing it.

The selected project is mounted read-write, so guest code can modify its
`.coop.toml`. Such changes do not affect the running container, but the next
host-side Coop invocation will load them. Review local configuration after
running code you do not trust.

## When changes take effect

| Change | Required action |
| --- | --- |
| `tools.packages`, `tools.github_release`, `image.name`, or Coop's embedded image inputs | Run `coop rebuild`; the next `coop up` or entry recreates the container |
| `resources`, `agents`, `ssh`, or `mount` | The next `coop up` or interactive entry recreates the container |
| `seed` | Applied on the next `coop up` or interactive entry |
| `credentials` or `include_credentials` | Applied on the next interactive entry |
| `[[projects]]` | Applied on the next `coop up` or interactive entry |
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
| Layer | Machine and project |
| Merge | A later non-empty value replaces the earlier value |
| Effect | Image rebuild, then container recreation |

`image.name` supplies the base local image reference used to name Coop's
derived image. It does not replace the embedded Containerfile or locked core
environment. The derived `local-...` tag incorporates the image name, embedded
image fingerprint, pinned package source, and effective tool set.

For one beta compatibility window, either configuration layer may use:

```toml
[image]
extra_packages = ["shellcheck"]
```

`image.extra_packages` is deprecated. It prints a warning and cannot be
combined with `tools.packages`. Migrate it to `[tools].packages`.

### `tools`

```toml
[tools]
packages = ["actionlint", "nodePackages.prettier"]
```

| Property | Value |
| --- | --- |
| Type | Array of strings |
| Default | Empty |
| Layer | Machine and project |
| Merge | Machine and project lists are combined, deduplicated, and sorted |
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
4. configured GitHub release tools;
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

This repeated table installs Linux commands that are not available from Coop's
pinned Nixpkgs revision. It may be declared machine-wide or for one project.

`asset` may contain `{tag}` and `{version}`. `{tag}` expands to the resolved
tag verbatim; `{version}` removes one leading `v`. No other placeholder,
archive format, arbitrary URL, source build, authentication, or install hook is
supported. Names must be unique across at most 32 declarations.

`coop rebuild` queries the public GitHub Releases API, resolves `latest` or the
exact tag, requires exactly one matching asset with a GitHub-provided
`sha256:` digest, downloads only through GitHub HTTPS hosts, verifies the
digest, and safely extracts exactly one configured regular file. The binary is
cached by digest and copied into the locally built image.

The resolved tag and digest are stored by declaration fingerprint below
`$XDG_STATE_HOME/coop/release-tools/`, or
`~/.local/state/coop/release-tools/` when that variable is unset. Separate
declaration sets therefore keep separate locks instead of overwriting one
machine-wide lock. A hashed project reference records which fingerprint each
project most recently rebuilt; changing or removing that project's declarations
replaces or removes the reference so stale locks and cache entries become
collectible. Cached archives and binaries live below
`$XDG_CACHE_HOME/coop/release-tools`, falling back to the platform user cache
directory. After a successful rebuild, Coop prunes digest entries that are not
referenced by any saved lock, plus incomplete downloads that have been inactive
for at least one hour; the grace period protects concurrent rebuilds.
Normal entry and `coop status` use the lock without network access. Changing a
declaration invalidates the lock; run `coop rebuild` to resolve it again. With
`"latest"`, only a later rebuild checks for a newer release. Use an exact tag
for repeatable resolution.
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
| Layer | Machine and project |
| Merge | A later explicitly configured value replaces the earlier value |
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

Seeds may be declared in either layer. A leading `~/` in `src` expands against
the host home; in `dest` it expands against the guest home.
The homes have the same absolute path inside and outside Coop. Absolute guest
destinations such as `/usr/local/bin/example-tool` are also supported.

Policies are:

| Policy | Behavior |
| --- | --- |
| `always` | Copy a file on every application, replacing the guest file |
| `if-absent` | Copy a file or complete directory only when the guest destination does not exist |
| `overlay` | Merge a directory tree, adding and replacing without deleting guest-only files |

Missing sources are skipped. Missing or non-directory overlay sources are also
skipped. Host-side symlinks are followed so Stow-managed sources work.

File writes use an atomic temporary file and reject symlink, non-regular, and
symlinked-parent destinations. Directory `if-absent` extracts into a random
sibling staging directory, then publishes the complete tree without replacing
an existing destination. These checks reduce redirection into the mounted
project, but a concurrently running guest can still race a check and write.

Do not use `overlay` for credentials or other sensitive data. Overlay
extraction may follow symlinks already present inside the guest destination.
Prefer [session credentials](#credentials) for secrets when the tool supports
an entry-scoped interface. Native clients that require durable interactive
login state should create it inside their project-specific agent volume.

### `credentials`

Credential grants are named, machine-local definitions. A grant separates
host-side acquisition (`source`) from one or more guest projections (`expose`):

```toml
[credentials.github-sarcasticbird]
source = { type = "git-credential", url = "https://github.com/sarcasticbird" }
expose = [
  { type = "git-credential-store" },
  { type = "environment", name = "GH_TOKEN", field = "password" },
]
```

Up to 32 grants may be defined, and up to 16 unique grants may be selected for
one entry. Grant names follow the same 63-character lowercase naming grammar
as agents.

#### Sources

| Source type | Fields | Behavior |
| --- | --- | --- |
| `file` | `path` | Reads a regular host file; path must be absolute or start with `~/` |
| `command` | `argv` | Executes argv directly on the host and treats stdout as secret |
| `git-credential` | `url` | Runs host `git credential fill` for one fixed HTTPS URL and returns username/password material |
| `aws-profile` | `profile` | Runs the host AWS CLI's credential export for the named profile |

A source must not include fields belonging to another source type. File paths
are resolved without following a final symlink through the open operation and
may not enter the project through lexical or resolved paths. Command paths that
contain `/` must be absolute; bare names resolve through a sanitized host
`PATH`. Commands run from the trusted host home with a restricted environment,
not from the project directory, and have a ten-minute timeout.

A `git-credential` URL must use HTTPS, include a host, and contain no userinfo,
query, or fragment. Its optional path is part of the trusted grant. Coop sends
the normalized protocol, host, and path through Git's credential protocol; it
does not derive them from a repository remote. Host Git configuration and its
helper chain are trusted executable inputs. On macOS, the recommended helper
is `git-credential-osxkeychain`. Git merges request fields into the output from
`git credential fill`, so a returned path confirms that Git preserved the
configured request but does not attest which backend record supplied the
secret. A helper may intentionally ignore paths; backend record scope remains
trusted host configuration.

Each acquired payload is limited to 1 MiB; the complete guest bundle is limited
to 8 MiB.

#### Exposures

| Exposure type | Fields | Compatible source/material |
| --- | --- | --- |
| `environment` | `name` | Opaque `file` or `command` payload |
| `environment` | `name`, `field = "username"` or `"password"` | `git-credential` |
| `file` | `path_env` | Opaque `file` or `command` payload |
| `git-credential-store` | None | `git-credential`, or legacy opaque `file` payload |
| `aws` | None | `aws-profile` |

`expose` is an array and may project one acquisition into several interfaces.
The singular `inject = { ... }` table remains supported for existing
configuration as one exposure. Defining both, defining neither, or providing
an empty `expose` array is an error.

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

For the operational model, macOS Keychain setup, GitHub example, migration,
and cleanup checklist, read [Credentials](credentials.md).

### `include_credentials`

```toml
include_credentials = ["git"]
```

This top-level array selects grants for every interactive entry in its scope.
Names must refer to defined grants. Duplicates are removed while preserving
order.

The `--credentials` flag adds grants for one entry:

```sh
coop --credentials github codex
coop --credentials aws-dev,kubernetes opencode
```

Defaults are followed by explicit selections, then the combined list is
deduplicated. The flag may be repeated and also applies to `coop tui`. Commands
that do not enter the guest, such as `coop up`, reject it.

### `mount`

```toml
mount = [
  { source = "~/Projects/sarcasticbird/homebrew-tap", access = "read-write" },
]
```

Each entry exposes one existing host directory at the same canonical absolute
path inside the guest. `source` must be absolute or begin with `~/`. `access`
defaults to `read-only`; use `read-write` only when the guest must modify the
host directory. The filesystem root, host home, ancestors of the host home,
paths overlapping the selected project, and paths overlapping an enabled
agent's persistent state directory are refused. Disable the conflicting agent
or choose a separate mount. Sources containing `,` or `=` are also rejected
because Apple's CLI reserves those characters in its mount grammar. A missing
source is an error. A mount change recreates the container while preserving
agent-state volumes.

Project-specific mounts normally belong in the ignored project `.coop.toml`.
The machine-wide file may declare a mount when every project should receive it.

### `projects`

```toml
[[projects]]
match               = "~/Projects/sarcasticbird"
include_credentials = ["github-sarcasticbird"]
```

This machine-wide array binds grants and seeds to a subset of projects. Top-level
`include_credentials` and `[[seed]]` apply to every coop; a `[[projects]]` block
applies only where `match` matches. Use it to keep a forge token with the
organization it belongs to rather than handing it to every project you enter.

`match` is a **path prefix, not a glob.** A leading `~/` expands against the
host home, and both sides are canonicalized before comparison:

- comparison happens at path-component boundaries, so `~/Projects/foo` does not
  match `~/Projects/foobar`;
- a bare-repo worktree at `<org>/<repo>/main` still matches a block naming
  `<org>`, which a glob would miss because `*` does not cross a separator;
- symlinks resolve, so a block may name either side of a link;
- a `match` naming a path that does not exist on this machine simply does not
  match. It is not an error — one user file may describe several machines, the
  same reason a missing seed source is skipped rather than fatal.

Blocks union. Overlapping blocks all apply, matched grants merge with
account-level ones, and a project can never opt out of a grant it would
otherwise receive. Every `include_credentials` name must refer to a defined
grant even in a block that does not match, so a typo fails the load in any
directory rather than only inside the project it targets.

Scoped seeds behave exactly like top-level ones, including `dest` defaulting to
`src` and the `always` policy default.

## Recipes

### Mount a sibling project directory

Put the mount beside the project it expands, not in the machine-wide project
list:

```toml
# ~/Projects/sarcasticbird/wrap/.coop.toml
mount = [
  { source = "~/Projects/sarcasticbird/homebrew-tap", access = "read-write" },
]
```

Use `read-only` or omit `access` when the guest only needs to inspect the
directory. Run `coop status` to see whether the existing container needs
recreation, then run `coop up` when ready to adopt the new mount.

### Add a guest package

For a project-specific requirement:

```toml
# <project-root>/.coop.toml
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
Nixpkgs. Use `[[tools.github_release]]` for a personal, public,
prebuilt Linux arm64 command absent from that revision. Use a project `.flox`
when the project needs pinned runtime versions or the same environment inside
and outside Coop. Flox is optional; Coop detects and activates the nearest
`.flox` between the current directory and project root.

### Install a public GitHub release tool

Put this in machine-wide configuration or an ignored project `.coop.toml`:

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

### Initialize portable agent configuration and rules

Agent state already lives in a project-local named volume. Let the native agent
create login state there through its own interactive flow. Seed only portable,
non-secret configuration and rules:

```toml
[agents.codex]
state = "~/.codex"

[[seed]]
src = "~/.codex/config.toml"
policy = "if-absent"

[[seed]]
src = "~/.codex/rules"
policy = "if-absent"

[[seed]]
src = "~/.agents/AGENTS.md"
policy = "if-absent"
```

Every path is explicit; Coop does not maintain hidden Codex, Claude, or other
provider defaults. Missing sources are skipped. Do not seed `auth.json` or an
equivalent provider login store; `coop doctor` treats recognized sensitive
seed paths as failures. Recognized Git paths include `.git-credentials` and
custom `~/.config/git/credentials-*` stores. The check covers source and
destination paths in every configured `[[projects]]` scope, not only the scope
matching the current project.

Files under `~/.codex` persist in that project's Codex volume. Native login,
refresh, or logout changes only the project copy. A destination outside a
configured state volume, such as
`~/.agents/AGENTS.md`, lives in the disposable container root filesystem and
is copied again after container recreation.

The state-volume root itself already exists when seeds run, so `if-absent`
intentionally leaves it untouched. Select the child files and directories you
want rather than naming the complete `~/.codex` mount.

There is no synchronization or writeback. `coop destroy` removes the project
volume. Prefer [session credentials](#credentials) when a tool supports an
entry-scoped environment variable, file path, Git credential store, or AWS
profile.

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
