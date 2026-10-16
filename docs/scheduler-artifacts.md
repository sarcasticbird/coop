# Scheduler artifact preparation

This public guide covers source and offline artifact preparation only. It does
not authorize installation, state conversion, profile changes, service reloads,
live bot handoffs, permission changes or external acceptance fixtures. V2 worker
startup remains fenced before state loading. A valid example, successful build
or verified artifact manifest does not remove that fence or approve live work.
The separately controlled private rollout packet is not reproduced here.

## Configuration and compatibility

[worker.json](../examples/worker.json) remains the v1 example.
[worker-team.json](../examples/worker-team.json) is a neutral v2 policy example,
not an installed team or deployment topology. All account IDs, logins, owner
scopes and model names are placeholders. Paths are generic product defaults;
the password path is not a signing-key or provider-credential location. Do not
put private identities, topology, credential paths or rollout evidence in these
public files.

| Contract | V1 | V2 |
| --- | --- | --- |
| Configuration | Fixed `role`, `implementer` and `reviewer`; `team` forbidden | Required version-2 `team`; legacy authority fields absent or empty |
| Identity | Fixed implementer/planner actor binding | `name` and immutable `self` identify one roster member |
| Assignment | V1 trusted task and PR metadata | V2 policy/body/assignment digests, independent assigned engineering peer and goal-required QA |
| Ledger | Reads/writes v1 only | Reads/writes v2 only; loading never converts v1 |
| Historical wake records | V1 record schema | Retained v1 record schema under `wake/v1`, gated by parent ledger/config version and instance fence |
| Conversion | Source for explicit offline conversion | One-way v1-to-v2 conversion only; no reverse migration or automatic restore |
| Runtime | Existing supported startup | Startup refused; offline validation and isolated fixtures are not live acceptance |

The example has a qualified planner, reciprocal independent engineering peers,
and a separate QA-capable member. Every implementing member needs an explicit
qualified peer other than itself. Capabilities do not grant GitHub permissions.
QA requirements come from the approved goal, not from the presence of a QA roster
member. Immutable IDs, not mutable logins or Project fields, bind authority.

For an offline syntax/policy check, copy the example to an absolute private file
with mode 0600 and run the matching locally built CLI:

```sh
coop-worker validate --config /absolute/private/worker-team.json
```

`validate` loads configuration without loading the ledger, contacting GitHub or
OpenCode, claiming work or dispatching a model. It may inspect the configured
password file's local permissions if that file exists. Validation does not prove
that placeholder accounts, scopes, models or permissions exist. Do not use
`preflight`, `dry-run` or `run` as substitutes for this offline example check.

## Build and digest evidence

Build from the reviewed source revision through the repository's Flox environment.
Record the exact commit and any source differences, toolchain versions, target
OS/architecture and build settings. The guest installer requires Linux arm64;
a host-native CLI used for offline validation is not the guest deployment binary.
Keep the worker and matching diagnostic, request and producer readers together.

The offline helper requires a clean committed checkout containing the observation
repair and its integration merge, plus a directory containing exactly `coop`
(Darwin arm64), `coop-worker` and `coop-github` (Linux arm64). It binds their
digests, the installer/launcher sources, itself and both public examples to the
exact source head. Keep generated files outside the checkout and binary directory:

```sh
flox activate -- python3 -B scripts/scheduler_artifacts.py create \
  --source-head FULL_COMMIT_SHA --artifacts /absolute/binaries \
  --manifest /absolute/evidence/manifest.json
flox activate -- python3 -B scripts/scheduler_artifacts.py verify \
  --source-head FULL_COMMIT_SHA --artifacts /absolute/binaries \
  --manifest /absolute/evidence/manifest.json
```

The helper checks binary headers but does not execute binaries or prove that
they were built from the recorded revision. Keep build and test evidence beside
the manifest. It refuses changed/missing inputs, dirty or index-hidden source,
wrong architectures and overwriting existing evidence.

Prepare SHA256 evidence for the exact worker binary, checked launcher scripts
and public example bytes, together with their source revision and build target.
Hash the completed manifest itself and retain that digest separately from the
manifest. Re-read the produced files and verify their digests before handoff;
after transfer, verify the received bytes against the same evidence. Rebuilding
from the same revision is not proof that the bytes are identical. Digests detect
byte changes; they do not independently establish provenance or authorization.

Keep private configuration and its digests/evidence in private storage. Do not
bundle credentials, native history, state snapshots or the private rollout packet
into a public artifact manifest. The installer's package checksum and private
`install.sha256` generation manifest are additional checks, not substitutes for
reviewed build evidence or a cold backup.

## Maintenance prerequisites

The following are requirements for a separately authorized maintenance window,
not permission to start one:

1. Fence every worker and wake producer, including old host/guest CLI copies and
   queued pings. Local pause alone does not stop a process or release its lock.
2. Drain worker-owned task and wake turns and all manual native sessions. Resolve
   active, preparing, recovery and uncertain submission evidence without clearing
   claims, replaying input or deleting history. A local ledger cannot prove that
   manual sessions or remote effects are drained.
3. Stop services before replacing artifacts. For a cold backup, stop the VM and
   all users of its Coop-owned volumes for the entire copy. Avoid external runtime
   operations, concurrent maintenance and host disk writes.
4. Retain and verify a private cold backup, saved pre-change binary/configuration,
   launcher generation, install manifests and exact saved worker `InstanceID`.
   Check archive completeness and checksums using the supported backup/restore
   contract. File existence or a digest alone does not prove recoverability.
5. Keep compatible binaries/readers and all maintenance evidence available until
   separately authorized acceptance is complete. Do not start services after a
   failed installation or ambiguous conversion.

See [cold backup and restore](agents.md#maintenance-and-recovery) and
[maintenance recovery](runtime.md#recovery-without-deleting-state). Backups include
root disks and Coop-owned volumes, not host mounts or credential tmpfs. Native
login stores and on-disk secrets can be included. Archives are mode 0600 but not
encrypted. Backup rechecks detect many concurrent changes, not all possible
writes or a hidden start/stop cycle; checksums prove copied bytes, not a consistent
point-in-time image in the presence of concurrent writers.

## Installation and recovery boundaries

The [worker installer contract](workers.md#runtime-and-private-configuration)
requires an already-running unshared VM, even though worker/server services must
stay stopped. A cold backup therefore precedes any separately authorized
maintenance-only VM start. Ensure profiles cannot restart services inadvertently.
The installer stages artifacts; it does not stop/start services or change profiles.

The candidate worker's offline `check-install` command checks the proposed and
installed configuration identity, immutable actor, state directory and version,
and the existing ledger/wake readers under nonblocking locks. It allows staging
a v2 profile over intact v1 state before explicit conversion, but rejects a v1
profile over v2 state, version downgrade, partial wake storage and pending
migration evidence. It never migrates or saves a ledger. These locks are released
before file replacement: this point-in-time check does not fence a later start
or establish that manual sessions are drained. The operator's stopped-state
prerequisites remain mandatory. An older candidate lacking this command is
refused rather than installed without a compatibility check.

Installation is serialized and publishes durable before/after recovery evidence
before replacing live files. A retry verifies the saved transaction and all live
files against its expected generations before finishing it. Independent content,
mode, ownership or symlink changes must cause refusal, not silent adoption or
overwrite. Preserve `install-pending/`, retained `install-history.*/generation/`,
`install-before/` and manifests. Never edit or delete them to bypass a refusal.
Files span directories and are not exposed as one atomic set; filesystem syncs
and isolated interruption tests do not prove native storage power-loss behavior.

For interrupted Coop maintenance, keep the VM stopped and the destination
configuration unchanged. Supported recovery reuses the original recovery archive
and durable journal, rejecting foreign disks or changed configuration/archive.
Do not delete the journal, replace its disks or start the runtime externally.
A restore into a new destination requires that destination to be absent and
validates the full archive before registering it. An interrupted restore can leave
a partial target; preserve and inspect it rather than silently overwriting or
adopting it as a complete restore.

The [offline migration contract](workers.md#offline-state-compatibility) requires
the exact saved instance, exclusive worker/producer locks and a validated v2
configuration. It verifies the whole managed store, rereads an immutable
`migration-backup.json`, publishes a `migration.json` inspection hold and the
instance-bound `wake/v1/inbox/version.json` fence, then verifies the result. Input
is bounded to 128 MiB and the encoded backup to 256 MiB. Conversion preserves old
records and queued input; it does not grant old jobs v2 assignments or clearances.

There is no automatic migration retry, reverse conversion or backup restore.
Existing migration evidence causes refusal, including a backup left before hold
publication. A failure after verified publication can be ambiguous success:
inspect the bytes, marker and fence, do not rerun blindly. Never remove the fence
to admit an old producer. Old control/status commands may not read the ledger,
so ledger refusal alone is not a complete producer fence.

Partial restore, mixed artifact/state generations, or newer ledger/native/remote
effects are not safe rollback points. Refuse a rollback that would overwrite new
state with an old snapshot. Preserve both generations and require separately
reviewed offline reconciliation of the ledger, receipts, native sessions, claims
and remote effects. Do not restore only `state.json`, discard newer wake receipts
or replace native history to make an old binary start. A cold archive and the
migration byte backup serve different purposes; neither authorizes replay.

## Native history source repair

The source repair tracked as #75 retains the bounded OpenCode 1.18.18 ownership
contract described in [automatic compaction](workers.md#automatic-conversation-compaction).
Saved-message and history responses have a separate 16 MiB **wire** ceiling,
including diff summaries and tool output. History expands from 20 to at most 320
messages in three bounded requests. Only ownership metadata and the boolean
compaction-summary marker are retained; diff-summary contents are discarded.
Only JSON boolean `true` marks a compaction summary; an object-valued diff
summary, a string `"true"`, `false` or `null` does not.
This is not an unbounded response allowance or a larger prompt budget. Other
OpenCode responses and GitHub pages retain their 2 MiB limits.

A summary or idle session is not completion. Only the marked continuation's actual
terminal response can complete the saved native turn, followed by normal artifact
reconciliation. Oversized responses, missing/ambiguous lineage and unmarked
continuations retain bounded recovery behavior, not permission to replay prompts,
abort manual turns or reset ownership evidence. Include the matching repaired
worker and readers in artifact evidence; source preparation does not establish
live recovery acceptance.
