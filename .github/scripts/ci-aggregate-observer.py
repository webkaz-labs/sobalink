#!/usr/bin/env python3
"""One fixed historical aggregate observation. NEVER qualification or release proof.

The observer lives outside the historical checkout. Original validators, default
policy hashes, API transports and finalize code execute without modification.
Any original coverage receipt is private, unpublished, and deleted on exit.
"""
import argparse
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import zipfile

REPOSITORY = 'webkaz-labs/sobalink'
TARGET_RUN = 38518837073
TARGET_ATTEMPT = 2
TARGET_HEAD = '86305067fcb45131b8df3683c2152ae1984f32b2'
TARGET_COMMIT = 'e3f6fd4adf74d9306f55d40b75ed96f2dd532d51'
TARGET_TREE = '0cd78d6cbbf4ba70b49873bccd0e973cd69bcb7c'
TARGET_BASE = 'e4f7f4ef0a38a032eef9776c5e4a5d0ea8cbf28f'
PLAN_ID = 11873410337
PLAN_SIZE = 3185
PLAN_DIGEST = '78ed7b2a9b985d6a0a2883071910031dde3f7634ca43a10ac960a8bde71f82f5'
MODULES = frozenset(('ci-coverage.py', 'ci-dnssd-required.py', 'ci-native-core-required.py',
    'ci-upload-artifact.py', 'ci-nearby-native.py', 'ci-nearby-diagnostic.py',
    'ci-dnssd-diagnostic.py', 'ci-dnssd-terminal.py', 'ci-native-core.py', 'ci-windows-packages.py'))
FUNCTIONS = frozenset(('require', 'finalize', 'verified_plan', 'read_plan', 'classify', 'head', 'git', 'digest',
    'api', 'current_jobs', 'evaluate_jobs', 'successful_step', 'audit_macos_discovery', 'audit_native_packages',
    'audit_artifact', 'native_job', 'pages', 'timestamp', 'download_job_log', 'validate_job_upload',
    'download_artifact', 'artifact_receipt', 'validate_receipt', 'validate_core', 'capture', 'parse_json',
    'module', '<dictcomp>', '<listcomp>', '<genexpr>'))
STAGES = frozenset(('observer_identity', 'source_identity', 'target_identity', 'plan_metadata', 'plan_download',
    'plan_validation', 'selection', 'jobs_fetch', 'jobs_validation', 'run_fetch', 'artifact_inventory',
    'artifact_metadata', 'job_log_capture', 'upload_validation', 'artifact_zip_capture', 'archive_validation',
    'source_policy', 'receipt_validation', 'final_readback', 'finalize_other', 'cleanup', 'complete', 'unknown'))


def require(ok):
    if not ok:
        raise ValueError('observation rejected')


def load(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), module.__dict__)
    return module


def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args], stderr=subprocess.DEVNULL, timeout=20).decode().strip()


def verify_source(root):
    require(git(root, 'rev-parse', 'HEAD') == TARGET_COMMIT)
    require(git(root, 'rev-parse', 'HEAD^{tree}') == TARGET_TREE)
    require(git(root, 'rev-list', '--parents', '-n', '1', 'HEAD').split() == [TARGET_COMMIT, TARGET_BASE, TARGET_HEAD])
    require(not git(root, 'status', '--porcelain', '--untracked-files=all'))
    require(not root.is_relative_to(Path(__file__).resolve().parent))
    require(not Path(__file__).resolve().is_relative_to(root))


def observer_identity(env):
    require(env.get('GITHUB_REPOSITORY') == REPOSITORY and env.get('GITHUB_EVENT_NAME') == 'create')
    require(re.fullmatch(r'refs/heads/aggregate-observer/[a-z0-9-]{1,100}', env.get('GITHUB_REF', '')))
    require(re.fullmatch(r'[0-9a-f]{40}', env.get('GITHUB_SHA', '')))
    require(env.get('GITHUB_RUN_ID', '').isdecimal() and env.get('GITHUB_RUN_ATTEMPT', '').isdecimal())
    require(int(env['GITHUB_RUN_ID']) > 0 and int(env['GITHUB_RUN_ID']) != TARGET_RUN and int(env['GITHUB_RUN_ATTEMPT']) > 0)
    event = json.loads(Path(env['GITHUB_EVENT_PATH']).read_text())
    require(event.get('repository', {}).get('full_name') == REPOSITORY
        and event.get('repository', {}).get('private') is False
        and event.get('ref_type') == 'branch'
        and 'refs/heads/' + event.get('ref', '') == env['GITHUB_REF'])
    require(git(Path(__file__).resolve().parents[2], 'rev-parse', 'HEAD') == env['GITHUB_SHA'])
    return {'run': int(env['GITHUB_RUN_ID']), 'attempt': int(env['GITHUB_RUN_ATTEMPT']), 'commit': env['GITHUB_SHA']}


def historical_environment(env, event_path):
    result = dict(env)
    result.update(GITHUB_REPOSITORY=REPOSITORY, GITHUB_RUN_ID=str(TARGET_RUN), GITHUB_RUN_ATTEMPT=str(TARGET_ATTEMPT),
        GITHUB_SHA=TARGET_COMMIT, GITHUB_EVENT_NAME='pull_request', GITHUB_REF='refs/pull/51/merge',
        GITHUB_EVENT_PATH=str(event_path), FORCE_FULL='false', PYTHONDONTWRITEBYTECODE='1')
    for key in ('GITHUB_OUTPUT', 'GITHUB_STEP_SUMMARY', 'GITHUB_ENV', 'GITHUB_PATH', 'GITHUB_STATE'):
        result.pop(key, None)
    return result


def original_event():
    # Historical event fields are reconstructed only from fixed reviewed Git
    # parents and run identity. No user/account/runner metadata is included.
    return {'number': 51, 'repository': {'full_name': REPOSITORY}, 'pull_request': {
        'base': {'ref': 'main', 'sha': TARGET_BASE, 'repo': {'full_name': REPOSITORY}},
        'head': {'sha': TARGET_HEAD}, 'merge_commit_sha': TARGET_COMMIT}}


def error_class(error):
    if isinstance(error, json.JSONDecodeError): return 'json_invalid'
    if isinstance(error, subprocess.TimeoutExpired): return 'timeout'
    if isinstance(error, subprocess.CalledProcessError): return 'nonzero'
    if isinstance(error, subprocess.SubprocessError): return 'subprocess_failed'
    if isinstance(error, OSError): return 'io_failed'
    if isinstance(error, (KeyError, TypeError)): return 'shape_invalid'
    if isinstance(error, ValueError): return 'validation_failed'
    return 'unknown'


def closed_failure(error, root, fallback):
    frames = []
    tb = error.__traceback__
    while tb:
        code = tb.tb_frame.f_code
        path = Path(code.co_filename)
        if path.parent == root / '.github/scripts' and path.name in MODULES and code.co_name in FUNCTIONS:
            if type(tb.tb_lineno) is int and 0 < tb.tb_lineno < 10000:
                frames.append({'module': path.name, 'function': code.co_name, 'line': tb.tb_lineno})
        tb = tb.tb_next
    stage = fallback if fallback in STAGES else 'unknown'
    # Only fixed source/function names and numeric locations are inspected.
    # No exception messages, args, locals, return values, or source text escape.
    for row in frames:
        function, module, line = row['function'], row['module'], row['line']
        if function in ('verified_plan', 'read_plan', 'classify'): stage = 'selection'
        elif function == 'current_jobs': stage = 'jobs_fetch'
        elif function in ('evaluate_jobs', 'successful_step'): stage = 'jobs_validation'
        elif function in ('head', 'git', 'digest'): stage = 'source_policy'
        elif function == 'download_job_log': stage = 'job_log_capture'
        elif function == 'validate_job_upload': stage = 'upload_validation'
        elif function == 'download_artifact': stage = 'artifact_zip_capture'
        elif function == 'artifact_receipt': stage = 'archive_validation'
        elif function in ('validate_receipt', 'validate_core'): stage = 'receipt_validation'
        elif function in ('audit_macos_discovery', 'audit_native_packages'): stage = 'run_fetch'
        elif function == 'audit_artifact':
            mac = module == 'ci-dnssd-required.py'
            if line >= (402 if mac else 629): stage = 'final_readback'
            elif line >= (399 if mac else 626): stage = 'source_policy'
            elif (386 if mac else 613) <= line <= (392 if mac else 619): stage = 'artifact_metadata'
            else: stage = 'artifact_inventory'
    return {'stage': stage, 'class': error_class(error), 'frames': frames[-12:]}


CAPTURE_STATES = frozenset(('completed', 'nonzero', 'timeout', 'output_limit',
    'capture_failed', 'launch_failed'))


class CaptureObserver:
    """Read only the closed status field from the verified capture return.

    Never inspect the remaining tuple elements, locals, command, or buffers.
    The callback cannot change the original return or validator decision.
    """
    def __init__(self, root):
        scripts = root / '.github/scripts'
        self.capture_path = str(scripts / 'ci-nearby-native.py')
        self.fetches = {(str(scripts / 'ci-upload-artifact.py'), 'download_job_log'): 'job_log_capture',
                        (str(scripts / 'ci-dnssd-required.py'), 'download_artifact'): 'artifact_zip_capture'}
        self.active = None
        self.last = None

    def observe(self, frame, event, value):
        name = (frame.f_code.co_filename, frame.f_code.co_name)
        if name in self.fetches:
            if event == 'call':
                self.active = self.fetches[name]
                self.last = None
            elif event == 'return':
                self.active = None
        elif event == 'return' and name == (self.capture_path, 'capture') and self.active is not None:
            status = 'unknown'
            if type(value) is tuple and len(value) == 4 and type(value[0]) is str and value[0] in CAPTURE_STATES:
                status = value[0]
            self.last = {'fetch': self.active, 'status': status}

    @contextlib.contextmanager
    def enabled(self):
        require(sys.getprofile() is None)
        sys.setprofile(self.observe)
        try:
            yield
        finally:
            sys.setprofile(None)


@contextlib.contextmanager
def silence_process_output():
    # Suppress both Python output and inherited child-process file descriptors.
    sys.stdout.flush(); sys.stderr.flush()
    saved = (os.dup(1), os.dup(2))
    try:
        with open(os.devnull, 'w') as sink:
            os.dup2(sink.fileno(), 1); os.dup2(sink.fileno(), 2)
            with contextlib.redirect_stdout(sink), contextlib.redirect_stderr(sink):
                yield
    finally:
        os.dup2(saved[0], 1); os.dup2(saved[1], 2)
        os.close(saved[0]); os.close(saved[1])


def run_observation(root, observation):
    report = {'schema': 'aggregate-observation-v1', 'qualifying': False,
        'observation': observation, 'target': {'run': TARGET_RUN, 'attempt': TARGET_ATTEMPT,
        'head': TARGET_HEAD, 'commit': TARGET_COMMIT, 'tree': TARGET_TREE},
        'result': 'rejected', 'stage': 'source_identity', 'class': 'unknown', 'frames': [],
        'eventReconstructedFromVerifiedGitAndRun': True, 'privateOutputDeleted': False, 'sourceRechecked': False,
        'targetRechecked': False, 'planSelectionReproduced': False, 'integrityFailures': []}
    stage = 'source_identity'
    original_env = dict(os.environ)
    private_path = None
    coverage = run = None
    capture_observer = CaptureObserver(root)
    try:
        with silence_process_output(), tempfile.TemporaryDirectory(prefix='aggregate-observation-') as private:
            private_path = Path(private)
            verify_source(root)
            coverage = load(root / '.github/scripts/ci-coverage.py', 'observed_original_coverage')
            native = load(root / '.github/scripts/ci-nearby-native.py', 'observed_original_capture')
            stage = 'target_identity'
            run = coverage.api('actions/runs/' + str(TARGET_RUN))
            require(run.get('id') == TARGET_RUN and type(run.get('id')) is int and run.get('run_attempt') == TARGET_ATTEMPT
                and type(run.get('run_attempt')) is int and run.get('head_sha') == TARGET_HEAD
                and run.get('event') == 'pull_request' and run.get('path') == '.github/workflows/ci.yml'
                and run.get('name') == 'Cross-platform CI' and run.get('head_branch') == 'feature/nearby-durable-text'
                and run.get('status') == 'completed' and run.get('conclusion') == 'failure')
            prs = run.get('pull_requests')
            require(type(prs) is list and len(prs) == 1 and prs[0].get('number') == 51
                and prs[0].get('head', {}).get('sha') == TARGET_HEAD
                and prs[0].get('base', {}).get('sha') == TARGET_BASE
                and prs[0].get('base', {}).get('ref') == 'main'
                and run.get('repository', {}).get('full_name') == REPOSITORY)
            stage = 'plan_metadata'
            meta = coverage.api('actions/artifacts/' + str(PLAN_ID))
            require(meta.get('id') == PLAN_ID and meta.get('name') == 'ci-impact-plan' and meta.get('expired') is False
                and meta.get('size_in_bytes') == PLAN_SIZE and meta.get('digest') == 'sha256:' + PLAN_DIGEST
                and meta.get('workflow_run', {}).get('id') == TARGET_RUN
                and meta.get('workflow_run', {}).get('head_sha') == TARGET_HEAD)
            stage = 'plan_download'
            state, code, raw, _ = native.capture(['gh', 'api', '--method', 'GET',
                'repos/' + REPOSITORY + '/actions/artifacts/' + str(PLAN_ID) + '/zip'], timeout=60, limit=1024*1024)
            require(state == 'completed' and code == 0 and len(raw) == PLAN_SIZE and hashlib.sha256(raw).hexdigest() == PLAN_DIGEST)
            stage = 'plan_validation'
            with zipfile.ZipFile(io.BytesIO(raw)) as archive:
                members = archive.infolist()
                require(len(members) == 1 and members[0].filename == 'ci-impact-plan.json' and not members[0].is_dir()
                    and not members[0].flag_bits & 1 and 0 < members[0].file_size <= 1024*1024)
                plan = archive.read(members[0])
            require(coverage.api('actions/artifacts/' + str(PLAN_ID)) == meta)
            private = Path(private)
            event_path = private / 'event.json'; event_path.write_text(json.dumps(original_event()))
            plan_path = private / 'plan.json'; plan_path.write_bytes(plan)
            os.environ.clear(); os.environ.update(historical_environment(original_env, event_path))
            stage = 'selection'
            require(coverage.read_plan(str(plan_path)) == coverage.classify(coverage.head(), coverage.force_full()))
            report['planSelectionReproduced'] = True
            stage = 'finalize_other'
            with capture_observer.enabled():
                coverage.finalize(str(plan_path), str(private / 'unpublished-coverage.json'))
            stage = 'target_identity'
            after = coverage.api('actions/runs/' + str(TARGET_RUN))
            require(all(after.get(key) == run.get(key) for key in ('id', 'run_attempt', 'head_sha', 'event', 'path', 'name', 'status', 'conclusion')))
            stage = 'source_identity'; verify_source(root)
            report.update(result='observed_success_not_qualification', stage='complete', **{'class': 'none'})
    except Exception as error:
        report.update(closed_failure(error, root, stage))
        captured = capture_observer.last
        if captured is not None and report['stage'] == captured['fetch']:
            report['captureStatus'] = captured['status']
    finally:
        os.environ.clear(); os.environ.update(original_env)
        report['privateOutputDeleted'] = private_path is None or not private_path.exists()
        try:
            with silence_process_output():
                verify_source(root)
            report['sourceRechecked'] = True
        except Exception:
            report['integrityFailures'].append('source_identity')
        if coverage is not None and run is not None:
            try:
                with silence_process_output():
                    after = coverage.api('actions/runs/' + str(TARGET_RUN))
                    require(all(after.get(key) == run.get(key) for key in ('id', 'run_attempt', 'head_sha', 'event', 'path', 'name', 'status', 'conclusion', 'pull_requests', 'repository')))
                report['targetRechecked'] = True
            except Exception:
                report['integrityFailures'].append('target_identity')
        if not report['privateOutputDeleted']:
            report['integrityFailures'].append('cleanup')
        if report['integrityFailures'] and report['result'] == 'observed_success_not_qualification':
            report.update(stage=report['integrityFailures'][0], **{'class': 'validation_failed', 'frames': []})
        if report['integrityFailures']:
            report['result'] = 'rejected'
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--baseline', required=True)
    args = parser.parse_args()
    # The observer is already a separate process invoked by its unique workflow.
    try:
        observation = observer_identity(os.environ)
        root = Path(args.baseline).resolve()
        report = run_observation(root, observation)
    except Exception:
        report = {'schema': 'aggregate-observation-v1', 'qualifying': False,
                  'result': 'rejected', 'stage': 'observer_identity', 'class': 'validation_failed', 'frames': []}
    print(json.dumps(report, sort_keys=True, separators=(',', ':')))
    return 0 if report['result'] == 'observed_success_not_qualification' else 1


if __name__ == '__main__':
    sys.dont_write_bytecode = True
    raise SystemExit(main())
