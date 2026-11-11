#!/usr/bin/env python3
"""Controlled acceptance: helper behavior and fixture configs against a real sbx.

SBX_SKILL_TEST_BINARY optionally enables real CLI tests; Docker is deliberately
replaced with an unavailable stub. This does not evaluate a live model dialogue.
"""
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import tarfile
import unittest

sys.dont_write_bytecode = True
REPO = Path(__file__).resolve().parents[2]
SKILL = REPO / "sbx_skills/sbx-configure"
spec = importlib.util.spec_from_file_location("knowledge", SKILL / "scripts/knowledge.py")
knowledge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(knowledge)


class KnowledgeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.package = self.root / "skill"
        shutil.copytree(SKILL, self.package, ignore=shutil.ignore_patterns("__pycache__"))
        self.binary = self.root / "sbx"
        self.binary.write_text("#!" + sys.executable + "\nimport json,sys\n"
                               "if sys.argv[1:] == ['version','--json']: "
                               "print(json.dumps({'application_version':'test','schema_version':'v2'}))\n"
                               "else: print('Usage: sbx ' + ' '.join(sys.argv[1:]))\n")
        self.binary.chmod(0o755)
        observed = knowledge.probe_binary(str(self.binary))
        manifest = {"format": "sbx.skill-knowledge/v1", "knowledge_revision": "test",
                    "source_commit": "fixture", "files_sha256": knowledge.inventory(self.package),
                    "application_version": observed["application_version"],
                    "schema_version": observed["schema_version"],
                    "help_sha256": observed["help_sha256"],
                    "builds": [{"binary_sha256": observed["binary_sha256"]}]}
        (self.package / knowledge.MANIFEST).write_text(json.dumps(manifest))

    def invoke(self, *args):
        p = subprocess.run([sys.executable, str(self.package / "scripts/knowledge.py"), *args],
                           capture_output=True, text=True, check=False)
        return p.returncode, json.loads(p.stdout)

    def test_bound_binary_passes(self):
        code, result = self.invoke("probe", "--sbx", str(self.binary))
        self.assertEqual((code, result["status"]), (0, "compatible"))

    def test_skill_links_resolve(self):
        for path in [SKILL / "SKILL.md", *(SKILL / "references").glob("*.md")]:
            for target in re.findall(r"\]\(([^)]+)\)", path.read_text()):
                if "://" not in target and not target.startswith("#"):
                    self.assertTrue((path.parent / target.split("#")[0]).is_file(),
                                    "%s -> %s" % (path.name, target))

    def test_same_version_replaced_binary_fails(self):
        self.binary.write_text(self.binary.read_text() + "\n# replaced\n")
        code, result = self.invoke("probe", "--sbx", str(self.binary))
        self.assertEqual(code, 2)
        self.assertIn("binary_not_bound_to_knowledge", result["reasons"])

    def test_changed_help_fails_even_when_digest_is_bound(self):
        self.binary.write_text(self.binary.read_text().replace("Usage: sbx", "New usage: sbx"))
        path = self.package / knowledge.MANIFEST
        data = json.loads(path.read_text())
        data["builds"][0]["binary_sha256"] = knowledge.file_hash(self.binary)
        path.write_text(json.dumps(data))
        code, result = self.invoke("probe", "--sbx", str(self.binary))
        self.assertEqual(code, 2)
        self.assertIn("command_help_mismatch", result["reasons"])

    def test_modified_reference_and_unlisted_file_fail(self):
        for path in (self.package / "references/config-rules.md", self.package / "references/new.md"):
            with self.subTest(path=path.name):
                original = path.read_text() if path.exists() else None
                path.write_text("changed")
                self.assertEqual(self.invoke("check")[0], 1)
                if original is None:
                    path.unlink()
                else:
                    path.write_text(original)

    def test_missing_binary_is_explicit_error(self):
        code, result = self.invoke("probe", "--sbx", str(self.root / "missing"))
        self.assertEqual((code, result["status"]), (1, "error"))

    def test_malformed_version_is_error(self):
        self.binary.write_text("#!/bin/sh\nprintf not-json\n")
        self.assertEqual(self.invoke("probe", "--sbx", str(self.binary))[0], 1)

    def test_no_unreviewed_seal(self):
        self.assertEqual(self.invoke("seal", "--sbx", str(self.binary), "--repo", str(REPO),
                                     "--revision", "test")[0], 1)

    def test_source_drift_detected(self):
        fake = self.root / "source"
        for directory in knowledge.SOURCE_DIRS:
            (fake / directory).mkdir(parents=True, exist_ok=True)
        for name in ("core/go.mod", "core/go.sum", "core/cmd/sbx/main.go"):
            path = fake / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("fixture")
        path = self.package / knowledge.MANIFEST
        data = json.loads(path.read_text())
        data["source_files_sha256"] = knowledge.source_inventory(fake)
        path.write_text(json.dumps(data))
        self.assertEqual(self.invoke("check", "--repo", str(fake))[0], 0)
        (fake / "core/go.mod").write_text("changed")
        self.assertEqual(self.invoke("check", "--repo", str(fake))[0], 1)


@unittest.skipUnless(os.getenv("SBX_SKILL_TEST_BINARY"), "set SBX_SKILL_TEST_BINARY for real CLI fixtures")
class ConfigFixtures(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.project = self.root / "project"
        self.project.mkdir()
        self.home = self.root / "sbx-home"
        self.home.mkdir()
        subprocess.run(["git", "init", "-q", str(self.project)], check=True)
        self.config = self.project / ".sbx/sandbox.toml"
        self.config.parent.mkdir()
        self.env = dict(os.environ, SBX_HOME=str(self.home))
        self.bin = self.root / "bin"
        self.bin.mkdir()
        docker = self.bin / "docker"
        docker.write_text("#!/bin/sh\nexit 1\n")
        docker.chmod(0o755)
        self.env["PATH"] = str(self.bin) + os.pathsep + self.env.get("PATH", "")
        self.sbx = str(Path(os.environ["SBX_SKILL_TEST_BINARY"]).resolve())

    def show(self):
        return subprocess.run([self.sbx, "config", "show"], cwd=self.project, env=self.env,
                              capture_output=True, text=True, check=False, timeout=20)

    def test_public_go_and_incremental_redis(self):
        # Representative outputs produced by the skill's minimal-patch workflow.
        self.config.write_text('# user comment\ndefault_agent = "codex"\nprofile = "web-go"\n')
        result = self.show()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertRegex(result.stdout, r'default_agent\s+"codex"\s+project')
        original = self.config.read_text()
        service = '\n[[services]]\nname = "redis"\nimage = "redis:7"\nport = 6379\n'
        for _ in range(2):
            current = self.config.read_text()
            if 'name = "redis"' not in current:
                self.config.write_text(current + service)
        self.assertEqual(self.config.read_text(), original + service)
        self.assertEqual(self.show().returncode, 0)

    def test_higher_layer_override_and_union(self):
        (self.home / "config.toml").write_text('[network]\nallow = ["first.example"]\n')
        self.config.write_text('default_agent = "codex"\n[network]\nallow = ["second.example"]\n')
        result = self.show()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("first.example", result.stdout)
        self.assertIn("second.example", result.stdout)
        workspace_line = next(line for line in result.stdout.splitlines() if line.startswith("workspace"))
        # Fixture paths are ASCII without spaces; use the exact CLI-resolved path.
        workspace = Path(workspace_line.split()[1])
        workspace.parent.mkdir(exist_ok=True)
        workspace.write_text('default_agent = "claude"\n')
        result = self.show()
        self.assertRegex(result.stdout, r'default_agent\s+"claude"\s+workspace')

    def test_forward_rejected_in_project_accepted_in_workspace(self):
        forward = '[[forward]]\nname = "dev-db"\nport = 5432\nto = 5432\n'
        self.config.write_text(forward)
        result = self.show()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("forward", result.stderr)
        self.config.write_text("")
        result = self.show()
        line = next(line for line in result.stdout.splitlines() if line.startswith("workspace"))
        workspace = Path(line.split()[1])
        workspace.parent.mkdir(exist_ok=True)
        workspace.write_text(forward)
        result = self.show()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("forward.dev-db", result.stdout)

    def test_private_secret_mapping_and_readiness(self):
        self.config.write_text('[prep]\nwatch = ["go.mod", "go.sum"]\nsecrets = ["netrc"]\n')
        self.assertNotEqual(self.show().returncode, 0)
        # Dummy file is not a real credential; CLI loading does not build prep.
        secret = self.root / "fixture.netrc"
        secret.write_text("fixture only")
        (self.home / "config.toml").write_text('[prep.secret]\nnetrc = ' + json.dumps(str(secret)) + '\n')
        result = self.show()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("prep.secrets", result.stdout)
        self.assertIn("note:", result.stderr)  # Docker/prep declaration verification omitted.

    def test_monorepo_config_found_from_child(self):
        self.config.write_text('default_agent = "codex"\n[deps]\nmask = ["apps/api/node_modules"]\n')
        child = self.project / "apps/api"
        child.mkdir(parents=True)
        self.project = child
        result = self.show()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("apps/api/node_modules", result.stdout)
        self.assertRegex(result.stdout, r'default_agent\s+"codex"\s+project')

    def test_unknown_fields_are_warnings(self):
        self.config.write_text('made_up_field = "ignored"\n')
        result = self.show()
        self.assertEqual(result.returncode, 0)
        self.assertIn("unknown field", result.stderr)

    def test_release_package_round_trip_and_no_overwrite(self):
        # Fixture archives contain the native binary under all four names. This
        # tests binding/packaging only, not cross-platform execution correctness.
        version = knowledge.probe_binary(self.sbx)["application_version"]
        archives = self.root / "dist"
        archives.mkdir()
        for platform in ("darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"):
            with tarfile.open(archives / ("sbx_%s_%s.tar.gz" % (version, platform)), "w:gz") as tar:
                tar.add(self.sbx, arcname="sbx")
        output = self.root / "skill.tar.gz"
        command = [sys.executable, str(REPO / "core/scripts/package-configure-skill.py"),
                   "--sbx", self.sbx, "--release-dir", str(archives),
                   "--version", version, "--output", str(output)]
        result = subprocess.run(command, capture_output=True, text=True, check=False, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(output) as tar:
            manifest = json.load(tar.extractfile("sbx-configure/knowledge-manifest.json"))
            self.assertEqual(len(manifest["builds"]), 5)
            self.assertEqual(manifest["application_version"], version)
            self.assertEqual(manifest["builds"][0]["execution_checked"], True)
            self.assertTrue(all(not item["execution_checked"] for item in manifest["builds"][1:]))
            self.assertIn("sbx-configure/SKILL.md", tar.getnames())
        previous = knowledge.file_hash(output)
        result = subprocess.run(command, capture_output=True, text=True, check=False, timeout=30)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(knowledge.file_hash(output), previous)


if __name__ == "__main__":
    unittest.main(verbosity=2)
