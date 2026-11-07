"""The opt-in cache experiment is isolated, paired and never acceptance proof."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('cache_profile', ROOT / '.github/scripts/ci-browser-cache-profile.py')
profile = importlib.util.module_from_spec(spec)
spec.loader.exec_module(profile)


class BrowserCacheProfileTests(unittest.TestCase):
    def environment(self, order='bp'):
        return {'GITHUB_SHA': 'a' * 40, 'GITHUB_REF': 'refs/heads/profile-browser-cache/' + 'a' * 12 + '-' + order,
                'GITHUB_REPOSITORY': 'webkaz-labs/sobalink', 'GITHUB_EVENT_NAME': 'create',
                'GITHUB_WORKFLOW': 'Browser cache profile (not acceptance)', 'GITHUB_JOB': 'profile',
                'RUNNER_OS': 'Linux', 'RUNNER_ARCH': 'X64', 'GITHUB_RUN_ID': '42', 'GITHUB_RUN_ATTEMPT': '1',
                'GOTOOLCHAIN': 'local', 'GOWORK': 'off', 'GOFLAGS': profile.GOFLAGS,
                'ImageVersion': '20261101.1.0'}

    def query(self, *args):
        if args == ('git', 'rev-parse', 'HEAD'):
            return 'a' * 40
        if args == ('git', 'rev-parse', 'HEAD^{tree}'):
            return 'b' * 40
        if args == ('git', 'rev-parse', '--show-toplevel'):
            return str(ROOT)
        if args == ('git', 'status', '--porcelain', '--untracked-files=no'):
            return ''
        if args[:3] == ('go', 'env', '-json'):
            return json.dumps({'GOVERSION': 'go1.27.1', 'GOOS': 'linux', 'GOARCH': 'amd64',
                               'GOHOSTOS': 'linux', 'GOHOSTARCH': 'amd64', 'CGO_ENABLED': '1', 'CC': 'gcc'})
        if args == ('zstd', '--version'):
            return 'Zstandard v1.5.7'
        raise AssertionError(args)

    def test_context_is_exact_head_and_explicitly_scoped(self):
        with patch.object(profile, 'query', side_effect=self.query):
            for order in ('bp', 'pb'):
                value = profile.context(self.environment(order))
                self.assertEqual(value['order'], order)
                self.assertEqual(value['commit'], 'a' * 40)
                self.assertEqual(value['tree'], 'b' * 40)
            for key, bad in (('GITHUB_REF', 'refs/heads/main'), ('GITHUB_SHA', 'c' * 40),
                             ('GITHUB_EVENT_NAME', 'pull_request'), ('GITHUB_JOB', 'browser-native'),
                             ('GITHUB_REPOSITORY', 'other/project'), ('RUNNER_ARCH', 'ARM64'),
                             ('GOFLAGS', '-mod=mod'), ('GOWORK', ''), ('GOTOOLCHAIN', 'auto'),
                             ('GITHUB_RUN_ATTEMPT', '0'), ('ImageVersion', 'private host'),
                             ('GOMAXPROCS', '1')):
                env = self.environment()
                env[key] = bad
                with self.subTest(key=key), self.assertRaises(ValueError):
                    profile.context(env)

    def experiment(self, order, *, alter_module=False, alter_binary=False):
        calls = []
        def measured(label, command, env, private, timings, timeout=600):
            calls.append((label, command, env))
            cache = Path(env['GOCACHE'])
            self.assertEqual(env['GOCACHEPROG'], '')
            self.assertEqual(env['GOENV'], 'off')
            if label == 'prepare':
                self.assertEqual(list(cache.iterdir()), [])
                (cache / 'preparer').write_bytes(b'p')
            if label.startswith('race-'):
                self.assertIn('-c', command)
                self.assertIn('-race', command)
                (cache / label).write_bytes(b'r')
            if label == 'writer-prewarm' or label.startswith('consumer-'):
                self.assertEqual(command[:3], ['go', 'build', '-tags=' + profile.UI_TAGS])
                target = Path(command[command.index('-o') + 1])
                self.assertFalse(target.exists())
                is_warm = (cache / 'nonrace').exists()
                (cache / 'nonrace').write_bytes(b'n')
                target.write_bytes(b'changed' if alter_binary and label == 'consumer-baseline' else b'fixture')
                if label == 'writer-prewarm':
                    self.assertNotEqual(env.get('GOPROXY'), 'off')
                    (Path(env['GOMODCACHE']) / 'additional-module').write_bytes(b'm')
                else:
                    self.assertEqual(env['GOPROXY'], 'off')
                    self.assertEqual(env['GOSUMDB'], 'off')
                    if alter_module:
                        (Path(env['GOMODCACHE']) / 'unexpected').write_bytes(b'x')
                timings[label] = 2.0 if is_warm else 20.0
            else:
                timings[label] = 1.0

        def archive(cache, modules, private):
            warm = (cache / 'nonrace').exists()
            return {'build_cache_files': len(profile.inventory(cache)), 'build_cache_bytes': 8 if warm else 7,
                    'module_and_build_zstd_bytes_estimate': 150 if warm else 100, 'compression_seconds': 1.0}

        with tempfile.TemporaryDirectory() as directory:
            private = Path(directory)
            with patch.object(profile, 'measured', side_effect=measured), patch.object(profile, 'archive_estimate', side_effect=archive), patch.object(profile, 'query', side_effect=self.query):
                result = profile.profile(private, {'commit': 'a' * 40, 'order': order}, {'GOCACHEPROG': 'unwanted'})
            self.assertFalse((private / 'cache-baseline' / 'nonrace').exists())
            self.assertTrue((private / 'cache-prewarmed' / 'nonrace').exists())
            self.assertFalse((private / 'consumer-baseline').exists())
            self.assertFalse((private / 'consumer-prewarmed').exists())
            self.assertNotIn(directory, json.dumps(result))
            return result, calls

    def test_paired_consumers_use_independent_seeds_and_no_network(self):
        for order, expected in (('bp', ['consumer-baseline', 'consumer-prewarmed']),
                                ('pb', ['consumer-prewarmed', 'consumer-baseline'])):
            result, calls = self.experiment(order)
            self.assertEqual([x[0] for x in calls if x[0].startswith('consumer-')], expected)
            self.assertEqual(result['consumer_build_seconds_saved'], 18.0)
            self.assertEqual(result['archive_bytes_added_estimate'], 50)
            self.assertEqual(result['module_bytes_added_by_prewarm'], 1)
            self.assertEqual(len([x for x in calls if x[0].startswith('race-')]), 6)
            self.assertEqual(len(result['seed_compile_targets']), 6)
            self.assertIn(('core-nearby', './internal/core', 'lanlink_integration,directlan_integration,' + profile.OMIT + ',ts_omit_udptransport'), profile.RACE_TARGETS)
            self.assertFalse(result['acceptance_validated'])
            self.assertFalse(result['production_cache_written'])
            self.assertFalse(result['remote_transfer_measured'])
            self.assertEqual(result['consumer_module_condition'], 'post-prewarm-fixed-offline')

    def test_mutated_module_cache_or_consumer_binary_cannot_pass(self):
        for options in ({'alter_module': True}, {'alter_binary': True}):
            with self.subTest(options=options), self.assertRaises(ValueError):
                self.experiment('bp', **options)

    @unittest.skipUnless(shutil.which('zstd') and shutil.which('tar'), 'tar and zstd unavailable')
    def test_archive_estimate_normalizes_seed_name_and_excludes_other_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            baseline, warmed, modules = (root / name for name in ('cache-baseline', 'cache-prewarmed', 'modules'))
            baseline.mkdir()
            modules.mkdir()
            (baseline / 'compiled').write_bytes(b'compiled object')
            (modules / 'source').write_bytes(b'module source')
            shutil.copytree(baseline, warmed)
            before = profile.archive_estimate(baseline, modules, root)
            (root / 'unpublished.private.log').write_bytes(b'not an archive member' * 1000)
            after = profile.archive_estimate(warmed, modules, root)
            self.assertEqual(before['module_and_build_zstd_bytes_estimate'], after['module_and_build_zstd_bytes_estimate'])
            self.assertEqual(before['build_cache_bytes'], len(b'compiled object'))

    def test_inventory_rejects_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'real').write_text('x')
            (root / 'link').symlink_to(root / 'real')
            with self.assertRaises(ValueError):
                profile.inventory(root)

    def test_failure_artifact_does_not_contain_exception_or_private_path(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, RUNNER_TEMP=directory):
            with patch.object(profile, 'context', return_value={'commit': 'a' * 40}), patch.object(profile, 'profile', side_effect=ValueError('private-sensitive-message')):
                self.assertEqual(profile.main(), 1)
            text = (Path(directory) / 'browser-cache-profile/result.json').read_text()
            self.assertNotIn('private-sensitive-message', text)
            self.assertNotIn(directory, text)
            self.assertFalse(json.loads(text)['success'])
            self.assertEqual(json.loads(text)['phase'], 'validation')

    def test_failure_phase_has_closed_vocabulary(self):
        self.assertEqual(profile.ProfileFailure('writer-prewarm').phase, 'writer-prewarm')
        with self.assertRaises(ValueError):
            profile.ProfileFailure('private-sensitive-message')

    def test_workflow_cannot_write_shared_cache_or_supply_acceptance(self):
        text = (ROOT / '.github/workflows/browser-cache-profile.yml').read_text()
        self.assertIn('on:\n  create:\n', text)
        self.assertNotIn('workflow_dispatch:', text)
        self.assertNotIn('pull_request:', text)
        self.assertNotIn('actions/cache', text)
        self.assertNotIn('id-token:', text)
        self.assertNotIn('secrets.', text)
        self.assertIn('permissions:\n  contents: read\n', text)
        self.assertIn('timeout-minutes: 20', text)
        self.assertIn('cache: false', text)
        self.assertIn('persist-credentials: false', text)
        self.assertEqual(text.count('path: ${{ runner.temp }}/browser-cache-profile/result.json'), 1)
        self.assertNotIn('ci-coverage', text)


if __name__ == '__main__':
    unittest.main()
