# Corrected B Runtime Handoff

Task: https://github.com/sarcasticbird/coop/issues/134. Approved correction revision:
`26af78a7e97991ea31c7524d40e8b37b06ed5983df9199f815dbbe8ffe0c46c7`.
Environment: `coop-tui-b-runtime-v1`. This is author integration evidence, not
independent QA. **Owner visual UAT remains FAILED/PENDING until Chris accepts.**

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

## Discrepancy Map

Compare both sides with F0/F/H/D/T/L/R/N/C/P/V/S/Q/X/A/B in the accepted packet.
The detailed historical audits remain linked above; this map consolidates them.

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
