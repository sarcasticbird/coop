# Fleet Shell

The Bubble Tea shell consumes `hostrouter` identities and interfaces. Inventory
updates arrive independently through `Inventory.Watch`; transport owns bounded
concurrency and deadlines. The UI keeps the last successful snapshot and its age
after host failures. An error never means an empty fleet. Failed-host snapshots
remain visible but cannot dispatch actions.
Host health and stale age wrap on their own lines, independently of clipped SSH
references; retained failed-host target rows are also marked `[stale]`.

`RunWithServices` accepts inventory, registry, action, and terminal dependencies.
`Run(runtime.Runtime)` preserves the existing CLI entry point and local session
lifecycle safeguards. The local compatibility adapter delegates to the existing
TUI lifecycle code, including session locks, profile checks, and project identity
checks. Remote actions use #95's `SSH.Execute` command contract, including the
captured target and server-side project path. Agents pass their exact profile
selector instead of a controller project path. A confirmed destroy supplies the
answer to the existing receiving CLI confirmation; no new force-delete handler
or ownership bypass exists. Cancellation and connection failure can leave an
unknown remote outcome: refresh and inspect before retrying. No mutation retries
automatically.

Remote terminal entry suspends Bubble Tea through `tea.Exec`, invokes the backend
`TerminalClient`, forwards resize events, and reserves `Ctrl+]` for local detach.
Ctrl+C is sent to the remote application. Normal exit drains output before
returning the remote status. Detach/cancellation closes and reaps the attachment;
raw terminal mode is restored and the browser redraws and refreshes on return.
PTY output can include private SSH diagnostics; keep transcripts private. Existing
local entry still returns `Result` to the CLI's normal session/agent entry path,
preserving its credential and first-build handling; it exits this TUI instance.

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

Forms use `tab` to switch fields, `backspace` to edit, `enter` to save, and `esc`
to cancel. Server names cannot be renamed by an update. Removal and destroy
identify the captured host and destination and require `y`. Busy mutations
serialize further mutations; Escape requests cancellation when supported and
Ctrl+C quits. Inventory results from before an action are dropped.
Coordination is explicitly a later workflow slice, not a functioning screen.
Bootstrap, services, diagnostics, worker controls and maintenance belong to the
follow-on complete-action workflow and command-coverage documentation, not to this
Fleet/Servers shell slice.

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
