# Configuration

Coop has two machine-local configuration files:

1. `~/.config/coop/coop.toml` applies across projects. When
   `XDG_CONFIG_HOME` is set, Coop uses `$XDG_CONFIG_HOME/coop/coop.toml`.
2. `<project-root>/.coop.toml` applies to one checkout and loads last.

Both files have full local authority. Keep `.coop.toml` Git-ignored; Coop
refuses to load it when Git tracks it. Missing files are fine, but unknown keys
and invalid combinations are errors.

Start with the parser-tested [machine-wide example](../examples/coop.user.toml)
and [project example](../examples/coop.project.toml).

## Merge rules

Later scalar values replace earlier values. Lists are generally combined,
deduplicated, and validated. Named `agents` and `credentials` tables merge by
name, so a project definition replaces a machine-wide definition with the same
name.

The main exceptions are:

- `volume` and `publish` are accepted only in the project `.coop.toml`;
- an agent with `state = ""` removes that inherited agent;
- all matching `[[projects]]` scopes contribute credentials and seeds.

## When changes take effect

| Setting | What to do |
| --- | --- |
| `tools`, `image` | Run `coop rebuild`, then enter or run `coop up` |
| `resources`, `agents`, `ssh`, `mount`, `volume`, `publish` | Enter or run `coop up`; Coop recreates the container when needed |
| `seed` | Enter or run `coop up` |
| `credentials`, `include_credentials`, `projects` | Start the next interactive entry |
| Project `.flox` | Start the next entry from a directory it governs |

`coop status` shows whether an image rebuild or container recreation is
pending. Recreation preserves Coop-owned named volumes and discards undeclared
changes to the container root filesystem.

## Tools

Add Nixpkgs packages to the machine-wide file for personal tools or to the
project file for project-specific tools:

```toml
[tools]
packages = ["actionlint", "nodejs_22", "python313Packages.ruff"]
```

Package lists from both files are combined, deduplicated, and sorted. Values
must be plain attribute paths from Coop's pinned Nixpkgs source; URLs, paths,
shell expressions, and flake references such as `nixpkgs#gh` are rejected. At
most 64 unique packages may be configured.

After changing packages:

```sh
coop rebuild
```

Use a project `.flox` when the repository needs pinned runtimes or the same
environment inside and outside Coop. Flox has command precedence over Coop's
core and configured tools.

### Public GitHub release tools

For a self-contained Linux arm64 command unavailable from the pinned Nixpkgs:

```toml
[[tools.github_release]]
name = "roborev"
repo = "kenn-io/roborev"
tag = "latest"
asset = "roborev_{version}_linux_arm64.tar.gz"
binary = "roborev"
```

`repo` is `owner/name`. `tag` is an exact tag or `latest`. `asset` is a
`.tar.gz` filename and may use `{tag}` or `{version}`; `binary` is the
normalized relative path to the executable inside that archive. Names must be
unique, and at most 32 tools may be declared.

`coop rebuild` downloads the public release asset, requires a GitHub-provided
SHA-256 digest, verifies it, and installs the selected executable in the local
image. Use an exact tag when repeatability matters.

## Resources and image name

```toml
[resources]
cpus = 6
memory = "12G"

[image]
name = "coop:latest"
```

The defaults are 4 CPUs, `"8G"` memory, and image name `"coop:latest"`.
Memory is a positive whole number followed by `G` or `M`. A later configured
resource or non-empty image name replaces the earlier value.

## Agent state

Coop's built-in agents store their project-specific state in named volumes:

| Agent | Default state directory |
| --- | --- |
| `codex` | `~/.codex` |
| `claude` | `~/.claude` |
| `opencode` | `~/.local/share/opencode` |

Add another agent or change its state directory:

```toml
[agents.gemini]
state = "~/.gemini"
```

Remove an inherited agent mount:

```toml
[agents.codex]
state = ""
```

State paths must be below the guest home and may not overlap another agent's
state or an additional host mount. Removing an agent detaches its existing
volume; `coop destroy` deletes all volumes owned by that project.

## Seeds

Seeds copy explicit, trusted host files or directories into the guest:

```toml
[[seed]]
src = "~/.agents/AGENTS.md"
policy = "if-absent"
```

`dest` defaults to `src`. A host `~/` expands against the host home and a guest
`~/` destination expands against the guest home, which uses the same absolute
path.

| Policy | Behavior |
| --- | --- |
| `always` | Replace a file on every application; this is the default |
| `if-absent` | Copy a file or directory only when the destination is absent |
| `overlay` | Merge a directory without deleting guest-only files |

Missing sources are skipped, allowing one machine-wide file to cover multiple
hosts. Host symlinks are followed, so Stow-managed sources work.

Seed portable agent instructions and preferences, not login stores or secret
files. There is no guest-to-host synchronization. For entry-scoped secrets,
use [credential grants](credentials.md).

To install a trusted script or self-contained Linux executable on guest
`PATH`:

```toml
[[seed]]
src = "~/bin/example-tool"
dest = "/usr/local/bin/example-tool"
policy = "always"
```

Declare any commands that script needs under `[tools].packages`.

## Credentials

A named credential separates host acquisition from one or more guest
exposures:

```toml
[credentials.github-example]
source = { type = "git-credential", url = "https://github.com/example" }
expose = [
  { type = "git-credential-store" },
  { type = "environment", name = "GH_TOKEN", field = "password" },
]
```

Credentials are not active merely because they are defined. Authorize a grant
globally, for selected paths, or for one command:

```toml
[[projects]]
match = "~/Projects/example"
include_credentials = ["github-example"]
```

```sh
coop --credentials github-example codex
```

Use the [credential guide](credentials.md) for GitHub setup and rotation,
valid source/exposure combinations, verification, and troubleshooting. The
[security model](security-model.md#session-credentials) documents the trust
boundary and residual risk.

## Project scopes

Machine-wide `[[projects]]` blocks bind credentials or seeds to a path:

```toml
[[projects]]
match = "~/Projects/work"
include_credentials = ["github-work"]
```

`match` is a canonical path prefix, not a glob. Matching occurs at directory
boundaries, so `~/Projects/foo` does not match `~/Projects/foobar`. It also
matches nested bare-repository worktrees. All overlapping blocks apply.

A missing match path simply does not match, but every credential name must be
defined even when its block does not match the current project.

Top-level `include_credentials` selects grants for every project:

```toml
include_credentials = ["credential-needed-everywhere"]
```

Prefer a `[[projects]]` scope for account credentials so unrelated projects do
not receive them.

## Linux-only project volumes

macOS and Linux cannot safely share generated trees containing native
artifacts, even when runtime versions match. Put such a directory in a
guest-only persistent volume:

```toml
# .coop.toml
[[volume]]
path = "web/node_modules"
```

The path is relative to the project. The host tree remains separate, while the
guest sees an initially empty Coop-owned volume at the same project path.
Install dependencies once on each platform.

Paths must be normalized, confined beneath the project, and free of symlinks or
overlap with another project volume. The final directory may be absent when its
parent exists. Volumes survive `down`, `up`, and container recreation. Removing
the declaration detaches a volume; `coop destroy` deletes it. At most 64 may be
declared.

## Published ports

Publish a guest TCP service to host loopback:

```toml
# .coop.toml
[[publish]]
guest_port = 5173
host_port = 5173
```

`host_port` defaults to `guest_port`. Both must be from 1 through 65535, and a
host port can map to only one guest port. The guest service must listen on
`0.0.0.0:<guest_port>`; host clients connect to
`127.0.0.1:<host_port>`. At most 32 mappings may be declared.

## Additional host mounts

Expose an existing host directory at the identical absolute path in the guest:

```toml
# .coop.toml
mount = [
  { source = "~/Projects/example/homebrew-tap" },
  { source = "~/Documents/reference", access = "read-write" },
]
```

Mounts default to `read-only`. Use `read-write` only when guest code must modify
the host directory. Coop rejects missing sources and broad or overlapping
mounts, including the host home, selected project, and agent state paths.

Project-specific mounts belong in the ignored `.coop.toml`. A machine-wide
mount is inherited by every project.

## SSH agent forwarding

```toml
ssh = true
```

The default is `false`. This forwards the host SSH-agent socket, not private-key
files, and lets every guest-root process request signatures while the guest has
network access. Prefer a narrowly selected credential grant when a tool can use
one. See the [security model](security-model.md#ssh-agent-forwarding).
