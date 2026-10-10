#!/usr/bin/env python3
"""One bounded hosted B1/F-S1 diagnostic; private capture, closed exports only.

Measured runner hashes identify observed inputs, not approved-source provenance.
A hard job teardown is relied-on failure containment, never test cleanup proof.
No effects occur on import. Publication and branch creation are external gates.
"""
import datetime
import errno
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import subprocess
import sys
import threading
import time

ROOT = Path(__file__).resolve().parents[2]
REPOSITORY = 'webkaz-labs/sobalink'
BRANCH = 'diagnostic-resource/browser-prewrite-743a1eb7-v1'
TAGS = 'resource_browser_native,resource_group_catalog_native,resource_management_native,resource_inspection_native,directlan_activation_native,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy'
REVIEWED_PACKAGES_SHA256 = 'ecf3b7eeaf53f5c5b169fda2f84897c2250b358347350bfd6efca6335e491b58'
REVIEWED_ACTIVE_GO_SHA256 = 'b79719f5a81260e8028a2805414f15ed4f2d068e48c8216239409b3e0eb14e02'
REVIEWED_REGISTRY_SHA256 = '48b6553508f8f3a64d58cb9bc5f69fd256d854957ae4975badd1f08c840aa05b'
CORE = 'github.com/webkaz-labs/sobalink/internal/core'
SAFE = sorted(('TestGroupOwnedStoreFirstUseIsReadOnly', 'TestGroupOwnedStorePrewriteFailureKeepsConfirmedSnapshot', 'TestGroupCoordinatorFailedPublicationKeepsLastConfirmedEvidence'))
B1 = 'TestResourceBrowserNativeLocalCatalog'
FS1 = 'TestResourceGroupNativeAcceptedPrewriteRefusal'
TITLE = 'resource-native-local-catalog-en-settings'
GREP = '(?:^| )resource-native-local-catalog-en-settings$'
MATCH = '**/resource-native-local-catalog.acceptance.mjs'
NATIVE_KEYS = ('SOBALINK_RUN_ACTIVATION_NATIVE', 'SOBALINK_RUN_MANAGED_RESTART_NATIVE', 'SOBALINK_RUN_PRODUCT_ACTIVATION_NATIVE', 'SOBALINK_RUN_RESOURCE_BROWSER_NATIVE', 'SOBALINK_RUN_RESOURCE_GROUP_CATALOG_NATIVE', 'SOBALINK_RUN_RESOURCE_GROUP_PREWRITE_NATIVE', 'SOBALINK_RUN_RESOURCE_GROUP_RESTART_NATIVE', 'SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE', 'SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE', 'SOBALINK_RUN_RESOURCE_PROCESS_NATIVE', 'SOBALINK_RUN_WEB_ACTIVATION_NATIVE')
PHASES = ('admission', 'inputs', 'metadata', 'build', 'inspect', 'safe', 'B1', 'F-S1', 'postflight', 'complete')
PREDICATES = ('none', 'event', 'source', 'toolchain', 'browser', 'metadata', 'capture', 'timeout', 'command', 'inventory', 'receipt', 'changed', 'interrupted', 'internal', 'metadata_shape', 'metadata_packages', 'metadata_imports', 'metadata_source_hashes', 'metadata_registry', 'buildinfo_version', 'buildinfo_records', 'buildinfo_modules', 'buildinfo_settings', 'buildinfo_time')
MAX_CAPTURE = 8 << 20
MAX_PRIVATE = 1 << 30
# Reuse the existing browser CI's pure toolchain/lock/managed-path validators.
_spec = importlib.util.spec_from_file_location('resource_browser_setup_reuse', ROOT / '.github/scripts/ci-product-activation.py')
SETUP = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(SETUP)


def require(condition, category):
    if not condition:
        raise RuntimeError(category)


def sha(path):
    digest = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda: stream.read(524288), b''):
            digest.update(block)
    return digest.hexdigest()


def validate_event(values, event, checkout):
    repo = event.get('repository') or {}
    require(values.get('GITHUB_ACTIONS') == 'true' and values.get('GITHUB_EVENT_NAME') == 'create'
            and values.get('GITHUB_REPOSITORY') == REPOSITORY and values.get('GITHUB_REF') == 'refs/heads/' + BRANCH
            and values.get('GITHUB_RUN_ATTEMPT') == '1' and event.get('ref_type') == 'branch'
            and event.get('ref') == BRANCH and repo.get('full_name') == REPOSITORY
            and repo.get('private') is False and repo.get('default_branch') == 'main'
            and re.fullmatch('[0-9a-f]{40}', values.get('GITHUB_SHA', '')) is not None
            and checkout == values['GITHUB_SHA'], 'event')
    return checkout


def browser_selection(filename, title, full_title, count=1):
    return (filename == 'resource-native-local-catalog.acceptance.mjs' and title == TITLE
            and type(count) is int and count == 1 and re.search(GREP, full_title) is not None)


def exact_tests(raw, names):
    lines = raw.decode('utf-8').splitlines()
    runs, passes = [], []
    for line in lines:
        if line.startswith('=== RUN   '):
            name = line[len('=== RUN   '):]
            require(re.fullmatch('Test[A-Za-z0-9_]+', name) is not None, 'inventory')
            runs.append(name)
        elif line.startswith('--- PASS: '):
            match = re.fullmatch(r'--- PASS: (Test[A-Za-z0-9_]+) \([0-9]+(?:\.[0-9]+)?s\)', line)
            require(match is not None, 'inventory')
            passes.append(match[1])
        elif re.search(r'--- (?:FAIL|SKIP):|^FAIL$|^=== (?:PAUSE|CONT)', line) or line.lstrip().startswith(('=== RUN', '--- PASS', '--- FAIL', '--- SKIP')):
            raise RuntimeError('inventory')
    require(sorted(runs) == sorted(names) and sorted(passes) == sorted(names)
            and len(runs) == len(names) and len(passes) == len(names)
            and lines and lines[-1] == 'PASS' and lines.count('PASS') == 1, 'inventory')
    return len(names)


def read_json(path, maximum=16384):
    facts = path.lstat()
    require(stat.S_ISREG(facts.st_mode) and facts.st_uid == os.getuid() and facts.st_mode & 0o077 == 0
            and 0 < facts.st_size <= maximum, 'receipt')
    with path.open('rb') as stream:
        opened = os.fstat(stream.fileno())
        raw = stream.read(maximum + 1)
    require((facts.st_dev, facts.st_ino, facts.st_size) == (opened.st_dev, opened.st_ino, opened.st_size)
            and len(raw) == facts.st_size, 'receipt')
    return SETUP.strict_json(raw.decode('utf-8'))


def validate_b1(native, scope, browser, http):
    keys = {'schema', 'accepted', 'browserJoined', 'coreClosed', 'lockClosed', 'httpCountsOnly', 'savedServiceNavigationCovered', 'pixelReviewPerformed'}
    require(type(native) is dict and set(native) == keys and type(native['schema']) is int and native['schema'] == 1
            and all(type(native[k]) is bool for k in keys - {'schema'}), 'receipt')
    require(all(native[k] for k in ('accepted', 'browserJoined', 'coreClosed', 'lockClosed', 'httpCountsOnly'))
            and not native['savedServiceNavigationCovered'] and not native['pixelReviewPerformed'], 'receipt')
    expected_scope = {'schema', 'complete', 'descendantsReaped', 'playwrightExit', 'forced', 'deadlineExceeded', 'errors', 'observed', 'reaped'}
    require(type(scope) is dict and set(scope) == expected_scope, 'receipt')
    require(all(type(scope[k]) is bool for k in ('complete', 'descendantsReaped', 'forced', 'deadlineExceeded'))
            and all(type(scope[k]) is int for k in ('schema', 'playwrightExit', 'errors', 'observed', 'reaped')), 'receipt')
    require(scope['schema'] == 1 and scope['complete'] and scope['descendantsReaped'] and not scope['forced']
            and not scope['deadlineExceeded'] and scope['playwrightExit'] == 0 and scope['errors'] == 0
            and 0 <= scope['observed'] <= 64 and 1 <= scope['reaped'] <= 8192, 'receipt')
    require(browser == {'schema': 1, 'expected': 1, 'observed': 1, 'passed': 1, 'errors': 0,
                        'selectionValid': True, 'unexpected': False, 'accepted': True}
            and all(type(browser[k]) is int for k in ('schema', 'expected', 'observed', 'passed', 'errors'))
            and all(type(browser[k]) is bool for k in ('selectionValid', 'unexpected', 'accepted')), 'receipt')
    ints = ('schema', 'requests', 'stateRequests', 'stateReady', 'loginRequests', 'listRequests', 'listResponses', 'snapshots', 'snapshotResponses', 'blocked', 'errors', 'captures')
    require(type(http) is dict and set(http) == {*ints, 'completed', 'requestsJoined'}
            and all(type(http[k]) is int for k in ints) and type(http['completed']) is bool
            and type(http['requestsJoined']) is bool, 'receipt')
    require(http['schema'] == 1 and 1 <= http['requests'] <= 256 and 1 <= http['stateRequests'] <= 96
            and 1 <= http['stateReady'] <= http['stateRequests'] and http['loginRequests'] == 1
            and http['listRequests'] == http['listResponses'] == 2 and http['snapshots'] == http['snapshotResponses'] == 1
            and http['blocked'] == http['errors'] == 0 and http['captures'] == 2
            and http['completed'] and http['requestsJoined'], 'receipt')
    return {'httpListRequests': 2, 'httpSnapshots': 1, 'browserCases': 1, 'nativeCleanupProven': True}


def environment(home, tmp, kind='build'):
    value = {'PATH': os.environ.get('PATH', '/usr/bin:/bin'), 'HOME': str(home), 'TMPDIR': str(tmp),
             'TMP': str(tmp), 'TEMP': str(tmp), 'LANG': 'C.UTF-8', 'LC_ALL': 'C.UTF-8', 'TZ': 'UTC',
             'GOOS': 'linux', 'GOARCH': 'amd64', 'GOAMD64': 'v1', 'CGO_ENABLED': '0', 'GOMAXPROCS': '2',
             'GOTOOLCHAIN': 'local', 'GOENV': 'off', 'GOWORK': 'off', 'GOPROXY': 'off', 'GOSUMDB': 'off',
             'GOAUTH': 'off', 'GOVCS': '*:off', 'GOTELEMETRY': 'off', 'GOFLAGS': '-mod=readonly',
             'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null', 'GIT_OPTIONAL_LOCKS': '0',
             **dict.fromkeys(NATIVE_KEYS, '')}
    if kind == 'B1': value['SOBALINK_RUN_RESOURCE_BROWSER_NATIVE'] = 'reviewed-offline-catalog-settings-v1'
    if kind == 'F-S1':
        value['SOBALINK_RUN_RESOURCE_GROUP_PREWRITE_NATIVE'] = 'reviewed-accepted-prewrite-refusal-v1'
        value['SOBALINK_RUN_ACTIVATION_NATIVE'] = '1'
    return value


# These capture/retained-group bodies are byte-for-byte reviewed local reuse.
# The B1 detached tree has its OWN in-test subreaper; killpg below is not its proof.

def capture(pipe, destination):
    try:
        with destination.open('xb') as sink:
            while True:
                data = pipe.read(8192)
                if not data:
                    break
                with lock:
                    state['seen'] += len(data)
                    if state['failure'] is not None:
                        continue
                    if state['written'] + len(data) > OUTPUT_LIMIT:
                        state['failure'] = 'metadata_output_overflow'
                        continue
                    sink.write(data)
                    state['written'] += len(data)
    except BaseException:
        with lock:
            if state['failure'] is None:
                state['failure'] = 'metadata_capture_failed'
    finally:
        pipe.close()


def stop_owned_group():
    # The exact spawned leader is retained by WNOWAIT. Clearing the flag before
    # ANY reap attempt prevents an exception path from signalling a reused group.
    require(proc is not None and leader_unreaped, 'owned_leader_not_retained')
    try:
        os.killpg(proc.pid, signal.SIGKILL)
        receipt['groupSignals'] += 1
    except ProcessLookupError:
        pass


def group_exists():
    try:
        os.killpg(proc.pid, 0)  # Existence observation only, including after reap.
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


def join_owned():
    global leader_unreaped, reap_deadline
    if reap_deadline is None:
        reap_deadline = time.monotonic() + 5
    reap_until = reap_deadline  # Cleanup retries cannot restart the five-second margin.
    while os.waitid(os.P_PID, proc.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is None:
        if time.monotonic() >= reap_until:
            fail('tool_process_unjoined')
            return
        time.sleep(0.02)
    # From this point no nonzero group signal is allowed, even if wait raises.
    leader_unreaped = False
    try:
        receipt['returncode'] = proc.wait(timeout=max(0.01, reap_until - time.monotonic()))
        receipt['processJoined'] = True
    except BaseException:
        fail('tool_reap_unproven')
        return
    join_until = time.monotonic() + 5
    while group_exists() and time.monotonic() < join_until:
        time.sleep(0.05)
    receipt['ownedGroupAbsent'] = not group_exists()
    for item in readers:
        if item['started']:
            item['thread'].join(timeout=max(0, join_until - time.monotonic()))
    receipt['readersJoined'] = (len(readers) == 2 and all(
        item['started'] and not item['thread'].is_alive() for item in readers))
    if not receipt['ownedGroupAbsent']:
        fail('tool_group_unjoined')
    if not receipt['readersJoined']:
        fail('tool_readers_unjoined')



def fail(category):
    if category not in receipt['failureCategories']:
        receipt['failureCategories'].append(category)


def sample_private(root, browser_root):
    first = root.lstat()
    require(stat.S_ISDIR(first.st_mode) and first.st_uid == os.getuid() and first.st_mode & 0o077 == 0, 'capture')
    def walk_error(exc):
        path = Path(exc.filename) if exc.filename else None
        if isinstance(exc, FileNotFoundError) and exc.errno == errno.ENOENT and path is not None and path != root and path.is_relative_to(root): return
        raise exc
    total, count = 0, 0
    for parent, dirs, files in os.walk(root, followlinks=False, onerror=walk_error):
        for name in dirs + files:
            path = Path(parent) / name
            count += 1
            require(count <= 32768, 'capture')
            try: item = path.lstat()
            except FileNotFoundError: continue
            if stat.S_ISREG(item.st_mode): total += item.st_size
            elif not stat.S_ISDIR(item.st_mode):
                require(path.is_relative_to(browser_root) and (stat.S_ISLNK(item.st_mode) or stat.S_ISSOCK(item.st_mode)), 'capture')
            require(total <= MAX_PRIVATE, 'capture')
    last = root.lstat()
    require((first.st_dev, first.st_ino) == (last.st_dev, last.st_ino) and stat.S_ISDIR(last.st_mode), 'capture')
    return total


def command(argv, env, seconds, name, private, browser_root):
    global proc, readers, leader_unreaped, receipt, reap_deadline, lock, state, OUTPUT_LIMIT, captured_total
    require(captured_total < MAX_CAPTURE, 'capture')
    OUTPUT_LIMIT = MAX_CAPTURE - captured_total
    lock = threading.Lock(); state = {'written': 0, 'seen': 0, 'failure': None}
    receipt = {'failureCategories': [], 'groupSignals': 0, 'processJoined': False, 'ownedGroupAbsent': False, 'readersJoined': False}
    proc = None; readers = []; leader_unreaped = False; reap_deadline = None
    outputs = [private / 'logs' / (name + suffix) for suffix in ('.stdout', '.stderr')]
    until = time.monotonic() + seconds
    try:
        proc = subprocess.Popen(argv, cwd=ROOT, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, close_fds=True, start_new_session=True, bufsize=0)
        leader_unreaped = True
        for pipe, destination in zip((proc.stdout, proc.stderr), outputs):
            item = {'thread': threading.Thread(target=capture, args=(pipe, destination), daemon=True), 'pipe': pipe, 'started': False}
            readers.append(item); item['thread'].start(); item['started'] = True
        while True:
            sample_private(private, browser_root)
            require(state['failure'] is None, 'capture')
            require(time.monotonic() < until, 'timeout')
            if os.waitid(os.P_PID, proc.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is not None: break
            time.sleep(0.05)
        join_owned()
        require(receipt.get('returncode') == 0, 'command')
        require(receipt['processJoined'] and receipt['ownedGroupAbsent'] and receipt['readersJoined']
                and not receipt['failureCategories'] and state['failure'] is None, 'capture')
        return outputs[0].read_bytes(), outputs[1].read_bytes()
    finally:
        if proc is not None and leader_unreaped:
            stop_owned_group(); join_owned()
        captured_total += state['written']
        for item in readers:
            if not item['started']: item['pipe'].close()


def canonical_hash(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def validate_graph_fingerprints(packages, active_go, names):
    require(canonical_hash(sorted(packages)) == REVIEWED_PACKAGES_SHA256, 'metadata_packages')
    require(canonical_hash(active_go) == REVIEWED_ACTIVE_GO_SHA256, 'metadata_source_hashes')
    require(canonical_hash(sorted(names)) == REVIEWED_REGISTRY_SHA256, 'metadata_registry')


def validate_build_info(raw, binary, commit):
    lines = raw.decode('utf-8').splitlines()
    require(lines and lines[0] == str(binary) + ': go1.27.1', 'buildinfo_version')
    settings = {}; paths = []; modules = []
    for line in lines[1:]:
        if line == '\t': continue
        parts = line.split('\t')
        require(len(parts) >= 3 and parts[0] == '' and parts[1] in ('path', 'mod', 'dep', '=>', 'build'), 'buildinfo_records')
        if parts[1] == 'path': paths.append(parts[2:])
        if parts[1] == 'mod': modules.append(parts[2:])
        if parts[1] == 'build':
            require(len(parts) == 3 and '=' in parts[2], 'buildinfo_records')
            key, value = parts[2].split('=', 1); require(key not in settings, 'buildinfo_settings'); settings[key] = value
    require(paths == [[CORE + '.test']] and len(modules) == 1 and modules[0][0] == 'github.com/webkaz-labs/sobalink', 'buildinfo_modules')
    expected = {'-buildmode': 'exe', '-compiler': 'gc', '-tags': TAGS, '-trimpath': 'true',
                'CGO_ENABLED': '0', 'GOARCH': 'amd64', 'GOOS': 'linux', 'GOAMD64': 'v1',
                'vcs': 'git', 'vcs.revision': commit, 'vcs.modified': 'false'}
    require(all(settings.get(key) == value for key, value in expected.items())
            and set(settings) in (set(expected) | {'vcs.time'}, set(expected) | {'vcs.time', '-pgo'})
            and settings.get('-pgo', 'off') == 'off', 'buildinfo_settings')
    try: stamp = datetime.datetime.fromisoformat(settings['vcs.time'].replace('Z', '+00:00'))
    except ValueError: raise RuntimeError('buildinfo_time') from None
    require(stamp.tzinfo is not None, 'buildinfo_time')
    # Any authored timezone offset is normalized to its actual UTC instant;
    # source commit identity is checked separately, not inferred from its text.
    return stamp.astimezone(datetime.timezone.utc).isoformat()


def parse_metadata(raw, tracked, cache):
    try: text = raw.decode('utf-8')
    except UnicodeError: raise RuntimeError('metadata_shape') from None
    decoder = json.JSONDecoder(); offset = 0; records = []
    while offset < len(text):
        while offset < len(text) and text[offset].isspace(): offset += 1
        if offset == len(text): break
        try: value, offset = decoder.raw_decode(text, offset)
        except ValueError: raise RuntimeError('metadata_shape') from None
        records.append(value)
    require(records and all(type(row) is dict for row in records), 'metadata_shape')
    paths = [row.get('ImportPath') for row in records]
    require(all(type(x) is str for x in paths) and len(paths) == len(set(paths)), 'metadata_packages')
    require(paths.count(CORE + '.test') == 1 and paths.count(CORE + ' [' + CORE + '.test]') == 1, 'metadata_packages')
    found_native = set(); generated = None; packages = set(); active_go = {}
    for row in records:
        require(not any(row.get(k) for k in ('Error', 'DepsErrors', 'Incomplete', 'CgoFiles', 'SwigFiles', 'SwigCXXFiles', 'CompiledGoFiles', 'Export')), 'metadata')
        require(all(row.get('ImportMap', {}).get(x, x) in paths for x in row.get('Imports', [])), 'metadata_imports')
        if row['ImportPath'] in (CORE, CORE + ' [' + CORE + '.test]'):
            require(not row.get('XTestGoFiles'), 'metadata')
        require(not row.get('ForTest') or row.get('ForTest') == CORE, 'metadata')
        if not row.get('Standard') and row['ImportPath'] != CORE + '.test':
            package = CORE if row['ImportPath'] == CORE + ' [' + CORE + '.test]' else row['ImportPath']
            require(' [' not in package and not package.endswith('.test'), 'metadata')
            packages.add(package)
            for filename in row.get('GoFiles', []):
                path = Path(filename) if Path(filename).is_absolute() else Path(row['Dir']) / filename
                require(path.is_file() and not path.is_symlink(), 'metadata')
                key = package + '/' + path.name; value = sha(path)
                require(key not in active_go or active_go[key] == value, 'metadata_source_hashes')
                active_go[key] = value
        if row['ImportPath'] == CORE + '.test':
            require(row.get('Name') == 'main' and len(row.get('GoFiles', [])) == 1, 'metadata')
            path = Path(row['GoFiles'][0]); require(path.is_absolute() and path.is_relative_to(cache) and path.resolve().is_relative_to(cache.resolve()) and path.is_file() and not path.is_symlink() and path.stat().st_size < 1 << 20, 'metadata')
            generated = path.read_text(); require('_test.TestMain' not in generated and '_xtest.' not in generated, 'metadata')
        directory = Path(row.get('Dir', '/nonexistent'))
        if directory.is_relative_to(ROOT) and '.sobalink-deps' not in directory.relative_to(ROOT).parts:
            for field in ('GoFiles', 'CFiles', 'HFiles', 'SFiles', 'EmbedFiles'):
                for name in row.get(field, []):
                    path = Path(name) if Path(name).is_absolute() else directory / name
                    if not path.is_relative_to(ROOT): continue
                    rel = path.relative_to(ROOT).as_posix()
                    require(rel in tracked and sha(path) == tracked[rel], 'source')
                    if rel.endswith(('resource_browser_native_test.go', 'resource_group_prewrite_native_test.go')): found_native.add(rel)
    require(found_native == {'internal/core/resource_browser_native_test.go', 'internal/core/resource_group_prewrite_native_test.go'} and generated is not None, 'metadata')
    names = re.findall(r'\{"(Test[A-Za-z0-9_]+)", _test\.', generated)
    require(len(names) == len(set(names)) and set(SAFE + [B1, FS1]).issubset(names), 'metadata_registry')
    validate_graph_fingerprints(packages, active_go, names)
    for field, typename in (('benchmarks', 'InternalBenchmark'), ('fuzzTargets', 'InternalFuzzTarget'), ('examples', 'InternalExample')):
        empty = re.search(r'(?ms)^var ' + field + r' = \[\]testing\.' + typename + r'\{(.*?)^\}', generated)
        require(empty is not None and not empty.group(1).strip(), 'metadata_registry')
    return {'packageCount': len(records), 'registeredTests': len(names), 'metadataSha256': hashlib.sha256(raw).hexdigest(), 'generatedMainSha256': hashlib.sha256(generated.encode()).hexdigest()}


def result_template():
    return {'schema': 1, 'accepted': False, 'phase': 'admission', 'predicate': 'none', 'completedCohorts': 0,
            'safe': {'status': 'not-started', 'passedTests': 0}, 'B1': {'status': 'not-started', 'passedTests': 0},
            'F-S1': {'status': 'not-started', 'passedTests': 0}, 'nativeCleanupProven': False,
            'platformTeardownReliedOn': False, 'platformTeardownObserved': False, 'pixelReviewPerformed': False}


def validate_result(value):
    require(type(value) is dict and set(value) == set(result_template()) and type(value['schema']) is int and value['schema'] == 1, 'receipt')
    require(value['phase'] in PHASES and value['predicate'] in PREDICATES and type(value['completedCohorts']) is int, 'receipt')
    for key in ('accepted', 'nativeCleanupProven', 'platformTeardownReliedOn', 'platformTeardownObserved', 'pixelReviewPerformed'):
        require(type(value[key]) is bool, 'receipt')
    require(not value['platformTeardownObserved'] and not value['pixelReviewPerformed'], 'receipt')
    for key, count in (('safe', 3), ('B1', 1), ('F-S1', 1)):
        row = value[key]
        require(type(row) is dict and set(row) == {'status', 'passedTests'} and row['status'] in ('not-started', 'started', 'passed', 'failed')
                and type(row['passedTests']) is int and row['passedTests'] == (count if row['status'] == 'passed' else 0), 'receipt')
    passed = sum(value[k]['status'] == 'passed' for k in ('safe', 'B1', 'F-S1'))
    require(value['completedCohorts'] == passed and value['accepted'] == (passed == 3 and value['phase'] == 'complete' and value['predicate'] == 'none' and value['nativeCleanupProven'] and not value['platformTeardownReliedOn']), 'receipt')
    return value


def validate_provenance(value):
    allowed = {'schema', 'hashMeaning', 'toolchain', 'commit', 'tree', 'sourceManifestSha256', 'sourceFiles', 'playwrightVersion', 'chromiumRevision', 'chromiumSha256', 'embeddedAssetManifestSha256', 'actualMetadata', 'coreImageSha256', 'buildInfoSha256', 'browserCounts'}
    require(type(value) is dict and set(value).issubset(allowed) and type(value.get('schema')) is int and value['schema'] == 1
            and value.get('hashMeaning') == 'Measured hosted input identities; no independent source approval inferred.', 'receipt')
    for key in ('commit', 'tree'):
        if key in value: require(type(value[key]) is str and re.fullmatch('[0-9a-f]{40}', value[key]) is not None, 'receipt')
    for key in ('sourceManifestSha256', 'chromiumSha256', 'embeddedAssetManifestSha256', 'coreImageSha256', 'buildInfoSha256'):
        if key in value: require(type(value[key]) is str and re.fullmatch('[0-9a-f]{64}', value[key]) is not None, 'receipt')
    if 'sourceFiles' in value: require(type(value['sourceFiles']) is int and 1 <= value['sourceFiles'] <= 8192, 'receipt')
    if 'playwrightVersion' in value: require(value['playwrightVersion'] == '1.63.0', 'receipt')
    if 'chromiumRevision' in value: require(type(value['chromiumRevision']) is str and re.fullmatch('[0-9]{1,8}', value['chromiumRevision']) is not None, 'receipt')
    if 'toolchain' in value:
        tool = value['toolchain']
        require(type(tool) is dict and set(tool) == {'versions', 'binarySha256'}, 'receipt')
        require(SETUP.validate_toolchain(tool['versions'], tool['binarySha256']) == tool, 'receipt')
    if 'actualMetadata' in value:
        meta = value['actualMetadata']
        require(type(meta) is dict and set(meta) == {'packageCount', 'registeredTests', 'metadataSha256', 'generatedMainSha256'}, 'receipt')
        require(all(type(meta[k]) is int and 1 <= meta[k] <= 4096 for k in ('packageCount', 'registeredTests'))
                and all(type(meta[k]) is str and re.fullmatch('[0-9a-f]{64}', meta[k]) is not None for k in ('metadataSha256', 'generatedMainSha256')), 'receipt')
    if 'browserCounts' in value:
        require(value['browserCounts'] == {'httpListRequests': 2, 'httpSnapshots': 1, 'browserCases': 1, 'nativeCleanupProven': True}
                and all(type(value['browserCounts'][k]) is int for k in ('httpListRequests', 'httpSnapshots', 'browserCases'))
                and type(value['browserCounts']['nativeCleanupProven']) is bool, 'receipt')
    return value


def save_json(path, value):
    raw = (json.dumps(value, sort_keys=True) + '\n').encode()
    require(len(raw) <= 1 << 20, 'receipt')
    with path.open('xb') as stream: stream.write(raw)
    path.chmod(0o600)


def main(argv=None):
    global captured_total
    args = sys.argv[1:] if argv is None else argv
    if args not in (['admit'], ['run']): return 1
    report = result_template(); provenance = {'schema': 1, 'hashMeaning': 'Measured hosted input identities; no independent source approval inferred.'}
    private = None; export = None; captured_total = 0; browser_root = None
    try:
        require(sys.platform == 'linux' and os.uname().machine == 'x86_64', 'event')
        git_env = {'PATH': '/usr/bin:/bin', 'HOME': '/nonexistent', 'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null', 'GIT_OPTIONAL_LOCKS': '0'}
        commit = subprocess.check_output(['git', '-C', str(ROOT), 'rev-parse', 'HEAD'], env=git_env, text=True, timeout=10).strip()
        validate_event(os.environ, json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text()), commit)
        if args == ['admit']:
            print('PASS: exact diagnostic source/event admitted.'); return 0
        os.umask(0o077)
        parent = Path(os.environ['RUNNER_TEMP']).resolve()
        private = parent / 'resource-browser-prewrite-private'; private.mkdir(mode=0o700)
        export = parent / 'resource-browser-prewrite-sanitized'; export.mkdir(mode=0o700)
        for name in ('logs', 'build', 'safe', 'B1', 'F-S1'):
            (private / name).mkdir(mode=0o700)
            if name != 'logs':
                for child in ('home', 'tmp'): (private / name / child).mkdir(mode=0o700)
        browser_root = private / 'browser-owner'; browser_root.mkdir(mode=0o700)
        env = environment(private / 'build/home', private / 'build/tmp')
        # Go caches were prepared under the hosted user's ordinary HOME. Pin their
        # existing locations, while every command HOME/tmp is fresh and private.
        host_home = Path(os.environ['HOME']).resolve()
        env.update(GOPATH=str(host_home / 'go'), GOMODCACHE=str(host_home / 'go/pkg/mod'), GOCACHE=str(host_home / '.cache/go-build'))
        def run(name, command_args, seconds=30, chosen=None):
            return command(command_args, chosen or env, seconds, name, private, browser_root)
        def read(name, command_args, chosen=None):
            out, err = run(name, command_args, chosen=chosen); require(not err, 'command'); return out.decode().strip()
        report['phase'] = 'inputs'
        tree = read('tree', ['git', 'rev-parse', 'HEAD^{tree}'])
        require(re.fullmatch('[0-9a-f]{40}', tree) is not None and not read('status', ['git', 'status', '--porcelain', '--untracked-files=no']), 'source')
        tracked = read('tracked', ['git', 'ls-files']).splitlines()
        require(all(not (ROOT / name).is_symlink() and (ROOT / name).is_file() for name in tracked), 'source')
        before = {name: sha(ROOT / name) for name in tracked}
        assets = SETUP.asset_hashes(ROOT / 'web/dist')
        require(assets == {name[9:]: digest for name, digest in before.items() if name.startswith('web/dist/')}, 'source')
        versions = {'go': read('go-version', ['go', 'env', 'GOVERSION']), 'node': read('node-version', ['node', '--version']), 'npm': read('npm-version', ['npm', '--version'])}
        node = Path(shutil.which('node')).resolve(); go = Path(shutil.which('go')).resolve()
        tools = {'go': sha(go), 'node': sha(node)}
        provenance['toolchain'] = SETUP.validate_toolchain(versions, tools)
        web = ROOT / 'web'; lockfile = json.loads((web / 'package-lock.json').read_text())
        installed = {name: json.loads((web / 'node_modules' / name / 'package.json').read_text()) for name in ('@playwright/test', 'playwright', 'playwright-core')}
        version = SETUP.validate_playwright(lockfile, json.loads((web / 'package.json').read_text()), installed)
        # Fixed installed module import query, no npm/npx launcher or browser start.
        query = "import {chromium} from './web/node_modules/@playwright/test/index.mjs'; console.log(chromium.executablePath())"
        resolution_env = dict(env, HOME=str(host_home))
        chromium = Path(read('chromium-path', [str(node), '--input-type=module', '-e', query], resolution_env))
        revision = SETUP.validate_chromium(json.loads((web / 'node_modules/playwright-core/browsers.json').read_text()), chromium, host_home / '.cache/ms-playwright')
        browser_hash = sha(chromium)
        package_hashes = {name: sha(web / 'node_modules' / name / 'package.json') for name in installed}
        source_go = (ROOT / 'internal/core/resource_browser_native_test.go').read_text()
        require('"--grep", "' + GREP + '"' in source_go and MATCH in (web / 'playwright.resource-native.config.mjs').read_text(), 'source')
        require(browser_selection('resource-native-local-catalog.acceptance.mjs', TITLE, 'resource-native-local-catalog.acceptance.mjs ' + TITLE), 'inventory')
        provenance.update(commit=commit, tree=tree, sourceManifestSha256=hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest(), sourceFiles=len(before), playwrightVersion=version, chromiumRevision=revision, chromiumSha256=browser_hash, embeddedAssetManifestSha256=hashlib.sha256(json.dumps(assets, sort_keys=True).encode()).hexdigest())
        report['phase'] = 'metadata'
        raw, err = run('metadata', [str(go), 'list', '-mod=readonly', '-deps', '-test', '-json', '-tags=' + TAGS, './internal/core'], 120)
        require(not err, 'metadata'); provenance['actualMetadata'] = parse_metadata(raw, before, Path(env['GOCACHE']))
        report['phase'] = 'build'; binary = private / 'build/core.test'
        out, err = run('build', [str(go), 'test', '-c', '-mod=readonly', '-trimpath', '-buildvcs=true', '-pgo=off', '-p=2', '-tags=' + TAGS, '-o', str(binary), './internal/core'], 480)
        require(not out and not err and binary.is_file(), 'command'); image_hash = sha(binary); provenance['coreImageSha256'] = image_hash
        report['phase'] = 'inspect'
        out, err = run('inspect', [str(go), 'version', '-m', str(binary)])
        require(not err, 'toolchain'); validate_build_info(out, binary, commit)
        provenance['buildInfoSha256'] = hashlib.sha256(out).hexdigest()
        for kind, tests, seconds in (('safe', SAFE, 60), ('B1', [B1], 120), ('F-S1', [FS1], 240)):
            report['phase'] = kind; report[kind]['status'] = 'started'
            require(sha(binary) == image_hash and {name: sha(ROOT / name) for name in tracked} == before, 'changed')
            native_env = environment(private / kind / 'home', private / kind / 'tmp', kind)
            if kind == 'B1':
                native_env.update(SOBA_RESOURCE_BROWSER_SCOPE='hosted-job-v1', SOBA_RESOURCE_BROWSER_PRIVATE_ROOT=str(browser_root), SOBA_RESOURCE_BROWSER_WEB_DIR=str(web), SOBA_RESOURCE_BROWSER_NODE=str(node), SOBA_RESOURCE_BROWSER_NODE_SHA256=tools['node'], SOBA_RESOURCE_BROWSER_CHROMIUM=str(chromium), SOBA_RESOURCE_BROWSER_CHROMIUM_SHA256=browser_hash)
            selector = '^' + tests[0] + '$' if len(tests) == 1 else '^(' + '|'.join(tests) + ')$'
            argv = [str(binary), '-test.run=' + selector, '-test.v=true', '-test.count=1', '-test.parallel=1', '-test.shuffle=off', '-test.timeout=' + str(seconds) + 's']
            out, err = run(kind, argv, seconds + 1, native_env)
            require(not err, 'command'); count = exact_tests(out, tests)
            if kind == 'B1':
                provenance['browserCounts'] = validate_b1(*(read_json(browser_root / name) for name in ('native-result.json', 'scope-proof.json', 'browser-summary.json', 'http-observations.json')))
                require(sha(node) == tools['node'] and sha(chromium) == browser_hash, 'changed')
            report[kind] = {'status': 'passed', 'passedTests': count}; report['completedCohorts'] += 1
        report['phase'] = 'postflight'
        require(before == {name: sha(ROOT / name) for name in tracked} and sha(binary) == image_hash and SETUP.asset_hashes(web / 'dist') == assets
                and sha(go) == tools['go'] and sha(node) == tools['node'] and sha(chromium) == browser_hash
                and package_hashes == {name: sha(web / 'node_modules' / name / 'package.json') for name in installed}, 'changed')
        report.update(phase='complete', accepted=True, nativeCleanupProven=True)
    except BaseException as exc:
        category = str(exc) if type(exc) is RuntimeError else 'internal'
        report['predicate'] = category if category in PREDICATES else 'internal'
        if report['phase'] in ('safe', 'B1', 'F-S1') and report[report['phase']]['status'] == 'started': report[report['phase']]['status'] = 'failed'
        # Conservatively mark reliance after any native failure. Teardown has not
        # been observed here and no detached browser completion is inferred.
        report['platformTeardownReliedOn'] = report['B1']['status'] in ('started', 'failed') or report['F-S1']['status'] in ('started', 'failed')
        report['accepted'] = False
    try:
        if export is not None:
            save_json(export / 'result.json', validate_result(report))
            save_json(export / 'provenance.json', validate_provenance(provenance))
            with open(os.environ['GITHUB_OUTPUT'], 'a') as output: output.write('sanitized_ready=true\n')
    except BaseException:
        report['accepted'] = False
    print('PASS: B1 and F-S1 exact attempts and test-owned cleanup verified.' if report['accepted'] else 'FAIL: acceptance incomplete; only closed sanitized diagnostics may be exported.')
    return 0 if report['accepted'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
