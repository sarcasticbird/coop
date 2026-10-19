# Read-Only Team Watch

Open the keyboard-driven Team screen on Blackbook using its existing private
client configuration for the rack-mini master:

```sh
coop --master-config /absolute/private/master-client.toml team --watch
```

When that client configuration is already in the default location, use
`coop team --watch`. No credentials are copied from a guest. This uses the same
authenticated, master-identity-pinned client as ordinary `coop team`, whose JSON
output is unchanged. Run this command in a terminal. No new service is needed.

## Keys and Status

| Key | Read-only behavior |
| --- | --- |
| `r` | Refresh now; ignored while a read is already in flight |
| Up/Down, `k`/`j` | Scroll one line |
| PgUp/PgDn, Space | Scroll a page (Space scrolls down) |
| Home/End, `g`/`G` | Top/end of the view |
| `?` | Toggle contextual help |
| `q`, Escape, Ctrl-C | Quit and cancel the outstanding read |

Reads time out after ten seconds. Automatic refresh starts five seconds after
the previous read completes. Age labels update every second. At most one request
is in flight; navigation and resize remain local and responsive during it.

The screen shows agent ID/alias, host, worker instance, placement generation,
returned availability, and last heartbeat age. It does not equate a running VM
with an agent working. The current Team API supplies diagnostic heartbeat reasons,
not a complete task catalog: task, stage, phase, blocker and `depends_on` tokens
are displayed when present in those diagnostics. Repeated fields remain visible;
they are not combined into an invented job. Diagnostics may be truncated by the
producer, and dependencies may therefore be incomplete. No other endpoint is
queried to fill gaps. Missing fields and timestamps are **unknown**; future
timestamps are marked unknown with a clock-ahead warning.

An initial error is **OFFLINE; no snapshot**. Subsequent errors retain the last
successful results and mark them **STALE**, with snapshot age and a safe error
category. A successful reconnect clears that marker. Returned agent `stale`
availability is separate from a stale screen snapshot. Status labels are reported
observations, not progress estimates. Raw transport errors are not shown.

All keys and refreshes only read Team. There are no message, ping, lifecycle,
goal, scheduler or model-wake operations, and no private message reads.

## Integration Handoff

Tracking: [task #102](https://github.com/sarcasticbird/coop/issues/102); its linked
PR carries the exact source artifact and validation. This is the monitoring slice
of native goal `goal_SGYSDUPHMYNP67EDX4ANUU2D7W`, not completion of the broader TUI.

`internal/tui/team.New(ctx, source, masterID)` returns a Bubble Tea `*Model` with
`Init`, `Update` and `View`; `Source` exposes only
`Team(context.Context) ([]coordination.Presence, error)`. Call `Close` on unmount
to cancel reads. `Run` supplies the standalone alternate-screen adapter. A parent
shell should intercept navigation/quit keys before forwarding messages if it
wants to switch tabs instead of exiting. Task #94 should integrate this view,
not build a duplicate Team dashboard. Broader fleet integration remains pending.

## Safe UAT: `fleet-tui-uat-v1`

Fixture checks, using the repository Flox environment:

```sh
flox activate -- go test ./internal/tui/team ./cmd/coop -run 'Test(ReturnedStates|FailureStale|SlowRead|RefreshCadence|ResizeScroll|AgeUnknown|TeamJSON)'
flox activate -- env CGO_ENABLED=1 go test -race ./internal/tui/team ./cmd/coop
```

Race instrumentation requires a C compiler on `PATH` (or a valid `CC` override).
The current Flox manifest provides Go but does not install a C compiler.

The fixtures exercise busy, idle, paused, dependency-blocked/recovering and stale
availability, unknown fields, initial failure, retained stale snapshots,
reconnect, repeated refresh with a slow read, cancellation, terminal dimensions,
help/scrolling, JSON compatibility and authenticated GET-only transport. They
use local disposable servers, not production fleet operations.

Operator-assisted Blackbook/rack-mini checklist (pending until separately evidenced):

1. Record the exact CLI head/build and environment revision `fleet-tui-uat-v1`.
2. Confirm Blackbook's existing client selects the intended master without
   printing its private configuration or token. Open the command above.
3. Compare visible identity, placement, availability and diagnostics with
   `coop team` from that same configured client. Missing details must say unknown.
4. Use help, scroll, narrow/resize the terminal, refresh repeatedly, and quit.
   No bird should receive a message or wake because of these operations.
5. With a disposable local fixture or an operator-provided already-offline
   endpoint, verify initial error, stale retention after a successful read,
   successful reconnect and prompt quit during a slow read. Do not stop, restart,
   reconfigure or deploy a production service to manufacture an outage.
6. Robin reviews the exact head independently. Wren records private exact-head
   QA in the designated Coop report, including what operator checks remain
   pending. Author fixture results are not independent QA or real-operator UAT.

No production fleet mutation, deployment, restart, release or permission expansion
is part of this checklist. Real-operator UAT and the full fleet/TUI goal are not
claimed complete by this slice.
