"""Offline native-inspection workflow contract; all process calls are mocked."""
import ast
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import textwrap
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('inspection_full_validation',
                                            Path(__file__).with_name('ci_full_validation.py'))
full = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(full)
GATE = 'Verify native remote resource inspection'
PACKAGE = 'github.com/webkaz-labs/sobalink/internal/core'
TESTS = (
    'TestResourceInspectionNativeFirstRemoteInspect',
    'TestResourceInspectionNativeRestartPreservesOriginalExpiry',
    'TestResourceInspectionNativeReverseRestartPreservesOriginalExpiry',
    'TestResourceInspectionNativeRevokeDeniesNewInspection',
    'TestResourceInspectionNativePortCollisionPreservesOwner',
)
SELECTOR = ('^TestResourceInspectionNative(FirstRemoteInspect|RestartPreservesOriginalExpiry|'
            'ReverseRestartPreservesOriginalExpiry|RevokeDeniesNewInspection|PortCollisionPreservesOwner)$')
TAGS = 'ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,directlan_activation_native,resource_inspection_native'
PROXIES = ('HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'http_proxy', 'https_proxy', 'all_proxy', 'TS_PROXY')
TARGETS = (('linux', 'amd64'), ('linux', 'arm64'), ('darwin', 'arm64'), ('windows', 'amd64'))


class ResourceInspectionWorkflowTests(unittest.TestCase):
    def setUp(self):
        self.workflow = (ROOT / full.WORKFLOW).read_text(encoding='utf-8')
        marker = '      - name: ' + GATE + '\n'
        self.step = marker + self.workflow.split(marker)[1].split('      # Run only the isolated')[0]
        self.script = textwrap.dedent(self.step.split("          python - <<'PYTHON'\n")[1]
                                     .split('          PYTHON\n')[0])

    def invoke(self, goos='linux', goarch='amd64', *, environment=None, actual=None, failure=None):
        script = self.script.replace('${{ matrix.goos }}', goos).replace('${{ matrix.goarch }}', goarch)
        # Compile only this reviewed inline stdlib script. Every process entry
        # point it uses is mocked before exec; no Go/helper/native test can run.
        with patch.dict(os.environ, environment or {}, clear=True), \
                patch.object(subprocess, 'check_output', return_value=actual if actual is not None else f'go1.27.1\n{goos}\n{goarch}\n') as probe, \
                patch.object(subprocess, 'run', side_effect=failure) as launch:
            error = None
            try:
                exec(compile(script, '<native-inspection-contract>', 'exec'), {})
            except (SystemExit, subprocess.SubprocessError) as exc:
                error = exc
        return probe, launch, error

    def test_exact_five_case_command_on_each_native_target(self):
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
                launch.assert_called_once_with(expected, env={
                    'SYNTHETIC_KEEP': 'fixture',
                    'SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE': 'reviewed-production-loopback-v1',
                    'SOBALINK_RUN_ACTIVATION_NATIVE': '1',
                }, check=True, timeout=10 * 60)

    def test_each_proxy_fails_before_toolchain_probe_without_exposing_value(self):
        for name in PROXIES:
            with self.subTest(proxy=name):
                probe, launch, error = self.invoke(environment={name: 'synthetic-private-value'})
                self.assertIsInstance(error, SystemExit)
                self.assertNotIn('synthetic-private-value', str(error))
                probe.assert_not_called()
                launch.assert_not_called()

    def test_empty_proxy_variables_are_preserved_not_stripped(self):
        environment = dict.fromkeys(PROXIES, '')
        _, launch, error = self.invoke(environment=environment)
        self.assertIsNone(error)
        for name in PROXIES:
            self.assertEqual(launch.call_args.kwargs['env'][name], '')

    def test_toolchain_or_target_mismatch_never_invokes_tests(self):
        for actual in ('', 'go1.27.0\nlinux\namd64\n', 'go1.27.1\ndarwin\namd64\n',
                       'go1.27.1\nlinux\narm64\n', 'go1.27.1\nlinux\n', 'unexpected\n'):
            with self.subTest(actual=actual):
                probe, launch, error = self.invoke(actual=actual)
                self.assertIsInstance(error, SystemExit)
                probe.assert_called_once()
                launch.assert_not_called()

    def test_failed_execution_is_not_retried(self):
        failure = subprocess.CalledProcessError(1, 'synthetic-command')
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

    def test_serial_full_only_step_and_private_fixture_scope(self):
        full.validate_resource_inspection_workflow(self.workflow.encode('utf-8'))
        full.validate_parallel_workflow(self.workflow.encode('utf-8'))
        self.assertIn("        if: steps.ci-plan.outputs.long_required != 'false'\n", self.step)
        self.assertIn('        timeout-minutes: 12\n', self.step)
        self.assertNotIn('background:', self.step)
        fixture = (ROOT / 'internal/core/resource_inspection_native_test.go').read_text(encoding='utf-8')
        self.assertIn('netip.MustParseAddr("127.0.0.1")', fixture)
        self.assertIn('newActivationNativePairDirectories(t, true)', fixture)
        for name in TESTS:
            self.assertEqual(fixture.count('func ' + name + '(t *testing.T)'), 1)
        self.assertIn('resourceInspectionNativeSelector = "' + SELECTOR + '"', fixture)

    def test_source_policy_rejects_any_weakened_or_expanded_gate(self):
        mutations = [
            self.workflow.replace(self.step, ''),
            self.workflow.replace(self.step, self.step + self.step),
        ]
        removed = self.workflow.replace(self.step, '')
        for marker in ('      - name: Verify recovery with the ordinary direct-enabled transport\n',
                       '      - name: Verify guarded relay real-time lease continuity\n'):
            mutations.append(removed.replace(marker, self.step + marker))
        changes = (
            ("if: steps.ci-plan.outputs.long_required != 'false'", 'if: always()'),
            ('timeout-minutes: 12', 'timeout-minutes: 13'),
            ('        run: |\n', '        continue-on-error: true\n        run: |\n'),
            ('        run: |\n', '        background: true\n        run: |\n'),
            ('"--exact",', ''),
            ('"--expect", "' + PACKAGE + ':' + TESTS[0] + '",', ''),
            ('-count=1', '-count=2'),
            ('"-race",', ''),
            ('-timeout=8m', '-timeout=9m'),
            (TAGS, TAGS + ',extra_native'),
            (SELECTOR, '^TestResourceInspectionNative'),
            ('if any(os.environ.get(name) for name in proxies):', 'if False:'),
            ('"http_proxy", ', ''),
            ('"TS_PROXY")', ')'),
            ('go1.27.1', 'go1.27.2'),
            ('${{ matrix.goos }}', 'linux'),
            ('${{ matrix.goarch }}', 'amd64'),
            ('reviewed-production-loopback-v1', '1'),
            ('env["SOBALINK_RUN_ACTIVATION_NATIVE"] = "1"', 'env["SOBALINK_RUN_ACTIVATION_NATIVE"] = "0"'),
            ('env = os.environ.copy()', 'env = {}'),
            ('check=True', 'check=False'),
            ('timeout=10 * 60', 'timeout=20 * 60'),
            ('          PYTHON\n', '          PYTHON\n          echo unreviewed\n'),
        )
        for old, new in changes:
            self.assertIn(old, self.step)
            mutations.append(self.workflow.replace(self.step, self.step.replace(old, new)))
        mutations.append(self.workflow.replace('env:\n', 'env:\n  SOBALINK_RUN_ACTIVATION_NATIVE: 1\n', 1))
        for index, workflow in enumerate(mutations):
            with self.subTest(mutation=index), self.assertRaises(ValueError):
                full.validate_resource_inspection_workflow(workflow.encode('utf-8'))


if __name__ == '__main__':
    unittest.main()
