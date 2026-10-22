# Compact Details Correction

Task #134 / PR #137, same approved correction revision
`26af78a7e97991ea31c7524d40e8b37b06ed5983df9199f815dbbe8ffe0c46c7`.
Environment: `coop-tui-b-runtime-v1`. This is author implementation evidence,
not independent review, QA or owner visual acceptance. Owner UAT is FAILED/PENDING.

## Correction

The prior Details page still duplicated header identity, target/runtime and
host/worker context, then appended the entire Team diagnostic dump. A clean
baseline executable was built and captured before changing that production
renderer. New three-size regressions reproduced the dump and redundant short
identity labels before the repair. No private screenshot or fleet capture is
published here.

The new Details overview groups bird/host, VM lifecycle/reported activity,
task/PR, blocker, heartbeat and distinct report/receipt/work/wait ages. At 120x40
and 80x24, fitting pairs align in two columns without clipping their values;
40x20 stacks the same labels. The first compact viewport includes the normal
fixture's exact target alongside the summary. Unknown work remains unknown;
VM running is never promoted to activity or a task.

Full exact target identity stays in the scrollable body. Master and principal
appear once in the pinned header when they fit; only clipped identities repeat
in full below. Existing long/shared-prefix identity, scroll, resize, confirmation
and exact-target dispatch tests remain intact. Runtime is shown separately only
when it differs from the human label; a real project path is retained without
an empty placeholder. Worker placement IDs, transport counters, models, retry
internals and transition histories remain in the existing `2`, then `t`, Team
diagnostics route, not in Details. Team rendering and all backend/binding/action
validation code are unchanged.

Reported task/PR/stage require a current compatible report and a fresh work
observation. Denied, stale, future, missing-wait and missing-work cases render
unknown task/PR, rather than presenting retained fields as current. Blockers use
reported wait evidence; multiple dependencies show the first and an extra count,
with full diagnostics still available in Team. Fixed stale/denied/control feedback
and existing cached-refresh behavior are retained.

## Actual Terminal Evidence

Accepted reference: immutable B packet
`5d79a84031045fe8dd8f3eeb345cadea953e0724`, `120x40.md` section D and its smaller
frames. Compare the native screens, not just test assertions:

| Matrix | Source / fixture | Coverage |
| --- | --- | --- |
| `details-before/` | Unchanged production `3e12e8d0b1e21d71a694a31613ae0aec3e3590be`; original launcher | Six Details viewports, three sizes, color and NO_COLOR |
| `details-after/` | `aa96a9ede673e07926b5475fdb6f6ac4cc8628fd`; same ordinary data | Six paired corrected Details viewports |
| `details-work/` | Same AFTER build, explicit synthetic reported-work option | Six supplemental viewports showing task, PR and dependency; not a paired BEFORE fixture |
| `details-context/` | Same AFTER build, existing long-ID/unbound fixture | 54 checkpoints including reachable full master/principal suffixes |
| `details-stress/` | Same AFTER build, existing duplicate Unicode/combining fixture | 156 checkpoints in six real navigation/refresh/paging/resize sessions |

All matrices passed assertions and clean native exits. Each manifest records
source, executable/fixture/harness digests, geometry, method and artifact paths.
Real keyboard input drives the native Bubble Tea renderer/event loop on Linux
arm64 PTYs at 120x40, 80x24 and 40x20, with color and NO_COLOR. Raw `.ansi` is
authoritative; `.txt` replays the known cursor/erase/Unicode subset. It is not
a full graphical terminal or light/dark contrast verdict. Captures dispatch no
actions or messages. AFTER's opt-in test launcher adds synthetic work only when
explicitly requested; the shipping CLI has no such injection.

| Executable | Source | SHA-256 |
| --- | --- | --- |
| BEFORE fixture | `3e12e8d0b1e21d71a694a31613ae0aec3e3590be` | `8859948b18cbd246b1625094ad049f6dfbee5242a5a1d3b29be9a5467c8c4566` |
| AFTER fixture | `aa96a9ede673e07926b5475fdb6f6ac4cc8628fd` | `4b10784f85be00f22beb632c4891a1dbb4ff6f36ef0a9487fcc6bac3e75646fe` |
| AFTER shipping CLI, two byte-identical builds | Same AFTER source | `8c7099fcd5e0c3a3d7c5dc34ed50e053f7c47ccb9e232739ed6818beb15c90a3` |

The shipping build records the exact revision and `vcs.modified=false`, Go
1.26.10 Linux arm64, CGO disabled, trimpath. Final evidence commits do not
retroactively relabel those executables. The earlier `35de931` intermediate
build/captures are preserved separately, not counted as final artifact evidence.

Author inspection confirms the ordinary BEFORE wide screen still reaches raw
counter diagnostics, while AFTER is a 14-line grouped overview before bottom
padding. Medium AFTER likewise fits without scrolling; compact shows work-first
stacked labels and exact target, with PgDn for remaining context. Short master/
principal values no longer repeat. Full long IDs still intentionally split over
physical lines rather than disappear. This is a bounded correction against B,
not a claim of literal parity or Chris's acceptance.

## Reproduction And Checks

Use a clean independent checkout of the published full source SHA. Record that
actual build revision instead of copying the historical checkpoint above:

```sh
flox activate -- go test -c -o /tmp/opencode/coop-details.test ./internal/tui
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-details.test /tmp/opencode/coop-details --details-only --source EXACT_SHA
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-details.test /tmp/opencode/coop-details-work --details-only --reported-work --source EXACT_SHA
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-details.test /tmp/opencode/coop-details-context --exact-context --source EXACT_SHA
flox activate -- python3 scripts/exercise-tui-parity.py /tmp/opencode/coop-details.test /tmp/opencode/coop-details-stress --stress --source EXACT_SHA
```

Output directories must be new under an existing parent. BEFORE was captured
with the new Details-only navigation harness against the unchanged baseline
executable; its production/fixture sources were not retrofitted after the repair.

Final-source author checks through Flox Go1.26.10 on Linux arm64 passed:

- `uat-tui-b.sh aa96a9ede673e07926b5475fdb6f6ac4cc8628fd https://github.com/sarcasticbird/coop/issues/134`, using the existing compiler: repeat-build equality, formatting, scoped TUI/config/hostrouter/masterclient/coordination/master/mastermcp/CLI regressions, integrated/foreground/terminal x10, CLI inventories/remote-terminal fixtures, NO_COLOR, repository build/vet and fitting uncached race.
- Focused Details compactness/freshness, full identity, binding/late-result/fault/control-feedback tests, ten repetitions.
- CI-pinned golangci-lint v2.12.2: zero issues; source/script/doc whitespace checks separately from terminal CR/padding.
- All five actual native capture matrices above.

No new full-suite, real-host, graphical-theme, live attach/send or independent
QA success is claimed. Main remains `96ca391`; separately delivered cache #131
and credential #138 are already integrated, not new implementation here.

The changed published head needs renewed Robin independent review, Wren's
matching-artifact runtime AND visual assessment against accepted B and owner
feedback, and current CI. Older operator and QA results do not clear the new
artifact. The remote sentinel evidence correction remains separate operator-owned
work, not a claimed product transport defect. Rook coordinates the new-head gates
and actual operator handoff; Chris retains owner visual UAT. No self-review, QA
verdict, source delivery/merge, deployment/restart, host/configuration/credential
effect, release or Homebrew action is performed by this author fix.
