# Runtime Model

Coop gives each selected project a persistent Linux container managed by
Apple's `container` runtime. The project is mounted read-write at its identical
host path; agent state and configured Linux-only directories live in
project-specific named volumes.

Use this page for operational details. See [Configuration](configuration.md)
for settings and [Security](security-model.md) for trust boundaries.

## Project selection

Starting from the current directory, Coop selects a project root in this order:

1. the nearest ancestor containing `.coop.toml`;
2. the parent of a Git worktree when that parent contains `.bare`;
3. the Git repository root;
4. the current directory.

An empty `.coop.toml` is a valid boundary marker. Coop canonicalizes the path
through symlinks and uses it to name the container and volumes. A path hash
keeps projects with the same directory name separate.

Coop refuses `/`, the user's home, and ancestors of the home as project roots.

## Paths and mounts

The project has the same absolute path in the guest as on the host. Coop also
sets guest `HOME` to the host home's absolute path, but it does not mount the
host home.

The guest can see only:

- the selected project;
- additional mounts declared in local configuration;
- configured seeds and selected credentials;
- configured agent and project volumes;
- the SSH-agent socket when enabled;
- interfaces supplied by Apple's runtime.

An additional mount also uses its canonical host path in the guest and defaults
to read-only.

## Container lifecycle

`coop up` compares the desired project specification with the existing
container, then creates, starts, or recreates it as needed. The specification
includes the image, resources, project path, agent state, mounts, project
volumes, port publications, and SSH setting.

| Command | Container | Named volumes |
| --- | --- | --- |
| `coop up` | Create, start, or reconcile | Preserve |
| `coop` / `coop <command>` | Reconcile, seed, then enter | Preserve |
| `coop down` | Stop | Preserve |
| `coop destroy` | Stop and remove | Delete all volumes owned by the project |

Container recreation discards changes made only to its root filesystem. Agent
and project volumes survive. `coop destroy` also removes volumes left behind by
older local configuration.

Coop serializes lifecycle changes per project and treats runtime inspection
failures as errors, not as proof that state is absent.

## Project volumes and published ports

A project-local `[[volume]]` overlays a persistent Linux volume within the
read-write project mount. The corresponding host directory remains a separate
tree. This keeps platform-specific output such as `node_modules`, `target`, or
a virtual environment from mixing macOS and Linux artifacts.

The guest volume starts empty. Install dependencies once on each platform.
Shared manifests and lockfiles remain in the project mount.

A project-local `[[publish]]` maps host `127.0.0.1:<host_port>` to a guest TCP
port. The guest service must listen on its guest network interface, normally
`0.0.0.0:<guest_port>`, rather than only guest localhost.

## Images and rebuilds

Coop builds its guest image locally. The binary supplies the base-image
reference, Containerfile, core tool lock, and pinned package source; local
configuration adds Nixpkgs packages and optional verified public GitHub release
tools.

`coop rebuild` creates the desired image for the current project. A different
core lock, embedded image input, configured package set, release-tool result,
or `image.name` produces a different desired image. `coop status` shows the
running and desired state without replacing a working container first.

Public GitHub release tools resolve only during `coop rebuild`. Ordinary entry
and status do not update them.

### Core upgrades

`coop upgrade` updates the user-local locked core toolset. It does not build
project images or stop, recreate, or destroy existing containers.

Adopt a changed core one project at a time:

```sh
coop upgrade
cd ~/Projects/my-app
coop status
coop rebuild
```

The existing container remains available until the replacement image has been
built.

## Command entry and Flox

Running `coop` with no guest command opens `zsh -l`. Arguments after a guest
command pass through unchanged, and Coop options such as `--credentials` must
appear before that command.

Command lookup is:

1. the nearest project `.flox` between the current directory and project root;
2. Coop's locked core tools;
3. configured Nixpkgs packages;
4. configured GitHub release tools;
5. operating-system fallback paths.

Project Flox is optional and must support `aarch64-linux`. Coop activates it but
does not create its manifest, install project dependencies, or start its
services.

## Seeds, agent state, and credentials

Seeds apply after the container starts on each `coop up` and interactive entry.
Their `always`, `if-absent`, and `overlay` policies are independent of image
builds. See [Seeds](configuration.md#seeds).

Each configured agent state directory is a named volume. State such as native
agent logins and transcripts therefore persists per project across stop/start
and recreation.

Selected credentials are acquired for an interactive entry, exposed through a
temporary guest lease, and cleaned up after that command exits. This is not a
confidentiality boundary against guest root. See [Credentials](credentials.md)
and [Session credentials](security-model.md#session-credentials).

## Recovery without deleting state

If only the container root filesystem is damaged, note its name from
`coop status`, remove that container with Apple's CLI, and let Coop recreate
it:

```sh
coop status
container stop <name>
container rm <name>
coop up
```

Do not use `coop destroy` for this recovery path because it also deletes agent
and project volumes.

When the desired image is missing after an upgrade or tool change:

```sh
coop rebuild
coop up
```

## Platform limits

- Hosts must be Apple silicon Macs running macOS 26 or later.
- The guest architecture is `aarch64-linux`.
- Guest images are built locally.
- Hosted CI uses a strict runtime mock; real Apple runtime validation happens
  on supported hardware.
- The project and additional paths are whole-directory mounts, not per-file
  capabilities.
