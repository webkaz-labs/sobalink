"""Offline checks for honest scope receipts and mandatory aggregate failures."""
import ast
import copy
import importlib.util
import json
import os
import pathlib
import re
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("ci_coverage", pathlib.Path(__file__).with_name("ci-coverage.py"))
coverage = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(coverage)
HEAD, TREE, POLICY = "a" * 40, "b" * 40, "c" * 64


def step(name, conclusion="success"):
    return {"name": name, "status": "completed", "conclusion": conclusion}


def job(name, steps=(), conclusion="success"):
    return {"name": name, "status": "completed", "conclusion": conclusion,
            "steps": [step(s) for s in steps]}


def jobs(scope="full"):
    result = [job("impact", (coverage.SCOPE_STEP,))]
    if scope in ("native-short", "full"):
        for target, name in coverage.TARGETS.items():
            steps = coverage.FAST_STEPS + coverage.LONG_STEPS
            if target == "windows-amd64":
                steps += ("Verify Windows receive-retirement directory barriers",)
            result.append(job(name, steps))
            if scope == "native-short":
                for item in result[-1]["steps"]:
                    if item["name"] in coverage.LONG_STEPS:
                        item["conclusion"] = "skipped"
        result.append(job("manifest-smoke", ("Exercise signing and verification offline with a disposable test key",)))
    else:
        result.extend([job("native", conclusion="skipped"), job("manifest-smoke", conclusion="skipped")])
    result.append(job("browser", coverage.BROWSER_STEPS) if scope in ("full", "native-short", "frontend")
                  else job("browser", conclusion="skipped"))
    result.append(job("go-unit", coverage.GO_STEPS) if scope == "go"
                  else job("go-unit", conclusion="skipped"))
    return result


def plan(scope="full"):
    return {"version": 3, "policy_id": "minimum-ci-v3", "scope": scope, "reason": "verified_fixture",
            "head_sha": HEAD, "head_tree": TREE, "policy_sha256": POLICY,
            "base_sha": "d" * 40, "changed_paths": [], "go_packages": []}


class ActualCoverageTests(unittest.TestCase):
    def test_full_requires_all_four_targets_and_real_time_steps(self):
        self.assertTrue(coverage.evaluate_jobs(jobs(), "full"))
        for target, name in coverage.TARGETS.items():
            for required in coverage.FAST_STEPS + coverage.LONG_STEPS:
                for bad in ("skipped", "failure", "cancelled", None):
                    data = jobs()
                    target_job = next(j for j in data if j["name"] == name)
                    next(s for s in target_job["steps"] if s["name"] == required)["conclusion"] = bad
                    with self.subTest(target=target, required=required, bad=bad), self.assertRaises(ValueError):
                        coverage.evaluate_jobs(data, "full")

    def test_managed_acceptance_steps_are_explicit_and_required(self):
        required_names = ('Verify managed session tls', 'Verify managed session outbound-tcp', 'Verify managed session native-caller', 'Verify managed session native-simultaneous', 'Verify managed session native-udp-capacity', 'Verify managed activation acceptance', 'Verify managed restart acceptance')
        workflow = (pathlib.Path(__file__).resolve().parents[1] / 'workflows/ci.yml').read_text(encoding='utf-8')
        for required in required_names:
            self.assertEqual(coverage.FAST_STEPS.count(required), 1)
            self.assertEqual(workflow.count('      - name: ' + required + '\n'), 1)
            for target, name in coverage.TARGETS.items():
                for mode in ('missing', 'skipped'):
                    data = jobs()
                    target_job = next(j for j in data if j['name'] == name)
                    if mode == 'missing':
                        target_job['steps'] = [s for s in target_job['steps'] if s['name'] != required]
                    else:
                        next(s for s in target_job['steps'] if s['name'] == required)['conclusion'] = 'skipped'
                    with self.subTest(target=target, step=required, mode=mode), self.assertRaises(ValueError):
                        coverage.evaluate_jobs(data, 'full')

    def test_schedule_workflow_routes_full_and_preserves_concurrency(self):
        workflow = (pathlib.Path(__file__).parents[1] / "workflows/ci.yml").read_text(encoding="utf-8")
        self.assertIn("  schedule:\n    # Daily 03:00 JST (UTC+09:00); changes to cadence require review.\n    - cron: '0 18 * * *'", workflow)
        self.assertIn("cancel-in-progress: ${{ github.event_name == 'pull_request' }}", workflow)
        self.assertIn("|| format('ci-run-{0}', github.run_id)", workflow)
        self.assertIn("needs: [impact, native, browser, go-unit, manifest-smoke]", workflow)
        self.assertIn("needs: [ci-required, web-activation, product-activation]", workflow)
        nightly = workflow.split("  nightly-full-check:\n", 1)[1]
        self.assertIn("ci-coverage.py nightly-finalize", nightly)
        self.assertIn("contents: read", nightly)
        self.assertIn("actions: read", nightly)
        self.assertNotIn(": write", nightly)
        values = {"github.event.repository.private": False, "github.event_name": "schedule",
                  "github.event.pull_request.head.repo.full_name": "",
                  "github.repository": coverage.REPOSITORY, "github.ref": "refs/heads/main",
                  "github.event.repository.default_branch": "main", "inputs.force_full": False,
                  "needs.impact.outputs.scope": "full"}

        def enabled(name, overrides=None):
            # Job-level fields are indented four spaces, so extract until next
            # two-space job header rather than nested steps.
            block = re.split(r"\n  [a-z][a-z-]*:", workflow.split("  " + name + ":\n", 1)[1])[0]
            condition = re.search(r"^    if: (.+)$", block, re.M).group(1)
            for key, value in sorted((values | (overrides or {})).items(), key=lambda item: -len(item[0])):
                condition = condition.replace(key, repr(value))
            condition = condition.replace("always()", "True").replace("cancelled()", "False")
            condition = condition.replace("false", "False").replace("&&", " and ").replace("||", " or ")
            condition = re.sub(r"!(?!=)", " not ", condition).strip()
            expression = ast.parse(condition, mode="eval")
            allowed = (ast.Expression, ast.BoolOp, ast.UnaryOp, ast.Compare, ast.Constant,
                       ast.And, ast.Or, ast.Not, ast.Eq, ast.NotEq)
            self.assertTrue(all(isinstance(node, allowed) for node in ast.walk(expression)))
            return eval(compile(expression, "<workflow condition>", "eval"), {"__builtins__": {}})

        for name in ("native", "browser", "web-activation", "product-activation", "nightly-full-check"):
            self.assertTrue(enabled(name), name)
            self.assertFalse(enabled(name, {"github.event.repository.private": True}), name)
        for name in (*coverage.SCHEDULE_ACCEPTANCE, "nightly-full-check"):
            for key, wrong in (("github.repository", "example/fork"),
                               ("github.ref", "refs/heads/topic"),
                               ("github.event.repository.default_branch", "other")):
                self.assertFalse(enabled(name, {key: wrong}), (name, key))
            self.assertFalse(enabled(name, {"github.event_name": "push"}), name)
            self.assertEqual(enabled(name, {"github.event_name": "workflow_dispatch", "inputs.force_full": True}), name != "nightly-full-check")
            if name == "nightly-full-check":
                self.assertFalse(enabled(name, {"github.event_name": "pull_request"}))
        self.assertEqual(workflow.count("if: steps.ci-plan.outputs.long_required != 'false'"), 3)
        self.assertIn("  manifest-smoke:\n    needs: native\n", workflow)
        self.assertEqual(set(coverage.TARGETS), {"linux-amd64", "linux-arm64", "darwin-arm64", "windows-amd64"})

    def test_schedule_requires_full_and_both_complete_acceptance_jobs(self):
        data = jobs() + [job("ci-required", ("Require every check selected for this change",))] + [job(name, steps) for name, steps in coverage.SCHEDULE_ACCEPTANCE.items()]
        self.assertTrue(coverage.evaluate_jobs(data, "full", scheduled=True))
        for scope in ("docs", "frontend", "go", "native-short"):
            with self.subTest(scope=scope), self.assertRaises(ValueError):
                coverage.evaluate_jobs(data, scope, scheduled=True)
        for index, required in enumerate(data):
            if required["conclusion"] == "skipped":
                continue
            missing = copy.deepcopy(data)
            missing.pop(index)
            with self.subTest(missing=required["name"]), self.assertRaises(ValueError):
                coverage.evaluate_jobs(missing, "full", scheduled=True)
            for bad in ("skipped", "failure", "cancelled", None):
                invalid = copy.deepcopy(data)
                invalid[index]["conclusion"] = bad
                with self.subTest(job=required["name"], bad=bad), self.assertRaises(ValueError):
                    coverage.evaluate_jobs(invalid, "full", scheduled=True)
                for step_index in range(len(required["steps"])):
                    invalid = copy.deepcopy(data)
                    invalid[index]["steps"][step_index]["conclusion"] = bad
                    with self.subTest(job=required["name"], step=step_index, bad=bad), self.assertRaises(ValueError):
                        coverage.evaluate_jobs(invalid, "full", scheduled=True)
            for step_index in range(len(required["steps"])):
                invalid = copy.deepcopy(data)
                invalid[index]["steps"].pop(step_index)
                with self.subTest(job=required["name"], missing_step=step_index), self.assertRaises(ValueError):
                    coverage.evaluate_jobs(invalid, "full", scheduled=True)

    def test_selected_scopes_are_never_full(self):
        for scope in ("docs", "frontend", "go", "native-short"):
            with self.subTest(scope=scope):
                self.assertFalse(coverage.evaluate_jobs(jobs(scope), scope))

    def test_actual_required_job_steps_cannot_be_missing_failed_or_skipped(self):
        for scope in coverage.SCOPES:
            data = jobs(scope)
            for index, required in enumerate(data):
                if required["conclusion"] == "skipped":
                    continue
                missing = copy.deepcopy(data)
                missing.pop(index)
                with self.subTest(scope=scope, missing=required["name"]), self.assertRaises(ValueError):
                    coverage.evaluate_jobs(missing, scope)
                for bad in ("failure", "cancelled", "skipped", None):
                    invalid = copy.deepcopy(data)
                    invalid[index]["conclusion"] = bad
                    with self.subTest(scope=scope, job=required["name"], bad=bad), self.assertRaises(ValueError):
                        coverage.evaluate_jobs(invalid, scope)
                for s in range(len(required["steps"])):
                    invalid = copy.deepcopy(data)
                    invalid[index]["steps"].pop(s)
                    with self.subTest(scope=scope, job=required["name"], step=s), self.assertRaises(ValueError):
                        coverage.evaluate_jobs(invalid, scope)

    def test_documentation_scope_does_not_claim_application_execution(self):
        self.assertFalse(coverage.evaluate_jobs([jobs("docs")[0]], "docs"))
        self.assertFalse(coverage.evaluate_jobs(jobs("docs"), "docs"))
        for name in (*coverage.TARGETS.values(), "native", "browser", "manifest-smoke", "go-unit"):
            for bad in ("failure", "cancelled", "success", None):
                data = [jobs("docs")[0], job(name, conclusion=bad)]
                with self.subTest(name=name, bad=bad), self.assertRaises(ValueError):
                    coverage.evaluate_jobs(data, "docs")

    def test_duplicate_jobs_or_named_steps_fail(self):
        for scope in coverage.SCOPES:
            data = jobs(scope)
            with self.assertRaises(ValueError):
                coverage.evaluate_jobs(data + [data[0]], scope)
            data[0]["steps"].append(copy.deepcopy(data[0]["steps"][0]))
            with self.assertRaises(ValueError):
                coverage.evaluate_jobs(data, scope)

    def test_full_rejects_selected_jobs_and_unknown_scope(self):
        for scope in ("docs", "frontend", "go", "native-short"):
            with self.assertRaises(ValueError):
                coverage.evaluate_jobs(jobs(scope), "full")
        for scope in (None, "", "changed", "FULL"):
            with self.assertRaises(ValueError):
                coverage.evaluate_jobs(jobs(), scope)

    def test_native_short_requires_exact_skips_and_never_full_credit(self):
        self.assertFalse(coverage.evaluate_jobs(jobs("native-short"), "native-short"))
        with self.assertRaises(ValueError):
            coverage.evaluate_jobs(jobs("native-short"), "full")
        for target in coverage.TARGETS.values():
            for name in coverage.LONG_STEPS:
                for bad in ("success", "failure", "cancelled", None):
                    data = jobs("native-short")
                    selected = next(j for j in data if j["name"] == target)
                    next(item for item in selected["steps"] if item["name"] == name)["conclusion"] = bad
                    with self.subTest(target=target, step=name, bad=bad), self.assertRaises(ValueError):
                        coverage.evaluate_jobs(data, "native-short")

    def test_native_short_plan_disables_only_real_time_steps(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "outputs"
            env = {"GITHUB_OUTPUT": str(output)}
            with patch.dict(os.environ, env, clear=True):
                coverage.write_plan(plan("native-short"), pathlib.Path(directory) / "plan.json")
            self.assertEqual(output.read_text(encoding="utf-8"), "scope=native-short\nlong_required=false\n")
        workflow = (pathlib.Path(__file__).parents[1] / "workflows/ci.yml").read_text(encoding="utf-8")
        self.assertEqual(workflow.count("if: steps.ci-plan.outputs.long_required != 'false'"), 3)
        self.assertIn("scope != 'docs' && needs.impact.outputs.scope != 'frontend' && needs.impact.outputs.scope != 'go'", workflow)
        self.assertIn("success() && steps.ci-plan.outputs.long_required != 'false'", workflow)
        for name in ("Verify Core context control over pinned TLS", "Verify context control over fixed loopback TCP"):
            self.assertIn(name, coverage.FAST_STEPS)

    def test_native_short_plan_reads_are_locale_independent(self):
        original = pathlib.Path.read_text
        def explicit_utf8(path, *args, **kwargs):
            self.assertEqual(kwargs.get("encoding"), "utf-8")
            return original(path, *args, **kwargs)
        with patch.object(pathlib.Path, "read_text", autospec=True, side_effect=explicit_utf8):
            self.test_native_short_plan_disables_only_real_time_steps()

    def test_timings_only_record_fixed_names(self):
        data = jobs()
        data[0].update(started_at="2026-01-01T00:00:00Z", completed_at="2026-01-01T00:00:10Z", runner_name="private-runner")
        data.append({"name": "private-job", "runner_name": "private-runner"})
        result = coverage.job_timings(data)
        self.assertEqual(result[0]["elapsed_seconds"], 10)
        self.assertNotIn("private-", json.dumps(result))

    def test_native_port_helper_remains_test_only(self):
        root = pathlib.Path(__file__).parents[2]
        imports = []
        for directory in ("cmd", "internal", "web"):
            for path in (root / directory).rglob("*.go"):
                if '"github.com/webkaz-labs/sobalink/internal/testfixture"' in path.read_text(encoding="utf-8"):
                    imports.append(path)
                    self.assertTrue(path.name.endswith("_test.go"))
        self.assertTrue(imports)


class PlanAndReceiptTests(unittest.TestCase):
    def test_plan_must_match_checkout_tree_policy_and_schema(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "plan.json"
            with patch.object(coverage, "head", return_value=HEAD), patch.object(coverage, "git", return_value=TREE.encode()), patch.object(coverage, "digest", return_value=POLICY):
                path.write_text(json.dumps(plan("docs")))
                self.assertEqual(coverage.read_plan(path), plan("docs"))
                for field, bad in (("version", 1), ("version", 2), ("policy_id", "old"), ("scope", "skip"), ("head_sha", "0" * 40), ("head_tree", "0" * 40), ("policy_sha256", "0" * 64)):
                    changed = plan("docs")
                    changed[field] = bad
                    path.write_text(json.dumps(changed))
                    with self.subTest(field=field), self.assertRaises(ValueError):
                        coverage.read_plan(path)

    def test_artifact_cannot_override_recomputed_event_delta(self):
        with patch.object(coverage, "read_plan", return_value=plan("docs")), patch.object(coverage, "head", return_value=HEAD), patch.object(coverage, "classify", return_value=plan("full")) as classifier:
            self.assertEqual(coverage.verified_plan("ignored"), plan("full"))
            self.assertTrue(classifier.call_args.kwargs["force"])

    def test_missing_plan_conservatively_requires_full(self):
        with patch.object(coverage, "read_plan", side_effect=OSError), patch.object(coverage, "head", return_value=HEAD), patch.object(coverage, "classify", return_value=plan("full")) as classifier:
            coverage.verified_plan("missing")
            classifier.assert_called_once_with(HEAD, force=True)

    def test_current_event_proof_needs_no_previous_receipt_or_network(self):
        with patch.object(coverage, "read_plan", return_value=plan("docs")), patch.object(coverage, "head", return_value=HEAD), patch.object(coverage, "classify", return_value=plan("docs")), patch.object(coverage, "api", side_effect=AssertionError("must not query old runs")):
            self.assertEqual(coverage.verified_plan("plan"), plan("docs"))

    def test_receipts_distinguish_not_run_from_success(self):
        for scope in coverage.SCOPES:
            with self.subTest(scope=scope), tempfile.TemporaryDirectory() as directory:
                output = pathlib.Path(directory) / "receipt.json"
                env = {"GITHUB_RUN_ID": "1", "GITHUB_RUN_ATTEMPT": "2", "GITHUB_STEP_SUMMARY": str(pathlib.Path(directory) / "summary")}
                with patch.dict(os.environ, env, clear=True), patch.object(coverage, "verified_plan", return_value=plan(scope)), patch.object(coverage, "current_jobs", return_value=jobs(scope)), patch.object(coverage, "head", return_value=HEAD), patch.object(coverage, "git", return_value=TREE.encode()), patch.object(coverage, "digest", return_value=POLICY):
                    coverage.finalize("plan", output)
                result = json.loads(output.read_text())
                self.assertEqual(result["schema_version"], 3)
                self.assertEqual(result["scope"], scope)
                self.assertEqual(result["full_native"], scope == "full")
                self.assertEqual(result["targets"], {t: "success" if scope == "full" else "short_checks_passed" if scope == "native-short" else "not_run" for t in coverage.TARGETS})
                self.assertEqual(result["browser"], "success" if scope in ("frontend", "native-short", "full") else "not_run")
                self.assertEqual(result["manifest"], "success" if scope in ("native-short", "full") else "not_run")
                self.assertEqual(result["long_checks"], {t: "success" if scope == "full" else "not_run" for t in coverage.TARGETS})
                self.assertEqual(result["go_unit"], "success" if scope == "go" else "not_run")
                if scope != "full":
                    self.assertIn("NOT RUN", pathlib.Path(env["GITHUB_STEP_SUMMARY"]).read_text())

    def test_nightly_command_and_receipt_are_separate_from_normal_aggregate(self):
        for command, scheduled in (("finalize", False), ("nightly-finalize", True)):
            with patch("sys.argv", ["ci-coverage.py", command, "--input", "plan", "--output", "receipt"]), patch.object(coverage, "finalize") as finalize:
                coverage.main()
                finalize.assert_called_once_with("plan", "receipt", scheduled=scheduled)
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "receipt.json"
            data = jobs() + [job("ci-required", ("Require every check selected for this change",))] + [job(name, steps) for name, steps in coverage.SCHEDULE_ACCEPTANCE.items()]
            env = {"GITHUB_EVENT_NAME": "schedule", "GITHUB_RUN_ID": "1", "GITHUB_RUN_ATTEMPT": "1"}
            with patch.dict(os.environ, env, clear=True), patch.object(coverage, "verified_plan", return_value=plan("full")), patch.object(coverage, "current_jobs", return_value=data), patch.object(coverage, "head", return_value=HEAD), patch.object(coverage, "git", return_value=TREE.encode()), patch.object(coverage, "digest", return_value=POLICY):
                coverage.finalize("plan", output, scheduled=True)
                result = json.loads(output.read_text())
                self.assertTrue(result["full_native"])
                self.assertEqual(result["scheduled_acceptance"], {name: "success" for name in coverage.SCHEDULE_ACCEPTANCE})
            with patch.dict(os.environ, {"GITHUB_EVENT_NAME": "push"}, clear=True), self.assertRaisesRegex(ValueError, "schedule event"):
                coverage.finalize("plan", output, scheduled=True)
            env = {"GITHUB_OUTPUT": str(pathlib.Path(directory) / "output")}
            with patch.dict(os.environ, env, clear=True):
                coverage.write_plan(plan("full"), pathlib.Path(directory) / "plan.json")
            self.assertEqual(pathlib.Path(env["GITHUB_OUTPUT"]).read_text(), "scope=full\nlong_required=true\n")

    def test_schedule_finalize_cannot_write_receipt_without_acceptance(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "receipt.json"
            with patch.dict(os.environ, {"GITHUB_EVENT_NAME": "schedule"}, clear=True), patch.object(coverage, "verified_plan", return_value=plan("full")), patch.object(coverage, "current_jobs", return_value=jobs()):
                with self.assertRaisesRegex(ValueError, "ci-required"):
                    coverage.finalize("plan", output, scheduled=True)
            self.assertFalse(output.exists())

    def test_failed_required_coverage_does_not_write_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "receipt.json"
            with patch.object(coverage, "verified_plan", return_value=plan("full")), patch.object(coverage, "current_jobs", return_value=jobs("docs")):
                with self.assertRaises(ValueError):
                    coverage.finalize("plan", output)
            self.assertFalse(output.exists())

    def test_current_attempt_inventory_rejects_truncation(self):
        with patch.dict(os.environ, {"GITHUB_RUN_ID": "1", "GITHUB_RUN_ATTEMPT": "2"}), patch.object(coverage, "api", return_value={"jobs": jobs(), "total_count": 100}) as api:
            with self.assertRaises(ValueError):
                coverage.current_jobs()
            self.assertIn("/attempts/2/", api.call_args.args[0])


if __name__ == "__main__":
    unittest.main()
