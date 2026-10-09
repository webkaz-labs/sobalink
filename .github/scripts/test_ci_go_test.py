"""Required Go tests must actually pass, even when go test exits zero."""

import importlib.util
import io
import json
from pathlib import Path
import subprocess
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("ci-go-test.py")
SPEC = importlib.util.spec_from_file_location("ci_go_test", SCRIPT)
ci_go_test = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ci_go_test)
PACKAGE = "example.org/project/pkg"
TEST = "TestRequired"
EXPECTED = (PACKAGE, TEST)


def event(action, test=TEST, package=PACKAGE, **fields):
    record = {"Action": action, "Package": package}
    if test is not None:
        record["Test"] = test
    record.update(fields)
    return record


class CIGoTestTests(unittest.TestCase):
    def invoke(self, events, expected=(EXPECTED,), code=0, command=None, *, exact=False):
        raw = b"".join(item if isinstance(item, bytes) else
                       (json.dumps(item) + "\n").encode() for item in events)
        process = mock.Mock(stdout=io.BytesIO(raw))
        process.wait.return_value = code
        stdout, stderr = io.StringIO(), io.StringIO()
        with mock.patch.object(ci_go_test.subprocess, "Popen", return_value=process) as launch, \
                mock.patch.object(ci_go_test.sys, "stdout", stdout), \
                mock.patch.object(ci_go_test.sys, "stderr", stderr):
            result = ci_go_test.run_tests(expected, command or ["go", "test", "-race", "-count=1", "./pkg"], exact=exact)
        return result, stdout.getvalue(), stderr.getvalue(), launch

    def test_pass_streams_only_output_and_inherits_stderr(self):
        result, stdout, stderr, launch = self.invoke([
            event("start", None), event("run"), event("output", Output="test output\n"),
            event("pass", Elapsed=0.1), event("pass", None),
        ])
        self.assertEqual((result, stdout, stderr), (0, "test output\n", ""))
        launch.assert_called_once_with(
            ["go", "test", "-json", "-race", "-count=1", "./pkg"],
            stdout=subprocess.PIPE, stderr=None, shell=False)

    def test_missing_test_and_zero_matching_tests_fail(self):
        for events in ([], [event("pass", None)], [event("pass", "TestOther")],
                       [event("run"), event("pass", None)]):
            with self.subTest(events=events):
                result, _, stderr, _ = self.invoke(events)
                self.assertEqual(result, 1)
                self.assertIn("did not all pass", stderr)

    def test_exact_package_and_test_are_required(self):
        for events in ([event("pass", package="example.org/other/pkg")],
                       [event("pass", "TestRequiredSuffix")],
                       [event("pass", "TestRequired/child")]):
            with self.subTest(events=events):
                self.assertEqual(self.invoke(events)[0], 1)

    def test_each_of_multiple_expected_tests_must_pass(self):
        expected = (EXPECTED, (PACKAGE, "TestSecond"), ("example.org/other/pkg", TEST))
        events = [event("pass"), event("pass", "TestSecond"),
                  event("pass", package="example.org/other/pkg"),
                  event("pass", None), event("pass", None, package="example.org/other/pkg")]
        self.assertEqual(self.invoke(events, expected)[0], 0)
        for missing in range(len(events)):
            self.assertEqual(self.invoke(events[:missing] + events[missing + 1:], expected)[0], 1)

    def test_skip_and_fail_are_rejected_even_with_exit_zero(self):
        for action in ("skip", "fail"):
            for events in ([event(action)], [event("pass"), event(action)],
                           [event(action), event("pass")]):
                with self.subTest(action=action, events=events):
                    self.assertEqual(self.invoke(events + [event("pass", None)])[0], 1)

    def test_parent_pass_never_covers_a_requested_subtest(self):
        expected = (EXPECTED, (PACKAGE, TEST + "/required"))
        for events in ([event("pass")],
                       [event("skip", TEST + "/required"), event("pass")],
                       [event("pass", TEST + "/other"), event("pass")]):
            with self.subTest(events=events):
                self.assertEqual(self.invoke(events + [event("pass", None)], expected)[0], 1)
        self.assertEqual(self.invoke([event("pass", TEST + "/required"), event("pass"),
                                      event("pass", None)], expected)[0], 0)

    def test_unrequested_skipped_subtest_does_not_fail_parent_requirement(self):
        self.assertEqual(self.invoke([event("skip", TEST + "/optional"), event("pass"),
                                      event("pass", None)])[0], 0)

    def test_repeated_pass_events_and_duplicate_expectations_are_allowed(self):
        events = [event("run"), event("pass"), event("run"), event("pass"), event("pass", None)]
        self.assertEqual(self.invoke(events, (EXPECTED, EXPECTED))[0], 0)

    def test_exact_requires_one_complete_execution_without_extras(self):
        expected = (EXPECTED, (PACKAGE, "TestSecond"))
        events = [event("start", None), event("run"), event("pass"),
                  event("run", "TestSecond"), event("pass", "TestSecond"), event("pass", None)]
        self.assertEqual(self.invoke(events, expected, exact=True)[0], 0)
        for missing in range(len(events)):
            with self.subTest(missing=missing):
                self.assertEqual(self.invoke(events[:missing] + events[missing + 1:], expected, exact=True)[0], 1)
        for duplicate in range(len(events)):
            repeated = events[:duplicate] + [events[duplicate]] + events[duplicate:]
            with self.subTest(duplicate=duplicate):
                self.assertEqual(self.invoke(repeated, expected, exact=True)[0], 1)

    def test_exact_rejects_any_extra_test_package_subtest_or_skip(self):
        good = [event("start", None), event("run"), event("pass"), event("pass", None)]
        for action in ("run", "pass", "skip", "output"):
            for test, package in (("TestOther", PACKAGE), (TEST + "/extra", PACKAGE),
                                  (TEST, "example.org/other/pkg"), (None, "example.org/other/pkg")):
                extra = event(action, test, package, **({"Output": "fixture\n"} if action == "output" else {}))
                with self.subTest(action=action, test=test, package=package):
                    self.assertEqual(self.invoke(good[:-1] + [extra] + good[-1:], exact=True)[0], 1)
        for test in (TEST, None):
            self.assertEqual(self.invoke(good[:-1] + [event("skip", test)] + good[-1:], exact=True)[0], 1)

    def test_exact_rejects_duplicate_expectations_without_launching(self):
        result, _, _, launch = self.invoke([], (EXPECTED, EXPECTED), exact=True)
        self.assertEqual(result, 2)
        launch.assert_not_called()

    def test_exact_rejects_terminal_events_before_their_execution(self):
        for events in ([event("start", None), event("pass"), event("run"), event("pass", None)],
                       [event("run"), event("start", None), event("pass"), event("pass", None)],
                       [event("start", None), event("pass", None), event("run"), event("pass")]):
            with self.subTest(events=events):
                self.assertEqual(self.invoke(events, exact=True)[0], 1)

    def test_exact_preserves_interleaved_runs_and_build_metadata(self):
        expected = (EXPECTED, (PACKAGE, "TestSecond"))
        events = [{"Action": "build-output", "ImportPath": "example.org/dependency", "Output": "build\n"},
                  event("start", None), event("run"), event("pause"), event("run", "TestSecond"),
                  event("cont"), event("pass", "TestSecond"), event("pass"), event("pass", None)]
        self.assertEqual(self.invoke(events, expected, exact=True)[0], 0)

    def test_required_packages_must_finish_with_a_pass(self):
        for events in ([event("pass")], [event("pass"), event("skip", None)],
                       [event("pass"), event("skip", None), event("pass", None)]):
            with self.subTest(events=events):
                self.assertEqual(self.invoke(events)[0], 1)

    def test_interleaved_parallel_events_do_not_change_exact_matching(self):
        expected = (EXPECTED, (PACKAGE, "TestSecond"))
        events = [event("run"), event("pause"), event("run", "TestSecond"),
                  event("pause", "TestSecond"), event("cont"), event("cont", "TestSecond"),
                  event("pass", "TestSecond"), event("pass"), event("pass", None)]
        self.assertEqual(self.invoke(events, expected)[0], 0)

    def test_malformed_json_cannot_be_hidden_by_a_later_pass(self):
        for bad in (b"not-json secret-fixture\n", b"\n", b"[]\n", b"null\n",
                    b'{"Action":"pass",', b"\xff\n", b'{}\n',
                    b'{"Action":"pass","Action":"skip","Package":"example.org/project/pkg","Test":"TestRequired"}\n',
                    b'{"Action":"pass","Package":"example.org/project/pkg","Test":"TestRequired","Elapsed":NaN}\n'):
            with self.subTest(bad=bad):
                result, stdout, stderr, _ = self.invoke([bad, event("pass"), event("pass", None)])
                self.assertEqual(result, 1)
                self.assertEqual(stdout, "")
                self.assertIn("malformed", stderr)
                self.assertNotIn("secret-fixture", stderr)

    def test_malformed_event_fields_are_rejected(self):
        for bad in (event("pass", package=None), event("pass", test=42),
                    event("pass", Output=[]), event("output"), event("pass", Elapsed=-1),
                    event("pass", Elapsed=True), event("pass", Elapsed=1e1000),
                    event("pass", Elapsed=10 ** 1000), event("unknown"),
                    {"Action": [], "Package": PACKAGE}):
            with self.subTest(bad=bad):
                result, _, stderr, _ = self.invoke([bad, event("pass"), event("pass", None)])
                self.assertEqual(result, 1)
                self.assertIn("malformed", stderr)

    def test_build_output_and_newer_go_metadata_events_are_supported(self):
        events = [{"Action": "build-output", "ImportPath": PACKAGE, "Output": "build output\n"},
                  event("attr", Key="key", Value="value"), event("artifacts", Path="fixture-path"),
                  event("output", Output="test output\n", OutputType="frame"), event("pass"),
                  event("pass", None)]
        result, stdout, stderr, _ = self.invoke(events)
        self.assertEqual((result, stdout, stderr), (0, "build output\ntest output\n", ""))

    def test_any_failure_event_is_rejected_even_after_expected_pass(self):
        for failure in (event("fail", None), event("fail", "TestOther"),
                        {"Action": "build-fail", "ImportPath": PACKAGE}):
            self.assertEqual(self.invoke([event("pass"), failure, event("pass", None)])[0], 1)

    def test_nonzero_process_status_is_preserved_even_with_malformed_output(self):
        for code in (1, 23, 127):
            for events in ([event("pass")], [], [b"bad JSON\n"]):
                with self.subTest(code=code, events=events):
                    self.assertEqual(self.invoke(events, code=code)[0], code)
        self.assertEqual(self.invoke([event("pass")], code=-15)[0], 143)

    def test_command_arguments_are_never_interpreted_as_a_shell(self):
        argument = "; touch unexpected; $(echo expanded)"
        command = ["go", "test", "-run", argument, "./pkg"]
        result, stdout, stderr, launch = self.invoke([event("pass"), event("pass", None)], command=command)
        self.assertEqual(result, 0)
        self.assertEqual(launch.call_args.args[0], command[:2] + ["-json"] + command[2:])
        self.assertIs(launch.call_args.kwargs["shell"], False)
        self.assertNotIn(argument, stdout + stderr)

    def test_only_go_test_commands_are_accepted(self):
        for command in (["go"], ["go", "run"], ["sh", "-c", "go test"],
                        ["not-go", "test"], ["go test", "./pkg"]):
            with self.subTest(command=command):
                result, _, _, launch = self.invoke([], command=command)
                self.assertEqual(result, 2)
                launch.assert_not_called()
        for executable in ("go.exe", str(Path("fixture-toolchain") / "go")):
            self.assertEqual(self.invoke([event("pass"), event("pass", None)],
                                         command=[executable, "test"])[0], 0)

    def test_launch_errors_do_not_disclose_arguments_paths_or_exception(self):
        for error, code in ((FileNotFoundError("secret-fixture"), 127), (OSError("secret-fixture"), 126)):
            stderr = io.StringIO()
            with mock.patch.object(ci_go_test.subprocess, "Popen", side_effect=error), \
                    mock.patch.object(ci_go_test.sys, "stderr", stderr):
                self.assertEqual(ci_go_test.run_tests((EXPECTED,), ["go", "test"]), code)
            self.assertNotIn("secret-fixture", stderr.getvalue())

    def test_cli_parses_repeated_expectations_and_exact_subtests(self):
        with mock.patch.object(ci_go_test, "run_tests", return_value=0) as run:
            self.assertEqual(ci_go_test.main([
                "--expect", PACKAGE + ":" + TEST, "--expect", PACKAGE + ":" + TEST + "/subtest",
                "--", "go", "test", "-count=1", "./pkg"]), 0)
        run.assert_called_once_with([EXPECTED, (PACKAGE, TEST + "/subtest")],
                                    ["go", "test", "-count=1", "./pkg"])

    def test_cli_exact_selects_strict_mode(self):
        with mock.patch.object(ci_go_test, "run_tests", return_value=0) as run:
            self.assertEqual(ci_go_test.main(["--exact", "--expect", PACKAGE + ":" + TEST,
                                              "--", "go", "test", "-count=1", "./pkg"]), 0)
        run.assert_called_once_with([EXPECTED], ["go", "test", "-count=1", "./pkg"], exact=True)

    def test_invalid_cli_does_not_launch_or_disclose_input(self):
        for args in ([], ["--expect", "secret-fixture", "--", "go", "test"],
                     ["--expect", ":TestRequired", "--", "go", "test"],
                     ["--expect", PACKAGE + ":", "--", "go", "test"],
                     ["--expect", PACKAGE + ":Test\nsecret-fixture", "--", "go", "test"],
                     ["--expect", PACKAGE + ":" + TEST, "go", "test"],
                     ["--secret-fixture"]):
            stderr = io.StringIO()
            with self.subTest(args=args), mock.patch.object(ci_go_test, "run_tests") as run, \
                    mock.patch.object(ci_go_test.sys, "stderr", stderr), self.assertRaises(SystemExit) as stop:
                ci_go_test.main(args)
            self.assertEqual(stop.exception.code, 2)
            run.assert_not_called()
            self.assertNotIn("secret-fixture", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
