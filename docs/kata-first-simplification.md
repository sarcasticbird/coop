# Kata-first simplification

## Working contract

- Chris sets outcomes and priorities and decides material product changes.
- Kata holds tasks, decisions, acceptance criteria and results.
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

Containers must be replaceable from pinned images, with durable memory,
checkpointed work, session data needed for recovery, and action receipts outside
the replaceable root disk. Replacement fences the old execution generation;
it does not create a new bird or erase knowledge.

Use focused tests for identity/access, restart persistence, no duplicate
consequential effects, and the complete implementation/review/QA/Rook-merge
loop. Keep gh-sweep unchanged for now: it is the refreshed birds' first real
project under the new workflow, not a historical migration prerequisite.

The memory API/importer, Rook-only native merge policy and earlier coordination/
startup cleanup are implemented in source. Portable execution state, actual
image-swap support and live fleet activation remain implementation work.

## Future history policy

The following is the target policy; automatic compaction is not yet implemented.

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
