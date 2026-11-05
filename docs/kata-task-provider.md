# Kata tasks through Coop

Kata is the team's task and conversation ledger. Every bird can create, read,
list, edit, claim, assign, comment on, close and reopen team tasks under its own
identity, without a planner grant, engineering job or owner/Rook permission relay.
GitHub holds
repositories, pull requests, checks and formal reviews. The existing native
worker still performs implementation, independent peer review, required customer
QA, exact-head merge and action recovery. Removing its remaining goal/binding
duplication is separate work in the [simplification plan](kata-first-simplification.md).

The master is the only Kata gateway. Workers use their existing authenticated,
identity-pinned master connection. They receive neither a host SSH key nor a Kata
administrator token. Kata runs beside the master with a private profile and its
authenticated native Unix socket; inherited `KATA_*` overrides are discarded.
Run its supported `daemon start --foreground` under the host service manager.

## Private configuration

Add this optional section to the master's owner-only service configuration:

```toml
[tasks]
provider = "kata"
binary = "/absolute/path/to/kata"
home = "/absolute/private/kata-profile"
project_name = "engineering"
project_uid = "01M5MCPEHHP4C98MZSHJEE3M8D"
policy_sha256 = "<current engineering TeamPolicySHA>"
```

Create and verify the native project first. Pin its immutable UID, rather than
trusting a mutable name. The profile must be an owner-only directory and must not
be a symlink. Keep configuration, credentials, database and acceptance evidence
outside the source repository.

Opt in each v3 worker with its existing master connection:

```json
{
  "task_source": {
    "provider": "kata",
    "project_uid": "01M5MCPEHHP4C98MZSHJEE3M8D",
    "retain_github_tasks": ["https://github.com/example/repository/issues/12"]
  }
}
```

Use the existing current configuration as the base. Preserve its master
placement, team policy, model, checkout and saved state. A missing `task_source`
keeps the GitHub task provider. Kata mode discovers new Kata tasks and only the
explicitly retained GitHub tasks; exact saved GitHub execution validation remains
available for recovery. An unavailable Kata provider fails closed.

## Everyday Kata use

Use the existing authenticated master connection, not a Kata admin token or
`gh issue`. Membership in the configured standing team conversation grants task
access to that team's pinned project. Services, nonmembers and unrelated projects
do not acquire access merely by knowing an issue UID. Task access grants no
code-delivery rights.

```sh
coop task list                         # follow next with --after CURSOR
coop task list --status open
coop task read ISSUE_UID
coop task create --file /private/create.json
coop task edit --file /private/edit.json
coop task comment --file /private/comment.json
coop task close --file /private/close.json
coop task reopen --file /private/reopen.json
```

Inside any bird use `/usr/local/libexec/coop-worker tasks OPERATION --config
/var/lib/coop/worker/config.json --input /private/input.json`. The same operations,
plus `claim` and `assign`, use that bird's existing identity. Input files are
private (mode 0600). Typical input:

```json
{"title":"Fix the selected row","body":"Context and acceptance criteria","request_id":"selected-row-task"}
```

```json
{"reference":"ISSUE_UID","message":"Found the cause; linking the PR here.","request_id":"selected-row-comment"}
```

```json
{"reference":"ISSUE_UID","expected_revision":7,"title":"Clarified task title","request_id":"selected-row-title"}
```

- List input is `{}`, with optional `status` (`open` or `closed`) and `after`
  from the previous response's `next`. Read input is `{"reference":"ISSUE_UID"}`.
- Create accepts `title`, `body`, optional `owner` and `request_id`. It returns
  the native issue UID without creating a goal, engineering binding or PR.
- Read before editing. Supply the observed `expected_revision` for edit, assign,
  claim, close and reopen. This prevents overwriting another bird's changes;
  it is not another approval requirement. Edit accepts `title`, `body` and/or
  `owner`; assign accepts `owner`; claim refuses to displace another bird.
- Close requires `reason` (`done`, `wontfix`, `duplicate`, `superseded` or
  `audit-no-change`), `message` and any applicable actual `evidence`. Reopen
  accepts `message`. Comments use `message` and optional `reply_to_uid`, without
  a revision precondition. Task closure is not restricted to Rook; do not mark
  delivery work done before its required review, QA and merge.
- MCP offers `coop_master_task`, `coop_master_tasks`, `coop_master_create_task`
  and `coop_master_update_task` for the same operations. Inputs cannot select
  an actor, endpoint, credentials or arbitrary provider metadata.
- Keep identical input and `request_id` on retry. Known successes replay from
  the existing receipt. Unknown outcomes retain their input/revision; after
  Kata's seven-day deduplication window, unresolved writes refuse rather than
  risk another effect. Inspect Kata rather than changing the request or revision
  to bypass uncertainty.

## Existing engineering registration

The normal CLI accepts the same master configuration as messages and goals:

```sh
coop task capabilities
coop task list --goal GOAL_ID --revision REVISION_SHA256
coop task read kata://project-uid/ISSUE_UID/owner/repository
coop task create --file /private/task.json
```

For existing engineering registration, creation JSON contains `goal`, `expected_approval_sequence`, `repository`,
`base_branch`, `title`, `body`, optional `depends_on`, and a stable `request_id`.
Read exact authority with `coop goal authority GOAL_ID --revision REVISION_SHA256`
before registration. This operation requires an owner or the `plan` grant and an already
approved goal. The master chooses the current approved actors and policy; prose
and Kata labels grant no authority. Retrying the identical request reconciles
its original task; changed payloads cannot reuse a request ID.

`coop_master_adopt_task` remains restricted to owners/planners for engineering
registration. Ordinary Kata maintenance above does not require that grant.
MCP does not expose unguarded engineering rebind or delivery-completion tools.

Inside an agent Coop, use `coop-worker tasks capabilities|read|list|create|adopt
--config /var/lib/coop/worker/config.json`; reads and mutations take a private
structured JSON file with `--input`. This lets a chief of staff plan through the
master without engineering or goal-approval privileges. Planning pins the provider
and project. Engineering additionally requires an exact local team-policy match.

## Completion, recovery and limits

Engineering task identities are canonical `kata://<project-uid>/<issue-uid>/<owner>/<repo>`
references. Coop stores approved artifact links and mutation receipts, rather
than another copy of the task ledger. Binding changes use Kata revision CAS and
a durable reservation that prevents PR publication against a changing assignment.
Failed responses retain their original request and reservation for reconciliation.

The native engine checks the actual merged PR, current exact head, peer review,
QA and checks before completing the Kata task. The engineering completion API
restricts automated delivery completion to the current Rook delivery principal and
requires its registered matching PR; it is not a separate GitHub merge verifier.
Only Kata's `done` closure satisfies dependencies. Canceled and superseded tasks
do not.

This version pins one project and engineering policy per master. Dependencies
must be Kata tasks in the same exact goal revision; finish retained GitHub work
on its original path. GitHub Projects and issues are not mirrored into Kata.
An unresolved create older than Kata's seven-day idempotency retention window
requires reconciliation instead of automatically risking a duplicate task.
If an owner changes approval while a provider write has an unknown outcome,
the original intent remains held. This version has no automatic repair or public
reservation-resolution command for that case; an operator must inspect the
provider and registry before repairing it. Ordinary rejected revision checks
with proof that the write never applied can recover with a fresh request.

Before switching workers, back up the master consistently, preserve native
journals and verify there are no active or uncertain turns. Install through the
checked worker installer, one worker at a time. Keep the prior configuration and
binary for rollback; never discard saved sessions or action journals to unblock
the migration. Back up the separate Kata profile too, using a consistent SQLite
backup or with its daemon stopped, rather than copying its live database file.

AgentsView remains a separate history viewer. It is neither task authority nor
current worker health. See [the isolated archive experiment](kenn-tools-pilot.md)
for its tested OpenCode import and evidence limitations.

## Adopt native intake without a duplicate ticket

Native comments and actor labels grant no approval. Rook first uses
`coop-worker tasks read --config /var/lib/coop/worker/config.json --input READ_JSON`
with `{"reference":"kata://PROJECT_UID/ISSUE_UID/owner/repository"}`. Read the
current issue revision and inspect the exact current goal authority through Coop.
The project UID is the URL hostname and is canonically lowercase; the issue UID
retains its uppercase spelling. Reuse the canonical reference returned by the
helper rather than constructing or case-normalizing the entire URL.
Then use `coop-worker tasks adopt` with the same config and a private input file:

```json
{
  "reference": "kata://PROJECT_UID/ISSUE_UID/owner/repository",
  "goal": {
    "source": "master",
    "master_id": "MASTER_ID",
    "goal_id": "GOAL_ID",
    "revision_sha256": "EXACT_APPROVED_REVISION_SHA256"
  },
  "repository": "owner/repository",
  "base_branch": "main",
  "expected_revision": 7,
  "expected_approval_sequence": 1,
  "request_id": "stable-adoption-request"
}
```

Use the actual read revision and approved decision sequence, not the illustrative
numbers above. The owner/planner operation admits that exact existing UID and
validates repository/base, policy, actors and dependencies. A retry reuses its
original request identity. `coop task adopt --file PRIVATE_JSON` and the
`coop_master_adopt_task` MCP tool expose the same operation. Stale revision,
unapproved scope or missing planning authority fails explicitly. Rook proposes a
new goal when needed and waits for an authenticated owner decision. It must not
infer approval from a native `approved` comment or reserved provider metadata.

If approval changes after the native adoption write but before registration, the
original request stays fixed. The same authenticated owner/planner can recover
through `POST /api/v1/tasks/bind` with a fresh request ID, the current approved
binding, the observed native revision, and the old binding's SHA in
`expected_assignment_sha`. This only reconciles a journaled adoption that has
not entered any task registry. It keeps the existing issue UID. If approval
changes again during recovery, read the new revision and assignment and submit
another fresh binding request under the current approval.

Conversation replies still use the existing worker comms/outbox and stable native
publication identity. See [the cutover recipe](consolidation-cutover.md) for source
mapping, canonical reads, old history, backups and restore recovery. No credential
or private profile path belongs in ticket text.

## Bounded current conversation reads

Exact comment reads and canonical page refreshes require a local Kata **0.19.0
SQLite profile, schema 34**. That native API's `show --thread` includes replies
and backlink bodies, and has no exact-comment GET. Coop therefore checks the
local daemon's pinned instance, version, project and issue access, then reads the
single current comment row from Kata's canonical database using the existing
SQLite driver with `mode=ro` and `query_only`. It never writes or migrates that
database, copies a discussion, or substitutes stale creation-event text.

The private profile's `[storage].dsn` selects an absolute SQLite path; when it is
absent, Kata's documented default is `<home>/kata.db`. The file and its immediate
directory must belong to the current host user and permit no group/other access;
symlink files/directories are refused. Unknown versions/schemas, PostgreSQL,
relative DSNs, inaccessible files and mismatched identities fail closed. Do not
change live profile permissions or storage to bypass a failure. A Kata upgrade
requires reviewing this narrow reader against the new native contract first.
Native writes, event polling and notifications continue through the native API.
Guests receive neither the database path nor provider credentials.

Canonical message pages measure the complete master response envelope against
the existing byte limit. A large page is retried at the same cursor with a
smaller event window, at most seven attempts. The accepted provider page's
cursor remains intact, including deduplicated edits and unrelated trailing
events. A single oversized result fails explicitly. Native UI membership probes
use an empty inbound selection to exclude discussion history before launching
the literal `kata tui ISSUE_UID` command.
