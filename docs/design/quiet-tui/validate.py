"""Validate authored ASCII static viewport fixtures, not runtime TUI behavior."""

from pathlib import Path
import re


ROOT = Path(__file__).resolve().parent
COMMON = {
    "H", "F0", "F", "F-offline", "F-reconnect", "T-list", "T",
    "D", "L", "R", "R-select", "N", "C-root", "C", "P", "Q", "X", "A",
    "S-master", "S-host", "G", "B",
}


def validate(path, default_size=None, text=None):
    heading = ""
    size = default_size
    lines = None
    frames = set()
    errors = []
    if text is None:
        text = path.read_text(encoding="utf-8")
    for number, line in enumerate(text.splitlines(), 1):
        if line.startswith("##") and lines is None:
            heading = line.lstrip("# ")
            if default_size is None:
                match = re.search(r"(\d+)x(\d+)", heading)
                size = tuple(map(int, match.groups())) if match else None
        if line == "```text":
            lines = []
        elif line == "```" and lines is not None:
            if size is None:
                errors.append(f"{path.name}:{number}: missing viewport dimensions")
            else:
                width, height = size
                if len(lines) > height:
                    errors.append(f"{path.name}:{heading}: {len(lines)} rows > {height}")
                for row, text in lines:
                    if not text.isascii() or any(ord(c) < 32 for c in text):
                        errors.append(f"{path.name}:{row}: non-ASCII/control cell")
                    if len(text) > width:
                        errors.append(f"{path.name}:{row}: {len(text)} cells > {width}")
            key = heading.split(":", 1)[0]
            if key in frames:
                errors.append(f"{path.name}:{number}: duplicate frame {key}")
            frames.add(key)
            lines = None
        elif lines is not None:
            lines.append((number, line))
    if lines is not None:
        errors.append(f"{path.name}: unclosed frame")
    if default_size is not None:
        required = COMMON | {"S" if default_size == (40, 20) else "S-list"}
        for key in sorted(required - frames):
            errors.append(f"{path.name}: missing frame {key}")
    elif len(frames) != 9:
        errors.append(f"{path.name}: expected 9 direction comparison frames")
    return errors, len(frames)


if __name__ == "__main__":
    failures = []
    count = 0
    for name, dimensions in (
        ("120x40.md", (120, 40)),
        ("80x24.md", (80, 24)),
        ("40x20.md", (40, 20)),
        ("directions.md", None),
    ):
        errors, frames = validate(ROOT / name, dimensions)
        failures.extend(errors)
        count += frames
    if failures:
        raise SystemExit("\n".join(failures))
    print(f"PASS: {count} static frames fit declared ASCII terminal viewports.")
    print("Blank remainder is intentional; no runtime or independent QA claimed.")
