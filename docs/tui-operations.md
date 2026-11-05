# TUI and administration

The [Coop table](fleet-tui.md) contains only session attachment, Kata and messaging.
It is not a graphical wrapper for every CLI command.

Use the existing CLI for deliberate administrative work:

```sh
coop --agent robin status
coop --agent robin up
coop --agent robin down
coop --agent robin worker status
coop server list
coop doctor
```

Backup, restore, deletion, credential changes, server registration and goal
decisions are not table actions. Their existing CLI safeguards are unchanged.
There is no hidden operations menu or second TUI mode.
