# Portable bird cold recovery — 2026-11-03

## Result

**Passed on Apple Container 1.6.1, macOS 27.0.1, Apple silicon.** The staged CLI
completed a real cold backup/restore round trip in 77 seconds. Full tests,
focused race tests, vet and Linux ARM64 builds also passed. This clears the
portable cold-recovery gate, not fleet workflow or rollout acceptance.

Source: `coop-agent-pilot-worker-vvs7d`. Fresh successful target:
`coop-agent-pilot-restore-vvs7g`. Both use
`coop-machine:local-b6e3eb00edc2-611f74ebb0e1` (OpenCode 1.18.18).

| Check | Result |
| --- | --- |
| Actual CLI archives the cold root and all four data volumes | Pass; five checksummed payloads, mode 0600 |
| Existing archive or restore destination cannot be overwritten | Pass |
| Live placement cannot be cloned | Pass; refused before target creation |
| Next master placement required; old token fenced | Pass; generation 2 → 3 |
| Restore remains stopped until explicit startup | Pass |
| All restored logical disk bytes match the source before boot | Pass |
| Sparse allocation retained | Pass; approximately 2.5 TiB logical, 3.8 GiB allocated |
| Stale restored worker configuration blocks helper startup | Pass |
| Actual worker adopts/reconciles new placement before startup | Pass |
| Existing native session, Git checkpoint and uncommitted work retained | Pass |
| Worker state, synthetic receipt and managed helper files retained | Pass |
| Restored guest reads the preserved master's SQLite memory | Pass; original body and revision 1 |
| Source disk digests, inode identities, sizes, mtimes and allocation unchanged | Pass |
| All disposable pilot/restore containers left stopped | Pass |

The retained archive is 1,176,010,219 bytes. The restored native session is
`ses_e5d8bb14bffe2fU8m6Mxg96Rwj`; the checkpoint is
`8b6efba3bd15fcc2bdeb42e34379563461e33fb0`. Private JSON evidence records each
step and both source/destination disk proofs. An earlier attempt was retained
stopped after its allocation-dependent hash assertion was corrected; logical
bytes were independently verified equal, not assumed equal.

The original workspace remained running on `coop:local-e1ccd0610816`.
Production bird configuration/storage and gh-sweep were not changed. No
production deployment, existing-memory import, model prompt, GitHub operation
or Kata workflow occurred.

## Scope

This fixture tests Apple Container **1.6.1 portable named agents only**. It does
not enable arbitrary 1.6.x releases, the machine plugin, or interactive
maintenance. Existing 1.5.x compatibility remains unchanged.

The inspected pilot uses these actual native storage paths relative to the
runtime-reported application root:

- `containers/<NAME>/rootfs.json`: an ext4 block filesystem with destination
  `/` and source `containers/<NAME>/rootfs.ext4`.
- `volumes/<NAME>--{home,workspace,state,helpers}/volume.img`: four sparse ext4
  disks. Public volume inspection agrees with each owned path.

This matches Apple's tagged 1.6.1 implementation:

- [Bundle.swift](https://github.com/apple/container/blob/1.6.1/Sources/ContainerResource/Container/Bundle.swift)
- [Filesystem.swift](https://github.com/apple/container/blob/1.6.1/Sources/ContainerResource/Container/Filesystem.swift)
- [VolumeConfiguration.swift](https://github.com/apple/container/blob/1.6.1/Sources/ContainerResource/Volume/VolumeConfiguration.swift)

Host disk access was explicitly approved for disposable pilot resources. The
source pilot remains stopped and is opened read-only. All writes go to new
private host fixture state or the fresh restore target, never production storage.

## Repeatable fixture

`TestPortableColdRestorePilot` in `cmd/coop/cold_pilot_test.go` invokes an actual
staged Coop CLI binary, not a mocked runtime. First run the
[runtime pilot](bird-runtime-pilot-2026-11-03.md), then use its successful private
state/evidence directory as the source. The fixture requires its closed,
checkpointed synthetic master database and epoch lineage; these are copied to
new private state, not reopened in place. It refuses a nonempty source WAL.

```sh
COOP_COLD_PILOT=1 \
COOP_COLD_ROOT=/absolute/private/fresh-cold-pilot \
COOP_COLD_SOURCE_ROOT=/absolute/private/successful-runtime-pilot \
COOP_COLD_NAME=pilot-fresh-restore-name \
COOP_COLD_CLI=/absolute/private/staged-coop \
COOP_COLD_GATEWAY=ACTUAL_APPLE_NETWORK_GATEWAY \
flox activate -- go test ./cmd/coop \
  -run '^TestPortableColdRestorePilot$' -v -count=1 -timeout=25m
```

The parent of `COOP_COLD_ROOT` must exist; the directory itself, runtime target,
and target volumes must not. The source must be a placement-bound portable
runtime pilot, with the fixture's no-op worker and no consequential model turns.
Its empty recovery inventory is checked by the copied synthetic master. This
does not establish recovery safety for a real engineering/GitHub/Kata workflow.

The fixture retains a private `pilot.zip`, JSON evidence, synthetic host
configuration/credentials, and stopped restore resources. It never deletes an
earlier attempt or overwrites an archive/target. Archives are mode 0600 and
unencrypted; retained synthetic credentials are private fixture material.

## Verification details

- Hash the source sparse streams and compare them with all five archive payload
  checksums. Check source inode, size, modification time, allocation and digest
  again after recovery.
- Before booting the restored target, compare every logical byte across the
  union of source and destination allocated extents. Outside that union, both
  disks contain holes (zero bytes). APFS can coalesce zero-filled extents on
  restore, so allocation-dependent sparse-stream hashes need not be identical
  even when the logical disks are identical. Sparse allocation is checked
  separately; no multi-terabyte hole scan is needed.
- Refuse a live placement clone, require the next authenticated placement proof,
  reject the old token, and block service start while the restored worker still
  has its old execution pin. Adopt and reconcile the new placement before
  starting services.

The compatibility tests also reproduce an image-name mismatch: Apple expands
local names to `docker.io/library/...`. Restore now normalizes that spelling
when checking the selected image, while still refusing a genuinely different
image before creating a target.
