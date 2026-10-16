"""No network or release actions: bind historical compatibility and fail closed."""
import base64
import hashlib
import importlib.util
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

PATH = pathlib.Path(__file__).with_name('historical-resource-validation.py')
spec = importlib.util.spec_from_file_location('historical_resource_validation', PATH)
history = importlib.util.module_from_spec(spec)
spec.loader.exec_module(history)
ENV = {'RELEASE_VERSION': history.VERSION, 'TESTED_COMMIT': history.COMMIT, 'RELEASE_ID': history.RELEASE_ID}
SOURCE = b'''calls = []
def filenames(version): return {'example.json'}
def release_state(version, commit, required=False, published=False):
    calls.append(('state', version, commit, required, published))
    return {}, {'id': 402850401}
def check_assets(root, version, commit, bundle=False): calls.append('assets')
def check_bundle(root, version, commit): calls.append('bundle')
'''


class HistoricalResourceValidationTests(unittest.TestCase):
    def validator(self):
        with patch.object(history, 'fetch_source', return_value=SOURCE), \
             patch.object(history, 'VALIDATOR_SHA256', hashlib.sha256(SOURCE).hexdigest()):
            return history.load_validator(ENV)

    def test_pins_exact_public_baseline_and_validator(self):
        self.assertEqual(ENV, {'RELEASE_VERSION': '0.3.0-alpha.2',
                              'TESTED_COMMIT': '00cc6a99809df77bf1754936ea7bf5ca4c5d0741',
                              'RELEASE_ID': '402850401'})
        self.assertEqual(history.VALIDATOR_SHA256, 'cd82d293da01ce947bcd78e79494a498e8058d6c495f77cbb03fd3d7b84d7bcd')
        self.assertEqual(history.VALIDATOR_URL, 'https://raw.githubusercontent.com/webkaz-labs/sobalink/'
                         '00cc6a99809df77bf1754936ea7bf5ca4c5d0741/.github/scripts/release-validation.py')

    def test_changed_baseline_rejected_before_fetch(self):
        for key in ENV:
            with self.subTest(key=key), patch.object(history, 'fetch_source') as fetch:
                with self.assertRaisesRegex(ValueError, 'historical-baseline-mismatch'):
                    history.load_validator(dict(ENV, **{key: 'changed'}))
                fetch.assert_not_called()

    def test_changed_validator_rejected_before_compile_or_execution(self):
        with patch.object(history, 'fetch_source', return_value=SOURCE), patch('builtins.compile') as compile_source, \
             patch('builtins.exec') as execute:
            with self.assertRaisesRegex(ValueError, 'historical-validator-digest'):
                history.load_validator(ENV)
            compile_source.assert_not_called()
            execute.assert_not_called()

    def test_release_id_and_published_source_guards_remain_required(self):
        validator = self.validator()
        validator.release_state(history.VERSION, history.COMMIT, required=True, published=True)
        self.assertEqual(validator.calls[0], ('state', history.VERSION, history.COMMIT, True, True))
        for version, commit, required, published in [('0.5.1-alpha.1', history.COMMIT, True, True),
                (history.VERSION, 'b' * 40, True, True), (history.VERSION, history.COMMIT, False, True),
                (history.VERSION, history.COMMIT, True, False)]:
            with self.assertRaisesRegex(ValueError, 'historical-baseline-mismatch'):
                validator.release_state(version, commit, required=required, published=published)
        changed = SOURCE.replace(b"{'id': 402850401}", b"{'id': 1}")
        with patch.object(history, 'fetch_source', return_value=changed), \
             patch.object(history, 'VALIDATOR_SHA256', hashlib.sha256(changed).hexdigest()):
            wrong_id = history.load_validator(ENV)
        with self.assertRaisesRegex(ValueError, 'historical-release-id'):
            wrong_id.release_state(history.VERSION, history.COMMIT, required=True, published=True)

    def test_duplicate_json_refused_before_historical_asset_checker(self):
        validator = self.validator()
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            (root / 'example.json').write_text('{"key":1,"key":2}')
            with self.assertRaisesRegex(AssertionError, 'duplicate source JSON key'):
                validator.check_assets(root, history.VERSION, history.COMMIT)
            self.assertEqual(validator.calls, [])
            (root / 'example.json').write_text('{"key":1}')
            validator.check_assets(root, history.VERSION, history.COMMIT)
            self.assertEqual(validator.calls, ['assets'])

    def test_signed_payload_duplicate_refused_before_bundle_checker(self):
        validator = self.validator()
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            (root / 'example.json').write_text('{}')
            payload = base64.b64encode(b'{"subject":[],"subject":[]}').decode()
            (root / 'packslip.sigstore.json').write_text(json.dumps({'dsseEnvelope': {'payload': payload}}))
            with self.assertRaisesRegex(AssertionError, 'duplicate source JSON key'):
                validator.check_bundle(root, history.VERSION, history.COMMIT)
            self.assertEqual(validator.calls, [])

    def test_inventory_and_other_version_refused(self):
        validator = self.validator()
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with self.assertRaisesRegex(ValueError, 'historical-asset-inventory'):
                validator.check_assets(root, history.VERSION, history.COMMIT)
            with self.assertRaisesRegex(ValueError, 'historical-baseline-mismatch'):
                validator.check_assets(root, '0.5.1-alpha.1', history.COMMIT)


if __name__ == '__main__':
    unittest.main()
