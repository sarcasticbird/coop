"""Exercise the documented evidence-path assignments against the real path guard."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

import scheduler_artifacts


class ReleaseInstallRecipe(unittest.TestCase):
    def test_documented_manifest_is_outside_checkout_and_artifacts(self):
        guide = (Path(__file__).resolve().parent.parent / "docs/release.md").read_text()
        section = guide.split("## Matching named-agent helper installation", 1)[1]
        block = section.split('source_head=$(git rev-parse HEAD)', 1)[1].split("flox activate -- python3", 1)[0]
        self.assertNotRegex(block, r"\$\([^)]*(?:git|gh|curl|ssh)\b")
        with tempfile.TemporaryDirectory() as root:
            source = Path(root) / "source"
            source.mkdir()
            commands = block + '\nprintf "%s\\n" "$manifest"\n'
            result = subprocess.run(["sh", "-eu", "-c", commands], cwd=source,
                                    env=dict(os.environ, TMPDIR=root), capture_output=True, text=True, check=True)
            manifest = Path(result.stdout.strip())
            try:
                self.assertNotIn(source, manifest.parents)
                self.assertNotIn(source / "bin/agent-install/artifacts", manifest.parents)
                self.assertEqual(manifest.parent.stat().st_mode & 0o777, 0o700)
                # Exercise the real path guard and immutable writer. Source
                # provenance has its own clean-Git fixtures in Manifest tests.
                args = ["--source-root", str(source), "--source-head", "a" * 40,
                        "--artifacts", str(source / "bin/agent-install/artifacts"),
                        "--manifest", str(manifest)]
                with mock.patch.object(scheduler_artifacts, "manifest_bytes", return_value=b'{"version":1}\n'):
                    self.assertEqual(scheduler_artifacts.main(["create", *args]), 0)
                    self.assertEqual(scheduler_artifacts.main(["verify", *args]), 0)
            finally:
                if manifest.parent != source and manifest.parent.parent == Path(root):
                    if manifest.exists():
                        manifest.unlink()
                    manifest.parent.rmdir()
