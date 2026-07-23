# Runtime Model

Coop gives each selected project a long-lived Linux container managed by
Apple's `container` runtime. The project is mounted read-write at the same
absolute path it has on the host, while coding-agent state lives in named
volumes dedicated to that project.

The configuration reference is in [configuration.md](configuration.md). The
trust and isolation boundaries are in
[security-model.md](security-model.md).

## Project selection

Starting from the current directory, Coop selects a project root in this order:

1. the nearest ancestor containing a regular `coop.toml` file;
2. the parent of a Git worktree when that parent contains a `.bare` directory;
3. the Git repository root;
4. the current directory.

The path is made absolute and canonicalized through symlinks before it becomes
the project identity. Coop refuses the filesystem root, the user's home
directory, and every ancestor of the home directory because mounting any of
them would expose an unreasonably broad host tree.

An empty `coop.toml` is a valid boundary marker. This is useful for a monorepo
subtree or a directory that owns a bare repository and several worktrees.

The container and volume names include a readable project slug plus a hash of
the canonical path. Two projects with the same basename therefore do not share
containers or state.

## Identical paths

The selected root is mounted read-write at the identical absolute path inside
the guest. Coop also sets the guest `HOME` to the host home path and creates
that directory in the guest.

This property keeps project paths, shell history, configuration references, and
agent session metadata meaningful on both sides of the boundary. It does not
mount the host home. Only the selected project, explicitly configured seeds,
the optional SSH-agent socket, and temporary selected credentials cross into
the guest.

When entry starts from a symlinked or out-of-project working directory, Coop
canonicalizes it and falls back to the selected project root if necessary.

## Runtime layers

```text
host
  Coop CLI
    Apple container service
      project container
        derived Coop image
        read-write project mount
        per-agent named volumes
        optional SSH-agent socket
        temporary credential lease
```

The Apple container service owns VM and container execution. Coop owns project
selection, the desired container specification, image derivation, seeding,
credential staging, and interactive entry.

Hosted CI cannot run this nested Apple runtime. Runtime behavior is tested
against a strict mock; release validation that depends on the real runtime must
also run on supported Apple silicon hardware.

## Container lifecycle

`coop up` reconciles the desired project container:

1. acquire a per-project host lock;
2. inspect the current container state;
3. compare its stored spec fingerprint with current configuration;
4. verify a replacement image exists before removing a stale container;
5. create or start the container;
6. apply configured seeds.

The spec fingerprint covers the effective image, CPU and memory allocation,
SSH-agent forwarding, canonical project mount, guest home, and sorted
agent-volume layout. A mismatch recreates the container. Named state volumes
survive recreation; undeclared root-filesystem changes do not.

The main commands have deliberately different persistence behavior:

| Command | Container | Agent-state volumes | Images |
| --- | --- | --- | --- |
| `coop up` | Create, start, or reconcile | Preserve | Require desired image |
| `coop` / `coop <command>` | Reconcile, seed, then enter | Preserve | Require desired image |
| `coop down` | Stop | Preserve | Preserve |
| `coop destroy` | Stop and remove | Delete all project-owned volumes | Preserve |

`coop destroy` finds volumes by the project's container-name prefix, not only
the current agent list. It therefore removes volumes left behind by older
configuration.

Coop serializes destructive lifecycle operations per project. Runtime
inspection failures remain errors; they are never treated as proof that a
container or volume is absent.

## Interactive entry

An interactive entry performs more work than `coop up`:

1. resolve and acquire default plus explicitly requested credentials on the
   host;
2. reconcile the container and apply seeds;
3. remove abandoned credential leases from earlier interrupted entries;
4. stage the new lease in guest memory-backed storage;
5. enter at the requested working directory;
6. clean up the lease and revoke upstream material when supported.

Credential acquisition finishes before Coop changes VM state. This avoids
tearing down a working container when a host credential helper fails.

Running `coop` without a guest command opens `zsh -l`. Arguments after a guest
command are passed through unchanged. Coop options, including
`--credentials`, must appear before that command.

## Images

The public beta does not distribute a prebuilt sandbox image. The Coop binary
embeds:

- a digest-pinned Flox base image reference;
- the Containerfile and shell wrapper;
- an exact Flox lock for the core workbench;
- an immutable Nixpkgs revision for configured packages.

`coop rebuild` materializes those inputs to a temporary build context and asks
Apple's runtime to build the desired local image. Every configured package is a
validated attribute resolved from the pinned Nixpkgs source. A failed package
install fails the image build.

The derived `local-...` image tag hashes:

- the configured `image.name`;
- the effective, sorted package set;
- the embedded image files;
- the pinned package source.

Changing any input produces a different desired tag. `coop status` reports
both the running and desired image, whether the desired build is missing, and
whether recreation is pending. Coop never removes a working container before
confirming that its replacement image exists.

## Tool lookup and Flox

Every guest command enters through Coop's locked core Flox environment.
Configured user/project packages live in a separate lower-priority Nix profile.
Coop reconstructs `PATH` so command lookup is:

1. an optional project `.flox`;
2. every path owned by the locked core activation;
3. the configured-tools profile;
4. absolute operating-system fallback paths.

Empty and relative inherited `PATH` entries are discarded. This prevents the
mounted repository or current directory from shadowing core credential,
seeding, and transport tools.

Before entry, Coop walks from the current guest directory toward the selected
project root. If it finds `.flox`, it activates the nearest one around the
guest command. This works with a worktree-local environment even when the
selected project root contains several worktrees.

Project Flox is optional. It must support `aarch64-linux` when used in Coop.
Flox settings and services remain project-owned; Coop does not synthesize a
manifest or force project services to start.

## Seeds and state

Seeds are applied after the container is running, on every `coop up` and
interactive entry. This makes `always`, `if-absent`, and `overlay` policies
independent of image builds. See the [seed reference](configuration.md#seed)
for exact behavior and safety constraints.

Agent state is different: each configured state directory is a named volume
mounted when the container is created. Changing the agent map changes the
container spec and causes recreation.

Manual software installs and other root-filesystem changes survive
`coop down` followed by `coop up`. They disappear when configuration causes
recreation or when the container is destroyed. A practical progression is:

```text
try manually -> declare in coop.toml -> move to .flox when portability matters
```

## Recovery without deleting state

If the container root filesystem is damaged, remove only the container with
Apple's CLI and let Coop recreate it:

```sh
coop status                 # note the container name
container stop <name>
container rm <name>
coop up
```

Do not use `coop destroy` for this recovery path; it also removes agent-state
volumes.

If the desired image is missing after a Coop upgrade or tool change, run:

```sh
coop rebuild
coop up
```

## Current limits

- Supported hosts are Apple silicon Macs running macOS 26 or later.
- Release binaries target Darwin arm64 and are not Developer ID signed or
  notarized.
- Sandbox images are built locally and are not published by this project.
- Guest architecture is `aarch64-linux`.
- Project Flox environments used in Coop must support `aarch64-linux`.
- The selected project is one read-write mount; Coop is not a per-file
  capability system.
- Guest root and unrestricted network egress are operating constraints, not
  additional security boundaries.
