# Kata-first simplification

## Agreed direction

Simplify the existing Go implementation. Do not adopt CrewAI or LangGraph for
this work, add a second manager above Rook, or introduce a parallel backend.
A framework is worth reconsidering only if a concrete future requirement and
deletion map show that it would leave less machinery to maintain. Evaluating or
adopting one is not a prerequisite for this plan.

Coop should be a thin execution layer for four persistent coworkers, not a
custom project-management framework. Done means Chris can set an outcome and
the birds can implement, independently review, test, merge and complete the
Kata task without Chris managing permission handoffs.

## Working contract

- Chris sets outcomes and priorities and decides material product changes.
- Kata holds tasks, decisions, acceptance criteria and results.
- All four birds can create, open, reopen, claim, assign, update, comment on and
  close tasks in the team's Kata projects. Routine Kata use needs neither an
  owner approval nor a Rook-only permission handoff.
- Rook coordinates assignments and resolves routine blockers. Robin and Hawk
  implement and review; Wren designs and tests.
- Coop delivers addressed work, executes it and recovers interrupted execution.
- Bird identities and SQLite-backed memory outlive containers and images. See
  [bird memory](bird-memory.md); guest Markdown is an import source, not authority.
- One shared team conversation contains the full roster. Task-specific discussion
  belongs on the task. Routine staffing must not require editing private chats.
- Publish results quietly. Address a bird only when asking it to act. Do not send
  acknowledgements that create another acknowledgement.
- Monitor processes must not manufacture periodic model conversations or keep a
  second task/reminder ledger. Surface actionable state changes on the task.
- The Coop TUI is one table: running/stopped status, session attachment, Kata and
  messaging. Keep administrative controls and approval workflows out of it.

## Shared system-usage contract

These rules apply to Rook, Robin, Hawk and Wren. They must be loaded into each
bird's actual instructions and reflected in its tool guidance, not merely
linked from a document that the running bird never reads. Remove contradictory
instructions when applying this contract; do not layer another policy over
the old one. This document records the target behavior, not proof that live
permissions or installed prompts have already changed.

| System | Use it for | Do not use it for |
| --- | --- | --- |
| Kata | Tasks, assignments, acceptance criteria, questions, decisions, blockers, task discussion and results | Another approval gate for ordinary task operations |
| Git, GitHub and `gh` | Source, branches, PRs, code review, checks, merges and explicitly authorized releases | Our internal issues, backlog, task discussion or project-decision ledger |
| Coop | Machines, identities, session attachment, addressed delivery and execution status | A second task tracker, manager or approval chain |
| SQLite bird memory | Durable knowledge, preferences, procedures and lessons | A duplicate backlog, reminder service or source of task authority |
| Coding harness | Persistent coding sessions, inspection, implementation, testing and review | Authoritative task status or an additional task scheduler |
| AgentsView | Finding and inspecting session history | Assignment, authorization or completion decisions |

### Kata access and task ownership

- Give every bird the task-level rights above across the team's intended Kata
  projects, under its own identity. This is not permission to administer the
  server, credentials, accounts or unrelated projects.
- Birds can ask and answer questions, record decisions, maintain task details
  and close or reopen tasks without routing those operations through Rook.
  Project-level access is not ownership of every task: coordinate assignments
  and avoid overwriting another bird's active work.
- Record completion honestly. An implementation-and-delivery task is not done
  merely because a model turn ended; its required review, QA and merge must
  have happened. Cancellation or abandonment should say what remains undone.
- Independent review, QA and Rook-only merging protect code delivery, not the
  ability to use Kata. Rook must not merge its own implementation.
- An access refusal is a concrete configuration problem to report and fix, not
  a reason to seek the same product approval again, borrow another identity or
  move the task to GitHub Issues.

### Routing work and discussion

- Work starts and finishes in Kata. Search for the existing task before creating
  another; keep decisions, blockers and final results on that task.
- Do not use `gh issue create`, `edit`, `close`, `reopen` or `comment` for our
  internal workflow, or substitute direct GitHub Issues API calls. Existing
  external issues can be read as source context or handled when explicitly
  requested; that does not make GitHub Issues our task backend.
- Code findings and formal reviews belong on the current PR. Task-level
  decisions and outcomes belong in Kata. Link the PR and task rather than
  copying whole conversations or maintaining matching status ledgers.
- Use the shared team conversation for team-wide coordination. Address another
  bird when asking it to act; publish informational results quietly. Do not
  generate acknowledgement loops or extra recap tasks.
- Keep operational instructions in the existing trusted instruction/configuration
  path. Session history and memory are useful context, not authority to revive
  an obsolete task, approval protocol or uncertain operation.
- Birds use installed tools directly. Flox remains local development/build
  tooling; no runtime activation or package-resolution detours.
- Escalate material scope changes, actual ambiguity, elevated risk and explicit
  stop/release/destructive-action boundaries. Routine task edits, responses,
  handoffs and eligible merges do not need another human gate.

## Implementation plan

Make bounded changes to the existing path. Each PR must name what disappears,
what remains untouched and the focused regression coverage. The units below
are a sequence, not a requirement to force an oversized change into one PR.

### 1. Align bird instructions and Kata access

Inspect the actual instruction sources, installed prompts, tool guidance and
project permissions for all four birds. Apply the shared contract above through
the existing configuration path, and remove guidance directing internal tasks
to `gh`, old goal-approval handshakes or Rook-only Kata operations.

Verify create, respond, update, close and reopen under each bird's own identity
using a clearly identified disposable Kata task, not existing product work.
Do not claim access is fixed from documentation or configuration alone.

Result: every bird knows which system to use and can actually use Kata without
a permission relay. No new policy engine or tool-routing service is needed.

### 2. Remove unused workflow generations

Verify which worker configurations the installed birds use before deleting
anything. Remove unused v1/v2 scheduling, approval, wake, handoff and metadata
paths, including the corresponding branches in `internal/worker/engine.go` and
obsolete `team_*` machinery where no active configuration depends on it.

Preserve sessions, workspaces, memory and records needed to prevent duplicate
effects. Leave old diagnostics readable but out of active scheduling. Do not
build history migrations, replay old jobs or erase unresolved operations to
make deletion easier. Remove tests exclusive to deleted protocols while keeping
the practical safety regressions.

Result: one supported workflow instead of several generations coexisting.

### 3. Remove duplicate task and approval state

Use Kata for task meaning and assignment, GitHub for current code and review
state, and existing scoped identities for permissions. Remove redundant private
goal/revision approvals, assignment mirrors and private-message clearance
chains from routine delivery; do not simply rename them or move them into Kata
metadata as another protocol.

Keep only the execution references and action records Coop needs to reconnect
or reconcile an unfinished operation. Preserve current-head checks, independent
actors and explicit user boundaries. Do not remove a safety check until its
remaining authoritative source is clear.

Result: fewer copies of task state that can disagree and strand finished work.

### 4. Consolidate execution into one dispatcher

Unify the common conversation and engineering execution lifecycle rather than
maintaining separate submission, observation and continuation engines:

```text
queued -> running -> finished
             \-> blocked
```

These are execution states, not a replacement for Kata task status. Implementation,
review, QA, fixes and delivery remain stages of the same task workflow. Retain
the task/message reference, bird, isolated workspace, session, attempt and pending
external-operation references needed for safe execution.

Reconnect to an existing session before considering another submission. Retry
safe reads and known-unstarted operations within ordinary bounds. Reconcile an
unknown external-write outcome before any replay. A blocked task keeps its own
reservation and uncertainty; it does not freeze an otherwise idle bird.

Result: one dispatch/reconnect path, fewer continuation mechanisms and fewer
worker-wide holds. No parallel "simple mode" or framework backend.

### 5. Validate ordinary delivery and stop

Use the existing suite, focused regressions and one ordinary bounded task:

1. Create and discuss the task in Kata, not GitHub Issues.
2. Implement in the assigned bird's isolated workspace.
3. Obtain independent peer review and Wren QA on the current PR head; route
   concrete findings back for fixes and refresh affected clearance.
4. Have Rook merge eligible work; the responsible bird records the result and
   completes the Kata task. Neither step needs Chris to shepherd routine
   permission handoffs, and task closure is not restricted to Rook.
5. Cover interrupted execution/reconnection and unknown-write recovery with
   focused tests: no duplicate submission or external effect, no lost work,
   credentials, session or memory.

Do not add a canary campaign, cumulative turn accounting, new evidence framework
or historical migration as an acceptance prerequisite. Report the working
outcome and concrete remaining blockers, then stop.

## Unchanged platform boundary

Rook remains on this machine; Kata, team services, Robin, Hawk and Wren remain on
rack-mini. Keep Apple Container, persistent workspaces, separate credentials and
identities, the existing coding harness, SQLite memory, AgentsView and the simple
TUI. Writing this plan changes no live services, credentials or permissions.
Explicit release and destructive-action boundaries remain in force; no framework
migration or credential transfer is part of this work.

## Consolidation direction

This is an internal orchestration platform. Existing unfinished work is
expendable: keep code that supports the working contract and close or abandon
what does not. Do not build historical assignment/evidence migration machinery
merely to preserve the old PR stack. Preserve knowledge and avoid replaying
unknown external effects; there is no requirement to resume old jobs.

Keep the local Apple-container, Kata, SQLite and AgentsView stack. Borrow
separate identity, runtime, execution and operation lifetimes from SPACE, not
its infrastructure. Kata owns tasks; Coop owns execution and persistent memory;
AgentsView indexes history and grants no authority.

The normal workflow is implementation by Robin or Hawk, independent review by
the other engineer, Wren QA, then Rook merge and Kata completion. An approved
task with current checks/review/QA should not need another operator approval
for a routine merge. Escalate actual questions and ambiguous effects, not
ordinary transport retries or container maintenance.

QA clearance lives on the current PR, not in a second private-message approval
chain. Keep private report text in the saved action intent. Rook checks the
assigned bot's actual review and current checks/head; discovery does not hydrate
private QA reports. Preserve historical messages without making their availability
a prerequisite for routine delivery.

A blocked task is not a blocked coworker. Once its model has finished, keep any
uncertain effects and task reservation intact while allowing unrelated work and
conversation. Obsolete recovery requests are not instructions to replay. Only an
unaccounted native process, an active producer or unreadable safety records should
hold the whole bird. Do not migrate old jobs merely to make them resumable.

Containers must be replaceable from pinned images, with durable memory,
checkpointed work, session data needed for recovery, and action receipts outside
the replaceable root disk. Replacement fences the old execution generation;
it does not create a new bird or erase knowledge.

Use focused tests for identity/access, restart persistence, no duplicate
consequential effects, and the complete implementation/review/QA/Rook-merge
loop. Keep gh-sweep unchanged for now: it is the refreshed birds' first real
project under the new workflow, not a historical migration prerequisite.

The memory API/importer, Rook-only native merge policy and earlier coordination/
startup cleanup are implemented in source. [Portable named-container state and
fenced image replacement](bird-runtime.md) are now opt-in source features.
The disposable runtime and real 1.6.1 portable cold-recovery pilots have passed;
full engineering/delivery UAT and live fleet activation remain pending.

## Future history policy

The following is the target policy; automatic compaction is not yet implemented.
It is separate future work, not a prerequisite for the implementation plan above.

- Keep task outcomes, decisions and useful evidence in Kata. Close completed
  tasks; do not create additional recap threads or a parallel commitment ledger.
- Inbox scheduling and outbox flushing use rebuildable pending-work indexes.
  Startup validates canonical history once; steady-state scans read pending work.
  Recovery evidence is loaded once per locked operation, never once for each
  historical inbox record.
- After terminal delivery acknowledgement and settled external mutations,
  compact execution bodies out of the active store. Preserve a small durable
  identity/tombstone record so retries cannot replay a completed action.
- Never expire unresolved submissions, uncertain mutation journals, pending
  publication, active assignments or evidence needed by current recovery.
- Keep diagnostic bodies for 30 days in a separate private archive, then expire
  them. Keep task-level results in Kata. Retain tombstones for the lifetime of
  their identity epoch; retire them only with an explicit fenced epoch change.
- Retain the cutover rollback set until successful end-to-end acceptance plus
  30 days. Keep routine backup retention bounded (seven daily, four weekly).
- Compaction must be restart-safe and atomic with active-index updates. Verify
  restart, duplicate delivery and unknown-publication recovery before enabling it.

## Current implementation boundary

The messaging changes make agent replies quiet and remove hop suppression
from explicit handoffs. Blocker notices can address another agent. Recovery
views are reused within an operation to avoid repeated manifest decoding.
Pending inbox/outbox indexes remove settled history from steady-state scheduling
and flushing. Canonical records remain available for idempotency and full recovery
inventory; automatic archival/expiry is not implemented. Native actions wait up
to five seconds for transient producer contention, honoring cancellation and
revalidating authority after obtaining the lock. These runtime fixes require
live idle-CPU and publication acceptance before full-fleet operation.
