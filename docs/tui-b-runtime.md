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
adds the Team diagnostics flow described below. The reader is not masked and
Escape clears conversation forms; conversation/draft protection remains stage 3.

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
