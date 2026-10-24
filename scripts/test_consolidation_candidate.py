"""Portable offline candidate tests. Fake executable headers, no installation."""
import json
from pathlib import Path
import struct
import subprocess
import tempfile
import unittest
from unittest import mock

import consolidation_candidate as candidate


class CandidateTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.git("init", "-q")
        self.git("config", "user.name", "Candidate fixture")
        self.git("config", "user.email", "candidate@example.invalid")
        (self.root / ".gitignore").write_text("/bin/\n")
        (self.root / "source.go").write_text("package fixture\n")
        self.git("add", ".")
        self.git("commit", "-qm", "fixture source")
        self.head = self.git("rev-parse", "HEAD").strip()
        self.output = self.root / "bin" / "candidate-v1"

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.root), *args], text=True)

    def fake_build(self, args, **kwargs):
        if args[0] != "go":
            return self.original_run(args, **kwargs)
        self.assertIn("-buildvcs=true", args)
        self.assertEqual(kwargs["env"]["GOFLAGS"], "")
        self.assertEqual(kwargs["env"]["CGO_ENABLED"], "0")
        data = bytearray(128)
        if kwargs["env"]["GOOS"] == "darwin":
            struct.pack_into("<IIII", data, 0, 0xFEEDFACF, 0x0100000C, 0, 2)
            self.assertIn("main.sourceRevision=" + self.head, args[args.index("-ldflags") + 1])
        else:
            data[:7] = b"\x7fELF\x02\x01\x01"
            struct.pack_into("<HHI", data, 16, 2, 183, 1)
        Path(args[args.index("-o") + 1]).write_bytes(data)
        return subprocess.CompletedProcess(args, 0)

    def stage(self, dirty=False):
        self.original_run = subprocess.run
        with mock.patch.object(candidate.subprocess, "run", side_effect=self.fake_build):
            return candidate.stage(self.root, self.head, self.output, dirty)

    def test_one_profile_exact_artifact_and_source(self):
        manifest = self.stage()
        self.assertEqual(manifest["profile"], str(self.output / "config" / "coop"))
        self.assertEqual(candidate.verify(self.root, self.head, self.output), manifest)
        self.assertEqual(manifest["acceptance"], "pending-review-and-operator-uat")
        self.assertNotIn("token", json.dumps(manifest))
        with self.assertRaises(ValueError):
            self.stage()
        artifact = self.output / "artifacts" / "coop"
        artifact.write_bytes(artifact.read_bytes() + b"tampered")
        with self.assertRaisesRegex(ValueError, "artifact mismatch"):
            candidate.verify(self.root, self.head, self.output)

    def test_dirty_preview_tracks_new_untracked_source(self):
        initial = candidate.source_snapshot(self.root, self.head)
        (self.root / "new.go").write_text("new source\n")
        preview = self.stage(True)
        self.assertTrue(preview["source_modified"])
        self.assertNotEqual(initial["source_tree_sha256"], preview["source_tree_sha256"])
        with self.assertRaisesRegex(ValueError, "cannot be installed"):
            candidate.verify(self.root, self.head, self.output)
        candidate.verify(self.root, self.head, self.output, require_clean=False)
        (self.root / "new.go").write_text("changed new source\n")
        with self.assertRaisesRegex(ValueError, "source mismatch"):
            candidate.verify(self.root, self.head, self.output, require_clean=False)

    def test_dirty_requires_opt_in_and_wrong_head_rejected(self):
        (self.root / "source.go").write_text("changed\n")
        with self.assertRaisesRegex(ValueError, "allow-dirty"):
            self.stage()
        with self.assertRaisesRegex(ValueError, "HEAD mismatch"):
            candidate.source_snapshot(self.root, "a" * 40)
        self.assertFalse(self.output.exists())

    def test_profile_switch_and_deleted_source_are_visible(self):
        self.stage()
        path = self.output / "config" / "coop" / "candidate.json"
        manifest = json.loads(path.read_text())
        manifest["profile"] = str(self.root / "other-profile")
        path.write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, "profile/schema"):
            candidate.verify(self.root, self.head, self.output)
        (self.root / "source.go").unlink()
        snapshot = candidate.source_snapshot(self.root, self.head)
        self.assertEqual(snapshot["files"]["source.go"], {"deleted": True})
        self.assertTrue(snapshot["source_modified"])

    def test_hidden_index_and_source_symlink_are_refused(self):
        self.git("update-index", "--assume-unchanged", "source.go")
        with self.assertRaisesRegex(ValueError, "hidden index"):
            candidate.source_snapshot(self.root, self.head)
        self.git("update-index", "--no-assume-unchanged", "source.go")
        (self.root / "linked.go").symlink_to("source.go")
        with self.assertRaisesRegex(ValueError, "symlink input"):
            candidate.source_snapshot(self.root, self.head)

    def test_source_change_during_build_never_publishes_manifest(self):
        original = self.fake_build
        def mutate(args, **kwargs):
            result = original(args, **kwargs)
            if args[0] == "go":
                (self.root / "source.go").write_text("concurrent edit\n")
            return result
        self.original_run = subprocess.run
        with mock.patch.object(candidate.subprocess, "run", side_effect=mutate):
            with self.assertRaisesRegex(ValueError, "source changed during build"):
                candidate.stage(self.root, self.head, self.output)
        self.assertFalse((self.output / "config" / "coop" / "candidate.json").exists())

    def test_ignored_private_files_do_not_enter_source_snapshot(self):
        before = candidate.source_snapshot(self.root, self.head)
        (self.root / "bin").mkdir()
        (self.root / "bin" / "private-token").write_text("fixture-only")
        self.assertEqual(before, candidate.source_snapshot(self.root, self.head))

    def test_nonignored_output_and_symlink_destination_are_refused(self):
        with self.assertRaisesRegex(ValueError, "ignored bin"):
            candidate.stage(self.root, self.head, self.root / "published")
        (self.root / "bin").symlink_to(self.root, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "symlink"):
            candidate.stage(self.root, self.head, self.output)


if __name__ == "__main__":
    unittest.main()
