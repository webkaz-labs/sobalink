#!/usr/bin/env python3
"""One-off paired experiment, never a required native/release receipt.

The fixed revisions differ only in test scheduling. All variants run the whole
Core inventory with ordinary CI flags on the same runner and private warm build
cache. Raw streams stay inside the existing bounded, owner-joining collector.
"""
import copy
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import sys
import tempfile
import time

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
VARIANTS = {
    'baseline': ('0826f7c6bbf2ccda01fbd142e28f37d83fe5d43e', '2497c09eb675943ae1c9f862fbf4b73e25d93cca'),
    'child': ('9ede150b05c9fe57ab990baf2725db4ae662061b', '621d97ecf93091c74e9cdbb20c3d33414d9e8154'),
    'parent': ('07c5c98bf6d7292d017b455a5c311cec06b90488', '6e8b1fd070370fb52ad7593eaa9c61b7ac36660d'),
}
CHANGED = {'internal/core/direct_lan_completion_test.go',
           'internal/core/direct_lan_endpoint_offline_test.go',
           'internal/core/direct_lan_endpoint_transaction_test.go'}
HELPERS = {'internal/core/direct_lan_parent_parallel_test.go',
           'internal/core/direct_lan_parent_serial_test.go'}
ORDER = ('baseline', 'child', 'parent', 'baseline')
SUBTEST_SKIPS = {'darwin-arm64': 0, 'windows-amd64': 32}
PROBE = 'TestAtomicOutcomeJournalCommittedRevocationMustCloseTrust'
FAILURE_CLASSES = {
    'path-length': ('filename or extension is too long', 'file name too long', 'path is too long', 'destination name too long'),
    'missing-path': ('cannot find the path', 'cannot find the file', 'no such file or directory'),
    'permission': ('access is denied', 'permission denied'),
    'non-directory': ('not a directory', 'must be a real directory', 'non-reparse directory'),
    'ownership': ('another sobalink process owns', 'ownership'),
    'invalid-argument': ('invalid argument', 'parameter is incorrect', 'directory name is invalid'),
}


def failure_classes(raw):
    lower = raw.lower()
    return {key: sum(lower.count(phrase.encode('ascii')) for phrase in phrases)
            for key, phrases in FAILURE_CLASSES.items()}


def module(root, name):
    path = root / '.github/scripts' / (name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


def require(ok):
    if not ok:
        raise ValueError('experiment invariant failed')


def source_delta(left, right):
    return {name for name in left.keys() | right.keys() if left.get(name) != right.get(name)}


def main():
    report = {'schema': 1, 'scope': 'paired-native-core-experiment',
              'releaseValidation': False, 'comparisonComplete': False,
              'phase': 'preflight', 'platform': None, 'variants': {}, 'runs': [],
              'order': list(ORDER), 'externalCacheRestore': False,
              'externalCacheSave': False, 'reusedTestResults': False}
    cases, export = {}, None
    # Leave enough time for bounded capture cleanup and artifact upload before
    # the 60-minute job limit, including checkout and official toolchain setup.
    deadline = time.monotonic() + 45 * 60
    started = time.monotonic()
    try:
        os.umask(0o077)
        require(os.environ.get('GITHUB_ACTIONS') == 'true'
                and os.environ.get('GITHUB_REPOSITORY') == 'webkaz-labs/sobalink'
                and os.environ.get('GITHUB_REF') == 'refs/heads/perf/profile-core-unit-comparison'
                and os.environ.get('GITHUB_EVENT_NAME') == 'push')
        temporary = Path(os.environ['RUNNER_TEMP'])
        destination = temporary / 'core-profile-results'
        destination.mkdir(mode=0o700, exist_ok=False)
        export = destination
        require(re.fullmatch('[0-9a-f]{40}', os.environ.get('GITHUB_SHA', ''))
                and re.fullmatch('[1-9][0-9]{0,19}', os.environ.get('GITHUB_RUN_ID', ''))
                and re.fullmatch('[1-9][0-9]{0,5}', os.environ.get('GITHUB_RUN_ATTEMPT', '')))
        bootstrap = module(ROOT, 'ci-native-core')
        platform = os.environ.get('SOBALINK_CORE_NATIVE_PLATFORM')
        require(platform in bootstrap.PLATFORMS)
        report['platform'] = platform
        require(os.environ.get('GOFLAGS') == bootstrap.GOFLAGS
                and os.environ.get('GOTOOLCHAIN') == 'local' and os.environ.get('GOWORK') == 'off'
                and not any(key in os.environ for key in ('GOMAXPROCS', 'GOTRACEBACK', 'GOCACHEPROG', 'GOEXPERIMENT')))
        runtime = bootstrap.runtime_capability()
        require(runtime['available'])
        workspace = Path(tempfile.mkdtemp(prefix='core-profile-', dir=temporary))
        for name in ('gomodcache', 'gocache'):
            (workspace / name).mkdir(mode=0o700)
        os.environ.update(GOMODCACHE=str(workspace / 'gomodcache'), GOCACHE=str(workspace / 'gocache'))
        versions = json.loads(bootstrap.checked(['go', 'env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH',
                                                'GOOS', 'GOARCH', 'CGO_ENABLED']))
        identity = bootstrap.native.identities(platform, versions)
        tool = Path(shutil.which('go'))
        tool_hash = bootstrap.native.digest(tool)
        report.update(toolchain=identity, goSha256=tool_hash, collectorRuntime=runtime,
                      workflowCommit=os.environ['GITHUB_SHA'],
                      runId=os.environ['GITHUB_RUN_ID'], runAttempt=os.environ['GITHUB_RUN_ATTEMPT'])

        def capture(case, phase, result, env=None):
            collector = case['collector']
            require(time.monotonic() + collector.BOUNDS[phase] + 60 < deadline)
            result['phase'] = phase
            case['ownersState'] = 'in_flight'
            process, raw = collector.capture_phase(phase, case['env'] if env is None else env, result)
            case['ownersState'] = result['ownersState']
            report['variants'][case['name']]['lastPhase'] = phase
            report['variants'][case['name']]['lastProcess'] = process
            require(process['ownersJoined'])
            return process, raw

        report['phase'] = 'prepare-all-variants'
        for name, (commit, tree) in VARIANTS.items():
            root = workspace / name
            bootstrap.checked(['git', 'worktree', 'add', '--detach', str(root), commit])
            collector = module(root, 'ci-native-core')
            validator = module(root, 'ci-native-core-required')
            require(collector.checked(['git', 'rev-parse', 'HEAD']).decode().strip() == commit
                    and collector.checked(['git', 'rev-parse', 'HEAD^{tree}']).decode().strip() == tree)
            source = collector.source_inventory()
            if name != 'baseline':
                require(source_delta(cases['baseline']['source'], source) == CHANGED | (HELPERS if name == 'parent' else set()))
            # Formatting must not mutate the source under comparison.
            require(not collector.checked(['gofmt', '-l', *sorted(CHANGED | (HELPERS if name == 'parent' else set()))]).strip())
            # Match the ordinary required Core collector's temporary-root depth.
            private = Path(tempfile.mkdtemp(prefix='soba-native-core-private-', dir=temporary))
            env = dict(os.environ, TMPDIR=str(private), TMP=str(private), TEMP=str(private))
            case = {'name': name, 'root': root, 'collector': collector, 'validator': validator, 'source': source,
                    'private': private, 'env': env, 'ownersState': 'not_started', 'preparation': {}}
            cases[name] = case
            row = {'commit': commit, 'tree': tree, 'sourceSha256': collector.aggregate(source),
                   'sourceFiles': len(source), 'preparation': case['preparation'], 'sourceVerified': False}
            report['variants'][name] = row
            result = validator.empty_result()
            for phase in ('prepare', 'verify', 'modules', 'inventory'):
                process, raw = capture(case, phase, result)
                case['preparation'][phase] = process
                require(process['process'] == 'completed' and process['returncode'] == 0)
                if phase == 'inventory':
                    case['names'] = collector.inventory(raw)
            require(source == collector.source_inventory())
            if name != 'baseline':
                require(case['names'] == cases['baseline']['names'])
            row['inventorySha256'] = collector.aggregate(case['names'])
            row['inventoryCount'] = len(case['names'])
            if name == 'baseline':
                # Catch fixture/setup differences before compiling the other
                # variants. This focused probe is not a whole-suite result.
                require(PROBE in case['names'])
                probe_env = dict(env, GOFLAGS=collector.GOFLAGS + ' -run=^' + PROBE + '$')
                probe_process, probe_raw = capture(case, 'suite', validator.empty_result(), probe_env)
                probe = collector.timing(probe_raw, [PROBE], probe_process)
                row['fixtureProbe'] = probe
                row['fixtureProbeFailureClasses'] = failure_classes(probe_raw) if not collector.execution_complete(probe) else {}
                require(collector.execution_complete(probe) and probe['passedCount'] == 1 and not probe['skippedCount'])

        # The repeated baseline brackets order/thermal drift. It is an
        # observation, not a claim of statistical significance or CI latency.
        report['phase'] = 'whole-core-runs'
        baseline_skipped_children = None
        for index, name in enumerate(ORDER):
            case = cases[name]
            collector, validator = case['collector'], case['validator']
            require(case['source'] == collector.source_inventory() and collector.native.digest(tool) == tool_hash)
            result = validator.empty_result()
            result['preparation'] = copy.deepcopy(case['preparation'])
            result['inventorySha256'] = collector.aggregate(case['names'])
            record = {'variant': name, 'ordinal': index + 1, 'result': result}
            report['runs'].append(record)
            process, raw = capture(case, 'suite', result)
            suite = collector.timing(raw, case['names'], process)
            result['runs']['suite'] = suite
            if not collector.execution_complete(suite):
                record['failureClasses'] = failure_classes(raw)
            skipped_children = sorted(event['Test'] for event in collector.events(raw)
                                      if event['Action'] == 'skip' and '/' in event.get('Test', ''))
            require(len(skipped_children) == len(set(skipped_children)) == SUBTEST_SKIPS[platform])
            if baseline_skipped_children is None:
                baseline_skipped_children = skipped_children
            require(skipped_children == baseline_skipped_children)
            record['subtestSkipNamesSha256'] = collector.aggregate(skipped_children)
            require(collector.execution_complete(suite) and collector.bound_suite(result)
                    and suite['subtestSkipped'] == SUBTEST_SKIPS[platform]
                    and {test['test'] for test in suite['tests'] if test['outcome'] == 'skip'} == validator.SKIPS[platform])
            result['metrics'] = [{'suite': 'native-core-suite', 'elapsed_seconds': process['wallSeconds'],
                                  'returncode': 0, 'status': 'passed'}]
            result['sourceVerified'] = case['source'] == collector.source_inventory()
            require(result['sourceVerified'])
            result.update(phase='complete', timingComplete=True)
            print('Completed whole Core run', index + 1, name, 'package seconds', suite['packageSeconds'], flush=True)

        report['phase'] = 'verify-final-source'
        for name, case in cases.items():
            result = case['validator'].empty_result()
            process, _ = capture(case, 'verify', result)
            report['variants'][name]['finalDependencyVerification'] = process
            require(process['process'] == 'completed' and process['returncode'] == 0)
            require(case['source'] == case['collector'].source_inventory())
            report['variants'][name]['sourceVerified'] = True
        require(bootstrap.native.digest(tool) == tool_hash)
        report['phase'] = 'complete'
    except (Exception, KeyboardInterrupt):
        # Do not export exceptions, raw test output, source inventories, paths,
        # environment values, or stacks. Partial observations are not success.
        pass
    finally:
        all_joined = all(case['ownersState'] in ('not_started', 'joined') for case in cases.values())
        cleaned = all_joined
        if all_joined:
            for case in cases.values():
                try:
                    shutil.rmtree(case['private'])
                    cleaned &= not case['private'].exists()
                except OSError:
                    cleaned = False
        try:
            require(report['phase'] == 'complete' and all_joined and cleaned and len(report['runs']) == len(ORDER))
            for record in report['runs']:
                record['result']['cleanupComplete'] = True
                cases[record['variant']]['validator'].validate_core(record['result'], report['platform'])
            report['comparisonComplete'] = True
        except (Exception, KeyboardInterrupt):
            report['comparisonComplete'] = False
        report['cleanupComplete'] = cleaned
        report['ownersJoined'] = all_joined
        report['driverWallSeconds'] = round(time.monotonic() - started, 6)
        try:
            require(export is not None)
            (export / 'profile.json').write_text(json.dumps(report, indent=2, allow_nan=False) + '\n', encoding='utf-8')
        except (Exception, KeyboardInterrupt):
            report['comparisonComplete'] = False
            report['phase'] = 'export-failed'
    print('Paired Core experiment complete.' if report['comparisonComplete'] else 'FAIL: Paired Core experiment incomplete.')
    return 0 if report['comparisonComplete'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
