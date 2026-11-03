"""Local packaging checks. No VMs, GitHub calls or provider credentials."""
import importlib.util
import io
import json
import hashlib
import os
from pathlib import Path
import stat
import sys
import subprocess
import tempfile
import tarfile
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

    def test_v3_requires_pinned_master_before_guest_access(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary, config = root / "worker", root / "config.json"
            data = bytearray(64)
            data[:6], data[18:20] = b"\x7fELF\x02\x01", b"\xb7\x00"
            binary.write_bytes(data)
            for master in (None, {}, {"backend": "github"}, {"backend": "master", "generation": True}):
                config.write_text(json.dumps(dict(version=3, name="builder", master=master)))
                config.chmod(0o600)
                with mock.patch.object(self.module, "invoke") as call:
                    with self.assertRaises(ValueError):
                        self.module.install("builder", binary, config)
                    call.assert_not_called()

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

    def test_adoption_metadata_is_bound_to_v2_candidate_and_explicit_mode(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary, config = root / 'worker', root / 'config.json'
            data = bytearray(64)
            data[:6], data[18:20] = b'\x7fELF\x02\x01', b'\xb7\x00'
            binary.write_bytes(data)
            config.write_text(json.dumps(dict(version=2, name='robin', state_dir='/private/unused-state')))
            config.chmod(0o600)
            with mock.patch.object(self.module, 'running_child', return_value='test-child'), mock.patch.object(self.module, 'invoke') as call:
                self.module.install('robin', binary, config, adopt_matching_launchers=True)
                argv, payload = call.call_args.args
                self.assertEqual(argv[-1], 'adopt')
                with tarfile.open(fileobj=io.BytesIO(payload)) as archive:
                    self.assertEqual(archive.extractfile('adoption-state').read(), b'/private/unused-state')
            for changes in (dict(version=1), dict(state_dir='relative'), dict(state_dir='/a/../b'), dict(state_dir='/a\nother'), dict(state_dir='/')):
                candidate = dict(version=2, name='robin', state_dir='/private/unused-state')
                candidate.update(changes)
                config.write_text(json.dumps(candidate))
                with mock.patch.object(self.module, 'invoke') as call:
                    with self.assertRaises(ValueError):
                        self.module.install('robin', binary, config, adopt_matching_launchers=True)
                    call.assert_not_called()

    def test_adoption_state_cannot_overlap_installation_namespace(self):
        root = '/var/lib/coop/worker'
        reserved = ('config.json', 'install.sha256', 'install.lock', 'install-pending',
                    'install-before', 'install-history.saved', '.install.stage',
                    '.manifest.next', '.worker.next')
        rejected = [root, '/usr/local/libexec', '/usr/local/libexec/coop-worker',
                    '/usr/local/libexec/new-data/child']
        rejected += [root + '/' + name + suffix for name in reserved for suffix in ('', '/child')]
        allowed = [root + '/state', root + '/data/child', '/var/lib/coop/worker-data',
                   '/separate/unused-state', '/usr/local/libexec-data']
        with tempfile.TemporaryDirectory() as directory:
            binary, config = Path(directory) / 'worker', Path(directory) / 'config.json'
            data = bytearray(64)
            data[:6], data[18:20] = b'\x7fELF\x02\x01', b'\xb7\x00'
            binary.write_bytes(data)
            for path in rejected + allowed:
                with self.subTest(state_dir=path):
                    config.write_text(json.dumps(dict(version=2, name='robin', state_dir=path)))
                    config.chmod(0o600)
                    with mock.patch.object(self.module, 'running_child', return_value='test-child'), mock.patch.object(self.module, 'invoke') as call:
                        if path in rejected:
                            with self.assertRaises(ValueError):
                                self.module.install('robin', binary, config, adopt_matching_launchers=True)
                            call.assert_not_called()
                        else:
                            self.module.install('robin', binary, config, adopt_matching_launchers=True)
                            with tarfile.open(fileobj=io.BytesIO(call.call_args.args[1])) as archive:
                                self.assertEqual(archive.extractfile('adoption-state').read(), path.encode())


if __name__ == "__main__":
    unittest.main()
