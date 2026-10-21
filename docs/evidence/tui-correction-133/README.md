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
