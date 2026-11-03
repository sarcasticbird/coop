# Bird workflow UAT — 2026-11-03

## Result

**Live UAT blocked at read-only credential preflight; not accepted.** Nine
focused deterministic workflow/recovery tests pass under the race detector,
but these use simulated GitHub, task-provider and model responses and do not
clear the live fleet gate.

The owner approved a fresh private `sarcasticbird/coop-fleet-uat-*` repository,
isolated Kata profile and disposable birds, with at most ten model turns, using
existing bird grants. Preflight stopped before creating those external resources
or submitting a model turn. Production profiles, birds, storage and gh-sweep
remain unchanged. The working branch was not pushed or merged.

## Live permission preflight

The host authenticated as `sarcasticbird`. Each bird's existing host-only App
registration/signing key authenticated read-only GitHub `/app` and installation
queries. Returned App identity and installation owner were checked against the
registration. Signing keys/JWTs were neither printed nor copied into a guest.

For the `sarcasticbird` installation, GitHub reports:

| Bird | Contents | Pull requests | Existing repository selection |
| --- | --- | --- | --- |
| Robin | write | write | all |
| Hawk | write | write | all |
| Wren | read | write | all |
| Rook | **read** | **read** | **all** |

Rook's `winged-keys-rook` App cannot perform the required merge with that grant.
The local registration also requests only read access for those permissions.
Coop's Rook delivery capability does not increase GitHub permissions.

Do not use an owner token, let the author merge, or claim a mocked merge as live
evidence. Choose one explicit owner action:

1. Supply a UAT-only Rook App/credential with Contents write and the existing
   pull-request/check read access, installed only on the test repository; pin its
   real bot ID in the isolated team policy. PR review-writing authority is not
   needed for Rook's merge role.
2. Explicitly approve upgrading the existing Rook App and installation to those
   merge permissions. Its current `all` selection expands authority over existing
   repositories, not just the new sandbox. This is not implicit in reusing grants.

The host's default Kata is v0.10.0. Stage the tested v0.19.0 binary privately for
isolated UAT rather than upgrading the host's existing Kata profile. That release
is available upstream; no production change or relaxed compatibility guard is
needed for this setup step.

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
