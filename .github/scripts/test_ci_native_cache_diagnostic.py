import hashlib
import json
from pathlib import Path
import tempfile
import unittest

import importlib.util
import inspect
import os
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('ci-native-cache-diagnostic.py')
SPEC = importlib.util.spec_from_file_location('ci_native_cache_diagnostic', SCRIPT)
lab = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), lab.__dict__)
ROOT = SCRIPT.parents[2]


def archive(version):
    body = f'go object windows amd64 {version} GOAMD64=v1\nbuild id "fictional"\n'.encode()
    header = ('__.PKGDEF'.ljust(16) + '0'.ljust(12) + '0'.ljust(6)
              + '0'.ljust(6) + '644'.ljust(8) + str(len(body)).ljust(10) + '`\n').encode()
    return b'!<arch>\n' + header + body


class PartitionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / 'source'
        (self.source / 'aa').mkdir(parents=True)

    def put(self, name, data):
        path = self.source / 'aa' / name
        path.write_bytes(data)
        return path

    def test_only_exact_known_package_header_is_classified(self):
        name = 'a' * 64 + '-d'
        for version, expected in [('go1.27.1', 'go1.27.1_package_archive'),
                                  ('go1.27.3', 'go1.27.3_package_archive'),
                                  ('go1.27.30', 'unclassified_cache_data'),
                                  ('go1.28.1', 'unclassified_cache_data')]:
            self.assertEqual(lab.category(self.put(name, archive(version))), expected)
        for data in [b'arbitrary go object windows amd64 go1.27.3 ', b'!<arch>\n',
                     archive('go1.27.3').replace(b'__.PKGDEF', b'OTHERHDR'),
                     archive('go1.27.3').replace(b'`\n', b'xx', 1)]:
            self.assertEqual(lab.category(self.put(name, data)), 'unclassified_cache_data')

    def test_metadata_and_unknown_data_retained_source_unchanged(self):
        data = {'a' * 64 + '-d': archive('go1.27.1'),
                'b' * 64 + '-d': archive('go1.27.3'),
                'c' * 64 + '-a': b'fictional reference to missing output',
                'd' * 64 + '-d': b'unknown data is retained'}
        before = {}
        for name, raw in data.items():
            path = self.put(name, raw)
            before[name] = (hashlib.sha256(raw).hexdigest(), path.stat().st_mtime_ns)
        destination = self.root / 'candidate'
        result = lab.copy_backend_candidate(self.source, destination)
        self.assertEqual(result['omitted']['files'], 1)
        self.assertEqual(result['copied']['files'], 3)
        for name, raw in data.items():
            self.assertEqual((hashlib.sha256((self.source / 'aa' / name).read_bytes()).hexdigest(),
                              (self.source / 'aa' / name).stat().st_mtime_ns), before[name])
            if name.startswith('b'):
                self.assertFalse((destination / 'aa' / name).exists())
            else:
                self.assertEqual((destination / 'aa' / name).read_bytes(), raw)

    def test_overlap_existing_destination_and_symlink_are_rejected(self):
        for destination in [self.source, self.source / 'inside', self.root]:
            with self.assertRaises(ValueError):
                lab.copy_backend_candidate(self.source, destination)
        self.put('a' * 64 + '-d', archive('go1.27.1'))
        (self.source / 'link').symlink_to(self.root / 'outside')
        with self.assertRaises(ValueError):
            lab.copy_backend_candidate(self.source, self.root / 'candidate')
        self.assertFalse((self.root / 'candidate').exists())

    def test_reproduction_candidates_remain_independent(self):
        self.put('a' * 64 + '-d', archive('go1.27.1'))
        first, second = self.root / 'first', self.root / 'second'
        lab.copy_backend_candidate(self.source, first)
        lab.copy_backend_candidate(self.source, second)
        (first / 'aa' / ('a' * 64 + '-d')).write_bytes(b'changed owned fixture')
        self.assertEqual((second / 'aa' / ('a' * 64 + '-d')).read_bytes(), archive('go1.27.1'))

    def test_only_owned_readonly_prepared_tree_is_removed(self):
        prepared = self.root / '.sobalink-deps'
        (prepared / 'dependency').mkdir(parents=True)
        file = prepared / 'dependency' / 'source.go'
        file.write_bytes(b'fictional dependency')
        file.chmod(0o444)
        with patch.object(lab, 'ROOT', self.root):
            with self.assertRaises(ValueError):
                lab.remove_owned_prepared(self.source)
            lab.remove_owned_prepared(prepared)
        self.assertFalse(prepared.exists())
        self.assertTrue(self.source.exists())

    def test_inventory_emits_only_fixed_categories_counts_and_sizes(self):
        self.put('a' * 64 + '-d', archive('go1.27.3'))
        report = lab.inventory(self.source)
        self.assertEqual(report['files'], 1)
        self.assertEqual(set(report['categories']), {'go1.27.3_package_archive'})
        output = json.dumps(report)
        for private in [str(self.source), 'fictional', 'windows', 'amd64']:
            self.assertNotIn(private, output)



class AdmissionTests(unittest.TestCase):
    def arguments(self):
        sha = 'a' * 40
        branch = 'cache-diagnostic/' + sha[:12] + '-attempt1'
        return dict(event_name='create', event={'ref_type': 'branch', 'ref': branch,
                    'repository': {'full_name': lab.REPOSITORY, 'private': False, 'default_branch': 'main'}},
                    repository=lab.REPOSITORY, ref='refs/heads/' + branch, sha=sha,
                    checkout_sha=sha, parent=lab.BASE, changed=['A\t' + p for p in lab.FILES])

    def test_only_exact_head_three_new_files_and_base_allowed(self):
        args = self.arguments()
        self.assertEqual(lab.select(**args), 'a' * 40)
        changes = [{'event_name': 'push'}, {'event_name': 'workflow_dispatch'},
                   {'repository': 'other/project'}, {'ref': 'refs/heads/main'},
                   {'ref': args['ref'] + '\n'}, {'ref': args['ref'].replace('attempt1', 'x/y')},
                   {'ref': args['ref'].replace('a' * 12, 'b' * 12)}, {'sha': 'A' * 40},
                   {'checkout_sha': 'b' * 40}, {'parent': 'b' * 40},
                   {'changed': args['changed'] + ['M\t.github/workflows/ci.yml']},
                   {'changed': ['M\t' + p for p in lab.FILES]}, {'changed': []}]
        for change in changes:
            with self.subTest(change=change), self.assertRaises(ValueError):
                lab.select(**(args | change))
        for metadata in [{'full_name': lab.REPOSITORY, 'private': True, 'default_branch': 'main'},
                         {'full_name': 'other/project', 'private': False, 'default_branch': 'main'},
                         {'full_name': lab.REPOSITORY, 'private': False, 'default_branch': 'other'}]:
            changed = self.arguments()
            changed['event']['repository'] = metadata
            with self.assertRaises(ValueError):
                lab.select(**changed)

    def test_fixed_commands_include_companion_cold_compile_and_real_ipc_suite(self):
        with patch.dict(os.environ, {'SOBALINK_NATIVE_GO': str(ROOT / 'fictional-go' / 'go')}):
            for phase in lab.BOUNDS:
                command = lab.fixed_command(phase)
                self.assertNotIn('clean', command)
                if phase in ('backend_inventory', 'companion_inventory', 'companion_suite'):
                    self.assertIn('-race', command)
                    self.assertIn('-count=1', command)
                if phase.startswith('companion_'):
                    self.assertEqual(command[0], str(ROOT / 'fictional-go' / 'go'))
            suite = lab.fixed_command('companion_suite')
            self.assertNotIn('-list=^(Test|Example|Fuzz)', suite)
            self.assertIn('-tags=native_integration,' + lab.TAGS, suite)
            self.assertIn('-timeout=15m', suite)
            self.assertEqual(suite[-1], './...')
            with self.assertRaises(ValueError):
                lab.fixed_command('arbitrary')

    def test_keys_remain_fixed_trusted_main_exact_source_only(self):
        self.assertEqual(set(lab.PLATFORMS), {'windows-amd64', 'darwin-arm64'})
        for target, value in lab.PLATFORMS.items():
            key = value[-1]
            self.assertTrue(key.startswith('trusted-main-go-v1-'))
            self.assertTrue(key.endswith('-' + lab.BASE))
            self.assertIn('-go1.27.1-', key)
            self.assertNotIn('development', key)
            self.assertNotIn('browser', key)

    def test_workflow_is_separate_restore_only_and_sanitized(self):
        workflow = (ROOT / lab.WORKFLOW).read_text()
        trigger = workflow.split('on:\n', 1)[1].split('\npermissions:', 1)[0]
        self.assertEqual(trigger.strip(), 'create:')
        for forbidden in ('actions/cache/save', 'restore-keys:', 'contents: write', 'actions: write',
                          'id-token: write', 'secrets.', 'continue-on-error:', 'pull_request_target',
                          'ci-required', 'prerelease.yml', 'cache-diagnostic/${{'):
            self.assertNotIn(forbidden, workflow)
        self.assertEqual(workflow.count('uses: actions/cache/restore@55cc8345863c7cc4c66a329aec7e433d2d1c52a9'), 1)
        self.assertIn('fail-on-cache-miss: true', workflow)
        self.assertIn('CACHE_MATCHED_KEY: ${{ steps.cache.outputs.cache-matched-key }}', workflow)
        self.assertEqual(workflow.count('cache: false'), 2)
        self.assertIn('soba-native-cache-sanitized/result.json', workflow)
        self.assertNotIn('path: |\n            ${{ runner.temp }}', workflow)
        self.assertIn("startsWith(github.ref, 'refs/heads/cache-diagnostic/')", workflow)
        self.assertIn('persist-credentials: false', workflow)

    def test_process_ownership_matches_reviewed_capture(self):
        guard = "            if shutil.disk_usage(env['GOTMPDIR']).free < MIN_FREE_BYTES:\n                status = 'disk_headroom'\n                break\n"
        self.assertIn(guard, inspect.getsource(lab.capture))
        self.assertEqual(inspect.getsource(lab.capture).replace(guard, ''), inspect.getsource(lab.collector.capture))
        self.assertIs(lab.OwnedJob, lab.collector.OwnedJob)
        self.assertIs(lab.OwnedGroup, lab.collector.OwnedGroup)

    def test_unresolved_owners_fail_before_any_success_path_cleanup(self):
        env = {'GOTMPDIR': str(ROOT)}
        row = {'phases': {}}
        with patch.object(lab, 'capture', return_value=({'process': 'cleanup_unresolved',
                'returncode': 1, 'ownersJoined': False, 'wallSeconds': 1.0}, b'')), \
                patch.object(lab.shutil, 'disk_usage', return_value=type('Space', (), {'free': 4 * 1024 ** 3})()), \
                patch.object(lab.shutil, 'rmtree') as remove:
            with self.assertRaises(ValueError):
                lab.measure_phase('companion_suite', env, row)
            remove.assert_not_called()
        source = inspect.getsource(lab.measure)
        self.assertNotIn('finally:', source)
        self.assertNotIn('TemporaryDirectory(', source)
        self.assertLess(source.index("measure_phase('companion_vet'"), source.index('remove_owned_prepared(prepared)'))
        self.assertFalse(row['phases']['companion_suite']['ownersJoined'])

    def test_split_companion_is_fresh_and_backend_copy_conservative(self):
        source = inspect.getsource(lab.measure)
        self.assertIn("companion = backend if mode == 'baseline' else candidate / 'companion'", source)
        self.assertIn('companion.mkdir()', source)
        self.assertIn('row[\'partition\'] = copy_backend_candidate(original, backend)', source)
        self.assertIn('GOMODCACHE=str(module_copy)', source)
        self.assertIn("GOPROXY='off', GOSUMDB='off'", source)
        self.assertIn("modes = ('baseline', 'split') if sample % 2 == 0 else ('split', 'baseline')", source)
        self.assertIn("os.environ.get('CACHE_HIT') == 'true'", source)
        self.assertIn("os.environ.get('CACHE_MATCHED_KEY') == plan['key']", source)
        self.assertIn('snapshot(original) == original_stamp', source)
        self.assertIn('snapshot(modules) == module_stamp', source)
        for forbidden in ['os.link(', 'symlink_to(', 'go clean', 'cache/save']:
            self.assertNotIn(forbidden, source)


class EventTests(unittest.TestCase):
    def row(self, action, test=None, **fields):
        result = {'Action': action, 'Package': lab.COMPANION, 'Time': '2026-01-01T00:00:00Z'}
        if test is not None:
            result['Test'] = test
        return result | fields

    def raw(self, events):
        return b''.join(json.dumps(e).encode() + b'\n' for e in events)

    def test_inventory_requires_real_package_and_protected_ipc(self):
        good = [self.row('start'), self.row('output', Output=lab.IPC + '\n'),
                self.row('pass', Elapsed=1)]
        self.assertEqual(lab.companion_inventory(self.raw(good)), [lab.IPC])
        for rows in [good[:-1], good + good, [good[0], good[-1]],
                     [good[0], self.row('output', Output='private path or arbitrary output\n'), good[-1]]]:
            with self.assertRaises(ValueError):
                lab.companion_inventory(self.raw(rows))

    def test_suite_requires_real_ipc_pass_and_sanitizes_all_test_output(self):
        names = [lab.IPC, 'TestHelper']
        good = [self.row('start'), self.row('run', lab.IPC),
                self.row('output', lab.IPC, Output='fictional-private-location-and-body\n'),
                self.row('pass', lab.IPC, Elapsed=0), self.row('run', 'TestHelper'),
                self.row('skip', 'TestHelper', Elapsed=0), self.row('pass', Elapsed=1)]
        process = {'process': 'completed', 'returncode': 0, 'ownersJoined': True}
        report = lab.companion_result(self.raw(good), names, process)
        self.assertEqual(report['passedCount'], 1)
        self.assertEqual(report['skippedCount'], 1)
        self.assertNotIn('fictional-private', json.dumps(report))
        for change in ['skip', 'fail']:
            rows = list(good)
            rows[3] = self.row(change, lab.IPC, Elapsed=0)
            with self.assertRaises(ValueError):
                lab.companion_result(self.raw(rows), names, process)
        with self.assertRaises(ValueError):
            lab.companion_result(self.raw(good), names, process | {'ownersJoined': False})
        with self.assertRaises(ValueError):
            lab.companion_result(self.raw(good + good), names, process)


if __name__ == '__main__':
    unittest.main()
