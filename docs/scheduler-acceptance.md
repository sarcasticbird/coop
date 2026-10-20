# Controlled scheduler acceptance packet

## Reliability regression checks

Run `scripts/uat-master.sh` for offline master, worker, CLI, MCP and Team screen
acceptance. It uses disposable local fixtures and does not restart the birds.
`TestNativeProductionDeliveryEndToEnd` compares selected-task eligibility with
complete discovery across implementation, independent review, QA and delivery.
Existing exact-session, uncertain-write, dependency wait and quota-recovery
tests run through the selected-task validation path too.

The deterministic no-Project fixture in `TestNativeTaskSnapshotIgnoresUnrelatedWork`
uses 11 GitHub requests for the selected task with either 1 or 101 registered
tasks. Complete discovery of those 101 tasks uses 506 requests. These are fixture
counts, not a live latency benchmark. Selected validation reads Project membership
and values through each relevant issue's bounded `projectItems` connection.
`TestNativeTaskSnapshotTargetsProjectMembership` rejects a whole-board scan and
an inaccessible unrelated board item. Discovery and plan creation keep complete
catalog reads and own board ordering.

DraftIssue links and private cross-owner Projects keep the bounded Project-side
reader when issue memberships are absent or inaccessible. Repository reads use
the repository owner's installation. Quota/transport errors retain their retry
instead of triggering this compatibility fallback. Regression fixtures cover
both representations and cancellation of an unstarted delivery after a rebind.

The targeted membership fields follow GitHub's [Issue GraphQL reference](https://docs.github.com/en/graphql/reference/issues)
and [Project item reference](https://docs.github.com/en/graphql/reference/projects).
Schema introspection confirmed these read-only fields during implementation.

Diagnostics tests exercise engagement visibility, retired placement/epoch
reports, bounded metadata, independent sections, mixed versions, nonblocking
reporting and canceled/stale TUI reads. A successful report or heartbeat is not
proof of VM/provider health. Live acceptance and deployment need a separate
reviewed packet with the final source and artifact identities.

This is a preparation checklist, not an executable rollout authorization. Keep
the populated engagement packet in an authorized private destination. Do not put
customer inventories, actual identities, deployment topology, credential paths,
state snapshots or native history in this public document or PR prose.

Source acceptance, fixture authorization, permission changes, activation, live
state conversion, deployment and release are separate decisions. The current
engine requires explicitly initialized or converted v2 storage and a matching
wake fence. An authorized, independently reviewed activation change and a newly
pinned artifact set are prerequisites for any real v2 exercise. Do not patch a
binary locally, select v1 to bypass v2 policy, or use fixture tests as a live pass.

## Freeze the inputs

Before execution, require a private evidence record for every row. An unresolved
value is a blocker for its stage, not permission to guess. Record UTC timestamps,
executor, exact commands, exit status and redacted evidence links for each action.

| Record | Required contents |
| --- | --- |
| Authority | Approved goal/body version, task/dependencies, concrete fixture/permission/maintenance authorization and expiry/window |
| Permissions | Explicit before/after grants per actor and owner installation; preserve existing planner Project write access while limiting any proposed repository permission delta to its authorized scope |
| Source | Full integration and reviewed heads, required repairs, clean diff, independent exact-head approval and checks |
| Artifacts | CLI/worker/helper targets, SHA256 of every binary/launcher/installer/example, manifest digest, tool versions/build flags and retained byte locations |
| Profiles | Private before/after host and worker profiles, hashes, immutable actors/capabilities, assignment/policy digests, unchanged instance/state/work roots, service/autostart fence and credential references only |
| Environment | Exact OS/runtime/image revisions and digests, OpenCode schema/version, Flox lock digest, toolchain, architecture, authorized data/tools and network scope |
| Fixtures | Proposed private repository, goal, branch, Project card, author/peer/QA actors, acceptance digest, artifact source, environment revision and cleanup owner for every case |
| Recovery | Verified cold archive and restore proof, old artifact/profile generation, install/maintenance journal locations, exact instance, byte inventory and rollback decision owner |
| Outcome | Per-case expected/actual result, native review/claim/session/message IDs, current head/scope, limitations, cleanup evidence and independent conclusion |

Use the [artifact preparation contract](scheduler-artifacts.md) to create and
re-read the manifest from a clean committed head. It identifies bytes, not build
provenance. Keep its SHA256 separately. Verify received bytes again after transfer;
do not substitute a rebuild. Do not include private profiles in the public
manifest. Profile hashes and backup inventories belong in private evidence.

## Before and after profiles

Retain the exact installed v1 profile rather than reconstructing it from an
example. Stage a separate v2 candidate, never an in-place silent reinterpretation.
The neutral [v1](../examples/worker.json) and [v2](../examples/worker-team.json)
examples describe syntax, not an installation.

| Field | Before | Candidate after |
| --- | --- | --- |
| Authority | V1 role and fixed implementer/reviewer | V2 roster, planner and explicit independent peer mapping; no legacy role authority |
| Identity/storage | Saved actor, name, instance, state/work roots | Same existing actor/instance/storage for an upgraded worker; a first worker gets its own new identity/store |
| QA | Existing explicitly approved gates | Required only under owner-approved goal policy; separate QA-only actor without contents-write |
| Model | Capture actual configured provider/model/variant | Explicit approved preference, independently verified submitted and native executed identity |
| Services | Saved commands/restart settings and launchers | Matching verified artifact generation, no automatic start during maintenance |
| Credentials | Existing owner-specific references/renewal | Separate identities; no copied secret values, broadened installation scope or new renewal authority |
| Limits/history | Existing retry/cycle/turn limits, native sessions and queued input | Preserve limits and evidence; final prompt ceiling 32,768 bytes and default peer-hop limit two |

Offline syntax/compatibility checks use the matching built worker:

```sh
coop-worker validate --config /absolute/private/candidate.json
coop-worker check-install --config /absolute/private/candidate.json \
  --previous-config /absolute/private/installed.json
```

These are not runtime acceptance. `check-install` releases its locks before any
installation; the operator must maintain the external fence throughout.

## Acceptance exercises

All rows require separately authorized disposable private fixtures. Capture
actual bot identities, complete native review/contributor evidence, canonical
repo/task/goal/head/assignment, clean separate checkouts and native sessions.
No bot may submit another bot's conclusion. Health checks alone prove none of
these workflows.

| Case | Required result and evidence |
| --- | --- |
| Reciprocal engineering | Each engineer authors once, independent assigned peer formally reviews exact head; author fixes and delivers using expected-head protection; merged PR and completed issue both confirmed |
| Independence negatives | Self, outsider, QA-as-engineer, joint author/contributor, unresolved identity pagination and board-only reassignment never clear engineering |
| Customer QA cycle | After engineering/checks, QA actor reports changes-needed on a seeded defect; author fixes; new engineering review then new QA pass on the actually tested artifact; only then author delivery |
| QA blocked | Unavailable tools/environment, missing artifact, wrong native review state/actor, copied/edited marker or stale environment cannot pass; explicit blocked review and visible wait reason |
| Freshness | New head, retarget away/back, changed assignment/acceptance/environment, dismissed review and paused goal invalidate affected clearance; reapproval never inferred from old evidence |
| Repository boundary | Foreign repository/PR/task/goal links cannot dispatch, fix, review or deliver; positive same-repository control still succeeds |
| Project continuity | Either author's merged/completed delivery schedules one planning reconciliation; existing card becomes Done and only eligible dependency advances; historical failed jobs remain inspectable without reopening completed cards |
| Fair service | Continuous handoffs serve eligible oldest owner/planning work after two handoffs and coding after three foreground completions; counters/ages survive restart and blocked jobs do not consume service |
| Restart ambiguity | Interrupt around claim/checkpoint/possibly accepted POST; retain intent, claims, queue and receipts; reconcile without duplicate submission/merge or deleting evidence |
| Manual/compaction | Later manual turn remains untouched, one active owned turn, native history/automatic continuation ancestry preserved; missing/ambiguous lineage blocks instead of replaying |
| Models and wakes | Correct self/role/author/peer/QA and target in prompts; requested selection, persisted submitted identity and actual assistant metadata agree; outsider wake refused, peer hops bounded, owner reset/dedup survive restart |
| Roster extension | Fourth qualified synthetic engineer works by policy only; unknown actor and unqualified peer still fail closed |
| Native maintenance | Supported host backup/restore and stopped-install interruption recovery preserve complete generations, queue and native history; partial restore, missing fence and backup-only conversion evidence refuse unchanged |

For source QA, compare `coop-worker qa-source-digest --archive PATH` with the
job's canonical source-bundle digest, not the raw tar checksum. For binary or
workflow QA, download and test the exact native artifact ID/run/head/digest; a
locally rebuilt substitute is not acceptance. Record strict formal `coop-qa:v1`
result as the entire native exact-head review body on the code PR; keep detailed
supporting evidence in the authorized goal repository and link it from that
marker. See [QA contracts](workers.md#independent-customer-qa).
Linux fake transports and isolated process-kill tests do not establish real bot,
native macOS/Apple Container or physical power-loss acceptance.

## Ordered maintenance gates

The following sequence must be instantiated with private exact profiles, paths,
commands and authorizations before execution. Stop at the first failed gate.

1. Freeze reviewed source/artifacts and execution approval. Prove a disposable
   native restore first. Confirm the separate privacy/portability audit result
   and any restrictions; absence of a result is not a release-ready pass.
2. Stop new admission and fence all producers, old CLI copies, queued host pings,
   service/VM autostart and automatic renewal/start paths. Drain owned task/wake
   and manual native turns. Pause alone is not a drain or process fence.
3. Resolve active/preparing/recovery/uncertain records by observation, not deletion
   or replay. Inventory ledger/queue/receipts/claims/native history and remote
   effects. Any ambiguity blocks conversion.
4. Stop the VM and every user of its owned volumes; use supported cold `backup`.
   Retain complete old binaries/profiles/manifests and exact instance. Verify the
   archive; it excludes host mounts and credential tmpfs and may contain on-disk
   login data. Keep it private, never in source/logs/images or a fixture VM.
5. Only under authorized maintenance, start the same unshared VM with worker and
   server startup fenced. Install through the existing hardened installer, retain
   `install-pending/` and history, and recheck received artifact/profile hashes.
   Installation also publishes the candidate worker JSON, not the host Coop
   profile. Keep the service fence intact over the pre-conversion interval.
   Do not replace manifests or bypass a refusal. Finish a saved transaction before
   considering another incoming generation.
6. Use the explicit offline conversion only once on a drained compatible store:

   ```sh
   coop-worker migrate-state --config /absolute/private/candidate.json \
     --from 1 --to 2 --expected-instance EXACT_SAVED_INSTANCE_SHA256
   ```

   Preserve immutable `migration-backup.json`, hold and inbox fence; verify the
   complete byte inventory. Input limit is 128 MiB, encoded backup 256 MiB. Any
   existing evidence or uncertain publication requires inspection, not a retry.
7. Before start, require the separately reviewed activation change, refreshed
   artifact evidence and explicit permission/service authorization. Current
   pre-activation binaries cannot perform this step. Accept one actor before
   advancing to the next; never run two workers as the same identity. A first QA
   worker requires separately authorized `init-state` on unused storage before
   startup; it must not reuse or reinterpret another actor's ledger.
8. Execute the real acceptance matrix, compare history/queue/receipts, verify
   renewal and actual models, collect independent conclusions and cleanup proof.
   Do not call a source merge live acceptance or a fixture merge production release.

## Stop and rollback

Any lost fence, unexpected process/remote effect, identity or model mismatch,
dirty review checkout, invalid evidence, failed check, unsupported runtime,
resource/tool failure or scope change stops new admission. Preserve all artifacts
and record the blocker privately; do not reset attempts or clear history.

Before any conversion or newer effect, a verified complete compatible generation
may be restored only under separately authorized maintenance and the supported
archive/journal contract. After conversion, mixed generations, partial restore,
or newer ledger/native/GitHub effects, rollback is **blocked pending offline
reconciliation**. There is no reverse migration or automatic state restore.
Fence new workers/producers, preserve both generations, inventory external
effects and obtain a reviewed reconciliation plan before reactivation. Never
restore just `state.json`, discard receipts, remove the inbox fence or start an
old reader on v2 state. See [recovery boundaries](scheduler-artifacts.md#installation-and-recovery-boundaries).

Cleanup is evidence-preserving: close fixture work as test-only, reconcile its
Project cards, retain reviews/logs/digests under the approved private retention
policy, remove only authorized disposable resources after inspection, and prove
no worker/renewal process or fixture access remains active. Do not delete the
only evidence of an unknown outcome. Live permission changes, fixtures, migration,
activation, host moves, main merge, tags, package publication and release remain
outside preparation approval.
