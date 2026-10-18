# Coop master

The master stores private agent conversations, addressed delivery receipts and
owner goal decisions in a local SQLite database. Workers connect outbound from
their Coops. Their existing OpenCode servers, GitHub Apps, workspaces and provider
logins remain inside the guests. GitHub keeps the code, tasks, Projects, PRs,
checks and formal reviews.

The master runs inside the `coop` CLI. It does not control remote VM lifecycle or
forward shell execution. Keep each host's existing MCP server for those actions.
The master exposes coordination through `/api/v1`, `/events` and `/mcp`.

## Private configuration

Use [master.toml](../examples/master.toml),
[master-client.toml](../examples/master-client.toml) and
[worker-master.json](../examples/worker-master.json) as neutral examples. Copy
them outside this checkout into the existing private configuration workflow.
Make configuration and credential files mode 0600 and their directories mode
0700. Replace every placeholder before enrollment. No example grants access.

The service stores SHA256 token verifiers. Each agent has its own random token,
stable principal ID, immutable GitHub bot ID and placement. A placement binds a
host ID, worker instance ID and generation. Agent tokens bind the same instance
and generation. An alias such as `builder` routes messages; goal assignments use
stable IDs. Renaming an alias does not transfer authority.

The client file selects the master URL and stable master ID. Choose exactly one
credential reference: `token_env` for an existing environment secret, or
`token_file` for an absolute private credential file. Guest services should use a
private file reference so startup does not depend on an attached terminal.
Provision credentials through the existing private workflow. The installer does
not copy an owner's token into a guest.

Loopback HTTP requires the explicit client setting `allow_loopback_http = true`
and an IP loopback URL. It is intended for disposable local tests. Remote workers
use HTTPS, including over Tailscale. Configure `tls_cert_file`, `tls_key_file`,
the service's allowed Host values and any required browser origins. The client
checks the certificate and pinned master ID and refuses redirects.
Every API, SSE and MCP request also supplies `X-Coop-Expected-Master-ID`. The
server checks it before applying an action. The CLI supplies it from the client
configuration. Generic MCP clients must configure this header and their bearer
Authorization header for `/mcp`.

Start a configured service explicitly:

```sh
coop master serve --config /absolute/private/master.toml
```

The owner principal has `read`, `send`, `manage` and `decide`. An agent normally
has `read` and `send`; the planner also needs `plan` to register native tasks.
A chief-of-staff service can read, send and propose with scoped capabilities.
It cannot approve goals or change membership. GitHub permissions remain a
separate prerequisite.

## Conversations

Pass `--master-config /absolute/private/master-client.toml` to select a client.
The default is `$XDG_CONFIG_HOME/coop/master-client.toml`, or
`$HOME/.config/coop/master-client.toml` when `XDG_CONFIG_HOME` is unset.
Commands print JSON, including durable IDs and receipt state.

```sh
coop thread create --title "Coop design" --engagement example-engagement \
  --member example-builder,example-planner,example-qa --request-id design-thread
coop thread list
# Continue a nonempty `next` cursor with: coop thread list --after <next>
coop message send THREAD_ID --to builder --body-file /absolute/message.txt \
  --request-id design-question-1
coop message status MESSAGE_ID
coop thread read THREAD_ID --follow
coop team
```

`coop inbox` reads an agent credential's own pending deliveries. The owner reads
replies through the thread and can follow it from the CLI or MCP.

Use the same request ID and input when retrying a send. Changed input with an old
request ID conflicts. A reply uses `--reply-to MESSAGE_ID`; without explicit
recipients it addresses the parent author, excluding the sender. An empty
recipient list on a new root message records a note without waking watchers.
Automatic peer replies have a two-hop budget. Owner messages start a new chain.

`coop --agent NAME ping --prompt "Your question"` uses the master only when that
private agent profile explicitly selects `communication.backend = "master"`
with its client configuration, stable agent ID and engagement. It queues while a
guest is stopped and does not start the VM. Legacy profiles retain their existing
GitHub ping behavior.

Only the owner changes a thread's members:

```sh
coop thread members THREAD_ID --expected-member OWNER,BUILDER,PLANNER \
  --member OWNER,BUILDER --request-id membership-change-1
```

The complete prior member set prevents overwriting another membership change.
Revocation removes read access and cancels unstarted addressed deliveries.
Running native evidence is held for reconciliation; revocation does not claim
that the model process stopped.

| Receipt | Meaning |
| --- | --- |
| `queued` | The master committed the message and addressed delivery. |
| `received` | The guest persisted it locally. |
| `running` | The guest saved the native session/message intent. |
| `replied` | The master committed a linked substantive reply. |
| `blocked` | A recorded condition needs attention. |
| `cancelled` | The delivery was explicitly cancelled. |
| `suppressed` | The automatic peer reply budget was exhausted. |

Presence is separate. Heartbeats run every 15 seconds and become stale after
45 seconds. An idle connected guest should persist an addressed message within
five seconds, even while GitHub catalog refresh is blocked. Busy, disconnected
and model execution time are separate waits. A message notification or SSE
cursor is never a delivery acknowledgement.

## Native goals and engineering

Conversation text does not authorize engineering. Propose an immutable native
goal revision with purpose, bounded scope, repository/base branches, acceptance
criteria, author, independent peer and QA policy. Required QA also names an
environment revision and a private evidence destination. These fields contribute
to the approved revision hash.

```sh
coop goal propose THREAD_ID --file /absolute/private/proposal.json \
  --request-id goal-proposal-1
coop goal authority GOAL_ID --revision REVISION_SHA256
coop goal approve GOAL_ID --revision REVISION_SHA256 \
  --expected-decision-seq 0 --request-id goal-approval-1
coop goal list --engagement example-engagement
```

The owner alone decides. The commands `reject`, `pause`, `resume`, `complete` and
`reopen` require the exact revision and observed decision sequence too. Read
authority again before making a later decision. A new draft leaves the current
approval intact. Approving its replacement supersedes the previous assignment;
the planner must explicitly rebind unfinished work and obtain fresh review/QA.
Completed deliveries do not reopen automatically.

The scheduler shares one native slot between conversations and engineering.
When engineering is ready, at most two completed conversation turns precede one
engineering turn. Existing engineering handoff fairness remains. Before
admission and consequential publication, workers recheck current master
authority and GitHub task/assignment, head, checks and independent reviews.
Master outage blocks new publication and merging while allowing observation of
the exact saved native turn. A remote decision can race the final check and
GitHub write; the two systems do not share a transaction.

The native helper registers GitHub task and PR links under the approved goal.
When more than one Project is configured, task creation and rebinding require
an explicit `project_owner` and `project_number` on the first request. Invalid
selection is rejected before saving a mutation journal or changing GitHub, so
the planner can correct that input using the same request ID. Once a journal
exists, its original input remains immutable.
It publishes immutable private QA evidence and places only its digest/reference
and safe summary in a formal exact-head GitHub QA review. A chat reply or private
report alone cannot satisfy QA. Authored work needs an independent peer review.

## Explicit worker conversion

Version 3 selects `master.backend = "master"` and pins the full placement plus
private client reference. V1/v2 metadata, jobs and wake records retain their
existing schemas and IDs. V3 never scans the retired GitHub conversation source.
It preserves that history without dispatching it.

For unused storage, run the matching staged worker's `init-state --config` with
the v3 configuration. It initializes the exact configured instance and verified
migration evidence. Existing workers first finish or explicitly resolve all
legacy jobs and wakes under their original authority. Stop the worker and all
producers, take a cold backup, and run the reviewed staged binary explicitly:

```sh
/absolute/staged/coop-worker migrate-state --config /absolute/private/worker-v3.json \
  --from 2 --to 3 --expected-instance EXACT_INSTANCE_ID
```

V1 needs a separate completed 1-to-2 conversion first. The standard installer
then checks the v3 receipt and exact instance before replacing its managed
artifact generation. It refuses older artifacts and silent placement changes.
Staging does not launch services. Do not point an old binary at v3 storage.

After checking native and GitHub evidence, an operator may explicitly name
obsolete, blocked, unbound jobs with repeated `--retire-legacy-job ID` flags on
the 2-to-3 command. Jobs with pending submissions or current ownership bindings
remain refused. Repeated `--accept-legacy-wake ID` flags accept only completed
legacy wake receipts whose old format omitted a terminal timestamp; independently
verify those exact native turns first. The converter keeps original statuses,
records the selections in the immutable migration backup, and never replays them.
Runtime startup and later placement adoption verify the unchanged retired jobs
against that backup. These options do not approve new work.

An interrupted conversion holds both versions. Inspect the immutable snapshot
and unchanged evidence, then use `recover-master-migration --config ...
--expected-instance ...` with the stopped worker. Startup never retries it.
The recovery command completes only the exact before/after publication; changed
evidence requires operator investigation.

For a held conversation, `coop-worker comms resolve --config ... --delivery ...
--session-id ... --message-id ... --action observe` inspects the saved turn.
`--action cancel` requires terminal native evidence for submitted work. It never
replays a prompt. After a bounded observation failure, `--action observe` can
resume observation of the same owned native intent. Engineering has the
corresponding `native resolve` command.
Keep uncertain GitHub mutation journals for explicit remote artifact inspection.

## Offline acceptance

Run `scripts/uat-master.sh` from the repository to exercise deterministic master
and native delivery fixtures. It creates no live VM or GitHub resource. These
checks establish protocol behavior, not live bot or Tailscale acceptance.

Enabling a live pilot requires a reviewed packet with exact builds, private
enrollment references, cold backups, source cutoff and recovery steps. Keep bird
host moves, permanent rack-mini hosting and release publication as later
operator decisions.

## Backup and recovery

`coop master backup --directory /absolute/private/backup-001` asks the running
master to write a consistent SQLite snapshot, its private service configuration,
and a hash manifest. The directory must be empty and owner-only. The snapshot
includes committed WAL writes. External TLS keys and token files referenced by
configuration need their existing private backup process.

The independent epoch lineage defaults to `<database>.epoch`. Set `epoch_path`
in the private service configuration to keep it in a separate durable location.
It records the highest reserved epoch and the current principal/placement
registry. Preserve its newest copy independently of database backups. Restoring
an old copy of this file defeats epoch fencing; do not roll it back with SQLite.
Both the database and lineage have process locks. Offline restore refuses while
the master holds the lineage lock.

A crash after publishing a principal or placement registry change but before the
SQLite commit can leave startup reporting a registry mismatch. This is a closed
recovery state. Keep the advanced lineage file: restore a known-good backup into
an empty directory with that same lineage, then reconcile the guests as below.
Restore preserves the newest principal and placement fences and reserves a new
epoch. It does not repair the original database in place; messages after the
backup require the guest evidence review.

After stopping the master through its normal service controls, review and restore
into an empty directory:

```sh
coop master restore --backup /private/backup-001 --directory /private/restored \
  --epoch-path /private/recovery/master.epoch --dry-run
coop master restore --backup /private/backup-001 --directory /private/restored \
  --epoch-path /private/recovery/master.epoch
```

Each applied attempt reserves a strictly higher epoch, even if it later fails.
Restore retains the master ID, immutable author history and current enrollment
registry. It copies the old configuration to `config.original.toml` for review.
Update the service configuration's database and epoch paths before starting the
restored service. Startup retains quiescence. Messages, delivery transitions,
goal mutations and artifact publication remain blocked.

Stop every enrolled worker and run its explicit evidence operation:

```sh
coop-worker comms reconcile --config /private/worker.json
coop master reconcile status
coop master reconcile evidence --agent stable-agent-id
```

The worker holds both its service lock and action producer lock. It enumerates
inbox, outbox, published receipts, native engineering and action journals. It
observes saved native identities without resubmitting them. Each record is
uploaded separately; the final digest covers the complete inventory. Evidence
pages accept `--after <last-evidence-id>`; keep reading until the page is empty.
An individual record over the protocol bound or a local inventory over 16 MiB
fails explicitly and keeps recovery blocked. Preserve that private evidence and
resolve its size through a separately reviewed recovery procedure. No records
are silently omitted.

Review each record and explicitly retire its associated restored delivery:

```sh
coop master reconcile resolve --epoch 2 --agent stable-agent-id \
  --evidence <evidence-id> --reason 'Reviewed terminal native receipt'
```

An uncertain record requires `--proof <matching-terminal-evidence-id>` from a
later report of the same saved native identity. Repeat the stopped-worker report
after terminal observation. Unknown remote action outcomes remain blocked;
terminal model output alone cannot resolve an uncertain GitHub action. Recovery
does not fabricate missing messages or replay replies from an older epoch.
Orphan evidence stays in the private database after resolution. Already replied,
cancelled and suppressed delivery truth is retained.

After resolving every guest's evidence, each stopped worker accepts the reviewed
manifest, then the operator resumes admission:

```sh
coop-worker comms reconcile --config /private/worker.json --apply
coop master reconcile resume --epoch 2
```

Acceptance preserves original inbox/outbox/native files and records which saved
work has been retired. It updates the connection epoch only after durable local
acceptance. Restart workers after resume. New delivery reconciliation starts at
the beginning, regardless of an old SSE cursor. An offline guest, an incomplete
manifest, an unresolved native/action record, or an unexplained received/running
master delivery prevents resume. Do not use a timeout to waive these blockers.

For a planned placement move, first use `coop master reconcile pause --epoch N`,
stop workers and complete the same report/resolution process. Then review:

```sh
coop master placement set --agent stable-agent-id --host new-host \
  --instance new-instance --expected-generation 1 --generation 2 --epoch N --dry-run
```

Apply the same command without `--dry-run`. Stable agent/thread IDs remain; the
placement generation advances once. Update the private service token policy and
configured placement to the new instance/generation before restarting the master
or starting the moved worker. The old generation is denied. Preserve the entire
private state directory, its permissions, and a copy of the previous private
worker configuration on the destination. Keep both workers and native action
helpers stopped. Adopt the accepted old inventory with the new configuration:

```sh
coop-worker comms adopt-placement --config /private/new-worker.json \
  --previous-config /private/old-worker.json
coop-worker comms reconcile --config /private/new-worker.json
# Review/resolve the new exact placement's report, then:
coop-worker comms reconcile --config /private/new-worker.json --apply
```

Adoption verifies the master's recorded transition, the old accepted inventory
and unchanged local evidence, including the scheduler file digest. It records the
relationship and preserves the old scheduler bytes before moving scheduler and
connection metadata. Eligibility timestamps and history counters stay intact;
ledger, inbox, outbox and native execution identities keep their original values.
A retry accepts only the recorded old or new scheduler bytes. New engineering
turns use a checkout directory scoped to the current placement, preserving old
checkout markers and checkpoints; foreign markers are still rejected. A fresh
fix checkout fetches and verifies its assigned PR head so the existing
implementation is present. Later preparation preserves local fix commits and
uncommitted checkpoints.
Changing either host alone or host and instance is supported. Historical work is
retired; fresh work uses the new placement. Installation accepts the changed
placement only after this proof exists. The new exact guest must reconcile
before the owner resumes the master and starts it. Adoption is retryable after
interruption, but cannot waive uncertain native actions or changed evidence.
The state path must match the previous configuration on the destination; changing
local directory layout is a separate maintenance step. The retained proof has the
same 16 MiB manifest limit and fails explicitly if exceeded.

These commands do not provision credentials, reload policy or forward lifecycle
operations between hosts.

Thread listings return `threads` and `next`. Each page contains at most 100
records and 1 MiB; use `--after` (or the API/MCP `after` field) until `next` is
empty. Native goal and QA validation use authorized individual thread lookups,
so retained inventory growth does not block authority checks.

## Historical source cutover

Import history as read-only provenance, then record the owner's explicit cutover:

```sh
coop master history import --thread <thread-id> --source-url https://github.com/org/repo/issues/1 \
  --cutoff 2026-10-17T00:00:00Z --body-file /private/history.txt --request-id import-001 --dry-run
# Apply the same import without --dry-run and review its returned SHA-256.
coop master history list --thread <thread-id>
coop master cutover --thread <thread-id> --source-url https://github.com/org/repo/issues/1 \
  --history-sha256 <reviewed-digest> --dry-run
```

Apply cutover without `--dry-run`. A thread's source cutover can be recorded once;
changed replays are rejected. Imported owner-looking text produces no native
messages, decisions, deliveries or wakes. V3 workers derive native authority from
the master and do not scan the retired GitHub goal conversation. Existing V1/V2
GitHub-governed work keeps its original authority; import/cutover never converts
in-flight work or changes worker configuration. Use the guarded offline migration
only after legacy work has terminal evidence.
