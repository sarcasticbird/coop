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

Stage 0 does not implement B. In particular, the current Fleet selects a target,
the reader is not masked, Escape clears forms, and integrated placement-aware
diagnostics/full-page flows remain future work. These are not stage-0 passes.

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
