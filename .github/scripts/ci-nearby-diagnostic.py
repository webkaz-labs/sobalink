#!/usr/bin/env python3
"""Exact-source nearby diagnostics; export only reconstructed fixed evidence."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

# This runner must not create or consume repository-local Python bytecode.
# Fresh hosted checkouts run the policy checks with -B as well.
sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
TAGS = 'lanlink_integration,directlan_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,ts_omit_udptransport'
CASES = {
    'manual': ('github.com/webkaz-labs/sobalink/internal/core', 'TestLANCoreNearbySetupManualIntegration'),
    'discovery': ('github.com/webkaz-labs/sobalink/internal/nearby', 'TestNativeLoopbackDiscoveryCapability'),
    'browser': ('github.com/webkaz-labs/sobalink/internal/core', 'TestNearbySetupTwoProcessBrowser'),
}
MARKER = 'NEARBY_NATIVE_CAPABILITY '
UNAVAILABLE = {'loopback_scope_unavailable', 'native_socket_unavailable', 'native_receive_unavailable', 'native_receive_timeout', 'native_session_unavailable'}
FAILED = {'candidate_binding_or_freshness_mismatch', 'candidate_survived_close', 'cleanup_close_uncertain', 'cleanup_not_joined', 'foreign_or_missing_packet_metadata', 'native_owner_missing', 'native_session_failed', 'native_session_missing', 'native_session_start_failed', 'owners_missing', 'session_entropy_unavailable', 'session_not_joined', 'socket_not_closed', 'startup_cleanup_uncertain'}
CAPABILITY_KEYS = {'status', 'reason', 'scope', 'bidirectionalCandidates', 'ownersJoined', 'socketFailureStage', 'sendFailureKind', 'windowsNoAncillaryProbe'}
SOCKET_FAILURE_STAGES = {'none', 'socket_create', 'socket_cloexec', 'socket_nonblock',
    'socket_reuse_address', 'socket_interface_options', 'socket_bind', 'socket_wrap',
    'socket_original_close', 'socket_type', 'socket_bound_address', 'socket_read_buffer',
    'socket_write_buffer', 'socket_receive_metadata', 'socket_multicast_interface',
    'socket_multicast_ttl', 'socket_multicast_loopback', 'socket_join_group', 'socket_read',
    'socket_write_deadline', 'socket_write'}
SEND_FAILURE_KINDS = {'none', 'short_payload', 'short_control', 'invalid_argument',
    'source_unavailable', 'network_unreachable', 'host_unreachable', 'permission_denied',
    'unsupported', 'timeout', 'other'}
SEND_PROBE_KEYS = {'status', 'reason', 'socketFailureStage', 'sendFailureKind',
    'metadataVerified', 'ownersJoined', 'productionDiscoveryVerified'}
BROWSER_FAILURE_MAX_BYTES = 4096
BROWSER_CORE_STAGES = {'waiting_checkpoint', 'waiting_discovery', 'invariants', 'hello_a', 'hello_b', 'acknowledged'}
BROWSER_CORE_STATES = {'unknown', 'idle', 'discovering', 'selecting', 'authenticating',
    'awaiting_approval', 'preparing', 'connecting', 'connected', 'recovery_required',
    'needs_restart', 'cancelling', 'cancelled', 'expired', 'stop_required', 'failed'}
BROWSER_CORE_REASONS = {'none', 'other', 'setup_connection_failed', 'setup_commit_uncertain',
    'setup_cleanup_unconfirmed', 'setup_session_expired', 'setup_authority_changed',
    'setup_state_changed', 'lan_environment_proxy', 'lan_environment_override', 'lan_start_failed'}
BROWSER_CORE_HELLO = {'not_run', 'passed', 'failed'}
BROWSER_CORE_CHECKS = {'connected', 'paired', 'untrusted', 'localApproved', 'remoteApproved',
    'sessionMatches', 'planMatches', 'discoveryStopped', 'backend', 'exactlyOnePair',
    'trustUnchanged', 'strictScope', 'localHello', 'remoteReceipt'}
BROWSER_EXPECTED = {
    'schema': 1, 'accepted': True, 'observed': 1, 'passed': 1, 'errors': 0, 'cores': 2,
    'browserContexts': 2, 'locales': ['en', 'ja'], 'widths': [375, 320],
    'path': 'manual-contact-loopback', 'nativeMulticastVerified': False,
    'bilateralFingerprint': True, 'persistentConsent': True, 'approvalResponseLost': True,
    'originalSessionRecovered': True, 'oneApprovalUnpaired': True, 'helloConfirmed': True,
    'receivingTrustUnchanged': True, 'cancelJoined': True, 'browserCommandExited': True,
    'coreChildrenJoined': True,
}


def module(name):
    path = Path(__file__).with_name(name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    value = importlib.util.module_from_spec(spec)
    # Execute the bound source bytes directly, never an untracked .pyc cache.
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value



# Only exact dependency trees prepared/verified by the pinned workflow are
# excluded. No source subtree, wildcard cache, .go, .py, or browser test path is
# excluded. Use git's complete "others" inventory, including ignored files.
DEPENDENCY_PREFIXES = ('.sobalink-deps/tailscale/', '.sobalink-deps/wireguard/',
                       '.sobalink-deps/gvisor/', 'web/node_modules/')


def validate_untracked(raw, *, after_build=False):
    if type(raw) is not bytes or raw and not raw.endswith(b'\0'):
        raise ValueError('invalid untracked inventory')
    for encoded in raw.split(b'\0'):
        if not encoded:
            continue
        path = encoded.decode('utf-8')
        if any(part in ('', '.', '..') for part in path.split('/')) or '\\' in path:
            raise ValueError('unexpected untracked input')
        if any(path.startswith(prefix) for prefix in DEPENDENCY_PREFIXES):
            continue
        # The sole local build output is allowed only after our source build.
        if after_build and path == 'web/tsconfig.tsbuildinfo':
            continue
        raise ValueError('unexpected untracked input')


def require_source_inventory(log, *, after_build=False):
    with log.open('ab') as errors:
        raw = subprocess.check_output(['git', 'ls-files', '--others', '-z'], cwd=ROOT,
                                      stderr=errors, timeout=15)
    validate_untracked(raw, after_build=after_build)
    # npm ci and prepare-engine create real directories, never outside-tree
    # dependency symlinks. This prevents an excluded root escaping its scope.
    if any((ROOT / prefix.rstrip('/')).is_symlink() for prefix in DEPENDENCY_PREFIXES):
        raise ValueError('dependency root is not a prepared directory')


def unique_object(pairs):
    out = {}
    for key, value in pairs:
        if key in out:
            raise ValueError('duplicate field')
        out[key] = value
    return out


def parse_json(text):
    return json.loads(text, object_pairs_hook=unique_object,
                      parse_constant=lambda _: (_ for _ in ()).throw(ValueError('invalid number')))


def send_failure(stage, kind):
    if (type(stage) is not str or stage not in SOCKET_FAILURE_STAGES
            or type(kind) is not str or kind not in SEND_FAILURE_KINDS
            or (stage == 'socket_write') != (kind != 'none')):
        raise ValueError('invalid fixed send failure')


def send_probe(value):
    if (type(value) is not dict or set(value) != SEND_PROBE_KEYS
            or any(type(value[k]) is not bool for k in ('metadataVerified', 'ownersJoined', 'productionDiscoveryVerified'))
            or value['productionDiscoveryVerified']):
        raise ValueError('invalid supplemental probe schema')
    send_failure(value['socketFailureStage'], value['sendFailureKind'])
    status, reason = value['status'], value['reason']
    if type(status) is not str or type(reason) is not str:
        raise ValueError('invalid supplemental probe type')
    if status == 'verified':
        if (reason != 'scoped_no_ancillary_delivery' or not value['metadataVerified']
                or not value['ownersJoined'] or value['socketFailureStage'] != 'none'):
            raise ValueError('incomplete supplemental proof')
    elif status == 'unavailable':
        if reason not in UNAVAILABLE or value['metadataVerified']:
            raise ValueError('invalid supplemental unavailable result')
    elif status != 'failed' or reason not in FAILED:
        raise ValueError('invalid supplemental failure')
    return {key: value[key] for key in sorted(SEND_PROBE_KEYS)}


def capability(value):
    if (type(value) is not dict or set(value) != CAPABILITY_KEYS or value['scope'] != 'isolated_loopback'
            or any(type(value[k]) is not bool for k in ('bidirectionalCandidates', 'ownersJoined'))):
        raise ValueError('invalid capability schema')
    status, reason, stage = value['status'], value['reason'], value['socketFailureStage']
    if type(status) is not str or type(reason) is not str:
        raise ValueError('invalid capability type')
    send_failure(stage, value['sendFailureKind'])
    probe = None if value['windowsNoAncillaryProbe'] is None else send_probe(value['windowsNoAncillaryProbe'])
    if probe is not None:
        if stage != 'socket_write' or not value['ownersJoined'] or status not in ('unavailable', 'failed'):
            raise ValueError('supplemental probe outside joined write failure')
        if probe['status'] == 'failed' and (status != 'failed' or reason != probe['reason']):
            raise ValueError('unsafe supplemental probe was not fatal')
    if status == 'verified':
        if stage != 'none' or probe is not None or reason != 'bidirectional_scoped_mdns' or not value['bidirectionalCandidates'] or not value['ownersJoined']:
            raise ValueError('incomplete capability proof')
    elif status == 'unavailable':
        if reason not in UNAVAILABLE or value['bidirectionalCandidates']:
            raise ValueError('invalid unavailable classification')
    elif status == 'not_run':
        if stage != 'none' or probe is not None or reason != 'opt_in_required' or value['bidirectionalCandidates'] or value['ownersJoined']:
            raise ValueError('invalid opt-in classification')
    elif status != 'failed' or reason not in FAILED:
        raise ValueError('unknown capability classification')
    result = {key: value[key] for key in sorted(CAPABILITY_KEYS)}
    result['windowsNoAncillaryProbe'] = probe
    return result



BROWSER_PHASES = {'launch', 'descriptor', 'authenticate-a', 'authenticate-b', 'open-a', 'start-a', 'cancel-a', 'checkpoint-cancelled', 'restart-a', 'open-b', 'start-b', 'manual-a', 'manual-b', 'plan-a', 'plan-b', 'fingerprint-a', 'fingerprint-b', 'checkpoint-review', 'approve-a', 'recover-a', 'checkpoint-one-approved', 'approve-b', 'connected-a', 'connected-b', 'checkpoint-connected', 'cleanup', 'core-start', 'core-cancelled', 'core-review', 'core-one-approved', 'core-connected', 'core-cleanup', 'manual-edit-a', 'manual-edit-b', 'manual-submit-a', 'manual-submit-b', 'fingerprint-read-a', 'fingerprint-read-b', 'fingerprint-initial-a', 'fingerprint-initial-b', 'fingerprint-mismatch-edit-a', 'fingerprint-mismatch-edit-b', 'fingerprint-mismatch-blocked-a', 'fingerprint-mismatch-blocked-b', 'fingerprint-match-edit-a', 'fingerprint-match-edit-b', 'fingerprint-verify-a', 'fingerprint-verify-b', 'fingerprint-consent-a', 'fingerprint-consent-b', 'fingerprint-unverify-a', 'fingerprint-unverify-b', 'fingerprint-ready-a', 'fingerprint-ready-b', 'approve-send-a', 'approve-response-a', 'approve-uncertain-a', 'recover-request-a', 'recover-receipt-a', 'approve-send-b', 'approve-receipt-b'}
BROWSER_ERROR_KINDS = {'timeout', 'assertion', 'selector_ambiguous', 'browser_launch', 'browser_closed', 'runtime', 'command_boundary', 'checkpoint', 'cleanup', 'interrupted', 'unexpected', 'core_evidence', 'evidence_unavailable', 'locator_missing', 'element_disabled', 'element_hidden', 'element_not_editable', 'element_detached', 'pointer_intercepted', 'element_unstable'}


def browser_core_evidence(value):
    if (type(value) is not dict or set(value) != {'checkpointArrived', 'stage', 'peers'}
            or type(value['checkpointArrived']) is not bool
            or type(value['stage']) is not str or value['stage'] not in BROWSER_CORE_STAGES
            or type(value['peers']) is not list or len(value['peers']) != 2):
        raise ValueError('invalid fixed core evidence')
    peers = []
    for peer in value['peers']:
        if (type(peer) is not dict or set(peer) != {'observed', 'state', 'reason', 'checks', 'freshHello'}
                or type(peer['observed']) is not bool
                or type(peer['state']) is not str or peer['state'] not in BROWSER_CORE_STATES
                or type(peer['reason']) is not str or peer['reason'] not in BROWSER_CORE_REASONS
                or type(peer['freshHello']) is not str or peer['freshHello'] not in BROWSER_CORE_HELLO
                or type(peer['checks']) is not dict or set(peer['checks']) != BROWSER_CORE_CHECKS
                or any(type(flag) is not bool for flag in peer['checks'].values())):
            raise ValueError('invalid fixed peer evidence')
        peers.append({'observed': peer['observed'], 'state': peer['state'], 'reason': peer['reason'],
                      'freshHello': peer['freshHello'],
                      'checks': {key: peer['checks'][key] for key in sorted(BROWSER_CORE_CHECKS)}})
    return {'checkpointArrived': value['checkpointArrived'], 'stage': value['stage'], 'peers': peers}


BROWSER_CLEANUP_STAGES = {'browser_returned', 'cancel_snapshot', 'cancel_request', 'cancel_reply',
    'join', 'post_cancel_snapshot', 'post_cancel_backend', 'post_cancel_hello', 'listener_release',
    'child_exit', 'summary_open', 'summary_write', 'summary_close', 'complete'}
BROWSER_CLEANUP_FLAGS = {'browserCommandExited', 'browserReceiptVerified', 'contextEnded',
                         'summaryOpened', 'summaryWritten', 'summaryClosed'}
BROWSER_CLEANUP_PEER_FLAGS = {'cancelReturned', 'cancelDecoded', 'joined', 'postObserved', 'connected', 'backend'}
BROWSER_CLEANUP_LISTENER = {'not_run', 'dial_rejected', 'still_open'}
BROWSER_CLEANUP_CHILD = {'not_run', 'requesting', 'replied', 'joined', 'exit_failed', 'timeout'}


def browser_cleanup_evidence(value):
    if (type(value) is not dict or set(value) != BROWSER_CLEANUP_FLAGS | {'stage', 'peer', 'peers'}
            or any(type(value[key]) is not bool for key in BROWSER_CLEANUP_FLAGS)
            or type(value['stage']) is not str or value['stage'] not in BROWSER_CLEANUP_STAGES
            or type(value['peer']) is not str or value['peer'] not in {'a', 'b', 'none'}
            or type(value['peers']) is not list or len(value['peers']) != 2):
        raise ValueError('invalid fixed cleanup evidence')
    peers = []
    categories = {'cancelState': BROWSER_CORE_STATES, 'cancelReason': BROWSER_CORE_REASONS,
                  'state': BROWSER_CORE_STATES, 'reason': BROWSER_CORE_REASONS,
                  'freshHello': BROWSER_CORE_HELLO, 'listener': BROWSER_CLEANUP_LISTENER,
                  'childExit': BROWSER_CLEANUP_CHILD}
    for peer in value['peers']:
        if (type(peer) is not dict or set(peer) != BROWSER_CLEANUP_PEER_FLAGS | set(categories)
                or any(type(peer[key]) is not bool for key in BROWSER_CLEANUP_PEER_FLAGS)
                or any(type(peer[key]) is not str or peer[key] not in allowed for key, allowed in categories.items())):
            raise ValueError('invalid fixed cleanup peer')
        peers.append({key: peer[key] for key in sorted(peer)})
    return {**{key: value[key] for key in sorted(BROWSER_CLEANUP_FLAGS | {'stage', 'peer'})}, 'peers': peers}


def browser_failure(value):
    if (type(value) is not dict or not {'schema', 'phase', 'errorKind'} <= set(value) <= {'schema', 'phase', 'errorKind', 'core', 'cleanup'}
            or type(value['schema']) is not int or value['schema'] != 1
            or type(value['phase']) is not str or value['phase'] not in BROWSER_PHASES
            or type(value['errorKind']) is not str or value['errorKind'] not in BROWSER_ERROR_KINDS):
        raise ValueError('invalid fixed browser failure')
    result = {'schema': 1, 'phase': value['phase'], 'errorKind': value['errorKind']}
    if 'core' in value:
        result['core'] = browser_core_evidence(value['core'])
    if 'cleanup' in value:
        result['cleanup'] = browser_cleanup_evidence(value['cleanup'])
    return result


def browser_summary(value):
    if type(value) is not dict or set(value) != set(BROWSER_EXPECTED):
        raise ValueError('invalid browser receipt schema')
    for key, expected in BROWSER_EXPECTED.items():
        actual = value[key]
        if type(actual) is not type(expected) or actual != expected:
            raise ValueError('invalid browser receipt evidence')
        if type(expected) is list and any(type(a) is not type(b) for a, b in zip(actual, expected)):
            raise ValueError('invalid browser receipt list')
    return dict(BROWSER_EXPECTED)


def test_events(raw, case, returncode):
    """An exact run/pass is mandatory; only a typed unavailable marker may skip."""
    if case not in CASES or type(returncode) is not int or len(raw) > 8 * 1024 * 1024:
        raise ValueError('invalid case output')
    package, name = CASES[case]
    parser = module('ci-go-test')
    lifecycle, package_lifecycle, markers = [], [], []
    for line in raw.splitlines():
        event = parser.parse_event(line)
        if event['Action'] in parser.BUILD_ACTIONS:
            if event['Action'] == 'build-fail':
                raise ValueError('case build failed')
            continue
        if event.get('Package') != package or event.get('Test') not in (None, name):
            raise ValueError('unexpected executed test')
        action = event['Action']
        if action != 'output':
            (lifecycle if event.get('Test') else package_lifecycle).append(action)
        if MARKER in event.get('Output', ''):
            if case != 'discovery' or event.get('Test') != name or event['Output'].count(MARKER) != 1:
                raise ValueError('unexpected capability marker')
            markers.append(capability(parse_json(event['Output'].split(MARKER, 1)[1].strip())))
    if returncode != 0 or package_lifecycle != ['start', 'pass']:
        raise ValueError('test process did not pass')
    if case == 'discovery':
        if len(markers) != 1:
            raise ValueError('exact capability marker missing')
        result = markers[0]
        expected = ['run', 'pass'] if result['status'] == 'verified' else ['run', 'skip']
        if result['status'] not in ('verified', 'unavailable') or lifecycle != expected:
            raise ValueError('capability event mismatch or not performed')
        return result
    if markers or lifecycle != ['run', 'pass']:
        raise ValueError('required case skipped or incomplete')
    return True


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def assets(directory):
    paths = sorted(directory.rglob('*'))
    if not (directory / 'index.html').is_file() or any(path.is_symlink() for path in paths):
        raise ValueError('invalid embedded asset inventory')
    return {path.relative_to(directory).as_posix(): digest(path) for path in paths if path.is_file()}


def empty_result():
    return {'schema': 1, 'diagnosticPassed': False, 'manualCorePassed': False, 'browserPassed': False,
            'nativeDiscoveryVerified': False, 'nativeDiscovery': None, 'browser': None, 'browserFailure': None,
            'scope': 'synthetic-loopback-manual-core-and-browser', 'physicalLANVerified': False,
            'releaseValidation': False}


def main(argv=None):
    if (sys.argv[1:] if argv is None else argv) or sys.platform != 'linux' or os.environ.get('GITHUB_ACTIONS') != 'true':
        print('FAIL: fixed Linux hosted diagnostic entry required.')
        return 1
    report, provenance = empty_result(), {'schema': 1, 'stage': 'preflight'}
    export_ready, private = False, None
    export = Path(os.environ['RUNNER_TEMP']) / 'soba-nearby-sanitized'
    try:
        os.umask(0o077)
        export.mkdir(mode=0o700, exist_ok=False)
        export_ready = True
        private = Path(tempfile.mkdtemp(prefix='soba-nearby-', dir=os.environ['RUNNER_TEMP']))
        log = private / 'private-build.log'
        def read(command, cwd=ROOT):
            with log.open('ab') as errors:
                return subprocess.check_output(command, cwd=cwd, stderr=errors, text=True, timeout=15).strip()
        def run(command, *, cwd=ROOT, env=None, timeout=600):
            with log.open('ab') as output:
                subprocess.run(command, cwd=cwd, env=env, stdout=output, stderr=subprocess.STDOUT,
                               check=True, shell=False, timeout=timeout)
        commit, tree = read(['git', 'rev-parse', 'HEAD']), read(['git', 'rev-parse', 'HEAD^{tree}'])
        selector = module('ci-diagnostic')
        event = parse_json(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
        if selector.select(os.environ['GITHUB_EVENT_NAME'], event, os.environ['GITHUB_REPOSITORY'],
                           os.environ['GITHUB_REF'], os.environ['GITHUB_SHA'], commit) != 'nearby':
            raise ValueError('wrong diagnostic suite')
        if read(['git', 'status', '--porcelain', '--untracked-files=no']) or not re.fullmatch('[0-9a-f]{40}', tree):
            raise ValueError('modified or unbound source')
        require_source_inventory(log)
        versions = {'go': read(['go', 'env', 'GOVERSION']), 'node': read(['node', '--version']), 'npm': read(['npm', '--version'])}
        if versions != {'go': 'go1.27.1', 'node': 'v24.19.0', 'npm': '11.9.0'}:
            raise ValueError('toolchain mismatch')
        web = ROOT / 'web'
        chromium = Path(read(['node', '--input-type=module', '-e', "import {chromium} from '@playwright/test'; console.log(chromium.executablePath())"], web))
        if not chromium.is_absolute() or not chromium.is_file():
            raise ValueError('pinned Chromium unavailable')
        inventory = read(['git', 'ls-files']).splitlines()
        before = {name: digest(ROOT / name) for name in inventory}
        provenance.update(commit=commit, tree=tree, versions=versions,
                          sourceSha256=hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest(),
                          lockSha256=digest(web / 'package-lock.json'), chromiumSha256=digest(chromium),
                          toolchainSha256={name: digest(Path(shutil.which(name))) for name in ('go', 'node')})
        provenance['stage'] = 'assets'
        asset_hashes = assets(web / 'dist')
        run(['npm', 'run', 'build'], cwd=web, timeout=120)
        if assets(web / 'dist') != asset_hashes or before != {name: digest(ROOT / name) for name in inventory}:
            raise ValueError('embedded assets do not reproduce source')
        require_source_inventory(log, after_build=True)
        provenance['assets'] = asset_hashes
        provenance['stage'] = 'compile'
        env = dict(os.environ, GOTOOLCHAIN='local', GOWORK='off', GOPROXY='off', GOSUMDB='off', GOTELEMETRY='off', GOFLAGS='-mod=readonly -buildvcs=false', TMPDIR=str(private))
        core, nearby = private / 'core.test', private / 'nearby.test'
        for package, binary in (('./internal/core', core), ('./internal/nearby', nearby)):
            run(['go', 'test', '-race', '-tags=' + TAGS, '-c', '-o', str(binary), package], env=env)
        provenance['fixtures'] = {'core': digest(core), 'nearby': digest(nearby)}
        env.update(SOBALINK_RUN_LAN_INTEGRATION='1', SOBALINK_RUN_SETUP_BROWSER_NATIVE='1', SOBALINK_NATIVE_NEARBY_LOOPBACK='1',
                   SOBA_TRANSFER_BROWSER_NODE=shutil.which('node'), SOBA_TRANSFER_BROWSER_CHROMIUM=str(chromium),
                   SOBA_SETUP_BROWSER_SUMMARY=str(private / 'browser.json'), SOBA_SETUP_BROWSER_FAILURE=str(private / 'browser-failure.json'))
        for case, (package, name) in CASES.items():
            provenance['stage'] = case
            binary = nearby if case == 'discovery' else core
            output = private / (case + '.jsonl')
            try:
                with output.open('wb') as stdout, log.open('ab') as stderr:
                    result = subprocess.run(['go', 'tool', 'test2json', '-p', package, str(binary), '-test.v=test2json',
                                             '-test.run=^' + name + '$', '-test.count=1', '-test.timeout=3m'],
                                            cwd=ROOT / ('internal/nearby' if case == 'discovery' else 'internal/core'),
                                            env=env, stdout=stdout, stderr=stderr, shell=False, timeout=210)
                if output.stat().st_size > 8 * 1024 * 1024:
                    raise ValueError('case output overflow')
                evidence = test_events(output.read_bytes(), case, result.returncode)
                if case == 'discovery':
                    report['nativeDiscovery'] = evidence
                    report['nativeDiscoveryVerified'] = evidence['status'] == 'verified'
                elif case == 'manual':
                    report['manualCorePassed'] = True
                else:
                    receipt = private / 'browser.json'
                    if receipt.is_symlink() or receipt.stat().st_size > 8192:
                        raise ValueError('invalid browser receipt file')
                    if (private / 'browser-failure.json').exists():
                        raise ValueError('browser success conflicts with failure evidence')
                    report['browser'] = browser_summary(parse_json(receipt.read_text()))
                    report['browserPassed'] = True
            except Exception:
                # Preserve only a closed, reconstructed first failure. Never
                # export the native/framework log or an exception's text.
                if case == 'browser':
                    failure = private / 'browser-failure.json'
                    try:
                        if failure.is_symlink() or not failure.is_file() or failure.stat().st_size > BROWSER_FAILURE_MAX_BYTES:
                            raise ValueError('invalid fixed failure file')
                        report['browserFailure'] = browser_failure(parse_json(failure.read_text()))
                    except Exception:
                        report['browserFailure'] = {'schema': 1, 'phase': 'launch', 'errorKind': 'evidence_unavailable'}
        provenance['stage'] = 'verify-source'
        require_source_inventory(log, after_build=True)
        if before != {name: digest(ROOT / name) for name in inventory} or assets(web / 'dist') != asset_hashes or provenance['fixtures'] != {'core': digest(core), 'nearby': digest(nearby)}:
            raise ValueError('source or executable changed')
        report['diagnosticPassed'] = report['manualCorePassed'] and report['browserPassed'] and report['nativeDiscovery'] is not None
        provenance['stage'] = 'passed' if report['diagnosticPassed'] else 'runtime-failed'
        if report['diagnosticPassed']:
            shutil.rmtree(private)
            private = None
    except Exception:
        report['diagnosticPassed'] = False
    try:
        if not export_ready:
            raise ValueError('export unavailable')
        (export / 'result.json').write_text(json.dumps(report, sort_keys=True) + '\n')
        (export / 'provenance.json').write_text(json.dumps(provenance, sort_keys=True) + '\n')
        with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
            output.write('sanitized_ready=true\n')
    except Exception:
        report['diagnosticPassed'] = False
    print('PASS: manual Core and browser verified; see separate native discovery capability status.' if report['diagnosticPassed'] else 'FAIL: nearby diagnostic acceptance not established; private details withheld.')
    return 0 if report['diagnosticPassed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
