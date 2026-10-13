#!/usr/bin/env python3
"""Exact-source Darwin API compilation and bounded DNS-SD capability evidence."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import shlex
import sys
import tempfile

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
PACKAGE = 'github.com/webkaz-labs/sobalink/internal/nearby'
CASES = {
    'TestDNSServiceLocalOnlyCapability': 'os_local_only',
    'TestDNSServiceSelectedLoopbackCapability': 'selected_loopback_os',
    'TestDNSServiceSelectedInterfaceCapability': 'selected_private_interface_os',
    'TestDNSServiceRawReceiveCapability': 'selected_private_interface_raw_receive',
}
SELECTOR = '^TestDNSService(LocalOnly|SelectedLoopback|SelectedInterface|RawReceive)Capability$'
OPT_INS = ('SOBALINK_DNSSD_LOCAL_ONLY', 'SOBALINK_DNSSD_SELECTED_LOOPBACK',
           'SOBALINK_DNSSD_SELECTED_INTERFACE', 'SOBALINK_DNSSD_RAW_RECEIVE')
MARKER = 'NEARBY_DNSSD_CAPABILITY '
FLAGS = {'reciprocalRecords', 'reciprocalCandidates', 'withdrawalObserved', 'ownersJoined',
         'rawPacketVerified', 'networkMulticastVerified'}
KEYS = FLAGS | {'status', 'reason', 'scope', 'failureStage', 'failureKind'}
STAGES = {'none', 'allocation', 'unknown', 'create_connection', 'schedule', 'registration', 'query', 'callback', 'admission'}
KINDS = {'none', 'unavailable', 'permission_denied', 'name_conflict', 'invalid_argument', 'unsupported'}
UNAVAILABLE = {'deadline', 'loopback_scope_unavailable', 'private_scope_unavailable', 'native_api_unavailable',
               'selected_binding_changed', 'raw_receiver_unavailable', 'raw_receive_unavailable'}
FAILED = {'fixture_unavailable', 'owners_not_joined', 'cleanup_uncertain', 'owners_missing',
          'candidate_survived_close', 'provider_invariant'}
PROOFS = {
    'os_local_only': ('native_local_api_observed', {'reciprocalRecords', 'withdrawalObserved', 'ownersJoined'}),
    'selected_loopback_os': ('native_scoped_api_observed', {'reciprocalCandidates', 'ownersJoined'}),
    'selected_private_interface_os': ('native_scoped_api_observed', {'reciprocalCandidates', 'ownersJoined'}),
    'selected_private_interface_raw_receive': ('native_raw_receive_observed', {'rawPacketVerified', 'ownersJoined'}),
}
POLICY_FILES = ('.github/workflows/activation-diagnostic.yml', '.github/scripts/ci-diagnostic.py',
    '.github/scripts/ci-dnssd-diagnostic.py', '.github/scripts/test_ci_dnssd_diagnostic.py',
    '.github/scripts/ci-nearby-native.py', '.github/scripts/ci-nearby-diagnostic.py',
    '.github/scripts/ci-go-test.py', 'internal/nearby/provider_darwin.go',
    'internal/nearby/dnssd_bridge_darwin.c', 'internal/nearby/dnssd_bridge_darwin.h',
    'internal/nearby/dnssd_native_acceptance_test.go', 'internal/nearby/dnssd_raw_receive_test.go',
    'go.mod', 'go.sum')


def module(name):
    path = Path(__file__).with_name(name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


def capability(value, scope):
    if (type(value) is not dict or set(value) != KEYS or value['scope'] != scope or scope not in PROOFS
            or any(type(value[key]) is not bool for key in FLAGS)
            or any(type(value[key]) is not str for key in KEYS - FLAGS)
            or value['failureStage'] not in STAGES or value['failureKind'] not in KINDS
            or value['networkMulticastVerified']):
        raise ValueError('invalid closed DNS-SD receipt')
    stage, kind, status, reason = (value[key] for key in ('failureStage', 'failureKind', 'status', 'reason'))
    if (stage == 'none') != (kind == 'none'):
        raise ValueError('incomplete API failure classification')
    if status == 'verified':
        expected_reason, expected_flags = PROOFS[scope]
        if reason != expected_reason or stage != 'none' or {key for key in FLAGS if value[key]} != expected_flags:
            raise ValueError('incomplete scope-specific proof')
    elif status == 'unavailable':
        if reason not in UNAVAILABLE or (reason == 'native_api_unavailable') != (stage != 'none'):
            raise ValueError('invalid unavailable classification')
    elif status == 'failed':
        # Cleanup can override the reason after an API error; preserve its fixed
        # originating stage/kind rather than erase it or report raw OS text.
        if reason not in FAILED:
            raise ValueError('unknown invariant failure')
    elif status == 'not_run':
        if reason != 'opt_in_required' or stage != 'none' or any(value[key] for key in FLAGS):
            raise ValueError('invalid opt-in receipt')
    else:
        raise ValueError('unknown DNS-SD status')
    # Non-verified observations can retain partial work, but must not acquire
    # unrelated scope proofs that the native fixture cannot produce.
    if any(value[key] for key in FLAGS - PROOFS[scope][1]):
        raise ValueError('proof escaped diagnostic scope')
    return {key: value[key] for key in sorted(KEYS)}


def test_events(raw, returncode):
    native, shared, parser = module('ci-nearby-native'), module('ci-nearby-diagnostic'), module('ci-go-test')
    if type(raw) is not bytes or len(raw) > 2 * native.RUNTIME_BYTES or type(returncode) is not int:
        raise ValueError('invalid DNS-SD capture')
    lifecycles = {name: [] for name in CASES}
    markers = {name: [] for name in CASES}
    package, active, order_valid = [], None, True
    for line in raw.splitlines():
        event = parser.parse_event(line)
        name, action = event.get('Test'), event['Action']
        if event.get('Package') != PACKAGE or name is not None and name not in CASES:
            raise ValueError('unexpected executed DNS-SD test')
        if action != 'output':
            if name is None:
                package.append(action)
            else:
                lifecycles[name].append(action)
                if action == 'run':
                    order_valid &= active is None and package == ['start']
                    active = name
                elif action in ('pass', 'skip', 'fail'):
                    order_valid &= active == name
                    active = None
                else:
                    order_valid = False
        if MARKER in event.get('Output', ''):
            if name not in CASES or event['Output'].count(MARKER) != 1 or active != name:
                raise ValueError('invalid DNS-SD marker owner')
            markers[name].append(capability(shared.parse_json(event['Output'].split(MARKER, 1)[1].strip()), CASES[name]))
    result, accepted = {}, order_valid and active is None and returncode == 0 and package == ['start', 'pass']
    for name, scope in CASES.items():
        record = markers[name][0] if len(markers[name]) == 1 else None
        result[scope] = record
        terminal = {'verified': 'pass', 'unavailable': 'skip'}.get(record['status']) if record else None
        accepted &= terminal is not None and lifecycles[name] == ['run', terminal]
    return result, bool(accepted)


def compiler_kind(stderr, state):
    if state in ('timeout', 'output_limit', 'launch_failed', 'capture_failed'):
        return state
    text = stderr.lower()
    for kind, needles in (
            ('missing_header', (b'file not found', b'no such file or directory')),
            ('missing_declaration', (b'undeclared function', b'undeclared identifier', b'implicit declaration')),
            ('argument_count', (b'too few arguments', b'too many arguments')),
            ('type_mismatch', (b'incompatible', b'conflicting types')),
            ('link_failed', (b'undefined symbols', b'linker command failed'))):
        if any(needle in text for needle in needles):
            return kind
    return 'compiler_failed'


def compiler_identity(raw, sdk_version, sdk_build):
    first = raw.decode('utf-8').splitlines()[0]
    match = re.fullmatch(r'Apple clang version ([0-9]+(?:\.[0-9]+){1,3}) \(clang-([0-9]+(?:\.[0-9]+){0,5})\)', first)
    if not match or not re.fullmatch(r'[0-9]+(?:\.[0-9]+){1,3}', sdk_version) or not re.fullmatch(r'[0-9]{1,3}[A-Z][0-9]{1,6}[a-z]?', sdk_build):
        raise ValueError('unrecognized native compiler identity')
    return {'vendor': 'apple-clang', 'version': match[1], 'build': match[2], 'sdkVersion': sdk_version, 'sdkBuild': sdk_build}


def empty_result():
    return {'schema': 1, 'diagnosticPassed': False, 'cCompilePassed': False, 'goCompilePassed': False,
            'compileFailureKind': None, 'processStatus': 'not_run', 'eventsAccepted': False,
            'capabilities': {scope: None for scope in CASES.values()}, 'selectedInterfaceVerified': False,
            'rawReceiveVerified': False, 'networkMulticastVerified': False, 'physicalLANVerified': False,
            'automaticCoreBrowserVerified': False, 'releaseValidation': False}


def main(argv=None):
    if (sys.argv[1:] if argv is None else argv) or sys.platform != 'darwin' or os.environ.get('GITHUB_ACTIONS') != 'true':
        print('FAIL: fixed hosted Darwin diagnostic entry required.')
        return 1
    report, provenance = empty_result(), {'schema': 1, 'stage': 'preflight'}
    export_ready, private = False, None
    try:
        os.umask(0o077)
        export = Path(os.environ['RUNNER_TEMP']) / 'soba-dnssd-sanitized'
        export.mkdir(mode=0o700, exist_ok=False)
        export_ready = True
        private = Path(tempfile.mkdtemp(prefix='soba-dnssd-', dir=os.environ['RUNNER_TEMP']))
        native, shared, selector = module('ci-nearby-native'), module('ci-nearby-diagnostic'), module('ci-diagnostic')
        read = lambda command: native.checked(command).decode().strip()
        commit, tree = read(['git', 'rev-parse', 'HEAD']), read(['git', 'rev-parse', 'HEAD^{tree}'])
        event_path = Path(os.environ['GITHUB_EVENT_PATH'])
        if event_path.stat().st_size > native.CAPTURE_BYTES:
            raise ValueError('event size exceeded')
        event = shared.parse_json(event_path.read_text(encoding='utf-8'))
        if (selector.select(os.environ['GITHUB_EVENT_NAME'], event, os.environ['GITHUB_REPOSITORY'],
                os.environ['GITHUB_REF'], os.environ['GITHUB_SHA'], commit) != 'dnssd'
                or not re.fullmatch('[0-9a-f]{40}', tree)):
            raise ValueError('wrong diagnostic suite or source')
        before = native.source_inventory()
        versions = shared.parse_json(native.checked(['go', 'env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH', 'GOOS', 'GOARCH', 'CGO_ENABLED']))
        identity = native.identities('darwin-arm64', versions)
        clang = Path(read(['xcrun', '--sdk', 'macosx', '--find', 'clang']))
        sdk = Path(read(['xcrun', '--sdk', 'macosx', '--show-sdk-path']))
        if not clang.is_absolute() or not clang.is_file() or not sdk.is_absolute() or not sdk.is_dir():
            raise ValueError('native SDK unavailable')
        compiler = compiler_identity(native.checked([str(clang), '--version']),
            read(['xcrun', '--sdk', 'macosx', '--show-sdk-version']),
            read(['xcrun', '--sdk', 'macosx', '--show-sdk-build-version']))
        sdk_files = {'dns_sd.h': sdk / 'usr/include/dns_sd.h', 'SDKSettings.json': sdk / 'SDKSettings.json',
                     'libdns_sd.tbd': sdk / 'usr/lib/libdns_sd.tbd'}
        tool = Path(shutil.which('go'))
        compiler_hashes = {'clang': native.digest(clang), 'go': native.digest(tool),
                          'sdk': {key: native.digest(path) for key, path in sdk_files.items()}}
        provenance.update(commit=commit, tree=tree, platform='darwin-arm64', toolchain=identity,
            compiler=compiler, toolchainSha256=compiler_hashes, tests=dict(CASES),
            sourceSha256=hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest(),
            policySha256={name: before[name] for name in POLICY_FILES}, modules=list(native.MODULES))
        # Use the actual selected SDK without changing xcode-select or any host
        # setting. No identifiers or absolute tool/SDK paths enter the receipt.
        env = dict(os.environ, GOTOOLCHAIN='local', GOWORK='off', GOTELEMETRY='off',
            GOFLAGS='-mod=readonly -buildvcs=false', CGO_ENABLED='1', GOMAXPROCS='2',
            CC=shlex.quote(str(clang)), SDKROOT=str(sdk), CGO_CFLAGS='-isysroot ' + shlex.quote(str(sdk)),
            CGO_LDFLAGS='-isysroot ' + shlex.quote(str(sdk)), TMPDIR=str(private), TMP=str(private), TEMP=str(private))
        provenance['stage'] = 'c-compile'
        state, _, _, stderr = native.capture([str(clang), '-arch', 'arm64', '-isysroot', str(sdk),
            '-std=c11', '-c', str(ROOT / 'internal/nearby/dnssd_bridge_darwin.c'),
            '-o', str(private / 'dnssd-bridge.o')], env=env, timeout=60)
        if state != 'completed':
            report['compileFailureKind'] = compiler_kind(stderr, state)
            raise ValueError('native C API compilation failed')
        report['cCompilePassed'] = True
        provenance['cObjectSha256'] = native.digest(private / 'dnssd-bridge.o')
        provenance['stage'] = 'dependencies'
        native.checked(['go', 'mod', 'download', *native.MODULES], env=env, timeout=120)
        env.update(GOPROXY='off', GOSUMDB='off')
        provenance['stage'] = 'go-compile'
        binary = private / 'dnssd.test'
        state, _, _, stderr = native.capture(['go', 'test', '-race', '-p=1', '-c', '-o', str(binary), './internal/nearby'], env=env, timeout=300)
        if state != 'completed':
            report['compileFailureKind'] = compiler_kind(stderr, state)
            raise ValueError('native Go API compilation failed')
        report['goCompilePassed'] = True
        provenance['testSha256'] = native.digest(binary)
        env.update({key: '1' for key in OPT_INS})
        provenance['stage'] = 'runtime'
        state, code, stdout, _ = native.capture([str(binary), '-test.v=test2json', '-test.run=' + SELECTOR,
            '-test.count=1', '-test.timeout=60s'], env=env, timeout=75, limit=native.RUNTIME_BYTES)
        report['processStatus'] = state
        if state in ('completed', 'nonzero'):
            converted, _, data, _ = native.capture(['go', 'tool', 'test2json', '-p', PACKAGE],
                env=env, timeout=15, limit=2 * native.RUNTIME_BYTES, stdin=stdout)
            if converted == 'completed':
                report['capabilities'], report['eventsAccepted'] = test_events(data, code)
        provenance['stage'] = 'verify-source'
        if (before != native.source_inventory() or provenance['testSha256'] != native.digest(binary)
                or compiler_hashes != {'clang': native.digest(clang), 'go': native.digest(tool),
                    'sdk': {key: native.digest(path) for key, path in sdk_files.items()}}):
            raise ValueError('source or toolchain changed')
        report['diagnosticPassed'] = report['eventsAccepted'] and state == 'completed'
        if report['diagnosticPassed']:
            report['selectedInterfaceVerified'] = report['capabilities']['selected_private_interface_os']['status'] == 'verified'
            report['rawReceiveVerified'] = report['capabilities']['selected_private_interface_raw_receive']['status'] == 'verified'
        provenance['stage'] = 'passed' if report['diagnosticPassed'] else 'runtime-failed'
    except Exception:
        report['diagnosticPassed'] = False
        report['selectedInterfaceVerified'] = report['rawReceiveVerified'] = False
    finally:
        if private is not None:
            try:
                shutil.rmtree(private)
            except OSError:
                report['diagnosticPassed'] = False
    try:
        if not export_ready:
            raise ValueError('export unavailable')
        (export / 'result.json').write_text(json.dumps(report, sort_keys=True) + '\n', encoding='utf-8')
        (export / 'provenance.json').write_text(json.dumps(provenance, sort_keys=True) + '\n', encoding='utf-8')
        with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
            output.write('sanitized_ready=true\n')
    except Exception:
        report['diagnosticPassed'] = False
    print('CLASSIFIED: DNS-SD API scopes recorded separately; network multicast remains unverified.' if report['diagnosticPassed'] else
          'FAIL: DNS-SD compile or capability diagnostic incomplete; private details withheld.')
    return 0 if report['diagnosticPassed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
