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
    args = parser.parse_args()
    args.output.mkdir(mode=0o700, parents=False, exist_ok=False)
    manifest = {"source": subprocess.check_output(["git", "rev-parse", args.source or "HEAD"], text=True).strip(),
                "binary": str(args.binary.resolve()), "sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(),
                "fixture": "TestRuntimeCapture / tuifixture.New (synthetic only)", "method": "PTY, native Bubble Tea, timed keyboard navigation", "captures": []}
    for width, height in [(120, 40), (80, 24), (40, 20)]:
        for no_color in [False, True]:
            master, slave = pty.openpty()
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", height, width, 0, 0))
            env = dict(os.environ, COOP_TUI_CAPTURE="1", TERM="xterm-256color")
            if no_color: env["NO_COLOR"] = "1"
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

            for page, keys in [("fleet", b""), ("fleet-selected", b"\x1b[B"), ("details", b"\r"), ("threads", b"\x1b2l"), ("reader", b"\r"), ("team", b"\x1bt"), ("team-detail", b"\r")]:
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
                expected = {"fleet": "Fleet", "fleet-selected": "Fleet", "details": "Fleet > Details", "threads": "Threads", "reader": "Threads > fixture-thread", "team": "Team", "team-detail": "Team > Agent"}[page]
                if expected not in screen:
                    raise RuntimeError(f"navigation did not reach {page}: {screen}")
                (args.output / (stem + ".ansi")).write_bytes(raw)
                (args.output / (stem + ".txt")).write_text(screen)
                manifest["captures"].append(stem)
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
