# Fleet Shell

The Bubble Tea shell consumes `hostrouter` identities and interfaces. Inventory
updates arrive independently through `Inventory.Watch`; transport owns bounded
concurrency and deadlines. The UI keeps the last successful snapshot and its age
after host failures. An error never means an empty fleet. Failed-host snapshots
remain visible but cannot dispatch actions.

`RunWithServices` accepts inventory, registry, action, and terminal dependencies.
`Run(runtime.Runtime)` preserves the existing CLI entry point and local session
lifecycle safeguards. The local compatibility adapter delegates to the existing
TUI lifecycle code, including session locks, profile checks, and project identity
checks. Concrete remote action/terminal launch wiring awaits backend task #95;
this checkpoint does not claim remote lifecycle or terminal completion.

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
| `u`, `s`, `d`, `enter` | Fleet start, stop, destroy confirmation, terminal |
| `a`, `e`, `d` | Server add, update, remove confirmation |

Forms use `tab` to switch fields, `backspace` to edit, `enter` to save, and `esc`
to cancel. Server names cannot be renamed by an update. Removal and destroy
identify the captured host and destination and require `y`. Busy mutations
serialize further mutations; inventory results from before an action are dropped.
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

Operator UAT and complete CLI action/terminal integration remain pending. Required
independent exact-head review and private `fleet-tui-uat-v1` QA are not replaced by
these author-run fixture tests.
