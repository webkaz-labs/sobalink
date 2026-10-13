#!/usr/bin/env python3
"""Required native race scheduling; diagnostic receipts never satisfy this gate.

One inventory-warmed, private whole-Core execution precedes other workloads.
The later exact package complement retains ordinary Go execution and metrics.
This is a setup change, not a claim about the cause of earlier timeouts.
"""
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import shutil
import sys
import tempfile

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
REPOSITORY = 'webkaz-labs/sobalink'
WORKFLOW = '.github/workflows/ci.yml'
RELEASE_WORKFLOW = '.github/workflows/prerelease.yml'
TARGETS = {
    'windows-amd64': ('native (windows-2025, windows, amd64)', 'windows-2025'),
    'darwin-arm64': ('native (macos-26, darwin, arm64)', 'macos-26'),
}
STEPS = ('Collect required whole-Core race evidence', 'Verify remaining native package race coverage',
         'Preserve required native package proof')
ARTIFACT = 'soba-native-core-required'
SHA = re.compile('[0-9a-f]{40}\\Z')
HASH = re.compile('[0-9a-f]{64}\\Z')
LOGS = ('logs/go-cache.txt', 'logs/dependencies.txt')
SKIPS = {
    'darwin-arm64': {'TestCoreReceiveCrashChild', 'TestCoreReceivePreparationChild'},
    'windows-amd64': {'TestCoreReceiveCrashChild', 'TestCoreReceivePreparationChild',
        'TestResourceBindingPrivateChildCreationUsesOwnedParent', 'TestGroupBindingRejectsUnsafePermissions'},
}


def require(ok, message='required native package proof differs'):
    if not ok:
        raise ValueError(message)


def module(name):
    path = Path(__file__).with_name(name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


collector = module('ci-native-core')
packages = module('ci-windows-packages')
shared = module('ci-dnssd-required')
POLICY_FILES = tuple(dict.fromkeys((*collector.POLICY_FILES, *packages.POLICY_FILES, WORKFLOW,
    RELEASE_WORKFLOW, '.github/scripts/ci-native-core-required.py',
    '.github/scripts/test_ci_native_core_required.py', '.github/scripts/ci-dnssd-required.py',
    '.github/scripts/ci-dnssd-diagnostic.py', '.github/scripts/ci-dnssd-terminal.py',
    '.github/scripts/ci-coverage.py', '.github/scripts/ci_full_validation.py',
    '.github/scripts/check-frontend.py', 'cmd/prepare-engine/main.go')))


def positive(value):
    return type(value) is int and value > 0


def number(value, maximum=720):
    return type(value) in (int, float) and math.isfinite(value) and 0 <= value <= maximum


def ci_identity(event, commit, tree, platform):
    require(platform in TARGETS and os.environ.get('GITHUB_ACTIONS') == 'true')
    if os.environ.get('GITHUB_WORKFLOW') == 'Cross-platform CI':
        result = shared.ci_identity(event, commit, tree)
    else:
        env = os.environ
        require(Path.cwd().resolve() == ROOT and env.get('GITHUB_REPOSITORY') == REPOSITORY
                and env.get('GITHUB_WORKFLOW') == 'Experimental prerelease' and env.get('GITHUB_JOB') == 'native'
                and env.get('GITHUB_EVENT_NAME') == 'workflow_dispatch' and env.get('GITHUB_REF') == 'refs/heads/main'
                and env.get('GITHUB_WORKFLOW_REF') == REPOSITORY + '/' + RELEASE_WORKFLOW + '@refs/heads/main'
                and event.get('repository', {}).get('full_name') == REPOSITORY
                and event.get('repository', {}).get('private') is False
                and event.get('repository', {}).get('default_branch') == 'main'
                and event.get('inputs', {}).get('tested_commit') == commit == env.get('TESTED_COMMIT')
                and SHA.fullmatch(commit) and SHA.fullmatch(tree) and SHA.fullmatch(env.get('GITHUB_SHA', '')))
        run_id, attempt = env.get('GITHUB_RUN_ID', ''), env.get('GITHUB_RUN_ATTEMPT', '')
        require(re.fullmatch('[1-9][0-9]{0,19}', run_id) and re.fullmatch('[1-9][0-9]{0,5}', attempt))
        result = {'repository': REPOSITORY, 'workflow': RELEASE_WORKFLOW, 'job': '', 'runId': int(run_id),
            'runAttempt': int(attempt), 'event': 'workflow_dispatch', 'eventHead': env['GITHUB_SHA'],
            'commit': commit, 'tree': tree}
    result['job'] = TARGETS[platform][0]
    return result


def source_inventory(*, late=False, release=False):
    if not late:
        return collector.source_inventory()
    native = collector.native
    require(not native.checked(['git', 'status', '--porcelain', '--untracked-files=no']).strip())
    raw = native.checked(['git', 'ls-files', '--others', '-z'])
    require(not raw or raw.endswith(b'\0'))
    names = [name.decode('utf-8') for name in raw.split(b'\0') if name]
    admitted = set(LOGS) if release else set()
    for name in admitted.intersection(names):
        path = ROOT / name
        require(path.is_file() and not path.is_symlink() and not path.parent.is_symlink() and path.stat().st_nlink == 1)
    retained = [name for name in names if name not in admitted]
    packages.verify_untracked('\0'.join(retained) + ('\0' if retained else ''), module('ci-nearby-diagnostic'))
    # Retain the required-discovery tracked-file and parent-link checks.
    raw = native.checked(['git', 'ls-files', '-z'])
    require(raw and raw.endswith(b'\0'))
    paths = [name.decode('utf-8') for name in raw[:-1].split(b'\0')]
    require(len(paths) == len(set(paths)))
    for name in paths:
        require(not any(part in ('', '.', '..') for part in name.split('/')) and '\\' not in name)
        path = ROOT / name
        require(path.is_file() and not any(parent.is_symlink() for parent in (path, *path.parents) if parent != ROOT.parent))
    for prefix in module('ci-nearby-diagnostic').DEPENDENCY_PREFIXES:
        require(not (ROOT / prefix.rstrip('/')).is_symlink() and not (ROOT / prefix.rstrip('/')).parent.is_symlink())
    return {name: native.digest(ROOT / name) for name in paths}


def native_identity(platform):
    require(platform in TARGETS and os.environ.get('GOFLAGS') == collector.GOFLAGS
            and os.environ.get('GOTOOLCHAIN') == 'local' and os.environ.get('GOWORK') == 'off'
            and not any(key in os.environ for key in ('GOMAXPROCS', 'GOTRACEBACK')))
    versions = json.loads(collector.checked(['go', 'env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH',
        'GOOS', 'GOARCH', 'CGO_ENABLED']), object_pairs_hook=collector.parser.unique_object)
    return collector.native.identities(platform, versions)


def empty_result():
    return {'schema': 1, 'scope': 'native-core-exact-suite-timing', 'phase': 'preflight',
        'sourceVerified': False, 'cleanupComplete': False, 'timingComplete': False,
        'ownersState': 'not_started', 'releaseValidation': False, 'preparation': {}, 'runs': {}, 'metrics': []}


def validate_process(value, platform, phase, *, passed=True):
    require(type(value) is dict and set(value) == {'process', 'returncode', 'ownersJoined', 'executionState',
        'observedReturncode', 'groupAbsenceObservedAfterWait', 'wallSeconds'})
    require(value['ownersJoined'] is True and number(value['wallSeconds'], collector.BOUNDS[phase] + 60)
            and type(value['returncode']) is int
            and value['groupAbsenceObservedAfterWait'] is (True if platform == 'darwin-arm64' else None))
    if passed:
        require(value['process'] == value['executionState'] == 'completed' and value['returncode'] == 0
                and type(value['observedReturncode']) is int and value['observedReturncode'] == 0)
    else:
        states = {'completed', 'nonzero', 'watchdog_timeout', 'output_limit', 'capture_failed'}
        require(value['process'] in states and value['executionState'] in states)
        if value['process'] == 'completed':
            require(value['executionState'] == 'completed' and value['returncode'] == 0)
        if value['process'] == 'nonzero':
            require(value['executionState'] == 'nonzero' and value['returncode'] != 0)
        if value['executionState'] in ('completed', 'nonzero'):
            require(type(value['observedReturncode']) is int
                    and (value['observedReturncode'] == 0) is (value['executionState'] == 'completed'))
        observed = value['observedReturncode']
        require((type(observed) is int and observed == value['returncode'])
                or (observed is None and value['executionState'] in {'watchdog_timeout', 'output_limit', 'capture_failed'}
                    and value['returncode'] != 0))


def validate_core(value, platform, *, passed=True):
    require(type(value) is dict and set(value) == set(empty_result()) | {'inventorySha256'})
    require(type(value['schema']) is int and value['schema'] == 1
            and value['scope'] == 'native-core-exact-suite-timing' and value['phase'] == 'complete'
            and value['sourceVerified'] is True and value['cleanupComplete'] is True
            and value['ownersState'] == 'joined' and value['releaseValidation'] is False
            and type(value['timingComplete']) is bool
            and type(value['preparation']) is dict and set(value['preparation']) == {'prepare', 'verify', 'modules', 'inventory'}
            and type(value['runs']) is dict and set(value['runs']) == {'suite'})
    for phase, row in value['preparation'].items():
        validate_process(row, platform, phase)
    suite = value['runs']['suite']
    process_keys = set(value['preparation']['inventory'])
    validate_process({key: suite[key] for key in process_keys}, platform, 'suite', passed=passed)
    template = collector.timing(b'', ['TestSynthetic'], value['preparation']['inventory'])
    require(type(suite) is dict and set(suite) == set(template))
    for key in ('accountingComplete', 'allTestsPassed', 'eventStreamValid', 'packageTimeoutObserved'):
        require(type(suite[key]) is bool)
    for key in ('expectedCount', 'startedCount', 'terminalCount', 'passedCount', 'failedCount', 'skippedCount',
                'runningCount', 'notRunCount', 'subtestStarted', 'subtestTerminal', 'subtestSkipped', 'subtestFailed'):
        require(type(suite[key]) is int and 0 <= suite[key] <= collector.MAX_TESTS + collector.MAX_SUBTESTS)
    require(type(suite['tests']) is list and 0 < len(suite['tests']) <= collector.MAX_TESTS)
    names = []
    counts = dict(pass_count=0, fail_count=0, skip_count=0, running_count=0, not_run_count=0)
    for row in suite['tests']:
        require(type(row) is dict and set(row) == {'test', 'outcome', 'startedSeconds', 'finishedSeconds',
            'elapsedSeconds', 'pauseCount', 'continueCount'} and type(row['test']) is str
            and collector.TEST_NAME.fullmatch(row['test']) and row['outcome'] in {'pass', 'fail', 'skip', 'running', 'not_run'})
        names.append(row['test'])
        counts[row['outcome'] + '_count'] += 1
        for key in ('pauseCount', 'continueCount'):
            require(type(row[key]) is int and 0 <= row[key] <= collector.MAX_EVENTS)
        require(row['continueCount'] <= row['pauseCount'])
        for key in ('startedSeconds', 'finishedSeconds', 'elapsedSeconds'):
            require(row[key] is None or number(row[key]))
        if row['outcome'] in ('pass', 'fail', 'skip'):
            require(all(row[key] is not None for key in ('startedSeconds', 'finishedSeconds', 'elapsedSeconds'))
                    and row['finishedSeconds'] >= row['startedSeconds'])
    require(len(names) == len(set(names)) and suite['expectedCount'] == len(names)
            and {collector.LEASE, collector.LEASE_PROOF, collector.MIXED}.issubset(names)
            and value['inventorySha256'] == collector.aggregate(names))
    for field, count in [('passedCount', 'pass_count'), ('failedCount', 'fail_count'), ('skippedCount', 'skip_count'),
                         ('runningCount', 'running_count'), ('notRunCount', 'not_run_count')]:
        require(suite[field] == counts[count])
    require(suite['startedCount'] == len(names) - counts['not_run_count']
            and suite['terminalCount'] == sum(counts[key] for key in ('pass_count', 'fail_count', 'skip_count'))
            and suite['subtestFailed'] + suite['subtestSkipped'] <= suite['subtestTerminal'] <= suite['subtestStarted']
            and number(suite['observedThroughSeconds']))
    accounting = (suite['eventStreamValid'] and suite['packageOutcome'] in {'pass', 'fail', 'skip'}
        and suite['terminalCount'] == len(names) and suite['subtestStarted'] == suite['subtestTerminal'])
    require(suite['accountingComplete'] is accounting)
    all_passed = (accounting and suite['process'] == 'completed' and suite['returncode'] == 0
        and suite['ownersJoined'] and suite['packageOutcome'] == 'pass' and suite['passedCount'] == len(names)
        and suite['subtestSkipped'] == suite['subtestFailed'] == 0)
    require(suite['allTestsPassed'] is all_passed)
    require(value['timingComplete'] is (collector.bound_suite(value) and collector.execution_complete(suite)))
    progress = [{'throughSeconds': boundary,
        'started': sum(row['startedSeconds'] is not None and row['startedSeconds'] <= boundary for row in suite['tests']),
        'terminal': sum(row['finishedSeconds'] is not None and row['finishedSeconds'] <= boundary for row in suite['tests'])}
        for boundary in range(30, int(suite['observedThroughSeconds']) + 31, 30)]
    shared.exact_shape(suite['progress'], progress)
    require(suite['progress'] == progress and type(value['metrics']) is list and len(value['metrics']) == 1)
    metric = collector.metrics.validate_record(value['metrics'][0])
    expected_code = suite['returncode']
    if expected_code == 0 and not collector.execution_complete(suite):
        expected_code = 1
    require(metric['suite'] == 'native-core-suite' and metric['elapsed_seconds'] == suite['wallSeconds']
            and metric['returncode'] == expected_code
            and suite['packageOutcome'] in {'pass', 'fail', 'skip', 'missing'}
            and (suite['packageSeconds'] is None or number(suite['packageSeconds'])))
    if passed:
        require(value['timingComplete'] is True and collector.bound_suite(value) and collector.execution_complete(suite)
                and suite['eventStreamValid'] is True and suite['packageTimeoutObserved'] is False
                and suite['startedCount'] == suite['terminalCount'] == suite['expectedCount']
                and suite['subtestStarted'] == suite['subtestTerminal']
                and suite['passedCount'] > 0 and suite['runningCount'] == suite['notRunCount'] == 0
                and number(suite['packageSeconds']) and metric['returncode'] == 0
                and suite['allTestsPassed'] is (suite['skippedCount'] == suite['subtestSkipped'] == 0))
        require({row['test'] for row in suite['tests'] if row['outcome'] == 'skip'} == SKIPS[platform],
                'unexpected ordinary-suite skips')
    return value


def validate_receipt(value, identity, policy_hashes, *, complete=True):
    require(type(value) is dict and set(value) == {'schema', 'requiredPassed', 'provenance', 'result',
        'inventory', 'inventorySha256', 'groups', 'remaining', 'remainingSourceVerified', 'remainingInventoryVerified'})
    require(type(value['schema']) is int and value['schema'] == 1 and type(value['requiredPassed']) is bool)
    prov = value['provenance']
    require(type(prov) is dict and set(prov) == {'ciIdentity', 'platform', 'sourceSha256', 'sourceFiles', 'policySha256',
        'toolchain', 'goSha256', 'collectorRuntime', 'commands', 'watchdogSeconds', 'race', 'count', 'packageTimeoutSeconds', 'ownershipMechanism'})
    platform = prov['platform']
    require(platform in TARGETS and type(identity) is dict and set(identity) == {'repository', 'workflow', 'job', 'runId',
        'runAttempt', 'event', 'eventHead', 'commit', 'tree'} and identity['repository'] == REPOSITORY
        and identity['workflow'] in (WORKFLOW, RELEASE_WORKFLOW) and identity['job'] == TARGETS[platform][0]
        and positive(identity['runId']) and positive(identity['runAttempt'])
        and identity['event'] in {'pull_request', 'push', 'workflow_dispatch', 'schedule'}
        and all(type(identity[name]) is str and SHA.fullmatch(identity[name]) for name in ('eventHead', 'commit', 'tree')))
    shared.exact_shape(prov['ciIdentity'], identity)
    require(prov['ciIdentity'] == identity and type(policy_hashes) is dict and set(policy_hashes) == set(POLICY_FILES)
            and all(type(item) is str and HASH.fullmatch(item) for item in policy_hashes.values())
            and prov['policySha256'] == policy_hashes and positive(prov['sourceFiles']) and prov['sourceFiles'] <= 100000
            and all(type(prov[key]) is str and HASH.fullmatch(prov[key]) for key in ('sourceSha256', 'goSha256')))
    goos, goarch = platform.split('-')
    expected_tool = {'go': 'go1.27.1', 'goos': goos, 'goarch': goarch, 'runner': TARGETS[platform][1], 'race': True}
    shared.exact_shape(prov['toolchain'], expected_tool)
    require(prov['toolchain'] == expected_tool and prov['commands'] == collector.COMMANDS
            and prov['watchdogSeconds'] == collector.BOUNDS and prov['race'] is True
            and type(prov['count']) is int and prov['count'] == 1
            and type(prov['packageTimeoutSeconds']) is int and prov['packageTimeoutSeconds'] == 600
            and prov['ownershipMechanism'] == ('windows-job-object' if goos == 'windows' else 'unreaped-leader-posix-process-group'))
    shared.exact_shape(prov['commands'], collector.COMMANDS)
    shared.exact_shape(prov['watchdogSeconds'], collector.BOUNDS)
    runtime = prov['collectorRuntime']
    primitives = ('waitid', 'getpgid', 'killpg', 'P_PID', 'WEXITED', 'WNOHANG', 'WNOWAIT', 'CLD_EXITED', 'CLD_KILLED', 'CLD_DUMPED')
    require(type(runtime) is dict and set(runtime) == {'pythonVersion', 'darwinMinimumPython', 'darwinNonReapingWaitRequired',
        'requiredPrimitives', 'available'} and runtime['available'] is True
        and runtime['darwinNonReapingWaitRequired'] is (goos == 'darwin')
        and type(runtime['pythonVersion']) is list and len(runtime['pythonVersion']) == 3
        and all(type(part) is int and 0 <= part <= 999 for part in runtime['pythonVersion'])
        and runtime['darwinMinimumPython'] == [3, 13, 0]
        and all(type(part) is int for part in runtime['darwinMinimumPython'])
        and runtime['requiredPrimitives'] == ({key: True for key in primitives} if goos == 'darwin' else {}))
    require(all(item is True for item in runtime['requiredPrimitives'].values())
            and (goos != 'darwin' or runtime['pythonVersion'] >= [3, 13, 0]))
    validate_core(value['result'], platform, passed=complete)
    packages.validate_partition(value['inventory'], value['groups'])
    require(value['inventorySha256'] == packages.fingerprint(value['inventory']))
    if complete:
        require(value['requiredPassed'] is True and value['remainingSourceVerified'] is True
                and value['remainingInventoryVerified'] is True)
        row = value['remaining']
        require(type(row) is dict and set(row) == {'packages', 'returncode', 'metric'}
                and row['packages'] == value['groups']['remaining'] and type(row['returncode']) is int and row['returncode'] == 0)
        metric = collector.metrics.validate_record(row['metric'])
        require(metric['suite'] == 'local-race-windows-remaining' and metric['returncode'] == 0)
    else:
        require(value['requiredPassed'] is False and value['remaining'] is None
                and value['remainingSourceVerified'] is False and value['remainingInventoryVerified'] is False)
    return value


def read_receipt(path):
    require(path.is_file() and not path.is_symlink() and path.stat().st_size <= shared.MAX_BYTES)
    return module('ci-nearby-diagnostic').parse_json(path.read_text(encoding='utf-8'))


def write_receipt(path, value):
    with path.open('x', encoding='utf-8') as output:
        json.dump(value, output, sort_keys=True, allow_nan=False)
        output.write('\n')


def context(platform):
    commit = collector.checked(['git', 'rev-parse', 'HEAD']).decode().strip()
    tree = collector.checked(['git', 'rev-parse', 'HEAD^{tree}']).decode().strip()
    path = Path(os.environ['GITHUB_EVENT_PATH'])
    require(path.stat().st_size <= collector.CAPTURE_BYTES)
    event = module('ci-nearby-diagnostic').parse_json(path.read_text(encoding='utf-8'))
    return ci_identity(event, commit, tree, platform)


def collect(platform, export, identity):
    result, private = empty_result(), None
    value = {'schema': 1, 'requiredPassed': False, 'provenance': {}, 'result': result,
        'inventory': [], 'inventorySha256': None, 'groups': {}, 'remaining': None,
        'remainingSourceVerified': False, 'remainingInventoryVerified': False}
    try:
        runtime = collector.runtime_capability()
        require(runtime['available'])
        toolchain = native_identity(platform)
        tool = Path(shutil.which('go'))
        tool_hash = collector.native.digest(tool)
        source = source_inventory()
        value['provenance'] = {'ciIdentity': identity, 'platform': platform, 'sourceSha256': collector.aggregate(source),
            'sourceFiles': len(source), 'policySha256': {name: source[name] for name in POLICY_FILES},
            'toolchain': toolchain, 'goSha256': tool_hash, 'collectorRuntime': runtime,
            'commands': collector.COMMANDS, 'watchdogSeconds': collector.BOUNDS,
            'race': True, 'count': 1, 'packageTimeoutSeconds': 600,
            'ownershipMechanism': 'windows-job-object' if platform == 'windows-amd64' else 'unreaped-leader-posix-process-group'}
        private = Path(tempfile.mkdtemp(prefix='soba-native-core-private-', dir=Path(os.environ['RUNNER_TEMP'])))
        env = dict(os.environ, TMPDIR=str(private), TMP=str(private), TEMP=str(private))
        previous_metrics = os.environ.get('SOBALINK_CI_METRICS')
        try:
            os.environ['SOBALINK_CI_METRICS'] = str(private / 'metrics.jsonl')
            collector.run_diagnostic(env, result)
        finally:
            if previous_metrics is None:
                os.environ.pop('SOBALINK_CI_METRICS', None)
            else:
                os.environ['SOBALINK_CI_METRICS'] = previous_metrics
        inventory = packages.packages(packages.query(['go', 'list', '-race', './...']))
        value.update(inventory=inventory, inventorySha256=packages.fingerprint(inventory), groups=packages.partition(inventory))
        require(source_inventory() == source and collector.native.digest(tool) == tool_hash
                and native_identity(platform) == toolchain and context(platform) == identity)
        result['sourceVerified'] = True
        result['timingComplete'] = collector.bound_suite(result) and collector.execution_complete(result['runs']['suite'])
        result['phase'] = 'complete'
    except (Exception, KeyboardInterrupt):
        pass
    finally:
        if private is not None and result['ownersState'] in ('not_started', 'joined') and all(
                row['ownersJoined'] for row in (*result['preparation'].values(), *result['runs'].values())):
            try:
                shutil.rmtree(private)
                result['cleanupComplete'] = not private.exists()
            except OSError:
                pass
        result['timingComplete'] &= result['cleanupComplete'] and result['sourceVerified']
        write_receipt(export / 'core.json', value)
    # Genuine Core failure is retained as a failed step. Only a source-verified,
    # joined, cleaned receipt can authorize attempting the exact complement.
    validate_receipt(read_receipt(export / 'core.json'), identity, value['provenance']['policySha256'], complete=False)
    with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
        print('remaining_safe=true', file=output)
    for metric in result['metrics']:
        collector.metrics.append_record(metric)
    validate_core(result, platform)


def remaining(platform, export, identity):
    require(not (export / 'receipt.json').exists())
    value = read_receipt(export / 'core.json')
    source = source_inventory(late=True, release=identity['workflow'] == RELEASE_WORKFLOW)
    policy = {name: source[name] for name in POLICY_FILES}
    validate_receipt(value, identity, policy, complete=False)
    require(value['provenance']['sourceSha256'] == collector.aggregate(source)
            and value['provenance']['sourceFiles'] == len(source)
            and native_identity(platform) == value['provenance']['toolchain']
            and collector.native.digest(Path(shutil.which('go'))) == value['provenance']['goSha256']
            and packages.packages(packages.query(['go', 'list', '-race', './...'])) == value['inventory'])
    try:
        # Reuse the existing whole-package runner verbatim. Its historical
        # metric label is retained on both native targets; membership is exact.
        value['remaining'] = packages.run_group('remaining', value['groups']['remaining'])
        value['remainingSourceVerified'] = source_inventory(late=True, release=identity['workflow'] == RELEASE_WORKFLOW) == source
        value['remainingInventoryVerified'] = packages.packages(packages.query(['go', 'list', '-race', './...'])) == value['inventory']
        require(context(platform) == identity and native_identity(platform) == value['provenance']['toolchain']
                and collector.native.digest(Path(shutil.which('go'))) == value['provenance']['goSha256'])
        value['requiredPassed'] = True
        validate_receipt(value, identity, policy)
    except (Exception, KeyboardInterrupt):
        value['requiredPassed'] = False
    write_receipt(export / 'receipt.json', value)
    validate_receipt(read_receipt(export / 'receipt.json'), identity, policy)


def native_job(api, run, attempt, platform):
    job_name, runner = TARGETS[platform]
    jobs = shared.pages(api, f"actions/runs/{run['id']}/attempts/{attempt}/jobs", 'jobs')
    selected = [job for job in jobs if job.get('name') == job_name]
    require(len(selected) == 1)
    job = selected[0]
    require(job.get('run_id') == run['id'] and type(job.get('run_id')) is int
            and job.get('run_attempt') == attempt and type(job.get('run_attempt')) is int
            and job.get('head_sha') == run['head_sha'] and runner in job.get('labels', [])
            and job.get('status') == 'completed' and job.get('conclusion') == 'success')
    require(shared.timestamp(job['started_at']) <= shared.timestamp(job['completed_at']))
    for name in STEPS:
        steps = [step for step in job.get('steps', []) if step.get('name') == name]
        require(len(steps) == 1 and steps[0].get('status') == 'completed' and steps[0].get('conclusion') == 'success')
        require(shared.timestamp(job['started_at']) <= shared.timestamp(steps[0]['started_at'])
                <= shared.timestamp(steps[0]['completed_at']) <= shared.timestamp(job['completed_at']))
    ordered = [next(step for step in job['steps'] if step['name'] == name) for name in STEPS]
    require(all(shared.timestamp(left['completed_at']) <= shared.timestamp(right['started_at'])
                for left, right in zip(ordered, ordered[1:])), 'native phase order differs')
    return job



def audit_artifact(api, run, commit, tree, platform, *, download=None, policy_hashes=None):
    require(platform in TARGETS)
    artifact_prefix = ARTIFACT + '-' + platform
    require(positive(run.get('id')) and positive(run.get('run_attempt'))
            and run.get('name') == 'Cross-platform CI' and run.get('path') == WORKFLOW
            and type(commit) is str and SHA.fullmatch(commit) and type(tree) is str and SHA.fullmatch(tree))
    current = native_job(api, run, run['run_attempt'], platform)
    artifacts = shared.pages(api, f"actions/runs/{run['id']}/artifacts", 'artifacts')
    seen, candidates = set(), []
    for artifact in artifacts:
        name = artifact.get('name', '')
        if not name.startswith(artifact_prefix + '-'):
            continue
        match = re.fullmatch(re.escape(artifact_prefix) + rf"-{run['id']}-([1-9][0-9]{{0,5}})", name)
        require(match is not None and name not in seen)
        seen.add(name)
        attempt = int(match[1])
        require(attempt <= run['run_attempt'])
        created = shared.timestamp(artifact['created_at'])
        if shared.timestamp(current['started_at']) <= created <= shared.timestamp(current['completed_at']):
            candidates.append((artifact, attempt))
    require(len(candidates) == 1, 'exact effective native artifact is missing or ambiguous')
    artifact, attempt = candidates[0]
    producer = current
    if attempt != run['run_attempt']:
        previous = api(f"actions/runs/{run['id']}/attempts/{attempt}")
        require(previous.get('id') == run['id'] and type(previous.get('id')) is int
                and previous.get('run_attempt') == attempt and type(previous.get('run_attempt')) is int
                and previous.get('head_sha') == run['head_sha'] and previous.get('event') == run['event']
                and previous.get('name') == run['name'] and previous.get('path') == WORKFLOW
                and previous.get('head_branch') == run.get('head_branch') and previous.get('status') == 'completed'
                and shared.timestamp(previous['run_started_at']) < shared.timestamp(run['run_started_at']))
        producer = native_job(api, run, attempt, platform)
        require(all(producer[key] == current[key] for key in ('started_at', 'completed_at')),
                'previous success was not carried into the effective attempt')
        for name in STEPS:
            old = next(step for step in producer['steps'] if step['name'] == name)
            effective = next(step for step in current['steps'] if step['name'] == name)
            require(all(old[key] == effective[key] for key in ('started_at', 'completed_at', 'status', 'conclusion')))
    exported = next(step for step in producer['steps'] if step['name'] == STEPS[-1])
    require(shared.timestamp(exported['started_at']) <= shared.timestamp(artifact['created_at']) <= shared.timestamp(exported['completed_at'])
            and shared.timestamp(artifact['created_at']) <= shared.timestamp(artifact['updated_at']) <= shared.timestamp(producer['completed_at'])
            and artifact.get('expired') is False)
    origin = artifact.get('workflow_run', {})
    require(origin.get('id') == run['id'] and type(origin.get('id')) is int
            and origin.get('head_sha') == run['head_sha'] and origin.get('head_branch') == run.get('head_branch'))
    require(api(f"actions/artifacts/{artifact['id']}") == artifact, 'artifact identity changed')
    raw = (download or shared.download_artifact)(artifact['id'])
    receipt, receipt_hash = shared.artifact_receipt(raw, artifact)
    identity = {'repository': REPOSITORY, 'workflow': WORKFLOW, 'job': TARGETS[platform][0], 'runId': run['id'],
        'runAttempt': attempt, 'event': run['event'], 'eventHead': run['head_sha'], 'commit': commit, 'tree': tree}
    if policy_hashes is None:
        policy_hashes = {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in POLICY_FILES}
    validate_receipt(receipt, identity, policy_hashes)
    require(api(f"actions/artifacts/{artifact['id']}") == artifact, 'artifact identity changed after admission')
    require(native_job(api, run, run['run_attempt'], platform) == current, 'effective native job changed during admission')
    return {'artifactId': artifact['id'], 'artifactSha256': hashlib.sha256(raw).hexdigest(),
            'receiptSha256': receipt_hash, 'runId': run['id'], 'producingAttempt': attempt,
            'producingJobId': producer['id'], 'effectiveAttempt': run['run_attempt'], 'effectiveJobId': current['id'],
            'commit': commit, 'tree': tree, 'sourceSha256': receipt['provenance']['sourceSha256']}



def main(argv=None):
    args = sys.argv[1:] if argv is None else argv
    try:
        require(args in (['core'], ['remaining']))
        platform = os.environ.get('SOBALINK_CORE_NATIVE_PLATFORM')
        identity = context(platform)
        export = Path(os.environ['RUNNER_TEMP']) / ARTIFACT
        os.umask(0o077)
        if args == ['core']:
            export.mkdir(mode=0o700, exist_ok=False)
            collect(platform, export, identity)
        else:
            require(export.is_dir() and not export.is_symlink())
            remaining(platform, export, identity)
        print('Required native package phase passed.')
        return 0
    except (Exception, KeyboardInterrupt):
        print('FAIL: Required native package phase is incomplete.')
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
