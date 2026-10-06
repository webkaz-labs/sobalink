"""Offline unit tests for release guards; these do not sign or publish anything."""
import base64
import copy
import contextlib
import io
import importlib.util
import json
import os
import pathlib
import re
import shlex
import tempfile
import textwrap
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
    MAIN_GUARD = "github.repository == 'webkaz-labs/sobalink' && github.ref == 'refs/heads/main' && github.event.repository.default_branch == 'main' && (github.event_name == 'push' || github.event_name == 'workflow_dispatch')"
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

    def key(self, workflow, job, cache_id="go-cache"):
        restore = self.step(workflow, job, "id: " + cache_id + "\n")
        return re.search(r"^          key: (.+)$", restore, re.MULTILINE).group(1)

    def test_release_validation_artifact_contains_offline_install_dependency(self):
        upload = self.step("prerelease", "gate", "name: release-validation-tools")
        for filename in ("release-validation.py", "verify-installed.py", "offline-smoke.py", "source_provenance.py"):
            self.assertIn("            .github/scripts/" + filename + "\n", upload)
        root = pathlib.Path(__file__).parent
        for filename in ("verify-installed.py", "smoke-package.py"):
            self.assertIn('with_name("offline-smoke.py")', (root / filename).read_text(encoding="utf-8"))
        offline = (root / "offline-smoke.py").read_text(encoding="utf-8")
        self.assertIn("check_locales(binary)", offline)
        self.assertIn("native locale fallback mismatch", offline)

    def test_ci_frontend_and_packaged_product_are_verified(self):
        for workflow in ("ci", "prerelease"):
            with self.subTest(workflow=workflow):
                native = self.job(workflow, "native")
                self.assertIn("node-version: '24.19.0'", native)
                self.assertIn("actions/setup-node@49933ea5288caeca8642d1e84afbd3f7d6820020", native)
                self.assertLess(native.index("python .github/scripts/check-frontend.py"), native.index("go test -race"))
                self.assertIn("dist/sobalink-$", native)
                self.assertIn("GOFLAGS: -mod=readonly -tags=ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy", self.workflow(workflow))
        frontend = (pathlib.Path(__file__).parent / "check-frontend.py").read_text(encoding="utf-8")
        self.assertEqual(frontend.count('run("ci", "--no-audit", "--no-fund")'), 2)
        self.assertIn('assert first == inventory()', frontend)
        self.assertIn('assert first == checked_in', frontend)

    def test_reviewed_engine_preparation_and_guarded_gates_are_required(self):
        for workflow, jobs in (("ci", ("native", "manifest-smoke", "browser")), ("prerelease", ("native", "provenance"))):
            for job in jobs:
                source = self.job(workflow, job)
                self.assertLess(source.index("go run ./cmd/prepare-engine\n"), source.index("go mod verify"))
                self.assertIn("go run ./cmd/prepare-engine --verify", source)
        for workflow in ("ci", "prerelease"):
            step = self.step(workflow, "native", "name: Verify guarded production direct and relay underlays")
            self.assertNotIn("        if:", step)
            self.assertNotIn("ts_omit_udptransport", step)
            engine_step = self.step(workflow, "native", "name: Verify adapted engine admission and native underlay denial")
            self.assertNotIn("        if:", engine_step)
            self.assertIn('env["SOBALINK_RUN_UNDERLAY_NATIVE"] = "1"', engine_step)
            self.assertIn('cwd=".sobalink-deps/tailscale"', engine_step)
            self.assertIn('"-run=^Test(UnderlayGuard|WANCandidate|Sobalink|GuardedLocalSocketEndpointUsesActualFamilyPort)"', engine_step)
            self.assertIn('"./net/underlayguard"', engine_step)
            self.assertIn('"./derp"', engine_step)
            self.assertIn("python .github/scripts/underlay-negative-controls.py --source .sobalink-deps/tailscale", engine_step)
            self.assertIn("go run ./cmd/prepare-engine --verify", engine_step)
            self.assertIn('env["SOBALINK_RUN_GUARDED_INTEGRATION"] = "1"', step)
            relay_test = "TestGuardedRelayTwoPeerFunctionalIntegration" if workflow == "ci" else "TestGuardedRelayTwoPeerIntegration"
            self.assertIn(relay_test + "|TestGuardedDirectEncryptedTCPUDPIntegration|TestGuardedDERPFailurePropagationIntegration", step)
            self.assertIn('"go", "test", "-race", "-count=1"', step)
        scripts = pathlib.Path(__file__).parent
        for name in ("smoke-package.py", "verify-installed.py"):
            self.assertIn("verify_sources(share,", (scripts / name).read_text())

    def test_direct_and_wan_native_extension_gates(self):
        for workflow in ("ci", "prerelease"):
            direct = self.step(workflow, "native", "name: Verify direct LAN WireGuard and Core native applications")
            self.assertNotIn("        if:", direct)
            self.assertIn("go test -race -count=2 -timeout=120s", direct)
            self.assertIn("-run='^TestDirectLAN' ./internal/core", direct)
            self.assertIn("-count=5 -timeout=5m", direct)
            title = "name: Verify direct LAN session natural rekey and idle lifecycle" if workflow == "ci" else "name: Verify direct LAN session rekey and idle lifecycle"
            lifecycle = self.step(workflow, "native", title)
            if workflow == "ci":
                self.assertIn("if: steps.ci-plan.outputs.long_required != 'false'", lifecycle)
                synthetic = self.step("ci", "native", "name: Verify direct LAN synthetic expiry and rekey")
                self.assertNotIn("        if:", synthetic)
                self.assertIn("-run='^TestNativeSessionSyntheticExpiryRekey$'", synthetic)
                self.assertIn("go test -race -count=1", synthetic)
            else:
                self.assertNotIn("        if:", lifecycle)
            self.assertIn("-count=1 -timeout=8m", lifecycle)
            self.assertIn("directlan_integration,directlan_lifecycle", lifecycle)
            pattern = "-run='^TestNativeSessionLifecycle$'" if workflow == "ci" else "-run='^TestNativeSession(SyntheticExpiryRekey|Lifecycle)$'"
            self.assertIn(pattern, lifecycle)
            self.assertNotIn("directlan_lifecycle", direct)
            self.assertNotIn("sudo", lifecycle)
            self.assertIn("go test -race -count=5", direct)
            self.assertIn("go vet -tags=directlan_integration", direct)
            self.assertNotIn("sudo", direct)
            wan = self.step(workflow, "native", "name: Verify explicit WAN discovery with isolated native STUN")
            self.assertNotIn("        if:", wan)
            self.assertIn('env["SOBALINK_RUN_WAN_INTEGRATION"] = "1"', wan)
            self.assertIn("TestWANCandidateNativeLoopbackSTUN", wan)
            self.assertNotIn("ts_omit_udptransport", wan)
            native = self.job(workflow, "native")
            self.assertIn("./internal/backendworker ./internal/connectionroute", native)
            self.assertIn("./internal/core -run '^TestMixed'", native)

    def test_release_product_identity_and_paired_transport_gate_match_packages(self):
        release = self.workflow("prerelease")
        self.assertNotIn("dist/tsnet-bridge-", release)
        self.assertNotIn("mise exec -- tsnet-bridge", release)
        self.assertIn('"<!-- sobalink-source:" + commit + " -->"', self.job("prerelease", "stage"))
        installed = self.job("prerelease", "verify-mise-install")
        self.assertIn("mise exec -- soba version", installed)
        self.assertIn("mise exec -- soba --help", installed)
        for workflow in ("ci", "prerelease"):
            step = self.step(workflow, "native", "name: Verify paired transport")
            self.assertNotIn("        if:", step)
            relay_test = "TestTrustedRelayTwoPeerFunctionalIntegration" if workflow == "ci" else "TestTrustedRelayTwoPeerIntegration"
            self.assertIn(relay_test + "|TestLANCorePeerApplicationsIntegration", step)
            self.assertIn('env["SOBALINK_RUN_LAN_INTEGRATION"] = "1"', step)
            self.assertIn("ts_omit_udptransport", step)

    def test_cache_keys_include_exact_runner_toolchain_manifests_and_source(self):
        prefix = "trusted-main-go-v1-${{ runner.os }}-${{ runner.arch }}-"
        suffix = "-go1.27.1-${{ hashFiles('go.mod', 'go.sum', 'internal/engineadaptation/**') }}-"
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
                    restores = re.findall(r"^      - .*?(?=^      - |\Z)", self.job(workflow, job), re.MULTILINE | re.DOTALL)
                    restores = [step for step in restores if "uses: actions/cache/restore@" in step]
                    self.assertEqual(len(restores), 2 if (workflow, job) == ("ci", "native") else 1)
                    for restore in restores:
                        self.assertIn("uses: actions/cache/restore@" + self.CACHE_PIN, restore)
                        self.assertIn(self.CACHE_PATHS, restore)
                    paths = self.step(workflow, job, "id: go-cache-info")
                    self.assertIn('test "$(go env GOVERSION)" = go1.27.1', paths)
                    self.assertIn('echo "gomodcache=$(go env GOMODCACHE)"', paths)
                    self.assertIn('echo "gocache=$(go env GOCACHE)"', paths)
                    for restore in restores:
                        self.assertLess(self.job(workflow, job).index(paths), self.job(workflow, job).index(restore))

    def test_only_successful_canonical_main_native_ci_can_save(self):
        ci = self.workflow("ci")
        self.assertEqual(ci.count("uses: actions/cache/save@"), 3)
        save = self.step("ci", "native", "name: Save Go caches after all native checks pass on main")
        self.assertIn("uses: actions/cache/save@" + self.CACHE_PIN, save)
        self.assertIn("        if: success() && steps.ci-plan.outputs.long_required != 'false' && " + self.MAIN_GUARD + " && steps.go-cache.outputs.cache-hit != 'true'\n", save)
        self.assertIn(self.CACHE_PATHS, save)
        self.assertIn("key: ${{ steps.go-cache.outputs.cache-primary-key }}", save)
        native = self.job("ci", "native")
        self.assertGreater(native.index(save), native.index("uses: actions/upload-artifact@"))
        for job in ("native", "manifest-smoke"):
            self.assertIn("        if: " + self.MAIN_GUARD + "\n", self.step("ci", job, "id: go-cache\n"))
        fallback = re.findall(r"^          restore-keys: (.+)$", self.step("ci", "native", "id: go-cache\n"), re.MULTILINE)
        self.assertEqual(fallback, [self.key("ci", "native").removesuffix("${{ github.sha }}")])

    def inline_python(self, step):
        match = re.search(r"^          python - <<'PYTHON'\n(.*?)^          PYTHON$", step, re.MULTILINE | re.DOTALL)
        self.assertIsNotNone(match)
        return textwrap.dedent(match.group(1))

    def development_scope(self, event_name="workflow_dispatch", ref="refs/heads/feature/example", repository="webkaz-labs/sobalink", event=None):
        if event is None:
            event = self.development_event()
        source = self.inline_python(self.step("ci", "native", "id: development-go-cache-scope\n"))
        with tempfile.TemporaryDirectory() as directory:
            event_path = pathlib.Path(directory) / "event.json"
            output_path = pathlib.Path(directory) / "output.txt"
            event_path.write_text(json.dumps(event), encoding="utf-8")
            env = {"GITHUB_EVENT_PATH": str(event_path), "GITHUB_OUTPUT": str(output_path),
                   "GITHUB_REPOSITORY": repository, "GITHUB_REF": ref, "GITHUB_EVENT_NAME": event_name}
            with patch.dict(os.environ, env, clear=True):
                exec(compile(source, "ci.yml development cache scope", "exec"), {})
            output = output_path.read_text(encoding="utf-8")
            self.assertRegex(output, r"^scope=([^\n]*)\n$")
            return output.removeprefix("scope=").strip()

    def development_event(self):
        repository = {"full_name": "webkaz-labs/sobalink", "default_branch": "main", "private": False}
        return {"repository": repository, "number": 42,
                "pull_request": {"number": 42, "head": {"repo": copy.deepcopy(repository)},
                                 "base": {"repo": copy.deepcopy(repository)}}}

    def test_only_same_repository_prs_and_non_main_dispatches_get_development_scopes(self):
        branch = self.development_scope()
        self.assertRegex(branch, r"^branch-[0-9a-f]{64}$")
        self.assertEqual(branch, self.development_scope())
        self.assertEqual(self.development_scope("pull_request", "refs/pull/42/merge"), "pr-42")
        for event_name, ref in (("push", "refs/heads/feature/example"), ("push", "refs/heads/main"),
                                ("workflow_dispatch", "refs/heads/"),
                                ("workflow_dispatch", "refs/heads/main"), ("workflow_dispatch", "refs/tags/v1.0.0"),
                                ("workflow_dispatch", "refs/pull/42/merge"), ("schedule", "refs/heads/feature/example"),
                                ("pull_request_target", "refs/pull/42/merge"), ("unknown", "refs/heads/feature/example"),
                                ("pull_request", "refs/heads/main"), ("pull_request", "refs/pull/42/head"),
                                ("pull_request", "refs/pull/43/merge")):
            with self.subTest(event_name=event_name, ref=ref):
                self.assertEqual(self.development_scope(event_name, ref), "")
        for repository in ("example/sobalink", "webkaz-labs/other", ""):
            with self.subTest(repository=repository):
                for event_name, ref in (("workflow_dispatch", "refs/heads/feature/example"), ("pull_request", "refs/pull/42/merge")):
                    self.assertEqual(self.development_scope(event_name, ref, repository=repository), "")

    def test_development_scope_rejects_forks_missing_and_inconsistent_event_metadata(self):
        changes = [("repository", "full_name", "example/sobalink"), ("repository", "default_branch", "other"),
                   ("repository", "default_branch", None), ("repository", "private", True),
                   ("repository", "private", "false"), ("repository", "private", None)]
        for section, field, value in changes:
            event = self.development_event()
            event[section][field] = value
            for event_name, ref in (("workflow_dispatch", "refs/heads/feature/example"), ("pull_request", "refs/pull/42/merge")):
                with self.subTest(field=field, value=value, event_name=event_name):
                    self.assertEqual(self.development_scope(event_name, ref, event=event), "")
        for side in ("head", "base"):
            for repository in ({"full_name": "example/sobalink"}, {}, None):
                event = self.development_event()
                event["pull_request"][side]["repo"] = repository
                with self.subTest(side=side, repository=repository):
                    self.assertEqual(self.development_scope("pull_request", "refs/pull/42/merge", event=event), "")
        for number in (None, 0, -1, True, "42", 43):
            event = self.development_event()
            event["number"] = number
            with self.subTest(number=number):
                self.assertEqual(self.development_scope("pull_request", "refs/pull/42/merge", event=event), "")
        for missing in ("repository", "pull_request", "number"):
            event = self.development_event()
            del event[missing]
            self.assertEqual(self.development_scope("pull_request", "refs/pull/42/merge", event=event), "")
        event = self.development_event()
        event["pull_request"]["number"] = 43
        self.assertEqual(self.development_scope("pull_request", "refs/pull/42/merge", event=event), "")

    def test_development_keys_and_fallbacks_stay_inside_their_full_boundary(self):
        restore = self.step("ci", "native", "id: development-go-cache\n")
        prefix = "development-go-v1-webkaz-labs-sobalink-${{ steps.development-go-cache-scope.outputs.scope }}-"
        boundary = prefix + "${{ runner.os }}-${{ runner.arch }}-${{ matrix.runner }}-go1.27.1-${{ hashFiles('go.mod', 'go.sum', 'internal/engineadaptation/**') }}-"
        self.assertEqual(self.key("ci", "native", "development-go-cache"), boundary + "${{ github.sha }}")
        self.assertEqual(re.findall(r"^          restore-keys: (.+)$", restore, re.MULTILINE), [boundary])
        self.assertNotIn("trusted-main-go", restore)
        values = {"steps.development-go-cache-scope.outputs.scope": self.development_scope(),
                  "runner.os": "Linux", "runner.arch": "X64", "matrix.runner": "ubuntu-24.04",
                  "hashFiles('go.mod', 'go.sum', 'internal/engineadaptation/**')": "a" * 64, "github.sha": "b" * 40}

        def render(template, values):
            for expression, value in values.items():
                template = template.replace("${{ " + expression + " }}", value)
            self.assertNotIn("${{", template)
            return template

        old_prefix = render(boundary, values)
        next_commit = dict(values, **{"github.sha": "c" * 40})
        self.assertTrue(render(self.key("ci", "native", "development-go-cache"), next_commit).startswith(old_prefix))
        other_scopes = [self.development_scope(ref="refs/heads/feature/other"),
                        self.development_scope(ref="refs/heads/feature/example-Linux-X64-ubuntu-24.04-go1.27.1-" + "a" * 64),
                        self.development_scope(ref="refs/heads/pr-42"), "pr-42", "pr-43"]
        for expression, alternatives in (("steps.development-go-cache-scope.outputs.scope", other_scopes),
                                         ("runner.os", ["Windows"]), ("runner.arch", ["ARM64"]),
                                         ("matrix.runner", ["ubuntu-24.04-arm", "ubuntu-22.04"]),
                                         ("hashFiles('go.mod', 'go.sum', 'internal/engineadaptation/**')", ["d" * 64])):
            for alternative in alternatives:
                with self.subTest(expression=expression, alternative=alternative):
                    other = dict(values, **{expression: alternative})
                    self.assertFalse(render(self.key("ci", "native", "development-go-cache"), other).startswith(old_prefix))
        self.assertFalse(render(self.key("ci", "native"), values).startswith(old_prefix))
        self.assertFalse(render(self.key("ci", "native", "development-go-cache"), values).replace("go1.27.1-", "go1.27.2-").startswith(old_prefix))

    def test_development_cache_saves_only_after_successful_native_validation(self):
        restore = self.step("ci", "native", "id: development-go-cache\n")
        save = self.step("ci", "native", "name: Save development Go caches after all native checks pass")
        self.assertIn("        if: steps.development-go-cache-scope.outputs.scope != ''\n", restore)
        self.assertIn("        if: success() && steps.development-go-cache-scope.outputs.scope != '' && steps.development-go-cache.outputs.cache-hit != 'true'\n", save)
        self.assertIn("uses: actions/cache/save@" + self.CACHE_PIN, save)
        self.assertIn(self.CACHE_PATHS, save)
        self.assertIn("key: ${{ steps.development-go-cache.outputs.cache-primary-key }}", save)
        self.assertNotIn("trusted-main-go", save)
        native = self.job("ci", "native")
        self.assertGreater(native.index(save), native.index("uses: actions/upload-artifact@"))
        for job in ("manifest-smoke", "browser"):
            self.assertNotIn("development-go-cache", self.job("ci", job))
        self.assertNotIn("development-go", self.workflow("prerelease"))

    def test_cache_reporting_distinguishes_fallback_miss_skip_and_failed_restore(self):
        step = self.step("ci", "native", "name: Report Go cache results\n")
        self.assertIn("        if: always()\n", step)
        source = self.inline_python(step)
        for namespace, cache_id in (("MAIN", "go-cache"), ("DEVELOPMENT", "development-go-cache")):
            for suffix, output in (("OUTCOME", "outcome"), ("PRIMARY_KEY", "outputs.cache-primary-key"), ("MATCHED_KEY", "outputs.cache-matched-key"), ("HIT", "outputs.cache-hit")):
                self.assertIn(namespace + "_CACHE_" + suffix + ": ${{ steps." + cache_id + "." + output + " }}", step)
            for outcome, primary, matched, hit, expected in (("success", "key-new", "key-new", "true", "exact hit"),
                                                           ("success", "key-new", "key-old", "false", "fallback hit"),
                                                           ("success", "key-new", "", "false", "miss"),
                                                           ("success", "key-new", "", "", "miss"),
                                                           ("skipped", "", "", "", "skipped"),
                                                           ("", "", "", "", "skipped"),
                                                           ("failure", "key-new", "", "", "restore failure"),
                                                           ("cancelled", "key-new", "", "", "restore cancelled")):
                with self.subTest(namespace=namespace, outcome=outcome, matched=matched, hit=hit):
                    env = {namespace + "_CACHE_OUTCOME": outcome, namespace + "_CACHE_PRIMARY_KEY": primary,
                           namespace + "_CACHE_MATCHED_KEY": matched, namespace + "_CACHE_HIT": hit}
                    output = io.StringIO()
                    with patch.dict(os.environ, env, clear=True), contextlib.redirect_stdout(output):
                        exec(compile(source, "ci.yml cache report", "exec"), {})
                    self.assertIn(namespace.lower() + " Go cache: " + expected + "\n", output.getvalue())
                    self.assertIn("Action exact-hit output: " + (hit or "unset") + "\n", output.getvalue())
                    self.assertIn("Requested key: " + (primary or "none") + "\nMatched key: " + (matched or "none"), output.getvalue())

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

    def test_development_cache_keeps_existing_triggers_and_browser_gates(self):
        workflow = self.workflow("ci")
        self.assertIn("on:\n  push:\n    branches: [main]\n  pull_request:\n  workflow_dispatch:\n", workflow)
        browser = self.job("ci", "browser")
        for command in ("go mod verify", "go build -tags=soba_e2e", "npm --prefix web run test:browser:safety",
                        "npm --prefix web run test:browser", "git diff --exit-code -- web/dist"):
            step = self.step("ci", "browser", command)
            self.assertNotIn("        if:", step)
            self.assertNotIn("cache-hit", step)
        self.assertNotIn("actions/cache", browser)
        relay = self.step("ci", "native", "name: Verify paired transport")
        self.assertNotIn("cache-hit", relay)
        self.assertIn('"go", "test", "-count=1", "-v"', relay)
        self.assertIn('"-timeout=5m"', relay)
        self.assertIn("timeout=9 * 60", relay)

    def test_full_native_real_time_steps_and_release_are_preserved(self):
        names = ("Verify direct LAN session natural rekey and idle lifecycle",
                 "Verify guarded relay real-time lease continuity",
                 "Verify relay-only real-time lease and idle continuity")
        for name in names:
            step = self.step("ci", "native", "name: " + name)
            self.assertIn("        if: steps.ci-plan.outputs.long_required != 'false'\n", step)
            self.assertNotIn("cache-hit", step)
            self.assertIn("-count=1", step)
        guarded = self.step("ci", "native", "name: Verify guarded relay real-time lease continuity")
        self.assertIn('"go", "test", "-race", "-count=1"', guarded)
        self.assertIn('env["SOBALINK_RUN_GUARDED_INTEGRATION"] = "1"', guarded)
        self.assertIn('"-run=^TestGuardedRelayTwoPeerIntegration$"', guarded)
        self.assertNotIn("ts_omit_udptransport", guarded)
        trusted = self.step("ci", "native", "name: Verify relay-only real-time lease and idle continuity")
        self.assertIn('env["SOBALINK_RUN_LAN_INTEGRATION"] = "1"', trusted)
        self.assertIn('"-run=^(TestTrustedRelayTwoPeerIntegration|TestMultipleRelayPresenceAndFreshClientRecovery)$"', trusted)
        self.assertIn('"./internal/lanlink", "./internal/routecat"', trusted)
        self.assertIn('"--expect", "github.com/webkaz-labs/sobalink/internal/routecat:TestMultipleRelayPresenceAndFreshClientRecovery"', trusted)
        self.assertIn("ts_omit_udptransport", trusted)
        # Prerelease does not consume the impact plan, even for prose-only changes.
        release = self.workflow("prerelease")
        self.assertNotIn("long_required", release)
        self.assertNotIn("ci-impact", release)

    def test_explicit_native_probes_require_actual_go_test_pass_events(self):
        for workflow in ("ci", "prerelease"):
            for title in ("Verify explicit WAN discovery with isolated native STUN",
                          "Verify guarded production direct and relay underlays",
                          "Verify paired transport and Core applications over an isolated relay",
                          "Verify recovery with the ordinary direct-enabled transport"):
                step = self.step(workflow, "native", "name: " + title)
                self.assertIn('sys.executable, ".github/scripts/ci-go-test.py"', step)
                self.assertIn('"--expect", "github.com/webkaz-labs/sobalink/internal/', step)
        for title in ("Verify direct LAN synthetic expiry and rekey",
                      "Verify direct LAN session natural rekey and idle lifecycle",
                      "Verify guarded relay real-time lease continuity",
                      "Verify relay-only real-time lease and idle continuity"):
            step = self.step("ci", "native", "name: " + title)
            self.assertIn("ci-go-test.py", step)
            self.assertIn("--expect", step)
        for workflow, title in (("ci", "Verify direct LAN session natural rekey and idle lifecycle"),
                                ("prerelease", "Verify direct LAN session rekey and idle lifecycle")):
            step = self.step(workflow, "native", "name: " + title)
            for test in ("TestNativeSessionLifecycle/active-through-natural-rekey",
                         "TestNativeSessionLifecycle/idle-through-natural-expiry-new-dial"):
                self.assertIn("--expect github.com/webkaz-labs/sobalink/internal/directlan:" + test, step)

    def test_impact_failure_and_missing_outputs_cannot_skip_native_coverage(self):
        native = self.job("ci", "native")
        self.assertIn("    needs: impact\n", native)
        self.assertIn("if: always() && !cancelled()", native)
        resolve = self.step("ci", "native", "id: ci-plan\n")
        self.assertNotIn("        if:", resolve)
        self.assertIn("ci-coverage.py resolve", resolve)
        workflow = self.workflow("ci")
        self.assertNotIn("paths-ignore:", workflow)
        self.assertNotIn("paths:", workflow)
        self.assertIn("      force_full:\n", workflow)
        self.assertIn("        type: boolean\n        default: true", workflow)
        aggregate = self.job("ci", "ci-required")
        self.assertIn("    needs: [impact, native, browser, go-unit, manifest-smoke]\n", aggregate)
        self.assertIn("    if: always()\n", aggregate)
        self.assertIn("ci-coverage.py finalize", aggregate)
        self.assertNotIn("contents: write", aggregate)
        self.assertNotIn("id-token: write", aggregate)

    def test_minimum_scope_jobs_and_main_concurrency_remain_explicit(self):
        workflow = self.workflow("ci")
        native = self.job("ci", "native")
        for excluded in ("docs", "frontend", "go"):
            self.assertIn("needs.impact.outputs.scope != '" + excluded + "'", native)
        browser = self.job("ci", "browser")
        self.assertIn("needs: impact", browser)
        self.assertIn("needs.impact.outputs.scope != 'docs'", browser)
        self.assertIn("needs.impact.outputs.scope != 'go'", browser)
        self.assertIn("python .github/scripts/check-frontend.py", browser)
        scoped = self.job("ci", "go-unit")
        self.assertIn("if: needs.impact.outputs.scope == 'go'", scoped)
        self.assertIn("runs-on: ubuntu-24.04", scoped)
        self.assertIn("ci-go-scope.py --plan", scoped)
        self.assertIn("ci-coverage.py resolve", scoped)
        self.assertNotIn("trusted-main-go", scoped)
        self.assertIn("format('ci-run-{0}', github.run_id)", workflow)
        self.assertIn("cancel-in-progress: ${{ github.event_name == 'pull_request' }}", workflow)
        for name in ("relay-traffic", "release-resources"):
            self.assertNotRegex(self.workflow(name), r"(?m)^      - docs/")
        release = self.workflow("prerelease")
        self.assertNotIn("ci-go-scope", release)
        self.assertNotIn("needs.impact", release)

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
