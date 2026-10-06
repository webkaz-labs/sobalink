"""Scoped Go selection and command execution, using synthetic Go-tool output."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("ci-go-scope.py")
SPEC = importlib.util.spec_from_file_location("ci_go_scope", SCRIPT)
scope = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(scope)
CHANGED = "./internal/servicepresets"
PACKAGE = scope.MODULE + CHANGED[1:]


def package(name, imports=(), test_imports=(), external_test_imports=(), **fields):
    return dict({"ImportPath": scope.MODULE + "/" + name,
                 "Module": {"Path": scope.MODULE, "Main": True},
                 "Imports": list(imports), "TestImports": list(test_imports),
                 "XTestImports": list(external_test_imports)}, **fields)


def graph(*items):
    return "\n".join(json.dumps(item) for item in items)


def fixture_graph():
    return graph(package("internal/servicepresets"),
                 package("internal/core", imports=[PACKAGE]),
                 package("cmd/app", imports=[scope.MODULE + "/internal/core"]),
                 package("internal/testconsumer", test_imports=[PACKAGE]),
                 package("internal/externaltest", external_test_imports=[PACKAGE]),
                 package("internal/independent", imports=["fmt"]))


def plan():
    return {"version": 2, "policy_id": "minimum-ci-v2", "scope": "go",
            "go_packages": [CHANGED], "head_sha": "a" * 40, "head_tree": "b" * 40}


def event(action, test=None, **fields):
    item = {"Action": action, "Package": PACKAGE, **fields}
    if test is not None:
        item["Test"] = test
    return item


PASSED = [event("run", "TestPreset"), event("pass", "TestPreset"), event("pass")]


class GraphTests(unittest.TestCase):
    def test_transitive_reverse_imports_and_both_test_import_types(self):
        parsed = scope.parse_graph(fixture_graph())
        self.assertEqual(scope.affected_packages(parsed, set(parsed), [CHANGED]), [
            "./cmd/app", "./internal/core", "./internal/externaltest",
            CHANGED, "./internal/testconsumer"])

    def test_changed_packages_are_merged_and_cycles_terminate(self):
        second = scope.MODULE + "/internal/boundedlog"
        parsed = scope.parse_graph(graph(package("internal/servicepresets", [second]),
                                        package("internal/boundedlog", [PACKAGE]),
                                        package("cmd/app", [second])))
        self.assertEqual(scope.affected_packages(parsed, set(parsed),
                                                [CHANGED, "./internal/boundedlog"]),
                         ["./cmd/app", "./internal/boundedlog", CHANGED])

    def test_graph_rejects_empty_malformed_errors_duplicates_and_unknown_local_imports(self):
        for raw in ("", "{}", "[]", "null", "{", "not-json",
                    graph(package("internal/servicepresets", Imports=None)),
                    graph(package("internal/servicepresets", Module={})),
                    graph(package("internal/servicepresets", Error={"Err": "fixture"})),
                    graph(package("internal/servicepresets", DepsErrors=[{}])),
                    graph(package("internal/servicepresets", Incomplete=True)),
                    graph(package("internal/servicepresets", ForTest=PACKAGE)),
                    graph(package("internal/servicepresets", [scope.MODULE + "/missing"])),
                    graph(package("internal/servicepresets", ["$(touch sentinel)"])),
                    graph(package("internal/servicepresets"), package("internal/servicepresets")),
                    graph(package("internal/servicepresets")) + "truncated",
                    '{"ImportPath":"one","ImportPath":"two"}'):
            with self.subTest(raw=raw), self.assertRaises(scope.InvalidInput):
                scope.parse_graph(raw)

    def test_unknown_changed_package_and_incomplete_graph_fail_selection(self):
        parsed = scope.parse_graph(fixture_graph())
        for changed in (["./internal/missing"], [], ["./internal/boundedlog"]):
            with self.subTest(changed=changed), self.assertRaises(scope.InvalidInput):
                scope.affected_packages(parsed, set(parsed), changed)
        with self.assertRaises(scope.InvalidInput):
            scope.affected_packages({PACKAGE: {"missing"}}, {PACKAGE}, [CHANGED])

    def test_tagged_test_import_widens_closure_without_running_tag_only_package(self):
        standard = graph(package("internal/servicepresets"), package("internal/core"),
                         package("internal/independent"))
        tagged = graph(package("internal/servicepresets"),
                       package("internal/core", test_imports=[PACKAGE]),
                       package("cmd/tagonly", [PACKAGE]), package("internal/independent"))
        with mock.patch.object(scope, "query", side_effect=[standard] * 2 + [tagged] * 4) as query:
            combined, runnable = scope.discover_graph({"GOFLAGS": "-mod=readonly -tags=standard_a,standard_b"})
        self.assertEqual(scope.affected_packages(combined, runnable, [CHANGED]),
                         ["./internal/core", CHANGED])
        for call in query.call_args_list[2:]:
            self.assertTrue(any(arg.startswith("-tags=standard_a,standard_b,")
                                for arg in call.args[0]))
        self.assertTrue(any("ts_omit_udptransport" in arg
                            for call in query.call_args_list for arg in call.args[0]))

    def test_race_build_imports_are_included_alongside_normal_vet_imports(self):
        standard = graph(package("internal/servicepresets"), package("internal/raceconsumer"))
        race = graph(package("internal/servicepresets"),
                     package("internal/raceconsumer", test_imports=[PACKAGE]))
        with mock.patch.object(scope, "query", side_effect=[standard] + [race] * 5) as query:
            combined, runnable = scope.discover_graph({})
        self.assertEqual(scope.affected_packages(combined, runnable, [CHANGED]),
                         ["./internal/raceconsumer", CHANGED])
        self.assertNotIn("-race", query.call_args_list[0].args[0])
        self.assertTrue(all("-race" in call.args[0] for call in query.call_args_list[1:]))


class RunnerTests(unittest.TestCase):
    def invoke(self, *, value=None, graphs=None, events=None, test_code=0, vet_code=0,
               head="a" * 40, tree="b" * 40, dirty=False):
        commands = []
        outputs = iter(graphs or [fixture_graph()] * len(scope.GRAPH_VARIANTS))

        def run(argv, **kwargs):
            commands.append((argv, kwargs))
            self.assertIs(kwargs["shell"], False)
            self.assertEqual(kwargs["env"]["GOWORK"], "off")
            if argv[0] == "git":
                if argv[1] == "rev-parse":
                    raw = head if argv[-1] == "HEAD" else tree
                    return subprocess.CompletedProcess(argv, 0, (raw + "\n").encode())
                return subprocess.CompletedProcess(argv, 1 if dirty else 0, b"")
            if argv[1] == "list":
                raw = next(outputs)
                if isinstance(raw, Exception):
                    raise raw
                return subprocess.CompletedProcess(argv, 1 if raw is None else 0,
                                                   (raw or "").encode())
            self.assertEqual(argv[1], "vet")
            return subprocess.CompletedProcess(argv, vet_code)

        raw = b"".join(item if isinstance(item, bytes) else
                       (json.dumps(item) + "\n").encode()
                       for item in (PASSED if events is None else events))
        process = mock.Mock(stdout=io.BytesIO(raw))
        process.wait.return_value = test_code

        def popen(argv, **kwargs):
            commands.append((argv, kwargs))
            self.assertIs(kwargs["shell"], False)
            return process

        stdout, stderr = io.StringIO(), io.StringIO()
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "plan.json"
            source.write_text(json.dumps(plan() if value is None else value), encoding="utf-8")
            with mock.patch.object(scope.subprocess, "run", side_effect=run), \
                    mock.patch.object(scope.subprocess, "Popen", side_effect=popen), \
                    mock.patch.dict(os.environ, {"GOWORK": "untrusted-workspace", "GOFLAGS": "-mod=readonly -tags=standard"}), \
                    contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                code = scope.main(["--plan", str(source)])
        return code, stdout.getvalue(), stderr.getvalue(), commands

    def test_complete_run_tests_and_vets_only_reverse_closure_with_standard_flags(self):
        code, stdout, stderr, commands = self.invoke(events=[event("output", Output="test output\n"), *PASSED])
        self.assertEqual(code, 0)
        self.assertIn("test output", stdout)
        self.assertEqual(stderr, "")
        execution = [(argv, kwargs) for argv, kwargs in commands if argv[0] == "go" and argv[1] != "list"]
        tests, vet = execution
        expected = ["./cmd/app", "./internal/core", "./internal/externaltest", CHANGED,
                    "./internal/testconsumer"]
        self.assertEqual(tests[0], ["go", "test", "-json", "-mod=readonly", "-race", "-count=1", "-timeout=10m", *expected])
        self.assertEqual(vet[0], ["go", "vet", "-mod=readonly", *expected])
        for _, kwargs in commands:
            self.assertEqual(kwargs["env"]["GOFLAGS"], "-mod=readonly -tags=standard")

    def test_query_failure_or_invalid_graph_falls_back_to_all_standard_packages(self):
        for output in (None, "[]", "", OSError("fixture"),
                       graph(package("internal/independent")),
                       graph(package("internal/servicepresets", DepsErrors=[{}]))):
            with self.subTest(output=output):
                code, _, stderr, commands = self.invoke(graphs=[output] * len(scope.GRAPH_VARIANTS))
                self.assertEqual(code, 0)
                self.assertIn("all standard Go packages", stderr)
                for argv, _ in commands:
                    if argv[:2] in (["go", "test"], ["go", "vet"]):
                        self.assertEqual(argv[-1], "./...")

    def test_failure_in_a_later_tag_variant_also_falls_back(self):
        code, _, stderr, _ = self.invoke(graphs=[fixture_graph(), None])
        self.assertEqual(code, 0)
        self.assertIn("all standard Go packages", stderr)

    def test_invalid_plan_stops_before_running_commands(self):
        for field, value in (("version", 1), ("version", True), ("policy_id", "old"),
                             ("scope", "full"), ("go_packages", []),
                             ("go_packages", [CHANGED, CHANGED]),
                             ("go_packages", ["./internal/core"]),
                             ("go_packages", ["./internal/servicepresets;touch sentinel"]),
                             ("go_packages", ["$(touch sentinel)"]),
                             ("go_packages", [42]), ("head_sha", "not-a-sha"),
                             ("head_tree", None)):
            with self.subTest(field=field, value=value):
                code, _, _, commands = self.invoke(value={**plan(), field: value})
                self.assertEqual(code, 2)
                self.assertEqual(commands, [])

    def test_wrong_head_tree_or_dirty_checkout_never_runs_go(self):
        for changed in ({"head": "c" * 40}, {"tree": "c" * 40}, {"dirty": True}):
            with self.subTest(changed=changed):
                code, _, _, commands = self.invoke(**changed)
                self.assertEqual(code, 2)
                self.assertTrue(all(argv[0] == "git" for argv, _ in commands))

    def test_no_tests_missing_package_pass_skips_or_malformed_output_cannot_succeed(self):
        for events in ([], [event("pass")], [event("pass", "TestPreset")],
                       [event("skip", "TestPreset"), *PASSED],
                       [event("fail", "TestOther"), *PASSED],
                       [b"not json\n", *PASSED], [b"\xff\n", *PASSED],
                       [b"[]\n", *PASSED], [event("output", Output=[]), *PASSED]):
            with self.subTest(events=events):
                code, _, _, commands = self.invoke(events=events)
                self.assertEqual(code, 1)
                self.assertFalse(any(argv[:2] == ["go", "vet"] for argv, _ in commands))

    def test_graph_fallback_cannot_succeed_without_real_changed_package_tests(self):
        code, _, _, _ = self.invoke(graphs=[None], events=[event("pass")])
        self.assertEqual(code, 1)

    def test_test_failure_prevents_vet_and_preserves_exit_code(self):
        for status, expected in ((7, 7), (-15, 143)):
            code, _, _, commands = self.invoke(test_code=status)
            self.assertEqual(code, expected)
            self.assertFalse(any(argv[:2] == ["go", "vet"] for argv, _ in commands))

    def test_vet_failure_is_preserved(self):
        self.assertEqual(self.invoke(vet_code=9)[0], 9)

    def test_plan_reader_rejects_duplicate_fields_and_missing_files(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "plan.json"
            with self.assertRaises(scope.InvalidInput):
                scope.read_plan(source)
            source.write_text('{"scope":"go","scope":"full"}', encoding="utf-8")
            with self.assertRaises(scope.InvalidInput):
                scope.read_plan(source)

    def test_each_changed_package_must_have_a_real_test_and_package_pass(self):
        both = {**plan(), "go_packages": [CHANGED, "./internal/boundedlog"]}
        raw = graph(package("internal/servicepresets"), package("internal/boundedlog"))
        self.assertEqual(self.invoke(value=both, graphs=[raw] * len(scope.GRAPH_VARIANTS))[0], 1)
        other = scope.MODULE + "/internal/boundedlog"
        events = [*PASSED, event("pass", "TestWriter", Package=other), event("pass", Package=other)]
        self.assertEqual(self.invoke(value=both, graphs=[raw] * len(scope.GRAPH_VARIANTS), events=events)[0], 0)


if __name__ == "__main__":
    unittest.main()
