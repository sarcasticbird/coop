# Coop and Kata acceptance recipe, version 1

This recipe stages one exact client/master and worker candidate, then describes a
separately authorized application. Building it does not install anything, change
normal `coop`, configure a live provider, migrate a conversation, or restart a
bird. A dirty preview is review evidence, never an accepted installation.

## Stage the candidate

Use the accepted worktree through Flox. Read the current source SHA and pass that
full SHA explicitly. Candidate output must be absent and under ignored `bin/`.

```sh
env TMPDIR=/private/tmp flox activate -- python3 -B scripts/consolidation_candidate.py \
  stage --source-head FULL_COMMIT_SHA --output ABSOLUTE_CHECKOUT/bin/candidate-v1
```

An implementation review may add `--allow-dirty`. This creates `source.json`
with the tracked and untracked nonignored file inventory, a source archive, the
Darwin ARM64 `coop` client/master, Linux ARM64 `coop-worker` and `coop-github`,
and `config/coop/candidate.json`. The manifest binds the source revision, source
tree digest, artifact hashes, platforms, and absolute selected profile. The
`coop-preview` launcher selects only that versioned profile. Its profile starts
empty, without production credentials or a provider destination. Missing private
configuration must remain an actionable failure until an approved fixture is
configured. Merely running `coop-preview --help` proves no live workflow.

The build clears `GOFLAGS`, disables workspace overrides, and uses
`-buildvcs=true`. When Go omits VCS metadata for a worktree `.git` pointer file,
explicit linker stamps record the source revision, dirty state, and tree digest.
The entry checks any native VCS metadata against those stamps. Missing or
contradictory provenance stays unknown. Dirty source stays a dirty preview even
when every artifact hash matches. This is local provenance, not a signature.

Before approval, verify against the same source checkout:

```sh
env TMPDIR=/private/tmp flox activate -- python3 -B scripts/consolidation_candidate.py \
  verify --source-head FULL_COMMIT_SHA --output ABSOLUTE_CHECKOUT/bin/candidate-v1
```

`verify --allow-dirty` can inspect a preview and still reports `dirty-preview`.
It does not approve it. Any source edit, including a new untracked file, requires
a new candidate. A clean candidate remains pending independent exact-source
review and operator UAT. Use `scripts/scheduler_artifacts.py` for the existing
scheduler packaging manifest and `scripts/install-agent-worker.sh` for checked
worker installation after approval. Do not invent a second guest installer. The approved worker staging command is:

```sh
env TMPDIR=/private/tmp flox activate -- bash scripts/install-agent-worker.sh \
  AGENT_NAME ABSOLUTE_CANDIDATE/artifacts/coop-worker ABSOLUTE_PRIVATE_WORKER_JSON
```

This stages into an already-running unshared VM and preserves its installation
transaction. It does not start services. Follow [worker lifecycle and backups](workers.md)
and [the release recipe](release.md) for the separately approved service/profile
application. The authorized master service command remains
`coop master serve --config ABSOLUTE_PRIVATE_MASTER_TOML`; its service manager
must select the accepted versioned binary and its existing private configuration.
Do not start a second master beside the current listener.

## Record the authorized transition

Keep a private evidence directory with the candidate manifest, operator decision,
master ID and restore epoch, native instance/project UIDs, host registrations,
selected thread and issue IDs, and each worker's placement/generation. Record the
standing `Winged Keys team` issue and every selected project conversation. Retain
stable setup comment IDs, their approved bounded context, the original thread's
legacy-read command, and the native event baseline. Do not put credentials or
private paths in a Kata comment.

Stage compatible master, client and worker binaries before source mappings.
New Source/progress journal fields can be rejected by older strict decoders.
Preserve the old binaries, configurations and journals. Never strip fields or
truncate journals to make a downgrade run.

Use existing worker admission controls to drain accepted replies and engineering
work before the maintenance window. Observe durable receipts, current native
turns, outboxes and uncertainty; an idle-looking TUI is insufficient. Do not
cancel a busy or uncertain turn to proceed. The mapping operation refuses a
thread with any unfinished legacy delivery. A failure leaves its history and
receipts unchanged.

This implementation uses a brief master-wide maintenance pause, rather than new
per-thread fence state. After drain, run the existing owner/operator controls:

```sh
coop master reconcile status
coop master reconcile pause --epoch CURRENT_EPOCH
coop master backup --directory EMPTY_PRIVATE_MASTER_BACKUP_DIRECTORY
coop master conversation cursor
```

Pause prevents new gateway roots, replies and event polling. Native Kata can
still receive comments. The master backup is consistent through the supported
snapshot command. Also take a consistent native Kata database/profile backup and
retain attachments and daemon configuration using its supported backup mechanism
or an offline SQLite backup while the native daemon is explicitly stopped. Do not
copy live SQLite files. Native comments must be held for that backup boundary or
accounted for in a subsequent consistent native snapshot. Preserve per-worker
journals/config/binaries through the existing checked installer and backup
procedure. Inventory and hash all backup files privately; prove an empty-directory
restore before application. A master backup alone is not a cross-system backup.

Record a real project event cutoff after pause, using native paginated events.
The first mapping may explicitly use zero for a genuinely fresh project. For an
existing project, advance through bounded native pages once during setup and
record the last existing event ID before the transition. This setup scan is not
a recurring poll. A positive cutoff must exist in the pinned project. Never pick
an estimated or future ID. Keep one baseline across the selected mappings while
polling is paused; a baseline behind the stored master cursor is rejected.
Comments created after that baseline, including during mapping, remain pending.
Publish any approved setup context before taking the cutoff, and record its IDs.

```sh
coop master conversation map --thread EXISTING_THREAD_ID \
  --instance NATIVE_INSTANCE_UID --project NATIVE_PROJECT_UID \
  --issue NATIVE_ISSUE_UID --after-event RECORDED_EVENT_ID \
  --request-id STABLE_MAPPING_REQUEST
coop master conversation legacy EXISTING_THREAD_ID --after 0
```

Only an authenticated owner with `manage` and current thread membership may map.
The bridge and intake principal must also belong to that thread. Map each goal
and QA evidence destination using the original internal thread ID. Do not create
replacement anchors. A retried mapping uses the same request ID and exact input.
After mapping, new text goes only to Kata. Legacy history remains available via
the command above, the `legacy-messages` API, and
`coop_master_read_legacy_thread` MCP tool. Normal reads use canonical native text
and fail explicitly when it is unavailable. Old history never creates a wake.

Inspect the resulting source mappings and receipt state, let workers reconcile
through the existing recovery protocol, and resume only when its safeguards pass:

```sh
coop master reconcile status
coop master reconcile resume --epoch CURRENT_EPOCH
```

No automatic fallback writes to both stores. If acceptance is uncertain, keep
maintenance explicit, retain the new-capable binaries and reconcile. A return to
old binaries after new state exists requires a consistent before-backup restore
across master, Kata and workers, with a new restore epoch and lost-response
reconciliation. There is no live unmap or journal-downgrade shortcut.

## Restore a source cursor

A master restore fences conversation polling/publication until the owner accepts
the exact stored cursor in the new epoch. Keep the restored master paused. Inspect
`coop master conversation cursor`, compare the native instance/project and
cutoff with the consistent backup and immutable publication IDs, then save:

```json
{
  "observed": {
    "restore_epoch": 2,
    "accepted_epoch": 1,
    "instance_uid": "EXACT_NATIVE_INSTANCE_UID",
    "project_uid": "EXACT_NATIVE_PROJECT_UID",
    "after_event_id": 123,
    "after_event_uid": "EXACT_IMMUTABLE_EVENT_UID_AT_123"
  },
  "reason": "Consistent backups and uncertain publication receipts reconciled",
  "request_id": "stable-owner-reacceptance"
}
```

Run `coop master conversation reaccept --input PRIVATE_JSON`. This verifies the
actual native provider and exact cursor, changes only its accepted master epoch,
and preserves receipts and publication intent IDs. It neither rewinds events nor
resumes execution. Wrong native identities require correction of the private
provider configuration and separately scoped migration evidence; this command
cannot authorize an unrelated provider. A native backup missing the observed
cutoff is refused. The saved event UID must also match: a restored provider that
reuses the same numeric ID for another event is refused. Ordinary polling checks
the same immutable checkpoint in one bounded read before fetching new events.
The first mapping also saves the event UID at its nonzero baseline.

Older saved cursors may lack `after_event_uid`. They fail closed even if the
provider still has the same numeric position. Keep the master paused and recover
the original event UID from independent retained checkpoint evidence or a
consistent native backup. Submit the exact anchorless `observed` cursor plus
`"reconciled_event_uid": "ORIGINAL_EVENT_UID"` through the same owner-only
`reaccept` operation, with a reason describing the evidence and a stable request
ID. The server verifies that UID at the unchanged numeric position before saving
it. This field cannot replace an existing anchor. Do not copy the current
provider UID merely because its number matches. If the original identity cannot
be established, restore a consistent backup or arrange a separately reviewed
reconciliation; there is no automatic adoption or cursor rewind.

Finish the existing guest recovery protocol before resume.

## Required acceptance evidence

Run full Go tests, vet, lint and builds, the packaging/recovery fixtures on Linux,
and independent review against the exact final source and candidate. Rebase onto
accepted main before the final clean candidate tests. Record which native API/MCP
checks ran against a disposable native profile and which tests used protocol
fixtures. A fixture model reply is not a real model session or operator UAT.

In an approved private deployment, run the complete operator journey:

1. Start the selected installed `coop tui` and verify its About/doctor revision,
   artifact, profile and destination on both hosts. Open `Winged Keys team` with
   Chat, notify Rook and observe exactly one native reply. Have Hark read and reply
   through its existing authenticated Coop MCP.
2. Create native intake, observe its acknowledgement, and let Rook read the current
   revision and adopt that same UID under an exact approved goal. Unapproved scope
   stays intake and requests an owner decision. Use the planner commands in
   [kata-task-provider.md](kata-task-provider.md).
3. Observe the assigned bird's current work and blocker. Attach directly, detach
   to the same fleet row, and open the current ticket. Complete peer, QA and
   exact-current-head merge checks and observe the reply on its Kata issue.
4. Disconnect during a pending turn and an uncertain comment write. Recover the
   same task, native intent and publication receipt without another model call or
   comment. Test duplicate comment/notify, busy queues, wrong identities, restore
   mismatch, native actor spoofing, self-wakes and peer loops. Verify one bounded
   event page per poll even with substantial historical comments.
5. Exercise retained GitHub task review/delivery and read old conversation history.
   Preserve evidence for unmet gates; do not declare a frontend fixture suite to
   be live communication acceptance.

Only after explicit install/restart authority and this acceptance, select one
normal `coop` binary/profile on both hosts and verify native tool handoffs again.
Retire competing temporary wrappers only after approval, retaining their manifests
and rollback binaries. Delete no VM disks, session history, old backups or unrelated
tools. Automatic AgentsView collection remains a separate follow-up.
