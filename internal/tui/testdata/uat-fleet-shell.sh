#!/usr/bin/env bash
set -euo pipefail

# Fixture validation only. The optional checklist does not execute fleet actions.
expected=${1:?usage: bash internal/tui/testdata/uat-fleet-shell.sh EXPECTED_HEAD [--operator-checklist]}
actual=$(git rev-parse HEAD)
if [[ "$actual" != "$expected" ]]; then
  printf 'Head mismatch: expected %s, found %s\n' "$expected" "$actual" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  printf 'Use a clean checkout of the exact artifact head.\n' >&2
  exit 1
fi
printf 'fleet-tui-uat-v1 shell fixtures at %s\n' "$actual"
if [[ -n "$(gofmt -l internal/tui/*.go)" ]]; then
  printf 'TUI formatting is not clean.\n' >&2
  exit 1
fi
go vet ./...
go test ./internal/tui ./cmd/coop ./internal/hostrouter
CGO_ENABLED=1 go test -race ./internal/tui ./cmd/coop ./internal/hostrouter -count=5
printf 'PASS: author/operator fixture reproduction, not independent QA or live UAT.\n'

if [[ "${2:-}" == '--operator-checklist' ]]; then
  printf '%s\n' \
    'LIVE UAT PENDING: perform only in the separately authorized fixture/operator environment.' \
    '1. Record exact artifact head, environment revision fleet-tui-uat-v1, platform and terminal size privately.' \
    '2. Launch coop tui with existing authorized registry references. Confirm explicit local and remote host identities.' \
    '3. Use 1/3, arrows, tab, ?, Escape and refresh. Repeat at 120x40 and 40x20; use PgUp/PgDn for long overlays.' \
    '4. Observe an already-offline fixture remote. Confirm local navigation remains responsive and old rows are stale, never stopped/empty.' \
    '5. In a disposable private registry only, test add/edit/remove, invalid inputs, cancel, address changes and selection clearing.' \
    '6. On an explicitly authorized running fixture, enter its terminal, resize, then Ctrl+] detach. Confirm browser redraw and refresh.' \
    '7. Verify terminal normal exit and simulated disconnect return useful host-qualified errors; do not retry mutations automatically.' \
    '8. Lifecycle/destroy tests are fixture-only. Inspect captured host/kind/ID in confirmation; cancel before changing selections.' \
    '9. Do not restart production services, deploy binaries, expand credentials or publish terminal transcripts. Record pending live checks honestly.' \
    '10. Robin exact-head engineering review and Wren private exact-head QA remain required. This checklist grants no authority.'
fi
