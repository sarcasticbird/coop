# Runtime Model

Coop gives each selected project a persistent Linux container managed by
Apple's `container` runtime. The project is mounted read-write at its identical
host path; agent state and configured Linux-only directories live in
project-specific named volumes.

Use this page for operational details. See [Configuration](configuration.md)
for settings and [Security](security-model.md) for trust boundaries.

For self-contained machines selected with `--agent`, see [Persistent agents](agents.md).

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
container, then creates or starts it. Configuration drift requires explicit maintenance. The specification
includes the image, resources, project path, agent state, mounts, project
volumes, port publications, and SSH setting.

| Command | Container | Named volumes |
| --- | --- | --- |
| `coop up` | Create or start; reject pending maintenance | Preserve |
| `coop` / `coop <command>` | Verify specification, seed, then enter | Preserve |
| `coop down` | Stop | Preserve |
| `coop destroy` | Stop and remove | Delete all volumes owned by the project |

Ordinary entry and stop/start preserve the root filesystem. Same-image `apply`
backs up the cold guest before changing runtime configuration. Image replacement
is currently unsupported. Agent and project volumes survive maintenance. `coop destroy` also removes volumes left behind by
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

Build the changed core image and inspect the selected workspace:

```sh
coop upgrade
cd ~/Projects/my-app
coop status
coop rebuild
```

New workspaces use the rebuilt image. Existing disks stay intact and report
pending maintenance. Base-image replacement is currently unsupported; migrating
an existing guest's installed software and native state requires a separate
workspace. Save a cold backup before that migration.

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
and explicit maintenance.

Selected credentials are acquired for an interactive entry, exposed through a
temporary guest lease, and cleaned up after that command exits. This is not a
confidentiality boundary against guest root. See [Credentials](credentials.md)
and [Session credentials](security-model.md#session-credentials).

## Recovery without deleting state

Stop the workspace and create a cold backup before maintenance:

```sh
coop down
coop backup /private/path/workspace.zip
coop apply
coop up
```

Same-image maintenance preserves rootfs and owned volumes, and retains a
recovery archive. Restore into an absent destination with `coop restore`.
Host mounts are outside backup ownership. Native guest auth and other secrets
saved to disk may be included in the private archive. See
[Persistent agents](agents.md#maintenance-and-recovery) for details.

If `coop apply` is interrupted, keep the workspace stopped and the destination
configuration unchanged, then rerun `coop apply` to resume the pending maintenance.
Alternatively, run `coop restore /path/to/original-recovery.zip` with the original
recovery archive. The durable maintenance journal rejects foreign disks and
changes to the destination configuration or recovery archive. Preserve the
original recovery archive until maintenance succeeds; retries reuse it without
creating a fresh backup. Coop refuses normal workspace entry and backup while
maintenance is incomplete. Disk writes retain their journaled file identities,
so partial writes and ownership-record save failures can be replayed safely.
Do not delete the journal, replace its disks, or start the runtime through
external `container` commands. Coop's workspace lock does not serialize those
external operations.

Backup rechecks the stopped runtime after copying and rejects changes to the
payload set, names, sizes, file identities, modification times, ownership record,
runtime version, or archived runtime metadata before publishing an archive.
Payload enumeration and JSON object-key order do not affect this comparison.
Each disk also retains size and modification-time checks during its own copy.
These checks are not a runtime or filesystem lock: keep the workspace and all
users of its owned volumes stopped for the entire backup, and avoid concurrent
maintenance or host writes. A start-and-stop cycle between inspections, a change
reverted before inspection, or same-size content writes that preserve modification
times can escape detection. Archive checksums verify the bytes copied, not that
concurrent writes formed a consistent point-in-time disk image.

`coop rebuild` prepares an image; it never changes an existing guest. An
existing guest whose desired image changes returns maintenance-required.
Image replacement is currently unsupported to preserve arbitrary disk changes.

## Platform limits

- Hosts must be Apple silicon Macs running macOS 26 or later.
- The guest architecture is `aarch64-linux`.
- Guest images are built locally.
- Hosted CI uses a strict runtime mock; real Apple runtime validation happens
  on supported hardware.
- The project and additional paths are whole-directory mounts, not per-file
  capabilities.
