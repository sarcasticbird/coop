# Selected B: Review And Future Work

## Decision And Boundary

B, Fleet Ledger With Full-Page Drill-In, is the owner-selected direction. It
supersedes the initial A recommendation because the operator needs a whole-fleet
view of multiple hosts and their birds. Do not reopen A/B or automatic target
selection. This packet now defaults to All, groups by host, and uses Fleet ->
Details -> Connect at every width. Host filtering is optional; no master switch
or cross-master routing is implied. This is design revision authority only.

Production implementation waits for approval of this revised packet after
independent exact-head engineering review and customer QA. Publication is not
merge/delivery permission. Do not invoke delivery under this goal. Release and
Homebrew also remain held for explicit operator UAT and release authorization.

## Acceptance And Artifact Map

| Requirement | v3 artifact coverage | Gate/limit |
| --- | --- | --- |
| Two directions, before evidence and recommendation | directions.md; README private provenance and selected-B record | A retained as alternative, B settled; original brief/image stay private |
| Whole-fleet visibility and explicit target | Every size: F0 All/no target, F selected, H optional filter, D full-page Details | No automatic selection, implicit Start or direct Connect from ledger |
| Host/agent identity, reachability, empty/stale state | F/F-offline/F-reconnect; compact F-filter/E/M; interaction state table | Unknown is not idle/stopped; spare host has 0 targets, lab is offline not empty |
| Team and actual diagnostics | Every size: T-list/T; compact T-end | Reports are observations, not job admission or recovery authority |
| Threads/reader/compose/reply | Every size: L/R/R-select/N/C-root/C/P | Masking/draft protection are proposed, not current runtime guarantees |
| Servers/setup at 120x40, 80x24 and 40x20 | S-master and S-host literal frames at all sizes; S-list/S for wide, S/S-add compact | Context, focus, separate master/SSH setup and safe routes shown; validator requires both first-use states |
| Help, confirmations and attach/return | Every size: Q/X/A/B; compact X-end/A-end | Full captured target/consequence review; existing CLI/PTY/authority checks retained |
| Quiet routine reads and visible exceptional work | Every size: G; interaction.md #112 contract | Initial/navigation/write/failure/blocked-control feedback explicit; no runtime no-flicker proof |
| Complete command/safety parity | source.md links unchanged exhaustive current CLI inventory | Ledger shortcuts drill into Details/action choice; never bypass consent or dispatch directly |
| Independent engineering and operator journeys | Renewed exact-head native Robin review and Wren QA `coop-tui-design-v1` | Pending; not performed or asserted by author |
| Implementation proposal and ownership | Stages below | Proposal only; no executable tasks created |

The published commit is the artifact identity; never insert a guessed SHA into
its own contents. Findings and their resolution evidence remain in the approved
private thread. This coverage map describes sanitized artifacts, not quotations
of private reports. QA private detail belongs only in native `qa.report_body`.
Relevant artifact changes renew review/QA; no previous-head result carries over.

## Independent Walkthroughs

These are assessment instructions, not author claims of pass. At every size use
the same B mental model: one focused full-page body, pinned context and footer;
no wide split inspector. The compact file adds long-form continuation and
uncertainty frames; the wider files state their dimensioned full-page reuse.

| Journey | Frames and keys | Pass condition/challenge |
| --- | --- | --- |
| Find remote Hawk from launch | F0, arrows past stopped desk birds to work-node Hawk -> F -> Enter -> D | Whole fleet/empty hosts visible; selected host/kind/ID explicit; Enter cannot connect from ledger |
| Filter and clear | F -> h -> H; choose work-node -> unselected filtered ledger; arrows select; h -> All | No replacement target; selected scope/filter/hidden counts visible; master unchanged |
| Duplicate and stopped filtering | Compact F-all -> select desk Hawk -> / hide stopped -> F-filter -> / clear -> choose again | No retirement/deletion/ownership inference; failed and empty host rows retained |
| Work/blocker | D observes mapped diagnostics; 2 then t -> T-list -> select Hawk -> Enter -> T | VM running separate from working/waiting/blocker; freshness/source explicit; absent evidence unknown |
| Connect/return | F -> D -> Enter -> A; compact PgDn -> A-end; Enter -> existing foreground; Ctrl+] -> B Details; Esc -> F | Ctrl+] versus Ctrl+D clear; same target/scope/ledger scroll retained; no transport/session rewrite |
| Host loss/recovery | F -> F-offline -> F-reconnect -> Enter D -> explicit action | Cached rows retained, actions blocked, no local fallback/auto-resume; prior uncertainty not declared success |
| Removed or changed registration | D or X -> compact M semantics | Refuse dispatch; no replacement; require fresh selection/intent |
| New thread | L -> n -> N -> Ctrl+S preview -> Ctrl+S create -> masked empty reader | Complete captured membership, engagement/request; no message/wake on creation; n resumes kept creation draft |
| Root/reply and scroll | L -> Enter R masked -> Ctrl+V; Down selects msg-demo-18; PgDn -> R-select; p -> C; c separately -> C-root | Reply/receipt follows selected stable message, not visible last line; root has no parent |
| Send/resize/update | C -> edit literal text, resize both ways, receive +2 new -> Ctrl+S P -> Ctrl+S V or U | Same full-page composer, captured master/principal/thread/parent/recipients; no focus/draft theft or automatic retry |
| Draft/cancel | C -> Escape K -> Keep -> C-resume -> c original C; explicit Discard/Return; quit warning | Memory-only retention; no duplicate ID/composer; frozen U reopened instead of new send |
| Setup | S-master (every size), S-host (every size), Servers -> add form; compact S-fail | Master distinct from SSH references; no token entry, trust bypass, service startup or local fallback |
| Action confirmation | D -> x Destroy -> X; compact PgDn X-end; refresh/remove/address edit | All identity/argv/consequence reviewed; stale target refuses; no retarget or rollback promise |
| Loading/control feedback | G; explicit reads, navigation, writes, failure; temporarily blocked action | Read/control status visible; no ignored key or delayed dispatch; routine cached reads remain quiet |
| Help/theme | Q; Tab Help inside editor; repeat all journeys at all sizes without color | Focus and keys discoverable; no private bodies in help/errors; light/dark/low/no-color meanings identical |

Static assessment can identify design contradictions, omissions and cell-fit
problems. It cannot prove real polling, runtime no-flicker, keyboard dispatch,
PTY restoration, privacy, freshness or permission behavior. Robin independently
assesses feasibility; Wren independently walks operator journeys. Rook coordinates
the single consolidated handoff, not duplicate follow-up nudges. All assessments
must identify the revised exact head, not the earlier merge-only head.

## Proposed Implementation Sequence

No tasks are created by this proposal. If Chris approves the revised packet and
subsequent production scope, use Go/Bubble Tea, hostrouter, masterclient and the
existing CLI/MCP/SSH handlers. No new framework/server/scheduler/credentials.

| Proposed stage | Small outcome and dependencies | Acceptance/tests | Proposed ownership |
| --- | --- | --- | --- |
| 0. Baseline | Start at published main 7e6fd1d19df601310819987036eae6961dedfb4e or an explicitly reconciled successor, not an older UAT overlay | Preserve #112 cached-read tests, initial/navigation/write/failure/blocked-control feedback and empty hosts | Hawk baseline integration; Robin independently verifies |
| 1. Ledger/Details | All hosts grouped, optional filter, no target by default, full-page Details at all widths | Duplicate names, empty/loading/offline hosts, filter/remove/reorder/registration changes; no direct ledger Connect; safe target capture | Hawk author; Robin review; Wren target-discovery journeys |
| 2. Honest diagnostics | Work/blocker and presence through existing report interface; depends on stable placement mapping | Unsupported/denied/stale/missing/skewed-report cases; unknown rather than invented idle; no authority inference | Hawk author; Robin data-contract review; Wren comprehension |
| 3. Conversations | Full-page list/reader/composer/preview, per-buffer masking, memory-only draft protection | Follow while scrolled, stable reply selection, resize/draft guards, exact frozen retry, privacy leakage tests | Hawk author; Robin interaction review; Wren message/reply journeys |
| 4. Setup/actions | Distinct master/SSH first use, complete action parity and confirmations | All current command-inventory tests; long identities/argv at three sizes; cancellation/retarget/refusal and real PTY return in authorized fixtures | Hawk author; Robin safety review; Wren setup/attach journeys |
| 5. Integrated acceptance | Authorized exact-runtime-artifact assessment after prior stages | Light/dark/NO_COLOR, wide/combining/control text, fault/poll/focus tests, required independent QA and explicit owner UAT | Robin engineering gate; Wren independent QA; Rook consolidated owner handoff |

Every stage retains independent exact-head review and required customer QA.
Success never implies release permission. Production approval, operator UAT and
release authorization are separate from choosing B and static design acceptance.

## Remaining Choices And Risks

B, All-by-default, optional host filter and no automatic target are settled. No
new proceed/A-versus-B question is needed. The next owner artifact is this revised
packet with independent results, not another direction poll. Remaining proposals
for packet approval are masked-reader opening and memory-only draft suspension,
plus scroll-to-review long confirmations. They trade an explicit reveal/review
step for privacy/target clarity, without durable drafts or a new safety authority.

Integration risks remain: exact placement mapping, long-label terminal cells,
request epochs/follow pagination, focus preservation and truthful diagnostics.
The published quiet-render baseline is integrated, not an unpublished dependency;
static review cannot clear its runtime behavior. New dependencies or contract
expansion require a scoped decision. Durable drafts, config editing/master
switching, tmux cleanup and transport/scheduler redesign remain outside this work.

## Author Validation

Run `flox activate -- python3 docs/design/quiet-tui/validate.py` for literal cell
fit and required frame coverage. `test_validate.py` deliberately removes each
wide first-use setup frame and asserts failure, guarding against a cell-only
false pass. Existing command inventories still need their Go tests. Record actual
author results on publication; none substitutes for independent acceptance.
No live fleet mutation, deployment, service change, formal self-review, QA
clearance, merge-to-main or UAT/release claim is part of author validation.

Author results on 2026-10-20 for v3, before publication:

- Static cell/frame validator: passed, 94 authored viewports, including required
  S-master/S-host states at all three dimensions and the alternative comparison.
- Setup-coverage regression guard: passed; each wide setup state was removed in
  turn in memory and its missing-frame error was required, not merely a fit pass.
- `flox activate -- go test ./internal/tui/... ./internal/config ./cmd/coop`:
  passed, including current command inventory and cached-refresh regression tests.
- `git diff --check`: passed. The author revision changes only design documents
  and static-validation tooling on the integrated #112 baseline.

These results are author evidence only. Renewed Robin review and Wren QA for the
published exact head remain pending, as does approval to implement the packet.
