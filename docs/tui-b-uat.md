# B Runtime UAT Handoff

This is the stage-5 handoff for [task #121](https://github.com/sarcasticbird/coop/issues/121),
approved goal `goal_CJ3FNEQKI6DAL7AKADQKRVUNCT`, revision
`9a400eb725c4a40237cf4f7baf39a23980c21582417827eb7fd9d10c1294ce62`.
Environment revision: **`coop-tui-b-runtime-v1`**. The accepted design remains PR
#111 at `5d79a84031045fe8dd8f3eeb345cadea953e0724`. Historical stage evidence is
append-only in [the runtime manifest](tui-b-runtime.md); this packet supersedes
neither those results nor the older `fleet-tui-uat-v1` scripts' narrower claims.

## Source And Gates

Integrated starting source: `7f14381` (delivered stage 4, PR #129).
The authoritative candidate source is the **full head SHA and immutable source
artifact on this task's published PR**, not a guessed hash embedded in its own
commit. Select that exact SHA before reproduction. A merge SHA or another build
is not automatically the reviewed runtime artifact.

| Stage | Delivered source | Source PR |
| --- | --- | --- |
| 0 baseline | `0c8db5e` | #123 |
| 1 Ledger/Details | `50a791c` | #124 |
| 2 Fleet/Team observations | `96f88d9` | #125 |
| 3a threads/reader | `94e23f7` | #127 |
| 3b composer/drafts/receipts | `fcb8fef` | #128 |
| 4 setup/help/confirmation/return | `7f14381` | #129 |
| 5 integrated handoff | Current task PR exact-head artifact; pending publication | #121 linked PR |

For the selected candidate, collect the exact-head Robin review URL/verdict/SHA,
required CI check URLs/results, and the safe native Wren QA evidence identifier
plus artifact digest and environment revision. These are **pending independent
gates**, not assertions in this source document. Read actual PR reviews/checks;
do not infer clearance from the table or a successful author run. Wren's private
report and operator details go only to the approved native `qa.report_body`
destination. Rook consolidates safe evidence references with this packet after
the gates run. Source delivery does not mean owner UAT or rollout succeeded.

## Reproduce

Use a clean independent checkout of the published full source SHA. From its root:

```sh
git rev-parse HEAD
flox activate -- go version
flox activate -- bash scripts/uat-tui-b.sh FULL_40_CHARACTER_SOURCE_SHA
```

The script refuses mismatched/dirty sources, uses private temporary output, builds
the native binary twice and compares bytes, and records source, platform, toolchain,
module/manifest digests, build command, binary SHA-256 and Go VCS build information.
`manifest.txt` and `validation.log` are written to the printed evidence directory.
It never launches that binary against user configuration or connects to a guest.
All exercised master/SSH/PTY fixtures are synthetic and disposable.

For identical binary digests, use the recorded OS/architecture, Go toolchain,
module inputs and build environment. Flox currently selects Go 1.26.10 through
`GOTOOLCHAIN=auto`; record the actual version, not just the catalog version.
The static native build uses `CGO_ENABLED=0`; race tests separately require an
existing C compiler and `CGO_ENABLED=1`. If necessary pass an already-available
compiler through `flox activate -- env CC=/absolute/path/to/gcc bash ...`.
Do not install packages or alter Flox just to conceal an environment failure.
Any failed command leaves partial evidence and a nonzero exit; absence of
`synthetic_validation=passed` is not a pass. Preserve blocked/failed lanes.

The runner includes the scoped integration suite, repeated cross-workspace and
disposable PTY journeys, CLI inventories, NO_COLOR, build, repository vet and a
fitting race lane. Run CI's pinned lint separately:

```sh
flox activate -- go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...
```

No full-suite-green claim is made by this scoped runner. Historical full-suite
environment failures (root file-mode tests, image PATH, seed modes and worker
snapshot-failure assumptions) are documented in the runtime manifest. Recheck
baseline versus candidate if a new failure appears; that history is not a waiver.

## Acceptance Matrix

Every three-size journey means **120x40, 80x24 and 40x20**, including resize in both
directions. Isolate fixtures with `flox activate -- go test ./internal/tui/... -run
'TestRuntime|TestLedger|TestTeam|TestConversation|TestCompose|TestSetup|TestConfirmation|TestForeground|TestTerminal'
-count=1 -v`. These are model/handler/PTY evidence, not visual operator acceptance.

| Journey / invariant | Synthetic evidence | Independent/operator boundary |
| --- | --- | --- |
| All/no target, grouped local/remote, filter, duplicate names, loading/empty/offline/cache/reconnect | `runtime_baseline_test.go`, `ledger_test.go`, `fleet_health_test.go`, `fleet_scroll_test.go` | Observe all host states without waking anything; do not mistake failure for empty |
| Ledger -> Details -> explicit Connect -> return, stable identity after reorder/removal/registration/placement change | `ledger_test.go`, `fleet_test.go`, `fleet_diagnostics_test.go` | Confirm exact disposable identity and current authority before any attach |
| Lifecycle separate from work/blocker/freshness; missing/denied/skewed/old-epoch reports remain unknown | `team_view_test.go`, `fleet_diagnostics_test.go`, `team/runtime_baseline_test.go` | Compare existing observation pin with actual configured placement privately; never guess a join |
| Thread list -> masked reader -> selected reply/root -> preview -> receipt | `conversations_test.go`, `compose_test.go`, `coordination_test.go` | Synthetic bodies only; receipt is delivery evidence, not proof of execution |
| Keep/Return/Discard, focus/cursor/scroll, frozen request, identical retry after uncertainty | `compose_test.go`, TLS/store `TestCoordinationHTTPRetryAfterCommitPreservesRequest` | No automatic retry, new request or restart recovery promise; draft stays memory-only |
| Kept reply -> Fleet Connect callback -> Servers/help -> original reader/resume | `TestRuntimeIntegrationKeptReplyAcrossWorkspaces` | Model callback does not prove live transport; actual PTY restoration covered separately |
| Pending reader read -> Fleet/Servers -> fault -> reconnect/recovery -> masked return | `TestRuntimeIntegrationDelayedReaderRefresh`, late-result/epoch tests | No workspace hijack, silent retarget, wake or write; stale content cannot regain reveal authority |
| Distinct master/SSH setup, contextual help and all commands | `setup_help_test.go`, `setup_actions_test.go`, CLI command inventories | Existing private references only; no token entry, master switch or trust bypass |
| Full long confirmation, contiguous review, resize resets review, stale intent refuses | `confirmation_test.go`, `setup_actions_test.go` | Read complete target/path/argv/consequence before consent; cancel unsafe live actions |
| Attach normal exit/failure/signal/remote Ctrl+] detach, Ctrl+D forwarding, terminal restore/input return | `foreground_test.go`, `terminal_test.go`, `cmd/coop/remote_terminal_test.go` | Disposable real PTYs are not Blackbook/rack-mini network/guest evidence |
| Light/dark TrueColor, low-color ANSI, Ascii/NO_COLOR; wide/combining/control/long text; bounds/privacy | `TestRuntimeIntegrationColorProfileParity`, conversation/composer sanitization tests | Model text semantics match after stripping ANSI; independent visual contrast and actual terminal capability detection remain to observe |

## Operator Journey

**Pending until actually observed.** Blackbook/rack-mini steps require an operator
to supply and explicitly authorize existing disposable fixtures. This packet does
not authorize production mutation, deployment/restart, credentials/permissions,
release or Homebrew publication. If a safe fixture is unavailable, mark blocked,
not passed. Do not use a real agent to simulate an offline host or kill a service.

1. Privately identify the Blackbook client and rack-mini fixture host, exact source
   and binary digest, terminal/version, OS, environment revision and existing SSH
   reference. Reserve names `tui-b-uat-local` / `tui-b-uat-remote` only as labels for
   operator-supplied disposable targets, not commands to provision anything. Include
   an already-empty and an already-unreachable registration if available. Operator
   confirms these are isolated and approves attach before proceeding.
2. Run only the verified binary with a disposable private HOME/config and existing
   authorized master/SSH references, following `docs/tui-operations.md`. Do not copy
   credentials into the packet. Ensure the fixture master supports the same source's
   expected-membership contract; an older server refusal must not trigger fallback.
3. At each size and light/dark/low-color/NO_COLOR setting, start All/no-target;
   inspect grouped hosts, duplicate labels and explicit filter/hidden count. Resize,
   page, refresh, open Details and return. Observe stale/offline data as unknown,
   and verify no reading action wakes a fixture. Inspect Team work/blocker/freshness
   independently of VM lifecycle. Unavailable diagnostic access remains unknown.
4. In a synthetic private thread, verify masked opening, explicit reveal, selected
   reply, root compose, Unicode cursor/focus, Help privacy, complete preview, receipt
   and memory-only Keep/Return/Discard. Keep a draft, navigate Fleet/Servers and
   back, then resize/resume and quit/Return. Never test uncertainty by resending a
   new request; use the synthetic HTTP fixture for lost-response validation.
5. Separately authorize attach to the named disposable target. From Details review
   complete captured host/kind/ID/path/argv, Connect, resize, exit or detach and
   verify browser input, refresh, target and terminal echo restoration. Remote
   Ctrl+] detaches the SSH bridge; Ctrl+D is forwarded. Local applications keep
   their normal exit/detach contract. Simulated disconnect/failure belongs in
   disposable PTY fixtures unless the operator explicitly authorizes a safe fault.
6. Inspect master-versus-SSH setup/help and long removal/lifecycle confirmations
   in the disposable configuration. Cancel before effect. Do not destroy, stop,
   register or modify live fleet resources to manufacture an acceptance result.
7. Privately record observed versus blocked cases, exact artifact and review/check
   identities, dimensions/themes, safe evidence IDs and operator decision. Owner
   UAT success remains pending until explicit owner/operator verification. Keep
   rollout, service deployment/restart, release and tap update separately gated.

Do not publish terminal captures containing private bodies, drafts, addresses,
credentials or raw errors. Public handoff summaries contain only safe fixture
labels, exact source/artifact identities, gate status and approved evidence IDs.
