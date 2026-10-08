"""Read-only, exact-source full-CI proof. Never reuse another run's jobs."""
import ast
import base64
import datetime
import hashlib
import json
from pathlib import Path
import re

REPOSITORY = 'webkaz-labs/sobalink'
WORKFLOW = '.github/workflows/ci.yml'
SOURCES = (WORKFLOW, '.github/scripts/ci-impact.py', '.github/scripts/ci-coverage.py',
           '.github/scripts/ci_full_validation.py', '.github/scripts/release-validation.py')
TARGETS = {
    'linux-amd64': ('native (ubuntu-24.04, linux, amd64)', 'ubuntu-24.04'),
    'linux-arm64': ('native (ubuntu-24.04-arm, linux, arm64)', 'ubuntu-24.04-arm'),
    'darwin-arm64': ('native (macos-26, darwin, arm64)', 'macos-26'),
    'windows-amd64': ('native (windows-2025, windows, amd64)', 'windows-2025'),
}
MAX_PAGES = 10


def require(ok, message):
    if not ok:
        raise ValueError(message)


def positive(value):
    return type(value) is int and value > 0


def digest(data):
    return hashlib.sha256(data).hexdigest()


def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode('utf-8')


def timestamp(value):
    require(isinstance(value, str), 'missing CI attempt time')
    try:
        result = datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))
    except ValueError as exc:
        raise ValueError('invalid CI attempt time') from exc
    require(result.tzinfo is not None, 'CI attempt time lacks timezone')
    return result


def pages(api, path, key):
    rows, identifiers, total = [], set(), None
    separator = '&' if '?' in path else '?'
    for page in range(1, MAX_PAGES + 1):
        data = api(path + separator + f'per_page=100&page={page}')
        count, batch = data.get('total_count'), data.get(key)
        require(type(count) is int and count >= 0 and isinstance(batch, list) and len(batch) <= 100,
                'invalid CI inventory')
        require(total is None or total == count, 'CI inventory changed during pagination')
        total = count
        for item in batch:
            require(isinstance(item, dict) and positive(item.get('id')) and item['id'] not in identifiers,
                    'duplicate or invalid CI inventory ID')
            identifiers.add(item['id'])
            rows.append(item)
        require(len(rows) <= total, 'CI inventory count mismatch')
        if len(batch) < 100:
            require(len(rows) == total, 'incomplete CI inventory')
            return rows
    raise ValueError('CI inventory exceeds audit limit')


def identity(run, commit):
    require(run.get('head_sha') == commit and run.get('head_branch') == 'main'
            and run.get('name') == 'Cross-platform CI' and run.get('path') == WORKFLOW
            and run.get('event') in ('push', 'workflow_dispatch')
            and positive(run.get('id')) and positive(run.get('run_attempt')),
            'unexpected canonical CI identity')


def attempt_time(api, run, commit):
    identity(run, commit)
    started, created = timestamp(run.get('run_started_at')), timestamp(run.get('created_at'))
    require(started >= created, 'CI attempt precedes run creation')
    if run['run_attempt'] > 1:
        previous = api(f"actions/runs/{run['id']}/attempts/{run['run_attempt'] - 1}")
        identity(previous, commit)
        require(previous['id'] == run['id'] and previous['run_attempt'] == run['run_attempt'] - 1
                and started > timestamp(previous.get('run_started_at')),
                'ambiguous CI rerun chronology')
    return started


def select_candidate(api, commit):
    """Every manual dispatch is full intent; never select only successful runs."""
    require(bool(re.fullmatch('[0-9a-f]{40}', commit)), 'invalid full-validation source')
    runs = pages(api, f'actions/workflows/ci.yml/runs?branch=main&head_sha={commit}', 'workflow_runs')
    require(bool(runs), 'CI has not run for this source')
    times = {}
    for run in runs:
        times[run['id']] = attempt_time(api, run, commit)
    manual = [run for run in runs if run['event'] == 'workflow_dispatch']
    require(bool(manual), 'dispatch Cross-platform CI with force_full for this exact main source')
    # An in-flight manual full validation is unresolved even if it started first.
    require(all(run.get('status') == 'completed' for run in manual), 'manual full validation is pending')
    newest = max(times[run['id']] for run in manual)
    selected = [run for run in manual if times[run['id']] == newest]
    require(len(selected) == 1, 'ambiguous latest full-validation attempt')
    selected = selected[0]
    require(selected.get('conclusion') == 'success', 'latest full validation did not succeed')
    for run in runs:
        if times[run['id']] >= newest:
            require(run.get('status') == 'completed' and run.get('conclusion') == 'success',
                    'newer canonical CI has unresolved or failed checks')
    return selected


def source_bytes(api, commit, local_root=None):
    sources = {}
    for path in SOURCES:
        item = api(f'contents/{path}?ref={commit}')
        require(item.get('type') == 'file' and item.get('path') == path
                and item.get('encoding') == 'base64' and type(item.get('size')) is int
                and 0 < item['size'] <= 1024 * 1024, 'invalid source policy object')
        try:
            data = base64.b64decode(''.join(item['content'].split()), validate=True)
        except (KeyError, ValueError, TypeError) as exc:
            raise ValueError('invalid encoded source policy') from exc
        require(len(data) == item['size'], 'incomplete source policy')
        # Bind bytes to Git's immutable blob identity, not only the requested URL.
        blob = hashlib.sha1(b'blob ' + str(len(data)).encode('ascii') + b'\0' + data).hexdigest()
        require(item.get('sha') == blob, 'source policy blob mismatch')
        if local_root is not None:
            require((Path(local_root) / path).read_bytes() == data, 'checkout policy differs from exact source')
        sources[path] = data
    # Publication consumes these two scripts from the gate artifact, not a new
    # checkout. Verify both loaded tools still match the immutable source.
    for name in ('ci_full_validation.py', 'release-validation.py'):
        require(Path(__file__).with_name(name).read_bytes() == sources['.github/scripts/' + name],
                'loaded release tool differs from exact source')
    return sources


def required_jobs(sources):
    """Read literal required-step constants from the protected source; no exec."""
    tree = ast.parse(sources['.github/scripts/ci-coverage.py'].decode('utf-8'))
    names = {'FAST_STEPS', 'LONG_STEPS', 'BROWSER_STEPS'}
    values = {}
    for node in tree.body:
        if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name):
            name = node.targets[0].id
            if name in names:
                require(name not in values, 'duplicate required-step policy')
                values[name] = ast.literal_eval(node.value)
    require(set(values) == names, 'missing required-step policy')
    for value in values.values():
        require(isinstance(value, tuple) and value and all(isinstance(v, str) and v for v in value)
                and len(set(value)) == len(value), 'invalid required-step policy')
    require(len(values['LONG_STEPS']) == 3, 'unexpected real-time gate policy')
    result = {name: values['FAST_STEPS'] + values['LONG_STEPS'] for name, _ in TARGETS.values()}
    result[TARGETS['windows-amd64'][0]] += ('Verify Windows receive-retirement directory barriers',)
    result.update({
        'impact': ('Select minimum CI scope',),
        'browser': values['BROWSER_STEPS'],
        'manifest-smoke': ('Exercise signing and verification offline with a disposable test key',),
        'ci-required': ('Require every check selected for this change',),
        'web-activation': ('Prepare locked dependencies and official Chromium', 'Verify fixed Web acceptance policy',
                           'Compile and run seven private synthetic-owner cases'),
        'product-activation': ('Prepare locked dependencies and official Chromium', 'Verify fixed product acceptance policy',
                               'Build normal assets and run two private production-entry cases'),
    })
    return result


def audit_jobs(api, run, required):
    jobs = pages(api, f"actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs", 'jobs')
    index = {}
    for job in jobs:
        require(job.get('run_id') == run['id'] and job.get('run_attempt') == run['run_attempt']
                and job.get('head_sha') == run['head_sha'] and isinstance(job.get('name'), str)
                and job['name'] not in index, 'invalid effective CI job identity')
        index[job['name']] = job
    proof = []
    for name, steps in sorted(required.items()):
        job = index.get(name, {})
        require(job.get('status') == 'completed' and job.get('conclusion') == 'success',
                'required full-validation job did not pass: ' + name)
        for expected, label in TARGETS.values():
            if name == expected:
                require(label in job.get('labels', []), 'native CI runner target mismatch')
        selected = []
        for step in steps:
            matches = [s for s in job.get('steps', []) if s.get('name') == step]
            require(len(matches) == 1 and matches[0].get('status') == 'completed'
                    and matches[0].get('conclusion') == 'success', 'required full-validation step did not pass: ' + step)
            selected.append(step)
        # GitHub may carry successful work into the effective current attempt
        # with new IDs and original timestamps. Do not demand physical reruns.
        proof.append({'name': name, 'id': job['id'], 'effective_attempt': run['run_attempt'],
                      'required_steps_passed': selected})
    return proof


def fingerprint(run):
    result = {key: run.get(key) for key in ('id', 'run_attempt', 'head_sha', 'head_branch', 'name', 'path',
                                           'event', 'status', 'conclusion', 'created_at', 'run_started_at')}
    result['head_commit_id'] = run.get('head_commit', {}).get('id')
    result['head_tree'] = run.get('head_commit', {}).get('tree_id')
    return result


def audit(api, commit, latest=None, local_root=None, previous_proof=None):
    repository = api('')
    require(repository.get('full_name') == REPOSITORY and repository.get('private') is False
            and repository.get('default_branch') == 'main', 'canonical release repository changed')
    require(api('git/ref/heads/main').get('object', {}).get('sha') == commit, 'main moved before full-CI audit')
    run = latest if latest is not None else select_candidate(api, commit)
    identity(run, commit)
    metadata = api(f"actions/runs/{run['id']}")
    require(fingerprint(metadata) == fingerprint(run), 'full-CI attempt changed before audit')
    commit_info = api('git/commits/' + commit)
    tree = commit_info.get('tree', {}).get('sha')
    require(commit_info.get('sha') == commit and bool(re.fullmatch('[0-9a-f]{40}', tree or ''))
            and run.get('head_commit', {}).get('id') == commit
            and run['head_commit'].get('tree_id') == tree, 'full-CI source tree mismatch')
    sources = source_bytes(api, commit, local_root)
    hashes = {path: digest(data) for path, data in sources.items()}
    if previous_proof is not None:
        require(set(previous_proof) == {'schema', 'repository', 'head_sha', 'head_tree', 'source_sha256',
                                        'full_validation', 'jobs', 'required_jobs_sha256'}
                and type(previous_proof.get('schema')) is int and previous_proof['schema'] == 1 and previous_proof.get('repository') == REPOSITORY
                and previous_proof.get('head_sha') == commit and previous_proof.get('head_tree') == tree
                and previous_proof.get('source_sha256') == hashes, 'stored full-CI proof source mismatch')
    required = required_jobs(sources)
    jobs = audit_jobs(api, run, required)
    current = select_candidate(api, commit)
    require(fingerprint(current) == fingerprint(run), 'full-CI selection changed during audit')
    require(fingerprint(api(f"actions/runs/{run['id']}")) == fingerprint(run), 'full-CI attempt changed during audit')
    require(api('git/ref/heads/main').get('object', {}).get('sha') == commit, 'main moved during full-CI audit')
    proof = {'schema': 1, 'repository': REPOSITORY, 'head_sha': commit, 'head_tree': tree,
             'source_sha256': hashes, 'full_validation': fingerprint(run), 'jobs': jobs,
             'required_jobs_sha256': digest(encoded(required))}
    if previous_proof is not None:
        proof['gate_proof_sha256'] = digest(encoded(previous_proof))
    return proof
