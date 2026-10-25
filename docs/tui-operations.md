# Fleet Operations

`coop tui` shows Fleet, Coordination, and Servers. From Fleet, Enter opens
Details without connecting; Details `x` opens operations for that captured target.
In Servers, `x` opens host-wide operations for the selected registration.
`v` opens the Services/About runbook; `?` shows keyboard help. `PgUp`/`PgDn`
scroll long content, including confirmations, output, and narrow-terminal forms.
F1 opens editor-owned help without showing input, changing fields or discarding
the editor viewport. `?` remains literal text inside an editor.

Master setup and SSH setup are separate. Missing master configuration leaves
Fleet available; configure an existing private `master-client.toml` and its
credential references outside the TUI, then relaunch with `--master-config`.
Servers add/update/remove edits only registered SSH references, such as existing
`~/.ssh/config` aliases. Neither route accepts tokens, switches masters, bypasses
SSH trust, provisions hosts or implicitly starts services.

## Routing And Safety

- A form captures the complete host name/address, target kind/ID, runtime name,
  and project path. Selection changes never retarget it. Confirmation shows the
  captured identity, host destination, paths, and literal argv before dispatch.
  Configured credential references are captured before this preview, not appended
  after consent. Details lifecycle shortcuts require the same consent as the menu.
  Long confirmations and server removals require contiguous page review with
  PgDn before `y` is accepted; resize resets review. Cancellation and foreground
  return retain the original Details/Servers viewport and Fleet scope/focus.
- At dispatch, current authoritative inventory and registration are checked.
  The receiving CLI revalidates the target and project path again. Refresh epochs
  invalidate inventory predating an action. Completion carries the request
  sequence and captured identity; mismatched or duplicate completions are dropped.
- The closed menu builds argv, not shell text. Paths are interpreted on the
  selected host. No remote archive/configuration is opened on the controller and
  no remote failure falls back locally. Secret values are never form inputs.
- State/kind/backend limitations disable menu entries with reasons. Ownership,
  configured worker/grants, master authority and permissions are enforced by
  existing CLI handlers, not invented from inventory. Inventory does not expose
  permission capabilities; a handler denial remains a denial, not a retry hint.
- Operations serialize and show queued, running, succeeded, failed or cancelled
  states. Background commands have a ten-minute deadline and retain at most
  32 KiB of stdout, with terminal controls removed. Escape cancels; cancellation
  or disconnect can leave an unknown outcome. Inspect/refresh before retrying.
  CLI stderr is not captured into the browser. Credentials run in the explicit
  foreground CLI, never a secret-bearing result buffer.
- Ping uses a fresh cryptographic 64-hex request ID per composer; confirmation
  preserves it. The ID is visible for receipt/retry investigation. Acceptance
  does not mean a reply, delivery completion or goal approval. Automatic retry
  is deliberately absent. Prompt input is literal, UTF-8, at most 8192 bytes.
- Foreground actions suspend Bubble Tea, release input and the alternate screen,
  and use a real controlling terminal. Local entry invokes the same CLI receiver
  as other actions. Remote entry/bootstrap use #95's SSH PTY/resize/disconnect
  handler. `Ctrl+]` detaches remote transport; use the local application's normal
  exit/detach. `Ctrl+D` is forwarded to the remote application and may end its
  shell; it is not a local detach shortcut. `Ctrl+C` is delivered to the foreground
  application. An exec wrapper
  restores the pre-child terminal settings even if it exits with echo disabled.
  On return the same host/target is retained and inventory is refreshed.
- Archives are private but unencrypted and can contain guest secrets. Backup
  requires a stopped guest. Restore is a host form for an absent destination,
  with exactly one configured agent profile or absolute project directory.
  Existing CLI cold-storage, checksum, ownership and recovery safeguards remain.
- Services never start implicitly. No deployment, restart, credential expansion,
  production mutation or migration is performed by author validation or UAT.

## Command Coverage

This matrix deliberately includes the remaining coordination/administration
surface rather than suggesting the whole goal is complete. #94 owns the final
coordination screens and consolidated `docs/fleet-tui.md` acceptance artifact.
`TestTUICommandCoverageExhaustive` checks all visible executable Cobra leaves
against this inventory; a newly added command must receive an explicit row.

| Current CLI | Representation |
| --- | --- |
| default entry, `shell` | Fleet Enter or `x` > enter; actual local terminal or remote SSH PTY, return and refresh |
| `tui` | Launch this browser; no nested TUI inside an action |
| `ls`, `inventory` | Fleet local and registered remote inventory; independent refresh and stale/error health |
| `status` | Fleet state plus `x` > status CLI result |
| `up`, `down` | Details shortcuts `u` / `s`, or operation menu; reviewed explicit confirmation and normal lifecycle/ownership handlers |
| `destroy` | Fleet `d` or menu; exact captured host/kind/ID and state-volume impact; explicit `y` confirmation |
| `rebuild` | Target maintenance; builds desired image without silently replacing guest |
| `apply` | Stopped-target maintenance; CLI recovery archive and lock safeguards |
| `upgrade` | Servers host menu; explicit host-wide confirmation; existing Coops become stale |
| `sync` | Agent foreground maintenance; may acquire configured credentials and start guest |
| `backup` | Stopped-target archive destination on selected host; explicit private/unencrypted warning |
| `restore` | Servers host form for configured absent agent or project; host-owned source archive |
| `credentials refresh` | Agent foreground form; configured grant references only, never secret values |
| `credentials watch` | Agent foreground watcher; explicit single-profile selection, Ctrl+C stops watcher, no unowned daemon |
| `worker status`, `worker requests` | Agent worker panel output; requires running guest and existing configured worker |
| `worker pause`, `worker resume` | Confirmed agent worker controls; existing ownership/policy checks |
| `message continue` | Coordinator CLI/MCP control using an exact scoped target and handoff; no chat or lifecycle action; dedicated TUI form deferred |
| `ping` | Literal prompt composer and stable request ID; CLI/master receipt output, no assumed delivery or approval |
| `init` | Servers host bootstrap form with absolute project directory; existing interactive CLI and its consent prompts |
| `doctor` | Servers host diagnostics; bounded output preserves `[warn]` versus `[fail]` labels and nonzero failure status |
| `server list`, `server add`, `server update`, `server remove` | Servers workspace registry forms; no SSH keys/tokens/config copied |
| `mcp register` | Advanced host form with private config path and canonical existing-project path; existing ownership validation |
| `mcp inspect` | Advanced host form with config reference and operation ID; structured metadata result |
| `mcp serve`, `mcp stdio`, `master serve` | Services runbook, never started/daemonized by the browser; private configuration and operator-managed service lifetime |
| `team`, `team inspect`, `inbox` | Team presence and selected-agent diagnostics are available through `team --watch`; unified coordination and pending delivery views belong to #94 |
| `cache status` | Read-only CLI/MCP cache diagnostics; no GitHub calls or agent wake, see `docs/github-cache.md` |
| `task` | CLI/runbook | Task-provider command help |
| `task tui` | CLI/runbook | Native Kata project or canonical issue reference through the configured master host |
| `chat` | CLI/runbook | Native Kata standing team ticket from the authenticated active conversation source |
| `task capabilities` | CLI/runbook | Read authoritative task-provider identity |
| `task read` | CLI/runbook | Read one scoped task and immutable assignment |
| `task list` | CLI/runbook | List tasks for an exact goal revision |
| `task create` | CLI/runbook | Plan within an existing approved goal using a stable request ID |
| `task adopt` | CLI/runbook | Adopt an existing Kata UID with current provider revision and exact approved goal |
| `task reassess-qa` | CLI/runbook | Request an exact-artifact QA reassessment with new private evidence |
| `task request-release` | CLI/runbook | Owner records exact-source release authority |
| `task handoffs` | CLI/runbook | Read bounded, scoped QA/release requests |
| `pilot` | CLI/runbook | Experimental fixture-only Kata/AgentsView spike |
| `pilot run` | CLI/runbook | Dispatch one approved disposable archive audit |
| `pilot status` | CLI/runbook | Read the saved fixture receipt |
| `pilot mcp` | CLI/runbook | Private stdio access to the same fixture coordinator |
| `thread list`, `thread create`, `thread read`, `thread members` | Coordination slice #94; thread navigation, compose/context and owner-authorized exact-set membership |
| `message send`, `message status` | Coordination slice #94; private compose/reply and durable receipt status |
| `goal propose`, `goal list`, `goal authority` | Coordination slice #94; structured proposals and exact revision authority |
| `goal approve`, `goal reject`, `goal pause`, `goal resume`, `goal complete`, `goal reopen`, `approve` | Coordination slice #94; owner-only revision/sequence checks; legacy approval is not silently mapped to a different authority |
| `master backup`, `master restore` | Advanced runbook: explicit master recovery CLI in `docs/master.md`, not a guest backup/restore action; offline restore requires separately authorized recovery environment |
| `master reconcile status`, `master reconcile pause`, `master reconcile resume`, `master reconcile evidence`, `master reconcile resolve` | Advanced runbook: master recovery authority, exact epoch and guest evidence; never confused with guest worker controls |
| `master conversation cursor`, `master conversation map`, `master conversation reaccept`, `master conversation legacy` | Advanced runbook: inspect exact native cursor, map drained anchors, acknowledge restore epoch, or read member history; no fleet action applies cutover implicitly |
| `master placement set`, `master history import`, `master history list`, `master cutover` | Advanced runbook: coordination administration with explicit generations, history digest and source retirement; no implicit migration/cutover through fleet actions |
| `help`, `--help`, `--version` | `?` keyboard help, `v` About/runbook and existing CLI help/build identity |
| `completion bash`, `completion zsh`, `completion fish`, `completion powershell` | About/runbook documents shell integration; completion is generated for the shell, not executed inside Bubble Tea |

`host-exec` and `tui-exec` are hidden validated transport/launch adapters, not
user-facing commands. They cannot recursively invoke another transport/browser.
Master recovery remains an explicit operator runbook, not a new migration UI.

## Reproducible UAT

In a clean exact-head checkout, activate the repository's Flox environment:

```sh
flox activate -- bash internal/tui/testdata/uat-fleet-operations.sh COMMIT --operator-checklist
```

The script runs fixture checks only. The printed operator checklist grants no
live authority and performs no fleet mutation. Automated coverage includes local
and remote argv/path contracts, every operation family, disabled state/kind
entries, confirmation capture, registry edits, output limits, permission failure,
disconnection, cancellation, stale results and real-PTY terminal restoration after
success/failure and resumed browser keyboard input. Synthetic transport fixtures
distinguish forwarded Ctrl+D from transport Ctrl+] detach; real-PTY attachment tests
check restoration after detach, cancellation, remote exit/failure and attach failure.
Existing SSH PTY fixtures cover detach, resize and lost connection.

The script above retains its original `fleet-tui-uat-v1` shell-fixture identity.
For the staged B implementation use the reproduction and acceptance map in
`docs/tui-b-runtime.md` and environment revision `coop-tui-b-runtime-v1`.
Operator-assisted Blackbook/rack-mini UAT is **pending**. Independent Robin
exact-head review and Wren private exact-artifact QA remain required. Neither
author tests nor a printed checklist replace their evidence.

The [consolidation cutover recipe](consolidation-cutover.md) records candidate
provenance, the bounded maintenance window, backups, source cursor recovery and
operator acceptance. Owners can inspect `coop master conversation cursor`, map a
drained thread with `map`, and read local history with `legacy THREAD`. Hark keeps
its existing authenticated Coop MCP; native history reads do not require a second
provider connection. Reaccepting a source cursor after restore does not resume
workers or grant new goal authority.
