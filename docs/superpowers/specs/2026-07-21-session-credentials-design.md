# Session Credentials Design

## Summary

Coop will support named credential grants that are acquired on the host and
made available only to an interactive guest entry. Trusted user configuration
can include grants automatically, and the user can add grants explicitly with
`--credentials`. Credentials are staged in guest memory for the lifetime of
the entered command and are never added to the container specification,
project mount, persistent agent volumes, or seed destinations.

This complements the existing seed mechanism. Seeds remain durable
host-to-guest synchronization for configuration, skills, helper programs, and
initial agent state. Credential grants become the supported mechanism for
machine credentials such as Git credential stores, AWS sessions, GitHub
tokens, and kubeconfigs.

## Goals

- Allow a user to select one or more named credentials for a Coop entry.
- Allow trusted user configuration to include credentials on every entry.
- Keep credential definitions and automatic inclusion outside repository
  control.
- Support credentials acquired from host files and host commands.
- Support secret environment values and temporary credential files without
  placing secret values in `container` command arguments.
- Preserve terminal behavior and the entered command's exit status while
  retaining host-side control for cleanup.
- Fail closed and atomically when any selected credential cannot be acquired
  or staged.
- Provide a migration path away from sensitive seed entries without breaking
  existing configurations.

## Non-goals

- Preventing a root-capable guest process from reading credentials exposed to
  another process in the same Coop VM.
- Preventing a root-capable process from persisting across entries or altering
  guest paths such as `/dev/shm` to capture credentials selected later.
- Revoking credentials that the upstream issuer does not support revoking.
- Making static credentials temporary after a guest has copied or exfiltrated
  them.
- Allowing project `coop.toml` files to define, include, or select host
  credentials.
- Refreshing credentials during a single long-running entry. A future broker
  may add refresh support if expiration during long sessions becomes a real
  limitation.
- Replacing seeds for non-secret configuration or agent-managed persistent
  authentication state.

## User experience

Trusted user configuration defines named grants and the grants included on
every interactive entry:

```toml
# ~/.config/coop/coop.toml

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
```

The root command accepts a plural list flag before the first positional
argument:

```sh
coop codex
# selected credentials: git

coop --credentials aws-dev,github codex
# selected credentials: git, aws-dev, github

coop --credentials aws-dev --credentials github
# selected credentials: git, aws-dev, github; opens a shell

coop --credentials aws-dev tui
# selected credentials on TUI entry: git, aws-dev
```

`--credentials` accepts comma-separated names and repeated uses. Selection is
the ordered, deduplicated union of `include_credentials` followed by explicit
`--credentials` values. Empty names and unknown grants are errors. Explicit
flags must precede the first agent or command token because Coop deliberately
forwards every later token unchanged.

`include_credentials` and `[credentials]` are trusted-user-only settings.
They are decoded and validated in a project file but have no effect, matching
the existing trust treatment of seeds, image settings, and SSH forwarding.

The flag is meaningful only for commands that can enter a guest: the root
command and TUI entry. Using it with lifecycle, diagnostic, listing, destroy,
or rebuild commands returns a usage error rather than pretending credentials
were applied. `coop up` never acquires credentials. TUI entry always applies
`include_credentials` and also applies explicit grants supplied when starting
the TUI.

## Architecture

The feature has four boundaries:

1. **Configuration and selection** validate trusted grant definitions and
   resolve the grants selected for one entry.
2. **Acquisition** reads a host file or executes a trusted host command without
   a shell and returns credential material plus safe metadata.
3. **Injection** converts acquired material into secret environment values,
   temporary files, and non-secret process configuration understood by the
   target tool.
4. **Lease orchestration** stages the complete bundle, runs the interactive
   command, cleans up guest material, and invokes provider revocation when a
   provider supports it.

The session and runtime packages depend only on the resulting credential
bundle and lease lifecycle. AWS, Git, and other provider behavior stays in a
focused credential package.

### Credential lease

Acquisition produces a lease containing:

- zero or more secret environment values;
- zero or more secret files with logical names and modes;
- non-secret environment values or process configuration that refer to the
  staged files;
- optional expiration time;
- safe display metadata such as provider, profile, or account identity; and
- an optional host-side revoke operation.

Secret values never implement `Stringer`, never appear in structured logs,
and are not included in wrapped errors. Safe metadata is kept separate from
secret material so status output cannot accidentally serialize the latter.

### Acquisition sources

The initial source types are:

- `file`: read one regular host file after `~/` expansion. Symlinks are
  followed host-side, consistent with seeds. Directories, devices, sockets,
  and missing files are errors.
- `command`: execute an argv vector directly on the host, never through a
  shell. Standard output is the secret payload. Standard input and standard
  error remain attached to the user's terminal for authentication prompts and
  instructions; standard error is not copied into Coop error values. Command
  acquisition has a ten-minute deadline. The helper uses a dedicated
  foreground process group. Coop reads at most 1 MiB plus one byte directly
  from the helper's pipe and kills the whole group as soon as the limit is
  exceeded. A bounded pipe wait prevents escaped descendants from blocking
  Coop indefinitely.
- `aws-profile`: run `aws configure export-credentials --profile <profile>
  --format process` on the host, parse the versioned process-credential JSON,
  and expose safe profile, account, and expiration metadata. When
  `require_expiration` is true, missing or elapsed expiration is an error.

Credential names use the same lowercase alphanumeric-and-hyphen grammar as
agent names. Configuration supports at most 32 grants. At most 16 grants may
be selected for one entry. Each acquired payload is limited to 1 MiB and the
complete secret bundle is limited to 8 MiB.

### Injection types

The initial injection types are:

- `environment`: expose the acquired payload as one validated environment
  variable. One trailing line ending from command output is removed; embedded
  newlines are rejected and should use file injection instead.
- `file`: stage the payload as a mode `0600` file and set the configured
  `path_env` variable to its guest path.
- `git-credential-store`: stage the credential store as a mode `0600` file
  and apply process-local Git configuration that first resets inherited
  credential helpers, then selects `credential.helper = store --file
  <temporary-path>`. It does not create or replace
  `~/.git-credentials`.
- `aws`: stage a one-profile shared credentials file and set
  `AWS_SHARED_CREDENTIALS_FILE`, `AWS_PROFILE`, and
  `AWS_EC2_METADATA_DISABLED` for the entered process. Before setting those,
  unset inherited `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and
  `AWS_SESSION_TOKEN` values so they cannot override the selected profile. The
  source must be an `aws-profile` source so arbitrary bytes cannot be
  mislabeled as parsed AWS credentials. Conversely, an `aws-profile` source
  must use this injection;
  generic payload adapters cannot consume its structured AWS material.

Environment variable names are validated before acquisition. Selecting grants
that would assign the same secret environment variable, path environment
variable, or process-local Git configuration is an error; order never decides
which credential wins.

## Entry lifecycle

For every interactive entry, Coop performs these operations in order:

1. Load and validate configuration, including definitions that are not
   selected for this entry.
2. Resolve `include_credentials` and `--credentials`, deduplicate names, and
   validate injection conflicts.
3. Acquire selected grants in deterministic order. If acquisition fails,
   revoke already acquired leases in reverse order and do not start or enter
   the VM.
4. Run the existing `Session.Up` flow, including image reconciliation and
   seeds. If it fails, revoke all acquired leases.
5. Create a random lease directory below
   `/dev/shm/coop-credentials/<lease-id>` with mode `0700` and stream the
   complete bundle through `container exec -i`. Secret values are sent only on
   standard input, never in argv, labels, container environment, or mounts.
6. Start a guest wrapper with only non-secret lease paths in its command
   arguments. The wrapper loads secret environment values, applies
   process-local configuration, and runs the requested shell, agent, or
   command.
7. Wait for the command while preserving TTY input/output, terminal resize,
   signal behavior, and its exact exit status.
8. The guest wrapper removes its own lease directory when the command exits.
   The host performs the same removal as a second cleanup attempt.
9. Revoke acquired leases in reverse order when supported, then exit with the
   entered command's status. If the command succeeded, a cleanup or revocation
   failure becomes a Coop error. If the command failed, Coop reports cleanup
   failures but preserves the command's non-zero status.

Interrupt, termination, and hangup signals are converted into cancellation
causes for this entire lifecycle. Context-aware acquisition and seed transports
stop immediately; other bounded runtime operations finish and then observe
cancellation. Revocation uses a non-canceled cleanup context, and a lease
cleanup registered before staging still runs if cancellation arrives during
transport or entry. A second managed signal abandons graceful cleanup and
restores normal signal termination so a wedged lifecycle can still be stopped.

Lease IDs make concurrent entries independent. Before loading any secret, the
guest wrapper atomically writes its guest PID and Linux process start-time
token into the lease directory. Every new interactive entry removes directories
whose recorded process identity is no longer running; checking both values
prevents PID reuse from retaining a stale lease. A newly staged directory
without an owner identity is protected by a short staging grace period so a
concurrent scrub cannot remove it before its wrapper starts. A malformed or
empty owner file is treated like a missing owner and ages out under the same
grace period. Abrupt host termination with
`SIGKILL` may leave material in the running VM until that scrub or until the VM
stops; `/dev/shm` prevents the material from persisting in the container root
filesystem or named volumes.

## Runtime changes

The current `ExecInteractive` replaces Coop with `unix.Exec`. Credential
leases require Coop to regain control after the guest command exits. The
runtime abstraction will therefore gain an interactive execution path that:

- launches `container exec -it` as a child process;
- attaches the current standard streams;
- gives a dedicated child process group terminal foreground ownership, then
  restores Coop's original group only while that child still owns the terminal;
- when the child stops, returns terminal ownership to Coop, stops Coop's job,
  and hands the terminal back to the child when the shell continues it without
  stealing the terminal if the shell resumes the job in the background;
- preserves terminal resize and delivers interrupt, termination, and hangup
  signals exactly once whether they originate from the terminal or target
  Coop directly;
- gives an unresponsive interactive child two seconds after cancellation,
  then kills its complete process group so cleanup can proceed;
- reports signal-relay failures and falls back to killing the direct runtime
  client so Coop is not left waiting forever;
- returns the guest command's exit code distinctly from infrastructure errors;
  and
- allows deferred cleanup and revocation before Coop exits with that code.

Credential-free entries may use the same path so terminal semantics have one
implementation. The existing argv-vector guarantee remains: user arguments
are never joined into or reparsed as shell source. Only Coop's fixed guest
wrapper is shell code, and all variable data reaches it through validated
positional arguments, files, or standard input.

## Security model

The explicit trust boundary is unchanged:

- Only trusted user configuration can define credentials or include them by
  default.
- A repository cannot name a host path, execute a host command, or add a grant
  to `include_credentials`.
- `--credentials` is a direct user action and can select only a grant already
  defined in trusted user configuration.
- Credentials are available to root-capable processes in the same guest while
  the entry is active. Because the guest container persists, a root-capable
  process can remain resident or alter guest filesystem paths to capture
  credentials selected for a later entry. Coop does not claim process or
  filesystem isolation from guest root inside the VM.
- Removing staged material ends Coop's exposure but does not invalidate a
  credential copied by the guest. Provider expiration or revocation controls
  upstream validity.
- Coop displays expiration when a source can report it. Sources such as files
  and opaque commands are labeled `validity: source-managed` rather than
  producing a warning on every entry. `require_expiration` is valid only for
  sources that report expiration and fails closed when it is absent.
- The VM retains unrestricted egress, so an exposed credential can be sent
  over the network.

Coop prints the selected grant names and safe metadata before entry. It never
prints access keys, tokens, secret file contents, source command stdout, or
secret environment values.

## Seeds and migration

Seeds continue to work unchanged. The documentation will distinguish them as
durable synchronization rather than the preferred credential mechanism.

Migration is opt-in:

1. Define a credential grant corresponding to an existing sensitive seed.
2. Add its name to `include_credentials` when the same always-on entry behavior
   is desired.
3. Remove the sensitive seed only after the grant works.

`coop doctor` warns, but does not fail, when a seed source resembles a common
machine-credential path such as `.git-credentials`, `.aws/credentials`,
`.netrc`, or a broad `.kube` tree. Agent login caches and agent state volumes
remain persistent because they are application state, not optional machine
capabilities.

## Error handling

- Configuration errors identify the grant and field but never include secret
  material.
- Unknown or conflicting selected grants fail before any provider executes.
- Acquisition and staging are atomic from the entered command's perspective:
  the command either receives the complete selected set or does not start.
- Cleanup and revocation attempt every lease and aggregate failures.
- An infrastructure failure returns a Coop error. A successfully launched
  guest command returns its own exit status even when cleanup also reports an
  error.
- Missing host executables produce actionable messages naming the executable
  and grant.
- Expired provider output is rejected before staging.

## Testing

Unit tests cover:

- global-only trust behavior for `include_credentials` and credential
  definitions;
- unknown keys, invalid names, count and payload limits, and malformed source
  or injection combinations;
- comma-separated and repeated `--credentials` parsing, ordering, and
  deduplication;
- conflicts between environment, file-path, Git, and AWS injections;
- file acquisition, command argv preservation, missing executables, bounded
  output, and command failures;
- AWS process-credential parsing, expiration requirements, and redaction;
- bundle staging through stdin with no secrets in runtime argv;
- reverse-order cleanup/revocation after acquisition, startup, staging, and
  guest-command failures;
- concurrent lease directory isolation and abandoned-directory scrubbing;
- exact guest exit-code propagation; and
- migration warnings for sensitive seed paths without false failures.

Runtime tests use fake `container` executables to verify argv, standard streams,
signals, and exit status without nested virtualization. Manual verification on
macOS checks TTY behavior, terminal resize, interruption, Git credential-store
operation, AWS identity and expiration display, cleanup after normal exit, and
cleanup after interruption.

## Documentation changes

The README will document:

- the difference between seeds and credential grants;
- trusted configuration examples for Git, command-to-environment, temporary
  files, and AWS profiles;
- `include_credentials` and `--credentials` selection semantics;
- the requirement that the flag precede the guest command;
- the guest-root and unrestricted-egress limitations;
- the difference between exposure lifetime and upstream credential validity;
  and
- migration from sensitive seed entries.
