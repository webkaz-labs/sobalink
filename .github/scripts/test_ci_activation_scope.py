"""Offline job-condition contracts; never launch either acceptance suite."""
import ast
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]
JOBS = ('product-activation', 'web-activation')
REPOSITORY = 'webkaz-labs/sobalink'


class ActivationScopeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        workflow = (ROOT / '.github/workflows/ci.yml').read_text()
        cls.jobs = {
            name: re.split(r'\n  [a-z][a-z-]*:', workflow.split('  ' + name + ':\n', 1)[1])[0]
            for name in JOBS
        }

    def enabled(self, name, *, cancelled=False, **overrides):
        values = {
            'github.event.repository.private': False,
            'github.event_name': 'pull_request',
            'github.event.pull_request.head.repo.full_name': REPOSITORY,
            'github.repository': REPOSITORY,
            'github.ref': 'refs/heads/main',
            'github.event.repository.default_branch': 'main',
            'inputs.force_full': False,
            'needs.impact.result': 'success',
            'needs.impact.outputs.scope': 'full',
        } | overrides
        condition = re.search(r'^    if: (.+)$', self.jobs[name], re.M).group(1)
        # An explicit status function prevents GitHub's implicit success()
        # from skipping acceptance after a failed impact job.
        self.assertIn('always()', condition)
        self.assertIn('!cancelled()', condition)
        for key, value in sorted(values.items(), key=lambda item: -len(item[0])):
            condition = condition.replace(key, repr(value))
        condition = condition.replace('always()', 'True').replace('cancelled()', repr(cancelled))
        condition = condition.replace('false', 'False').replace('&&', ' and ').replace('||', ' or ')
        expression = ast.parse(re.sub(r'!(?!=)', ' not ', condition).strip(), mode='eval')
        allowed = (ast.Expression, ast.BoolOp, ast.UnaryOp, ast.Compare, ast.Constant,
                   ast.And, ast.Or, ast.Not, ast.Eq, ast.NotEq)
        self.assertTrue(all(isinstance(node, allowed) for node in ast.walk(expression)))
        return eval(compile(expression, '<activation condition>', 'eval'), {'__builtins__': {}})

    def test_only_verified_docs_or_native_ui_prs_skip_both_suites(self):
        for name in JOBS:
            self.assertEqual(re.findall(r'^    needs: (.+)$', self.jobs[name], re.M), ['impact'])
            for scope in ('docs', 'native-ui', 'go', 'frontend', 'native-short', 'full', '', None, 'unrecognized'):
                for result in ('success', 'failure', 'cancelled', 'skipped', ''):
                    with self.subTest(job=name, scope=scope, impact=result):
                        self.assertEqual(self.enabled(name, **{
                            'needs.impact.outputs.scope': scope, 'needs.impact.result': result,
                        }), not (scope in ('docs', 'native-ui') and result == 'success'))

    def test_forced_and_exact_schedule_runs_ignore_pr_scope_optimization(self):
        for name in JOBS:
            for scope in ('docs', 'native-ui', 'go', 'frontend', 'native-short', 'full', '', None, 'unrecognized'):
                for result in ('success', 'failure', 'cancelled', 'skipped', ''):
                    values = {'needs.impact.outputs.scope': scope, 'needs.impact.result': result}
                    with self.subTest(job=name, scope=scope, impact=result):
                        self.assertTrue(self.enabled(name, **values, **{
                            'github.event_name': 'workflow_dispatch', 'inputs.force_full': True,
                        }))
                        self.assertFalse(self.enabled(name, **values, **{
                            'github.event_name': 'workflow_dispatch', 'inputs.force_full': False,
                        }))
                        self.assertTrue(self.enabled(name, **values, **{'github.event_name': 'schedule'}))

    def test_existing_event_and_repository_boundaries_remain(self):
        for name in JOBS:
            for event in ('pull_request', 'workflow_dispatch', 'schedule'):
                values = {'github.event_name': event, 'inputs.force_full': True}
                with self.subTest(job=name, event=event):
                    self.assertFalse(self.enabled(name, **values, **{'github.event.repository.private': True}))
                    self.assertFalse(self.enabled(name, cancelled=True, **values))
            self.assertFalse(self.enabled(name, **{'github.event.pull_request.head.repo.full_name': 'example/fork'}))
            for event in ('push', 'pull_request_target', 'unknown', ''):
                self.assertFalse(self.enabled(name, **{'github.event_name': event}))
            for key, value in (('github.repository', 'example/fork'),
                               ('github.ref', 'refs/heads/topic'),
                               ('github.event.repository.default_branch', 'other')):
                with self.subTest(job=name, boundary=key):
                    self.assertFalse(self.enabled(name, **{'github.event_name': 'schedule', key: value}))

    def test_policy_helpers_remain_when_shared_policy_job_is_not_selected(self):
        # The shared policy job skips frontend and scoped Go, but these suites
        # still run there. Retain their Python helpers and Node diagnostics.
        for name, diagnostics in (('product-activation', 'product-activation'),
                                  ('web-activation', 'activation')):
            with self.subTest(job=name):
                module = name.replace('-', '_')
                self.assertIn(f"python -m unittest discover -s .github/scripts -p 'test_ci_{module}.py' -v", self.jobs[name])
                self.assertIn(f'node --test web/browser/{diagnostics}-diagnostics.test.mjs', self.jobs[name])


if __name__ == '__main__':
    unittest.main()
