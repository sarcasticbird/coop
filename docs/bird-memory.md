# Bird memory

Coop stores bird memory in `agent_memory` in the existing master's SQLite
database. No additional database or service is needed. The key is the stable
bird principal plus a memory key, not a host, container, image, session or
placement generation. The existing master backup includes memory.

Memory is working knowledge, not goal approval, an instruction hierarchy,
an action receipt or a second task ledger. Kata remains task authority;
AgentsView remains session history. Never store credentials or full transcripts.

## Read and write

Use the configured master client from a host. An owner must name the bird:

```sh
coop memory list --bird robin
coop memory read projects/coop.md --bird robin
coop memory write projects/coop.md --bird robin --file /private/note.txt --revision 0
```

Revision `0` creates a key. To edit, read its current revision and supply it to
`write --revision N`. A stale edit conflicts rather than overwriting knowledge.
Retrying an identical just-committed write returns its original revision; after
a subsequent edit, reread and reconcile instead. Keys are relative names, up to
256 UTF-8 bytes. Bodies are limited to 32 KiB; each bird has at most 512 keys.
`list` returns at most 50 summaries, never memory bodies. Continue with
`--after NEXT`; `--prefix` is a literal prefix.

Inside a bird, the existing identity-pinned master connection is used:

```sh
coop-worker memory list --config /var/lib/coop/worker/config.json
coop-worker memory read --config /var/lib/coop/worker/config.json --input /private/read.json
coop-worker memory write --config /var/lib/coop/worker/config.json --input /private/write.json
```

Read JSON: `{"key":"projects/coop.md"}`.
Write JSON: `{"key":"projects/coop.md","body":"Useful knowledge","expected_revision":0}`.
List JSON optionally supplies `prefix` and `after`.

Birds can read and write only their own memory. Owners with `read`/`manage`
can maintain birds in a shared engagement. Service accounts and other birds
cannot read another bird's private knowledge. Every HTTP request checks current
placement, so a retired container token cannot access memory after replacement.
Memory access does not depend on Kata availability or a task-policy digest.

MCP exposes `coop_master_memory_list`, `coop_master_memory_read` and, for
eligible writers, `coop_master_memory_write`, with the same server-side checks.

## Import existing Markdown

```sh
coop memory import-markdown /private/robin-memory --bird robin
# Or run inside the existing bird:
coop-worker memory import-markdown --config /var/lib/coop/worker/config.json --path /root/path/to/memory
```

A file or directory is accepted. Relative Markdown filenames become keys,
with a `markdown:relative/path.md` provenance label. Empty files and non-Markdown
files are ignored. Symlinks, special Markdown files and oversized/invalid bodies
are refused during preflight, before any writes. Existing equal bodies are
skipped; different bodies conflict instead of overwriting newer knowledge.
An interrupted import can be rerun. Source files are never removed.

After importing, read back the keys and update private bird instructions to use
Coop memory instead of Markdown. New engineering and conversation prompts
include that instruction. Retire the old files only after verification.

## Current boundary

The API and importer are source changes, not a live fleet migration. Installing
the matching master and worker binaries and importing each bird's existing files
are still required. Host-side memory persistence removes one dependency on guest
disks; it does not yet make checkouts, native session databases or action journals
portable. Full image replacement remains a separate lifecycle change.
