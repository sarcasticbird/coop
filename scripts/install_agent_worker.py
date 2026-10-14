"""Stage the worker and launchers into an existing, running named Coop."""
import hashlib
import io
import json
import logging
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tarfile

LOG = logging.getLogger("coop.worker.install")
SCRIPTS = Path(__file__).resolve().parent

GUEST_INSTALL = r'''set -eu
umask 077
expected=$1
agent=$2
test "$(uname -m)" = aarch64 || { echo 'guest architecture mismatch' >&2; exit 1; }
test ! -L /var/lib/coop/worker && test ! -L /usr/local/libexec || exit 1
mkdir -p /var/lib/coop/worker /usr/local/libexec
test "$(stat -c %a /var/lib/coop/worker)" = 700 || exit 1
stage=$(mktemp -d /var/lib/coop/worker/.install.XXXXXXXX)
trap 'rm -rf -- "$stage"' EXIT
cat > "$stage/package.tar"
actual=$(sha256sum "$stage/package.tar"); actual=${actual%% *}
test "$actual" = "$expected" || { echo 'worker package checksum mismatch' >&2; exit 1; }
tar -xf "$stage/package.tar" -C "$stage"
(cd "$stage" && sha256sum -c SHA256SUMS >/dev/null)
chmod 0755 "$stage/coop-worker" "$stage/coop-opencode-server" "$stage/coop-opencode-attach" "$stage/coop-opencode-launch"
chmod 0600 "$stage/config.json"
"$stage/coop-worker" validate --config "$stage/config.json" >/dev/null
test "$(cat "$stage/identity")" = "$agent" || exit 1
manifest=/var/lib/coop/worker/install.sha256
if test -e "$manifest"; then
  test ! -L "$manifest" && test "$(stat -c %a "$manifest")" = 600 || exit 1
  sha256sum -c "$manifest" >/dev/null || { echo 'independent guest changes; refusing replacement' >&2; exit 1; }
fi
backup=/var/lib/coop/worker/install-before
test ! -L "$backup" || exit 1
mkdir -p "$backup"
test "$(stat -c %a "$backup")" = 700 || exit 1
for file in coop-worker coop-opencode-server coop-opencode-attach coop-opencode-launch config.json; do
  destination=/usr/local/libexec/$file
  if test "$file" = config.json; then destination=/var/lib/coop/worker/config.json; fi
  test ! -L "$destination" || exit 1
  if test -e "$destination"; then
    test -f "$destination" || exit 1
    mode=755
    if test "$file" = config.json; then mode=600; fi
    test "$(stat -c %a "$destination")" = "$mode" || { echo 'independent artifact mode change' >&2; exit 1; }
    if test ! -e "$manifest"; then
      echo 'unmanaged worker artifact; refusing replacement' >&2; exit 1
    fi
    cp -p "$destination" "$stage/$file.previous"
    if test ! -e "$backup/$file"; then cp -p "$destination" "$backup/$file"; fi
  fi
done
rollback() {
  for file in coop-worker coop-opencode-server coop-opencode-attach coop-opencode-launch config.json; do
    destination=/usr/local/libexec/$file
    if test "$file" = config.json; then destination=/var/lib/coop/worker/config.json; fi
    if test -e "$stage/$file.installed"; then
      if test -e "$stage/$file.previous"; then mv -f "$stage/$file.previous" "$destination"; else rm -f "$destination"; fi
    fi
  done
  rm -rf -- "$stage"
}
trap rollback EXIT
: > "$stage/manifest"
for file in coop-worker coop-opencode-server coop-opencode-attach coop-opencode-launch config.json; do
  destination=/usr/local/libexec/$file
  if test "$file" = config.json; then destination=/var/lib/coop/worker/config.json; fi
  # Each rename is on its destination filesystem.
  next=$(mktemp "$(dirname "$destination")/.worker.XXXXXXXX")
  cp -p "$stage/$file" "$next"
  mv -f "$next" "$destination"
  touch "$stage/$file.installed"
  sha256sum "$destination" >> "$stage/manifest"
done
chmod 0600 "$stage/manifest"
mv -f "$stage/manifest" "$manifest"
trap 'rm -rf -- "$stage"' EXIT
'''


def invoke(argv, payload=None):
    result = subprocess.run(argv, input=payload, capture_output=True)
    if result.returncode:
        # Never echo guest command stdout/stderr; it may contain config data.
        raise ValueError("container operation failed; staging did not activate services")
    return result.stdout


def running_child(runtime, agent):
    name = "coop-agent-" + agent
    rows = json.loads(invoke([runtime, "machine", "inspect", name]))
    if len(rows) != 1 or rows[0].get("id") != name or rows[0].get("status") != "running" or rows[0].get("homeMount") != "none" or not rows[0].get("containerId"):
        raise ValueError("agent must be running, unshared and match the selected identity")
    return rows[0]["containerId"]


def regular(path, private=False):
    if not path.is_absolute() or path.is_symlink() or not path.is_file():
        raise ValueError("artifact must be an absolute regular file")
    if private and stat.S_IMODE(path.stat().st_mode) != 0o600:
        raise ValueError("worker config must be mode 0600")
    return path.read_bytes()


def install(agent, binary, config):
    if not re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", agent) or "--" in agent:
        raise ValueError("invalid agent identity")
    executable = regular(binary)
    if len(executable) < 64 or executable[:6] != b"\x7fELF\x02\x01" or executable[18:20] != b"\xb7\x00":
        raise ValueError("worker must be Linux arm64 ELF")
    private = regular(config, private=True)
    if len(private) > 1 << 20 or json.loads(private).get("name") != agent:
        raise ValueError("worker config identity mismatch")
    files = {"coop-worker": executable, "config.json": private, "identity": agent.encode()}
    for name in ("coop-opencode-server", "coop-opencode-attach", "coop-opencode-launch"):
        files[name] = (SCRIPTS / name).read_bytes()
    files["SHA256SUMS"] = "".join(hashlib.sha256(data).hexdigest() + "  " + name + "\n" for name, data in files.items()).encode()
    payload = io.BytesIO()
    with tarfile.open(fileobj=payload, mode="w") as archive:
        for name, data in files.items():
            entry = tarfile.TarInfo(name)
            entry.size, entry.mode = len(data), 0o600
            archive.addfile(entry, io.BytesIO(data))
    data = payload.getvalue()
    runtime = os.environ.get("COOP_CONTAINER_BIN", "container")
    child = running_child(runtime, agent)
    invoke([runtime, "exec", "--user", "root", "-i", child, "sh", "-c", GUEST_INSTALL, "coop-install-worker", hashlib.sha256(data).hexdigest(), agent], data)
    LOG.info("worker artifacts staged", extra={"agent": agent, "activated": False})


if __name__ == "__main__":
    logging.basicConfig(format='{"level":"%(levelname)s","message":"%(message)s"}', level=logging.INFO)
    try:
        if len(sys.argv) != 4:
            raise ValueError("usage: install-agent-worker.sh AGENT /absolute/linux-arm64-worker /absolute/private-worker.json")
        install(sys.argv[1], Path(sys.argv[2]), Path(sys.argv[3]))
    except (ValueError, OSError, json.JSONDecodeError):
        LOG.error("worker staging failed; verify identity, artifacts, private config and guest ownership")
        sys.exit(1)
