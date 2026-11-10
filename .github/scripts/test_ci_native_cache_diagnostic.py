import hashlib
import json
from pathlib import Path
import tempfile
import time
import tarfile
import random
import stat
import unittest

import importlib.util
import inspect
import contextlib
import io
import os
import shutil
import subprocess
import sys
from types import SimpleNamespace
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

    def test_split_warm_retains_metadata_unknowns_and_both_toolchains(self):
        data = {'a' * 64 + '-d': archive('go1.27.1'),
                'b' * 64 + '-d': archive('go1.27.3'),
                'c' * 64 + '-a': b'fictional action metadata',
                'd' * 64 + '-d': b'unknown cache payload',
                'e' * 64 + '-d': archive('go1.28.1')}
        for name, raw in data.items():
            self.put(name, raw)
        before = content_snapshot(self.source)
        backend, companion = self.root / 'backend', self.root / 'companion'
        result = lab.copy_warm_pair(self.source, backend, companion)
        for role, root, keep, omit in [('backend', backend, 'a', 'b'),
                                       ('companion', companion, 'b', 'a')]:
            self.assertEqual(result[role]['omitted']['files'], 1)
            self.assertEqual(result[role]['copied']['files'], 4)
            self.assertTrue((root / 'aa' / (keep * 64 + '-d')).is_file())
            self.assertFalse((root / 'aa' / (omit * 64 + '-d')).exists())
            for prefix, suffix in [('c', '-a'), ('d', '-d'), ('e', '-d')]:
                relative = Path('aa') / (prefix * 64 + suffix)
                self.assertEqual((root / relative).read_bytes(), (self.source / relative).read_bytes())
                self.assertFalse(os.path.samefile(root / relative, self.source / relative))
                self.assertFalse(os.path.samefile(backend / relative, companion / relative))
        self.assertEqual(lab.inventory(backend)['bytes'] + lab.inventory(companion)['bytes'],
                         lab.split_bytes(lab.inventory(self.source)))
        (companion / 'aa' / ('d' * 64 + '-d')).write_bytes(b'private changed fixture')
        self.assertEqual(content_snapshot(self.source), before)
        self.assertEqual((backend / 'aa' / ('d' * 64 + '-d')).read_bytes(), data['d' * 64 + '-d'])

    def test_truncated_declared_member_is_unknown_and_retained(self):
        data = bytearray(archive('go1.27.3'))
        data[56:66] = b'9999999999'
        self.assertEqual(lab.category(self.put('a' * 64 + '-d', data)), 'unclassified_cache_data')

    def test_warm_pair_rejects_destinations_before_creating_either(self):
        backend, companion = self.root / 'backend', self.root / 'companion'
        companion.mkdir()
        with self.assertRaises(ValueError):
            lab.copy_warm_pair(self.source, backend, companion)
        self.assertFalse(backend.exists())
        for other in (backend, backend / 'nested', self.source / 'nested'):
            with self.assertRaises(ValueError):
                lab.copy_warm_pair(self.source, backend, other)
            self.assertFalse(backend.exists())
        with self.assertRaises(ValueError):
            lab.copy_candidate(self.source, backend, 'unknown')
        self.assertFalse(backend.exists())

    def test_partition_bounds_and_empty_cache(self):
        with patch.object(lab, 'MAX_ENTRIES', 0), self.assertRaises(ValueError):
            lab.copy_warm_pair(self.source, self.root / 'backend', self.root / 'companion')
        self.assertFalse((self.root / 'backend').exists())
        result = lab.copy_warm_pair(self.source, self.root / 'backend', self.root / 'companion')
        for row in result.values():
            self.assertEqual(row['copied'], {'files': 0, 'bytes': 0})
            self.assertEqual(row['omitted'], {'files': 0, 'bytes': 0})

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



class ActionReferenceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def put(self, identifier, suffix, raw):
        path = self.root / identifier[:2] / (identifier + suffix)
        path.parent.mkdir(exist_ok=True)
        path.write_bytes(raw)
        return path

    def record(self, identifier, output, size=0, timestamp=1):
        return f'v1 {identifier} {output} {size:20d} {timestamp:20d}\n'.encode('ascii')

    def test_fixed_reference_buckets_count_only_metadata_bytes_without_mutation(self):
        cases = [('a', '1', archive('go1.27.1')), ('b', '2', archive('go1.27.3')),
                 ('c', '3', b'opaque-output'), ('d', '4', None)]
        for action, output, raw in cases:
            if raw is not None:
                self.put(output * 64, '-d', raw)
            self.put(action * 64, '-a', self.record(action * 64, output * 64))
        # Go's executable-cache -d directory must be present, never missing.
        directory = self.root / '55' / ('5' * 64 + '-d')
        directory.mkdir(parents=True)
        (directory / 'fictional-program').write_bytes(b'fictional executable')
        self.put('e' * 64, '-a', self.record('e' * 64, '5' * 64))
        self.put('f' * 64, '-a', b'private-sentinel-unparseable-record')
        before = content_snapshot(self.root)
        report = lab.action_reference_inventory(self.root)
        self.assertEqual(content_snapshot(self.root), before)
        self.assertEqual(report['files'], 6)
        self.assertEqual(report['bytes'], 5 * 175 + len(b'private-sentinel-unparseable-record'))
        self.assertEqual({name: value['files'] for name, value in report['categories'].items()},
                         {'go1.27.1_package_archive': 1, 'go1.27.3_package_archive': 1,
                          'other_present': 2, 'missing': 1, 'unparseable': 1})
        for name in lab.ACTION_REFERENCE_CATEGORIES[:-1]:
            self.assertEqual(report['categories'][name]['bytes'], report['categories'][name]['files'] * 175)
        encoded = json.dumps(report)
        for private in (str(self.root), 'a' * 64, '1' * 64, 'private-sentinel', 'fictional-program'):
            self.assertNotIn(private, encoded)

    def test_only_canonical_bounded_v1_entries_are_parsed(self):
        action, output = 'a' * 64, 'b' * 64
        valid = self.record(action, output, 2 ** 63 - 1, 2 ** 63 - 1)
        path = self.put(action, '-a', valid)
        relative = path.relative_to(self.root)
        self.assertEqual(len(valid), 175)
        self.assertEqual(lab.action_output(path, relative), output)
        malformed = [valid[:-1], valid + b'x', valid + b'x' * 4096,
                     valid.replace(b'v1 ', b'v2 ', 1), valid.replace(b'\n', b'\r'),
                     valid.replace(action.encode(), b'c' * 64, 1),
                     valid.replace(output.encode(), b'../' + b'b' * 61, 1),
                     valid[:67] + b'\t' + valid[68:],
                     self.record(action, output, -1, 0), self.record(action, output, 0, -1),
                     self.record(action, output, 2 ** 63, 0), self.record(action, output, 0, 2 ** 63),
                     self.record(action, output).replace(b'                   0', b'00000000000000000000', 1)]
        for raw in malformed:
            with self.subTest(case=malformed.index(raw)):
                path.write_bytes(raw)
                self.assertIsNone(lab.action_output(path, relative))
        path.write_bytes(valid)
        self.assertIsNone(lab.action_output(path, Path('wrong') / path.name))
        reader = unittest.mock.MagicMock()
        reader.__enter__.return_value.read.return_value = valid + b'x'
        with patch.object(Path, 'open', return_value=reader):
            self.assertIsNone(lab.action_output(path, relative))
        reader.__enter__.return_value.read.assert_called_once_with(176)

    def test_reference_inventory_rejects_links_and_respects_entry_bound(self):
        self.put('a' * 64, '-a', self.record('a' * 64, 'b' * 64))
        with patch.object(lab, 'MAX_ENTRIES', 1), self.assertRaises(ValueError):
            lab.action_reference_inventory(self.root)
        (self.root / 'linked').symlink_to(self.root / 'outside')
        with self.assertRaises(ValueError):
            lab.action_reference_inventory(self.root)


class AdmissionTests(unittest.TestCase):
    def arguments(self):
        sha = 'a' * 40
        branch = 'cache-diagnostic/' + sha[:12] + '-attempt1'
        return dict(event_name='create', event={'ref_type': 'branch', 'ref': branch,
                    'repository': {'full_name': lab.REPOSITORY, 'private': False, 'default_branch': 'main'}},
                    repository=lab.REPOSITORY, ref='refs/heads/' + branch, sha=sha,
                    checkout_sha=sha, ancestor=lab.BASE, changed=['A\t' + p for p in lab.FILES])

    def test_only_exact_head_three_new_files_and_base_allowed(self):
        args = self.arguments()
        self.assertEqual(lab.select(**args), 'a' * 40)
        changes = [{'event_name': 'push'}, {'event_name': 'workflow_dispatch'},
                   {'repository': 'other/project'}, {'ref': 'refs/heads/main'},
                   {'ref': args['ref'] + '\n'}, {'ref': args['ref'].replace('attempt1', 'x/y')},
                   {'ref': args['ref'].replace('a' * 12, 'b' * 12)}, {'sha': 'A' * 40},
                   {'checkout_sha': 'b' * 40}, {'ancestor': 'b' * 40},
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

    def test_fixed_commands_include_companion_compile_and_real_ipc_suite(self):
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
        self.assertIn('timeout-minutes: 45', workflow)
        self.assertIn('SOBALINK_CACHE_FIXTURE_GO="$backend_go"', workflow)
        self.assertIn('SOBALINK_CACHE_FIXTURE_GO="$SOBALINK_NATIVE_GO"', workflow)
        self.assertLess(workflow.index('Admit exact source'), workflow.index('Verify warm hits'))
        self.assertLess(workflow.index('Verify warm hits'), workflow.index('Restore exact trusted main cache'))

    def test_process_ownership_matches_reviewed_capture(self):
        guard = "            if shutil.disk_usage(env['GOTMPDIR']).free < MIN_FREE_BYTES:\n                status = 'disk_headroom'\n                break\n"
        self.assertIn(guard, inspect.getsource(lab.capture))
        archive_guard = "            if phase in ARCHIVE_BOUNDS and archive_budget_exceeded(env):\n                status = 'artifact_limit'\n                break\n"
        deadline = "deadline = start + (ARCHIVE_BOUNDS[phase] if phase in ARCHIVE_BOUNDS else BOUNDS[phase])\n        if phase in ARCHIVE_BOUNDS:\n            deadline = min(deadline, float(env['SOBALINK_ARCHIVE_DEADLINE']))"
        self.assertIn(archive_guard, inspect.getsource(lab.capture))
        self.assertIn(deadline, inspect.getsource(lab.capture))
        normalized = inspect.getsource(lab.capture).replace(guard, '').replace(archive_guard, '')
        self.assertEqual(normalized.replace(deadline, 'deadline = start + BOUNDS[phase]'), inspect.getsource(lab.collector.capture))
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
        self.assertLess(source.index("run_workload("), source.index('remove_owned_prepared(prepared)'))
        self.assertFalse(row['phases']['companion_suite']['ownersJoined'])

    def test_runtime_temp_environment_preserved_and_warmup_precedes_pairs(self):
        with patch.dict(os.environ, {'TMPDIR': '/fictional-short', 'TMP': '/fictional-tmp', 'TEMP': '/fictional-temp'}):
            env = lab.workload_environment('backend', 'modules', 'compiler-scratch')
        self.assertEqual([env[k] for k in ('TMPDIR', 'TMP', 'TEMP')],
                         ['/fictional-short', '/fictional-tmp', '/fictional-temp'])
        self.assertEqual(env['GOTMPDIR'], 'compiler-scratch')
        source = inspect.getsource(lab.measure)
        self.assertLess(source.index("report['comparisonSeed'] = inventory(seed)"), source.index('for sample in range(ROUNDS)'))
        self.assertIn('run_workload(workload_environment(seed, module_copy, warmup_scratch), seed', source)
        self.assertIn('full_copy(seed, backend)', source)
        self.assertIn('copy_warm_pair(seed, backend, companion)', source)

    def test_split_companion_is_independently_warm_and_partition_conservative(self):
        source = inspect.getsource(lab.measure)
        self.assertIn("companion = backend if mode == 'baseline' else candidate / 'companion'", source)
        self.assertNotIn('companion.mkdir()', source)
        self.assertIn('row[\'partition\'] = copy_warm_pair(seed, backend, companion)', source)
        self.assertIn('GOMODCACHE=str(modules)', inspect.getsource(lab.workload_environment))
        self.assertIn("GOPROXY='off', GOSUMDB='off'", inspect.getsource(lab.workload_environment))
        self.assertIn("modes = ('baseline', 'split-warm') if sample % 2 == 0 else ('split-warm', 'baseline')", source)
        self.assertEqual(lab.ROUNDS, 2)
        self.assertIn("os.environ.get('CACHE_HIT') == 'true'", source)
        self.assertIn("os.environ.get('CACHE_MATCHED_KEY') == plan['key']", source)
        self.assertIn('snapshot(original) == original_stamp', source)
        self.assertIn('snapshot(modules) == module_stamp', source)
        self.assertIn('snapshot(seed) == seed_stamp', source)
        self.assertIn("row['before']", source)
        self.assertIn("row['after']", source)
        for forbidden in ['os.link(', 'symlink_to(', 'go clean', 'cache/save']:
            self.assertNotIn(forbidden, source)
        self.assertEqual(source.count('action_reference_inventory('), 2)
        self.assertLess(source.index("report['actionReferences']['comparisonSeed']"), source.index('for sample in range(ROUNDS)'))
        self.assertNotIn('action_reference_inventory(', inspect.getsource(lab.copy_candidate))


def content_snapshot(root):
    result = {}
    for path, relative, size in lab.entries(root):
        with path.open('rb') as stream:
            result[str(relative)] = (hashlib.file_digest(stream, 'sha256').hexdigest(),
                                    size, path.stat().st_mtime_ns)
    return result


# Opt-in fixture checks use the same bounded, gated, descendant-owning capture
# as the native diagnostic. No arbitrary commands and no dependency downloads.
FIXTURE_COMMANDS = {
    'fixture_identity': ['env', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH'],
    'fixture_race': ['test', '-race', '-count=1', '-timeout=1m', './...'],
    'fixture_vet': ['vet', './...'],
    'fixture_export': ['list', '-race', '-export', '-f', '{{.Export}}', '.'],
    'fixture_stale': ['list', '-race', '-f', '{{.Stale}}', '.'],
    'fixture_format': ['fmt', './...'],
}


FIXTURE_SOURCES = {
    'go.mod': 'module example.invalid/cachefixture\n\ngo 1.27.1\n',
    'value.go': 'package cachefixture\n\nfunc Value() int { return 42 }\n',
    'value_test.go': 'package cachefixture\n\nimport "testing"\n\nfunc TestValue(t *testing.T) {\n\tif Value() != 42 {\n\t\tt.Fatal("wrong fixture value")\n\t}\n}\n',
}


def write_go_fixture(module):
    # gofmt emits LF on every platform. Do not let Windows text-mode writes
    # introduce CRLF and turn the no-formatting-change assertion into a miss.
    for name, source in FIXTURE_SOURCES.items():
        (module / name).write_text(source, encoding='utf-8', newline='\n')


class FixtureSourceTests(unittest.TestCase):
    def test_fixture_failures_never_print_paths_hashes_or_raw_operands(self):
        sentinel = 'synthetic-private-cache-path-hash-and-object'
        errors = [ValueError(sentinel), OSError(sentinel),
                  AssertionError({sentinel: b'private-archive-bytes'})]
        for error in errors:
            def fail():
                raise error

            class FailedFixture(unittest.TestCase):
                def runTest(self):
                    private_fixture_check(fail)

            stdout, stderr = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                result = unittest.TextTestRunner(stream=stderr).run(FailedFixture())
            self.assertFalse(result.wasSuccessful())
            self.assertEqual(len(result.failures), 1)
            output = stdout.getvalue() + stderr.getvalue()
            self.assertIn('Offline Go cache fixture failed; details withheld.', output)
            self.assertNotIn(sentinel, output)
            self.assertNotIn('private-archive-bytes', output)
        self.assertNotIn('self.assert', inspect.getsource(RealGoFixtureTests.check_fixture))

    def test_explicit_utf8_lf_survives_windows_default_newline_translation(self):
        original_open = Path.open
        writes = []

        def windows_default_open(path, *args, **kwargs):
            if kwargs.get('mode', args[0] if args else 'r') == 'w':
                writes.append((path.name, kwargs.get('encoding'), kwargs.get('newline')))
                if kwargs.get('newline') is None:
                    kwargs['newline'] = '\r\n'
            return original_open(path, *args, **kwargs)

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with patch.object(Path, 'open', new=windows_default_open):
                # Prove the emulation detects the original platform-sensitive
                # behavior before checking the actual fixture writer.
                control = root / 'default.txt'
                control.write_text('control\n')
                write_go_fixture(root)
            self.assertEqual(control.read_bytes(), b'control\r\n')
            self.assertEqual(set(FIXTURE_SOURCES), {'go.mod', 'value.go', 'value_test.go'})
            self.assertEqual(writes[1:], [(name, 'utf-8', '\n') for name in FIXTURE_SOURCES])
            for name, source in FIXTURE_SOURCES.items():
                self.assertEqual((root / name).read_bytes(), source.encode('utf-8'))
                self.assertNotIn(b'\r', (root / name).read_bytes())


def owned_fixture(phase):
    if phase not in FIXTURE_COMMANDS or sys.stdin.buffer.read(1) != b'G':
        return 126
    tool = Path(os.environ.get('SOBALINK_CACHE_FIXTURE_GO', ''))
    if not tool.is_absolute() or not tool.is_file():
        return 126
    return subprocess.run([str(tool), *FIXTURE_COMMANDS[phase]],
                          stdin=subprocess.DEVNULL, shell=False, check=False).returncode


def private_fixture_check(operation):
    try:
        operation()
    except Exception:
        # Exceptions (including permission/cleanup errors) and unittest operand
        # diffs may contain private cache paths, IDs, hashes or object bytes.
        raise AssertionError('Offline Go cache fixture failed; details withheld.') from None


class RealGoFixtureTests(unittest.TestCase):
    @unittest.skipUnless(os.environ.get('SOBALINK_CACHE_FIXTURE_GO'), 'explicit offline Go fixture opt-in required')
    def test_real_go_warm_hits_missing_output_regeneration_and_source_immutability(self):
        private_fixture_check(self.check_fixture)

    def check_fixture(self):
        # This proves cache mechanics for the supplied compiler, not native
        # acceptance or a production/native performance improvement.
        root = Path(tempfile.mkdtemp(prefix='soba-warm-fixture-'))
        module, source = root / 'module', root / 'original-cache'
        module.mkdir()
        source.mkdir()
        scratch = root / 'scratch'
        scratch.mkdir()
        write_go_fixture(module)
        env = lab.workload_environment(source, root / 'modules', scratch)
        env.update(GOFLAGS='', CGO_ENABLED='1')
        records, unresolved = [], False

        def run(cache, phase):
            nonlocal unresolved
            with patch.object(lab, '__file__', str(Path(__file__).resolve())), \
                    patch.object(lab, 'ROOT', module), \
                    patch.object(lab, 'BOUNDS', {key: 180 for key in FIXTURE_COMMANDS}):
                process, raw = lab.capture(phase, dict(env, GOCACHE=str(cache)))
            records.append({'phase': phase, 'process': process['process'],
                            'returncode': process['returncode'], 'ownersJoined': process['ownersJoined']})
            unresolved |= not process['ownersJoined']
            lab.require(process['process'] == 'completed')
            lab.require(process['returncode'] == 0)
            lab.require(process['ownersJoined'])
            return raw.decode().strip()

        passed = False
        try:
            identity = run(source, 'fixture_identity').splitlines()
            lab.require(identity[0] in ('go1.27.1', 'go1.27.3'))
            role = 'backend' if identity[0] == 'go1.27.1' else 'companion'
            opposite = 'go1.27.3' if role == 'backend' else 'go1.27.1'
            lab.require(run(source, 'fixture_format') == '')
            run(source, 'fixture_race')
            run(source, 'fixture_vet')
            original_export = Path(run(source, 'fixture_export'))
            lab.require(source in original_export.parents)
            lab.require(lab.category(original_export) == identity[0] + '_package_archive')
            fake = source / 'ee' / ('e' * 64 + '-d')
            fake.parent.mkdir(exist_ok=True)
            fake.write_bytes(archive(opposite))
            before, sources_before = content_snapshot(source), content_snapshot(module)
            references = lab.action_reference_inventory(source)
            lab.require(references['files'] > 0 and references['categories']['unparseable']['files'] == 0)
            lab.require(references['categories'][identity[0] + '_package_archive']['files'] > 0)
            baseline = root / 'baseline'
            lab.full_copy(source, baseline)
            backend, companion = root / 'backend', root / 'companion'
            partition = lab.copy_warm_pair(source, backend, companion)
            candidate, peer = (backend, companion) if role == 'backend' else (companion, backend)
            peer_before = content_snapshot(peer)
            lab.require(not (candidate / fake.relative_to(source)).exists())
            lab.require(partition[role]['omitted']['files'] == 1)
            relative_export = original_export.relative_to(source)
            for cache in (baseline, candidate):
                lab.require(run(cache, 'fixture_stale') == 'false')
                lab.require((cache / relative_export).read_bytes() == original_export.read_bytes())
                run(cache, 'fixture_race')
                run(cache, 'fixture_vet')
            target = candidate / relative_export
            target.unlink()  # Only one known owned output; metadata is retained.
            lab.require(run(candidate, 'fixture_stale') == 'true')
            lab.require(Path(run(candidate, 'fixture_export')) == target)
            lab.require(target.is_file())
            lab.require(run(candidate, 'fixture_stale') == 'false')
            run(candidate, 'fixture_race')
            run(candidate, 'fixture_vet')
            lab.require(content_snapshot(source) == before)
            lab.require(content_snapshot(module) == sources_before)
            lab.require(content_snapshot(peer) == peer_before)
            passed = True
            # Only fixed labels, toolchain identity and counts, never paths,
            # raw compiler output, cache IDs or content hashes.
            print(json.dumps({'schema': 1, 'scope': 'offline-synthetic-cache-mechanics-only',
                              'toolchain': identity, 'role': role, 'sourceContentUnchanged': True,
                              'peerContentUnchanged': True, 'warmHitVerified': True,
                              'missingOutputRegenerated': True, 'partition': partition,
                              'actionReferences': references, 'phases': records}))
        finally:
            # Retain failed fixtures; never delete paths with unresolved owners.
            if passed and not unresolved:
                shutil.rmtree(root)


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
            summary = lab.companion_summary(self.raw(rows), names, process)
            self.assertFalse(summary['accepted'])
            self.assertNotIn('fictional-private', json.dumps(summary))
            with self.assertRaises(ValueError):
                lab.companion_result(self.raw(rows), names, process)
        with self.assertRaises(ValueError):
            lab.companion_result(self.raw(good), names, process | {'ownersJoined': False})
        with self.assertRaises(ValueError):
            lab.companion_result(self.raw(good + good), names, process)


class ArchiveTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.modules, self.build = self.root / 'modules-source', self.root / 'build-source'
        self.modules.mkdir()
        (self.modules / 'empty').mkdir()
        (self.modules / 'ordinary.go').write_bytes(b'package fictional\n')
        (self.build / 'aa').mkdir(parents=True)
        for letter, raw in [('a', archive('go1.27.1')), ('b', archive('go1.27.3')),
                            ('c', b'unknown payload')]:
            (self.build / 'aa' / (letter * 64 + '-d')).write_bytes(raw)
        (self.build / 'aa' / ('e' * 64 + '-a')).write_bytes(b'opaque action record')
        (self.build / 'aa' / ('f' * 64 + '-d')).mkdir()
        binary = self.build / 'aa' / ('f' * 64 + '-d') / 'program'
        binary.write_bytes(b'fictional executable')
        binary.chmod(0o755)
        self.roots = {'modules': self.modules, 'build': self.build}
        self.deadline = time.monotonic() + 60

    def snapshot(self):
        return lab.archive_snapshot(self.roots, self.deadline)

    def test_owned_fixture_resolves_noncanonical_temporary_parent(self):
        # Model a temporary parent alias without requiring native symlink
        # privileges or a particular host's temporary-directory spelling.
        intermediate = self.root / 'intermediate'
        intermediate.mkdir()
        owned = tempfile.TemporaryDirectory(dir=self.root)
        self.addCleanup(owned.cleanup)
        aliased = intermediate / '..' / Path(owned.name).name
        self.assertTrue(aliased.is_dir())
        self.assertFalse(aliased.is_symlink())
        self.assertNotEqual(aliased, aliased.resolve())
        temporary = type('TemporaryAlias', (), {'name': str(aliased), 'cleanup': staticmethod(owned.cleanup)})()
        fixture = ArchiveTests()
        try:
            with patch.object(tempfile, 'TemporaryDirectory', return_value=temporary):
                fixture.setUp()
            self.assertEqual(fixture.root, Path(owned.name).resolve())
            self.assertGreater(lab.archive_totals(fixture.snapshot())['files'], 0)
            # Canonicalizing this owned fixture does not relax source admission.
            roots = {'modules': aliased / 'modules-source', 'build': aliased / 'build-source'}
            with self.assertRaises(ValueError):
                lab.archive_snapshot(roots, fixture.deadline)
        finally:
            fixture.doCleanups()

    def test_projection_preserves_unknown_action_metadata_empty_dirs_and_programs(self):
        snapshot = self.snapshot()
        projected = {name: lab.archive_projection(snapshot, name) for name in lab.ARCHIVE_OBJECTS}
        self.assertEqual(lab.archive_totals(projected['combined']), lab.archive_totals(snapshot))
        for obj in ('backend', 'companion', 'unit-bundle'):
            prefix = 'companion' if obj == 'companion' else 'backend'
            for letter, suffix in [('c', '-d'), ('e', '-a')]:
                self.assertIn(prefix + '/aa/' + letter * 64 + suffix, projected[obj])
            self.assertIn(prefix + '/aa/' + 'f' * 64 + '-d/program', projected[obj])
        self.assertIn('modules/empty', projected['unit-bundle'])
        self.assertNotIn('backend/aa/' + 'b' * 64 + '-d', projected['backend'])
        self.assertNotIn('companion/aa/' + 'a' * 64 + '-d', projected['companion'])
        before = self.snapshot()
        stage = self.root / 'stage'
        lab.archive_stage(projected['unit-bundle'], stage, self.deadline)
        lab.archive_verify_destination(stage, projected['unit-bundle'], self.deadline)
        self.assertEqual(self.snapshot(), before)
        self.assertFalse(os.path.samefile(stage / 'modules/ordinary.go', self.modules / 'ordinary.go'))
        lab.archive_remove(stage)
        self.assertEqual(self.snapshot(), before)

    def test_names_reject_escape_alias_and_collision(self):
        for name in ('/absolute', '../escape', 'build/../escape', 'build/./x', 'build//x',
                     'C:/drive', '//server/share', 'build\\x', 'build/file:stream',
                     'build/end.', 'build/end ', 'build/CON', 'build/nul.txt', 'build/LPT¹',
                     'build/CONIN$', 'build/conout$', 'build/CLOCK$',
                     'build/a\0b', 'build/a\nb', '-option', 'build/e\u0301', 'x' * 1025):
            with self.subTest(name=name), self.assertRaises(ValueError):
                lab.archive_name(name)
        self.assertEqual(lab.archive_name('modules/example.invalid/mod@v1.0.0/!upper.go'),
                         'modules/example.invalid/mod@v1.0.0/!upper.go')
        # Model both entries without relying on a case-sensitive filesystem.
        # Native Windows/macOS may alias these names and overwrite one file.
        scandir = os.scandir

        def duplicate(directory):
            if Path(directory) != self.modules:
                return scandir(directory)
            with scandir(directory) as entries:
                children = list(entries)
            alias = type('Entry', (), {'name': 'ORDINARY.GO', 'path': str(self.modules / 'ordinary.go')})()
            return contextlib.nullcontext(children + [alias])

        with patch.object(lab.os, 'scandir', side_effect=duplicate), self.assertRaises(ValueError):
            self.snapshot()

    def test_snapshot_rejects_links_reparse_modes_and_bounds(self):
        link = self.modules / 'link'
        link.symlink_to(self.modules / 'ordinary.go')
        with self.assertRaises(ValueError):
            self.snapshot()
        link.unlink()
        os.link(self.modules / 'ordinary.go', link)
        with self.assertRaises(ValueError):
            self.snapshot()
        link.unlink()
        fake = type('Info', (), {'st_file_attributes': 0x400})()
        with self.assertRaises(ValueError):
            lab.archive_identity(fake)
        for key, value in [('MAX_ENTRIES', 1), ('ARCHIVE_MAX_BYTES', 1),
                           ('ARCHIVE_MAX_FILE_BYTES', 1), ('ARCHIVE_NAME_TOTAL_BYTES', 1)]:
            with patch.object(lab, key, value), self.assertRaises(ValueError):
                self.snapshot()
        if sys.platform != 'win32':
            (self.modules / 'ordinary.go').chmod(0o4644)
            with self.assertRaises(ValueError):
                self.snapshot()

    def test_content_hash_catches_same_size_and_mtime_mutation(self):
        before = self.snapshot()
        target = self.modules / 'ordinary.go'
        original = target.stat()
        target.write_bytes(b'X' * original.st_size)
        os.utime(target, ns=(original.st_atime_ns, original.st_mtime_ns))
        after = self.snapshot()
        self.assertNotEqual(before['modules/ordinary.go']['sha256'], after['modules/ordinary.go']['sha256'])
        with self.assertRaises(ValueError):
            lab.archive_stage(before, self.root / 'stage', self.deadline)

    def test_fresh_destination_exact_membership_and_modes_required(self):
        rows = self.snapshot()
        stage = self.root / 'stage'
        lab.archive_stage(rows, stage, self.deadline)
        (stage / 'extra').write_bytes(b'extra')
        with self.assertRaises(ValueError):
            lab.archive_verify_destination(stage, rows, self.deadline)
        (stage / 'extra').unlink()
        target = stage / 'modules/ordinary.go'
        target.write_bytes(b'wrong content')
        with self.assertRaises(ValueError):
            lab.archive_verify_destination(stage, rows, self.deadline)
        with self.assertRaises(ValueError):
            lab.archive_stage(rows, stage, self.deadline)

    def test_policy_geometry_counts_unique_writer_objects_and_unmeasured_controls(self):
        projected = {name: lab.archive_projection(self.snapshot(), name) for name in lab.ARCHIVE_OBJECTS}
        samples = {name: {'compressedBytes': 10 + i, 'phases': {
            'archive_pack': {'wallSeconds': 3 + i}, 'archive_extract': {'wallSeconds': 2 + i}}}
            for i, name in enumerate(lab.ARCHIVE_PILOT)}
        result = lab.archive_accounting(projected, samples)
        self.assertEqual(result['A']['writer']['objectCount'], 1)
        self.assertEqual(result['H']['writer']['compressedBytes'], 21)
        self.assertEqual(result['Q']['writer']['compressedBytes'], 23)
        self.assertEqual(result['F']['writer']['objectCount'], 3)
        self.assertEqual(result['F']['integration']['objects'], ['modules', 'backend', 'companion'])
        self.assertEqual(result['Q']['integration']['componentSeconds'], 7)
        for name in ('R', 'F'):
            self.assertIsNone(result[name]['writer']['compressedBytes'])
            self.assertIsNone(result[name]['integration']['componentSeconds'])
            self.assertEqual(result[name]['unit']['measurement'], 'unmeasured')
        output = json.dumps(result)
        for private in ('ordinary.go', str(self.root), 'sha256', 'opaque'):
            self.assertNotIn(private, output)
        self.assertEqual(lab.ARCHIVE_ORDERS[1], tuple(reversed(lab.ARCHIVE_ORDERS[0])))
        self.assertEqual(lab.archive_report()['plan']['objectSamples'], 6)
        self.assertLessEqual(lab.ARCHIVE_SECONDS, 32 * 60)

    def test_native_command_profiles_are_streaming_relative_and_fixed(self):
        for target in ('windows-amd64', 'darwin-arm64', 'linux-fixture'):
            spec = {'tools': {'tar': '/tool/tar', 'pack': '/tool/zstd',
                             'unpack': '/tool/unzstd', 'platform': target}, 'private': '/private'}
            pack = lab.archive_command('archive_pack', spec)
            extract = lab.archive_command('archive_extract', spec)
            self.assertIn('--posix', pack)
            self.assertIn('--null', pack)
            self.assertIn('--verbatim-files-from', pack)
            self.assertIn('--no-recursion', pack)
            self.assertIn('--no-same-owner', extract)
            self.assertNotIn('-P', pack + extract)
            self.assertNotIn('--absolute-names', pack + extract)
            if target == 'windows-amd64':
                self.assertIn('--force-local', pack)
                self.assertEqual(pack[-1], 'zstd -T0')
                self.assertEqual(extract[-1], 'zstd -d')
            if target == 'darwin-arm64':
                self.assertIn('--delay-directory-restore', pack)
                self.assertIn('--delay-directory-restore', extract)
                self.assertEqual(pack[-1], 'zstdmt')
                self.assertEqual(extract[-1], 'unzstd')
        self.assertEqual(lab.archive_version(b'tar (GNU tar) 1.35\nprivate paths', 'tar'), '1.35')
        self.assertEqual(lab.archive_version(b'*** Zstandard CLI (64-bit) v1.5.7, by author ***', 'pack'), '1.5.7')
        with self.assertRaises(ValueError):
            lab.archive_version(b'bsdtar 3.8.0', 'tar')

    def test_workflow_uses_archive_only_and_retains_compiler_fixtures(self):
        workflow = (ROOT / lab.WORKFLOW).read_text()
        self.assertIn('ci-native-cache-diagnostic.py archive', workflow)
        self.assertNotIn('ci-native-cache-diagnostic.py measure', workflow)
        self.assertEqual(workflow.count('RealGoFixtureTests -v'), 2)
        source = inspect.getsource(lab.measure_archives)
        self.assertNotIn('run_workload(', source)
        self.assertNotIn('adapted-seed', source)
        self.assertIn("archive_snapshot(roots, deadline) == before", source)
        self.assertNotIn('finally:', source)
        self.assertLess(source.index("archive_capture('archive_validate'"), source.index("archive_capture('archive_extract'"))
        self.assertLess(source.index("archive_capture('archive_extract'"), source.index('archive_remove('))
        for key in ('TMP', 'TEMP', 'TMPDIR'):
            self.assertNotRegex(source, r'\b' + key + '=')

    def test_unresolved_capture_retains_private_work_and_has_fixed_report(self):
        private = self.root / 'private'
        private.mkdir()
        env = dict(os.environ, GOTMPDIR=str(private), SOBALINK_ARCHIVE_SPEC=str(private / 'spec.json'))
        row = {}
        with patch.object(lab, 'capture', return_value=({'process': 'cleanup_unresolved',
                'returncode': 1, 'ownersJoined': False, 'wallSeconds': 1.0}, b'private payload')), \
                patch.object(lab, 'archive_remove') as remove:
            with self.assertRaises(ValueError):
                lab.archive_capture('archive_pack', env, {}, self.deadline, row)
            remove.assert_not_called()
        self.assertNotIn('private payload', json.dumps(row))
        self.assertFalse(row['archive_pack']['ownersJoined'])
        self.assertTrue((private / 'spec.json').exists())

    def test_archive_environment_clears_tool_knobs_preserves_runtime_tmp_and_checks_alias(self):
        tools = {'tar': '/tools/tar', 'pack': '/tools/zstd', 'unpack': '/tools/zstd', 'platform': 'windows-amd64'}
        with patch.dict(os.environ, {'TMP': '/short-tmp', 'TEMP': '/short-temp', 'TMPDIR': '/short-tmpdir',
                'TAR_OPTIONS': '--unsafe', 'ZSTD_NBTHREADS': '1', 'ZSTD_CLEVEL': '9', 'ZSTD_OTHER': 'x'}), \
                patch.object(lab.shutil, 'which', return_value='/tools/zstd') as which:
            env = lab.archive_environment(tools, self.root)
        self.assertEqual([env[k] for k in ('TMP', 'TEMP', 'TMPDIR')], ['/short-tmp', '/short-temp', '/short-tmpdir'])
        for key in ('TAR_OPTIONS', 'ZSTD_NBTHREADS', 'ZSTD_CLEVEL', 'ZSTD_OTHER'):
            self.assertNotIn(key, env)
        self.assertEqual([call.args[0] for call in which.call_args_list], ['zstd', 'zstd'])
        with patch.object(lab.shutil, 'which', return_value='/wrong/zstd'), self.assertRaises(ValueError):
            lab.archive_environment(tools, self.root)

    def test_windows_readonly_semantics_do_not_claim_posix_mode_identity(self):
        row = {'kind': 'file', 'mode': 0o666}
        self.assertTrue(lab.archive_mode_matches(0o755, row, windows=True))
        self.assertFalse(lab.archive_mode_matches(0o555, row, windows=True))
        self.assertFalse(lab.archive_mode_matches(0o755, row, windows=False))
        self.assertTrue(lab.archive_mode_matches(0o555, row | {'mode': 0o444}, windows=True))
        with self.assertRaises(ValueError):
            lab.archive_mode_matches(0o4755, row, windows=True)

    def test_deadline_size_caps_and_readonly_owned_cleanup(self):
        with self.assertRaises(ValueError):
            lab.archive_tick(time.monotonic() - 1)
        stage = self.root / 'stage'
        lab.archive_stage(self.snapshot(), stage, self.deadline)
        (stage / 'modules/ordinary.go').chmod(0o444)
        (stage / 'modules').chmod(0o555)
        lab.archive_remove(stage)
        self.assertFalse(stage.exists())
        packed = self.root / 'cache.tzst'
        packed.write_bytes(b'0123456789')
        self.assertTrue(lab.archive_budget_exceeded({'GOTMPDIR': str(self.root), 'SOBALINK_ARCHIVE_FILE_BOUND': '9'}))
        self.assertFalse(lab.archive_budget_exceeded({'GOTMPDIR': str(self.root), 'SOBALINK_ARCHIVE_FILE_BOUND': '10'}))


class ArchiveHandleIdentityTests(unittest.TestCase):
    FIELDS = ('st_dev', 'st_ino', 'st_mode', 'st_size', 'st_mtime_ns',
              'st_ctime_ns', 'st_nlink', 'st_file_attributes', 'st_birthtime_ns')

    def info(self, **changes):
        return SimpleNamespace(**(dict(st_dev=17, st_ino=23, st_mode=stat.S_IFREG | 0o666,
            st_size=7, st_mtime_ns=31, st_ctime_ns=37, st_nlink=1,
            st_file_attributes=32, st_birthtime_ns=37) | changes))

    def test_windows_cross_api_semantics_preserve_every_shared_identity_field(self):
        pathname = self.info(st_mode=stat.S_IFREG | 0o777)
        handle = self.info(st_ctime_ns=41)
        self.assertEqual(lab.archive_handle_identity(pathname, handle, windows=True), lab.archive_identity(handle))
        with self.assertRaises(ValueError):
            lab.archive_handle_identity(pathname, handle, windows=False)
        for field in ('st_dev', 'st_ino', 'st_size', 'st_mtime_ns', 'st_nlink', 'st_file_attributes', 'st_birthtime_ns'):
            changed = self.info(**(vars(handle) | {field: getattr(handle, field) + 1}))
            with self.subTest(field=field), self.assertRaises(ValueError):
                lab.archive_handle_identity(pathname, changed, windows=True)
        for mode in (stat.S_IFDIR | 0o666, stat.S_IFREG | 0o444, stat.S_IFREG | 0o4666):
            with self.assertRaises(ValueError):
                lab.archive_handle_identity(pathname, self.info(st_mode=mode), windows=True)
        for info in (self.info(st_file_attributes=0x400), self.info(st_ino=0)):
            with self.assertRaises(ValueError):
                lab.archive_handle_identity(info, info, windows=True)

    def test_birthtime_capability_is_explicit_and_older_runtime_ctime_remains_checked(self):
        pathname, handle = self.info(), self.info()
        del handle.st_birthtime_ns
        with self.assertRaises(ValueError):
            lab.archive_handle_identity(pathname, handle, windows=True)
        del pathname.st_birthtime_ns
        self.assertEqual(lab.archive_handle_identity(pathname, handle, windows=True), lab.archive_identity(handle))
        handle.st_ctime_ns += 1
        with self.assertRaises(ValueError):
            lab.archive_handle_identity(pathname, handle, windows=True)

    def test_hash_rejects_every_raw_handle_and_path_change_even_with_windows_semantics(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'fixture.exe'
            path.write_bytes(b'payload')
            pathname, handle = self.info(st_mode=stat.S_IFREG | 0o777), self.info(st_ctime_ns=41)
            helper = lab.archive_handle_identity
            with patch.object(lab, 'archive_handle_identity', side_effect=lambda a, b: helper(a, b, windows=True)):
                for source in ('path', 'handle'):
                    for field in self.FIELDS:
                        original = pathname if source == 'path' else handle
                        changed = self.info(**(vars(original) | {field: getattr(original, field) + 1}))
                        path_values = [pathname, changed] if source == 'path' else [pathname, pathname]
                        handle_values = [handle, changed] if source == 'handle' else [handle, handle]
                        with self.subTest(source=source, field=field), \
                                patch.object(Path, 'lstat', side_effect=path_values), \
                                patch.object(lab.os, 'fstat', side_effect=handle_values), self.assertRaises(ValueError):
                            lab.archive_hash(path, time.monotonic() + 10)
                with patch.object(Path, 'lstat', return_value=pathname), \
                        patch.object(lab.os, 'fstat', return_value=handle):
                    self.assertEqual(lab.archive_hash(path, time.monotonic() + 10), hashlib.sha256(b'payload').hexdigest())

    def test_staging_uses_shared_comparison_and_keeps_full_handle_revalidation(self):
        source = inspect.getsource(lab.archive_stage)
        self.assertIn('observed = archive_identity(before)', source)
        self.assertEqual(source.count("require(observed == row['identity'])"), 3)
        self.assertIn('opened = archive_handle_identity(before, handle_info)', source)
        self.assertIn('require(observed == opened)', source)
        self.assertIn('after = source.lstat()', source)
        self.assertIn("after = row['source'].lstat()", source)
        self.assertIn("digest.hexdigest() == row['sha256']", source)

    def test_synthetic_native_identity_reports_only_field_equalities(self):
        try:
            with tempfile.TemporaryDirectory() as temporary:
                path = Path(temporary).resolve() / 'fixture.exe'
                path.write_bytes(b'before!')
                time.sleep(0.02)
                path.write_bytes(b'payload')
                pathname = path.lstat()
                with path.open('rb') as stream:
                    handle = os.fstat(stream.fileno())
                    available = all(hasattr(info, 'st_birthtime_ns') for info in (pathname, handle))
                    # Emit fixed boolean comparisons BEFORE compatibility checks
                    # so a new native mismatch remains diagnosable without IDs.
                    result = {'schema': 1, 'scope': 'synthetic-path-handle-identity-only',
                        'pythonVersion': list(sys.version_info[:3]),
                        'platform': sys.platform if sys.platform in ('linux', 'darwin', 'win32') else 'other',
                        'rawFieldEqual': {field: getattr(pathname, field, None) == getattr(handle, field, None) for field in self.FIELDS},
                        'birthtimeAvailable': available}
                    print(json.dumps(result))
                    opened = lab.archive_handle_identity(pathname, handle)
                    lab.require(stream.read() == b'payload')
                    lab.require(lab.archive_identity(os.fstat(stream.fileno())) == opened)
                lab.require(lab.archive_identity(path.lstat()) == lab.archive_identity(pathname))
        except Exception:
            raise AssertionError('Synthetic file identity fixture failed; details withheld.') from None

    def test_native_field_diagnostic_survives_failure_without_raw_values(self):
        output = io.StringIO()
        with contextlib.redirect_stdout(output), \
                patch.object(lab, 'archive_handle_identity', side_effect=ValueError('private-identity-sentinel')), \
                self.assertRaisesRegex(AssertionError, 'Synthetic file identity fixture failed; details withheld.'):
            self.test_synthetic_native_identity_reports_only_field_equalities()
        record = json.loads(output.getvalue())
        self.assertEqual(set(record), {'schema', 'scope', 'pythonVersion', 'platform', 'rawFieldEqual', 'birthtimeAvailable'})
        self.assertEqual(set(record['rawFieldEqual']), set(self.FIELDS))
        self.assertTrue(all(type(value) is bool for value in record['rawFieldEqual'].values()))
        self.assertTrue(all(type(value) is int for value in record['pythonVersion']))
        self.assertNotIn('private-identity-sentinel', output.getvalue())
        self.assertNotIn('fixture.exe', output.getvalue())



class ArchiveStageDiagnosticTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.source = self.root / 'source.bin'
        self.source.write_bytes(b'payload')
        self.directory = self.root / 'source-directory'
        self.directory.mkdir()
        self.serial = 0

    def run_failure(self, fault):
        self.serial += 1
        stage = self.root / ('stage-' + str(self.serial))
        before = lab.archive_identity(self.source.lstat())
        directory = fault == 'stage_directory_create'
        source = self.directory if directory else self.source
        info = source.lstat()
        row = {'source': source, 'identity': lab.archive_identity(info),
               'kind': 'directory' if directory else 'file', 'size': 0 if directory else 7,
               'sha256': hashlib.sha256(b'payload').hexdigest()}
        if fault == 'payload_bound':
            row['size'] = 6
        if fault == 'payload_short':
            row['size'] = 8
        if fault == 'payload_hash':
            row['sha256'] = 'private-hash-sentinel'
        if fault == 'stage_fresh':
            stage.mkdir()
        diagnostic, counts = {}, {}
        original_open, original_mkdir, original_lstat = Path.open, Path.mkdir, Path.lstat
        original_fstat, original_handle, original_copy, original_tick = os.fstat, lab.archive_handle_identity, shutil.copystat, lab.archive_tick

        def hit(name):
            counts[name] = counts.get(name, 0) + 1
            if fault == name:
                raise OSError(13, 'private-error-sentinel', 'private-member-sentinel')

        class Proxy:
            def __init__(self, stream, role):
                self.stream, self.role = stream, role

            def __enter__(self):
                return self

            def __exit__(self, kind, value, traceback):
                self.stream.close()
                if kind is None:
                    hit('stage_file_close' if self.role == 'stage' else 'source_close')

            def fileno(self):
                return self.stream.fileno()

            def read(self, size):
                hit('source_read')
                return self.stream.read(size)

            def write(self, block):
                hit('stage_write')
                return self.stream.write(block)

        def opened(path, mode='r', *args, **kwargs):
            if path == source and mode == 'rb':
                hit('source_open')
                return Proxy(original_open(path, mode, *args, **kwargs), 'source')
            if path == stage / 'member' and mode == 'xb':
                hit('stage_file_create')
                return Proxy(original_open(path, mode, *args, **kwargs), 'stage')
            return original_open(path, mode, *args, **kwargs)

        def mkdir(path, *args, **kwargs):
            if path == stage:
                hit('stage_mkdir')
            elif path == stage / 'member':
                hit('stage_directory_create')
            return original_mkdir(path, *args, **kwargs)

        def lstat(path, *args, **kwargs):
            actual = original_lstat(path, *args, **kwargs)
            if path != source:
                return actual
            number = counts.get('lstat', 0) + 1
            counts['lstat'] = number
            phase = {1: 'before', 2: 'after', 3: 'after_metadata'}[number]
            hit('source_stat_' + phase)
            if fault == 'source_identity_' + phase:
                values = {field: getattr(actual, field, 0 if field == 'st_file_attributes' else None)
                          for field in lab.ARCHIVE_STAGE_FIELDS}
                values['st_mtime_ns'] += 1
                return SimpleNamespace(**values)
            return actual

        def fstat(fd):
            number = counts.get('fstat', 0) + 1
            counts['fstat'] = number
            hit('source_handle_stat_before' if number == 1 else 'source_handle_stat_after')
            actual = original_fstat(fd)
            if fault == 'handle_identity_after' and number == 2:
                values = {field: getattr(actual, field, 0 if field == 'st_file_attributes' else None)
                          for field in lab.ARCHIVE_STAGE_FIELDS}
                values['st_ctime_ns'] += 1
                return SimpleNamespace(**values)
            return actual

        def handle(a, b):
            if fault == 'handle_identity_initial':
                raise ValueError('private-identity-sentinel')
            return original_handle(a, b)

        def copy(a, b, **kwargs):
            hit('metadata_copy')
            return original_copy(a, b, **kwargs)

        def tick(deadline):
            counts['ticks'] = counts.get('ticks', 0) + 1
            if fault == 'deadline_check' or fault == 'chunk_deadline' and counts['ticks'] == 2:
                raise ValueError('private-clock-sentinel')
            return original_tick(deadline)

        with patch.object(Path, 'open', opened), patch.object(Path, 'mkdir', mkdir), \
                patch.object(Path, 'lstat', lstat), patch.object(lab.os, 'fstat', fstat), \
                patch.object(lab, 'archive_handle_identity', handle), patch.object(lab.shutil, 'copystat', copy), \
                patch.object(lab, 'archive_tick', tick), patch.object(lab, 'archive_remove') as remove, \
                patch.object(lab.shutil, 'rmtree') as rmtree:
            with self.assertRaises((OSError, ValueError)):
                lab.archive_stage({'member': row}, stage, time.monotonic() + 10, diagnostic)
            remove.assert_not_called()
            rmtree.assert_not_called()
        self.assertEqual(self.source.read_bytes(), b'payload')
        self.assertEqual(lab.archive_identity(self.source.lstat()), before)
        self.assertEqual(stage.exists(), fault != 'stage_mkdir')
        encoded = json.dumps(diagnostic)
        for private in (str(self.root), 'private-', 'source.bin', 'member-sentinel', row['sha256']):
            self.assertNotIn(private, encoded)
        self.assertLess(len(encoded), 1024)
        if fault in counts:
            self.assertEqual(counts[fault], 1)  # No retry of a failing operation.
        if fault == 'chunk_deadline':
            self.assertEqual(counts['ticks'], 2)
        return diagnostic

    def test_all_stage_guard_and_io_boundaries_are_classified_without_cleanup_or_retry(self):
        invariants = {'stage_fresh': 'stage_not_fresh', 'deadline_check': 'deadline_rejected',
            'source_identity_before': 'source_identity_rejected', 'handle_identity_initial': 'handle_identity_rejected',
            'payload_bound': 'payload_size_rejected', 'handle_identity_after': 'handle_identity_changed',
            'source_identity_after': 'source_identity_rejected', 'source_identity_after_metadata': 'source_identity_rejected'}
        for operation in sorted(lab.ARCHIVE_STAGE_OPERATIONS - {'payload_verify'}):
            with self.subTest(operation=operation):
                result = self.run_failure(operation)
                self.assertEqual(result['operation'], operation)
                self.assertEqual(result['category'], invariants.get(operation, 'io_permission'))
                self.assertEqual(result['status'], 'failed')
        self.assertEqual(self.run_failure('chunk_deadline'),
                         {'status': 'failed',
                          'operationAttribution': 'last-marked-boundary-may-precede-unwind-error', 'operation': 'deadline_check', 'category': 'deadline_rejected'})
        for fault in ('payload_short', 'payload_hash'):
            with self.subTest(fault=fault):
                result = self.run_failure(fault)
                self.assertEqual(result, {'status': 'failed',
                          'operationAttribution': 'last-marked-boundary-may-precede-unwind-error', 'operation': 'payload_verify', 'category': 'payload_rejected'})

    def test_double_fault_keeps_exception_precedence_and_marks_uncertain_attribution(self):
        stage = self.root / 'double-fault'
        before = lab.archive_identity(self.source.lstat())
        row = {'source': self.source, 'identity': before, 'kind': 'file',
               'size': 7, 'sha256': hashlib.sha256(b'payload').hexdigest()}
        original_open = Path.open
        first = ValueError('private-read-error')
        second = PermissionError(lab.errno.EACCES, 'private-close-error', 'private-path')
        counts, diagnostic = {}, {}

        class Proxy:
            def __init__(self, stream, role):
                self.stream, self.role = stream, role

            def __enter__(self):
                return self

            def __exit__(self, kind, value, traceback):
                self.stream.close()
                counts[self.role + '_close'] = counts.get(self.role + '_close', 0) + 1
                if self.role == 'stage':
                    raise second

            def fileno(self):
                return self.stream.fileno()

            def read(self, size):
                counts['source_read'] = counts.get('source_read', 0) + 1
                raise first

        def opened(path, mode='r', *args, **kwargs):
            stream = original_open(path, mode, *args, **kwargs)
            return Proxy(stream, 'source' if path == self.source else 'stage')

        with patch.object(Path, 'open', opened), patch.object(lab, 'archive_remove') as remove, \
                patch.object(lab.shutil, 'rmtree') as rmtree:
            with self.assertRaises(PermissionError) as caught:
                lab.archive_stage({'member': row}, stage, time.monotonic() + 10, diagnostic)
            remove.assert_not_called()
            rmtree.assert_not_called()
        self.assertIs(caught.exception, second)
        self.assertIs(caught.exception.__context__, first)
        self.assertEqual(counts, {'source_read': 1, 'stage_close': 1, 'source_close': 1})
        self.assertEqual(diagnostic, {'status': 'failed', 'operation': 'source_read',
            'category': 'io_permission',
            'operationAttribution': 'last-marked-boundary-may-precede-unwind-error'})
        encoded = json.dumps(diagnostic)
        for private in ('private-', str(self.root), 'member', row['sha256']):
            self.assertNotIn(private, encoded)
        self.assertLess(len(encoded), 1024)
        self.assertEqual(self.source.read_bytes(), b'payload')
        self.assertEqual(lab.archive_identity(self.source.lstat()), before)
        self.assertTrue((stage / 'member').is_file())

    def test_identity_diagnostics_are_only_already_observed_field_equalities(self):
        for fault, changed in [('source_identity_before', 'st_mtime_ns'),
                               ('source_identity_after', 'st_mtime_ns'),
                               ('source_identity_after_metadata', 'st_mtime_ns'),
                               ('handle_identity_after', 'st_ctime_ns')]:
            with self.subTest(fault=fault):
                fields = self.run_failure(fault)['fieldEqual']
                self.assertEqual(set(fields), set(lab.ARCHIVE_STAGE_FIELDS))
                self.assertEqual([key for key, value in fields.items() if not value], [changed])
                self.assertTrue(all(type(value) is bool for value in fields.values()))

    def test_error_classifier_is_closed_and_never_serializes_private_operands(self):
        for number, category in [(lab.errno.ENOENT, 'io_missing'), (lab.errno.EACCES, 'io_permission'),
                                 (lab.errno.EPERM, 'io_permission'), (lab.errno.EEXIST, 'io_exists'),
                                 (lab.errno.ENOSPC, 'io_disk_full'), (lab.errno.ENAMETOOLONG, 'io_path_length'),
                                 (123456789, 'io_other')]:
            result = lab.archive_stage_failure('source_open', OSError(number, 'private-error', 'private-path'))
            self.assertEqual(result, {'status': 'failed',
                          'operationAttribution': 'last-marked-boundary-may-precede-unwind-error', 'operation': 'source_open', 'category': category})
        for number, category in [(2, 'io_missing'), (3, 'io_missing'), (5, 'io_permission'),
                (32, 'io_sharing_or_lock'), (33, 'io_sharing_or_lock'), (80, 'io_exists'),
                (112, 'io_disk_full'), (183, 'io_exists'), (206, 'io_path_length'), (50, 'io_unsupported')]:
            error = OSError(13, 'private-error', 'private-path')
            error.winerror = number
            self.assertEqual(lab.archive_stage_failure('stage_write', error)['category'], category)
        error = OSError(123456789, 'private-error-sentinel', 'private-path-sentinel')
        error.winerror = ['private-noninteger']
        result = lab.archive_stage_failure('private-operation-sentinel', error,
            {'private-member-sentinel': True, 'st_ino': 123456789, 'st_mtime_ns': 'private-time', 'st_dev': False})
        self.assertEqual(result, {'status': 'failed',
                          'operationAttribution': 'last-marked-boundary-may-precede-unwind-error', 'operation': 'unknown_operation', 'category': 'io_other',
                                  'fieldEqual': {'st_dev': False}})
        for error in (ValueError('private-value'), RuntimeError('private-runtime'), KeyError('private-key'),
                      NotImplementedError('private-unsupported'), TypeError('private-type')):
            encoded = json.dumps(lab.archive_stage_failure('source_read', error))
            self.assertNotIn('private', encoded)
        class NoStringError(OSError):
            def __str__(self):
                raise AssertionError('exception string must never be used')
        self.assertEqual(lab.archive_stage_failure('source_read', NoStringError(13, 'private'))['category'], 'io_permission')

    def test_success_preserves_exact_filesystem_observation_counts(self):
        info = self.source.lstat()
        row = {'source': self.source, 'identity': lab.archive_identity(info), 'kind': 'file',
               'size': 7, 'sha256': hashlib.sha256(b'payload').hexdigest()}
        original = Path.lstat
        calls = []

        def lstat(path, *args, **kwargs):
            calls.append(path)
            return original(path, *args, **kwargs)

        with patch.object(Path, 'lstat', lstat), patch.object(lab.os, 'fstat', wraps=os.fstat) as observed:
            lab.archive_stage({'member': row}, self.root / 'observed', time.monotonic() + 10, {})
        self.assertEqual(calls.count(self.source), 3)
        self.assertEqual(observed.call_count, 2)

    def test_success_record_is_bounded_and_has_no_identity_values(self):
        info = self.source.lstat()
        row = {'source': self.source, 'identity': lab.archive_identity(info), 'kind': 'file',
               'size': 7, 'sha256': hashlib.sha256(b'payload').hexdigest()}
        record = {'private-old-key': 'private-old-value'}
        stage = self.root / 'successful'
        lab.archive_stage({'member': row}, stage, time.monotonic() + 10, record)
        self.assertEqual(record, {'status': 'completed'})
        self.assertEqual((stage / 'member').read_bytes(), b'payload')
        self.assertEqual(lab.archive_identity(self.source.lstat()), row['identity'])


class ArchiveStageFailureReportTests(unittest.TestCase):
    setUp = ArchiveTests.setUp

    def test_failed_stage_survives_main_sanitization_without_promoting_sample(self):
        tool = self.root / 'synthetic-tool'
        tool.write_bytes(b'generic tool identity fixture')
        plan = {'roots': [self.modules, self.build], 'key': 'synthetic-exact-key',
                'platform': 'linux-fixture', 'source': {'fixture': 'generic'},
                'commit': 'a' * 40, 'runner': 'synthetic'}
        tools = {'tar': str(tool), 'pack': str(tool), 'unpack': str(tool), 'platform': 'linux-fixture'}
        calls = []

        def capture(phase, *args):
            calls.append(phase)
            return b'tar (GNU tar) 1.35' if phase == 'archive_tar_version' else b'*** Zstandard CLI (64-bit) v1.5.7 ***'

        output = io.StringIO()
        with patch.dict(os.environ, {'CACHE_HIT': 'true', 'CACHE_MATCHED_KEY': plan['key'],
                    'RUNNER_TEMP': str(self.root), 'ImageVersion': '20260101.1', 'GITHUB_OUTPUT': str(self.root / 'outputs')}), \
                patch.object(lab, 'admission', return_value=plan), patch.object(lab.os, 'umask'), \
                patch.object(lab, 'archive_tools', return_value=tools), \
                patch.object(lab, 'archive_environment', side_effect=lambda tools, private: dict(os.environ)), \
                patch.object(lab, 'archive_capture', side_effect=capture), \
                patch.object(lab.shutil, 'copystat', side_effect=OSError(13, 'private-error-sentinel', 'private-path-sentinel')), \
                patch.object(lab, 'archive_remove') as remove, contextlib.redirect_stdout(output):
            self.assertEqual(lab.main(['archive']), 1)
            remove.assert_not_called()
        encoded = (self.root / 'soba-native-cache-sanitized/result.json').read_text()
        report = json.loads(encoded)
        self.assertFalse(report['diagnosticPassed'])
        self.assertIsNone(report['originalContentIdentityAndMembershipUnchanged'])
        self.assertEqual(len(report['samples']), 1)
        sample = report['samples'][0]
        self.assertEqual(sample['staging'], {'status': 'failed',
                          'operationAttribution': 'last-marked-boundary-may-precede-unwind-error', 'operation': 'metadata_copy', 'category': 'io_permission'})
        self.assertEqual(sample['phases'], {})
        self.assertNotIn('stagingSeconds', sample)
        self.assertNotIn('sampleTotalSeconds', sample)
        self.assertEqual(calls, ['archive_tar_version', 'archive_pack_version', 'archive_unpack_version'])
        self.assertTrue(list(self.root.glob('soba-cache-archive-*/stage')))
        self.assertLess(len(encoded.encode()), lab.ARCHIVE_RESULT_BYTES)
        for private in (str(self.root), 'private-error-sentinel', 'private-path-sentinel'):
            self.assertNotIn(private, encoded + output.getvalue())



class ArchiveFormatTests(unittest.TestCase):
    def member(self, name='build/file', data=b'payload', kind=tarfile.REGTYPE, mode=0o644, link=''):
        info = tarfile.TarInfo(name)
        info.type, info.size, info.mode, info.linkname = kind, len(data), mode, link
        return info.tobuf(format=tarfile.USTAR_FORMAT) + data + bytes((-len(data)) % 512)

    def expected(self):
        return {'build/file': {'kind': 'file', 'mode': 0o644, 'size': 7,
                               'sha256': hashlib.sha256(b'payload').hexdigest()}}

    def check(self, raw, expected=None, limit=1024 * 1024):
        return lab.archive_validate_stream(io.BytesIO(raw), self.expected() if expected is None else expected, limit)

    def test_valid_stream_padding_and_private_content_hash(self):
        raw = self.member() + bytes(1024)
        result = self.check(raw)
        self.assertEqual(result['streamBytes'], len(raw))
        self.assertEqual(result['filePayloadBytes'], 7)
        self.assertEqual(result['headerBytes'], 512)
        self.assertEqual(result['paxPayloadBytes'], 0)
        self.assertEqual(result['membersVerified'], 1)
        self.assertNotIn('sha256', json.dumps(result))

    def test_reject_links_special_sparse_contiguous_global_and_unknown_types(self):
        for kind in (tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.CHRTYPE, tarfile.BLKTYPE,
                     tarfile.FIFOTYPE, tarfile.GNUTYPE_SPARSE, tarfile.CONTTYPE,
                     tarfile.XGLTYPE, b'Z'):
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                self.check(self.member(kind=kind) + bytes(1024))
        with self.assertRaises(ValueError):
            self.check(self.member(link='outside') + bytes(1024))

    def test_reject_paths_duplicate_member_mode_content_checksum_and_trailing_payload(self):
        for raw in [self.member(name='../escape'), self.member(name='/build/file'),
                    self.member(name='C:/build/file'), self.member(name='build\\file'),
                    self.member(name='build//file'), self.member(name='build/file:stream'),
                    self.member() * 2, self.member(mode=0o755), self.member(data=b'changed'),
                    b'X' + self.member()[1:], self.member() + bytes(1024) + b'trailing']:
            with self.assertRaises(ValueError):
                self.check(raw + bytes(1024))
        with self.assertRaises(ValueError):
            self.check(self.member() + bytes(1024), limit=1000)
        with self.assertRaises(ValueError):
            self.check(self.member() + bytes(512))
        with self.assertRaises(ValueError):
            self.check(bytes(1024))

    def pax_record(self, key, value):
        body = key + b'=' + value + b'\n'
        size = len(body) + 2
        while len(str(size)) + 1 + len(body) != size:
            size = len(str(size)) + 1 + len(body)
        return str(size).encode() + b' ' + body

    def test_pax_override_allows_truncated_utf8_header_stub(self):
        pax = self.pax_record(b'path', b'build/file')
        header = bytearray(self.member())
        header[:100] = (b'build/' + b'x' * 92 + b'\xe6\x97')
        header[148:156] = b'        '
        header[148:156] = ('%06o\0 ' % sum(header[:512])).encode()
        raw = self.member(name='PaxHeaders/file', data=pax, kind=tarfile.XHDTYPE) + header + bytes(1024)
        self.assertEqual(self.check(raw)['membersVerified'], 1)

    def test_bounded_pax_path_and_times_only(self):
        pax = self.pax_record(b'path', b'build/file') + self.pax_record(b'mtime', b'123.456')
        raw = self.member(name='PaxHeaders/file', data=pax, kind=tarfile.XHDTYPE) + self.member(name='ignored') + bytes(1024)
        self.assertEqual(self.check(raw)['paxPayloadBytes'], len(pax))
        for key in (b'GNU.sparse.map', b'SCHILY.filetype', b'linkpath', b'size', b'unknown'):
            with self.assertRaises(ValueError):
                lab.archive_pax(self.pax_record(key, b'1'))
        for raw in (b'999999999999999999 x=1\n', b'0 x=1\n', b'9 path=x',
                    self.pax_record(b'mtime', b'nan'), self.pax_record(b'path', b'x') * 2,
                    self.pax_record(b'mtime', b'1') * 2):
            with self.assertRaises(ValueError):
                lab.archive_pax(raw)
        # Refuse huge PAX size before any read/allocation of its payload.
        info = tarfile.TarInfo('PaxHeaders/file')
        info.type, info.size = tarfile.XHDTYPE, lab.ARCHIVE_PAX_BYTES + 1
        stream = io.BytesIO(info.tobuf(format=tarfile.USTAR_FORMAT))
        with self.assertRaises(ValueError):
            lab.archive_validate_stream(stream, self.expected(), 1024 * 1024)
        self.assertEqual(stream.tell(), 512)
        with self.assertRaises(ValueError):
            self.check(self.member(data=pax, kind=tarfile.XHDTYPE) + bytes(1024))


class RealArchiveFixtureTests(unittest.TestCase):
    setUp = ArchiveTests.setUp
    snapshot = ArchiveTests.snapshot
    @unittest.skipUnless(sys.platform == 'linux' and shutil.which('zstd') and shutil.which('tar'),
                         'real Linux GNU tar/zstd fixture requires both tools')
    def test_real_streaming_roundtrip_preserves_large_files_long_pax_names_and_modes(self):
        try:
            self.check_streaming()
        except Exception:
            raise AssertionError('Offline streaming archive fixture failed; details withheld.') from None

    @unittest.skipUnless(sys.platform == 'linux' and shutil.which('zstd') and shutil.which('tar'),
                         'real Linux GNU tar/zstd fixture requires both tools')
    def test_full_pilot_accounting_deadline_and_cleanup(self):
        report = lab.archive_report()
        plan = {'roots': [self.modules, self.build], 'key': 'synthetic-exact-key',
                'platform': 'linux-fixture', 'source': {'fixture': 'generic'}}
        with patch.dict(os.environ, {'CACHE_HIT': 'true', 'CACHE_MATCHED_KEY': plan['key'],
                'RUNNER_TEMP': str(self.root), 'ImageVersion': '20260101.1'}), \
                patch.object(lab.collector, 'source_inventory', return_value=plan['source']):
            try:
                lab.measure_archives(plan, report)
            except Exception:
                raise AssertionError('Offline archive pilot fixture failed; details withheld.') from None
        self.assertEqual(len(report['samples']), 6)
        self.assertEqual([row['object'] for row in report['samples']],
                         [name for order in lab.ARCHIVE_ORDERS for name in order])
        self.assertEqual(len(report['blocks']), 2)
        self.assertTrue(report['originalContentIdentityAndMembershipUnchanged'])
        self.assertTrue(report['toolExecutableIdentityUnchanged'])
        self.assertLess(len(json.dumps(report)), lab.ARCHIVE_RESULT_BYTES)
        self.assertFalse(list(self.root.glob('soba-cache-archive-*')))
        for private in (str(self.root), 'ordinary.go', 'program', 'sha256'):
            self.assertNotIn(private, json.dumps(report))

    def check_streaming(self):
        # Synthetic only: no source checkout or restored native cache bytes.
        longdir = self.modules / ('long-' + 'x' * 90)
        longdir.mkdir()
        large = longdir / ('file-' + 'y' * 90)
        large.write_bytes(random.Random(0).randbytes(2 * 1024 ** 2 + 256))  # > 2 MiB, not a tiny USTAR fixture.
        large.chmod(0o444)
        (self.modules / ('日本語' * 24)).write_bytes(b'generic unicode-name fixture')
        (self.modules / 'ordinary.go').chmod(0o444)
        before = self.snapshot()
        tools = lab.archive_tools('linux-fixture')
        private = self.root / 'private'
        private.mkdir()
        env = dict(os.environ, GOTMPDIR=str(private), SOBALINK_ARCHIVE_SPEC=str(private / 'spec.json'))
        records, versions = [], {}
        for name in lab.ARCHIVE_OBJECTS:
            rows = lab.archive_projection(before, name)
            lab.archive_stage(rows, private / 'stage', self.deadline)
            (private / 'manifest.bin').write_bytes(b''.join(n.encode('utf-8') + b'\0' for n in rows))
            spec = {'private': str(private), 'tools': tools, 'streamBound': lab.archive_stream_bound(rows),
                    'expected': {n: {k: r[k] for k in ('kind', 'size', 'mode', 'sha256') if k in r} for n, r in rows.items()}}
            phases = {}
            for role in ('tar', 'pack', 'unpack'):
                raw = lab.archive_capture('archive_' + role + '_version', env, spec, self.deadline, phases)
                versions[role] = lab.archive_version(raw, role)
            lab.archive_capture('archive_pack', env, spec, self.deadline, phases)
            packed = private / 'cache.tzst'
            lab.require(packed.is_file() and packed.stat().st_size > 0)
            raw = lab.archive_capture('archive_validate', env, spec, self.deadline, phases)
            validated = json.loads(raw)
            lab.require(validated['membersVerified'] == len(rows))
            lab.require(validated['filePayloadBytes'] == lab.archive_totals(rows)['payloadBytes'])
            (private / 'destination').mkdir()
            lab.archive_capture('archive_extract', env, spec, self.deadline, phases)
            lab.archive_verify_destination(private / 'destination', rows, self.deadline)
            lab.require(self.snapshot() == before)
            records.append({'object': name, 'payload': lab.archive_totals(rows), 'compressedBytes': packed.stat().st_size,
                            'archiveValidation': validated, 'phases': phases})
            lab.archive_remove(private / 'destination')
            lab.archive_remove(private / 'stage')
            packed.unlink()
            (private / 'manifest.bin').unlink()
        lab.require(self.snapshot() == before)
        (private / 'spec.json').unlink()
        private.rmdir()
        print(json.dumps({'scope': 'linux-synthetic-streaming-mechanics-only',
                          'nativeCacheMeasurement': False, 'sourceContentUnchanged': True, 'versions': versions, 'objects': records}))


if __name__ == '__main__':
    if len(sys.argv) == 3 and sys.argv[1] == '--owned':
        raise SystemExit(owned_fixture(sys.argv[2]))
    unittest.main()
