"""Run the production guest transaction against isolated, harmless fixtures."""
import hashlib
import io
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

from install_agent_worker import GUEST_INSTALL


FILES = ('coop-worker', 'coop-opencode-server', 'coop-opencode-attach',
         'coop-opencode-launch', 'config.json')


class Recovery(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.state = self.root / 'state'
        self.libexec = self.root / 'libexec'
        self.script = GUEST_INSTALL.replace('/var/lib/coop/worker', str(self.state))
        self.script = self.script.replace('/usr/local/libexec', str(self.libexec))
        self.script = self.script.replace('test "$(uname -m)" = aarch64', 'test fixture = fixture')

    def package(self, version):
        files = {name: (f'#!/bin/sh\n# {version}\nexit 0\n').encode() for name in FILES}
        files['identity'] = b'robin'
        files['SHA256SUMS'] = ''.join(
            hashlib.sha256(data).hexdigest() + '  ' + name + '\n'
            for name, data in files.items()).encode()
        payload = io.BytesIO()
        with tarfile.open(fileobj=payload, mode='w') as archive:
            for name, data in files.items():
                entry = tarfile.TarInfo(name)
                entry.size = len(data)
                entry.mode = 0o600
                archive.addfile(entry, io.BytesIO(data))
        return payload.getvalue()

    def run_install(self, version='new', script=None):
        payload = self.package(version)
        return subprocess.run(['sh', '-c', script or self.script, 'test-installer',
                               hashlib.sha256(payload).hexdigest(), 'robin'],
                              input=payload, capture_output=True, timeout=20)

    def assert_generation(self, version):
        for name in FILES:
            path = (self.state if name == 'config.json' else self.libexec) / name
            self.assertIn(f'# {version}\n', path.read_text())
            self.assertEqual(path.stat().st_mode & 0o777, 0o600 if name == 'config.json' else 0o755)
        result = subprocess.run(['sha256sum', '-c', str(self.state / 'install.sha256')],
                                capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.state / 'install-pending').exists())

    def fault(self, boundary, count, action):
        # Count repeated file renames without depending on timing or shell traps.
        return self.script.replace('set -eu', 'set -eu\nfault_count=0', 1).replace(
            boundary, boundary + '\nfault_count=$((fault_count + 1))\n'
            f'if test "$fault_count" = {count}; then {action}; fi')

    def test_interruptions_at_every_publication_boundary(self):
        boundaries = [('sync -f "$stage"', 1),
                      ('mv "$stage" "$pending"', 1),
                      ('mv -f "$next" "$destination"', 5),
                      ('mv -f "$next" "$manifest"', 1),
                      ('mv "$pending" "$history/generation"', 1)]
        for upgrade in (False, True):
            for action in ('kill -KILL $$', 'exit 73'):
                for boundary, total in boundaries:
                    for count in range(1, total + 1):
                        with self.subTest(upgrade=upgrade, action=action, boundary=boundary, count=count):
                            # Each case owns a fresh guest filesystem.
                            with tempfile.TemporaryDirectory() as directory:
                                original = self.script
                                self.script = original.replace(str(self.root), directory)
                                try:
                                    if upgrade:
                                        self.assertEqual(self.run_install('old').returncode, 0)
                                    failed = self.run_install(script=self.fault(boundary, count, action))
                                    self.assertNotEqual(failed.returncode, 0)
                                    result = self.run_install()
                                    self.assertEqual(result.returncode, 0, result.stderr)
                                    old_state, old_libexec = self.state, self.libexec
                                    self.state, self.libexec = Path(directory) / 'state', Path(directory) / 'libexec'
                                    try:
                                        self.assert_generation('new')
                                        if upgrade:
                                            copies = list(self.state.glob('install-history.*/generation/before/coop-worker'))
                                            self.assertTrue(any(b'# old\n' in p.read_bytes() for p in copies))
                                    finally:
                                        self.state, self.libexec = old_state, old_libexec
                                finally:
                                    self.script = original

    def test_recovery_failure_is_retryable_and_does_not_consume_evidence(self):
        self.assertEqual(self.run_install('old').returncode, 0)
        self.assertNotEqual(self.run_install(script=self.fault('mv -f "$next" "$destination"', 1, 'exit 73')).returncode, 0)
        pending = self.state / 'install-pending'
        before = (pending / 'before/coop-worker').read_bytes()
        self.assertNotEqual(self.run_install(script=self.fault('mv -f "$next" "$destination"', 2, 'exit 74')).returncode, 0)
        self.assertEqual((pending / 'before/coop-worker').read_bytes(), before)
        result = self.run_install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_generation('new')

    def test_failed_copy_rename_and_sync_can_be_retried(self):
        commands = ['cp -p "$pending/after/$file" "$next"',
                    'sync -f "$next"',
                    'mv -f "$next" "$destination"',
                    'sync -f "$(dirname "$destination")"',
                    'cp -p "$pending/new.manifest" "$next"',
                    'mv -f "$next" "$manifest"',
                    'mv "$pending" "$history/generation"']
        for command in commands:
            with self.subTest(command=command):
                with tempfile.TemporaryDirectory() as directory:
                    script = self.script.replace(str(self.root), directory)
                    failed = self.run_install(script=script.replace(command, 'false', 1))
                    self.assertNotEqual(failed.returncode, 0)
                    self.assertTrue((Path(directory) / 'state/install-pending').is_dir())
                    result = self.run_install(script=script)
                    self.assertEqual(result.returncode, 0, result.stderr)

    def test_retry_recovers_saved_generation_before_different_package(self):
        self.assertEqual(self.run_install('old').returncode, 0)
        self.assertNotEqual(self.run_install(script=self.fault('mv -f "$next" "$destination"', 2, 'kill -KILL $$')).returncode, 0)
        script = self.script.replace('if test -e "$pending" || test -L "$pending"; then recover; fi',
                                     'if test -e "$pending" || test -L "$pending"; then recover; fi\nexit 73')
        self.assertNotEqual(self.run_install('later', script=script).returncode, 0)
        self.assert_generation('new')
        self.assertEqual(self.run_install('later').returncode, 0)
        self.assert_generation('later')

    def test_data_is_synced_before_publishing_renames(self):
        # SIGKILL tests do not discard the kernel cache. Guard power-loss ordering
        # separately; actual storage durability still needs native VM validation.
        self.assertIn('sync -f "$next"\n    mv -f "$next" "$destination"', self.script)
        self.assertIn('sync -f "$next"\n  mv -f "$next" "$manifest"', self.script)
        self.assertIn('sync -f "$stage"\n# Publishing', self.script)

    @unittest.skipUnless(os.geteuid() == 0, 'changing ownership requires root')
    def test_independent_ownership_change_is_rejected(self):
        self.assertEqual(self.run_install('old').returncode, 0)
        live = self.libexec / 'coop-worker'
        os.chown(live, 65534, 65534)
        self.assertNotEqual(self.run_install().returncode, 0)
        self.assertEqual(live.stat().st_uid, 65534)

    def test_incomplete_manifest_is_not_accepted_as_management_evidence(self):
        self.assertEqual(self.run_install('old').returncode, 0)
        manifest = self.state / 'install.sha256'
        manifest.write_text(''.join(manifest.read_text().splitlines(keepends=True)[1:]))
        self.assertNotEqual(self.run_install().returncode, 0)
        self.assertIn('# old\n', (self.libexec / 'coop-worker').read_text())

    def test_independent_edits_and_damaged_evidence_are_preserved(self):
        for target in ('live', 'manifest', 'evidence', 'mode', 'symlink'):
            with self.subTest(target=target):
                # Restore the fixture via its own new temporary filesystem.
                with tempfile.TemporaryDirectory() as directory:
                    script = self.script.replace(str(self.root), directory)
                    result = self.run_install(script=script.replace('mv -f "$next" "$destination"',
                                                                  'mv -f "$next" "$destination"\nexit 73'))
                    self.assertNotEqual(result.returncode, 0)
                    state = Path(directory) / 'state'
                    live = Path(directory) / 'libexec/coop-worker'
                    path = live
                    if target == 'manifest':
                        path = state / 'install.sha256'
                    elif target == 'evidence':
                        path = state / 'install-pending/after/coop-worker'
                    if target == 'mode':
                        path.chmod(0o700)
                    elif target == 'symlink':
                        path.unlink()
                        path.symlink_to(state / 'install-pending/after/coop-worker')
                    else:
                        path.write_text('independent edit\n')
                    result = self.run_install(script=script)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertTrue((state / 'install-pending').is_dir())


if __name__ == '__main__':
    unittest.main()
