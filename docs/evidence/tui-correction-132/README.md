# Fleet Correction Evidence

Task: https://github.com/sarcasticbird/coop/issues/132. Approved correction
revision: `26af78a7e97991ea31c7524d40e8b37b06ed5983df9199f815dbbe8ffe0c46c7`.
Environment: `coop-tui-b-runtime-v1`. Owner UAT remains failed/pending.

## Capture Method

The `before/` and `after/` directories contain **actual native Bubble Tea PTY
output**, not static design frames or direct `View()` snapshots. The opt-in
`TestRuntimeCapture` launcher injects only synthetic in-memory inventory and
master services into the normal `fleetModel` event loop. The application receives
terminal dimensions and real arrow, Enter, Escape and workspace keys on its PTY.
It navigates Fleet -> Details -> Threads -> masked reader -> Team -> diagnostic
detail. Neither transport nor user configuration, credentials, private fleet
data, service startup or mutation is used. The launcher is absent from the
shipping CLI binary; it is compiled into the fixture test executable only.

Each `.ansi` file is cumulative **raw terminal output up to that navigation
checkpoint**, retaining the preceding pages and terminal capability negotiation.
Each `.txt` is the resulting screen replay, preserving terminal cell alignment
but intentionally omitting color. The standard-library replay supports Bubble
Tea's cursor/erase commands; raw captures remain authoritative. All fixture
content is sanitized. Each matrix includes 120x40, 80x24 and 40x20, with and
without `NO_COLOR=1`, using `TERM=xterm-256color` on a Linux arm64 PTY.
These captures demonstrate the built renderer and navigation, not an independent
visual approval or real Blackbook/rack-mini transport/owner-UAT pass.

The manifest records the production source revision used for each executable,
fixture identity, binary SHA-256, capture method and paths. BEFORE was compiled
before production edits at unchanged main `ece7f185ebbb50e5bc893f724ee738161184e000`,
adding only the fixture launcher. Failed harness trials are preserved privately
under `/tmp/opencode/hawk-132-before*`; only the verified navigable run belongs
in this packet. AFTER uses the checkpointed implementation revision recorded
in its manifest. The final PR revision adds evidence/documentation only after
that implementation checkpoint; the PR's immutable artifact is the source
authority for independent review and QA.

## Reproduction

From the selected production source, add the identical test-only launcher from
this correction if reproducing the older baseline. Use the capture script from
this correction. Build and run through Flox:

```sh
flox activate -- go version
flox activate -- go test -c -o /tmp/opencode/coop-tui-capture.test ./internal/tui
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-tui-capture.test /tmp/opencode/coop-tui-captures --source FULL_PRODUCTION_SOURCE_SHA
```

The output directory must not already exist. Capture reads are timed (0.8 seconds
per input/paint boundary); each process has its own fresh fixture observation
times. The runner answers standard cursor/background queries and verifies the
expected destination page, refuses panic/early exit, and quits cleanly with
Ctrl+C. Source and binary digests, not timestamps embedded in screens, identify
the artifact. Test builds are not claimed byte-identical across paths/toolchains.

The published packet contains 36 checkpoints per side (six pages, three sizes,
two color modes), each with raw and replay output. The reproduction runner also
adds an explicit `fleet-selected` checkpoint immediately before Details so the
compact activity row can be inspected separately; the existing cumulative raw
Details captures already contain that selection paint.

## Author Validation

Linux arm64, Flox-selected Go `go1.26.10`, 2026-10-21. Actual commands/results:

| Check | Result |
| --- | --- |
| `flox activate -- go test ./internal/tui/... ./internal/config ./internal/hostrouter ./internal/masterclient ./internal/mastermcp ./cmd/coop -count=1` | Passed all packages |
| `flox activate -- go vet ./...` | Passed |
| `flox activate -- go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...` | Passed, 0 issues |
| `flox activate -- env CGO_ENABLED=1 CC=/nix/store/z2qkl46kv87zcs2sfwfjgc75nq0hbzhf-gcc-wrapper-15.3.0/bin/gcc go test -race ./internal/tui/... ./internal/hostrouter ./internal/masterclient ./internal/master -count=1` | Passed all packages; existing compiler, no packages installed |
| `flox activate -- env NO_COLOR=1 TERM=dumb go test ./internal/tui/... ./cmd/coop -count=1` | Passed |
| `flox activate -- env CGO_ENABLED=0 go build -trimpath -o /tmp/opencode/hawk-132-coop ./cmd/coop` | Passed; native shipping CLI build, not the injected capture launcher |
| `flox activate -- go test -c -o /tmp/opencode/hawk-132-after.test ./internal/tui` | Passed; exact fixture executable for AFTER |
| `flox activate -- python3 scripts/capture-tui.py /tmp/opencode/hawk-132-before.test docs/evidence/tui-correction-132/before --source ece7f185ebbb50e5bc893f724ee738161184e000` | Passed page verification and clean exits |
| `flox activate -- python3 scripts/capture-tui.py /tmp/opencode/hawk-132-after.test docs/evidence/tui-correction-132/after --source 33e7d3f` | Passed page verification and clean exits |

BEFORE fixture executable SHA-256:
`e7dfe1e668286a7d92032952682ab344ff7c64c364b4159f73b5cc27ce0e8c12`.
AFTER fixture executable SHA-256:
`a8c9cbd0afaa20e2465ca6182ec9a4c57e98228f3c588ff809eb788dfabdd558`.
Shipping CLI build at the implementation checkpoint SHA-256:
`d056c46353c39c6b57ca2a1dd77947c9199f38034fc008eddf9899967fa218af`.
The CLI was built with its normal VCS build metadata. It is not the fixture
capture executable and its digest is not an assertion of final-PR-head QA.

Focused regressions cover terminal-cell alignment, Unicode/wide/combining/long
labels and control removal, generated runtime labels, duplicate-name drill-in,
inventory startup diagnostics without Details, single-batch quiet refresh,
exact placement/master/epoch, denied/unsupported/missing data, future/old/missing
timestamps, stale work versus fresh activity, principal/context changes,
out-of-order results, registration/placement/ambiguity changes and late results.
Existing navigation, paging, resize, masking/drafts, confirmation, CLI/MCP and
lifecycle suites remain intact; assertions that required repeated full IDs in
Ledger now check physical highlighted-row visibility and exact drill-in identity.

No repository-wide test-suite-green claim, live fleet/SSH claim, independent QA
clearance or color-contrast acceptance is made. No deployment, restart, release,
Homebrew publication or source delivery was performed by this implement stage.

## Design Comparison

Compared to accepted B at immutable PR #111 head
`5d79a84031045fe8dd8f3eeb345cadea953e0724`, especially F0/F/D/T/L/R:

- BEFORE has repeated `host / kind / ID` row prose, duplicate Fleet headings and
  zero-hidden counts, no aligned table or list activity even with valid pins.
- AFTER uses aligned NAME, KIND, VM, ACTIVITY and WORK / BLOCKER columns at
  120x40, combined activity/work at 80x24, and bounded NAME/KIND/VM rows with
  selected activity at 40x20. Human profile/project labels replace generated
  runtime dominance; exact identities remain in Details and confirmations.
- Host groups remain clear; successful empty and unreachable-without-data hosts
  remain distinct. Host health stays separately readable for long SSH references.
  This is slightly more vertical than B's wide single-line host headings.
- Fresh, compatible diagnostic activity is visible in Fleet before drill-in;
  the fixture intentionally pairs VM running with **waiting**, not working.
  Work time/task absent in this fixture stays **work unknown**. There is no
  fabricated task or live placement pin to make the screenshot look busier.
- Details leads with lifecycle and a concise activity/work summary; exact
  routing identity and raw diagnostic evidence remain scrollable. Redundant
  inline action help and repeated scroll hints are removed.
- Remaining discrepancies are explicitly handed to dependent task #133:
  Team still leads with technical diagnostic age/epoch prose before work;
  Details still has a verbose raw evidence section and no compact presence
  summary; messaging repeats context/actions in the body and footer, and its
  compact footer truncates. The captured reader stays masked. Setup/help need
  the complete frame audit in #133; this task does not claim their visual parity.
- Integrated color/contrast, full remaining-page capture matrix and reproducible
  owner journey belong to #134 after #133. Model text tests and this author's
  comparison do not clear Robin's independent review, Wren's exact-artifact
  runtime/visual QA or Chris's owner UAT.

## Binding Investigation

Local profile `Diagnostics` pins are exported by `hostrouter.LocalClient`; SSH
inventory preserves the pin while replacing only transport `Target.Host` with
the selected exact registration. The pin is master/agent/coordination-host/
worker-instance/generation. Runtime/profile/SSH aliases are not join keys.
The configured master client remains pinned; no alternate credential discovery.

Before correction, the existing safe path was gated on opening Details.
Fleet's list did not consume reports at all. The correction reuses that exact
validator for a bounded inventory-sized ledger cache and one serial batch:
authenticated Self, one Team snapshot, then report reads for compatible pins.
Every response is still authorized by the existing master endpoints. Report
placement, restore epoch, receipt/report/wait time and work time remain distinct.
Current reader identity/placement/capabilities/engagement context is part of the
cache context; a newer observed incompatible coordination context suppresses it.
There is no separate access-epoch field in the existing protocol, so no such
field is invented or asserted.

Inventory removal, registration address changes, changed pins and ambiguity
discard cache entries and invalidate in-flight serials. Denial/unavailable/old
responses cannot render cached work as current; restore changes reset ordering.
The ledger never borrows Details' retained stale report as current evidence and
never selects, wakes or authorizes lifecycle actions. No actual private operator
configuration was available in this checkout; missing live pins remain unknown,
not repaired by name guessing.

Main was re-read at `ece7f185ebbb50e5bc893f724ee738161184e000`; separate owner
cache PR #131 remained OPEN at `61c114b0f8a4e608a7c4917df34e21c43207f85b`
during author verification. It was not copied, reviewed, merged or used as a
dependency. Later main/cache delivery must be reconciled normally.
