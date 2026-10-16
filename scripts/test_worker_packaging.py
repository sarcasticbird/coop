"""Local packaging checks. No VMs, GitHub calls or provider credentials."""
import importlib.util
import json
import hashlib
import os
from pathlib import Path
import stat
import sys
import subprocess
import tempfile
import unittest
from unittest import mock

SCRIPTS = Path(__file__).resolve().parent


class Launchers(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.state = self.root / "opencode"
        self.log = self.root / "commands"
        self.env = dict(os.environ, PATH=str(self.bin) + ":" + os.environ["PATH"], TEST_LOG=str(self.log))
        self.executable("stat", f'#!{sys.executable}\nimport os, sys, stat\np=sys.argv[-1]\nprint(oct(stat.S_IMODE(os.stat(p).st_mode))[2:] if sys.argv[-2] == "%a" else "0")\n')
        self.executable("sleep", "#!/bin/sh\nexit 0\n")
        self.executable("curl", '#!/bin/sh\ncat > /dev/null\ntest "${TEST_READY:-yes}" = yes\n')
        self.executable("opencode", '#!/bin/sh\nprintf "%s\\n" "$*" >> "$TEST_LOG"\ntest "${#OPENCODE_SERVER_PASSWORD}" = 64\n')
        helper = self.executable("helper", '#!/bin/sh\nshift 2\nexec "$@"\n')
        self.launch = self.root / "launch"
        source = (SCRIPTS / "coop-opencode-launch").read_text()
        self.launch.write_text(source.replace("/var/lib/coop/opencode", str(self.state)).replace("/usr/local/libexec/coop-github", str(helper)))

    def executable(self, name, source):
        path = self.bin / name
        path.write_text(source)
        path.chmod(0o755)
        return path

    def run_launcher(self, mode):
        return subprocess.run(["bash", str(self.launch), mode], env=self.env, capture_output=True, text=True)

    def test_server_auth_and_attach_share_password_without_second_server(self):
        result = self.run_launcher("serve")
        self.assertEqual(result.returncode, 0, result.stderr)
        password = (self.state / "server-password").read_text()
        self.assertEqual(stat.S_IMODE(self.state.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE((self.state / "server-password").stat().st_mode), 0o600)
        result = self.run_launcher("attach")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.state / "server-password").read_text(), password)
        self.assertEqual(self.log.read_text().splitlines(), ["serve --hostname 127.0.0.1 --port 4096", "attach http://127.0.0.1:4096"])
        self.assertNotIn(password, result.stdout + result.stderr + self.log.read_text())

    def test_attach_never_starts_server_and_refuses_insecure_auth(self):
        self.assertNotEqual(self.run_launcher("attach").returncode, 0)
        self.assertFalse(self.log.exists())
        self.state.mkdir(exist_ok=True, mode=0o700)
        password = self.state / "server-password"
        password.write_text("a" * 64)
        password.chmod(0o644)
        self.assertNotEqual(self.run_launcher("serve").returncode, 0)
        password.unlink()
        password.symlink_to(self.root / "outside")
        self.assertNotEqual(self.run_launcher("serve").returncode, 0)

    def test_readiness_failure_is_bounded_and_does_not_start_server(self):
        self.assertEqual(self.run_launcher("serve").returncode, 0)
        self.log.unlink()
        self.env["TEST_READY"] = "no"
        result = self.run_launcher("attach")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not ready", result.stderr)
        self.assertFalse(self.log.exists())

    def test_attach_waits_for_server_to_create_authentication_file(self):
        self.env["TEST_PASSWORD_PATH"] = str(self.state / "server-password")
        self.executable("sleep", '#!/bin/sh\nif test ! -f "$TEST_PASSWORD_PATH"; then printf "%064d" 0 > "$TEST_PASSWORD_PATH"; chmod 0600 "$TEST_PASSWORD_PATH"; fi\n')
        result = self.run_launcher("attach")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.log.read_text().splitlines(), ["attach http://127.0.0.1:4096"])


class Installer(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location("installer", SCRIPTS / "install_agent_worker.py")
        self.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.module)

    def test_running_unshared_exact_identity_is_required(self):
        for change in ({"status": "stopped"}, {"homeMount": "rw"}, {"id": "coop-agent-hawk"}):
            row = dict(id="coop-agent-robin", status="running", homeMount="none", containerId="child")
            row.update(change)
            with mock.patch.object(self.module, "invoke", return_value=json.dumps([row]).encode()) as call:
                with self.assertRaises(ValueError):
                    self.module.running_child("container", "robin")
                self.assertEqual(call.call_args.args[0][:2], ["container", "machine"])

    def test_elf_and_private_config_validation_before_container_mutation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary, config = root / "worker", root / "config.json"
            binary.write_bytes(b"invalid executable")
            config.write_text('{"name":"robin"}')
            config.chmod(0o600)
            with mock.patch.object(self.module, "invoke") as call:
                with self.assertRaises(ValueError): self.module.install("robin", binary, config)
                call.assert_not_called()
            data = bytearray(64)
            data[:6], data[18:20] = b"\x7fELF\x02\x01", b"\xb7\x00"
            binary.write_bytes(data)
            config.chmod(0o644)
            with mock.patch.object(self.module, "invoke") as call:
                with self.assertRaises(ValueError): self.module.install("robin", binary, config)
                call.assert_not_called()


class FloxUpgrade(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.libexec = self.root / "libexec"
        self.libexec.mkdir()
        self.state = self.root / "state"
        self.candidate = self.root / "candidate"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.validation = self.root / "validation"
        self.validation.mkdir()
        core = self.root / "core"
        (core / ".flox/env").mkdir(parents=True)
        (core / ".flox/env.json").write_text('{"name":"coop-core","version":1}')
        self.env = dict(os.environ, PATH=str(self.bin) + ":" + os.environ["PATH"])
        for name, text in {
            "id": "#!/bin/sh\necho 0\n",
            "uname": "#!/bin/sh\necho aarch64\n",
            "stat": f'#!{sys.executable}\nimport os,sys,stat\nprint(oct(stat.S_IMODE(os.stat(sys.argv[-1]).st_mode))[2:])\n',
            "sha256sum": f'#!{sys.executable}\nimport hashlib,sys\np=sys.argv[-1]\nprint(hashlib.sha256(open(p,"rb").read()).hexdigest()+"  "+p)\n',
            "git": '#!/bin/bash\nif [[ "$1" == clone ]]; then mkdir -p "${@: -1}"; fi\nexit 0\n',
            "go": '#!/bin/bash\n[[ "$(basename "$PWD")" == to ]]\n',
            "opencode": "#!/bin/sh\nexit 0\n",
            "nix": '#!/bin/bash\nif [[ "$1" == --version ]]; then echo "nix (Nix) 2.31.5"; exit; fi\nwhile [[ "$1" != --profile ]]; do shift; done\nshift\nmkdir -p "$1/bin"\ncp "$TEST_FLOX" "$1/bin/flox"\n',
            "readlink": '#!/bin/sh\nshift\nprintf "%s\\n" "$1"\n',
        }.items():
            path = self.bin / name
            path.write_text(text)
            path.chmod(0o755)
        flox = self.bin / "flox-fixture"
        flox.write_text('#!/bin/bash\nif [[ "$1" == --version ]]; then echo "1.18.1-gf264cf2"; exit; fi\nwhile [[ "$1" != -- && "$1" != -c ]]; do shift; done\nmode=$1; shift\nif [[ "$mode" == -- ]]; then exec "$@"; fi\nexec bash -c "$1"\n')
        flox.chmod(0o755)
        self.env["TEST_FLOX"] = str(flox)
        self.bootstrap = self.libexec / "coop-env"
        self.original = f'#!/bin/bash\nexport PATH={self.bin}:{os.environ["PATH"]}\nexec "$@"\n'
        self.bootstrap.write_text(self.original)
        self.bootstrap.chmod(0o755)
        helper = self.libexec / "coop-github"
        helper.write_text('#!/bin/sh\nshift 2\nexec "$@"\n')
        helper.chmod(0o755)
        self.expected = hashlib.sha256(self.original.encode()).hexdigest()
        self.launch = self.root / "upgrade"
        source = (SCRIPTS / "upgrade-guest-flox").read_text()
        source = source.replace("/opt/coop-core", str(core)).replace("/opt/coop-flox-validation-core", str(self.root / "core-validation"))
        source = source.replace("/usr/local/libexec", str(self.libexec)).replace("/var/lib/coop/flox-upgrade", str(self.state)).replace("/opt/coop-flox-upgrade", str(self.candidate)).replace("/workspace/worker-validation", str(self.validation)).replace("export PATH=/nix/var/nix/profiles/default/bin:/usr/local/bin:/usr/bin:/bin", f'export PATH={self.env["PATH"]}')
        self.launch.write_text(source)

    def run_upgrade(self, mode):
        return subprocess.run(["bash", str(self.launch), mode, self.expected], env=self.env, capture_output=True, text=True)

    def test_stage_apply_resolution_and_verified_rollback(self):
        result = self.run_upgrade("stage")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.bootstrap.read_text(), self.original)
        result = self.run_upgrade("apply")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(f'PATH={self.candidate}/bin:', self.bootstrap.read_text())
        result = self.run_upgrade("rollback")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.bootstrap.read_text(), self.original)

    def test_unexpected_bootstrap_is_preserved(self):
        self.bootstrap.write_text(self.original + "# independent change\n")
        result = self.run_upgrade("stage")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unexpected guest bootstrap", result.stderr)
        self.assertTrue(self.bootstrap.read_text().endswith("# independent change\n"))


if __name__ == "__main__":
    unittest.main()
