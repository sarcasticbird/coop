# Quiet Coop: Design Packet v3

Status: author proposal, awaiting independent Robin review and Wren journey QA.
Design only. No production implementation, merge, deployment, service change,
release or Homebrew publication is authorized by this packet. Static review is
not operator UAT success. Chris's design decision and release UAT gate remain open.

Task: https://github.com/sarcasticbird/coop/issues/110
Native goal: `goal_6XFLKYPBU7N7OYL4GHQRFJTBKT`, approved revision
`2ff3a786197747a3056d12246b836f5608786e9b86384d21a10510a1b0c0fbad`.
QA environment: Static Coop terminal mockups and operator journey design review.
Environment revision: `coop-tui-design-v1`.

## Read This Packet

1. [Directions](directions.md): two information architectures and selected B.
2. [120x40](120x40.md), [80x24](80x24.md), [40x20](40x20.md): recommended
   fleet ledger, same sanitized fixture and labeled journey frames.
3. [Interaction contract](interaction.md): focus, safety, states, refresh, privacy,
   theme and responsive rules. These rules also govern the alternate direction.
4. [Source and coverage](source.md): inspected baseline, existing capabilities,
   command retention and explicit proposed-only changes.
5. [Handoff](handoff.md): acceptance map, independent walkthrough script, staged
   future proposal, risks and decisions for Chris.

All names, addresses, work references and message content in the mockups are
invented fixtures, not snapshots of customer activity. `design-master`, `desk`,
`work-node`, `demo-owner`, issue `#42` and addresses under `.example` are fixtures.
Hawk/Robin/Wren are role names used to exercise duplicate-name targeting.
No private screenshot, brief, original identifiers or review findings belong here.

## Before Evidence

The author read the originating private brief and inspected the supplied before
image, verifying its SHA256 privately. The original remains at its private input
location and in the originating Coop thread; it is deliberately not embedded,
copied or linked through a public asset service. The public-safe observations
are: repeated identity strings compete with friendly names, a stopped local
duplicate is initially actionable above a running remote duplicate, the footer
competes with the content, and available width does not clarify selection.
The recommendation addresses these observed problems, not an invented before UI.
Private provenance and independent findings stay in the originating thread.

## Selected Direction

B, Fleet Ledger With Full-Page Drill-In, is the owner-selected direction and
supersedes the initial A recommendation. Default to the whole fleet grouped by
host, with no selected target. Host filtering is optional. Fleet -> Details ->
Connect is consistent at every width; Team, threads, readers and forms also use
full pages. A remains only as a comparison, not an open decision. This records
the selected experience, not approval to implement, merge or release it.

## How To Read The Frames

Each fenced `text` block is a static terminal viewport. Its heading declares the
viewport; unused cells to the right and below are blank, not hidden content.
Frames intentionally leave breathing room. They are not a screenshot of running
software. `>` is selection, `[FOCUS]` names keyboard ownership, `|` divides panes,
and `[more below]` means the focused region can scroll. No ANSI controls, special
font, mouse or color are required. `...` in a list name means clipped display;
`...` never substitutes for a safety-critical captured identity in confirmation.
The validator checks every line fits the declared cell rectangle and that all
required frames exist; it does not validate runtime layout or behavior.

```sh
flox activate -- python3 docs/design/quiet-tui/validate.py
```

All frames show no-color rendering. Dark/light treatment is specified in the
interaction contract; identical text, selection and state labels survive both.
The richest frame is not a promise that every data source is available: missing
diagnostics must render `unknown`, not a plausible invented work summary.

Frame IDs `A` (attach preflight) and `B` (return) are journey labels, not layout
direction choices. Every frame in the dimension files now belongs to selected
layout direction B. Run the setup-coverage regression guard as well:

```sh
flox activate -- env PYTHONDONTWRITEBYTECODE=1 python3 docs/design/quiet-tui/test_validate.py
```
