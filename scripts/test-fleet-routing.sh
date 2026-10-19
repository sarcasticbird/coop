#!/usr/bin/env bash
# fleet-tui-uat-v1: offline fixtures only, never live UAT or customer QA.
set -euo pipefail

if [[ $# != 2 || ! $1 =~ ^[0-9a-f]{40}$ || ! $2 =~ ^[0-9a-f]{40}$ ]]; then
  printf '%s\n' 'Usage: scripts/test-fleet-routing.sh BACKEND_FULL_COMMIT_SHA TUI_FULL_COMMIT_SHA' >&2
  exit 2
fi

root=$(git -C "$(dirname -- "${BASH_SOURCE[0]}")/.." rev-parse --show-toplevel)
cd -- "$root"
backend_head=$(git rev-parse HEAD)
if [[ $1 != "$backend_head" ]]; then
  printf 'Backend head mismatch: requested %s, checkout %s\n' "$1" "$backend_head" >&2
  exit 2
fi

printf '%s\n' 'fleet-tui-uat-v1: fixture evidence only; no live SSH, install, restart, Wren QA or live UAT.'
printf 'backend_head=%s\ntui_head_reference=%s\n' "$backend_head" "$2"
printf '%s\n' 'TUI head is supplied provenance only; no separate TUI checkout is tested.'
printf 'utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
uname -srm
printf 'effective_uid=%s\numask=%s\n' "$(id -u)" "$(umask)"
printf '%s\n' 'Worktree status (dirty results are not exact-commit clearance):'
git status --short

# Re-enter once through the repository environment; no application is launched.
exec flox activate -- bash -c '
  set -euo pipefail
  # Existing fixture mode assertions assume the standard development umask.
  umask 022
  go version
  go env -json GOOS GOARCH GOVERSION GOTOOLCHAIN CGO_ENABLED
  # Fail closed if a regression reaches SSH without its own test stand-in.
  # Install this PATH guard after Flox activation so it takes precedence.
  [[ -d ${TMPDIR:-/tmp} ]]
  guard=$(mktemp -d "${TMPDIR:-/tmp}/coop-fleet-fixtures.XXXXXXXX")
  trap '\''rm -rf -- "$guard"'\'' EXIT
  printf "%s\n" "#!/bin/sh" "exit 255" > "$guard/ssh"
  chmod 700 "$guard/ssh"
  export PATH="$guard:$PATH"
  status=0
  printf "%s\n" "Formatting check (gofmt -l ., read-only equivalent of go fmt):"
  if formatting=$(gofmt -l .); then
    if [[ -n $formatting ]]; then
      printf "%s\n" "$formatting"
      printf "%s\n" "FAIL: Go formatting differs; no files were rewritten."
      status=1
    else
      printf "%s\n" "PASS: Go formatting"
    fi
  else
    printf "%s\n" "FAIL: formatting check could not complete."
    status=1
  fi
  printf "%s\n" "go vet ./..."
  if go vet ./...; then
    printf "%s\n" "PASS: go vet"
  else
    status=1
  fi
  printf "%s\n" "go test -race -count=1 ./..."
  if go test -race -count=1 ./...; then
    printf "%s\n" "PASS: race fixtures"
  else
    status=1
  fi
  printf "Fixture exit status: %s (not live UAT or Wren QA)\n" "$status"
  exit "$status"
'
