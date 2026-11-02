# Supplemental Source Journey

This is a capability candidate, not activation, fixture-intake or delivery
authority. Original PR158/159/160 human provenance and PR161/162 native provenance
remain immutable. Nothing here grants deployment, installation, service restart,
hold lifting, source-author delivery, operator GitHub merging, tags or releases.
Robin must independently review the changed head; Wren must separately assess its
exact artifact in `external-intake-stacked-main-v1`. Predecessor approvals do not
clear this delta. PR162 activation and gh-sweep are independent of this capability.

## Implemented Surface

Owner commands (own authenticated owner configuration, private input files):

```sh
coop --master-config /absolute/private/owner.toml task create --file /absolute/private/gates.json
coop --master-config /absolute/private/owner.toml task source-decision --file /absolute/private/decision.json
coop --master-config /absolute/private/owner.toml task source-decisions --goal GOAL --revision REVISION
```

`gates.json` uses the existing exact-approved `TaskCreateInput` schema, with
`purpose: "source-gates"`, `expected_approval_sequence`, `goal`, `repository`,
`base_branch`, `title`, `body`, stable `request_id` and optional `depends_on`.
Use a gate-only container, not a replacement implementation of the product PR.
The immutable purpose suppresses ordinary implementation before intake. Existing
published source tasks/bindings are never rewritten to add this purpose.

The complete decision wire schema is
`internal/coordination/source_journey.go:SourceDecisionInput`. Unknown fields are
rejected. Required fields for intake/transition are:

```json
{
  "kind": "intake",
  "goal": {"source":"master","master_id":"MASTER","goal_id":"GOAL","revision_sha256":"REVISION"},
  "approval_sequence": 1,
  "gate_task_url": "CANONICAL_GATE_TASK",
  "gate_assignment_sha256": "REGISTERED_ASSIGNMENT",
  "policy_sha256": "CURRENT_EXECUTION_POLICY",
  "source": {
    "repository":"OWNER/REPO", "repository_id":1,
    "pr_url":"https://github.com/OWNER/REPO/pull/1", "pr_id":"ACTUAL_NODE_ID",
    "author_id":1,"author_kind":"User","owner_id":1,
    "source_repository":"OWNER/REPO","source_repository_id":1,"branch":"SOURCE_BRANCH",
    "original_base_branch":"main","original_base_sha":"ORIGINAL_BASE",
    "head_sha":"HEAD","tree_sha":"TREE",
    "bundle_kind":"source-bundle-v1","bundle_revision":1,
    "bundle_sha256":"CANONICAL_SOURCE_BUNDLE","payload_sha256":"CANONICAL_SOURCE_BUNDLE",
    "contributors":[1],"contributors_complete":true,
    "external_evidence":[{"id":"https://github.com/OWNER/REPO/pull/1#body","kind":"pr-body","body_sha256":"ORIGINAL_BODY_DIGEST"}]
  },
  "target": {"repository":"OWNER/REPO","repository_id":1,"branch":"main","sha":"CURRENT_MAIN","result_tree_sha":"TESTED_INTEGRATION_TREE","result_bundle_sha256":"TESTED_INTEGRATION_BUNDLE"},
  "acceptance_sha256":"CURRENT_ACCEPTANCE",
  "environment":"APPROVED_ENVIRONMENT","environment_revision":"APPROVED_REVISION",
  "reviewer_id":"ASSIGNED_INDEPENDENT_REVIEWER","qa_id":"ASSIGNED_INDEPENDENT_QA",
  "evidence_message_id":"AUTHENTICATED_OWNER_MESSAGE","request_id":"STABLE_OWNER_REQUEST"
}
```

The strings above are schema placeholders, not valid grants or fabricated fixture
values. Hashes must have the exact lengths/formats validated by the schema. Source
bundle digesting uses the repository's canonical `source-bundle-v1` implementation;
`SourceDigest` hashes typed Go JSON, not arbitrarily reordered JSON. Do not substitute
an archive-download hash, a Git tree ID, or a caller's display-name comparison.
The Go coordination/masterclient APIs are the supported programmable digest and
registration interfaces; there is not yet a general human-intake digest CLI.

For a native source, `author_kind` is `Bot` and the additional original fields are
`original_goal`, `original_task_url`, `original_assignment_sha256`,
`original_policy_sha256`, and `original_registration` (typed registered artifact
digest). These must match actual retained records. Human intake forbids these
historical native fields. The source body, commits and source branch are not edited.
Unknown contributors fail closed. Authors, committers and resolved co-authors use
immutable account IDs, including bot aliases. Bounded predecessor contributors are
retained when retargeting changes GitHub's displayed commit comparison.

## Target And Gates

A stacked source includes `target.predecessor`, an actual stored
`SourceIntegrationReceipt`: `goal`, `id`, `decision_id`, `pr_url`, `source_sha256`,
`target_repository_id`, `target_branch`, `before_sha`, `commit_sha`, `tree_sha`,
`request_id`. It must name the source's actual original base branch/head. Its
integrated commit must have the recorded main parent and tree and be an ancestor
of current main. Merely merging into a feature branch or comparing equal SHAs
does not establish this proof.

`transition` admits fresh gates for an already published native source, or refreshes
a human intake using its exact `parent_id`. Original task/base/assignment/source
records are not updated. A new main SHA requires an explicit new transition and
renewed gates. No historical report is relabeled to the new environment or target.
Preparation and pre-effect validation recompute the integration tree and canonical
bundle. Conflicts stop for separately authorized author integration and fresh gates;
this path does not silently rebase, push or give Rook author privileges.

The assigned stages are `source-review` and `source-qa`. Both are independent of
all bound contributors and one another. Their checked-out remotes cannot push.
Engineering review writes a fresh exact-head GitHub review. QA writes a fresh
exact-head verdict without publishing its private report text. Both create a new
private `SourceGateReport` and separately journaled gate receipt. Reports bind the
decision, complete scope digest, stage, verdict and actual GitHub review ID.

Assigned actors use only:

```sh
/usr/local/libexec/coop-worker native action --config /var/lib/coop/worker/config.json --job ASSIGNED_JOB --input /absolute/private/action.json
```

Review action: `operation: source-review`, stable `request_id` (at most 100
characters), `verdict: approved|changes-needed`, `body`.
QA action: `operation: source-qa`, stable `request_id`, `verdict`, private
`report_body`. No prior native or imported report substitutes for these assessments.

## Rook Effects

Owner `retarget` and `deliver` decisions copy the exact gate decision scope and add
its `parent_id`, `actor_id: rook`, `engineering_report_id`,
`engineering_body_sha256`, `qa_report_id`, `qa_body_sha256`, plus a new explicit
owner request/evidence identity. These are separate grants. A transition does not
implicitly authorize either effect. Reports must remain applicable, current and
independent; delivery also checks their real GitHub review records.

Only principal `rook`, immutable GitHub bot `345406391`, with current `deliver`
capability and assigned delivery role can execute `source-retarget` or
`source-deliver`. Their action payload contains the operation and stable request.
Retarget performs only `PATCH /repos/{repo}/pulls/{n}` with `base: main`. Delivery
performs a head-pinned squash `PUT /repos/{repo}/pulls/{n}/merge`. No ref push,
predecessor modification, force push, implicit branch integration or source-author
merge fallback exists. Holds remain effective.

Each effect has an immutable input intent, local pre/post step and separate master
receipt. Delivery checks the acknowledged commit's exact parent and integration
tree before recording integration provenance or completing a task. Native sources
complete their original product task under supplemental authority, not a rebound
main assignment. External sources complete their gate-only intake task. A separate
gate container used for a native source may remain open; its stages terminate via
the source effect receipts. Provider completion preserves its own stable request.

Authenticated HTTP/masterclient surface:

| Endpoint | Input | Meaning |
| --- | --- | --- |
| `POST /api/v1/source-decisions` | `SourceDecisionInput` | Owner decision/withdrawal |
| `/api/v1/source-decisions/list` | `SourceDecisionQuery` | Scoped monotonic history |
| `/api/v1/source-decisions/validate` | `SourceDecisionValidation` | Exact current assigned authority |
| `/api/v1/source-decisions/effect` | `SourceEffectReceipt` | Exact acknowledged native fact |
| `/api/v1/source-decisions/effects` | `SourceDecisionValidation` | Read recorded facts |
| `/api/v1/source-decisions/task-state` | `SourceDecisionValidation` | Scoped product completion status |
| `/api/v1/source-decisions/complete` | `SourceCompletionInput` | Current Rook-only terminal completion |

## Withdrawal And Recovery

Withdrawal input contains only `kind: revoke`, exact `goal`, `revoke_id` and a
stable `request_id`. Authenticated owner read/decide and historical membership are
required; paused/superseded goals or disabled executors cannot veto withdrawal.
Revocation is append-only and applies through parent authority. Re-enabling agents,
resuming goals or rolling binaries back never removes it. Newer decisions are
selected before validation; an invalid newer decision does not expose an older one.

Lost GitHub responses remain uncertain, even when remote state looks equal.
Neither another request ID nor a restart grants a second effect. Observe the
original request without changing any journal:

```sh
/usr/local/libexec/coop-worker native observe-source --config /var/lib/coop/worker/config.json --job ORIGINAL_JOB --request ORIGINAL_REQUEST
```

Observation is not adoption, confirmation, execution authority or completion. It
remains read-only after withdrawal. There is deliberately no automatic adoption of
an ambiguous foreign retarget/merge and no manual journal-repair recipe. An
unresolved uncertain effect is an operator reconciliation hold. Preserve the exact
request and lineage. Master/provider acknowledged retries use original durable
request identities; they never imply a fresh GitHub write.

## Contract Evidence

| Contract | Evidence |
| --- | --- |
| Separate gate-only admission, source preservation | `TestSourceIntakeRegistrationAndDurableWithdrawal`, `TestSourceStackProductionJourney` |
| Owner-only decisions, duplicate/retry/concurrent intake | `TestSourceConcurrentIntakeExactlyOnce`, `TestSourceIntakeRegistrationAndDurableWithdrawal` |
| Contributor/alias and current policy independence | `TestSourceIntakeIndependentIdentityAndPolicy`, production drift matrix |
| Actual PR158 -> PR159 -> PR161 -> PR162 main receipts | `TestSourceStackProductionJourney` through actual owner CLI, master HTTP and worker production paths |
| PR160 sibling refresh, intervening main drift, author-integration refusal | End of `TestSourceStackProductionJourney` |
| Fresh native reports and Rook-only authority/completion | `TestSourceExecutionRequiresRenewedIndependentReports`, production journey |
| Head/tree/payload/body/contributor/check/policy/approval/acceptance/environment/target/hold/revocation drift | `TestSourceProductionZeroEffectDriftMatrix` |
| Uncertain review response/restart/new-request refusal and read-only withdrawal observation | `TestSourceLostReviewResponseHeldAcrossRestart` |
| Ordinary unrelated native guards | Existing unmodified native/retained suites; source-gate purpose never clears them |

Fixtures are real pinned source archives, public PR/contributor/review snapshots and
authenticated native bindings/registrations under `internal/worker/testdata/source_stack`.
Their recorder performs reads only. New test decisions, reports, retargets and
integrated commits are explicitly synthetic. No private historical QA body is
distributed or claimed as independently retested. Every GitHub/provider write is
mocked. The tests simulate GitHub/local merge responses; they are not real GitHub
stack integrations or a proof of every GitHub merge-algorithm edge case.

## Activation Packet

Operator activation remains separately authorized. Before any activation, retain
the exact reviewed commit/tree/source bundle, full build manifests and binary
hashes, installed and `/proc/PID/exe` hashes for the actual running services,
configuration and canonical roster hashes, placement/generation and complete
outstanding action/intent/engineering journal inventory. Back up the original
master database with its supported consistent-backup operation and the stopped
worker state/checkout evidence. Do not substitute this guest's CLI hash for host
master identity or assume an installed/running match that has not been observed.

This implementation session observed Linux/arm64 Go 1.26.10, Python 3.14.7,
Flox 1.13.2 and an already-present Nix GCC 15.3.0 wrapper. No environment manifest,
live configuration or installation was changed. Observed guest binaries/config:

```text
coop-worker 2306df21178e37199865bd2d150ad96abd81b305a6d8768551b44b9143e4927e
coop        9ccf5c7dc5e5972d6f52be176f1b7a79408c238be2d470ace83151ee2777e2ed
config      6ec2b1784b4180cb13c42331620b740a975c66ddece1fbcf94d53657a4480920
```

These are pre-activation observations, NOT hashes of a deployed capability delta.
Reconcile them against the operator's platform build and actual running artifacts.
Install the reviewed master/API before enabling workers that require these
supplements. Preserve admission/delivery holds through a fully isolated canary
using disposable databases, fixture clients and mocked GitHub/provider boundaries.
Run the production journey and negative suites on the reviewed head before any
separate exact fixture intake/retarget/delivery grants. Refresh roster/config/build
hashes after every authorized change. Do not test a canary by mutating real PRs.

The additional tables are append-only supplemental history; legacy source records,
bindings and serialized empty-purpose requests are unchanged. Older binaries do
not understand the new purpose/stages/tables and must not execute outstanding new
requests. Rollback must preserve all journals and deny unknown requests rather than
replaying, deleting or manually repairing them. Rollback neither undoes GitHub
effects nor withdraws authority: perform explicit authenticated withdrawal and
preserve recorded facts first. Source-changing author integration and cross-goal
source re-admission/migration are not implemented by this path and fail closed.

## Outstanding Verification

This packet does not claim complete acceptance or production activation readiness.
The full non-race suite passed with root DAC bypass dropped; build, vet, Python
58/root installer23 and vulnerability checks passed. The initial unfiltered race
run exceeded its 240-second command deadline before the worker package reported a
result, so there is no full-race green receipt from that run. Exact-head hosted CI,
fresh independent review and separate QA remain required.

The exhaustive lost-response/revocation/concurrency matrix at EVERY retarget,
merge, gate-registration and provider-completion boundary is not yet covered by
the new production fixture suite. The implemented review-loss hold is not evidence
for all those other boundaries. Cross-service revocation can race the final check
and a GitHub write; no distributed transaction is claimed. Unknown effects fail
closed. Operator backup/inventory/running-artifact verification, live activation
and all fixture grants remain outstanding and must be routed through Rook/Hark.
