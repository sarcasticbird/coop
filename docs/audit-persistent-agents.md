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
independent conclusion or authorize feature delivery.

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
  approval binds to body hash/edit time; current title/body hashes and trusted
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
| Never-posted submission intent occupies the slot indefinitely | Medium / P2 | `internal/worker/engine.go:497-503,285-296,712-718`; `opencode.go:301-303` | `TestAuditMissingSubmissionStaysActiveAfterDeadlineAndRestart` | [#28](https://github.com/sarcasticbird/coop/issues/28) |
| Pause aborts a later manual turn in the saved session | Medium / P2 | `internal/worker/engine.go:235-250`; `opencode.go:360-375` | `TestAuditPauseAbortsLaterManualTurn` | [#32](https://github.com/sarcasticbird/coop/issues/32) |
| Clone-before-branch interruption cannot recover | Medium / P2 | `internal/worker/checkout.go:117-153` | `TestAuditCloneBeforeSwitchCannotRecover` | [#36](https://github.com/sarcasticbird/coop/issues/36) |
| Descendant survives inherited-pipe timeout | Medium / P2 | `internal/worker/process_unix.go:80-103` | `TestAuditExecRunnerLeavesChildAfterParentExit` | [#33](https://github.com/sarcasticbird/coop/issues/33) |
| Progress fingerprint omits executable mode changes | Low / P3 | `internal/worker/checkout.go:201-245`; `engine.go:350-361` | `TestAuditProgressIgnoresExecutableBitOnlyChange` | [#31](https://github.com/sarcasticbird/coop/issues/31) |
| Untracked config link loads tracked backing bytes | Medium / P2 | `internal/project/resolve.go:193-196`; `internal/config/config.go:232-235,287-288` | `TestAuditLocalMarkerCanLoadTrackedSymlinkTarget` | [#37](https://github.com/sarcasticbird/coop/issues/37) |
| Named acquisition executes project-controlled PATH helper | Medium, conditional / P2 | `internal/agent/session.go:60`; `internal/credential/acquire.go:286-303,452-480,571-574` | `TestAuditNamedAgentExecutesProjectPATHHelper` | [#34](https://github.com/sarcasticbird/coop/issues/34) |
| gh alias bypasses accidental credential-printing guard | Low / P3 | `internal/githubapp/route.go:60-62`; `cmd/coop-github/main.go:135-148` | `TestAuditAliasBypassesCredentialPrintingGuard` | [#30](https://github.com/sarcasticbird/coop/issues/30) |

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

### Recovery and execution

Saving active/submitting intent before POST closes the accepted-but-response-lost
window but leaves another window: termination before POST. A real HTTP client
fixture returns 404 for that saved message. Two restarts and six expired ticks
produce six successful aborts and six 404s, remain active with zero attempts, and
never free the sole task/wake slot.

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

## Approval propagation

`TestAuditValidApprovalTemporarilyUnapproved` mocks the actual separate Discussion
list/comments API calls (`github_read.go:358-375`). The first response lists an
old goal body; the next returns a valid owner approval for its newly edited body.
Hash evaluation (`approval.go:79-85,107-116`) conservatively returns `unapproved`.
The next consistent read returns `approved` with the **same approval comment**.

This demonstrates non-atomic reads and fail-closed eventual consistency, not an
invalid approval or forged authority. Future-dated comments are also deferred
until the guest clock catches up. The historical reported incident was not
replayed from production logs, so its specific cause is not established. The
available approval record does not justify editing the goal or seeking another
approval merely because one stale poll said unapproved.

Status currently collapses this to `owner_approval` (`status.go:100-106`).
[Recommendation #38](https://github.com/sarcasticbird/coop/issues/38) asks for
pending re-evaluation guidance and observability, while preserving genuine
authorization blockers after a consistent fresh read. #35 covers the distinct
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

- All twelve assessment probes pass, including the fixed-core-gh alias fixture
  (not skipped on this guest). They reproduce eleven defects plus mixed-read
  temporary unapproval. Tests use temporary repositories, synthetic credentials
  and HTTP/GitHub mocks; they do not modify live goals, credentials or native turns.
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

Existing tests provide positive evidence for actor/type and scope/hash checks,
conditional/quoted/future approval rejection, dismissed/stale review rejection,
failed/unknown check rejection, merged-issue reconciliation, accepted-response-loss
recovery, exclusive stores, atomic saves/corruption rejection, concurrent wake
enqueue/idempotency, dirty/foreign checkout preservation, backup payload checks
before mutation, and workspace/archive traversal/symlink rejection. Passing these
does not cover every production interleaving or hostile path replacement.

## Recommendations and remaining limits

The eleven verified findings are scoped fixes of varying severity, not permission
to silently expand the product. #38, #39 and #40 are separately identified
observability, dependency-hygiene and validation recommendations. Hawk should
validate/deduplicate these against independent findings, maintain the approved
Project and determine dependencies/admission before implementation.

No Apple Container VM lifecycle, live mint/rotation/revocation, sleep/connectivity
recovery, backup restore into a real VM, native OpenCode ownership/reconciliation,
Python packaging/upgrade test, successful native `wt` checkout, power-loss/ENOSPC
injection or adversarial path-replacement race was exercised. Backup/workspace
inspection was narrower than worker inspection. Third-party agent/runtime supply
chains beyond Go modules were not comprehensively scanned. Permission-error
classification on live GraphQL revocation remains unverified; it is not reported
as a confirmed defect. Root guest permissions and prompt-based role constraints
remain explicit residual trust assumptions.

No service restart, deployment, merge, secret access or real credential printing
was part of the assessment. Independent Hawk review and any later delivery remain
outstanding at publication.
