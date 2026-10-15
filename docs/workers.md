# GitHub workers

An agent Coop can run an authenticated OpenCode server and a small GitHub worker
as independent helper services. The terminal attaches to that server. Detaching
the terminal leaves the server and worker running inside the VM.

This is an opt-in backend for named agents. Interactive Coops and profiles with
an ordinary terminal command keep their existing behavior and mounts.

## Runtime and private configuration

Build `cmd/coop-worker` for Linux arm64 through the repository's Flox environment.
Keep each worker JSON configuration in the private machine branch of dotfiles,
alongside its Coop profile. Do not put credentials or private goal text in this
public repository. The example in `examples/worker.json` contains placeholders.
GitHub App signing keys remain on the host; guest operations use the existing
renewable installation-token helper. Provider logins remain in native OpenCode
storage. The server generates its password in the guest, mode 0600.

The worker configuration names immutable human/bot account IDs, code owners,
Discussion backing repositories, Project owners or explicit Project selectors,
an explicit provider/model, private state and checkout roots, and bounded limits.
Use `repositories` and explicit `projects` for isolated acceptance fixtures;
otherwise repository and Project catalogs refresh automatically. Robin's role is
`implementer`; Hawk's is `planner`. Hawk needs Project write access, while Robin
only needs read access. A repository's `checkout_modes` value can be `wt` when it
uses the shared bare-repo workflow. Install `wt` before selecting that mode.

Guest paths:

| Path | Purpose |
| --- | --- |
| `/usr/local/libexec/coop-worker` | Worker binary |
| `/usr/local/libexec/coop-opencode-{server,attach,launch}` | Checked launchers |
| `/var/lib/coop/worker/config.json` | Private configuration |
| `/var/lib/coop/worker/state/` | Checkpoints, lock, pause control and safe status |
| `/var/lib/coop/opencode/server-password` | Guest-local server authentication |
| `/workspace/worker/` | Owned task checkouts, separate from manual work |

Stage artifacts with `scripts/install-agent-worker.sh NAME /absolute/linux-binary
/absolute/private-worker.json`. The installer requires an already-running,
unshared VM, verifies architecture/checksums/configuration and preserves original
managed artifacts before replacing them. It refuses independent guest changes.
It does not start services or change profiles. Keep a cold VM backup before a
cutover. Apply profile changes through Coop's supported down/apply/up lifecycle.

Example profile commands, retaining existing resources, credentials and seeds:

```toml
[profiles.example]
command = ["/usr/local/libexec/coop-opencode-attach"]
restart = "always"

[profiles.example.services.opencode]
command = ["/usr/local/libexec/coop-opencode-server"]

[profiles.example.services.worker]
command = ["/usr/local/libexec/coop-github", "env", "--", "/usr/local/libexec/coop-worker", "run", "--config", "/var/lib/coop/worker/config.json"]
```

The server binds to `127.0.0.1:4096` and requires Basic authentication. Helper
startup order is unspecified. The worker waits for readiness, and the attach
client waits up to 30 attempts. With `restart = "always"`, exiting the client
reopens it after two seconds; this does not restart the server. Press `Ctrl-b`,
release, then `d` to detach tmux. Copy uses terminal selection or tmux copy mode;
OSC 52 clipboard integration needs a compatible host terminal and explicit tmux
configuration during onboarding.

## Goals and work admission

A Discussion or private issue describing a goal includes repository scope:

```html
<!-- coop-goal:v1 {"repositories":["example/project"]} -->
```

The configured human owner approves in the goal Discussion with a standalone
`/coop approve` comment or a clear unconditional statement such as `approved`,
`I approve this goal`, or `this looks good to me`. Quoted, conditional and
ambiguous comments do not approve work. Agents use the worker's verified approval
decision, not their own interpretation of a conversation.
`/coop pause`, `/coop resume`, `/coop reject` and `/coop complete` control that
goal. Approval is bound to the current goal body and edit timestamp. Editing the
body requires renewed approval. Host bootstrap approvals are a separate explicit
owner-confirmed configuration, not a bot's claim that a goal is approved.

Catalog admission and fresh revalidation require two matching full comment scans
bracketed by matching goal body/edit versions. A body edit or changed decision
during those reads fails closed until a later poll obtains a coherent version.
These are optimistic GitHub reads, not a lock on subsequent owner activity;
revalidation still runs immediately before a selected job is submitted.

You can also approve from your Mac through Coop:

```sh
coop --agent hawk approve https://github.com/example/coordination/discussions/3
# Old threads without repository scope need an explicit scope once:
coop --agent hawk approve https://github.com/example/coordination/discussions/3 --repo example/project
```

This uses your existing host `gh` login and verifies that it matches the configured
human owner. It records approval on the Discussion, bound to its exact body and
repository scope. It never sends your credentials into a VM or starts a stopped
VM. Both agents see the same decision on their next poll. Ping remains a way to
send a prompt; a ping alone does not approve a new goal.

Hawk includes repository scope in each new proposal before asking for approval.
For an approved goal, Hawk can repair task/PR links and maintain Project state
from a planning or conversation turn. Robin can repair her own PR's tracking
metadata. These administrative changes let the scheduler dispatch review or
delivery; they do not permit code changes or merges in conversation sessions.
Metadata changes that invalidate a prior review require a fresh Hawk review.

Hawk can propose goals and plan within approved goals. A task needs trusted
metadata from Hawk or the owner, matching its current title/body hash and linking
its goal/dependencies. The helper prints correctly escaped metadata:

```sh
/usr/local/libexec/coop-worker task-metadata --config /var/lib/coop/worker/config.json \
  --task https://github.com/example/project/issues/4 \
  --goal https://github.com/orgs/example/discussions/1
```

Publish the comment, then mark its Project item Ready. Dependencies must already
be completed. Title/body changes invalidate task metadata. Work claimed manually
is not silently adopted. Worker-created claims identify the worker instance.

Robin opens PRs with `coop-pr:v1` metadata linking `task` and `goal`. Hawk reviews
the exact head SHA in an independent checkout and submits a formal review.
Review freshness also binds the canonical PR/task/goal links and trusted task
metadata. Catalog reads and fresh revalidation scan GitHub's durable PR body-edit
history before and after reading reviews, comparing complete history including
edit IDs and order, then verify the body version/head again. Whole-second body
timestamps alone cannot rule out same-second changes that restore the old links.
Settled histories may contain equal timestamps in their durable newest-first
order; a review in that same second remains unproven and requires a later verdict.
Changing either link, including restoring old links or repairing missing/invalid
metadata, requires a review strictly after the latest scope transition. Restarts
and edits made entirely between polls do not reset this boundary. The scope
revision also distinguishes dedicated review jobs at the same head.

Comments, check updates and other unrelated PR activity do not invalidate reviews.
Body prose edits or JSON formatting changes preserve the continuous canonical
scope visible in edit history. GitHub does not expose the original pre-edit body:
for edited PRs, reviews before the oldest known matching body version cannot be
proven in scope and require re-review. Unedited PRs use their creation time.
Unavailable, deleted, inconsistent or ambiguous history fails closed; no poll-local
cache or current-link hash substitutes for that evidence. This is optimistic
read validation, not a lock against subsequent GitHub edits or review changes.
Already-merged PR issue finalization still checks current tracking and repository
identity without requiring a new review or historical review-scope evidence.

Every automated turn receives its configured agent name, bot identity and role
separately from quoted GitHub input. A planning input that links a PR also carries
a review handoff for its current head. Posting "Hawk will review next" does not
complete that handoff. Completion needs a verified current-head formal review,
a closed PR, or a reply acknowledging the current admission blocker with the
supplied `BlockerMarker` and planning `ResultMarker`. Blocker markers bind the
job, PR, head and reason; a stale marker cannot excuse a newly available review.
Ordinary planning messages without PR links retain their existing reply flow.
Reviewable handoffs dispatch through the dedicated review checkout and session
before planning resumes. A planning turn that discovers a newly available review
yields its slot without consuming a retry as a failed attempt.

An implementer PR without task/goal metadata remains visible for diagnostics.
Status reports `pr_metadata` and dry-run names the affected PR. Observation does
not admit the PR or silently adopt manual work. Its goal approval and task/PR
metadata still need to be established before automatic review/fix/delivery.

Changes requested return to Robin; a new head returns to Hawk. Delivery requires
Hawk's current-head approval, successful checks and GitHub's CLEAN merge state.
An explicitly absent check rollup also permits delivery when GitHub reports
CLEAN. Unknown, pending or failed checks still block delivery. An approved goal
does not need additional owner approval for routine merges within its scope.
After a merge, Robin reconciles the issue as completed before Hawk marks the
Project item Done. Cancelled issues stop implementation. A busy saved session
holds automation for manual takeover without changing its checkout or aborting it.
Verified completed deliveries also schedule one Hawk planning handoff to reconcile
the Project and advance eligible dependent work. The normal job ledger suppresses
repeat turns. This uses merged-PR/completed-task evidence within an approved goal;
it does not reset conversation reply limits or grant new goal approval. A status
promise alone cannot complete the handoff: board Done and eligible dependent
queue progress must be observed.
New goals/projects wait for owner approval. Agents coordinate through GitHub;
terminal messages are not needed for routine handoffs.

## Controls and recovery

```sh
coop --agent robin status
coop --agent robin worker status
coop --agent robin worker pause
coop --agent robin worker resume
```

These controls require a running VM and never acquire credentials or start one.
Inside the guest, inspect the worker's safe status with:

```sh
/usr/local/libexec/coop-worker status --config /var/lib/coop/worker/config.json
systemctl status coop-helper@worker.service --no-pager
systemctl status coop-helper@opencode.service --no-pager
```

The helper units use Coop's `coop-helper@.service` template. There is no
`coop-worker.service` unit. Unit status proves the process is running; worker
status also reports the last GitHub poll, server health and admission wait reason.

Pause reconciles the worker's exact message before attempting a session abort.
The same guard covers deadlines, permission blocks, revoked approval and wake
interruptions. A terminal worker reply is reconciled even if a later manual turn
has made the session busy; no abort is sent in that case. Missing messages (including
404), unreadable history, a foreign session, or a newer user message without a
terminal worker reply leave ownership uncertain and retain the active slot with
retry backoff, rather than cancelling manual work or replaying the worker prompt.
The bounded history window may require operator reconciliation for old messages.
These are optimistic reads: OpenCode 1.18.18 exposes session-wide abort, not an
atomic message-conditional abort. A takeover concurrent with the final ownership
read and abort request is not protected by a server-side lock; pause automation
and let its active turn settle before starting manual work in the same session.
Resume clears local pause; it cannot override a goal paused on GitHub.
Safe status reports heartbeat, server
health, active task, submission state and reasons such as owner approval, review,
retry or unknown submission. A heartbeat older than two minutes is stale.

The worker polls GitHub, normally every minute with jitter. Catalog scans run
independently of active-turn observation and local pause. GitHub rate limits and
server outages back off visibly. Unchanged idle polls do not invoke a model.
Each task retains its checkout and session across review heads and worker
restarts. Persisted message intent prevents blind replay after a lost response.
An uncertain submission or abort holds the active slot until reconciliation.
Model prose alone never proves completion; later GitHub artifacts do.

A failed task checkpoint stops the worker with `checkpoint_failed`. The worker
reloads the stored ledger for inspection when possible and exits rather than
continuing with uncommitted jobs, counters, or session bindings. Correct the
storage problem before restarting; do not delete or reset the ledger. A save can
report failure after replacing its file, so restart reconciles any saved message
intent without blindly submitting it again. This fail-safe does not abort the
OpenCode turn: it may already have been accepted and must retain its identity.
Wake receipts and routing are read afresh from storage on each operation; failed
writes likewise never authorize replay of a persisted running intent.

Only worker-owned sessions receive unattended permissions. Existing explicit
deny rules are preserved, interactive questions and credential-dump operations
are denied, and unresolved tool permission requests block visibly. The VM still
grants the agent root authority; these rules reduce accidental disclosure.

Stopping the VM or sleeping/offlining the host stops monitoring. Restart rejoins
the saved task/session and refreshes GitHub before dispatch. No launchd timer or
remote host wake is installed.

## Queued pings and Discussion replies

Worker v2 is opt-in. The implementation and local artifacts are available in
this feature checkout; installed guests keep their current behavior until the
private configuration and worker binary are applied and the worker is reloaded.
The OpenCode server and terminal attachment are unchanged.

Add this to each agent's private worker.json in your private dotfiles repo:

```json
"wake": {
  "enabled": true,
  "coordination_url": "https://github.com/orgs/YOUR_ORG/discussions/1",
  "peer_hop_limit": 2
}
```

The coordination Discussion must be in the configured Discussion scope. Missing
or disabled wake configuration preserves v1 behavior. The default peer limit is
two; supported limits are 1 through 8.

```sh
coop --agent robin ping
coop --agent hawk ping --prompt 'Please explain the current review queue.'
coop --agent robin worker requests
coop --agent hawk worker status
```

Ping requires a running VM and a fresh, running, wake-enabled worker. It queues
behind active work and respects local pause and GitHub backoff. It never starts a
VM or resumes a worker. Prompt text travels to the guest as structured stdin;
ordinary status and request summaries omit it. An optional `--request-id` takes
a 64-character lowercase hex idempotency key for retries. Reusing that key with
different input fails. Without a key, identical prompts coalesce while queued;
a new ping after completion remains new work. For shell metacharacters, use
normal single-quote shell quoting, as above.

Each complete GitHub poll finds mentions of the exact configured bot handles,
case-insensitively, with or without `[bot]`. It also finds replies whose actual
GitHub parent is one of those bots. An explicit mention takes precedence over
an implicit parent recipient. Quoted examples and code do not address an agent.
Only the configured owner and agent identities can trigger a wake, verified by
immutable GitHub ID and type. An agent's own message cannot wake itself.

The first complete poll records a history baseline without answering old
messages. Later restarts catch up on unread revisions. Meaningful edits create
new requests; outer whitespace and CRLF changes do not. Directed messages do not
also start Hawk's general planning turn. GitHub flattens replies under a root
comment; agents reply within that verified root thread and reference the source
permalink when needed.

Conversation can answer questions, propose work and report approval or metadata
blockers. It does not approve new goals or authorize implementation, formal
review or merging. Those actions use the existing admitted task jobs. One worker
turn runs at a time. At idle, one eligible fix/review/delivery job may precede the
oldest queued wake; that allowance survives restart. Further work waits for the
wake. Worker-owned coordination sessions are separate from task checkouts.

A wake completes only after the native turn ends and a substantive Self reply
with its request marker is found in the intended GitHub thread. Unknown submits
retain their exact native message ID and active slot. The worker reconciles
before retrying. Marker-only comments and planned actions are not completion.
Peer messages inherit a durable per-thread budget, even without a marker. Only
new owner input starts a fresh chain. No automatic enqueue/completion reply is
posted. Suppressed peer messages are recorded privately without another reply.

Requests, receipts and routing live under `STATE_DIR/wake/v1` with private file
modes. V1 state.json/control.json retain their schemas. Rolling back the worker
binary/config leaves accepted wake inputs and native conversation history in
place; do not restore an entire old native history directory. The queue holds
at most 128 nonterminal requests and 10,000 retained requests; reaching either
limit fails visibly. Ordinary summaries show at most the latest 20 requests.
Inside the guest, an exact redacted lookup is available:

```sh
/usr/local/libexec/coop-worker requests --config /var/lib/coop/worker/config.json --request-id HEX_ID
```

Use guest `coop-worker validate`, `preflight` and `dry-run` before activation.
Preflight checks server/API/model compatibility without a model call; dry-run
reads GitHub and prints admitted jobs/reasons without claims or model dispatch.
Keep reports private because dry-run includes repository/task identifiers.

## Existing guest Flox migration

Existing VMs can retain an older Flox that cannot parse current project manifests.
`scripts/upgrade-agent-flox.sh NAME stage|apply|rollback` stages pinned Flox 1.18.1
in `/opt/coop-flox-upgrade` using its official immutable Nix flake. Stage checks
core tools in a separate environment copy and runs the real `to` test/build
commands in an isolated validation checkout. The pinned source reports version
`1.18.1-gf264cf2`. These root guest images use single-user Nix without a `nixbld`
group; the migration scopes that setting to its invocations and guest bootstrap.
Apply preserves the verified guest bootstrap, prepends the candidate
to its PATH, verifies effective resolution and restores the prior bootstrap on
failure. Rollback restores the before-copy after checking both revisions.
Apply requires a maintenance window with the old core activations stopped;
Flox refuses mixed-version activation of the same environment. Stage leaves
those activations running and never applies the new bootstrap implicitly.

The script leaves the default Nix profile, Nix daemon and repository image assets
unchanged. It requires explicit operation on a running named VM. Keep the staged
closure, checksum/version evidence and cold backup until live acceptance passes.
For future VMs, perform this onboarding before enabling worker services; this
delivery deliberately preserves the existing image fingerprint.

## Prompt size failures

Worker and wake prompts have a 32,768-byte UTF-8 limit. The worker measures the
complete outgoing prompt after JSON escaping, framing and the daily-proposal
restriction. Required identity, approval, current goal/task, source message and
exact-head review facts are never truncated to fit.

Planning includes the selected full goal once and a compact index of other goals.
The index preserves URLs, repositories, versions and evaluated approval; it does
not authorize administration of those other goals in this turn. A `BodyRef` points
to the complete supplied goal body when a source repeats that exact text.
There are no optional background excerpts in this format. All included sections
share the final byte budget; excessive required context fails closed.

An oversized prompt is blocked as `prompt_size` before model submission, without
consuming an execution attempt or retrying unchanged input. `worker status` shows
`prompt_failure` for a blocked task. `worker requests --request-id ID` shows the
same diagnostic for a wake. Diagnostics contain the actual byte count, limit,
job/request ID, source Discussion/goal URL and per-section byte counts, without
quoted bodies or credential information. A marked report in the existing source
thread explains the failure once. Wake report retries have bounded backoff and
the configured attempt limit, independently of model execution attempts. Fresh
eligible requests take precedence over stale reports. Terminal size failures do
not consume pending queue capacity. Retrying a report does not retry the model.

Use the section counts to identify required context that needs to be split into
separate scoped inputs. Preserve the blocked job/receipt and its claims and
session evidence. Do not clear the ledger, replay an uncertain turn, weaken the
approved goal, or request redundant approval. A new eligible source revision or
distinct supported ping can derive normal new work. Authorization failures still
have their own scope/approval reasons; `prompt_build` identifies a different
prompt-construction failure.

The new reader accepts existing state and receipts. The optional `PromptFailure`
and `PromptReportAttempts` job fields are emitted only for size failures and
their wake report attempts. Previous strict readers
reject records containing it, so a rollback must retain a compatible reader or
reconcile/archive newer records explicitly. Never delete records to make an old
binary start. Wake receipts use the existing 16 MiB snapshot ceiling for lossless revalidated
sources, session identity and diagnostics, including JSON escape expansion; inbox requests remain limited to 32 KiB. Older readers
also reject receipts above their former limit. Deploy the worker and matching
status/request CLI together during an authorized maintenance window.
