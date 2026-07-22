# coop

Run coding agents in a project-scoped Linux VM on Apple silicon.

coop uses Apple's [`container`](https://github.com/apple/container) runtime.
It mounts one project read-write at the same path it has on the host and keeps
agent state in volumes dedicated to that project. Docker is not required.

coop is pre-1.0. Command and configuration behavior may change between beta
versions.

## Requirements

- macOS 26 or later on Apple silicon
- Apple's `container` CLI and a running container service
- Go 1.26.5 or later to build coop

Flox is part of the guest image, not a host prerequisite. Repositories do not
need a `.flox` environment unless they want the same project toolchain inside
and outside Coop.

Install and start Apple's runtime:

```sh
brew install container
container system start
```

## Install

The beta is installed from source. If you have access to the source repository:

```sh
git clone https://github.com/sarcasticbird/coop.git
cd coop
mkdir -p "$HOME/.local/bin"
go build -trimpath -o "$HOME/.local/bin/coop" ./cmd/coop
```

`$HOME/.local/bin` must be on `PATH`. For the current shell:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Add that export to your shell startup file if it is not already configured
there.

Run `coop doctor` after installation. It uses trusted user configuration and
does not need to run from a project directory. It checks:

- whether the `container` CLI is on `PATH` and its API service responds;
- whether the configured local image is present;
- whether configured seed sources exist; and
- whether known credential paths should migrate out of seeds; and
- whether old coop containers or volumes need manual cleanup.

It does not validate the Go toolchain, host OS and architecture, agent login,
Flox environments, network restrictions, project configuration, or the VM
isolation boundary.

## Quick Start

From a project directory:

```sh
cd ~/Projects/my-app
coop rebuild
coop claude
```

The beta does not distribute a prebuilt sandbox image. `coop rebuild` builds
the embedded image definition locally. Run it once after installation and
again after changing `[tools].packages` or updating Coop to a version with
different embedded image inputs.

Running `coop` without a command opens a Zsh login shell in the VM. The embedded
image definition includes a locked operational workbench and the opencode,
Claude Code, and Codex agents. Application runtimes such as Node.js, Go, and
Python are intentionally project-owned rather than part of Coop's command
contract. See
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) before distributing an
image.

## Commands

```text
coop [command [args...]]  Run a command in the project VM
coop                      Open a shell in the project VM
coop up                   Create or start the project VM
coop down                 Stop it while preserving state volumes
coop status               Show VM and desired/running image state
coop ls                   List coop VMs
coop tui                  Open the VM dashboard
coop doctor               Check host and trusted user configuration
coop rebuild              Build the embedded image definition locally
coop destroy              Delete the VM and all of its state volumes
```

Arguments after the command name are passed through unchanged:

```sh
coop claude --help
coop codex --model o3
coop opencode run "fix the tests"
coop --credentials aws-dev,github codex
```

Coop flags must appear before the guest command. For example,
`coop --credentials github codex` selects a Coop credential grant, while
`coop codex --credentials github` passes both arguments to Codex unchanged.

`coop down` only stops the VM. `coop destroy` asks for confirmation, removes
the current project's container, and removes every state volume owned by that
container, including volumes left by an older agent configuration. It does not
remove the project directory or images. A failed volume removal is reported as
an error.

### Recovery Without Deleting State

If a container root filesystem is damaged, remove only the container with
Apple's CLI and let coop recreate it. The named agent-state volumes remain:

```sh
coop status                 # note the container name
container stop <name>
container rm <name>
coop up
```

Do not use `coop destroy` for this recovery path; it removes the state volumes.

## Project Selection

coop selects a project root in this order:

1. The nearest ancestor containing a regular `coop.toml` file.
2. The parent of a Git worktree when that parent contains a `.bare` directory.
3. The Git repository root.
4. The current directory.

The selected directory is mounted read-write at the identical path inside the
VM. coop refuses a root that is the filesystem root, the user's home directory,
or an ancestor of the home directory.

An empty `coop.toml` can mark a boundary that differs from the Git root, such
as a monorepo directory or a directory containing related worktrees.

## Configuration

Trusted user configuration is loaded from:

- `$XDG_CONFIG_HOME/coop/coop.toml` when `XDG_CONFIG_HOME` is non-empty; or
- `~/.config/coop/coop.toml` otherwise.

The selected project's `<project>/coop.toml` is then loaded. Unknown keys are
rejected. Resource values, agent names and state paths, agent count, overlapping
state targets, and seed policies are validated before runtime changes begin.

The project file is repository-controlled. `[resources]`, `[agents]`, and
additive `[tools]` entries from it take effect. `ssh`, `[image]`, `[[seed]]`,
`include_credentials`, and `[credentials]` settings take effect only from
trusted user configuration; placing them in a project file does not grant
those capabilities. Project resource requests are capped at 8 CPUs and 16 GB
of memory. Agent state paths must remain below the guest home.

### User Configuration

```toml
# $XDG_CONFIG_HOME/coop/coop.toml, or ~/.config/coop/coop.toml

ssh = false

[image]
name = "coop:latest"

[tools]
packages = ["bat"]

[resources]
cpus = 4
memory = "8G"

# Included on every interactive entry. Additional grants can be selected with
# --credentials before the guest command.
include_credentials = ["git"]

[credentials.git]
source = { type = "file", path = "~/.git-credentials" }
inject = { type = "git-credential-store" }

[credentials.github]
source = { type = "command", argv = ["gh", "auth", "token"] }
inject = { type = "environment", name = "GH_TOKEN" }

[credentials.kubernetes]
source = { type = "file", path = "~/.kube/config" }
inject = { type = "file", path_env = "KUBECONFIG" }

[credentials.aws-dev]
source = { type = "aws-profile", profile = "dev" }
inject = { type = "aws" }
require_expiration = true

[[seed]]
src = "~/.config/opencode/opencode.jsonc"
policy = "always"

[[seed]]
src = "~/.claude/skills"
policy = "overlay"
```

Seed policies are:

- `always`: copy a file on every entry, replacing the guest copy.
- `if-absent`: copy a file only when the guest destination is absent.
- `overlay`: merge a directory into the guest destination without deleting
  files that exist only in the guest.

`dest` defaults to `src`, and an omitted policy defaults to `always`. A leading
`~/` in `src` uses the host home; in `dest` it uses the guest home. The guest
home has the same path as the host home. Missing seed sources are skipped, and
host source symlinks are followed.

Do not use `overlay` for credentials or other sensitive data. Overlay
extraction can follow symlinks already present in the guest destination tree.
File seeding rejects symlink destinations and symlinked parent paths, but its
path check and write are separate operations; a concurrently running guest
process could change the path between them. Seed only data the guest is allowed
to read, and avoid seeding while untrusted guest processes are running.

### Session Credentials

A credential grant has two independent parts: `source` acquires material on
the host, and `inject` chooses the interface exposed to the guest command.
File, command, and AWS-profile sources can therefore support tools without
making the configuration AWS-specific. Command sources execute the configured
argv directly on the host; their stdout is treated as secret data. AWS-profile
sources run the host AWS CLI's `configure export-credentials` command, so the
host must provide `aws` and the named profile.

File source paths must be absolute or start with `~/`. Relative paths are
rejected so the current project cannot influence which host file is acquired.
Command executables must be absolute or resolved by name through `PATH`; paths
such as `./helper` are rejected. Host commands run from the trusted host home,
not the current project directory.

`include_credentials` is the ordered default set for every interactive entry.
The `--credentials` flag adds named grants for one entry, accepts commas, and
may be repeated:

```sh
coop --credentials aws-dev,github codex
coop --credentials kubernetes --credentials github opencode
```

Defaults and explicit selections are combined in order and deduplicated. The
flag also applies when entering through `coop tui`; commands that do not enter
the guest, such as `coop up`, reject it.

Coop acquires selected material before changing VM state, streams it through
stdin into a mode-0700 directory below `/dev/shm/coop-credentials`, exposes it
only to that interactive command, and removes the directory when the command
ends. Secret values are not placed in container arguments, labels, mounts,
persistent environment, project files, seeds, or named volumes. A source's
upstream validity is independent of the guest exposure lifetime: Coop does not
refresh credentials during a long-running entry or revoke credentials that the
source itself cannot revoke.

To migrate a Git credential-store seed, remove the `[[seed]]` entry for
`~/.git-credentials`, define `[credentials.git]` as above, and keep `git` in
`include_credentials` if it should be available on every entry. Seeds remain
appropriate for durable, non-secret configuration and application state;
session credentials are the preferred path for narrowly scoped material that
a tool can consume through an environment variable, temporary file, Git
credential helper, or AWS shared-credentials file. `coop doctor` warns about
several common credential paths that are still configured as seeds.

### Project Configuration

```toml
# <project>/coop.toml

[resources]
cpus = 6
memory = "12G"

[tools]
packages = ["actionlint", "shellcheck"]

[agents.codex]
state = "" # remove the default Codex state volume
```

The default persistent state directories are `~/.local/share/opencode` for
opencode, `~/.claude` for Claude Code, and `~/.codex` for Codex. Each is backed
by a volume unique to the project. An agent entry with an empty `state` removes
that volume from new VM specifications; `coop destroy` also removes volumes
from older specifications.

### Images and Additional Packages

`image.name` is the base for the local image reference. It does not change the
base image in the embedded image definition. Coop always derives a `local-...`
tag from the name, embedded definition, locked core environment, configured
package source, and effective package set. Changing any effective input
requires a new local build.

Trusted user configuration and repository configuration may add tools with the
same additive syntax:

```toml
[tools]
packages = ["actionlint", "shellcheck"]
```

The global and project lists are sorted, deduplicated, and combined. Package
values are simple Nixpkgs attribute paths such as `gh`, `shellcheck`, or
`nodePackages.prettier`; URLs, flakes, paths, shell syntax, and per-package
version expressions are rejected. At most 64 configured packages may be
effective. Coop resolves them from the immutable Nixpkgs revision owned by the
installed Coop release, so it does not maintain a package catalog or lockfile
format of its own.

For one beta release, global `[image].extra_packages` is accepted as a
deprecated alias and prints a warning. Defining it together with
`[tools].packages` is an error, and the old field is not accepted in project
configuration.

`coop rebuild` prints the core, global, and project inputs before building. A
failed build leaves the existing image and container untouched. `coop status`
shows the running image, desired image, whether the desired image needs a
build, and whether the existing container will be recreated. Recreation
preserves named agent-state volumes but discards undeclared root-filesystem
changes.

The declaration is a reproducible starting state, not an allowlist. Guest root
may install or download other software. Manual changes survive `coop down`
followed by `coop up`, but disappear after image-driven recreation or
`coop destroy`. A useful progression is:

```text
try manually -> declare in coop.toml -> move to .flox when portability matters
```

## Flox

Flox and Nix are required inside the Coop image. The pinned Flox base supplies
the package engine, while Coop embeds a Linux-only manifest and exact lock for
the core environment. The promised core packages are:

- shell support: `bashInteractive`, `zsh`, `coreutils`, `gnugrep`, `gnused`,
  `findutils`, `gawk`, `gnutar`, `gzip`, and `cacert`;
- repository tools: `git`, `gh`, `openssh`, `curl`, `ripgrep`, `jq`,
  `diffutils`, `patch`, `file`, `less`, `procps`, `tmux`, and `unzip`; and
- agents: `codex`, `claude-code`, and `opencode`.

Configured global/repository packages live in a separate image profile. User
commands see the core environment first, configured tools ahead of it, and the
nearest project `.flox` environment with highest precedence. Coop maintenance
uses only the controlled core PATH, so repository tools cannot shadow its
seeding or credential helpers.

For a command or bare shell entered from a project subdirectory, Coop walks
toward the selected project root and automatically activates the nearest
ancestor containing `.flox`. This also finds a worktree-local environment when
the selected root contains multiple worktrees. Coop does not force services to
start; the project environment's Flox settings remain authoritative.

Project Flox remains optional and must support `aarch64-linux` when present.

## Security Model

coop reduces the host resources directly available to coding agents. It does
not make untrusted code safe.

- Commands run as root in the guest.
- The selected project is exposed read-write to the guest.
- Seeded files and project-specific agent state are readable by guest
  processes with sufficient guest permissions.
- Credentials selected for an entry are readable by guest root and other
  sufficiently privileged guest processes for that entry's lifetime.
- Coop containers persist across entries. A root-capable guest process can
  remain running or alter guest filesystem paths such as `/dev/shm` so it can
  capture credentials selected for a later entry. Session cleanup limits
  ordinary exposure; it is not a security boundary against guest root.
- The VM has unrestricted outbound network access. Data and credentials
  available in the VM can be sent over the network.
- Repository tool declarations make intended contents auditable, but they are
  not a security allowlist: guest root can obtain additional software.
- SSH-agent forwarding is off by default and can be enabled only with
  `ssh = true` in trusted user configuration. The private key files remain on
  the host, but guest processes can use loaded keys to authenticate or sign.
- A VM escape or vulnerability in Apple's runtime, Virtualization framework,
  hypervisor, or host-facing devices can defeat the VM boundary.

Avoid seeding SSH directories, broad configuration trees, cloud credentials,
or credentials shared across unrelated projects. Prefer narrowly scoped,
revocable credentials. See [`SECURITY.md`](SECURITY.md) for vulnerability
reporting.

## Current Limits

- Hosts other than Apple silicon Macs running macOS 26 or later are not
  supported.
- The beta has a source-only installation path.
- Sandbox images are built locally and are not published by this project.
- Flox environments are limited to `aarch64-linux`.
- Guest root access and unrestricted egress are operating constraints, not
  additional security boundaries.

## License

Apache-2.0. See [`LICENSE`](LICENSE).
