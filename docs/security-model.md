# Security Model

Coop narrows which host resources coding agents can reach. It does not make
untrusted code safe.

Commands run as root inside a persistent project container with unrestricted
outbound network access. Assume guest code can modify the mounted project and
retain anything intentionally exposed to it.

Report suspected vulnerabilities through the repository
[security policy](../SECURITY.md).

## Configuration authority

Coop loads two machine-local files with full authority:

- `~/.config/coop/coop.toml`, or its XDG equivalent;
- the selected project's ignored `.coop.toml`.

The project file may grant mounts, credentials, seeds, SSH forwarding, and
other host capabilities. Coop refuses it when Git tracks it, so committed
repository content cannot grant itself those capabilities.

The selected project is writable from the guest, which means guest code can
change an ignored `.coop.toml`. The running container does not gain the new
capability immediately, but the next host-side Coop invocation will load it.
Review local configuration after running code you do not trust.

## Host exposure

Host data can enter the guest through:

- the selected read-write project mount;
- explicitly configured additional mounts;
- trusted seeds;
- selected session credentials;
- optional SSH-agent forwarding;
- Apple's runtime and virtualization interfaces.

Coop refuses `/`, the host home, ancestors of the home, and broad or
overlapping additional mounts. It does not otherwise mount the home
automatically.

Every guest process can read and modify the selected project. A read-only
additional mount prevents writes through that mount but still discloses its
contents. A read-write mount makes guest changes immediately host-visible.
Keep unrelated secrets outside mounted directories.

A project `[[volume]]` hides its corresponding host subtree only inside the
guest and stores persistent guest-owned data. It does not delete or protect the
host tree. A `[[publish]]` exposes a guest TCP service to processes that can
connect to the selected host loopback port; Coop does not add authentication or
TLS.

## Guest authority and persistence

Guest root can inspect processes, read environments and temporary files,
modify shell startup files, install software, and access all mounted project
and state data. A process can remain running after the interactive command that
started it exits.

The container root filesystem persists across ordinary entries and is
discarded on recreation. Named agent and project volumes persist across
recreation until `coop destroy`.

Per-project volumes prevent accidental sharing between different canonical
project paths. They do not isolate root-capable processes within one project.
Agent transcripts, native login state, and other volume contents are available
to guest root.

## Seeds

Seeds copy trusted host material into guest state. A top-level seed reaches
every project; a seed under a matching `[[projects]]` scope reaches that path.
Prefer narrow, explicit sources and scopes.

Use seeds for non-secret instructions, preferences, plugins, and executables.
Do not seed credentials, agent login stores, or broad configuration
directories. `coop doctor` rejects recognized sensitive seed paths.

`if-absent` is useful for one-time initialization. `overlay` may follow links
already present in a guest destination, so use it only for non-sensitive
content. There is no guest-to-host synchronization or automatic cleanup of
seeded copies.

## Session credentials

Credential definitions and project scopes are local user authorization. A
grant fixes its host source, and a matching `[[projects]]` block fixes where it
is selected. Repository remotes cannot retarget either value.

Coop reads sources on the host before changing runtime state. File and command
sources are constrained, and `git-credential` delegates lookup to the host
Git helper chain for one fixed HTTPS URL. That helper configuration is trusted
host executable input. Keychain protects a host copy at rest; it does not
protect a secret after the user authorizes its guest exposure.

For an interactive entry, Coop stages selected material in a randomized,
memory-backed guest lease and exposes only the configured environment, file,
Git, or AWS interface. It removes the lease after the command exits and clears
abandoned leases before a later entry. Secrets are not intentionally placed in
project config, container arguments, labels, mounts, seeds, or named volumes.

These controls reduce routine persistence, not guest-root authority. Any
guest-root process can read or copy a selected credential, intercept a later
entry, or preserve captured material elsewhere. Cleanup cannot erase retained
copies or revoke a static token. Provider-side revocation is required to
invalidate old material.

Use narrowly scoped, short-lived credentials. After running untrusted guest
code, recreate the container before introducing a sensitive grant. See the
[credential guide](credentials.md) for GitHub setup, rotation, and operational
verification.

Native OAuth and device-login state for Codex, Claude Code, OpenCode, and other
agents remains provider-owned state in that agent's project volume. Coop should
broker it only when the provider documents a non-persistent environment or
temporary-file interface.

## Tool supply chain

Configured Nix packages are constrained identifiers resolved from Coop's
pinned source. Public GitHub release tools require GitHub-hosted assets and a
release-provided SHA-256 digest. The digest checks that the download matches
the release metadata; it is not a publisher endorsement or independent
signature.

Guest root can install or download other software after entry. Coop is not a
software allowlist.

## SSH-agent forwarding

SSH-agent forwarding is off by default. When enabled, private-key files remain
on the host, but guest processes can request authentication and signatures from
loaded keys. Combined with outbound network access, this may be enough to act
as the user on remote systems.

Load only appropriate identities and prefer a narrower session credential when
the tool supports one.

## Network and runtime boundary

The guest has unrestricted outbound network access. Project contents, seeds,
agent state, credentials, and SSH-agent results can be sent over that network.
Coop does not provide destination filtering, DNS isolation, or per-process
egress controls.

Host isolation relies on Apple's `container` runtime, Virtualization framework,
hypervisor, kernel, and host-facing devices. A vulnerability in that stack can
defeat the boundary. Keep macOS and Apple's runtime updated.

## Non-promises

Coop does not promise:

- protection of the mounted project from guest code;
- isolation between root-capable processes in one project;
- confidentiality of a credential from guest root;
- restricted network egress;
- automatic revocation or rotation of source credentials;
- a software allowlist;
- protection from runtime, VM, hypervisor, or kernel escapes;
- safe sharing of agent-state volumes with code that should not read them.

Use Coop to make host exposure narrower and project requirements explicit.
Continue to review repositories, scope credentials, maintain backups, and
assume the mounted project is writable.
