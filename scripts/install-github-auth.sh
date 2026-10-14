#!/bin/bash
# Install a trusted, compiled guest helper without starting or restarting it.
set -euo pipefail
if [[ $# != 3 ]]; then echo 'usage: install-github-auth.sh agent linux-arm64-helper app-id' >&2; exit 64; fi
agent=$1
helper=$2
app_id=$3
[[ "$agent" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ && "$agent" != *--* ]] || { echo 'invalid agent name' >&2; exit 64; }
[[ "$app_id" =~ ^[1-9][0-9]{0,17}$ ]] || { echo 'invalid App ID' >&2; exit 64; }
[[ "$helper" == /* && -f "$helper" && ! -L "$helper" ]] || { echo 'helper must be an absolute regular file' >&2; exit 64; }
digest=$(python3 - "$helper" <<'PY'
import hashlib, sys
with open(sys.argv[1], 'rb') as source:
    data = source.read()
if len(data) < 64 or data[:6] != b'\x7fELF\x02\x01' or data[18:20] != b'\xb7\x00':
    raise SystemExit('helper must be a Linux arm64 ELF executable')
print(hashlib.sha256(data).hexdigest())
PY
)
runtime=${COOP_CONTAINER_BIN:-container}
name=coop-agent-$agent
child=$("$runtime" machine inspect "$name" | python3 -c '
import json, sys
rows=json.load(sys.stdin)
if len(rows)!=1 or rows[0].get("id")!=sys.argv[1] or rows[0].get("status")!="running" or rows[0].get("homeMount")!="none" or not rows[0].get("containerId"):
    raise SystemExit("agent must be running, unshared, and match the selected identity")
print(rows[0]["containerId"])
' "$name")
install_script='set -eu
umask 077
digest=$1
app_id=$2
mkdir -p /usr/local/libexec /var/lib/coop
if test -e /var/lib/coop/github-app-id; then
  test "$(cat /var/lib/coop/github-app-id)" = "$app_id" || { echo "configured App identity conflicts" >&2; exit 1; }
fi
tmp=$(mktemp /usr/local/libexec/.coop-github.XXXXXXXX)
pin=$(mktemp /var/lib/coop/.github-app-id.XXXXXXXX)
trap '\''rm -f -- "$tmp" "$pin"'\'' EXIT
cat > "$tmp"
actual=$(sha256sum "$tmp"); actual=${actual%% *}
test "$actual" = "$digest" || { echo "helper checksum mismatch" >&2; exit 1; }
chmod 0755 "$tmp"
printf "%s\n" "$app_id" > "$pin"
chmod 0600 "$pin"
mv -f -- "$pin" /var/lib/coop/github-app-id
mv -f -- "$tmp" /usr/local/libexec/coop-github
trap - EXIT
'
"$runtime" exec --user root -i "$child" sh -c "$install_script" coop-install-github "$digest" "$app_id" < "$helper"
printf 'GitHub helper staged for %s; agent not restarted.\n' "$agent"
