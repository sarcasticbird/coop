# Persistent agents

Named agents have their own Linux machine, writable root disk, `/root` home,
and `/workspace` checkout directory. They have administrative access inside
the guest. Host home and project folders are not shared. Interactive Coops
continue to mount the selected project and explicitly configured folders.

Agent machines require Apple Container 1.5.0 or newer. Cold disk recovery is
currently supported only with its verified 1.5.x storage layout.

## Configure and launch

Merge the [agent example](../examples/coop.agent.toml) into the private host
`~/.config/coop/coop.toml`. Put each agent's GitHub token in its referenced host
file with mode 0600. Use that account's provider permissions to grant access to
the repositories it needs. Coop does not create or revoke those permissions.

```sh
coop --agent robin rebuild
coop --agent robin
```

Named profiles load only the host configuration. The current folder, its
`.coop.toml`, project scopes, interactive mounts, and interactive agent state
volumes do not grant capabilities to a named machine. Global tool packages
remain available. Profile-specific `[[profiles.robin.seed]]` entries provide
explicit initial copies of portable instructions. Seeds run once per machine.

The `orgs` list selects GitHub organizations or personal accounts for discovery.
Coop enumerates all pages visible to the selected token and clones missing repositories into
`/workspace/<org>/<repo>`. Existing checkouts must have the expected origin;
Coop never pulls, resets, cleans, or overwrites them. A failed clone can be
retried with `sync`. Host checkouts and other agents exchange changes through
GitHub. For personal accounts, Coop uses authenticated repository discovery and
filters results to the selected owner, including private repositories granted
to the token. PAT and App user-token profiles use authenticated user discovery.
Profiles explicitly exposing `COOP_GITHUB_APP_BUNDLE_FILE` use installation-scoped
discovery and the guest GitHub helper. See [App authentication](github-apps.md)
for signing-key placement, owner routing, helper installation and host renewal.

```sh
coop --agent robin up                 # start the managed agent without attaching
coop --agent robin                   # reconnect to its terminal
coop --agent robin shell             # separate administrative shell
coop --agent robin gh auth status    # one command, no duplicate managed agent
coop --agent robin sync              # clone newly visible repositories
coop --agent robin status
coop --agent robin credentials refresh
coop --agent robin down
```

Detach from the managed terminal with Ctrl-b then d. Detaching preserves the
job and credentials. `down` stops the whole machine. On the next start Coop
acquires fresh credentials before starting the configured agent and helpers.
Native coding-tool login state, installed packages, databases, and checkouts
remain on the guest disk. Each coding tool still controls its own permission
and approval settings; Coop does not rewrite them.

## Servers and helper services

A profile command can be a server. For example:

```toml
[profiles.robin]
command = ["opencode", "serve", "--hostname", "0.0.0.0", "--port", "8788"]
restart = "on-failure"
include_credentials = ["robin-github", "robin-server-password"]

[credentials.robin-server-password]
source = { type = "file", path = "~/.config/coop/secrets/robin-server-password" }
expose = [{ type = "environment", name = "OPENCODE_SERVER_PASSWORD" }]

[profiles.robin.services.worker]
command = ["/root/bin/worker"]
```

`restart` accepts `no`, `on-failure`, or `always`. Helpers run under systemd
and restart on failure. They receive the selected credential generation and
start only after host credential acquisition succeeds. An already exited
managed terminal remains available for inspecting its output; stop/start the
machine to launch it again. Refresh changes credentials for new processes;
existing processes retain their environment until restarted.

Remote access requires a reachable VM address or a separately installed guest
tunnel helper. Coop does not publish a tunnel, install Cloudflare, configure
DNS, add TLS, or change service authentication. Tools for a helper can be
installed inside the machine or added to global `[tools]` configuration.

## Maintenance and recovery

Ordinary entry never replaces an existing root filesystem. Configuration drift
returns a maintenance-required error. Stop the machine, then run `apply` to
adopt profile or resource changes. `rebuild` only builds an image. Changing an
existing machine's base image is unsupported because arbitrary guest disk
changes cannot safely be merged into a new image; the existing disk is kept.

```sh
coop --agent robin down
coop --agent robin apply
coop --agent robin up
coop --agent robin down
coop --agent robin backup /private/path/robin.zip
coop --agent robin-copy restore /private/path/robin.zip
```

Configure the destination profile before restoring. It must be absent. A
restore validates the full archive and checksums before registering its target.
The original machine and archive remain intact. An interrupted publication may
leave an incomplete target; Coop reports the target rather than deleting it.

Backups include cold root disks and Coop-owned volumes, preserving sparse disk
allocation. Host-mounted files and credential tmpfs are excluded. Native login
stores and any secrets guest root saved to disk can be included. Archives are
created with mode 0600 and are not encrypted. Keep them private.

Interactive Coops use the same `backup` and `restore` commands from the selected
project. Same-image `apply` retains a verified cold recovery archive before
replacing runtime configuration and grafting its preserved disk and volumes.
Image changes remain unsupported. Host mount contents are never restored or
deleted. `destroy` explicitly removes the selected guest disk and its owned
volumes after confirmation.

Ordinary file credentials have no automatic renewal. App-backed profiles can use
`coop credentials watch` under a host supervisor. Previous credential generations remain in guest tmpfs until machine
shutdown so active shells keep working. Updating a source file does not revoke
an old provider token. Guest root can retain any credential it receives.

For unattended monitoring and review handoffs, see [GitHub workers](workers.md).
That optional backend runs an OpenCode server and worker as helper services,
with a terminal client that can detach independently.
