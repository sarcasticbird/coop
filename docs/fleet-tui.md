# Coop table

`coop tui` shows local and registered-host Coops in one table:

```text
COOPS
  COOP                HOST          STATE        KATA
> rook                local         running      —
  hawk                rack-mini     running      Review the current change
  robin               rack-mini     running      Implement the current change
  wren                rack-mini     stopped      unknown

↑/↓ select · Enter session · k Kata · m message · r refresh · q quit
```

Running Coops appear first. Stopped Coops stay visible. Unreachable hosts retain
their last rows marked `unavailable`, not stopped. The table refreshes in the
background without changing the selected Coop when rows reorder.

- **Enter:** attach to the selected running session. Detaching returns to the
  table. Named birds use attach-only entry; this does not start or rebuild them,
  refresh credentials, or apply a new runtime. Remote sessions retain `Ctrl+]`
  detach; Ctrl+C goes to the foreground application.
- **k:** open the selected bird's current Kata task, or the Kata project if no
  current task is available. `coop kata [REFERENCE]` and `coop task tui [REFERENCE]`
  use this same native Kata entry; neither enters or maintains a project VM.
- **m:** write a message to the selected named bird in the standing team Kata.
  Enter inserts a newline; Ctrl+S sends; Escape cancels. Text is visible while
  composing. There is no preview or approval screen. An offline bird can receive
  a queued message; queued does not mean it has started work.
- **r:** refresh. Arrow and page keys navigate; `q` or Ctrl+C quits.

Messaging uses the configured authenticated master and its advertised team Kata,
not a thread guessed from a display name. Host-owned bird identity resolves the
master's current placement, so replacing a worker does not break the table.
Session attachment still uses the complete captured host/address/target identity.
Unknown or stale task observations are not shown as current work.

A failed send is not automatically repeated. If its outcome is unknown, Ctrl+S
retries the same message and request ID without allowing the payload to change.
Closing that composer does not retry it; check Kata before sending it again.
Drafts remain in process memory only and are discarded on exit.

## Configuration

Host discovery uses the existing registered SSH servers. Kata and messages use
`master-client.toml`; `--master-config` selects an existing alternative. A missing
master does not prevent listing or attaching to Coops.

The existing native Kata entry uses `work-tools.toml` beside the master client:

```toml
master_id = "configured-master-id"
project_uid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
host_name = "registered-server-name"
team_issue_uid = "01ARZ3NDEKTSV4RRFFQ69G5FAW" # optional
```

Those pins must match the current master. The selected host keeps its private
Kata configuration and credentials; the TUI does not copy or manage them.

The table has no server editor, lifecycle controls, diagnostics pages, goal
approval forms, split panes or command runbooks. Administration stays in the CLI.
