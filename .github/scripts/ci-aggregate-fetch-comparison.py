#!/usr/bin/env python3
"""Nonqualifying original/candidate transport comparison against fixed history.

Only the candidate job-log transport is injected through the original gates'
explicit callback. Historical policy hashes remain the ORIGINAL defaults.
Passing these old receipts never qualifies the candidate source or a release.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
HELPER = '.github/scripts/ci-upload-artifact.py'
HELPER_HASH = '0d79e4b753b2a68a77aca5e717635e38d108721140e2b8bdf56cf4f0c8276169'


def load_observer():
    path = ROOT / '.github/scripts/ci-aggregate-observer.py'
    spec = importlib.util.spec_from_file_location('original_aggregate_observer', path)
    module = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), module.__dict__)
    return module


def verify_candidate(observer, baseline, observation):
    observer.require(observer.git(ROOT, 'rev-parse', 'HEAD') == observation['commit'])
    observer.require(not observer.git(ROOT, 'status', '--porcelain', '--untracked-files=all'))
    observer.require(hashlib.sha256((ROOT / HELPER).read_bytes()).hexdigest() == HELPER_HASH)
    # The bounded capture implementation must remain exactly historical.
    observer.require((ROOT / '.github/scripts/ci-nearby-native.py').read_bytes()
                     == (baseline / '.github/scripts/ci-nearby-native.py').read_bytes())


def compare(observer, baseline, observation):
    original = observer.run_observation(baseline, observation)
    report = {'schema': 'aggregate-fetch-comparison-v1', 'qualifying': False,
        'observation': observation, 'target': original.get('target'),
        'original': original, 'candidateTransportSha256': HELPER_HASH,
        'transportSubstitution': 'explicit_job_log_callback',
        'receiptPolicy': 'unchanged_historical_source_defaults',
        'result': 'rejected', 'candidateProofs': {}, 'integrityFailures': [],
        'privateOutputDeleted': False, 'sourceRechecked': False,
        'candidateSourceRechecked': False, 'targetRechecked': False}
    environment = dict(os.environ)
    private_path = None
    coverage = run = None
    stage = 'source_identity'
    try:
        with observer.silence_process_output(), tempfile.TemporaryDirectory(prefix='aggregate-comparison-') as private:
            private_path = Path(private)
            observer.require(original.get('result') == 'rejected'
                and original.get('stage') == 'job_log_capture'
                and original.get('captureError') == 'escape_sequence_refused'
                and original.get('integrityFailures') == []
                and all(original.get(key) is True for key in ('planSelectionReproduced',
                    'privateOutputDeleted', 'sourceRechecked', 'targetRechecked')))
            observer.verify_source(baseline)
            verify_candidate(observer, baseline, observation)
            event_path = private_path / 'event.json'
            event_path.write_text(json.dumps(observer.original_event()))
            os.environ.clear()
            os.environ.update(observer.historical_environment(environment, event_path))
            coverage = observer.load(baseline / '.github/scripts/ci-coverage.py', 'comparison_original_coverage')
            stage = 'target_identity'
            run = coverage.api('actions/runs/' + str(observer.TARGET_RUN))
            observer.require(run.get('id') == observer.TARGET_RUN and run.get('run_attempt') == observer.TARGET_ATTEMPT
                and run.get('head_sha') == observer.TARGET_HEAD and run.get('event') == 'pull_request'
                and run.get('path') == '.github/workflows/ci.yml' and run.get('status') == 'completed'
                and run.get('conclusion') == 'failure')
            stage = 'jobs_validation'
            observer.require(coverage.evaluate_jobs(coverage.current_jobs(), 'full') is True)
            candidate = observer.load(ROOT / HELPER, 'comparison_candidate_transport')
            dnssd = observer.load(baseline / '.github/scripts/ci-dnssd-required.py', 'comparison_original_dnssd')
            core = observer.load(baseline / '.github/scripts/ci-native-core-required.py', 'comparison_original_core')
            stage = 'receipt_validation'
            # Explicit transport-only substitution. Never pass policy_hashes,
            # modify a gate, print a proof, or write an official receipt.
            dnssd.audit_artifact(coverage.api, run, observer.TARGET_COMMIT, observer.TARGET_TREE,
                                 job_log=candidate.download_job_log)
            report['candidateProofs']['macos_discovery'] = True
            for platform, label in (('windows-amd64', 'windows_package'), ('darwin-arm64', 'macos_package')):
                core.audit_artifact(coverage.api, run, observer.TARGET_COMMIT, observer.TARGET_TREE, platform,
                                    job_log=candidate.download_job_log)
                report['candidateProofs'][label] = True
            observer.require(set(report['candidateProofs']) == {'macos_discovery', 'windows_package', 'macos_package'})
            report.update(result='candidate_transport_observed_success_not_qualification', stage='complete', **{'class': 'none'})
    except Exception as error:
        report.update(observer.closed_failure(error, baseline, stage))
    finally:
        os.environ.clear(); os.environ.update(environment)
        report['privateOutputDeleted'] = private_path is None or not private_path.exists()
        for label, operation in (
                ('sourceRechecked', lambda: observer.verify_source(baseline)),
                ('candidateSourceRechecked', lambda: verify_candidate(observer, baseline, observation))):
            try:
                with observer.silence_process_output(): operation()
                report[label] = True
            except Exception:
                report['integrityFailures'].append(label)
        if coverage is not None and run is not None:
            try:
                with observer.silence_process_output():
                    after = coverage.api('actions/runs/' + str(observer.TARGET_RUN))
                    observer.require(all(after.get(key) == run.get(key) for key in
                        ('id', 'run_attempt', 'head_sha', 'event', 'path', 'status', 'conclusion', 'pull_requests')))
                report['targetRechecked'] = True
            except Exception:
                report['integrityFailures'].append('target_identity')
        if not report['privateOutputDeleted']: report['integrityFailures'].append('cleanup')
        if report['integrityFailures']: report['result'] = 'rejected'
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--baseline', required=True)
    args = parser.parse_args()
    try:
        observer = load_observer()
        observation = observer.observer_identity(os.environ)
        report = compare(observer, Path(args.baseline).resolve(), observation)
    except Exception:
        report = {'schema': 'aggregate-fetch-comparison-v1', 'qualifying': False,
                  'result': 'rejected', 'stage': 'observer_identity', 'class': 'validation_failed'}
    print(json.dumps(report, sort_keys=True, separators=(',', ':')))
    return 0 if report['result'] == 'candidate_transport_observed_success_not_qualification' else 1


if __name__ == '__main__':
    sys.dont_write_bytecode = True
    raise SystemExit(main())
