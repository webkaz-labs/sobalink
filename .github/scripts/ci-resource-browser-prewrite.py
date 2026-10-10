#!/usr/bin/env python3
"""One bounded hosted B1 diagnostic; historical F-S1 evidence stays separate.

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
BRANCH = 'diagnostic-resource/browser-prewrite-743a1eb7-v4'
TAGS = 'resource_browser_native,resource_group_catalog_native,resource_management_native,resource_inspection_native,directlan_activation_native,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy'
REVIEWED_PACKAGES_SHA256 = 'ecf3b7eeaf53f5c5b169fda2f84897c2250b358347350bfd6efca6335e491b58'
REVIEWED_ACTIVE_GO_SHA256 = 'cdde0d244acd368b42db12a0a0774b27e98c2a9fdd48eb09402a343aeeff3c0a'
REVIEWED_REGISTRY_SHA256 = '961d2cdf9aff0d32670395430ae8bebf3ff92801cfb0a6995fa04d2c28541410'
# Immutable reviewed historical evidence. Never counted as this image's result.
PRIOR_FS1 = {'outcome': 'passed', 'executedInCurrentRun': False, 'run': 38064320241, 'attempt': 1, 'commit': '45d2560bdd615d0f1a77e2993be98016c6044faa', 'tree': 'b3427a082bafe7bc2c5503762a83e7bac4a1999e', 'coreImageSha256': '08c80ac92ada39823ec276f68819dc174fa850ecd7d5191c2ac8bc48387814ef', 'resultSha256': 'cfb96ddbb2b6997f5cba0e803b667db45dbc543901e5eaf6559d8698171ddfc4', 'provenanceSha256': 'df418c2209b78f07b730001bb0dce8fe89404496b5fe974a83468184ad933c53', 'independentReviewSha256': 'c0bb6c56ad6a858fda9a9838a49f2f0a037735344f0637c04c219f4de142f009', 'claim': 'Frozen prior acceptance evidence only; not rerun or current-image coverage.'}
CORE = 'github.com/webkaz-labs/sobalink/internal/core'
SAFE = sorted(('TestResourceBrowserCatalogSelectionCanonicalOrder', 'TestGroupOwnedStoreFirstUseIsReadOnly', 'TestGroupOwnedStorePrewriteFailureKeepsConfirmedSnapshot', 'TestGroupCoordinatorFailedPublicationKeepsLastConfirmedEvidence'))
B1 = 'TestResourceBrowserNativeLocalCatalog'
FS1 = 'TestResourceGroupNativeAcceptedPrewriteRefusal'
TITLE = 'resource-native-local-catalog-en-settings'
GREP = '(?:^| )resource-native-local-catalog-en-settings$'
MATCH = '**/resource-native-local-catalog.acceptance.mjs'
NATIVE_KEYS = ('SOBALINK_RUN_ACTIVATION_NATIVE', 'SOBALINK_RUN_MANAGED_RESTART_NATIVE', 'SOBALINK_RUN_PRODUCT_ACTIVATION_NATIVE', 'SOBALINK_RUN_RESOURCE_BROWSER_NATIVE', 'SOBALINK_RUN_RESOURCE_GROUP_CATALOG_NATIVE', 'SOBALINK_RUN_RESOURCE_GROUP_PREWRITE_NATIVE', 'SOBALINK_RUN_RESOURCE_GROUP_RESTART_NATIVE', 'SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE', 'SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE', 'SOBALINK_RUN_RESOURCE_PROCESS_NATIVE', 'SOBALINK_RUN_WEB_ACTIVATION_NATIVE')
PHASES = ('admission', 'inputs', 'metadata', 'build', 'inspect', 'safe', 'B1', 'postflight', 'complete')
PREDICATES = ('none', 'event', 'source', 'toolchain', 'browser', 'metadata', 'capture', 'timeout', 'command', 'inventory', 'receipt', 'changed', 'interrupted', 'internal', 'metadata_shape', 'metadata_packages', 'metadata_imports', 'metadata_source_hashes', 'metadata_registry', 'buildinfo_version', 'buildinfo_records', 'buildinfo_modules', 'buildinfo_settings', 'buildinfo_time', 'browser_image', 'browser_binary', 'browser_version', 'browser_support_unavailable', 'browser_profile', 'browser_changed')
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


FAILURE_STAGES = ('none', 'browser_ownership', 'browser_summary', 'http_observations', 'catalog_request', 'catalog_response', 'product_invariants', 'core_close', 'lock_close', 'native_deadline')
RECEIPT_FILES = {'native': 'native-result.json', 'scope': 'scope-proof.json', 'browser': 'browser-summary.json', 'http': 'http-observations.json'}


BROWSER_CATEGORIES = ('none', 'fixture_guard', 'fixture_descriptor', 'fixture_authentication', 'browser_launch_sandbox', 'browser_launch', 'browser_context', 'browser_page', 'unclassified', 'unavailable')


def validate_browser_diagnostic(value):
    require(type(value) is dict and set(value) == {'status', 'unit', 'line', 'column', 'category'}
            and type(value['line']) is int and type(value['column']) is int and value['category'] in BROWSER_CATEGORIES, 'receipt')
    if value['status'] in ('none', 'unavailable'):
        require(value == {'status': value['status'], 'unit': value['status'], 'line': 0, 'column': 0, 'category': value['status']}, 'receipt')
    else:
        require(value['status'] in ('failed', 'timedOut', 'skipped', 'interrupted') and value['category'] != 'none', 'receipt')
        if value['unit'] == 'unavailable': require(value['line'] == value['column'] == 0, 'receipt')
        else:
            require(value['unit'] in ('case', 'fixture', 'config', 'reporter')
                    and 1 <= value['line'] <= 100000 and 1 <= value['column'] <= 10000, 'receipt')
    return value


def validate_b1_receipt(kind, value):
    require(type(value) is dict and type(value.get('schema')) is int, 'receipt')
    if kind == 'native':
        bools = {'accepted', 'browserJoined', 'coreClosed', 'lockClosed', 'httpCountsOnly', 'savedServiceNavigationCovered', 'pixelReviewPerformed'}
        require(set(value) == bools | {'schema', 'failureStage'} and value['schema'] == 2
                and all(type(value[k]) is bool for k in bools) and value['failureStage'] in FAILURE_STAGES
                and not value['savedServiceNavigationCovered'] and not value['pixelReviewPerformed'], 'receipt')
    elif kind == 'scope':
        bools = {'complete', 'descendantsReaped', 'forced', 'deadlineExceeded'}
        ints = {'playwrightExit', 'errors', 'observed', 'reaped'}
        require(set(value) == bools | ints | {'schema'} and value['schema'] == 1
                and all(type(value[k]) is bool for k in bools) and all(type(value[k]) is int for k in ints)
                and -128 <= value['playwrightExit'] <= 255 and 0 <= value['errors'] <= 8192
                and 0 <= value['observed'] <= 8192 and 0 <= value['reaped'] <= 8192, 'receipt')
    elif kind == 'browser':
        ints = {'expected', 'observed', 'passed', 'errors'}; bools = {'selectionValid', 'unexpected', 'accepted'}
        require(set(value) == ints | bools | {'schema', 'diagnostic'} and value['schema'] == 3
                and all(type(value[k]) is int and 0 <= value[k] <= 8192 for k in ints)
                and all(type(value[k]) is bool for k in bools), 'receipt')
        validate_browser_diagnostic(value['diagnostic'])
    elif kind == 'http':
        ints = {'requests', 'stateRequests', 'stateReady', 'loginRequests', 'listRequests', 'listResponses', 'snapshots', 'snapshotResponses', 'blocked', 'errors', 'captures'}
        require(set(value) == ints | {'schema', 'completed', 'requestsJoined'} and value['schema'] == 1
                and all(type(value[k]) is int and 0 <= value[k] <= 8192 for k in ints)
                and type(value['completed']) is bool and type(value['requestsJoined']) is bool, 'receipt')
    else: raise RuntimeError('receipt')
    return value


def b1_diagnostics(root=None, joined=False):
    result = {kind: {'availability': 'unavailable', 'value': None} for kind in RECEIPT_FILES}
    if root is None or not joined: return result
    for kind, filename in RECEIPT_FILES.items():
        try: value = validate_b1_receipt(kind, read_json(root / filename))
        except FileNotFoundError: result[kind]['availability'] = 'missing'
        except (OSError, ValueError, RuntimeError, TypeError, KeyError): result[kind]['availability'] = 'invalid'
        else: result[kind] = {'availability': 'valid', 'value': value}
    return result


def validate_b1(native, scope, browser, http):
    for kind, value in zip(RECEIPT_FILES, (native, scope, browser, http)): validate_b1_receipt(kind, value)
    require(all(native[k] for k in ('accepted', 'browserJoined', 'coreClosed', 'lockClosed', 'httpCountsOnly'))
            and native['failureStage'] == 'none', 'receipt')
    require(scope['complete'] and scope['descendantsReaped'] and not scope['forced']
            and not scope['deadlineExceeded'] and scope['playwrightExit'] == 0 and scope['errors'] == 0
            and 0 <= scope['observed'] <= 64 and 1 <= scope['reaped'] <= 8192, 'receipt')
    require(browser == {'schema': 3, 'expected': 1, 'observed': 1, 'passed': 1, 'errors': 0,
                        'selectionValid': True, 'unexpected': False, 'accepted': True,
                        'diagnostic': {'status': 'none', 'unit': 'none', 'line': 0, 'column': 0, 'category': 'none'}}, 'receipt')
    require(1 <= http['requests'] <= 256 and 1 <= http['stateRequests'] <= 96
            and 1 <= http['stateReady'] <= http['stateRequests'] and http['loginRequests'] == 1
            and http['listRequests'] == http['listResponses'] == 2 and http['snapshots'] == http['snapshotResponses'] == 1
            and http['blocked'] == http['errors'] == 0 and http['captures'] == 2
            and http['completed'] and http['requestsJoined'], 'receipt')
    return {'httpListRequests': 2, 'httpSnapshots': 1, 'browserCases': 1, 'nativeCleanupProven': True}



CHROME_PATH = '/opt/google/chrome/chrome'
CHROME_HELPER = '/opt/google/chrome/chrome-sandbox'
CHROME_VERSION = '154.0.8037.97'
IMAGE_VERSION = '20261004.327.1'
IMAGE_MANIFEST = 'https://raw.githubusercontent.com/actions/runner-images/ubuntu24/20261004.327/images/ubuntu/Ubuntu2404-Readme.md'
IMAGE_INSTALL_SOURCE = 'https://raw.githubusercontent.com/actions/runner-images/ubuntu24/20261004.327/images/ubuntu/scripts/build/install-google-chrome.sh'


def validate_root_facts(value, maximum, executable=False):
    require(type(value) is dict and set(value) == {'mode', 'uid', 'size'}
            and all(type(value[k]) is int for k in value)
            and stat.S_ISREG(value['mode']) and value['uid'] == 0 and value['mode'] & 0o022 == 0
            and value['mode'] & stat.S_ISGID == 0 and value['size'] >= 0
            and (not executable or value['size'] <= maximum)
            and (not executable or value['mode'] & 0o111 != 0), 'browser_binary' if executable else 'browser_profile')


def root_input(path, maximum, executable=False):
    # /proc and /sys report size0; bounded reads, not stat-size equality, apply.
    category = 'browser_binary' if executable else 'browser_support_unavailable'
    try:
        require(path.is_absolute() and path.resolve(strict=True) == path, category)
        first = path.lstat()
        validate_root_facts({'mode': first.st_mode, 'uid': first.st_uid, 'size': first.st_size}, maximum, executable)
        def identity(facts): return (facts.st_dev, facts.st_ino, facts.st_mode, facts.st_uid, facts.st_gid, facts.st_size, facts.st_mtime_ns, facts.st_ctime_ns)
        digest = hashlib.sha256(); total = 0; head = b''; chunks = []
        with path.open('rb') as stream:
            opened = os.fstat(stream.fileno()); require(identity(first) == identity(opened), category)
            while True:
                data = stream.read(min(524288, maximum + 1 - total))
                if not data: break
                total += len(data); require(total <= maximum, category); digest.update(data)
                if len(head) < 20: head = (head + data)[:20]
                if maximum <= 16384: chunks.append(data)
            require(identity(opened) == identity(os.fstat(stream.fileno())) == identity(path.lstat()), category)
        require(total > 0 or not executable, category)
        return {'identity': identity(first), 'sha256': digest.hexdigest(), 'setuid': bool(first.st_mode & stat.S_ISUID), 'mode': stat.S_IMODE(first.st_mode),
                'data': b''.join(chunks) if maximum <= 16384 else head}
    except OSError: raise RuntimeError(category) from None


def optional_root_input(path, maximum, executable=False):
    try: path.lstat()
    except FileNotFoundError: return None
    except OSError: raise RuntimeError('browser_support_unavailable') from None
    return root_input(path, maximum, executable)


def validate_chrome_support(image, texts):
    require(image == {'os': 'ubuntu24', 'version': IMAGE_VERSION}, 'browser_image')
    require(type(texts) is dict and set(texts) == {'profile', 'local', 'disabled', 'enabled', 'restricted', 'clone', 'namespaces'}, 'browser_profile')
    require(type(texts['profile']) is str and (texts['local'] is None or type(texts['local']) is str)
            and texts['disabled'] is False, 'browser_profile')
    lines = [line.strip() for line in texts['profile'].splitlines() if line.strip() and not line.lstrip().startswith('#')]
    require(lines == ['abi <abi/4.0>,', 'include <tunables/global>',
                      'profile chrome /opt/google/chrome/chrome flags=(unconfined) {', 'userns,',
                      'include if exists <local/chrome>', '}'], 'browser_profile')
    require(texts['local'] is None or all(not line.strip() or line.lstrip().startswith('#') for line in texts['local'].splitlines()), 'browser_profile')
    require(texts['enabled'] == 'Y' and texts['restricted'] == '1' and texts['clone'] == '1'
            and type(texts['namespaces']) is str and re.fullmatch('[0-9]{1,10}', texts['namespaces']) is not None
            and 1 <= int(texts['namespaces']) <= 2147483647, 'browser_profile')


def validate_chrome_binary(data):
    require(type(data) is bytes and data[:7] == b'\x7fELF\x02\x01\x01' and data[18:20] == b'\x3e\x00', 'browser_binary')


def existing_chrome_inputs(values):
    image = {'os': values.get('ImageOS'), 'version': values.get('ImageVersion')}
    require(image == {'os': 'ubuntu24', 'version': IMAGE_VERSION}, 'browser_image')
    binary = root_input(Path(CHROME_PATH), 1 << 30, True)
    validate_chrome_binary(binary['data'])
    require(not binary['setuid'], 'browser_binary')
    helper = optional_root_input(Path(CHROME_HELPER), 16 << 20, True)
    profile = root_input(Path('/etc/apparmor.d/chrome'), 16384)
    local = optional_root_input(Path('/etc/apparmor.d/local/chrome'), 16384)
    try: Path('/etc/apparmor.d/disable/chrome').lstat(); disabled = True
    except FileNotFoundError: disabled = False
    except OSError: raise RuntimeError('browser_support_unavailable') from None
    support = {}
    for key, path in (('enabled', '/sys/module/apparmor/parameters/enabled'),
                      ('restricted', '/proc/sys/kernel/apparmor_restrict_unprivileged_userns'),
                      ('clone', '/proc/sys/kernel/unprivileged_userns_clone'),
                      ('namespaces', '/proc/sys/user/max_user_namespaces')):
        support[key] = root_input(Path(path), 128)['data']
    try:
        texts = {key: raw.decode('ascii').strip() for key, raw in support.items()}
        texts.update(profile=profile['data'].decode('utf-8'), local=None if local is None else local['data'].decode('utf-8'), disabled=disabled)
    except UnicodeError: raise RuntimeError('browser_profile') from None
    validate_chrome_support(image, texts)
    # No privileged loaded-profile read. These files are configuration evidence;
    # only the later sandbox=true launch demonstrates operational support.
    return {'image': image, 'binary': binary, 'helper': helper, 'profile': profile, 'local': local, 'support': support}


def chrome_provenance(inputs):
    helper, local = inputs['helper'], inputs['local']
    return {'kind': 'existing-google-chrome', 'channel': 'chrome', 'version': CHROME_VERSION,
            'imageOS': 'ubuntu24', 'imageVersion': IMAGE_VERSION, 'imageManifest': IMAGE_MANIFEST,
            'imageInstallSource': IMAGE_INSTALL_SOURCE, 'binarySha256': inputs['binary']['sha256'],
            'profileSha256': inputs['profile']['sha256'], 'localProfilePresent': local is not None,
            'localProfileSha256': None if local is None else local['sha256'],
            'helperPresent': helper is not None, 'helperSha256': None if helper is None else helper['sha256'],
            'helperSetuid': False if helper is None else helper['setuid'], 'helperMode': None if helper is None else helper['mode'],
            'accessibleSupportMatched': True, 'loadedProfileObserved': False, 'loadedProfileReadAttempted': False,
            'loadedProfileObservation': 'unavailable-unprivileged', 'packageSignatureVerified': False,
            'identityMeaning': 'Measured existing image inputs; manifest version reference, not independent binary or loaded-policy attestation.',
            'helperMeaning': 'Chrome may automatically use its existing setuid helper; actual helper use and privilege transitions are unobserved.'}


def validate_chrome_provenance(value):
    require(type(value) is dict, 'receipt')
    dummy = {'binary': {'sha256': 'a' * 64}, 'profile': {'sha256': 'b' * 64}, 'local': None, 'helper': None}
    fixed = chrome_provenance(dummy)
    dynamic = {'binarySha256', 'profileSha256', 'localProfilePresent', 'localProfileSha256', 'helperPresent', 'helperSha256', 'helperSetuid', 'helperMode'}
    require(set(value) == set(fixed) and all(type(value[k]) is type(fixed[k]) and value[k] == fixed[k] for k in fixed.keys() - dynamic), 'receipt')
    for key in ('binarySha256', 'profileSha256'):
        require(type(value[key]) is str and re.fullmatch('[0-9a-f]{64}', value[key]) is not None, 'receipt')
    for flag, digest in (('localProfilePresent', 'localProfileSha256'), ('helperPresent', 'helperSha256')):
        require(type(value[flag]) is bool and (type(value[digest]) is str and re.fullmatch('[0-9a-f]{64}', value[digest]) is not None if value[flag] else value[digest] is None), 'receipt')
    require(type(value['helperSetuid']) is bool and (value['helperPresent'] or not value['helperSetuid']), 'receipt')
    if value['helperPresent']:
        mode = value['helperMode']
        require(type(mode) is int and 0 <= mode <= 0o7777 and mode & 0o111 != 0 and mode & 0o2022 == 0
                and value['helperSetuid'] == bool(mode & stat.S_ISUID), 'receipt')
    else: require(value['helperMode'] is None, 'receipt')
    return value


def environment(home, tmp, kind='build'):
    value = {'PATH': os.environ.get('PATH', '/usr/bin:/bin'), 'HOME': str(home), 'TMPDIR': str(tmp),
             'TMP': str(tmp), 'TEMP': str(tmp), 'LANG': 'C.UTF-8', 'LC_ALL': 'C.UTF-8', 'TZ': 'UTC',
             'GOOS': 'linux', 'GOARCH': 'amd64', 'GOAMD64': 'v1', 'CGO_ENABLED': '0', 'GOMAXPROCS': '2',
             'GOTOOLCHAIN': 'local', 'GOENV': 'off', 'GOWORK': 'off', 'GOPROXY': 'off', 'GOSUMDB': 'off',
             'GOAUTH': 'off', 'GOVCS': '*:off', 'GOTELEMETRY': 'off', 'GOFLAGS': '-mod=readonly',
             'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null', 'GIT_OPTIONAL_LOCKS': '0',
             **dict.fromkeys(NATIVE_KEYS, '')}
    if kind == 'B1': value['SOBALINK_RUN_RESOURCE_BROWSER_NATIVE'] = 'reviewed-offline-catalog-settings-v1'
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
    receipt = {'name': name, 'spawned': False, 'failureCategories': [], 'groupSignals': 0, 'processJoined': False, 'ownedGroupAbsent': False, 'readersJoined': False}
    state = {'written': 0, 'seen': 0, 'failure': None}
    require(captured_total < MAX_CAPTURE, 'capture')
    OUTPUT_LIMIT = MAX_CAPTURE - captured_total
    lock = threading.Lock(); state = {'written': 0, 'seen': 0, 'failure': None}
    proc = None; readers = []; leader_unreaped = False; reap_deadline = None
    outputs = [private / 'logs' / (name + suffix) for suffix in ('.stdout', '.stderr')]
    until = time.monotonic() + seconds
    try:
        proc = subprocess.Popen(argv, cwd=ROOT, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, close_fds=True, start_new_session=True, bufsize=0)
        leader_unreaped = True; receipt['spawned'] = True
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


def empty_command():
    return {'spawned': False, 'returnCodeObserved': False, 'returnCode': None, 'returnCategory': 'not-started',
            'processJoined': False, 'groupAbsent': False, 'readersJoined': False, 'captureComplete': False,
            'stdoutBytes': None, 'stderrBytes': None, 'stderrPresent': None,
            'go': {'available': False, 'run': 0, 'pass': 0, 'fail': 0, 'skip': 0, 'extra': 0}}


def go_counts(raw, names):
    value = {'available': False, 'run': 0, 'pass': 0, 'fail': 0, 'skip': 0, 'extra': 0}
    try: lines = raw.decode('utf-8').splitlines()
    except UnicodeError: return value
    value['available'] = True
    for line in lines:
        match = re.fullmatch(r'=== RUN   (Test[A-Za-z0-9_]+)', line)
        if match:
            value['run'] += 1; value['extra'] += match[1] not in names
        else:
            match = re.fullmatch(r'--- (PASS|FAIL|SKIP): (Test[A-Za-z0-9_]+) \([0-9]+(?:\.[0-9]+)?s\)', line)
            if match:
                value[match[1].lower()] += 1; value['extra'] += match[2] not in names
            elif line.lstrip().startswith(('=== RUN', '--- PASS', '--- FAIL', '--- SKIP', '=== PAUSE', '=== CONT')):
                value['extra'] += 1
    return value


def command_diagnostic(name, names, private):
    result = empty_command()
    if receipt.get('name') != name: return result
    result.update(spawned=receipt['spawned'], processJoined=receipt['processJoined'],
                  groupAbsent=receipt['ownedGroupAbsent'], readersJoined=receipt['readersJoined'])
    code = receipt.get('returncode')
    if type(code) is int and -128 <= code <= 255:
        result.update(returnCodeObserved=True, returnCode=code,
                      returnCategory='zero' if code == 0 else 'signal' if code < 0 else 'nonzero')
    elif result['spawned']: result['returnCategory'] = 'unobserved'
    if not all(result[k] for k in ('processJoined', 'groupAbsent', 'readersJoined')) or state['failure'] is not None: return result
    try:
        streams = []
        for suffix in ('.stdout', '.stderr'):
            path = private / 'logs' / (name + suffix); facts = path.lstat()
            require(stat.S_ISREG(facts.st_mode) and facts.st_uid == os.getuid() and facts.st_mode & 0o077 == 0
                    and 0 <= facts.st_size <= MAX_CAPTURE, 'capture')
            with path.open('rb') as stream:
                opened = os.fstat(stream.fileno()); raw = stream.read(MAX_CAPTURE + 1)
            require((facts.st_dev, facts.st_ino, facts.st_size) == (opened.st_dev, opened.st_ino, opened.st_size)
                    and len(raw) == facts.st_size, 'capture')
            streams.append(raw)
        require(sum(map(len, streams)) <= MAX_CAPTURE and sum(map(len, streams)) == state['written'] == state['seen'], 'capture')
    except (OSError, RuntimeError): return result
    result.update(captureComplete=True, stdoutBytes=len(streams[0]), stderrBytes=len(streams[1]),
                  stderrPresent=bool(streams[1]), go=go_counts(streams[0], names))
    return result


def validate_command(value):
    require(type(value) is dict and set(value) == set(empty_command()), 'receipt')
    bools = ('spawned', 'returnCodeObserved', 'processJoined', 'groupAbsent', 'readersJoined', 'captureComplete')
    require(all(type(value[k]) is bool for k in bools), 'receipt')
    code = value['returnCode']
    if value['returnCodeObserved']:
        require(value['spawned'] and value['processJoined'] and type(code) is int and -128 <= code <= 255
                and value['returnCategory'] == ('zero' if code == 0 else 'signal' if code < 0 else 'nonzero'), 'receipt')
    else:
        require(code is None and value['returnCategory'] == ('unobserved' if value['spawned'] else 'not-started'), 'receipt')
    if not value['spawned']: require(not any(value[k] for k in bools[1:]), 'receipt')
    if value['captureComplete']:
        require(all(value[k] for k in ('processJoined', 'groupAbsent', 'readersJoined'))
                and all(type(value[k]) is int and 0 <= value[k] <= MAX_CAPTURE for k in ('stdoutBytes', 'stderrBytes'))
                and value['stdoutBytes'] + value['stderrBytes'] <= MAX_CAPTURE
                and type(value['stderrPresent']) is bool and value['stderrPresent'] == (value['stderrBytes'] > 0), 'receipt')
    else: require(all(value[k] is None for k in ('stdoutBytes', 'stderrBytes', 'stderrPresent')), 'receipt')
    counts = value['go']
    require(type(counts) is dict and set(counts) == {'available', 'run', 'pass', 'fail', 'skip', 'extra'}
            and type(counts['available']) is bool and (not counts['available'] or value['captureComplete'])
            and all(type(counts[k]) is int and 0 <= counts[k] <= MAX_CAPTURE for k in ('run', 'pass', 'fail', 'skip', 'extra'))
            and (counts['available'] or not any(counts[k] for k in ('run', 'pass', 'fail', 'skip', 'extra'))), 'receipt')
    return value


def result_template():
    return {'schema': 3, 'selection': 'current-b1-only', 'accepted': False, 'phase': 'admission', 'predicate': 'none', 'completedCohorts': 0,
            'safe': {'status': 'not-started', 'passedTests': 0},
            'B1': {'status': 'not-started', 'passedTests': 0, 'predicate': 'none', 'command': empty_command(), 'receipts': b1_diagnostics()},
            'F-S1': {'status': 'not-selected', 'passedTests': 0, 'predicate': 'none', 'command': empty_command()},
            'nativeCleanupProven': False,
            'platformTeardownReliedOn': False, 'platformTeardownObserved': False, 'pixelReviewPerformed': False}


def validate_result(value):
    require(type(value) is dict and set(value) == set(result_template()) and type(value['schema']) is int and value['schema'] == 3 and value['selection'] == 'current-b1-only', 'receipt')
    require(value['phase'] in PHASES and value['predicate'] in PREDICATES and type(value['completedCohorts']) is int, 'receipt')
    for key in ('accepted', 'nativeCleanupProven', 'platformTeardownReliedOn', 'platformTeardownObserved', 'pixelReviewPerformed'):
        require(type(value[key]) is bool, 'receipt')
    require(not value['platformTeardownObserved'] and not value['pixelReviewPerformed'], 'receipt')
    for key, count in (('safe', 4), ('B1', 1)):
        row = value[key]
        expected = {'status', 'passedTests'} | ({'predicate', 'command'} if key != 'safe' else set()) | ({'receipts'} if key == 'B1' else set())
        require(type(row) is dict and set(row) == expected and row['status'] in ('not-started', 'started', 'passed', 'failed')
                and type(row['passedTests']) is int and row['passedTests'] == (count if row['status'] == 'passed' else 0), 'receipt')
        if key != 'safe':
            require(row['predicate'] in PREDICATES and (row['status'] != 'passed' or row['predicate'] == 'none'), 'receipt')
            validate_command(row['command'])
            if row['status'] == 'passed':
                command_value = row['command']
                require(command_value['returnCodeObserved'] and command_value['returnCode'] == 0 and command_value['captureComplete']
                        and command_value['stderrBytes'] == 0 and command_value['go'] == {'available': True, 'run': 1, 'pass': 1, 'fail': 0, 'skip': 0, 'extra': 0}, 'receipt')
    diagnostics = value['B1']['receipts']
    require(type(diagnostics) is dict and set(diagnostics) == set(RECEIPT_FILES), 'receipt')
    for kind, row in diagnostics.items():
        require(type(row) is dict and set(row) == {'availability', 'value'} and row['availability'] in ('unavailable', 'missing', 'invalid', 'valid'), 'receipt')
        if row['availability'] == 'valid': validate_b1_receipt(kind, row['value'])
        else: require(row['value'] is None, 'receipt')
    if value['B1']['status'] == 'passed': validate_b1(*(diagnostics[k]['value'] for k in RECEIPT_FILES))
    # Historical evidence cannot turn this fixed not-selected row into a PASS.
    require(type(value['F-S1']) is dict and value['F-S1'] == result_template()['F-S1']
            and type(value['F-S1']['passedTests']) is int, 'receipt')
    validate_command(value['F-S1']['command'])
    passed = sum(value[k]['status'] == 'passed' for k in ('safe', 'B1'))
    require(value['completedCohorts'] == passed and value['accepted'] == (passed == 2 and value['phase'] == 'complete' and value['predicate'] == 'none' and value['nativeCleanupProven'] and not value['platformTeardownReliedOn']), 'receipt')
    return value



def validate_provenance(value):
    allowed = {'schema', 'hashMeaning', 'toolchain', 'commit', 'tree', 'sourceManifestSha256', 'sourceFiles', 'playwrightVersion', 'existingChrome', 'embeddedAssetManifestSha256', 'actualMetadata', 'coreImageSha256', 'buildInfoSha256', 'browserCounts', 'priorFS1'}
    require(type(value) is dict and set(value).issubset(allowed) and type(value.get('schema')) is int and value['schema'] == 1
            and value.get('hashMeaning') == 'Measured hosted input identities; no independent source approval inferred.', 'receipt')
    prior = value.get('priorFS1')
    require(type(prior) is dict and set(prior) == set(PRIOR_FS1)
            and all(type(prior[k]) is type(PRIOR_FS1[k]) and prior[k] == PRIOR_FS1[k] for k in PRIOR_FS1), 'receipt')
    for key in ('commit', 'tree'):
        if key in value: require(type(value[key]) is str and re.fullmatch('[0-9a-f]{40}', value[key]) is not None, 'receipt')
    for key in ('sourceManifestSha256', 'embeddedAssetManifestSha256', 'coreImageSha256', 'buildInfoSha256'):
        if key in value: require(type(value[key]) is str and re.fullmatch('[0-9a-f]{64}', value[key]) is not None, 'receipt')
    if 'sourceFiles' in value: require(type(value['sourceFiles']) is int and 1 <= value['sourceFiles'] <= 8192, 'receipt')
    if 'playwrightVersion' in value: require(value['playwrightVersion'] == '1.63.0', 'receipt')
    if 'existingChrome' in value: validate_chrome_provenance(value['existingChrome'])
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


def closed_exception(exc):
    category = str(exc) if type(exc) is RuntimeError else 'interrupted' if isinstance(exc, (KeyboardInterrupt, SystemExit)) else 'internal'
    return category if category in PREDICATES else 'internal'


def main(argv=None):
    global captured_total
    args = sys.argv[1:] if argv is None else argv
    if args not in (['admit'], ['run']): return 1
    report = result_template(); provenance = {'schema': 1, 'hashMeaning': 'Measured hosted input identities; no independent source approval inferred.', 'priorFS1': dict(PRIOR_FS1)}
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
        for name in ('logs', 'build', 'safe', 'B1'):
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
        browser_inputs = existing_chrome_inputs(os.environ)
        chromium = Path(CHROME_PATH); browser_hash = browser_inputs['binary']['sha256']
        try: chrome_version = read('chrome-version', [CHROME_PATH, '--version'])
        except RuntimeError as exc:
            if str(exc) == 'command': raise RuntimeError('browser_version') from None
            raise
        require(chrome_version == 'Google Chrome ' + CHROME_VERSION, 'browser_version')
        require(existing_chrome_inputs(os.environ) == browser_inputs, 'browser_changed')
        package_hashes = {name: sha(web / 'node_modules' / name / 'package.json') for name in installed}
        source_go = (ROOT / 'internal/core/resource_browser_native_test.go').read_text()
        require('"--grep", "' + GREP + '"' in source_go and MATCH in (web / 'playwright.resource-native.config.mjs').read_text(), 'source')
        require(browser_selection('resource-native-local-catalog.acceptance.mjs', TITLE, 'resource-native-local-catalog.acceptance.mjs ' + TITLE), 'inventory')
        provenance.update(commit=commit, tree=tree, sourceManifestSha256=hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest(), sourceFiles=len(before), playwrightVersion=version, existingChrome=chrome_provenance(browser_inputs), embeddedAssetManifestSha256=hashlib.sha256(json.dumps(assets, sort_keys=True).encode()).hexdigest())
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
        first_failure = None
        def unchanged():
            return (before == {name: sha(ROOT / name) for name in tracked} and sha(binary) == image_hash
                    and SETUP.asset_hashes(web / 'dist') == assets and sha(go) == tools['go'] and sha(node) == tools['node']
                    and existing_chrome_inputs(os.environ) == browser_inputs and package_hashes == {name: sha(web / 'node_modules' / name / 'package.json') for name in installed})
        for kind, tests, seconds in (('safe', SAFE, 60), ('B1', [B1], 120)):
            report['phase'] = kind
            require(unchanged(), 'changed')
            report[kind]['status'] = 'started'
            native_env = environment(private / kind / 'home', private / kind / 'tmp', kind)
            if kind == 'B1':
                native_env.update(SOBA_RESOURCE_BROWSER_SCOPE='hosted-job-v1', SOBA_RESOURCE_BROWSER_PRIVATE_ROOT=str(browser_root), SOBA_RESOURCE_BROWSER_WEB_DIR=str(web), SOBA_RESOURCE_BROWSER_NODE=str(node), SOBA_RESOURCE_BROWSER_NODE_SHA256=tools['node'], SOBA_RESOURCE_BROWSER_CHROMIUM=str(chromium), SOBA_RESOURCE_BROWSER_CHROMIUM_SHA256=browser_hash)
            selector = '^' + tests[0] + '$' if len(tests) == 1 else '^(' + '|'.join(tests) + ')$'
            argv = [str(binary), '-test.run=' + selector, '-test.v=true', '-test.count=1', '-test.parallel=1', '-test.shuffle=off', '-test.timeout=' + str(seconds) + 's']
            failure = 'none'
            try:
                out, err = run(kind, argv, seconds + 1, native_env)
                require(not err, 'command'); count = exact_tests(out, tests)
                if kind == 'B1':
                    provenance['browserCounts'] = validate_b1(*(read_json(browser_root / name) for name in RECEIPT_FILES.values()))
                report[kind].update(status='passed', passedTests=count); report['completedCohorts'] += 1
            except BaseException as exc:
                failure = closed_exception(exc)
                report[kind]['status'] = 'failed'
                if first_failure is None: first_failure = (kind, failure)
            if kind == 'safe':
                if failure != 'none': raise RuntimeError(failure)
                continue
            report[kind]['predicate'] = failure
            report[kind]['command'] = command_diagnostic(kind, tests, private)
            if kind == 'B1':
                report[kind]['receipts'] = b1_diagnostics(browser_root, all(report[kind]['command'][k] for k in ('processJoined', 'groupAbsent', 'readersJoined')))
            if failure != 'none': report['platformTeardownReliedOn'] = True
        report['phase'] = 'postflight'
        require(unchanged(), 'changed')
        if first_failure is None: report.update(phase='complete', accepted=True, nativeCleanupProven=True)
        else: report.update(phase=first_failure[0], predicate=first_failure[1])
    except BaseException as exc:
        failure = closed_exception(exc)
        if report['phase'] in ('safe', 'B1') and report[report['phase']]['status'] == 'started':
            report[report['phase']]['status'] = 'failed'
            if report['phase'] != 'safe': report[report['phase']]['predicate'] = failure
        # A later cleanup/input failure cannot erase an earlier cohort failure.
        first = locals().get('first_failure')
        report.update(phase=first[0] if first else report['phase'], predicate=first[1] if first else failure, accepted=False)
        report['platformTeardownReliedOn'] = report['B1']['status'] in ('started', 'failed')
    try:
        if export is not None:
            save_json(export / 'result.json', validate_result(report))
            save_json(export / 'provenance.json', validate_provenance(provenance))
            with open(os.environ['GITHUB_OUTPUT'], 'a') as output: output.write('sanitized_ready=true\n')
    except BaseException:
        report['accepted'] = False
    print('PASS: current B1 exact attempt and test-owned cleanup verified; F-S1 is historical only.' if report['accepted'] else 'FAIL: acceptance incomplete; only closed sanitized diagnostics may be exported.')
    return 0 if report['accepted'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
