"""Synthetic no-skip native CI policy; never execute sockets or Go tests."""
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('ci-nearby-required.py')
SPEC = importlib.util.spec_from_file_location('ci_nearby_required', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), gate.__dict__)


class RequiredNearbyTests(unittest.TestCase):
    def test_required_entrypoint_only_accepts_exact_verified_native_proof(self):
        native = gate.native_module()
        fixtures = native.module('test_ci_nearby_native')
        for status, state, code, terminal, expected in (
                ('verified', 'completed', 0, 'pass', 0),
                ('unavailable', 'completed', 0, 'skip', 1),
                ('not_run', 'completed', 0, 'skip', 1),
                ('failed', 'nonzero', 1, 'fail', 1),
                ('verified', 'nonzero', 1, 'pass', 1),
                ('verified', 'completed', 0, 'skip', 1),
                ('verified', 'timeout', -9, 'pass', 1),
                ('verified', 'output_limit', -9, 'pass', 1)):
            with self.subTest(status=status, state=state), tempfile.TemporaryDirectory() as temporary:
                calls = []
                def checked(command, **kwargs):
                    calls.append((command, kwargs))
                    if command[:2] == ['go', 'env']:
                        return b'{}'
                    if command == ['git', 'rev-parse', 'HEAD']:
                        return b'a' * 40
                    if command[:3] == ['go', 'test', '-race']:
                        Path(command[command.index('-o') + 1]).write_bytes(b'synthetic binary')
                    return b''
                record = fixtures.capability(status)
                converted = fixtures.raw(fixtures.events(record, terminal))
                results = [(state, code, b'synthetic-private-output', b'synthetic-private-error'),
                           ('completed', 0, converted, b'synthetic-private-converter')]
                with patch.dict(os.environ, GITHUB_ACTIONS='true', GITHUB_SHA='a' * 40,
                        RUNNER_TEMP=temporary, SOBALINK_NEARBY_NATIVE_PLATFORM='linux-arm64'), \
                     patch.object(gate.sys, 'platform', 'linux'), \
                     patch.object(gate, 'native_module', return_value=native), \
                     patch.object(native, 'identities'), \
                     patch.object(native, 'checked', side_effect=checked), \
                     patch.object(native, 'capture', side_effect=results) as capture, \
                     patch('sys.stdout', new_callable=io.StringIO) as output:
                    self.assertEqual(gate.main([]), expected)
                lines = output.getvalue().splitlines()
                result = json.loads(lines[0])
                self.assertEqual(set(result), {'schema', 'nativeDiscoveryVerified', 'nativeDiscovery',
                    'physicalLANVerified', 'automaticCoreBrowserVerified', 'releaseValidation'})
                self.assertEqual(result['nativeDiscoveryVerified'], expected == 0)
                if state in ('completed', 'nonzero'):
                    self.assertEqual(result['nativeDiscovery'], record)
                else:
                    self.assertIsNone(result['nativeDiscovery'])
                for key in ('physicalLANVerified', 'automaticCoreBrowserVerified', 'releaseValidation'):
                    self.assertFalse(result[key])
                self.assertEqual(lines[1].startswith('PASS:'), expected == 0)
                self.assertNotIn('synthetic-private', output.getvalue())
                self.assertNotIn(temporary, output.getvalue())
                self.assertFalse(list(Path(temporary).iterdir()))
                native_call = capture.call_args_list[0]
                self.assertEqual(native_call.args[0][1:], ['-test.v=test2json', '-test.run=^' + native.TEST + '$', '-test.count=1', '-test.timeout=20s'])
                self.assertEqual(native_call.kwargs['timeout'], 30)
                self.assertEqual(native_call.kwargs['env']['SOBALINK_NATIVE_NEARBY_LOOPBACK'], '1')
                self.assertEqual(sum(command == ['git', 'diff', '--quiet', 'HEAD', '--'] for command, _ in calls), 2)
                self.assertEqual(sum(command == ['git', 'ls-files', '--others', '-z', '--', 'internal/nearby'] for command, _ in calls), 2)
                compile_env = next(kwargs['env'] for command, kwargs in calls if command[:3] == ['go', 'test', '-race'])
                self.assertEqual(compile_env['GOFLAGS'], '-mod=readonly -buildvcs=false')
                self.assertEqual(compile_env['GOPROXY'], 'off')

    def test_unqualified_platform_source_change_and_bad_host_fail_before_runtime(self):
        native = gate.native_module()
        for kind in ('unqualified', 'wrong-source', 'dirty-source', 'extra-source', 'wrong-host'):
            def checked(command, **kwargs):
                if command[:2] == ['go', 'env']:
                    return b'{}'
                if command == ['git', 'rev-parse', 'HEAD']:
                    return (b'b' if kind == 'wrong-source' else b'a') * 40
                if command[:2] == ['git', 'diff'] and kind == 'dirty-source':
                    raise ValueError('synthetic-private')
                if command[:2] == ['git', 'ls-files'] and kind == 'extra-source':
                    return b'synthetic-private-file'
                return b''
            with self.subTest(kind=kind), patch.dict(os.environ, GITHUB_ACTIONS='true', GITHUB_SHA='a' * 40,
                    SOBALINK_NEARBY_NATIVE_PLATFORM='windows-amd64' if kind == 'unqualified' else 'linux-arm64'), \
                 patch.object(gate.sys, 'platform', 'linux'), \
                 patch.object(gate, 'native_module', return_value=native), \
                 patch.object(native, 'identities', side_effect=ValueError('synthetic-private') if kind == 'wrong-host' else None), \
                 patch.object(native, 'checked', side_effect=checked), \
                 patch.object(native, 'capture') as capture, \
                 patch('sys.stdout', new_callable=io.StringIO) as output:
                self.assertEqual(gate.main([]), 1)
                capture.assert_not_called()
                self.assertNotIn('synthetic-private', output.getvalue())

    def test_fixed_boundary_reuses_sanitizer_and_never_changes_diagnostic_unavailable(self):
        self.assertEqual(gate.QUALIFIED_TARGETS, ('linux-amd64', 'linux-arm64'))
        source = SCRIPT.read_text()
        self.assertIn('native.native_events(data, code)', source)
        self.assertIn("evidence['status'] == 'verified'", source)
        self.assertIn('timeout=300', source)
        self.assertIn('timeout=30, limit=native.RUNTIME_BYTES', source)
        self.assertIn('timeout=15, limit=2 * native.RUNTIME_BYTES', source)
        self.assertIn("'-test.timeout=20s'", source)
        for forbidden in ('go mod download', 'prepare-engine', 'npm', 'shell=True', 'sudo', 'str(exc)',
                          "'./internal/core'", 'write_bytes(', 'print(stdout', 'print(stderr'):
            self.assertNotIn(forbidden, source)
        diagnostic = Path(__file__).with_name('ci-nearby-native.py').read_text()
        self.assertIn("report['diagnosticPassed'] = report['eventsAccepted'] and state == 'completed'", diagnostic)
        with patch.dict(os.environ, GITHUB_ACTIONS='false'), patch('sys.stdout', new_callable=io.StringIO):
            self.assertEqual(gate.main([]), 1)
            self.assertEqual(gate.main(['--unknown']), 1)


if __name__ == '__main__':
    unittest.main()
