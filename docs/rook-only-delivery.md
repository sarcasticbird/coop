# Rook-only delivery activation and rollback

Policy source base: `d5d8dea1f0f71a81176c4f1eda94dcf8e06b642a`. This packet describes operator activation of the
Rook-only delivery policy, not evidence that it is installed or runtime-cleared.
Source acceptance, installation, configuration, restart and grants are separate
receipts. Installation, restart, configuration and owner grants are operator-only;
this change does not authorize them. There is no bootstrap merge: an installed
old `AUTHOR` policy cannot authorize an author to merge its own policy replacement.
Keep delivery held until an independently authorized operator path installs the
accepted implementation and verifies its effective policy.

## Activation gates

1. Record the independently accepted source head and build/artifact digest, the
   installed versions and policy hashes, current placement, pause/admission state,
   and outstanding action journals. Preserve backups and terminal/unknown-write
   receipts before any separately authorized install or restart.
2. The operator installs compatible master, worker and CLI artifacts and restarts
   only under applicable grants. Source tests and this document are not that grant.
   Verify the actual running binaries and effective policy, not just checkout HEAD.
3. In the real configuration, the operator must explicitly grant `deliver` only
   to principal `rook`, kind `agent`, GitHub bot ID `345406391`. Do not grant it to
   authors, peers, QA, aliases or another bot. This packet makes no real config
   change. Verify Rook's current engagement and private goal/QA thread membership.
4. Re-read goal approval, provider identity, registration and immutable assignment.
    Adding `deliver` changes the roster policy digest. Already-published tasks
    bound to the old digest cannot use this grant to migrate their assignment:
    published Kata rebinding and cross-policy evidence carry-forward remain
    unsupported. Stop on that mismatch and request an explicit supported
    authority disposition; do not patch policy hashes, database rows, bindings
    or journals by hand, or create replacement implementation tasks.
5. Obtain current native peer clearance and fresh independent QA for the exact
   candidate, artifact, acceptance and approved environment. Then the authenticated
   owner records the exact delivery handoff below. Verify native Rook admission,
   current-head checks, merge protection and completion receipts separately.

## Exact owner handoff

Only an authenticated owner with current `read` and `decide` authority may create
the grant. Rook executes delivery; it cannot self-grant. Use a private JSON file:

```sh
coop task authorize-delivery --file /private/path/deliver.json
coop task handoffs --goal GOAL_ID --revision EXACT_REVISION_SHA
```

With remote `--host`, the file must already exist on that host; it is not uploaded.
The JSON fields below are exact native values, not values to infer from prose or
copy from a different PR. `kind` may be omitted because the command supplies
`deliver`; if present it must be `"deliver"`.

| Fields | Required provenance |
| --- | --- |
| `goal` | Exact `source`, `master_id`, `goal_id`, `revision_sha256` reference |
| `approval_sequence`, `acceptance_sha256` | Current approved goal sequence and native acceptance digest |
| `task_url`, `pr_url`, `assignment_sha256` | Canonical registered task/PR pair and unchanged assignment digest |
| `task_scope_sha256`, `base_branch` | Immutable binding scope digest and approved branch |
| `head_sha`, `base_sha`, `pr_scope_revision` | Exact 40-hex head/base commits and native 64-hex PR-scope revision |
| `artifact_sha256`, `peer_review_id` | Exact candidate artifact digest and current independent native peer review |
| `environment`, `environment_revision` | Exact approved QA environment when QA is required; supply both together |
| `qa_review_id`, `qa_evidence_message_id`, `qa_evidence_body_sha256` | Fresh independent assigned-QA review and native private report message/body digest; all required when QA is required |
| `evidence_message_id` | Non-imported owner-authored approval message in the goal or configured private QA thread |
| `request_id` | Stable unique request identity for this exact decision |
| `revoked` | Omit or `false` for a grant; `true` for an explicit revocation |

Digests are lowercase 64-hex SHA-256 values; use native serialization/digests,
not an invented hash of a paraphrase. Omit `review_id`, `tag` and `workflow`.
Use the actual native review IDs, not GraphQL node IDs or display labels.
The master records owner provenance and checks registered authority; it does not
fetch live GitHub state. A stored handoff alone is not execution or merge clearance.
The worker must freshly validate identities, head/base, reviews, QA, CI,
mergeability, placement and unresolved-write holds before delivery.

Identical actor/request retries return the same handoff; changed payloads under
that request ID conflict. To revoke, submit the exact decision fields with
`revoked: true` and a new request ID. The latest recorded delivery decision for
the exact goal revision/task/assignment/PR wins, including revocation or stale
scope; an older grant must not become a fallback. The master stamps a monotonic
`sequence` on each decision, so clock rollback and opaque IDs do not change that
ordering. Callers never supply the sequence. Rook completion references the
selected handoff ID. A later grant requires a new explicit owner decision and
fresh validation, not replay of an older grant.

## Retained work boundaries

A delivery-only amendment is supported for the **same goal, task and assignment**
with an exact handoff alongside the existing immutable binding. It does not
change the author, peer, QA actor, assignment or provenance. It is not migration
of old reviews or QA into a new goal. A changed goal, head or base requires
renewed applicable native stages and an exact current owner grant. Published
Kata rebind is refused; do not manufacture a replacement binding to evade that.
Owner UAT and developer assertions do not replace fresh independent QA.

External owner PR #158 (`99587865`, base `main`) and PR #159 (`d5d8dea`, base
`simplify/kata-first`) are not supported native delivery candidates merely
because their source exists. Native delivery requires the configured
`native_github` bot identity, trusted v3 provenance and exact native registration;
external-owner history cannot be relabeled to satisfy those gates. Do not invent
bindings, assignments, reviews or registrations to bridge the gap. PR #157 smoke
evidence is not runtime clearance for these candidates or this policy rollout.
Resolve unsupported retained work through an explicitly supported owner/operator
disposition, not new-goal evidence migration.

Their exact source heads are `99587865a8133a67311dcb1b65e67a59562eedb5`
and `d5d8dea1f0f71a81176c4f1eda94dcf8e06b642a`. Native intake rejects
their human author before review/QA selection (`native_PR_identity_or_scope_changed`)
and neither body carries native v3 PR provenance. Task adoption admits an existing
Kata issue, not an external PR; there is no external-author review/QA admission
operation. Preserve the owner-authored stack. This policy does not fabricate that
missing path or represent a research report as assigned native clearance.

## Reproducible build packet

Run in an isolated checkout at the independently accepted exact policy head, not
on a running worker's checkout. These commands build files only; they do not
install or activate them. Record `git rev-parse HEAD`, `go version`, compiler and
Python versions, all test receipts, and artifact digests alongside the native
`source-bundle-v1` digest. The canonical source digest is not the tarball checksum.

```sh
flox activate -- go build -trimpath -o /private/staging/coop ./cmd/coop
flox activate -- env GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o /private/staging/coop-worker ./cmd/coop-worker
sha256sum /private/staging/coop /private/staging/coop-worker
```

The operator must supply and verify the existing private staging directory and
target-platform master/CLI build; the first command targets the build host. Use
the matching repository installer and applicable platform service manager only
under the separate install/restart grant, with exact artifact/config paths and
their prechange backups in the operator receipt. No host/service names or private
configuration locations are inferred by this packet.

The only new roster capability is `deliver` on Rook's existing member entry:

```json
{"name":"rook","actor":{"id":345406391,"login":"sarcastic-bird-rook[bot]","kind":"Bot"},"capabilities":["plan","deliver"]}
```

Retain only capabilities already separately granted (for example `plan`); this
example does not grant them. Rook cannot hold implement/review/QA capabilities.
Do not replace the rest of the roster. Retain `delivery_hold: true` until the
operator verifies effective Rook identity, exact current policy and owner grant;
any hold change is a separate authorized configuration action. All other actors
remain unable to deliver even with their hold disabled. Before merge, the live
base must equal the grant; after merge, confirmation verifies the squash commit's
single parent against that original base, rather than the now-advanced live ref.

## Rollback and holds

Pause delivery through the supported operator controls and record the hold before
rollback. Revoke applicable grants through new owner decisions while the matching
API is available. Preserve original requests, assignments, journals, evidence and
receipts; inspect unknown external writes rather than blindly replaying them.
Restore only the separately approved known-good artifact/configuration under an
operator grant, then verify actual running versions, policy hashes and placement.
Rolling back binaries does not revoke persisted grants or undo a GitHub merge.

If rollback restores `AUTHOR`, leave delivery paused: that old policy is not
permission for author self-merge. Incompatible bound policy hashes require
supported authority reconciliation, never ad hoc edits. Reopening delivery needs
explicit operator authorization, compatible runtime/configuration and current
owner grants with renewed exact-artifact checks. Do not report installation,
restart, delivery or customer QA as complete from a green source test run.

## Author verification

The implementation was checked in the configured Flox environment on Linux arm64:
Go 1.26.10, Python 3.14.7, golangci-lint 2.12.2, govulncheck 1.6.0 and the existing
Nix-store GCC 15.3.0 wrapper. No toolchain installation or runtime activation was
performed. These are author checks, not Robin's engineering verdict or Wren's QA.

Passed: full `go test ./...`, full `go test -race ./...`, `go build ./...`,
`go vet ./...`, golangci-lint (zero issues), govulncheck (no vulnerabilities),
gofmt, and all 58 Python packaging/installer/artifact tests matching CI.

This root container starts with a restrictive umask and DAC override. Full Go
tests used `umask 022` and `setpriv --bounding-set=-dac_override,-dac_read_search`
so unreadable-directory and permission-mode fixtures exercise their intended
semantics without weakening source or private-directory permissions. The PATH
fixture explicitly isolates its release-tools directory from installed tools.
Race tests used `CGO_ENABLED=1` with
`CC=/nix/store/gw0vcpg33m9cqd6rscyqvds45apfrxfy-gcc-wrapper-15.3.0/bin/gcc`.
The first full race run hit the shell's 120-second limit; the completed rerun
used a 300-second limit. Hosted CI and independent exact-head stages are separate
receipts, not inferred from these local results.
