#!/bin/sh
# Offline deterministic acceptance only. No VM, service or GitHub mutation.
set -eu
if [ "$#" -ne 0 ]; then
  printf '%s\n' 'This command accepts no live targets. Use the reviewed rollout packet for live acceptance.' >&2
  exit 2
fi
exec flox activate -- go test -race ./internal/masteracceptance ./internal/master ./internal/masterclient ./internal/mastermcp ./internal/tui/team ./internal/worker ./cmd/coop ./cmd/coop-worker
