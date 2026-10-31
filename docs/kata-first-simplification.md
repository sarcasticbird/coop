# Kata-first simplification

## Working contract

- Chris sets outcomes and priorities and decides material product changes.
- Kata holds tasks, decisions, acceptance criteria and results.
- Rook coordinates assignments and resolves routine blockers. Robin and Hawk
  implement and review; Wren designs and tests.
- Coop delivers addressed work, executes it and recovers interrupted execution.
- One shared team conversation contains the full roster. Task-specific discussion
  belongs on the task. Routine staffing must not require editing private chats.
- Publish results quietly. Address a bird only when asking it to act. Do not send
  acknowledgements that create another acknowledgement.
- Monitor processes must not manufacture periodic model conversations or keep a
  second task/reminder ledger. Surface actionable state changes on the task.

## Clean-start cutover

gh-sweep remains held until a small Kata task has passed the complete assignment,
implementation, review and result-publication path on the new installation.

1. Pause admissions, drain saved turns and stop all workers. Disable the old
   monitor, security digest, fleet automation and history jobs during maintenance.
2. Keep one private offline rollback set: consistent master and Kata snapshots,
   worker state, configurations and binary identities. Validate archives before
   retiring live state. This set is rollback evidence, not scheduler input.
3. Reconcile action journals and remote mutation outcomes. Terminal conversation
   or engineering turns alone do not establish that every mutation is settled.
4. Replace obsolete coordination conversations and reminders with the shared
   team conversation and current tasks. Start workers with clean, consistently
   registered identities/state; never mix a fresh master with old delivery cursors.
5. Install a single tested source generation for master, clients and all workers.
   Update bird instructions and host configuration to the same working contract.
6. Run the small end-to-end task and check quiet result publication, addressed
   handoffs, coordinator blocker delivery, restart recovery and idle CPU usage.
7. Resume normal work only after those checks; choose gh-sweep priority with Chris.

## Future history policy

The following is the target policy; automatic compaction is not yet implemented.

- Keep task outcomes, decisions and useful evidence in Kata. Close completed
  tasks; do not create additional recap threads or a parallel commitment ledger.
- Schedule from an active-work index. Completed history must not be scanned on
  each heartbeat. Recovery evidence may be loaded once per locked operation,
  never once for each historical inbox record.
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

The staged messaging changes make agent replies quiet and remove hop suppression
from explicit handoffs. Blocker notices can address another agent. Recovery
views are reused within an operation to avoid repeated manifest decoding.
The worker still scans historical inbox records: this optimization is not active
indexing or automatic retention. Fleet reset, staffing changes and deployment
remain cutover work until verified on the running system.
