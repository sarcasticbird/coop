# Persistent-agent assessment

Assessment date: 2026-10-14. Task: [#26](https://github.com/sarcasticbird/coop/issues/26).
Approved audit/planning goal: https://github.com/sarcasticbirds/coop-coordination/discussions/4.

Production anchor: `04e6dff288153f07fd4c44be8cc6dbaec4b8439a`.
Diff base: `f15d258bc5cf881271c6527483f5b2e9b6d7288e` (`main` at intake).
The audit branch fast-forwards that base to the supplied feature anchor, then
adds only this report and assessment probes. It implements no findings. The PR
is an exact-head review target for the feature diff and assessment, not evidence
that the feature is ready to merge. Hawk must independently review the current
PR head and record an explicit conclusion. This report does not supply that
independent conclusion or authorize feature delivery. Hawk recorded **changes
needed** for audit head `0f897bb4ca265fcef059e3a00486d7ac083ef5c7` in
[review 5525072164](https://github.com/sarcasticbird/coop/pull/41#pullrequestreview-5525072164).
This revision corrects and extends the assessment in response; it needs a new
independent current-head review.

No repository `AGENTS.md` was present. README, security/runtime/agent/worker and
credential documentation and the Flox manifest informed the assessment. All Go
commands used `flox activate --`. Validation ran as root on Linux arm64 with
Flox `1.18.1-gf264cf2` and Go `1.26.6`.

## Architecture and boundaries

- The host resolves local configuration, selects named profiles, acquires
  credentials, mints installation tokens and manages the Apple Container VM.
  Signing keys stay host-side. Named profiles remove interactive project mounts,
  home sharing and SSH forwarding; their persistent state is separate.
- GitHub App minting verifies App/installation owner, permissions and expiry,
  refuses redirects, and stages renewable owner-specific guest bundles. The guest
  helper selects one installation owner per operation. Interactive leases use
  temporary paths; named-agent generations intentionally persist until shutdown.
- Independent helper services run the native OpenCode server and GitHub worker.
  Server readiness, heartbeat, last poll, pause and wait reasons are separate
  observables. Detaching a terminal does not stop the services; VM stop, host sleep
  and connectivity loss interrupt operation. No live service-health claim is made
  by this source assessment.
- The worker catalogs GitHub goals/tasks/PRs and Projects, evaluates admission,
  persists job/submission identities, prepares owned checkouts, then submits and
  observes native turns. Task dispatch refreshes authorization after preparation.
  Artifacts and current-head review/check state, not model prose, prove completion.
- Owner, implementer and reviewer authority uses immutable actor ID/type. Goal
  evaluation uses the observed body hash/edit time; host approval comments bind
  an explicit hash, but ordinary approval statements do not. Current title/body
  hashes and trusted
  metadata bind tasks to goals/dependencies. Review/fix/delivery jobs retain exact
  head identity. Merged delivery additionally requires completed issue state.
- Discussion mentions/replies and host pings use a durable wake ledger, baseline,
  idempotency/coalescing and peer-hop limits. Conversation sessions are separate
  from task checkouts. Their prohibition on code changes/reviews/merges is a prompt
  restriction, not a separate capability sandbox.
- GitHub prose is quoted input, not identity or approval authority. Worker-owned
  sessions receive broad tool permissions with credential-pattern denies. Guest
  root can access staged tokens, modify tools and access the network. These rules
  reduce accidental disclosure; they do not make malicious repositories or
  prompt-injected guest execution safe. That is a documented non-boundary, not a
  newly demonstrated credential escape.

## Verified findings

Locations below refer to the production anchor. Each linked issue includes
acceptance criteria, priority and dependencies. Existing issues were checked
before publication; only #26 existed in the issue catalog. No implementation
task metadata or Ready status was created for findings.

Assessment tests named `TestAudit*` deliberately assert the observed behavior at
the anchor. A passing assessment test means reproduction succeeded, **not** that
the defect was fixed. Follow-up fixes must replace these assertions with desired
behavior rather than preserve the defect as a regression expectation.

| Finding | Severity / priority | Anchor location | Reproduction test | Tracking |
| --- | --- | --- | --- | --- |
| PR repository is not bound to the approved task repository | High / P1 | `internal/worker/select.go:57,112-126`; `revalidate.go:129-145` | `TestAuditCrossRepositoryPRAdmission` | [#29](https://github.com/sarcasticbird/coop/issues/29) |
| Fresh wake source is paired with cached approval | Medium / P2 | `internal/worker/github_wake.go:53-74`; `wake_engine.go:315-340`; `wake_prompt.go:45-49` | `TestAuditWakePromptUsesStaleApproval` | [#35](https://github.com/sarcasticbird/coop/issues/35) |
| Historical malformed trusted metadata defeats a valid repair | Medium / P2 | `internal/worker/metadata.go:74-105`; `github_read.go:289-292` | `TestAuditMalformedMetadataPoisonsLaterRepair` | [#27](https://github.com/sarcasticbird/coop/issues/27) |
| Permanent observation failure occupies the slot in all active submission states | Medium / P2 | `internal/worker/engine.go:282-297,497-503,712-718`; `opencode.go:301-303` | `TestAuditPermanentObservationFailureStaysActiveAfterDeadlineAndRestart` | [#28](https://github.com/sarcasticbird/coop/issues/28) |
| Pause aborts a later manual turn in the saved session | Medium / P2 | `internal/worker/engine.go:235-250`; `opencode.go:360-375` | `TestAuditPauseAbortsLaterManualTurn` | [#32](https://github.com/sarcasticbird/coop/issues/32) |
| Clone-before-branch interruption cannot recover | Medium / P2 | `internal/worker/checkout.go:117-153` | `TestAuditCloneBeforeSwitchCannotRecover` | [#36](https://github.com/sarcasticbird/coop/issues/36) |
| Descendant survives inherited-pipe timeout | Medium / P2 | `internal/worker/process_unix.go:80-103` | `TestAuditExecRunnerLeavesChildAfterParentExit` | [#33](https://github.com/sarcasticbird/coop/issues/33) |
| Progress fingerprint omits executable mode changes | Low / P3 | `internal/worker/checkout.go:201-245`; `engine.go:350-361` | `TestAuditProgressIgnoresExecutableBitOnlyChange` | [#31](https://github.com/sarcasticbird/coop/issues/31) |
| Untracked config link loads tracked backing bytes | Medium / P2 | `internal/project/resolve.go:193-196`; `internal/config/config.go:232-235,287-288` | `TestAuditLocalMarkerCanLoadTrackedSymlinkTarget` | [#37](https://github.com/sarcasticbird/coop/issues/37) |
| Named acquisition executes project-controlled PATH helper | Medium, conditional / P2 | `internal/agent/session.go:60`; `internal/credential/acquire.go:286-303,452-480,571-574` | `TestAuditNamedAgentExecutesProjectPATHHelper` | [#34](https://github.com/sarcasticbird/coop/issues/34) |
| gh alias bypasses accidental credential-printing guard | Low / P3 | `internal/githubapp/route.go:60-62`; `cmd/coop-github/main.go:135-148` | `TestAuditAliasBypassesCredentialPrintingGuard` | [#30](https://github.com/sarcasticbird/coop/issues/30) |
| Ordinary approval can authorize a stale broader goal body | High / P1 | `internal/worker/github_read.go:356-375`; `approval_input.go:47-54,81-84`; `approval.go:107-116` | `TestAuditOrdinaryApprovalApprovesStaleBroadScope` | [#45](https://github.com/sarcasticbird/coop/issues/45) |
| PR metadata retargeting reuses review from another task/goal | Medium / P2 | `internal/worker/github_read.go:469,473-478`; `revalidate.go:153-155`; `select.go:37-50,116-126` | `TestAuditPRMetadataRetargetReusesEarlierReview` | [#47](https://github.com/sarcasticbird/coop/issues/47) |
| Failed checkpoint mutates live state before persistence | Medium / P2 | `internal/worker/engine.go:84-85,497-503` | `TestAuditFailedCheckpointMutatesLiveJobBeforePersistence` | [#42](https://github.com/sarcasticbird/coop/issues/42) |
| Interrupted installer publishes mixed generations and refuses recovery | Medium / P2 | `scripts/install_agent_worker.py:37-39,54-55,61-84` | Four `AuditWorkerInstall` Python tests | [#43](https://github.com/sarcasticbird/coop/issues/43) |
| Backup ignores changed post-copy snapshot contents | Medium / P2 | `internal/backup/archive.go:186-199,201-254` | `TestAuditBackupPublishesDespiteChangedPostcopySnapshot` | [#44](https://github.com/sarcasticbird/coop/issues/44) |
| Failed maintenance leaves incoherent ownership and blocks retries | Medium / P2 | `internal/backup/storage.go:381-384,441-443,460-509` | `TestAuditMaintenanceFailureLeavesOldRecordAndBlocksRetries` | [#46](https://github.com/sarcasticbird/coop/issues/46) |

### Authorization and metadata

A PR in an unapproved repository can link an approved task in another repository
and be admitted for delivery if trusted implementer authorship, independent
current-head approval, checks and CLEAN state are present. Installation-wide PR
discovery supplies such inputs. The planner checkout guard at
`checkout.go:154-158` rejects the normal automatic review path, mitigating that
case, but implementer delivery of an already formally reviewed PR lacks the
binding. The fixture verifies admission, not an actual unauthorized merge.

Wake source validation reads a fresh Discussion twice, but the prompt evaluates
the cached full snapshot. A changed body or new pause/rejection can coexist with
an old approved decision. Administrative repair and operator reports can thus
use stale authority. Dedicated task authorization still revalidates separately;
this probe does not establish a task-admission bypass.

Task metadata parsing visits comments oldest-first and immediately returns on
malformed trusted metadata. A later valid replacement cannot repair admission.
Untrusted outsider comments are ignored. Recovery currently requires editing or
deleting the malformed trusted comment rather than adding corrected metadata.

PR scope freshness has a different gap: editing only the reviewed PR body's
task/goal links keeps the head and formal review unchanged. `readPR` records the
new `updatedAt`, but `bindReviewScope` uses only the linked task's metadata time.
If a different admissible task/approved goal has metadata predating that review,
delivery selection reuses the approval for acceptance criteria the reviewer did
not review. The actual PR/read-review mock verifies retargeted delivery admission;
using post-review scope time in the fixture control blocks it. No live PR was
retargeted or merged. A fix needs scope-specific evidence, not blind invalidation
on every unrelated PR update.

### Recovery and execution

Permanent observation failure is broader than a never-posted intent. A real HTTP
client fixture returns message 404 for each saved state: submitting, unknown,
accepted and running. Each survives two restarts and six expired ticks, producing
six successful aborts and six 404s while remaining active with zero attempts.
Unknown state reports a blocker but still retains the slot. A deleted/lost
message after accepted work can therefore block the sole task/wake slot too.
A 404 does not prove the POST never happened or that no side effect occurred;
safe bounded escalation/recovery must not blindly replay uncertain work.

One known trigger remains termination after saving active/submitting intent but
before POST. Another is a failed checkpoint without any process termination:
`saveJob` updates the live map before persistence succeeds. An injected failed
pre-POST save leaves the durable fixture preparing, the live job active, and zero
POSTs. Three subsequent expired ticks abort but keep the slot active without
retry. This separately violates checkpoint coherence (#42), even if observation
recovery (#28) is improved.

If an owned turn finishes and a human starts a later turn in the same session
between polls, local pause still sends session-wide abort without checking latest
message ownership. The mock later manual turn is canceled. This needs native
server validation and ownership-safe treatment on other interruption paths too.

A real local clone with an injected failure before `git switch -c` leaves the
owned checkout on its default branch. Three healthy retries reject it as
`checkout branch changed`. This is incomplete initialization, distinct from a
genuine foreign or manually changed checkout, which must remain protected.

A synthetic shell spawns `sleep 30`, retains pipes in its child, then exits zero.
The runner reports a WaitDelay failure while the child remains alive; probe
cleanup kills it. Retried commands can overlap surviving work. Separately, real
Git reports an executable-mode-only change while progress hashes stay identical,
so an otherwise useful uncommitted repair consumes the no-progress retry budget.

### Configuration and credentials

An untracked `.coop.toml` symlink to tracked `settings.toml` passes the marker
guard and loads its bytes. A user or guest process must first create the local
link; ordinary tracked markers are rejected. Thereafter repository updates can
change full-authority configuration. The affected guard predates the feature.
The harmless fixture verifies loading, not an actual capability grant.

Named agents construct a credential manager without a project root. A bare
helper resolves through an absolute project directory inherited in PATH even
though execution changes to host home. The same helper is refused by an
interactive manager with project-root containment. This requires a host PATH
including the checkout; merely entering a directory is insufficient. Only a
synthetic helper was executed, without host-secret access.

The gh router blocks direct `auth token`, but permits alias setup and invocation.
Core gh expands an alias after token injection. A fixture with isolated temporary
CLI config prints only a synthetic noncredential value. This defeats a documented
accidental-disclosure guard, not the guest-root confidentiality boundary.

### Installation and backup

The installer renames five live artifacts sequentially and publishes the
manifest last. Isolated tests execute the production guest script with only
temporary destination substitutions and harmless packages. SIGKILL after the
first rename leaves a new worker with old launchers/config and the old manifest;
retry refuses the checksum mismatch. First-install interruption instead leaves
an unmanaged artifact that retry refuses. EXIT rollback cannot handle SIGKILL,
and a trappable error between rename and its installed marker also escapes
rollback. A control error after a recorded marker rolls back successfully.
Neither the running guest installation nor services were touched. Preserve
verified prior-generation recovery material; deleting the manifest alone is not
a supported repair and does not make the artifact set coherent.

Backup checks individual opened-file size/mtime, but discards the successful
second snapshot's contents. Four fixture changes (added volume, removed root,
changed size and metadata) still publish an archive describing the initial
snapshot. Checksums validate copied bytes, not completeness of the snapshot set.
These are consistency-check fault injections, not proof that every stopped VM
backup races in practice.

Maintenance removes/recreates the runtime and restores disks before publishing
ownership. A real FileStore/filesystem fixture injects missing metadata after
recreation: the disk is fresh, but the old ownership record remains. Ordinary
restore refuses that existing record; maintenance retry of the original archive
refuses the recreated target's new fingerprint. Mock Apple commands cover this
failure path without touching a VM. Preserve the original archive and stopped
target; rerunning `apply` first makes another backup and could capture damaged
state. A supported ownership-safe rollback/retry protocol is required; manual
recovery instructions were not validated by these probes.

## Approval propagation

`TestAuditValidApprovalTemporarilyUnapproved` mocks the actual separate Discussion
list/comments API calls (`github_read.go:358-375`). The first response lists an
old goal body; the next returns a valid owner approval for its newly edited body.
Hash evaluation (`approval.go:79-85,107-116`) conservatively returns `unapproved`.
The next consistent read returns `approved` with the **same approval comment**.

That **hash-bound host approval** case demonstrates conservative eventual
consistency, not an invalid approval or forged authority. It must not be
generalized to all accepted approval forms, as my initial report did.

`TestAuditOrdinaryApprovalApprovesStaleBroadScope` combines an old body approving
two repositories with a new ordinary owner decision after the body narrowed to
one. Both `/coop approve` and `approved` authorize the stale broader body and
admit a task in the excluded repository. A consistent fresh body excludes that
task, and the hash-bound control fails closed. This is a **High-severity
authorization defect** tracked as required fix #45, not only an observability
recommendation. The probe verifies selection, not end-to-end dispatch through
revalidation; the latter also uses separate goal/decision reads and needs
consistent-version guarantees.

Future-dated comments are also deferred until the guest clock catches up. The
historical reported incident was not replayed from production logs, so its
specific cause is not established. The hash-bound case does not justify editing
the goal or seeking another approval merely because one stale poll said
unapproved, while the ordinary-approval case requires an authorization fix.

Status currently collapses this to `owner_approval` (`status.go:100-106`).
[Recommendation #38](https://github.com/sarcasticbird/coop/issues/38) asks for
pending re-evaluation guidance for the hash-bound case, while preserving genuine
authorization blockers after a consistent fresh read. Its scope was corrected
to reference #45 and not characterize all mixed reads as fail-closed. #35 covers
the distinct
defect of quoting cached authority alongside a freshly revalidated wake source.

## Validation evidence

Commands ran against the anchor plus assessment-only tests:

```sh
flox activate -- go test ./...
flox activate -- go test -race ./...
flox activate -- go vet ./...
flox activate -- go mod verify
flox activate -- env GOOS=darwin GOARCH=arm64 go build ./...
flox activate -- go test ./internal/worker ./internal/credential ./internal/githubapp ./internal/project -run '^TestAudit' -count=1 -v
flox activate -- bash -c 'umask 022; go test -race ./internal/... ./cmd/coop ./cmd/coop-worker -count=1'
flox activate -- go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

After review, the new and broadened probes were validated with:

```sh
flox activate -- go test ./internal/worker ./internal/backup ./internal/credential ./internal/githubapp ./internal/project -run '^TestAudit' -count=1 -v
flox activate -- bash -c 'umask 022; go test -race ./internal/worker ./internal/backup ./internal/workspace ./internal/project ./internal/credential ./internal/githubapp -count=1'
flox activate -- /nix/store/3inr3nxzqarhb9rh1z7s2s20j8c0395h-python3-3.14.7/bin/python3.14 -B scripts/test_audit_worker_install.py -v
```

- Seventeen Go assessment tests and four Python installer tests pass, including
  their approval-form, active-state and backup-change subcases. The fixed-core-gh
  alias fixture is not skipped on this guest. They cover seventeen defects plus
  the hash-bound temporary-unapproval case and installer rollback controls. Tests
  use temporary repositories/filesystems, synthetic credentials and HTTP/GitHub
  mocks; they do not modify live goals, credentials or native turns.
- An existing Nix-store Python 3.14.7 interpreter was found and invoked explicitly
  through Flox for the isolated installer crash tests. The project manifest was
  not changed. This does not make `python3` available by default or validate all
  existing packaging/launcher/Flox-upgrade tests, which use `/usr/bin/python3`.
- Vet, module verification and Darwin arm64 cross-build pass. Shell syntax checks
  pass for the install/UAT/upgrade/OpenCode scripts. Cross-build is not macOS
  runtime validation. No race was detected in the executed packages.
- Full ordinary and race suites fail in four package groups: installer requires
  unavailable `python3`; image PATH fixture assumes no installed
  `/opt/coop-release-tools/bin`; root bypasses chmod-based unreadability; restrictive
  inherited umask changes expected modes in projectinit/seed fixtures.
- With `umask 022`, all internal packages plus `cmd/coop` and `cmd/coop-worker`
  pass their uncached race suites except the root-unreadability projectinit test.
  Both mode failures disappear. These are environment/test assumptions, not four
  independently verified production regressions. [Recommendation #40](https://github.com/sarcasticbird/coop/issues/40)
  tracks contributor-environment and fixture reproducibility.
- The vulnerability tool resolved to `golang.org/x/vuln v1.9.0`. It exits with
  findings (wrapped `go run` exit 1, scanner exit 3), reporting nine stdlib symbol
  advisories fixed in Go 1.26.9: GO-2026-6617, 6613, 6612, 6611, 6610, 6608, 6607,
  6605 and 6603. It also reports two imported-package and two required-module
  advisories without detected vulnerable calls. These are scanner results, not
  nine confirmed exploits. Server-side advisories have broad interface-derived
  traces despite Coop using HTTP clients; client HTTP/2/CONNECT/textproto paths
  need applicability review. [Recommendation #39](https://github.com/sarcasticbird/coop/issues/39)
  tracks advisory triage and a patched supported toolchain. The scan added no
  project dependency or toolchain changes.
- Hosted [run 38641704999](https://github.com/sarcasticbird/coop/actions/runs/38641704999)
  on prior audit head `0f897bb` passed formatting, build, tests, vet and lint;
  its test job failed specifically at govulncheck. This is distinct from the local
  guest fixture failures. The assessment does not fix or bypass that required
  security check; a later head has its own check state.

Existing tests provide positive evidence for actor/type and scope/hash checks,
conditional/quoted/future approval rejection, dismissed/stale review rejection,
failed/unknown check rejection, merged-issue reconciliation, accepted-response-loss
recovery, exclusive stores, atomic saves/corruption rejection, concurrent wake
enqueue/idempotency, dirty/foreign checkout preservation, backup payload checks
before mutation, and workspace/archive traversal/symlink rejection. Passing these
does not cover every production interleaving or hostile path replacement.

## Recommendations and remaining limits

The seventeen verified findings are scoped fixes of varying severity, not permission
to silently expand the product. #38, #39 and #40 are separately identified
observability, dependency-hygiene and validation recommendations. Hawk should
validate/deduplicate these against independent findings, maintain the approved
Project and determine dependencies/admission before implementation.

No Apple Container VM lifecycle, live mint/rotation/revocation, sleep/connectivity
recovery, backup restore into a real VM, native OpenCode ownership/reconciliation,
full Python packaging/upgrade suite, successful native `wt` checkout, power-loss/ENOSPC
injection or adversarial path-replacement race was exercised. Backup/workspace
inspection was narrower than worker inspection, but installer SIGKILL and partial
backup-maintenance filesystem faults are now explicitly exercised. Third-party
agent/runtime supply
chains beyond Go modules were not comprehensively scanned. Permission-error
classification on live GraphQL revocation remains unverified; it is not reported
as a confirmed defect. Root guest permissions and prompt-based role constraints
remain explicit residual trust assumptions.

No service restart, deployment, merge, secret access or real credential printing
was part of the assessment. Hawk's prior-head changes-needed conclusion is
recorded above; independent re-review of the corrected current head and any later
delivery remain outstanding.
