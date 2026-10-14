"""Socket-free tests of the non-authorizing kernel-path explanation."""
import copy
import ctypes
import errno
import json
import os
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from test_ci_dnssd_terminal import gate, native_observation, observation


def process(pid=123, ppid=1, uid=1001, command='/synthetic/python'):
    return {'pid': pid, 'ppid': ppid, 'uid': uid, 'start': 'Tue Oct 13 00:00:00 2026', 'command': command}


def explain(chain, paths, *, mutate=None, cleanup=True, verify_native=False,
            reference_path='/synthetic/python', reference_status='observed', reference_recheck_path=None):
    calls = [chain[0]]
    reference = process(900, command='/synthetic/launcher')
    if verify_native:
        calls.extend((reference, reference))
        paths = [(reference_status, reference_path), *paths[:len(chain)],
                 ('observed', reference_path if reference_recheck_path is None else reference_recheck_path), *paths[len(chain):]]
    for row in chain:
        calls.extend((row, row))
    if verify_native:
        calls.extend((reference, reference))
        for row in chain:
            calls.extend((row, row))
    if mutate:
        mutate(calls)
    def snapshot(pid, *, on_cleanup):
        on_cleanup(cleanup)
        row = calls.pop(0)
        if isinstance(row, Exception):
            raise row
        return row
    with patch.object(gate.os, 'getuid', return_value=1001), \
            patch.object(gate, 'process_info', side_effect=snapshot), \
            patch.object(gate.sys, 'executable', '/synthetic/launcher' if verify_native else '/synthetic/python'), \
            patch.object(gate, 'kernel_path', side_effect=paths) as query:
        result = gate.explain_chain(gate.identity(chain[0]), verify_native=verify_native,
                                   interpreter_parent=gate.identity(reference) if verify_native else None)
    gate.validate_origin(result)
    return result, query.call_count


@unittest.skipUnless(os.name == 'posix', 'POSIX ownership contract; executed by required macOS CI')
class TerminalOriginTests(unittest.TestCase):
    def test_live_coordinator_image_matches_despite_a_different_launcher_path(self):
        chain = [process(ppid=124), process(124, 125, command='-zsh'),
                 process(125, 126, uid=0, command='(login)'), process(126, command=gate.TERMINAL_EXECUTABLE)]
        paths = [('observed', path) for path in ('/synthetic/python', '/bin/zsh', '/usr/bin/login', gate.TERMINAL_EXECUTABLE)]
        value, _ = explain(chain, paths * 2, verify_native=True)
        value['workerJoined'] = True
        self.assertTrue(value['interpreterReferenceVerified'])
        self.assertTrue(gate.native_origin_verified(value))
        value, _ = explain(chain, paths * 2, verify_native=True, reference_path='/synthetic/different-python')
        value['workerJoined'] = True
        self.assertTrue(value['interpreterReferenceVerified'])
        self.assertFalse(value['supervisorExecutableMatched'])
        self.assertFalse(gate.native_origin_verified(value))

    def test_missing_mismatched_or_raced_coordinator_reference_is_closed(self):
        for changed in (None, process(900, uid=0), dict(process(900), start='changed'), process(901)):
            value, count = explain([process()], [], verify_native=True,
                mutate=lambda calls: calls.__setitem__(1, changed))
            self.assertEqual(value['reason'], 'reference_changed')
            self.assertEqual(count, 0)
            self.assertFalse(value['interpreterReferenceVerified'])
        value, count = explain([process()], [], verify_native=True,
            mutate=lambda calls: calls.__setitem__(2, process(900, ppid=901)))
        self.assertEqual(value['reason'], 'reference_changed')
        self.assertEqual(count, 1)
        self.assertFalse(value['interpreterReferenceVerified'])

    def test_reference_denial_and_unavailability_never_try_fallback(self):
        for status in ('denied', 'unavailable', 'invalid'):
            value, count = explain([process()], [], verify_native=True, reference_status=status, reference_path=None)
            self.assertEqual(value['reason'], 'reference_' + status)
            self.assertEqual(count, 1)
            self.assertFalse(value['interpreterReferenceVerified'])
            self.assertFalse(value['supervisorExecutableMatched'])
        for status in ('unavailable', 'invalid'):
            value, count = explain([process()], [], verify_native=True,
                mutate=lambda calls: calls.__setitem__(1, gate.ProcessObservationError(status)))
            self.assertEqual(value['reason'], 'reference_' + status)
            self.assertEqual(count, 0)

    def test_final_reference_identity_and_image_are_rechecked_with_chain(self):
        chain = [process(ppid=124), process(124, 125, command='-zsh'),
                 process(125, 126, uid=0, command='(login)'), process(126, command=gate.TERMINAL_EXECUTABLE)]
        paths = [('observed', path) for path in ('/synthetic/python', '/bin/zsh', '/usr/bin/login', gate.TERMINAL_EXECUTABLE)]
        value, count = explain(chain, paths * 2, verify_native=True,
            mutate=lambda calls: calls.__setitem__(11, dict(calls[11], start='changed')))
        self.assertEqual((value['reason'], count), ('recheck_changed', 5))
        self.assertFalse(value['chainRechecked'])
        value, count = explain(chain, paths * 2, verify_native=True, reference_recheck_path='/synthetic/changed')
        self.assertEqual((value['reason'], count), ('recheck_changed', 6))
        self.assertFalse(value['chainRechecked'])

    def test_native_verifier_requires_the_fresh_exact_four_hop_chain(self):
        chain = [process(ppid=124), process(124, 125, command='-zsh'),
                 process(125, 126, uid=0, command='(login)'), process(126, command=gate.TERMINAL_EXECUTABLE)]
        paths = [('observed', path) for path in ('/synthetic/python', '/bin/zsh', '/usr/bin/login', gate.TERMINAL_EXECUTABLE)]
        value, count = explain(chain, paths * 2, verify_native=True)
        self.assertEqual(count, 10)
        self.assertTrue(value['chainRechecked'])
        self.assertTrue(value['supervisorExecutableMatched'])
        self.assertFalse(gate.native_origin_verified(value))
        value['workerJoined'] = True
        self.assertTrue(gate.native_origin_verified(value))
        self.assertFalse(value['authorizesNative'])
        historical, _ = explain(chain, paths)
        historical['workerJoined'] = True
        self.assertFalse(historical['chainRechecked'])
        self.assertFalse(gate.native_origin_verified(historical))

    def test_native_verifier_rejects_every_unexpected_uid_path_or_shape(self):
        base = native_observation()
        changes = [lambda v: v.update(workerJoined=False), lambda v: v.update(chainRechecked=False),
            lambda v: v.update(interpreterReferenceVerified=False),
            lambda v: v.update(supervisorExecutableMatched=False),
            lambda v: v['hops'][0].update(uidRelation='other'),
            lambda v: v['hops'][1].update(uidRelation='other'),
            lambda v: v['hops'][1].update(kernelPathClass='other'),
            lambda v: v['hops'][2].update(uidRelation='same'),
            lambda v: v['hops'][2].update(effectiveUidClass='nonzero'),
            lambda v: v['hops'][2].update(kernelPathClass='other'),
            lambda v: v['hops'][3].update(uidRelation='other'),
            lambda v: v['hops'][3].update(effectiveUidClass='zero'),
            lambda v: v.update(hops=[v['hops'][0], *v['hops']], hopCount=5),
            lambda v: v.update(hops=[v['hops'][0], v['hops'][2], v['hops'][1], v['hops'][3]])]
        for modify in changes:
            value = copy.deepcopy(base)
            modify(value)
            self.assertFalse(gate.native_origin_verified(value))
        # Display names are not used to establish a kernel path.
        value = copy.deepcopy(base)
        value['hops'][2].update(commandClass='login', kernelPathClass='other')
        self.assertFalse(gate.native_origin_verified(value))

    def test_final_recheck_rejects_changed_links_paths_and_denials(self):
        chain = [process(ppid=124), process(124, 125, command='-zsh'),
                 process(125, 126, uid=0, command='(login)'), process(126, command=gate.TERMINAL_EXECUTABLE)]
        paths = [('observed', path) for path in ('/synthetic/python', '/bin/zsh', '/usr/bin/login', gate.TERMINAL_EXECUTABLE)]
        for change in ({'pid': 999}, {'ppid': 999}, {'uid': 0}, {'start': 'changed'}):
            value, count = explain(chain, paths * 2, verify_native=True,
                mutate=lambda calls: calls.__setitem__(13, dict(calls[13], **change)))
            self.assertEqual(value['reason'], 'recheck_changed')
            self.assertEqual(count, 6)
            self.assertFalse(value['chainRechecked'])
        for state in ('denied', 'unavailable', 'invalid'):
            value, count = explain(chain, paths + [(state, None)], verify_native=True)
            self.assertEqual(value['reason'], 'recheck_' + state)
            self.assertEqual(count, 7)
            self.assertFalse(value['chainRechecked'])
        value, _ = explain(chain, paths + [('observed', '/synthetic/changed')], verify_native=True)
        self.assertEqual(value['reason'], 'recheck_changed')

    def test_native_receipt_cannot_mix_old_gate_or_marker_explanation(self):
        value = gate.empty_receipt()
        value.update(nativeOriginObservation=native_observation(), nativeOriginVerified=True, terminalAncestorObserved=True,
            availabilityVerified=True, openCompleted=True, markerVerified=True, markerOwnerJoined=True)
        gate.receipt(value)
        for modify in (lambda v: v.update(gateObservation=observation()),
                lambda v: v.update(originObservation=native_observation()),
                lambda v: v.update(nativeOriginVerified=False),
                lambda v: v.update(markerVerified=False),
                lambda v: v.update(nativeOriginObservation=None),
                lambda v: v.update(terminalAncestorObserved=False), lambda v: v.update(mode='marker_only')):
            changed = copy.deepcopy(value)
            modify(changed)
            with self.assertRaises(ValueError):
                gate.receipt(changed)

    def test_observer_mode_and_nonce_cannot_be_reused_across_gates(self):
        for marker_only, verify, nonce_matches in ((True, False, True), (False, True, True),
                (True, True, True), (False, False, True), (False, True, False)):
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                own = gate.identity(dict(process(), uid=os.getuid()))
                nonce = 'a' * 64
                gate.write_private(root / 'plan.json', {'nonce': nonce, 'markerOnly': marker_only,
                    'parent': gate.identity(dict(process(900), uid=os.getuid())),
                    'launchDeadline': 135, 'scriptSha256': gate.digest(gate.SCRIPT)})
                gate.write_private(root / 'started.json', {'nonce': nonce, 'owner': own})
                gate.write_private(root / 'marker.json', {'nonce': nonce if nonce_matches else 'b' * 64,
                    'owner': own, 'markerVerified': True, 'markerOwnerJoined': True})
                with patch.object(gate.time, 'monotonic', return_value=100), patch.object(gate, 'explain_chain',
                        return_value=gate.empty_origin('deadline')) as explain_call, patch('builtins.print'):
                    self.assertEqual(gate.observe_child(root, verify_native=verify),
                        0 if marker_only != verify and nonce_matches else 1)
                self.assertEqual(explain_call.call_count, int(marker_only != verify and nonce_matches))
                if explain_call.called:
                    self.assertEqual(explain_call.call_args.kwargs, {'verify_native': verify,
                        'interpreter_parent': gate.identity(dict(process(900), uid=os.getuid())) if verify else None})

    def test_system_login_boundary_is_explained_but_never_authorizes(self):
        chain = [process(ppid=124), process(124, 125, command='-zsh'),
                 process(125, 126, uid=0, command='(login)'), process(126, command=gate.TERMINAL_EXECUTABLE)]
        paths = [('observed', path) for path in ('/synthetic/python', '/bin/zsh', '/usr/bin/login', gate.TERMINAL_EXECUTABLE)]
        result, count = explain(chain, paths)
        self.assertEqual(count, 4)
        self.assertEqual(result['reason'], 'terminal_path')
        self.assertTrue(result['terminalPathObserved'])
        self.assertTrue(result['queriesJoined'])
        self.assertFalse(result['workerJoined'])
        self.assertFalse(result['authorizesNative'])
        self.assertEqual(result['hops'][2], {'uidRelation': 'other', 'effectiveUidClass': 'zero',
            'commandClass': 'other', 'kernelPathClass': 'login', 'pathStatus': 'observed',
            'commandMatchesKernelPath': False, 'snapshotStable': True})
        with patch.object(gate.os, 'getuid', return_value=1001), patch.object(gate, 'process_info', side_effect=[chain[0], *chain]):
            original = gate.gate_observation(gate.identity(chain[0]))
        self.assertFalse(original['verified'])
        self.assertEqual(original['reason'], 'other_uid')
        exported = json.dumps(result)
        for private in ('/synthetic', '/usr/bin/login', gate.TERMINAL_EXECUTABLE, chain[0]['start'], '1001'):
            self.assertNotIn(private, exported)

    def test_denied_unavailable_invalid_path_stops_without_fallback(self):
        for state in ('denied', 'unavailable', 'invalid'):
            result, count = explain([process()], [(state, None)])
            self.assertEqual(result['reason'], 'path_' + state)
            self.assertEqual(count, 1)
            self.assertFalse(result['terminalPathObserved'])
            self.assertEqual(result['hops'][0]['kernelPathClass'], 'unknown')

    def test_snapshot_races_stop_before_following_parent(self):
        for change in ({'pid': 999}, {'ppid': 999}, {'uid': 0}, {'start': 'changed'}, {'command': 'changed'}):
            result, count = explain([process(ppid=124)], [('observed', '/synthetic/python')],
                mutate=lambda calls: calls.__setitem__(2, dict(calls[2], **change)))
            self.assertEqual(result['reason'], 'snapshot_changed')
            self.assertEqual(count, 1)
            self.assertFalse(result['hops'][0]['snapshotStable'])

    def test_cycle_depth_end_and_query_failure_are_closed(self):
        for chain, reason in (([process(ppid=123)], 'cycle'), ([process()], 'end_reached'),
                ([process(pid=123+i, ppid=124+i) for i in range(12)], 'depth_limit')):
            result, count = explain(chain, [('observed', '/synthetic/python')] * len(chain))
            self.assertEqual(result['reason'], reason)
            self.assertEqual(count, len(chain))
            self.assertLessEqual(result['hopCount'], 12)
        for state in ('unavailable', 'invalid'):
            result, count = explain([process()], [], mutate=lambda calls: calls.__setitem__(0, gate.ProcessObservationError(state)))
            self.assertEqual(result['reason'], 'process_' + state)
            self.assertEqual(count, 0)

    def test_unjoined_query_is_not_explanatory_success(self):
        result, count = explain([process()], [], cleanup=False)
        self.assertEqual(result['reason'], 'cleanup_unresolved')
        self.assertFalse(result['queriesJoined'])
        self.assertEqual(count, 0)

    def test_internal_deadline_starts_no_more_queries(self):
        with patch.object(gate.time, 'monotonic', side_effect=[100, 106]), patch.object(gate, 'process_info') as read:
            result = gate.explain_chain(gate.identity(process()))
        self.assertEqual(result['reason'], 'deadline')
        read.assert_not_called()

    def test_libproc_query_has_fixed_library_signature_and_error_classes(self):
        for data, failure, expected in ((b'/usr/bin/login', 0, 'observed'), (None, errno.EPERM, 'denied'),
                (None, errno.EACCES, 'denied'), (None, errno.ESRCH, 'unavailable'),
                (b'relative', 0, 'invalid'), (b'/bad\xff', 0, 'invalid'), (b'/bad\npath', 0, 'invalid')):
            def read(pid, buffer, size):
                self.assertEqual((pid, size), (123, 4096))
                if data is None:
                    return 0
                ctypes.memmove(buffer, data + b'\0', len(data) + 1)
                return len(data)
            library = SimpleNamespace(proc_pidpath=read)
            with patch.object(ctypes, 'CDLL', return_value=library) as load, patch.object(ctypes, 'get_errno', return_value=failure):
                state, path = gate.kernel_path(123)
            self.assertEqual(state, expected)
            self.assertEqual(path, '/usr/bin/login' if expected == 'observed' else None)
            load.assert_called_once_with('/usr/lib/libproc.dylib', use_errno=True)
            self.assertEqual(read.argtypes, (ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32))
        with patch.object(ctypes, 'CDLL', side_effect=OSError('private')):
            self.assertEqual(gate.kernel_path(123), ('unavailable', None))

    def test_parent_watchdog_and_receipt_loss_do_not_prove_query_cleanup(self):
        for captured in (('timeout', -9, b'', b'', True), ('completed', 0, b'', b'', True),
                ('capture_failed', 0, b'{}', b'', True), ('completed', 0, b'{}', b'', False)):
            with patch.object(gate.time, 'monotonic', return_value=100), patch.object(gate, 'capture_owned', return_value=captured):
                result = gate.origin_observation(Path('/synthetic/private'), 135)
            self.assertEqual(result['reason'], 'worker_incomplete')
            self.assertFalse(result['queriesJoined'])
            self.assertFalse(result['terminalPathObserved'])
        with patch.object(gate.time, 'monotonic', return_value=115), patch.object(gate, 'capture_owned') as capture:
            result = gate.origin_observation(Path('/synthetic/private'), 135)
        capture.assert_not_called()
        self.assertEqual(result['reason'], 'deadline')
        self.assertFalse(result['workerJoined'])

    def test_parent_sets_only_its_actual_direct_wait(self):
        result, _ = explain([process(command=gate.TERMINAL_EXECUTABLE)], [('observed', gate.TERMINAL_EXECUTABLE)])
        captured = ('completed', 0, json.dumps(result).encode(), b'', True)
        with patch.object(gate.time, 'monotonic', return_value=100), patch.object(gate, 'capture_owned', return_value=captured) as capture:
            observed = gate.origin_observation(Path('/synthetic/private'), 135)
        self.assertTrue(observed['workerJoined'])
        self.assertFalse(observed['authorizesNative'])
        self.assertEqual(capture.call_args.kwargs, {'timeout': 20, 'limit': 8192})
        self.assertEqual(capture.call_args.args[0][:5], [sys.executable, '-I', '-B', str(gate.SCRIPT), '--observe'])

    def test_duplicate_multiple_and_trailing_child_results_are_incomplete(self):
        value = dict(gate.empty_origin('process_absent'), queriesJoined=True)
        valid = json.dumps(value)
        duplicate = valid.replace('"queriesJoined": true', '"queriesJoined": false, "queriesJoined": true')
        for payload in (duplicate, valid + '\n' + valid, valid + '\ntrailing'):
            with patch.object(gate.time, 'monotonic', return_value=100), patch.object(gate, 'capture_owned',
                    return_value=('completed', 0, payload.encode(), b'', True)):
                result = gate.origin_observation(Path('/synthetic/private'), 135)
            self.assertEqual(result['reason'], 'worker_incomplete')
            self.assertFalse(result['queriesJoined'])

    def test_closed_fields_reject_raw_context_and_contradictory_success(self):
        original, _ = explain([process(command=gate.TERMINAL_EXECUTABLE)], [('observed', gate.TERMINAL_EXECUTABLE)])
        for modify in (lambda v: v.update(rawPid=123), lambda v: v.update(authorizesNative=True),
                lambda v: v.update(hopCount=13), lambda v: v.update(queriesJoined=False),
                lambda v: v['hops'][0].update(rawPath='/synthetic'),
                lambda v: v['hops'][0].update(snapshotStable=False),
                lambda v: v['hops'][0].update(kernelPathClass='other')):
            changed = copy.deepcopy(original)
            modify(changed)
            with self.assertRaises(ValueError):
                gate.validate_origin(changed)

    def test_query_cleanup_callback_observes_the_actual_child_wait(self):
        cleaned = []
        state, code, _, _, joined = gate.capture_owned([sys.executable, '-I', '-c', 'pass'], timeout=2, on_cleanup=cleaned.append)
        self.assertEqual((state, code, joined, cleaned), ('completed', 0, True, [True]))
        process = Mock(returncode=0)
        process.poll.return_value = process.wait.return_value = 0
        reader = Mock()
        reader.is_alive.return_value = True
        cleaned = []
        with patch.object(gate.subprocess, 'Popen', return_value=process), patch.object(gate.threading, 'Thread', return_value=reader):
            state, _, _, _, joined = gate.capture_owned(['synthetic-leaf'], timeout=1, on_cleanup=cleaned.append)
        self.assertEqual((state, joined, cleaned), ('capture_failed', True, [False]))


if __name__ == '__main__':
    unittest.main()
