# Inspected Source And Coverage

Read-only baseline: `c10925823b23b0964c416fcf931e37aba55afb14` on `main`.
This packet changes documentation and static validation only. The privately
reported quiet-render repair is uncommitted elsewhere; it was not inspected,
copied, overwritten or assumed present in this baseline.

## Contracts Inspected

| Source | Existing behavior used by the proposal |
| --- | --- |
| `internal/tui/fleet.go` | Bubble Tea shell, Fleet selection/details, registry forms, operations, scroll and window resize |
| `internal/tui/coordination.go` | Team/Threads/Inbox/Goals, captured forms, preview/submit, exact retry, bounded reader/follow |
| `internal/tui/team/team.go`, `diagnostics.go` | Separate read-only Team watch; diagnostics, work/blocker observation, stale/error retained snapshots |
| `internal/coordination/types.go`, `diagnostics.go` | Presence and diagnostic report fields; handling, phase, waits, observed timestamps |
| `internal/masterclient/config.go`, `client.go`, `diagnostics.go` | Pinned master config and diagnostics reads, no Fleet routing inference |
| `internal/hostrouter/registry.go` | Local versus named SSH registration; host identity is more than display name |
| `cmd/coop/tui.go`, `thread.go`, `team.go` | Existing CLI dispatch, private master config, thread operations and separate Team watch |
| `docs/fleet-tui.md`, `docs/tui-operations.md` | Exhaustive command inventory, safety and foreground transport/runbooks |

Presence fields include agent, host, worker instance, placement generation,
state, reason and last-seen. Heartbeat is normally 15s and stale after 45s.
Heartbeat age is not work-progress age. Basic Coordination Team does not yet
embed the richer selected-agent diagnostics interface. Separate Team watch
parses some reason strings; those are not structured task queues. Rich reports
can expose task/PR/head/job/session, handling, wait reason/dependencies and
report/receipt times. Absent or denied reports remain unknown; no percentages,
completion forecast or cross-agent job graph is inferred.

## Proposed, Not Implemented

Host/filter picker, no automatic target, persistent context strip, aligned
split workspace, focus-owned viewports, full-page compact drill-in, integrated
diagnostics, private reader masking and in-session draft suspension are proposed
UI work. Current reader bodies are unmasked even though forms default masked;
the global masking proposal must not be mistaken for a current privacy guarantee.
Current Escape clears Coordination forms; proposed draft protection changes that
interaction explicitly. No durable draft storage or profile switching is promised.
Current PgUp/PgDn behavior is not a full-page pane scroll; that is future work.

## Retain Every Command And Safety Path

The authoritative, exhaustive existing command-to-disposition matrix remains
[`docs/fleet-tui.md`](../../fleet-tui.md#command-coverage-inventory), including its
inventory markers and `TestFleetTUICommandCoverageInventory` test. This design
does not replace it with a smaller command set. Future implementation must keep
that test passing and update representation, not silently drop leaves.

| Families | Proposed discovery location; existing handlers retained |
| --- | --- |
| default entry, shell, tui, ls, inventory, status | Fleet, Connect, status result, About launch/runbook |
| up, down, destroy | x Actions and retained u/s/d shortcuts; existing confirmations |
| rebuild, apply, sync, backup | x Target maintenance; state/kind/path/ownership checks |
| upgrade, restore, init, doctor | Servers x Host actions; explicit captured host, no local fallback |
| credentials refresh/watch | Foreground configured-reference actions; no secret values in UI |
| worker status/requests/pause/resume, ping | Target actions; worker/master/goal controls remain distinct |
| server list/add/update/remove | Servers list/forms; remove reference is not remote deletion |
| team, team inspect | Coordination Team; integrate diagnostics only through existing read contract |
| inbox | Coordination i; agent identity denial is not empty data |
| thread list/create/read/members | Threads, n, reader, owner exact-set membership action |
| message send/status | c/p composer, s receipt; immutable request and two-stage submit |
| goal propose/list/authority/approve/reject/pause/resume/complete/reopen | g Goals and contextual actions; exact revision/sequence and owner checks |
| legacy approve | v CLI runbook; never remapped to native goal decision |
| mcp register/inspect | Advanced host actions; private reference and canonical path validation |
| mcp serve/stdio, master serve | v Services runbook only; never implicitly started |
| master backup/restore/reconcile status/pause/resume/evidence/resolve | v Recovery runbook; separate recovery authorization |
| master placement set/history import/history list/cutover | v Administration runbook; no implicit migration |
| help, version, completion bash/zsh/fish/powershell | ? contextual help, v About and CLI runbook |

Hidden host-exec/tui-exec remain validated transport adapters, not new user
actions. UI availability never asserts permission: backend denial is final.
Host operations retain captured name/address/kind/ID/runtime/project path,
literal argv preview, authoritative pre-dispatch validation and receiving CLI
revalidation. State/kind restrictions, locks, archive safeguards, ownership and
grants remain. No remote error falls back locally; no raw shell text is built.
Goal/membership forms preserve exact revision/decision sequence or exact old/new
member sets. Conversation is never approval, scheduling or delivery authority.
