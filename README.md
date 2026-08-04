# coop

Run coding agents in a project-scoped Linux environment on Apple silicon.

Coop uses Apple's [`container`](https://github.com/apple/container) runtime. It
mounts one project read-write at the same path it has on the host, keeps agent
state in project-specific volumes, and loads machine-local project settings
from an ignored `.coop.toml`. Docker is not required.

Coop is pre-1.0. Command and configuration behavior may change between
releases.

## Requirements

- macOS 26 or later on Apple silicon
- Apple's `container` CLI and a running container service

The preferred Homebrew formula installs Apple's `container` CLI as a
dependency. When installing coop from source with Go, install the runtime
separately as described below.

Flox is built into the guest image; it is not a host prerequisite. A project
`.flox` is optional and useful when the repository wants the same pinned
toolchain inside and outside Coop.

## Install

```sh
brew install sarcasticbird/tap/coop
container system start
```

The formula builds coop from the tagged source on your machine and installs
Apple's `container` runtime as a dependency; start its service once per boot
as shown. Then:

```sh
coop --version
coop doctor
```

Or install the current release from source with Go 1.26.5 or later:

```sh
go install github.com/sarcasticbird/coop/cmd/coop@v0.2.0
coop --version
coop doctor
```

`go install` writes to `$(go env GOPATH)/bin`; make sure that directory is on
your `PATH`. Unlike the Homebrew formula, this path does not install Apple's
runtime. Install it separately with `brew install container`, then start its
service with `container system start`.

Prebuilt archives are attached to the
[`v0.2.0` release](https://github.com/sarcasticbird/coop/releases/tag/v0.2.0).
Verify an archive against `checksums.txt` before installing it. Release
binaries target Apple silicon and are not Developer ID signed or notarized.

## Quick start

From a project directory:

```sh
cd ~/Projects/my-app
coop init              # optional: review local isolation and port proposals
coop claude
```

`coop init` is an interactive convenience for the ignored project
`.coop.toml`. It detects likely platform-specific dependency trees, asks about
localhost port publications, previews the exact TOML, and defaults every
addition to no. It does not start Apple Container, build an image, or install
dependencies.

The first time you enter a project, coop offers to build the sandbox image —
the first build takes a few minutes. Coop builds this image locally rather
than publishing one. Run `coop rebuild` after changing configured tools or
after upgrading to a release with different embedded image inputs. Rebuild is
the only command that resolves configured GitHub release tools.

Running `coop` without a guest command opens a Zsh login shell. The locked core
includes Git, GitHub CLI, SSH, common shell tools, and the opencode, Claude
Code, and Codex agents. Application runtimes such as Go, Node.js, and Python
are project-owned.

To update Coop's complete locked core independently of a Coop release:

```sh
coop upgrade
# Existing coops keep running unchanged.

cd ~/Projects/my-app
coop status
coop rebuild
```

`coop upgrade` is machine-wide for the current user. It resolves packages in a
short-lived Apple container, so host Flox is not required. It changes the
desired core lock but does not build images, stop containers, recreate coops,
or modify project Flox environments and configured project tools. Existing
coops become stale only when the lock changes; run `coop rebuild` in each
project when ready to adopt it. Version one upgrades the complete core rather
than individual core packages.

## Commands

```text
coop [command [args...]]  Run a command in the project environment
coop                      Open a shell
coop init                 Review project-local dependency isolation and ports
coop up                   Create or start the project container
coop down                 Stop it while preserving state volumes
coop status               Show container and desired/running image state
coop ls                   List all coops
coop tui                  Open the fleet dashboard
coop doctor               Check the host and local configuration
coop rebuild              Build the sandbox image locally
coop upgrade              Upgrade the machine-wide locked core
coop destroy              Delete the container and all project state volumes
coop --version            Print the installed Coop version
```

Arguments after the guest command pass through unchanged:

```sh
coop claude --help
coop codex --model o3
coop opencode run "fix the tests"
coop --credentials aws-dev,github codex
```

Coop flags must appear before the guest command. `coop down` preserves state;
`coop destroy` asks for confirmation and removes every volume belonging to the
project.

## How it works

Coop selects a project boundary, mounts it read-write at the identical path in
the Linux guest, and reconciles a long-lived project container. Agent state
lives in named volumes isolated by project. Trusted user seeds copy selected
host files or executables into the guest. Selected credentials are staged for
one interactive entry and cleaned up afterward, but every guest-root process
can access or retain them while staged.

The sandbox image has four tool layers:

1. Coop's locked core workbench;
2. additive packages declared in machine or project configuration;
3. checksum-verified public GitHub release tools declared locally;
4. an optional project `.flox`, activated at entry with highest precedence.

See the [runtime model](docs/runtime.md) for project selection, image identity,
container lifecycle, persistence, tool ordering, recovery, and current limits.

## Configuration

Coop loads machine-wide configuration from
`$XDG_CONFIG_HOME/coop/coop.toml` or `~/.config/coop/coop.toml`, then loads
`<project-root>/.coop.toml` last. The project file is machine-local, must remain
Git-ignored, and may override or expand any setting—including additional host
directory mounts. Coop refuses a `.coop.toml` tracked by Git and does not load
a committed project `coop.toml`.

Use a project volume when one path cannot be safely shared between macOS and
Linux—for example, native npm optional dependencies under `node_modules`:

```toml
[[volume]]
path = "web/node_modules"

[[publish]]
guest_port = 5173
host_port = 5173
```

The host keeps its own `web/node_modules`; Coop overlays a persistent,
initially empty Linux volume at that path in the guest. Install dependencies
once on each side. Published services are reachable at host
`127.0.0.1:5173`; the guest service must listen on `0.0.0.0:5173`.

Upgrading from 0.1.x requires renaming any project `coop.toml` to the ignored
`.coop.toml` form and moving scoped mounts into the project file. See the
[migration guide](docs/configuration.md#migrating-from-01x).

The full [`coop.toml` reference](docs/configuration.md) documents every key,
default, merge rule, trust boundary, validation rule, and lifecycle effect.
The [credential guide](docs/credentials.md) covers macOS Keychain-backed Git
and `gh`, project authorization, agent-owned login state, migration, and cleanup.
Start from:

- [machine-wide example](examples/coop.user.toml)
- [machine-local project example](examples/coop.project.toml)

## Security

Coop narrows direct host exposure; it does not make untrusted code safe.
Commands run as guest root, the selected project is writable, containers
persist across entries, and outbound network access is unrestricted.

Read the [security model](docs/security-model.md) before granting credentials,
forwarding the SSH agent, or seeding sensitive host data. Report suspected
vulnerabilities through the repository [security policy](SECURITY.md).

## Development and releases

Contributor and release checks are documented in
[docs/release.md](docs/release.md). The embedded image's third-party
distribution notices are in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
