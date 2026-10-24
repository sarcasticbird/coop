# Reply Producer Lock

## Reproduction

The reported live failure was a prepared native conversation reply failing to
queue after the five-second producer wait. The native turn finished without a
reply; the operator preserved its terminal evidence and canceled that test
delivery through supported recovery. This source handoff does not replay it or
claim that the live gate is accepted.

On main `1b5959b`, `FlushOutbox` held the shared producer lock while scanning all
retained records. Each record re-read and decoded the entire accepted recovery
snapshot; placement adoption could also be re-read and validated repeatedly.
This was a suspected amplifier until measured, not a proven explanation of the
specific live incident's lock owner or duration.

The isolated fixture in `internal/worker/master_outbox_history_test.go` creates
220 synthetic retired outbox records with 16,000-byte bodies and a 3,624,224-byte
accepted snapshot (about 3.46 MiB, comparable to the reported 3.57 MiB snapshot).
No fleet journal, private conversation or real credential is included.

Measurements on Linux arm64, Go 1.26.10, without race instrumentation:

| Path | Producer hold / reply wait |
| --- | --- |
| Original flush, three runs | 6.373 s, 6.254 s, 6.217 s |
| Repaired flush, three runs | 87.7 ms, 78.2 ms, 94.4 ms |
| Repaired concurrent reply, including 100 ms transport barrier | 230 ms |

`TestQueueMasterReplyDuringFlushWithRetainedHistory` uses a transport barrier on
a synthetic first pending record to ensure concurrent flushing owns the producer
before the prepared reply starts queuing. The retained history follows it. With
the original `master_outbox.go` supplied via Go's `-overlay`, this test fails with
`conflict: native action already running`; with the repair it queues, publishes
exactly one actual reply and makes exactly one model submission. Identical
request identity and changed-body conflict behavior remain unchanged. A later
flush rejects changed/corrupt recovery evidence instead of using a stale cache.
The fixture timings establish this contention mechanism, not live causation or
an upper bound for arbitrary history or remote transport latency.

Run from the source checkout:

```sh
flox activate -- go test ./internal/worker -run '^TestFlushOutboxRetainedHistoryCost$' -count=3 -v
flox activate -- go test -race ./internal/worker -run 'Test(QueueMasterReply|FlushOutbox|MasterRecoveryView|MasterReply|MasterConversation|MasterNativeUnknown|FinalReview|RecoveryUnknown|Native.*Recovery)' -count=1
```

For the red control, keep the repaired source and test, obtain the original
`internal/worker/master_outbox.go` from main `1b5959b` in a separate worktree,
and supply an overlay JSON whose `Replace` maps the absolute current outbox
source path to that original file. Run the concurrent-history test with
`go test -overlay /absolute/private/overlay.json`. Only that file is substituted;
the new operation-local recovery helpers remain available to the test suite.

## Repair Boundary

One flush-local recovery view lazily loads placement adoption and accepted
recovery evidence once under the existing producer lock. It is never shared
between flushes or across an unlock. All original validation, canonical record
matching, ownership/placement/epoch checks, immutable outbox identity, saved
receipts and remote request IDs remain authoritative. Unknown-write holds and
native action journals are unchanged. No lock is bypassed or widened, no wait
limit is increased, and no journals are deleted or automatically replayed.

## Author Validation

Flox Go 1.26.10 on Linux arm64:

- Focused reply, concurrency, ownership/placement, recovery and unknown-write
  race tests passed. The operation-local view test deterministically detects
  repeated accepted-evidence decoding and verifies a fresh view rejects corruption.
- `go build ./...`, `go vet ./...`, golangci-lint v2.12.2 (`0 issues`) and all
  58 Python packaging/recovery fixtures passed.
- `go test -race ./...` completed but is not green in this root-run environment.
  The exact failures also reproduce on untouched `1b5959b`: image
  `TestUserEnvironmentDropsUnsafePathEntries` includes the existing
  `/opt/coop-release-tools/bin` path; projectinit
  `TestDiscoverSkipsUnreadableDirectory` and
  `TestAppendConfigCreatesPrivateFileAndPreservesExistingBytesAndMode/existing`,
  seed `TestAlwaysSeedsAndPreservesMode`, and worker
  `TestMasterMigrationRetriesBeforeBackupPublication` (all three subcases)
  depend on permission/umask behavior. No unrelated fixes are included.

Hosted checks, independent current-head Hawk review and Wren QA remain separate
requirements. Author tests are not peer or QA clearance.

## Operator Gate

This recipe requires separate explicit authority for installation, service
changes and live acceptance. Independent peer review, isolated QA for
`coop-reply-producer-lock-v1`, and exact-head checks precede that handoff.

1. Record the reviewed source SHA, built/installed artifact digest, worker and
   master revisions, placement generation and restore epoch. Confirm the intended
   native profile, thread mapping and current authority through existing read-only
   diagnostics. Preserve the original canceled delivery, terminal native intent,
   recovery evidence and journals. Do not retry its reply or change its request ID.
2. In the existing native intake ticket, add a **new**, uniquely marked native
   comment without an explicit recipient, using the normal authenticated native
   interface. This tests unaddressed intake routing, not an addressed canary.
   Reuse the existing ticket UID, not a replacement task or duplicate adoption.
3. Observe the new event/comment identity and exactly one new mapped master
   delivery to the configured intake agent. Read its actual saved session/message
   intent and placement. Let that new turn prepare its acknowledgement and use
   the ordinary `coop-worker comms reply --config ... --delivery NEW_DELIVERY
   --body-file ...` path. Do not manually substitute a model response or use a
   recovery command to manufacture a successful gate.
4. Record reply queue duration, deterministic outbox request ID, publication
   receipt and native acknowledgement comment. Verify the new delivery reaches
   `replied`, exactly one acknowledgement appears, and no second native submission
   or duplicate remote comment occurs. A `queued`/`publication_pending` receipt
   is not publication success. The old canceled delivery must remain canceled.
5. If contention, placement/epoch mismatch, unknown-write evidence or missing
   publication occurs, stop and retain the exact IDs and original request. Use
   supported read-only diagnostics and operator inspection; do not replay the
   old model turn, renew request IDs or remove history to make the test pass.
   Report the gate as unaccepted with the exact failing stage.

Keep actual fleet IDs, logs, profiles and captured conversation evidence in the
authorized private acceptance thread. Record only sanitized outcomes publicly.
This bounded gate does not substitute for the broader cutover journey in
[consolidation-cutover.md](../consolidation-cutover.md).
