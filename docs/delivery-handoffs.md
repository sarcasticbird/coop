# QA and release handoffs

Coop records these follow-ups separately from the original goal, task binding,
PR and reviews. A conversation acknowledgement is not an assignment or approval.
Deploy the master API before updating workers and guest CLIs. Historical source
acceptance, reviews and unknown-write holds remain in place.

## Request first postmerge QA

An authenticated owner or coordinator with `plan` capability can record a bounded
first postmerge QA request against the original approved goal and task:

```sh
coop task first-postmerge-qa --file /private/path/first-postmerge-qa.json
```

The private JSON contains `kind: "first-postmerge-qa"` (or omits `kind`), the exact
`goal`, canonical `task_url`, `pr_url`, `assignment_sha256`, `head_sha`,
`artifact_sha256`, `environment`, `environment_revision`, `evidence_message_id`,
and a stable `request_id`. QA must be required by the approved goal, and both
environment fields must match its QA policy exactly. Omit `review_id`, `tag`,
and `workflow`; those fields are forbidden for this kind. Evidence must come
from an owner or the original assigned QA actor, in the goal thread or its
configured private QA evidence destination. Developer evidence alone is not
authority. Identical retries return the same handoff; changed payloads under the
same actor/request ID conflict.

This is an operator/master command, not a worker action or a substitute for an
independent QA verdict. It does not authorize source edits, release, deployment,
restart, re-adoption, or clearing historical evidence and unknown-write holds.
The master validates registered task/PR bindings, not live GitHub merge state or
artifact contents. Worker execution must independently validate those facts.
With remote `--host`, the private file must exist on the selected host; the CLI
does not upload a local evidence file.

The matching worker admits one native `qa` job to the original independent QA
actor for the exact merged artifact. Both configured retained Kata tasks and
trusted retained GitHub tasks use their existing binding and registered PR
association. Closed/completed tasks stay closed/completed. A prior QA review,
including a dismissed review or one on another head, prevents first assessment;
use the appropriate separately authorized disposition instead of inventing a
changes-requested review. Wrong identity, stale approval/placement, artifact
failure, missing engineering clearance and unresolved local source writes hold
admission/publication. Later duplicate handoffs cannot create another first QA.

The assigned QA actor uses the normal native `qa` action with `verdict` and
private `report_body`. Publication is a GitHub `COMMENT` review bound to the exact
commit, not `APPROVE` or `REQUEST_CHANGES`. Its `coop-qa:v3` metadata carries
`assessment: "first-postmerge"`, the authenticated `handoff_id`, artifact digest,
acceptance and environment revisions, and the native private evidence message ID
and body digest. `ValidateNativePostMergeQA` verifies this provenance;
`ValidateNativeQA` explicitly refuses to treat it as pre-merge clearance.
An acknowledged action retry returns the same receipt. A lost external write
response preserves the uncertain journal and requires operator inspection with
the original request ID, never a new request or blind replay.

Roll out separately authorized schema/master support first, then the matching
operator CLI and QA worker. Old workers cannot execute this new kind. After
installation is verified, the next operator command is the
`coop task first-postmerge-qa --file ...` command above, populated with the
retained source's actual identities and fresh evidence, not this capability's
source-maintenance goal. Confirm original-QA pickup and its formal result.
Production installation, historical QA disposition and owner UAT remain separate
steps. A stored handoff is not job pickup or completion; a post-merge assessment
does not repair the historical missing pre-merge QA or complete old acceptance.

## Reassess unchanged source after new evidence

An owner or a coordinator with `plan` capability can request a fresh QA job:

```sh
coop task reassess-qa --file /private/path/reassess.json
```

The private JSON contains the existing `goal` reference, canonical `task_url`,
`pr_url`, `assignment_sha256`, `head_sha`, `artifact_sha256`, the latest native
QA `review_id`, a new `evidence_message_id`, and a stable `request_id`.
Use the review identity from Coop's native PR evidence, not a shortened display
label. The evidence must be an owner or assigned-QA message in the approved goal
thread or its configured private QA evidence destination. An author message
cannot grant reassessment authority.

The worker selects one new exact-artifact QA job when the old matching review
requests changes. Reusing the old evidence, changing the candidate or referencing
a different review does not admit a job. Further requests for the same unchanged
review cannot produce a stream of retries. Wren must independently reconcile the
new evidence and publish a fresh verdict through the guarded QA action. Neither
the request nor owner UAT replaces her verdict or clears a merge gate.

This handles new device/UAT evidence or a resolved environment prerequisite.
An actual source defect still goes to the assigned developer. Do not make an
empty commit, re-adopt a task, or rewrite an old QA report to force pickup.

## Record an owner-approved release

```sh
coop task request-release --file /private/path/release.json
coop task handoffs --goal GOAL_ID --revision EXACT_REVISION_SHA
```

Only an authenticated owner with `decide` capability can create a release request. Its private JSON has
the same exact goal/task/PR/assignment/head/artifact and evidence fields, plus
`tag` (a stable `vMAJOR.MINOR.PATCH`) and `workflow` (an existing workflow filename,
such as `release.yml`). Omit `review_id`. The request itself records the bounded
owner grant; messages or planner capability cannot create that grant. A repository
and tag can have only one request. Changed idempotency payloads are rejected.

When the matching source delivery job is selected, it includes this recorded
owner extension. Current peer approval, independent QA, CI, mergeability and
head-match protection still apply. It uses the normal protected `deliver` action
to merge and complete the original source task.

Rook routes the release request to the original assigned author. That author can
execute the guarded follow-up without inventing a new implementation task:

```sh
/usr/local/libexec/coop-worker native release --config /var/lib/coop/worker/config.json \
  --goal GOAL_ID --revision EXACT_REVISION_SHA --handoff HANDOFF_ID
```

The command re-reads the authenticated request, current placement, pause state,
goal, task binding, merged PR, immutable source artifact, peer and QA evidence.
It derives the merged commit from the exact PR and confirms it remains on the
approved base branch. The workflow must exist in that commit. It creates an
annotated tag using the configured GitHub App; the repository's existing
tag-triggered workflow performs CI, packaging, attestations and publication.
It does not add or modify workflows, create release assets itself, or replace
an existing tag. A missing App permission is an actionable error.
Unresolved source-task action journals remain a hold even when GitHub reports
the PR merged. A remotely visible merge cannot bypass local write recovery.

The result is `waiting` while the matching tag workflow/publication is pending.
Failed or cancelled workflow runs return `blocked` with a concrete reason. A
later successful workflow rerun can be checked with the same handoff and tag.
Rook schedules a later check using the same handoff. `published` requires a
successful matching workflow and a non-draft release with assets. This receipt
is not independent artifact QA, checksum/provenance verification or an install
pass. The private local journal prevents duplicate tag writes. An unknown write
outcome stops for operator reconciliation; do not delete the journal or use a
new request ID to retry it. Journal state belongs to its original worker
placement; moving a worker requires an explicit recovery disposition.
Release journals are included in the existing recovery inventory. Unknown writes
require terminal proof. Acknowledged object/ref checkpoints can be explicitly
retired without claiming publication; a retired pre-ref checkpoint cannot resume
its write. An acknowledged tag can continue read-only publication checks through
the accepted placement proof, preserving its original request and tag object.
For an acknowledged tag, add `--observe` to the worker command to check only
GitHub's tag/workflow/release state while paused or after a restore. This mode
cannot create a tag or retry an uncertain write; it can record successful
publication as terminal recovery evidence. The observer can also report a known
pre-ref checkpoint as `checkpoint` with `tag_reference_not_started`; that is
retirement evidence, not release completion. Receipts remain readable after a
placement move only through the existing accepted-inventory proof.

## Homebrew follows the published release

Use a separate bounded, owner-approved delivery goal/task for the existing tap
repository. Reference the original source task, merged PR, release and owner
approval as evidence links; do not move the original task to a new goal.
The normal implementation, independent peer/QA, checks and protected PR delivery
apply to the formula update. Rook can plan that task once its exact repository,
base and approved goal are registered.

The formula's checksum must match what it downloads. A formula built from a
source tarball needs that source checksum, not a platform binary archive digest.
QA verifies the published tag/source ancestry, appropriate checksums/provenance
and an isolated supported Homebrew install/version/smoke test. Report unavailable
platform checks honestly. Publishing the tap does not authorize upgrading a
host, replacing a running binary or restarting existing shares.
