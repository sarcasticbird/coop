#!/bin/sh
# Run the isolated MCP contract suite. Live fixtures require a separate reviewed packet.
set -eu
agent=""
while [ "$#" -gt 0 ]; do
 case "$1" in
 --agent) [ "$#" -ge 2 ] || exit 2; agent=$2; shift 2 ;;
 *) printf '%s\n' 'Only --agent mcp-uat-<name> is accepted; production targets are refused.' >&2; exit 2 ;;
 esac
done
case "$agent" in mcp-uat-?*) ;; *) printf '%s\n' 'A disposable mcp-uat- agent identity is required.' >&2; exit 2 ;; esac
# This mode creates no VM, service, caller credential or tailnet route.
exec flox activate -- go test -race ./internal/hostcontrol ./internal/hostmcp ./internal/workspace ./internal/agent ./internal/session ./cmd/coop
