"""Offline checks for honest scope receipts and mandatory aggregate failures."""
import copy
import importlib.util
import json
import os
import pathlib
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
    if scope == "full":
        for target, name in coverage.TARGETS.items():
            steps = coverage.FAST_STEPS + coverage.LONG_STEPS
            if target == "windows-amd64":
                steps += ("Verify Windows receive-retirement directory barriers",)
            result.append(job(name, steps))
        result.append(job("manifest-smoke", ("Exercise signing and verification offline with a disposable test key",)))
    else:
        result.extend([job("native", conclusion="skipped"), job("manifest-smoke", conclusion="skipped")])
    result.append(job("browser", coverage.BROWSER_STEPS) if scope in ("full", "frontend")
                  else job("browser", conclusion="skipped"))
    result.append(job("go-unit", coverage.GO_STEPS) if scope == "go"
                  else job("go-unit", conclusion="skipped"))
    return result


def plan(scope="full"):
    return {"version": 2, "policy_id": "minimum-ci-v2", "scope": scope, "reason": "verified_fixture",
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

    def test_selected_scopes_are_never_full(self):
        for scope in ("docs", "frontend", "go"):
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
        for scope in ("docs", "frontend", "go"):
            with self.assertRaises(ValueError):
                coverage.evaluate_jobs(jobs(scope), "full")
        for scope in (None, "", "changed", "FULL"):
            with self.assertRaises(ValueError):
                coverage.evaluate_jobs(jobs(), scope)

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
                for field, bad in (("version", 1), ("policy_id", "old"), ("scope", "skip"), ("head_sha", "0" * 40), ("head_tree", "0" * 40), ("policy_sha256", "0" * 64)):
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
                self.assertEqual(result["schema_version"], 2)
                self.assertEqual(result["scope"], scope)
                self.assertEqual(result["full_native"], scope == "full")
                self.assertEqual(result["targets"], {t: "success" if scope == "full" else "not_run" for t in coverage.TARGETS})
                self.assertEqual(result["browser"], "success" if scope in ("frontend", "full") else "not_run")
                self.assertEqual(result["manifest"], "success" if scope == "full" else "not_run")
                self.assertEqual(result["go_unit"], "success" if scope == "go" else "not_run")
                if scope != "full":
                    self.assertIn("NOT RUN", pathlib.Path(env["GITHUB_STEP_SUMMARY"]).read_text())

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
