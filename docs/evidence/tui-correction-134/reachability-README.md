# Split Context Reachability

Task #134 / PR #137, same approved correction revision and
`coop-tui-b-runtime-v1`. Author fix evidence, not independent review/QA or owner
acceptance. Owner UAT remains FAILED/PENDING.

## Finding And Repair

Robin's current-head review of `ef1f80275af53a63369b7467b2762a580743b9d3`
identified a genuine geometry defect. Keyboard bounds measured clipped header
rows from one content path, while the split renderer wrapped the original long
headers before pinning two physical rows. A long Master consumed both pinned
rows; Principal was not pinned. The rendered body was also longer than the
keyboard projection, making its last identity/content unreachable at short wide
sizes even though direct scroll-offset tests and the original capture assertions
passed.

The new regression failed before the repair at 100x24, 120x24, 120x20 and 120x40,
with both PgDn and Down. It now uses actual keyboard events, never assigns
`m.scroll`, and verifies one Master row plus one Principal row at every step,
unchanged pinned context, full distinct identity suffixes and the final Team link
reachable in the body, exact keyboard end clamp, unchanged target and no action.

Rendering and focused keyboard bounds now share `detailsPaneLines`, one physical
projection. It clips each of the two context rows before wrapping only the body;
the existing full long identities stay in that scrollable body. Both View and
navigation use identical header/body line counts. No focus, layout, selection,
access/binding/dispatch or full-width confirmation behavior changes are made.

## Actual Built Evidence

Build checkpoint: `5eb50a0caf7347ac2e6a405ee042b100f4738de2`, Linux arm64,
Flox Go1.26.10. Final publication adds documentation/generated synthetic captures
only after this checkpoint.

| Executable | SHA-256 |
| --- | --- |
| Newly built native fixture | `e115e95b134ef019fea36c32b1040db333ace1a97617190ff8b1daa5346b6764` |
| Shipping CLI, two byte-identical builds | `32681c5aedf2054a5affe40cec74b0b90d0d2d1c9bbec418f8d6103844250dbc` |

CLI metadata records the exact build revision and `vcs.modified=false`, trimpath,
CGO disabled. The injected test fixture is a separate executable and not an
installed-host identity or live CLI.

- `reachability-short/manifest.json`: 80 actual native PTY checkpoints at 100x24, 120x24, 120x20 and 120x40, color and NO_COLOR. New assertions check the two pinned labels before and during real paging; full IDs are reconstructed from the actual right pane. A keyboard-end checkpoint separately requires the final Team link.
- `reachability-context/manifest.json`: 54 standard long-ID/navigation checkpoints at 120x40, 80x24 and 40x20, both color modes.
- `reachability-pages/manifest.json`: 150 extended native page/privacy/confirmation checkpoints at the three standard geometries and color modes.
- `reachability-stress/manifest.json`: 156 checkpoints in six real keyboard/refresh/paging/ioctl+SIGWINCH resize sessions with duplicate wide/combining labels and messages.

All assertions and clean exits passed. Representative final screens are
`reachability-short/100x24-details-keyboard-end.txt` and
`reachability-short/120x20-no-color-details-keyboard-end.txt`: distinct clipped
Master/Principal stay at the top while both full IDs and the last body line are
visible after actual PgDn. No capture dispatches, consents, attaches or sends.

Historical BEFORE is preserved, not recreated after the fix: the earlier
`split-context/120x40-details-exact-context.txt` and its raw ANSI show the original
fragmented header at their recorded source/build. The strengthened PTY harness
also failed against the retained old split executable at 100x24 before the final
AFTER runs. Its partial negative-control run is preserved separately, excluded
from passing evidence. No older executable is relabeled as this checkpoint.

Raw ANSI is authoritative native Bubble Tea PTY output; text replay implements
the known cursor/erase/Unicode subset, not a full graphical terminal or theme
contrast verdict. Manifests identify exact source/binary/fixture/harness digests,
geometry, navigation and paths. Public input is sanitized synthetic data only.

## Checks And Reproduction

Fresh final-source scoped validation runner passed: repeat CLI build equality,
formatting/source whitespace, TUI/config/hostrouter/masterclient/coordination/
master/mastermcp/CLI regressions, integration/foreground/terminal x10, inventories
and remote-terminal fixtures, NO_COLOR, repository build/vet and fitting uncached
race checks using the existing compiler. Focused split/ledger/diagnostic/
confirmation/exact-context/integration/foreground/terminal selection passed ten
repetitions. CI-pinned golangci-lint v2.12.2 reports zero issues.

From an independent clean published-source checkout, build the fixture and use a
new output directory under an existing parent. Use its actual full SHA:

```sh
flox activate -- go test -c -o /tmp/opencode/coop-context.test ./internal/tui
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-context.test /tmp/opencode/coop-context-short --exact-context --wide-short --source EXACT_SHA
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-context.test /tmp/opencode/coop-context-standard --exact-context --source EXACT_SHA
flox activate -- go test ./internal/tui -run '^TestSplitDetailsKeyboardReachesExactContext$' -count=10
```

No full-suite, live-host, graphical-theme, independent QA or owner pass is
claimed. Main remains `96ca391` with #131/#138 already integrated. Published
ancestry is preserved. This new head needs Robin's renewed independent review,
Wren's separate matching-built runtime/visual assessment in
`coop-tui-b-runtime-v1`, current CI, candidate-bound BOTH-host evidence and Chris's
visual UAT. No old clearance carries forward automatically. Rook owns that actual
new-head handoff. No author self-review/QA verdict, merge/delivery, production
restart/deployment, host/configuration/credential effect, release or Homebrew
publication is performed by this fix.
