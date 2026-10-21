# Issue 133 BEFORE PTY Captures

Production baseline: `103a896a911f53a005126852f7c7f89a748b87e3`.
The worktree was clean at build time. No production or fixture source was
modified. The executable was built before the capture script edits and was
not rebuilt during capture.

## Build

```sh
flox activate -- go test -c -o /tmp/opencode/hawk-133-before.test ./internal/tui
```

Executable SHA-256:
`194737ac9c1b3a87e0d1a2a51073395bdcfdbb09de00b3449a6a499eebaecbdc`

## Capture

```sh
flox activate -- python3 scripts/capture-tui.py /tmp/opencode/hawk-133-before.test docs/evidence/tui-correction-133/before --source 103a896a911f53a005126852f7c7f89a748b87e3 --extended
```

All 114 checkpoints passed their visible-state assertions: 19 states at
120x40, 80x24 and 40x20, each with color enabled and with `NO_COLOR=1`.
All six fixture processes exited successfully. Each checkpoint contains a
cumulative raw PTY `.ansi` transcript and a final-screen `.txt` replay.
Raw terminal output is authoritative; the replay handles only the terminal
control subset documented in the script. These are actual native Bubble Tea
sessions, not direct `View()` snapshots or visual-parity approval.

The original seven states remain first: Fleet, selected Fleet, Details,
Threads, reader, Team and Team detail. Extended states are Team help,
Servers, SSH add editor, SSH editor help, Details help, Connect confirmation,
new-thread creation composer, creation preview, root composer, composer help,
root preview and draft protection.

Navigation uses normal keys and synthetic fixture data only. Connect is
cancelled with Escape, never confirmed. Each preview receives exactly one
Ctrl+S; neither creation nor message submission is executed. Drafts are
discarded before normal exit. No real services, customer configuration or
transport are loaded. Production layout defects remain visible in this
baseline; collection does not imply the screens meet the correction design.

`manifest.json` records the production revision, executable digest, final
capture-script digest, and fixture source revisions and digests. The fixture
launcher remains unchanged at revision
`103a896a911f53a005126852f7c7f89a748b87e3`, SHA-256
`75a6e528bff8190303c087222576bdee7dd472ee918e234075a064de5a7d66ca`.
Fixture revision entries identify the last commit touching each listed file
at the production baseline; digests identify the actual unchanged sources.

## Verification

- `flox activate -- go test ./internal/tui`: passed.
- `git diff --check`: passed.
- Default capture invocation without `--extended`: passed all 42 original
  checkpoints, with output under `/tmp/opencode/hawk-133-default`.
- A preliminary extended run stopped at an incorrect harness navigation
  assertion before Connect confirmation. Its partial output is isolated at
  `/tmp/opencode/hawk-133-before-partial-navigation`, not in this matrix.
  The final run reopened Details explicitly and completed the entire matrix.
