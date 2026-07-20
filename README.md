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
again when changing `image.extra_packages` or updating coop to a version with
different image inputs.

Running `coop` without a command opens a Zsh login shell in the VM. The embedded
image definition includes opencode, Claude Code, Codex, Flox, Git, Zsh,
ripgrep, jq, tmux, Node.js, OpenSSH, and common shell utilities. See
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) before distributing an
image.

## Commands

```text
coop [command [args...]]  Run a command in the project VM
coop                      Open a shell in the project VM
coop up                   Create or start the project VM
coop down                 Stop it while preserving state volumes
coop status               Show the current project's VM state
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
```

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

The project file is repository-controlled. Only `[resources]` and `[agents]`
entries from it take effect. `ssh`, `[image]`, and `[[seed]]` settings take
effect only from trusted user configuration; placing them in a project file
does not grant those capabilities. Project resource requests are capped at 8
CPUs and 16 GB of memory. Agent state paths must remain below the guest home.

### User Configuration

```toml
# $XDG_CONFIG_HOME/coop/coop.toml, or ~/.config/coop/coop.toml

ssh = false

[image]
name = "coop:latest"
extra_packages = []

[resources]
cpus = 4
memory = "8G"

[[seed]]
src = "~/.config/opencode/opencode.jsonc"
policy = "always"

[[seed]]
src = "~/.local/share/opencode/auth.json"
policy = "if-absent"

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

### Project Configuration

```toml
# <project>/coop.toml

[resources]
cpus = 6
memory = "12G"

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
base image in the embedded image definition. For the default name and for
images with additional packages, coop derives a `local-...` tag from the image
definition and package set. A non-default name without additional packages is
treated as an existing local image reference.

Additional tools can be installed from Nix packages through trusted user
configuration:

```toml
[image]
name = "coop:latest"
extra_packages = ["gemini-cli"]

[agents.gemini]
state = "~/.gemini"
```

When `extra_packages` is non-empty, coop never substitutes another image for
the derived local tag. Run `coop rebuild` after changing either image setting,
then start the project. Existing state volumes survive VM recreation when the
effective image or another VM setting changes.

## Flox

For a command run from a project subdirectory, coop walks from that directory
toward the selected project root and uses the nearest ancestor containing a
`.flox` directory. It runs the command through `flox activate --dir` for that
environment. This also finds a worktree-local environment when the selected
root contains multiple worktrees.

The Flox environment must support `aarch64-linux`. A shell opened with bare
`coop` is not activated automatically; run `flox activate` in that shell when
needed.

## Security Model

coop reduces the host resources directly available to coding agents. It does
not make untrusted code safe.

- Commands run as root in the guest.
- The selected project is exposed read-write to the guest.
- Seeded files and project-specific agent state are readable by guest
  processes with sufficient guest permissions.
- The VM has unrestricted outbound network access. Data and credentials
  available in the VM can be sent over the network.
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
