"""Guard required setup-state coverage independently of ASCII cell fit."""

import re
import unittest

from validate import ROOT, validate


class SetupCoverageTests(unittest.TestCase):
    def test_authored_viewports(self):
        for width, height in ((120, 40), (80, 24), (40, 20)):
            with self.subTest(width=width):
                errors, _ = validate(ROOT / f"{width}x{height}.md", (width, height))
                self.assertEqual(errors, [])

    def test_each_missing_wide_setup_state_fails(self):
        for width, height in ((120, 40), (80, 24)):
            path = ROOT / f"{width}x{height}.md"
            text = path.read_text(encoding="utf-8")
            for key in ("S-master", "S-host"):
                with self.subTest(width=width, frame=key):
                    removed, count = re.subn(
                        rf"(?ms)^## {key}:.*?(?=^## |\Z)", "", text
                    )
                    self.assertEqual(count, 1)
                    errors, _ = validate(path, (width, height), removed)
                    self.assertIn(f"{path.name}: missing frame {key}", errors)


if __name__ == "__main__":
    unittest.main()
