import hashlib
import json
from pathlib import Path
import tempfile
import unittest

import importlib.util
import inspect
import contextlib
import io
import os
import shutil
import subprocess
import sys
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


if __name__ == '__main__':
    if len(sys.argv) == 3 and sys.argv[1] == '--owned':
        raise SystemExit(owned_fixture(sys.argv[2]))
    unittest.main()
