"""Cache reuse must not cross trust, target, dependency or validation boundaries."""
import copy
import hashlib
import importlib.util
import os
from pathlib import Path
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
        self.assertNotIn('development-main-seed', block)
        self.assertNotIn('browser-go-v1-', self.sources['.github/workflows/prerelease.yml'].decode())


if __name__ == '__main__':
    unittest.main()
