"""Offline report mutations; never launch browsers."""
import copy
import contextlib
import io
from types import SimpleNamespace
from unittest.mock import patch
import importlib.util
import json
from pathlib import Path
import unittest
spec = importlib.util.spec_from_file_location('pairing_ui', Path(__file__).with_name('ci-pairing-ui.py'))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


def fixture():
    return {'errors': [], 'stats': {'expected': 12, 'unexpected': 0, 'flaky': 0, 'skipped': 0},
            'suites': [{'specs': [{'title': name, 'file': 'pairing-v2.acceptance.mjs', 'ok': True,
            'tests': [{'expectedStatus': 'passed', 'status': 'expected', 'results': [
                {'status': 'passed', 'retry': 0, 'errors': [], 'stdout': ['synthetic-private']}]}]}
            for name in gate.EXPECTED]}]}


class PairingUiTests(unittest.TestCase):
    def test_exact_inventory(self):
        self.assertTrue(gate.validate(json.dumps(fixture()).encode(), 0))

    def test_missing_duplicate_unknown_and_wrong_file(self):
        for mode in ('missing', 'duplicate', 'unknown', 'file'):
            value = fixture(); specs = value['suites'][0]['specs']
            if mode == 'missing': specs.pop()
            elif mode == 'duplicate': specs[-1] = copy.deepcopy(specs[0])
            elif mode == 'unknown': specs[0]['title'] = 'other'
            else: specs[0]['file'] = 'other.mjs'
            with self.subTest(mode=mode), self.assertRaises(ValueError): gate.validate(json.dumps(value).encode(), 0)

    def test_skip_flaky_retry_and_case_errors(self):
        for key, replacement in [('status', 'skipped'), ('status', 'failed'), ('retry', 1), ('retry', False),
                                 ('errors', ['synthetic']), ('error', {'message': 'synthetic'})]:
            value = fixture(); value['suites'][0]['specs'][0]['tests'][0]['results'][0][key] = replacement
            with self.subTest(key=key, replacement=replacement), self.assertRaises(ValueError): gate.validate(json.dumps(value).encode(), 0)
        for level, key, replacement in [('spec', 'ok', False), ('test', 'status', 'flaky'), ('test', 'expectedStatus', 'skipped')]:
            value = fixture(); s = value['suites'][0]['specs'][0]; (s if level == 'spec' else s['tests'][0])[key] = replacement
            with self.assertRaises(ValueError): gate.validate(json.dumps(value).encode(), 0)
        for mode in ('empty', 'duplicate'):
            value = fixture(); results = value['suites'][0]['specs'][0]['tests'][0]['results']; results[:] = [] if mode == 'empty' else results * 2
            with self.assertRaises(ValueError): gate.validate(json.dumps(value).encode(), 0)

    def test_totals_and_report_failure(self):
        for key in ('expected', 'unexpected', 'flaky', 'skipped'):
            for replacement in (True, -1, 9):
                value = fixture(); value['stats'][key] = replacement
                with self.assertRaises(ValueError): gate.validate(json.dumps(value).encode(), 0)
        value = fixture(); value['errors'] = ['synthetic']
        with self.assertRaises(ValueError): gate.validate(json.dumps(value).encode(), 0)

    def test_private_capture_exit_and_exact_command(self):
        for state, code, payload in [('completed', 0, json.dumps(fixture()).encode()),
                ('nonzero', 1, b'synthetic-private'), ('timeout', None, b'synthetic-private'),
                ('completed', 0, b'synthetic-private')]:
            commands = []
            def capture(command, **kwargs):
                commands.append((command, kwargs))
                return state, code, payload, b'synthetic-private-stderr'
            loader = SimpleNamespace(create_module=lambda spec: None, exec_module=lambda module: setattr(module, 'capture', capture))
            original_spec = importlib.util.spec_from_file_location
            def source_spec(name, path):
                result = original_spec(name, path)
                result.loader = loader
                return result
            output = io.StringIO()
            with patch.object(gate.importlib.util, 'spec_from_file_location', side_effect=source_spec), contextlib.redirect_stdout(output):
                result = gate.main([])
            self.assertEqual(result, 0 if state == 'completed' and payload.startswith(b'{') else 1)
            self.assertNotIn('synthetic-private', output.getvalue())
            self.assertEqual(len(commands), 1)
            command, args = commands[0]
            self.assertEqual(command, ['node', 'node_modules/@playwright/test/cli.js', 'test',
                '--config=playwright.pairing-ui.config.mjs', '--forbid-only', '--retries=0', '--workers=1', '--reporter=json'])
            self.assertEqual(args['timeout'], 240)
            self.assertEqual(args['limit'], 8 * 1024 * 1024)
            self.assertNotIn('SOBA_CHROMIUM_PATH', args['env'])
            self.assertNotIn('PLAYWRIGHT_JSON_OUTPUT_NAME', args['env'])

    def test_malformed_process_capture(self):
        raw = json.dumps(fixture()).encode()
        for value in (raw[:-1], b'null', b'[]', b'{}', b'\xff', raw.replace(b'"errors": []', b'"errors": [], "errors": []', 1), b'x' * (gate.LIMIT + 1)):
            with self.assertRaises((ValueError, TypeError)): gate.validate(value, 0)
        for code in (1, None, False, -9):
            with self.assertRaises(ValueError): gate.validate(raw, code)


if __name__ == '__main__': unittest.main()
