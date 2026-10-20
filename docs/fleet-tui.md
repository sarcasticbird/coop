# Fleet TUI

`coop tui` brings fleet operations, server references and private master
coordination into one terminal. This is the consolidated issue #94 operator
reference. This document is not evidence that live UAT has passed.

## Architecture And Authority

The Bubble Tea event loop renders snapshots and dispatches through injected
services; it is not a new scheduler, credential store or authorization service.
There are two deliberately separate backend paths:

- **Fleet/Servers:** `internal/hostrouter` combines local discovery and registered
  SSH hosts. `cmd/coop/tui.go` reexecutes the same binary through the validated
  `tui-exec` receiver locally; remote actions use the existing SSH contract and
  receiving host's Coop. Normal CLI ownership, lifecycle and recovery checks apply.
- **Coordination:** the configured `internal/masterclient` talks to the master
  API. The private client selects a stable master ID, URL and credential
  reference. Thread/message/goal actions use that master, not whichever Fleet row
  or Servers host happens to be selected. Master placement/presence is not a
  lifecycle route. An offline guest may receive queued messages without starting.

Select a client using `coop --master-config /absolute/private/master-client.toml tui`.
The CLI default is `$XDG_CONFIG_HOME/coop/master-client.toml`, falling back to
`$HOME/.config/coop/master-client.toml`. Use an existing private `token_file` or
`token_env` reference, never paste credentials into a form. Missing configuration
or master failure must remain an explicit unavailable state, not a switch to
GitHub or another master. Remote HTTPS verifies certificates and the pinned
master identity; the protocol sends `X-Coop-Expected-Master-ID` and refuses
redirects. Explicit loopback HTTP is for disposable tests only.

Host identity is the complete registered name/address plus target kind and ID.
Duplicate display names across hosts or kinds are not interchangeable. Forms
capture that identity and host-owned project path; refresh, reordering or a
changed registration must not retarget a pending action. The CLI receiver
revalidates before dispatch. Failed host refresh retains stale rows and age;
unknown is not stopped, absent or permission to mutate. No remote failure falls
back to the controller. Paths and configured grant names resolve on the selected
host, without copying configurations, archives, SSH keys or tokens.

Coordination identity includes master ID, authenticated principal, engagement,
stable member/agent IDs, thread/message IDs and exact goal revision. Aliases are
display/routing conveniences, not transferred authority. A goal decision binds
the exact revision SHA256 and observed decision sequence, not a title or row
position. The owner alone decides goals and replaces membership. Agent inbox
reads the authenticated agent's pending deliveries; owner credentials need not
have an agent inbox. Backend denials remain denials even if a form can be opened.

Native master authority and legacy GitHub approval are distinct. `approve` uses
the selected profile's existing backend; it must not silently become a native
goal decision. Conversation text, presence, a receipt, an SSE notification or
an approved goal alone does not satisfy task admission, independent exact-head
engineering review, required QA or GitHub delivery checks.

## Keyboard And Workflows

Keys are contextual. Letter shortcuts navigate outside editors; inside a form,
text is literal. Use `?` for the integrated runtime's contextual help.

| Context | Key | Behavior |
| --- | --- | --- |
| Browser | `1`, `2`, `3` | Fleet, Coordination, Servers respectively |
| Browser | arrows or `j` / `k` | Select a row |
| Browser | `tab` | Focus/show details; in forms, move between fields |
| Browser | `r`, `?`, `esc` | Refresh, help, back/cancel |
| Browser | `PgUp`, `PgDn` | Scroll long rows, details, forms and confirmations |
| Browser | `q`, `Ctrl+C` | Quit; editors handle text contextually |
| Fleet | `enter`, `u`, `s`, `d` | Terminal, start, stop, destroy confirmation |
| Fleet/Servers | `x` | Exact-target or selected-host operations menu |
| Servers | `a`, `e`, `d` | Add, update, remove a server reference |
| Browser | `v` | Services/About, help/version, completion and advanced runbooks |
| Coordination | `t`, `l`, `i`, `g` | Team, Threads, Inbox, Goals sections |
| Coordination | `enter` | Open/select highlighted item |
| Coordination | `n`, `c`, `p` | New thread, compose, reply to selected message; on Team, `p` opens an explicit master prompt form |
| Coordination | `m`, `s`, `a` | Exact membership form, message receipt, exact goal authority |
| Coordination | `o`, `d` | Structured JSON proposal editor, goal decision form |
| Coordination | `r`, `f`, `]`, `esc` | Refresh, thread follow, next bounded page, back |
| Coordination form | `tab`, `enter` | Move fields, insert newline; Enter is not mutation consent |
| Coordination form | `Ctrl+S` twice | Preview captured request, then explicitly submit it |
| Coordination | `Ctrl+R` | Retry the identical retained uncertain request, not a new mutation |
| Private buffer | `Ctrl+V` | Toggle masked/revealed rendering; default is masked |

Host operation forms retain their existing Enter/save and explicit confirmation
rules, including `y` for destructive confirmation. Do not apply those rules to
the two-stage Coordination submit flow. Foreground entry suspends Bubble Tea and
releases its terminal. Remote `Ctrl+]` detaches transport, while `Ctrl+C` reaches
the foreground application. Use the local application's normal exit. On return,
terminal settings are restored and the browser refreshes the same host/target.

### Conversations

Open Threads, page with `]`, select a thread and read/follow its committed
messages. Create a thread with a title, authorized engagement and complete stable
member IDs. Compose a root message with explicit recipients, or reply with the
selected parent ID. With no explicit recipients a reply addresses its parent
author excluding the sender; a root message with no recipients is a note and
wakes nobody. Preview thread, recipients, parent, body and request ID before
submission. Automatic peer replies have a two-hop budget; owner messages start
a new chain. Do not interpret suppression as a transport error.

Membership replacement requires the complete observed old member set and the
complete desired set. A conflict requires a fresh read and a newly reviewed
change, not overwriting concurrent membership. Revocation removes read access
and cancels unstarted deliveries; it does not prove a running model has stopped.

Receipt states are `queued`, `received`, `running`, `replied`, `blocked`,
`cancelled` and `suppressed`. They describe durable message/delivery progress,
not a promise of execution, a substantive reply or goal approval. Team presence
and placement are separate: heartbeats normally arrive every 15 seconds and
become stale after 45 seconds. Inbox/follow/page results are bounded views, not
proof that no other records exist. Refresh after reconnect; follow never sends.
Reader unread markers are local to this TUI session, not durable master delivery
acknowledgments. Enter in the reader marks the loaded sequence read locally.
The reader retains at most 500 messages; reopen the thread to read earlier pages.
Use `r` or `]` to load the next message page, and `f` for two-second polling.

### Goals

Goals lists a bounded page of active approved goals, not every draft or rejected
revision. Use exact authority to inspect a known goal/revision. Proposal JSON
can be typed or pasted into the structured editor; there is no TUI file import.
CLI `goal propose --file` and `message send --body-file` remain CLI-only input
mechanisms, not implied browser file pickers.

Proposal fields follow `coordination.GoalProposal`: optional `goal_id` for a new
revision, purpose, bounded scope, repositories with `name` and `base_branches`,
acceptance criteria, stable `author_id`, independent `peer_id`, and `qa` policy.
Required QA specifies `actor_id`, environment, `environment_revision` and private
`evidence_destination` (`master_id`, `thread_id`, `engagement`). The proposal's
explicit `thread_id` selects its conversation independently of Fleet selection.
The form replaces pasted `request_id` with its retained request identity; inspect
the input and exact request ID before submitting. These fields contribute to the
immutable approved revision hash. Invalid JSON, unknown fields, excessive size
or unauthorized scope must be rejected without a partial proposal.

Read exact authority before deciding. The decision form explicitly selects
approve/reject/pause/resume/complete/reopen and captures goal ID, revision hash,
observed decision sequence and request ID. Conflicts require another authority
read and fresh intent. A new draft does not revoke the old approval; approving
a replacement supersedes it and requires explicit unfinished-work rebinding and
fresh review/QA. Completed work does not reopen automatically.

### Privacy And Uncertainty

Private buffers render masked by default. Reveal deliberately with `Ctrl+V` only
in a private terminal; masking is not encryption, an authorization boundary or
a guarantee that terminal recording is safe. Never enter credentials. Avoid
screenshots, shared scrollback and terminal recordings containing messages,
proposal text, principals, host paths or configuration. Keep real evidence in
the approved private destination, not this repository or public GitHub comments.

Coordination sends use a request ID bound to immutable input. Preview is not
submission. If the response is lost after submission, the outcome is uncertain:
inspect receipt/authority where available. `Ctrl+R` retries only the same retained
request ID and payload. Do not edit and reuse an old ID, create a duplicate with
a fresh ID, or treat cancellation as rollback. No mutation retries automatically.
Host operations have no equivalent general idempotent replay guarantee: inspect
and refresh after cancellation/disconnect before considering another action.

## Command Coverage Inventory

One row per visible executable Cobra command, including default entry and
generated help/completion. `TestFleetTUICommandCoverageInventory` constructs
`root()`, initializes Cobra with `--help` without running handlers, then checks
this marked table in both directions for missing, duplicate and stale rows.
Coverage means an explicit UI or CLI/runbook disposition, not that every command
has an embedded form. Flags and positional agent argv are not separate commands.

<!-- fleet-tui-command-inventory:start -->
| Current command | Disposition | Representation and boundary |
| --- | --- | --- |
| `coop` | UI | Default shell or agent argv entry through Fleet terminal; normal session and credential handling |
| `shell` | UI | Fleet Enter or enter operation, real local terminal or remote SSH PTY |
| `tui` | UI | Launch browser; no nested TUI action |
| `ls` | UI | Fleet local inventory; original CLI remains local without host selection |
| `inventory` | UI | Combined local/registered-host snapshots with independent stale/error health |
| `status` | UI | Selected target state and status operation output |
| `up` | UI | Fleet start, normal lifecycle and ownership checks |
| `down` | UI | Fleet stop; state volumes persist |
| `destroy` | UI | Exact host/kind/ID confirmation and explicit state-volume deletion consent |
| `rebuild` | UI | Target image maintenance, no silent guest replacement |
| `apply` | UI | Stopped-target maintenance with archive/lock safeguards |
| `upgrade` | UI | Explicit selected-host core upgrade; existing Coops become stale |
| `sync` | UI | Agent foreground operation; may start guest/acquire configured grants |
| `backup` | UI | Stopped-target archive on selected host; private but unencrypted |
| `restore` | UI | Absent-destination host form; host-owned archive, existing recovery checks |
| `init` | UI | Host bootstrap with absolute project directory and existing interactive consent |
| `doctor` | UI | Selected-host diagnostics; warnings differ from failures/nonzero exit |
| `credentials refresh` | UI | Agent foreground CLI; grant references only, never secret fields |
| `credentials watch` | UI | Foreground single-profile watcher; Ctrl+C ends it, no new daemon |
| `worker status` | UI | Running configured agent worker status, not master presence |
| `worker requests` | UI | Existing worker requests output |
| `worker pause` | UI | Confirmed agent worker control, not goal or master reconciliation pause |
| `worker resume` | UI | Confirmed agent worker control, existing policy checks |
| `ping` | UI | Literal prompt and stable request ID; selected profile chooses backend, receipt is not approval |
| `approve` | CLI/runbook | Legacy/profile approval contract; never silently mapped to native goal authority |
| `server list` | UI | Private named server references |
| `server add` | UI | Add validated SSH destination reference, no provisioning |
| `server update` | UI | Edit captured registration; changed address invalidates pending selection |
| `server remove` | UI | Confirm reference removal, not remote deletion |
| `team` | UI | Coordination Team presence/placement; CLI --watch remains read-only |
| `inbox` | UI | Authenticated agent's pending delivery page; owner denial is not an empty inbox |
| `thread list` | UI | Threads with continuation cursor |
| `thread create` | UI | New thread, engagement and stable member set, preview/submit |
| `thread read` | UI | Thread detail, committed messages, pagination and follow |
| `thread members` | UI | Owner-only exact old/new member sets |
| `message send` | UI | Private compose/reply and immutable request retry; no TUI body-file import |
| `message status` | UI | Exact message receipt lookup |
| `goal propose` | UI | Structured JSON editor, immutable revision; no TUI file import |
| `goal list` | UI | Bounded active approved goals page |
| `goal authority` | UI | Exact goal/revision authority and observed decision sequence |
| `goal approve` | UI | Owner decision form with exact revision/sequence |
| `goal reject` | UI | Owner decision form with exact revision/sequence |
| `goal pause` | UI | Owner decision form, not worker pause |
| `goal resume` | UI | Owner decision form with fresh authority |
| `goal complete` | UI | Explicit owner completion, not an inferred task outcome |
| `goal reopen` | UI | Explicit owner reopen, never automatic |
| `mcp register` | UI | Advanced host form, private config and existing canonical project; ownership validated |
| `mcp inspect` | UI | Advanced host operation metadata inspection with config/operation references |
| `mcp serve` | CLI/runbook | Explicit host-local control service; never started by browser |
| `mcp stdio` | CLI/runbook | Explicit MCP client transport; not a nested foreground browser service |
| `master serve` | CLI/runbook | Explicit private master API/SSE/MCP service lifecycle |
| `master backup` | CLI/runbook | Master-host consistent recovery snapshot, distinct from guest archives |
| `master restore` | CLI/runbook | Offline empty-destination restore and new epoch, separate recovery authorization |
| `master reconcile status` | CLI/runbook | Recovery reconciliation status, not guest worker status |
| `master reconcile pause` | CLI/runbook | Explicit master reconciliation admission pause |
| `master reconcile resume` | CLI/runbook | Resume only after current-epoch evidence resolution |
| `master reconcile evidence` | CLI/runbook | Inspect retained guest evidence under recovery authority |
| `master reconcile resolve` | CLI/runbook | Explicit evidence disposition; no replay through UI |
| `master placement set` | CLI/runbook | Reconciled stopped placement and explicit next generation |
| `master history import` | CLI/runbook | Explicit read-only history import with source/digest, no wakes/decisions |
| `master history list` | CLI/runbook | Inspect imported history, not live thread authority |
| `master cutover` | CLI/runbook | Explicit source retirement, no implicit migration |
| `help` | CLI/runbook | CLI command help; contextual keyboard help via ? |
| `completion bash` | CLI/runbook | Generate for operator's shell, do not execute in Bubble Tea |
| `completion zsh` | CLI/runbook | Generate for operator's shell, do not execute in Bubble Tea |
| `completion fish` | CLI/runbook | Generate for operator's shell, do not execute in Bubble Tea |
| `completion powershell` | CLI/runbook | Generate for operator's shell, do not execute in Bubble Tea |
<!-- fleet-tui-command-inventory:end -->

`--help` and `--version` remain CLI flags; `?` and `v` expose keyboard help and
build/runbook context. Non-executable command groups are navigation, not actions.
Hidden `host-exec` and `tui-exec` are validated transport adapters, not operator
commands; neither authorizes recursive routing or a nested browser.

## Services And Recovery

The `v` runbook is documentation, not a launch button. `mcp serve` manages the
existing host-local control plane; `mcp stdio` is an explicit client connection;
`master serve --config /absolute/private/master.toml` runs coordination, not VM
control. The operator owns their service manager, private configuration and
lifetime. Merely opening the TUI starts none of these services. It does not
deploy, install, restart, expand grants, migrate workers or cut over history.

Use [MCP](mcp.md), [master operations/recovery](master.md),
[workers](workers.md), [credentials](credentials.md), [remote routing](servers.md)
and [fleet operations](tui-operations.md) for their existing CLI contracts.
Master restore/reconciliation needs offline storage, epochs, placement generations
and verified guest evidence; it is not guest restore or worker pause/resume.
Do not point old binaries at converted state or infer authority from a browser
snapshot. Shell completion is generated by the CLI for deliberate shell setup;
the browser does not modify shell startup files.

## Reproducible Acceptance

From a clean exact-head checkout, use the repository environment:

```sh
flox activate -- bash internal/tui/testdata/uat-fleet-coordination.sh FULL_COMMIT_SHA --operator-checklist
```

The script validates only local deterministic fixtures and prints a manual
checklist. It never invokes a live Coop CLI, reads private client configuration,
connects to registered hosts, starts persistent services or dispatches live
mutations. Fixture tests may use temporary loopback servers and PTYs. Record the
exact integrated backend/UI heads, toolchain, platform, terminal dimensions and
environment revision `fleet-tui-uat-v1` privately. Use 120x40 and 40x20 views.
An uncommitted author checkout cannot pass the script's clean-artifact gate;
targeted tests during development are not an exact-artifact acceptance run.
The race stage requires CGO and the C compiler selected by `go env CC` on PATH;
Flox activation alone may not supply that compiler. A missing compiler is a
blocked validation stage, not a reason to omit race checks or claim acceptance.

Operator-assisted Blackbook/rack-mini UAT is **pending**, as are independently
recorded exact-head engineering review and Wren customer QA where required.
Separately authorize disposable fixtures before any manual mutation, fault
injection or terminal attachment. Checklist output and author fixture success
are neither live UAT nor independent QA. Preserve failures, unavailable backend
checks and unperformed steps as pending rather than reporting a blanket pass.
