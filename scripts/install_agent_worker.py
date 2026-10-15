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
owner=$(id -u):$(id -g)
test "$(uname -m)" = aarch64 || { echo 'guest architecture mismatch' >&2; exit 1; }
test ! -L /var/lib/coop/worker && test ! -L /usr/local/libexec || exit 1
mkdir -p /var/lib/coop/worker /usr/local/libexec
test "$(stat -c %a /var/lib/coop/worker)" = 700 || exit 1
test "$(stat -c %u:%g /var/lib/coop/worker)" = "$owner" || exit 1
# The kernel releases this lock even when the installer is killed.
test ! -L /var/lib/coop/worker/install.lock || exit 1
exec 9>/var/lib/coop/worker/install.lock
flock -x 9
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
pending=/var/lib/coop/worker/install-pending
files='coop-worker coop-opencode-server coop-opencode-attach coop-opencode-launch config.json'
destination_for() {
  destination=/usr/local/libexec/$file
  mode=755
  if test "$file" = config.json; then destination=/var/lib/coop/worker/config.json; mode=600; fi
}
matches() {
  test ! -L "$1" && test -f "$1" &&
    test "$(stat -c %a "$1")" = "$mode" &&
    test "$(stat -c %u:%g "$1")" = "$owner" &&
    test "$(sha256sum < "$1")" = "$(sha256sum < "$2")"
}
recover() {
  test ! -L "$pending" && test -d "$pending" && test "$(stat -c %a "$pending")" = 700 || exit 1
  test "$(stat -c %u:%g "$pending")" = "$owner" || exit 1
  for record in identity old.manifest new.manifest evidence.sha256; do
    test ! -L "$pending/$record" && test -f "$pending/$record" || exit 1
    test "$(stat -c %a "$pending/$record")" = 600 || exit 1
    test "$(stat -c %u:%g "$pending/$record")" = "$owner" || exit 1
  done
  for directory in before after; do
    test ! -L "$pending/$directory" && test -d "$pending/$directory" || exit 1
    test "$(stat -c %a "$pending/$directory")" = 700 || exit 1
    test "$(stat -c %u:%g "$pending/$directory")" = "$owner" || exit 1
  done
  test "$(cat "$pending/identity")" = "$agent" || exit 1
  # Check all recovery evidence and live files before changing any of them.
  (cd "$pending" && sha256sum -c evidence.sha256 >/dev/null) || exit 1
  if test -e "$manifest" || test -L "$manifest"; then
    mode=600
    matches "$manifest" "$pending/old.manifest" || matches "$manifest" "$pending/new.manifest" || exit 1
  else
    test ! -s "$pending/old.manifest" || exit 1
  fi
  for file in $files; do
    destination_for
    test ! -L "$pending/after/$file" && test -f "$pending/after/$file" || exit 1
    test "$(stat -c %a "$pending/after/$file")" = "$mode" || exit 1
    test "$(stat -c %u:%g "$pending/after/$file")" = "$owner" || exit 1
    if test -e "$pending/before/$file"; then
      test ! -L "$pending/before/$file" && test "$(stat -c %a "$pending/before/$file")" = "$mode" || exit 1
      test "$(stat -c %u:%g "$pending/before/$file")" = "$owner" || exit 1
    fi
    if test -e "$destination" || test -L "$destination"; then
      matches "$destination" "$pending/after/$file" ||
        { test -f "$pending/before/$file" && matches "$destination" "$pending/before/$file"; } || {
          echo 'independent guest changes; preserving installation transaction' >&2; exit 1;
        }
    else
      test ! -e "$pending/before/$file" || exit 1
    fi
  done
  for file in $files; do
    destination_for
    # Do not consume recovery material. A failed copy/rename is retryable.
    next=$(mktemp "$(dirname "$destination")/.worker.XXXXXXXX")
    cp -p "$pending/after/$file" "$next"
    sync -f "$next"
    mv -f "$next" "$destination"
    sync -f "$(dirname "$destination")"
  done
  next=$(mktemp /var/lib/coop/worker/.manifest.XXXXXXXX)
  cp -p "$pending/new.manifest" "$next"
  sync -f "$next"
  mv -f "$next" "$manifest"
  sync -f /var/lib/coop/worker
  # Retain verified prior-generation material, including after successful retry.
  history=$(mktemp -d /var/lib/coop/worker/install-history.XXXXXXXX)
  mv "$pending" "$history/generation"
  sync -f /var/lib/coop/worker
}
if test -e "$pending" || test -L "$pending"; then recover; fi
if test -e "$manifest" || test -L "$manifest"; then
  test ! -L "$manifest" && test "$(stat -c %a "$manifest")" = 600 || exit 1
  test "$(stat -c %u:%g "$manifest")" = "$owner" || exit 1
  sha256sum -c "$manifest" >/dev/null || { echo 'independent guest changes; refusing replacement' >&2; exit 1; }
fi
mkdir "$stage/before" "$stage/after"
: > "$stage/old.manifest"
if test -e "$manifest"; then cp -p "$manifest" "$stage/old.manifest"; fi
: > "$stage/new.manifest"
: > "$stage/current.manifest"
for file in $files; do
  destination_for
  test ! -L "$destination" || exit 1
  if test -e "$destination"; then
    test -f "$destination" || exit 1
    test "$(stat -c %a "$destination")" = "$mode" || { echo 'independent artifact mode change' >&2; exit 1; }
    test "$(stat -c %u:%g "$destination")" = "$owner" || exit 1
    if test ! -e "$manifest"; then
      echo 'unmanaged worker artifact; refusing replacement' >&2; exit 1
    fi
    cp -p "$destination" "$stage/before/$file"
    sha256sum "$destination" >> "$stage/current.manifest"
  elif test -e "$manifest"; then
    # A managed generation must contain every canonical destination.
    echo 'incomplete managed worker generation; refusing replacement' >&2; exit 1
  fi
  cp -p "$stage/$file" "$stage/after/$file"
  digest=$(sha256sum < "$stage/$file"); digest=${digest%% *}
  printf '%s  %s\n' "$digest" "$destination" >> "$stage/new.manifest"
done
if test -e "$manifest"; then cmp -s "$manifest" "$stage/current.manifest" || exit 1; fi
(cd "$stage" && {
  sha256sum identity old.manifest new.manifest after/*
  if test -e before/coop-worker; then sha256sum before/*; fi
} > evidence.sha256)
sync -f "$stage"
# Publishing this directory is the write-ahead commit: no live file changed yet.
mv "$stage" "$pending"
sync -f /var/lib/coop/worker
recover
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
