"""Stage the worker and launchers into an existing, running named Coop."""
import argparse
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
install_mode=${3:-normal}
case "$install_mode" in normal|adopt) ;; *) exit 1 ;; esac
owner=$(id -u):$(id -g)
no_symlink_parents() {
  parent=$1
  while test "$parent" != /; do
    test ! -L "$parent" || exit 1
    parent=$(dirname "$parent")
  done
}
if test "$install_mode" = adopt; then
  test "$(id -u)" = 0 || exit 1
  no_symlink_parents /var/lib/coop/worker
  no_symlink_parents /usr/local/libexec
fi
test "$(uname -m)" = aarch64 || { echo 'guest architecture mismatch' >&2; exit 1; }
test ! -L /var/lib/coop/worker && test ! -L /usr/local/libexec || exit 1
mkdir -p /var/lib/coop/worker /usr/local/libexec
test "$(stat -c %a /var/lib/coop/worker)" = 700 || exit 1
test "$(stat -c %u:%g /var/lib/coop/worker)" = "$owner" || exit 1
# The kernel releases this lock even when the installer is killed.
test ! -L /var/lib/coop/worker/install.lock || exit 1
if test "$install_mode" = adopt; then
  if test -e /var/lib/coop/worker/install.lock; then
    test -f /var/lib/coop/worker/install.lock || exit 1
    test "$(stat -c %a /var/lib/coop/worker/install.lock)" = 600 || exit 1
    test "$(stat -c %u:%g /var/lib/coop/worker/install.lock)" = "$owner" || exit 1
    test "$(stat -c %h /var/lib/coop/worker/install.lock)" = 1 || exit 1
  fi
  exec 9>>/var/lib/coop/worker/install.lock
else
  exec 9>/var/lib/coop/worker/install.lock
fi
flock -x 9
stage=$(mktemp -d /var/lib/coop/worker/.install.XXXXXXXX)
retain_stage=no
trap 'if test "$retain_stage" = no; then rm -rf -- "$stage"; fi' EXIT
cat > "$stage/package.tar"
actual=$(sha256sum "$stage/package.tar"); actual=${actual%% *}
test "$actual" = "$expected" || { echo 'worker package checksum mismatch' >&2; exit 1; }
tar -xf "$stage/package.tar" -C "$stage"
(cd "$stage" && sha256sum -c SHA256SUMS >/dev/null)
chmod 0755 "$stage/coop-worker" "$stage/coop-opencode-server" "$stage/coop-opencode-attach" "$stage/coop-opencode-launch"
chmod 0600 "$stage/config.json"
"$stage/coop-worker" validate --config "$stage/config.json" >/dev/null
test "$(cat "$stage/identity")" = "$agent" || exit 1
check_compatibility() {
  if test "$install_mode" = adopt; then adoption_state_absent; fi
  if test -e /var/lib/coop/worker/config.json || test -L /var/lib/coop/worker/config.json; then
    "$stage/coop-worker" check-install --config "$stage/config.json" --previous-config /var/lib/coop/worker/config.json >/dev/null
  else
    "$stage/coop-worker" check-install --config "$stage/config.json" >/dev/null
  fi
}
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
absent() { test ! -e "$1" && test ! -L "$1"; }
adoption_state_absent() {
  no_symlink_parents "$candidate_state"
  absent "$candidate_state" || { echo 'adoption requires absent worker state' >&2; exit 1; }
}
if test "$install_mode" = adopt; then
  # Host metadata comes from the same hash-bound candidate validated above.
  test ! -L "$stage/adoption-state" && test -f "$stage/adoption-state" || exit 1
  candidate_state=$(cat "$stage/adoption-state")
  case "$candidate_state" in /*) ;; *) exit 1 ;; esac
  case "$candidate_state" in
    /usr/local/libexec|/usr/local/libexec/*|/var/lib/coop/worker)
      echo 'adoption state overlaps installer namespace' >&2; exit 1 ;;
    /var/lib/coop/worker/*)
      state_child=${candidate_state#/var/lib/coop/worker/}
      state_child=${state_child%%/*}
      case "$state_child" in
        config.json|install.sha256|install.lock|install-pending|install-before|install-history.*|.install.*|.manifest.*|.worker.*)
          echo 'adoption state overlaps installer namespace' >&2; exit 1 ;;
      esac ;;
  esac
  adoption_state_absent
  {
    printf 'adopt-matching-launchers-v1\nagent=%s\nstate=%s\nconfig-version=2\n' "$agent" "$candidate_state"
    printf 'absent=coop-worker config.json install.sha256\npresent=coop-opencode-server coop-opencode-attach coop-opencode-launch\n'
    for file in $files; do
      digest=$(sha256sum < "$stage/$file"); digest=${digest%% *}
      printf '%s  %s\n' "$digest" "$file"
    done
  } > "$stage/adoption"
fi
check_compatibility || { echo 'worker/state compatibility refused; keep services stopped and inspect' >&2; exit 1; }
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
  adopting=no
  if test -e "$pending/adoption" || test -L "$pending/adoption"; then
    adopting=yes
    test "$install_mode" = adopt || { echo 'pending adoption requires explicit flag' >&2; exit 1; }
    test ! -L "$pending/adoption" && test -f "$pending/adoption" || exit 1
    test "$(stat -c %a "$pending/adoption")" = 600 || exit 1
    test "$(stat -c %u:%g "$pending/adoption")" = "$owner" || exit 1
    test "$(stat -c %h "$pending/adoption")" = 1 || exit 1
    cmp -s "$pending/adoption" "$stage/adoption" || { echo 'adoption generation mismatch' >&2; exit 1; }
    adoption_state_absent
    test ! -s "$pending/old.manifest" || exit 1
    absent "$pending/before/coop-worker" && absent "$pending/before/config.json" || exit 1
    for file in coop-opencode-server coop-opencode-attach coop-opencode-launch; do
      test -f "$pending/before/$file" || exit 1
    done
  elif test "$install_mode" = adopt; then
    echo 'pending managed transaction cannot be adopted' >&2; exit 1
  fi
  # Check all recovery evidence and live files before changing any of them.
  (cd "$pending" && sha256sum -c evidence.sha256 >/dev/null) || exit 1
  if test "$adopting" = yes; then
    # Rebuild the exact fixed inventory so removing an evidence row cannot hide
    # a changed saved launcher.
    write_evidence "$pending" | cmp -s - "$pending/evidence.sha256" || exit 1
  fi
  if test -e "$manifest" || test -L "$manifest"; then
    mode=600
    if test "$adopting" = yes; then
      # The old manifest was absent, so an empty live file is independent data.
      test "$(stat -c %h "$manifest")" = 1 || exit 1
      matches "$manifest" "$pending/new.manifest" || exit 1
    else
      matches "$manifest" "$pending/old.manifest" || matches "$manifest" "$pending/new.manifest" || exit 1
    fi
  else
    test ! -s "$pending/old.manifest" || exit 1
  fi
  for file in $files; do
    destination_for
    test ! -L "$pending/after/$file" && test -f "$pending/after/$file" || exit 1
    test "$(stat -c %a "$pending/after/$file")" = "$mode" || exit 1
    test "$(stat -c %u:%g "$pending/after/$file")" = "$owner" || exit 1
    if test "$adopting" = yes; then
      test "$(stat -c %h "$pending/after/$file")" = 1 || exit 1
      cmp -s "$pending/after/$file" "$stage/$file" || exit 1
      if test -e "$destination"; then test "$(stat -c %h "$destination")" = 1 || exit 1; fi
    fi
    if test -e "$pending/before/$file"; then
      test ! -L "$pending/before/$file" && test "$(stat -c %a "$pending/before/$file")" = "$mode" || exit 1
      test "$(stat -c %u:%g "$pending/before/$file")" = "$owner" || exit 1
      if test "$adopting" = yes; then
        test "$(stat -c %h "$pending/before/$file")" = 1 || exit 1
        cmp -s "$pending/before/$file" "$stage/$file" || exit 1
      fi
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
write_evidence() {
  (cd "$1" && {
    sha256sum identity old.manifest new.manifest
    if test -f adoption; then sha256sum adoption; fi
    for file in $files; do sha256sum "after/$file"; done
    for file in $files; do if test -f "before/$file"; then sha256sum "before/$file"; fi; done
  })
}
had_pending=no
if test -e "$pending" || test -L "$pending"; then had_pending=yes; fi
if test -e "$pending" || test -L "$pending"; then recover; fi
if test "$had_pending" = yes && test "$install_mode" = adopt; then exit 0; fi
check_compatibility || exit 1
if test "$install_mode" = adopt; then
  absent "$manifest" || { echo 'already managed; adoption refused' >&2; exit 1; }
  absent /usr/local/libexec/coop-worker && absent /var/lib/coop/worker/config.json || exit 1
  # Include dot entries; only the lock and this invocation's stage are allowed.
  for entry in /var/lib/coop/worker/* /var/lib/coop/worker/.[!.]* /var/lib/coop/worker/..?*; do
    if absent "$entry"; then continue; fi
    case "$entry" in /var/lib/coop/worker/install.lock|"$stage") ;; *) echo 'prior or unknown installation evidence; adoption refused' >&2; exit 1 ;; esac
  done
  for file in coop-opencode-server coop-opencode-attach coop-opencode-launch; do
    destination_for
    matches "$destination" "$stage/$file" || exit 1
    test "$(stat -c %h "$destination")" = 1 || exit 1
  done
  # Once before-files are captured, preserve an unpublished interrupted stage.
  retain_stage=yes
fi
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
    if test ! -e "$manifest" && test "$install_mode" != adopt; then
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
if test "$install_mode" = adopt; then
  adoption_state_absent
  no_symlink_parents /var/lib/coop/worker
  no_symlink_parents /usr/local/libexec
  absent "$manifest" && absent /usr/local/libexec/coop-worker && absent /var/lib/coop/worker/config.json || exit 1
  for file in coop-opencode-server coop-opencode-attach coop-opencode-launch; do
    destination_for
    matches "$destination" "$stage/before/$file" || exit 1
    test "$(stat -c %h "$destination")" = 1 || exit 1
  done
fi
write_evidence "$stage" > "$stage/evidence.sha256"
sync -f "$stage"
# Publishing this directory is the write-ahead commit: no live file changed yet.
mv "$stage" "$pending"
sync -f /var/lib/coop/worker
if test "$install_mode" = adopt; then stage=$pending; fi
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


def adoption_state_path(state_dir):
    if (not isinstance(state_dir, str) or not state_dir.startswith("/")
            or state_dir == "/" or str(Path(state_dir)) != state_dir
            or ".." in Path(state_dir).parts
            or any(ord(char) < 32 or ord(char) == 127 for char in state_dir)):
        raise ValueError("adoption requires a canonical absolute state path")
    path = Path(state_dir)
    executable_root = Path("/usr/local/libexec")
    install_root = Path("/var/lib/coop/worker")
    if path == executable_root or executable_root in path.parents or path == install_root:
        raise ValueError("adoption state overlaps installer namespace")
    if install_root in path.parents:
        child = path.relative_to(install_root).parts[0]
        if (child in {"config.json", "install.sha256", "install.lock", "install-pending", "install-before"}
                or child.startswith(("install-history.", ".install.", ".manifest.", ".worker."))):
            raise ValueError("adoption state overlaps installer namespace")
    return state_dir


def install(agent, binary, config, adopt_matching_launchers=False):
    if not re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", agent) or "--" in agent:
        raise ValueError("invalid agent identity")
    executable = regular(binary)
    if len(executable) < 64 or executable[:6] != b"\x7fELF\x02\x01" or executable[18:20] != b"\xb7\x00":
        raise ValueError("worker must be Linux arm64 ELF")
    private = regular(config, private=True)
    candidate = json.loads(private)
    if len(private) > 1 << 20 or candidate.get("name") != agent:
        raise ValueError("worker config identity mismatch")
    if candidate.get("version") == 3:
        master = candidate.get("master")
        if (not isinstance(master, dict) or master.get("backend") != "master"
                or not isinstance(master.get("worker_instance_id"), str)
                or not re.fullmatch(r"[a-f0-9]{64}", master["worker_instance_id"])
                or type(master.get("generation")) is not int or master["generation"] < 1
                or any(not isinstance(master.get(key), str) or not master[key]
                       for key in ("master_id", "agent_id", "host_id", "engagement", "client_config"))
                or not Path(master["client_config"]).is_absolute()):
            raise ValueError("v3 worker requires explicit pinned master placement")
    files = {"coop-worker": executable, "config.json": private, "identity": agent.encode()}
    if adopt_matching_launchers:
        if type(candidate.get("version")) is not int or candidate["version"] != 2:
            raise ValueError("adoption requires a v2 candidate")
        state_dir = adoption_state_path(candidate.get("state_dir"))
        files["adoption-state"] = state_dir.encode()
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
    invoke([runtime, "exec", "--user", "root", "-i", child, "sh", "-c", GUEST_INSTALL, "coop-install-worker", hashlib.sha256(data).hexdigest(), agent, "adopt" if adopt_matching_launchers else "normal"], data)
    LOG.info("worker artifacts staged", extra={"agent": agent, "activated": False})


if __name__ == "__main__":
    logging.basicConfig(format='{"level":"%(levelname)s","message":"%(message)s"}', level=logging.INFO)
    try:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--adopt-matching-launchers", action="store_true")
        parser.add_argument("agent")
        parser.add_argument("binary", type=Path)
        parser.add_argument("config", type=Path)
        args = parser.parse_args()
        install(args.agent, args.binary, args.config, adopt_matching_launchers=args.adopt_matching_launchers)
    except (ValueError, OSError, json.JSONDecodeError):
        LOG.error("worker staging failed; verify identity, artifacts, private config and guest ownership")
        sys.exit(1)
