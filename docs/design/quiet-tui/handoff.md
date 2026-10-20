# Review And Future Work

## Acceptance Map

| Approved requirement | Artifact/evidence | Current gate |
| --- | --- | --- |
| Two directions, recommendation, before evidence | directions.md; README private provenance | Author proposal; independent review pending |
| Fleet/hosts, Team/detail, threads/reader/compose, Servers, help/confirmation at three sizes | 120x40.md, 80x24.md, 40x20.md; interaction.md | Cell-fit validation only; independent QA pending |
| Host/master/agent context, safe defaults/filtering, aligned rows/actions | interaction.md Context; H/F/D/X/A frames; F-all/F-filter/M | Proposed, not implemented |
| Running/work/stale/offline/empty, evidence, quiet refresh | interaction.md state table; T/S-fail/E/B frames | Static semantics only; no runtime flicker evidence |
| Current commands, safety, privacy, attach and reply | source.md retention matrix; interaction.md; A/C/P/U/K/V frames | Existing handler inventory preserved in proposal |
| Independent Robin feasibility and Wren journeys | Exact-head native review/QA, private originating thread | Not performed by author; required before ready for Chris |
| Concise packet and staged proposal, owner boundary | README index; this document | Owner design decision open; implementation unauthorized |

The publication head is the artifact identity. Robin must review that exact head,
and Wren must record QA for that head and `coop-tui-design-v1`. Any relevant
artifact change renews both gates. Findings and resolutions belong privately in
the originating Coop thread, not repository review-body quotations or screenshots.
QA private detail belongs only in native `qa` action `report_body`. Do not mark
this packet ready on author cell checks alone. Publication is not merge permission;
do not invoke delivery under this design-only scope.

## Independent Walkthrough Script

These are instructions for assigned independent assessments, not author claims
of completion. Use literal frames, interaction rules and the inspected baseline.
The full behavior contract applies to every size; 40x20 includes additional
transition frames to expose the hardest constraints instead of hiding them.

| Journey | Keys and frame sequence | Pass condition / challenge |
| --- | --- | --- |
| Find remote Hawk | H: Down to work-node, Enter -> F with no selection, Down explicitly selects Hawk -> F selected; Tab -> D (40) or inspector/detail (wide) | Cannot accidentally connect/start desk Hawk; host/address/kind/ID and separate master are clear |
| Inspect work/blocker | 2 -> t -> T-list; select Hawk, Enter -> T; PgDn -> T-end; Esc -> list | Can distinguish VM running, master presence and observed waiting/dependency; unknown data stays unknown |
| Connect/return | Fleet selected Hawk -> Enter -> A; compact PgDn -> A-end; Enter -> existing foreground; Ctrl+] -> B | Return instructions differ from Ctrl+D; exact target/filter/scroll retained; removed target -> M, no fallback |
| Message/reply | 2 -> l -> L; Enter -> R initially masked; Ctrl+V -> R revealed; select msg-demo-18; p -> C; type literal text; resize; Ctrl+S -> P; Ctrl+S -> V or U | Captured master/principal/thread/parent/recipient visible; new messages do not steal focus; preview not send; receipt not reply |
| Send uncertainty | C -> P -> submit with response lost -> U; Ctrl+R | Frozen same ID/input, no automatic resend or editable duplicate; no private body in errors |
| Draft/cancel | C -> Esc -> K; Keep -> R with draft indicator; c -> C; discard explicitly or guard on quit | No silent draft loss on refresh/resize; memory-only limit discoverable; not durable storage |
| Duplicate names and filter | H All -> F-all; explicit desk Hawk selection -> / hide stopped -> F-filter; / clear -> choose again | Hidden count visible; filtered selection no longer actionable; no inferred retirement; lab failure retained |
| Confirmation under change | x Destroy -> X; PgDn X-end at compact; background reorder/removal/address edit | Captured key stable; removal/address change refuses dispatch; full identity/argv/consequence reviewed before y |
| Setup | S-master, S-host, S -> a -> S-add; S-fail | Master and SSH setup distinct; no secrets, silent trust, service startup or local fallback |
| Empty/unknown | E, S-fail, M; diagnostics denied/unsupported per contract | Empty is successful filtered read, offline retains evidence, missing report not idle |
| Discovery/no-color | Q from Fleet, Tab Help inside C; every journey without color at 40x20 | No wall-of-shortcuts requirement; focused scroll owns keys; editor ? is literal, Help field works |

Additional v2 paths, at all three sizes:

- H -> Enter work-node -> F0 -> first Up/Down -> F. F0 has no target or action.
- F -> F-offline -> F-reconnect. Reordering preserves the key; reconnect never
  dispatches or establishes a prior action's success. Changed identity uses M.
- T-list -> Enter -> T; at 120x40 Tab can also focus the inspector without
  opening diagnostics. At 80x24 Fleet Tab -> D; at 40x20 Fleet Tab -> D.
- S-list -> a -> S at 120/80; S -> a -> S-add at 40. Server reference and master
  setup remain separate, with no invented registry fields.
- L -> n -> N -> Ctrl+S preview -> second Ctrl+S create -> new masked empty
  reader. The compact N-preview illustrates captured thread creation, not a send.
- Reader -> c -> C-root (no parent); reader Down selects msg-demo-18, p -> C.
  R-select demonstrates PgDn leaving the chosen reply parent unchanged/offscreen.
- C -> Escape -> K -> Keep -> C-resume -> c -> original C. These compact
  transition frames govern the same memory-only behavior at larger sizes.

For 120x40, traverse list/inspector with Tab; for 80x24 use full-page detail;
for 40x20 use D/T and top/end pages. Repeat reader/draft resize in both directions.
Run dark, light, low-color and no-color interpretation against the theme contract.
Static assessment can identify interaction contradictions and fitting failures;
it cannot prove real no-flicker rendering, PTY restoration, runtime privacy,
backend freshness, permission behavior or keyboard event handling.

## Staged Implementation Proposal (Not Executable Tasks)

No tasks are created by this section. Production work needs a subsequent scoped
owner decision. Reuse Go/Bubble Tea, current masterclient, hostrouter and CLI/SSH;
no new server, framework, scheduler, credentials or transport rewrite.

| Future stage | Small outcome | Dependencies and acceptance/tests |
| --- | --- | --- |
| 0. Baseline reconciliation | Establish integrated source including separately owned quiet-render repair | Coordinate its actual branch/head; peer review it separately; verify cached-read/render tests without inventing a published commit |
| 1. Context and host selection | Sticky host/master/principal context, explicit chooser, no automatic target, visible filter counts | Chris chooses default; key-based selection tests for duplicate names, removal, address edit, offline and no-result filters; existing handler tests unchanged |
| 2. Layout and focus | Aligned Fleet, split inspector, compact drill-in, contextual help and scroll | Stage 1; golden cell snapshots at three sizes plus wide/combining/control input; focus/anchor/resize property tests; command coverage test still exhaustive |
| 3. Honest Team integration | Existing diagnostics read contract in Coordination, lifecycle versus work labels | Stable placement join and read interface adaptation; stale/unsupported/denied/no-report/clock-skew fixtures; no new authority contract or inferred busy state |
| 4. Conversations and privacy | Reader/list/composer navigation, per-buffer masking and protected memory-only drafts | Chris accepts masking/draft choices; backend request semantics unchanged; follow while scrolled, resize/draft, exact retry, cancellation, no-content leakage tests |
| 5. Setup and action clarity | Separate master/SSH setup guidance, contextual actions, complete confirmation/preflight | Existing forms and safety handlers; long-identity/argv scroll review gate, retarget/race/cancel tests; real PTY detach/return tests separately authorized |
| 6. Integrated acceptance | Exact-artifact peer review and Wren journeys on authorized runtime fixture | Prior stages; light/dark/NO_COLOR, three sizes, quiet refresh under faults, permission and privacy checks; owner operator UAT explicitly recorded |

Every production stage needs Robin's independent exact-head engineering review
if Hawk authors, and required Wren QA under that stage's approved environment.
No success in stage 6 automatically releases a package: explicit operator UAT
and the existing separate release authorization still apply.

## Feasibility Risks And Owner Decisions

1. Approve A or B and the per-launch host chooser. Recommendation: A with no
   persisted host default or automatic target. This intentionally adds one step.
2. Accept masked reader opening and in-session draft suspension. Recommendation:
   yes, but label metadata sensitivity and process-loss limits. They are changes
   from current reader/form behavior, not current guarantees.
3. Confirm 40x20 drill-in and scroll-to-review confirmation. Recommendation: full
   identity/consequences over fitting everything into one cramped dialog.
4. Keep diagnostics as observations from existing contracts. Integration needs
   an adapter and exact placement mapping; do not fabricate a job queue or infer
   idle. If mapping is unavailable, unknown is the correct degraded result.
5. Quiet refresh is a cross-cutting implementation risk, especially resize,
   follow pagination and request epochs. Reconcile the separate repair first;
   static QA cannot clear its runtime behavior.
6. No new dependencies are proposed. If implementation reveals a contract or
   dependency expansion, return for a scoped decision instead of burying it in
   a visual stage. Master config editing, durable drafts, tmux cleanup and
   transport redesign are outside this proposal.

## Author Validation Boundary

The static validator checks ASCII cell fit, height, required common frames and
the alternative comparison frames. It does not connect to hosts/master or mutate
fleet state. The inspected source revision is in source.md; actual artifact SHA
is derived on publication, not a guessed SHA embedded into its own contents.
Private review and QA records remain authoritative for their own conclusions.

Initial v1 author checks on 2026-10-20 (superseded artifact, not v2 clearance):

- `flox activate -- python3 docs/design/quiet-tui/validate.py`: passed, 55 static
  ASCII frames within declared cell viewports, including 7 comparison frames.
- `flox activate -- go test ./cmd/coop -run 'Test(FleetTUICommandCoverageInventory|TUICommandCoverageExhaustive)$' -count=1`:
  passed against the inspected baseline; checks the unchanged current command
  inventories, not implementation of the proposed interactions.
- `git diff --check`: passed before publication. No production files changed.

No live host/master mutations, runtime UI test, service changes, product UAT,
independent peer conclusion or independent QA clearance were performed by the
author. Missing runtime validation is intentional for this static design scope.

## v2 Author Revision

The author expanded literal entry/transition coverage and clarified the reader
selection/scroll contract, new-thread versus root/reply behavior, draft/frozen
request re-entry, reconnect safety and the owner name. These are proposed design
changes, not product implementation. Independent exact-head review and required
QA must be renewed; no previous result is carried forward as clearance. Private
independent findings/resolutions remain in their approved private destination,
not this public artifact. The author does not infer resolution of feedback that
has not been handed off.

v2 author checks on 2026-10-20: the same Flox validator passes 84 frames,
including 9 direction-comparison frames, with common interaction entry frames
now required at all three sizes. Both command-inventory tests pass again, and
`git diff --check` passes. These checks still establish only static fit and
retention of the existing command inventories, not independent acceptance.
