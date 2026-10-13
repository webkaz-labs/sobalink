"""Synthetic fixed-summary validation; no browsers, sockets or external writes."""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SOURCE = Path(__file__).with_name('ci-browser-summary.py')
SPEC = importlib.util.spec_from_file_location('fixed_browser_summary', SOURCE)
summary = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(summary)


class FixedBrowserSummaryTests(unittest.TestCase):
    def test_exact_success_is_reconstructed_for_each_case(self):
        for case, value in (('setup', summary.nearby.BROWSER_EXPECTED), ('text', summary.TEXT_EXPECTED)):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as tmp:
                directory = Path(tmp)
                path = directory / (case + '-native-summary.json')
                path.write_text(json.dumps(value, indent=4))
                self.assertTrue(summary.reconstruct(case, 0, directory))
                self.assertEqual(json.loads(path.read_text()), value)
                self.assertEqual(path.read_text(), json.dumps(value, sort_keys=True, separators=(',', ':')) + '\n')

    def test_only_reconstructed_evidence_reaches_separate_export_directory(self):
        with tempfile.TemporaryDirectory() as tmp:
            raw, exported = Path(tmp) / 'private', Path(tmp) / 'exports'
            raw.mkdir()
            exported.mkdir()
            path = raw / 'text-native-summary.json'
            path.write_text(json.dumps(summary.TEXT_EXPECTED, indent=4))
            (raw / 'unrelated-private.json').write_text('synthetic-private')
            self.assertTrue(summary.reconstruct('text', 0, raw, exported))
            self.assertFalse(path.exists())
            self.assertEqual([p.name for p in exported.iterdir()], ['text-native-summary.json'])
            self.assertEqual(json.loads((exported / path.name).read_text()), summary.TEXT_EXPECTED)

    def test_missing_wrong_duplicate_or_oversized_evidence_never_exports(self):
        for case, expected in (('setup', summary.nearby.BROWSER_EXPECTED), ('text', summary.TEXT_EXPECTED)):
            bad = [None, '{}', '{"schema":1,"schema":1}', 'x' * 8193]
            for key, value in expected.items():
                changed = copy.deepcopy(expected)
                changed[key] = not value if type(value) is bool else 'synthetic-invalid'
                bad.append(json.dumps(changed))
            changed = dict(expected, unrelated='synthetic-invalid')
            bad.append(json.dumps(changed))
            changed = dict(expected, observed=True)
            bad.append(json.dumps(changed))
            for index, raw in enumerate(bad):
                with self.subTest(case=case, mutation=index), tempfile.TemporaryDirectory() as tmp:
                    directory = Path(tmp)
                    path = directory / (case + '-native-summary.json')
                    if raw is not None:
                        path.write_text(raw)
                    self.assertFalse(summary.reconstruct(case, 0, directory))
                    self.assertFalse(path.exists())

    def test_nonzero_case_exit_never_accepts_success_summary(self):
        for case, expected in (('setup', summary.nearby.BROWSER_EXPECTED), ('text', summary.TEXT_EXPECTED)):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as tmp:
                directory = Path(tmp)
                path = directory / (case + '-native-summary.json')
                path.write_text(json.dumps(expected))
                self.assertFalse(summary.reconstruct(case, 1, directory))
                self.assertFalse(path.exists())

    def test_setup_failure_is_closed_reconstructed_and_cannot_coexist_with_success(self):
        fixed = {'schema': 1, 'phase': 'open-a', 'errorKind': 'timeout'}
        for status in (0, 1):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as tmp:
                directory = Path(tmp)
                success, failure = directory / 'setup-native-summary.json', directory / 'setup-native-failure.json'
                success.write_text(json.dumps(summary.nearby.BROWSER_EXPECTED))
                failure.write_text(json.dumps(fixed, indent=4))
                self.assertFalse(summary.reconstruct('setup', status, directory))
                self.assertFalse(success.exists())
                self.assertEqual(json.loads(failure.read_text()), fixed)

    def test_large_closed_core_failure_vector_is_reconstructed(self):
        peer = {'observed': True, 'state': 'connected', 'reason': 'none',
                'checks': {key: True for key in summary.nearby.BROWSER_CORE_CHECKS},
                'freshHello': 'not_run'}
        fixed = {'schema': 1, 'phase': 'core-connected', 'errorKind': 'core_evidence',
                 'core': {'checkpointArrived': True, 'stage': 'hello_a',
                          'peers': [copy.deepcopy(peer), copy.deepcopy(peer)]}}
        encoded = json.dumps(fixed, indent=2)
        self.assertGreater(len(encoded.encode()), 1024)
        self.assertLessEqual(len(encoded.encode()), summary.nearby.BROWSER_FAILURE_MAX_BYTES)
        for mutation in ('valid', 'unknown_nested_field', 'nonboolean_check', 'oversized'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as tmp:
                raw, exported = Path(tmp) / 'private', Path(tmp) / 'exports'
                raw.mkdir()
                exported.mkdir()
                value = copy.deepcopy(fixed)
                if mutation == 'unknown_nested_field':
                    value['core']['peers'][0]['endpoint'] = 'synthetic-private'
                elif mutation == 'nonboolean_check':
                    value['core']['peers'][0]['checks']['paired'] = 1
                text = json.dumps(value, indent=2)
                if mutation == 'oversized':
                    text += ' ' * summary.nearby.BROWSER_FAILURE_MAX_BYTES
                failure = raw / 'setup-native-failure.json'
                failure.write_text(text)
                (raw / 'setup-native-summary.json').write_text(json.dumps(summary.nearby.BROWSER_EXPECTED))
                self.assertFalse(summary.reconstruct('setup', 0, raw, exported))
                self.assertFalse(failure.exists())
                self.assertFalse((raw / 'setup-native-summary.json').exists())
                self.assertFalse((exported / 'setup-native-summary.json').exists())
                if mutation == 'valid':
                    output = exported / failure.name
                    self.assertEqual(json.loads(output.read_text()), fixed)
                    self.assertEqual(output.read_text(), json.dumps(fixed, sort_keys=True, separators=(',', ':')) + '\n')
                else:
                    self.assertEqual(list(exported.iterdir()), [])

    def test_invalid_failure_drops_both_inputs(self):
        for value in ({'schema': 1, 'phase': 'synthetic-private', 'errorKind': 'timeout'},
                      {'schema': 1, 'phase': 'open-a', 'errorKind': 'timeout', 'raw': 'synthetic-private'}):
            with self.subTest(value=value), tempfile.TemporaryDirectory() as tmp:
                directory = Path(tmp)
                success, failure = directory / 'setup-native-summary.json', directory / 'setup-native-failure.json'
                success.write_text(json.dumps(summary.nearby.BROWSER_EXPECTED))
                failure.write_text(json.dumps(value))
                self.assertFalse(summary.reconstruct('setup', 0, directory))
                self.assertFalse(success.exists())
                self.assertFalse(failure.exists())

    def test_symlink_input_is_removed_without_reading_or_changing_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp)
            target = directory / 'synthetic-target'
            target.write_text('synthetic-private')
            path = directory / 'text-native-summary.json'
            try:
                path.symlink_to(target)
            except OSError:
                self.skipTest('symlink creation unavailable')
            self.assertFalse(summary.reconstruct('text', 0, directory))
            self.assertFalse(path.is_symlink())
            self.assertEqual(target.read_text(), 'synthetic-private')
