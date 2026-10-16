"""Disposable offline fixtures only: no real profiles, binaries or credentials."""

import contextlib
import hashlib
import io
import json
from pathlib import Path
import struct
import subprocess
import tempfile
import unittest
from unittest import mock

import scheduler_artifacts as artifacts


def elf(machine=183, kind=2):
    data = bytearray(64)
    data[:7] = b"\x7fELF\x02\x01\x01"
    struct.pack_into("<HHI", data, 16, kind, machine, 1)
    return bytes(data)


def macho(cpu=0x0100000C, kind=2):
    return struct.pack("<8I", 0xFEEDFACF, cpu, 0, kind, 0, 0, 0, 0)


class Manifest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.base = Path(temp.name)
        self.source = self.base / "source"
        self.source.mkdir()
        self.binary_dir = self.base / "binaries"
        self.binary_dir.mkdir()
        self.destination = self.base / "manifest.json"
        self.git("init", "-q")
        for name in artifacts.SOURCES:
            path = self.source / name
            path.parent.mkdir(exist_ok=True)
            path.write_text(json.dumps({"version": artifacts.PROFILES[name]}) if name in artifacts.PROFILES else "# fixture\n")
        self.commit("repair fixture")
        repair = self.head()
        self.git("commit", "-q", "--allow-empty", "-m", "integration fixture")
        integration = self.head()
        self.git("commit", "-q", "--allow-empty", "-m", "source fixture")
        self.source_head = self.head()
        for constant, value in (("REPAIR_ANCESTOR", repair), ("INTEGRATION_ANCESTOR", integration)):
            patch = mock.patch.object(artifacts, constant, value)
            patch.start()
            self.addCleanup(patch.stop)
        for name in artifacts.BINARIES:
            (self.binary_dir / name).write_bytes(macho() if name == "coop" else elf())

    def git(self, *args):
        # Identity is local to each command, never changes user Git configuration.
        return subprocess.run(["git", "-C", str(self.source),
                               "-c", "user.name=Offline Fixture",
                               "-c", "user.email=fixture@example.invalid",
                               "-c", "commit.gpgsign=false", *args],
                              capture_output=True, check=True).stdout.decode().strip()

    def head(self):
        return self.git("rev-parse", "HEAD")

    def commit(self, message):
        self.git("add", ".")
        self.git("commit", "-q", "-m", message)

    def invoke(self, action, *extra):
        with contextlib.redirect_stdout(io.StringIO()) as out, contextlib.redirect_stderr(io.StringIO()) as err:
            status = artifacts.main([action, "--source-root", str(self.source),
                                     "--source-head", self.source_head,
                                     "--artifacts", str(self.binary_dir),
                                     "--manifest", str(self.destination), *extra])
        self.assertNotIn(str(self.base), out.getvalue() + err.getvalue())
        return status

    def create(self):
        self.assertEqual(self.invoke("create"), 0)
        return self.destination.read_bytes()

    def test_reproducible_exact_inventory_and_relative_paths(self):
        first = self.create()
        manifest = json.loads(first)
        self.assertEqual(manifest["source_head"], self.source_head)
        self.assertEqual(manifest["repair_ancestor"], artifacts.REPAIR_ANCESTOR)
        self.assertEqual(manifest["integration_ancestor"], artifacts.INTEGRATION_ANCESTOR)
        records = manifest["files"]
        expected = {"source/" + path for path in artifacts.SOURCES} | {"artifacts/" + name for name in artifacts.BINARIES}
        self.assertEqual({record["path"] for record in records}, expected)
        self.assertEqual(len(records), len(expected))
        self.assertIn("source/scripts/scheduler_artifacts.py", {record["path"] for record in records})
        self.assertNotIn(str(self.base).encode(), first)
        for record in records:
            prefix, relative = record["path"].split("/", 1)
            data = ((self.source if prefix == "source" else self.binary_dir) / relative).read_bytes()
            self.assertEqual(record["sha256"], hashlib.sha256(data).hexdigest())
            self.assertEqual(record["size"], len(data))
        self.assertEqual(self.invoke("verify"), 0)
        self.destination.unlink()
        self.assertEqual(self.create(), first)
        # Existing evidence cannot be silently replaced.
        self.assertEqual(self.invoke("create"), 1)
        self.assertEqual(self.destination.read_bytes(), first)

    def test_each_binary_mutation_is_rejected(self):
        self.create()
        for name in artifacts.BINARIES:
            with self.subTest(name=name):
                path = self.binary_dir / name
                original = path.read_bytes()
                path.write_bytes(original + b"mutation")
                self.assertEqual(self.invoke("verify"), 1)
                path.write_bytes(original)

    def test_each_required_source_mutation_or_deletion_is_rejected(self):
        self.create()
        for name in artifacts.SOURCES:
            with self.subTest(name=name):
                path = self.source / name
                original = path.read_bytes()
                path.write_bytes(original + b" ")
                self.assertEqual(self.invoke("verify"), 1)
                path.unlink()
                self.assertEqual(self.invoke("verify"), 1)
                path.write_bytes(original)

    def test_clean_but_missing_required_source_is_rejected(self):
        for name in artifacts.SOURCES:
            with self.subTest(name=name):
                path = self.source / name
                original = path.read_bytes()
                path.unlink()
                self.commit("remove required source")
                self.source_head = self.head()
                self.assertEqual(self.invoke("create"), 1)
                path.write_bytes(original)
                self.commit("restore required source")
                self.source_head = self.head()

    def test_changed_head_even_when_clean_is_rejected(self):
        self.create()
        self.git("commit", "-q", "--allow-empty", "-m", "new head")
        self.assertEqual(self.invoke("verify"), 1)
        self.source_head = self.head()
        self.assertEqual(self.invoke("verify"), 1)

    def test_dirty_staged_and_untracked_sources_are_rejected(self):
        path = self.source / artifacts.SOURCES[0]
        path.write_text("changed")
        self.assertEqual(self.invoke("create"), 1)
        self.git("add", ".")
        self.assertEqual(self.invoke("create"), 1)
        self.commit("changed source")
        self.source_head = self.head()
        (self.source / "unexpected").write_text("untracked")
        self.assertEqual(self.invoke("create"), 1)

    def test_source_bytes_hidden_from_git_status_are_rejected(self):
        name = artifacts.SOURCES[0]
        self.git("update-index", "--assume-unchanged", name)
        (self.source / name).write_text("hidden modification")
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertEqual(self.invoke("create"), 1)

    def test_hidden_changes_outside_inventory_are_rejected(self):
        name = "cmd/coop-worker/main.go"
        self.assertNotIn(name, artifacts.SOURCES)
        path = self.source / name
        path.parent.mkdir(parents=True)
        original = "package main\n"
        path.write_text(original)
        self.commit("tracked Go source outside manifest inventory")
        self.source_head = self.head()
        manifest = self.create()
        for flag in ("assume-unchanged", "skip-worktree"):
            with self.subTest(flag=flag):
                self.destination.unlink()
                self.git("update-index", "--" + flag, name)
                # Reject even an unmodified flagged entry: status cannot be
                # trusted to report future changes to that tracked file.
                with self.assertRaisesRegex(ValueError, "source index"):
                    artifacts.source_identity(self.source, self.source_head)
                path.write_text(original + "// hidden modification\n")
                self.assertEqual(self.git("status", "--porcelain"), "")
                with self.assertRaisesRegex(ValueError, "source index"):
                    artifacts.source_identity(self.source, self.source_head)
                self.assertEqual(self.invoke("create"), 1)
                self.assertFalse(self.destination.exists())
                self.destination.write_bytes(manifest)
                self.assertEqual(self.invoke("verify"), 1)
                path.write_text(original)
                self.git("update-index", "--no-" + flag, name)
        self.assertEqual(self.invoke("verify"), 0)

    def test_committed_source_symlink_is_rejected(self):
        path = self.source / artifacts.SOURCES[0]
        path.unlink()
        path.symlink_to(Path(artifacts.SOURCES[1]).name)
        self.commit("symlink source")
        self.source_head = self.head()
        self.assertEqual(self.invoke("create"), 1)

    def test_head_must_be_exact_full_lowercase_sha(self):
        for head in (self.source_head[:12], self.source_head.upper(), "HEAD", "0" * 40):
            with self.subTest(head=head):
                self.assertEqual(self.invoke("create", "--source-head", head), 1)

    def test_missing_or_unrelated_repair_and_integration_are_rejected(self):
        unrelated = self.git("commit-tree", "HEAD^{tree}", "-m", "unrelated root")
        for constant in ("REPAIR_ANCESTOR", "INTEGRATION_ANCESTOR"):
            for ancestor in ("0" * 40, unrelated):
                with self.subTest(constant=constant, ancestor=ancestor):
                    with mock.patch.object(artifacts, constant, ancestor):
                        self.assertEqual(self.invoke("create"), 1)
        self.assertFalse(self.destination.exists())

    def test_wrong_architecture_format_and_truncated_headers(self):
        for name in artifacts.BINARIES:
            path = self.binary_dir / name
            original = path.read_bytes()
            bad_headers = (b"", b"invalid", original[:20], elf(62), macho(0x01000007), macho(kind=1))
            for header in bad_headers:
                with self.subTest(name=name, header=header):
                    path.write_bytes(header)
                    self.assertEqual(self.invoke("create"), 1)
            path.write_bytes(original)
        artifacts.check_binary(elf(kind=3), "linux/arm64")

    def test_missing_unexpected_and_symlink_artifacts(self):
        path = self.binary_dir / "coop-worker"
        original = path.read_bytes()
        path.unlink()
        self.assertEqual(self.invoke("create"), 1)
        path.symlink_to(self.binary_dir / "coop-github")
        self.assertEqual(self.invoke("create"), 1)
        path.unlink()
        path.mkdir()
        self.assertEqual(self.invoke("create"), 1)
        path.rmdir()
        path.write_bytes(original)
        (self.binary_dir / "extra").write_text("unexpected")
        self.assertEqual(self.invoke("create"), 1)

    def test_invalid_profile_versions_and_duplicate_keys(self):
        for name in artifacts.PROFILES:
            path = self.source / name
            for body in ('{"version":99}', '{"version":1,"version":2}', '[]', '{broken'):
                with self.subTest(name=name, body=body):
                    path.write_text(body)
                    self.commit("invalid example")
                    self.source_head = self.head()
                    self.assertEqual(self.invoke("create"), 1)
            path.write_text(json.dumps({"version": artifacts.PROFILES[name]}))
            self.commit("restore example")
            self.source_head = self.head()

    def test_manifest_duplicate_unknown_missing_and_changed_records(self):
        original = self.create()
        for mutation in ("duplicate", "unknown", "missing", "digest", "head", "target"):
            manifest = json.loads(original)
            if mutation == "duplicate":
                manifest["files"].append(manifest["files"][0])
            elif mutation == "unknown":
                manifest["unexpected"] = "input"
            elif mutation == "missing":
                manifest["files"].pop()
            elif mutation == "head":
                manifest["source_head"] = "0" * 40
            else:
                manifest["files"][0]["sha256" if mutation == "digest" else "target"] = "wrong"
            self.destination.write_bytes(artifacts.canonical(manifest))
            self.assertEqual(self.invoke("verify"), 1, mutation)
        self.destination.write_bytes(original.replace(b'"version": 1', b'"version": 1, "version": 1'))
        self.assertEqual(self.invoke("verify"), 1)

    def test_manifest_destination_cannot_overwrite_inputs_or_follow_symlink(self):
        for path in (self.source / "manifest.json", self.binary_dir / "coop"):
            self.assertEqual(self.invoke("create", "--manifest", str(path)), 1)
        target = self.base / "untouched"
        target.write_text("preserve")
        self.destination.symlink_to(target)
        self.assertEqual(self.invoke("create"), 1)
        self.assertEqual(self.invoke("verify"), 1)
        self.assertEqual(target.read_text(), "preserve")


if __name__ == "__main__":
    unittest.main()
