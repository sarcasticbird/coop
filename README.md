# coop

Run coding agents in a project-scoped Linux environment on Apple silicon.

Coop uses Apple's [`container`](https://github.com/apple/container) runtime. It
mounts one project read-write at the same path it has on the host, keeps agent
state in project-specific volumes, and gives repositories a declarative
`coop.toml` for resources and tools. Docker is not required.

Coop is pre-1.0. Command and configuration behavior may change between beta
versions.

## Requirements

- macOS 26 or later on Apple silicon
- Apple's `container` CLI and a running container service

Install and start Apple's runtime:

```sh
brew install container
container system start
```

Flox is built into the guest image; it is not a host prerequisite. A project
`.flox` is optional and useful when the repository wants the same pinned
toolchain inside and outside Coop.

## Install

Install the [GitHub CLI](https://cli.github.com/) if needed, then download the
current public release. Authentication is not required for the release assets.

```sh
brew install gh
```

Download and verify the exact release:

```sh
version=v0.1.0-beta.4
archive="coop_${version}_darwin_arm64.tar.gz"
gh release download "$version" -R sarcasticbird/coop \
  -p "$archive" -p checksums.txt
shasum -a 256 -c checksums.txt
tar -xzf "$archive" coop
mkdir -p "$HOME/.local/bin"
install -m 0755 coop "$HOME/.local/bin/coop"
```

Ensure `$HOME/.local/bin` is on `PATH`, then run:

```sh
coop --version
coop doctor
```

Release binaries target Apple silicon and are not Developer ID signed or
notarized. Downloads through `gh` do not carry browser quarantine metadata;
macOS may treat a browser download differently.

To build from source, install Go 1.26.5 or later:

```sh
git clone https://github.com/sarcasticbird/coop.git
cd coop
mkdir -p "$HOME/.local/bin"
go build -trimpath -o "$HOME/.local/bin/coop" ./cmd/coop
```

## Quick start

From a project directory:

```sh
cd ~/Projects/my-app
coop rebuild
coop claude
```

The beta builds its sandbox image locally rather than publishing one. Run
`coop rebuild` once after installation, after changing configured tools, or
after upgrading to a release with different embedded image inputs. Rebuild is
the only command that resolves configured GitHub release tools.

Running `coop` without a guest command opens a Zsh login shell. The locked core
includes Git, GitHub CLI, SSH, common shell tools, and the opencode, Claude
Code, and Codex agents. Application runtimes such as Go, Node.js, and Python
are project-owned.

## Commands

```text
coop [command [args...]]  Run a command in the project environment
coop                      Open a shell
coop up                   Create or start the project container
coop down                 Stop it while preserving state volumes
coop status               Show container and desired/running image state
coop ls                   List all coops
coop tui                  Open the fleet dashboard
coop doctor               Check the host and trusted user configuration
coop rebuild              Build the sandbox image locally
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
2. additive packages declared in user or project `coop.toml`;
3. checksum-verified public GitHub release tools declared by the trusted user;
4. an optional project `.flox`, activated at entry with highest precedence.

See the [runtime model](docs/runtime.md) for project selection, image identity,
container lifecycle, persistence, tool ordering, recovery, and current limits.

## Configuration

Coop loads trusted user configuration from
`$XDG_CONFIG_HOME/coop/coop.toml` or `~/.config/coop/coop.toml`, then loads
`<project-root>/coop.toml`.

Repositories may declare capped resources, additive packages from Coop's
pinned Nixpkgs source, and persistent agent state. Host file seeds,
credential grants, SSH-agent forwarding, and image selection remain under
trusted user control. GitHub release tools are also user-only because they
select publisher-controlled executable assets.

The full [`coop.toml` reference](docs/configuration.md) documents every key,
default, merge rule, trust boundary, validation rule, and lifecycle effect.
Start from:

- [trusted user example](examples/coop.user.toml)
- [project example](examples/coop.project.toml)

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
