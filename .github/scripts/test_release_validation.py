"""Offline unit tests for release guards; these do not sign or publish anything."""
import base64
import copy
import importlib.util
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("release_checks", pathlib.Path(__file__).with_name("release-validation.py"))
checks = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checks)
VERSION = "0.1.0-alpha.1"
COMMIT = "a" * 40


class ReleaseGuards(unittest.TestCase):
    def setUp(self):
        self.env = {"GITHUB_REPOSITORY": checks.REPOSITORY, "GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_REF": "refs/heads/main", "GITHUB_SHA": COMMIT, "GITHUB_WORKFLOW_REF": checks.REPOSITORY + "/" + checks.WORKFLOW + "@refs/heads/main"}
        self.repo = {"full_name": checks.REPOSITORY, "private": False, "default_branch": "main"}
        self.ref = {"object": {"type": "commit", "sha": COMMIT}}
        self.run = {"id": 10, "head_sha": COMMIT, "head_branch": "main", "event": "push", "name": "Cross-platform CI", "path": ".github/workflows/ci.yml", "status": "completed", "conclusion": "success", "html_url": "https://github.com/" + checks.REPOSITORY + "/actions/runs/10"}
        self.runs = [self.run]
        self.tag = None
        self.release = None
        self.mock = patch.object(checks, "api", side_effect=self.api)
        self.mock.start()
        self.addCleanup(self.mock.stop)

    def api(self, path, missing=False):
        if not path:
            return self.repo
        if path == "git/ref/heads/main":
            return self.ref
        if path.startswith("actions/workflows/"):
            return {"workflow_runs": self.runs}
        if path.startswith("git/ref/tags/"):
            return self.tag
        if path.startswith("releases/tags/"):
            return self.release
        self.fail("unexpected API path: " + path)

    def gate(self):
        return checks.source_gate(VERSION, COMMIT, self.env)

    def draft(self):
        self.tag = copy.deepcopy(self.ref)
        self.release = {"tag_name": "v" + VERSION, "target_commitish": COMMIT, "prerelease": True, "draft": True, "body": "<!-- tsnet-bridge-source:" + COMMIT + " -->", "assets": []}

    def test_accept_exact_main_and_successful_ci(self):
        self.assertEqual(self.gate()["id"], 10)

    def test_valid_prerelease_versions(self):
        for version in (VERSION, "1.2.3-rc.0", "10.20.30-beta-1"):
            checks.validate_inputs(version, COMMIT)

    def test_reject_stable_and_malformed_inputs(self):
        for version in ("0.1.0", "v0.1.0-alpha.1", "0.1.0-01", "01.2.3-alpha", "0.1.0-alpha+build", "0.1.0-alpha\n", "0.1.0-foo/evil", "0.1.0-$(id)", "0.1.0-"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                checks.validate_inputs(version, COMMIT)
        for commit in ("a" * 39, "A" * 40, COMMIT + "\n", "main", "$(id)"):
            with self.subTest(commit=commit), self.assertRaises(ValueError):
                checks.validate_inputs(VERSION, commit)

    def test_reject_wrong_workflow_execution(self):
        for key, value in (("GITHUB_REPOSITORY", "example/fork"), ("GITHUB_EVENT_NAME", "push"), ("GITHUB_REF", "refs/tags/v" + VERSION), ("GITHUB_SHA", "b" * 40), ("GITHUB_WORKFLOW_REF", "another/workflow@refs/heads/main")):
            with self.subTest(key=key), patch.dict(self.env, {key: value}), self.assertRaises(ValueError):
                self.gate()

    def test_reject_private_repo_and_wrong_default_branch(self):
        for key, value in (("private", True), ("default_branch", "other"), ("full_name", "example/fork")):
            with self.subTest(key=key), patch.dict(self.repo, {key: value}), self.assertRaises(ValueError):
                self.gate()

    def test_reject_moved_main(self):
        self.ref["object"]["sha"] = "b" * 40
        with self.assertRaises(ValueError):
            self.gate()

    def test_reject_missing_ci(self):
        self.runs = []
        with self.assertRaises(ValueError):
            self.gate()

    def test_latest_ci_must_be_successful(self):
        newer = copy.deepcopy(self.run)
        newer.update(id=11, status="in_progress", conclusion=None)
        self.runs.append(newer)
        with self.assertRaises(ValueError):
            self.gate()
        newer.update(status="completed", conclusion="failure")
        with self.assertRaises(ValueError):
            self.gate()

    def test_reject_wrong_ci_identity(self):
        for key, value in (("head_sha", "b" * 40), ("head_branch", "other"), ("event", "pull_request"), ("name", "Other CI"), ("path", ".github/workflows/other.yml")):
            with self.subTest(key=key), patch.dict(self.run, {key: value}), self.assertRaises(ValueError):
                self.gate()

    def test_new_release_and_same_commit_draft_retry(self):
        self.assertEqual(checks.release_state(VERSION, COMMIT), (None, None))
        self.draft()
        self.assertEqual(checks.release_state(VERSION, COMMIT, required=True)[1], self.release)

    def test_main_commitish_metadata_requires_exact_immutable_tag(self):
        self.draft()
        self.release["target_commitish"] = "main"
        checks.release_state(VERSION, COMMIT, required=True)
        self.tag["object"]["sha"] = "b" * 40
        with self.assertRaises(ValueError):
            checks.release_state(VERSION, COMMIT, required=True)

    def test_never_resume_published_release(self):
        self.draft()
        self.release["draft"] = False
        with self.assertRaises(ValueError):
            checks.release_state(VERSION, COMMIT)
        checks.release_state(VERSION, COMMIT, required=True, published=True)

    def test_reject_moved_or_annotated_tag(self):
        self.draft()
        for key, value in (("sha", "b" * 40), ("type", "tag")):
            with self.subTest(key=key), patch.dict(self.tag["object"], {key: value}), self.assertRaises(ValueError):
                checks.release_state(VERSION, COMMIT)

    def test_reject_wrong_draft_identity_or_source(self):
        self.draft()
        for key, value in (("target_commitish", "unreviewed-branch"), ("tag_name", "v9.9.9-rc.1"), ("prerelease", False), ("body", "missing source marker")):
            with self.subTest(key=key), patch.dict(self.release, {key: value}), self.assertRaises(ValueError):
                checks.release_state(VERSION, COMMIT)

    def test_reject_missing_required_release_or_tag(self):
        with self.assertRaises(ValueError):
            checks.release_state(VERSION, COMMIT, required=True)
        self.draft()
        self.tag = None
        with self.assertRaises(ValueError):
            checks.release_state(VERSION, COMMIT)

    def test_reject_unknown_or_duplicate_staged_assets(self):
        self.draft()
        for assets in ([{"name": "unexpected.txt"}], [{"name": "SHA256SUMS"}] * 2):
            with self.subTest(assets=assets), patch.dict(self.release, {"assets": assets}), self.assertRaises(ValueError):
                checks.release_state(VERSION, COMMIT)


class ArtifactGuards(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = pathlib.Path(temp.name)
        for name in checks.filenames(VERSION):
            (self.root / name).write_bytes(b"fixture content\n")
        for target in checks.TARGETS:
            stem = "tsnet-bridge-" + VERSION + "-" + target
            self.write(stem + ".build.json", {"project": checks.PROJECT, "version": VERSION, "source_commit": COMMIT, "target": target, "go_version": "go1.27.1", "cgo_enabled": False, "trimpath": True, "buildvcs": False})
            self.write(stem + ".cdx.json", {"bomFormat": "CycloneDX", "specVersion": "1.5"})
            self.write(stem + ".notices.json", {"modules": [{"notices": [{"path": "licenses/example/LICENSE"}]}], "go_standard_library": {"notices": [{"path": "licenses/go/LICENSE"}]}})
        self.checksums()

    def write(self, name, value):
        (self.root / name).write_text(json.dumps(value))

    def checksums(self):
        names = checks.filenames(VERSION) - {"SHA256SUMS"}
        (self.root / "SHA256SUMS").write_text("".join(checks.sha256(self.root / name) + "  " + name + "\n" for name in sorted(names)))

    def check(self):
        checks.check_assets(self.root, VERSION, COMMIT)

    def test_complete_five_target_inventory(self):
        self.assertEqual(len(checks.filenames(VERSION)), 22)
        self.check()

    def test_unexpected_asset_rejected(self):
        (self.root / "unintended.txt").write_text("do not publish")
        with self.assertRaises(ValueError):
            self.check()

    def test_missing_target_rejected(self):
        next(self.root.glob("*windows*.zip")).unlink()
        with self.assertRaises(ValueError):
            self.check()

    def test_modified_bytes_rejected(self):
        next(self.root.glob("*.tar.gz")).write_bytes(b"modified")
        with self.assertRaises(ValueError):
            self.check()

    def test_wrong_source_rejected_even_with_valid_checksums(self):
        file = next(self.root.glob("*.build.json"))
        meta = json.loads(file.read_text())
        meta["source_commit"] = "b" * 40
        self.write(file.name, meta)
        self.checksums()
        with self.assertRaises(ValueError):
            self.check()

    def test_missing_license_notices_rejected(self):
        file = next(self.root.glob("*.notices.json"))
        notices = json.loads(file.read_text())
        notices["go_standard_library"]["notices"] = []
        self.write(file.name, notices)
        self.checksums()
        with self.assertRaises(ValueError):
            self.check()

    def test_duplicate_or_incomplete_checksums_rejected(self):
        file = self.root / "SHA256SUMS"
        original = file.read_text()
        for content in (original + original.splitlines()[0] + "\n", "\n".join(original.splitlines()[:-1]) + "\n"):
            with self.subTest(content=content[:70]):
                file.write_text(content)
                with self.assertRaises(ValueError):
                    self.check()

    def test_unsafe_checksum_path_rejected(self):
        (self.root / "SHA256SUMS").write_text("0" * 64 + "  ../outside\n")
        with self.assertRaises(ValueError):
            self.check()

    def statement(self):
        prefix = "https://" + checks.PROJECT + "/releases/download/v" + VERSION + "/"
        artifacts, resources = [], []
        for target in checks.TARGETS:
            target_os, arch = target.split("-")
            stem = "tsnet-bridge-" + VERSION + "-" + target
            name = stem + (".zip" if target_os == "windows" else ".tar.gz")
            artifacts.append({"name": name, "format": "zip" if target_os == "windows" else "tar.gz", "bin": ["bin/tsnet-bridge" + (".exe" if target_os == "windows" else "")], "os": target_os, "arch": {"arm64": "aarch64", "amd64": "x86_64"}[arch], "url": prefix + name, "size": (self.root / name).stat().st_size, "provenance": ["https://api.github.com/repos/" + checks.REPOSITORY + "/attestations/sha256:" + checks.sha256(self.root / name)]})
            resources.append({"kind": "sbom", "format": "cyclonedx", "artifact": name, "asset": stem + ".cdx.json", "url": prefix + stem + ".cdx.json"})
        return {"_type": "https://in-toto.io/Statement/v1", "predicateType": "https://packslip.dev/release/v1", "subject": [{"name": name, "digest": {"sha256": checks.sha256(self.root / name)}} for name in sorted(checks.filenames(VERSION)) if name.endswith((".tar.gz", ".zip", ".cdx.json"))], "predicate": {"project": checks.PROJECT, "version": VERSION, "source": {"repo": "https://" + checks.PROJECT, "commit": COMMIT, "tag": "v" + VERSION}, "identity": {"scheme": "sigstore-oidc", "key_id": checks.IDENTITY, "issuer": checks.ISSUER}, "artifacts": artifacts, "resources": resources}}

    def bundle(self, statement):
        # Deliberately unsigned semantic-test envelope, never an installable bundle.
        self.write("packslip.sigstore.json", {"dsseEnvelope": {"payload": base64.b64encode(json.dumps(statement).encode()).decode()}})
        checks.check_bundle(self.root, VERSION, COMMIT)

    def test_bundle_semantics(self):
        self.bundle(self.statement())

    def test_wrong_bundle_identity_project_version_and_commit_rejected(self):
        original = self.statement()
        for path, value in ((["project"], "example.com/other"), (["version"], "0.1.0-alpha.2"), (["source", "commit"], "b" * 40), (["identity", "key_id"], "https://github.com/another/workflow"), (["identity", "issuer"], "https://example.com/issuer")):
            changed = copy.deepcopy(original)
            destination = changed["predicate"]
            for key in path[:-1]:
                destination = destination[key]
            destination[path[-1]] = value
            with self.subTest(path=path), self.assertRaises(ValueError):
                self.bundle(changed)

    def test_missing_or_duplicate_signed_subject_rejected(self):
        for mode in ("missing", "duplicate"):
            statement = self.statement()
            if mode == "missing":
                statement["subject"].pop()
            else:
                statement["subject"][-1] = statement["subject"][0]
            with self.subTest(mode=mode), self.assertRaises(ValueError):
                self.bundle(statement)

    def test_wrong_signed_digest_rejected(self):
        statement = self.statement()
        statement["subject"][0]["digest"]["sha256"] = "0" * 64
        with self.assertRaises(ValueError):
            self.bundle(statement)

    def test_bad_platform_provenance_and_sbom_binding_rejected(self):
        original = self.statement()
        for collection, key, value in (("artifacts", "arch", "wrong"), ("artifacts", "format", "raw"), ("artifacts", "bin", ["../wrong"]), ("artifacts", "libc", "gnu"), ("artifacts", "provenance", []), ("artifacts", "url", "https://example.com/artifact"), ("resources", "asset", "other.cdx.json")):
            statement = copy.deepcopy(original)
            statement["predicate"][collection][0][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.bundle(statement)


if __name__ == "__main__":
    unittest.main()
