# Bird workflow UAT — 2026-11-03

## Result

**Live UAT not accepted.** The latest attempt passed the real implementation,
requested-changes/fix cycle, exact-head peer review, exact-artifact QA and Rook
merge. It failed the injected post-merge completion/restart check. Nine
focused deterministic workflow/recovery tests pass under the race detector,
but these use simulated GitHub, task-provider and model responses and do not
clear the live fleet gate.

The owner approved a fresh private `sarcasticbird/coop-fleet-uat-*` repository,
isolated Kata profile and disposable birds, with at most ten model turns, using
existing bird grants. The original permission preflight stopped before creating
external resources or submitting a model turn. After the explicitly approved
Rook permission upgrade, the isolated live fixture started. Production profiles,
birds and gh-sweep remain unchanged. The working branch was not pushed or merged.

## Live permission preflight

The host authenticated as `sarcasticbird`. Each bird's existing host-only App
registration/signing key authenticated read-only GitHub `/app` and installation
queries. Returned App identity and installation owner were checked against the
registration. Signing keys/JWTs were neither printed nor copied into a guest.

After the owner upgraded and accepted Rook's personal installation, GitHub reports
the following effective permissions for `sarcasticbird`:

| Bird | Contents | Pull requests | Existing repository selection |
| --- | --- | --- | --- |
| Robin | write | write | all |
| Hawk | write | write | all |
| Wren | read | write | all |
| Rook | write | write | all |

The original Rook read/read grant was insufficient. The owner explicitly approved
the upgrade; signed installation queries and actual token minting now verify
write/write. Only the personal entry in Rook's host registration was changed,
after a private backup. The `sarcasticbirds` organization entry remains read/read.
Coop's delivery capability itself does not increase GitHub permissions.

Do not use an owner token, let the author merge, or claim a mocked merge as live
evidence. The original preflight offered two explicit owner actions:

1. Supply a UAT-only Rook App/credential with Contents write and the existing
   pull-request/check read access, installed only on the test repository; pin its
   real bot ID in the isolated team policy. PR review-writing authority is not
   needed for Rook's merge role.
2. Explicitly approve upgrading the existing Rook App and installation to those
   merge permissions. Its current `all` selection expands authority over existing
   repositories, not just the new sandbox. This is not implicit in reusing grants.

The host's default Kata remains v0.10.0. A SHA256-verified v0.19.0 binary was staged
privately for isolated UAT, without upgrading the existing host profile or
relaxing compatibility guards.

## Live attempt and cutover preflight

The first attempt failed during provider acquisition, before workers or model
turns. The fixture was corrected to use normal Coop credential staging, including
its supported host symlink resolution. Provider/App credentials enter guest tmpfs
leases; signing keys remain on the host. GitHub verifies each token is restricted
to the one new private sandbox repository.

The second attempt performed two observed native turns: Hawk planned one real
Kata task and Robin implemented and published one real PR. At inspection, Wren
and Rook had submitted no turns. Robin's commit used a numeric-only noreply email;
GitHub returned null author and committer accounts. The existing
`unresolved_team_contributors` guard refused to treat that commit as resolved
team evidence. The implementation journal is held at
`native_completed_without_result`. No requested-changes review, QA, merge, native
completion or post-merge restart acceptance has been established. The test failed
after 1801.14 seconds; its retained result records `passed: false` and the Kata
task still open. All four disposable birds were subsequently verified stopped,
and the isolated Kata daemon was gracefully stopped through its own private
profile. Sandbox repositories, task, disks, journals and evidence were retained.
No turn cap was raised and no identity guard was relaxed.

### Narrow retry

The fixture had never configured Git author/committer identity. It now sets the
assigned bot's login and canonical ID-plus-login noreply address before startup,
checks `git var GIT_AUTHOR_IDENT`, and tells the model to use that identity.
GitHub's real account-resolution guard remains authoritative and unchanged.
Concrete completed-without-result or observation-unavailable execution holds now
fail the test promptly, preserving their evidence rather than waiting 30 minutes.

The retained failed attempt contains two native intents. The next fresh sandbox
attempt has a hard eight-turn total (Robin 2, Hawk 3, Wren 1, Rook 2), keeping the
combined ceiling at the originally approved ten. No production binary or profile
was changed for this retry.

The retry failed after 606.25 seconds at Wren's
`native_completed_without_result`. Its six recorded native intents cover planning,
implementation, requested-changes review, author fix, exact-head peer approval and
QA. Together with the earlier two intents, eight of the ten approved turns have
been used. Rook had no delivery turn; no merge, task completion or post-merge
restart gate has passed. All four retry birds were verified stopped and the
isolated Kata daemon was gracefully stopped again.

To inspect without restarting Wren or its workers, a temporary inspector mounted
only its stopped home volume read-only and copied the cold native SQLite database
with its WAL/SHM files into private evidence. The inspector was removed after
each read; the original disks, sandbox and native messages remain intact.

The retained native tool output proves Wren downloaded the registered exact-head
archive, computed the matching source digest and ran `bash acceptance.sh`, which
printed `ACCEPTANCE_PASS`. Its action file, however, used verdict `passed` instead
of the supported `approved`/`changes-needed`. After correcting its file mode to
0600, the helper returned `forbidden`; Wren preserved the same request ID and
reported the refusal rather than bypassing it. This is a publication-contract
failure, not accepted formal QA and not a merge clearance.

The narrow product correction makes file mode and both allowed verdict values
explicit in the generated prompt, and rejects invalid review/QA verdicts with an
actionable invalid-input error before persisting an immutable intent. Regression
coverage verifies invalid input creates no action/intent journal or GitHub call,
and a corrected input can reuse the uncommitted request ID without bypassing live
authority. Existing saved bad intents are **not** rewritten or cleared. Focused
QA/delivery/recovery race tests, vet and the full `go test ./...` rerun pass.
No further live turn was submitted after this failure.

The owner then explicitly increased the cumulative UAT allowance from ten to
sixteen native turns. With eight already used, one fresh run was capped at eight.
That attempt used a newly built worker containing the Git-identity fixture
correction, explicit verdict/file-mode prompt, and early verdict validation. The
existing candidate tool staged and verified a hash-bound dirty preview; it is not
a clean exact-source acceptance candidate or a production installation.

### Latest result: completion uncertainty remains held

The latest attempt failed after 1801.29 seconds. Seven native intents were
recorded, bringing cumulative use to fifteen of sixteen authorized turns. The
actual private PR was merged by Rook's bot after Hawk requested changes on the
initial whitespace defect, Robin repaired it, Hawk approved the repaired exact
head, and Wren published an independent QA approval. Both GitHub test check runs
passed on that same head.

Wren's cold native tool records establish an actual download and digest check of
the registered archive, `bash acceptance.sh` returning `ACCEPTANCE_PASS`, and a
successful native QA publication receipt. Rook's retained GitHub audit contains
exactly one merge PUT. Its original native delivery action returned `forbidden`
after merging. The local journal is `State: uncertain`, `Step: task-completed`;
Kata remained open. The test restarted Rook after its terminal model evidence and
restored authority, but completion did not recover.

The fault occurred **inside** the first completion HTTP request: the fixture
revoked delivery capability before the master processed it, after the worker had
already journaled the effect as uncertain. This is not the prepared `merge`
checkpoint exercised by `TestKataNativeOfflineMergedDeliveryRecovery`, which
explicitly invokes the stopped-worker recovery command. That command refuses an
uncertain/started effect, and an ordinary restart does not invoke it. Treating
this live case as automatic recovery was an unsupported fixture expectation;
the uncertainty hold itself is intentional safety behavior. Neither a timeout
nor a known merge receipt clears an uncertain, unfinished task-completion write.

All four latest birds were verified stopped; their original state/home volumes
were inspected through a temporary read-only inspector and retained. The isolated
Kata daemon was gracefully stopped. No additional model turn, completion write,
merge, journal reset or production change was attempted. The remaining gate is
the exact post-merge completion/restart journey, not implementation, CI or QA.

## Owner-directed rollout decision

After reviewing the latest result, the owner explicitly requested opening the
merge PR and upgrading the birds with the known recovery gap accepted. This is
an operator decision to proceed, **not** a passed completion/restart UAT result.
The PR and rollout must retain that distinction. Checked installers, placement
fences and uncertain-action holds remain in force; the decision does not permit
discarding journals, inventing completion evidence or converting legacy disks
in place. Keep original work, configurations, binaries and rollback evidence.

The owner subsequently authorized production cutover. Read-only discovery found
Robin, Hawk and Wren on the registered remote host and Rook locally, all still
using legacy machine profiles. The remote host reports Container 1.5.0. The
master reports epoch 1 and no pending guest recovery submissions; that does not
prove the workers are drained. Hawk retains an accepted native fix invocation
with no terminal evidence and `native_observation_unavailable`, plus a separate
completed-without-result planning record. Host diagnostic pins also differ from
the live master worker instances and cannot be used as placement authority.

A consistent production-master snapshot was created through the existing backup
API, with its database and configuration hash manifest retained privately. It is
a preflight master snapshot, **not** a coordinated master/Kata/worker rollback
boundary. No production bird was stopped, fenced, reconfigured or replaced.
The local owner client returned forbidden; the existing remote owner client
successfully served the read-only preflight and supported backup request, without
copying credentials between hosts.

Read-only native API inspection subsequently confirmed Hawk's deployed
`1.18.18-coop-recovery.2` server is healthy. Its exact saved fix message returns
404, although the session exists and is idle. The session's older recovery
receipt explicitly says `interrupted` with effects `unknown`; it is not terminal
proof for the missing later invocation. The separate planning message is present
and has a completed assistant response. Hawk's task checkout is clean and its
existing PR remains open at its saved head. None of those facts authorizes
inventing a missing message, marking the invocation successful, or replaying it.
A regression assertion now verifies missing native evidence cannot be cancelled
as if it were terminal. Focused recovery/UAT race tests and vet passed.

Before deployment, resolve the live identity failure and establish the full
workflow acceptance below; reconcile Hawk's exact saved invocation and effects;
then stage a reviewed clean candidate and coordinated backups. Do not clear
journals, infer identity from email text, or convert a legacy disk in place.

## Deterministic baseline

Run through Flox with `TMPDIR=/private/tmp`, `-race`, `-count=1`:

- `TestNativeProductionDeliveryEndToEnd`
- `TestKataNativeEngineeringDeliveryAndRebind`
- `TestKataNativeOfflineMergedDeliveryRecovery`
- `TestNativeRookDeliveryChecksLiveIdentityAndCapabilities`
- `TestTaskMutationGuardsAndCompletionRequiresMatchingPR`
- `TestTaskRookCompletionRechecksLiveAuthorityAfterProviderRead`
- `TestQAGateDeliveryRequiresBothIndependentGates`
- `TestQAGateDefectsRequireAuthorFixNewReviewThenQA`
- `TestNativeDeliveryRecoveryRefusesUnsafeInputsWithoutEffects`

All passed. These cover requested changes and author repair, independent review,
required QA, Rook-only integration/completion, fresh authority checks, and recovery
without repeating an acknowledged merge. They establish local protocol behavior,
not live provider credentials, model execution, GitHub CI or fleet health.

## Pending live journey

After the permission blocker is resolved, use the approved isolated resources
for one tiny change: implementation → requested changes → author fix → exact-head
independent approval → required exact-artifact QA → Rook merge → Kata `done`.
Exercise interrupted publication/completion and verify one merged PR and one task
completion without manual owner merging. Retain private evidence and leave
disposable birds stopped. Fleet activation remains separate.
