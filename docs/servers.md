# Named Servers And Remote Routing

Issue #95 extends the #90 private server registry and inventory with CLI action
routing and SSH terminal transport. A registered `--host` runs supported commands
using the Coop already installed on that server, not the controller's runtime,
project, or credentials. Registration and transport install, restart, deploy, or
provision no services. Explicit lifecycle commands still have their normal effects
on the selected server. Companion TUI integration and live acceptance are separate
evidence, not implied by this backend contract.

## CLI Contract

Configure destinations in your existing trusted SSH configuration first. Coop
does not edit that configuration, read key files, copy folders, or acquire new
credentials. An address is an SSH alias, hostname, IP address, or `user@hostname`.
Use SSH configuration for ports, identities, proxies, and connection policy.
URLs, password-bearing addresses, options, shell expressions, and paths are
rejected. Examples use fictional aliases:

```sh
coop server add workshop coop-workshop
coop server add lab operator@coop-lab
coop server list
coop server update workshop coop-workshop-next
coop server remove lab

coop inventory                         # local plus all registered hosts
coop inventory --json                  # versioned, deterministically sorted snapshot
coop inventory --timeout 2s            # per-host deadline, maximum 1m; default 5s
coop --host workshop inventory --json  # just this registered destination
coop --host workshop ls                # selected host's inventory table
coop --host local inventory            # local only; never reads the registry

coop --host workshop --remote-directory /srv/project status
coop --host workshop --remote-directory /srv/project shell
coop --host workshop --target-kind interactive --target-id coop-captured-id shell
coop --host workshop --agent robin status
coop --host workshop --agent robin shell
coop --host workshop --remote-directory /srv/project backup /srv/backups/project.zip
```

Server names are lowercase ASCII letters/digits/hyphens, start with a letter,
have no trailing hyphen, and are at most 63 characters. `local` is reserved.
Adding the same name/address again and updating to the current address are
idempotent. Reusing a name with another address requires `update`. Updating or
removing an unknown name is an error. The registry allows up to 64 servers.
Removing a reference never stops a host or removes its remote data.

Existing commands without `--host`, and commands with `--host local`, retain
their existing local behavior. `coop ls` remains its original local list;
`coop inventory` is the explicit combined view. Flags precede guest commands, as
before: in `coop codex --host example`, `--host example` belongs to Codex.
Server and inventory commands reject `--agent` and `--credentials` rather than
looking up local profiles or copying local grants to another host.

`--remote-directory` is a global flag selecting an **absolute server-side project
directory**. Project operations require it, a captured interactive target, or a
persistent agent selection. The project operations are entry/guest argv, `init`,
`up`, `down`, `status`, `shell`, `apply`, `rebuild`, `destroy`, `backup`, and `restore`.
The controller's cwd is never forwarded. `--target-kind interactive --target-id ID`
uses the runtime ID captured from inventory, not its display name; the server
refreshes inventory, resolves the project, and checks that its runtime identity
still matches before executing. A vanished or changed selection requires refresh.

`--agent NAME` automatically captures a persistent `agent` target whose ID is the
server's configured profile name. Equivalently, `--target-kind agent --target-id
NAME` supplies that selection. An explicit `--agent` must match an agent target;
agents cannot also select a project directory, and interactive targets cannot
select an agent. The receiver still applies the existing command-specific
`--agent` support restrictions; remote routing does not make every command an
agent operation. Remote-only selectors, including `--tty`, are rejected with local
or omitted `--host`.

All command paths are remote-owned: `--config`, `--master-config`, `--body-file`,
MCP `--project`, backup destinations, restore archives, master backup/restore
directories, and epoch paths. No controller file is opened or uploaded for these
arguments. Relative paths, where the existing command permits them, are relative
to the selected server directory or the remote process's default cwd, never the
controller cwd. Prefer absolute paths. Master API backup destinations retain
their existing meaning on the configured master host, which can differ from the
SSH execution host. `--credentials` passes grant names for server-side resolution,
not local credentials. The controller retains only SSH destination references;
there is no copying of credentials, configuration, archives, or project folders.

## Private References

The version-1 JSON registry lives in the user configuration directory at
`coop/servers/registry.json` (normally `~/.config/coop/servers/registry.json`,
including on macOS, alongside existing Coop configuration).
`XDG_CONFIG_HOME` selects the configuration root and must be absolute. The dedicated
`servers` directory must belong to the effective user with no group/other
permissions, normally mode 0700. The registry and its lock file must be regular,
effective-user-owned files, normally mode 0600. Symlinks in any path component
of that private storage are refused. The user-selected configuration root and
its `coop` directory may be symlinks, such as a Stow-managed dotfiles location;
Coop resolves that location before opening the private `servers` directory.
Existing unsafe permissions are not
silently repaired. Writes use a descriptor-relative lock, private temporary
file, atomic rename, and file/directory sync through the existing host-control
private-file safeguards.

Only the version and named SSH destination references are persisted. Keys,
tokens, credential contents, credential paths, local project paths, and host
inventories do not belong in the registry. `server list` prints the registered
names and destinations, never resolved SSH configuration or authentication
diagnostics. Keep real configuration and inventory output private; do not add
them to the repository or publish them as test evidence.

## Inventory Semantics

Each row has `Target{Host, Kind, ID}`. A `Host` contains its registered name and
destination; `local` has no destination. `Kind` distinguishes `agent` from
`interactive`. Agent IDs are profile names; interactive IDs are runtime names.
Display names are not routing keys. The same display name or ID on two hosts,
or in two kinds, does not collide. Changing/removing a registration invalidates
targets from the old address; consumers must refresh rather than reroute a
stale selection.

Local discovery reuses `fleet.Runtime`, runtime parsing, and ownership records:
owned agent machines are included and internal machine child containers are
hidden. Configured profiles that are not in that instantiated inventory are
inspected through the existing agent backend, including absent profiles.
Interactive containers include legacy discovered Coops; listing does not adopt
them or confer control authority. Project paths come from the existing
project-mount validation. `Owned` is display information, not an authorization
decision. Actions re-resolve ownership, configuration, lifecycle, and master-client
authority on the selected host using existing controllers.

The remote executes the fixed read-only command
`coop --host local inventory --json` through SSH, so profiles, ownership,
runtime, and configuration are resolved there, not from the client's working
directory. Both ends need a Coop version supporting this protocol, and `coop`
must be on the remote noninteractive SSH PATH. Registration does not upgrade
either end. SSH is noninteractive, uses strict known-host checking, disables
agent, X11, and port forwarding and local-command execution, and uses the existing
user's authentication references. Host-key trust must already be provisioned.
The trusted SSH configuration and executable remain part of the user's trust
boundary. Existing MCP authentication and host-bound references are unchanged;
this SSH inventory is not an MCP credential or policy bypass.

Queries have independent deadlines and at most eight remote queries run at a
time. Local work never waits for a remote slot. The table streams completed
hosts, including local results before slow remotes; cross-host table order can
vary. JSON waits for the bounded snapshot and sorts hosts and identities.
Malformed, oversized (over 4 MiB), or foreign-host responses are rejected.
Raw remote stderr is never included in output. Unavailable hosts have an empty
`entries` array **and an explicit error**, never a fabricated stopped state or
an apparently successful empty inventory. Healthy local/remote results survive
other host failures. A malformed/unsafe registry preserves local inventory and
sets `registry_error` to `registry_unavailable`. Exit status is nonzero for
partial inventory; explicit `--host local` remains independent of that registry.

## Backend Contract

`internal/hostrouter` is shared by CLI and in-repository UI consumers. It
does not import CLI presentation or TUI packages:

- `Registry.List`, `Change`, `Resolve`: strict private reference CRUD and selection.
- `Host`, `Target`, `InventoryEntry`, `HostInventory`, `Snapshot`: typed identity and partial-result data.
- `InventoryClient.Inventory(ctx, host)`: context-aware read adapter; implementations must honor cancellation.
- `Router.Watch`: buffered stream of independently completed host results, safe error codes, and host-stamped identities.
- `Router.Inventory`: sorted complete snapshot of the same stream.
- `Registry.ResolveTarget`: revalidates selected registration/address, kind, and stable ID without loading local profiles for a remote target.
- `ActionRequest`, `ActionResult`, `ActionClient`, `SSH.Execute`: streamed command execution with captured identity and existing command-specific request IDs.
- `TerminalClient`, `Attachment`, `TerminalSize`, `SSH.AttachCommand`: real local PTY plus forced remote SSH PTY, with read/write, resize, close/detach, and wait.

Transport errors are distinct from observed guest states. Remote responses must
describe exactly one `local` host; the router stamps the selected trusted
registration onto validated entries. The host identifier is a client registry
identity, not a new remotely authenticated UUID. SSH host-key checking provides
server authenticity. SSH alias resolution changes remain the responsibility of
trusted user configuration.

## Execution And Terminal Contract

Actions run the fixed remote executable `coop host-exec ENVELOPE`. The envelope is
versioned JSON encoded as unpadded base64url; argv and paths are not interpolated
into a shell command, and stdin remains available to the remote command. It is
encoding, not encryption or authorization. SSH provides the encrypted channel.
The receiver validates the bounded envelope (95 KiB JSON before encoding on send,
128 KiB envelope on receive, at most 1024 argv entries, at most 64 KiB per argument),
forces local dispatch on the server, rejects nested remote selectors, and runs
the same local handlers and safeguards. Both ends must support this protocol;
the server's installed `coop` must be on its noninteractive SSH PATH. Nothing
uploads or upgrades a binary automatically.

Before execution the controller re-resolves the registration and captured target.
Unknown, empty, unsafe, or stale selections fail without local fallback. SSH uses
batch authentication, strict known-host checking, no agent/X11/port forwarding,
no local-command execution, and a three-second connection timeout. This is not a
three-second operation deadline: long-running commands retain their context.
Non-PTY execution uses `ssh -T`, streams stdin/stdout, discards raw remote stderr
(including SSH diagnostics), and returns a safe error and remote exit code.
Disconnect/SSH status 255 means the remote outcome may be unknown. No action is
automatically retried; preserve existing request IDs and inspect state before
retrying mutations. Offline inventory remains a partial-result error, not a
stopped guest; an offline action remains a transport error, not local execution.

Entry/guest argv, `shell`, `init`, and `destroy` automatically use the terminal
path; other routed commands can request it with global `--tty`. Terminal stdin
must be an actual terminal. The CLI saves and restores raw-mode state, bridges
input/output through a **real local PTY** running `ssh -tt`, and propagates
`SIGWINCH` dimensions. Ctrl-C is sent as a byte to the remote terminal. **Ctrl-]**
is the local detach key; it is not forwarded. Detach or input EOF closes/reaps
the SSH transport without issuing `down`, `stop`, or `destroy`. It is not a promise
that an arbitrary foreground remote process survives SSH hangup. Cancellation
closes the attachment and preserves the context error. Normal exit drains output
before returning the remote status and restoring the caller's terminal. PTY
stdout/stderr are merged and can contain SSH diagnostics: keep terminal transcripts
private, unlike sanitized non-PTY transport errors.

`TerminalClient.Attach` uses normal selected agent/project entry, including the
persistent agent's application attachment; it does not manage
the calling UI. An `Attachment` caller (including a TUI) owns raw mode, input
bridging, detach-key policy, resize forwarding, cancellation, output draining,
`Wait`, and `Close`, as well as restoring/redrawing its interface on return.
`Close` detaches and unblocks pending I/O, intentional detach makes `Wait` succeed,
and context cancellation makes it return the context error. `Wait` is idempotent
and leaves output readable; the caller must still `Close` to release the PTY.

## Command Coverage

The matrix audits the executable tree in `cmd/coop`: `main.go`, `agent.go`,
`credentials.go`, `master*.go`, `thread.go`, `message.go`, `team.go`, `goal.go`,
`mcp.go`, and `server.go`, against the explicit allowlist in `remote.go`.
Grouping below enumerates every current command, not just the #90 inventory slice.
Routing eligibility is not permission to bypass the receiving handler's checks.
This is an allowlist/handler audit, not a claim that all integration fixtures pass;
use the head-pinned fixture results to establish implementation readiness.

| Surface | Local / Omitted Host | Registered Remote Host / Existing Safeguards |
| --- | --- | --- |
| `server add/list/update/remove` | Private reference management | Rejected; management is client-local |
| `inventory` | Combined by default; explicit local isolates it | Selected-host inventory |
| `ls` | Existing local list unchanged | Selected-host inventory |
| Entry / arbitrary guest argv | Unchanged | Routed, automatic PTY; explicit project or agent, existing seeds/grants/spec reconciliation |
| `shell` | Unchanged | Routed, automatic PTY; same project/agent shell handlers |
| `init` | Unchanged | Routed, automatic PTY; explicit project, terminal requirement; not an agent operation |
| `up`, `down`, `status` | Unchanged | Routed; explicit project/agent; lifecycle/ownership checks; runtime errors are not stopped state |
| `sync` | Unchanged | Routed; requires configured `--agent`; existing clone/grant policy |
| `apply` | Unchanged | Routed; explicit project/agent; lock, stopped-state and maintenance/recovery checks; unsupported image changes preserve existing guest |
| `rebuild` | Unchanged | Routed; explicit project/agent; existing image/release-tool/core lock handling |
| `destroy` | Unchanged | Routed, automatic PTY; explicit project/agent; existing destructive confirmation, no implicit deletion on detach |
| `backup`, `restore` | Unchanged | Routed; explicit project/agent; server-owned archives, recovery lock, stopped backup / absent restore target, manifest/mode/identity validation |
| `doctor`, `upgrade` | Unchanged | Routed host operations; server configuration/runtime/core state; no automatic per-project rebuild after upgrade |
| `credentials refresh` | Unchanged | Routed; requires `--agent`; named grants resolved on server under existing grant restrictions |
| `credentials watch` | Unchanged | Routed foreground watcher; use `--profile` filters, not `--agent` (which selects the existing single-profile refresh path); refreshes running profiles only, existing interval/retry and GitHub App grant checks |
| `worker status`, `worker pause`, `worker resume`, `worker requests` | Unchanged | Routed; requires `--agent`; existing worker control/authority and private request handling |
| `ping`, `approve` | Unchanged | Routed; requires `--agent`; prompt/request-ID validation or existing host GitHub identity, goal/repository approval checks |
| `master serve` | Unchanged | Routed foreground server; remote `--config`, private config/database/epoch and existing listener/TLS policy; no service installation |
| `master backup` | Unchanged | Routed master-client API call; existing authority and consistent owner-only snapshot directory on master |
| `master restore` | Unchanged | Routed offline restore on SSH host; private backup, empty destination, independent epoch lineage, optional dry-run; no server restart |
| `master reconcile status`, `master reconcile pause`, `master reconcile resume`, `master reconcile evidence`, `master reconcile resolve` | Unchanged | Routed; master-client authority, exact restore epoch, evidence/proof/reason requirements; explicit resume, no automatic recovery clearance |
| `master placement set` | Unchanged | Routed allowlist; master authority, stopped/reconciled placement, expected/new generation and restore epoch; command-local `--host`/`--agent` are placement fields, not fleet selectors |
| `master history import`, `master history list`, `master cutover` | Unchanged | Routed; remote body file, existing authority/request ID/digest/source-retirement checks; history does not create messages, decisions, or wakes |
| `thread list`, `thread create`, `thread read`, `thread members` | Unchanged | Routed; server master-client config, membership/owner checks including exact prior member set |
| `message send`, `message status`, `team`, `inbox` | Unchanged | Routed; server master-client principal, membership/visibility and receipt/request-ID checks; body file is remote |
| `goal propose`, `goal list`, `goal authority`, `goal approve`, `goal reject`, `goal pause`, `goal resume`, `goal complete`, `goal reopen` | Unchanged | Routed; existing master authority, exact revision and decision sequence for decisions; routing is not owner approval |
| `mcp serve`, `mcp stdio`, `mcp inspect`, `mcp register` | Unchanged | Routed; server-side absolute private `--config`, rejects agent/grant selectors; existing MCP authentication, host binding, policy/ledger, and registration checks remain; serve/stdio are foreground |
| `tui` | Existing dashboard | Rejected as a remote executable; launch the controller UI locally; companion fleet UI consumes backend interfaces |
| Hidden `host-exec ENVELOPE` | Validated receiver entry point | Not recursively routable; version/size/target/allowlist and nested-selector guards |
| Cobra `help`, `--help`, `--version`, generated `completion bash/fish/powershell/zsh`, hidden completion callbacks `__complete`/`__completeNoDesc` | Local parser/help/completion output | Controller-side parser utilities, not remote actions; do not prove connectivity or remote version |
| Non-executable groups: `server`, `credentials`, `worker`, `master`, `master reconcile`, `master placement`, `master history`, `thread`, `message`, `goal`, `mcp` | Help/usage | No standalone action; choose an enumerated leaf |

New executable commands are not implicitly routable: they require an explicit
allowlist update. `master serve`, `mcp serve`, `mcp stdio`, and `credentials watch`
stay attached foreground commands over SSH. They do not install a launchd/systemd
unit, daemonize, restart existing services, or turn an offline host into a healthy
one. A lost connection has no transparent reconnect or replay; the operator must
inspect remote process/service state before restarting anything. Avoid `--tty`
for machine-readable MCP stdio; PTY echo and merged output are not that protocol.

## Validation Boundary

Fixtures cover private registry CRUD, schema and permissions, symlink refusal,
credential-reference boundaries, concurrent updates, absent agents, legacy local
discovery, duplicate identity/name collisions, stale registrations, streaming
local results, timeouts, partial/offline hosts, response limits, malformed remote
output, and no-fallback CLI dispatch. No test needs a real SSH server or Apple
VM. Real local-PTY subprocess fixtures exercise read/write, resize, exit/drain,
detach, cancellation and disconnect without a live SSH server. CLI bridge fixtures
exercise Ctrl-] versus Ctrl-C and return/cleanup. These are not live remote PTY or
customer acceptance results.

Run the reproducible **fixture portion** of `fleet-tui-uat-v1` from this checkout:

```sh
scripts/test-fleet-routing.sh BACKEND_FULL_COMMIT_SHA TUI_FULL_COMMIT_SHA
```

The script requires two full commit IDs, rejects a backend ID that differs from
the current checkout HEAD, records both IDs, UTC time, platform, selected Go
environment/toolchain versions, and worktree status. The TUI ID is a supplied
cross-artifact reference, not verification of another checkout or its UI. Tests
run against the current worktree; dirty status means this is not exact-commit
clearance. Capture stdout/stderr privately if durable evidence is needed.

Through `flox activate`, it checks Go formatting without rewriting source, runs
`go vet ./...`, and runs uncached `go test -race -count=1 ./...`. A formatting
failure is reported but does not suppress vet/race evidence; any failed stage
makes the final status nonzero. It invokes no live Coop/SSH command, installer,
deployment, or service restart. Tests use isolated temporary fixtures and SSH
stand-ins; a temporary PATH guard blocks unmocked SSH after Flox activation.
Go/Flox dependency acquisition may still require network/cache access. The guard
is a fixture safeguard, not an OS sandbox for arbitrary newly added tests.
The fixture subprocess uses umask 022. Run in an unprivileged development checkout:
root bypasses the existing unreadable-directory fixture, and a host-installed
`/opt/coop-release-tools/bin` changes the existing image PATH fixture's assumptions.
These pre-existing environment-sensitive failures are not fleet acceptance.

**Still pending:** operator-assisted Blackbook/rack-mini two-host acceptance,
live offline/reconnect behavior, remote PTY resize/detach/cancel and UI return on
both hosts, and the companion TUI's command/selection behavior at the recorded
heads. Independent current-head engineering review and Wren customer QA/live UAT
remain separate gates. A passing fixture script claims neither Wren QA nor live
`fleet-tui-uat-v1` completion. This documentation/fixture work performs no live
fleet mutation.
