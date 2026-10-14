#!/bin/sh
# Run through `flox activate -c scripts/uat-persistent-coops.sh` on a supported Mac.
# Only unique identities created here are eligible for cleanup. Never starts
# Apple's service. Retains private logs/archives beneath ignored bin/.
set -eu
cd "$(dirname "$0")/.."
exec /usr/bin/python3 - <<'PY'
import json
import os
import pathlib
import secrets
import sqlite3
import subprocess
import tempfile
import zipfile

repo = pathlib.Path.cwd()
binary = pathlib.Path(os.environ.get("COOP_UAT_BINARY", repo / "bin/coop-persistent")).resolve()
if not binary.is_file():
    raise SystemExit("Build bin/coop-persistent first with Flox.")
status = json.loads(subprocess.check_output(["container", "system", "status", "--format", "json"]))
if status.get("status") != "running":
    raise SystemExit("Apple Container is stopped; start it deliberately before UAT.")
root = pathlib.Path(tempfile.mkdtemp(prefix="persistent-uat-", dir=repo / "bin"))
root.chmod(0o700)
config = root / "config/coop"
config.mkdir(parents=True)
state = root / "state"
state.mkdir()
prefix = "uat-" + secrets.token_hex(4)
agent, copy = prefix + "-agent", prefix + "-copy"
password = root / "password"
password.write_text(secrets.token_hex(24))
password.chmod(0o600)
(config / "coop.toml").write_text(f'''
[credentials.password]
source = {{type="file", path="{password}"}}
expose = [{{type="environment", name="OPENCODE_SERVER_PASSWORD"}}]
[profiles.{agent}]
command = ["opencode", "serve", "--hostname", "127.0.0.1", "--port", "8788"]
include_credentials = ["password"]
restart = "on-failure"
[profiles.{agent}.resources]
cpus = 2
memory = "4G"
[profiles.{agent}.services.heartbeat]
command = ["bash", "-c", "while true; do date +%s > /var/lib/coop/heartbeat; sleep 2; done"]
[profiles.{copy}]
command = ["sleep", "infinity"]
[profiles.{copy}.resources]
cpus = 2
memory = "4G"
''')
env = dict(os.environ, XDG_CONFIG_HOME=str(root / "config"), XDG_STATE_HOME=str(state))
interactive = []

def coop(*args, cwd=repo, input=None, check=True):
    result = subprocess.run([str(binary), *args], cwd=cwd, env=env, input=input,
                            capture_output=True, text=True)
    with (root / "commands.log").open("a") as log:
        log.write("coop " + " ".join(args) + "\n" + result.stdout + result.stderr)
    if check and result.returncode:
        raise RuntimeError("Coop command failed; see " + str(root / "commands.log"))
    return result

def inspect_agent(name):
    rows = json.loads(subprocess.check_output(["container", "machine", "inspect", "coop-agent-" + name]))
    assert len(rows) == 1 and rows[0]["id"] == "coop-agent-" + name
    return rows[0]

def guest(name, *args):
    info = inspect_agent(name)
    assert info["homeMount"] == "none" and info["status"] == "running"
    return subprocess.check_output(["container", "exec", "--uid", "0", info["containerId"], *args], text=True).strip()

def terminal_pid():
    return guest(agent, "/usr/local/libexec/coop-env", "tmux", "-S", "/run/coop/tmux.sock",
                 "display-message", "-p", "-t", "agent", "#{pane_pid}")

def interactive_name(path):
    output = coop("status", cwd=path).stdout
    return next(line.split(":", 1)[1].strip() for line in output.splitlines() if line.startswith("container:"))

def interactive_guest(name, *args):
    return subprocess.check_output(["container", "exec", "--uid", "0", name, *args], text=True).strip()

def ensure_image(image_name, args, cwd=repo):
    images = subprocess.check_output(["container", "image", "list", "--quiet"], text=True).splitlines()
    if image_name not in images:
        coop(*args, cwd=cwd)

def assert_sparse_disk(path, archive):
    with zipfile.ZipFile(archive) as backup:
        manifest = json.loads(backup.read("manifest.json"))
    encoded = next(item["encoded_size"] for item in manifest["files"] if item["name"] == "rootfs")
    assert path.stat().st_blocks * 512 < encoded * 2 + (128 << 20), "restored sparse holes were allocated"

try:
    image_name = next(line.split(":", 1)[1].strip() for line in coop("--agent", agent, "status").stdout.splitlines() if line.startswith("image:"))
    ensure_image(image_name, ("--agent", agent, "rebuild"))
    coop("--agent", agent, "up")
    assert guest(agent, "id", "-u") == "0"
    guest(agent, "sh", "-c", "printf persistent > /root/uat-sentinel")
    database = root / "fixture.sqlite"
    with sqlite3.connect(database) as db:
        db.execute("create table fixture (value text)")
        db.execute("insert into fixture values (?)", ("persistent database",))
    subprocess.run(["container", "exec", "--uid", "0", "-i", inspect_agent(agent)["containerId"],
                    "sh", "-c", "cat > /root/uat.sqlite"], input=database.read_bytes(), check=True)
    pid = terminal_pid()
    coop("--agent", agent, "up")
    assert terminal_pid() == pid
    assert guest(agent, "systemctl", "is-active", "coop-helper@heartbeat") == "active"
    # Wait for server startup without displaying its secret.
    guest(agent, "/usr/local/libexec/coop-env", "bash", "-c", '''
for n in {1..60}; do
  code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8788/global/health || true)
  if [[ "$code" == 401 ]]; then
    test "$(curl -s -o /dev/null -w '%{http_code}' -u "opencode:$OPENCODE_SERVER_PASSWORD" http://127.0.0.1:8788/global/health)" = 200
    exit
  fi
  sleep 1
done
exit 1''')
    lease = guest(agent, "readlink", "/run/coop/current-credentials")
    held = root / "password-held"
    password.rename(held)
    try:
        assert coop("--agent", agent, "up", check=False).returncode != 0
        assert terminal_pid() == pid
        assert guest(agent, "readlink", "/run/coop/current-credentials") == lease
    finally:
        held.rename(password)
    coop("--agent", agent, "credentials", "refresh")
    assert guest(agent, "readlink", "/run/coop/current-credentials") != lease
    coop("--agent", agent, "down")
    coop("--agent", agent, "up")
    assert guest(agent, "cat", "/root/uat-sentinel") == "persistent"
    coop("--agent", agent, "down")
    coop("--agent", agent, "backup", str(root / "agent.zip"))
    coop("--agent", copy, "restore", str(root / "agent.zip"))
    agent_disk = pathlib.Path(status["paths"]["appRoot"]) / "plugin-state/machine-apiserver/machines" / ("coop-agent-" + copy) / "rootfs.ext4"
    assert_sparse_disk(agent_disk, root / "agent.zip")
    coop("--agent", copy, "up")
    assert_sparse_disk(agent_disk, root / "agent.zip")
    assert guest(copy, "cat", "/root/uat-sentinel") == "persistent"
    restored_database = root / "restored.sqlite"
    restored_database.write_bytes(subprocess.check_output(["container", "exec", "--uid", "0", inspect_agent(copy)["containerId"], "cat", "/root/uat.sqlite"]))
    with sqlite3.connect(restored_database) as db:
        assert db.execute("select value from fixture").fetchall() == [("persistent database",)]

    source, target = root / "project", root / "project-copy"
    project_config = '''[resources]
cpus=2
memory="4G"
[agents.claude]
state=""
[agents.codex]
state=""
[agents.opencode]
state=""
[[volume]]
path="deps"
'''
    for path in (source, target):
        path.mkdir()
        (path / "deps").mkdir()
        (path / ".coop.toml").write_text(project_config)
    (source / "host-sentinel").write_text("host-only")
    (source / "deps/host-dependency").write_text("mac-only")
    image_name = next(line.split(":", 1)[1].strip() for line in coop("status", cwd=source).stdout.splitlines() if line.startswith("desired image:"))
    ensure_image(image_name, ("rebuild",), cwd=source)
    for path in (source, target):
        interactive.append((path, interactive_name(path)))
    coop("up", cwd=source)
    name = interactive[0][1]
    assert interactive_guest(name, "cat", str(source / "host-sentinel")) == "host-only"
    interactive_guest(name, "sh", "-c", 'test ! -e "$1"; mkdir -p /root; echo persistent > /root/uat-sentinel; echo linux-only > "$2"',
                      "uat", str(source / "deps/host-dependency"), str(source / "deps/guest-dependency"))
    assert not (source / "deps/guest-dependency").exists()
    (source / ".coop.toml").write_text(project_config.replace("cpus=2", "cpus=3"))
    failed = coop("up", cwd=source, check=False)
    assert failed.returncode and "maintenance" in failed.stderr
    coop("down", cwd=source)
    coop("apply", cwd=source)
    coop("up", cwd=source)
    assert interactive_guest(name, "cat", "/root/uat-sentinel") == "persistent"
    coop("down", cwd=source)
    coop("backup", str(root / "interactive.zip"), cwd=source)
    coop("restore", str(root / "interactive.zip"), cwd=target)
    interactive_disk = pathlib.Path(status["paths"]["appRoot"]) / "containers" / interactive[1][1] / "rootfs.ext4"
    assert_sparse_disk(interactive_disk, root / "interactive.zip")
    coop("up", cwd=target)
    assert_sparse_disk(interactive_disk, root / "interactive.zip")
    assert interactive_guest(interactive[1][1], "cat", "/root/uat-sentinel") == "persistent"
    assert interactive_guest(interactive[1][1], "cat", str(target / "deps/guest-dependency")) == "linux-only"
    assert (source / "deps/host-dependency").read_text() == "mac-only"
    print("Agent and interactive persistence, credentials, server, and cold recovery passed.")
    print("GitHub organization/token acceptance was not exercised by this credential-free fixture.")
finally:
    # Eligibility comes from this invocation's private ownership records and
    # exact captured interactive identities. No prefix-wide cleanup or prune.
    for name in (agent, copy):
        record = state / "coop/workspaces/agent" / (name + ".json")
        if record.exists():
            owned = json.loads(record.read_text())
            if owned["identity"] == name and owned["runtime_name"] == "coop-agent-" + name:
                result = coop("--agent", name, "destroy", input="y\n", check=False)
                if result.returncode:
                    print("Cleanup incomplete for " + name)
    for path, name in interactive:
        if interactive_name(path) == name:
            result = coop("destroy", cwd=path, input="y\n", check=False)
            if result.returncode:
                print("Cleanup incomplete for " + name)
    print("Private UAT artifacts: " + str(root))
PY
