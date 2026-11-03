# Disposable bird runtime pilot — 2026-11-03

## Result

**Core runtime acceptance passed on Apple Container 1.6.1, macOS 27.0.1,
Apple silicon. This is not fleet rollout approval.** Cold backup/restore remains
held because the disk adapter is verified only for Apple Container 1.5.x.

The pilot used isolated host configuration/state, a separate temporary master
SQLite database, synthetic principals and placement tokens, empty credential
grants, and no GitHub/Kata access or model prompts. Production configuration,
bird disks, task data and gh-sweep were not changed.

## Checks performed

| Check | Result |
| --- | --- |
| Actual CLI creates and boots a named portable container | Pass; systemd running |
| Home, workspace, Coop state and managed helpers on independent guest-only disks | Pass; four ext4 mounts, no host bind mounts |
| Ordinary `up` refuses image drift | Pass; maintenance-required error |
| Explicit `apply` replaces the root and leaves it stopped | Pass |
| Real worker binary validates portable paths and installed placement | Pass |
| Guest reads/writes memory through authenticated Coop HTTP APIs | Pass |
| Recovery inventory accepted, placement advanced, guest adoption performed | Pass |
| Previous placement token rejected before and after root replacement | Pass |
| Intentional interruption after old-root removal | Pass; ordinary startup refused, unchanged `apply` resumed |
| Same native OpenCode session retained, with no duplicate session | Pass |
| Local Git commit and uncommitted source changes retained | Pass |
| Worker state, managed helper files and synthetic receipt bytes retained | Pass |
| Identical memory retry after replacement | Pass; revision remains 1 |
| CLI cold backup on unverified 1.6 layout | Correctly refused, not silently enabled |
| Pilot containers left stopped | Pass |

The successful bound-pilot run used these actual built images:

- Before: `coop-machine:local-b8bfa2872e8b-611f74ebb0e1` (OpenCode 1.18.3).
- After: `coop-machine:local-b6e3eb00edc2-611f74ebb0e1` (OpenCode 1.18.18).

The isolated worker slot was deliberately a no-op service. The actual worker
binary performed `init-state`, `portable-check`, memory operations, recovery
inventory reconciliation and placement adoption. The OpenCode service created
and retained one native session without submitting a model turn. This does not
claim that a live engineering action, GitHub merge or model turn was replayed
safely; those remain separate workflow acceptance checks.

## Defects found and corrected

1. **Image/server version drift.** The core lock contained OpenCode 1.18.3 while
   dispatch required 1.18.18. Pin the image to the existing supported version,
   without weakening health admission. The Linux package entry was obtained
   through Flox resolution; all other core package locks are unchanged. A test
   now compares the embedded image pin with the worker runtime contract.
2. **Systemd readiness race.** Apple's successful container start can precede
   the systemd manager socket. Portable startup now waits, with a deadline,
   before service operations. Already-running containers use the same readiness
   check. A degraded but usable manager remains available for administrative
   repair; worker authority and individual service checks still apply.

## Repeatable fixture

`TestPortableRuntimePilot` in `internal/agent/portable_pilot_test.go` is skipped
unless explicitly enabled. It requires a fresh private state directory, a fresh
`pilot-*` profile name, two built images and Linux ARM64 worker/proxy binaries.
The fixture fails rather than deleting or resetting previous pilot state.

Build the proxy from `scripts/pilot-master-proxy`. It binds only guest loopback
and accepts only literal private/loopback upstream addresses. Its plaintext
transport is restricted to synthetic acceptance credentials; it is not a
production transport or credential pattern.

```sh
COOP_PORTABLE_PILOT=1 \
COOP_PILOT_ROOT=/absolute/private/fresh-pilot \
COOP_PILOT_NAME=pilot-unique-name \
COOP_PILOT_IMAGE_A=EXACT_SOURCE_IMAGE \
COOP_PILOT_IMAGE_B=EXACT_TARGET_IMAGE \
COOP_PILOT_WORKER=/absolute/private/coop-worker-linux \
COOP_PILOT_PROXY=/absolute/private/pilot-master-proxy-linux \
COOP_PILOT_GATEWAY=ACTUAL_APPLE_NETWORK_GATEWAY \
flox activate -- go test ./internal/agent \
  -run '^TestPortableRuntimePilot$' -v -count=1 -timeout=20m
```

The fixture leaves stopped runtime/data volumes and private JSON evidence for
inspection. Synthetic token material is retained only in its private directory.
There is no automatic destruction, production profile installation, provider
credential acquisition or live master restart.

## Remaining gates

- Verify the current runtime's cold-storage layout and exercise an actual
  backup/restore round trip before making fleet replacement the normal path.
  This pilot neither read unapproved runtime disk paths nor bypassed the
  existing version guard.
- Run the real engineering → independent review → QA → Rook merge → Kata
  completion journey, including interrupted consequential operations.
- Plan explicit memory import and fleet activation after those gates pass.
