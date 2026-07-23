# Security Model

Coop reduces the host resources directly exposed to coding agents by running
them in a project-scoped Linux environment. It does not make untrusted code
safe, and it does not treat guest root as an adversary it can contain within a
long-lived project container.

For vulnerability reporting, see the repository
[security policy](../SECURITY.md).

## Trust boundaries

Coop distinguishes two configuration authorities:

- **Trusted user configuration** belongs to the person running Coop and may
  select the image namespace, read host seed sources, forward the SSH agent,
  define credential grants, and select public GitHub release executables.
- **Project configuration** may be controlled by the checked-out repository.
  It may request capped resources, additive packages from Coop's pinned
  Nixpkgs source, and persistent agent-state directories.

Project configuration cannot make host seed reads, enable SSH-agent forwarding,
select a different image namespace, or define effective host credential grants.
See the exact [trust matrix](configuration.md#trust-boundary).

Repository tool declarations are constrained package identifiers, not arbitrary
URLs, flakes, paths, or install hooks. The locked core workbench remains ahead
of configured tools on `PATH`, so a project package cannot shadow the core
`git`, `gh`, `ssh`, `curl`, or credential-transport binaries used by normal
entries.

GitHub release tools are a separate trusted-user capability. Coop accepts only
public repositories, GitHub-hosted HTTPS downloads, `.tar.gz` archives, and a
single configured regular file. It rejects traversal paths, links, devices,
oversized inputs, missing or ambiguous assets, and assets without a
GitHub-provided SHA-256 digest. The digest protects cache and download
integrity against the release metadata; it is not an independent signature or
an endorsement of the publisher. An exact tag improves repeatability, while
`latest` deliberately trusts the publisher's next release when the user runs
`coop rebuild`.

This improves auditability; it is not an allowlist. Guest root can install or
download other software after entry.

## Host exposure

The selected project is mounted read-write at its identical host path. Any
guest process with sufficient permissions can read, modify, create, or delete
project files, and those changes are immediately host-visible.

Coop refuses to mount `/`, the host home, or an ancestor of the home as a
project root. It does not automatically mount the rest of the home directory.
Host data crosses into the guest only through:

- the selected project mount;
- trusted user seeds;
- configured named credentials selected for one entry;
- optional SSH-agent forwarding;
- runtime and virtualization interfaces provided by Apple's stack.

Do not place unrelated secrets in a project directory that will be mounted.

## Guest authority and persistence

Commands run as root in the guest. Root can inspect other guest processes,
change the container filesystem, alter shell startup files, install software,
and access mounted project and state data.

Containers persist across entries. A malicious root-capable process can remain
running after an interactive command exits or modify guest paths used by a
later entry. Container recreation discards the root filesystem, but named
agent-state volumes persist until `coop destroy`.

Per-project volumes prevent accidental state sharing between projects with
different canonical paths. They do not isolate processes inside the same
project container from one another. Agent transcripts, refreshed application
tokens, and history stored in a configured state directory are available to
guest root.

## Seeds

Seeds are a trusted user capability to copy host material into a running guest.
The project file cannot create effective seed rules.

File seeding follows host source symlinks, which supports Stow-managed files.
On the guest side it rejects a symlink destination, a non-regular destination,
and symlinked parent directories before using an atomic temporary-file
replacement. The validation and write are separate operations, so a
concurrently running guest may still race them.

Directory `overlay` extraction can follow symlinks already present inside the
guest destination tree. Use it only for non-sensitive content such as skills,
plugins, or documentation. Never use it for credentials.

An executable seed gives the guest that executable with the same authority as
other guest software. Keep executable seeds in trusted user configuration and
seed only material every process in that project container may run or read.

## Session credentials

Named credentials are defined only by trusted user configuration. The project
may not choose host file paths or credential commands. Defaults from
`include_credentials` and explicit `--credentials` selections are validated
and deduplicated before acquisition.

Host acquisition is hardened as follows:

- file sources must be regular files outside the project through both lexical
  and resolved paths;
- host path traversal uses no-follow opens;
- command sources run argv directly, not through a shell;
- relative executable paths containing `/` are rejected;
- bare executable names resolve through a sanitized `PATH` that excludes the
  project;
- commands run from the trusted host home with a restricted environment;
- payload and bundle sizes are bounded.

Selected secrets are streamed over stdin into a unique mode-0700 directory
below `/dev/shm/coop-credentials`. They are exposed to one interactive command
through an environment value, temporary file, Git credential-store
configuration, or AWS shared-credentials file. Coop removes the lease after the
command and scrubs abandoned leases before a later entry.

Secret values are not intentionally stored in container arguments, labels,
mount definitions, project files, seeds, or named volumes. Coop diagnostics
format acquired and staged secret objects as redacted.

These controls limit routine exposure; they are not a boundary against guest
root. A root process in the persistent container can read another process's
environment or temporary files, intercept guest execution, or pre-position
state to capture credentials selected during a later entry. Recreate the
container before introducing sensitive credentials after running untrusted
guest code, and prefer narrowly scoped, short-lived, revocable grants.

Coop does not refresh a credential during a long-running entry. Cleanup removes
guest exposure; it cannot revoke source material unless the source itself
provides a revocation mechanism.

## SSH-agent forwarding

SSH-agent forwarding is off by default and can be enabled only from trusted
user configuration:

```toml
ssh = true
```

Private-key files remain on the host, but guest processes can ask the agent to
authenticate or sign with loaded keys. With outbound network access, this may
be enough to access remote systems as the user. Load only appropriate keys and
prefer separate, constrained identities when possible.

## Network

The guest has unrestricted outbound network access. Project contents, seeded
data, agent state, selected credentials, and results obtained through the SSH
agent can be sent over the network by guest processes.

Coop does not currently provide destination filtering, DNS isolation,
per-process egress controls, or a claim that package installation is offline.

## VM and runtime boundary

Coop relies on Apple's `container` runtime, Virtualization framework,
hypervisor, kernel, and host-facing devices for the host isolation boundary. A
vulnerability or escape in that stack can defeat the boundary.

Coop's own input validation and lifecycle checks do not replace security
updates for macOS or Apple's runtime.

## Non-promises

Coop does not promise:

- that untrusted code cannot damage the mounted project;
- isolation between root-capable processes in the same project container;
- confidentiality of a credential from guest root during or after a persistent
  compromised session;
- restricted network egress;
- a software allowlist;
- protection against runtime, hypervisor, kernel, or VM escape vulnerabilities;
- automatic revocation of source credentials;
- safe sharing of agent-state volumes with code that should not read them.

Use Coop to narrow accidental host exposure and make project requirements
declarative. Continue to apply ordinary controls: review repositories before
running them, scope credentials, maintain backups, update the host runtime, and
assume the mounted project is writable by the guest.
