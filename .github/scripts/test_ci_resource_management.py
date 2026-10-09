"""Offline native-management workflow contract; all process calls are mocked."""
import ast
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import textwrap
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('management_full_validation',
                                            Path(__file__).with_name('ci_full_validation.py'))
full = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(full)
WRAPPER_SPEC = importlib.util.spec_from_file_location('management_go_test',
                                                    Path(__file__).with_name('ci-go-test.py'))
wrapper = importlib.util.module_from_spec(WRAPPER_SPEC)
WRAPPER_SPEC.loader.exec_module(wrapper)
GATE = 'Verify native remote resource management'
PACKAGE = 'github.com/webkaz-labs/sobalink/internal/core'
TESTS = (
    'TestResourceManagementNativePreviewApplyReplayAndMigration',
    'TestResourceManagementNativeRevokeReplacementAndProtocolSeparation',
    'TestResourceManagementNativeRestartPreservesOriginalExpiryAndHistory',
)
SELECTOR = ('^TestResourceManagementNative(PreviewApplyReplayAndMigration|'
            'RevokeReplacementAndProtocolSeparation|RestartPreservesOriginalExpiryAndHistory)$')
TAGS = ('ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,'
        'resource_management_native,resource_inspection_native,directlan_activation_native')
PROXIES = ('HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'http_proxy', 'https_proxy', 'all_proxy', 'TS_PROXY')
TARGETS = (('linux', 'amd64'), ('linux', 'arm64'), ('darwin', 'arm64'), ('windows', 'amd64'))
OPTINS = {
    'SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE': 'reviewed-production-loopback-v2',
    'SOBALINK_RUN_ACTIVATION_NATIVE': '1',
}


class ResourceManagementWorkflowTests(unittest.TestCase):
    def setUp(self):
        self.workflow = (ROOT / full.WORKFLOW).read_text(encoding='utf-8')
        marker = '      - name: ' + GATE + '\n'
        self.step = marker + self.workflow.split(marker)[1].split('      # Run only the isolated')[0]
        self.script = textwrap.dedent(self.step.split("          python - <<'PYTHON'\n")[1]
                                     .split('          PYTHON\n')[0])

    def invoke(self, goos='linux', goarch='amd64', *, environment=None, actual=None, failure=None, probe_failure=None):
        script = self.script.replace('${{ matrix.goos }}', goos).replace('${{ matrix.goarch }}', goarch)
        # Execute only the reviewed inline stdlib code, with both process entry
        # points already mocked. Neither Go nor the child helper is executed.
        with patch.dict(os.environ, environment or {}, clear=True), \
                patch.object(subprocess, 'check_output', return_value=actual if actual is not None else f'go1.27.1\n{goos}\n{goarch}\n',
                             side_effect=probe_failure) as probe, \
                patch.object(subprocess, 'run', side_effect=failure) as launch:
            parent_environment = dict(os.environ)
            error = None
            try:
                exec(compile(script, '<native-management-contract>', 'exec'), {})
            except (SystemExit, subprocess.SubprocessError) as exc:
                error = exc
            self.assertEqual(dict(os.environ), parent_environment)
        return probe, launch, error

    def test_exact_three_case_command_on_each_native_target(self):
        for goos, goarch in TARGETS:
            with self.subTest(target=(goos, goarch)):
                probe, launch, error = self.invoke(goos, goarch, environment={'SYNTHETIC_KEEP': 'fixture'})
                self.assertIsNone(error)
                probe.assert_called_once_with(['go', 'env', 'GOVERSION', 'GOOS', 'GOARCH'], text=True, timeout=30)
                expected = [sys.executable, '.github/scripts/ci-go-test.py', '--exact']
                for name in TESTS:
                    expected += ['--expect', PACKAGE + ':' + name]
                expected += ['--', 'go', 'test', '-race', '-count=1', '-v', '-timeout=8m',
                             '-tags=' + TAGS, '-run=' + SELECTOR, './internal/core']
                launch.assert_called_once_with(expected, env={'SYNTHETIC_KEEP': 'fixture', **OPTINS},
                                               check=True, timeout=10 * 60)

    def test_each_proxy_is_omitted_only_from_child_environment(self):
        for name in PROXIES:
            for value in ('', 'synthetic-private-value'):
                with self.subTest(proxy=name, empty=not value):
                    _, launch, error = self.invoke(environment={name: value, 'SYNTHETIC_KEEP': 'fixture'})
                    self.assertIsNone(error)
                    self.assertEqual(launch.call_args.kwargs['env'], {'SYNTHETIC_KEEP': 'fixture', **OPTINS})

    def test_all_proxies_and_inherited_inspection_optin_are_removed(self):
        environment = dict.fromkeys(PROXIES, 'synthetic-private-value')
        environment.update({
            'SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE': 'reviewed-production-loopback-v1',
            'SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE': 'unreviewed',
            'SOBALINK_RUN_ACTIVATION_NATIVE': '0',
            'SYNTHETIC_KEEP': 'fixture',
        })
        _, launch, error = self.invoke(environment=environment)
        self.assertIsNone(error)
        self.assertEqual(launch.call_args.kwargs['env'], {'SYNTHETIC_KEEP': 'fixture', **OPTINS})

    def test_toolchain_or_target_mismatch_never_invokes_tests(self):
        for actual in ('', 'go1.27.0\nlinux\namd64\n', 'go1.27.1\ndarwin\namd64\n',
                       'go1.27.1\nlinux\narm64\n', 'go1.27.1\nlinux\n', 'unexpected\n'):
            with self.subTest(actual=actual):
                probe, launch, error = self.invoke(actual=actual)
                self.assertIsInstance(error, SystemExit)
                probe.assert_called_once()
                launch.assert_not_called()

    def test_failed_probe_or_execution_is_not_retried(self):
        for failure in (subprocess.CalledProcessError(1, 'synthetic-command'),
                        subprocess.TimeoutExpired('synthetic-command', 30)):
            with self.subTest(stage='probe', failure=type(failure)):
                probe, launch, error = self.invoke(probe_failure=failure)
                self.assertIs(error, failure)
                probe.assert_called_once()
                launch.assert_not_called()
            with self.subTest(stage='execution', failure=type(failure)):
                _, launch, error = self.invoke(failure=failure)
                self.assertIs(error, failure)
                launch.assert_called_once()

    def test_inline_script_imports_only_stdlib_and_has_one_test_launch(self):
        tree = ast.parse(self.script)
        imports = [node for node in ast.walk(tree) if isinstance(node, (ast.Import, ast.ImportFrom))]
        self.assertEqual([alias.name for node in imports for alias in node.names], ['os', 'subprocess', 'sys'])
        self.assertTrue(all(isinstance(node, ast.Import) for node in imports))
        calls = [node.func.attr for node in ast.walk(tree) if isinstance(node, ast.Call)
                 and isinstance(node.func, ast.Attribute) and isinstance(node.func.value, ast.Name)
                 and node.func.value.id == 'subprocess']
        self.assertCountEqual(calls, ['check_output', 'run'])
        self.assertFalse(any(isinstance(node, (ast.For, ast.While, ast.Try, ast.With)) for node in ast.walk(tree)))

    def test_serial_full_only_step_and_separate_fixture_guards(self):
        full.validate_resource_management_workflow(self.workflow.encode('utf-8'))
        full.validate_resource_inspection_workflow(self.workflow.encode('utf-8'))
        full.validate_parallel_workflow(self.workflow.encode('utf-8'))
        self.assertIn("        if: steps.ci-plan.outputs.long_required != 'false'\n", self.step)
        self.assertIn('        timeout-minutes: 12\n', self.step)
        self.assertNotIn('background:', self.step)
        fixture = (ROOT / 'internal/core/resource_management_native_test.go').read_text(encoding='utf-8')
        self.assertTrue(fixture.startswith('//go:build resource_management_native && resource_inspection_native && directlan_activation_native\n'))
        self.assertEqual(re.findall(r'^func (TestResourceManagementNative\w+)\(t \*testing.T\)', fixture, re.M), list(TESTS))
        for name in TESTS:
            self.assertIn('func ' + name + '(t *testing.T) {\n\tf := newResourceManagementNativePair(t)', fixture)
        self.assertIn('resourceManagementNativeSelector = "' + SELECTOR + '"', fixture)
        guard = fixture.split('func newResourceManagementNativePair(t *testing.T)')[1].split('\nfunc ')[0]
        for required in ('os.Getenv("SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE") != "reviewed-production-loopback-v2"',
                         'os.Getenv("SOBALINK_RUN_ACTIVATION_NATIVE") != "1"',
                         'run.Value.String() != resourceManagementNativeSelector',
                         'time.Until(deadline) > 8*time.Minute', 'for _, name := range []string{'):
            self.assertLess(guard.index(required), guard.index('newResourceNativeOwnedPairWithPolicy(t, true)'))
        self.assertNotIn('SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE', fixture)
        for name in PROXIES:
            self.assertIn('"' + name + '"', guard)

    def test_source_policy_rejects_any_weakened_or_expanded_gate(self):
        mutations = [self.workflow.replace(self.step, ''), self.workflow.replace(self.step, self.step + self.step)]
        removed = self.workflow.replace(self.step, '')
        for marker in ('      - name: Verify recovery with the ordinary direct-enabled transport\n',
                       '      - name: Verify native remote resource inspection\n',
                       '      - name: Verify guarded relay real-time lease continuity\n'):
            mutations.append(removed.replace(marker, self.step + marker))
        changes = (
            ("if: steps.ci-plan.outputs.long_required != 'false'", 'if: always()'),
            ('timeout-minutes: 12', 'timeout-minutes: 13'),
            ('        run: |\n', '        continue-on-error: true\n        run: |\n'),
            ('        run: |\n', '        background: true\n        run: |\n'),
            ('"--exact",', ''),
            ('"--expect", "' + PACKAGE + ':' + TESTS[0] + '",', ''),
            ('-count=1', '-count=2'), ('"-race",', ''), ('-timeout=8m', '-timeout=9m'),
            (TAGS, TAGS + ',extra_native'), (SELECTOR, '^TestResourceManagementNative'),
            ('if name not in proxies and name != "SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE"', 'if True'),
            (' and name != "SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE"', ''),
            ('"http_proxy", ', ''), ('"TS_PROXY")', ')'),
            ('go1.27.1', 'go1.27.2'), ('${{ matrix.goos }}', 'linux'), ('${{ matrix.goarch }}', 'amd64'),
            ('reviewed-production-loopback-v2', '1'),
            ('env["SOBALINK_RUN_ACTIVATION_NATIVE"] = "1"', 'env["SOBALINK_RUN_ACTIVATION_NATIVE"] = "0"'),
            ('check=True', 'check=False'), ('timeout=10 * 60', 'timeout=20 * 60'),
            ('          PYTHON\n', '          PYTHON\n          echo unreviewed\n'),
        )
        for old, new in changes:
            self.assertIn(old, self.step)
            mutations.append(self.workflow.replace(self.step, self.step.replace(old, new)))
        mutations.append(self.workflow.replace('env:\n', 'env:\n  SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE: 1\n', 1))
        for index, workflow in enumerate(mutations):
            with self.subTest(mutation=index), self.assertRaises(ValueError):
                full.validate_resource_management_workflow(workflow.encode('utf-8'))
        for name in ('SOBALINK_RUN_ACTIVATION_NATIVE', 'SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE'):
            workflow = self.workflow.replace('env:\n', 'env:\n  ' + name + ': 1\n', 1)
            with self.subTest(escaped=name), self.assertRaises(ValueError):
                full.validate_resource_inspection_workflow(workflow.encode('utf-8'))

    def invoke_events(self, events):
        raw = b''.join((json.dumps(event) + '\n').encode('utf-8') for event in events)
        # A local synthetic object supplies the entire JSON stream and exit code.
        # Popen is mocked, so the wrapper cannot execute its Go command.
        class Process:
            stdout = io.BytesIO(raw)

            def wait(self):
                return 0

        with patch.object(subprocess, 'Popen', return_value=Process()) as launch, \
                patch.object(sys, 'stdout', io.StringIO()), patch.object(sys, 'stderr', io.StringIO()):
            result = wrapper.run_tests([(PACKAGE, name) for name in TESTS],
                                       ['go', 'test', '-race', '-count=1', './internal/core'], exact=True)
        launch.assert_called_once()
        return result

    def good_events(self):
        events = [{'Action': 'start', 'Package': PACKAGE}]
        for name in TESTS:
            events.extend({'Action': action, 'Package': PACKAGE, 'Test': name} for action in ('run', 'pass'))
        events.append({'Action': 'pass', 'Package': PACKAGE})
        return events

    def test_exact_wrapper_requires_every_management_case_once_without_skips(self):
        good = self.good_events()
        self.assertEqual(self.invoke_events(good), 0)
        for index in range(len(good)):
            with self.subTest(missing=index):
                self.assertEqual(self.invoke_events(good[:index] + good[index + 1:]), 1)
            with self.subTest(duplicate=index):
                self.assertEqual(self.invoke_events(good[:index] + [good[index]] + good[index:]), 1)
        for name in (*TESTS, None):
            for action in ('skip', 'fail'):
                event = {'Action': action, 'Package': PACKAGE}
                if name is not None:
                    event['Test'] = name
                with self.subTest(test=name, action=action):
                    self.assertEqual(self.invoke_events(good[:-1] + [event] + good[-1:]), 1)

    def test_exact_wrapper_rejects_inspection_extra_subtest_or_package_execution(self):
        good = self.good_events()
        for name, package in (('TestResourceInspectionNativeFirstRemoteInspect', PACKAGE),
                              (TESTS[0] + '/extra', PACKAGE), ('TestExtra', PACKAGE),
                              (TESTS[0], 'example.org/synthetic/other')):
            events = [{'Action': action, 'Package': package, 'Test': name} for action in ('run', 'pass')]
            with self.subTest(test=name, package=package):
                self.assertEqual(self.invoke_events(good[:-1] + events + good[-1:]), 1)


if __name__ == '__main__':
    unittest.main()
