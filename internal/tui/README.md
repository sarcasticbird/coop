# Fleet Shell

The Bubble Tea shell consumes `hostrouter` identities and interfaces. Inventory
updates arrive independently through `Inventory.Watch`; transport owns bounded
concurrency and deadlines. The UI keeps the last successful snapshot and its age
after host failures. An error never means an empty fleet. Failed-host snapshots
remain visible but cannot dispatch actions.
Host health and stale age wrap on their own lines, independently of clipped SSH
references; retained failed-host target rows are also marked `[stale]`.

`RunWithServices` accepts inventory, registry, action, terminal, and foreground
CLI dependencies. The production launch adapter in `cmd/coop/tui.go` reexecutes
the same binary through the validated CLI receiver for local operations, isolating
Cobra globals and cwd from the event loop. `Run(runtime.Runtime)` retains its
local compatibility adapter for embedded callers. Remote actions use #95's
`SSH.Execute` command contract, including the
captured target and server-side project path. Agents pass their exact profile
selector instead of a controller project path. A confirmed destroy supplies the
answer to the existing receiving CLI confirmation; no new force-delete handler
or ownership bypass exists. Cancellation and connection failure can leave an
unknown remote outcome: refresh and inspect before retrying. No mutation retries
automatically.

Production terminal entry suspends Bubble Tea through `tea.Exec`, invoking the
normal local CLI attachment or #95's remote CLI/PTTY bridge. The remote bridge
forwards resize events and reserves `Ctrl+]` for local detach.
Ctrl+C is sent to the remote application. Normal exit drains output before
returning the remote status. Detach/cancellation closes and reaps the attachment;
raw terminal mode is restored and the browser redraws and refreshes on return.
PTY output can include private SSH diagnostics; keep transcripts private. Local
entry now returns to the browser rather than exiting it. The exec wrapper restores
the controlling terminal even when a child changes settings before exit. Existing
credential and first-build handling remains in the receiving CLI. Explicit grant
names from `coop --credentials ... tui` are forwarded to applicable entry actions.

Selection and confirmation capture the complete comparable `hostrouter.Target`,
including the configured SSH address. Refresh results cannot replace it with a
row index or same-named target. Target dispatch checks the current successful
snapshot and registry; the backend remains responsible for mutation authorization
and ownership. Server forms capture the original registration and reject an
updated or removed reference before submitting an edit.

## Navigation

| Key | Behavior |
| --- | --- |
| `1`, `2`, `3` | Fleet, Coordination, Servers |
| `up` / `down`, `k` / `j` | Select target or host |
| `tab` | Focus/show target details |
| `r` | Refresh without stacking inventory requests |
| `?` | Contextual keyboard help |
| `esc` | Close help, cancel form/confirmation, return to browser |
| `q`, `Ctrl+C` | Quit; Ctrl+C also works in overlays |
| `PgUp`, `PgDn` | Scroll all host/status rows, details, confirmations and forms |
| `u`, `s`, `d`, `enter` | Fleet start, stop, destroy confirmation, terminal |
| `a`, `e`, `d` | Server add, update, remove confirmation |
| `x` | Capability/state-gated target actions in Fleet; host actions in Servers |
| `v` | Advanced Services, About, completion and command-coverage runbook |

Forms use `tab` to switch fields, `backspace` to edit, `enter` to save, and `esc`
to cancel. Server names cannot be renamed by an update. Removal and destroy
identify the captured host and destination and require `y`. Busy mutations
serialize further mutations; Escape requests cancellation when supported and
Ctrl+C quits. Inventory results from before an action are dropped.
Coordination uses the injected identity-pinned master client, separately from
Fleet host selection. Team, threads, inbox, private compose/reply, receipts and
exact-revision goal workflows are documented in
[`docs/fleet-tui.md`](../../docs/fleet-tui.md), including complete command coverage
and the `fleet-tui-uat-v1` checklist. Coordination forms use Ctrl+S to preview and
submit, Ctrl+V to reveal masked input, and Ctrl+R to retry identical frozen input.
Team (`2`, then `t`) uses a full-page list and selected-agent diagnostic detail.
Arrows select; Enter inspects the captured master placement; Escape returns with
the list selection/scroll intact. Detail refresh is read-only and verifies agent,
host, worker instance and placement generation. Heartbeat is not work progress;
report, receipt, work and wait observations have separate ages. Unsupported,
denied, missing or stale evidence never implies idle, healthy or stopped. Fleet
inventory has no authoritative master-placement binding, so Fleet Details keeps
work unknown rather than guessing by name. See the stage-2 boundary and exact-head
reproduction instructions in [`docs/tui-b-runtime.md`](../../docs/tui-b-runtime.md).
Operational forms, safeguards, command coverage and fixture UAT are documented in
[`docs/tui-operations.md`](../../docs/tui-operations.md). Forms never accept raw
shell commands or credential values. Config and archive paths are host-owned.
Permissions and configured worker/grant prerequisites remain server-authoritative;
the inventory contract does not advertise those permissions. CLI denials are
reported, never bypassed or interpreted as permission to retry.

## Fixture Validation

From the repository root, in a Go 1.26.9 development environment:

```sh
go test ./internal/tui ./cmd/coop
go test -race ./internal/tui ./internal/hostrouter
go vet ./...
```

The fixture suite covers duplicate display names, selection/confirmation across
reordering, stale registration addresses, lost targets, changed project paths,
pre-action refresh races, loading and partial failures, reconnect, malformed or
wrong-host results, server CRUD and recoverable validation errors, narrow views,
focus, and help. No test requires a registered live server or mutates a fleet.

`bash internal/tui/testdata/uat-fleet-shell.sh EXPECTED_HEAD` reruns this shell's
fixture checks at an exact commit. Append `--operator-checklist` to print the
read-only/manual operator procedure. It never connects to a server, edits a live
registry, restarts a service, or dispatches a lifecycle action automatically.

Operator-assisted live UAT remains pending. Required independent exact-head review
and private `fleet-tui-uat-v1` QA are not replaced by these author-run fixture tests.
