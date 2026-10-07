"""Pure source/command checks for the opt-in context-control gates; no Go runs."""

import importlib.util
import io
import os
from pathlib import Path
import re
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = Path(__file__).with_name("ci-context-control.py")
SPEC = importlib.util.spec_from_file_location("ci_context_control", SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)
TAGS = {"core-tls": "directlan_context_fixture", "fixed-tcp": "directlan_context_tcp"}
SOURCES = {
    "core-tls": "internal/core/direct_lan_context_tls_test.go",
    "fixed-tcp": "internal/directlan/context_fixed_tcp_test.go",
}


class ContextControlGateTests(unittest.TestCase):
    def source_cases(self, suite):
        """Read the deliberately simple table-driven inventory without compiling.

        Reject an unfamiliar subtest shape rather than silently omitting it.
        The exclusion case expands its four targets by seven build profiles.
        """
        source = (ROOT / SOURCES[suite]).read_text(encoding="utf-8")
        self.assertTrue(source.startswith("//go:build " + TAGS[suite] + "\n\n"))
        functions = re.findall(
            r"^func (Test\w+)\(t \*testing.T\) \{\n(.*?)(?=^func |\Z)",
            source, re.MULTILINE | re.DOTALL)
        self.assertEqual(len(functions), len(set(name for name, _ in functions)))
        cases = {}
        for name, body in functions:
            children = []
            runs = re.findall(r"\bt\.Run\((.*?), func\(t \*testing.T\)", body)
            if name == "TestContextTLSFixtureExcludedFromProductBuilds":
                self.assertEqual(runs, ['target.os+"-"+target.arch+"/"+profile.name'])
                profiles = re.search(r"profiles := \[\]struct \{.*?\n\t\}\{(.*?)\n\t\}",
                                     body, re.DOTALL)
                targets = re.search(r"for _, target := range \[\]struct\{ os, arch string \}\{(.*?)\} \{",
                                    body)
                self.assertIsNotNone(profiles)
                self.assertIsNotNone(targets)
                profile_names = re.findall(r'^\s*\{"([^\"]+)",', profiles[1], re.MULTILINE)
                target_names = re.findall(r'\{"([^\"]+)", "([^\"]+)"\}', targets[1])
                self.assertEqual((len(target_names), len(profile_names)), (4, 7))
                children = [os_name + "-" + arch + "/" + profile
                            for os_name, arch in target_names for profile in profile_names]
            elif runs:
                self.assertEqual(runs, ["name"])
                tables = re.findall(r"for _, name := range \[\]string\{([^}]+)\}", body)
                self.assertEqual(len(tables), 1)
                children = re.findall(r'"([^\"]+)"', tables[0])
                self.assertTrue(children)
            self.assertEqual(len(children), len(set(children)))
            cases[name] = tuple(children)
        return cases

    def test_exact_frozen_inventory_matches_go_source(self):
        self.assertEqual(set(gate.SUITES), {"core-tls", "fixed-tcp"})
        for suite, counts in (("core-tls", (9, 53)), ("fixed-tcp", (2, 0))):
            with self.subTest(suite=suite):
                cases = self.source_cases(suite)
                self.assertEqual((len(cases), sum(map(len, cases.values()))), counts)
                self.assertEqual(gate.SUITES[suite][3], cases)

    def test_only_selected_bounded_go_test_receives_its_tag(self):
        for suite, package, timeout in (("core-tls", "core", "3m"),
                                        ("fixed-tcp", "directlan", "45s")):
            with self.subTest(suite=suite):
                command = gate.command(suite)
                separator = command.index("--")
                self.assertEqual(command[:2], [gate.sys.executable, str(SCRIPT.with_name("ci-go-test.py"))])
                expected = ["github.com/webkaz-labs/sobalink/internal/" + package + ":" + name + suffix
                            for name, children in self.source_cases(suite).items()
                            for suffix in ("", *("/" + child for child in children))]
                self.assertEqual(command[2:separator:2], ["--expect"] * len(expected))
                self.assertEqual(command[3:separator:2], expected)
                self.assertEqual(len(expected), len(set(expected)))
                test_command = command[separator + 1:]
                self.assertEqual(test_command[:5], ["go", "test", "-race", "-count=1", "-v"])
                self.assertEqual(test_command[5:7], ["-timeout=" + timeout, "-tags=" + TAGS[suite]
                                 + ",ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy"])
                self.assertEqual(test_command[-1], "./internal/" + package)
                self.assertEqual(len(test_command), 9)
                pattern = test_command[7].removeprefix("-run=")
                for name in self.source_cases(suite):
                    self.assertIsNotNone(re.fullmatch(pattern, name))
                    self.assertIsNone(re.search(pattern, name + "Extra"))
                    self.assertIsNone(re.search(pattern, "Extra" + name))
                self.assertIsNone(re.search(pattern, "TestOther"))
                self.assertEqual([tag for tag in TAGS.values() if any(tag in part for part in command)],
                                 [TAGS[suite]])

    def test_driver_inherits_without_mutating_product_flags(self):
        flags = "-mod=readonly -tags=ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy"
        for suite in gate.SUITES:
            with self.subTest(suite=suite), mock.patch.dict(os.environ, {"GOFLAGS": flags}), \
                    mock.patch.object(gate.subprocess, "run", return_value=mock.Mock(returncode=0)) as run:
                before = dict(os.environ)
                self.assertEqual(gate.main([suite]), 0)
                run.assert_called_once_with(gate.command(suite), check=False, shell=False)
                self.assertEqual(dict(os.environ), before)

    def test_failure_statuses_and_launch_errors_cannot_pass(self):
        for code, expected in ((1, 1), (23, 23), (-15, 143)):
            with mock.patch.object(gate.subprocess, "run", return_value=mock.Mock(returncode=code)):
                self.assertEqual(gate.main(["core-tls"]), expected)
        for error, expected in ((OSError("private fixture text"), 1), (KeyboardInterrupt(), 130)):
            with mock.patch.object(gate.subprocess, "run", side_effect=error), \
                    mock.patch.object(gate.sys, "stderr", io.StringIO()) as stderr:
                self.assertEqual(gate.main(["fixed-tcp"]), expected)
                self.assertNotIn("private fixture text", stderr.getvalue())

    def test_arbitrary_commands_tags_and_extra_arguments_are_rejected(self):
        for arguments in ([], ["build"], ["go", "test"], ["core-tls", "./..."],
                          ["fixed-tcp", "-tags=extra"], ["core-tls", "--", "go", "build"]):
            with self.subTest(arguments=arguments), mock.patch.object(gate.subprocess, "run") as run, \
                    mock.patch.object(gate.sys, "stderr", io.StringIO()), self.assertRaises(SystemExit) as error:
                gate.main(arguments)
            self.assertEqual(error.exception.code, 2)
            run.assert_not_called()

    def test_both_workflows_require_each_gate_only_in_native_tests(self):
        for workflow in ("ci", "prerelease"):
            content = (ROOT / ".github/workflows" / (workflow + ".yml")).read_text(encoding="utf-8")
            native = re.search(r"^  native:\n(.*?)(?=^  [\w-]+:|\Z)", content, re.MULTILINE | re.DOTALL)[0]
            for suite, minutes in (("core-tls", 5), ("fixed-tcp", 2)):
                invocation = "python .github/scripts/ci-context-control.py " + suite
                self.assertEqual(content.count(invocation), 1)
                steps = re.findall(r"^      - .*?(?=^      - |\Z)", native, re.MULTILINE | re.DOTALL)
                step, = [step for step in steps if invocation in step]
                self.assertIn("timeout-minutes: " + str(minutes), step)
                self.assertIn("set -euo pipefail", step)
                self.assertNotIn("        if:", step)
                self.assertNotIn("continue-on-error", step)
                self.assertLess(native.index(step), native.index("go run ./cmd/package-tool build"))
                if workflow == "ci":
                    self.assertIn("ci-metrics.py run --suite context-" + suite + " -- " + invocation, step)
                else:
                    self.assertIn(" | tee logs/context-" + suite + ".txt", step)

    def test_tag_literals_are_confined_to_the_test_driver_and_its_tests(self):
        allowed = {".github/scripts/ci-context-control.py", ".github/scripts/test_ci_context_control.py"}
        bytecode = {Path(importlib.util.cache_from_source(str(ROOT / path))) for path in allowed}
        for directory in (".github/scripts", ".github/workflows"):
            for path in (ROOT / directory).rglob("*"):
                if not path.is_file() or path in bytecode or path.relative_to(ROOT).as_posix() in allowed:
                    continue
                for tag in TAGS.values():
                    self.assertNotIn(tag.encode(), path.read_bytes(), path.relative_to(ROOT).as_posix())
        source = (ROOT / SOURCES["core-tls"]).read_text(encoding="utf-8")
        exceptions = re.findall(r'allowed\["([^\"]+)"\] = true', source)
        self.assertEqual(set(exceptions), allowed)
        self.assertEqual(len(exceptions), len(allowed))


if __name__ == "__main__":
    unittest.main()
