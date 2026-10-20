#!/usr/bin/env bash
set -euo pipefail

# Fixture-only reproduction. Printing the checklist grants no live authority.
if [[ $# -lt 1 || $# -gt 2 || ( $# -eq 2 && "$2" != '--operator-checklist' ) ]]; then
  printf 'usage: bash internal/tui/testdata/uat-fleet-coordination.sh FULL_COMMIT_SHA [--operator-checklist]\n' >&2
  exit 2
fi
expected=$1
actual=$(git rev-parse HEAD)
if [[ "$actual" != "$expected" ]]; then
  printf 'Head mismatch: expected %s, found %s\n' "$expected" "$actual" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  printf 'Use a clean checkout of the exact integrated artifact head.\n' >&2
  exit 1
fi
printf 'fleet-tui-uat-v1 coordination fixtures at %s\n' "$actual"
go version
if [[ -n "$(gofmt -l cmd/coop/tui*.go internal/tui/*.go)" ]]; then
  printf 'TUI/coverage formatting is not clean.\n' >&2
  exit 1
fi
go test ./cmd/coop -run 'Test(FleetTUICommandCoverageInventory|TUICommandCoverageExhaustive)$' -count=1
go test ./internal/tui/... ./cmd/coop ./internal/hostrouter ./internal/masterclient ./internal/coordination -count=1
go vet ./...
CGO_ENABLED=1 go test -race ./internal/tui/... ./cmd/coop ./internal/hostrouter ./internal/masterclient ./internal/coordination -count=5
printf 'PASS: local fixtures only. Live UAT, independent review and customer QA remain pending.\n'

if [[ "${2:-}" == '--operator-checklist' ]]; then
  printf '%s\n' \
    'fleet-tui-uat-v1 OPERATOR CHECKLIST: NOT EXECUTED; no authority is granted by this script.' \
    '1. Privately record exact UI/backend heads, Go/Coop versions, environment revision, host OS, client/master identity, principal role and fixture authorization. Never record tokens.' \
    '2. Separately authorize disposable fixtures and a private evidence destination. Keep production services, guests, registry and goals untouched. Mark unavailable checks pending.' \
    '3. Launch the exact artifact manually with an existing private master-client reference. At 120x40 and 40x20 inspect 1 Fleet, 2 Coordination and 3 Servers; t/l/i/g select Team/Threads/Inbox/Goals. Verify tab, Enter, Escape, help and scrolling.' \
    '4. Verify duplicate display names remain distinct by host/kind/ID and master/principal identity. Observe an already-unavailable fixture host/master: stale/unknown must not become stopped/empty, reroute locally or silently select another backend.' \
    '5. In authorized read-only views: page Threads/Inbox/Goals with ], open a thread, refresh and follow. Escape back must retain the thread-list page/selection and ] continuation, including later pages. In Goals, verify the displayed authorized engagement and use e to cycle it with a reset page. Verify no skipped/duplicated committed messages and no implicit sends. Owner inbox denial is not an empty agent inbox.' \
    '6. Inspect private message/proposal buffers masked by default; toggle Ctrl+V only in private. Verify terminal controls do not alter the screen. Never publish screenshots, bodies, principals or configuration.' \
    '7. Open n/c/p/m/o/d forms, tab through fields and cancel with Escape. Enter must not submit Coordination mutations. First Ctrl+S previews captured identity/input/request ID; cancel and confirm no write.' \
    '8. With separate mutation authorization only: create a disposable thread, send an addressed message, reply and inspect s receipt. Compare queued/received/running/replied/blocked/cancelled/suppressed with actual evidence, not presence or notification.' \
    '9. With authorized fixture fault injection only: lose the response after a send; Ctrl+R must retain identical request ID/input and yield one durable effect. Changed-input reuse must conflict. No automatic replay or cancellation-as-rollback claim.' \
    '10. With owner fixture authorization only: m replaces exact old/new member sets. After a concurrent conflict, Escape cancels the form; r refreshes thread members without losing reader history, then m must snapshot the current members. Exercise revocation; agent/manage denial must not be bypassed or described as an empty view.' \
    '11. With goal fixture authorization only: paste valid structured JSON into o; reject malformed/unknown/oversize inputs without partial writes. Inspect a exact revision authority. d requires action, hash and observed sequence; test stale sequence, wrong revision and non-owner denial.' \
    '12. Only if separately authorized, exercise approve/reject/pause/resume/complete/reopen on disposable revisions. Confirm new draft versus replacement semantics and no inference from chat, receipts or legacy approve.' \
    '13. Inspect x confirmations for captured host/path/argv and disabled reasons. On disposable fixtures only, verify selection reordering/registration changes cannot retarget actions. Never replay uncertain lifecycle operations blindly.' \
    '14. Only on authorized terminal fixtures: attach, resize, normal exit, remote Ctrl+] detach and simulated disconnect; confirm terminal echo/raw mode, host-qualified errors and browser refresh. Ctrl+C reaches the foreground app.' \
    '15. Open v from Fleet, Servers and Coordination outside forms; verify services/help/version/completion and master recovery distinctions, then Escape back. In forms v must remain literal text. Do not launch daemons, deploy, restart services, expand credentials, import history, migrate state or restore a master for this check.' \
    '16. Record each performed step as pass/fail/pending with private exact-head evidence. Robin independent engineering review and Wren private exact-head customer QA remain separate requirements. Printed output is not live UAT evidence.'
fi
