# Kata and AgentsView orchestration pilot

This experimental integration tests Coop directing work through an external task
ledger and a searchable session archive. Coop keeps approval and delivery
authority, Kata records the task, OpenCode executes it, and AgentsView indexes
the history. A private run receipt links those identities and prevents blind replay.

The commands are deliberately restricted to disposable fixtures. They cannot
select a named agent or production master. They do not change existing workers.

## Commands

- `coop pilot run --pilot-config /private/pilot.json --request-file /private/request.json`
  runs one approved fixture task, or reconciles evidence from its exact saved turn.
- `coop pilot status REQUEST_ID --pilot-config /private/pilot.json` reads its receipt.
- `coop pilot mcp --pilot-config /private/pilot.json` exposes `coop_pilot_run` and
  `coop_pilot_status` through stdio, using the same coordinator.

Configuration must be private, with credentials referenced through private token
files. The prototype uses an isolated master named `coop-maestro-pilot`, an isolated
Kata project named `coop-kenn-spike`, and a disposable runtime named
`coop-native-vm-*`. Configuration and artifacts belong outside Git.

The archive-only fixture reads imported public session evidence. Native fixtures
support a read-only file check or a constrained standalone Go coding task, locally
or through SSH to the explicitly supported test host. These restrictions are part
of the experiment and should not be relaxed to deploy production workers.

## Approval, replay and recovery

The exact immutable Kata task scope must match the approved Coop goal. Approval
is checked before claiming or dispatching and before recording completion. Saved
requests pin the configuration, task hash and approval sequence. Changed requests
cannot reuse a completed receipt.

A stable request ID reconciles an uncertain master send. Claim uncertainty stops
for inspection. A saved execution can reconcile its already submitted native
intent; that path never creates a session or submits another prompt. Unfinished
or uncertain work remains held. A completed receipt can be read or replayed after
the disposable runtime is removed.

Kata claims and Coop messages are separate operations. This prototype does not
provide a distributed transaction or dispatcher lease.

## Evidence and archive limits

Native ownership is checked through Coop's existing OpenCode observation client.
The coding fixture requires an explicit successful native test exit, a source
change restricted to its allowed file, unchanged protected files, and an
independent uncached test run. The patch and sanitized native output are retained.

AgentsView v0.46.1 can omit successful tool-output bodies for the tested OpenCode
format. Native results remain separate evidence; the prototype does not invent
missing archive contents. Its digest covers both sources. AgentsView is historical
evidence, not current worker health or execution authority.

Before archive operations, the integration checks the tested AgentsView version,
requires the selected OpenCode source and working-directory filter, disables all
other providers from the tested registry, and rejects remote-host imports. Child
processes receive an empty private HOME/XDG namespace and an environment allowlist.
SQLite snapshots are created with `VACUUM INTO` before transfer or import.

## Validation

The local and two-host experiments verified:

- rejection before dispatch when the exact goal is unapproved;
- a real OpenCode file read and a standalone coding task with passing tests;
- unchanged protected test files and one allowed source-file change;
- controller termination after prompt persistence, followed by completion of the
  same remote native turn;
- exactly one user prompt for that coding run, and no added deliveries or messages
  after real CLI and MCP replay;
- saved-receipt replay after disposable runtime teardown.

These tests do not establish physical sleep/reboot recovery, provider-login
renewal, production peer/QA routing, or a live worker migration.

## Production integration boundary

The default worker configuration admits engineering work from GitHub issue assignments. A
Kata catalog wrapper alone cannot replace that path: fresh action guards,
checkout repository resolution, task/PR metadata, artifact registration and action
recovery also require GitHub issue identities.

A production task provider must supply immutable Kata identities and an explicit
repository binding, use authenticated access, and participate in both discovery
and fresh revalidation. Task creation/rebinding/completion must use the existing
action journal and recover ambiguous responses. PR, review, QA and exact-head
merge checks remain in the existing engineering engine. Retained GitHub work must
continue to reconcile while new work selects its configured provider.

Before moving workers, validate the production path on an isolated Kata project
through implementation, requested changes, peer review, required QA, merge and
Kata completion. Resolve existing uncertain executions before changing admission.
Use one authoritative Kata placement that remains available when a client laptop
is disconnected. Native conversation turns remain conversations; they do not
provide an alternate engineering authorization path.

The optional production provider is documented in
[Kata tasks through Coop](kata-task-provider.md). It reuses the existing native
engineering engine and is separate from these fixture-only commands.
