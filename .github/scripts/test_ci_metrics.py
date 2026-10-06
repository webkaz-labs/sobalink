"""Content-free timing and command-result preservation checks."""

import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("ci-metrics.py")
SPEC = importlib.util.spec_from_file_location("ci_metrics", SCRIPT)
ci_metrics = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ci_metrics)


class CIMetricsTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.metrics = self.root / "metrics.jsonl"
        self.env = dict(os.environ, SOBALINK_CI_METRICS=str(self.metrics))
        self.env.pop("GITHUB_STEP_SUMMARY", None)

    def invoke(self, *args, **kwargs):
        return subprocess.run([sys.executable, str(SCRIPT), *args], cwd=self.root,
                              env=self.env, capture_output=True, text=True, **kwargs)

    def records(self):
        return [json.loads(line) for line in self.metrics.read_text().splitlines()]

    def test_success_inherits_output_and_records_only_allowlisted_fields(self):
        self.env["FIXTURE_PRIVATE_ENV"] = "environment-fixture-secret"
        secret = "argument-fixture-secret"
        result = self.invoke("run", "--suite", "unit-tests", "--", sys.executable,
                             "-c", "import sys; print('standard output'); print('standard error', file=sys.stderr)",
                             secret)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "standard output\n")
        self.assertEqual(result.stderr, "standard error\n")
        record, = self.records()
        self.assertEqual(set(record), {"suite", "elapsed_seconds", "status", "returncode"})
        self.assertEqual(record["suite"], "unit-tests")
        self.assertEqual(record["status"], "passed")
        self.assertEqual(record["returncode"], 0)
        self.assertGreaterEqual(record["elapsed_seconds"], 0)
        raw = self.metrics.read_text()
        for private in (secret, self.env["FIXTURE_PRIVATE_ENV"], str(self.root),
                        sys.executable, "standard output", "standard error"):
            self.assertNotIn(private, raw)

    def test_failed_command_is_recorded_and_its_status_is_preserved(self):
        result = self.invoke("run", "--suite", "failed-suite", "--", sys.executable,
                             "-c", "import sys; sys.exit(23)")
        self.assertEqual(result.returncode, 23)
        record, = self.records()
        self.assertEqual((record["status"], record["returncode"]), ("failed", 23))

    def test_write_failure_never_overrides_command_result(self):
        self.env["SOBALINK_CI_METRICS"] = str(self.root)
        for code in (0, 19):
            with self.subTest(code=code):
                result = self.invoke("run", "--suite", "write-failure", "--", sys.executable,
                                     "-c", f"import sys; sys.exit({code})")
                self.assertEqual(result.returncode, code)
                self.assertIn("CI timing record could not be written", result.stderr)
                self.assertNotIn(str(self.root), result.stderr)

    def test_write_warning_is_visible_in_github_actions(self):
        self.env.update(GITHUB_ACTIONS="true", SOBALINK_CI_METRICS=str(self.root))
        result = self.invoke("run", "--suite", "write-failure", "--", sys.executable, "-c", "pass")
        self.assertEqual(result.returncode, 0)
        self.assertTrue(result.stderr.startswith("::warning::CI timing record could not be written"))

    def test_command_arguments_are_not_interpreted_by_a_shell(self):
        argument = "; echo unexpected > injected.txt; $(echo expanded)"
        result = self.invoke("run", "--suite", "literal-arguments", "--", sys.executable,
                             "-c", "import sys; print(sys.argv[1])", argument)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, argument + "\n")
        self.assertFalse((self.root / "injected.txt").exists())
        self.assertNotIn(argument, self.metrics.read_text())

    def test_monotonic_clock_is_used(self):
        with mock.patch.object(ci_metrics.time, "monotonic", side_effect=[40.0, 42.125]), \
                mock.patch.object(ci_metrics.subprocess, "run", return_value=mock.Mock(returncode=0)) as run, \
                mock.patch.object(ci_metrics, "append_record") as append:
            self.assertEqual(ci_metrics.run_suite("clock-test", ["fake-command", "literal argument"]), 0)
        run.assert_called_once_with(["fake-command", "literal argument"], check=False, shell=False)
        self.assertEqual(append.call_args.args[0]["elapsed_seconds"], 2.125)

    def test_append_preserves_each_run(self):
        for suite in ("first", "second"):
            result = self.invoke("run", "--suite", suite, "--", sys.executable, "-c", "pass")
            self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([row["suite"] for row in self.records()], ["first", "second"])

    def test_missing_command_has_safe_failure_record(self):
        result = self.invoke("run", "--suite", "missing-command", "--", str(self.root / "missing-secret-command"))
        self.assertEqual(result.returncode, 127)
        self.assertIn("CI suite command was not found", result.stderr)
        self.assertNotIn("missing-secret-command", result.stderr + self.metrics.read_text())
        self.assertEqual(self.records()[0]["returncode"], 127)

    def test_launch_errors_and_interrupts_have_failure_records(self):
        for error, code in ((PermissionError("fixture-secret"), 126), (KeyboardInterrupt(), 130)):
            with self.subTest(error=type(error)), \
                    mock.patch.object(ci_metrics.subprocess, "run", side_effect=error), \
                    mock.patch.object(ci_metrics, "append_record") as append, \
                    mock.patch.object(ci_metrics.sys, "stderr") as stderr:
                self.assertEqual(ci_metrics.run_suite("launch-failure", ["fake-command"]), code)
                self.assertEqual(append.call_args.args[0]["returncode"], code)
                self.assertEqual(append.call_args.args[0]["status"], "failed")
                self.assertNotIn("fixture-secret", str(stderr.write.call_args_list))

    @unittest.skipIf(os.name == "nt", "POSIX signal status")
    def test_child_signal_is_recorded_with_shell_compatible_status(self):
        result = self.invoke("run", "--suite", "signal-test", "--", sys.executable,
                             "-c", "import os, signal; os.kill(os.getpid(), signal.SIGTERM)")
        self.assertEqual(result.returncode, 128 + signal.SIGTERM)
        self.assertEqual(self.records()[0]["returncode"], -signal.SIGTERM)

    def test_summary_writes_json_and_appends_step_summary(self):
        record = {"suite": "unit-tests", "elapsed_seconds": 1.25, "status": "passed", "returncode": 0}
        failed = {"suite": "failed-tests", "elapsed_seconds": 0.5, "status": "failed", "returncode": 23}
        self.metrics.write_text(json.dumps(record) + "\n" + json.dumps(failed) + "\n", encoding="utf-8")
        step = self.root / "step-summary.md"
        step.write_text("Existing summary\n", encoding="utf-8")
        self.env["GITHUB_STEP_SUMMARY"] = str(step)
        artifact = self.root / "artifact" / "metrics.json"
        result = self.invoke("summary", "--output", str(artifact))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(artifact.read_text()), [record, failed])
        self.assertTrue(step.read_text().startswith("Existing summary\n"))
        self.assertIn("| unit-tests | 1.250 | passed | 0 |", step.read_text())
        self.assertIn("| failed-tests | 0.500 | failed | 23 |", step.read_text())
        self.assertEqual(result.stdout, "")

    def test_empty_and_missing_metrics_have_explicit_summary(self):
        for present in (False, True):
            with self.subTest(present=present):
                if present:
                    self.metrics.write_text("", encoding="utf-8")
                artifact = self.root / "empty.json"
                result = self.invoke("summary", "--output", str(artifact))
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("No suite timings were recorded", result.stdout)
                self.assertEqual(json.loads(artifact.read_text()), [])

    def test_malformed_or_private_input_fails_before_writing_outputs(self):
        valid = {"suite": "unit-tests", "elapsed_seconds": 1.25, "status": "passed", "returncode": 0}
        bad_records = [None, [], {**valid, "argv": ["fixture-secret"]},
                       {**valid, "suite": "bad|markdown"}, {**valid, "suite": "bad\nidentifier"},
                       {**valid, "elapsed_seconds": -1}, {**valid, "elapsed_seconds": True},
                       {**valid, "elapsed_seconds": float("nan")}, {**valid, "elapsed_seconds": float("inf")},
                       {**valid, "elapsed_seconds": 10 ** 400}, {**valid, "returncode": True},
                       {**valid, "returncode": 1}, {**valid, "status": "unknown"}]
        bad_sources = [json.dumps(record) for record in bad_records] + ["not-json-fixture-secret", "",
                       "[" * 1100 + "]" * 1100,
                       '{"suite":"first","suite":"second","elapsed_seconds":0,"status":"passed","returncode":0}']
        step = self.root / "step.md"
        self.env["GITHUB_STEP_SUMMARY"] = str(step)
        artifact = self.root / "invalid.json"
        for bad in bad_sources:
            with self.subTest(bad=bad):
                self.metrics.write_text(json.dumps(valid) + "\n" + bad + "\n", encoding="utf-8")
                result = self.invoke("summary", "--output", str(artifact))
                self.assertEqual(result.returncode, 1)
                self.assertIn("malformed", result.stderr)
                self.assertNotIn("fixture-secret", result.stdout + result.stderr)
                self.assertFalse(artifact.exists())
                self.assertFalse(step.exists())

    def test_default_paths_stay_in_runner_temp_or_ignored_build_directory(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertEqual(ci_metrics.metrics_path(), Path(".build/sobalink-ci-metrics.jsonl"))
            os.environ["RUNNER_TEMP"] = str(self.root)
            self.assertEqual(ci_metrics.metrics_path(), self.root / "sobalink-ci-metrics.jsonl")
            os.environ["SOBALINK_CI_METRICS"] = str(self.metrics)
            self.assertEqual(ci_metrics.metrics_path(), self.metrics)

    def test_invalid_suite_and_missing_command_do_not_run(self):
        for args in (("run", "--suite", "unsafe|suite", "--", sys.executable, "-c", "pass"),
                     ("run", "--suite", "valid", "--")):
            with self.subTest(args=args):
                result = self.invoke(*args)
                self.assertEqual(result.returncode, 2)
                self.assertFalse(self.metrics.exists())


if __name__ == "__main__":
    unittest.main()
