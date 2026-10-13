"""Synthetic fixed-summary validation; no browsers, sockets or external writes."""
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import textwrap
import tempfile
import unittest
from unittest import mock

SOURCE = Path(__file__).with_name('ci-browser-summary.py')
SPEC = importlib.util.spec_from_file_location('fixed_browser_summary', SOURCE)
summary = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(summary)


class FixedBrowserSummaryTests(unittest.TestCase):
    @unittest.skipUnless(os.name == 'posix', 'Browser job uses POSIX private directory ownership')
    def test_workflow_exclusively_prepares_optional_private_illustrations(self):
        workflow = (SOURCE.parents[1] / 'workflows' / 'ci.yml').read_text()
        start = workflow.index('          export SOBA_SETUP_BROWSER_SCREENSHOTS_PREPARED=0\n')
        finish = workflow.index('          status=0\n', start)
        preparation = textwrap.dedent(workflow[start:finish])
        for case in ('fresh', 'existing-directory', 'symlink', 'mkdir-failure'):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                private = root / 'soba-setup-browser-illustrations'
                target = root / 'synthetic-target'
                target.mkdir()
                (target / 'preserved').write_text('synthetic')
                if case == 'existing-directory':
                    private.mkdir(mode=0o700)
                    (private / 'preserved').write_text('synthetic')
                elif case == 'symlink':
                    try:
                        private.symlink_to(target, target_is_directory=True)
                    except OSError:
                        self.skipTest('symlink creation unavailable')
                script = 'set -euo pipefail\n'
                if case == 'mkdir-failure':
                    script += 'mkdir() { return 1; }\n'
                script += preparation + 'printf "%s\\n%s\\n" "$SOBA_SETUP_BROWSER_SCREENSHOTS_PREPARED" "${SOBA_SETUP_BROWSER_SCREENSHOTS-unset}"\n'
                environment = dict(os.environ, SOBA_SETUP_BROWSER_SCREENSHOTS=str(private),
                                   SOBA_SETUP_BROWSER_SCREENSHOTS_PREPARED='1')
                result = subprocess.run(['bash', '-c', script], env=environment,
                                        capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 0)
                self.assertEqual(result.stderr, '')
                prepared, directory = result.stdout.splitlines()
                if case == 'fresh':
                    self.assertEqual((prepared, directory), ('1', str(private)))
                    self.assertEqual(stat.S_IMODE(private.stat().st_mode), 0o700)
                    self.assertEqual(private.stat().st_uid, os.getuid())
                    self.assertEqual(list(private.iterdir()), [])
                    continue
                self.assertEqual((prepared, directory), ('0', 'unset'))
                output = root / 'github-output'
                with mock.patch.object(summary.sys, 'argv', ['ci-browser-summary.py', 'setup', '0']), \
                        mock.patch.dict(summary.os.environ, {'RUNNER_TEMP': str(root),
                                                            'GITHUB_OUTPUT': str(output),
                                                            'SOBA_SETUP_BROWSER_SCREENSHOTS_PREPARED': prepared}), \
                        mock.patch.object(summary, 'reconstruct', return_value=True), \
                        mock.patch.object(summary.nearby, 'module') as exporter, \
                        mock.patch.object(summary.sys, 'stdout', new_callable=io.StringIO) as stdout:
                    self.assertEqual(summary.main(), 0)
                    self.assertEqual(stdout.getvalue(), 'Fixed browser summary accepted\n')
                    exporter.assert_not_called()
                self.assertFalse(output.exists())
                self.assertFalse((root / 'soba-setup-ui-sanitized').exists())
                self.assertEqual((target / 'preserved').read_text(), 'synthetic')
                if case in ('existing-directory', 'symlink'):
                    self.assertEqual((private / 'preserved').read_text(), 'synthetic')
                    self.assertEqual(private.is_symlink(), case == 'symlink')
                else:
                    self.assertFalse(private.exists())

    def test_unprepared_illustrations_are_untouched(self):
        for prepared in (False, None, 0, 1, '1'):
            with self.subTest(prepared=prepared), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                private = root / 'soba-setup-browser-illustrations'
                private.mkdir(mode=0o700)
                (private / 'preserved').write_text('synthetic')
                with mock.patch.object(summary.nearby, 'module') as exporter:
                    self.assertFalse(summary.initial_illustrations(root, accepted=True, prepared=prepared))
                    exporter.assert_not_called()
                self.assertEqual((private / 'preserved').read_text(), 'synthetic')

    def test_optional_output_flag_failure_preserves_accepted_result_without_raw_error(self):
        with mock.patch.object(summary.sys, 'argv', ['ci-browser-summary.py', 'setup', '0']), \
                mock.patch.dict(summary.os.environ, {'RUNNER_TEMP': '/synthetic', 'GITHUB_OUTPUT': '/synthetic/output',
                                                      'SOBA_SETUP_BROWSER_SCREENSHOTS_PREPARED': '1'}), \
                mock.patch.object(summary, 'reconstruct', return_value=True), \
                mock.patch.object(summary, 'initial_illustrations', return_value=True) as images, \
                mock.patch('builtins.open', side_effect=OSError('synthetic private path')), \
                mock.patch.object(summary.sys, 'stdout', new_callable=io.StringIO) as output:
            self.assertEqual(summary.main(), 0)
            images.assert_called_once_with(Path('/synthetic'), accepted=True, prepared=True)
            self.assertEqual(output.getvalue(), 'Fixed browser summary accepted\n')

    def test_initial_illustrations_require_accepted_receipt_and_joined_cleanup(self):
        for accepted in (False, True):
            with self.subTest(accepted=accepted), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                private = root / 'soba-setup-browser-illustrations'
                private.mkdir()
                (private / 'synthetic-private').write_text('synthetic')
                exporter = mock.Mock()
                exporter.export_initial_screenshots.return_value = True
                with mock.patch.object(summary.nearby, 'module', return_value=exporter) as loader:
                    self.assertEqual(summary.initial_illustrations(root, accepted=accepted, prepared=True), accepted)
                self.assertFalse(private.exists())
                if accepted:
                    loader.assert_called_once_with('ci-nearby-screenshots')
                    exporter.export_initial_screenshots.assert_called_once_with(
                        private, root / 'soba-setup-ui-sanitized', verified=True)
                else:
                    loader.assert_not_called()

    def test_initial_illustrations_cleanup_failure_cannot_publish(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            private = root / 'soba-setup-browser-illustrations'
            private.mkdir()
            exporter = mock.Mock()
            exporter.export_initial_screenshots.return_value = True
            with mock.patch.object(summary.nearby, 'module', return_value=exporter), \
                    mock.patch.object(summary.shutil, 'rmtree', side_effect=OSError('synthetic')):
                self.assertFalse(summary.initial_illustrations(root, accepted=True, prepared=True))

    def test_initial_illustrations_failed_export_is_optional_and_cleans_private_input(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            private = root / 'soba-setup-browser-illustrations'
            private.mkdir()
            exporter = mock.Mock()
            exporter.export_initial_screenshots.return_value = False
            with mock.patch.object(summary.nearby, 'module', return_value=exporter):
                self.assertFalse(summary.initial_illustrations(root, accepted=True, prepared=True))
            self.assertFalse(private.exists())

    def test_initial_illustrations_symlink_does_not_remove_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            target = root / 'synthetic-target'
            target.mkdir()
            (target / 'preserved').write_text('synthetic')
            private = root / 'soba-setup-browser-illustrations'
            try:
                private.symlink_to(target, target_is_directory=True)
            except OSError:
                self.skipTest('symlink creation unavailable')
            self.assertFalse(summary.initial_illustrations(root, accepted=True, prepared=True))
            self.assertFalse(private.is_symlink())
            self.assertEqual((target / 'preserved').read_text(), 'synthetic')

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

    def test_cleanup_failure_reconstruction_preserves_prior_connection_without_accepting(self):
        peer = {'observed': True, 'state': 'connected', 'reason': 'none',
                'checks': {key: True for key in summary.nearby.BROWSER_CORE_CHECKS}, 'freshHello': 'passed'}
        cleanup_peer = {**{key: True for key in summary.nearby.BROWSER_CLEANUP_PEER_FLAGS},
                        'cancelState': 'cancelling', 'cancelReason': 'none', 'state': 'connected',
                        'reason': 'none', 'freshHello': 'passed', 'listener': 'dial_rejected', 'childExit': 'joined'}
        fixed = {'schema': 1, 'phase': 'core-cleanup', 'errorKind': 'core_evidence',
                 'core': {'checkpointArrived': True, 'stage': 'acknowledged', 'peers': [copy.deepcopy(peer), copy.deepcopy(peer)]},
                 'cleanup': {**{key: True for key in summary.nearby.BROWSER_CLEANUP_FLAGS}, 'contextEnded': False,
                             'stage': 'summary_close', 'peer': 'none', 'peers': [copy.deepcopy(cleanup_peer), copy.deepcopy(cleanup_peer)]}}
        child = {'wait': 'exit_one', 'runtime': dict.fromkeys(summary.nearby.BROWSER_CHILD_RUNTIME, False),
                 'areas': dict.fromkeys(summary.nearby.BROWSER_CHILD_AREAS, False),
                 'fixture': {'schema': 1, 'exitRequested': True, 'replyWritten': True, 'testFailed': True,
                             'coreClose': 'failed', 'coreErrors': dict.fromkeys(summary.nearby.BROWSER_CHILD_CLOSE_ERRORS, True),
                             'lockClose': 'passed', 'lockErrors': dict.fromkeys(summary.nearby.BROWSER_CHILD_CLOSE_ERRORS, False)}}
        for peer in fixed['cleanup']['peers']:
            peer['termination'] = copy.deepcopy(child)
        encoded = json.dumps(fixed)
        self.assertLessEqual(len(encoded.encode()), summary.nearby.BROWSER_FAILURE_MAX_BYTES)
        for mutation in ('valid', 'unknown_field', 'integer_boolean', 'private_outcome', 'private_child', 'integer_child', 'oversized'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as tmp:
                directory = Path(tmp)
                value = copy.deepcopy(fixed)
                if mutation == 'unknown_field':
                    value['cleanup']['peers'][0]['message'] = 'synthetic-private'
                if mutation == 'integer_boolean':
                    value['cleanup']['browserReceiptVerified'] = 1
                if mutation == 'private_outcome':
                    value['cleanup']['peers'][1]['childExit'] = 'synthetic-private'
                if mutation == 'private_child':
                    value['cleanup']['peers'][0]['termination']['fixture']['error'] = 'synthetic-private'
                if mutation == 'integer_child':
                    value['cleanup']['peers'][1]['termination']['runtime']['panic'] = 1
                text = json.dumps(value)
                if mutation == 'oversized':
                    text += ' ' * summary.nearby.BROWSER_FAILURE_MAX_BYTES
                failure, success = directory / 'setup-native-failure.json', directory / 'setup-native-summary.json'
                failure.write_text(text)
                success.write_text(json.dumps(summary.nearby.BROWSER_EXPECTED))
                self.assertFalse(summary.reconstruct('setup', 0, directory))
                self.assertFalse(success.exists())
                if mutation == 'valid':
                    self.assertEqual(json.loads(failure.read_text()), fixed)
                else:
                    self.assertFalse(failure.exists())

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
