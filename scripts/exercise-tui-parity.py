"""Exercise the synthetic TestRuntimeCapture executable on six bounded real PTYs.

Usage: python3 scripts/exercise-tui-parity.py BINARY NEW_OUTPUT --source REVISION
Builds nothing. No confirmation, reveal, composer, or send keys are issued.
The existing capture-tui.py supplies replay; raw ANSI remains authoritative.
"""

import argparse
import errno
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import pty
import re
import select
import signal
import struct
import subprocess
import sys
import termios
import time


BOUNDARY = 0.8
SIZES = ((120, 40), (80, 24), (40, 20))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path, help="already-built native TestRuntimeCapture executable")
    parser.add_argument("output", type=Path, help="new directory under an existing parent")
    parser.add_argument("--source", required=True, help="source revision used to build the executable")
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    if not binary.is_file() or not os.access(binary, os.X_OK):
        parser.error("binary must be an executable file")
    root = Path(__file__).resolve().parent.parent
    source = args.source
    if not re.fullmatch(r"[0-9a-f]{40}", source):
        source = subprocess.check_output(
            ["git", "rev-parse", "--verify", f"{source}^{{commit}}"], cwd=root, text=True
        ).strip()
    replay_path = root / "scripts/capture-tui.py"
    sys.dont_write_bytecode = True
    spec = importlib.util.spec_from_file_location("capture_tui_parity_replay", replay_path)
    capture = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(capture)
    args.output.mkdir(mode=0o700, parents=False, exist_ok=False)
    manifest = {
        "source": source,
        "binary": str(binary),
        "sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "harness_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
        "replay_sha256": hashlib.sha256(replay_path.read_bytes()).hexdigest(),
        "fixture": "TestRuntimeCapture / synthetic only; fixture rejects action dispatch and sends",
        "method": "native Bubble Tea on real PTY; fixed 0.8s input boundaries; ioctl + SIGWINCH resize",
        "source_limit": "source is caller-supplied build provenance, not proof of an unmodified checkout",
        "raw_policy": (
            "Each checkpoint ANSI is cumulative within one fixed-dimension segment. "
            "Before a changed-size ioctl, drain the old segment, preserve it, then start a new "
            "raw segment. Replay starts blank and relies on the native resize repaint; no "
            "synthetic ANSI is inserted. Full session ANSI preserves every byte across all "
            "phases; segment offsets and dimensions describe how to interpret it. "
            "A same-size SIGWINCH retains the current segment."
        ),
        "limits": [
            "No quiet-refresh content equality inferred from timestamps or fixed waits.",
            "Selection identity is checked wherever rendered; hidden internal state is not observable.",
            "Single-message fixture cannot demonstrate preservation among multiple reply candidates.",
            "Fleet paging uses actual fixture rows only; no long-name or Unicode data is injected.",
            "Replay covers the existing capture script's CSI/Unicode subset, not a full terminal emulator.",
            "No live transport, sends, confirmations, owner UAT or visual-parity verdict.",
        ],
        "sessions": [],
        "captures": [],
        "status": "running",
    }

    def save_manifest():
        (args.output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")

    try:
        for initial_width, initial_height in SIZES:
            for no_color in (False, True):
                session_id = f"{initial_width}x{initial_height}-{'no-color' if no_color else 'color'}"
                session = {"id": session_id, "initial_width": initial_width, "initial_height": initial_height,
                           "no_color": no_color, "segments": [], "events": [], "status": "running"}
                manifest["sessions"].append(session)
                master, slave = pty.openpty()
                process = None
                raw = bytearray()
                segment_start = 0
                width, height = initial_width, initial_height
                query_tail = b""
                context = None

                def begin_segment():
                    session["segments"].append({"index": len(session["segments"]), "width": width,
                                                "height": height, "start_byte": len(raw)})

                def drain():
                    nonlocal query_tail
                    end = time.monotonic() + BOUNDARY
                    while time.monotonic() < end:
                        if not select.select([master], [], [], min(0.05, max(0, end - time.monotonic())))[0]:
                            continue
                        try:
                            chunk = os.read(master, 65536)
                        except OSError as exc:
                            if exc.errno == errno.EIO:
                                break
                            raise
                        if not chunk:
                            break
                        raw.extend(chunk)
                        if len(raw) > 32 * 1024 * 1024:
                            raise RuntimeError("PTY output exceeded 32 MiB session bound")
                        combined = query_tail + chunk
                        for query, answer in ((b"\x1b[6n", b"\x1b[1;1R"),
                                              (b"\x1b]11;?", b"\x1b]11;rgb:0000/0000/0000\x07")):
                            for match in re.finditer(re.escape(query), combined):
                                if match.end() > len(query_tail):
                                    os.write(master, answer)
                        query_tail = combined[-16:]

                def key(label, value):
                    session["events"].append({"input": label, "hex": value.hex(), "byte_offset": len(raw)})
                    os.write(master, value)
                    drain()

                def checkpoint(phase, page, reader=False, masked=False):
                    screen = capture.replay(bytes(raw[segment_start:]), width, height)
                    stem = f"{session_id}-{len(manifest['captures']):03d}-{phase}-{width}x{height}"
                    (args.output / f"{stem}.ansi").write_bytes(raw[segment_start:])
                    (args.output / f"{stem}.txt").write_text(screen, encoding="utf-8")
                    entry = {"stem": stem, "session": session_id, "phase": phase, "width": width,
                             "height": height, "segment": len(session["segments"]) - 1,
                             "start_byte": segment_start, "end_byte": len(raw), "checks": []}
                    manifest["captures"].append(entry)
                    if process.poll() is not None or b"panic:" in raw or b"--- FAIL:" in raw:
                        raise RuntimeError(f"{stem}: fixture exited or failed before quit")
                    # Reject even transient body output, not just the final replayed frame.
                    if b"Synthetic" in raw or "Content: revealed" in screen:
                        raise RuntimeError(f"{stem}: private synthetic body/revealed state appeared")
                    lines = screen.splitlines()
                    if not lines or not lines[0].startswith(page):
                        raise RuntimeError(f"{stem}: expected pinned page {page!r}; see capture")
                    entry["checks"].extend(["expected page", "no synthetic body in cumulative raw"])
                    if reader:
                        if tuple(lines[1:3]) != context or "Esc Threads" not in screen:
                            raise RuntimeError(f"{stem}: reader lost pinned identity or Esc Threads")
                        if "^V reveal" not in screen or "^V mask" in screen:
                            raise RuntimeError(f"{stem}: reader no longer advertises masked state")
                        if masked and "Content: masked" not in screen:
                            raise RuntimeError(f"{stem}: initial reader is not visibly masked")
                        targets = re.findall(r"Reply target(?: offscreen)?: ([^\s/]+)", screen)
                        selections = re.findall(r"^> (?:\[unread this session\] )?([^\s|]+)\s*\|", screen, re.M)
                        if any(target != "fixture-message" for target in targets + selections):
                            raise RuntimeError(f"{stem}: reply target/selection changed: {targets + selections}")
                        if masked and not targets and not selections:
                            raise RuntimeError(f"{stem}: fixture-message selection not observable")
                        entry["visible_reply_targets"] = targets
                        entry["visible_message_selections"] = selections
                        entry["checks"].extend(["pinned reader identity", "masked reader controls",
                                                "exact fixture-message identity wherever visible", "Esc Threads"])
                    return screen

                try:
                    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", height, width, 0, 0))
                    env = dict(os.environ, COOP_TUI_CAPTURE="1", COOP_TUI_CAPTURE_MODE="", TERM="xterm-256color")
                    if no_color:
                        env["NO_COLOR"] = "1"
                    else:
                        env.pop("NO_COLOR", None)
                    process = subprocess.Popen([str(binary), "-test.run=^TestRuntimeCapture$", "-test.count=1",
                                                "-test.timeout=90s"],
                                               stdin=slave, stdout=slave, stderr=slave, env=env)
                    os.close(slave)
                    slave = None
                    begin_segment()
                    drain()
                    checkpoint("fleet", "Fleet")
                    key("Fleet selection down", b"\x1b[B")
                    checkpoint("fleet-selected", "Fleet")
                    key("open Details", b"\r")
                    checkpoint("details", "Fleet > Details")
                    key("Esc Details", b"\x1b")
                    checkpoint("fleet-return", "Fleet")
                    for page in range(1, 7):
                        key("Fleet PgDn", b"\x1b[6~")
                        checkpoint(f"fleet-scrolled-{page}", "Fleet")
                    for page in range(6):
                        key("Fleet PgUp (no reselection)", b"\x1b[5~")
                    checkpoint("fleet-scroll-return", "Fleet")
                    key("workspace 2", b"2")
                    key("Threads section", b"l")
                    threads = checkpoint("threads", "Threads")
                    context = tuple(threads.splitlines()[1:3])
                    if len(context) != 2 or not context[0].startswith("Master: ") or not context[1].startswith("Principal: "):
                        raise RuntimeError("Threads identity context is missing")
                    key("open reader", b"\r")
                    checkpoint("reader-masked", "Threads > fixture-thread", reader=True, masked=True)
                    key("explicit message selection down", b"\x1b[B")
                    checkpoint("reader-selected", "Threads > fixture-thread", reader=True, masked=True)
                    key("reader PgDn (no reselection)", b"\x1b[6~")
                    checkpoint("reader-pgdown", "Threads > fixture-thread", reader=True)
                    key("reader refresh", b"r")
                    checkpoint("reader-refresh", "Threads > fixture-thread", reader=True)
                    for new_width, new_height in ((40, 20), (80, 24), (120, 40)):
                        drain()
                        if (width, height) != (new_width, new_height):
                            session["segments"][-1]["end_byte"] = len(raw)
                            segment_start = len(raw)
                            width, height = new_width, new_height
                            begin_segment()
                        session["events"].append({"resize": [width, height], "byte_offset": len(raw),
                                                  "segment": len(session["segments"]) - 1})
                        fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack("HHHH", height, width, 0, 0))
                        process.send_signal(signal.SIGWINCH)
                        drain()
                        checkpoint("reader-resize", "Threads > fixture-thread", reader=True)
                        key("resized reader PgDn (no reselection)", b"\x1b[6~")
                        checkpoint("reader-resize-pgdown", "Threads > fixture-thread", reader=True)
                    key("Esc reader", b"\x1b")
                    returned = checkpoint("threads-return", "Threads")
                    if tuple(returned.splitlines()[1:3]) != context or "Design check" not in returned:
                        raise RuntimeError("Esc reader lost Threads identity or retained list")
                    key("reopen reader", b"\r")
                    checkpoint("reader-reopened-masked", "Threads > fixture-thread", reader=True, masked=True)
                    key("Esc reopened reader", b"\x1b")
                    checkpoint("threads-second-return", "Threads")
                    key("Fleet workspace", b"1")
                    checkpoint("fleet-final-return", "Fleet")
                    key("quit without confirmation or sending", b"\x03")
                    process.wait(timeout=5)
                    drain()
                    if process.returncode != 0 or b"--- FAIL:" in raw or b"panic:" in raw or b"PASS" not in raw:
                        raise RuntimeError(f"fixture did not exit cleanly: {process.returncode}")
                    if b"Synthetic" in raw:
                        raise RuntimeError("private synthetic body appeared in session output")
                    session["status"] = "passed"
                finally:
                    if slave is not None:
                        os.close(slave)
                    if process is not None and process.poll() is None:
                        process.terminate()
                        try:
                            process.wait(timeout=3)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait(timeout=3)
                    if process is not None:
                        session["exit_code"] = process.returncode
                    os.close(master)
                    if session["segments"]:
                        session["segments"][-1]["end_byte"] = len(raw)
                    session["raw"] = f"{session_id}-session.ansi"
                    (args.output / session["raw"]).write_bytes(raw)
                    for segment in session["segments"]:
                        name = f"{session_id}-segment-{segment['index']}.ansi"
                        segment["raw"] = name
                        (args.output / name).write_bytes(raw[segment["start_byte"]:segment["end_byte"]])
                    save_manifest()
        manifest["status"] = "passed"
    except BaseException as exc:
        manifest["status"] = "failed"
        manifest["error"] = f"{type(exc).__name__}: {exc}"
        if manifest["sessions"] and manifest["sessions"][-1]["status"] == "running":
            manifest["sessions"][-1]["status"] = "failed"
        raise
    finally:
        save_manifest()
    print(f"Passed {len(manifest['sessions'])} PTY sessions; evidence: {args.output / 'manifest.json'}")


if __name__ == "__main__":
    main()
