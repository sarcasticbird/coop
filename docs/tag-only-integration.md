# Tag-Only Integration Tests

The central selector, live/scoped and cached hydration, historical-read bridge,
dispatcher prompt/revalidation, action routing and result hooks are now in the
combined tree. This is author test implementation, not independent review or QA
clearance. The former unapplied-hook snippets are obsolete.

## Tested Paths

`internal/worker/native_tag_only_production_test.go` uses the real native readers,
selector, action helper and local journals with mocked external transports:

- Original retained delivery runs through `RunNativeAction`; completed historical
  task/merged PR then admits only `tag-only` through `NativeCandidates`, without
  replacing the source policy, assignment, task metadata or PR metadata.
- Live `NativeSnapshot`, scoped `NativeTaskSnapshot` via validation routing, cached
  hydration and team scheduling preserve the historical source. Dirty cache,
  missing reports, imported owner evidence and withdrawal deny admission.
- Dispatcher `freshCandidate` and `nativePrompt` preserve tag-only instructions.
  A separate current-policy version task/PR is included in scoped validation and
  cached hydration; missing version registration/evidence or unsettled source
  execution prevents effects.
- Valid checkout preparation, `RunNativeAction` routing, pre-effect master checks,
  action guards and `nativeJobHasResult` are exercised together. A source delivery
  receipt is not a tag result; an uncertain tag action is not completed.
- Read-only observation returns `observed` or an object-only `checkpoint`, never
  native completion. It writes neither local files nor remote state, even after
  pause, withdrawal, delivery disablement or hold. Snapshot tests compare file
  bytes, modes, timestamps, inodes and directories, including a missing lock file.
- Only the assigned action with the original serialized request can confirm a
  visible owned tag. It rechecks current authority, roster, holds, placement and
  evidence, re-reads the integrated commit/tree and preserves receipt lineage.
  Lost responses never replay object/ref writes. Completed receipts are read
  idempotently without rewriting files; interrupted receipt publication can finish
  only with current authority. A versioned tag targets the final version integration.

The full worker `TestTagOnly*` selection passed 10 repeated runs. Master/worker
`Test(MasterTagOnly|TagOnly)*` passed three race-enabled runs in Flox using the
existing GCC 15.3 wrapper, explicit `umask 022` and dropped DAC override/search
capabilities. This includes the now-hooked exact checkout-stage guard. This author
changed only tag-only implementation/tests and this note, not central or
`native_retained*` files. No live GitHub writes, commit or push were performed.

## Limits

External GitHub/master authority is mocked. The source retained delivery executes
the native helper against those mocks; the version task is seeded as independently
delivered. The mock master validation is not a substitute for the separate master
tests of exact source/version native completion authorization receipts. These tests
do not inspect or authorize a live source, backfill completion provenance, or test
live GitHub annotated-object conformance.

Only exact source integration and the fixed `coop-go-version-v1` and
`gh-sweep-version-v1` version payload contracts are supported. See
[native digest commands and the actual PR9 shell contract](native-authority-digests.md).
Tag-only does not authorize merge, task rebind/reopen,
release workflows/assets, installation, deployment or restart. No cross-placement
journal adoption, release receipt or released-artifact QA is claimed.
