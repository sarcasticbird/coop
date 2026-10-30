"""Run the production guest transaction against isolated, harmless fixtures."""
import hashlib
import io
from contextlib import contextmanager
import os
from pathlib import Path
import shlex
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
        self.worker_state = self.root / 'worker-state'
        self.check_install_contract = False
        self.script = GUEST_INSTALL.replace('/var/lib/coop/worker', str(self.state))
        self.script = self.script.replace('/usr/local/libexec', str(self.libexec))
        self.script = self.script.replace('test "$(uname -m)" = aarch64', 'test fixture = fixture')
        self.production_root_script = self.script
        # Fixtures belong to the invoking test user. Keep the production
        # current-UID/GID checks, while allowing these isolated paths as nonroot.
        self.script = self.script.replace('test "$(id -u)" = 0 || exit 1', 'test fixture = fixture')

    def package(self, version, adopt=False, config_bytes=None):
        files = {name: (f'#!/bin/sh\n# {version}\nexit 0\n').encode() for name in FILES}
        if config_bytes is not None:
            files['config.json'] = config_bytes
        if self.check_install_contract:
            # CheckInstall calls FileStore.Lock when state exists. Reproduce
            # its O_CREAT side effect so refusal tests cannot hide that write.
            files['coop-worker'] = (f'#!/bin/sh\n# {version}\n'
                                    f'state={shlex.quote(str(self.worker_state))}\n'
                                    'if test "$1" = check-install && test -d "$state"; then\n'
                                    '  umask 077\n  : >> "$state/worker.lock"\nfi\nexit 0\n').encode()
        files['identity'] = b'robin'
        if adopt:
            files['adoption-state'] = str(self.worker_state).encode()
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

    def run_install(self, version='new', script=None, adopt=False, config_bytes=None, drift=None):
        payload = self.package(version, adopt=adopt, config_bytes=config_bytes)
        return subprocess.run(['sh', '-c', script or self.script, 'test-installer',
                                hashlib.sha256(payload).hexdigest(), 'robin',
                                'adopt' if adopt else 'normal', *(drift or ())],
                               input=payload, capture_output=True, timeout=20)

    def launchers_only(self):
        self.libexec.mkdir(mode=0o700)
        for name in FILES[1:4]:
            path = self.libexec / name
            path.write_bytes(b'#!/bin/sh\n# new\nexit 0\n')
            path.chmod(0o755)
        self.native_history = self.root / 'native-history'
        self.native_history.write_bytes(b'preserve native sessions and provider login')

    @contextmanager
    def fresh_fixture(self):
        original = self.root, self.state, self.libexec, self.script, self.worker_state, self.check_install_contract
        with tempfile.TemporaryDirectory() as directory:
            self.root = Path(directory)
            self.state, self.libexec = self.root / 'state', self.root / 'libexec'
            self.worker_state = self.root / 'worker-state'
            self.check_install_contract = False
            self.script = original[3].replace(str(original[0]), directory)
            try:
                yield
            finally:
                (self.root, self.state, self.libexec, self.script,
                 self.worker_state, self.check_install_contract) = original

    def saved_bytes(self, directory):
        return {str(path.relative_to(directory)): (path.read_bytes(), path.stat().st_mode,
                                                    path.stat().st_uid, path.stat().st_gid)
                for path in directory.rglob('*') if path.is_file() and not path.is_symlink()}

    def test_production_adoption_still_refuses_nonroot_before_filesystem_access(self):
        guard = 'test "$(id -u)" = 0 || exit 1'
        self.assertIn(guard, GUEST_INSTALL)
        script = self.production_root_script.replace(guard, guard + '\necho root-guard-bypassed >&2')
        payload = self.package('new', adopt=True)
        identity = {'user': 65534, 'group': 65534} if os.geteuid() == 0 else {}
        result = subprocess.run(['sh', '-c', script, 'test-installer',
                                 hashlib.sha256(payload).hexdigest(), 'robin', 'adopt'],
                                input=payload, capture_output=True, timeout=20, **identity)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(b'root-guard-bypassed', result.stderr)
        self.assertFalse(self.state.exists())
        self.assertFalse(self.libexec.exists())

    def test_matching_launchers_require_explicit_adoption_and_preserve_before(self):
        self.launchers_only()
        before = {name: (self.libexec / name).read_bytes() for name in FILES[1:4]}
        self.assertNotEqual(self.run_install().returncode, 0)
        self.assertEqual(before, {name: (self.libexec / name).read_bytes() for name in before})
        result = self.run_install(adopt=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_generation('new')
        generations = list(self.state.glob('install-history.*/generation'))
        self.assertEqual(len(generations), 1)
        saved = generations[0]
        self.assertEqual((saved / 'old.manifest').read_bytes(), b'')
        self.assertTrue((saved / 'adoption').is_file())
        for name, content in before.items():
            self.assertEqual((saved / 'before' / name).read_bytes(), content)
            self.assertIn('before/' + name, (saved / 'evidence.sha256').read_text())
        self.assertEqual(self.native_history.read_bytes(), b'preserve native sessions and provider login')
        self.assertNotEqual(self.run_install(adopt=True).returncode, 0)

    def test_adoption_refuses_existing_state_before_check_install_can_create_lock(self):
        for pending in (False, True):
            for existing_data in (False, True):
                with self.subTest(pending=pending, existing_data=existing_data), self.fresh_fixture():
                    self.launchers_only()
                    self.check_install_contract = True
                    if pending:
                        result = self.run_install(adopt=True, script=self.fault(
                            'mv -f "$next" "$destination"', 1, 'exit 73'))
                        self.assertNotEqual(result.returncode, 0)
                        self.assertTrue((self.state / 'install-pending').exists())
                    self.worker_state.mkdir(mode=0o700)
                    if existing_data:
                        (self.worker_state / 'state.json').write_bytes(b'unchanged existing evidence')
                    before = self.saved_bytes(self.worker_state)
                    pending_before = self.saved_bytes(self.state / 'install-pending') if pending else None
                    result = self.run_install(adopt=True)
                    self.assertNotEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(before, self.saved_bytes(self.worker_state))
                    self.assertFalse((self.worker_state / 'worker.lock').exists())
                    if pending:
                        self.assertEqual(pending_before, self.saved_bytes(self.state / 'install-pending'))

    def test_check_install_contract_fixture_exercises_lock_creation_in_normal_mode(self):
        self.launchers_only()
        self.check_install_contract = True
        self.worker_state.mkdir(mode=0o700)
        self.assertNotEqual(self.run_install().returncode, 0)
        self.assertTrue((self.worker_state / 'worker.lock').is_file())

    def test_adoption_state_collision_refuses_before_publication_or_pending_mutation(self):
        reserved = ('config.json', 'install.sha256', 'install.lock', 'install-pending',
                    'install-before', 'install-history.saved', '.install.stage',
                    '.manifest.next', '.worker.next')
        locations = [('state', name + suffix) for name in reserved for suffix in ('', '/child')]
        locations += [('libexec', 'coop-worker'), ('libexec', 'future-data/child')]
        for pending in (False, True):
            for parent, child in locations:
                with self.subTest(pending=pending, parent=parent, child=child), self.fresh_fixture():
                    self.launchers_only()
                    if pending:
                        result = self.run_install(adopt=True, script=self.fault(
                            'mv -f "$next" "$destination"', 1, 'exit 73'))
                        self.assertNotEqual(result.returncode, 0)
                    before = self.saved_bytes(self.root)
                    self.worker_state = (self.state if parent == 'state' else self.libexec) / child
                    result = self.run_install(adopt=True)
                    self.assertNotEqual(result.returncode, 0, result.stderr)
                    self.assertIn(b'overlaps installer namespace', result.stderr)
                    after = self.saved_bytes(self.root)
                    if 'state/install.lock' not in before:
                        after.pop('state/install.lock', None)
                    self.assertEqual(before, after)
                    self.assertEqual((self.state / 'install-pending').exists(), pending)
                    self.assertFalse((self.state / 'install.sha256').exists())

    def test_adoption_allows_absent_worker_state_child_and_external_data_path(self):
        for worker_child in (False, True):
            with self.subTest(worker_child=worker_child), self.fresh_fixture():
                self.launchers_only()
                if worker_child:
                    self.worker_state = self.state / 'state'
                self.check_install_contract = True
                result = self.run_install(adopt=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assert_generation('new')
                self.assertFalse(self.worker_state.exists())

    def test_adoption_refuses_noncanonical_starting_layouts_unchanged(self):
        cases = ('missing', 'bytes', 'mode', 'symlink', 'hardlink', 'parent-symlink',
                 'ancestor-symlink', 'worker', 'config', 'dangling-worker',
                 'manifest', 'empty-manifest', 'dangling-manifest', 'state', 'wake',
                 'state-symlink', 'state-parent-symlink', 'history', 'install-before',
                 'unknown', 'unknown-dotfile', 'retained-stage', 'lock-hardlink', 'lock-mode')
        if os.geteuid() == 0:
            cases += ('owner',)
        for case in cases:
            with self.subTest(case=case), self.fresh_fixture():
                self.launchers_only()
                self.state.mkdir(mode=0o700)
                launcher = self.libexec / FILES[1]
                candidate_state = self.root / 'worker-state'
                if case == 'missing': launcher.unlink()
                elif case == 'bytes': launcher.write_bytes(b'changed launcher')
                elif case == 'mode': launcher.chmod(0o700)
                elif case == 'owner': os.chown(launcher, 65534, 65534)
                elif case == 'symlink':
                    launcher.unlink()
                    launcher.symlink_to(self.native_history)
                elif case == 'hardlink': os.link(launcher, self.root / 'shared-launcher')
                elif case == 'parent-symlink':
                    self.libexec.rename(self.root / 'actual-libexec')
                    self.libexec.symlink_to(self.root / 'actual-libexec', target_is_directory=True)
                elif case == 'ancestor-symlink':
                    alias = self.root / 'alias'
                    alias.symlink_to(self.root, target_is_directory=True)
                    self.script = self.script.replace(str(self.libexec), str(alias / 'libexec'))
                elif case in ('worker', 'dangling-worker'):
                    worker = self.libexec / 'coop-worker'
                    if case == 'worker': worker.write_bytes(b'existing worker')
                    else: worker.symlink_to(self.root / 'missing')
                elif case == 'config': (self.state / 'config.json').write_bytes(b'existing config')
                elif case in ('manifest', 'empty-manifest', 'dangling-manifest'):
                    manifest = self.state / 'install.sha256'
                    if case == 'dangling-manifest': manifest.symlink_to(self.root / 'missing')
                    else: manifest.write_bytes(b'' if case == 'empty-manifest' else b'prior manifest')
                elif case in ('state', 'wake'):
                    candidate_state.mkdir()
                    if case == 'wake': (candidate_state / 'wake').mkdir()
                elif case == 'state-symlink': candidate_state.symlink_to(self.root / 'missing')
                elif case == 'state-parent-symlink':
                    parent = self.root / 'other-parent'
                    parent.mkdir()
                    candidate_state.symlink_to(parent, target_is_directory=True)
                    self.worker_state = candidate_state / 'absent-child'
                elif case == 'history': (self.state / 'install-history.old').mkdir()
                elif case == 'install-before': (self.state / 'install-before').mkdir()
                elif case == 'unknown': (self.state / 'unknown').write_bytes(b'inspect me')
                elif case == 'unknown-dotfile': (self.state / '.unknown').write_bytes(b'inspect me')
                elif case == 'retained-stage': (self.state / '.install.interrupted').mkdir()
                elif case in ('lock-hardlink', 'lock-mode'):
                    lock = self.state / 'install.lock'
                    lock.write_bytes(b'preserve this existing lock')
                    lock.chmod(0o600 if case == 'lock-hardlink' else 0o644)
                    if case == 'lock-hardlink': os.link(lock, self.root / 'shared-lock')
                before = self.saved_bytes(self.root)
                result = self.run_install(adopt=True)
                self.assertNotEqual(result.returncode, 0, result.stderr)
                after = self.saved_bytes(self.root)
                if 'state/install.lock' not in before:
                    after.pop('state/install.lock', None)
                self.assertEqual(before, after)
                self.assertFalse((self.state / 'install-pending').exists())

    def test_adoption_interruptions_preserve_before_and_recover_same_generation(self):
        boundaries = [('mv "$stage" "$pending"', 1),
                      ('mv -f "$next" "$destination"', 5),
                      ('mv -f "$next" "$manifest"', 1),
                      ('history=$(mktemp -d', 1),
                      ('mv "$pending" "$history/generation"', 1)]
        for action in ('kill -KILL $$', 'exit 73'):
            for boundary, total in boundaries:
                for count in range(1, total + 1):
                    with self.subTest(action=action, boundary=boundary, count=count), self.fresh_fixture():
                        self.launchers_only()
                        if boundary == 'history=$(mktemp -d':
                            # Insert before archival, after mktemp has completed.
                            boundary = 'history=$(mktemp -d ' + str(self.state) + '/install-history.XXXXXXXX)'
                        failed = self.run_install(adopt=True, script=self.fault(boundary, count, action))
                        self.assertNotEqual(failed.returncode, 0)
                        if (self.state / 'install-pending').exists():
                            result = self.run_install(adopt=True)
                            self.assertEqual(result.returncode, 0, result.stderr)
                        else:
                            self.assertNotEqual(self.run_install(adopt=True).returncode, 0)
                            self.assertEqual(self.run_install().returncode, 0)
                        self.assert_generation('new')
                        saved = list(self.state.glob('install-history.*/generation/adoption'))
                        self.assertEqual(len(saved), 1)
                        for name in FILES[1:4]:
                            before = saved[0].parent / 'before' / name
                            self.assertEqual(before.read_bytes(), b'#!/bin/sh\n# new\nexit 0\n')

    def test_adoption_unpublished_stage_is_preserved_for_inspection(self):
        self.launchers_only()
        for action in ('exit 73', 'kill -KILL $$'):
            with self.subTest(action=action), self.fresh_fixture():
                self.launchers_only()
                failed = self.run_install(adopt=True, script=self.fault('sync -f "$stage"', 1, action))
                self.assertNotEqual(failed.returncode, 0)
                stages = list(self.state.glob('.install.*'))
                self.assertEqual(len(stages), 1)
                saved = self.saved_bytes(stages[0])
                self.assertNotEqual(self.run_install(adopt=True).returncode, 0)
                self.assertEqual(saved, self.saved_bytes(stages[0]))
                self.assertFalse((self.libexec / 'coop-worker').exists())

    def test_pending_adoption_refuses_other_generation_state_and_changed_evidence(self):
        cases = ('flag-absent', 'different-package', 'before', 'record', 'record-mode',
                 'before-mode', 'before-hardlink', 'after', 'live', 'live-mode',
                 'live-hardlink', 'state', 'missing-before-hash', 'empty-live-manifest')
        if os.geteuid() == 0:
            cases += ('live-owner', 'before-owner')
        for case in cases:
            with self.subTest(case=case), self.fresh_fixture():
                self.launchers_only()
                failed = self.run_install(adopt=True, script=self.fault('mv -f "$next" "$destination"', 1, 'exit 73'))
                self.assertNotEqual(failed.returncode, 0)
                pending = self.state / 'install-pending'
                before_launcher = pending / 'before' / FILES[1]
                live = self.libexec / 'coop-worker'
                if case == 'before': before_launcher.write_bytes(b'changed saved launcher')
                elif case == 'record': (pending / 'adoption').write_bytes(b'changed record')
                elif case == 'record-mode': (pending / 'adoption').chmod(0o644)
                elif case == 'before-mode': before_launcher.chmod(0o700)
                elif case == 'before-hardlink': os.link(before_launcher, self.root / 'shared-before')
                elif case == 'after': (pending / 'after' / FILES[1]).write_bytes(b'changed after')
                elif case == 'live': live.write_bytes(b'independent worker')
                elif case == 'live-mode': live.chmod(0o700)
                elif case == 'live-owner': os.chown(live, 65534, 65534)
                elif case == 'before-owner': os.chown(before_launcher, 65534, 65534)
                elif case == 'live-hardlink': os.link(live, self.root / 'shared-live')
                elif case == 'state': (self.root / 'worker-state').mkdir()
                elif case == 'empty-live-manifest':
                    manifest = self.state / 'install.sha256'
                    manifest.write_bytes(b'')
                    manifest.chmod(0o600)
                elif case == 'missing-before-hash':
                    evidence = pending / 'evidence.sha256'
                    evidence.write_text(''.join(row for row in evidence.read_text().splitlines(keepends=True)
                                                if 'before/' + FILES[1] not in row))
                saved = self.saved_bytes(pending)
                live_before = self.saved_bytes(self.libexec)
                manifest_before = ((self.state / 'install.sha256').read_bytes()
                                   if (self.state / 'install.sha256').exists() else None)
                result = self.run_install(version='later' if case == 'different-package' else 'new',
                                          adopt=case != 'flag-absent')
                self.assertNotEqual(result.returncode, 0, result.stderr)
                self.assertEqual(saved, self.saved_bytes(pending))
                self.assertEqual(live_before, self.saved_bytes(self.libexec))
                manifest_after = ((self.state / 'install.sha256').read_bytes()
                                  if (self.state / 'install.sha256').exists() else None)
                self.assertEqual(manifest_before, manifest_after)

    def test_adoption_copy_rename_and_sync_failures_are_retryable(self):
        commands = ['cp -p "$pending/after/$file" "$next"', 'sync -f "$next"',
                    'mv -f "$next" "$destination"', 'sync -f "$(dirname "$destination")"',
                    'cp -p "$pending/new.manifest" "$next"', 'mv -f "$next" "$manifest"',
                    'mv "$pending" "$history/generation"']
        for command in commands:
            with self.subTest(command=command), self.fresh_fixture():
                self.launchers_only()
                result = self.run_install(adopt=True, script=self.script.replace(command, 'false', 1))
                self.assertNotEqual(result.returncode, 0)
                result = self.run_install(adopt=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assert_generation('new')

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

    def test_compatibility_refusal_preserves_generation_and_pending_evidence(self):
        self.assertEqual(self.run_install('old').returncode, 0)
        refused = self.script.replace('check_compatibility() {',
                                      'check_compatibility() {\n  return 71')
        self.assertNotEqual(self.run_install(script=refused).returncode, 0)
        self.assert_generation('old')
        self.assertNotEqual(self.run_install(script=self.fault(
            'mv -f "$next" "$destination"', 2, 'exit 73')).returncode, 0)
        pending = self.state / 'install-pending'
        before = {str(p.relative_to(pending)): p.read_bytes()
                  for p in pending.rglob('*') if p.is_file()}
        result = self.run_install('later', script=refused)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b'compatibility refused', result.stderr)
        self.assertEqual(before, {str(p.relative_to(pending)): p.read_bytes()
                                  for p in pending.rglob('*') if p.is_file()})
        self.assertEqual(self.run_install().returncode, 0)
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

    def test_explicit_config_rebaseline_pins_live_and_manifest_and_preserves_before(self):
        self.assertEqual(self.run_install('old').returncode, 0)
        config = self.state / 'config.json'
        changed = b'#!/bin/sh\n# new\nexit 0\n'
        config.write_bytes(changed)
        old_manifest = (self.state / 'install.sha256').read_bytes()
        drift = (hashlib.sha256(changed).hexdigest(), hashlib.sha256(old_manifest).hexdigest())
        self.assertNotEqual(self.run_install(config_bytes=changed).returncode, 0)
        for incorrect in (("0" * 64, drift[1]), (drift[0], "0" * 64)):
            with self.subTest(incorrect=incorrect):
                self.assertNotEqual(self.run_install(config_bytes=changed, drift=incorrect).returncode, 0)
                self.assertEqual((self.state / 'install.sha256').read_bytes(), old_manifest)
        self.assertNotEqual(self.run_install(version='later', drift=drift).returncode, 0)
        result = self.run_install(config_bytes=changed, drift=drift)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_generation('new')
        generations = list(self.state.glob('install-history.*/generation'))
        saved = next(g for g in generations if (g / 'config-drift').exists())
        self.assertEqual((saved / 'old.manifest').read_bytes(), old_manifest)
        self.assertEqual((saved / 'before/config.json').read_bytes(), changed)
        self.assertEqual((saved / 'config-drift').read_text(),
                         f'accepted-config={drift[0]}\nold-manifest={drift[1]}\n')

    def test_config_rebaseline_rejects_other_drift_and_recovers_interruption(self):
        for corruption in ('binary', 'manifest-extra', 'mode', 'symlink', None):
            with self.subTest(corruption=corruption), self.fresh_fixture():
                self.assertEqual(self.run_install('old').returncode, 0)
                config = self.state / 'config.json'
                changed = b'#!/bin/sh\n# new\nexit 0\n'
                config.write_bytes(changed)
                drift = (hashlib.sha256(changed).hexdigest(),
                         hashlib.sha256((self.state / 'install.sha256').read_bytes()).hexdigest())
                if corruption == 'binary': (self.libexec / 'coop-worker').write_bytes(b'bad')
                if corruption == 'manifest-extra':
                    with (self.state / 'install.sha256').open('ab') as out: out.write(b'extra\n')
                    drift = (drift[0], hashlib.sha256((self.state / 'install.sha256').read_bytes()).hexdigest())
                if corruption == 'mode': config.chmod(0o644)
                if corruption == 'symlink':
                    config.unlink()
                    config.symlink_to(self.root / 'missing')
                if corruption is not None:
                    before = self.saved_bytes(self.libexec)
                    self.assertNotEqual(self.run_install(config_bytes=changed, drift=drift).returncode, 0)
                    self.assertEqual(before, self.saved_bytes(self.libexec))
                    self.assertFalse((self.state / 'install-pending').exists())
                    continue
                failed = self.run_install(config_bytes=changed, drift=drift,
                                          script=self.fault('mv -f "$next" "$destination"', 2, 'exit 73'))
                self.assertNotEqual(failed.returncode, 0)
                self.assertTrue((self.state / 'install-pending').is_dir())
                retry = self.run_install(config_bytes=changed)
                self.assertEqual(retry.returncode, 0, retry.stderr)
                self.assert_generation('new')

    def test_manifest_must_cover_exactly_the_complete_canonical_generation(self):
        for edit in (*FILES, 'duplicate-row', 'extra-row'):
            with self.subTest(edit=edit), tempfile.TemporaryDirectory() as directory:
                script = self.script.replace(str(self.root), directory)
                self.assertEqual(self.run_install('old', script=script).returncode, 0)
                state, libexec = Path(directory) / 'state', Path(directory) / 'libexec'
                manifest = state / 'install.sha256'
                rows = manifest.read_text().splitlines(keepends=True)
                paths = [(state if name == 'config.json' else libexec) / name for name in FILES]
                if edit in FILES:
                    index = FILES.index(edit)
                    paths[index].unlink()
                    del rows[index]
                elif edit == 'duplicate-row':
                    rows.append(rows[0])
                else:
                    outside = Path(directory) / 'outside'
                    outside.write_bytes(b'not a managed artifact')
                    rows.append(hashlib.sha256(outside.read_bytes()).hexdigest() + '  ' + str(outside) + '\n')
                damaged = ''.join(rows)
                manifest.write_text(damaged)
                result = self.run_install(script=script)
                self.assertNotEqual(result.returncode, 0, result.stderr)
                self.assertEqual(manifest.read_text(), damaged)
                self.assertFalse((state / 'install-pending').exists())
                for name, path in zip(FILES, paths):
                    if name == edit:
                        self.assertFalse(path.exists())
                    else:
                        self.assertIn('# old\n', path.read_text())

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
