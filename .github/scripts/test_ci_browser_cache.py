"""Cache reuse must not cross trust, target, dependency or validation boundaries."""
import copy
import hashlib
import importlib.util
import os
from pathlib import Path
import re
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('browser_cache', Path(__file__).with_name('ci-browser-cache.py'))
cache = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cache)
import ci_full_validation as full


class ScopeTests(unittest.TestCase):
    def setUp(self):
        self.env = dict(GITHUB_REPOSITORY='webkaz-labs/sobalink', GITHUB_JOB='browser',
                        RUNNER_OS='Linux', RUNNER_ARCH='X64', GITHUB_EVENT_NAME='pull_request',
                        GITHUB_REF='refs/pull/123/merge')
        repo = dict(full_name='webkaz-labs/sobalink', default_branch='main', private=False)
        self.event = dict(repository=repo, number=123, pull_request=dict(number=123,
            head=dict(repo=dict(repo)), base=dict(repo=dict(repo))))

    def test_same_repository_pull_request(self):
        self.assertEqual(cache.cache_scope(self.env, self.event), 'pr-123')

    def test_main_trigger_allowlist(self):
        self.env['GITHUB_REF'] = 'refs/heads/main'
        for event in ('push', 'workflow_dispatch', 'schedule'):
            self.env['GITHUB_EVENT_NAME'] = event
            self.assertEqual(cache.cache_scope(self.env, self.event), 'main')
        for event in ('pull_request_target', 'issue_comment', 'workflow_run', 'release', 'pull_request'):
            self.env['GITHUB_EVENT_NAME'] = event
            self.assertEqual(cache.cache_scope(self.env, self.event), '')

    def test_manual_branch_hashes_arbitrary_names(self):
        self.env.update(GITHUB_EVENT_NAME='workflow_dispatch', GITHUB_REF='refs/heads/synthetic/main-prefix')
        self.assertEqual(cache.cache_scope(self.env, self.event),
                         'branch-' + hashlib.sha256(self.env['GITHUB_REF'].encode()).hexdigest())
        for ref in ('refs/heads/', 'refs/tags/example', ''):
            self.env['GITHUB_REF'] = ref
            self.assertEqual(cache.cache_scope(self.env, self.event), '')

    def test_fork_and_mismatched_identity_have_no_scope(self):
        for location in ('head', 'base'):
            event = copy.deepcopy(self.event)
            event['pull_request'][location]['repo']['full_name'] = 'synthetic/fork'
            self.assertEqual(cache.cache_scope(self.env, event), '')
        for key, value in (('full_name', 'synthetic/fork'), ('private', True), ('default_branch', 'other')):
            event = copy.deepcopy(self.event)
            event['repository'][key] = value
            self.assertEqual(cache.cache_scope(self.env, event), '')

    def test_target_job_and_repository_are_fixed(self):
        for key, value in (('GITHUB_REPOSITORY', 'synthetic/fork'), ('GITHUB_JOB', 'native'),
                           ('RUNNER_OS', 'Windows'), ('RUNNER_ARCH', 'ARM64')):
            env = dict(self.env, **{key: value})
            self.assertEqual(cache.cache_scope(env, self.event), '')

    def test_pull_request_number_and_merge_ref_are_bound(self):
        for number in (True, '123', -1, 0, None):
            event = dict(self.event, number=number)
            self.assertEqual(cache.cache_scope(self.env, event), '')
        for ref in ('refs/pull/122/merge', 'refs/pull/123/head', 'refs/heads/main'):
            self.assertEqual(cache.cache_scope(dict(self.env, GITHUB_REF=ref), self.event), '')
        event = copy.deepcopy(self.event)
        event['pull_request']['number'] = 122
        self.assertEqual(cache.cache_scope(self.env, event), '')

    def test_main_emits_only_go_cache_paths_and_scope(self):
        import json
        with tempfile.TemporaryDirectory() as tmp:
            event = Path(tmp) / 'event.json'
            out = Path(tmp) / 'out'
            event.write_text(json.dumps(self.event))
            env = dict(self.env, GITHUB_EVENT_PATH=str(event), GITHUB_OUTPUT=str(out))
            with mock.patch.dict(os.environ, env, clear=True), mock.patch.object(
                    cache.subprocess, 'check_output', return_value='go1.27.1\nlinux\namd64\n/tmp/mod\n/tmp/build\n'):
                cache.main()
            self.assertEqual(out.read_text(), 'scope=pr-123\ngomodcache=/tmp/mod\ngocache=/tmp/build\n')

    def test_actual_toolchain_mismatch_is_rejected(self):
        import json
        with tempfile.TemporaryDirectory() as tmp:
            event = Path(tmp) / 'event.json'
            event.write_text(json.dumps(self.event))
            env = dict(self.env, GITHUB_EVENT_PATH=str(event), GITHUB_OUTPUT=str(Path(tmp) / 'out'))
            with mock.patch.dict(os.environ, env, clear=True), mock.patch.object(
                    cache.subprocess, 'check_output', return_value='go1.27.3\nlinux\namd64\n/tmp/mod\n/tmp/build\n'):
                with self.assertRaises(SystemExit):
                    cache.main()


class WorkflowTests(unittest.TestCase):
    def setUp(self):
        self.sources = {p: (ROOT / p).read_bytes() for p in full.SOURCES}

    def test_reviewed_workflow_passes(self):
        full.validate_browser_cache_workflow(self.sources)
        full.required_jobs(self.sources)

    def test_browser_cache_mutations_fail_closed(self):
        original = self.sources[full.WORKFLOW]
        start = original.index(b'  browser:\n')
        region = original[start:]
        changes = (
            (b'actions/cache/restore@', b'actions/cache/save@'),
            (b'55cc8345863c7cc4c66a329aec7e433d2d1c52a9', b'unreviewed'),
            (b'browser-go-v1-', b'trusted-main-go-v1-'),
            (b"-${{ runner.arch }}-ubuntu-24.04-go1.27.1-", b'-'),
            (b"'cmd/prepare-engine/**'", b"'README.md'"),
            (b'${{ steps.browser-go-cache-info.outputs.gocache }}', b'${{ github.workspace }}'),
            (b"if: success() && steps.browser-go-cache-info", b"if: always() && steps.browser-go-cache-info"),
            (b"steps.browser-go-cache-info.outputs.scope != ''", b'true'),
        )
        for old, new in changes:
            self.assertIn(old, region)
            self.sources[full.WORKFLOW] = original[:start] + region.replace(old, new, 1)
            with self.subTest(change=old), self.assertRaises(ValueError):
                full.validate_browser_cache_workflow(self.sources)

    def test_browser_helper_is_source_bound(self):
        self.sources['.github/scripts/ci-browser-cache.py'] += b'\n# changed\n'
        with self.assertRaises(ValueError):
            full.validate_browser_cache_workflow(self.sources)

    def test_browser_seed_matches_only_the_same_target_and_dependencies(self):
        workflow = self.sources[full.WORKFLOW].decode()
        start = workflow.index('      - name: Restore isolated browser Go cache\n')
        end = workflow.index('      - name: Report browser Go cache result\n', start)
        restore = workflow[start:end]
        primary = re.search(r'^          key: (.+)$', restore, re.MULTILINE).group(1)
        prefixes = restore.split('          restore-keys: |\n', 1)[1].splitlines()
        prefixes = [line.strip() for line in prefixes]
        self.assertEqual(len(prefixes), 2)
        self.assertEqual(prefixes[0], primary.removesuffix('${{ github.sha }}'))
        self.assertEqual(prefixes[1], prefixes[0].replace(
            '${{ steps.browser-go-cache-info.outputs.scope }}', 'main'))

        values = {'steps.browser-go-cache-info.outputs.scope': 'pr-123',
                  'runner.os': 'Linux', 'runner.arch': 'X64', 'github.sha': 'a' * 40}
        manifests = re.search(r'\$\{\{ (hashFiles\(.+?\)) \}\}', primary).group(1)
        values[manifests] = 'b' * 64

        def render(template, replacements):
            return re.sub(r'\$\{\{ (.+?) \}\}', lambda m: replacements[m[1]], template)

        rendered_prefixes = [render(prefix, values) for prefix in prefixes]
        # This PR's earlier result wins; main seeds only the first cold run.
        for scope, expected_index in (('pr-123', 0), ('main', 1)):
            old_key = render(primary, dict(values, **{
                'steps.browser-go-cache-info.outputs.scope': scope, 'github.sha': 'c' * 40}))
            self.assertEqual(next(i for i, prefix in enumerate(rendered_prefixes)
                                  if old_key.startswith(prefix)), expected_index)
        for field, alternatives in (
                ('steps.browser-go-cache-info.outputs.scope', ('pr-124', 'branch-' + 'd' * 64)),
                ('runner.os', ('Windows', 'macOS')),
                ('runner.arch', ('ARM64',)), (manifests, ('e' * 64,))):
            for value in alternatives:
                other_key = render(primary, dict(values, **{field: value}))
                with self.subTest(field=field, value=value):
                    self.assertFalse(any(other_key.startswith(p) for p in rendered_prefixes))
        for old, new in (('ubuntu-24.04', 'ubuntu-26.04'), ('go1.27.1', 'go1.27.3'),
                         ('browser-go-v1-', 'trusted-main-go-v1-')):
            other_key = render(primary, values).replace(old, new)
            self.assertFalse(any(other_key.startswith(p) for p in rendered_prefixes))

    def test_browser_seed_never_replaces_the_scoped_save_key(self):
        workflow = self.sources[full.WORKFLOW].decode()
        start = workflow.index('      - name: Save isolated browser Go cache after all checks pass\n')
        end = workflow.index('  go-unit:\n', start)
        save = workflow[start:end]
        self.assertIn('key: ${{ steps.browser-go-cache.outputs.cache-primary-key }}', save)
        self.assertNotIn('key: ${{ steps.browser-go-cache.outputs.cache-matched-key }}', save)
        self.assertNotIn('browser-go-v1-main-', save)
        self.assertIn("success() && steps.browser-go-cache-info.outputs.scope != ''", save)
        self.assertIn("steps.browser-go-cache.outputs.cache-matched-key == ''", save)

    def test_acceleration_cache_saves_require_a_complete_miss(self):
        workflow = self.sources[full.WORKFLOW].decode()
        cases = (
            ('Save development Go caches after integration checks pass',
             'steps.development-go-cache-scope.outputs.scope',
             ('steps.development-go-cache.outputs.cache-matched-key',
              'steps.development-main-seed.outputs.cache-matched-key')),
            ('Save isolated browser Go cache after all checks pass',
             'steps.browser-go-cache-info.outputs.scope',
             ('steps.browser-go-cache.outputs.cache-matched-key',)),
        )
        for name, scope_field, cache_fields in cases:
            condition = workflow.split('      - name: ' + name + '\n', 1)[1].splitlines()[0]
            self.assertTrue(condition.startswith('        if: '))
            condition = condition.removeprefix('        if: ')
            # Evaluate the actual fixed workflow expression over exact hits,
            # fallback hits, cold misses, disallowed scopes and failed jobs.
            from itertools import product
            for success, scope, suite, hits in product(
                    (False, True), ('', 'pr-123'), ('unit', 'integration'),
                    product(('', 'exact-key', 'fallback-key'), repeat=len(cache_fields))):
                values = {'success()': success, scope_field: scope, 'matrix.suite': suite,
                          **dict(zip(cache_fields, hits))}
                expression = condition
                for field, value in values.items():
                    expression = expression.replace(field, repr(value))
                expression = expression.replace('&&', 'and')
                actual = eval(expression, {'__builtins__': {}}, {})
                expected = success and bool(scope) and not any(hits)
                if len(cache_fields) == 2:
                    expected = expected and suite == 'integration'
                with self.subTest(name=name, values=values):
                    self.assertEqual(actual, expected)

    def test_duplicate_cache_write_mutations_fail_closed(self):
        original = self.sources[full.WORKFLOW]
        for old, new in (
                (b"steps.browser-go-cache.outputs.cache-matched-key == ''",
                 b"steps.browser-go-cache.outputs.cache-hit != 'true'"),
                (b"steps.development-go-cache.outputs.cache-matched-key == '' && "
                 b"steps.development-main-seed.outputs.cache-matched-key == ''",
                 b"steps.development-go-cache.outputs.cache-hit != 'true'"),
                (b" && steps.development-main-seed.outputs.cache-matched-key == ''", b'')):
            self.assertIn(old, original)
            self.sources[full.WORKFLOW] = original.replace(old, new)
            with self.subTest(change=old), self.assertRaises(ValueError):
                full.required_jobs(self.sources)

    def test_browser_seed_policy_mutations_fail_closed(self):
        original = self.sources[full.WORKFLOW]
        seed = next(line for line in original.splitlines(keepends=True)
                    if line.startswith(b'            browser-go-v1-main-'))
        for replacement in (b'', seed.replace(b'-main-', b'-pr-124-'),
                            seed.replace(b'${{ runner.os }}', b'Windows'),
                            seed.replace(b'${{ runner.arch }}', b'ARM64'),
                            b'            browser-go-v1-\n',
                            seed.replace(b'go1.27.1', b'go1.27.3'),
                            seed.replace(b"'go.sum'", b"'README.md'")):
            self.sources[full.WORKFLOW] = original.replace(seed, replacement)
            with self.subTest(replacement=replacement), self.assertRaises(ValueError):
                full.validate_browser_cache_workflow(self.sources)

    def test_native_main_seed_is_restore_only_and_miss_only(self):
        workflow = self.sources[full.WORKFLOW].decode()
        start = workflow.index('      - name: Restore trusted main seed after development cache miss\n')
        end = workflow.index('      - name: Report Go cache results\n', start)
        block = workflow[start:end]
        self.assertIn("steps.development-go-cache.outputs.cache-matched-key == ''", block)
        self.assertIn('actions/cache/restore@', block)
        self.assertNotIn('actions/cache/save@', block)
        for old, new in (("cache-matched-key == ''", "cache-matched-key != ''"),
                         ('actions/cache/restore@', 'actions/cache/save@'),
                         ('trusted-main-go-v1-', 'development-go-v1-')):
            self.sources[full.WORKFLOW] = (workflow[:start] + block.replace(old, new) + workflow[end:]).encode()
            with self.subTest(change=old), self.assertRaises(ValueError):
                full.required_jobs(self.sources)

    def test_native_and_release_cache_writes_remain_separate(self):
        workflow = self.sources[full.WORKFLOW].decode()
        start = workflow.index('      - name: Save Go caches after integration checks pass on main\n')
        end = workflow.index('      - name: Preserve bounded Core failure context\n', start)
        block = workflow[start:end]
        self.assertIn("github.ref == 'refs/heads/main'", block)
        self.assertIn('key: ${{ steps.go-cache.outputs.cache-primary-key }}', block)
        self.assertIn('key: ${{ steps.development-go-cache.outputs.cache-primary-key }}', block)
        self.assertNotIn('key: ${{ steps.development-main-seed.', block)
        main_save = block.split('      - name: Save development Go caches', 1)[0]
        self.assertIn("steps.go-cache.outputs.cache-hit != 'true'", main_save)
        self.assertNotIn('cache-matched-key', main_save)
        self.assertNotIn('browser-go-v1-', self.sources['.github/workflows/prerelease.yml'].decode())


if __name__ == '__main__':
    unittest.main()
