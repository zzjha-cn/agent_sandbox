#!/usr/bin/env python3
"""Read-only runtime probes and explicit maintainer sealing for sbx skill knowledge.

Python 3.9+, standard library only. No Docker, network, user config, or project reads
in probe. Exit 0: compatible; 2: incompatible; 1: error/incomplete package.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = "knowledge-manifest.json"
COMMANDS = [[], ["config", "show"], ["config", "init"], ["prep", "build"],
            ["prep", "show"], ["prep", "reseed"], ["forward", "init"],
            ["run"], ["shell"], ["path"], ["trust"], ["schema"], ["version"]]
SOURCE_DIRS = ["core/internal", "core/assets"]


def digest(data):
    return hashlib.sha256(data).hexdigest()


def file_hash(path):
    h = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def run(argv, cwd=None):
    result = subprocess.run(argv, cwd=cwd, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=20, check=False)
    if result.returncode:
        # Avoid printing arbitrary stderr or config values from an executable.
        raise ValueError("command failed (exit %s): %s" % (result.returncode, argv[0]))
    if len(result.stdout) > 2 * 1024 * 1024:
        raise ValueError("probe output exceeds 2 MiB")
    return result.stdout


def executable(value):
    found = shutil.which(value)
    if not found:
        raise ValueError("sbx executable not found")
    return Path(found).resolve()


def probe_binary(value):
    binary = executable(value)
    before = file_hash(binary)
    version = json.loads(run([str(binary), "version", "--json"]))
    if not isinstance(version, dict) or not isinstance(version.get("application_version"), str):
        raise ValueError("version --json has no application_version")
    help_hashes = {}
    for command in COMMANDS:
        help_hashes[" ".join(command) or "root"] = digest(
            run([str(binary), *command, "--help"]))
    if file_hash(binary) != before:
        raise ValueError("binary changed during probe; retry against stable executable")
    return {"binary_sha256": before, "application_version": version["application_version"],
            "schema_version": version.get("schema_version"), "help_sha256": help_hashes}


def inventory(root):
    paths = [root / "SKILL.md"]
    for name in ("agents", "references", "scripts"):
        paths.extend(p for p in (root / name).rglob("*")
                     if p.is_file() and "__pycache__" not in p.parts and p.suffix != ".pyc")
    return {str(p.relative_to(root)): file_hash(p) for p in sorted(paths)}


def source_inventory(repo):
    paths = []
    for name in SOURCE_DIRS:
        paths.extend(p for p in (repo / name).rglob("*")
                     if p.is_file() and not p.name.endswith("_test.go")
                     and "testdata" not in p.parts
                     and (name == "core/assets" or p.suffix == ".go"))
    paths.extend(repo / name for name in ("core/go.mod", "core/go.sum", "core/cmd/sbx/main.go"))
    return {str(p.relative_to(repo)): file_hash(p) for p in sorted(paths)}


def verify(root=ROOT, repo=None):
    manifest = json.loads((root / MANIFEST).read_text())
    if not isinstance(manifest, dict) or manifest.get("format") != "sbx.skill-knowledge/v1":
        raise ValueError("unsupported knowledge manifest")
    if inventory(root) != manifest["files_sha256"]:
        raise ValueError("knowledge files changed; maintainer review and reseal required")
    if repo is not None and source_inventory(repo) != manifest["source_files_sha256"]:
        raise ValueError("source knowledge dependencies changed; review references before resealing")
    if not manifest.get("builds"):
        raise ValueError("knowledge package has no bound binaries")
    return manifest


def probe(args):
    manifest = verify()
    observed = probe_binary(args.sbx)
    reasons = []
    matches = [b for b in manifest["builds"]
               if b["binary_sha256"] == observed["binary_sha256"]]
    if not matches:
        reasons.append("binary_not_bound_to_knowledge")
    if observed["application_version"] != manifest["application_version"]:
        reasons.append("application_version_mismatch")
    if observed["schema_version"] != manifest["schema_version"]:
        reasons.append("protocol_version_mismatch")
    if observed["help_sha256"] != manifest["help_sha256"]:
        reasons.append("command_help_mismatch")
    return {"status": "compatible" if not reasons else "incompatible",
            "reasons": reasons, "observed": observed,
            "knowledge_revision": manifest["knowledge_revision"],
            "source_commit": manifest["source_commit"]}, 2 if reasons else 0


def seal(args):
    if not args.reviewed:
        raise ValueError("seal is for maintainers after semantic review; pass --reviewed")
    repo = args.repo.resolve()
    commit = run(["git", "rev-parse", "HEAD"], repo).decode().strip()
    # Disallow blessing references against changed runtime semantics.
    if run(["git", "diff", "HEAD", "--", "core"], repo):
        raise ValueError("tracked core changes must be committed before sealing")
    observed = probe_binary(args.sbx)
    builds = [{"binary_sha256": observed["binary_sha256"], "execution_checked": True,
               "execution_scope": ["version", "help"]}]
    if args.release_dir:
        for archive in sorted(args.release_dir.glob("sbx_*.tar.gz")):
            with tarfile.open(archive, "r:gz") as tar:
                members = [m for m in tar.getmembers() if m.name in ("sbx", "./sbx") and m.isfile()]
                if len(members) != 1:
                    raise ValueError("release archive must contain exactly one sbx binary")
                with tar.extractfile(members[0]) as stream:
                    h = hashlib.sha256()
                    for block in iter(lambda: stream.read(1024 * 1024), b""):
                        h.update(block)
                builds.append({"binary_sha256": h.hexdigest(), "archive": archive.name,
                               "execution_checked": False})
        if len(builds) != 5:
            raise ValueError("expected four cross-platform sbx archives")
    manifest = {"format": "sbx.skill-knowledge/v1", "knowledge_revision": args.revision,
                "source_commit": commit, "reviewed_at": datetime.now(timezone.utc).isoformat(),
                "application_version": observed["application_version"],
                "schema_version": observed["schema_version"], "help_sha256": observed["help_sha256"],
                "builds": builds, "files_sha256": inventory(ROOT),
                "source_files_sha256": source_inventory(repo),
                "validation": {"automated": "see references/acceptance.md",
                               "real_docker": "not_executed", "real_private_network": "not_executed"}}
    (ROOT / MANIFEST).write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
    return {"status": "sealed", "source_commit": commit, "builds": len(builds)}, 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("probe").add_argument("--sbx", default="sbx")
    check = commands.add_parser("check")
    check.add_argument("--repo", type=Path)
    maint = commands.add_parser("seal", help="maintainer-only; never an automatic runtime fallback")
    maint.add_argument("--sbx", required=True)
    maint.add_argument("--repo", required=True, type=Path)
    maint.add_argument("--revision", required=True)
    maint.add_argument("--reviewed", action="store_true")
    maint.add_argument("--release-dir", type=Path)
    args = parser.parse_args()
    try:
        if args.command == "probe":
            result, status = probe(args)
        elif args.command == "seal":
            result, status = seal(args)
        else:
            manifest = verify(repo=args.repo.resolve() if args.repo else None)
            result, status = {"status": "valid", "knowledge_revision": manifest["knowledge_revision"]}, 0
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError, tarfile.TarError):
        # Details stay generic to prevent subprocess/private source leakage.
        result, status = {"status": "error", "reason": "probe_or_package_invalid",
                          "action": "check executable, package integrity and maintainer prerequisites"}, 1
    print(json.dumps(result, ensure_ascii=False))
    return status


if __name__ == "__main__":
    sys.exit(main())
