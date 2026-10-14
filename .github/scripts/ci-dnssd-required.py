#!/usr/bin/env python3
"""Required same-host DNS-SD proof; optional diagnostics cannot satisfy this gate."""
import datetime
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import sys
import zipfile

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
REPOSITORY = 'webkaz-labs/sobalink'
WORKFLOW = '.github/workflows/ci.yml'
JOB = 'native (macos-26, darwin, arm64)'
STEPS = ('Verify macOS five-scope discovery without skips', 'Preserve required macOS discovery proof')
ARTIFACT = 'soba-dnssd-required'
MAX_BYTES = 1024 * 1024
SHA = re.compile('[0-9a-f]{40}\\Z')
HASH = re.compile('[0-9a-f]{64}\\Z')
LOADED_FILES = ('ci-dnssd-required.py', 'ci-dnssd-diagnostic.py', 'ci-dnssd-terminal.py',
                'ci-nearby-native.py', 'ci-nearby-diagnostic.py', 'ci-go-test.py')


def require(ok, message='required DNS-SD proof differs'):
    if not ok:
        raise ValueError(message)


def module(name):
    path = Path(__file__).with_name(name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


diag = module('ci-dnssd-diagnostic')
POLICY_FILES = tuple(dict.fromkeys((*diag.POLICY_FILES, WORKFLOW,
    '.github/workflows/prerelease.yml', '.github/scripts/ci-dnssd-required.py',
    '.github/scripts/ci-dnssd-terminal-marker.py', '.github/scripts/test_ci_dnssd_required.py',
    '.github/scripts/ci-coverage.py', '.github/scripts/ci_full_validation.py',
    '.github/scripts/ci-metrics.py', '.github/scripts/check-frontend.py',
    'cmd/prepare-engine/main.go', 'internal/nearby/provider_raw.go',
    'internal/nearby/records.go')))


def positive(value):
    return type(value) is int and value > 0


IDENTITY_PREDICATES = frozenset({
    'identity_environment', 'identity_repository', 'identity_checkout', 'identity_event',
    'identity_pr_source', 'identity_pr_parents', 'identity_merge_hint',
    'identity_workflow_ref', 'identity_run_attempt',
})


def identity_require(ok, predicate):
    require(predicate in IDENTITY_PREDICATES)
    require(ok, predicate)


def ci_identity(event, commit, tree):
    env = os.environ
    identity_require(Path.cwd().resolve() == ROOT and env.get('GITHUB_REPOSITORY') == REPOSITORY
            and env.get('GITHUB_WORKFLOW') == 'Cross-platform CI' and env.get('GITHUB_JOB') == 'native'
            and type(commit) is str and SHA.fullmatch(commit) and type(tree) is str and SHA.fullmatch(tree)
            and commit == env.get('GITHUB_SHA'), 'identity_environment')
    identity_require(type(event) is dict and type(event.get('repository')) is dict, 'identity_repository')
    metadata, kind, ref = event['repository'], env.get('GITHUB_EVENT_NAME'), env.get('GITHUB_REF')
    identity_require(metadata.get('full_name') == REPOSITORY and metadata.get('private') is False
            and metadata.get('default_branch') == 'main', 'identity_repository')
    native = module('ci-nearby-native')
    identity_require(native.checked(['git', 'rev-parse', 'HEAD', 'HEAD^{tree}']).decode('ascii').splitlines()
                     == [commit, tree], 'identity_checkout')
    event_head = commit
    if kind == 'pull_request':
        pr, number = event.get('pull_request'), event.get('number')
        identity_require(type(pr) is dict and positive(number) and ref == f'refs/pull/{number}/merge', 'identity_event')
        base, head = pr.get('base'), pr.get('head')
        identity_require(type(base) is dict and type(head) is dict
                and type(base.get('repo')) is dict and base['repo'].get('full_name') == REPOSITORY
                and base.get('ref') == 'main', 'identity_pr_source')
        base_sha, event_head = base.get('sha'), head.get('sha')
        identity_require(all(type(value) is str and SHA.fullmatch(value) and set(value) != {'0'}
                             for value in (base_sha, event_head)) and base_sha != event_head, 'identity_pr_source')
        head_repo = head.get('repo')
        identity_require(head_repo is None or type(head_repo) is dict
                and type(head_repo.get('full_name')) is str
                and re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', head_repo['full_name']), 'identity_pr_source')
        # Match the existing impact policy: the checked-out SHA, tree and exact
        # ordered parents prove the tested merge. The mergeability hint may lag
        # or be absent; it never supplies identity or rescues mismatched parents.
        parents = native.checked(['git', 'rev-list', '--parents', '-n', '1', commit]).decode('ascii').split()
        identity_require(parents == [commit, base_sha, event_head], 'identity_pr_parents')
        hint = pr.get('merge_commit_sha')
        identity_require(hint is None or type(hint) is str and SHA.fullmatch(hint) and set(hint) != {'0'},
                         'identity_merge_hint')
    else:
        identity_require(kind in {'push', 'workflow_dispatch', 'schedule'} and type(ref) is str
                and (ref == 'refs/heads/main' or kind == 'workflow_dispatch'
                     and re.fullmatch(r'refs/heads/[A-Za-z0-9][A-Za-z0-9._/-]{0,200}', ref)), 'identity_event')
        if kind == 'push':
            identity_require(event.get('after') == commit, 'identity_event')
    identity_require(env.get('GITHUB_WORKFLOW_REF') == REPOSITORY + '/' + WORKFLOW + '@' + ref,
                     'identity_workflow_ref')
    run_id, attempt = env.get('GITHUB_RUN_ID', ''), env.get('GITHUB_RUN_ATTEMPT', '')
    identity_require(re.fullmatch('[1-9][0-9]{0,19}', run_id) and re.fullmatch('[1-9][0-9]{0,5}', attempt),
                     'identity_run_attempt')
    return {'repository': REPOSITORY, 'workflow': WORKFLOW, 'job': JOB, 'runId': int(run_id),
            'runAttempt': int(attempt), 'event': kind, 'eventHead': event_head, 'commit': commit, 'tree': tree}


def source_inventory():
    native, shared = module('ci-nearby-native'), module('ci-nearby-diagnostic')
    require(not native.checked(['git', 'status', '--porcelain', '--untracked-files=no']).strip())
    raw = native.checked(['git', 'ls-files', '--others', '-z'])
    require(not raw or raw.endswith(b'\0'))
    # Ordinary CI prepared these exact roots. No source subtree or wildcard
    # cache is excluded; helpers execute bound source bytes, never pyc files.
    cache = re.compile(rb'\.github/scripts/__pycache__/[A-Za-z0-9_-]+\.cpython-[0-9]{2,3}(?:\.opt-[12])?\.pyc\Z')
    retained = [name for name in raw.split(b'\0') if name and not cache.fullmatch(name)]
    shared.validate_untracked(b'\0'.join(retained) + (b'\0' if retained else b''), after_build=True)
    for prefix in shared.DEPENDENCY_PREFIXES:
        for path in (ROOT / prefix.rstrip('/'), (ROOT / prefix.rstrip('/')).parent):
            require(not path.is_symlink())
    names = native.checked(['git', 'ls-files', '-z'])
    require(names and names.endswith(b'\0'))
    paths = [name.decode('utf-8') for name in names[:-1].split(b'\0')]
    require(len(paths) == len(set(paths)))
    for name in paths:
        require(not any(part in ('', '.', '..') for part in name.split('/')) and '\\' not in name)
        path = ROOT / name
        require(path.is_file() and not any(parent.is_symlink() for parent in (path, *path.parents) if parent != ROOT.parent))
    return {name: native.digest(ROOT / name) for name in paths}


def exact_shape(value, template):
    """Keep every exported nested field closed, including bool/int distinctions."""
    require(type(value) is type(template))
    if type(template) is dict:
        require(set(value) == set(template))
        for name in template:
            exact_shape(value[name], template[name])
    elif type(template) is list:
        require(len(value) == len(template))
        for actual, expected in zip(value, template):
            exact_shape(actual, expected)


def validate_report(value):
    require(type(value) is dict and set(value) == set(diag.empty_result()))
    for name, initial in diag.empty_result().items():
        if type(initial) is bool:
            require(type(value[name]) is bool)
    require(type(value['schema']) is int and value['schema'] == 1)
    require(all(value[name] is True for name in ('diagnosticPassed', 'cCompilePassed', 'cLinkPassed',
        'goCompilePassed', 'eventsAccepted', 'selectedInterfaceVerified', 'rawReceiveVerified', 'legacySerializationVerified')))
    require(all(value[name] is False for name in ('networkMulticastVerified', 'physicalLANVerified',
        'automaticCoreBrowserVerified', 'releaseValidation')))
    require(all(value[name] is None for name in ('compileFailureKind', 'compileFailureLocation', 'preflightFailureKind')))
    require(value['processStatus'] == 'completed' and value['legacyReceivePath'] == 'selected_interface')
    require(type(value['capabilities']) is dict and set(value['capabilities']) == set(diag.CASES.values()))
    for scope, proof in value['capabilities'].items():
        parsed = diag.legacy_capability(proof) if scope == diag.LEGACY_SCOPE else diag.capability(proof, scope)
        require(parsed['status'] == 'verified' and parsed['ownersJoined'] is True)
        if scope == diag.LEGACY_SCOPE:
            require(parsed['receivePath'] == 'selected_interface')
    launch = module('ci-dnssd-terminal').receipt(value['terminalLaunch'])
    require(launch['mode'] == 'native_comparison' and launch['status'] == 'observed'
            and launch['reason'] == 'observed' and launch['nativeProcessStatus'] == 'completed'
            and type(launch['nativeExitCode']) is int and launch['nativeExitCode'] == 0
            and launch['nativeOriginVerified'] is True and launch['nativeOwnerJoined'] is True
            and launch['supervisorTerminationObserved'] is True and launch['policyDeniedObserved'] is False)
    collector = value['collector']
    template = diag.collector_observation(b'', b'', 'completed', 0, b'', b'')
    exact_shape(collector, template)
    require(collector['converterState'] == 'completed' and collector['converterExitCode'] == 0
            and collector['acceptanceStatus'] == 'accepted' and collector['eventScanStatus'] == 'scanned'
            and collector['countsCapped'] is False and collector['incompleteOutputLines'] == 0
            and collector['incompleteMarkerLines'] == 0 and collector['nativeStderrBytes'] == 0
            and collector['converterStderrBytes'] == 0 and collector['converterStderrClass'] == 'empty')
    require(0 < collector['nativeStdoutBytes'] <= 65536 and 0 < collector['converterStdoutBytes'] <= 131072
            and 0 < collector['outputEventCount'] < collector['eventCount'] <= 4096)
    require(collector['packageLifecycleCounts'] == {'start': 1, 'pass': 1, 'skip': 0, 'fail': 0, 'other': 0})
    for name in diag.CASES:
        require(collector['testLifecycleCounts'][name] == {'run': 1, 'pass': 1, 'skip': 0, 'fail': 0, 'other': 0}
                and collector['completeMarkerLineCounts'][name] == 1)
    for stream, observed in collector['rawStreams'].items():
        expected_count = int(stream == 'stdout')
        require(observed['passTokenSeen'] is bool(expected_count) and observed['failTokenSeen'] is False
                and observed['noTestsTokenSeen'] is False)
        for name in diag.CASES:
            require(observed['runTokenCounts'][name] == expected_count
                    and observed['terminalTokenCounts'][name] == {'pass': expected_count, 'skip': 0, 'fail': 0})
        for counts in ('markerTokenCounts', 'completeMarkerLineCounts'):
            require(observed[counts] == {'standard': 4 * expected_count, 'legacy': expected_count})
    return value


def validate_provenance(value, identity, policy_hashes):
    keys = {'schema', 'stage', 'commit', 'tree', 'ciIdentity', 'sourceFiles', 'sourceSha256',
        'sourceRechecked', 'toolchainRechecked', 'privateCleanupVerified', 'goObserved', 'runnerObserved',
        'toolchain', 'platform', 'compilerObserved', 'sdkVersionObserved', 'sdkBuildObserved', 'compiler',
        'sdkFiles', 'toolchainSha256', 'tests', 'policySha256', 'modules', 'cObjectSha256', 'cLinkedSha256',
        'linkage', 'cLinkLibraries', 'testSha256', 'launchMethod', 'originVerification', 'testDriverDenialFence'}
    require(type(value) is dict and set(value) == keys and type(value['schema']) is int and value['schema'] == 1)
    require(value['stage'] == 'passed' and value['ciIdentity'] == identity
            and value['commit'] == identity['commit'] and value['tree'] == identity['tree']
            and type(value['sourceFiles']) is int and 0 < value['sourceFiles'] <= 100000
            and value['policySha256'] == policy_hashes and value['tests'] == diag.CASES
            and value['modules'] == list(module('ci-nearby-native').MODULES))
    require(all(value[key] is True for key in ('sourceRechecked', 'toolchainRechecked', 'privateCleanupVerified', 'testDriverDenialFence')))
    expected_go = {'GOVERSION': 'go1.27.1', 'GOHOSTOS': 'darwin', 'GOHOSTARCH': 'arm64', 'GOOS': 'darwin', 'GOARCH': 'arm64', 'CGO_ENABLED': '1'}
    require(value['goObserved'] == expected_go and value['runnerObserved'] == {'os': 'macOS', 'arch': 'ARM64', 'pythonArch': 'arm64'}
            and value['toolchain'] == {'go': 'go1.27.1', 'goos': 'darwin', 'goarch': 'arm64', 'runner': 'macos-26', 'race': True}
            and type(value['toolchain']['race']) is bool and value['platform'] == 'darwin-arm64')
    compiler = value['compiler']
    require(type(compiler) is dict and set(compiler) == {'vendor', 'version', 'build', 'sdkVersion', 'sdkBuild'})
    checked = diag.compiler_identity(('Apple clang version ' + compiler['version'] + ' (clang-' + compiler['build'] + ')').encode(), compiler['sdkVersion'], compiler['sdkBuild'])
    require(compiler == checked and value['compilerObserved'] == {key: compiler[key] for key in ('vendor', 'version', 'build')}
            and value['sdkVersionObserved'] == compiler['sdkVersion'] and value['sdkBuildObserved'] == compiler['sdkBuild'])
    sdk, hashes = value['sdkFiles'], value['toolchainSha256']
    require(type(sdk) is dict and set(sdk) == set(diag.SDK_FILES) and all(item in {'available', 'missing'} for item in sdk.values())
            and all(sdk[key] == 'available' for key in diag.REQUIRED_SDK_FILES))
    require(type(hashes) is dict and set(hashes) == {'clang', 'go', 'sdk'} and type(hashes['sdk']) is dict
            and set(hashes['sdk']) == {name for name in sdk if sdk[name] == 'available'})
    for item in (value['sourceSha256'], value['cObjectSha256'], value['cLinkedSha256'], value['testSha256'], hashes['clang'], hashes['go'], *hashes['sdk'].values()):
        require(type(item) is str and HASH.fullmatch(item))
    require(value['linkage'] == 'implicit-system-umbrella' and type(value['cLinkLibraries']) is list
            and len(value['cLinkLibraries']) == len(set(value['cLinkLibraries']))
            and set(value['cLinkLibraries']).issubset(diag.LINK_LIBRARIES.values())
            and 'libSystem.B.dylib' in value['cLinkLibraries']
            and value['launchMethod'] == 'ordinary_terminal_command'
            and value['originVerification'] == 'exact-four-hop-kernel-path-with-final-recheck')


def validate_receipt(value, identity, policy_hashes):
    require(type(value) is dict and set(value) == {'schema', 'requiredPassed', 'result', 'provenance'}
            and type(value['schema']) is int and value['schema'] == 1 and value['requiredPassed'] is True)
    require(type(identity) is dict and set(identity) == {'repository', 'workflow', 'job', 'runId', 'runAttempt', 'event', 'eventHead', 'commit', 'tree'}
            and positive(identity['runId']) and positive(identity['runAttempt'])
            and identity['repository'] == REPOSITORY and identity['workflow'] == WORKFLOW and identity['job'] == JOB
            and identity['event'] in {'pull_request', 'push', 'workflow_dispatch', 'schedule'}
            and all(type(identity[name]) is str and SHA.fullmatch(identity[name]) for name in ('eventHead', 'commit', 'tree')))
    # Equality alone would allow JSON true to masquerade as run/attempt 1.
    exact_shape(value['provenance']['ciIdentity'], identity)
    require(set(policy_hashes) == set(POLICY_FILES) and all(type(item) is str and HASH.fullmatch(item) for item in policy_hashes.values()))
    validate_report(value['result'])
    validate_provenance(value['provenance'], identity, policy_hashes)
    return value


def write_receipt(export, report, provenance):
    value = {'schema': 1, 'requiredPassed': False, 'result': report, 'provenance': provenance}
    try:
        value['requiredPassed'] = True
        validate_receipt(value, provenance['ciIdentity'], provenance['policySha256'])
    except Exception:
        value['requiredPassed'] = False
    with (export / 'receipt.json').open('x', encoding='utf-8') as output:
        json.dump(value, output, sort_keys=True, allow_nan=False)
        output.write('\n')
    validate_receipt(module('ci-nearby-diagnostic').parse_json((export / 'receipt.json').read_text()),
                     provenance['ciIdentity'], provenance['policySha256'])


def timestamp(value):
    require(type(value) is str)
    parsed = datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))
    require(parsed.tzinfo is not None)
    return parsed


def pages(api, path, key):
    rows, ids, total = [], set(), None
    for page in range(1, 11):
        data = api(path + f'?per_page=100&page={page}')
        count, batch = data.get('total_count'), data.get(key)
        require(type(count) is int and count >= 0 and type(batch) is list and len(batch) <= 100
                and (total is None or count == total))
        total = count
        for row in batch:
            require(type(row) is dict and positive(row.get('id')) and row['id'] not in ids)
            ids.add(row['id'])
            rows.append(row)
        require(len(rows) <= count)
        if len(batch) < 100:
            require(len(rows) == count)
            return rows
    raise ValueError('DNS-SD artifact inventory exceeded')


def native_job(api, run, attempt):
    jobs = pages(api, f"actions/runs/{run['id']}/attempts/{attempt}/jobs", 'jobs')
    selected = [job for job in jobs if job.get('name') == JOB]
    require(len(selected) == 1)
    job = selected[0]
    require(job.get('run_id') == run['id'] and type(job.get('run_id')) is int
            and job.get('run_attempt') == attempt and type(job.get('run_attempt')) is int
            and job.get('head_sha') == run['head_sha'] and 'macos-26' in job.get('labels', [])
            and job.get('status') == 'completed' and job.get('conclusion') == 'success')
    require(timestamp(job['started_at']) <= timestamp(job['completed_at']))
    for name in STEPS:
        steps = [step for step in job.get('steps', []) if step.get('name') == name]
        require(len(steps) == 1 and steps[0].get('status') == 'completed' and steps[0].get('conclusion') == 'success')
        require(timestamp(job['started_at']) <= timestamp(steps[0]['started_at'])
                <= timestamp(steps[0]['completed_at']) <= timestamp(job['completed_at']))
    execution, exported = (next(step for step in job['steps'] if step['name'] == name) for name in STEPS)
    require(timestamp(execution['completed_at']) <= timestamp(exported['started_at']),
            'Mac proof export precedes required execution')
    return job


def download_artifact(identifier):
    require(positive(identifier))
    # Reuse the CI token already held by gh. No credential enters argv or the
    # receipt; no new grant, login or persistent access is requested.
    state, code, raw, _ = module('ci-nearby-native').capture([
        'gh', 'api', '--method', 'GET', 'repos/' + REPOSITORY + f'/actions/artifacts/{identifier}/zip'],
        timeout=60, limit=MAX_BYTES)
    require(state == 'completed' and code == 0 and 0 < len(raw) <= MAX_BYTES)
    return raw


def artifact_receipt(raw, artifact):
    require(type(raw) is bytes and 0 < len(raw) <= MAX_BYTES
            and type(artifact.get('size_in_bytes')) is int and len(raw) == artifact['size_in_bytes']
            and artifact.get('digest') == 'sha256:' + hashlib.sha256(raw).hexdigest())
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        members = archive.infolist()
        require(len(members) == 1 and members[0].filename == 'receipt.json'
                and not members[0].is_dir() and members[0].flag_bits & 1 == 0
                and 0 < members[0].file_size <= MAX_BYTES
                and members[0].compress_type in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED)
                and (members[0].external_attr >> 16) & 0o170000 != 0o120000)
        data = archive.read(members[0])  # ZIP CRC is checked before JSON admission.
    require(len(data) == members[0].file_size)
    return module('ci-nearby-diagnostic').parse_json(data.decode('utf-8')), hashlib.sha256(data).hexdigest()


def audit_artifact(api, run, commit, tree, *, download=None, policy_hashes=None):
    require(positive(run.get('id')) and positive(run.get('run_attempt'))
            and run.get('name') == 'Cross-platform CI' and run.get('path') == WORKFLOW
            and type(commit) is str and SHA.fullmatch(commit) and type(tree) is str and SHA.fullmatch(tree))
    current = native_job(api, run, run['run_attempt'])
    artifacts = pages(api, f"actions/runs/{run['id']}/artifacts", 'artifacts')
    seen, candidates = set(), []
    for artifact in artifacts:
        name = artifact.get('name', '')
        if not name.startswith(ARTIFACT + '-'):
            continue
        match = re.fullmatch(re.escape(ARTIFACT) + rf"-{run['id']}-([1-9][0-9]{{0,5}})", name)
        require(match is not None and name not in seen)
        seen.add(name)
        attempt = int(match[1])
        require(attempt <= run['run_attempt'])
        created = timestamp(artifact['created_at'])
        if timestamp(current['started_at']) <= created <= timestamp(current['completed_at']):
            candidates.append((artifact, attempt))
    require(len(candidates) == 1, 'exact effective macOS artifact is missing or ambiguous')
    artifact, attempt = candidates[0]
    producer = current
    if attempt != run['run_attempt']:
        previous = api(f"actions/runs/{run['id']}/attempts/{attempt}")
        require(previous.get('id') == run['id'] and type(previous.get('id')) is int
                and previous.get('run_attempt') == attempt and type(previous.get('run_attempt')) is int
                and previous.get('head_sha') == run['head_sha'] and previous.get('event') == run['event']
                and previous.get('name') == run['name'] and previous.get('path') == WORKFLOW
                and previous.get('head_branch') == run.get('head_branch') and previous.get('status') == 'completed'
                and timestamp(previous['run_started_at']) < timestamp(run['run_started_at']))
        producer = native_job(api, run, attempt)
        require(all(producer[key] == current[key] for key in ('started_at', 'completed_at')),
                'previous success was not carried into the effective attempt')
        for name in STEPS:
            old = next(step for step in producer['steps'] if step['name'] == name)
            effective = next(step for step in current['steps'] if step['name'] == name)
            require(all(old[key] == effective[key] for key in ('started_at', 'completed_at', 'status', 'conclusion')))
    exported = next(step for step in producer['steps'] if step['name'] == STEPS[1])
    require(timestamp(exported['started_at']) <= timestamp(artifact['created_at']) <= timestamp(exported['completed_at'])
            and timestamp(artifact['created_at']) <= timestamp(artifact['updated_at']) <= timestamp(producer['completed_at'])
            and artifact.get('expired') is False)
    origin = artifact.get('workflow_run', {})
    require(origin.get('id') == run['id'] and type(origin.get('id')) is int
            and origin.get('head_sha') == run['head_sha'] and origin.get('head_branch') == run.get('head_branch'))
    require(api(f"actions/artifacts/{artifact['id']}") == artifact, 'artifact identity changed')
    raw = (download or download_artifact)(artifact['id'])
    receipt, receipt_hash = artifact_receipt(raw, artifact)
    identity = {'repository': REPOSITORY, 'workflow': WORKFLOW, 'job': JOB, 'runId': run['id'],
        'runAttempt': attempt, 'event': run['event'], 'eventHead': run['head_sha'], 'commit': commit, 'tree': tree}
    if policy_hashes is None:
        policy_hashes = {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in POLICY_FILES}
    validate_receipt(receipt, identity, policy_hashes)
    require(api(f"actions/artifacts/{artifact['id']}") == artifact, 'artifact identity changed after admission')
    require(native_job(api, run, run['run_attempt']) == current, 'effective Mac job changed during admission')
    return {'artifactId': artifact['id'], 'artifactSha256': hashlib.sha256(raw).hexdigest(),
            'receiptSha256': receipt_hash, 'runId': run['id'], 'producingAttempt': attempt,
            'producingJobId': producer['id'], 'effectiveAttempt': run['run_attempt'], 'effectiveJobId': current['id'],
            'commit': commit, 'tree': tree, 'sourceSha256': receipt['provenance']['sourceSha256']}


def main(argv=None):
    return diag.main(argv, required=sys.modules[__name__])


if __name__ == '__main__':
    raise SystemExit(main())
