# Named Servers And Inventory

This is the first backend slice of remote fleet support. It adds private server
references and read-only local/remote inventory. Full action routing, terminal
transport, and the companion TUI are separate work; none is implied by a server
registration. No service is installed, restarted, deployed, or provisioned.

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

## Private References

The version-1 JSON registry lives in the user configuration directory at
`coop/servers/registry.json` (normally `~/.config/coop/servers/registry.json`,
including on macOS, alongside existing Coop configuration).
`XDG_CONFIG_HOME` selects the configuration root and must be absolute. The dedicated
`servers` directory must belong to the effective user with no group/other
permissions, normally mode 0700. The registry and its lock file must be regular,
effective-user-owned files, normally mode 0600. Symlinks in any path component
are refused; use physical absolute paths. Existing unsafe permissions are not
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
decision. Future actions must re-resolve ownership, configuration, lifecycle,
and master-client authority on the selected host using existing controllers.

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

`internal/hostrouter` is shared by CLI and future in-repository UI consumers. It
does not import CLI presentation or TUI packages:

- `Registry.List`, `Change`, `Resolve`: strict private reference CRUD and selection.
- `Host`, `Target`, `InventoryEntry`, `HostInventory`, `Snapshot`: typed identity and partial-result data.
- `InventoryClient.Inventory(ctx, host)`: context-aware read adapter; implementations must honor cancellation.
- `Router.Watch`: buffered stream of independently completed host results, safe error codes, and host-stamped identities.
- `Router.Inventory`: sorted complete snapshot of the same stream.
- `Registry.ResolveTarget`: revalidates selected registration/address, kind, and stable ID without loading local profiles for a remote target.
- `ActionRequest`, `ActionResult`, `ActionClient`: execution boundary, including target identity and retry request ID; not yet wired to remote execution.
- `TerminalClient`, `Attachment`, `TerminalSize`: attachment boundary for read/write, resize, close/detach, and wait; no PTY implementation in this slice.

Transport errors are distinct from observed guest states. Remote responses must
describe exactly one `local` host; the router stamps the selected trusted
registration onto validated entries. The host identifier is a client registry
identity, not a new remotely authenticated UUID. SSH host-key checking provides
server authenticity. SSH alias resolution changes remain the responsibility of
trusted user configuration.

## Command Coverage

| Surface | Local / Omitted Host | Registered Remote Host In This Slice |
| --- | --- | --- |
| `server add/list/update/remove` | Private reference management | Rejected; management is client-local |
| `inventory` | Combined by default; explicit local isolates it | Selected-host inventory |
| `ls` | Existing local list unchanged | Selected-host inventory |
| Entry / guest argv / `shell` | Unchanged | Rejected; PTY/action transport is dependent work |
| `up`, `down`, `status`, `sync`, `apply`, `rebuild`, `destroy` | Unchanged | Rejected before local execution |
| `backup`, `restore`, credential refresh/watch | Unchanged | Rejected; no local archive or credential path is sent |
| Worker status/pause/resume/requests, `ping`, `approve` | Unchanged | Rejected; existing authority checks will be reused by dependent routing |
| `init`, `doctor`, `upgrade`, master, MCP, TUI | Unchanged | Rejected; TUI belongs to companion work |

Unknown/empty remote selections fail before local execution. This table records
the deliberately bounded first slice, not complete remote command parity.

## Validation Boundary

Fixtures cover private registry CRUD, schema and permissions, symlink refusal,
credential-reference boundaries, concurrent updates, absent agents, legacy local
discovery, duplicate identity/name collisions, stale registrations, streaming
local results, timeouts, partial/offline hosts, response limits, malformed remote
output, and no-fallback CLI dispatch. No test needs a real SSH server or Apple
VM. Live two-host/operator-assisted acceptance, PTY behavior, independent
exact-head engineering review, and private QA for `fleet-tui-uat-v1` remain
separate required evidence. This change performs no live fleet mutation.
