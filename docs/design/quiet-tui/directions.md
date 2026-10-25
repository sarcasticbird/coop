# Two Directions

Both directions use the same sanitized inventory: four stopped local targets
(including Hawk), three running agents on `work-node`, and an unreachable `lab`
host with retained evidence. `spare` is a successfully read empty host retained
as a row. Coordination uses `design-master` independently.
All mockup diagnostics are illustrative observations, not authority.

## A: Host-Scoped Workspace (Alternative, Not Selected)

Choose a host scope, then a target, then an action. Persistent context anchors
the screen; at wide widths the inspector uses the otherwise wasted right side.
At narrow widths the same list drills into detail without losing its selection.
This records the original alternative only; it is not the selected interaction.

### A Comparison / 120x40

```text
COOP  [1 Fleet]  2 Coordination  3 Servers                         ? Help
SSH scope: work-node   h Change    State: all (0 hidden)
Master: design-master / demo-owner    (not the SSH target)
-------------------------------------------------------------------------------
FLEET [FOCUS]                         | INSPECTOR
    NAME       KIND    VM             | Host: work-node / ops@work.example
>   Hawk       agent   running        | Target: agent / hawk
    Robin      agent   running        | Runtime: coop-agent-hawk
    Wren       agent   running        | VM running; activity: waiting
                                     | Work: #42 / implement
3 visible; 0 hidden                   | Blocker: dependency #41
Inventory: SSH, 3s old, ok             | Report: master diagnostics, 8s old
                                     | Enter Connect   x Actions
-------------------------------------------------------------------------------
Enter Connect  Tab Inspector  h Hosts  x Actions  ? Help
```

A trades one explicit host-scope step for stronger target comprehension. It is
best for connecting to a known remote agent and comparing same-host state.
Cross-host comparison requires choosing All; that choice is visible, reversible
and never inferred from master placement. No host is privileged just because it
is reachable or has running VMs.

## B: Fleet Ledger With Full-Page Drill-In (Selected)

Start with one grouped, fleet-wide ledger. A single full-page detail replaces
the ledger on Enter; only the detail page offers Connect. Host headings carry
reachability and freshness. Host filter exists but is not the organizing rail.
This is not A with different borders: the primary object is a cross-host ledger,
and every inspection/action requires a page transition, even on a wide screen.

### B Fleet / 120x40

```text
COOP  [1 Fleet]  2 Coordination  3 Servers                         ? Help
SSH scope: All / explicit target below  Master: design-master   As: demo-owner
FLEET LEDGER [FOCUS]   State: all (0 hidden)
-------------------------------------------------------------------------------
desk (local)                         Inventory ok, 2s old
    Hawk         agent               stopped
    Robin        agent               stopped
    Wren         agent               stopped
    scratch...   interactive         stopped
work-node / ops@work.example          Inventory ok, 3s old
>   Hawk         agent               running
    Robin        agent               running
    Wren         agent               running
lab / ops@lab.example                OFFLINE; retained snapshot 12m old
    Build        interactive         unknown (last: stopped)
spare / ops@spare.example            ok, 4s old / 0 targets
-------------------------------------------------------------------------------
Selection: work-node / agent / hawk   [not connected]
Enter Details  h Filter hosts  ? Help
```

### B Detail / 80x24

```text
COOP / Fleet / Detail       Master: design-master   As: demo-owner
SSH target: work-node / agent / hawk
DETAIL [FOCUS]                 Inventory: SSH, ok, 3s old
VM: running                    Runtime: coop-agent-hawk
Activity: waiting              Report: master diagnostics, 8s old
Work: #42 / implement          Blocker: dependency #41
Host address: ops@work.example

Enter Connect   x Operations   Esc Fleet   ? Help
```

### B Coordination / 80x24

```text
COOP / Coordination       Master: design-master   As: demo-owner
SSH scope: All (no bearing on this page)
[t Team]  l Threads  i Inbox  g Goals
TEAM LEDGER [FOCUS]
> Hawk   work-node   online   waiting    #42 / dependency #41
  Robin  work-node   online   working    #41 / review
  Wren   work-node   online   unknown    no visible report
Enter Diagnostics (full page)   p Prompt   ? Help
```

### B Threads / 80x24

```text
COOP / Coordination / Threads   Master: design-master   As: demo-owner
SSH scope: All (not message routing)
THREADS [FOCUS]                 Content: masked
> Design check    members: demo-owner, hawk, robin, wren
  Runbook check   members: demo-owner, hawk
Enter Reader (full page)   n New thread   ] Next page
READER: parent and member metadata; Ctrl+V reveals content
p Reply -> masked composer -> Ctrl+S preview -> Ctrl+S send
```

### B Setup / 80x24

```text
COOP / Servers           Master: not configured
SERVER LEDGER [FOCUS]
  desk       local / this controller
> work-node  SSH / ops@work.example / not yet checked
a Add SSH reference   e Edit   x Host actions
Master setup is separate: v Runbook / private client config
No credentials entered here. Enter validates fields, not SSH trust.
```

### B Help And Confirmation / 40x20

```text
COOP / Fleet / Confirm
Master: design-master
As: demo-owner
SSH host: work-node
Address: ops@work.example
Kind: agent
ID: hawk
Action: stop
VM stops; state volumes remain.
Captured target; no retarget.
Argv: coop --agent hawk down
? opens scoped, scrollable help
Esc Cancel   y Confirm
```

B is easier for fleet-wide scanning and offers a consistent drill-in mental
model at every size. It costs more back-and-forth for monitoring a known host,
and the stopped-local-first mistake remains easier to make in the long ledger.
Mitigation is no automatic initial target and Enter-to-inspect, not sorting by
running state. At 40 columns, B uses the same full-page forms and confirmation
contract as A. Its weakness is more transitions, not insufficient terminal width.

### B Reader / 80x24

```text
COOP / Coordination / Reader   Master: design-master   As: demo-owner
SSH scope: All (not message routing) / Engagement: demo
Thread: demo-design / Design check
READER [FOCUS] / Content: masked / full page even on wide terminals
Selected: msg-demo-18 / hawk -> demo-owner
Body: [hidden] / deliberate Ctrl+V reveal
Arrows select message; PgUp/PgDn scroll without changing selection.
+2 new below / follow on / loaded 20 of bounded pages / retained limit 500
p Reply selected  c Root compose  s Receipt  Esc Thread ledger  ? Help
```

### B Composer / 80x24

```text
COOP / Coordination / Reply   Master: design-master   As: demo-owner
SSH scope: All (not message routing) / Engagement: demo
Thread: demo-design / Design check
COMPOSE [FOCUS] / Content: masked / no ledger pane while composing
To: hawk / stable member ID
Parent: msg-demo-18 / author hawk
Request: req-demo-reply-1 / not submitted
Body: [hidden, 32 bytes]
Tab fields: To / Body / Help; Enter inserts newline in Body.
Ctrl+S previews, second Ctrl+S submits; no automatic mutation retries.
Escape: Keep / Discard / Return, draft retained only in memory.
Ctrl+S Preview  Ctrl+V Reveal  Tab Field  Esc Draft guard
```

These full-page B forms use the same captured-request, privacy, membership and
uncertainty contract as A; B never adds a new messaging backend. A keeps the
thread ledger visible at wide sizes, while B deliberately removes it until Back.

## Selected Recommendation

Proceed with B as the design direction. The owner selected it because multiple
hosts and their birds need to be visible together; this supersedes the initial
A recommendation. The three dimension files now specify B, not A with a renamed
heading. All sizes use a fleet-wide grouped ledger and full-page drill-in; no
automatic target, mandatory host picker or wide split pane. Only Details offers
Connect. Optional host/state filters remain visible, reversible and non-authorizing.

The tradeoff is one extra Details transition before connecting, in exchange for
whole-fleet visibility and a consistent model across widths. Explicit selection,
host context and captured confirmations address the stopped-local duplicate risk
without hiding it or selecting a running target automatically. Empty and failed
hosts stay visible. Master identity stays separate from SSH filtering; B is not
cross-master routing. Do not ask Chris to choose A/B again. Approval to implement
the revised packet and the separate release/UAT gate remain outstanding.
