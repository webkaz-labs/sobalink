"""Bounded fixture-diagnostic CI contract; every subprocess is mocked."""
import importlib.util
import os
from pathlib import Path
import re
import subprocess
import sys
import textwrap
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('fixture_diagnostic_full',
                                            Path(__file__).with_name('ci_full_validation.py'))
full = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(full)
GATE = 'Verify privacy-safe fixture diagnostics'
CASES = {
    'cmd/soba-e2e': ('soba_e2e', (
        'TestStartupDiagnosticClosedVocabulary',
        'TestStartupArgumentFailureWritesDiagnostic')),
    'internal/core': ('lanlink_integration,ts_omit_udptransport', (
        'TestOutboxBrowserFailurePrivacy',
        'TestNativeLANCommandDiagnosticRetainsOnlyAllowedCodes',
        'TestNativeLANCommandDiagnosticPreservesProductionListenClassification',
        'TestNativeLANHeldRelayPortDiagnosticIntegration')),
    'internal/directlan': ('resource_inspection_native', (
        'TestInspectionDialResultClassifications',
        'TestInspectionDialResultStagesAndPrivacy',
        'TestInspectionDialResultFirstTerminalResult',
        'TestInspectionDialResultNoObservation',
        'TestInspectionDialInitialWakeObservation',
        'TestInspectionDialResultNeverFormatsError')),
}


class FixtureDiagnosticWorkflowTests(unittest.TestCase):
    def setUp(self):
        self.workflow = (ROOT / full.WORKFLOW).read_text(encoding='utf-8')
        marker = '      - name: ' + GATE + '\n'
        self.step = marker + self.workflow.split(marker)[1].split('      - ', 1)[0]
        self.script = textwrap.dedent(self.step.split("          python - <<'PYTHON'\n")[1]
                                     .split('          PYTHON\n')[0])

    def invoke(self, *, environment=None, actual='go1.27.1\nlinux\namd64\n', failure=None):
        with patch.dict(os.environ, environment or {}, clear=True), \
                patch.object(subprocess, 'check_output', return_value=actual) as probe, \
                patch.object(subprocess, 'run', side_effect=failure) as launch:
            error = None
            try:
                exec(compile(self.script, '<fixture-diagnostic-contract>', 'exec'), {})
            except (SystemExit, subprocess.SubprocessError) as exc:
                error = exc
        return probe, launch, error

    def test_all_twelve_cases_are_required_once_with_race_and_fixed_bounds(self):
        probe, launch, error = self.invoke(environment={'SYNTHETIC_KEEP': 'fixture'})
        self.assertIsNone(error)
        probe.assert_called_once_with(['go', 'env', 'GOVERSION', 'GOOS', 'GOARCH'], text=True, timeout=30)
        self.assertEqual(launch.call_count, 3)
        self.assertEqual(sum(len(tests) for _, tests in CASES.values()), 12)
        for call, (package, (tags, tests)) in zip(launch.call_args_list, CASES.items()):
            expected = [sys.executable, '.github/scripts/ci-go-test.py']
            for name in tests:
                expected += ['--expect', 'github.com/webkaz-labs/sobalink/' + package + ':' + name]
            expected += ['--', 'go', 'test', '-race', '-count=1', '-v', '-timeout=2m',
                         '-tags=' + tags + ',ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy',
                         '-run=^(' + '|'.join(tests) + ')$', './' + package]
            self.assertEqual(call.args, (expected,))
            environment = {'SYNTHETIC_KEEP': 'fixture'}
            if package == 'internal/core':
                environment['SOBALINK_RUN_LAN_INTEGRATION'] = '1'
            self.assertEqual(call.kwargs, dict(env=environment, check=True, timeout=180))

    def test_private_overrides_are_removed_only_from_children(self):
        environment = {'SYNTHETIC_KEEP': 'fixture', 'HTTP_PROXY': 'synthetic-private',
                       'https_proxy': 'synthetic-private', 'All_Proxy': 'synthetic-private',
                       'TS_PROXY': 'synthetic-private', 'TS_OTHER': 'synthetic-private'}
        with patch.dict(os.environ, environment, clear=True):
            before = dict(os.environ)
            _, launch, error = self.invoke(environment=before)
            self.assertIsNone(error)
            self.assertEqual(dict(os.environ), before)
        for call in launch.call_args_list:
            self.assertNotIn('synthetic-private', call.kwargs['env'].values())

    def test_target_or_toolchain_mismatch_never_launches_regressions(self):
        for actual in ('', 'go1.27.0\nlinux\namd64\n', 'go1.27.1\nwindows\namd64\n',
                       'go1.27.1\nlinux\narm64\n'):
            with self.subTest(actual=actual):
                _, launch, error = self.invoke(actual=actual)
                self.assertIsInstance(error, SystemExit)
                launch.assert_not_called()

    def test_failure_stops_immediately_without_retry(self):
        for index in range(3):
            failure = subprocess.CalledProcessError(1, 'synthetic-command')
            _, launch, error = self.invoke(failure=[None] * index + [failure])
            self.assertIs(error, failure)
            self.assertEqual(launch.call_count, index + 1)

    def test_cases_correspond_to_actual_test_definitions(self):
        files = {'cmd/soba-e2e': ('startup_diagnostic_test.go',),
                 'internal/core': ('lan_integration_diagnostics_test.go', 'outbox_browser_diagnostics_integration_test.go'),
                 'internal/directlan': ('resource_inspection_observer_test.go',)}
        for package, (_, tests) in CASES.items():
            source = '\n'.join((ROOT / package / path).read_text(encoding='utf-8') for path in files[package])
            self.assertCountEqual(re.findall(r'^func (Test\w+)\(t \*testing.T\)', source, re.M), tests)

    def test_required_browser_gate_and_source_policy(self):
        sources = {path: (ROOT / path).read_bytes() for path in full.SOURCES}
        required = full.required_jobs(sources)
        self.assertEqual(required['browser-native'].count(GATE), 1)
        self.assertTrue(all(GATE not in steps for job, steps in required.items() if job != 'browser-native'))
        sources['.github/scripts/ci-coverage.py'] = sources['.github/scripts/ci-coverage.py'].replace(
            b'                 "Verify privacy-safe fixture diagnostics",\n', b'')
        with self.assertRaisesRegex(ValueError, 'every native-browser'):
            full.required_jobs(sources)

    def test_source_policy_rejects_missing_duplicate_or_weakened_execution(self):
        full.validate_fixture_diagnostics_workflow(self.workflow.encode())
        mutations = [self.workflow.replace(self.step, ''),
                     self.workflow.replace(self.step, self.step + self.step)]
        for old, new in (('timeout-minutes: 10', 'timeout-minutes: 11'),
                         ('        run: |\n', '        continue-on-error: true\n        run: |\n'),
                         ('"--expect"', '"--optional"'), ('"-race", ', ''),
                         ('-count=1', '-count=2'), ('-timeout=2m', '-timeout=3m'),
                         ('check=True', 'check=False'), ('timeout=3 * 60', 'timeout=6 * 60'),
                         ('"linux", "amd64"', '"windows", "amd64"'),
                         ('env["SOBALINK_RUN_LAN_INTEGRATION"] = "1"', 'pass'),
                         ('          PYTHON\n', '          PYTHON\n          echo unreviewed\n')):
            self.assertIn(old, self.step)
            mutations.append(self.workflow.replace(self.step, self.step.replace(old, new)))
        for changed in mutations:
            with self.subTest(mutation=mutations.index(changed)), self.assertRaises(ValueError):
                full.validate_fixture_diagnostics_workflow(changed.encode())


    def test_preparation_is_joined_before_embedded_assets_or_browser_consumers(self):
        region = self.workflow[self.workflow.index('\n  browser-native:\n'):]
        names = ('Prepare verified native-browser engine', 'Install locked native-browser dependencies',
                 'Wait for verified native-browser engine', 'Install official native-browser Chromium and fonts',
                 GATE, 'Wait for native-browser runtime preparation',
                 'Verify real browser UI launch without skips')
        positions = [region.index('      - name: ' + name + '\n') for name in names]
        self.assertEqual(positions, sorted(positions))
        self.assertNotIn('go run ./cmd/prepare-engine', self.step)
        self.assertNotIn('GITHUB_ENV', self.step)
        full.validate_fixture_diagnostics_workflow(self.workflow.encode())

    def test_background_failures_cannot_escape_waits_or_race_vite_writes(self):
        mutations = []
        for name, identifier in (('Prepare verified native-browser engine', 'native-browser-engine'),
                                 ('Install official native-browser Chromium and fonts', 'native-browser-chromium')):
            marker = '      - name: ' + name + '\n'
            for suffix in ('        if: false\n', '        continue-on-error: true\n'):
                mutations.append(self.workflow.replace(marker, marker + suffix, 1))
            before = '        id: ' + identifier + '\n        background: true\n'
            mutations.append(self.workflow.replace(before, before.replace('true', 'false'), 1))
        for name, identifier in (('Wait for verified native-browser engine', 'native-browser-engine'),
                                 ('Wait for native-browser runtime preparation', 'native-browser-chromium')):
            block = '      - name: ' + name + '\n        wait: ' + identifier + '\n'
            mutations += [self.workflow.replace(block, ''),
                          self.workflow.replace(block, block + '        if: always()\n'),
                          self.workflow.replace(block, block + '        continue-on-error: true\n'),
                          self.workflow.replace(block, block.replace('wait: ' + identifier, 'run: true'))]
        marker = '      - name: Install locked native-browser dependencies\n'
        start = self.workflow.index(marker)
        end = self.workflow.index('      - ', start + len(marker))
        frontend = self.workflow[start:end]
        moved = self.workflow.replace(frontend, '', 1)
        # This would compile diagnostics while Vite later deletes/rebuilds dist.
        mutations.append(moved.replace(self.step, self.step + frontend, 1))
        for index, mutation in enumerate(mutations):
            with self.subTest(mutation=index), self.assertRaises(ValueError):
                full.validate_fixture_diagnostics_workflow(mutation.encode())

    def test_each_preparation_and_wait_is_required_runtime_evidence(self):
        sources = {path: (ROOT / path).read_bytes() for path in full.SOURCES}
        gates = ('Prepare verified native-browser engine', 'Wait for verified native-browser engine',
                 'Install official native-browser Chromium and fonts', 'Wait for native-browser runtime preparation')
        required = full.required_jobs(sources)
        for gate in gates:
            self.assertEqual(required['browser-native'].count(gate), 1)
            changed = dict(sources)
            changed['.github/scripts/ci-coverage.py'] = sources['.github/scripts/ci-coverage.py'].replace(
                ('"' + gate + '"').encode(), b'"Unreviewed replacement"', 1)
            message = 'every native-browser'
            with self.subTest(gate=gate), self.assertRaisesRegex(ValueError, message):
                full.required_jobs(changed)


if __name__ == '__main__':
    unittest.main()
