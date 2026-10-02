"""Offline unit tests for release guards; these do not sign or publish anything."""
import base64
import copy
import importlib.util
import json
import pathlib
import re
import shlex
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("release_checks", pathlib.Path(__file__).with_name("release-validation.py"))
checks = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checks)
VERSION = "0.1.0-alpha.1"
COMMIT = "a" * 40


class WorkflowAttestationPolicy(unittest.TestCase):
    SELECTORS = {"--cert-identity", "--cert-identity-regex", "--signer-repo", "--signer-workflow"}

    def check_command(self, command):
        words = shlex.split(command)
        selectors = [word.split("=", 1)[0] for word in words if word.split("=", 1)[0] in self.SELECTORS]
        self.assertEqual(selectors, ["--cert-identity"], "gh attestation identity selectors are mutually exclusive; retain the exact certificate identity")
        for flag, value in (("--repo", "$GITHUB_REPOSITORY"), ("--cert-identity", "https://github.com/$GITHUB_WORKFLOW_REF"), ("--cert-oidc-issuer", "https://token.actions.githubusercontent.com"), ("--source-digest", "$TESTED_COMMIT"), ("--source-ref", "refs/heads/main")):
            self.assertEqual(words[words.index(flag) + 1], value)
        self.assertIn("--deny-self-hosted-runners", words)

    def commands(self):
        workflow = pathlib.Path(__file__).parents[1] / "workflows" / "prerelease.yml"
        logical_lines = workflow.read_text(encoding="utf-8").replace("\\\n", " ").splitlines()
        return [line.strip() for line in logical_lines if line.strip().startswith("gh attestation verify ")]

    def test_every_attestation_command_uses_one_exact_identity_selector(self):
        commands = self.commands()
        self.assertEqual(len(commands), 3, "check signing inputs, signature bundle, and public assets independently")
        for command in commands:
            with self.subTest(command=command):
                self.check_command(command)

    def test_duplicate_identity_selectors_are_rejected(self):
        command = self.commands()[0]
        for selector in self.SELECTORS:
            with self.subTest(selector=selector), self.assertRaises(AssertionError):
                self.check_command(command + " " + selector + " conflicting-selector")


class WorkflowCachePolicy(unittest.TestCase):
    CACHE_PIN = "55cc8345863c7cc4c66a329aec7e433d2d1c52a9"
    MAIN_GUARD = "github.repository == 'webkaz-labs/tsnet-bridge' && github.ref == 'refs/heads/main' && github.event.repository.default_branch == 'main' && (github.event_name == 'push' || github.event_name == 'workflow_dispatch')"
    CACHE_PATHS = "\n".join(("          path: |", "            ${{ steps.go-cache-info.outputs.gomodcache }}", "            ${{ steps.go-cache-info.outputs.gocache }}"))

    def workflow(self, name):
        return (pathlib.Path(__file__).parents[1] / "workflows" / (name + ".yml")).read_text(encoding="utf-8")

    def job(self, workflow, name):
        # Extract known, fixed-indentation policy blocks without adding a YAML
        # dependency to the offline gate. actionlint validates the full syntax.
        match = re.search(r"^  " + re.escape(name) + r":\n.*?(?=^  [a-z][a-z0-9-]*:\n|\Z)", self.workflow(workflow), re.MULTILINE | re.DOTALL)
        self.assertIsNotNone(match, "missing workflow job: " + name)
        return match.group()

    def step(self, workflow, job, needle):
        steps = re.findall(r"^      - .*?(?=^      - |\Z)", self.job(workflow, job), re.MULTILINE | re.DOTALL)
        matches = [step for step in steps if needle in step]
        self.assertEqual(len(matches), 1, "expected exactly one step containing: " + needle)
        return matches[0]

    def key(self, workflow, job):
        restore = self.step(workflow, job, "uses: actions/cache/restore@")
        return re.search(r"^          key: (.+)$", restore, re.MULTILINE).group(1)

    def test_release_validation_artifact_contains_offline_install_dependency(self):
        upload = self.step("prerelease", "gate", "name: release-validation-tools")
        for filename in ("release-validation.py", "verify-installed.py", "offline-smoke.py"):
            self.assertIn("            .github/scripts/" + filename + "\n", upload)
        root = pathlib.Path(__file__).parent
        for filename in ("verify-installed.py", "smoke-package.py"):
            self.assertIn('with_name("offline-smoke.py")', (root / filename).read_text(encoding="utf-8"))
        offline = (root / "offline-smoke.py").read_text(encoding="utf-8")
        self.assertIn("check_locales(binary)", offline)
        self.assertIn("native locale fallback mismatch", offline)

    def test_ci_frontend_and_packaged_product_are_verified(self):
        # Sobalink release-workflow preparation is a separate integration.
        # Normal CI must already validate the complete packaged product.
        native = self.job("ci", "native")
        self.assertIn("node-version: '24.19.0'", native)
        self.assertIn("actions/setup-node@49933ea5288caeca8642d1e84afbd3f7d6820020", native)
        self.assertLess(native.index("python .github/scripts/check-frontend.py"), native.index("go test -race"))
        self.assertIn("dist/sobalink-$", native)
        frontend = (pathlib.Path(__file__).parent / "check-frontend.py").read_text(encoding="utf-8")
        self.assertEqual(frontend.count('run("ci", "--no-audit", "--no-fund")'), 2)
        self.assertIn('assert first == inventory()', frontend)
        self.assertIn('assert first == checked_in', frontend)

    def test_cache_keys_include_exact_runner_toolchain_manifests_and_source(self):
        prefix = "trusted-main-go-v1-${{ runner.os }}-${{ runner.arch }}-"
        suffix = "-go1.27.1-${{ hashFiles('go.mod', 'go.sum') }}-"
        for job, runner in (("native", "${{ matrix.runner }}"), ("manifest-smoke", "ubuntu-24.04")):
            release_job = "native" if job == "native" else "provenance"
            with self.subTest(job=job):
                self.assertEqual(self.key("ci", job), prefix + runner + suffix + "${{ github.sha }}")
                self.assertEqual(self.key("prerelease", release_job), prefix + runner + suffix + "${{ needs.gate.outputs.commit }}")
        native_matrix = lambda name: re.findall(r"^          - runner: (.+)\n            goos: (.+)\n            goarch: (.+)$", self.job(name, "native"), re.MULTILINE)
        self.assertEqual(native_matrix("ci"), native_matrix("prerelease"))
        self.assertEqual(len(native_matrix("ci")), 4)
        self.assertIn(("ubuntu-24.04", "linux", "amd64"), native_matrix("ci"))

    def test_four_target_inventories_match(self):
        expected = ("linux-amd64", "linux-arm64", "darwin-arm64", "windows-amd64")
        self.assertEqual(checks.TARGETS, expected)
        root = pathlib.Path(__file__).parents[2]
        source = (root / "internal/distribution/distribution.go").read_text(encoding="utf-8")
        target_line = next(line for line in source.splitlines() if line.startswith("var Targets = "))
        go_targets = tuple(a + "-" + b for a, b in re.findall(r'\{"([^"\n]+)", "([^"\n]+)"\}', target_line))
        self.assertEqual(go_targets, expected)
        install_targets = tuple(re.findall(r"^            target: ([a-z0-9-]+)$", self.job("prerelease", "verify-mise-install"), re.MULTILINE))
        self.assertEqual(install_targets, expected)
        for name in ("ci", "prerelease"):
            self.assertNotIn("macos-15-intel", self.workflow(name))
        self.assertFalse(any("darwin-amd64" in name for name in checks.filenames(VERSION)))

    def test_all_cache_paths_and_actions_match_the_pinned_producer(self):
        for workflow, jobs in (("ci", ("native", "manifest-smoke")), ("prerelease", ("native", "provenance"))):
            self.assertNotIn("cache: true", self.workflow(workflow))
            self.assertNotRegex(self.workflow(workflow), r"uses: actions/cache@")
            for job in jobs:
                with self.subTest(workflow=workflow, job=job):
                    restore = self.step(workflow, job, "uses: actions/cache/restore@")
                    self.assertIn("uses: actions/cache/restore@" + self.CACHE_PIN, restore)
                    self.assertIn(self.CACHE_PATHS, restore)
                    paths = self.step(workflow, job, "id: go-cache-info")
                    self.assertIn('test "$(go env GOVERSION)" = go1.27.1', paths)
                    self.assertIn('echo "gomodcache=$(go env GOMODCACHE)"', paths)
                    self.assertIn('echo "gocache=$(go env GOCACHE)"', paths)
                    self.assertLess(self.job(workflow, job).index(paths), self.job(workflow, job).index(restore))

    def test_only_successful_canonical_main_native_ci_can_save(self):
        ci = self.workflow("ci")
        self.assertEqual(ci.count("uses: actions/cache/save@"), 1)
        save = self.step("ci", "native", "uses: actions/cache/save@")
        self.assertIn("uses: actions/cache/save@" + self.CACHE_PIN, save)
        self.assertIn("        if: success() && " + self.MAIN_GUARD + " && steps.go-cache.outputs.cache-hit != 'true'\n", save)
        self.assertIn(self.CACHE_PATHS, save)
        self.assertIn("key: ${{ steps.go-cache.outputs.cache-primary-key }}", save)
        native = self.job("ci", "native")
        self.assertGreater(native.index(save), native.index("uses: actions/upload-artifact@"))
        for job in ("native", "manifest-smoke"):
            self.assertIn("        if: " + self.MAIN_GUARD + "\n", self.step("ci", job, "uses: actions/cache/restore@"))
        fallback = re.findall(r"^          restore-keys: (.+)$", ci, re.MULTILINE)
        self.assertEqual(fallback, [self.key("ci", "native").removesuffix("${{ github.sha }}")])

    def test_prerelease_restores_exact_tested_source_without_saving(self):
        release = self.workflow("prerelease")
        self.assertNotIn("actions/cache/save@", release)
        self.assertNotIn("restore-keys:", release)
        self.assertEqual(release.count("uses: actions/cache/restore@"), 2)
        for job in ("native", "provenance"):
            with self.subTest(job=job):
                report = self.step("prerelease", job, "CACHE_MATCHED_KEY:")
                self.assertIn('test -z "$CACHE_MATCHED_KEY" || test "$CACHE_MATCHED_KEY" = "$CACHE_PRIMARY_KEY"', report)
                self.assertIn("steps.go-cache.outputs.cache-primary-key", report)
                self.assertIn("steps.go-cache.outputs.cache-matched-key", report)
                self.assertIn("steps.go-cache.outputs.cache-hit", report)
                self.assertLess(self.job("prerelease", job).index(report), self.job("prerelease", job).index("go mod verify"))
        self.assertIn("tee logs/go-cache.txt", self.step("prerelease", "native", "CACHE_MATCHED_KEY:"))
        self.assertIn("    needs: gate", self.job("prerelease", "native"))
        self.assertIn("    needs: [gate, native]", self.job("prerelease", "provenance"))

    def test_tests_and_native_validation_always_execute(self):
        for workflow in ("ci", "prerelease"):
            for needle in ("go test -race -count=1 -timeout=10m ./...", "go vet ./...", "gofmt -l cmd internal", "python .github/scripts/smoke-package.py"):
                with self.subTest(workflow=workflow, command=needle):
                    step = self.step(workflow, "native", needle)
                    self.assertNotIn("        if:", step)
                    self.assertNotIn("cache-hit", step)
        self.assertIn("python -m unittest discover -s .github/scripts -p 'test_*.py' -v", self.job("ci", "native"))
        self.assertIn("packslip verify", self.job("ci", "manifest-smoke"))
        self.assertIn("sha256sum --check SHA256SUMS", self.job("ci", "manifest-smoke"))

    def test_native_ipc_regression_is_repeated_without_cache_skips(self):
        for workflow, command in (("ci", "go test -race -count=5 -timeout=2m ./internal/control"), ("prerelease", "go test -race -count=25 -timeout=3m ./internal/control")):
            with self.subTest(workflow=workflow):
                step = self.step(workflow, "native", command)
                self.assertNotIn("        if:", step)
                self.assertNotIn("cache-hit", step)
        self.assertIn("tee logs/ipc-regression.txt", self.job("prerelease", "native"))

    def test_ci_builds_once_and_prerelease_still_compares_two_builds(self):
        for workflow, count in (("ci", 1), ("prerelease", 2)):
            native = self.job(workflow, "native")
            with self.subTest(workflow=workflow):
                self.assertEqual(native.count("go run ./cmd/package-tool build "), count)
                self.assertEqual(native.count("go run ./cmd/package-tool checksums"), count)
        self.assertNotIn("first-SHA256SUMS", self.job("ci", "native"))
        release = self.job("prerelease", "native")
        self.assertIn('cp dist/SHA256SUMS "$RUNNER_TEMP/first-SHA256SUMS"', release)
        self.assertIn('cmp "$RUNNER_TEMP/first-SHA256SUMS" dist/SHA256SUMS', release)


class ScriptTextEncoding(unittest.TestCase):
    def test_workflow_policy_reads_ignore_legacy_windows_default(self):
        original_open = pathlib.Path.open

        def legacy_windows_open(path, mode="r", buffering=-1, encoding=None, errors=None, newline=None):
            if "b" not in mode and encoding in (None, "locale"):
                encoding = "cp1252"
            return original_open(path, mode, buffering, encoding, errors, newline)

        workflow = pathlib.Path(__file__).parents[1] / "workflows" / "prerelease.yml"
        expected = workflow.read_bytes().decode("utf-8")
        with patch.object(pathlib.Path, "open", legacy_windows_open):
            # Reproduce the Windows failure first, without changing PYTHONUTF8.
            with self.assertRaises(UnicodeDecodeError):
                workflow.read_text(encoding="locale")
            self.assertEqual(WorkflowCachePolicy().workflow("prerelease"), expected)
            commands = WorkflowAttestationPolicy().commands()
            self.assertEqual(len(commands), 3)
            for command in commands:
                WorkflowAttestationPolicy().check_command(command)


class ReleaseLookup(unittest.TestCase):
    def setUp(self):
        self.tag = "v" + VERSION
        self.draft = {"id": 123, "tag_name": self.tag, "draft": True}

    def test_published_release_uses_tag_endpoint(self):
        published = dict(self.draft, draft=False)
        with patch.object(checks, "api", return_value=published) as api:
            self.assertEqual(checks.find_release(self.tag), published)
            api.assert_called_once_with("releases/tags/" + self.tag, missing=True)

    def test_draft_tag_404_falls_back_to_list_and_numeric_id(self):
        with patch.object(checks, "api", side_effect=[None, [self.draft], self.draft]) as api:
            self.assertEqual(checks.find_release(self.tag), self.draft)
            self.assertEqual([call.args[0] for call in api.call_args_list], [
                "releases/tags/" + self.tag, "releases?per_page=100&page=1", "releases/123"])

    def test_draft_on_second_page_is_found(self):
        first = [{"id": i + 1, "tag_name": "v9.0.0-test." + str(i)} for i in range(100)]
        with patch.object(checks, "api", side_effect=[None, first, [self.draft], self.draft]) as api:
            self.assertEqual(checks.find_release(self.tag), self.draft)
            self.assertEqual(api.call_args_list[2].args[0], "releases?per_page=100&page=2")

    def test_absent_release_requires_completed_listing(self):
        with patch.object(checks, "api", side_effect=[None, []]) as api:
            self.assertIsNone(checks.find_release(self.tag))
            self.assertEqual(api.call_count, 2)

    def test_duplicate_matching_drafts_fail_closed(self):
        with patch.object(checks, "api", side_effect=[None, [self.draft, dict(self.draft, id=124)]]):
            with self.assertRaisesRegex(ValueError, "multiple releases"):
                checks.find_release(self.tag)

    def test_changed_release_identity_and_invalid_id_fail_closed(self):
        for detail in (dict(self.draft, id=124), dict(self.draft, tag_name="v9.9.9-test")):
            with self.subTest(detail=detail), patch.object(checks, "api", side_effect=[None, [self.draft], detail]):
                with self.assertRaisesRegex(ValueError, "release changed"):
                    checks.find_release(self.tag)
        for release_id in ("123", "../unexpected", 0, True):
            with self.subTest(id=release_id), patch.object(checks, "api", side_effect=[None, [dict(self.draft, id=release_id)]]):
                with self.assertRaisesRegex(ValueError, "invalid release ID"):
                    checks.find_release(self.tag)

    def test_listing_errors_are_not_treated_as_absence(self):
        error = PermissionError("fixture access denied")
        with patch.object(checks, "api", side_effect=[None, error]):
            with self.assertRaises(PermissionError):
                checks.find_release(self.tag)

    def test_incomplete_listing_fails_closed(self):
        page = [{"id": i + 1, "tag_name": "v9.0.0-test." + str(i)} for i in range(100)]
        with patch.object(checks, "api", side_effect=[None] + [page] * 100):
            with self.assertRaisesRegex(ValueError, "incomplete lookup"):
                checks.find_release(self.tag)


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
            return self.release if self.release is not None and not self.release["draft"] else None
        if path == "releases?per_page=100&page=1":
            return [self.release] if self.release is not None else []
        if path == "releases/1":
            return self.release
        self.fail("unexpected API path: " + path)

    def gate(self):
        return checks.source_gate(VERSION, COMMIT, self.env)

    def draft(self):
        self.tag = copy.deepcopy(self.ref)
        self.release = {"id": 1, "tag_name": "v" + VERSION, "target_commitish": COMMIT, "prerelease": True, "draft": True, "body": "<!-- sobalink-source:" + COMMIT + " -->", "assets": []}

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

    def test_staging_lifecycle_with_realistic_draft_visibility(self):
        self.assertEqual(checks.release_state(VERSION, COMMIT), (None, None))
        self.tag = copy.deepcopy(self.ref)
        self.assertEqual(checks.release_state(VERSION, COMMIT), (self.tag, None))
        self.draft()
        # GitHub's by-tag route is still 404 after successful draft creation.
        self.assertIsNone(self.api("releases/tags/v" + VERSION, missing=True))
        self.assertEqual(checks.release_state(VERSION, COMMIT, required=True)[1], self.release)
        self.release["assets"] = [{"name": name} for name in checks.filenames(VERSION)]
        checks.release_state(VERSION, COMMIT, required=True)
        self.release["assets"].append({"name": "packslip.sigstore.json"})
        checks.release_state(VERSION, COMMIT, required=True)
        self.release["draft"] = False
        checks.release_state(VERSION, COMMIT, required=True, published=True)
        with self.assertRaisesRegex(ValueError, "published releases are immutable"):
            checks.release_state(VERSION, COMMIT)

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
                checks.release_state(VERSION, COMMIT, required=True)

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
            stem = "sobalink-" + VERSION + "-" + target
            self.write(stem + ".build.json", {"project": checks.PROJECT, "product":"sobalink", "frontend":{"node_version":"24.19.0","npm_version":"11.9.0","lock_sha256":"f"*64,"assets":[{"path":"index.html","sha256":"e"*64}]}, "version": VERSION, "source_commit": COMMIT, "target": target, "go_version": "go1.27.1", "build_tags":"ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy", "cgo_enabled": False, "trimpath": True, "buildvcs": False})
            self.write(stem + ".cdx.json", {"bomFormat": "CycloneDX", "specVersion": "1.5"})
            self.write(stem + ".notices.json", {"frontend_modules":[{"notices":[{"path":"licenses/npm/example/LICENSE"}]}], "modules": [{"notices": [{"path": "licenses/example/LICENSE"}]}], "go_standard_library": {"notices": [{"path": "licenses/go/LICENSE"}]}})
        self.checksums()

    def write(self, name, value):
        (self.root / name).write_text(json.dumps(value), encoding="utf-8")

    def checksums(self):
        names = checks.filenames(VERSION) - {"SHA256SUMS"}
        (self.root / "SHA256SUMS").write_text("".join(checks.sha256(self.root / name) + "  " + name + "\n" for name in sorted(names)), encoding="utf-8")

    def check(self):
        checks.check_assets(self.root, VERSION, COMMIT)

    def test_complete_four_target_inventory(self):
        self.assertEqual(len(checks.filenames(VERSION)), 18)
        self.check()

    def test_unexpected_asset_rejected(self):
        (self.root / "unintended.txt").write_text("do not publish", encoding="utf-8")
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
        meta = json.loads(file.read_text(encoding="utf-8"))
        meta["source_commit"] = "b" * 40
        self.write(file.name, meta)
        self.checksums()
        with self.assertRaises(ValueError):
            self.check()

    def test_missing_license_notices_rejected(self):
        file = next(self.root.glob("*.notices.json"))
        notices = json.loads(file.read_text(encoding="utf-8"))
        notices["go_standard_library"]["notices"] = []
        self.write(file.name, notices)
        self.checksums()
        with self.assertRaises(ValueError):
            self.check()

    def test_duplicate_or_incomplete_checksums_rejected(self):
        file = self.root / "SHA256SUMS"
        original = file.read_text(encoding="utf-8")
        for content in (original + original.splitlines()[0] + "\n", "\n".join(original.splitlines()[:-1]) + "\n"):
            with self.subTest(content=content[:70]):
                file.write_text(content, encoding="utf-8")
                with self.assertRaises(ValueError):
                    self.check()

    def test_unsafe_checksum_path_rejected(self):
        (self.root / "SHA256SUMS").write_text("0" * 64 + "  ../outside\n", encoding="utf-8")
        with self.assertRaises(ValueError):
            self.check()

    def statement(self):
        prefix = "https://" + checks.PROJECT + "/releases/download/v" + VERSION + "/"
        artifacts, resources = [], []
        for target in checks.TARGETS:
            target_os, arch = target.split("-")
            stem = "sobalink-" + VERSION + "-" + target
            name = stem + (".zip" if target_os == "windows" else ".tar.gz")
            artifacts.append({"name": name, "format": "zip" if target_os == "windows" else "tar.gz", "bin": ["bin/soba" + (".exe" if target_os == "windows" else "")], "os": target_os, "arch": {"arm64": "aarch64", "amd64": "x86_64"}[arch], "url": prefix + name, "size": (self.root / name).stat().st_size, "provenance": ["https://api.github.com/repos/" + checks.REPOSITORY + "/attestations/sha256:" + checks.sha256(self.root / name)]})
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
