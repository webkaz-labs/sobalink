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
RAW_SCOPE = 'selected_private_interface_raw_receive'
LEGACY_SCOPE = 'selected_private_interface_legacy_dns'
CASES = {
    'TestDNSServiceLocalOnlyCapability': 'os_local_only',
    'TestDNSServiceSelectedLoopbackCapability': 'selected_loopback_os',
    'TestDNSServiceSelectedInterfaceCapability': 'selected_private_interface_os',
    'TestDNSServiceRawReceiveCapability': RAW_SCOPE,
    'TestDNSServiceLegacySerializationCapability': LEGACY_SCOPE,
}
SELECTOR = '^TestDNSService(LocalOnly|SelectedLoopback|SelectedInterface|RawReceive|LegacySerialization)Capability$'
OPT_INS = ('SOBALINK_DNSSD_LOCAL_ONLY', 'SOBALINK_DNSSD_SELECTED_LOOPBACK',
           'SOBALINK_DNSSD_SELECTED_INTERFACE', 'SOBALINK_DNSSD_RAW_RECEIVE',
           'SOBALINK_DNSSD_LEGACY_SERIALIZATION')
MARKER = 'NEARBY_DNSSD_CAPABILITY '
LEGACY_MARKER = 'NEARBY_DNSSD_LEGACY_CAPABILITY '
# Go test2json splits Output events at 1024 bytes. Reassemble one bounded
# logical line, never across a test owner or lifecycle boundary.
MAX_OUTPUT_LINE_BYTES = 4096
FLAGS = {'reciprocalRecords', 'reciprocalCandidates', 'withdrawalObserved', 'ownersJoined',
         'rawPacketVerified', 'networkMulticastVerified'}
KEYS = FLAGS | {'status', 'reason', 'scope', 'failureStage', 'failureKind', 'receiveCounters'}
COUNTERS = {'receivedPackets', 'receivedBytes', 'metadataAccepted', 'metadataRejected',
            'decodedPackets', 'decodeRejected', 'rateLimitedPackets', 'matchingRecordKinds'}
STAGES = {'none', 'allocation', 'unknown', 'create_connection', 'schedule', 'registration', 'query', 'callback', 'admission'}
KINDS = {'none', 'unavailable', 'permission_denied', 'name_conflict', 'invalid_argument', 'unsupported'}
LEGACY_FLAGS = {'nativeSendAttempted', 'apiRecordsReady', 'oneQuerySent', 'legacySerializationVerified', 'ownersJoined',
                'rawPacketVerified', 'networkMulticastVerified'}
LEGACY_PROOF_FLAGS = {'nativeSendAttempted', 'apiRecordsReady', 'oneQuerySent', 'legacySerializationVerified', 'ownersJoined'}
LEGACY_KEYS = LEGACY_FLAGS | {'status', 'reason', 'scope', 'receivePath', 'failureStage', 'failureKind', 'receiveCounters', 'sendPrimitive', 'socketReadback', 'routeLookup'}
READBACK_KEYS = {'boundEndpoint', 'boundInterface', 'multicastInterface', 'multicastAddress', 'multicastTTL', 'multicastLoopback', 'nonblocking'}
ROUTE_FLAGS = {'destinationMatched', 'interfaceMatched', 'up', 'multicast', 'scoped', 'reject', 'blackhole', 'ownerJoined'}
ROUTE_STATUSES = {'observed', 'unavailable', 'timeout', 'output_limit', 'invalid'}
LEGACY_SOCKET_STAGES = {'legacy_socket', 'legacy_readback', 'legacy_deadline', 'legacy_send', 'legacy_receive'}
LEGACY_SEND_KINDS = {'invalid_argument', 'source_unavailable', 'network_unreachable',
    'host_unreachable', 'permission_denied', 'unsupported', 'timeout', 'other', 'short_payload'}
LEGACY_STAGE_KINDS = {'legacy_readback': {'unavailable', 'mismatch'}, 'legacy_socket': {'unavailable'}, 'legacy_receive': {'unavailable'},
    'legacy_deadline': LEGACY_SEND_KINDS - {'short_payload'}, 'legacy_send': LEGACY_SEND_KINDS}
LEGACY_UNAVAILABLE = {'readiness_deadline', 'reply_deadline', 'private_scope_unavailable',
    'loopback_scope_unavailable', 'selected_binding_changed', 'native_api_unavailable',
    'legacy_socket_unavailable', 'legacy_readback_unavailable', 'legacy_deadline_unavailable', 'legacy_send_unavailable', 'legacy_receive_unavailable'}
LEGACY_FAILED = {'socket_readback_mismatch', 'fixture_unavailable', 'provider_invariant', 'receive_budget',
                 'cleanup_uncertain', 'owners_not_joined', 'owners_missing'}
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
PREFLIGHT_STAGES = {'preflight', 'source-identity', 'source-event', 'source-inventory',
    'go-identity', 'clang-discovery', 'sdk-discovery', 'clang-identity', 'sdk-version',
    'sdk-build', 'sdk-files', 'toolchain-hash', 'source-policy'}
PREFLIGHT_FAILURES = {'none', 'command_failed', 'command_timeout', 'command_output_limit',
    'command_launch_failed', 'command_capture_failed', 'invalid_source_identity',
    'invalid_source_event', 'invalid_source_inventory', 'native_identity_mismatch',
    'clang_unavailable', 'sdk_unavailable', 'compiler_identity_format', 'sdk_version_format',
    'sdk_build_format', 'sdk_file_missing', 'sdk_file_outside_selected_sdk',
    'toolchain_file_unreadable', 'source_policy_missing', 'unexpected_preflight_failure'}
SDK_FILES = {'dns_sd.h': 'usr/include/dns_sd.h', 'SDKSettings.json': 'SDKSettings.json',
             'libdns_sd.tbd': 'usr/lib/libdns_sd.tbd', 'libSystem.tbd': 'usr/lib/libSystem.tbd'}
REQUIRED_SDK_FILES = {'dns_sd.h', 'SDKSettings.json', 'libSystem.tbd'}
LINK_LIBRARIES = {'/usr/lib/libSystem.B.dylib': 'libSystem.B.dylib',
                  '/usr/lib/libdns_sd.dylib': 'libdns_sd.dylib',
                  '/usr/lib/system/libsystem_dnssd.dylib': 'libsystem_dnssd.dylib'}


class PreflightError(ValueError):
    def __init__(self, kind):
        if kind not in PREFLIGHT_FAILURES:
            raise ValueError('unknown preflight category')
        self.kind = kind
        super().__init__('fixed preflight failure')


POLICY_FILES = ('.github/workflows/activation-diagnostic.yml', '.github/scripts/ci-diagnostic.py',
    '.github/scripts/ci-dnssd-diagnostic.py', '.github/scripts/test_ci_dnssd_diagnostic.py',
    '.github/scripts/ci-dnssd-terminal.py', '.github/scripts/test_ci_dnssd_terminal.py',
    '.github/scripts/test_ci_dnssd_terminal_origin.py', '.github/scripts/test_ci_dnssd_terminal_marker.py',
    '.github/scripts/ci-nearby-native.py', '.github/scripts/ci-nearby-diagnostic.py',
    '.github/scripts/ci-go-test.py', 'internal/nearby/provider_darwin.go',
    'internal/nearby/dnssd_bridge_darwin.c', 'internal/nearby/dnssd_bridge_darwin.h',
    'internal/nearby/dnssd_native_acceptance_test.go', 'internal/nearby/dnssd_raw_receive_test.go',
    'internal/nearby/dnssd_capability_test.go', 'internal/nearby/dnssd_wire_evidence_test.go',
    'internal/nearby/dnssd_legacy_native_test.go', 'internal/nearby/dnssd_legacy_send_unix_test.go',
    'internal/nearby/dnssd_socket_diagnostic_test.go', 'internal/nearby/dnssd_socket_native_test.go',
    'internal/nearby/dnssdprobe/probe_darwin.go',
    'go.mod', 'go.sum')


def module(name):
    path = Path(__file__).with_name(name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


def receive_counters(value):
    if value is None:
        return None
    if (type(value) is not dict or set(value) != COUNTERS
            or any(type(count) is not int for count in value.values())):
        raise ValueError('invalid closed receive counters')
    for key, count in value.items():
        limit = 512 * 1201 if key == 'receivedBytes' else 15 if key == 'matchingRecordKinds' else 512
        if not 0 <= count <= limit:
            raise ValueError('receive counter outside bound')
    # receivedBytes measures bytes copied into the bounded receive buffer,
    # including truncated observations; it is not a full wire-byte count.
    if (value['receivedBytes'] > value['receivedPackets'] * 1201
            or value['receivedBytes'] < 12 * value['metadataAccepted']
            or value['metadataAccepted'] + value['metadataRejected'] + value['rateLimitedPackets'] > value['receivedPackets']
            or value['decodedPackets'] + value['decodeRejected'] > value['metadataAccepted']
            or value['matchingRecordKinds'] and not value['decodedPackets']):
        raise ValueError('inconsistent receive counters')
    return {key: value[key] for key in sorted(COUNTERS)}


def capability(value, scope):
    if (type(value) is not dict or set(value) != KEYS or value['scope'] != scope or scope not in PROOFS
            or any(type(value[key]) is not bool for key in FLAGS)
            or any(type(value[key]) is not str for key in KEYS - FLAGS - {'receiveCounters'})
            or value['failureStage'] not in STAGES or value['failureKind'] not in KINDS
            or value['networkMulticastVerified']):
        raise ValueError('invalid closed DNS-SD receipt')
    counts = receive_counters(value['receiveCounters'])
    if (scope != RAW_SCOPE and counts is not None or scope == RAW_SCOPE
            and (counts is None) != (value['status'] == 'not_run')):
        raise ValueError('invalid scope-specific receive counters')
    stage, kind, status, reason = (value[key] for key in ('failureStage', 'failureKind', 'status', 'reason'))
    if (stage == 'none') != (kind == 'none'):
        raise ValueError('incomplete API failure classification')
    if status == 'verified':
        expected_reason, expected_flags = PROOFS[scope]
        if reason != expected_reason or stage != 'none' or {key for key in FLAGS if value[key]} != expected_flags:
            raise ValueError('incomplete scope-specific proof')
        if scope == RAW_SCOPE and counts['matchingRecordKinds'] != 15:
            raise ValueError('incomplete raw receive evidence')
    elif status == 'unavailable':
        if reason not in UNAVAILABLE or (reason == 'native_api_unavailable') != (stage != 'none'):
            raise ValueError('invalid unavailable classification')
    elif status == 'failed':
        # Cleanup can override the reason after an API error; preserve its fixed
        # originating stage/kind rather than erase it or report raw OS text.
        if reason not in FAILED:
            raise ValueError('unknown invariant failure')
    elif status == 'not_run':
        if reason != 'opt_in_required' or stage != 'none' or counts is not None or any(value[key] for key in FLAGS):
            raise ValueError('invalid opt-in receipt')
    else:
        raise ValueError('unknown DNS-SD status')
    # Non-verified observations can retain partial work, but must not acquire
    # unrelated scope proofs that the native fixture cannot produce.
    if any(value[key] for key in FLAGS - PROOFS[scope][1]):
        raise ValueError('proof escaped diagnostic scope')
    return {key: counts if key == 'receiveCounters' else value[key] for key in sorted(KEYS)}


def socket_readback(value):
    if value is None:
        return None
    if type(value) is not dict or set(value) != READBACK_KEYS or any(type(item) is not str or item not in {'matched', 'mismatch', 'unavailable'} for item in value.values()):
        raise ValueError('invalid closed socket readback')
    return {key: value[key] for key in sorted(READBACK_KEYS)}


def readback_state(value):
    if value is None or 'unavailable' in value.values():
        return 'unavailable'
    return 'matched' if all(value[key] == 'matched' for key in READBACK_KEYS - {'multicastAddress'}) else 'mismatch'


def route_lookup(value):
    if value is None:
        return None
    if (type(value) is not dict or set(value) != ROUTE_FLAGS | {'status'}
            or any(type(value[key]) is not bool for key in ROUTE_FLAGS)
            or type(value['status']) is not str or value['status'] not in ROUTE_STATUSES
            or value['status'] != 'observed' and any(value[key] for key in ROUTE_FLAGS - {'ownerJoined'})):
        raise ValueError('invalid closed route snapshot')
    return {key: value[key] for key in sorted(value)}


def legacy_capability(value):
    # Legacy unicast DNS serialization has a different envelope and admission
    # contract. It cannot enter the four original capability/proof definitions.
    if (type(value) is not dict or set(value) != LEGACY_KEYS
            or any(type(value[key]) is not bool for key in LEGACY_FLAGS)
            or any(type(value[key]) is not str for key in LEGACY_KEYS - LEGACY_FLAGS - {'receiveCounters', 'socketReadback', 'routeLookup'})
            or value['sendPrimitive'] != 'native_c_sendto'
            or value['scope'] != LEGACY_SCOPE or value['receivePath'] not in {'none', 'selected_interface', 'local_loopback'}
            or value['failureStage'] not in STAGES | LEGACY_SOCKET_STAGES
            or value['failureKind'] not in KINDS | LEGACY_SEND_KINDS | {'mismatch'}
            or value['rawPacketVerified'] or value['networkMulticastVerified']):
        raise ValueError('invalid closed legacy DNS receipt')
    counts = receive_counters(value['receiveCounters'])
    options = socket_readback(value['socketReadback'])
    route = route_lookup(value['routeLookup'])
    if ((options is not None or route is not None) and not value['apiRecordsReady']
            or value['nativeSendAttempted'] and (not value['apiRecordsReady'] or readback_state(options) != 'matched'
            or route is None or not route['ownerJoined'])
            or value['oneQuerySent'] and not value['nativeSendAttempted']
            or route is not None and not route['ownerJoined'] and value['status'] != 'failed'):
        raise ValueError('native send escaped verified socket ownership')
    stage, kind, status, reason = (value[key] for key in ('failureStage', 'failureKind', 'status', 'reason'))
    if ((stage == 'none') != (kind == 'none')
            or stage == 'legacy_readback' and options is None
            or kind == 'short_payload' and stage != 'legacy_send'
            or stage in LEGACY_SOCKET_STAGES and kind not in LEGACY_STAGE_KINDS[stage]
            or stage in STAGES and kind not in KINDS
            or (counts is None) != (status == 'not_run')
            or value['oneQuerySent'] and not value['apiRecordsReady']):
        raise ValueError('inconsistent legacy DNS classification')
    if status == 'verified':
        if (reason != 'native_legacy_serialization_observed' or stage != 'none'
                or {key for key in LEGACY_FLAGS if value[key]} != LEGACY_PROOF_FLAGS
                or value['receivePath'] == 'none' or counts['matchingRecordKinds'] != 15):
            raise ValueError('incomplete legacy DNS serialization proof')
    elif status == 'unavailable':
        if reason not in LEGACY_UNAVAILABLE:
            raise ValueError('unknown legacy DNS unavailability')
        if reason == 'native_api_unavailable':
            if stage not in STAGES - {'none'}:
                raise ValueError('missing native API classification')
        elif reason in {item + '_unavailable' for item in LEGACY_SOCKET_STAGES}:
            if stage != reason.removesuffix('_unavailable') or stage == 'legacy_readback' and (kind != 'unavailable' or readback_state(options) != 'unavailable'):
                raise ValueError('incorrect legacy socket classification')
        elif stage != 'none':
            raise ValueError('unexpected legacy failure classification')
    elif status == 'failed':
        # Cleanup failure can retain the originating fixed API/socket category.
        if reason not in LEGACY_FAILED:
            raise ValueError('unknown legacy DNS invariant failure')
        if reason == 'socket_readback_mismatch' and (stage != 'legacy_readback' or kind != 'mismatch' or readback_state(options) != 'mismatch'):
            raise ValueError('invalid socket mismatch evidence')
    elif status == 'not_run':
        if (reason != 'opt_in_required' or stage != 'none' or value['receivePath'] != 'none' or options is not None or route is not None
                or any(value[key] for key in LEGACY_FLAGS)):
            raise ValueError('invalid legacy opt-in receipt')
    else:
        raise ValueError('unknown legacy DNS status')
    if status != 'verified' and value['legacySerializationVerified']:
        raise ValueError('unverified legacy serialization proof')
    if counts is not None and ((value['receivePath'] != 'none') != (counts['decodedPackets'] > 0)
            or counts['decodedPackets'] > 0 and not value['oneQuerySent']):
        raise ValueError('incomplete legacy receive route')
    return {key: counts if key == 'receiveCounters' else options if key == 'socketReadback' else route if key == 'routeLookup' else value[key] for key in sorted(LEGACY_KEYS)}


def test_events(raw, returncode):
    native, shared, parser = module('ci-nearby-native'), module('ci-nearby-diagnostic'), module('ci-go-test')
    if type(raw) is not bytes or len(raw) > 2 * native.RUNTIME_BYTES or type(returncode) is not int:
        raise ValueError('invalid DNS-SD capture')
    lifecycles = {name: [] for name in CASES}
    markers = {name: [] for name in CASES}
    package, active, order_valid = [], None, True
    pending, pending_owner, pending_bytes = '', None, 0

    def accept_line(output, name):
        present = [marker for marker in (MARKER, LEGACY_MARKER) if marker in output]
        if not present:
            return
        expected = LEGACY_MARKER if name == 'TestDNSServiceLegacySerializationCapability' else MARKER
        if (name not in CASES or present != [expected] or output.count(expected) != 1
                or active != name or markers[name]):
            raise ValueError('invalid DNS-SD marker owner or multiplicity')
        value = shared.parse_json(output.split(expected, 1)[1].strip())
        markers[name].append(legacy_capability(value) if expected == LEGACY_MARKER
                             else capability(value, CASES[name]))

    for line in raw.splitlines():
        event = parser.parse_event(line)
        name, action = event.get('Test'), event['Action']
        if event.get('Package') != PACKAGE or name is not None and name not in CASES:
            raise ValueError('unexpected executed DNS-SD test')
        if action != 'output':
            if pending:
                raise ValueError('unfinished DNS-SD output before lifecycle boundary')
            if any(marker in event.get('Output', '') for marker in (MARKER, LEGACY_MARKER)):
                raise ValueError('DNS-SD marker outside output action')
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
            continue
        if pending and name != pending_owner or name is not None and name != active:
            raise ValueError('DNS-SD output owner changed mid-line')
        # A single Output event can contain several complete lines or only a
        # fragment. Count UTF-8 bytes, including the newline, before appending.
        parts = event['Output'].split('\n')
        for index, part in enumerate(parts):
            complete = index < len(parts) - 1
            fragment = part + ('\n' if complete else '')
            if not fragment:
                continue
            size = len(fragment.encode('utf-8'))
            if pending_bytes + size > MAX_OUTPUT_LINE_BYTES:
                raise ValueError('DNS-SD logical output line exceeds bound')
            if not pending:
                pending_owner = name
            pending += fragment
            pending_bytes += size
            if complete:
                accept_line(pending, pending_owner)
                pending, pending_owner, pending_bytes = '', None, 0
    if pending:
        raise ValueError('unfinished DNS-SD output at capture end')
    result, accepted = {}, order_valid and active is None and returncode == 0 and package == ['start', 'pass']
    for name, scope in CASES.items():
        record = markers[name][0] if len(markers[name]) == 1 else None
        result[scope] = record
        terminal = {'verified': 'pass', 'unavailable': 'skip'}.get(record['status']) if record else None
        accepted &= terminal is not None and lifecycles[name] == ['run', terminal]
    return result, bool(accepted)


def collector_observation(stdout, stderr, state='not_run', code=None, data=b'', error=b''):
    """Closed explanatory counts only; never used by capability acceptance."""
    native, parser = module('ci-nearby-native'), module('ci-go-test')
    if (any(type(value) is not bytes for value in (stdout, stderr, data, error))
            or max(len(stdout), len(stderr)) > native.RUNTIME_BYTES
            or max(len(data), len(error)) > 2 * native.RUNTIME_BYTES
            or state not in native.PROCESS_STATES
            or code is not None and (type(code) is not int or not -127 <= code <= 255)):
        raise ValueError('invalid bounded collector observation')
    result = {'nativeStdoutBytes': len(stdout), 'nativeStderrBytes': len(stderr),
        'rawStreams': {},
        'converterState': state, 'converterExitCode': code,
        'converterStdoutBytes': len(data), 'converterStderrBytes': len(error),
        'converterStderrClass': ('empty' if not error else 'usage' if error.startswith(b'usage: go tool test2json ')
            else 'go_tool_error' if error.startswith(b'go: ') else 'panic' if error.startswith(b'panic: ') else 'unclassified'),
        'eventScanStatus': 'not_run' if state != 'completed' else 'scanned',
        'eventCount': 0, 'outputEventCount': 0, 'countsCapped': False,
        'packageLifecycleCounts': {name: 0 for name in ('start', 'pass', 'skip', 'fail', 'other')},
        'testLifecycleCounts': {name: {action: 0 for action in ('run', 'pass', 'skip', 'fail', 'other')} for name in CASES},
        'completeMarkerLineCounts': {name: 0 for name in CASES},
        'incompleteOutputLines': 0, 'incompleteMarkerLines': 0, 'acceptanceStatus': 'not_run'}
    def bounded(value, maximum=16):
        if value > maximum:
            result['countsCapped'] = True
        return min(value, maximum)
    for stream, value in (('stdout', stdout), ('stderr', stderr)):
        observed = {'runTokenCounts': {}, 'terminalTokenCounts': {}, 'markerTokenCounts': {},
            'completeMarkerLineCounts': {},
            'passTokenSeen': bool(re.search(rb'(?m)^\x16?PASS\r?\n', value)),
            'failTokenSeen': bool(re.search(rb'(?m)^\x16?FAIL\r?\n', value)),
            'noTestsTokenSeen': b'testing: warning: no tests to run' in value}
        for name in CASES:
            observed['runTokenCounts'][name] = bounded(len(re.findall(
                rb'(?m)^\x16?=== RUN   ' + re.escape(name.encode()) + rb'\r?\n', value)))
            observed['terminalTokenCounts'][name] = {action: bounded(len(re.findall(
                rb'(?m)^\x16?--- ' + action.upper().encode() + rb': ' + re.escape(name.encode())
                + rb' \([0-9]+(?:\.[0-9]+)?s\)\r?\n', value))) for action in ('pass', 'skip', 'fail')}
        for kind, marker in (('standard', MARKER.encode()), ('legacy', LEGACY_MARKER.encode())):
            observed['markerTokenCounts'][kind] = bounded(value.count(marker))
            observed['completeMarkerLineCounts'][kind] = bounded(sum(marker in line and line.endswith(b'\n')
                for line in value.splitlines(keepends=True)))
        result['rawStreams'][stream] = observed
    if state != 'completed':
        return result
    pending, pending_owner = '', None
    def unfinished():
        if pending:
            result['incompleteOutputLines'] = bounded(result['incompleteOutputLines'] + 1)
            result['incompleteMarkerLines'] = bounded(result['incompleteMarkerLines'] + int(MARKER in pending or LEGACY_MARKER in pending))
    try:
        for line in data.splitlines():
            event = parser.parse_event(line)
            result['eventCount'] = bounded(result['eventCount'] + 1, 4096)
            name, action = event.get('Test'), event['Action']
            if event.get('Package') != PACKAGE or name is not None and name not in CASES:
                result['eventScanStatus'] = 'unexpected_inventory'
                break
            if action != 'output':
                unfinished()
                pending, pending_owner = '', None
                counts = result['packageLifecycleCounts'] if name is None else result['testLifecycleCounts'][name]
                key = action if action in counts else 'other'
                counts[key] = bounded(counts[key] + 1)
                continue
            result['outputEventCount'] = bounded(result['outputEventCount'] + 1, 4096)
            if pending and pending_owner != name:
                unfinished()
                pending = ''
            pending_owner = name
            # Only join adjacent fragments for the same owner. A token in an
            # unfinished line never counts as a complete logical marker line.
            parts = (pending + event['Output']).split('\n')
            pending = parts.pop()
            if name in CASES:
                marker = LEGACY_MARKER if name == 'TestDNSServiceLegacySerializationCapability' else MARKER
                result['completeMarkerLineCounts'][name] = bounded(
                    result['completeMarkerLineCounts'][name] + sum(marker in part for part in parts))
        unfinished()
        if result['eventScanStatus'] == 'scanned' and result['incompleteOutputLines']:
            result['eventScanStatus'] = 'incomplete_lines'
    except (ValueError, TypeError, KeyError, UnicodeError, RecursionError):
        result['eventScanStatus'] = 'invalid_event'
    except Exception:
        result['eventScanStatus'] = 'scanner_failed'
    return result


def compiler_kind(stderr, state):
    if state in ('timeout', 'output_limit', 'launch_failed', 'capture_failed'):
        return state
    first = first_compiler_error(stderr)
    text = (first if first is not None else stderr[:64 * 1024]).lower()
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
    match = re.fullmatch(r'Apple clang version ([0-9]{1,4}(?:\.[0-9]{1,6}){1,3}) \(clang-([0-9]{1,6}(?:\.[0-9]{1,6}){0,5})\)', first)
    if not match or not re.fullmatch(r'[0-9]{1,4}(?:\.[0-9]{1,6}){1,3}', sdk_version) or not re.fullmatch(r'[0-9]{1,3}[A-Z][0-9]{1,6}[a-z]?', sdk_build):
        raise ValueError('unrecognized native compiler identity')
    return {'vendor': 'apple-clang', 'version': match[1], 'build': match[2], 'sdkVersion': sdk_version, 'sdkBuild': sdk_build}


def first_compiler_error(stderr):
    return next((line for line in stderr[:64 * 1024].splitlines()[:512]
                 if re.search(rb': (?:fatal )?error:', line)), None)


def compiler_location(stderr):
    """First compiler error only; publish a fixed source enum and bounded point."""
    files = ('dnssd_bridge_darwin.c', 'dnssd_bridge_darwin.h')
    # Do not inspect unbounded diagnostic data or skip an earlier external error
    # in order to manufacture an apparently actionable product-source location.
    first = first_compiler_error(stderr)
    for line in (() if first is None else (first,)):
        for name in files:
            relative = 'internal/nearby/' + name
            spellings = (str(ROOT / relative), relative, name)
            pattern = rb'^(?:' + b'|'.join(re.escape(path.encode()) for path in spellings) + rb'):([0-9]{1,6}):([0-9]{1,5}): (?:fatal )?error:'
            match = re.match(pattern, line)
            if not match:
                continue
            row, column = int(match[1]), int(match[2])
            source = (ROOT / relative).read_bytes().splitlines()
            if (1 <= row <= len(source) and 1 <= column <= 8192
                    and column <= len(source[row - 1].expandtabs(8)) + 1):
                return {'file': name, 'line': row, 'column': column}
        return None
    return None


def compiler_observation(raw):
    # Keep only bounded numeric identity parts even when the full format fails.
    first = raw.decode('utf-8', errors='replace').splitlines()[0] if raw else ''
    version = re.match(r'Apple clang version ([0-9]{1,4}(?:\.[0-9]{1,6}){1,3})(?: |$)', first)
    build = re.search(r'\(clang-([0-9]{1,6}(?:\.[0-9]{1,6}){0,5})\)', first)
    return {'vendor': 'apple-clang' if version else 'unknown',
            'version': version[1] if version else None, 'build': build[1] if build else None}


def linked_libraries(raw):
    if len(raw) > 16384:
        raise ValueError('link inventory exceeded')
    lines = raw.decode('utf-8').splitlines()
    if not lines or not lines[0].endswith(':'):
        raise ValueError('invalid link inventory')
    found = []
    for line in lines[1:]:
        match = re.fullmatch(r'\s+([^ ]+) \(compatibility version [0-9.]+, current version [0-9.]+\)', line)
        if not match or match[1] not in LINK_LIBRARIES or LINK_LIBRARIES[match[1]] in found:
            raise ValueError('unreviewed linked library')
        found.append(LINK_LIBRARIES[match[1]])
    if 'libSystem.B.dylib' not in found:
        raise ValueError('required system umbrella missing')
    return sorted(found)


def sdk_file_inventory(sdk):
    files, states = {}, {}
    root = sdk.resolve(strict=True)
    for name, relative in SDK_FILES.items():
        path = sdk / relative
        if not path.is_file():
            states[name] = 'missing'
        elif not path.resolve(strict=True).is_relative_to(root):
            states[name] = 'outside_selected_sdk'
        else:
            states[name] = 'available'
            files[name] = path
    return files, states


def empty_result():
    return {'schema': 1, 'diagnosticPassed': False, 'cCompilePassed': False, 'cLinkPassed': False, 'goCompilePassed': False,
            'compileFailureKind': None, 'compileFailureLocation': None, 'preflightFailureKind': None, 'processStatus': 'not_run', 'eventsAccepted': False,
            'terminalLaunch': None, 'collector': None,
            'capabilities': {scope: None for scope in CASES.values()}, 'selectedInterfaceVerified': False,
            'rawReceiveVerified': False, 'legacySerializationVerified': False, 'legacyReceivePath': 'none',
            'networkMulticastVerified': False, 'physicalLANVerified': False,
            'automaticCoreBrowserVerified': False, 'releaseValidation': False}


def summarize_scope_proofs(report):
    # A legacy unicast observation never upgrades the original passive raw
    # scope, even when the OS returned it on the selected interface.
    report['selectedInterfaceVerified'] = report['rawReceiveVerified'] = False
    report['legacySerializationVerified'], report['legacyReceivePath'] = False, 'none'
    if report['diagnosticPassed']:
        report['selectedInterfaceVerified'] = report['capabilities']['selected_private_interface_os']['status'] == 'verified'
        report['rawReceiveVerified'] = report['capabilities'][RAW_SCOPE]['status'] == 'verified'
        legacy = report['capabilities'][LEGACY_SCOPE]
        report['legacySerializationVerified'] = legacy['status'] == 'verified'
        if report['legacySerializationVerified']:
            report['legacyReceivePath'] = legacy['receivePath']


def main(argv=None, *, required=None):
    if (sys.argv[1:] if argv is None else argv) or sys.platform != 'darwin' or os.environ.get('GITHUB_ACTIONS') != 'true':
        print('FAIL: fixed hosted Darwin diagnostic entry required.')
        return 1
    report, provenance = empty_result(), {'schema': 1, 'stage': 'preflight'}
    export_ready, private = False, None
    try:
        os.umask(0o077)
        export = Path(os.environ['RUNNER_TEMP']) / ('soba-dnssd-required' if required else 'soba-dnssd-sanitized')
        export.mkdir(mode=0o700, exist_ok=False)
        export_ready = True
        private = Path(tempfile.mkdtemp(prefix='soba-dnssd-', dir=os.environ['RUNNER_TEMP']))
        native, shared, selector = module('ci-nearby-native'), module('ci-nearby-diagnostic'), module('ci-diagnostic')
        failure_kind = 'unexpected_preflight_failure'
        def checked(command):
            state, _, stdout, _ = native.capture(command)
            if state != 'completed':
                kind = {'nonzero': 'command_failed', 'timeout': 'command_timeout',
                        'output_limit': 'command_output_limit', 'launch_failed': 'command_launch_failed',
                        'capture_failed': 'command_capture_failed'}.get(state, 'command_failed')
                raise PreflightError(kind)
            return stdout
        read = lambda command: checked(command).decode().strip()
        provenance['stage'], failure_kind = 'source-identity', 'invalid_source_identity'
        commit, tree = read(['git', 'rev-parse', 'HEAD']), read(['git', 'rev-parse', 'HEAD^{tree}'])
        if not all(re.fullmatch('[0-9a-f]{40}', value) for value in (commit, tree)):
            raise ValueError('invalid source identity')
        provenance.update(commit=commit, tree=tree)
        provenance['stage'], failure_kind = 'source-event', 'invalid_source_event'
        event_path = Path(os.environ['GITHUB_EVENT_PATH'])
        if event_path.stat().st_size > native.CAPTURE_BYTES:
            raise ValueError('event size exceeded')
        event = shared.parse_json(event_path.read_text(encoding='utf-8'))
        if required:
            provenance['ciIdentity'] = required.ci_identity(event, commit, tree)
        elif selector.select(os.environ['GITHUB_EVENT_NAME'], event, os.environ['GITHUB_REPOSITORY'],
                os.environ['GITHUB_REF'], os.environ['GITHUB_SHA'], commit) != 'dnssd':
            raise ValueError('wrong diagnostic suite or source')
        provenance['stage'], failure_kind = 'source-inventory', 'invalid_source_inventory'
        inventory = required.source_inventory if required else native.source_inventory
        before = inventory()
        if required:
            provenance.update(sourceFiles=len(before), sourceRechecked=False, toolchainRechecked=False, privateCleanupVerified=False)
        provenance['sourceSha256'] = hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest()
        provenance['stage'], failure_kind = 'go-identity', 'native_identity_mismatch'
        versions = shared.parse_json(checked(['go', 'env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH', 'GOOS', 'GOARCH', 'CGO_ENABLED']))
        known = {'GOVERSION': ('go1.27.1',), 'GOHOSTOS': ('linux', 'darwin', 'windows'),
                 'GOOS': ('linux', 'darwin', 'windows'), 'GOHOSTARCH': ('amd64', 'arm64'),
                 'GOARCH': ('amd64', 'arm64'), 'CGO_ENABLED': ('0', '1')}
        provenance['goObserved'] = {key: versions.get(key) if versions.get(key) in values else 'unknown'
                                    for key, values in known.items()} if type(versions) is dict else None
        provenance['runnerObserved'] = {
            'os': os.environ.get('RUNNER_OS') if os.environ.get('RUNNER_OS') in ('Linux', 'macOS', 'Windows') else 'unknown',
            'arch': os.environ.get('RUNNER_ARCH') if os.environ.get('RUNNER_ARCH') in ('X64', 'ARM64') else 'unknown',
            'pythonArch': native.platform.machine().lower() if native.platform.machine().lower() in ('arm64', 'aarch64', 'amd64', 'x86_64') else 'unknown'}
        provenance['toolchain'] = native.identities('darwin-arm64', versions)
        provenance['platform'] = 'darwin-arm64'
        provenance['stage'], failure_kind = 'clang-discovery', 'clang_unavailable'
        clang = Path(read(['xcrun', '--sdk', 'macosx', '--find', 'clang']))
        if not clang.is_absolute() or not clang.is_file():
            raise ValueError('native compiler unavailable')
        provenance['stage'], failure_kind = 'sdk-discovery', 'sdk_unavailable'
        sdk = Path(read(['xcrun', '--sdk', 'macosx', '--show-sdk-path']))
        if not sdk.is_absolute() or not sdk.is_dir():
            raise ValueError('native SDK unavailable')
        provenance['stage'], failure_kind = 'clang-identity', 'compiler_identity_format'
        clang_version = checked([str(clang), '--version'])
        provenance['compilerObserved'] = compiler_observation(clang_version)
        # Preserve the same strict Apple compiler format check, independently
        # of SDK version discovery so a failed assumption is identifiable.
        compiler_identity(clang_version, '1.0', '1A1')
        provenance['stage'], failure_kind = 'sdk-version', 'sdk_version_format'
        sdk_version = read(['xcrun', '--sdk', 'macosx', '--show-sdk-version'])
        valid_version = re.fullmatch(r'[0-9]{1,4}(?:\.[0-9]{1,6}){1,3}', sdk_version)
        provenance['sdkVersionObserved'] = sdk_version if valid_version and len(sdk_version) <= 24 else None
        if not valid_version or len(sdk_version) > 24:
            raise ValueError('invalid SDK version')
        provenance['stage'], failure_kind = 'sdk-build', 'sdk_build_format'
        sdk_build = read(['xcrun', '--sdk', 'macosx', '--show-sdk-build-version'])
        valid_build = re.fullmatch(r'[0-9]{1,3}[A-Z][0-9]{1,6}[a-z]?', sdk_build)
        provenance['sdkBuildObserved'] = sdk_build if valid_build else None
        if not valid_build:
            raise ValueError('invalid SDK build')
        provenance['compiler'] = compiler_identity(clang_version, sdk_version, sdk_build)
        provenance['stage'], failure_kind = 'sdk-files', 'sdk_file_missing'
        sdk_files, provenance['sdkFiles'] = sdk_file_inventory(sdk)
        if 'outside_selected_sdk' in provenance['sdkFiles'].values():
            raise PreflightError('sdk_file_outside_selected_sdk')
        if not REQUIRED_SDK_FILES.issubset(sdk_files):
            raise PreflightError('sdk_file_missing')
        provenance['stage'], failure_kind = 'toolchain-hash', 'toolchain_file_unreadable'
        tool = Path(shutil.which('go'))
        compiler_hashes = {'clang': native.digest(clang), 'go': native.digest(tool),
                          'sdk': {key: native.digest(path) for key, path in sdk_files.items()}}
        provenance['toolchainSha256'] = compiler_hashes
        provenance['stage'], failure_kind = 'source-policy', 'source_policy_missing'
        provenance.update(tests=dict(CASES), policySha256={name: before[name] for name in (required.POLICY_FILES if required else POLICY_FILES)}, modules=list(native.MODULES))
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
            report['compileFailureLocation'] = compiler_location(stderr)
            raise ValueError('native C API compilation failed')
        report['cCompilePassed'] = True
        provenance['cObjectSha256'] = native.digest(private / 'dnssd-bridge.o')
        provenance['stage'] = 'c-link'
        # Apple libSystem is implicitly linked and reexports system_dnssd.
        # Test that supported production contract with the actual selected SDK;
        # the absent historical per-library stub remains an honest observation.
        # https://github.com/apple-oss-distributions/Libsystem/blob/main/requiredlibs
        # https://github.com/apple-oss-distributions/Libsystem/blob/main/xcodescripts/linker_arguments.sh
        provider = (ROOT / 'internal/nearby/provider_darwin.go').read_text(encoding='utf-8')
        if re.search(r'^\s*#cgo\b[^\n]*\bLDFLAGS:', provider, re.M):
            raise ValueError('production link policy changed')
        linked = private / 'dnssd-link'
        state, _, _, stderr = native.capture([str(clang), '-arch', 'arm64', '-isysroot', str(sdk),
            str(private / 'dnssd-bridge.o'), '-x', 'c', '-', '-o', str(linked)],
            env=env, timeout=60, stdin=b'int main(void) { return 0; }\n')
        if state != 'completed':
            report['compileFailureKind'] = compiler_kind(stderr, state)
            report['compileFailureLocation'] = compiler_location(stderr)
            raise ValueError('native C API link failed')
        report['cLinkPassed'] = True
        provenance['cLinkedSha256'] = native.digest(linked)
        provenance['linkage'] = 'implicit-system-umbrella'
        provenance['cLinkLibraries'] = linked_libraries(native.checked(['otool', '-L', str(linked)], env=env))
        provenance['stage'] = 'dependencies'
        if not required:
            native.checked(['go', 'mod', 'download', *native.MODULES], env=env, timeout=120)
        env.update(GOPROXY='off', GOSUMDB='off')
        provenance['stage'] = 'go-compile'
        binary = private / 'dnssd.test'
        state, _, _, stderr = native.capture(['go', 'test', '-race', '-p=1', '-c', '-o', str(binary), './internal/nearby'], env=env, timeout=300)
        if state != 'completed':
            report['compileFailureKind'] = compiler_kind(stderr, state)
            report['compileFailureLocation'] = compiler_location(stderr)
            raise ValueError('native Go API compilation failed')
        report['goCompilePassed'] = True
        provenance['testSha256'] = native.digest(binary)
        env.update({key: '1' for key in OPT_INS})
        provenance['stage'] = 'runtime'
        provenance['launchMethod'] = 'ordinary_terminal_command'
        provenance['originVerification'] = 'exact-four-hop-kernel-path-with-final-recheck'
        provenance['testDriverDenialFence'] = True
        state, code, stdout, stderr, report['terminalLaunch'] = module('ci-dnssd-terminal').capture(
            binary, env=env, private=private)
        report['processStatus'] = state
        if state in ('completed', 'nonzero'):
            # Failed launch/ownership paths intentionally withhold raw streams.
            # Absence of a collector must not misreport those streams as empty.
            report['collector'] = collector_observation(stdout, stderr)
            converted, converted_code, data, converted_error = native.capture(['go', 'tool', 'test2json', '-p', PACKAGE],
                env=env, timeout=15, limit=2 * native.RUNTIME_BYTES, stdin=stdout)
            report['collector'] = collector_observation(stdout, stderr, converted, converted_code, data, converted_error)
            if converted == 'completed':
                try:
                    report['capabilities'], report['eventsAccepted'] = test_events(data, code)
                    report['collector']['acceptanceStatus'] = 'accepted' if report['eventsAccepted'] else 'unaccepted'
                except Exception:
                    report['collector']['acceptanceStatus'] = 'validation_failed'
                    raise
        provenance['stage'] = 'verify-source'
        if (before != inventory() or commit != read(['git', 'rev-parse', 'HEAD'])
                or tree != read(['git', 'rev-parse', 'HEAD^{tree}'])
                or provenance['testSha256'] != native.digest(binary)
                or compiler_hashes != {'clang': native.digest(clang), 'go': native.digest(tool),
                    'sdk': {key: native.digest(path) for key, path in sdk_files.items()}}):
            raise ValueError('source or toolchain changed')
        if required:
            provenance.update(sourceRechecked=True, toolchainRechecked=True)
        report['diagnosticPassed'] = (report['eventsAccepted'] and state == 'completed'
            and report['terminalLaunch']['status'] == 'observed')
        provenance['stage'] = 'passed' if report['diagnosticPassed'] else 'runtime-failed'
    except Exception as error:
        if provenance['stage'] in PREFLIGHT_STAGES:
            report['preflightFailureKind'] = error.kind if isinstance(error, PreflightError) else locals().get('failure_kind', 'unexpected_preflight_failure')
        report['diagnosticPassed'] = False
    finally:
        if private is not None:
            try:
                shutil.rmtree(private)
                if required:
                    provenance['privateCleanupVerified'] = True
            except OSError:
                report['diagnosticPassed'] = False
    summarize_scope_proofs(report)
    try:
        if not export_ready:
            raise ValueError('export unavailable')
        if required:
            required.write_receipt(export, report, provenance)
        else:
            (export / 'result.json').write_text(json.dumps(report, sort_keys=True) + '\n', encoding='utf-8')
            (export / 'provenance.json').write_text(json.dumps(provenance, sort_keys=True) + '\n', encoding='utf-8')
        with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
            output.write('sanitized_ready=true\n')
    except Exception as error:
        if provenance['stage'] in PREFLIGHT_STAGES:
            report['preflightFailureKind'] = error.kind if isinstance(error, PreflightError) else locals().get('failure_kind', 'unexpected_preflight_failure')
        report['diagnosticPassed'] = False
    print(('PASS: all five required DNS-SD scopes and cleanup verified.' if required else
           'CLASSIFIED: DNS-SD API scopes recorded separately; network multicast remains unverified.') if report['diagnosticPassed'] else
          'FAIL: DNS-SD compile or capability diagnostic incomplete; private details withheld.')
    return 0 if report['diagnosticPassed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
