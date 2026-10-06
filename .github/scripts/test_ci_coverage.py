"""Offline tests for actual coverage, trusted baselines and conservative fallback."""
import copy
import importlib.util
import io
import json
import os
import pathlib
import tempfile
import unittest
import zipfile
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("ci_coverage", pathlib.Path(__file__).with_name("ci-coverage.py"))
coverage = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(coverage)
HEAD = "a" * 40
TREE = "b" * 40
POLICY = "c" * 64


def step(name, conclusion="success"):
    return {"name": name, "status": "completed", "conclusion": conclusion}


def jobs(full=True):
    result = []
    for target, name in coverage.TARGETS.items():
        steps = [step(s) for s in coverage.FAST_STEPS]
        steps += [step(s, "success" if full else "skipped") for s in coverage.LONG_STEPS]
        if target == "windows-amd64":
            steps.append(step("Verify Windows receive-retirement directory barriers"))
        result.append({"name": name, "status": "completed", "conclusion": "success", "steps": steps})
    result += [
        {"name": "browser", "status": "completed", "conclusion": "success", "steps": [step("Verify actual UI workflows with isolated Playwright fixtures")]},
        {"name": "manifest-smoke", "status": "completed", "conclusion": "success", "steps": [step("Exercise signing and verification offline with a disposable test key")]},
    ]
    return result


class ActualCoverageTests(unittest.TestCase):
    def test_native_port_helper_is_only_imported_by_tests(self):
        root = pathlib.Path(__file__).parents[2]
        imports = []
        for directory in ("cmd", "internal", "web"):
            for path in (root / directory).rglob("*.go"):
                if '"github.com/webkaz-labs/sobalink/internal/testfixture"' in path.read_text(encoding="utf-8"):
                    imports.append(path.relative_to(root).as_posix())
                    self.assertTrue(path.name.endswith("_test.go"), "test fixture leaked into product source: " + str(path.relative_to(root)))
        self.assertTrue(imports, "shared port fixture must have checked test-only consumers")

    def test_timings_only_record_fixed_names_and_elapsed_seconds(self):
        data = jobs()
        data[0].update({"started_at": "2026-01-01T00:00:00Z", "completed_at": "2026-01-01T00:00:10Z", "runner_name": "private-runner"})
        data += [{"name": "private-job", "runner_name": "private-runner"}]
        result = coverage.job_timings(data)
        self.assertEqual(result[0]["elapsed_seconds"], 10)
        self.assertNotIn("private-", json.dumps(result))

    def test_full_receipt_requires_all_targets_and_long_steps(self):
        self.assertTrue(coverage.evaluate_jobs(jobs(), True))

    def test_selected_success_is_explicitly_not_full(self):
        self.assertFalse(coverage.evaluate_jobs(jobs(False), False))

    def test_skipped_long_cannot_claim_full(self):
        with self.assertRaises(ValueError):
            coverage.evaluate_jobs(jobs(False), True)

    def test_every_target_and_each_fast_step_is_required(self):
        for target in range(4):
            for name in coverage.FAST_STEPS:
                data = jobs(False)
                next(s for s in data[target]["steps"] if s["name"] == name)["conclusion"] = "skipped"
                with self.subTest(target=target, step=name), self.assertRaises(ValueError):
                    coverage.evaluate_jobs(data, False)

    def test_each_long_step_must_really_pass_on_each_target(self):
        for target in range(4):
            for name in coverage.LONG_STEPS:
                for bad in ("skipped", "failure", "cancelled", None):
                    data = jobs()
                    next(s for s in data[target]["steps"] if s["name"] == name)["conclusion"] = bad
                    with self.subTest(target=target, step=name, bad=bad), self.assertRaises(ValueError):
                        coverage.evaluate_jobs(data, True)

    def test_missing_duplicate_or_failed_jobs_are_not_green(self):
        for i in range(6):
            data = jobs()
            data.pop(i)
            with self.assertRaises(ValueError):
                coverage.evaluate_jobs(data, False)
            for bad in ("failure", "cancelled", "skipped", None):
                data = jobs()
                data[i]["conclusion"] = bad
                with self.assertRaises(ValueError):
                    coverage.evaluate_jobs(data, False)
        with self.assertRaises(ValueError):
            coverage.evaluate_jobs(jobs() + [jobs()[0]], True)

    def test_windows_barrier_browser_and_manifest_steps_are_required(self):
        for i, needle in ((3, "Verify Windows receive-retirement directory barriers"),
                          (4, "Verify actual UI workflows with isolated Playwright fixtures"),
                          (5, "Exercise signing and verification offline with a disposable test key")):
            data = jobs()
            data[i]["steps"] = [s for s in data[i]["steps"] if s["name"] != needle]
            with self.assertRaises(ValueError):
                coverage.evaluate_jobs(data, True)

    def test_selected_long_failure_or_missing_step_is_rejected(self):
        for bad in ("failure", "cancelled", None):
            data = jobs(False)
            next(s for s in data[0]["steps"] if s["name"] == coverage.LONG_STEPS[0])["conclusion"] = bad
            with self.assertRaises(ValueError):
                coverage.evaluate_jobs(data, False)
        data[0]["steps"] = [s for s in data[0]["steps"] if s["name"] != coverage.LONG_STEPS[0]]
        with self.assertRaises(ValueError):
            coverage.evaluate_jobs(data, False)


class BaselineTests(unittest.TestCase):
    def run_data(self):
        return {"id": 19, "run_attempt": 2, "head_sha": HEAD, "status": "completed", "conclusion": "success",
                "event": "push", "head_branch": "main", "name": "Cross-platform CI", "path": coverage.WORKFLOW,
                "repository": {"full_name": coverage.REPOSITORY}, "head_repository": {"full_name": coverage.REPOSITORY}}

    def receipt(self):
        digest = coverage.hashlib.sha256(b"source").hexdigest()
        return {"schema_version": 1, "repository": coverage.REPOSITORY, "run_id": 19, "run_attempt": 2,
                "head_sha": HEAD, "head_tree": TREE, "policy_sha256": digest, "workflow_sha256": digest,
                "full_native": True, "targets": {t: "success" for t in coverage.TARGETS},
                "browser": "success", "manifest": "success"}

    def validate(self, receipt, run):
        def git(*args):
            return (TREE + "\n").encode() if args[0] == "rev-parse" else b"source"
        with patch.object(coverage, "git", side_effect=git), patch.object(coverage, "digest", return_value=coverage.hashlib.sha256(b"source").hexdigest()):
            return coverage.validate_receipt(receipt, run)

    def test_only_canonical_completed_main_full_coverage_is_a_baseline(self):
        self.validate(self.receipt(), self.run_data())
        for field, bad in (("event", "pull_request"), ("head_branch", "feature"), ("conclusion", "failure"),
                           ("status", "in_progress"), ("path", "other.yml"), ("name", "Other CI"),
                           ("head_sha", "main"), ("repository", {"full_name": "example/other"}),
                           ("head_repository", {"full_name": "example/fork"})):
            run = self.run_data()
            run[field] = bad
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.validate(self.receipt(), run)

    def test_selected_or_wrong_source_receipt_cannot_advance_baseline(self):
        for field, bad in (("full_native", False), ("run_attempt", 1), ("run_id", 20), ("head_sha", "d" * 40),
                           ("head_tree", "e" * 40), ("policy_sha256", "f" * 64), ("workflow_sha256", "f" * 64),
                           ("targets", {"linux-amd64": "success"}), ("browser", "skipped"), ("manifest", "failure")):
            receipt = self.receipt()
            receipt[field] = bad
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.validate(receipt, self.run_data())

    def test_missing_or_non_numeric_attempt_never_matches(self):
        for bad in (None, 0, -1, True, "2"):
            run, receipt = self.run_data(), self.receipt()
            run["run_attempt"] = receipt["run_attempt"] = bad
            with self.subTest(attempt=bad), self.assertRaises(ValueError):
                self.validate(receipt, run)

    def archive(self, names):
        data = io.BytesIO()
        with zipfile.ZipFile(data, "w") as z:
            for name in names:
                z.writestr(name, json.dumps(self.receipt()))
        return data.getvalue()

    def test_receipt_archive_never_extracts_or_accepts_other_files(self):
        listing = {"total_count": 1, "artifacts": [{"id": 7, "name": "ci-coverage", "expired": False, "size_in_bytes": 1000}]}
        for names in (["../ci-coverage.json"], ["ci-coverage.json", "extra.py"], []):
            with patch.object(coverage, "api", side_effect=[listing, self.archive(names)]), self.assertRaises(ValueError):
                coverage.read_receipt(self.run_data())
        with patch.object(coverage, "api", side_effect=[listing, self.archive(["ci-coverage.json"])]):
            self.assertEqual(coverage.read_receipt(self.run_data()), self.receipt())

    def test_missing_expired_oversized_or_incomplete_receipts_are_rejected(self):
        for listing in ({"total_count": 0, "artifacts": []}, {"total_count": 101, "artifacts": []},
                        {"total_count": 2, "artifacts": [{"id": 7, "name": "ci-coverage", "expired": False, "size_in_bytes": 1000}]},
                        {"total_count": True, "artifacts": []}, {"total_count": 0, "artifacts": {}},
                        {"total_count": 1, "artifacts": [{"id": 7, "name": "ci-coverage", "expired": True, "size_in_bytes": 1000}]},
                        {"total_count": 1, "artifacts": [{"id": 7, "name": "ci-coverage", "expired": False, "size_in_bytes": 999999}]}):
            with patch.object(coverage, "api", return_value=listing), self.assertRaises(ValueError):
                coverage.read_receipt(self.run_data())


class ConservativeFallbackTests(unittest.TestCase):
    def plan(self):
        return {"version": 1, "policy_id": "native-long-impact-v1", "long_required": False,
                "head_sha": HEAD, "head_tree": TREE, "policy_sha256": POLICY,
                "baseline_sha": "d" * 40, "baseline_run_id": 7, "reason": "documentation_only"}

    def test_bad_plan_identity_or_missing_provenance_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "plan.json"
            for field, bad in (("version", 2), ("head_sha", "d" * 40), ("head_tree", "e" * 40),
                               ("policy_sha256", "f" * 64), ("long_required", "false"),
                               ("baseline_sha", None), ("baseline_run_id", "7")):
                value = self.plan()
                value[field] = bad
                path.write_text(json.dumps(value))
                with patch.object(coverage, "head", return_value=HEAD), patch.object(coverage, "git", return_value=(TREE + "\n").encode()), patch.object(coverage, "digest", return_value=POLICY), self.assertRaises((ValueError, TypeError)):
                    coverage.read_plan(path)

    def test_missing_or_unreproducible_selection_requires_full_actual_steps(self):
        forced = self.plan() | {"long_required": True, "reason": "forced_full"}
        for read_error in (True, False):
            with patch.object(coverage, "read_plan", side_effect=ValueError() if read_error else None,
                              return_value=self.plan()), patch.object(coverage, "baseline_by_id", return_value={}), \
                 patch.object(coverage, "classify", return_value=forced), patch.object(coverage, "head", return_value=HEAD), \
                 patch.object(coverage, "current_jobs", return_value=jobs(False)), self.assertRaises(ValueError):
                coverage.finalize("absent.json", "unused.json")

    def test_incomplete_job_inventory_cannot_pass(self):
        with patch.dict(os.environ, {"GITHUB_RUN_ID": "19", "GITHUB_RUN_ATTEMPT": "2"}), \
             patch.object(coverage, "api", return_value={"jobs": jobs(), "total_count": 7}), self.assertRaises(ValueError):
            coverage.current_jobs()


if __name__ == "__main__":
    unittest.main()
