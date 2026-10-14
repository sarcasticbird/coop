"""Isolated crash probes: production guest script, temporary destination paths only."""
import hashlib
import importlib.util
import io
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tarfile
import tempfile
import unittest


class AuditWorkerInstall(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location(
            "audit_installer", Path(__file__).with_name("install_agent_worker.py"))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.state = self.root / "state"
        self.libexec = self.root / "libexec"
        self.bin = self.root / "bin"
        for path in (self.state, self.libexec, self.bin):
            path.mkdir(mode=0o700)
        self.names = ("coop-worker", "coop-opencode-server", "coop-opencode-attach",
                      "coop-opencode-launch", "config.json")
        self.script = module.GUEST_INSTALL.replace(
            "/var/lib/coop/worker", str(self.state)).replace(
            "/usr/local/libexec", str(self.libexec))
        self.executable("uname", "#!/bin/sh\nprintf 'aarch64\\n'\n")
        # A real rename precedes the injected failure. Kill the installer shell,
        # not the wrapper, so EXIT cannot run. No production script hooks added.
        mv = shutil.which("mv")
        self.assertIsNotNone(mv)
        self.executable("mv", f'''#!/bin/sh
"{mv}" "$@" || exit $?
if test "${{AUDIT_FAULT:-}}" != "" && test "$3" = "$AUDIT_DEST"; then
  if test "$AUDIT_FAULT" = kill; then kill -KILL "$PPID"; else exit 73; fi
fi
''')
        self.env = dict(os.environ, PATH=str(self.bin) + ":" + os.environ["PATH"])

    def executable(self, name, text):
        path = self.bin / name
        path.write_text(text)
        path.chmod(0o755)

    def destination(self, name):
        return (self.state if name == "config.json" else self.libexec) / name

    def package(self, version):
        files = {name: (f"#!/bin/sh\n# {version} {name}\nexit 0\n").encode()
                 for name in self.names}
        files["config.json"] = ('{"name":"robin","version":"' + version + '"}').encode()
        files["identity"] = b"robin"
        files["SHA256SUMS"] = "".join(
            hashlib.sha256(data).hexdigest() + "  " + name + "\n"
            for name, data in files.items()).encode()
        stream = io.BytesIO()
        with tarfile.open(fileobj=stream, mode="w") as archive:
            for name, data in files.items():
                entry = tarfile.TarInfo(name)
                entry.size, entry.mode = len(data), 0o600
                archive.addfile(entry, io.BytesIO(data))
        return stream.getvalue(), files

    def install(self, version, fault="", destination="coop-worker"):
        payload, _ = self.package(version)
        return subprocess.run(
            ["sh", "-c", self.script, "audit-install", hashlib.sha256(payload).hexdigest(), "robin"],
            input=payload, capture_output=True, timeout=20,
            env=dict(self.env, AUDIT_FAULT=fault, AUDIT_DEST=str(self.destination(destination))))

    def test_sigkill_mid_upgrade_preserves_mixed_set_and_blocks_retry(self):
        self.assertEqual(self.install("old").returncode, 0)
        manifest = (self.state / "install.sha256").read_bytes()
        result = self.install("new", "kill")
        self.assertEqual(result.returncode, -signal.SIGKILL, result.stderr)
        _, old = self.package("old")
        _, new = self.package("new")
        for name in self.names:
            self.assertEqual(self.destination(name).read_bytes(),
                             (new if name == "coop-worker" else old)[name])
        self.assertEqual((self.state / "install.sha256").read_bytes(), manifest)
        retry = self.install("new")
        self.assertNotEqual(retry.returncode, 0)
        self.assertIn(b"independent guest changes; refusing replacement", retry.stderr)
        self.assertTrue(list(self.state.glob(".install.*")))
        for name in self.names:
            self.assertEqual((self.state / "install-before" / name).read_bytes(), old[name])

    def test_sigkill_first_install_blocks_retry_as_unmanaged(self):
        self.assertEqual(self.install("new", "kill").returncode, -signal.SIGKILL)
        self.assertTrue(self.destination("coop-worker").exists())
        self.assertFalse((self.state / "install.sha256").exists())
        retry = self.install("new")
        self.assertNotEqual(retry.returncode, 0)
        self.assertIn(b"unmanaged worker artifact; refusing replacement", retry.stderr)

    def test_trappable_error_after_marked_rename_rolls_back(self):
        self.assertEqual(self.install("old").returncode, 0)
        # Fail a copy before the second rename; the first installed marker exists.
        cp = shutil.which("cp")
        self.assertIsNotNone(cp)
        self.executable("cp", f'''#!/bin/sh
case "$2" in *.install.*/coop-opencode-server) exit 74 ;; esac
exec "{cp}" "$@"
''')
        self.assertNotEqual(self.install("new").returncode, 0)
        _, old = self.package("old")
        for name in self.names:
            self.assertEqual(self.destination(name).read_bytes(), old[name])
        check = subprocess.run(["sha256sum", "-c", str(self.state / "install.sha256")],
                               capture_output=True, timeout=20)
        self.assertEqual(check.returncode, 0, check.stderr)

    def test_error_between_rename_and_marker_also_escapes_rollback(self):
        self.assertEqual(self.install("old").returncode, 0)
        self.assertNotEqual(self.install("new", "error").returncode, 0)
        _, new = self.package("new")
        self.assertEqual(self.destination("coop-worker").read_bytes(), new["coop-worker"])
        self.assertIn(b"independent guest changes; refusing replacement", self.install("new").stderr)


if __name__ == "__main__":
    unittest.main()
