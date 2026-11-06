"""Synthetic reusable caller/source contracts; no GitHub or native execution."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

SPEC = importlib.util.spec_from_file_location('native_ui_source', Path(__file__).with_name('ci-native-ui-source.py'))
source = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(source)
SHA, TREE = 'a' * 40, 'b' * 40


class NativeUISourceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.event = Path(self.temp.name) / 'event.json'
        self.event.write_text(json.dumps({'repository': {'full_name': source.REPOSITORY, 'private': False}, 'inputs': {'ci_lane_smoke': 'true'}}), encoding='utf-8')
        self.impact = SimpleNamespace(
            object_id=Mock(side_effect=lambda root, ref: SHA if ref == 'HEAD^{commit}' else TREE),
            validate_checkout=Mock(return_value={'file'}), parse_tree=Mock(return_value={'file': ()}),
            git=Mock(return_value=b'fixture'))
        self.coverage = SimpleNamespace(verified_plan=Mock(return_value={
            'scope': 'native-ui', 'head_sha': SHA, 'head_tree': TREE}))
        self.loader = patch.object(source, 'load', side_effect=lambda name: self.impact if name == 'ci-impact' else self.coverage)
        self.loader.start()
        self.addCleanup(self.loader.stop)

    def environment(self, mode):
        workflow, name = source.CALLERS[mode]
        ref = 'refs/pull/42/merge' if mode == 'scope' else 'refs/heads/fixture'
        return {'GITHUB_REPOSITORY': source.REPOSITORY, 'GITHUB_SHA': SHA,
                'GITHUB_EVENT_PATH': str(self.event), 'GITHUB_REF': ref,
                'GITHUB_WORKFLOW': name, 'GITHUB_WORKFLOW_REF': f'{source.REPOSITORY}/{workflow}@{ref}',
                'GITHUB_EVENT_NAME': 'pull_request' if mode == 'scope' else 'workflow_dispatch'}

    def verify(self, mode, **changes):
        with contextlib.redirect_stdout(io.StringIO()):
            return source.verify(mode, 'missing-plan.json', self.environment(mode) | changes)

    def test_scope_reproduces_plan_but_manual_smoke_never_reads_or_writes_one(self):
        self.assertEqual(self.verify('scope'), (SHA, TREE))
        self.coverage.verified_plan.assert_called_once_with('missing-plan.json')
        self.coverage.verified_plan.reset_mock()
        with patch.dict(os.environ, {'GITHUB_OUTPUT': str(Path(self.temp.name) / 'outputs')}):
            self.assertEqual(self.verify('smoke'), (SHA, TREE))
        self.coverage.verified_plan.assert_not_called()
        self.assertFalse((Path(self.temp.name) / 'outputs').exists())
        self.assertEqual(sorted(p.name for p in Path(self.temp.name).iterdir()), ['event.json'])
        self.impact.validate_checkout.assert_called_with(source.ROOT, SHA)

    def test_source_and_caller_mismatches_fail_in_both_modes(self):
        for mode in source.CALLERS:
            for key, value in (('GITHUB_SHA', 'c' * 40), ('GITHUB_REPOSITORY', 'fixture/other'),
                               ('GITHUB_WORKFLOW_REF', 'wrong'), ('GITHUB_WORKFLOW', 'wrong'),
                               ('GITHUB_REF', 'branch')):
                with self.subTest(mode=mode, key=key), self.assertRaises(ValueError):
                    self.verify(mode, **{key: value})
            self.impact.validate_checkout.side_effect = ValueError('dirty fixture')
            with self.assertRaises(ValueError): self.verify(mode)
            self.impact.validate_checkout.side_effect = None
            self.impact.parse_tree.return_value = {'other': ()}
            with self.assertRaises(ValueError): self.verify(mode)
            self.impact.parse_tree.return_value = {'file': ()}
        for repo in ({'full_name': source.REPOSITORY, 'private': True}, {'full_name': 'fixture/other', 'private': False}):
            self.event.write_text(json.dumps({'repository': repo}), encoding='utf-8')
            with self.assertRaises(ValueError): self.verify('smoke')

    def test_only_exact_scope_plan_qualifies_main_and_smoke_requires_manual_caller(self):
        for field, invalid in (('scope', 'full'), ('scope', 'docs'), ('head_sha', 'c' * 40), ('head_tree', 'd' * 40)):
            plan = dict(scope='native-ui', head_sha=SHA, head_tree=TREE)
            plan[field] = invalid
            self.coverage.verified_plan.return_value = plan
            with self.subTest(field=field), self.assertRaises(ValueError): self.verify('scope')
        # Manual smoke succeeds without manufacturing an eligible Git delta.
        self.assertEqual(self.verify('smoke'), (SHA, TREE))
        with self.assertRaises(ValueError): self.verify('smoke', GITHUB_EVENT_NAME='pull_request')
        with self.assertRaises(ValueError): self.verify('scope', GITHUB_EVENT_NAME='workflow_dispatch')
        event = json.loads(self.event.read_text(encoding='utf-8'))
        event['inputs']['ci_lane_smoke'] = 'false'
        self.event.write_text(json.dumps(event), encoding='utf-8')
        with self.assertRaises(ValueError): self.verify('smoke')
        with self.assertRaises(ValueError):
            source.verify('scope', None, self.environment('smoke'))
        with self.assertRaises(ValueError):
            source.verify('smoke', None, self.environment('scope'))
        with self.assertRaises(ValueError): source.verify('unknown', None, {})


if __name__ == '__main__':
    unittest.main()
