#!/usr/bin/env bash
set -euo pipefail
umask 077

# Synthetic validation only. Invoke through flox activate from the repository root.
expected=${1:?usage: bash scripts/uat-tui-b.sh EXACT_SOURCE_SHA}
actual=$(git rev-parse HEAD)
if [[ "$expected" != "$actual" || ! "$expected" =~ ^[0-9a-f]{40}$ ]]; then
  printf 'Expected an exact source SHA matching HEAD. Found %s\n' "$actual" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  printf 'Use a clean checkout of the exact source artifact.\n' >&2
  exit 1
fi
out=$(mktemp -d "${TMPDIR:-/tmp}/coop-tui-b-uat.XXXXXXXX")
printf 'Private synthetic evidence directory: %s\n' "$out"
exec > >(tee "$out/validation.log") 2>&1

digest() {
  if command -v sha256sum >/dev/null; then
    sha256sum "$1"
  else
    shasum -a 256 "$1"
  fi
}

export GOWORK=off GOFLAGS=
os=$(go env GOOS)
arch=$(go env GOARCH)
{
  printf 'environment_revision=coop-tui-b-runtime-v1\nsource_sha=%s\n' "$actual"
  printf 'task=https://github.com/sarcasticbird/coop/issues/121\n'
  printf 'platform=%s/%s\n' "$os" "$arch"
  go version
  printf 'build=CGO_ENABLED=0 go build -trimpath -buildvcs=true -o ARTIFACT ./cmd/coop\n'
  printf 'build_environment=GOWORK=off GOFLAGS= GOOS=%s GOARCH=%s\n' "$os" "$arch"
  printf 'review=pending independent exact-head Robin gate\n'
  printf 'qa=pending private exact-artifact Wren gate\n'
  printf 'owner_uat=pending operator observation; no production effects authorized\n'
  digest go.mod
  digest go.sum
  digest .flox/env/manifest.toml
} > "$out/manifest.txt"
cat "$out/manifest.txt"

CGO_ENABLED=0 go build -trimpath -buildvcs=true -o "$out/coop" ./cmd/coop
CGO_ENABLED=0 go build -trimpath -buildvcs=true -o "$out/coop-repeat" ./cmd/coop
cmp "$out/coop" "$out/coop-repeat"
digest "$out/coop" | tee -a "$out/manifest.txt"
go version -m "$out/coop" | tee -a "$out/manifest.txt"

if [[ -n "$(gofmt -l internal/tui cmd/coop)" ]]; then
  printf 'TUI/CLI formatting is not clean.\n' >&2
  exit 1
fi
git diff --check
go test ./internal/tui/... ./internal/config ./internal/hostrouter ./internal/masterclient ./internal/coordination ./internal/master ./internal/mastermcp ./cmd/coop -count=1
go test ./internal/tui -run '^TestRuntimeIntegration|^TestForeground|^TestTerminal' -count=10
go test ./cmd/coop -run 'TestTUICommandCoverageExhaustive|TestFleetTUICommandCoverageInventory|TestRemoteTerminal' -count=1 -v
NO_COLOR=1 TERM=dumb go test ./internal/tui/... ./cmd/coop -count=1
go build ./...
go vet ./...
# Set CC to an already available compiler if the activation PATH lacks one.
CGO_ENABLED=1 go test -race ./internal/tui/... ./internal/config ./internal/hostrouter ./internal/masterclient ./internal/coordination ./internal/master ./cmd/coop -count=1
printf 'synthetic_validation=passed\n' | tee -a "$out/manifest.txt"
printf 'PASS: reproducible build and synthetic journeys only; independent review, QA, CI and owner/operator UAT are separate gates.\n'
