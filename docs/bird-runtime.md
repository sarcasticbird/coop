# Replaceable bird runtimes

Refreshed birds can opt into `runtime = "container"` in their host-owned profile.
This uses the same local Apple Container runtime and Coop systemd image, without
the `machine` plugin's inseparable root disk. Existing profiles default to the
unchanged machine backend. Changing an existing profile's backend is refused;
there is no automatic guest-root migration or deletion.

## Durable and replaceable state

Each portable named agent owns four guest-only volumes:

| Guest directory | Durable contents |
| --- | --- |
| `/root` | Native sessions, coding-tool login state and portable instructions |
| `/workspace` | Repositories, task checkouts and checkpointed work |
| `/var/lib/coop` | Worker configuration, execution journals and action receipts |
| `/usr/local/libexec` | Managed worker/helper binaries |

Bird memory lives separately in the master's SQLite database. No host home or
project directory is mounted. The new image refreshes only its four image-owned
Coop helper scripts before systemd starts. Other managed helper binaries survive.
Packages and system configuration outside these volumes belong in the image;
arbitrary root-disk modifications are intentionally not carried forward.
Systemd uses the same full in-guest capability/mount-path settings as Apple's
machine wrapper, without its host-home mount or SSH forwarding. This preserves
guest administration, not host administration. Native stop requests systemd
poweroff using the image's explicit stop signal.

Use worker state under `/var/lib/coop/…` and work roots under `/workspace/…`.
Before starting services, the host runs the read-only worker `portable-check`
and compares its installed placement with the host execution pin. A missing
binary, stale configuration or nonportable state/work root blocks service start.
Administrative shell entry remains available for installation and repair.

## Configure a refreshed bird

See [the portable profile example](../examples/coop.portable-agent.toml).
The profile's `execution` block names the real master, stable agent, host,
worker instance and generation. Its absolute `client_config` references that
bird's private, placement-pinned **host** client, not an owner credential.
This reference validates runtime authority; it does not install credentials
inside the guest. Existing explicit grants and worker installation still do that.
For an existing bird, fence its previous placement before creating its refreshed
runtime; never reuse a still-live placement for a second container. Local owned
portable profiles cannot duplicate the same execution pin.

Initial setup is: build the image, enter an administrative shell, stage the
matching worker/configuration and initialize native state, then start the bird.
Do not launch its services until identity and installation checks succeed.
The same canonical `/var/lib/coop/worker/config.json` is used for the host check
and the managed worker service.

## Replace an image

For a worker bird, use the existing master recovery/placement protocol first:

1. Hold the worker and all producers; checkpoint useful local work. Reconcile
   unknown effects rather than treating replacement as a way to clear them.
2. Submit and accept its exact recovery inventory, advance the master placement
   by one generation with a new worker instance, and stage the corresponding
   worker placement adoption/configuration and newly pinned credential.
3. Update the host profile execution pin and its private bird client. Stop the
   entire container, then run `coop --agent BIRD apply` with the new built image.
4. Check the stopped result and installation before `coop --agent BIRD up`.

`apply` independently reads live authenticated identity and the master's exact
old→new placement proof before deleting the old root. It does not register a
placement, invent a recovery decision or reuse an old worker token. The old
token is rejected by the master after the placement transition. A diagnostic
heartbeat is not a fencing proof. Ordinary stop/start keeps the same placement.

All four data volumes must exist, match their ownership, and have no references
from other containers, even stopped ones. Replacement removes only the stopped
root/container configuration, creates a fresh root on the same volumes and
leaves it stopped. Its host runtime incarnation changes independently of the
worker placement. No model or provider mutation is submitted by replacement.

A private host journal precedes the destructive step. If interrupted, retry
`apply` with unchanged configuration. It recognizes an acknowledged new root
without recreating it. Ordinary `up` cannot bypass a pending transaction, adopt
orphaned volumes, or invent an empty replacement for a missing owned container.
Unexpected runtime drift, changed intent, missing volumes or lost authority
leave recovery held rather than deleting data.

## Backup and recovery

`backup` includes the cold root and all four volumes. `restore` requires an
absent, explicitly configured container profile and the archive's image. Worker
restore additionally requires the next authenticated placement proof; it cannot
clone a live execution identity. The target stays stopped and ownership is not
published until disk restoration succeeds. Restored worker configuration must
be adopted/staged to the new placement before services can start.

Cold recovery is verified on Apple Container **1.6.1 for portable agents**, in
addition to the existing 1.5.x support. Other 1.6 releases, machine-plugin disks
and interactive maintenance are not implicitly enabled by this verification.
See the [real cold-recovery evidence](evidence/bird-cold-recovery-pilot-2026-11-03.md).

Archives can contain login stores and secrets written to disk. They are private
but unencrypted. Existing machines and their backups remain unchanged; their
root disks cannot be grafted into this volume layout. Import useful memory and
explicitly copy useful portable work when preparing refreshed birds instead of
rescuing obsolete jobs wholesale.

## Acceptance boundary

The [disposable live pilot](evidence/bird-runtime-pilot-2026-11-03.md) passed real
boot/mount, native-session continuity, placement fencing and interrupted image
replacement on Apple Container 1.6.1. Mocked cold backup/restore tests also pass,
and a [real CLI cold backup/restore](evidence/bird-cold-recovery-pilot-2026-11-03.md)
now passes for portable agents on that exact release.
Production birds have not been converted, restarted or deployed, and no existing
memory import has run. Full workflow UAT remains a gate before fleet activation.
gh-sweep remains untouched.
