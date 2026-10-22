# Responsive Fleet/Details

Task #134 / PR #137, approved correction revision
`26af78a7e97991ea31c7524d40e8b37b06ed5983df9199f815dbbe8ffe0c46c7`.
Environment: `coop-tui-b-runtime-v1`. Author evidence, not independent QA or owner
acceptance. Owner UAT remains FAILED/PENDING.

## Working Reference

The settled working default supersedes the older full-page-only B treatment:
Fleet stays LEFT at roughly 40%, selected Details stays RIGHT at roughly 60%
at widths of at least 100 columns. Below 100 columns use full-width Fleet and
Details. Wren leads UX; this implements the settled preview rather than another
developer design. B's non-conflicting identity, privacy and action-safety
requirements remain. Private owner screenshots/state are not published here.

Both panes have thin frames. A persistent `>` means selected; `[focus]` identifies
the pane receiving keyboard input, including NO_COLOR. Theme-aware accents and
bold section labels are supplementary, never the sole status/focus signal.
The right overview puts REPORTED WORK before OBSERVATION and TARGET. Deep raw
diagnostics remain in the existing Team route. The list shows human bird labels,
reported activity, work/reason and report/receipt ages under explicit host groups.
Failure, stale cache, successful empty hosts and absent evidence remain distinct.

## Interaction And Safety

- Up/Down or j/k in Fleet selects and previews only. Preview rendering performs no reads or actions; it uses the existing exact-placement ledger cache and its access/freshness/context guards. No automatic target at startup.
- Enter or Tab opens/focuses captured Details. Enter remains navigation at every width, including after a resize. Explicit `c` opens the existing Connect preflight; reviewed Enter still confirms that preflight. This intentionally replaces the older Details-Enter-to-Connect shortcut. Lifecycle/menu actions retain their separate existing confirmation and dispatch guards.
- Tab/Shift-Tab or Esc returns to Fleet with the same identity and retained list position. Details remembers its own scroll across focus changes. Focused Details arrows/page keys scroll rather than changing the selected target. Resize across 99/100 preserves focus/identity; no implicit Connect.
- The left viewport uses its actual pane width/height for selection and paging. Right scroll never overwrites left scroll. Refresh and foreground return retain state; the renderer uses read-only projections rather than driving backend state from a shallow copy.
- A removed/changed target stays unavailable in the preview, with fresh explicit reselection required. It never substitutes a neighboring target. Full target and clipped exact master/principal remain inspectable. Unknown/stale/denied work is never inferred from VM lifecycle.
- Filters, help, forms, runbooks, operation previews, draft guards and exact-target confirmations stay full-width. Confirmation review accounting and resize resets use the original full terminal geometry, not hidden pane dimensions.

Terminals shorter than ten rows use the single-page fallback even at wide widths,
avoiding a frame with no usable body. Normal accepted geometries are unchanged.

## Actual Built Comparison

Production BEFORE: `7ab40595b40677dd3db24288ee6b100ba4aaf001`.
The baseline executable was built/captured before production renderer edits;
test-only Connect-key migration was already present in the test executable but
does not change its native production event-loop behavior. Ordinary fixture
data/launcher remain unchanged. `split-before/manifest.json` records 42 actual
Fleet/selected-Fleet/Details/messaging/Team checkpoints, all three sizes with
color and NO_COLOR. Its historical harness used the original page expectations;
do not run the updated split assertions against an old binary and call it a new
BEFORE capture.

AFTER build checkpoint: `3ec4e338ccb8751ee8c8ac8b0913f73552baff73`.
Final publication adds documentation/generated sanitized evidence only.

| Executable | SHA-256 |
| --- | --- |
| BEFORE native fixture | `b0d9d787789a185d4e1fa542b42c294a299b18dbb4362b3b3372b1d0b6c4f7ad` |
| AFTER native fixture | `7381faeb7838833d47b51f98bd82f3d6b6b10e4919210d4f439833203fa50dda` |
| AFTER shipping CLI, two byte-identical builds | `df7813d3d55d8dea7c65bb3196c1d2ad8a35e5703d7b91025984498897ba946f` |

Linux arm64, Flox Go1.26.10. Shipping CLI build uses CGO disabled, trimpath and
buildvcs=true with the checkpoint revision and `vcs.modified=false`. The injected
fixture is a distinct executable, not a live CLI or an installed-host identity.

`split-after/manifest.json`: 150 actual PTY checkpoints, extended pages and safe
cancelled confirmations. `split-work/manifest.json`: six explicitly supplemental
synthetic reported-task/PR/dependency viewports, not invented live work.
`split-context/manifest.json`: 54 long-ID/navigation checkpoints.
`split-stress/manifest.json`: 156 checkpoints in six real selection/paging/refresh/
ioctl+SIGWINCH resize sessions with duplicate wide/combining labels and messages.
All assertions and clean exits passed at 120x40, 80x24 and 40x20, color/NO_COLOR.

The actual 120x40 selected-Fleet screen shows BOTH list and summary with Fleet
focus; the following Details screen keeps that same list/selection and moves only
focus. At 80x24 and 40x20 the same selected fixture opens full-width Details and
returns to Fleet. Work sections, source/age, VM/presence and exact target remain
readable above a fixed footer, with scrolling for long content. Missing task data
stays unknown; separate `split-work` shows the reported synthetic task/PR/blocker.

Raw `.ansi` is native Bubble Tea PTY output, not a static mockup or View-only test.
Readable `.txt` replays the established cursor/erase/Unicode subset, not a complete
graphical terminal or real light/dark contrast assessment. Manifests record actual
source/binary/fixture/harness digests, geometry, navigation and paths. Wide exact-ID
assertions use a display-cell slice of the actual right pane, never concatenating
Fleet text into identity fragments or substituting a full-page render. Captures
dispatch no lifecycle action, Connect or message submission.

## Validation And Reproduction

Fresh final-source validation passed on Linux arm64 through Flox Go1.26.10:

- Full scoped `uat-tui-b.sh 3ec4e338ccb8751ee8c8ac8b0913f73552baff73 https://github.com/sarcasticbird/coop/issues/134` runner: repeat-build equality, formatting/source whitespace, TUI/config/hostrouter/masterclient/coordination/master/mastermcp/CLI regressions, integration/foreground/terminal x10, command inventories/remote-terminal fixtures, NO_COLOR, repository build/vet and fitting uncached race using the existing compiler.
- Focused split/breakpoint/ledger/identity/freshness/confirmation/page/integration/foreground/terminal selection, ten repetitions; explicit 99/100/101 geometry, preview read purity/access guard, removed-target refusal, independent scroll and resize/Enter safety.
- CI-pinned golangci-lint v2.12.2: zero issues. Both capture scripts parse; source/script/doc whitespace checks are separate from raw terminal padding/CR.
- All actual BEFORE/AFTER matrices above. No full repository test-suite, fresh live-host, graphical-theme or independent QA success is asserted.

From an independent clean checkout of the published exact head, record that SHA,
build the fixture and use new output directories under an existing parent:

```sh
flox activate -- go test -c -o /tmp/opencode/coop-split.test ./internal/tui
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-split.test /tmp/opencode/coop-split-pages --extended --source EXACT_SHA
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-split.test /tmp/opencode/coop-split-work --details-only --reported-work --source EXACT_SHA
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-split.test /tmp/opencode/coop-split-context --exact-context --source EXACT_SHA
flox activate -- python3 scripts/exercise-tui-parity.py /tmp/opencode/coop-split.test /tmp/opencode/coop-split-stress --stress --source EXACT_SHA
```

Main remains `96ca391`, with separate #131/#138 already integrated. This is a
normal fast-forward author publication; no published history is rewritten.
Old compact/full-page captures and approvals remain historical, not current split
clearance. Robin must independently review this changed exact head; Wren must
independently build/identify and assess actual three-size screens against the
settled working reference in `coop-tui-b-runtime-v1`; required current CI must pass.
Candidate-bound BOTH-host observations and Chris's final visual UAT remain open
and must not be relabeled from older binaries. Rook coordinates these actual
new-head gates. No self-review, author QA verdict, merge/delivery, production
restart/deployment, host/configuration/credential effect, release or Homebrew
publication is performed by this author fix.
