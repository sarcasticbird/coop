# Corrected B Runtime Handoff

Task: https://github.com/sarcasticbird/coop/issues/134. Approved correction revision:
`26af78a7e97991ea31c7524d40e8b37b06ed5983df9199f815dbbe8ffe0c46c7`.
Environment: `coop-tui-b-runtime-v1`. This is author integration evidence, not
independent QA. **Owner visual UAT remains FAILED/PENDING until Chris accepts.**

Current local repair evidence is in [Presentation Repairs](#presentation-repairs).
The publication candidate uses the [Post-Rebase Candidate](#post-rebase-candidate).
The original build identities and discrepancy map below are historical records,
not assertions that the repaired runtime still has every listed defect.

## Provenance

Both prerequisites were delivered before this integration: #132 / PR #135 as
`103a896a911f53a005126852f7c7f89a748b87e3`, and #133 / PR #136 as
`711d46fb070b8589752ee55c6e19022f5c60c867`. Integration starts on the latter main.
The build checkpoint is `503cd5929fcecd446eb118e78d604ec4cbf44235`:
the only change from merged main is optional task attribution in the validation
runner. Application and fixture sources are unchanged. Final evidence commits
do not retroactively change the identity of these builds.

Accepted B remains [PR #111's immutable packet](https://github.com/sarcasticbird/coop/tree/5d79a84031045fe8dd8f3eeb345cadea953e0724/docs/design/quiet-tui).
It is not present on main. No new design is substituted for it.
Separate cache PR #131 was verified OPEN at
`61c114b0f8a4e608a7c4917df34e21c43207f85b`, not merged, copied or made a
dependency. Its independent repair is not part of this packet.

## Historical Before Evidence

Preserve the original evidence and its identities; do not regenerate a BEFORE
from corrected source. [#132's manifest and comparison](../tui-correction-132/README.md)
identify the pre-correction main `ece7f185ebbb50e5bc893f724ee738161184e000`,
fixture digest and 36 actual PTY checkpoints for Fleet, Details, Team and messaging
at all three geometries, color and NO_COLOR.
[#133's evidence](../tui-correction-133/README.md) adds the unchanged
`103a896a911f53a005126852f7c7f89a748b87e3` baseline for remaining pages,
114 ordinary and 36 supplemental checkpoints, with separate binary identities.
Its review-fix evidence is historical AFTER evidence, not a new BEFORE.

The fresh `after`, `supplemental`, `exact-context` and `stress` directories here
are generated from the build checkpoint above. Each manifest records binary,
fixture and harness digests, geometry, color mode, real key navigation and paths.
Raw `.ansi` is authoritative terminal output; `.txt` is readable replay of the
runtime's cursor/erase/Unicode subset, not a complete terminal emulator or a
color-contrast verdict. All data is sanitized synthetic input. Fixture binaries
run the native Bubble Tea renderer and event loop but are not the shipping CLI.
No live config, credentials, fleet data or private QA report is published.

| Build | Source | SHA-256 |
| --- | --- | --- |
| PTY fixture executable | `503cd5929fcecd446eb118e78d604ec4cbf44235` | `5078978920f757321c3461a3c8743d2750d79fbde2873131371ba9264308a589` |
| Shipping CLI, two byte-identical builds | `c345aa8dae1716083de2610910a6026167e12523` | `3437deeb566505c7b26b6ab316a3e7f031e579af90e7bd5c57ce8106e099d3e8` |

The second source adds this handoff document only. CLI build metadata records
`vcs.modified=false`, Linux arm64, Go 1.26.10, CGO disabled and trimpath enabled.
The fixture and shipping CLI are different artifacts, not interchangeable QA
identities. Final published source must receive its own independent build/QA.

## Author Results

Executed 2026-10-21 through Flox, Linux arm64, Go 1.26.10:

- `uat-tui-b.sh c345aa8dae1716083de2610910a6026167e12523 https://github.com/sarcasticbird/coop/issues/134`: passed. Includes repeat-build byte comparison, Go formatting, scoped TUI/config/hostrouter/masterclient/coordination/master/mastermcp/CLI regression tests, integrated/foreground/terminal tests ten times, CLI command inventories and remote-terminal fixtures, NO_COLOR TUI/CLI, repository build/vet and fitting uncached race checks. Race uses the existing compiler `/nix/store/z2qkl46kv87zcs2sfwfjgc75nq0hbzhf-gcc-wrapper-15.3.0/bin/gcc`; no installation or configuration change.
- `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...`: passed, zero issues.
- Actual native PTY matrices: `after` 150 checkpoints, `supplemental` 42, `exact-context` 54, `stress` 156 in six real navigation/refresh/resize sessions. All destination/privacy/context assertions and clean exits passed, at three sizes with color and NO_COLOR.
- Author inspection of terminal replays confirmed the wide aligned Fleet table, medium Details work-first summary, compact masked reader and compact Team detail against accepted B. Remaining compact hard-wraps and footer abbreviation are visible, not concealed; semantic replay does not establish real light/dark color contrast. Wren's independent exact-artifact visual assessment remains required.

The initial validation invocation correctly refused a shortened/nonmatching SHA;
the full-SHA clean-checkout run above passed. Initial capture attempts exceeded
the two-minute command limit; their partial output was preserved separately and
excluded. Complete reruns used ten-minute limits. No full repository test-suite,
live Blackbook/rack-mini, operator binding, live send/attach, independent QA or
owner-UAT success is claimed. Historical root-mode/image-PATH/seed/worker snapshot
environment failures remain documented in `docs/tui-b-runtime.md`; no full-suite
rerun or new baseline comparison is claimed here.

## Historical Discrepancy Map

Compare both sides with F0/F/H/D/T/L/R/N/C/P/V/S/Q/X/A/B in the accepted packet.
The detailed historical audits remain linked above; this map consolidates them.
This map describes the pre-repair `7ae524d` artifact. The resolution map below
records the subsequent source changes; original captures are preserved unchanged.

| Surface | Correction and actual evidence | Remaining difference or boundary |
| --- | --- | --- |
| Fleet | Aligned NAME/KIND/VM/ACTIVITY/WORK / BLOCKER wide; combined work medium; selected activity compact; human labels, explicit host groups and no automatic selection. `after/*fleet*` | Host health uses more vertical space than B's single-line heading. Fixture work is unknown, not illustrative busy work. |
| Details | Work-first lifecycle/activity/presence/freshness summary, pinned context, scrollable full target and exact master/principal. `after/*details*`, `exact-context/` | Raw diagnostic evidence remains below the summary; long technical lines may hard-wrap. No evidence is hidden to mimic a mockup. |
| Team | Alias-first aligned ledger and work-first detail, separate heartbeat/report/work ages. `after/*team*` | List handling/work stays unknown without a safely loaded per-agent report; alias is never a binding key. |
| Threads/reader | Title-first list, selected membership, masked-open reader, stable parent summary and contextual bottom controls. `after/`, `stress/` | Runtime identifiers and real availability replace illustrative packet prose. Capture replay is not a color-contrast assessment. |
| Forms/preview/receipts | State-specific controls, memory-only draft guard, explicit Reveal/Mask including creation preview, captured identity and canonical membership. `after/`, `supplemental/` | Receipt is synthetic read-only data, not actual send/ack/execution. No capture submits a message or creates a thread. |
| Setup/Servers/help | Distinct master/SSH first use, context-first runbook and contextual help. `after/`, `supplemental/` | No credentials editor, master switching, provisioning or trust bypass is added. |
| Confirmations | Pinned operation/master/principal/target, full scrollable exact identity/argv/consequence and complete-review consent gate. `after/`, `supplemental/`, exact-target regressions | Captures cancel Connect/destroy. They establish neither live attach nor permission for a destructive operation. |
| Attach/return/navigation | Actual PTY paging, keys, refresh and ioctl/SIGWINCH resize in `stress/`; disposable foreground/terminal tests in validation | Live Blackbook/rack-mini attach/return is operator-assisted and remains unverified here. |
| Quiet refresh | Existing deterministic cache/fault/ordering/selection regressions plus real PTY refresh | Fixed capture delays alone do not prove no-flicker or cached-content equality. |

The shared runtime uses compact route/master/principal headers rather than B's
literal global numbered navigation bar, separator lines and FOCUS captions.
Workspace keys and contextual help remain available; this is a visible residual
presentation difference for independent QA and Chris, not a claim of pixel parity.

### Explicit Residuals

The broad rows above did not enumerate all visible differences. That omission
is corrected here using the already committed sanitized native captures, not
private QA captures or report content. These are **remaining discrepancies**, not
resolved behavior, accepted design changes or a claim that the source now matches
every B frame. The following six groups must remain in the owner handoff:

| Group | Reproduce in this public packet | Actual difference and disposition |
| --- | --- | --- |
| Shared hierarchy and density | `after/120x40-fleet.txt`, `after/80x24-team.txt` | Global numbered navigation, FOCUS captions and separators from B are absent from the compact route headers; host health takes a separate line. Medium Team uses stacked AGENT / HOST, presence and unknown-work rows rather than B's ledger treatment. Keys still work, but literal hierarchy/density parity is not established. Unchanged; owner-visible. |
| Compact wrapping and controls | `after/40x20-details.txt`, `after/40x20-team-detail.txt`, `after/40x20-reader.txt` | Work/blocker, age/source and reply-target explanations hard-wrap inside words; compact feedback/footer strings can end with an ellipsis. This is not merely different fixture text. Full scrollable evidence and contextual help remain, but readability and key discoverability differ from B. Unchanged; owner-visible. |
| Creation preview labels and repetition | `after/120x40-create-preview.txt`, `after/40x20-create-preview.txt` | Preview retains editable-field markers and the long Stable member IDs label. It repeats blank Engagement and Captured members metadata after the masked input summary and includes editing instructions plus generic second-Ctrl+S submission prose. The control footer does distinguish create, but generic feedback still says submits and calls Esc draft rather than B's preview/edit wording. Explicit masking and owner-included membership are preserved; copy/density differences remain unresolved. |
| Confirmation consent copy | `after/40x20-connect-reviewed.txt`, `supplemental/40x20-destroy-reviewed.txt` | Connect body includes generic y-confirms/n-cancels prose as well as Enter-after-review guidance; its footer uses Enter. Destroy retains y consent. Both accepted keys are functional, but Connect's mixed vocabulary differs from B's dedicated Enter continuation copy. Complete-review and exact-target gates are retained; copy remains unchanged. |
| Draft guard feedback | `after/40x20-draft-guard.txt` | Keep/Discard/Return body and contextual footer are present, but the fixed feedback row still carries preview review-input/PgDn guidance underneath the guard. Thus one contextual control footer does not mean every feedback line is contextual. Memory-only protection is retained; leftover preview feedback is an unresolved presentation discrepancy. |
| Details Help context | `after/40x20-details-help.txt` | Help identifies full captured target, destination and runtime, but does not show labeled master/principal context as B's Q frame does. Details and confirmation identity preservation does not establish Help identity parity. Action authority remains unchanged; this missing Help context remains unresolved. |

This amendment changes documentation only. It does not repair or waive these
presentation differences, replace the accepted packet, or turn functional fixture
assertions into visual clearance. Historical binaries/captures keep their original
source/digest attribution; no new runtime capture or product change is claimed.

Amendment validation on 2026-10-21: inspected the named public terminal replays,
confirmed the diff is this document only, and passed `git diff --check` and
`flox activate -- go test ./internal/tui/... -count=1` on Linux arm64. No new
build/vet/race/full-suite run or new runtime/visual clearance is claimed by this
documentation fix. The published amended head still requires renewed independent
Robin review, Wren's matching-artifact environment assessment and required CI.

## Binding And Operator Limit

Fleet consumes diagnostics only through the existing host-owned exact pin:
master, agent, coordination host, worker instance and generation. Self, Team,
epoch, access, freshness, ambiguity and late-result checks remain required.
Communication IDs, host aliases and running VM state do not establish this link.
Missing, stale, denied or incompatible evidence must remain unknown/stale.

This checkout has no authorized live operator fixture/configuration or live
mutation permission. Host-owned setup and observation must be verified by the
operator; author synthetic success cannot establish installed fleet binding.
Rook owns coordinating this prerequisite with the operator and reporting the
exact limit privately. Do not invent pins, read worker secrets, upgrade/restart
a service or deploy this build as a shortcut. Public evidence stays synthetic.

The required environment has two lanes: native synthetic fixtures **and**
operator-assisted Blackbook/rack-mini acceptance. A fixture-only pass cannot
clear the combined requirement. The outstanding input is an operator-assisted
session or artifact-bound observation packet identifying the exact candidate
source, built/installed binary, actual host-owned placement binding and current
access/freshness, with explicitly authorized disposable attach/return and
messaging/retry journeys. No such live access or journey permission was supplied
to this author checkout; this is an unavailable prerequisite, not a denial
observed by an attempted live probe. Rook owns obtaining and verifying that input
with the operator and arranging Wren's independent recheck. No replacement task
or guessed dependency is introduced to manufacture clearance.

## Reproduction

Use an independent clean checkout of the published exact PR source artifact.
Record its full head, platform and toolchain. Use a fresh output parent and
replace `EXACT_SHA` with that actual full source; do not label a new executable
with this packet's historical build checkpoint.

```sh
git rev-parse HEAD
flox activate -- go version
flox activate -- bash scripts/uat-tui-b.sh EXACT_SHA https://github.com/sarcasticbird/coop/issues/134
flox activate -- go test -c -o /tmp/opencode/coop-134.test ./internal/tui
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-134.test /tmp/opencode/coop-134-after --extended --source EXACT_SHA
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-134.test /tmp/opencode/coop-134-extra --supplemental --source EXACT_SHA
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/coop-134.test /tmp/opencode/coop-134-context --exact-context --source EXACT_SHA
flox activate -- python3 scripts/exercise-tui-parity.py /tmp/opencode/coop-134.test /tmp/opencode/coop-134-stress --stress --source EXACT_SHA
```

Set `CC` to an already available compiler through `flox activate -- env CC=...`
if required for race checks. Do not install or change the environment to mask
a failure. The validation runner writes a private manifest/transcript, builds
the CLI twice and compares bytes. It never launches against live configuration.
For a visible synthetic launch, set `COOP_TUI_CAPTURE=1` and run the built test
binary with `-test.run '^TestRuntimeCapture$' -test.count=1` in a terminal.
Use 1 for Fleet, 2 for Coordination, t for Team, arrows/Enter/Esc to navigate,
PgDn to review, Ctrl+V only for deliberate synthetic reveal, Ctrl+C to quit.
The fixture refuses real lifecycle/send dispatch; do not substitute a live CLI
without the operator's explicit approved journey and exact disposable target.

## Independent Gates And Owner Checkpoint

The authoritative task/PR/head/source artifact comes from native publication,
not a guessed self-referential hash in this file. Robin must independently review
that exact current head. Wren must independently build/identify and exercise the
same candidate artifact, recording runtime AND visual findings privately through
native `qa.report_body` in `coop-tui-b-runtime-v1`. Required hosted CI must pass.
Prior #132/#133 clearances do not clear #134. Author captures are not a QA verdict.

Rook consolidates the actual PR/head/artifact, current Robin review, Wren matching
artifact/environment report reference, hosted check URLs, this capture map and
remaining operator limits in the originating private engagement. Those new gates
are pending publication and independent jobs, not prefilled as passed here.
Chris then owns visual UAT: inspect all three sizes, readable no-color output,
human-readable host/table hierarchy, Details/Team work truth and messaging
masking/context. Any live attach, draft/send or lifecycle journey requires its
separate authorized disposable fixture and current exact-target confirmation.
Record pass/fail and remaining differences, not merely whether the process ran.

Source delivery is not owner acceptance. No production deployment, service
restart, release, Homebrew publication, new permissions or scheduler work is
authorized or performed by this integration.

## Presentation Repairs

The retained #134 author fix now includes local source repairs, not another
documentation-only successor. The six observations were reproduced against the
published source/public captures before edits. New presentation regressions failed
on missing creation privacy labels and missing Help identities; reading-height
and medium-Team regressions separately failed before their fixes. The source
checkpoint for all final runtime evidence is
`b6e4fc8760b11246ed40be63b3a7b169507eb057`. Later evidence/documentation commits
do not change the build attribution or count as independent clearance.

| Finding | Bounded repair | Actual native evidence |
| --- | --- | --- |
| Compact hierarchy, wrapping and controls | Explicit Host labels distinguish Fleet groups; medium Team is a cell-aligned AGENT/HOST/PRESENCE/WORK ledger. Reading pages wrap at word boundaries while long exact tokens can still split without losing characters. Spaces in paths/argv remain intact. Reader anchor heights use the same wrapper; editor cursor geometry retains its existing hard wrapping. Scroll hints append only when they fit, leaving the complete contextual control row readable. | `presentation-pages/120x40-fleet.txt`, `80x24-team.txt`, `40x20-details.txt`, `40x20-team-detail.txt`, `40x20-reader.txt`; `presentation-stress/` exercises real keys, paging, refresh and resize. |
| Creation privacy labels | Explicit Content: masked/revealed in the page; Ctrl+V action alternates reveal/mask in the footer. First preview never reveals automatically. Failed/incompatible authority still renders masked. | `presentation-pages/*create-preview.txt`, `*create-revealed.txt`, `*create-remasked.txt`; raw output sentinel checks reject disclosure before explicit reveal. |
| Creation preview context and edit/create copy | Review shows title, engagement and canonical owner-included members once, plus captured request/exact identity/epoch. No blank captured-thread fields, root-parent prose, field-selection markers or editing instructions in creation preview. Creation-specific create feedback; Esc returns an unfrozen preview to Edit, and the next Esc from nonempty input opens the existing draft guard. Frozen uncertainty still goes directly to draft protection and identical retry. | `presentation-pages/40x20-create-preview.txt`, `40x20-create-revealed.txt`; real capture navigation uses two Esc keys to edit then open guard, without submission. |
| Connect consent vocabulary | Connect body and footer both advertise Enter after full review and Esc cancel. Destructive confirmation keeps y. Accepted key behavior, contiguous-review accounting and exact-target revalidation are unchanged. | `presentation-pages/40x20-connect-reviewed.txt`, `presentation-extra/40x20-destroy-reviewed.txt`; no consent is issued. |
| Draft guard feedback | Guard owns its fixed feedback row as well as its contextual control footer. Ordinary words wrap intact; leftover preview-review instructions no longer appear beneath Keep/Discard/Return. | `presentation-pages/40x20-draft-guard.txt`; Keep/Discard/Return/quit and frozen-retry suites remain covered. |
| Details Help identity | Labeled Master and Principal precede exact target/destination/runtime. They are read-only display context, not lifecycle authority. Long values remain scrollable instead of being clipped out of Help. | `presentation-pages/40x20-details-help.txt`; model checks at all three sizes. |

Author inspection of these final built terminal replays against immutable B
confirmed the named word splits, preview blank/repeated fields, mixed consent
copy, stale guard feedback and missing Help labels are repaired. The runtime is
still not a pixel-identical design mockup: global route headers remain compact
rather than B's literal numbered navigation/FOCUS/separator treatment; host
health retains a separate line; unbroken long identities necessarily split;
busy/error feedback may abbreviate long diagnostic prose. Medium Team omits the
separate handling column to fit, retaining truthful unknown work. These residual
differences and real light/dark contrast remain for independent QA/owner judgment,
not silently waived. Editor wrapping is intentionally unchanged for cursor safety.

### Repair Checkpoint

Linux arm64, Flox Go 1.26.10; source checkpoint above:

| Artifact | SHA-256 | Method |
| --- | --- | --- |
| Native fixture test executable | `00736c1944672ca43ce4202961c2f4a42801e920644d25e88b1088940ae798b5` | `go test -c ./internal/tui`, synthetic services in native Bubble Tea |
| Shipping CLI, two byte-identical builds | `777ca2fb4bf02a9d46677cb5c776efdfc0c935cdc5b48b18715e221970bbdb4d` | CGO disabled, trimpath, buildvcs=true, embedded checkpoint and vcs.modified=false |

Final `presentation-pages/manifest.json` records 150 checkpoints;
`presentation-extra/manifest.json` 42; `presentation-context/manifest.json` 54;
`presentation-stress/manifest.json` 156 across six sessions. All passed assertions
and clean exits at 120x40, 80x24 and 40x20, color and NO_COLOR. Each records the
same source and executable digest, fixture-source/harness digests, geometry and
artifact paths. Raw `.ansi` and readable `.txt` retain the established limitations.
No private fleet/conversation or private QA content is included.

The exact-context harness now sends actual PgDn keys until both full distinct
master/principal suffixes are visible together, with a bounded 64-key ceiling;
the manifest records the real number of keys used at each checkpoint. A fixed
six-key baseline under-scrolled the new compact layout, while twelve keys
overscrolled wide Team. Those intermediate/partial matrices are retained
separately and excluded from final evidence. Final captures all use the final
source and updated bounded navigation, not an assertion weakened to accept clipped
identities. Earlier successful intermediate builds are not relabeled current.

Reproduce the final checkpoint using the existing commands above and new output
directories. Use the updated capture script from the selected source; `--extended`
now exercises preview -> Esc Edit -> Esc draft guard and asserts explicit privacy
state/action labels, guard feedback, Help context and Connect vocabulary. The
fixture still rejects lifecycle/send dispatch; no capture submits or consents.

### Validation And Gates

Final-source author checks passed through Flox:

- `uat-tui-b.sh b6e4fc8760b11246ed40be63b3a7b169507eb057 https://github.com/sarcasticbird/coop/issues/134`, with the existing compiler path documented above: repeat-build equality, formatting, scoped TUI/config/hostrouter/masterclient/coordination/master/mastermcp/CLI tests, integration/foreground/terminal ten times, command inventories/remote-terminal fixtures, NO_COLOR TUI/CLI, repository build/vet and fitting uncached race checks.
- `go test ./internal/tui -run 'TestPresentation|TestReadingWrap|TestConversationLineHeight|TestTeamMedium|TestConfirmation|TestCreationPreview|TestRuntimeIntegration|TestCompose' -count=10`.
- `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...`: zero issues.
- All four final PTY matrices above; source/script/doc whitespace checks separately from raw terminal CR/padding.

No new full-repository test-suite, live binding/transport/send/attach, independent
QA, graphical theme-contrast or owner acceptance success is claimed. Prior
environment limits remain documented, not waived. Main was
`711d46fb070b8589752ee55c6e19022f5c60c867` at the start of this repair. The final
read found main advanced to `96ca3911640b5715a6539ae73070d3d871e3b524`:
cache PR #131 merged at that revision (source head
`7454d5e9aad38b72506087347a73785681e40918`) after credential-renewal PR #138
(`efbea35`). Neither changes TUI source. These separately delivered changes are
reconciled by base integration, not copied into this task as new cache work.
The capture/build checkpoint above remains its actual pre-base-refresh source.

Publication must renew Robin's independent review, Wren's exact-artifact runtime
AND visual assessment in `coop-tui-b-runtime-v1`, and required hosted CI. Old-head
approvals and fixture QA do not clear this source. The both-host operator-assisted
lane remains a final acceptance gate; it did not prevent these local repairs.
Rook coordinates the corrected candidate and independent jobs; the operator owns
preparing assistance once that candidate is ready. Owner UAT stays FAILED/PENDING.
This author fix performs no self-review, QA verdict, merge, deployment/restart,
release, Homebrew or host/configuration/credential changes.

### Integrated Candidate

After the separately delivered base changes landed, this branch merged current
main `96ca3911640b5715a6539ae73070d3d871e3b524` without conflicts at
`2cc746084dc8b66739b5d5e19cc42ce672c6ba7a`. The merge adds #131/#138 only;
`internal/tui`, the fixture and capture harnesses are byte-unchanged from the
repair checkpoint above. No unrelated cache implementation is authored by #134.

Both executables were rebuilt on that clean integrated source, and every runtime
matrix was repeated rather than relabeling the pre-base-refresh executable:

| Integrated build | SHA-256 |
| --- | --- |
| Native fixture executable | `8859948b18cbd246b1625094ad049f6dfbee5242a5a1d3b29be9a5467c8c4566` |
| Shipping CLI, two byte-identical builds | `9cd3410993a6a00d4a8bfd14c84646e84a3c93c9bc08fdcd391269b282096163` |

`integrated-pages/manifest.json` (150), `integrated-extra/manifest.json` (42),
`integrated-context/manifest.json` (54) and `integrated-stress/manifest.json` (156,
six sessions) all passed at all three geometries, color and NO_COLOR, using this
same integrated source/binary. The six-group resolution evidence paths above have
equivalent fresh screens under these `integrated-*` directories. These are the
latest author runtime evidence, and the earlier `presentation-*` matrices retain
their distinct true identities. No capture submits or consents.

On this integrated checkpoint the full scoped validation runner passed again,
including byte-identical repeat CLI builds, formatting, regression/CLI/MCP/PTY
fixtures, ten-repeat integrated/foreground/terminal tests, NO_COLOR, repository
build/vet and fitting uncached race checks with the existing compiler. Pinned
golangci-lint v2.12.2 again reports zero issues. Source/script/doc whitespace
checks pass; raw terminal CR/padding is not treated as source whitespace. No
fresh full-repository test-suite or live-fleet result is implied.

Final publication adds only this documentation and generated sanitized evidence
after the integrated build checkpoint. Robin and Wren must assess the published
exact head/source artifact afresh; this author's build/capture packet does not
replace their gates. Rook receives the actual updated PR/head for renewed
independent review, matching-artifact runtime/visual QA and hosted CI. The owner
prepares the separate both-host operator assistance after this corrected candidate
is ready. No deployment, installation, service restart, live action or source
merge is performed by the author job; the base merge above is local integration,
not delivery of PR #137. Owner UAT remains FAILED/PENDING.

### Post-Rebase Candidate

The assigned branch-refresh execution freshly fetched and verified exact main
`96ca3911640b5715a6539ae73070d3d871e3b524` and the published PR branch head
`7ae524d33244ec6a5b6b30f3479926c777bde580`. The pre-rebase clean checkpoint
`1d9f3a19391425a76cc28d2ab6833edb7538a968` is preserved locally. An integrated
base `6821a9c3ff1f551fd8a18c0c3b0bc7135a0581a4` contains both published head
and exact main. Unpublished repair/evidence commits were rebased onto that base;
the already-integrated #131/#138 changes were dropped as redundant during replay.
Published commits are not rewritten. Both required ancestor checks pass, so the
result can publish with a normal fast-forward, without force-push or a lease
bypass. No source conflict occurred.

Post-rebase build checkpoint: `58255eae188887fb8e8e04f33c98df60f59ef888`.
Its full tree is byte-identical to the preserved pre-rebase checkpoint, verified
with `git diff --exit-code`. This does not relabel old tests/captures as current:
the rebased source was freshly built, tested and captured again on Linux arm64
through Flox Go 1.26.10.

| Newly Built Post-Rebase Artifact | SHA-256 |
| --- | --- |
| Native fixture executable | `8859948b18cbd246b1625094ad049f6dfbee5242a5a1d3b29be9a5467c8c4566` |
| Shipping CLI, two byte-identical builds | `93bf3d537dc068ce6205f72bdafa8f4c6e46eccfc94c353a8a54c3c377d6f559` |

The freshly built fixture matches the prior fixture bytes because its build
inputs are unchanged; it does not embed a claim of VCS provenance. The shipping
CLI records the post-rebase revision with `vcs.modified=false`. Final publication
adds this documentation and generated evidence only after the build checkpoint.

Latest actual native terminal evidence: `rebased-pages/manifest.json` (150),
`rebased-extra/manifest.json` (42), `rebased-context/manifest.json` (54) and
`rebased-stress/manifest.json` (156, six real keyboard/scroll/refresh/resize
sessions). Every matrix passed assertions and clean exits at 120x40, 80x24 and
40x20 with color and NO_COLOR. The six-group resolution examples above have
fresh equivalents in these directories. Full identity suffixes, explicit creation
mask/reveal/remask, contextual guard feedback and Help/confirmation labels are
verified through the real event loop. These are sanitized fixtures; raw ANSI
and replay limitations remain unchanged. Earlier matrices and source/binary
identities are historical, not rewritten or substituted for these runs.

The full scoped validation runner passed again on the clean post-rebase source:
repeat-build equality, formatting, scoped regressions/CLI/MCP/terminal fixtures,
integrated/foreground/terminal tests ten times, NO_COLOR, repository build/vet
and fitting uncached race checks with the existing compiler. Pinned lint v2.12.2
reported zero issues. The focused presentation/wrapping/Team/privacy/confirmation/
compose/integration matrix also passed ten times after rebase. Source/script/doc
whitespace checks passed separately from raw terminal padding/CR. No new full-suite,
graphical theme, live-host or independent QA success is asserted.

This candidate still requires Robin's renewed exact-head review, Wren's separate
matching-artifact runtime AND visual assessment in `coop-tui-b-runtime-v1`, and
required current hosted CI. Both-host operator assistance and Chris's UAT remain
final gates. No source delivery, host effect, deployment/restart or release is
performed by this author branch refresh.
