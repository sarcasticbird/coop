#!/bin/bash
# Explicit stage/apply/rollback; never starts a VM or modifies image assets.
set -euo pipefail
[[ $# == 2 ]] || { echo 'usage: upgrade-agent-flox.sh AGENT stage|apply|rollback' >&2; exit 64; }
agent=$1
mode=$2
[[ "$agent" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ && "$agent" != *--* ]] || exit 64
case "$mode" in stage|apply|rollback) ;; *) exit 64;; esac
runtime=${COOP_CONTAINER_BIN:-container}
child=$("$runtime" machine inspect "coop-agent-$agent" | /usr/bin/python3 -c '
import json,sys
rows=json.load(sys.stdin)
if len(rows)!=1 or rows[0].get("id")!="coop-agent-"+sys.argv[1] or rows[0].get("status")!="running" or rows[0].get("homeMount")!="none" or not rows[0].get("containerId"):
    raise SystemExit("agent must be running, unshared and match its identity")
print(rows[0]["containerId"])
' "$agent")
source_root=$(cd "$(dirname "$0")/.." && pwd)
checksum=$(shasum -a 256 "$source_root/image/machine/coop-env")
checksum=${checksum%% *}
"$runtime" exec --user root -i "$child" /bin/bash -s -- "$mode" "$checksum" < "$source_root/scripts/upgrade-guest-flox"
