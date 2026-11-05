# Coop table

One Bubble Tea model renders local and registered-host inventory as a table.
Its only actions are attach, native Kata and a direct message composer. See
[the user guide](../../docs/fleet-tui.md).

- `tui.go`: inventory refresh, stable selection, table and foreground entry.
- `work.go`: current task observations, mapped through the host-owned stable bird
  identity and the master's current placement. No scheduler or execution authority.
- `message.go`: the master's canonical team conversation, one visible draft, one
  send gesture. An uncertain send keeps its payload/request ID for explicit retry.
- `terminal.go`: return from the existing CLI with terminal modes and descriptor
  flags restored. Host routing and the SSH PTY bridge remain in their existing
  packages; the TUI does not implement another attachment backend.

Failed inventory retains cached rows but disables attachment. Selection and
draft recipients are captured identities, never row indexes or display names.
No refresh dispatches a message, starts a VM or changes credentials.

```sh
go test ./internal/tui ./cmd/coop
go test -race ./internal/tui ./internal/hostrouter
go vet ./...
```

Focused tests cover stale and reordered hosts, stopped Coops, replaced workers,
current Kata selection, direct sends and identical-input retry, narrow terminals,
and real-PTY attachment/return. There are no command-parity matrices or approval
screen campaigns to maintain.
