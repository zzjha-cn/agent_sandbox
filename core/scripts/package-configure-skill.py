#!/usr/bin/env python3
"""Package reviewed skill knowledge alongside four sbx release archives."""
import argparse
import importlib.util
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile

sys.dont_write_bytecode = True
REPO = Path(__file__).resolve().parents[2]
SKILL = REPO / "sbx_skills/sbx-configure"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sbx", required=True, type=Path)
    parser.add_argument("--release-dir", required=True, type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]*", args.version):
        parser.error("invalid version")
    spec = importlib.util.spec_from_file_location("knowledge", SKILL / "scripts/knowledge.py")
    knowledge = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(knowledge)
    try:
        template = knowledge.verify(repo=REPO)
        observed = knowledge.probe_binary(str(args.sbx.resolve()))
        if observed["application_version"] != args.version:
            parser.error("native binary version does not match requested release")
        # Version labels may change, but reviewed CLI semantics must still match.
        if observed["help_sha256"] != template["help_sha256"]:
            parser.error("CLI help drift; review and reseal source knowledge first")
        if observed["schema_version"] != template["schema_version"]:
            parser.error("protocol drift; review and reseal source knowledge first")
        expected = {"sbx_%s_%s.tar.gz" % (args.version, platform) for platform in
                    ("darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64")}
        found = {p.name for p in args.release_dir.glob("sbx_*.tar.gz")}
        if found != expected:
            parser.error("release archives do not match the four expected platform/version names")
        output = args.output.resolve()
        if output.exists():
            parser.error("output already exists; choose a new path")
        output.parent.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory() as directory:
            package = Path(directory) / "sbx-configure"
            shutil.copytree(SKILL, package, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
            subprocess.run([sys.executable, str(package / "scripts/knowledge.py"), "seal",
                            "--sbx", str(args.sbx.resolve()), "--repo", str(REPO),
                            "--revision", template["knowledge_revision"], "--reviewed",
                            "--release-dir", str(args.release_dir.resolve())], check=True)
            subprocess.run([sys.executable, str(package / "scripts/knowledge.py"), "check",
                            "--repo", str(REPO)], check=True)
            subprocess.run([sys.executable, str(package / "scripts/knowledge.py"), "probe",
                            "--sbx", str(args.sbx.resolve())], check=True, stdout=subprocess.DEVNULL)
            # Exclusive create: never overwrite an existing deliverable.
            with output.open("xb") as stream:
                with tarfile.open(fileobj=stream, mode="w:gz") as archive:
                    archive.add(package, arcname="sbx-configure")
        print(json.dumps({"status": "packaged", "output": str(output),
                          "cross_platform_execution": "not_checked"}))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError, tarfile.TarError) as error:
        parser.exit(1, "skill packaging failed: %s\n" % type(error).__name__)


if __name__ == "__main__":
    main()
