"""Whole-package coverage models, never a Go invocation or Windows assumption."""
import contextlib
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

SCRIPT = Path(__file__).with_name('ci-windows-packages.py')
SPEC = importlib.util.spec_from_file_location('ci_windows_packages', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), gate.__dict__)
INVENTORY = [gate.MODULE + '/cmd/example', gate.CORE, gate.MODULE + '/internal/example']
SOURCE = {'commit': 'a' * 40, 'tree': 'b' * 40, 'policySha256': {'fixture': 'c' * 64}}


def metric(group, code=0):
    return {'suite': 'local-race-windows-' + group, 'elapsed_seconds': 2.0,
            'returncode': code, 'status': 'passed' if code == 0 else 'failed'}


def receipt():
    groups = gate.partition(INVENTORY)
    return {'schema': 1, 'source': SOURCE, 'inventory': INVENTORY, 'inventorySha256': gate.fingerprint(INVENTORY),
            'groups': groups, 'phases': {g: {'packages': groups[g], 'returncode': 0, 'metric': metric(g)} for g in gate.GROUPS},
            'sourceVerified': True, 'inventoryVerified': True, 'complete': True}


class PackagePolicyTests(unittest.TestCase):
    def test_exact_ordered_inventory_and_partition(self):
        self.assertEqual(gate.packages('\n'.join(INVENTORY) + '\n'), INVENTORY)
        self.assertEqual(gate.partition(INVENTORY), {'core': [gate.CORE], 'remaining': [INVENTORY[0], INVENTORY[2]]})
        self.assertTrue(gate.validate_receipt(receipt(), SOURCE, INVENTORY))

    def test_missing_core_duplicate_unexpected_and_malformed_inventory(self):
        for value in ('', '\n', '\n'.join(INVENTORY), gate.CORE + '\n' + gate.CORE + '\n',
                      INVENTORY[0] + '\n', gate.CORE + '\n-private\n', gate.CORE + '\nother.invalid/pkg\n',
                      gate.CORE + '\n' + gate.MODULE + '/../escape\n', gate.CORE + '\n\n'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                gate.packages(value)
        with self.assertRaises(ValueError):
            gate.partition([gate.CORE])

    def test_no_omitted_duplicate_unexpected_or_reordered_group_membership(self):
        good = gate.partition(INVENTORY)
        mutations = [dict(good, core=[]), dict(good, core=[gate.CORE, gate.CORE]),
                     dict(good, remaining=good['remaining'][:-1]),
                     dict(good, remaining=good['remaining'] + [gate.CORE]),
                     dict(good, remaining=good['remaining'] + [gate.MODULE + '/extra']),
                     dict(good, remaining=good['remaining'][::-1]), dict(good, extra=[])]
        for groups in mutations:
            with self.subTest(groups=groups), self.assertRaises(ValueError):
                gate.validate_partition(INVENTORY, groups)

    def test_receipt_rejects_missing_phase_source_or_inventory_and_nonzero(self):
        mutations = []
        for group in gate.GROUPS:
            item = receipt(); del item['phases'][group]; mutations.append(item)
            item = receipt(); item['phases'][group]['returncode'] = 1; mutations.append(item)
            item = receipt(); item['phases'][group]['packages'] = []; mutations.append(item)
            item = receipt(); item['phases'][group]['metric'] = metric(group, 1); mutations.append(item)
            item = receipt(); item['phases'][group]['metric']['suite'] = 'unexpected'; mutations.append(item)
        for key, value in (('source', dict(SOURCE, commit='d' * 40)), ('inventory', INVENTORY[:-1]),
                           ('inventorySha256', 'd' * 64), ('complete', False), ('sourceVerified', False),
                           ('sourceVerified', 1), ('inventoryVerified', False), ('inventoryVerified', 1)):
            item = receipt(); item[key] = value; mutations.append(item)
        item = receipt(); item['phases'] = dict(reversed(list(item['phases'].items()))); mutations.append(item)
        for item in mutations:
            with self.subTest(item=item), self.assertRaises(ValueError):
                gate.validate_receipt(item, SOURCE, INVENTORY)

    def test_metrics_preserve_native_nonzero_and_exact_unselected_command(self):
        for group in gate.GROUPS:
            for code, returned in ((0, 0), (1, 1), (66, 66), (-9, 137)):
                prior = [metric('earlier')]
                with patch.object(gate.metrics, 'read_records', side_effect=[prior, prior + [metric(group, code)]]), patch.object(gate.metrics, 'run_suite', return_value=returned) as run:
                    selected = gate.partition(INVENTORY)[group]
                    result = gate.run_group(group, selected)
                    self.assertEqual(result['returncode'], returned)
                    self.assertEqual(result['metric']['returncode'], code)
                    self.assertEqual(run.call_args.args, ('local-race-windows-' + group,
                        ['go', 'test', '-race', '-count=1', '-timeout=10m'] + selected))

    def test_missing_duplicate_mismatched_or_failed_metric_receipts_fail(self):
        for after in ([], [metric('core'), metric('core')], [metric('remaining')], [metric('core', 1)]):
            with patch.object(gate.metrics, 'read_records', side_effect=[[], after]), patch.object(gate.metrics, 'run_suite', return_value=0), self.assertRaises(ValueError):
                gate.run_group('core', [gate.CORE])

    def test_normal_policy_bytecode_is_allowed_but_untracked_source_is_not(self):
        nearby = gate.module('ci-nearby-diagnostic')
        for suffix in ('cpython-312.pyc', 'cpython-314.opt-1.pyc'):
            gate.verify_untracked('.github/scripts/__pycache__/test_ci_windows_packages.' + suffix + '\0', nearby)
        for value in ('.github/scripts/__pycache__/injected.go\0', '.github/scripts/injected.py\0',
                      'internal/core/injected_test.go\0', '.github/scripts/__pycache__/invalid.pyc\0', 'unterminated'):
            with self.assertRaises(ValueError):
                gate.verify_untracked(value, nearby)
        gate.verify_untracked('.sobalink-deps/tailscale/example.go\0web/node_modules/example.js\0web/tsconfig.tsbuildinfo\0', nearby)

    def test_exact_native_identity_rejects_cross_compilation_and_flag_changes(self):
        env = {'GITHUB_ACTIONS': 'true', 'RUNNER_OS': 'Windows', 'RUNNER_ARCH': 'X64',
               'GOTOOLCHAIN': 'local', 'GOWORK': 'off', 'GOFLAGS': gate.GOFLAGS}
        versions = {'GOVERSION': 'go1.27.1', 'GOHOSTOS': 'windows', 'GOHOSTARCH': 'amd64',
                    'GOOS': 'windows', 'GOARCH': 'amd64', 'CGO_ENABLED': '1'}
        with patch.dict(os.environ, env, clear=True), patch.object(gate.sys, 'platform', 'win32'), patch.object(gate, 'query', return_value=json.dumps(versions)):
            gate.native_identity()
            for key, value in (('GOFLAGS', gate.GOFLAGS + ' -run=Nothing'), ('GOFLAGS', '-short'),
                               ('GOTOOLCHAIN', 'auto'), ('GOWORK', 'other'), ('RUNNER_ARCH', 'ARM64'), ('RUNNER_OS', 'Linux')):
                with patch.dict(os.environ, {key: value}), self.assertRaises(ValueError):
                    gate.native_identity()
            for key in versions:
                with patch.object(gate, 'query', return_value=json.dumps(dict(versions, **{key: 'other'}))), self.assertRaises(ValueError):
                    gate.native_identity()

    def test_source_identity_requires_clean_matching_commit_and_tree(self):
        for bad in ('commit', 'tree', 'dirty', 'untracked'):
            def query(command):
                if command == ['git', 'rev-parse', 'HEAD']:
                    return ('d' * 40 if bad == 'commit' else SOURCE['commit']) + '\n'
                if command == ['git', 'rev-parse', 'HEAD^{tree}']:
                    return 'bad\n' if bad == 'tree' else SOURCE['tree'] + '\n'
                if command[1] == 'status':
                    return 'modified' if bad == 'dirty' else ''
                return 'internal/core/injected_test.go\0' if bad == 'untracked' else ''
            with patch.dict(os.environ, {'GITHUB_SHA': SOURCE['commit']}), patch.object(gate, 'query', side_effect=query), self.assertRaises(ValueError):
                gate.source_identity()


class RunnerTests(unittest.TestCase):
    @contextlib.contextmanager
    def fixture(self, codes=(0, 0), source=None, inventories=None):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            calls = []
            code_iter = iter(codes)
            def run(suite, command):
                calls.append((suite, command))
                code = next(code_iter)
                gate.metrics.append_record(dict(metric(suite.removeprefix('local-race-windows-'), code)))
                return code
            inventories = inventories or [INVENTORY, INVENTORY]
            with patch.dict(os.environ, {'RUNNER_TEMP': directory, 'SOBALINK_CI_METRICS': str(path / 'metrics.jsonl')}), patch.object(gate, 'native_identity'), patch.object(gate, 'source_identity', side_effect=source or [SOURCE, SOURCE]), patch.object(gate, 'query', side_effect=['\n'.join(names) + '\n' for names in inventories]), patch.object(gate.metrics, 'run_suite', side_effect=run), contextlib.redirect_stdout(io.StringIO()):
                yield path, calls

    def test_both_complete_serial_phases_and_saved_receipt_are_required(self):
        with self.fixture() as (path, calls):
            self.assertEqual(gate.main([]), 0)
            self.assertEqual([x[0] for x in calls], ['local-race-windows-core', 'local-race-windows-remaining'])
            result = json.loads((path / 'windows-package-race.json').read_text())
            self.assertTrue(gate.validate_receipt(result, SOURCE, INVENTORY))
            self.assertEqual(calls[0][1], list(gate.BASE) + [gate.CORE])
            self.assertEqual(calls[1][1], list(gate.BASE) + [INVENTORY[0], INVENTORY[2]])

    def test_first_phase_failure_still_runs_second_and_retains_both_codes(self):
        for codes in ((1, 0), (0, 66), (1, 66)):
            with self.fixture(codes=codes) as (path, calls):
                self.assertEqual(gate.main([]), 1)
                self.assertEqual(len(calls), 2)
                result = json.loads((path / 'windows-package-race.json').read_text())
                self.assertEqual([result['phases'][g]['returncode'] for g in gate.GROUPS], list(codes))
                self.assertTrue(result['sourceVerified'])
                self.assertTrue(result['inventoryVerified'])
                self.assertFalse(result['complete'])

    def test_source_and_inventory_drift_fail_after_both_attempts(self):
        for change in ({'source': [SOURCE, dict(SOURCE, tree='e' * 40)]},
                       {'inventories': [INVENTORY, INVENTORY + [gate.MODULE + '/new']]}):
            with self.fixture(**change) as (path, calls):
                self.assertEqual(gate.main([]), 1)
                self.assertEqual(len(calls), 2)
                self.assertFalse(json.loads((path / 'windows-package-race.json').read_text())['complete'])

    def test_interrupted_or_missing_second_phase_cannot_become_success(self):
        with self.fixture() as (path, calls), patch.object(gate, 'run_group', side_effect=KeyboardInterrupt):
            self.assertEqual(gate.main([]), 1)
            result = json.loads((path / 'windows-package-race.json').read_text())
            self.assertFalse(result['complete'])
            self.assertFalse(result['sourceVerified'])
            self.assertEqual(result['phases'], {})

    def test_missing_or_substituted_export_cannot_pass(self):
        for mode in ('missing', 'substituted'):
            with self.fixture() as (path, calls):
                original = Path.read_text
                def read(target, *args, **kwargs):
                    if target == path / 'windows-package-race.json':
                        if mode == 'missing':
                            raise FileNotFoundError
                        data = receipt(); data['source'] = dict(SOURCE, commit='e' * 40)
                        return json.dumps(data)
                    return original(target, *args, **kwargs)
                with patch.object(Path, 'read_text', read):
                    self.assertEqual(gate.main([]), 1)

    def test_existing_receipt_cannot_be_overwritten_or_reused(self):
        with self.fixture() as (path, calls):
            target = path / 'windows-package-race.json'
            target.write_text('existing')
            self.assertEqual(gate.main([]), 1)
            self.assertEqual(target.read_text(), 'existing')
            self.assertEqual(calls, [])

    def test_arbitrary_arguments_cannot_select_a_smaller_inventory(self):
        with patch.object(gate, 'native_identity') as native, contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(gate.main(['--run=Nothing']), 1)
            native.assert_not_called()


if __name__ == '__main__':
    unittest.main()
