"""Closed DNS-SD receipts and synthetic process results; no native execution."""
import copy
import importlib.util
import io
import json
import os
import subprocess
from pathlib import Path
import tempfile
import unittest
from contextlib import ExitStack
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('ci-dnssd-diagnostic.py')
SPEC = importlib.util.spec_from_file_location('ci_dnssd_diagnostic', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), gate.__dict__)


def counters(verified=False):
    return {'receivedPackets': 1 if verified else 0, 'receivedBytes': 100 if verified else 0,
            'metadataAccepted': 1 if verified else 0, 'metadataRejected': 0,
            'decodedPackets': 1 if verified else 0, 'decodeRejected': 0,
            'rateLimitedPackets': 0, 'matchingRecordKinds': 15 if verified else 0}


def record(scope, status='verified', **changes):
    if scope == gate.LEGACY_SCOPE:
        value = {'scope': scope, 'status': status,
                 'reason': 'native_legacy_serialization_observed' if status == 'verified' else 'reply_deadline',
                 'failureStage': 'none', 'failureKind': 'none', 'sendPrimitive': 'native_c_sendto',
                 'socketReadback': {key: 'matched' for key in gate.READBACK_KEYS} if status == 'verified' else None,
                 'routeLookup': dict(status='observed', **{key: key in {'destinationMatched', 'interfaceMatched', 'up', 'multicast', 'scoped', 'ownerJoined'} for key in gate.ROUTE_FLAGS}) if status == 'verified' else None,
                 'receivePath': 'selected_interface' if status == 'verified' else 'none',
                 'receiveCounters': counters(status == 'verified'),
                 **{flag: status == 'verified' and flag in gate.LEGACY_PROOF_FLAGS for flag in gate.LEGACY_FLAGS}}
    else:
        reason, flags = gate.PROOFS[scope]
        value = {'scope': scope, 'status': status, 'reason': reason if status == 'verified' else 'deadline',
                 'failureStage': 'none', 'failureKind': 'none',
                 'receiveCounters': counters(status == 'verified') if scope == gate.RAW_SCOPE else None,
                 **{flag: status == 'verified' and flag in flags for flag in gate.FLAGS}}
    value.update(changes)
    return value


def events(records):
    rows = [{'Action': 'start', 'Package': gate.PACKAGE}]
    failed = False
    for name, scope in gate.CASES.items():
        value = records[scope]
        base = {'Package': gate.PACKAGE, 'Test': name}
        terminal = {'verified': 'pass', 'unavailable': 'skip', 'not_run': 'skip', 'failed': 'fail'}[value['status']]
        failed |= terminal == 'fail'
        rows += [dict(base, Action='run'), dict(base, Action='output', Output=(gate.LEGACY_MARKER if scope == gate.LEGACY_SCOPE else gate.MARKER) + json.dumps(value) + '\n'),
                 dict(base, Action=terminal)]
    rows.append({'Action': 'fail' if failed else 'pass', 'Package': gate.PACKAGE})
    return rows


def raw(rows):
    return b'\n'.join(json.dumps(row).encode() for row in rows)


class DNSSDDiagnosticTests(unittest.TestCase):
    def test_scope_specific_proofs_never_claim_network_multicast(self):
        for scope in gate.PROOFS:
            value = record(scope)
            self.assertEqual(gate.capability(value, scope), value)
            for key in gate.FLAGS:
                with self.subTest(scope=scope, key=key), self.assertRaises(ValueError):
                    gate.capability(dict(value, **{key: not value[key]}), scope)
            for other in gate.PROOFS.keys() - {scope}:
                with self.assertRaises(ValueError):
                    gate.capability(value, other)
        local = record('os_local_only')
        self.assertFalse(local['reciprocalCandidates'])
        self.assertFalse(local['rawPacketVerified'])

    def test_closed_fields_enums_types_and_partial_unavailable_are_preserved(self):
        scope = 'os_local_only'
        value = record(scope, 'unavailable', reciprocalRecords=True)
        self.assertEqual(gate.capability(value, scope), value)
        for reason in gate.UNAVAILABLE:
            changes = {'failureStage': 'query', 'failureKind': 'permission_denied'} if reason == 'native_api_unavailable' else {}
            self.assertEqual(gate.capability(record(scope, 'unavailable', reason=reason, **changes), scope)['reason'], reason)
        for reason in gate.FAILED:
            value = record(scope, 'failed', reason=reason, failureStage='registration', failureKind='name_conflict')
            self.assertEqual(gate.capability(value, scope), value)
        for key in gate.KEYS:
            value = record(scope)
            del value[key]
            with self.assertRaises(ValueError):
                gate.capability(value, scope)
        for changes in ({'secret': 'synthetic-private'}, {'reason': 'synthetic-private'},
                        {'failureStage': 'synthetic-private'}, {'failureKind': 'synthetic-private'},
                        {'ownersJoined': 1}, {'status': []}, {'networkMulticastVerified': True}):
            with self.assertRaises(ValueError):
                gate.capability(record(scope, **changes), scope)

    def test_exact_batch_requires_five_runs_and_matching_terminals(self):
        records = {scope: record(scope) for scope in gate.CASES.values()}
        self.assertEqual(gate.test_events(raw(events(records)), 0), (records, True))
        unavailable = {scope: record(scope, 'unavailable') for scope in gate.CASES.values()}
        self.assertEqual(gate.test_events(raw(events(unavailable)), 0), (unavailable, True))
        for kind in ('missing', 'duplicate', 'skip', 'wrong-test', 'wrong-scope', 'opt-out', 'nonzero'):
            rows = events(records)
            code = 0
            if kind == 'missing':
                rows = rows[:1] + rows[4:]
            elif kind == 'duplicate':
                rows.insert(3, copy.deepcopy(rows[2]))
            elif kind == 'skip':
                rows[3]['Action'] = 'skip'
            elif kind == 'wrong-test':
                rows[1]['Test'] += '/child'
            elif kind == 'wrong-scope':
                rows[2]['Output'] = gate.MARKER + json.dumps(record('selected_private_interface_os'))
            elif kind == 'opt-out':
                changed = dict(records, os_local_only=record('os_local_only', 'not_run', reason='opt_in_required'))
                rows = events(changed)
            else:
                code = 1
            try:
                _, accepted = gate.test_events(raw(rows), code)
            except ValueError:
                accepted = False
            self.assertFalse(accepted, kind)
        failed = dict(records, os_local_only=record('os_local_only', 'failed', reason='cleanup_uncertain'))
        got, accepted = gate.test_events(raw(events(failed)), 1)
        self.assertEqual(got, failed)
        self.assertFalse(accepted)

    def test_fragmented_markers_preserve_owner_schema_and_byte_bounds(self):
        records = {scope: record(scope) for scope in gate.CASES.values()}
        original = events(records)
        for split in (1, len(gate.LEGACY_MARKER) - 1, 1024):
            rows = copy.deepcopy(original)
            target = next(i for i, row in enumerate(rows) if row.get('Test') == 'TestDNSServiceLegacySerializationCapability' and row['Action'] == 'output')
            event = rows[target]
            text = event['Output']
            pieces = [text[:split], text[split:]]
            rows[target:target + 1] = [dict(event, Output=piece) for piece in pieces]
            self.assertEqual(gate.test_events(raw(rows), 0), (records, True))
        target = next(i for i, row in enumerate(original) if row.get('Test') == 'TestDNSServiceLegacySerializationCapability' and row['Action'] == 'output')
        event = original[target]
        prefix, suffix = event['Output'][:10], event['Output'][10:]
        for violation in ('owner-switch', 'package-switch', 'terminal', 'run', 'unfinished', 'duplicate', 'oversize', 'utf8-size', 'schema'):
            rows = copy.deepcopy(original)
            chunks = [dict(event, Output=prefix), dict(event, Output=suffix)]
            if violation == 'owner-switch': chunks[1]['Test'] = 'TestDNSServiceLocalOnlyCapability'
            elif violation == 'package-switch': chunks[1].pop('Test')
            elif violation in ('terminal', 'run'):
                chunks.insert(1, dict(event, Action='pass' if violation == 'terminal' else 'run', Output=''))
            elif violation == 'unfinished': chunks[-1]['Output'] = suffix.rstrip('\n')
            elif violation == 'duplicate': chunks.append(copy.deepcopy(event))
            elif violation == 'oversize': chunks = [dict(event, Output='x' * gate.MAX_OUTPUT_LINE_BYTES), dict(event, Output='\n')]
            elif violation == 'utf8-size': chunks = [dict(event, Output='é' * (gate.MAX_OUTPUT_LINE_BYTES // 2)), dict(event, Output='\n')]
            else: chunks[-1]['Output'] = suffix.replace('native_c_sendto', 'unapproved_primitive')
            rows[target:target + 1] = chunks
            with self.subTest(violation=violation), self.assertRaises(ValueError):
                gate.test_events(raw(rows), 0)
        # A final fragment also fails without a following terminal event.
        with self.assertRaises(ValueError):
            gate.test_events(raw(original[:target] + [dict(event, Output=prefix)]), 0)
        for text in ('x' * (gate.MAX_OUTPUT_LINE_BYTES - 1) + '\n', 'é' * ((gate.MAX_OUTPUT_LINE_BYTES - 2) // 2) + 'x\n'):
            rows = copy.deepcopy(original)
            rows.insert(target, dict(event, Output=text))
            self.assertTrue(gate.test_events(raw(rows), 0)[1])

    def test_pinned_converter_fragments_complete_logged_receipt(self):
        # Opt-in runs the installed pinned converter only, never a Go test or
        # native socket. CI without that explicit tool path keeps pure tests.
        tool = os.environ.get('SOBALINK_TEST2JSON_GO')
        if not tool:
            self.skipTest('pinned converter path not selected')
        env = dict(os.environ, GOTOOLCHAIN='local', GOPROXY='off', GOSUMDB='off')
        version = subprocess.run([tool, 'env', 'GOVERSION'], capture_output=True, check=True, timeout=10, env=env).stdout.strip()
        self.assertEqual(version, b'go1.27.1')
        records = {scope: record(scope) for scope in gate.CASES.values()}
        good = record(gate.LEGACY_SCOPE)
        records[gate.LEGACY_SCOPE] = record(gate.LEGACY_SCOPE, 'unavailable',
            reason='legacy_send_unavailable', apiRecordsReady=True, nativeSendAttempted=True, ownersJoined=True,
            failureStage='legacy_send', failureKind='host_unreachable',
            socketReadback=good['socketReadback'], routeLookup=good['routeLookup'])
        lines = []
        for name, scope in gate.CASES.items():
            value = records[scope]
            marker = gate.LEGACY_MARKER if scope == gate.LEGACY_SCOPE else gate.MARKER
            lines += ['=== RUN   ' + name, '    dnssd_legacy_native_test.go:31: ' + marker + json.dumps(value, separators=(',', ':')),
                      ('--- SKIP: ' if value['status'] == 'unavailable' else '--- PASS: ') + name + ' (0.00s)']
        lines += ['PASS']
        converted = subprocess.run([tool, 'tool', 'test2json', '-p', gate.PACKAGE],
            input=('\n'.join(lines) + '\n').encode(), capture_output=True, check=True, timeout=10, env=env).stdout
        chunks = [json.loads(line) for line in converted.splitlines()]
        marker_chunk = next(row['Output'] for row in chunks if gate.LEGACY_MARKER in row.get('Output', ''))
        self.assertEqual(len(marker_chunk.encode()), 1024)
        self.assertFalse(marker_chunk.endswith('\n'))
        self.assertEqual(gate.test_events(converted, 0), (records, True))
        observed = gate.collector_observation(('\n'.join(lines) + '\n').encode(), b'', 'completed', 0, converted)
        self.assertEqual(observed['eventScanStatus'], 'scanned')
        self.assertEqual(observed['rawStreams']['stdout']['markerTokenCounts'], {'standard': 4, 'legacy': 1})
        self.assertTrue(all(count == 1 for count in observed['completeMarkerLineCounts'].values()))
        self.assertEqual(observed['incompleteMarkerLines'], 0)

    def test_collector_fixed_counts_do_not_replace_acceptance(self):
        records = {scope: record(scope) for scope in gate.CASES.values()}
        rows = events(records)
        source = b''.join(b'\x16=== RUN   ' + name.encode() + b'\n' for name in gate.CASES)
        source += b''.join(b'--- PASS: ' + name.encode() + b' (0.00s)\n' for name in gate.CASES)
        source += gate.MARKER.encode() + b'synthetic-private\n\x16PASS\n'
        data = raw(rows)
        observed = gate.collector_observation(source, source, 'completed', 0, data, b'synthetic-private')
        self.assertEqual(observed['nativeStdoutBytes'], len(source))
        self.assertEqual(observed['nativeStderrBytes'], len(source))
        self.assertEqual(observed['rawStreams']['stdout'], observed['rawStreams']['stderr'])
        self.assertEqual(observed['converterStderrClass'], 'unclassified')
        self.assertEqual(observed['converterStdoutBytes'], len(data))
        self.assertEqual(observed['converterState'], 'completed')
        self.assertEqual(observed['converterExitCode'], 0)
        self.assertEqual(observed['eventScanStatus'], 'scanned')
        self.assertEqual(observed['eventCount'], 17)
        self.assertEqual(observed['outputEventCount'], 5)
        self.assertTrue(observed['rawStreams']['stdout']['passTokenSeen'])
        self.assertFalse(observed['rawStreams']['stdout']['noTestsTokenSeen'])
        self.assertTrue(all(count == 1 for count in observed['rawStreams']['stdout']['runTokenCounts'].values()))
        self.assertEqual(observed['packageLifecycleCounts'], {'start': 1, 'pass': 1, 'skip': 0, 'fail': 0, 'other': 0})
        for name in gate.CASES:
            self.assertEqual(observed['rawStreams']['stdout']['terminalTokenCounts'][name], {'pass': 1, 'skip': 0, 'fail': 0})
            self.assertEqual(observed['testLifecycleCounts'][name], {'run': 1, 'pass': 1, 'skip': 0, 'fail': 0, 'other': 0})
            self.assertEqual(observed['completeMarkerLineCounts'][name], 1)
        self.assertEqual(observed['acceptanceStatus'], 'not_run')
        self.assertNotIn('synthetic-private', json.dumps(observed))
        self.assertEqual(gate.test_events(data, 0), (records, True))
        # Explanatory counts can stay positive when the independent validator
        # rejects the JSON receipt. No scanner result is a capability proof.
        rows[2]['Output'] = gate.MARKER + 'synthetic-private\n'
        self.assertEqual(gate.collector_observation(b'', b'', 'completed', 0, raw(rows))['completeMarkerLineCounts'][rows[2]['Test']], 1)
        with self.assertRaises(ValueError):
            gate.test_events(raw(rows), 0)

    def test_collector_fragments_require_complete_same_owner_lines(self):
        name, other = list(gate.CASES)[:2]
        base = {'Action': 'output', 'Package': gate.PACKAGE, 'Test': name}
        for boundary in ('complete', 'eof', 'owner', 'lifecycle'):
            rows = [dict(base, Output=gate.MARKER[:5]), dict(base, Output=gate.MARKER[5:] + 'synthetic-private')]
            if boundary == 'complete':
                rows.append(dict(base, Output='\n'))
            elif boundary == 'owner':
                rows.append(dict(base, Test=other, Output='\n'))
            elif boundary == 'lifecycle':
                rows.append(dict(base, Action='pass'))
            observed = gate.collector_observation(gate.MARKER.encode(), b'', 'completed', 0, raw(rows))
            self.assertEqual(observed['rawStreams']['stdout']['markerTokenCounts']['standard'], 1)
            self.assertEqual(observed['rawStreams']['stdout']['completeMarkerLineCounts']['standard'], 0)
            self.assertEqual(observed['completeMarkerLineCounts'][name], int(boundary == 'complete'))
            self.assertEqual(observed['incompleteMarkerLines'], int(boundary != 'complete'))
            self.assertEqual(observed['eventScanStatus'], 'scanned' if boundary == 'complete' else 'incomplete_lines')
            self.assertNotIn('synthetic-private', json.dumps(observed))

    def test_collector_preserves_converter_failure_and_no_test_evidence(self):
        source = b'testing: warning: no tests to run\nPASS\n'
        for state, code in (('not_run', None), ('timeout', -9), ('output_limit', -9),
                            ('launch_failed', None), ('capture_failed', None), ('nonzero', 2)):
            observed = gate.collector_observation(source, b'', state, code, b'synthetic-private', b'synthetic-private')
            self.assertEqual((observed['converterState'], observed['converterExitCode']), (state, code))
            self.assertEqual(observed['eventScanStatus'], 'not_run')
            self.assertEqual(observed['eventCount'], 0)
            self.assertEqual(observed['acceptanceStatus'], 'not_run')
            self.assertTrue(observed['rawStreams']['stdout']['noTestsTokenSeen'])
            self.assertTrue(observed['rawStreams']['stdout']['passTokenSeen'])
            self.assertNotIn('synthetic-private', json.dumps(observed))
        for error, category in ((b'', 'empty'), (b'usage: go tool test2json synthetic-private', 'usage'),
                (b'go: synthetic-private', 'go_tool_error'), (b'panic: synthetic-private', 'panic'),
                (b'synthetic-private', 'unclassified')):
            observed = gate.collector_observation(b'', b'', 'nonzero', 2, error=error)
            self.assertEqual(observed['converterStderrClass'], category)
            self.assertEqual(observed['converterStderrBytes'], len(error))
            self.assertNotIn('synthetic-private', json.dumps(observed))
        observed = gate.collector_observation(b'', b'', 'completed', 0)
        self.assertEqual(observed['eventScanStatus'], 'scanned')
        self.assertEqual(observed['nativeStdoutBytes'], 0)
        self.assertEqual(observed['eventCount'], 0)
        self.assertEqual(gate.test_events(b'', 0), ({scope: None for scope in gate.CASES.values()}, False))

    def test_collector_strict_event_inventory_and_bounded_counts(self):
        base = {'Action': 'output', 'Package': gate.PACKAGE, 'Test': next(iter(gate.CASES)), 'Output': 'synthetic-private\n'}
        for data, status in ((b'synthetic-private', 'invalid_event'), (b'\xff', 'invalid_event'),
                (b'[' * 1500 + b']' * 1500, 'invalid_event'),
                (b'{"Action":"start","Action":"pass","Package":"synthetic-private"}', 'invalid_event'),
                (raw([dict(base, Test='synthetic-private')]), 'unexpected_inventory'),
                (raw([dict(base, Package='synthetic-private')]), 'unexpected_inventory'),
                (raw([dict(base, Output=3)]), 'invalid_event')):
            observed = gate.collector_observation(b'', b'', 'completed', 0, data)
            self.assertEqual(observed['eventScanStatus'], status)
            self.assertNotIn('synthetic-private', json.dumps(observed))
        parser, load = gate.module('ci-go-test'), gate.module
        with patch.object(parser, 'parse_event', side_effect=RuntimeError('synthetic-private')), \
                patch.object(gate, 'module', side_effect=lambda name: parser if name == 'ci-go-test' else load(name)):
            observed = gate.collector_observation(b'', b'', 'completed', 0, raw([base]))
        self.assertEqual(observed['eventScanStatus'], 'scanner_failed')
        self.assertNotIn('synthetic-private', json.dumps(observed))
        source = (b'=== RUN   ' + base['Test'].encode() + b'\n') * 17 + (gate.MARKER.encode() + b'\n') * 17
        observed = gate.collector_observation(source, b'', 'completed', 0,
            raw([dict(base, Output=gate.MARKER + '\n')] * 17))
        self.assertTrue(observed['countsCapped'])
        self.assertEqual(observed['rawStreams']['stdout']['runTokenCounts'][base['Test']], 16)
        self.assertEqual(observed['rawStreams']['stdout']['markerTokenCounts']['standard'], 16)
        self.assertEqual(observed['completeMarkerLineCounts'][base['Test']], 16)
        for kwargs in ({'stdout': b'x' * (64 * 1024 + 1)}, {'stderr': b'x' * (64 * 1024 + 1)},
                       {'data': b'x' * (128 * 1024 + 1)}, {'error': b'x' * (128 * 1024 + 1)},
                       {'stdout': 'synthetic-private'}, {'code': True}, {'code': 256}, {'state': 'synthetic-private'}):
            with self.assertRaises(ValueError):
                gate.collector_observation(**dict({'stdout': b'', 'stderr': b''}, **kwargs))

    def test_receive_counters_are_bounded_closed_and_retained_on_failure(self):
        observed = {'receivedPackets': 2, 'receivedBytes': 99, 'metadataAccepted': 1,
                    'metadataRejected': 1, 'decodedPackets': 0, 'decodeRejected': 1,
                    'rateLimitedPackets': 0, 'matchingRecordKinds': 0}
        for status, reason in (('unavailable', 'deadline'), ('failed', 'cleanup_uncertain')):
            value = record(gate.RAW_SCOPE, status, reason=reason, receiveCounters=observed)
            self.assertEqual(gate.capability(value, gate.RAW_SCOPE), value)
        for key in gate.COUNTERS:
            for count in (-1, True, 1.5, 'synthetic-private', None,
                          512 * 1201 + 1 if key == 'receivedBytes' else 16 if key == 'matchingRecordKinds' else 513):
                with self.subTest(key=key, count=count), self.assertRaises(ValueError):
                    gate.receive_counters(dict(observed, **{key: count}))
            missing = dict(observed)
            del missing[key]
            with self.assertRaises(ValueError):
                gate.receive_counters(missing)
        for bad in ([], 'synthetic-private', dict(observed, address='synthetic-private'),
                    dict(observed, receivedBytes=2 * 1201 + 1), dict(observed, receivedBytes=11),
                    dict(observed, metadataAccepted=2), dict(observed, decodedPackets=1),
                    dict(observed, matchingRecordKinds=1)):
            with self.assertRaises(ValueError):
                gate.receive_counters(bad)
        self.assertEqual(gate.receive_counters(counters()), counters())
        minimum = dict(counters(True), receivedBytes=12)
        self.assertEqual(gate.receive_counters(minimum), minimum)
        for scope in (gate.RAW_SCOPE, gate.LEGACY_SCOPE):
            value = record(scope, receiveCounters=dict(counters(True), receivedBytes=0))
            with self.assertRaises(ValueError):
                gate.legacy_capability(value) if scope == gate.LEGACY_SCOPE else gate.capability(value, scope)
        self.assertIsNone(gate.receive_counters(None))
        for scope in gate.PROOFS.keys() - {gate.RAW_SCOPE}:
            with self.assertRaises(ValueError):
                gate.capability(record(scope, receiveCounters=counters()), scope)

    def test_legacy_serialization_is_separate_from_all_original_scope_proofs(self):
        for route in ('selected_interface', 'local_loopback'):
            value = record(gate.LEGACY_SCOPE, receivePath=route)
            self.assertEqual(gate.legacy_capability(value), value)
            with self.assertRaises(ValueError):
                gate.capability(value, gate.RAW_SCOPE)
            for key in gate.LEGACY_FLAGS:
                with self.subTest(route=route, key=key), self.assertRaises(ValueError):
                    gate.legacy_capability(dict(value, **{key: not value[key]}))
            records = {scope: record(scope, 'unavailable') for scope in gate.CASES.values()}
            records[gate.LEGACY_SCOPE] = value
            got, accepted = gate.test_events(raw(events(records)), 0)
            self.assertTrue(accepted)
            report = gate.empty_result()
            report.update(capabilities=got, diagnosticPassed=True)
            gate.summarize_scope_proofs(report)
            self.assertTrue(report['legacySerializationVerified'])
            self.assertEqual(report['legacyReceivePath'], route)
            for flag in ('selectedInterfaceVerified', 'rawReceiveVerified', 'networkMulticastVerified',
                         'physicalLANVerified', 'automaticCoreBrowserVerified', 'releaseValidation'):
                self.assertFalse(report[flag], flag)
            report['diagnosticPassed'] = False
            gate.summarize_scope_proofs(report)
            self.assertFalse(report['legacySerializationVerified'])
            self.assertEqual(report['legacyReceivePath'], 'none')
        value = record(gate.LEGACY_SCOPE)
        for key in gate.LEGACY_KEYS:
            missing = dict(value)
            del missing[key]
            with self.assertRaises(ValueError):
                gate.legacy_capability(missing)
        for changes in ({'scope': gate.RAW_SCOPE}, {'receivePath': 'none'},
                        {'receivePath': 'synthetic-private'}, {'receiveCounters': None},
                        {'receiveCounters': counters()}, {'reason': 'native_raw_receive_observed'},
                        {'reason': 'synthetic-private'}, {'failureStage': 'synthetic-private'},
                        {'failureKind': 'synthetic-private'}, {'secret': 'synthetic-private'},
                        {'oneQuerySent': 1}):
            with self.assertRaises(ValueError):
                gate.legacy_capability(dict(value, **changes))
        with self.assertRaises(ValueError):
            gate.legacy_capability(record(gate.RAW_SCOPE))

    def test_legacy_failure_observations_are_closed_and_never_proof(self):
        records = {scope: record(scope) for scope in gate.CASES.values()}
        for reason in gate.LEGACY_UNAVAILABLE:
            stage, kind = 'none', 'none'
            if reason == 'native_api_unavailable':
                stage, kind = 'callback', 'name_conflict'
            elif reason.startswith('legacy_'):
                stage = reason.removesuffix('_unavailable')
                kind = 'other' if stage in {'legacy_deadline', 'legacy_send'} else 'unavailable'
            value = record(gate.LEGACY_SCOPE, 'unavailable', reason=reason,
                           failureStage=stage, failureKind=kind,
                           apiRecordsReady=stage == 'legacy_readback',
                           socketReadback={key: 'unavailable' for key in gate.READBACK_KEYS} if stage == 'legacy_readback' else None)
            self.assertEqual(gate.legacy_capability(value), value)
            current = dict(records, **{gate.LEGACY_SCOPE: value})
            got, accepted = gate.test_events(raw(events(current)), 0)
            self.assertTrue(accepted)
            self.assertEqual(got[gate.LEGACY_SCOPE], value)
        for reason in gate.LEGACY_FAILED - {'socket_readback_mismatch'}:
            value = record(gate.LEGACY_SCOPE, 'failed', reason=reason,
                           apiRecordsReady=True, failureStage='legacy_send', failureKind='short_payload')
            self.assertEqual(gate.legacy_capability(value), value)
            current = dict(records, **{gate.LEGACY_SCOPE: value})
            got, accepted = gate.test_events(raw(events(current)), 1)
            self.assertFalse(accepted)
            self.assertEqual(got[gate.LEGACY_SCOPE], value)
        partial = record(gate.LEGACY_SCOPE, 'unavailable', apiRecordsReady=True, oneQuerySent=True, nativeSendAttempted=True,
                         socketReadback=record(gate.LEGACY_SCOPE)['socketReadback'], routeLookup=record(gate.LEGACY_SCOPE)['routeLookup'],
                         receivePath='local_loopback', receiveCounters=dict(counters(True), matchingRecordKinds=3))
        self.assertEqual(gate.legacy_capability(partial), partial)
        for changes in ({'reason': 'deadline'}, {'failureStage': 'legacy_send', 'failureKind': 'short_payload'},
                        {'failureStage': 'none', 'failureKind': 'unavailable'},
                        {'failureStage': 'query', 'failureKind': 'short_payload'},
                        {'reason': 'legacy_socket_unavailable', 'failureStage': 'legacy_receive', 'failureKind': 'unavailable'},
                        {'oneQuerySent': True}, {'legacySerializationVerified': True},
                        {'receiveCounters': None}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                gate.legacy_capability(record(gate.LEGACY_SCOPE, 'unavailable', **changes))
        for scope in (gate.RAW_SCOPE, gate.LEGACY_SCOPE):
            parse = gate.legacy_capability if scope == gate.LEGACY_SCOPE else lambda value: gate.capability(value, scope)
            with self.assertRaises(ValueError):
                parse(record(scope, 'unavailable', receiveCounters=None))
            with self.assertRaises(ValueError):
                parse(record(scope, 'verified', receiveCounters=dict(counters(True), matchingRecordKinds=7)))

    def test_legacy_deadline_and_send_error_classes_are_separate(self):
        for stage, kinds in gate.LEGACY_STAGE_KINDS.items():
            for kind in kinds - {'mismatch'}:
                value = record(gate.LEGACY_SCOPE, 'unavailable', apiRecordsReady=True,
                               reason=stage + '_unavailable', failureStage=stage, failureKind=kind,
                               socketReadback={key: 'unavailable' for key in gate.READBACK_KEYS} if stage == 'legacy_readback' else None)
                self.assertEqual(gate.legacy_capability(value), value)
                self.assertFalse(value['oneQuerySent'])
                self.assertFalse(value['legacySerializationVerified'])
                self.assertFalse(value['rawPacketVerified'])
                for bad_kind in (gate.KINDS | gate.LEGACY_SEND_KINDS) - kinds:
                    with self.subTest(stage=stage, kind=bad_kind), self.assertRaises(ValueError):
                        gate.legacy_capability(dict(value, failureKind=bad_kind))
        # Old collapsed send classification must not pass as a new exact-source
        # result; API callbacks cannot acquire socket-only errno categories.
        for stage, kind in (('legacy_send', 'unavailable'), ('legacy_deadline', 'short_payload'),
                            ('query', 'source_unavailable'), ('query', 'other')):
            with self.subTest(stage=stage, kind=kind), self.assertRaises(ValueError):
                gate.legacy_capability(record(gate.LEGACY_SCOPE, 'unavailable',
                    reason='native_api_unavailable' if stage == 'query' else stage + '_unavailable',
                    apiRecordsReady=True, failureStage=stage, failureKind=kind))

    def test_native_readback_route_and_send_boundaries(self):
        good = record(gate.LEGACY_SCOPE)
        for key in gate.READBACK_KEYS:
            for state in ('unavailable', 'mismatch'):
                changed = dict(good, socketReadback=dict(good['socketReadback'], **{key: state}))
                if key == 'multicastAddress' and state == 'mismatch':
                    self.assertEqual(gate.legacy_capability(changed), changed)
                else:
                    with self.assertRaises(ValueError): gate.legacy_capability(changed)
        for value in (dict(good['socketReadback'], localAddress='synthetic-private'), {}, [], {'boundEndpoint': 1}):
            with self.assertRaises(ValueError): gate.socket_readback(value)
        mismatch = record(gate.LEGACY_SCOPE, 'failed', reason='socket_readback_mismatch', apiRecordsReady=True,
                          failureStage='legacy_readback', failureKind='mismatch',
                          socketReadback=dict(good['socketReadback'], nonblocking='mismatch'))
        self.assertEqual(gate.legacy_capability(mismatch), mismatch)
        for status in gate.ROUTE_STATUSES - {'observed'}:
            route = dict(status=status, **{key: key == 'ownerJoined' for key in gate.ROUTE_FLAGS})
            # A missing ordinary route is contextual; it cannot invalidate the
            # independently verified selected-interface multicast socket.
            value = dict(good, routeLookup=route)
            self.assertEqual(gate.legacy_capability(value), value)
        for route in (None, dict(good['routeLookup'], ownerJoined=False),
                      dict(good['routeLookup'], gateway='synthetic-private')):
            with self.assertRaises(ValueError): gate.legacy_capability(dict(good, routeLookup=route))
        for changes in ({'sendPrimitive':'go_sendto'}, {'nativeSendAttempted':False},
                        {'socketReadback':None}, {'routeLookup':None}):
            with self.assertRaises(ValueError): gate.legacy_capability(dict(good, **changes))

    def test_marker_types_and_lifecycles_cannot_be_cross_wired(self):
        records = {scope: record(scope) for scope in gate.CASES.values()}
        for wrong in ('original-marker', 'legacy-marker', 'mixed-marker', 'run-marker',
                      'missing-legacy', 'duplicate-legacy', 'legacy-skip', 'legacy-opt-out'):
            rows = events(records)
            legacy = next(index for index, row in enumerate(rows)
                          if row.get('Test') == 'TestDNSServiceLegacySerializationCapability' and row['Action'] == 'output')
            if wrong == 'original-marker':
                rows[legacy]['Output'] = gate.MARKER + json.dumps(records[gate.LEGACY_SCOPE])
            elif wrong == 'legacy-marker':
                rows[2]['Output'] = gate.LEGACY_MARKER + json.dumps(records[gate.LEGACY_SCOPE])
            elif wrong == 'mixed-marker':
                rows[legacy]['Output'] += gate.MARKER
            elif wrong == 'run-marker':
                rows[legacy - 1]['Output'] = rows[legacy]['Output']
                del rows[legacy]
            elif wrong == 'missing-legacy':
                del rows[legacy - 1:legacy + 2]
            elif wrong == 'duplicate-legacy':
                rows.insert(legacy, copy.deepcopy(rows[legacy]))
            elif wrong == 'legacy-skip':
                rows[legacy + 1]['Action'] = 'skip'
            else:
                records[gate.LEGACY_SCOPE] = record(gate.LEGACY_SCOPE, 'not_run', reason='opt_in_required', receiveCounters=None)
                rows = events(records)
            with self.subTest(wrong=wrong):
                try:
                    _, accepted = gate.test_events(raw(rows), 0)
                except ValueError:
                    accepted = False
                self.assertFalse(accepted)

    def test_compiler_identity_and_failure_categories_never_return_raw_text(self):
        got = gate.compiler_identity(b'Apple clang version 17.0.0 (clang-1700.3.19.1)\nTarget: private-host', '26.0', '25A354')
        self.assertEqual(got, {'vendor': 'apple-clang', 'version': '17.0.0', 'build': '1700.3.19.1', 'sdkVersion': '26.0', 'sdkBuild': '25A354'})
        for inputs in ((b'synthetic-private', '26.0', '25A354'),
                       (b'Apple clang version 17.0.0 (clang-1700.3.19.1)', 'private', '25A354'),
                       (b'Apple clang version 17.0.0 (clang-1700.3.19.1)', '26.0', 'private')):
            with self.assertRaises(ValueError):
                gate.compiler_identity(*inputs)
        for text, expected in ((b'/private/file: undeclared function', 'missing_declaration'),
                               (b'/private/file: too few arguments', 'argument_count'),
                               (b'/private/file: incompatible pointer', 'type_mismatch'),
                               (b'/private/file: file not found', 'missing_header'),
                               (b'/private/file: undefined symbols', 'link_failed')):
            self.assertEqual(gate.compiler_kind(text, 'nonzero'), expected)
        self.assertEqual(gate.compiler_kind(b'synthetic-private', 'timeout'), 'timeout')
        self.assertEqual(gate.compiler_kind(b'dnssd_bridge_darwin.c:1:1: error: incompatible pointer\n/private/header:2:1: error: file not found', 'nonzero'), 'type_mismatch')

    def test_mocked_compile_and_batch_exports_only_reconstructed_receipts(self):
        preflight = {'source-event': 'invalid_source_event', 'source-inventory': 'invalid_source_inventory',
                     'go-identity': 'native_identity_mismatch', 'clang-identity': 'compiler_identity_format',
                     'sdk-version': 'sdk_version_format', 'sdk-build': 'sdk_build_format',
                     'sdk-files': 'sdk_file_missing', 'source-policy': 'source_policy_missing',
                     'clang-discovery': 'command_failed'}
        for failure in (None, 'c-compile', 'c-link', 'go-compile', 'unavailable', 'runtime-failed',
                        'converter-timeout', 'converter-empty', 'converter-invalid', *preflight):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                provider = root / 'internal/nearby/provider_darwin.go'
                provider.parent.mkdir(parents=True)
                provider.write_text('#cgo CFLAGS: -std=c11\n')
                clang, go = root / 'clang', root / 'go'
                clang.write_bytes(b'synthetic clang'); go.write_bytes(b'synthetic go')
                sdk = root / 'SDK'
                for relative in ('usr/include/dns_sd.h', 'SDKSettings.json', 'usr/lib/libSystem.tbd'):
                    path = sdk / relative; path.parent.mkdir(parents=True, exist_ok=True)
                    if failure != 'sdk-files' or relative != 'usr/include/dns_sd.h':
                        path.write_bytes(b'synthetic SDK')
                sha, tree = 'a' * 40, 'b' * 40
                branch = 'diagnostic/dnssd/' + sha[:12] + '-synthetic'
                event, output = root / 'event', root / 'output'
                event.write_text(json.dumps({'ref_type': 'branch', 'ref': branch,
                    'repository': {'full_name': 'webkaz-labs/sobalink', 'private': False}}))
                native = gate.module('ci-nearby-native')
                real_module = gate.module
                terminal = real_module('ci-dnssd-terminal')
                def terminal_capture(binary, *, env, private):
                    state, code, out, err = native.capture(terminal.native_command(binary), env=env, timeout=75, limit=native.RUNTIME_BYTES)
                    launch = terminal.empty_receipt()
                    from test_ci_dnssd_terminal import native_observation
                    launch['nativeOriginObservation'] = native_observation()
                    launch['nativeOriginVerified'] = True
                    launch.update(status='observed', reason='observed', nativeProcessStatus=state, nativeExitCode=code, **{key: True for key in (
                        'availabilityVerified', 'openCompleted', 'markerVerified', 'markerOwnerJoined',
                        'terminalAncestorObserved', 'nativeStartObserved', 'nativeOwnerJoined', 'supervisorTerminationObserved')})
                    return state, code, out, err, terminal.receipt(launch)
                terminal.capture = terminal_capture
                def module(name):
                    return native if name == 'ci-nearby-native' else terminal if name == 'ci-dnssd-terminal' else real_module(name)
                def checked(command, **kwargs):
                    if command == ['git', 'rev-parse', 'HEAD']: return sha.encode()
                    if command == ['git', 'rev-parse', 'HEAD^{tree}']: return tree.encode()
                    if command[:2] == ['go', 'env']: return b'{}'
                    if command[:2] == ['otool', '-L']: return b'private-binary:\n\t/usr/lib/libSystem.B.dylib (compatibility version 1.0.0, current version 1351.0.0)\n'
                    if command[-1] == '--version': return b'synthetic-private' if failure == 'clang-identity' else b'Apple clang version 17.0.0 (clang-1700.3.19.1)'
                    if command[-1] == 'clang': return str(clang).encode()
                    if command[-1] == '--show-sdk-path': return str(sdk).encode()
                    if command[-1] == '--show-sdk-version': return b'synthetic-private' if failure == 'sdk-version' else b'26.0'
                    if command[-1] == '--show-sdk-build-version': return b'synthetic-private' if failure == 'sdk-build' else b'25A354'
                    if command[:3] == ['go', 'mod', 'download']: return b''
                    raise AssertionError('unexpected command')
                records = {scope: record(scope, 'unavailable' if failure == 'unavailable' else 'verified') for scope in gate.CASES.values()}
                if failure == 'runtime-failed':
                    records['os_local_only'] = record('os_local_only', 'failed', reason='cleanup_uncertain')
                commands = []
                def capture(command, **kwargs):
                    if command[:3] != ['go', 'test', '-race'] and command[:3] != ['go', 'tool', 'test2json'] and '-c' not in command and '-x' not in command and not command[0].endswith('dnssd.test'):
                        if failure == 'clang-discovery' and command[-1] == 'clang':
                            return 'nonzero', 1, b'', b'synthetic-private'
                        return 'completed', 0, checked(command), b''
                    commands.append((command, kwargs))
                    if command[0] == str(clang) or command[:3] == ['go', 'test', '-race']:
                        stage = ('c-link' if '-x' in command else 'c-compile') if command[0] == str(clang) else 'go-compile'
                        if failure == stage:
                            return 'nonzero', 1, b'', b'synthetic-private: too few arguments'
                        Path(command[command.index('-o') + 1]).write_bytes(b'synthetic object')
                    elif command[:3] == ['go', 'tool', 'test2json']:
                        if failure == 'converter-timeout':
                            return 'timeout', -9, b'', b'synthetic-private'
                        if failure == 'converter-empty':
                            return 'completed', 0, b'', b''
                        if failure == 'converter-invalid':
                            return 'completed', 0, b'synthetic-private', b''
                        return 'completed', 0, raw(events(records)), b'synthetic-private'
                    else:
                        return ('nonzero', 1, b'synthetic-private', b'') if failure == 'runtime-failed' else ('completed', 0, b'synthetic-private', b'')
                    return 'completed', 0, b'', b''
                with ExitStack() as stack:
                    stack.enter_context(patch.dict(os.environ, GITHUB_ACTIONS='true', RUNNER_TEMP=temporary,
                        GITHUB_EVENT_PATH=str(event), GITHUB_OUTPUT=str(output), GITHUB_EVENT_NAME='create',
                        GITHUB_REPOSITORY='webkaz-labs/sobalink', GITHUB_SHA=('c' * 40 if failure == 'source-event' else sha), GITHUB_REF='refs/heads/' + branch))
                    stack.enter_context(patch.object(gate.sys, 'platform', 'darwin'))
                    stack.enter_context(patch.object(gate, 'ROOT', root))
                    stack.enter_context(patch.object(gate, 'module', side_effect=module))
                    stack.enter_context(patch.object(native, 'checked', side_effect=checked))
                    stack.enter_context(patch.object(native, 'capture', side_effect=capture))
                    stack.enter_context(patch.object(native, 'identities', side_effect=ValueError('synthetic-private') if failure == 'go-identity' else None, return_value={'go': 'go1.27.1', 'goos': 'darwin', 'goarch': 'arm64', 'runner': 'macos-26', 'race': True}))
                    stack.enter_context(patch.object(native, 'source_inventory', side_effect=ValueError('synthetic-private') if failure == 'source-inventory' else None, return_value={name: '0' * 64 for name in gate.POLICY_FILES if failure != 'source-policy' or name != 'go.sum'}))
                    stack.enter_context(patch.object(gate.shutil, 'which', return_value=str(go)))
                    printed = stack.enter_context(patch('sys.stdout', new_callable=io.StringIO))
                    self.assertEqual(gate.main([]), 0 if failure in (None, 'unavailable') else 1)
                export = root / 'soba-dnssd-sanitized'
                result_text, provenance_text = (export / 'result.json').read_text(), (export / 'provenance.json').read_text()
                result, provenance = json.loads(result_text), json.loads(provenance_text)
                self.assertEqual(set(result), set(gate.empty_result()))
                self.assertEqual(result['selectedInterfaceVerified'], failure is None)
                self.assertEqual(result['rawReceiveVerified'], failure is None)
                self.assertEqual(result['legacySerializationVerified'], failure is None)
                self.assertEqual(result['legacyReceivePath'], 'selected_interface' if failure is None else 'none')
                self.assertFalse(result['networkMulticastVerified'])
                self.assertEqual(provenance['commit'], sha)
                self.assertEqual(provenance['tree'], tree)
                if failure not in preflight or failure in ('sdk-files', 'source-policy'):
                    self.assertIn('compiler', provenance)
                self.assertEqual(output.read_text(), 'sanitized_ready=true\n')
                for text in (result_text, provenance_text, printed.getvalue()):
                    self.assertNotIn('synthetic-private', text)
                    self.assertNotIn(temporary, text)
                if failure in preflight:
                    self.assertEqual(provenance['stage'], failure)
                    self.assertEqual(result['preflightFailureKind'], preflight[failure])
                    self.assertFalse(commands)
                    if failure == 'sdk-files':
                        self.assertEqual(provenance['sdkFiles']['dns_sd.h'], 'missing')
                    continue
                self.assertEqual(commands[0][0][0], str(clang))
                if failure in ('c-compile', 'c-link', 'go-compile'):
                    self.assertEqual(provenance['stage'], failure)
                    self.assertEqual(result['compileFailureKind'], 'argument_count')
                else:
                    collector = result['collector']
                    if failure and failure.startswith('converter-'):
                        self.assertEqual(result['capabilities'], {scope: None for scope in gate.CASES.values()})
                        self.assertFalse(result['eventsAccepted'])
                        self.assertFalse(result['diagnosticPassed'])
                        expected = {'converter-timeout': ('timeout', 'not_run', 'not_run'),
                                    'converter-empty': ('completed', 'scanned', 'unaccepted'),
                                    'converter-invalid': ('completed', 'invalid_event', 'validation_failed')}[failure]
                        self.assertEqual(tuple(collector[key] for key in ('converterState', 'eventScanStatus', 'acceptanceStatus')), expected)
                    else:
                        self.assertEqual(result['capabilities'], records)
                        self.assertEqual(collector['acceptanceStatus'], 'accepted' if failure != 'runtime-failed' else 'unaccepted')
                    self.assertEqual(collector['nativeStdoutBytes'], len(b'synthetic-private'))
                    self.assertTrue(result['cLinkPassed'])
                    self.assertEqual(provenance['sdkFiles']['libdns_sd.tbd'], 'missing')
                    self.assertEqual(provenance['sdkFiles']['libSystem.tbd'], 'available')
                    self.assertEqual(provenance['cLinkLibraries'], ['libSystem.B.dylib'])
                    self.assertEqual(provenance['linkage'], 'implicit-system-umbrella')
                    link = next(command for command, _ in commands if '-x' in command)
                    self.assertNotIn('-ldns_sd', link)
                    self.assertNotIn('-lSystem', link)
                    runtime = next((command, kwargs) for command, kwargs in commands if command[0].endswith('dnssd.test'))
                    self.assertEqual(runtime[0][1:], ['-test.v=test2json', '-test.run=' + gate.SELECTOR, '-test.count=1', '-test.timeout=60s'])
                    self.assertEqual(runtime[1]['timeout'], 75)
                    self.assertTrue(all(runtime[1]['env'][key] == '1' for key in gate.OPT_INS))
                    converter = next((command, kwargs) for command, kwargs in commands if command[:3] == ['go', 'tool', 'test2json'])
                    self.assertEqual(converter[0], ['go', 'tool', 'test2json', '-p', gate.PACKAGE])
                    self.assertEqual(converter[1]['timeout'], 15)
                    self.assertEqual(converter[1]['limit'], 2 * native.RUNTIME_BYTES)
                    self.assertEqual(converter[1]['stdin'], b'synthetic-private')

    def test_compiler_location_is_first_error_in_exact_tracked_bridge_only(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            directory = root / 'internal/nearby'
            directory.mkdir(parents=True)
            for name in ('dnssd_bridge_darwin.c', 'dnssd_bridge_darwin.h'):
                (directory / name).write_bytes(b'first line\nsecond line\n')
            with patch.object(gate, 'ROOT', root):
                for name in ('dnssd_bridge_darwin.c', 'dnssd_bridge_darwin.h'):
                    for spelling in (str(directory / name), 'internal/nearby/' + name, name):
                        diagnostic = (spelling + ':2:4: error: synthetic-private-symbol\n').encode()
                        self.assertEqual(gate.compiler_location(diagnostic), {'file': name, 'line': 2, 'column': 4})
                valid = b'dnssd_bridge_darwin.c:1:1: error: synthetic-private'
                for diagnostic in (b'/private/other/dnssd_bridge_darwin.c:1:1: error: private',
                        b'../dnssd_bridge_darwin.c:1:1: error: private',
                        b'dnssd_bridge_darwin.c:0:1: error: private',
                        b'dnssd_bridge_darwin.c:3:1: error: private',
                        b'dnssd_bridge_darwin.c:1:0: error: private',
                        b'dnssd_bridge_darwin.c:1:9000: error: private',
                        b'dnssd_bridge_darwin.c:1:99: error: private',
                        b'dnssd_bridge_darwin.c:1:1: warning: private',
                        b'\x1b[31mdnssd_bridge_darwin.c:1:1: error: private',
                        b'/private/SDK/header.h:1:1: error: private\n' + valid,
                        b'x' * (64 * 1024) + b'\n' + valid,
                        b'no error\n' * 512 + valid):
                    self.assertIsNone(gate.compiler_location(diagnostic))
                self.assertEqual(gate.compiler_location(valid + b'\n' + b'dnssd_bridge_darwin.h:2:2: error: private'),
                                 {'file': 'dnssd_bridge_darwin.c', 'line': 1, 'column': 1})

    def test_sdk_aliases_must_resolve_inside_selected_sdk(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            sdk = root / 'SDK'
            sdk.mkdir()
            actual = sdk / 'actual'
            actual.write_bytes(b'synthetic')
            for relative in gate.SDK_FILES.values():
                path = sdk / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.symlink_to(actual)
            files, states = gate.sdk_file_inventory(sdk)
            self.assertEqual(set(files), set(gate.SDK_FILES))
            self.assertEqual(set(states.values()), {'available'})
            external = root / 'external'
            external.write_bytes(b'synthetic')
            alias = sdk / 'usr/lib/libdns_sd.tbd'
            alias.unlink()
            alias.symlink_to(external)
            files, states = gate.sdk_file_inventory(sdk)
            self.assertEqual(states['libdns_sd.tbd'], 'outside_selected_sdk')
            self.assertNotIn('libdns_sd.tbd', files)
        observed = gate.compiler_observation(b'Apple clang version 17.0.0 (clang-1700.3.19.1) unexpected-private-suffix')
        self.assertEqual(observed, {'vendor': 'apple-clang', 'version': '17.0.0', 'build': '1700.3.19.1'})
        self.assertEqual(gate.compiler_observation(b'private-path'), {'vendor': 'unknown', 'version': None, 'build': None})

    def test_link_inventory_exports_only_fixed_system_library_names(self):
        valid = b'private-path:\n\t/usr/lib/libSystem.B.dylib (compatibility version 1.0.0, current version 1351.0.0)\n'
        self.assertEqual(gate.linked_libraries(valid), ['libSystem.B.dylib'])
        for bad in (valid.replace(b'/usr/lib/libSystem.B.dylib', b'/private/libSystem.B.dylib'),
                    valid + valid.split(b'\n', 1)[1], b'private-path:\n',
                    valid.replace(b'(compatibility', b'(private'), b'x' * 16385):
            with self.assertRaises(ValueError):
                gate.linked_libraries(bad)
        self.assertEqual(gate.REQUIRED_SDK_FILES, {'dns_sd.h', 'SDKSettings.json', 'libSystem.tbd'})
        self.assertEqual(gate.SDK_FILES['libSystem.tbd'], 'usr/lib/libSystem.tbd')

    def test_workflow_is_one_native_only_job_with_literal_safe_artifacts(self):
        workflow = (gate.ROOT / '.github/workflows/activation-diagnostic.yml').read_text()
        job = workflow.split('  diagnostic-dnssd:\n', 1)[1]
        self.assertIn("if: needs.diagnostic-selection.outputs.suite == 'dnssd'", job)
        self.assertEqual(job.count('runs-on:'), 1)
        self.assertIn('runs-on: macos-26', job)
        for required in ("go-version: '1.27.1'", 'persist-credentials: false', 'contents: read',
                         "CGO_ENABLED: '1'", "GOMAXPROCS: '2'", 'cache: false',
                         'run: python -B .github/scripts/ci-dnssd-diagnostic.py'):
            self.assertIn(required, job)
        for forbidden in ('matrix:', 'setup-node', 'npm', 'prepare-engine', 'sudo', 'xcode-select',
                          'continue-on-error', 'secrets.', 'contents: write', 'id-token:', 'sandbox'):
            self.assertNotIn(forbidden, job)
        for action in ('actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1',
                       'actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16',
                       'actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a'):
            self.assertIn(action, job)
        artifact = job.split('uses: actions/upload-artifact', 1)[1]
        for filename in ('result.json', 'provenance.json'):
            self.assertIn('${{ runner.temp }}/soba-dnssd-sanitized/' + filename, artifact)
        for forbidden in ('*.json', '.log', '.jsonl', 'private', 'dnssd.test', '.o'):
            self.assertNotIn(forbidden, artifact)
        source = SCRIPT.read_text()
        self.assertIn("native.identities('darwin-arm64', versions)", source)
        self.assertIn('before != inventory()', source)
        self.assertIn('inventory = required.source_inventory if required else native.source_inventory', source)
        self.assertIn("'-race', '-p=1', '-c'", source)
        self.assertNotIn('str(exc)', source)


if __name__ == '__main__':
    unittest.main()
