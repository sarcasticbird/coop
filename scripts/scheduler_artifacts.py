"""Offline scheduler artifact inventory. Uses only Python stdlib and local Git.

Usage (keep outputs outside the source checkout):
  python3 scripts/scheduler_artifacts.py create --source-head FULL_SHA \
      --artifacts /path/to/binaries --manifest /path/to/manifest.json
  python3 scripts/scheduler_artifacts.py verify --source-head FULL_SHA \
      --artifacts /path/to/binaries --manifest /path/to/manifest.json

The artifact directory must contain exactly coop, coop-worker and coop-github.
All committed repository example profiles are required; no private profile or
credential paths are accepted. Digests bind supplied bytes, not build provenance
or a signature. No binary is executed and no installation is performed.
"""

import argparse
import hashlib
import json
from pathlib import Path
import re
import stat
import struct
import subprocess
import sys


REPAIR_ANCESTOR = "d7e86225666ad9824db3f35903de19c556a83557"
INTEGRATION_ANCESTOR = "ac6353199e02b3bbca7da0256eaf6fa5567044c6"
BINARIES = {"coop": "darwin/arm64", "coop-worker": "linux/arm64", "coop-github": "linux/arm64"}
PROFILES = {"examples/worker.json": 1, "examples/worker-team.json": 2,
            "examples/worker-master.json": 3}
REQUIRED_LICENSES = ("modernc.org/sqlite", "modernc.org/libc", "modernc.org/memory",
                     "modernc.org/mathutil", "github.com/dustin/go-humanize",
                     "github.com/google/uuid", "github.com/ncruces/go-strftime",
                     "github.com/remyoudompheng/bigfft")
SOURCES = (
    "scripts/scheduler_artifacts.py",
    "scripts/install-agent-worker.sh",
    "scripts/install_agent_worker.py",
    "scripts/install-github-auth.sh",
    "scripts/coop-opencode-launch",
    "scripts/coop-opencode-server",
    "scripts/coop-opencode-attach",
    "examples/master.toml",
    "examples/master-client.toml",
    "THIRD_PARTY_NOTICES.md",
    *PROFILES,
)


def git(root, *args):
    result = subprocess.run(
        ["git", "--no-optional-locks", "-C", str(root), *args],
        capture_output=True,
    )
    if result.returncode:
        raise ValueError("local Git check failed: " + args[0])
    return result.stdout


def source_identity(root, head):
    if not re.fullmatch(r"[0-9a-f]{40}", head):
        raise ValueError("source head must be a full lowercase commit SHA")
    if Path(git(root, "rev-parse", "--show-toplevel").decode().strip()).resolve() != root:
        raise ValueError("source root must be the checkout root")
    if git(root, "rev-parse", "HEAD").decode().strip() != head:
        raise ValueError("source head mismatch")
    # These index flags can hide modified tracked files from status anywhere in
    # the checkout. -v lowercases assume-unchanged tags; S marks skip-worktree.
    entries = git(root, "ls-files", "-v", "-z").split(b"\0")
    if any(entry[:1].islower() or entry[:1] == b"S" for entry in entries):
        raise ValueError("source index must not use assume-unchanged or skip-worktree")
    if git(root, "status", "--porcelain=v1", "--untracked-files=all"):
        raise ValueError("source checkout must be clean (including untracked files)")
    for ancestor in (REPAIR_ANCESTOR, INTEGRATION_ANCESTOR):
        git(root, "merge-base", "--is-ancestor", ancestor, head)


def regular_bytes(root, relative):
    path = root
    for part in Path(relative).parts:
        path = path / part
        if path.is_symlink():
            raise ValueError("symlink input refused: " + relative)
    if not stat.S_ISREG(path.stat().st_mode):
        raise ValueError("regular file required: " + relative)
    return path.read_bytes()


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def check_binary(data, target):
    if target == "darwin/arm64":
        # Thin little-endian Mach-O 64, ARM64, executable. Fat binaries fail closed.
        valid = len(data) >= 32 and struct.unpack_from("<I", data)[0] == 0xFEEDFACF
        valid = valid and struct.unpack_from("<I", data, 4)[0] == 0x0100000C
        valid = valid and struct.unpack_from("<I", data, 12)[0] == 2
    else:
        # ELF64 little-endian AArch64, executable or PIE/shared ELF (ET_DYN).
        valid = len(data) >= 64 and data[:7] == b"\x7fELF\x02\x01\x01"
        valid = valid and struct.unpack_from("<HHI", data, 16) in ((2, 183, 1), (3, 183, 1))
    if not valid:
        raise ValueError("incorrect binary architecture/format: " + target)


def canonical(value):
    return (json.dumps(value, sort_keys=True, indent=2, ensure_ascii=True) + "\n").encode()


def manifest_bytes(source, artifacts, head):
    source, artifacts = Path(source).resolve(), Path(artifacts).resolve()
    source_identity(source, head)
    if {entry.name for entry in artifacts.iterdir()} != set(BINARIES):
        raise ValueError("artifact directory must contain exactly coop, coop-worker and coop-github")
    records = []
    for relative in sorted(SOURCES):
        data = regular_bytes(source, relative)
        if data != git(source, "show", head + ":" + relative):
            raise ValueError("source bytes differ from committed head: " + relative)
        if relative in PROFILES:
            profile = json.loads(data, object_pairs_hook=unique_object)
            if not isinstance(profile, dict) or type(profile.get("version")) is not int or profile["version"] != PROFILES[relative]:
                raise ValueError("incorrect example profile version: " + relative)
        if relative == "THIRD_PARTY_NOTICES.md" and any(name.encode() not in data for name in REQUIRED_LICENSES):
            raise ValueError("missing SQLite runtime dependency notices")
        records.append({"path": "source/" + relative, "sha256": hashlib.sha256(data).hexdigest(), "size": len(data)})
    for name, target in sorted(BINARIES.items()):
        data = regular_bytes(artifacts, name)
        check_binary(data, target)
        records.append({"path": "artifacts/" + name, "target": target,
                        "sha256": hashlib.sha256(data).hexdigest(), "size": len(data)})
    # Catch ordinary concurrent checkout changes before publishing evidence.
    source_identity(source, head)
    return canonical({"version": 1, "source_head": head,
                      "worker_profile_schemas": [1, 2, 3],
                      "native_authority": "master/v1",
                      "repair_ancestor": REPAIR_ANCESTOR,
                      "integration_ancestor": INTEGRATION_ANCESTOR,
                      "files": sorted(records, key=lambda row: row["path"])})


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("action", choices=("create", "verify"))
    parser.add_argument("--source-root", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--source-head", required=True)
    parser.add_argument("--artifacts", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        # Do not overwrite inputs or put generated evidence into the clean source.
        destination = args.manifest.absolute()
        if destination.is_symlink():
            raise ValueError("manifest symlink refused")
        destination = destination.resolve()
        for root in (args.source_root.resolve(), args.artifacts.resolve()):
            if destination == root or root in destination.parents:
                raise ValueError("manifest must be outside source and artifact directories")
        expected = manifest_bytes(args.source_root, args.artifacts, args.source_head)
        if args.action == "verify":
            actual = regular_bytes(destination.parent, destination.name)
            # Parse explicitly to reject duplicate object keys; byte equality also
            # rejects duplicate/missing/extra records and noncanonical encodings.
            json.loads(actual, object_pairs_hook=unique_object)
            if actual != expected:
                raise ValueError("manifest does not match exact source, profiles and artifacts")
        else:
            # A failed validation never truncates an existing manifest.
            with destination.open("xb") as stream:
                stream.write(expected)
    except (ValueError, OSError, subprocess.SubprocessError):
        # Never include input contents, Git output or absolute paths in diagnostics.
        print("artifact manifest refused: check clean head, repair ancestry, required inputs, architectures and digests", file=sys.stderr)
        return 1
    print("artifact manifest " + ("created" if args.action == "create" else "verified"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
