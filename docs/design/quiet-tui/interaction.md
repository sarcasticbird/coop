# Interaction Contract

Everything described as a change here is proposed, not shipped. Keep the current
CLI/client/SSH handlers and safety boundaries. No new dependency is required.

## Context And Selection

The header always distinguishes `SSH scope/target` from `Master` and `As`.
Fleet scope does not filter messages or select a master. Coordination shows the
authenticated principal, authorized engagement and pinned master; Team detail
adds stable agent/worker instance/generation. Host addresses and long identities
wrap in detail; list labels may clip, but selected identity is always inspectable.
At 40 columns these labels occupy separate sticky lines, not a clipped footer.
Never abbreviate two distinct identities to indistinguishable actionable labels.
If a value itself exceeds the viewport, its labeled detail is horizontally
independent of the list and wraps/scrolls vertically; actions remain disabled
until the complete captured identity and required consequences have been shown.

On each launch: open H host chooser, default focus on All, with no selected
target and no pending action. Enter commits a scope, not a target. The operator
then moves to a row explicitly; Enter on the untouched list is a no-op with
`Choose a target with arrows`. All includes local and every registered host;
it is never replaced by the first reachable host. A per-launch picker costs one
step deliberately. From no target, either Up or Down selects the first visible
row (not the second); no rows means neither key selects anything. No persisted
default, running-first sort or inferred retirement.
Return from attach restores the session's explicitly chosen scope/target.

`h` opens chooser; arrows then Enter commit; Escape leaves previous scope.
Local means this controller, not the master host. `All` rows are grouped by host,
with one host health heading, plus a mandatory HOST column even on compact
screens. A single-host view omits repeated host prefixes on rows. Stable ordering
uses registered host order and stable target IDs, not activity or refresh age.
The optional state filter (`/` opens filters) defaults to all, shows hidden count,
and can explicitly hide stopped VMs. Stopped is not retired; no deletion or
ownership inference. Filters do not hide failed-host notices. Help exposes the
active filter and one action to clear it, including on a zero-result screen.
The narrow All view uses `int` for interactive kind, expanded to `interactive`
in detail and contextual help; stable runtime IDs never dominate friendly names.
Hidden counts count only excluded rows, not unknown rows retained for safety.

Selection is a full native key, not an index: registered name/address, target
kind/ID and runtime/project as required. Refresh/reordering retains the key and
scroll anchor. If removed, keep a tombstone `selected target unavailable`, disable
actions, and require a new explicit selection. If filtered out, show `selection
outside filter`, clear actionable selection and require explicit re-selection.
An address edit invalidates captured actions even when the friendly name matches.
Switching host never selects a replacement. A pending form captures its own
identity and ignores background selection updates; changing scope requires
closing/suspending the form first, never quietly changing its destination.

## Layout, Focus And Cells

Use the terminal's font and measured display cells, not bytes or rune count.
The fixtures are ASCII. Runtime must handle wide characters, combining marks,
emoji and terminal-control stripping using the existing width/sanitization
helpers; test them before shipping. No custom font or box-drawing dependency.

| Width/height | Fleet/Team | Threads | Forms/help |
| --- | --- | --- | --- |
| 120x40 | Left 43-cell list, 1-cell divider, 1-cell gap, up to 75-cell inspector; two focusable regions | Thread list beside reader; composer replaces reader | Captured context above a scrollable form; footer below |
| 80x24 | Full-width list plus short selected summary; Tab/Enter diagnostics opens detail page | List and reader are separate pages | Full page, sticky context/footer, scrolling body |
| 40x20 | Full-page list then full-page detail; mandatory context on both | Separate list/reader/compose; no squeezed side rail | Full page; sticky context/footer; field/identity wrap |

At intermediate widths use 100 cells as split threshold, provided height >=28;
otherwise use stacked/drill-in. Below 40x20 show `Terminal too small; resize`
with no mutation shortcuts, preserve state, and allow safe quit. At 40x20 the
first four/five context lines and final two action/status lines are reserved;
the body alone scrolls. Long confirmations expand vertically, never truncate
identity/argv or hide a consequence behind a fixed-height dialog. Read through
required content before enabling confirmation; show `review remaining lines`.
Long argv wraps as literal text, not a shell command to copy/execute.

`Tab`/`Shift+Tab` cycle the visible list/inspector or form fields; the focus label
is explicit and a bold region heading is redundant decoration. On compact
Fleet, Tab opens detail; Escape restores list position. On Team, Enter opens
diagnostics. On Threads, Enter opens reader. Lists: arrows/j/k select, PgUp/PgDn
move one visible page. Detail: arrows/j/k scroll, PgUp/PgDn page. Reader:
arrows/j/k select the previous/next loaded message by stable ID and reveal that
message's start; PgUp/PgDn scroll without changing that reply/receipt selection.
`p` and `s` always target the visibly labeled selected message, never the last
line scrolled into view. If selection is offscreen show its ID/author in a sticky
reader status line. Home/End scroll within the focused region, not the global
document; End intentionally goes to latest but does not change the reply target.
No scroll wheel or mouse required. On resize preserve focus by semantic region; collapsed inspector
becomes detail, never a new selected row. On widening preserve current reader or
composer, do not forcibly focus the newly visible list.

## Keyboard And Discovery

Keep `1 Fleet`, `2 Coordination`, `3 Servers`; Coordination `t Team`, `l Threads`,
`i Inbox`, `g Goals`. Do not add a fourth top-level key and displace existing
commands. At 40 columns the footer uses at most two short lines, prioritizing
the primary action, Back and Help over secondary accelerators. Wider views can
show roughly six hints; `? Help` is always available outside literal editors.
Help is a scrollable page, grouped as navigation, safe reads, actions and advanced
CLI/runbook. It names the current focus and exact action target without revealing
message bodies. `x` opens all eligible operations, with disabled reasons, so
shortcuts are accelerators rather than required memory. `v` preserves About,
Services, administration/recovery and shell-completion runbooks.

Editors treat letters, digits, `?` and Enter as literal text; Tab/Shift+Tab moves
fields. A visible `Help` field/button is focusable; Enter on that button opens
editor help (not submit). `Ctrl+S` previews then `Ctrl+S` submits Coordination
requests; not Enter, not `y`. Escape from preview returns to editing if unsubmitted.
Servers/host forms keep their existing Enter/save and explicit confirmation
rules, not Coordination's submission convention. Destructive host confirmation
uses `y` after complete review, Escape cancels. Existing `u/s/d`, `p/c/n/m/s/a/o/d`,
`f`, `]`, `e`, `Ctrl+R` and `Ctrl+V` retain their contextual meaning as inventoried
in source.md. Global Ctrl+C follows current exit/cancel behavior; explain that it
is not rollback. Proposed quit guard warns about an unsent in-memory draft.

## Attach And Return

Fleet Enter remains Connect, not Start on a stopped duplicate. It opens A
preflight with captured host/address/kind/ID, VM state, runtime/project, literal
entry argv and return instructions. `Enter Continue` uses the existing entry
handler; Connect remains disabled for stopped targets, as it is today. Starting
requires explicit Start, its credential/grant warning and existing consent.
No new remote transport. Remote `Ctrl+]` detaches transport; `Ctrl+D` quits the
OpenCode frontend, not the same operation. Ctrl+C reaches the foreground app.
Local entry uses the application's exit/detach convention. Suspend Bubble Tea,
release the real terminal, restore terminal settings and resume the same key,
scope, scroll and draft. Refresh after return without focus theft. A missing
target returns a tombstone, not another Hawk. Retained tmux cleanup remains a
separate follow-up; design does not promise transport/session cleanup.

## Honest States And Freshness

| Label | Evidence and behavior |
| --- | --- |
| VM running / stopped / unknown | Lifecycle from selected host inventory, not presence or model activity |
| working | Fresh diagnostic handling `working`; job/stage reference when available, not heartbeat alone |
| waiting | Diagnostic handling `waiting`, with reason/dependency if visible |
| auto_retry | Diagnostic handling `auto_retry`, reported retry/deadline when present; no UI mutation retry |
| action_required / blocked | Diagnostic reports a blocking condition needing action; show original reason, never auto-resolve |
| activity unknown | Missing/denied/unsupported/stale diagnostics, or no evidence of work; never infer idle |
| online / stale presence | Master presence state; heartbeat normally 15s, stale after 45s; show last seen |
| offline host | SSH read failed/unreachable; retain last successful inventory labeled age and last-known VM state |
| loading | No successful read yet; not zero VMs, not stopped, not a reason to choose a different host |
| empty | Successful authorized snapshot has no matching records; show filter/hidden count and reset path |
| denied / unsupported | Explicit access or capability failure, not empty; no local/master fallback |

Idle is only used if a source explicitly reports it; absence of work evidence is
unknown. Labels keep raw handling alongside friendly explanation. Render source,
observed/report timestamp and receipt age separately where available; master
receipt time is not execution time. Negative/skewed ages show timestamp and
`clock uncertain`. For diagnostics use the backend's freshness assessment and
show report/receipt times; if unavailable label freshness unknown, not a new
invented TTL. Host inventory shows last success and error; after a failed read
disable mutations pending authoritative validation, even if cached VM was running.
All-scope banners retain every failed host, including one with no retained rows.
No cross-host Fleet-to-Team join by alias alone: match explicit stable agent,
placement host/instance/generation; an unresolved mapping shows `work unknown`.
Presence/diagnostics are observations, never admission or permission evidence.

## Quiet Refresh And Failure

Keep the last coherent snapshot while fetching. Render only changed regions;
no clearing to Loading between successful cached reads, spinner animations or
periodic cursor jumps. Separate per-source request generations; discard late
results from superseded scopes and pre-action epochs. Keep row key, focus,
scroll offset/anchor, reader position, reveal setting and draft buffer on refresh.
Relative ages update in coarse text buckets, not a flashing second counter.
Resize reflows by message/field identity and logical offset, not physical row.

Thread follow is an explicit read-only mode. New messages increment `+N new`
without moving a scrolled reader; End intentionally jumps to latest. Marking read
remains local session state. Bounded pages and the 500-message retained limit
must be visible; `]` next page and reopen-for-earlier remain discoverable. No
global unread count or durable synchronized read state is proposed.
An error is a quiet persistent status line plus inline source status; `r` retry
reads, never mutations. Successful recovery clears current error but does not
erase an operation's uncertain outcome. Serialized host actions keep request
sequence and captured identity; cancelling or disconnecting may be unknown, not
rollback. Inspect/refresh before any host retry; never auto retry a mutation.

## Conversations And Privacy

Thread list shows sanitized metadata only, never body snippets. Reader opens
masked in the proposal; `Ctrl+V` deliberately reveals the active private buffer
in a private terminal. Header labels `Content: masked/revealed`. Opening a new
reader resets to masked; revealing one buffer never reveals another pane.
Help, errors, notifications and confirmation titles do not echo bodies. Inactive
composer masks on focus loss; returning requires deliberate reveal. Metadata can
also be sensitive: masking is not screenshot safety or encryption. No logging,
disk draft cache or new telemetry is authorized. Never enter credentials.

From Threads, `n` opens N New thread: title, authorized engagement, complete
stable member IDs and a Help field. Ctrl+S previews the captured member set,
engagement, title and request ID; second Ctrl+S creates the thread, not a message.
Success opens the new masked empty reader with the returned stable thread ID.
No wake/message occurs merely because a thread was created. Failure retains
the form; an uncertain creation freezes input and offers identical Ctrl+R retry,
just like an uncertain message. Never create another thread to bypass uncertainty.
`c` in a reader opens C-root (no parent); `p` opens C reply using the selected
message ID/author. Opening a reader initially selects the first loaded message;
Down then selects the next. No loaded messages disables reply and receipt.
Pressing c/p without a reader explains `Open a thread first`, not an implicit
new thread. Thread/engagement changes never retarget an open form.
Before a thread exists, Keep retains the creation form at session level: the
Threads list shows `New thread draft retained; n Resume`. `n` resumes that
same form/request, not another creation. A frozen uncertain creation likewise
reopens its status on n and prevents duplicate creation. This is separate from
per-thread message drafts; K's thread-specific c route is not used for N.

Compose captures master, authenticated principal, engagement, thread, recipients,
parent and new request ID. Preview shows the full captured identity with the
body still masked unless deliberately revealed; the body remains in immutable
input. Recipients are editable stable IDs before preview. Blank root recipients
are explicitly `Note; wakes nobody`; blank reply recipients are resolved and
shown as parent author excluding sender. Membership edit remains owner-only,
exact observed/desired sets. Goal decisions remain exact revision/sequence.

Proposed Escape opens draft guard: Keep (suspend in session), Discard or Return.
Keep restores a visible `Draft retained in memory` affordance on the thread;
`c` resumes that draft, rather than creating a duplicate composer. Only one
unsubmitted draft per active thread/session in this proposal; switching to a new
draft requires explicit keep/discard choice. Quit warns memory-only drafts are
lost. No promise of process-crash recovery. Poll/resize never modifies typed text.
Any workspace/section switch, host chooser or opening another form while editing
uses the same draft guard; text keys are never intercepted as navigation. On
return from Keep, C-resume renders the retained-draft route on the reader; c
resumes that draft and does not make a fresh request ID. While a frozen uncertain
request exists, c instead reopens its U status; new compose is blocked until its
outcome is established. The session can close the status view, not forget it.

After Ctrl+S submit, freeze payload/request ID. If response is lost, display
`Uncertain; inspect receipt; Ctrl+R identical retry` and forbid editing/reuse of
that ID or a fresh duplicate. Escape may close presentation but retains frozen
request state and visible receipt/retry route in session. A known validation
rejection can return to edit with new reviewed request identity only after
backend confirms no commit; denial is not a retry hint. No automatic resend.
Commit shows message ID and receipt; queued/received/running/replied/blocked/
cancelled/suppressed stay distinct. Accepted is not delivered, worked or approved.

## Target-Safety Storyboard

At every size: H -> F0 (scope chosen, no target) -> Down -> F (remote Hawk
explicitly selected) -> F-offline (same key retained, actions disabled) ->
F-reconnect (same validated key, operator must press an action again). Refresh
may reorder rows but never changes the selected key or dispatches a queued click.
Reconnection does not resolve any prior uncertain operation or imply its success.
If the host address/runtime identity changed, use M unavailable rather than
F-reconnect; require explicit re-selection and new captured intent. The compact
M frame illustrates that state; larger layouts render it in the inspector.

## Servers And First Use

S shows existing local and SSH references, with separate master setup status.
Add SSH reference asks registered name and SSH address/user as today's form
does; no invented path/options field. No SSH key/token entry or copying.
Field validation is not trust/connection success. On first connection use the
existing explicit SSH trust/error contract, never silent acceptance. Missing
Coop/PATH or permission failures show a runbook/retry route, not local fallback.
Removing a reference does not delete remote VMs. Registration/address change
invalidates pending targets and requires fresh selection/validation.

Master not configured shows private configuration instructions from `v` and
`--master-config`, while Fleet remains usable. Master configured but no SSH
hosts registered explains these are separate; offer Servers add. Master config
editing, profile switching and credential provisioning remain explicit CLI/config
runbook work. Do not infer native targeting from a shell wrapper name.

## Terminal Theme

Use terminal default background and foreground, not a painted dark rectangle.
Dark: default text, bold section/selected name, muted secondary metadata only if
readable; one cyan-like accent for focus and amber-like warning optional. Light:
same defaults and hierarchy, darker accent/warning variants, no pale gray text.
Low-color: default + bold + optional single accent, no dim text. No-color/NO_COLOR:
ASCII `>`, labels, separators and `[FOCUS]`; do not use ANSI styling. Honor user
terminal theme; never assume exact RGB contrast of unknown defaults. Test both
actual light/dark terminal profiles before product acceptance; fall back to
default text rather than low-contrast dim styles. No red/green-only distinction,
blinking, gradients, animation or full-row inverse bar needed. State words,
warning prefixes and focus/selection markers convey every meaning without hue.
