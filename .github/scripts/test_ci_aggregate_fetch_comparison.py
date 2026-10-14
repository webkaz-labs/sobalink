"""Invented nonqualifying comparison fixtures; never hosted proof."""
import contextlib
import importlib.util
import json
import os
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

PATH = Path(__file__).with_name('ci-aggregate-fetch-comparison.py')
SPEC = importlib.util.spec_from_file_location('aggregate_comparison', PATH)
comparison = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(comparison)


class ComparisonTests(unittest.TestCase):
    def fixture(self):
        observer = comparison.load_observer()
        original = {'result':'rejected', 'stage':'job_log_capture', 'captureError':'escape_sequence_refused',
            'integrityFailures':[], 'planSelectionReproduced':True, 'privateOutputDeleted':True,
            'sourceRechecked':True, 'targetRechecked':True, 'qualifying':False,
            'target':{'run':observer.TARGET_RUN,'attempt':observer.TARGET_ATTEMPT}}
        run = {'id':observer.TARGET_RUN,'run_attempt':observer.TARGET_ATTEMPT,
            'head_sha':observer.TARGET_HEAD,'event':'pull_request','path':'.github/workflows/ci.yml',
            'status':'completed','conclusion':'failure'}
        coverage = Mock()
        coverage.api.return_value = run
        coverage.evaluate_jobs.return_value = True
        candidate = Mock()
        dnssd, core = Mock(), Mock()
        def audit(*args, **kwargs):
            self.assertEqual(set(kwargs), {'job_log'})
            self.assertIs(kwargs['job_log'], candidate.download_job_log)
            self.assertEqual(args[2:4], (observer.TARGET_COMMIT, observer.TARGET_TREE))
            print('synthetic-secret')
            os.write(2, b'synthetic-secret\n')
            return {'synthetic-private-proof':'synthetic-secret'}
        dnssd.audit_artifact.side_effect = audit
        core.audit_artifact.side_effect = audit
        stack = contextlib.ExitStack()
        stack.enter_context(patch.object(observer, 'run_observation', return_value=original))
        source = stack.enter_context(patch.object(observer, 'verify_source'))
        candidate_source = stack.enter_context(patch.object(comparison, 'verify_candidate'))
        loader = stack.enter_context(patch.object(observer, 'load', side_effect=[coverage,candidate,dnssd,core]))
        self.addCleanup(stack.close)
        return observer, original, coverage, dnssd, core, source, candidate_source, loader

    def run_comparison(self, observer):
        return comparison.compare(observer, Path('/synthetic/baseline'), {'run':20,'attempt':1,'commit':'a'*40})

    def test_three_original_gates_use_only_explicit_candidate_transport(self):
        observer, _, coverage, dnssd, core, _, _, _ = self.fixture()
        environment = dict(os.environ)
        report = self.run_comparison(observer)
        self.assertEqual(os.environ, environment)
        self.assertEqual(report['result'], 'candidate_transport_observed_success_not_qualification')
        self.assertEqual(report['candidateProofs'], {'macos_discovery':True,'windows_package':True,'macos_package':True})
        self.assertFalse(report['qualifying'])
        self.assertEqual(report['transportSubstitution'], 'explicit_job_log_callback')
        self.assertEqual(report['receiptPolicy'], 'unchanged_historical_source_defaults')
        self.assertEqual(dnssd.audit_artifact.call_count, 1)
        self.assertEqual(core.audit_artifact.call_count, 2)
        self.assertEqual([x.args[4] for x in core.audit_artifact.call_args_list], ['windows-amd64','darwin-arm64'])
        coverage.finalize.assert_not_called()
        for key in ('sourceRechecked','candidateSourceRechecked','targetRechecked','privateOutputDeleted'):
            self.assertTrue(report[key])
        self.assertNotIn('synthetic-secret', json.dumps(report))
        self.assertNotIn('schema_version', report)

    def test_unconfirmed_original_failure_prevents_candidate_execution(self):
        observer, original, _, dnssd, core, _, _, loader = self.fixture()
        original['captureError'] = 'unknown'
        report = self.run_comparison(observer)
        self.assertEqual(report['result'], 'rejected')
        self.assertEqual(report['candidateProofs'], {})
        loader.assert_not_called(); dnssd.audit_artifact.assert_not_called(); core.audit_artifact.assert_not_called()

    def test_gate_failure_stays_rejected_and_rechecks_integrity(self):
        observer, _, _, _, core, _, _, _ = self.fixture()
        core.audit_artifact.side_effect = ValueError('synthetic-secret')
        report = self.run_comparison(observer)
        self.assertEqual(report['result'], 'rejected')
        self.assertEqual(report['candidateProofs'], {'macos_discovery':True})
        self.assertTrue(report['sourceRechecked']); self.assertTrue(report['targetRechecked'])
        self.assertTrue(report['privateOutputDeleted']); self.assertFalse(report['qualifying'])
        self.assertNotIn('synthetic-secret', json.dumps(report))

    def test_source_change_after_success_forces_rejection(self):
        observer, _, _, _, _, source, _, _ = self.fixture()
        source.side_effect = [None, ValueError('synthetic-secret')]
        report = self.run_comparison(observer)
        self.assertEqual(report['result'], 'rejected')
        self.assertEqual(report['integrityFailures'], ['sourceRechecked'])
        self.assertFalse(report['qualifying'])

    def test_candidate_source_requires_exact_helper_and_capture(self):
        observer = comparison.load_observer()
        baseline = Path('/synthetic/baseline')
        with patch.object(observer, 'git', side_effect=['b'*40]), self.assertRaises(ValueError):
            comparison.verify_candidate(observer, baseline, {'commit':'a'*40})
        with patch.object(observer, 'git', side_effect=['a'*40,'']), patch.object(Path,'read_bytes',return_value=b'synthetic-secret'):
            with self.assertRaises(ValueError): comparison.verify_candidate(observer, baseline, {'commit':'a'*40})


if __name__ == '__main__':
    unittest.main()
