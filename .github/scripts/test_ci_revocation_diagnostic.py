"""Synthetic closed-evidence policy checks; never run the native test."""
import contextlib
import importlib.util
import io
import json
import os
import tempfile
from types import SimpleNamespace
from pathlib import Path
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('ci-revocation-diagnostic.py')
SPEC = importlib.util.spec_from_file_location('revocation_gate', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), gate.__dict__)


def marker(stage='complete'):
    lines = (gate.ROOT / gate.SOURCE).read_text().splitlines()
    location = next(i for i, line in enumerate(lines, 1) if 't.Logf("' + gate.MARKER in line)
    later = 'fresh_message=not_acknowledged receiver_message_observed_at_snapshot=absent' if stage == 'complete' else 'fresh_message=not_run receiver_message_observed_at_snapshot=not_run'
    return f'    lan_integration_test.go:{location}: revocation_observation_v1 stage={stage} read_bytes=0 read_result=timeout target_released=true app_trusted=false core_grants=0 range_policies=0 range_flows=0 durable_pairs=0 {later}\n'


def events(fail=False, markers=None):
    owner = {'Package': gate.PACKAGE, 'Test': gate.TEST}
    package = {'Package': gate.PACKAGE}
    terminal = 'fail' if fail else 'pass'
    return [{'Action': 'start', **package}, {'Action': 'run', **owner},
            {'Action': 'output', **owner, 'Output': 'synthetic-private-token-address-payload\n'},
            *[{'Action': 'output', **owner, 'Output': value} for value in (markers or [])],
            {'Action': terminal, **owner}, {'Action': terminal, **package}]


def raw(rows):
    return b''.join(json.dumps(row).encode() + b'\n' for row in rows)


class RevocationDiagnosticTests(unittest.TestCase):
    def test_exact_pass_has_no_invented_observations(self):
        self.assertEqual(gate.observations(raw(events()), 0), {'testStatus': 'pass', 'observations': []})

    def test_failure_retains_original_status_and_closed_observations(self):
        result = gate.observations(raw(events(True, [marker('stream'), marker()])), 1)
        self.assertEqual(result['testStatus'], 'fail')
        self.assertEqual([r['stage'] for r in result['observations']], ['stream', 'complete'])
        self.assertIs(result['observations'][1]['target_released'], True)
        self.assertIs(result['observations'][1]['app_trusted'], False)
        self.assertEqual(result['observations'][1]['range_flows'], 0)
        self.assertNotIn('synthetic-private', json.dumps(result))
        self.assertEqual(gate.observations(raw(events(True)), 1)['observations'], [])
        self.assertEqual(len(gate.observations(raw(events(True, [marker()])), 1)['observations']), 1)

    def test_skip_wrong_owner_duplicate_and_reordered_terminal_fail_closed(self):
        original = events()
        for rows in (original[1:], original[:-1], original + original,
                     [original[1], original[0], *original[2:]],
                     original[:1] + original[2:], original[:2] + [original[1]] + original[2:],
                     original[:-2] + [original[-1], original[-2]], original + [original[2]]):
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                gate.observations(raw(rows), 0)
        for mutation in ({'Action': 'skip'}, {'Action': 'fail'}, {'Action': 'pause'},
                         {'Action': 'cont'}, {'Package': 'example.invalid/other'},
                         {'Test': 'TestOther'}, {'Test': gate.TEST + '/subtest'}):
            rows = events()
            rows[-2].update(mutation)
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                gate.observations(raw(rows), 0)
        for code in (1, -9, 66, None, False, '0'):
            with self.subTest(code=code), self.assertRaises(ValueError):
                gate.observations(raw(events()), code)
        with self.assertRaises(ValueError):
            gate.observations(raw(events(True)), 0)

    def test_malformed_truncated_oversized_records_fail_closed(self):
        good = raw(events())
        for value in (good[:-1], good[:-4], good + b'{\n', b'null\n', b'[]\n', b'\xff\n',
                      good.replace(b'"Action": "run"', b'"Action": "run", "Action": "pass"'),
                      good.replace(b'"Action": "run"', b'"Action": "run", "Elapsed": NaN'),
                      good.replace(b'"Action": "run"', b'"Action": "run", "Elapsed": true'),
                      b'x' * gate.CAPTURE_BYTES + b'\n', good.decode()):
            with self.subTest(kind=type(value)), self.assertRaises((ValueError, UnicodeError)):
                gate.observations(value, 0)

    def test_failure_marker_source_binding_and_stage_rules(self):
        good = marker()
        bad = [good.replace('lan_integration_test.go:', 'other.go:'),
               good.replace(': revocation_', '0: revocation_'),
               good.replace('stage=complete', 'stage=unknown'),
               good.replace('read_result=timeout', 'read_result=secret'),
               good.replace('read_bytes=0', 'read_bytes=2'),
               good.replace('range_flows=0', 'range_flows=100000'),
               good.replace('app_trusted=false', 'app_trusted=0'),
               good.replace('fresh_message=not_acknowledged', 'fresh_message=not_run'),
               good.replace('snapshot=absent', 'snapshot=not_run'),
               good[:-1], good + 'private\n', good.replace(' core_grants=0', ' core_grants=0 core_grants=1')]
        for value in bad:
            with self.subTest(value=value), self.assertRaises(ValueError):
                gate.observations(raw(events(True, [value])), 1)
        for markers in ([marker('stream')], [good, good], [good, marker('stream')],
                        [marker('stream'), marker('stream'), good]):
            with self.assertRaises(ValueError):
                gate.observations(raw(events(True, markers)), 1)
        with self.assertRaises(ValueError):
            gate.observations(raw(events(False, [good])), 0)
        rows = events(True, [good])
        rows[3].pop('Test')
        with self.assertRaises(ValueError):
            gate.observations(raw(rows), 1)
        rows = events(True)
        rows.insert(-1, {'Action': 'output', 'Package': gate.PACKAGE, 'Test': gate.TEST, 'Output': good})
        with self.assertRaises(ValueError):
            gate.observations(raw(rows), 1)

    def test_fixed_command_scope_and_privacy_boundaries(self):
        text = SCRIPT.read_text()
        workflow = (gate.ROOT / '.github/workflows/activation-diagnostic.yml').read_text()
        job = workflow.split('  diagnostic-revocation:\n')[1]
        self.assertIn('runs-on: macos-26', job)
        self.assertIn("suite == 'revocation'", job)
        self.assertIn('cache: false', job)
        self.assertIn('persist-credentials: false', job)
        self.assertEqual(job.count('run: python -B .github/scripts/ci-revocation-diagnostic.py'), 1)
        self.assertEqual(text.count("'-test.count=1'"), 1)
        self.assertIn("'-test.timeout=5m'", text)
        self.assertIn('timeout=330', text)
        self.assertIn("'-test.run=^' + TEST + '$'", text)
        self.assertEqual(gate.TEST, 'TestLANCorePeerApplicationsIntegration')
        for forbidden in ('continue-on-error', 'actions/cache', 'contents: write', 'id-token: write', '*.log', 'core.test'):
            self.assertNotIn(forbidden, job)
        for forbidden in ('print(stdout', 'print(stderr', 'print(data', 'print(event', 'print(exc', 'check_output', 'shell=True'):
            self.assertNotIn(forbidden, text)
        self.assertIn("'releaseValidation': False", text)
        self.assertIn("'race': False", text)
        self.assertIn("report['observations'] = []", text)

    def test_main_retains_failures_and_never_exports_raw_output(self):
        for state, code, payload, cleanup_failure in (
                ('completed', 0, raw(events()), False),
                ('completed', 0, raw(events()), True),
                ('nonzero', 1, raw(events(True, [marker('stream'), marker()])), False),
                ('nonzero', 1, raw(events(True, [marker('stream'), marker()])), True),
                ('nonzero', 1, raw(events(True, [marker('stream'), marker()])), 'retained'),
                ('timeout', -9, b'synthetic-secret-raw-timeout', False)):
            with self.subTest(state=state, cleanup_failure=cleanup_failure), tempfile.TemporaryDirectory() as directory:
                calls = []
                def checked(command, **kwargs):
                    calls.append(command)
                    if command[:2] == ['git', 'rev-parse']:
                        return (b'a' if command[-1] == 'HEAD' else b'b') * 40 + b'\n'
                    if command[:2] == ['go', 'env']:
                        return b'{}'
                    return b''
                captures = [('completed', 0, payload, b'synthetic-secret-converter-stderr')]
                captures.insert(0, (state, code, b'synthetic-secret-test-stdout', b'synthetic-secret-test-stderr'))
                output = io.StringIO()
                with contextlib.ExitStack() as stack:
                    stack.enter_context(patch.object(gate.sys, 'platform', 'darwin'))
                    stack.enter_context(patch.dict(os.environ, {'GITHUB_ACTIONS': 'true', 'RUNNER_TEMP': directory,
                        'GITHUB_OUTPUT': directory + '/output', 'GITHUB_EVENT_PATH': directory + '/event',
                        'GITHUB_EVENT_NAME': 'create', 'GITHUB_REPOSITORY': 'webkaz-labs/sobalink',
                        'GITHUB_REF': 'fixed', 'GITHUB_SHA': 'a' * 40}))
                    stack.enter_context(patch.object(gate, 'checked', side_effect=checked))
                    stack.enter_context(patch.object(gate, 'module', return_value=SimpleNamespace(select=lambda *args: 'revocation')))
                    stack.enter_context(patch.object(gate.native, 'identities', return_value={}))
                    stack.enter_context(patch.object(gate.native, 'capture', side_effect=captures))
                    stack.enter_context(patch.object(gate.source, 'source_inventory', return_value={'fixed': 'a' * 64}))
                    stack.enter_context(patch.object(gate.source, 'digest', return_value='a' * 64))
                    stack.enter_context(patch.object(gate.source.summary, 'read_fixed', return_value={}))
                    if cleanup_failure:
                        effect = (lambda path: None) if cleanup_failure == 'retained' else OSError('synthetic-secret-cleanup')
                        stack.enter_context(patch.object(gate.shutil, 'rmtree', side_effect=effect))
                    stack.enter_context(contextlib.redirect_stdout(output))
                    result = gate.main([])
                report = json.loads((Path(directory) / 'soba-revocation-sanitized/result.json').read_text())
                provenance = (Path(directory) / 'soba-revocation-sanitized/provenance.json').read_text()
                passed = state == 'completed' and not cleanup_failure
                self.assertEqual(result, 0 if passed else 1)
                self.assertEqual(report['diagnosticPassed'], passed)
                self.assertEqual(report['testStatus'], {'completed': 'pass', 'nonzero': 'fail', 'timeout': 'unavailable'}[state])
                self.assertEqual(report['processStatus'], state)
                self.assertFalse(report['releaseValidation'])
                self.assertNotIn('synthetic-secret', json.dumps(report) + provenance + output.getvalue())
                self.assertEqual(len([call for call in calls if call[:3] == ['go', 'test', '-c']]), 1)
                private_directories = [path for path in Path(directory).glob('soba-revocation-*')
                                       if path.name != 'soba-revocation-sanitized']
                self.assertEqual(len(private_directories), 1 if cleanup_failure else 0)
                self.assertEqual(report['privateCleanupVerified'], not cleanup_failure)
                if cleanup_failure:
                    self.assertEqual(report['firstFailure'], 'cleanup' if state == 'completed' else 'runtime')
                    self.assertEqual(report['testStatus'], 'pass' if state == 'completed' else 'fail')

    def test_unsupported_entry_never_launches_and_prints_no_arguments(self):
        output = io.StringIO()
        with patch.object(gate.native, 'capture', side_effect=AssertionError('must not run')), contextlib.redirect_stdout(output):
            self.assertEqual(gate.main(['synthetic-secret-argument']), 1)
        self.assertNotIn('synthetic-secret', output.getvalue())


if __name__ == '__main__':
    unittest.main()
