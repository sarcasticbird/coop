"""Capture the fixture native TUI on a real PTY using only the standard library.

Raw .ansi files are authoritative terminal output. .txt files replay the CSI
cursor/erase subset emitted by Bubble Tea for convenient no-color inspection.
No View() strings are used. Run from the repository root after building the
opt-in test executable; output must be a new evidence directory.
"""

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pty
import re
import select
import struct
import subprocess
import termios
import time
import unicodedata


def replay(data, width, height):
    screen = [[" " for _ in range(width)] for _ in range(height)]
    row = col = 0
    tokens = re.findall(r"\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07]*(?:\x07)|\x1b.|[^\x1b]", data.decode("utf-8", "replace"))
    for token in tokens:
        if token.startswith("\x1b["):
            cmd = token[-1]
            private = token[2:-1].startswith("?")
            if private:
                continue
            args = [int(x or 0) for x in token[2:-1].split(";") if x.isdigit() or x == ""]
            n = (args or [0])[0]
            if cmd in "Hf":
                row = max(0, (n or 1) - 1)
                col = max(0, (args[1] if len(args) > 1 else 1) - 1)
            elif cmd == "A": row = max(0, row - (n or 1))
            elif cmd == "B": row = min(height - 1, row + (n or 1))
            elif cmd == "C": col = min(width - 1, col + (n or 1))
            elif cmd == "D": col = max(0, col - (n or 1))
            elif cmd == "G": col = max(0, (n or 1) - 1)
            elif cmd == "J":
                for y in range(height):
                    for x in range(width):
                        if n == 2 or n == 0 and (y, x) >= (row, col) or n == 1 and (y, x) <= (row, col):
                            screen[y][x] = " "
            elif cmd == "K" and row < height:
                for x in range(width):
                    if n == 2 or n == 0 and x >= col or n == 1 and x <= col:
                        screen[row][x] = " "
            continue
        if token.startswith("\x1b"): continue
        if token == "\r": col = 0
        elif token == "\n": row = min(height - 1, row + 1)
        elif token == "\b": col = max(0, col - 1)
        elif not unicodedata.category(token).startswith("C"):
            if unicodedata.combining(token):
                if col: screen[min(row, height - 1)][min(col - 1, width - 1)] += token
                continue
            cells = 2 if unicodedata.east_asian_width(token) in "WF" else 1
            if col >= width:
                col, row = 0, min(height - 1, row + 1)
            if row < height:
                screen[row][col] = token
                if cells == 2 and col + 1 < width: screen[row][col + 1] = ""
            col += cells
    return "\n".join("".join(line).rstrip() for line in screen) + "\n"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--source", help="production source revision used to build the fixture executable")
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--extended", action="store_true", help="also capture help, confirmations, compose previews and read-only receipts without submitting")
    modes.add_argument("--supplemental", action="store_true", help="capture reply/receipt and isolated missing-master, first-use and destroy states")
    modes.add_argument("--exact-context", action="store_true", help="verify long master/principal IDs in scrolled Team and unbound Details")
    args = parser.parse_args()
    args.output.mkdir(mode=0o700, parents=False, exist_ok=False)
    source = args.source
    if not source or not re.fullmatch(r"[0-9a-f]{40}", source):
        source = subprocess.check_output(["git", "rev-parse", source or "HEAD"], text=True).strip()
    manifest = {"source": source,
                "binary": str(args.binary.resolve()), "sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(),
                "fixture": "TestRuntimeCapture / tuifixture.New (synthetic only)", "method": "PTY, native Bubble Tea, timed keyboard navigation", "captures": []}
    manifest["extended"] = args.extended
    manifest["supplemental"] = args.supplemental
    manifest["exact_context"] = args.exact_context
    manifest["fixture_overlay"] = "Updated test-only launcher; source identifies production revision, not an unmodified source tree. Build with these fixture inputs."
    manifest["harness_sha256"] = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
    fixture_paths = ["internal/tui/runtime_capture_test.go", "internal/tui/runtime_baseline_test.go",
                     "internal/tui/team_view_test.go", "internal/tui/coordination_test.go",
                     "internal/tui/fleet_test.go", *sorted(str(p) for p in Path("internal/tui/tuifixture").glob("*.go"))]
    manifest["fixture_sources"] = {
        path: {"sha256": hashlib.sha256(Path(path).read_bytes()).hexdigest()}
        for path in fixture_paths
    }
    for width, height in [(120, 40), (80, 24), (40, 20)]:
        for no_color, mode in [(color, mode) for color in [False, True]
                               for mode in (["", "setup", "first-use", "destroy"] if args.supplemental else [""])]:
            master, slave = pty.openpty()
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", height, width, 0, 0))
            env = dict(os.environ, COOP_TUI_CAPTURE="1", COOP_TUI_CAPTURE_MODE=mode, TERM="xterm-256color")
            env["COOP_TUI_CAPTURE_EXACT_CONTEXT"] = "1" if args.exact_context else "0"
            if no_color: env["NO_COLOR"] = "1"
            else: env.pop("NO_COLOR", None)
            process = subprocess.Popen([str(args.binary.resolve()), "-test.run=^TestRuntimeCapture$"], stdin=slave, stdout=slave, stderr=slave, env=env)
            os.close(slave)
            raw = bytearray()

            def drain():
                end = time.monotonic() + 0.8
                while time.monotonic() < end:
                    if select.select([master], [], [], 0.05)[0]:
                        try: chunk = os.read(master, 65536)
                        except OSError: break
                        raw.extend(chunk)
                        # Answer terminal position queries, as an actual terminal does.
                        if b"\x1b[6n" in chunk: os.write(master, b"\x1b[1;1R")
                        if b"\x1b]11;?" in chunk: os.write(master, b"\x1b]11;rgb:0000/0000/0000\x07")

            pages = [("fleet", b""), ("fleet-selected", b"\x1b[B"), ("details", b"\r"), ("threads", b"\x1b2l"), ("reader", b"\r"), ("team", b"\x1bt"), ("team-detail", b"\r")]
            expected_pages = {"fleet": "Fleet", "fleet-selected": "Fleet", "details": "Fleet > Details", "threads": "Threads", "reader": "Threads > fixture-thread", "team": "Team", "team-detail": "Team > Agent"}
            if args.exact_context:
                pages.insert(3, ("details-exact-context", b"jjjjjj"))
                pages.append(("team-exact-context", b"jjjjjj"))
                expected_pages.update({"details-exact-context": "Fleet > Details", "team-exact-context": "Team > Agent"})
            if args.extended:
                # Each list item is one terminal key or a synthetic text chunk.
                # Never send y in confirmation or a second Ctrl+S in preview.
                extended = [
                    ("team-help", [b"?"], "Team Help"),
                    ("servers", [b"\x1b", b"3"], "CONFIGURED SSH REFERENCE"),
                    ("ssh-add", [b"a"], "ADD SERVER"),
                    ("ssh-add-help", [b"\x1bOP"], "SSH Server Help"),
                    ("details-help", [b"\x1b", b"\x1b", b"1", b"\r", b"?"], "Help"),
                    ("connect-confirmation", [b"\x1b", b"\r"], "Connect on the foreground terminal?"),
                    ("threads-new", [b"\x1b", b"2", b"\x1b", b"l", b"n"], "Threads > Composer (create)"),
                    ("create-preview", [b"Synthetic capture", b"\t", b"coop", b"\t", b"owner,hawk", b"\x13"], "Threads > Preview (create)"),
                    ("root-composer", [b"\x1b", b"d", b"\r", b"c"], "Threads > Composer (send)"),
                    ("composer-help", [b"\x1bOP"], "Composer Help"),
                    ("root-preview", [b"\x1b", b"hawk", b"\t", b"Synthetic capture only.", b"\x13"], "Threads > Preview (send)"),
                    ("draft-guard", [b"\x1b"], "Draft protection"),
                    ("reply-composer", [b"d", b"p"], "Threads > Composer (reply)"),
                    ("reply-preview", [b"hawk", b"\t", b"Synthetic reply only.", b"\x13"], "Threads > Preview (reply)"),
                    ("receipt", [b"\x1b", b"d", b"s"], "Threads > Delivery receipt"),
                ]
                pages.extend((page, keys) for page, keys, _ in extended)
                expected_pages.update((page, expected) for page, _, expected in extended)
            if args.supplemental:
                supplemental = {
                    "": [("reply-composer", [b"2", b"l", b"\r", b"p"], "Threads > Composer (reply)"),
                         ("reply-preview", [b"hawk", b"\t", b"Synthetic reply only.", b"\x13"], "Threads > Preview (reply)"),
                         ("receipt", [b"\x1b", b"d", b"s"], "Threads > Delivery receipt")],
                    "setup": [("missing-master", [], "Master setup")],
                    "first-use": [("no-remote-first-use", [], "SSH setup")],
                    "destroy": [("destroy-confirmation", [b"\x1b[B", b"\r", b"d"], "CONFIRM DESTROY")],
                }[mode]
                pages = [(page, keys) for page, keys, _ in supplemental]
                expected_pages = {page: expected for page, _, expected in supplemental}
            for page, keys in pages:
                if isinstance(keys, list):
                    for key in keys:
                        os.write(master, key)
                        drain()
                    keys = b""
                if keys.startswith(b"\x1b") and not keys.startswith(b"\x1b["):
                    os.write(master, b"\x1b")
                    drain()
                    keys = keys[1:]
                if keys.startswith(b"\x1b["):
                    os.write(master, keys[:3])
                    drain()
                    keys = keys[3:]
                for key in keys:
                    os.write(master, bytes([key]))
                    drain()
                drain()
                if b"panic:" in raw or process.poll() is not None:
                    raise RuntimeError(f"fixture exited during {page}: {bytes(raw[-5000:])!r}")
                stem = f"{width}x{height}-{'no-color-' if no_color else ''}{page}"
                screen = replay(raw, width, height)
                expected = expected_pages[page]
                if expected not in screen:
                    raise RuntimeError(f"navigation did not reach {page}: {screen}")
                if page.endswith("exact-context"):
                    for prefix in ("master-", "principal-"):
                        if prefix + "shared-prefix-" * 8 + "second" not in screen.replace("\n", ""):
                            raise RuntimeError(f"full exact identity not inspectable in {page}: {screen}")
                (args.output / (stem + ".ansi")).write_bytes(raw)
                (args.output / (stem + ".txt")).write_text(screen)
                manifest["captures"].append(stem)
            if mode == "destroy":
                os.write(master, b"\x1b")  # Cancel, never consent to destruction.
                drain()
            os.write(master, b"\x03")
            try: process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.terminate()
                process.wait(timeout=5)
            if process.returncode != 0:
                raise RuntimeError(f"fixture exit {process.returncode}")
            os.close(master)
    (args.output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")


if __name__ == "__main__":
    main()
