# Retained Delivery Activation

Operator packet for the capability stack following PR #161 at
`e55da25979087dfb16ef23226263435c14c45fe9`, under approved implementation goal
`goal_65JPPPOTVKYAI7EX7XOVX5OQCJ`, revision
`b1d627ef0544cc0a61b0d75474520506071cb08f4024cd40da64c213eddcb6ae`,
approval sequence **2**. The predecessor SHA is **not** the final capability
build SHA. The native publication receipt supplies the exact head and bundle;
fresh Robin review, Wren QA and matching CI remain separate gates. This document supplies
none of those receipts and grants no install, restart, configuration or delivery
authority. PR #158/#159 remain separate external-owner dispositions, not native
retained candidates.

## Pins and Privacy

Keep the implementation goal distinct from the historical source goal. Never
rebind, recreate, reopen or relabel the source to acquire today's policy. Native
task/PR registration, source assignment and original evidence stay unchanged.
The bridge supplements that history with exact owner decisions and renewed gates.

The actual PR9 fixture identifies these source pins (not activation authority):

| Pin | Value |
| --- | --- |
| Master | `coop-master-20261031` |
| Source goal | `goal_A72JLAJEXNT2TOEXHXCRWZGXJU` |
| Source revision | `94c8f3482a88ed3cb1a09f241ba8b28ecf7151e9aa7e4f284b0735d56ca5cdb5` |
| Source approval / base branch | `1` / `main` |
| Source task | `kata://01m69m973pwdtsmg3gyrqm6r74/01M69NJWQXXBT2PQWX307ZFPFH/sarcasticbird/gh-sweep` |
| Source PR | `https://github.com/sarcasticbird/gh-sweep/pull/9` |
| Assignment | `49d021b8e3bbb8ed034ff3abc8a90d066b7ada6098c64a2f8ef01cfa022f1384` |
| Task scope | `d61fa88d444ab3c5b662149aa47f8ca6935f196367f5d8c4687dc908c382b9fb` |
| Source policy | `05bf02a92869ae26d8d8f5bdcab01523703ca824b592b314a79ea993d2a525eb` |
| Reviewed head | `6a3efb78904ea8e1ff47af2d85b4e772eec190c0` |
| Original base | `de9b32cace62e9a97b3ed86428fc17bc62338993` |
| Source tree | `88a6d8efe9db403584a8bf69bac874b41c926614` |
| Canonical source bundle | `9e4e061ebafbf3d2f94c9d46561dd55f00ad9dc902665a8dc850b616c9fda4c0` |
| Original private QA message | `msg_NNGYSQTALFDVNOKZIB3WTJIUI5` |
| Original private QA body SHA-256 | `1b5a21bc8339e204e10ff3cd998980e6ca96fcd6b308aa6633653c93cd9da992` |

The original QA and goal records remain in an authorized private fixture outside
the repository. Never copy its bodies, full JSON, transcripts or captured responses
into public docs/PRs/logs. Reference IDs and body digests only. Set
`COOP_PR9_PRIVATE_FIXTURE` to that private file for the exact-original tests.
Without it those tests explicitly skip; public CI does not prove the private
evidence journey. The public fixture contains only its README and source archive.

## Build and Schema

Use an isolated checkout of the final accepted head, existing private staging
directories, and the configured Flox environment. Record source/worktree identity,
target OS/architecture, tool versions, commands, results and output hashes. The
specified verification toolchain is Go **1.26.10**, Flox **1.18.1**, GCC
**15.3.0**, golangci-lint **2.12.2** (Python **3.14.7**, govulncheck **1.6.0**).
The manifest currently selects Go `^1.26.8`; `go.mod` requires 1.26.10, obtained
through Go's automatic toolchain selection until the catalog catches up. Verify
effective versions, not just the manifest.

```sh
git rev-parse HEAD
git status --short
flox --version
flox activate -- go version
flox activate -- gcc --version
flox activate -- golangci-lint version
flox activate -- go build -trimpath -o /private/staging/coop ./cmd/coop
flox activate -- env GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o /private/staging/coop-worker ./cmd/coop-worker
sha256sum /private/staging/coop /private/staging/coop-worker
flox activate -- go version -m /private/staging/coop
flox activate -- go version -m /private/staging/coop-worker
/private/staging/coop-worker qa-source-digest --archive /absolute/private/source.tar.gz
```

The first binary includes master and CLI and targets the build host. Build it
for the actual master/CLI platform; Linux arm64 worker output is not a macOS
host binary. Installation is a separate operator step using the applicable
repository installer/service manager and explicit artifact/config paths.
`qa-source-digest` computes canonical `source-bundle-v1`, not the compressed
tarball's SHA-256. Use the read-only native digest command for normalized payload
and handoff input hashes, including native zero-valued struct serialization:

```sh
/private/staging/coop-worker native authority-digests --input /absolute/private/source-delivery-input.json
/private/staging/coop-worker native authority-digests --archive /absolute/private/source.tar.gz --payload-schema gh-sweep-version-v1
```

See [Authority Digests](native-authority-digests.md). Do not substitute a generic
JSON canonicalizer or hash the handoff envelope instead of its `input`.

There is **no retained-delivery SQL migration command**. `coop master serve
--config /absolute/private/master.toml` opens the store and initializes schemas
through `OpenStore.initialize`; retained/tag-only authority uses JSON in the
existing `handoffs` table, monotonic `sequence`, native `requests` completion
receipts and `artifacts` registrations. Do not insert rows or fabricate completion
proofs. Preserve a consistent master backup plus independent epoch lineage before
an authorized service update:

```sh
coop --master-config /absolute/private/owner.toml master backup --directory /absolute/private/empty-backup
coop --master-config /absolute/private/owner.toml master reconcile status
```

Worker config/state remains v3; `master/v1` is a compatibility directory name,
not evidence of v1 worker state. This capability is not a reason to run
`migrate-state`. An actual legacy conversion requires the separate procedure in
[Master](master.md#explicit-worker-conversion). Preserve config, state, epoch,
placement and outstanding journals; never reset state to make admission succeed.

## Task Creation

Before creating **new bounded implementation work**, an authorized planner reads
the full approved implementation goal/revision, sequence 2, provider and predecessor:

```sh
coop --master-config /absolute/private/planner.toml goal authority IMPLEMENTATION_GOAL --revision IMPLEMENTATION_REVISION_SHA256
coop --master-config /absolute/private/planner.toml task capabilities
coop --master-config /absolute/private/planner.toml task read PREDECESSOR_TASK_REFERENCE
coop --master-config /absolute/private/planner.toml task list --goal IMPLEMENTATION_GOAL --revision IMPLEMENTATION_REVISION_SHA256
coop --master-config /absolute/private/planner.toml task create --file /absolute/private/create.json
```

Representative `create.json`; replace placeholders from current native authority:

```json
{
  "goal": {"source":"master","master_id":"MASTER_ID","goal_id":"IMPLEMENTATION_GOAL","revision_sha256":"IMPLEMENTATION_REVISION_SHA256"},
  "expected_approval_sequence": 2,
  "repository": "sarcasticbird/coop",
  "base_branch": "agent/hawk/e0642638a17acc68",
  "title": "Bounded approved capability work",
  "body": "Approved acceptance and exact predecessor PR #161 at e55da25979087dfb16ef23226263435c14c45fe9; no runtime activation.",
  "depends_on": [],
  "request_id": "stable-task-create-request"
}
```

`base_branch` is the supported creation pin; there is no `base_sha` or PR-number
creation flag. Preserve the predecessor commit in the approved task scope and
verify the eventual native PR/base and delivery `base_sha` separately. Dependencies
must share the exact goal revision; a PR URL is not a task dependency. Do not
duplicate existing admitted work. On retry reuse identical payload/request ID.
PR161 is an immutable predecessor input, not a cross-revision native dependency.
Recheck its remote branch is exactly `e55da25979087dfb16ef23226263435c14c45fe9`
before admission and publication effects; drift requires reconciliation.
The PR9 incremental test uses its historical goal approval **1**, not implementation
approval 2, and does not authorize cross-goal dependency adoption.

## Retained Decisions

All uppercase values below are sanitized placeholders, not executable hashes.
Use lowercase 64-hex SHA-256 and 40-hex Git object IDs; copy native digests, not
hashes of reformatted JSON. Review IDs are native IDs, not GraphQL node labels.
Body digests hash exact body bytes. Keep actual files private (0600, private parent
directories); do not print bodies during preparation.

After compatible runtime installation/verification, the authenticated owner with
current read/decide authority records a non-imported private decision. Only then
submit this complete representative `retained-gates` input:

```json
{
  "kind": "retained-gates",
  "goal": {"source":"master","master_id":"MASTER_ID","goal_id":"SOURCE_GOAL","revision_sha256":"SOURCE_REVISION_SHA256"},
  "task_url": "ORIGINAL_CANONICAL_TASK_REFERENCE",
  "pr_url": "ORIGINAL_NATIVE_PR_URL",
  "assignment_sha256": "ORIGINAL_ASSIGNMENT_SHA256",
  "head_sha": "ORIGINAL_HEAD_40HEX",
  "artifact_sha256": "EXACT_QA_ARTIFACT_SHA256",
  "environment": "CURRENT_APPROVED_ENVIRONMENT",
  "environment_revision": "CURRENT_APPROVED_ENVIRONMENT_REVISION",
  "evidence_message_id": "NATIVE_OWNER_GATE_DECISION_MESSAGE_ID",
  "request_id": "stable-retained-gates-request",
  "approval_sequence": 1,
  "task_scope_sha256": "ORIGINAL_TASK_SCOPE_SHA256",
  "base_branch": "ORIGINAL_APPROVED_BASE_BRANCH",
  "base_sha": "EXACT_PREMERGE_BASE_40HEX",
  "pr_scope_revision": "EXACT_NATIVE_PR_SCOPE_SHA256",
  "acceptance_sha256": "CURRENT_SOURCE_GOAL_ACCEPTANCE_SHA256",
  "peer_review_id": "ORIGINAL_NATIVE_PEER_REVIEW_ID",
  "qa_review_id": "ORIGINAL_NATIVE_QA_REVIEW_ID",
  "qa_evidence_message_id": "ORIGINAL_PRIVATE_QA_MESSAGE_ID",
  "qa_evidence_body_sha256": "ORIGINAL_PRIVATE_QA_BODY_SHA256",
  "revoked": false,
  "retained": {
    "source_policy_sha256": "ORIGINAL_BINDING_POLICY_SHA256",
    "execution_policy_sha256": "CURRENT_EXACT_ROSTER_POLICY_SHA256",
    "source_tree_sha": "ORIGINAL_TREE_40HEX",
    "payload_sha256": "ORIGINAL_SOURCE_BUNDLE_SHA256",
    "source_peer_review_id": "ORIGINAL_NATIVE_PEER_REVIEW_ID",
    "source_peer_body_sha256": "ORIGINAL_PEER_BODY_SHA256",
    "source_qa_review_id": "ORIGINAL_NATIVE_QA_REVIEW_ID",
    "source_qa_body_sha256": "ORIGINAL_QA_REVIEW_BODY_SHA256",
    "source_report_id": "ORIGINAL_PRIVATE_QA_MESSAGE_ID",
    "source_report_body_sha256": "ORIGINAL_PRIVATE_QA_BODY_SHA256",
    "source_environment": "ORIGINAL_QA_ENVIRONMENT",
    "source_environment_revision": "ORIGINAL_QA_ENVIRONMENT_REVISION",
    "delivery_acceptance_sha256": "CURRENT_SOURCE_GOAL_ACCEPTANCE_SHA256",
    "override": "stop-before-merge:exact-retained-delivery"
  }
}
```

```sh
coop --master-config /absolute/private/owner.toml task authorize-delivery --file /absolute/private/retained-gates.json
coop --master-config /absolute/private/owner.toml task handoffs --goal SOURCE_GOAL --revision SOURCE_REVISION_SHA256
```

The command preserves explicit `retained-gates`/`retained-deliver` kinds. Omit
`review_id`, `tag`, `workflow`, `tag_only` and report fields for gates. Source
peer/QA/report IDs must match top-level references. Source environment may differ
from current approved QA only with the required native historical environment
handoff; prose is insufficient. The execution policy is today's exact shared
roster SHA, not the historical source policy SHA or the binary SHA.

Native admission assigns Hawk `delivery-review` / `engineering-review`, and Wren
`delivery-qa` / `customer-qa`, each with exact executor, gate handoff and
`input_revision: retained:HANDOFF_ID`. Both independently assess applicability of
**all original assertions**, unchanged tree/payload/head, current acceptance and
environment. Neither rewrites original reviews/QA or posts a new GitHub review.
Only in the assigned job, each uses the prompt's helper/config/job IDs:

```sh
/usr/local/libexec/coop-worker native action --config /absolute/private/worker.json --job EXACT_ASSIGNED_GATE_JOB --input /absolute/private/readiness.json
```

```json
{"operation":"delivery-readiness","request_id":"stable-assigned-gate-result","verdict":"ready"}
```

Use `blocked` instead of `ready` on failure. The helper emits the private
`RetainedGateReport` with `gate_handoff_id`, `assignment_sha256`, exact gate input
as `binding`, and `result`; retain its message ID/body digest. It does not accept
caller-authored binding metadata as authority.

For **retained-deliver**, start with the exact gates payload above. Change only
`kind` to `retained-deliver`, `request_id` to a new stable decision identity,
`evidence_message_id` to the separate native owner delivery decision, and add
these four fields inside `retained`:

```json
{
  "engineering_report_id": "NATIVE_HAWK_READINESS_MESSAGE_ID",
  "engineering_report_body_sha256": "NATIVE_HAWK_READINESS_BODY_SHA256",
  "qa_report_id": "NATIVE_WREN_READINESS_MESSAGE_ID",
  "qa_report_body_sha256": "NATIVE_WREN_READINESS_BODY_SHA256"
}
```

Submit via the same `task authorize-delivery --file` command. Master selects the
latest gates before checking them: revoked/drifted gates do not fall back. Both
non-imported ready reports must bind that exact gate decision. Rook alone receives
the native `deliver` job (`delivery` role, bot **345406391**); its assigned action
is `{"operation":"deliver","request_id":"stable-assigned-delivery"}` through
`native action` above. Fresh head/base, CI/check state, contributor independence,
scope, placement and authority are checked again before effects. A stored grant
is not merge or completion evidence.

## Tag-Only Follow-Up

Tag-only is a separate owner decision after source native delivery and original
task completion, not permission to merge again. Retain the source handoff ID and
native serialized input digest, plus the **master task-complete request ID**.
For a production `deliver` action this ID is
`native-complete-` + SHA-256 of `jobID + actionRequestID` (no separator), not the
action's request ID alone. Master requires Rook's accepted `task-complete` receipt
for the exact goal/task/assignment/PR/delivery handoff. Closed/done provider state
or a merged PR alone is not proof; no backfill operation is supplied.

Construct the tag owner input from the exact source `deliver`/`retained-deliver`
input. Preserve **every** original top-level artifact/scope/approval/review/QA/base
field. Change `kind` to `tag-only`, supply the exact `vMAJOR.MINOR.PATCH` `tag`, new
owner `evidence_message_id` and stable `request_id`; remove `retained` and add the
following complete representative `tag_only` object. Omit `workflow`/`review_id`.
For direct-source tagging omit every optional version field shown below.

```json
{
  "source_completion_request_id": "ACCEPTED_SOURCE_MASTER_COMPLETION_REQUEST_ID",
  "source_delivery_handoff_id": "NATIVE_SOURCE_DELIVERY_HANDOFF_ID",
  "source_delivery_input_sha256": "NATIVE_SOURCE_DELIVERY_INPUT_SHA256",
  "tag_acceptance_sha256": "CURRENT_TAG_ACCEPTANCE_SHA256",
  "tag_environment": "CURRENT_APPROVED_TAG_ENVIRONMENT",
  "tag_environment_revision": "CURRENT_APPROVED_TAG_ENVIRONMENT_REVISION",
  "execution_policy_sha256": "CURRENT_EXACT_ROSTER_POLICY_SHA256",
  "source_policy_sha256": "ORIGINAL_BINDING_POLICY_SHA256",
  "source_commit_sha": "ORIGINAL_REVIEWED_HEAD_40HEX",
  "source_tree_sha": "ORIGINAL_REVIEWED_TREE_40HEX",
  "integrated_commit_sha": "EXACT_SOURCE_SQUASH_COMMIT_40HEX",
  "integrated_tree_sha": "ORIGINAL_REVIEWED_TREE_40HEX",
  "owner_body_sha256": "NEW_NATIVE_TAG_OWNER_MESSAGE_BODY_SHA256",
  "peer_body_sha256": "ORIGINAL_PEER_REVIEW_BODY_SHA256",
  "qa_body_sha256": "ORIGINAL_QA_REVIEW_BODY_SHA256",
  "engineering_report_id": "RENEWED_NATIVE_HAWK_TAG_REPORT_ID",
  "engineering_report_body_sha256": "RENEWED_NATIVE_HAWK_TAG_REPORT_BODY_SHA256",
  "qa_report_id": "RENEWED_NATIVE_WREN_TAG_REPORT_ID",
  "qa_report_body_sha256": "RENEWED_NATIVE_WREN_TAG_REPORT_BODY_SHA256",
  "version_completion_request_id": "ACCEPTED_VERSION_MASTER_COMPLETION_REQUEST_ID",
  "payload_schema": "gh-sweep-version-v1",
  "payload_sha256": "NORMALIZED_FIXED_CONTRACT_PAYLOAD_SHA256",
  "source_bundle_sha256": "EXACT_ORIGINAL_SOURCE_BUNDLE_SHA256",
  "version_bundle_sha256": "EXACT_REVIEWED_VERSION_SOURCE_BUNDLE_SHA256",
  "version_pr_url": "SEPARATE_NATIVE_VERSION_PR_URL",
  "version_head_sha": "REVIEWED_VERSION_HEAD_40HEX",
  "version_commit_sha": "FINAL_VERSION_SQUASH_COMMIT_40HEX",
  "version_tree_sha": "REVIEWED_AND_INTEGRATED_VERSION_TREE_40HEX",
  "version_base_sha": "EXACT_VERSION_SQUASH_PARENT_40HEX",
  "version_goal": {"source":"master","master_id":"SAME_MASTER_ID","goal_id":"VERSION_GOAL","revision_sha256":"EXACT_VERSION_GOAL_REVISION_SHA256"},
  "version_task_url": "SEPARATE_NATIVE_VERSION_TASK_REFERENCE",
  "version_delivery_handoff_id": "NATIVE_VERSION_DELIVER_HANDOFF_ID",
  "version_delivery_input_sha256": "NATIVE_VERSION_DELIVER_INPUT_SHA256"
}
```

The optional version fields are all-or-nothing. Version work is a separately
registered current-policy task/PR, independently reviewed/QA-cleared and delivered
through ordinary native `deliver`, with its own accepted completion receipt and
current authority. It must be in the same repository/base branch, distinct from
the source PR. Its base descends from source integration; its final squash has
the exact granted single parent and remains an ancestor of the base branch.

`coop-go-version-v1` permits only the single verified `var version = "dev"` or
stable-semver literal in `cmd/coop/main.go` to normalize. Version head/integration
must contain the requested tag. AST validation rejects fake/duplicate declarations.
Everything else, including that file's other bytes/newlines, paths, modes, file
types, symlink targets, executable/library/build inputs, additions and removals,
remains in the digest. Four archives are checked: reviewed source, its integration,
reviewed version, its integration. Each reviewed/integrated pair must also have
equal full `source-bundle-v1` digests. No arbitrary exclusions, caller schema,
archive extraction or build execution is supported. This is a **source payload**
contract, not proof of reproducible binaries or released-artifact QA.
For gh-sweep use `gh-sweep-version-v1`: only the literal in the actual fixed
header's `VERSION="0.4.0"` assignment may normalize. A tag `vX.Y.Z` requires
the version sources to contain `VERSION="X.Y.Z"`. All other executable/library
and archive bytes, paths, modes and links stay exact. Additional assignment
tokens, including comments, heredocs or functions, fail closed. This contract
is tested against the actual PR9 archive; arbitrary exclusions are unsupported.

Renewed tag reports are native non-imported Hawk/Wren messages in the configured
private QA thread, shaped `{"binding": TAG_REPORT_BINDING, "result":"ready"}`.
`TagOnlyReportBinding` sets `request_id` to `tag-only-report`, `revoked` false and
clears only the four tag report ID/digest fields to avoid a cycle; all other fields
remain exact. Retained readiness reports are not interchangeable with these
reports. There is no dedicated tag-readiness action in the current CLI: use only
the separately authorized native private-message stage, not forged agent bodies.

```sh
coop --master-config /absolute/private/owner.toml task authorize-tag-only --file /absolute/private/tag-only.json
```

The assigned Rook `tag-only` job has delivery role, exact executor and handoff.
Use `native action --config ... --job ... --input ...` with **only**:

```json
{"operation":"tag-only","request_id":"stable-assigned-tag-action"}
```

The helper derives target and annotation. Direct-source target is the verified
integration commit; versioned target is the **final integrated version commit**,
never the reviewed head or old integration. Master validation and worker guards
run before object and ref effects. Existing refs or equal objects without this
intent conflict; there is no tag move/delete, merge, task reopen, release, asset,
workflow, tap update, install, deployment or restart operation in this grant.
Creating a tag can nevertheless trigger repository automation: inspect triggers
before granting. If external workflows would run, do not treat tag-only as their
authorization; retain the hold until separately authorized or prevented.

## Activation Stages

1. **Hold and inventory.** Preserve `delivery_hold: true`, pause new admission,
   inventory active/uncertain actions and back up state/config/master lineage.
   Do not abort unknown writes or infer terminal status from a heartbeat.
2. **Offline canary.** In an isolated private checkout supply the actual PR9
    fixture companion through `COOP_PR9_PRIVATE_FIXTURE` and run the contract tests
   below with no live credentials/transport. The PR9 journey uses a disposable
   real master HTTP/SQLite and owner CLI, production hashes/actions, simulated
   GitHub/provider effects and one simulated exact merge/completion. It is not
   an external canary and must make **no external writes**. Do not regenerate
   evidence or substitute source to make it pass.
3. **Install while held.** Only an independently authorized operator installs
   accepted compatible master/CLI/workers and config, preserving state and
   placement. Keep the master/provider and worker roster policy identical.
   Only Rook has `deliver`; Hawk has engineering-review and Wren customer-qa,
   neither a source contributor. Current engagement/thread membership and
   permissions must cover owner, original actors, renewed gates and Rook.
4. **Attest effective runtime.** Verify actual running executable identity/hash,
   installed config digest, epoch/placement, current roster digest and credentials.
   Then record fresh owner gates decisions and admit only the bounded assigned
   gate jobs. **Code checks `delivery_hold` for retained gates too**: there is no
   gates-only bypass while it is true. Any gate pilot needs a separately authorized
   controlled hold change after runtime verification, no active deliver/tag grant,
   and re-holding before the next decision. Do not quietly clear it in a rollout.
5. **Delivery then tag.** After exact native gate receipts, revalidate source and
   record a separate owner retained-deliver decision. Admit only Rook under a
   separately approved hold change; verify merge and native task completion,
   then re-hold. Tag-only requires another current owner decision and renewed tag
   reports/completion chain. End each stage held until the next explicit decision.

The worker/provider roster entry uses the existing actor, not a new identity:

```json
{"name":"rook","actor":{"id":345406391,"login":"sarcastic-bird-rook[bot]","kind":"Bot"},"capabilities":["plan","deliver"]}
```

Keep `plan` only if already separately granted. Mirror the exact authorized roster
in the provider and participating workers; no author/review/QA capability belongs
on Rook. Master principal grants and thread membership are separate operator-owned
configuration, not acquired by editing a task, roster example or this document.

Supported inspection/control commands (config paths and IDs are operator supplied):

```sh
/absolute/installed/coop --version
/usr/local/libexec/coop-worker validate --config /absolute/private/worker.json
/usr/local/libexec/coop-worker check-install --config /absolute/private/candidate.json --previous-config /absolute/private/installed.json
/usr/local/libexec/coop-worker status --config /absolute/private/worker.json
/usr/local/libexec/coop-worker native preflight --config /absolute/private/worker.json
coop --master-config /absolute/private/operator.toml team inspect rook
coop --master-config /absolute/private/operator.toml task capabilities
/usr/local/libexec/coop-worker pause --admission-only --config /absolute/private/worker.json
/usr/local/libexec/coop-worker pause --config /absolute/private/worker.json
```

`native dry-run` is the same preview as `native preflight`: neither is a canary
execution or permission to act. `validate` checks configuration; `check-install`
checks compatibility. `team inspect` reports observations/placement/staleness, not
binary hash or build version (`report.version` is diagnostics schema **1**).
The worker has **no `--version` command**. `coop --version` may say `dev` and
cannot attest the service process. On Linux the operator can hash
`/proc/VERIFIED_SERVICE_PID/exe` and compare to staging; use the platform service
manager's actual PID/path on the master host and retain process-start/install
evidence plus `go version -m`. Hashing a path alone does not prove the running
process loaded those bytes. No universal runtime-attestation CLI is supplied.

## Recovery and Rollback

Tag intents live at `STATE_DIR/master/v1/tag-only/SHA256(handoffID).json`; action
receipts at `master/v1/actions/SHA256(jobID + NUL + requestID).json`, native
executions under `master/v1/engineering`. Preserve private modes and exact master,
placement, executor, handoff, job, request and input-digest lineage. Annotation
name/email/time/message and expected Git object ID are fixed before effects.
The journal advances phases but may not be adopted by a new request or placement.

Lost acknowledgment means **observe**, never replay POST or change request ID:

```sh
/usr/local/libexec/coop-worker native observe-tag-only --config /absolute/private/rook.json --goal EXACT_SOURCE_GOAL --revision EXACT_SOURCE_REVISION_SHA256 --handoff EXACT_TAG_HANDOFF_ID
```

This command performs only reads, with no local or remote writes, even after
goal pause, grant withdrawal or delivery hold. It still
requires authenticated Rook, matching placement/engagement and repository scope.
`object-intent`/`object-created` yields `checkpoint`, never starts a ref.
`ref-intent` returns `observed` after exact annotated object/ref readback, not
native completion. Only the same original assigned action/request can confirm
after fresh authority, roster, holds, evidence and integrated commit/tree checks,
without another object/ref write.
`ref-rejected` remains conflict. Wrong annotation/target/lightweight/foreign refs
cannot become a successful receipt. Object-only uncertainty has no automated
resume-to-ref operation; preserve it for operator disposition.

For an acknowledged retained merge with missing terminal result, inspect first;
the supported `native recover-delivery --config ... --job ... --session ...
--message ... --input ...` requires exact saved terminal execution and original
action payload. It is not the tag observer or general blind retry, and must not
repeat an already acknowledged merge.

Rollback ordering is mandatory:

1. Hold delivery and pause admission/execution through approved controls; capture
   current journals and external state. A local producer lock is not a remote fence.
2. **Before downgrading**, while the compatible API remains available, the owner
   submits exact withdrawals for affected `retained-gates`, `retained-deliver`
   and `tag-only` grants: copy the recorded input, change only `revoked` to true
   and `request_id` to a new stable withdrawal ID; use its original authorize
   command. Confirm higher-sequence persisted handoffs via `task handoffs`.
   Withdrawal does not depend on current execution eligibility, but still needs
    live owner authority and historical thread membership, not source execution
    eligibility.
3. Preserve withdrawal receipts, database/epoch lineage and all action/object/ref
   journals across the separately authorized artifact/config rollback. Do not
   restore a pre-withdrawal database or erase uncertain intents. Check older
   binaries against persisted kinds before using them; unknown authority is a
   reason to remain held, not a reason to edit rows.
4. Verify effective known-good runtime/config/placement with delivery still held.
   Observe uncertain remote results without new writes. Binary rollback undoes
   neither a merge nor a tag. No deletion/move cleanup is authorized. Resumption
   requires new explicit compatible authority and checks; old grants must never
   resurrect. The tag's repository/tag reservation persists after withdrawal,
   so a new request cannot simply reclaim the same tag name.

## Contract-to-Test Map

These are exact current test names, not claims that they ran in this docs-only
session. Paths are relative to `internal/`; fixture capture is deliberately not
part of the offline acceptance run.

| Contract | Package and tests |
| --- | --- |
| Same-goal incremental creation, exact predecessor; no cross-revision replacement | `worker`: `TestRetainedPR9IncrementalTaskCreatePinsOriginalPredecessor` |
| Actual PR9 archive, owner CLI/master HTTP, independent gates, original merge/completion | `worker`: `TestRetainedPR9ExactMasterWorkerCLIJourney`, `TestRetainedPR9ExactAuthorityDenials`, `TestRetainedPR9ExactAcknowledgedMergeRecovery` |
| No ordinary-delivery bridge or source-author gate impersonation | `worker`: `TestRetainedPR9OrdinaryDeliveryDoesNotInheritBridge`, `TestRetainedPR9SourceAuthorCannotPublishRenewedGate` |
| Immutable binding, drift, revocation despite ineligibility | `master`: `TestRetainedDeliveryPreservesSourceBinding`, `TestRetainedAuthorityValidation`, `TestRetainedDeliveryDriftAndRevocation`, `TestRetainedRevocationIndependentOfEligibility` |
| Native gate dispatch, cached/live admission, fail closed, historical environment | `worker`: `TestRetainedProductionGateDispatch`, `TestRetainedProductionMergeJourney`, `TestRetainedProductionFailsClosed`, `TestRetainedCachedProductionDiscovery`, `TestRetainedRevokedEvidenceIsNotHydrated`, `TestRetainedHistoricalQAEnvironmentRequiresOriginalHandoff`, `TestRetainedProductionRecovery` |
| Tag owner schema, current authority, withdrawal, accepted source/version completion | `master`: `TestMasterTagOnlyAuthorityAndWithdrawal`, `TestMasterTagOnlyValidationRechecksEvidence`, `TestMasterTagOnlyFailsClosed`, `TestMasterTagOnlyVersionNativeProvenance` |
| Assigned Rook stage, checkout guard, source lifecycle, caches and action routing | `worker`: `TestTagOnlySchemaAndAssignedStage`, `TestTagOnlyCandidatesExactAuthority`, `TestTagOnlyProductionPrepareRejectsForeignStage`, `TestTagOnlyProductionLifecycle`, `TestTagOnlyProductionCachedHydration`, `TestTagOnlyProductionActionGuards` |
| Exact source target, canonical object, lost ack/read-only recovery | `worker`: `TestTagOnlyIntegratedSourceVerification`, `TestTagOnlyExpectedObjectMatchesGit`, `TestTagOnlyEffectsAndReadOnlyRecovery` |
| Version provenance, final integrated target, historical bridge guard | `worker`: `TestTagOnlyVersionRequiresTrustedNativeChain`, `TestTagOnlyVersionProductionTarget`, `TestTagOnlyVersionEffectsBindFinalIntegratedCommit`, `TestTagOnlyProductionVersionJourney`, `TestTagOnlyHistoricalReadBridgePreservesOrdinaryGuards` |
| Fixed payload semantics including library/executable/mode/link drift | `worker`: `TestTagOnlyPayloadEquivalence`, `TestTagOnlyBinaryAndLibraryPayloadEquivalence` |

Offline targeted run, then full verification in the private isolated checkout:

```sh
flox activate -- env COOP_PR9_PRIVATE_FIXTURE=/absolute/private/original-pr9.json go test ./internal/master ./internal/worker -run 'Test(Retained|MasterTagOnly|TagOnly)' -count=1
flox activate -- go build ./...
flox activate -- go test ./...
flox activate -- env CGO_ENABLED=1 CC=/absolute/verified/gcc-wrapper-15.3.0/bin/gcc go test -race ./...
flox activate -- go vet ./...
flox activate -- golangci-lint run
flox activate -- go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
flox activate -- python3 -B -m unittest discover -s scripts -p 'test_*.py'
```

Also check `gofmt -l cmd internal image` is empty; do not format source in a docs
task. Run permission fixtures with intended DAC semantics: root's DAC override
and restrictive umask can invalidate them. The existing Linux test procedure uses
`umask 022` and drops `dac_override,dac_read_search` via `setpriv`; race tests need
the verified GCC wrapper and sufficient timeout. Hosted CI and platform/live
acceptance remain independent. No full-test, CI-pass or PR-publication receipt is
asserted here; main must supply exact-head results and resolve private-fixture
availability without publishing private bodies.

## Author Verification

The combined author worktree passed full `go test -count=1 ./...`, full
`go test -race ./...`, build, vet, golangci-lint 2.12.2 (zero issues), CI's
format check, govulncheck 1.6.0 (no vulnerabilities), all 58 Python packaging
tests and the 23 root installer fixtures. Focused retained/tag suites also
passed five repeated runs. Exact PR9 runs used the authorized private fixture;
it was neither staged nor published. Commands used Go 1.26.10, Flox 1.18.1,
GCC 15.3.0 for race builds, umask 022 and dropped root DAC-bypass capabilities.
These author results do not replace Robin's exact-head review, Wren's separate
`rook-retained-delivery-tag-only-v2` QA or matching hosted CI.

## Residual Limits

Master, Kata, GitHub and local state do not share a transaction. Pause, owner
withdrawal, permissions or branch state can change between the last guard and a
remote write. Producer locks and pre-effect revalidation narrow that window, not
eliminate it. Exact object/ref recovery cannot distinguish an independently
created byte-identical object/ref racing inside a persisted uncertain-write
window; a conclusive ref rejection is not adopted. Live GitHub annotated-object
conformance, repository permissions/rulesets and actual runtime activation need
separate operator evidence; mocks do not establish them. Observe can fail after
permission/placement changes, and no cross-placement journal adoption is supplied.
Keep holds and route these cases for explicit disposition rather than retrying
with new identities, modifying tags or relabeling evidence.
