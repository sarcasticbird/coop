# B Runtime Implementation Manifest

Environment revision: `coop-tui-b-runtime-v1`. This is stage 0 of
[task #115](https://github.com/sarcasticbird/coop/issues/115), approved native goal
`goal_CJ3FNEQKI6DAL7AKADQKRVUNCT`, revision
`9a400eb725c4a40237cf4f7baf39a23980c21582417827eb7fd9d10c1294ce62`.
This manifest is an implementation/test handoff, not a new design approval,
runtime QA clearance, rollout or release authorization.

## Reconciled Baseline

| Input | Immutable revision and disposition |
| --- | --- |
| Implementation starting main | `255e0029a57ff4c039b2561356ebd4c007b579b0`, verified against origin main on 2026-10-20 |
| Assigned base refresh | `0888eb6c0bcc212ce0692e9e35ea49d7a699ac62`, fetched and verified against origin main on 2026-10-20, then merged without conflicts; adds only #122 catalog discovery's separate read budget, worker regression test and master documentation, with no TUI/design changes |
| Quiet cached refresh | `7e6fd1d19df601310819987036eae6961dedfb4e` (#112), ancestor of the starting main |
| Successor changes | #113 (`172f01d`) scoped worker continuations; #114 (`255e002`) coordination-only worker discovery; neither changes the TUI design |
| Accepted B packet | [handoff.md](https://github.com/sarcasticbird/coop/blob/5d79a84031045fe8dd8f3eeb345cadea953e0724/docs/design/quiet-tui/handoff.md), [source.md](https://github.com/sarcasticbird/coop/blob/5d79a84031045fe8dd8f3eeb345cadea953e0724/docs/design/quiet-tui/source.md) and sibling interaction/viewport artifacts in PR #111 |
| Independently reviewed design head | `9fc62de7d7cfe57b6b3dc3444d06f8d5c27d5a2e`; diff to packet head `5d79a84031045fe8dd8f3eeb345cadea953e0724` contains only #114 worker files, no design delta |
| Previously delivered work | Reuse #90/#95/#92/#93/#94/#102 and PRs #97/#100/#103/#104/#107/#108/#109; no replacement backend tasks |

The accepted packet is not on this main. Reference its immutable content; do not
merge its old design-only delivery metadata or change historical ledgers. Its
design-only restrictions describe that historical goal. The approved native
implementation goal above supplies this task's authority. Reconcile later main
or substantive design changes explicitly before using a new baseline.

## Retained Contracts

| Contract | Source and characterization evidence |
| --- | --- |
| Quiet cached reads, initial/navigation/write/blocked-control feedback | `internal/tui/refresh_test.go`, `internal/tui/team/refresh_test.go`; `TestRuntimeBaselineSizesAndQuietRefresh` adds the three-size matrix |
| Successful empty host differs from unavailable host; cached failure is not empty | `fleet_scroll_test.go`, `fleet_health_test.go`, `fleet_test.go`; shared runtime fixture includes both entryless cases |
| Target is host registration + kind + ID, not name or row index | `internal/hostrouter/router.go`, `registry.go`; confirmation/reorder/refresh tests in `fleet_test.go` |
| Authority is rechecked at dispatch; changed address, removed target and unreachable host refuse | `TestRuntimeBaselineAuthorityAtEverySize`, `operations_test.go`, receiving CLI tests in `cmd/coop/tui_test.go`; `Owned` and diagnostics never authorize |
| VM lifecycle, availability, work/blocker, freshness are separate observations | `internal/coordination/types.go`, `diagnostics.go`, `internal/tui/team`; `TestRuntimeBaselineDiagnosticInputs` tests fresh/stale/missing reports through a read-only source |
| Captured master/principal/context and exact request retries | `internal/tui/coordination.go` and its authority, permission, message-retry tests; reuse `internal/masterclient` and current handlers |
| Exhaustive command and safety parity | `docs/fleet-tui.md`, `docs/tui-operations.md`; `TestFleetTUICommandCoverageInventory`, `TestTUICommandCoverageExhaustive` |

Stage 0 did not implement B. Stage 1 below implements only Ledger/Details. Stage 2
adds the Team diagnostics flow described below. Stage 3a adds full-page threads
and masked readers. Stage 3b adds the full-page forms and memory-only protection
described below; these are source behaviors pending independent exact-head gates.

## Stage Acceptance Map

| Stage | Bounded outcome and dependency | Required evidence still to collect |
| --- | --- | --- |
| 0, this task | Baseline, synthetic inputs and reproducible commands | Author checks below, Robin exact-head review and Wren exact-artifact runtime QA |
| 1, after verified stage 0 delivery | All-host grouped Ledger, optional host filter, no automatic target, full-page Details, explicit Connect | All three sizes; duplicate names; empty/loading/offline/cached hosts; reorder/remove/address/filter/focus stability; no direct Ledger Connect or silent retarget |
| 2, after stable target/placement mapping | Honest work/blocker/freshness through existing reports | Denied/unsupported/missing/stale/skewed reports; moved placement and late old response; no inferred idle, wake or action authority |
| 3, on the reconciled shell | Full-page threads, masked reader, selected-message reply, composer, preview, receipt | Captured context; focus/scroll on follow/resize; memory-only Keep/Discard/Return and quit guard; identical frozen retry on uncertain send; CLI/MCP compatibility |
| 4, on Details and captured actions | Distinct master/SSH setup, contextual help, all actions, complete long confirmations, attach/return | Command inventories; three-size full-context review; stale/refusal/cancel; authorized PTY attach/detach/return without transport changes |
| 5, after prior stage deliveries | Integrated acceptance and reproducible owner UAT packet | Light/dark/no-color; Unicode/combining/control text; polling/fault/focus/privacy; operator-assisted Blackbook/rack-mini journeys; explicit owner UAT remains separate |

Each stage requires Robin's independent review of Hawk's exact head and Wren's
private exact-runtime-artifact QA in `coop-tui-b-runtime-v1`, plus passing required
checks before source delivery. Rook coordinates readiness and the consolidated
owner handoff. Static packet QA is not runtime QA.

## Synthetic Runtime Inputs

`internal/tui/tuifixture.New(at)` returns independent native Go data, not a live
client. `Sizes()` supplies `120x40`, `80x24`, `40x20`. Fleet and Team adapters live
in their own test packages so no production internals are exported for tests.

- Local running agent and remote stopped/absent agents share a display name but
  have distinct target identities. Registration aliases and all IDs are synthetic.
- A successfully empty remote host and an unreachable host with no snapshot are
  distinct. Existing snapshot-retention/recovery tests cover cached offline rows.
- Running/idle presence is paired with reported waiting work; stopped/paused
  presence has stale diagnostics; absent/unknown presence has unknown heartbeat
  and no report. No lifecycle-to-work or health-to-authority inference is valid.
- `Moved` preserves agent identity but changes host, instance and generation;
  `ChangedRegistration` preserves name but changes address. The latter drives
  dispatch refusal tests. The former is reusable stage-2 input, not evidence that
  integrated placement mapping or rejection of old-placement reports is complete.
- Adapters inject recording action clients or read-only diagnostic functions.
  No real customer bodies, credentials, config, live SSH or fleet mutation are used.

## Reproduction And Evidence

Use the exact published PR source SHA in a separate checkout. Record `git rev-parse
HEAD`, platform, `flox activate -- go version`, commands and actual results. The
published commit identifies the source; do not guess its SHA inside its own file.
Flox supplies Go, and its manifest documents automatic Go 1.26.9 toolchain
selection. Run from the repository root:

```sh
flox activate -- gofmt -l internal/tui/runtime_baseline_test.go internal/tui/team/runtime_baseline_test.go internal/tui/tuifixture
flox activate -- go test ./internal/tui/... ./internal/config ./internal/hostrouter ./internal/masterclient ./cmd/coop -count=1
flox activate -- go vet ./internal/tui/... ./internal/config ./internal/hostrouter ./internal/masterclient ./cmd/coop
flox activate -- go test -race ./internal/tui/... ./internal/config ./internal/hostrouter ./internal/masterclient ./cmd/coop -count=1
flox activate -- env NO_COLOR=1 TERM=dumb go test ./internal/tui/... ./cmd/coop -count=1
git diff --check
```

The fixture matrix runs as part of `go test`; to isolate it use
`flox activate -- go test ./internal/tui/... -run TestRuntime -count=1 -v`.
These are model-level runtime tests, not screenshots or a substitute for an
interactive terminal/PTY journey. No production restart or real mutation is
needed for stage 0. Wren should independently run these against the published
artifact, assess retained quiet-refresh/feedback/authority behavior and report
stage-0 results only. Later B journeys remain pending, not waived.

Record independent QA with native `qa` using private `report_body`, exact artifact,
environment revision, dimensions, commands, outcomes and untested boundaries.
Do not place private operator evidence or customer bodies in a public PR. Real
Blackbook/rack-mini attach/UAT requires the separately authorized operator fixture;
do not imply synthetic tests established live transport or production behavior.

### Author Baseline Results

Executed on 2026-10-20, Linux arm64, Go `go1.26.9` through `flox activate`:
repeated after merging assigned base `0888eb6c0bcc212ce0692e9e35ea49d7a699ac62`
with the same results below. The new base's regression test also passed with
`flox activate -- go test ./internal/worker -run '^TestNativeDiscoveryHasBudgetForMultiGoalCatalog$' -count=1`.

| Check | Actual result |
| --- | --- |
| `gofmt -l` on added Go files | Passed, no output after formatting |
| Scoped `go test` command above | Passed all seven packages, including command inventories and existing refresh/authority tests |
| Scoped `go vet` command above | Passed |
| `NO_COLOR=1 TERM=dumb` test command above | Passed all TUI packages and CLI tests; not an interactive theme assessment |
| Scoped `go test -race` command above | Environment blocked: `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1` |
| Same race command with `env CGO_ENABLED=1` | Environment blocked: `cgo: C compiler "gcc" not found`; no race tests executed |
| `git diff --check` | Passed |

Flox also emits a shell-detection warning and defaults to bash; ordinary tests
and vet still succeed. No environment manifest or system packages were changed.
Repeat race validation in a compiler-equipped Go environment before claiming that
gate passed. Independent Robin review, Wren QA, live PTY and operator UAT remain
pending. These author checks are not independent clearance or permission to
deliver with required checks outstanding.

## Stage 1 Ledger/Details

[Task #116](https://github.com/sarcasticbird/coop/issues/116) starts at delivered
stage-0 main `0c8db5e`. PR #111 still points to accepted packet
`5d79a84031045fe8dd8f3eeb345cadea953e0724` as checked on 2026-10-20; the diff from
reviewed `9fc62de7d7cfe57b6b3dc3444d06f8d5c27d5a2e` has no design-file delta.

- All/no-target startup, grouped local/remote hosts, exact-registration host
  filter and hidden counts, full-page Details at all widths. Ledger Enter and
  lifecycle shortcuts never dispatch. Connect requires Details and confirmation,
  then uses the existing foreground adapter and receiving CLI checks.
- Stable identity survives reorder/resize/cached reads; removal/placement/path/
  registration changes invalidate captured intent rather than selecting another
  row. Failed inventory and registry reads retain cached rows with actions blocked.
- Ledger scroll/filter/selection survive Details and foreground return. Context
  and feedback remain visible while paging; long target identities wrap instead
  of disappearing behind truncation. Lifecycle is labeled separately from unknown
  work/blocker information; stage 2 supplies actual mapped diagnostics.
- `ledger_test.go` adds three-size navigation/Connect callback tests, initial
  loading, blocked controls, shortcut non-dispatch, captured filter identity,
  invalidation that cannot revive and cached registry failure. Existing tests now
  explicitly open Details before exercising the same action/authority boundaries.

Author validation on Linux arm64 through Flox: scoped tests and vet above passed;
`NO_COLOR=1 TERM=dumb` TUI/CLI tests passed. Formatting and `git diff --check`
passed. Fitting race tests for `./internal/tui/... ./internal/hostrouter` remain
environment-blocked: default CGO disabled; retry with `CGO_ENABLED=1` fails because
`gcc` is absent. No race test ran and no environment packages were changed.

For independent stage-1 QA, run the scoped reproduction commands above and
`flox activate -- go test ./internal/tui -run 'TestLedger|TestFleet' -count=1 -v`
against the published exact head. Repeat the accepted target-discovery, filter,
offline/reconnect and resize journeys at 120x40, 80x24, 40x20. Model tests inject
the foreground callback without attaching a live guest; they do not establish
real Blackbook/rack-mini PTY behavior. Robin exact-head approval, Wren private
exact-runtime QA, CI, live operator UAT and rollout remain separate gates. Stage
4 still owns complete long-confirmation review and the remaining setup/help parity.

Stage-1 review follow-up: Robin's populated-Ledger resize case reproduced on
`f0a58f5322f9bd7e1819dc725a5ebc07c65c6524`: shrinking from 120x40 to 40x20
left profile 14 selected but off-screen. Resize now recomputes the selected
physical row and re-anchors only when its highlight would be outside the viewport;
visible manual paging is retained. `TestLedgerResizeKeepsSelectedHighlightVisible`
covers wide/narrow/medium transitions and Details entry for the visible identity;
`TestLedgerResizePreservesVisibleManualScroll` guards intentional paging. The
focused reproduction failed before the fix and passed after it. Scoped tests,
vet, no-color checks, formatting and diff checks passed again. Race testing in
this author environment is still blocked by disabled CGO / missing `gcc`; Robin's
previous-head race pass is not a current-head validation result.

Additional author focus regressions cover Details resize followed by Esc/Tab,
inventory reorder through the actual update handler, and Help/Services return.
These failed on `caf18883fcd0e803c4d90293734ec7714adbaaa2` before correction.
The same Ledger viewport calculation now handles resize, refreshed visible focus
and return reconciliation. Overlay scroll is saved independently; a deliberately
paged-away target does not steal the viewport on return or cached refresh. Tests
check both visible stable identity and intentional paging without dispatch.

The focused focus/resize tests passed three repetitions after correction. Scoped
tests, vet and no-color checks above passed again. Race validation also passed
using an existing compiler, without installing packages or changing configuration:
`flox activate -- env CGO_ENABLED=1 CC=/nix/store/z2qkl46kv87zcs2sfwfjgc75nq0hbzhf-gcc-wrapper-15.3.0/bin/gcc go test -race ./internal/tui/... ./internal/hostrouter -count=1`.
Earlier blocked author attempts remain historical results, not the current race
outcome. Independent current-head engineering review and private exact-artifact
QA must be renewed; these author regressions do not clear either gate.

## Stage 2 Team Diagnostics

[Task #117](https://github.com/sarcasticbird/coop/issues/117) starts at delivered
stage-1 main `50a791c`. Accepted PR #111 remains at
`5d79a84031045fe8dd8f3eeb345cadea953e0724`, verified on 2026-10-20; no later design
delta is being adopted.

- `2`, then `t`, opens a full-page Team list at every size. Arrows select by
  master AgentID, not alias/index. Enter captures the exact placement and opens
  a full-page read-only diagnostic detail. Esc cancels pending detail reads,
  rejects late results, and restores list selection/scroll. Removed selections
  stay unselected instead of silently targeting another agent for a prompt.
- Detail refresh reuses `Team` and optional `AgentDiagnostics` reads. Agent,
  host, worker instance and placement generation must all match. Master restore
  epoch is checked against authenticated identity when supplied. Changed or
  missing placement clears the old report; older observations/receipts cannot
  replace a newer cached report. No diagnostic value authorizes controls.
- Master availability/heartbeat, receipt, report observation, work observation
  and wait observation have separate labels/ages. Missing timestamps are unknown,
  future timestamps identify clock uncertainty, and old observations are marked
  stale. Handling and reasons remain reported observations, not inferred health,
  progress percentages or recovery advice. Faults retain explicitly stale cached
  evidence; arbitrary transport errors are never rendered.
- Team watch keeps its CLI entry point, refresh cadence and existing presence
  display, and shares the diagnostic renderer. Its selected detail now also
  verifies full placement, rejects old reports and restores list scroll. CLI/MCP,
  scheduler, report backend, lifecycle and permission contracts are unchanged.

### Fleet Binding Contract

The initial artifact `75ff31f` identified a genuine gap: fleet identity (`Host
registration + kind + profile/runtime ID`) did not bind a target to a master
placement. Robin's review correctly required resolving this before stage delivery.
A profile's communication AgentID remains only a message destination; registration
names remain distinct from coordination HostIDs. Neither supplies an implicit join.

The fix adds one optional **host-owned observation pin** to an existing agent
profile in the host's global `coop.toml`. For example, on a synthetic host with an
already-configured `fixture-bird` profile:

```toml
[profiles.fixture-bird.diagnostics]
master_id = "fixture-master"
agent_id = "fixture-principal"
host_id = "fixture-master-host-id"
worker_instance_id = "fixture-worker-instance"
generation = 1
```

The operator/provisioning source must copy the complete master and placement tuple
from that machine's actual configured worker, not invent identifiers from VM names,
SSH destinations or aliases. This explicit target binding is trusted host
configuration, not a discovery heuristic or proof from a diagnostic report. Update
it when replacing a worker instance or changing placement generation. The TUI never
writes it, reads worker configuration/credentials, registers placement or changes
master configuration. No live profile has been modified as part of these tests.

Local inventory exports only this nonsecret pin on that exact agent target,
including running, stopped and absent machines. Remote inventory transports the
same optional `diagnostics` field through the existing validated SSH JSON route;
transport registration normalization does not rewrite its master placement.
Unbound profiles retain their existing inventory shape. Partial/invalid pins,
interactive-target pins and duplicate placement pins within a host are rejected;
the TUI also refuses ambiguous same-placement claims across hosts.

Details shows VM lifecycle separately from the report's handling, reason,
work/wait fields and observation ages. Before reading a report, it requires the
configured master client to match the pin and the master's current Team snapshot
to contain the exact agent/host/worker-instance/generation. Reports must match
that tuple and the authenticated restore epoch. A missing, denied, unsupported,
unreachable, stale or clock-skewed observation remains unknown/stale. Cached
failures remain visible even while scrolled down. Reads never grant action
authority; existing dispatch/receiving checks remain final. A changed binding
invalidates captured Details/confirmation intent and late reads, rather than
silently moving the view or a control to a replacement worker.

Genuinely unbound targets still show unknown work and explain the missing pin;
Team remains independently navigable. Existing installations need no migration,
and no fallback guesses are added. The optional inventory field is additive;
older strict clients that do not recognize a bound row fail closed as an invalid
inventory instead of guessing a mapping. CLI flags, MCP actions, the report API,
scheduler and master placement authority are unchanged.

### Reproduction

Run the scoped commands above, adding `./internal/coordination ./internal/master`
to ordinary tests/vet. Isolate the new full-page journeys with:

```sh
flox activate -- go test ./internal/tui -run '^TestTeam' -count=1 -v
flox activate -- go test ./internal/tui/team -count=1
flox activate -- env CGO_ENABLED=1 CC=/nix/store/z2qkl46kv87zcs2sfwfjgc75nq0hbzhf-gcc-wrapper-15.3.0/bin/gcc go test -race ./internal/tui/... ./internal/hostrouter ./internal/masterclient ./internal/master -count=1
```

Synthetic tests cover the three sizes, duplicate aliases, selection/reorder/resize,
missing reports, access denial, unsupported diagnostics, unavailable reads, changed
host/agent/instance/generation, removal, cancellation/late results, out-of-order
observations, clock skew, stale data, cached refresh quietness and safe error text.
They do not establish live guest placement or Blackbook/rack-mini operator UAT.
Robin's exact-head independent engineering review, Wren's private exact-artifact
runtime QA in `coop-tui-b-runtime-v1`, required CI and source delivery remain separate
gates. No deployment, restart, release or tap publication is part of this task.

Author validation on 2026-10-20 through Flox passed: formatting; scoped tests and
vet for TUI/Team, config, hostrouter, masterclient, coordination, master and
`cmd/coop`; worker tests selected by `-run Diagnostic`; no-color TUI/CLI tests;
the race command above; and `git diff --check`. The race lane uses the already
available compiler, with no environment/package changes. Flox's shell-detection
warning and activation-lock waits did not prevent execution. These results cover
the implemented Team slice and unknown Fleet boundary, not complete stage-2
acceptance, independent review/QA or live operator UAT.

### Review Fix Validation

The follow-up adds regressions for Robin's new-epoch/earlier-observation case and
scrolled Team/diagnostic denial at all three sizes. Epoch ordering compares times
only within the same restore epoch; older-epoch results cannot replace newer
evidence. Error/stale feedback uses the existing fixed status row, outside the
scroll viewport, without inserting cached-refresh loading rows. CLI Team watch
shares this pinned diagnostic feedback and epoch behavior.

New config/inventory tests cover host-only TOML loading, partial pins, running and
absent profiles, unbound defaults, invalid/duplicate pins, and SSH JSON transport.
Fleet model tests cover mapped running-VM/waiting-agent observations at all sizes,
unknown unbound duplicate names, master and placement mismatch, cross-host
ambiguity, placement/registration/removal changes, stale cached inventory, scrolled
faults, pending-intent invalidation, late reads and epoch clock correction. An
integration test reads a real report through the existing authenticated master
HTTP handler/client and verifies no lifecycle dispatch.

Author follow-up validation on 2026-10-20 through Flox passed:

- Formatting and `git diff --check`.
- `go test` and `go vet` for `./internal/tui/... ./internal/config
  ./internal/hostrouter ./internal/masterclient ./internal/coordination
  ./internal/master ./internal/agent ./internal/workspace ./internal/fleet
  ./cmd/coop`; tests used `-count=1`.
- `go test ./internal/worker -run Diagnostic -count=1`.
- `NO_COLOR=1 TERM=dumb go test ./internal/tui/... ./cmd/coop -count=1`.
- `CGO_ENABLED=1` with the existing compiler path above, `go test -race
  ./internal/tui/... ./internal/config ./internal/hostrouter
  ./internal/masterclient ./internal/master -count=1`.

No environment/package changes were needed. Flox emitted its existing shell
warning and occasional activation-lock waits, not test failures. These fix results
supersede the initial artifact's mapped-Fleet gap, not the need for independent
Robin review, private Wren exact-artifact QA, CI or operator UAT. No live pin,
fleet, service, credentials, deployment or release state was changed.

### Feedback And Identity Follow-Up

Robin's review of `2d18ec8` found that pinned diagnostic feedback could overwrite
an immediate lifecycle/control outcome. Details now keeps the action outcome in
the fixed status row and, when both are present, puts diagnostic truth first in
the existing fixed footer. This adds no viewport rows or scroll movement. The
regression exercises actual cancellation and stale-registration refusal with
denied, stale and initially loading diagnostics at all three required sizes,
checking both fixed rows rather than merely finding text in the scrolled body.

The review also reproduced a supported `master@rack` identity being rejected by
the new binding's worker-ID grammar. Master-store identity validation is now a
shared coordination contract, used by both store opening and diagnostic binding.
Store acceptance is unchanged; the binding additionally rejects non-printable or
invalid UTF-8 identity text at its presentation boundary. Worker placement IDs
retain their stricter grammar. Tests open real fixture stores with supported
punctuation, printable Unicode and the 128-byte boundary, verify matching pins,
reject unsafe values, and load/transport `master@rack` through profile/inventory
fixtures. No live master configuration changed.

Both findings reproduced before correction. Focused regressions passed afterward;
the full scoped ordinary/vet, no-color and worker-diagnostic commands above passed
again on 2026-10-20. The race lane also passed, now including
`./internal/coordination` with the same already-available compiler. Formatting and
diff checks passed. These remain author results; the new head still needs Robin's
independent review, Wren's private exact-artifact QA and CI before source delivery.

## Stage 3a Threads And Masked Reader

[Task #118](https://github.com/sarcasticbird/coop/issues/118) starts at delivered
stage-2 main `96f88d9`. PR #111 was checked on 2026-10-21 and remains at accepted
packet `5d79a84031045fe8dd8f3eeb345cadea953e0724`; no later design delta is adopted.

- Threads and reader use one full-page body at all three dimensions, with pinned
  thread/master/principal context, contextual help/footer and fixed read/error
  feedback. Every opened reader starts masked. Explicit reveal belongs only to
  that reader; opening compose does not reveal its private input.
- Stable thread/message IDs are independent of physical scroll. Cached refresh,
  arrivals, the 500-message retention bound and Unicode rewrapping do not retarget
  reply/receipt controls. An evicted selection remains unselected. Visible focus
  survives resize; intentional paging away from selection does not snap back.
- Esc cancels pending reads and invalidates their serial before returning to the
  saved thread-list viewport/page. Old results cannot reopen a departed reader.
  Metadata/message identities are checked before commitment. Principal/master/
  restore-epoch changes fail closed; denied/missing/conflicting authority clears
  cached bodies. Transport failure retains masked cache and requires a successful
  authority refresh before another reveal.
- Existing creation, membership, Inbox, Goals, root/reply, immutable request retry
  and receipt handlers remain available. No CLI/MCP/backend contract changes.
  Full-page composer/preview, memory-only draft suspension and quit guards are
  stage 3b; this task does not claim them or interactive/live UAT.

Author validation on Linux arm64, Flox Go 1.26.9, 2026-10-21:

- Formatting, repository-wide `go vet ./...` and `git diff --check` passed.
- `go test ./internal/tui/... ./internal/masterclient ./internal/coordination
  ./internal/master ./cmd/coop -count=1` passed, including existing request/retry,
  membership, pagination, unread, quiet-refresh and command-inventory regressions.
- `NO_COLOR=1 TERM=dumb go test ./internal/tui/... ./cmd/coop -count=1` passed.
- Default race invocation was blocked by disabled CGO. With `CGO_ENABLED=1` and
  the existing compiler path documented above, `go test -race ./internal/tui/...
  ./internal/masterclient/... ./internal/coordination/... -count=1` passed.
- `go test ./...` did not pass. Unchanged baseline `96f88d9` independently
  reproduces image PATH expectations (`/opt/coop-release-tools/bin`), projectinit
  permission/mode tests, seed mode preservation and worker migration snapshot
  failure expectations in this environment. The full run also hit worker
  `TestNativeTerminalWaitAllowsConversation` TempDir cleanup; its focused baseline
  rerun passed, so that result is classified as an intermittent cleanup failure,
  not a demonstrated regression or a full-suite pass.

For independent stage-3a QA, run the scoped commands and isolate journeys with
`flox activate -- go test ./internal/tui -run '^TestConversation' -count=1 -v`.
All bodies in these fixtures are synthetic. Repeat list -> masked reader ->
reveal -> select -> root/reply/receipt -> return, scrolled follow, reconnect,
denial, Help and resize at 120x40, 80x24 and 40x20. Robin's exact-head review,
Wren's private exact-runtime-artifact QA in `coop-tui-b-runtime-v1`, required CI
and source delivery remain separate gates. No live service/fleet/release changed.

### Stage 3a Review Fixes

Robin's review of `9c683daa150372c58331c1743065af2f77e6df38` identified two
reproducible gaps. Both new regression tests failed on that head before correction.

- Reader-bound calls now compare fresh authenticated principal ID/kind, master
  and restore epoch with the captured context **before invoking the handler**,
  not just when consuming its result. A conflict dispatches no write and retains
  the original frozen request, parent, body and request ID. Tests cover root and
  reply submission, uncertain-send retry, repeated blocked retry, all four context
  changes after scheduling, and identical retry after original authority returns.
- Thread lists retain the successful page's query cursor separately from its
  Next cursor. Refresh/follow reads that page, including after reader return.
  Failed or cancelled advances cannot replace the retained query cursor. The
  regression exercises page two, explicit refresh, the actual follow tick,
  reader return and recovery after a failed page-three read.

Follow-up author validation on 2026-10-21 through Flox passed: scoped ordinary
tests for TUI, masterclient, coordination, master and `cmd/coop`; repository-wide
vet; no-color TUI/CLI tests; fitting race tests for TUI/masterclient/coordination
with the existing compiler above; formatting and diff checks. The full-suite
baseline/environment failures described above remain classified, not waived or
reported as a green full suite. These are author checks only; Robin must review
the new exact head and Wren must renew private exact-artifact runtime QA before
delivery with required CI. No merge, deployment or release is part of this fix.

### Reader Recovery Boundary

Reader recovery is now separate from the latest coordination request's success.
A reader-bound failure locks cached bodies until a successful coherent read of
that captured thread's metadata and message page. Receipt and goal inspections
may succeed independently but cannot unlock the cached reader. Both reveal
controls and rendering enforce this boundary; the masked label and footer point
to `r` for recovery. Genuine refresh recovery still requires explicit reveal.

The synthetic `TestConversationReaderRecoveryRequiresThreadSnapshot` regression
failed on `40d80bf` before correction at 120x40, 80x24 and 40x20, for metadata and
message-read failures. It passes three repetitions after correction, covering
receipt and goal inspection/return without authority reads, renderer masking,
repeated failure, successful coherent recovery and retained selected-message ID.
No private QA report content or customer bodies are included in these fixtures.

Author validation on Go 1.26.10 through Flox passed: scoped TUI/masterclient/
coordination/master/CLI tests; repository-wide vet; no-color TUI/CLI tests;
fitting race tests for TUI/masterclient/coordination with the existing compiler;
the unchanged CI vulnerability scanner; formatting and diff checks. No new
full-suite pass is claimed. Robin must independently review the new exact head,
and Wren must perform renewed private exact-artifact runtime QA before delivery
with required CI. No live service, fleet, release or deployment was changed.

## Stage 3b Composer And Receipts

[Task #119](https://github.com/sarcasticbird/coop/issues/119) starts on delivered
#118 at `94e23f7`. Accepted design PR #111 still has head
`5d79a84031045fe8dd8f3eeb345cadea953e0724`, verified 2026-10-21; no design delta
is adopted. This stage reuses the masterclient, creation, membership, message,
status and idempotency handlers, without any wake or fleet lifecycle action.

- Creation, root/reply composition, preview, frozen uncertainty and receipts are
  full-page at 120x40, 80x24 and 40x20. Master/principal remain pinned; complete
  identities, epoch, request, engagement, thread, stable parent and membership
  wrap in the reviewable body. Creation preview includes the implicit owner in
  its full membership and creation opens an empty masked reader, not a message.
- Arrows/Home/End edit a rune cursor; Tab/Shift+Tab retain independent field
  positions. Literal Unicode/newlines are preserved in the payload, while
  terminal controls are sanitized in views. Ctrl+V affects only that buffer.
  F1 opens contextual help without including private input; Ctrl+L rechecks the
  original identity without sending. Resize retains visible cursor focus and
  does not change buffer, selected parent or request identity.
- Esc on nonempty/frozen forms offers Keep/Discard/Return. Kept buffers live only
  in process memory, including focus, scroll, reveal state, captured context and
  immutable built request. Reopening the same intent resumes it; a frozen send
  takes precedence over another root/reply in the same thread. Ctrl+C, or quitting
  elsewhere with kept drafts, warns before explicit discard-and-quit. Completed
  and discarded buffers are removed from owned state. There is no draft file,
  persistence, automatic send, automatic retry or restart recovery promise.
- Preview never dispatches. The second Ctrl+S submits only after the complete
  preview can be reviewed; long views require PgDn. Uncertainty freezes input and
  request ID. Ctrl+R reuses the identical closure/payload, including after Keep;
  fresh principal/kind/master/epoch/capability checks precede dispatch. Known
  authority failure masks buffers and cannot retarget their context. The original
  authority can recheck and resume; a replacement cannot reveal the old buffer.
- `SendMessage.expected_members` is optional for existing CLI/MCP callers. The
  TUI includes its captured exact member set. The existing Send handler checks it
  atomically before insertion or idempotent replay, closing the membership-change
  race without a new transport or endpoint. This requires the matching source
  master in runtime fixtures; an older server rejecting the field is a refusal,
  not permission for an unguarded fallback. Nil legacy inputs keep their existing
  request JSON and behavior.
- Receipt views show committed message ID, master, epoch, backend, sequence and
  server-provided delivery state/reply linkage. No state implies execution or
  wake. Status checks reject mismatched message/master identity; serialized
  requests reject late results. Failures mark retained receipts stale/unknown.
  Model/form diagnostic formatting omits private buffers, including debug formats.

### Stage 3b Reproduction

Use the exact published source head, not a static packet or another build. Run:

```sh
flox activate -- go test ./internal/tui/... ./internal/master ./internal/masterclient ./internal/mastermcp ./cmd/coop -count=1
flox activate -- go test ./internal/tui -run 'TestCompose|TestCoordinationHTTPRetryAfterCommitPreservesRequest' -count=1 -v
flox activate -- go vet ./...
flox activate -- env NO_COLOR=1 TERM=dumb go test ./internal/tui/... ./cmd/coop -count=1
flox activate -- env CGO_ENABLED=1 CC=/nix/store/z2qkl46kv87zcs2sfwfjgc75nq0hbzhf-gcc-wrapper-15.3.0/bin/gcc go test -race ./internal/tui/... ./internal/masterclient ./internal/master -count=1
git diff --check
```

`compose_test.go` covers the dimension matrix, creation/no-message, draft lifecycle,
quit warnings, literal cursor editing, resize/pinned identity, synthetic diagnostic/
log/user-state-directory privacy, identity/permission refusal, complete preview,
HTTP exact-membership conflict and mismatched status. The real TLS/store retry
test now suspends and resumes after response loss before recovering one original
message with byte-identical requests. `send_membership_test.go` covers atomic
stale-set refusal, replay after membership change/restoration, normalization and
unchanged nil-input behavior. Existing reader/follow/privacy/late-result and
CLI/MCP coverage remains in the scoped suite.

Author validation on Linux arm64, Go 1.26.10 through Flox: scoped tests, repository
vet and fitting race tests passed with the existing compiler above. Initial race
attempts were blocked by disabled CGO, then missing `gcc` on PATH; no compiler was
installed or environment configuration changed. Full `go test ./...` is not green:
image PATH expectations, projectinit unreadable-directory/file-mode assumptions,
seed mode preservation and worker migration snapshot-failure assertions all
reproduce on unchanged baseline `94e23f7` in this root-run environment. These are
classified baseline/environment failures, not a full-suite pass or a waiver.

These author model/HTTP checks are not independent customer QA or interactive
PTY/operator UAT. Robin must review the exact published head and Wren must perform
private exact-artifact QA in `coop-tui-b-runtime-v1`, including create, root/reply,
preview, mask/help, draft/resume/quit and uncertain retry at all three sizes.
Required CI remains a delivery gate. No deployment, service restart, production
fleet mutation, rollout, release or tap publication is included.

### Stage 3b Review Corrections

Robin's review of `be82aee690711dfabba035cceb796143c49ed607` identified two
reproduced edge cases, now covered by synthetic regressions:

- `expected_members` uses JSON `omitzero`, not `omitempty`: a nil legacy slice
  is still absent with byte-identical request encoding, while explicit-empty
  membership remains `[]` on the wire and conflicts with a populated thread.
  The JSON round-trip test distinguishes nil/empty/nonempty; the HTTP/client
  test asserts conflict, no committed message and unchanged legacy nil behavior.
- Draft protection takes final content/footer precedence over Help, Services and
  other overlays without replacing the underlying workspace. Return restores
  the retained overlay and viewport. Update/View tests at all three dimensions
  cover active Composer Help and kept drafts in Fleet Help and Servers Services,
  including visible quit-loss warning, private-input exclusion, original request
  retention, Return and explicit discard-and-quit.

The focused regressions failed on the reviewed head before correction and passed
after correction. Re-run `TestSendMessageMembershipWireSemantics`,
`TestComposeHTTPExplicitEmptyMembershipFailsClosed` and
`TestComposeQuitWarningOwnsOverlayAndRestoresReturn` with the reproduction commands
above. These author regressions do not renew independent review or private
exact-artifact QA; both must be performed against the new published head.

Correction validation through Flox Go 1.26.10 passed: uncached scoped TUI,
coordination, master, masterclient, mastermcp and CLI tests; repository-wide vet;
golangci-lint v2.12.2 (zero issues); no-color TUI/CLI tests; fitting uncached race
tests for TUI/coordination/masterclient/master with the existing compiler above;
formatting and diff checks. No new full-suite or independent-clearance claim.

## Stage 4 Setup, Help And Actions

[Task #120](https://github.com/sarcasticbird/coop/issues/120) starts at delivered
stage-3b main `fcb8fef`. Accepted design PR #111 remains at
`5d79a84031045fe8dd8f3eeb345cadea953e0724`, verified 2026-10-21; no later design
delta is adopted. Existing masterclient, registry, action, SSH and foreground CLI
handlers remain the transport and receiving-side authority.

- Missing master setup explains existing private configuration and credential
  references, relaunch, independent Fleet availability and separate SSH setup.
  A local-only Servers list offers registration of existing SSH references.
  No token input, master switching, SSH trust bypass or implicit service startup.
  Server references wrap completely, with add/edit/remove discoverable at 40 columns.
- Server and operation editors own F1 help; `?` is literal input. Help excludes
  underlying input/output/error buffers, retains field and viewport on return,
  and cannot credit unseen removal content as reviewed. Existing Composer,
  Team and reader help remains available without revealing private bodies.
- Details start/stop, destroy and Connect all capture the same operation argv as
  the menu before consent. Configured grant references are prepared before preview
  and frozen into the invocation. Full host/kind/ID/name/path/argv and the specific
  consequence, including unencrypted backup secrets, remain completely reviewable.
  Long confirmations and server removals require contiguous PgDn review before
  `y`; geometry changes reset review. Target/placement changes invalidate intent;
  current registration/inventory and receiving CLI checks still gate dispatch.
- Confirmation cancellation, operation-menu return and foreground return retain
  Details/Servers scroll plus existing ledger filter, identity and focus. Actual
  disposable PTYs exercise terminal restoration and resumed browser keyboard input
  after success, failure, signal and synthetic remote detach. Ctrl+D reaches the
  remote application; Ctrl+] detaches the existing remote bridge. Local foreground
  entry retains the application's normal exit/detach behavior; no new local
  transport or uniform local Ctrl+] contract is introduced.
- Services/About includes cache status, team inspect and explicit message
  continuation alongside existing services/recovery/admin/CLI runbooks. Existing
  exhaustive Cobra command inventories and operation-spec/consent tests remain
  enforced; no shortcut is a new authorization route.

### Stage 4 Reproduction

Run from an independent checkout of the exact published source head:

```sh
flox activate -- go test ./internal/tui/... ./internal/config ./internal/hostrouter ./internal/masterclient ./cmd/coop -count=1
flox activate -- go test ./internal/tui -run 'TestSetup|TestConfirmation|TestForeground|TestTerminal' -count=1 -v
flox activate -- go test ./cmd/coop -run 'TestTUI|TestFleetTUI|TestRemoteTerminal' -count=1 -v
flox activate -- go build ./...
flox activate -- go vet ./...
flox activate -- go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...
flox activate -- env NO_COLOR=1 TERM=dumb go test ./internal/tui/... ./cmd/coop -count=1
flox activate -- env CGO_ENABLED=1 CC=/nix/store/z2qkl46kv87zcs2sfwfjgc75nq0hbzhf-gcc-wrapper-15.3.0/bin/gcc go test -race ./internal/tui/... ./internal/hostrouter ./cmd/coop -count=1
git diff --check
```

Author checks on Linux arm64, Flox Go 1.26.10 passed: scoped tests above,
repository-wide build/vet, no-color tests, fitting race tests, formatting and diff
checks. The race lane uses the already-installed compiler above; default CGO and
PATH-only compiler attempts by delegated fixture checks were blocked. No compiler
or environment configuration was installed/changed. The standalone
`golangci-lint` binary is not on the Flox PATH; running the CI-pinned v2.12.2
through `go run` passed with zero issues after correcting one test-only switch
style finding. Setup/confirmation/foreground/terminal tests also passed ten
consecutive runs. CI remains an independent gate.
No new full-suite pass is claimed; previously documented root-run environment
failures remain outside this stage's scoped validation.

New three-size model coverage lives in `confirmation_test.go`, `setup_help_test.go`
and `setup_actions_test.go`. PTY coverage is in `foreground_test.go`,
`terminal_test.go`, `cmd/coop/remote_terminal_test.go` and existing hostrouter
fixtures. These fixtures use synthetic targets, not live guests. Wren must assess
the exact runtime artifact in `coop-tui-b-runtime-v1`, privately recording setup,
editor help, long review, stale/refusal/cancel and authorized attach/return journeys
at 120x40, 80x24 and 40x20. Real Blackbook/rack-mini operator-assisted evidence is
still pending, not inferred from these fixtures. Robin exact-head approval,
private Wren QA and required CI precede source delivery. No source merge, service
deployment/restart, production mutation, release, tap update or rollout is performed
by this author implementation stage.

## Stage 5 Integrated Handoff

[Task #121](https://github.com/sarcasticbird/coop/issues/121) starts at delivered
stage-4 main `7f14381`. Accepted PR #111 remains at
`5d79a84031045fe8dd8f3eeb345cadea953e0724`, verified on 2026-10-21; no design
delta is adopted. [The owner UAT packet](tui-b-uat.md) links the delivered stage
sources, acceptance matrix, reproducible build/evidence commands and explicitly
pending Blackbook/rack-mini operator journey. Historical results above are not
rewritten or treated as current-head independent clearance.

`runtime_integration_test.go` adds actual-key cross-workspace journeys at all three
sizes: a kept private reply survives explicit Fleet Connect callback return and
Servers/help without losing its captured request, parent, buffer or cursor; delayed
reader failure/recovery cannot hijack Fleet/Servers or reveal cached bodies.
An explicit light/dark/ANSI/Ascii renderer matrix compares bounded text semantics,
Unicode/control sanitization and privacy across pages. Real terminal contrast and
live guest behavior are not inferred from model rendering.

`scripts/uat-tui-b.sh` refuses a dirty or mismatched source head, builds twice,
compares the bytes and writes a private manifest with exact source, build metadata,
binary digest and synthetic validation transcript. It runs no live guest action.
Robin exact-head review, Wren private exact-artifact QA, required CI and actual
owner/operator UAT remain separate gates. Production rollout, service restart,
release and tap publication remain excluded.

### Stage 5 Acceptance-Evidence Corrections

Author reproduction on `12ec20d` confirmed that the integrated draft test's combined
valid text and prohibited control input was rejected as a whole, leaving its
later snapshot with only `X`. A positive assertion for the intended Unicode body
failed at all three sizes before correction. The test now inserts and verifies
the exact multiline wide/combining buffer and a nontrivial rune cursor, separately
asserts prohibited input leaves buffer/cursor unchanged, and checks the full
captured content/focus through Keep, Connect callback return, navigation and resume.
The production input-rejection contract is unchanged.

Both cross-workspace journeys now send actual resize events through 120x40 ->
40x20 -> 80x24 -> their starting size, including active/kept draft, pending reader,
foreground callback/return and fault/recovery states. Assertions cover captured
identity/request, editor buffer/cursor visibility, masked views, reader selection
and applicable scroll anchors. The packet now distinguishes independently
initialized size matrices, integrated resize transitions and component/PTY evidence.
These are test/evidence corrections, not demonstrated production draft loss or
disclosure. Renew exact-head engineering review and exact-artifact private QA;
earlier-head approvals do not clear the corrected candidate.
