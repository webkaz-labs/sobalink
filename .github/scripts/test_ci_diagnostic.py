"""Closed selection and source parity only; never launch an acceptance runner."""
import importlib.util
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('ci_diagnostic', Path(__file__).with_name('ci-diagnostic.py'))
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


class DiagnosticPolicyTests(unittest.TestCase):
    def args(self, suite='web'):
        sha = 'a' * 40
        ref = 'diagnostic/' + suite + '/' + sha[:12] + '-attempt1'
        return dict(event_name='create', event={'ref_type': 'branch', 'ref': ref,
                    'repository': {'full_name': gate.REPOSITORY, 'private': False}},
                    repository=gate.REPOSITORY, ref='refs/heads/' + ref,
                    sha=sha, checkout_sha=sha)

    def test_exact_closed_suites(self):
        for suite in ('web', 'product', 'activation', 'nearby', 'text', 'dnssd', 'outbox-accept'):
            self.assertEqual(gate.select(**self.args(suite)), suite)

    def test_rejects_other_events_refs_and_heads(self):
        for change in ({'event_name': 'push'}, {'event_name': 'pull_request'},
                       {'repository': 'other/project'}, {'ref': 'refs/heads/main'},
                       {'ref': self.args()['ref'].replace('/web/', '/all/')},
                       {'ref': self.args()['ref'] + '\n'},
                       {'ref': self.args()['ref'].replace('attempt1', 'a/b')},
                       {'ref': self.args()['ref'].replace('a' * 12, 'b' * 12)},
                       {'sha': 'A' * 40}, {'checkout_sha': 'b' * 40}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                gate.select(**dict(self.args(), **change))
        for change in ({'ref_type': 'tag'}, {'ref': 'diagnostic/web/other'},
                       {'repository': {'full_name': gate.REPOSITORY, 'private': True}},
                       {'repository': {'full_name': gate.REPOSITORY}}):
            args = self.args()
            args['event'].update(change)
            with self.subTest(change=change), self.assertRaises(ValueError):
                gate.select(**args)

    def workflow(self, name):
        return (ROOT / '.github/workflows' / (name + '.yml')).read_text()

    def body(self, workflow, job):
        match = re.search(r'^  ' + re.escape(job) + r':\n.*?(?=^  [a-z][a-z0-9-]*:\n|\Z)', workflow, re.M | re.S)
        self.assertIsNotNone(match)
        text = match.group()
        return text[text.index('    runs-on:'):].strip()

    def test_diagnostics_retain_entire_original_jobs(self):
        original = self.workflow('ci')
        diagnostic = self.workflow('activation-diagnostic')
        for job in ('web-activation', 'product-activation'):
            expected = self.body(original, job).replace('name: soba-' + job + '-sanitized',
                                                       'name: diagnostic-soba-' + job + '-sanitized')
            self.assertEqual(self.body(diagnostic, 'diagnostic-' + job), expected)

    def test_diagnostic_success_cannot_satisfy_full_coverage(self):
        spec = importlib.util.spec_from_file_location('ci_coverage', ROOT / '.github/scripts/ci-coverage.py')
        coverage = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(coverage)
        jobs = [dict(name=name, status='completed', conclusion='success', steps=[])
                for name in ('diagnostic-selection', 'diagnostic-web-activation',
                             'diagnostic-product-activation', 'diagnostic-nearby', 'diagnostic-nearby-native', 'diagnostic-text', 'diagnostic-dnssd', 'diagnostic-outbox-accept')]
        for scope in coverage.SCOPES:
            with self.subTest(scope=scope), self.assertRaises(ValueError):
                coverage.evaluate_jobs(jobs, scope)

    def test_only_create_and_distinct_jobs_without_release_credit(self):
        text = self.workflow('activation-diagnostic')
        trigger = text.split('on:\n', 1)[1].split('\npermissions:', 1)[0]
        self.assertEqual(trigger.strip(), 'create:')
        self.assertEqual(re.findall(r'^  ([a-z][a-z0-9-]*):$', text.split('\njobs:\n')[1], re.M),
                         ['diagnostic-selection', 'diagnostic-outbox-accept', 'diagnostic-product-activation', 'diagnostic-web-activation', 'diagnostic-nearby', 'diagnostic-nearby-native', 'diagnostic-text', 'diagnostic-dnssd'])
        for forbidden in ('continue-on-error:', 'actions/cache', 'secrets.', 'workflow_dispatch:',
                          'ci-coverage.py', 'ci-required:', 'package-tool', 'id-token: write', 'contents: write'):
            self.assertNotIn(forbidden, text)
        self.assertIn("github.repository == 'webkaz-labs/sobalink'", text)
        self.assertIn("github.event.ref_type == 'branch'", text)
        self.assertIn("startsWith(github.ref, 'refs/heads/diagnostic/')", text)
        for suite in ('web', 'product'):
            self.assertIn("if: needs.diagnostic-selection.outputs.suite == '" + suite +
                          "' || needs.diagnostic-selection.outputs.suite == 'activation'", text)
        self.assertIn("if: needs.diagnostic-selection.outputs.suite == 'nearby'", text)
        self.assertIn("if: needs.diagnostic-selection.outputs.suite == 'text'", text)
        self.assertIn('cancel-in-progress: false', text)
        self.assertIn('not release validation', text)
        self.assertIn('push:\n    branches: [main]', self.workflow('ci'))



    def test_outbox_accept_preserves_exact_original_command(self):
        diagnostic = self.body(self.workflow('activation-diagnostic'), 'diagnostic-outbox-accept')
        original = self.workflow('ci')
        command = next(line.strip() for line in original.splitlines()
                       if 'ci-metrics.py run --suite outbox-process-restart ' in line)
        self.assertEqual(diagnostic.count(command), 1)
        self.assertIn('runs-on: windows-2025', diagnostic)
        for check in ('test "$(go env GOVERSION)" = go1.27.1',
                      'test "$(go env GOOS)" = windows',
                      'test "$(go env GOARCH)" = amd64'):
            self.assertIn(check, diagnostic)
        self.assertNotIn('matrix:', diagnostic)
        self.assertIn('timeout-minutes: 6', diagnostic)
        self.assertIn("SOBALINK_RUN_DIRECT_INTEGRATION: '1'", diagnostic)
        self.assertIn('go test -race -c -tags=lanlink_integration,directlan_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./internal/core -o "$RUNNER_TEMP/outbox-accept-core.test.exe"', diagnostic)
        self.assertIn('test "$(git rev-parse HEAD)" = "$GITHUB_SHA"', diagnostic)
        self.assertIn('ref: ${{ github.sha }}', diagnostic)
        self.assertIn("if: needs.diagnostic-selection.outputs.suite == 'outbox-accept'", self.workflow('activation-diagnostic'))
        self.assertIn('name: diagnostic-outbox-accept-metrics', diagnostic)
        self.assertNotIn('rerun', diagnostic)
        focused = diagnostic.split('name: Verify exact durable acceptance contract regressions', 1)[1].split('name: Observe one original', 1)[0]
        self.assertIn('timeout-minutes: 4', focused)
        self.assertIn('ci-go-test.py --exact', focused)
        expected = re.findall(r'--expect (github.com/webkaz-labs/sobalink/[^ :]+):(Test[^ \\]+)', focused)
        self.assertEqual(len(expected), 48)
        self.assertEqual(len(set(expected)), 48)
        self.assertEqual(sum('/' not in test for _, test in expected), 15)
        self.assertEqual({pkg for pkg, _ in expected}, {'github.com/webkaz-labs/sobalink/internal/transfer', 'github.com/webkaz-labs/sobalink/internal/core', 'github.com/webkaz-labs/sobalink/cmd/soba'})
        self.assertIn('go test -race -count=1 -timeout=3m', focused)
        self.assertIn('run --suite outbox-accept-contract', focused)
        self.assertNotIn('SOBALINK_RUN_DIRECT_INTEGRATION', focused)


if __name__ == '__main__':
    unittest.main()
