# Coop

Coop runs persistent Linux workspaces on Apple silicon. Interactive Coops
mount your selected project at the same writable path. Named agent Coops own
their entire guest filesystem and clone repositories using a separate agent
identity configured on your Mac.

Coop uses Apple's [`container`](https://github.com/apple/container) runtime.
Docker is not required.

## Install

You need macOS 26 or later on Apple silicon.

```sh
brew install sarcasticbird/tap/coop
container system start
coop doctor
```

The Homebrew formula installs Apple's `container` CLI. Start its service once
after each reboot.

To build Coop from source instead, use the Go version declared in `go.mod`:

```sh
go install github.com/sarcasticbird/coop/cmd/coop@latest
brew install container
container system start
coop doctor
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`.

## Start an agent session

From a project checkout:

```sh
cd ~/Projects/my-app
coop init
coop codex
```

`coop init` reviews useful machine-local settings such as Linux-only dependency
volumes and published development ports. It previews every change and defaults
to doing nothing. You can skip it and enter an agent directly with `coop codex`,
`coop claude`, or `coop opencode`.

On first entry, Coop offers to build the local guest image. The guest includes
Git, the GitHub CLI, common shell tools, Flox, and the supported coding agents.
Your repository owns application runtimes such as Go, Node.js, and Python;
declare them in the project's Flox environment or in Coop's local config.

Arguments after the agent command pass through unchanged:

```sh
coop codex --help
coop claude --help
coop opencode run "fix the tests"
```

## Named agents

Configure a host-owned profile and separate credentials, then launch it from
any folder:

```sh
coop --agent robin rebuild
coop --agent robin
coop --agent robin shell
coop --agent robin down
```

The managed agent continues after terminal detachment. Its home, repositories,
and installed services survive stop/start. See [Persistent agents](docs/agents.md)
for profiles, server commands, helper services, credentials, and recovery.

## Everyday commands

```text
coop [command [args...]]  Run a command in the project workspace
coop                      Open a Zsh login shell
coop init                 Review machine-local project settings
coop up                   Create or start the project workspace
coop down                 Stop it and preserve project state
coop status               Show workspace and image status
coop ls                   List all workspaces
coop tui                  Open the workspace dashboard
coop doctor               Check host requirements and configuration
coop rebuild              Rebuild after changing configured tools
coop upgrade              Update Coop's locked core toolset
coop apply                Apply maintenance to a stopped workspace
coop backup destination   Back up a stopped workspace
coop restore archive      Restore into an absent workspace
coop destroy              Delete this workspace and its state volumes
```

Coop flags go before the guest command. For example:

```sh
coop --credentials github-work codex
```

`coop down` preserves state. `coop destroy` asks for confirmation and deletes
the project's Coop-owned volumes.

## Machine-local configuration

Coop loads:

1. `~/.config/coop/coop.toml` for settings shared across projects; then
2. `<project-root>/.coop.toml` for this checkout.

The project file has full local authority and must stay Git-ignored. Coop
refuses to load it when Git tracks it.

A common project file keeps macOS and Linux dependencies separate and exposes a
development server to the host:

```toml
# .coop.toml
[tools]
packages = ["go-task", "nodejs_22"]

[[volume]]
path = "web/node_modules"

[[publish]]
guest_port = 5173
host_port = 5173
```

The Linux volume starts empty, so install dependencies once inside Coop. The
host retains its own `web/node_modules`. A published guest service must listen
on `0.0.0.0`; it is reachable from the host on `127.0.0.1`.

See the parser-tested [machine-wide](examples/coop.user.toml) and
[project-local](examples/coop.project.toml) examples, then use the
[configuration reference](docs/configuration.md) for all supported settings.

## GitHub credentials

Coop does not manage or store your source credentials. GitHub creates and
revokes tokens, Git stores them through your host credential helper, and Coop
reads an authorized credential only for an interactive project entry.

The [credential guide](docs/credentials.md) provides one copy-paste path for
initial GitHub setup, rotation, project authorization, and verification from
both Git and `gh` inside Coop.

## Security boundary

Coop reduces direct host exposure; it does not make untrusted code safe. Guest
commands run as root, the selected project is writable, containers persist,
and outbound network access is unrestricted. Guest processes can copy or retain
credentials while those credentials are exposed to a session.

Read the [security model](docs/security-model.md) before granting credentials,
forwarding SSH, mounting more host directories, or seeding sensitive data.

## More documentation

- [Configuration](docs/configuration.md): current schema, examples, and when
  changes take effect
- [Credentials](docs/credentials.md): GitHub setup and rotation plus other
  credential sources
- [Runtime model](docs/runtime.md): project selection, persistence, images,
  Flox, and recovery
- [GitHub workers](docs/workers.md): OpenCode servers, unattended approved work,
  review handoffs, controls and recovery
- [Security model](docs/security-model.md): trust boundaries and non-promises
- [Release process](docs/release.md): contributor-only release procedure

Third-party distribution notices for the embedded image are in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
