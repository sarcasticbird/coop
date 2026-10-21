# Full-Page Correction Evidence

Task: https://github.com/sarcasticbird/coop/issues/133. Approved correction
revision: `26af78a7e97991ea31c7524d40e8b37b06ed5983df9199f815dbbe8ffe0c46c7`.
Environment: `coop-tui-b-runtime-v1`. Author evidence, not an independent review
or QA verdict. Owner visual UAT remains failed/pending until Chris accepts.

## Sources And Artifacts

Dependency #132 was verified delivered as PR #135, merge
`103a896a911f53a005126852f7c7f89a748b87e3`, before implementation. This checkout
started clean at that current main. Main remained at that revision on the final
read. Separate owner cache PR #131 remained OPEN at
`61c114b0f8a4e608a7c4917df34e21c43207f85b`; it was not copied, merged, reviewed,
or treated as a prerequisite.

Accepted B is the existing packet at PR #111 immutable head
`5d79a84031045fe8dd8f3eeb345cadea953e0724`, `docs/design/quiet-tui/`.
That directory is absent on current main; compare using `git show` at the
immutable revision rather than interpreting its absence as a new design choice.
This packet supplements, and does not replace, #132's historical BEFORE captures.

| Evidence | Production/build source | Binary SHA-256 | Coverage |
| --- | --- | --- | --- |
| `before/` | `103a896a911f53a005126852f7c7f89a748b87e3` | `194737ac9c1b3a87e0d1a2a51073395bdcfdbb09de00b3449a6a499eebaecbdc` | 114 checkpoints, 19 pages x 3 sizes x 2 modes |
| `before-extra/` | same baseline plus updated test-only launcher | `3c0e16530e7545ac6860534e8b498b2e65e54305ee5af2d9bf94cf428bed1eca` | 36 supplemental reply/receipt/setup/first-use/destroy checkpoints |
| `after/` | `db45d45e6fc951ae4a00469288e7004adb5bd9b7` | `379251e7ddd5af29ed2a8e35591358d468504f3de639eabaf4c5a8e6c6dcaf0c` | 132 checkpoints, 22 pages x 3 sizes x 2 modes |
| `after-extra/` | same AFTER source and executable | same AFTER digest | 36 supplemental checkpoints |
| `interactions/` | `749618aa434dedaf3d3b7b9e494d5444fe7c1cb5` | `951ef580ab7ef4f1727f09e3f18ec6b88125fa5e2068205fe392b9b669599fb0` | 156 checkpoints in 6 real PTY navigation/refresh/paging/resize sessions |
| `stress/` | `db45d45e6fc951ae4a00469288e7004adb5bd9b7` | AFTER digest | 156 checkpoints in 6 PTY sessions with duplicate long/wide/combining labels and 30 targets/messages |

Native shipping CLI built at `db45d45` with `CGO_ENABLED=0 go build -trimpath`:
`8906bc62f60829cc7f459bb4605d69bed15c9065315c0ad0e9c0a1213aa984de`.
It is a separate executable from the injected test launcher. The final PR head
adds this evidence and documentation after the build checkpoint. The immutable
native source artifact, not these historical binary paths, binds peer/QA gates.

## Capture Method

All captures are output from the running native Bubble Tea application on a real
Linux arm64 PTY, using the ordinary renderer, event loop and keyboard handlers.
No direct `View()` snapshots or static mockups are used for this evidence.
The opt-in `TestRuntimeCapture` launcher supplies synthetic in-memory services;
it is not part of the shipping CLI. It rejects dispatched lifecycle actions and
sends. Connect and destroy are cancelled; previews never submit. Receipts are
explicitly synthetic read-only responses, not a real delivery claim.

Each matrix uses `120x40`, `80x24`, `40x20`, `TERM=xterm-256color`, with and without
`NO_COLOR=1`. Raw `.ansi` is authoritative. `.txt` is a readable replay of the
cursor/erase/Unicode subset emitted by this runtime, not a full terminal emulator
or color-contrast verdict. Capture manifests identify source, executable, fixture
inputs, harness digests and paths. `before-extra` was built from an isolated
`git archive` of the unchanged baseline with only the updated fixture launcher.
The first baseline binary was built before any production edit.

Interaction manifests record real key bytes, ioctl/SIGWINCH dimension changes,
raw segment boundaries and clean native exits. Changed-size segments start at
the application's actual resize repaint; no synthetic ANSI is inserted. Stress
sessions verify the exact `fixture-message` reply target remains observable after
paging, refresh and resize while other messages exist. Bodies remain masked,
including transient raw output. Long Unicode labels are actual fixture input,
not drawn into a screenshot afterward. Quiet cached-refresh equality and failure
ordering additionally have deterministic model regressions; timed PTY reads alone
do not establish equality or prove hidden state.

Failed/intermediate harness trials are preserved privately under
`/tmp/opencode/hawk-133-*`, including the interrupted final matrix. Only completed
manifests and successful sessions are included here. No real fleet, credentials,
private conversation or private customer-QA findings are published.

## Frame Audit

Comparison against accepted B's 120x40, 80x24 and 40x20 frames:

| Page / B frames | Baseline discrepancy | Correction / retained behavior |
| --- | --- | --- |
| Details / D, M, B | Context discarded; presence missing; raw diagnostics precede useful evidence | Pinned route/master/principal; human name, host, VM lifecycle, activity/work, exact-bound Team presence/heartbeat and report/receipt age first. Full target/runtime/project/placement and diagnostic evidence remain scrollable. Presence is retained only from the successfully accepted exact-placement read, never inferred from a report or VM state; failures render unknown. |
| Team ledger / T-list | Four prose lines per agent; generated identity and worker metadata dominate | Aligned AGENT/HOST/PRESENCE/HANDLING/WORK columns wide; compact alias/host/presence rows narrow. Exact selection identity is unchanged and retained in drill-in. The list does not issue diagnostic reads or borrow reports by alias; unavailable list work stays unknown. |
| Team diagnostics / T, T-end | Handling below the compact viewport; technical ages first | Agent/host, actual presence/heartbeat, safe activity/work/blocker summary precede technical evidence. Route/master/principal stay pinned; exact agent/worker/generation remain available even without a report. Denied/stale/missing/skewed observations do not become current work. |
| Threads / L | ID-first rows, duplicate navigation, membership missing | Title-first rows, selected membership, bounded page cursor and masked-opening statement. No body previews; exact thread ID remains in the row and captured reader. |
| Reader / R, R-select, C-resume | Duplicate thread/actions; no selected-parent summary; compact Esc clipped | Explicit masked/revealed state, loaded/retained limit/follow, sequence and parent metadata; reply target summary and fixed offscreen-target feedback. Return and privacy controls fit 40 columns. Paging never reselects; newly opened readers remain masked. Kept draft routing is unchanged. |
| New thread / N, N-preview | Metadata buries fields; preview/send footer ambiguous | Editable fields precede technical evidence; preview distinguishes Create from Submit. Stable members, request, master, principal and epoch remain in complete review. Creation still sends no message or wake. |
| Root/reply / C-root, C, P | Long developer-style recipient label; editing/preview controls duplicated | Short stable-ID To/Body labels; blank-root note and captured parent-author behavior remain explicit. State-specific Preview/Submit footer; full captured identity and request remain reviewable. Input editing, rune cursor, masking, memory-only drafts and complete-preview gate remain intact. |
| Receipt / V | Actions repeated in body/footer | One contextual receipt footer; commitment remains distinct from acknowledgment, execution or wake. Master/epoch/message/backend/sequence and agent delivery states are retained. |
| Draft guard / K, U | Guard loses intent context | Adds captured master/principal/thread/request without body text. Existing Keep/Discard/Return and frozen identical retry semantics are unchanged. |
| Servers/setup / S-list, S-add, S-master, S-host | Generic workspace heading; missing master context; setup route buried | Explicit Servers page and master/principal context; existing SSH-reference editor gains context. Missing-master page exposes the read-only runbook early. SSH references remain separate from master identity, provisioning and trust. |
| Contextual help / Q | Details help generic | Captured exact target, destination/runtime and running/stopped restrictions precede general keys. Team, reader, composer, SSH and operation help retain their existing contextual safety guidance. |
| Confirmations / X, X-end, A, A-end | Connect called CONFIRM ENTER; no visible review-disabled state | Connect preflight names its purpose, captured VM requirement and Ctrl+C/EOF/detach/return behavior. Enter continues only through the existing complete-review gate; y remains supported. Incomplete confirmation visibly requests paging; destructive actions retain explicit y consent and exact validation. |
| Attach/return / A, B | Need preserve native entry and captured return | Existing native foreground/SSH entry and exact target/filter/scroll return are retained and regression-tested. No new cleanup/session promise, automatic reconnect, start-on-Connect or fallback target. Live SSH attachment was not performed. |
| All pages | Footer floats under short content; stale Fleet cancellation leaks into Coordination | Viewport fills to the two fixed terminal-bottom rows. Control/diagnostic outcomes share feedback without replacing contextual return keys. Workspace-specific feedback no longer carries Fleet cancellation into a composer. |

This is not a pixel-identical copy of the design fixture. Runtime IDs, actual
states and missing evidence replace its illustrative busy tasks. In particular,
the fixture intentionally has VM running while activity is waiting and work
observation time is absent. Raw technical evidence remains below the summary;
some long lines hard-wrap within the existing renderer. The Team list's unknown
handling/work is intentional without a safely loaded per-agent report. This
task adds no diagnostic API, name join, speculative scheduler state or new design.

## Reproduction And Checks

From the recorded source, using new output directories under an existing parent:

```sh
flox activate -- go test -c -o /tmp/opencode/coop-133.test ./internal/tui
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-133.test /tmp/opencode/coop-133-pages --extended --source db45d45
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-133.test /tmp/opencode/coop-133-extra --supplemental --source db45d45
flox activate -- python3 scripts/exercise-tui-parity.py /tmp/opencode/coop-133.test /tmp/opencode/coop-133-stress --source db45d45 --stress
```

Use the actual source revision used for the executable, not a later metadata-only
head. Baseline reproduction needs the fixture overlay and harness hashes recorded
by its manifest. Builds are not asserted byte-identical across paths/toolchains.

Author validation on Linux arm64 through Flox Go 1.26.10:

- `go test ./internal/tui/... ./internal/hostrouter ./internal/masterclient ./internal/mastermcp ./cmd/coop -count=1`: passed.
- Focused PageParity/TeamParity/confirmation/preview/frozen/quiet/resize tests, `-count=10`: passed.
- `go vet ./...`: passed.
- `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...`: passed, 0 issues.
- `CGO_ENABLED=1 CC=/nix/store/z2qkl46kv87zcs2sfwfjgc75nq0hbzhf-gcc-wrapper-15.3.0/bin/gcc go test -race ./internal/tui/... ./internal/hostrouter ./internal/masterclient ./internal/master -count=1`: passed; existing compiler, nothing installed.
- `NO_COLOR=1 TERM=dumb go test ./internal/tui/... ./cmd/coop -count=1`: passed.
- Native CLI and fixture test executable builds, gofmt and `git diff --check`: passed.
- Actual PTY extended/supplemental matrices and both ordinary/stress interaction matrices: page checks and clean exits passed.

No repository-wide test-suite-green, real Blackbook/rack-mini transport, live
send, independent visual acceptance or owner UAT pass is claimed. Existing
CLI/MCP/lifecycle packages and exact-target/long-confirmation/masking/draft/
identity/uncertain-retry regressions ran; no backend replacement was made.

## Handoff

Robin independently reviews the current published head. Wren independently
builds/exercises the exact immutable artifact and records runtime AND visual
findings privately in native `report_body` for `coop-tui-b-runtime-v1`. Required
CI and both current-head/artifact gates precede delivery. #134 receives this map,
the manifests and the exact built identities for integrated evidence and the
reproducible owner journey. Rook consolidates the actual gates for Chris/Hark.
This implement stage performs no self-review, QA clearance, source merge,
service restart/deployment, permissions expansion, release or Homebrew publication.

## Exact-Identity Review Fix

Robin's changes-needed review of `672cbfd8183da320fb0fdb833eb7d7ba66a852b5`
found that compact Team and Details headers discarded the only complete master
and principal IDs. An unbound Fleet target had no diagnostic-master fallback;
neither view retained the full principal in the scrollable body. This was a
presentation defect, not a reason to relax any identity or binding checks.

The new three-size regression reproduced the loss before the fix. Both detail
bodies now include labeled `Exact Master` and `Exact Principal` values while
preserving the three compact pinned context lines and work-first overview.
Tests cover two long IDs sharing the clipped prefix but differing in their
suffix, Team detail and unbound Fleet Details, complete content preservation,
scroll reachability, bounded headers and terminal geometry. They pass ten runs.
The production fix adds two rendering lines; no routing, report binding,
credential, request, privacy or action authority changes are made.

Build checkpoint: `f551e45a63869d86b9494c9a7306a2823ba6b0e8`.
Fixture binary `/tmp/opencode/hawk-136-context.test` SHA-256:
`76c59b62463567b84b30dff98f02e1939cb85b09d997a35d8489aa3512103dde`.
Native CLI `/tmp/opencode/hawk-136-context-coop` SHA-256:
`56f0553c7c6dc2693b9731695ef6e84aad7056692f22262cb4f55b246cab5ab1`.
The later evidence commit changes only capture navigation/documentation and
adds generated evidence; the fixture executable uses this checkpoint's source.

`exact-context-fix/` contains 54 actual PTY checkpoints at all three sizes,
color and NO_COLOR, including six real PgDn keys to reach the full identities
in each affected view. The fixture uses long synthetic master/principal values
and deliberately leaves Fleet diagnostic bindings absent. The runner verifies
both exact IDs in the scrolled native render, not just the model's content.
The compact scrolled captures visibly retain distinct full `second` suffixes
behind the clipped header. Raw ANSI and the manifest preserve provenance.

The ordinary 132-page extended PTY matrix was rebuilt and rerun successfully
with the same binary at `/tmp/opencode/hawk-136-context-pages/manifest.json`;
the earlier committed matrices remain historical and are not relabeled as new
artifact evidence. One initial exact-context harness run used j keys, which
Details intentionally does not use for scrolling; it failed its visibility
assertion and is preserved at `/tmp/opencode/hawk-136-exact-context`. The
verified runner sends actual PgDn keys. No application change was needed for
that harness error.

Reproduction with this checkpoint's launcher and the updated capture runner:

```sh
flox activate -- go test -c -o /tmp/opencode/coop-136-context.test ./internal/tui
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-136-context.test /tmp/opencode/coop-136-context --exact-context --source f551e45
```

Flox scoped TUI/hostrouter/masterclient/mastermcp/CLI tests, repository vet,
golangci-lint v2.12.2 (0 issues), NO_COLOR TUI/CLI, uncached fitting race
TUI/hostrouter/masterclient/master using the existing compiler, native builds,
Go formatting and source/script/document whitespace checks passed again.
Raw terminal captures contain normal CR/padding; no unfiltered whitespace-clean
claim is made for them. No full-suite, live transport, independent QA or owner
UAT pass is claimed. Renew Robin's changed-head review, Wren's exact-artifact
runtime/visual QA in `coop-tui-b-runtime-v1` and hosted checks before delivery.

## Preview Privacy And Consent Context

The creation preview now preserves explicit Reveal/Mask semantics for all
creation input fields and the canonical owner-included member set. A masked
preview displays field sizes/member count, never the title or input values.
Ctrl+S only enters preview; it does not implicitly reveal or submit. Ctrl+V
explicitly reveals, and another Ctrl+V masks the rendered preview again.
The full captured request and complete-review submission gate remain intact.

Connect and lifecycle confirmations capture master/principal display context
when opened, independently of lifecycle routing authority. Four compact lines
remain pinned during review: operation, master, principal and captured target.
Full exact master/principal/target values remain in the scrollable body, which
explicitly states that the master context is not the lifecycle destination.
The renderer, PgDn step and contiguous-review accounting share the same pinned
header geometry. Resize still clears review progress; jumping to the end still
cannot count skipped body lines. Confirmation/cancellation and receiving-side
exact-target validation are unchanged.

Rendered regressions first reproduced automatic creation disclosure and missing
confirmation context. They now cover masked/revealed/remasked creation values,
Connect and destroy at all three sizes, frozen captured context after later
display-context changes, paging/header geometry and cancellation. Existing
long-confirmation/skipped-page/resize-reset and real creation-submit tests pass;
the latter explicitly reveals before checking cleartext creation values.
The focused privacy/confirmation/creation matrix passed ten repetitions.

Build checkpoint: `cf102005e4e57742f0510f91bd54ebc0d1ddc86a`.
Fixture `/tmp/opencode/hawk-136-consent.test` SHA-256:
`97e3d0650a9710cb73e2a95ca09aa73b78bb48246de5d7af8cb60dd695e00cd8`.
Shipping CLI `/tmp/opencode/hawk-136-consent-coop` SHA-256:
`0861aa40ef2a4f5a10b1a89b8d97130ce84edbfebe1f7cc50c81d4a183f97ab7`.
The later evidence commit adds only documentation/generated synthetic captures.
Historical matrices above retain their original identities; they are not
relabeled as the new build.

`consent-pages/manifest.json` records 150 actual native Bubble Tea PTY
checkpoints at 120x40, 80x24 and 40x20 in color and NO_COLOR. In addition to the
existing page journey, it captures the masked creation preview, explicit Reveal,
Mask, Connect top and reviewed end. The runner rejects any synthetic title in
cumulative raw output before explicit Reveal, and checks revealed/remasked screen
state. It also checks pinned master/principal/target at both ends of Connect.
`consent-extra/manifest.json` records 42 supplemental checkpoints including
destroy top/end with the same pinned-context checks. Neither confirmation is
consented; no creation or message is submitted. Synthetic input is deliberately
revealed only by the dedicated Reveal checkpoint; it is not customer content.

The same rebuilt executable passed the 54-checkpoint exact-context matrix at
`/tmp/opencode/hawk-136-consent-context/manifest.json` and six actual PTY stress
sessions/156 checkpoints at `/tmp/opencode/hawk-136-consent-stress/manifest.json`.
These preserve long/Unicode/combining labels, multiple message candidates,
masking, selection, paging, refresh and ioctl/SIGWINCH resize evidence. No private
fleet/conversation captures or private customer-QA findings are published.

Reproduce using new output directories and the actual build-source revision:

```sh
flox activate -- go test -c -o /tmp/opencode/coop-136-consent.test ./internal/tui
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-136-consent.test /tmp/opencode/coop-136-consent-pages --extended --source cf102005
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-136-consent.test /tmp/opencode/coop-136-consent-extra --supplemental --source cf102005
```

Flox scoped TUI/hostrouter/masterclient/mastermcp/CLI tests, repository vet,
golangci-lint v2.12.2 (0 issues), NO_COLOR TUI/CLI, uncached fitting race
TUI/hostrouter/masterclient/master using the existing compiler, native builds,
Go formatting and source/script/document whitespace checks passed again.
No whole-suite, live fleet/transport, independent QA or owner-UAT pass is
claimed. Main remained `103a896a911f53a005126852f7c7f89a748b87e3`; separate cache
PR #131 remained OPEN/unchanged and was not included. The new head/artifact must
receive Robin's renewed independent review, Wren's separate private runtime AND
visual QA in `coop-tui-b-runtime-v1`, and required CI before source delivery.
Owner UAT remains failed/pending; no deployment, restart or release is performed.
