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
        for suite in ('web', 'product', 'activation', 'outbox-windows'):
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

    def test_windows_outbox_keeps_the_exact_case_and_timeout(self):
        original = self.workflow('ci')
        diagnostic = self.workflow('activation-diagnostic')
        windows = self.body(diagnostic, 'diagnostic-outbox-windows')
        production_case = next(line.strip() for line in original.splitlines()
                               if '--suite outbox-process-restart -- ' in line)
        exact_case = production_case.split('--suite outbox-process-restart -- ', 1)[1]
        self.assertIn(exact_case, windows)
        self.assertIn('runs-on: windows-2025', windows)
        self.assertNotIn('matrix:', windows)
        self.assertIn('timeout-minutes: 6', windows)
        self.assertIn('SOBALINK_RUN_DIRECT_INTEGRATION: \'1\'', windows)
        self.assertIn('go test -c -race', windows)
        for name in ('TestOutboxDiagnosticTokensExcludeFreeFormValues', 'TestOutboxProcessPhaseDistinguishesOwnershipAndNamespace', 'TestOutboxProcessPhaseDistinguishesGlobalQueueAndRun', 'TestOutboxProcessPhaseReportsBusyLocksWithoutWaiting', 'TestOutboxProcessPhaseHistoryCoverageIsExplicit', 'TestOutboxObserverPreservesGateAndBusyDecisions', 'TestOutboxObserverRecordsPlanningAndRunAdmissionRefusal', 'TestOutboxObserverRecordsFinishOwnerAndPersistenceOutcome', 'TestOutboxObserverInstalledAtConstruction', 'TestOutboxNilObserverRetainsGateDecision', 'TestOutboxObserverRecordsPumpPersistenceRefusal', 'TestOutboxObserverPreservesGateAndBusyDecisions/busy', 'TestOutboxObserverPreservesGateAndBusyDecisions/owner-refused', 'TestOutboxObserverPreservesGateAndBusyDecisions/profile', 'TestOutboxObserverPreservesGateAndBusyDecisions/namespace', 'TestOutboxObserverPreservesGateAndBusyDecisions/clock', 'TestOutboxObserverPreservesGateAndBusyDecisions/context', 'TestOutboxObserverRecordsFinishOwnerAndPersistenceOutcome/busy', 'TestOutboxObserverRecordsFinishOwnerAndPersistenceOutcome/saved', 'TestOutboxObserverRecordsFinishOwnerAndPersistenceOutcome/save-failed'):
            self.assertIn('--expect github.com/webkaz-labs/sobalink/internal/core:' + name, windows)
        self.assertIn("if: needs.diagnostic-selection.outputs.suite == 'outbox-windows'", diagnostic)

    def test_diagnostic_success_cannot_satisfy_full_coverage(self):
        spec = importlib.util.spec_from_file_location('ci_coverage', ROOT / '.github/scripts/ci-coverage.py')
        coverage = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(coverage)
        jobs = [dict(name=name, status='completed', conclusion='success', steps=[])
                for name in ('diagnostic-selection', 'diagnostic-web-activation',
                             'diagnostic-product-activation', 'diagnostic-outbox-windows')]
        for scope in coverage.SCOPES:
            with self.subTest(scope=scope), self.assertRaises(ValueError):
                coverage.evaluate_jobs(jobs, scope)

    def test_only_create_and_distinct_jobs_without_release_credit(self):
        text = self.workflow('activation-diagnostic')
        trigger = text.split('on:\n', 1)[1].split('\npermissions:', 1)[0]
        self.assertEqual(trigger.strip(), 'create:')
        self.assertEqual(re.findall(r'^  ([a-z][a-z0-9-]*):$', text.split('\njobs:\n')[1], re.M),
                         ['diagnostic-selection', 'diagnostic-product-activation', 'diagnostic-web-activation', 'diagnostic-outbox-windows'])
        for forbidden in ('continue-on-error:', 'actions/cache', 'secrets.', 'workflow_dispatch:',
                          'ci-coverage.py', 'ci-required:', 'package-tool', 'id-token: write', 'contents: write'):
            self.assertNotIn(forbidden, text)
        self.assertIn("github.repository == 'webkaz-labs/sobalink'", text)
        self.assertIn("github.event.ref_type == 'branch'", text)
        self.assertIn("startsWith(github.ref, 'refs/heads/diagnostic/')", text)
        for suite in ('web', 'product'):
            self.assertIn("if: needs.diagnostic-selection.outputs.suite == '" + suite +
                          "' || needs.diagnostic-selection.outputs.suite == 'activation'", text)
        self.assertIn('cancel-in-progress: false', text)
        self.assertIn('not release validation', text)
        self.assertIn('push:\n    branches: [main]', self.workflow('ci'))


if __name__ == '__main__':
    unittest.main()
