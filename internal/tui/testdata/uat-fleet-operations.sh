#!/usr/bin/env bash
set -euo pipefail

expected=${1:?usage: bash internal/tui/testdata/uat-fleet-operations.sh EXPECTED_HEAD [--operator-checklist]}
# The shell script checks exact HEAD, clean tree, formatting, vet, fixtures and race.
bash internal/tui/testdata/uat-fleet-shell.sh "$expected"
if [[ -n "$(gofmt -l cmd/coop/tui.go cmd/coop/tui_test.go)" ]]; then
  printf 'Launch adapter formatting is not clean.\n' >&2
  exit 1
fi
printf 'PASS: fleet operations fixtures; environment fleet-tui-uat-v1. Live UAT remains pending.\n'
if [[ "${2:-}" == '--operator-checklist' ]]; then
  printf '%s\n' \
    'LIVE UAT PENDING. Use only separately authorized disposable fixtures, never production guests.' \
    '1. Privately record exact HEAD, integrated backend head, environment fleet-tui-uat-v1, host, OS and terminal size.' \
    '2. Confirm local/remote host, target kind and ID. Open x on Fleet and Servers; inspect disabled reasons.' \
    '3. At 120x40 and 40x20, edit/cancel each form; page long menus, paths, confirmation and output.' \
    '4. Open a destructive confirmation; refresh/reorder or change a disposable registration. Confirm no retargeted mutation.' \
    '5. On authorized fixtures only: terminal attach, resize, local normal exit, remote Ctrl+] detach, foreground failure and disconnect. Check echo/raw mode and browser refresh.' \
    '6. Read worker status/requests and doctor warnings/failures. Check bounded output and host-qualified retry guidance.' \
    '7. With separate fixture mutation authorization only: stopped backup, absent restore, rebuild/apply, lifecycle, worker pause/resume and ping receipt. Never infer a reply or goal approval.' \
    '8. Inspect init consent and credential reference forms without entering secrets; cancel before modifying unapproved state. Foreground watch must end on Ctrl+C.' \
    '9. Open v: verify service, completion, version/help and master recovery runbook distinctions; no daemon/recovery starts.' \
    '10. Record actual evidence privately. Coordination remains #94. Robin review and Wren private exact-head QA are required; pending live steps stay pending.'
fi
