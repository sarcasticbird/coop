"""Stage or verify a versioned Coop candidate. No install or service operations.

Run through Flox. The private output directory must be absent and Git-ignored.
Dirty previews require --allow-dirty and cannot pass the acceptance verifier.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tarfile

sys.dont_write_bytecode = True

from scheduler_artifacts import git, regular_bytes, check_binary, canonical, unique_object

SCHEMA = "coop-consolidation-candidate-v1"
TARGETS = {"coop": "darwin/arm64", "coop-worker": "linux/arm64", "coop-github": "linux/arm64"}


def source_snapshot(root, head):
    if not re.fullmatch(r"[0-9a-f]{40}", head) or git(root, "rev-parse", "HEAD").decode().strip() != head:
        raise ValueError("exact source HEAD mismatch")
    if Path(git(root, "rev-parse", "--show-toplevel").decode().strip()).resolve() != root:
        raise ValueError("source must be the checkout root")
    entries = git(root, "ls-files", "-v", "-z").split(b"\0")
    if any(entry[:1].islower() or entry[:1] == b"S" for entry in entries):
        raise ValueError("hidden index changes refused")
    names = sorted(set(git(root, "ls-files", "--cached", "--others", "--exclude-standard", "-z").decode().strip("\0").split("\0")))
    inventory = {}
    for name in names:
        path = root / name
        if not path.exists() and not path.is_symlink():
            inventory[name] = {"deleted": True}
            continue
        data = regular_bytes(root, name)
        inventory[name] = {"sha256": hashlib.sha256(data).hexdigest(), "mode": stat.S_IMODE(path.stat().st_mode)}
    dirty = bool(git(root, "status", "--porcelain=v1", "--untracked-files=all"))
    return {"source_revision": head, "source_modified": dirty,
            "source_tree_sha256": hashlib.sha256(canonical(inventory)).hexdigest(), "files": inventory}


def write_json(path, value):
    with path.open("xb") as stream:
        stream.write(canonical(value))


def stage(root, head, output, allow_dirty=False):
    before = source_snapshot(root, head)
    if before["source_modified"] and not allow_dirty:
        raise ValueError("dirty source requires explicit --allow-dirty preview")
    if not output.is_absolute() or output != Path(os.path.abspath(output)) or output.exists() or output.is_symlink():
        raise ValueError("candidate output must be an absent absolute path")
    try:
        relative = output.relative_to(root)
    except ValueError as exc:
        raise ValueError("candidate must be inside the ignored checkout bin directory") from exc
    if relative.parts[0] != "bin":
        raise ValueError("candidate must be inside ignored bin/")
    git(root, "check-ignore", str(output))
    # Parent symlinks must not redirect private staging into another profile.
    parent = output.parent
    while parent != root:
        if parent.is_symlink():
            raise ValueError("symlink candidate parent refused")
        parent = parent.parent
    output.mkdir(mode=0o700, parents=True)
    artifacts = output / "artifacts"
    artifacts.mkdir(mode=0o700)
    profile = output / "config" / "coop"
    profile.mkdir(mode=0o700, parents=True)
    write_json(output / "source.json", before)
    with tarfile.open(output / "source.tar", "w") as archive:
        for name, details in before["files"].items():
            if not details.get("deleted"):
                archive.add(root / name, arcname=name, recursive=False)
    env = dict(os.environ, GOFLAGS="", GOWORK="off", CGO_ENABLED="0")
    stamp = (f"-X main.sourceRevision={head} -X main.sourceModified={str(before['source_modified']).lower()} "
             f"-X main.sourceTreeSHA256={before['source_tree_sha256']}")
    hashes = {}
    for name, target in TARGETS.items():
        goos, goarch = target.split("/")
        build_env = dict(env, GOOS=goos, GOARCH=goarch)
        args = ["go", "build", "-trimpath", "-buildvcs=true"]
        if name == "coop":
            args += ["-ldflags", stamp]
        subprocess.run(args + ["-o", str(artifacts / name), "./cmd/" + name], cwd=root, env=build_env, check=True)
        data = regular_bytes(artifacts, name)
        check_binary(data, target)
        hashes[name] = {"sha256": hashlib.sha256(data).hexdigest(), "target": target}
    if source_snapshot(root, head) != before:
        raise ValueError("source changed during build; staged files remain unaccepted")
    manifest = {"schema": SCHEMA, **{k: before[k] for k in ("source_revision", "source_modified", "source_tree_sha256")},
                "artifact_sha256": hashes["coop"]["sha256"], "profile": str(profile), "artifacts": hashes,
                "source_archive_sha256": hashlib.sha256((output / "source.tar").read_bytes()).hexdigest(),
                "acceptance": "dirty-preview" if before["source_modified"] else "pending-review-and-operator-uat"}
    write_json(profile / "candidate.json", manifest)
    launcher = output / "coop-preview"
    launcher.write_text('#!/bin/sh\nset -eu\nbase=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)\nexport XDG_CONFIG_HOME="$base/config"\nexec "$base/artifacts/coop" "$@"\n')
    launcher.chmod(0o700)
    return manifest


def verify(root, head, output, require_clean=True):
    profile = output / "config" / "coop"
    if not output.is_absolute() or output != Path(os.path.abspath(output)) or not output.is_relative_to(root / "bin"):
        raise ValueError("candidate must be an absolute ignored bin path")
    parent = profile
    while parent != root:
        if parent.is_symlink():
            raise ValueError("symlink candidate parent refused")
        parent = parent.parent
    manifest = json.loads(regular_bytes(output, "config/coop/candidate.json"), object_pairs_hook=unique_object)
    snapshot = source_snapshot(root, head)
    if manifest.get("schema") != SCHEMA or manifest.get("profile") != str(profile):
        raise ValueError("candidate profile/schema mismatch")
    for field in ("source_revision", "source_modified", "source_tree_sha256"):
        if manifest.get(field) != snapshot[field]:
            raise ValueError("candidate source mismatch: " + field)
    if require_clean and manifest["source_modified"]:
        raise ValueError("dirty preview is unverified and cannot be installed")
    inventory = json.loads(regular_bytes(output, "source.json"), object_pairs_hook=unique_object)
    if inventory != snapshot:
        raise ValueError("source inventory mismatch")
    if hashlib.sha256(regular_bytes(output,"source.tar")).hexdigest() != manifest.get("source_archive_sha256"):
        raise ValueError("source archive mismatch")
    if set(manifest.get("artifacts", {})) != set(TARGETS):
        raise ValueError("candidate artifact inventory mismatch")
    for name, target in TARGETS.items():
        data = regular_bytes(output / "artifacts", name)
        check_binary(data, target)
        if manifest["artifacts"][name] != {"sha256": hashlib.sha256(data).hexdigest(), "target": target}:
            raise ValueError("candidate artifact mismatch: " + name)
    if manifest["artifact_sha256"] != manifest["artifacts"]["coop"]["sha256"]:
        raise ValueError("entry artifact mismatch")
    return manifest


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("stage", "verify"))
    parser.add_argument("--source-head", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--allow-dirty", action="store_true", help="stage or inspect a dirty preview; never accepts it")
    args = parser.parse_args()
    root = Path.cwd().resolve()
    if args.operation == "stage":
        manifest = stage(root, args.source_head, args.output, args.allow_dirty)
    else:
        manifest = verify(root, args.source_head, args.output, not args.allow_dirty)
    # Structured evidence contains no configuration contents or tokens.
    json.dump(manifest, sys.stdout, sort_keys=True, indent=2)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
