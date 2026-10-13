"""Synthetic launch/ownership fixtures and network-free process checks only."""
import importlib.util
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import types
import unittest
from contextlib import ExitStack
from unittest.mock import Mock, patch

SCRIPT = Path(__file__).with_name('ci-dnssd-terminal.py')
SPEC = importlib.util.spec_from_file_location('ci_dnssd_terminal', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), gate.__dict__)


def owner(pid=123):
    return {'pid': pid, 'uid': os.getuid(), 'start': 'Tue Oct 13 09:00:00 2026'}


def environment(root):
    return {**{key: '1' for key in gate.OPT_INS}, 'GOMAXPROCS': '2',
            **{key: str(root) for key in ('TMPDIR', 'TMP', 'TEMP')}}


def observation(verified=True):
    return {'verified': verified, 'reason': 'verified' if verified else 'other_uid',
            'identity': {'state': 'observed', 'pidMatched': True, 'uidMatched': True, 'startMatched': True, 'matched': True},
            'hopCount': 1, 'hops': [{'uidRelation': 'same' if verified else 'other', 'commandClass': 'terminal_exact'}]}


def native_observation(verified=True):
    if not verified:
        return gate.empty_origin()
    value = gate.empty_origin('terminal_path')
    value.update(hopCount=4, terminalPathObserved=True, workerJoined=True, queriesJoined=True,
                 supervisorExecutableMatched=True, interpreterReferenceVerified=True, chainRechecked=True)
    value['hops'] = [{'uidRelation': relation, 'effectiveUidClass': uid, 'kernelPathClass': path,
        'commandClass': path, 'pathStatus': 'observed', 'commandMatchesKernelPath': True, 'snapshotStable': True}
        for relation, uid, path in [('same', 'nonzero', 'other'), ('same', 'nonzero', 'shell'),
                                  ('other', 'zero', 'login'), ('same', 'nonzero', 'terminal_exact')]]
    return value


class Clock:
    def __init__(self):
        self.now = 1000

    def monotonic(self):
        return self.now

    def sleep(self, value):
        self.now += value


class TerminalDiagnosticTests(unittest.TestCase):
    def test_direct_children_are_joined_on_normal_timeout_limit_and_denial(self):
        cases = [
            ('completed', 'print("synthetic")', 2, 1024, False),
            ('nonzero', 'raise SystemExit(7)', 2, 1024, False),
            ('timeout', 'import time; time.sleep(5)', 0.05, 1024, False),
            ('output_limit', 'print("x" * 4096)', 2, 32, False),
            ('policy_denied', 'import os,time; os.write(1,b"Policy"); time.sleep(.02); os.write(1,b"Denied"); time.sleep(5)', 2, 1024, True),
            ('policy_denied', 'print(\'{"failureKind": "permission_denied"}\')', 2, 1024, True),
        ]
        for expected, program, timeout, limit, denied in cases:
            with self.subTest(expected=expected):
                state, code, out, err, joined = gate.capture_owned(
                    [sys.executable, '-I', '-B', '-c', program], timeout=timeout, limit=limit,
                    stop_on_denial=denied)
                self.assertEqual(state, expected)
                self.assertIs(type(code), int)
                self.assertTrue(joined)
                self.assertLessEqual(len(out), limit)
                self.assertLessEqual(len(err), limit)

    def test_direct_cancellation_and_launch_failure_never_fabricate_join(self):
        state, code, _, _, joined = gate.capture_owned(
            [sys.executable, '-I', '-c', 'import time; time.sleep(5)'], timeout=2,
            cancelled=lambda: True)
        self.assertEqual(state, 'capture_failed')
        self.assertIs(type(code), int)
        self.assertTrue(joined)
        with patch.object(gate.subprocess, 'Popen', side_effect=OSError('synthetic-private')):
            self.assertEqual(gate.capture_owned(['fixed'], timeout=1),
                             ('launch_failed', None, b'', b'', False))


    def test_joined_child_with_unresolved_pipe_readers_is_not_success(self):
        process = Mock(returncode=0)
        process.poll.return_value = 0
        process.wait.return_value = 0
        reader = Mock()
        reader.is_alive.return_value = True
        with patch.object(gate.subprocess, 'Popen', return_value=process), \
                patch.object(gate.threading, 'Thread', return_value=reader):
            state, code, _, _, joined = gate.capture_owned(['synthetic-leaf'], timeout=1)
        self.assertEqual((state, code, joined), ('capture_failed', 0, True))

    def test_alarm_enters_exact_child_cleanup(self):
        real_popen = subprocess.Popen
        processes = []
        def launch(*args, **kwargs):
            process = real_popen(*args, **kwargs)
            processes.append(process)
            return process
        with patch.object(gate.subprocess, 'Popen', side_effect=launch), \
                patch.object(gate, 'time', types.SimpleNamespace(monotonic=gate.time.monotonic,
                    sleep=lambda _: (_ for _ in ()).throw(TimeoutError('synthetic deadline')))):
            result = gate.capture_owned([sys.executable, '-I', '-c', 'import time; time.sleep(5)'], timeout=5)
            self.assertEqual(result[0], 'capture_failed')
            self.assertTrue(result[4])
        self.assertIsNotNone(processes[0].poll())

    def test_private_records_publish_complete_and_reject_links_or_permissions(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = root / 'result.json'
            gate.write_private(path, {'synthetic': True})
            self.assertEqual(gate.read_private(path), {'synthetic': True})
            self.assertEqual(path.stat().st_nlink, 1)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            with self.assertRaises(ValueError):
                gate.write_private(path, {'synthetic': False})
            self.assertEqual(gate.read_private(path), {'synthetic': True})
            alias = root / 'alias'
            alias.symlink_to(path)
            with self.assertRaises(OSError):
                gate.read_private(alias)
            os.link(path, root / 'link')
            with self.assertRaises(ValueError):
                gate.read_private(path)
            (root / 'link').unlink()
            path.chmod(0o644)
            with self.assertRaises(ValueError):
                gate.read_private(path)

    def test_process_metadata_and_ancestry_do_not_infer_responsible_identity(self):
        raw = b' 123 42 501 Tue Oct 13 09:00:00 2026 /synthetic/python\n'
        with patch.object(gate, 'capture_owned', return_value=('completed', 0, raw, b'', True)):
            info = gate.process_info(123)
            self.assertEqual(info['ppid'], 42)
            self.assertEqual(info['command'], '/synthetic/python')
        with patch.object(gate, 'capture_owned', return_value=('nonzero', 1, b'', b'', True)):
            self.assertIsNone(gate.process_info(123))
        with patch.object(gate, 'capture_owned', return_value=('nonzero', 1, b'', b'PolicyDenied', True)):
            with self.assertRaises(ValueError):
                gate.process_info(123)
        for chain, expected in [
            ([dict(owner(), ppid=42, command='synthetic'), dict(owner(42), ppid=1, command=gate.TERMINAL_EXECUTABLE)], True),
            ([dict(owner(), ppid=123, command='synthetic')], False),
            ([dict(owner(), ppid=1, command='Terminal')], False),
            ([dict(owner(), ppid=1, command=gate.TERMINAL_EXECUTABLE, uid=-1)], False),
        ]:
            with patch.object(gate, 'process_info', side_effect=chain):
                self.assertEqual(gate.terminal_ancestry(123), expected)

    def test_receipt_is_closed_and_cannot_upgrade_unknown_privacy_or_app_cleanup(self):
        value = gate.empty_receipt()
        self.assertEqual(gate.receipt(value), value)
        for changes in ({'responsibleIdentityVerified': True}, {'terminalAppCleanupVerified': True},
                        {'promptState': 'absent'}, {'status': 'observed'}, {'secret': 'synthetic-private'},
                        {'markerVerified': True}, {'nativeOwnerJoined': True}, {'openCompleted': 1},
                        {'nativeExitCode': 0}, {'nativeProcessStatus': 'completed'},
                        {'policyDeniedObserved': True}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                gate.receipt(dict(value, **changes))

    def test_child_gates_once_and_waits_for_actual_native_result(self):
        for failure in (None, 'expired', 'nonce', 'cancelled', 'parent', 'parent-lost', 'hash', 'marker', 'denied', 'permit-timeout'):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                directory = root / 'terminal-launch'
                directory.mkdir(mode=0o700)
                binary = root / 'dnssd.test'
                binary.write_bytes(b'synthetic binary')
                clock, own = Clock(), owner(os.getpid())
                plan = {'markerOnly': False, 'nonce': 'a' * 64, 'parent': owner(77), 'deadline': 1120,
                    'launchDeadline': 999 if failure == 'expired' else 1035,
                    'scriptSha256': gate.digest(gate.SCRIPT),
                    'binarySha256': '0' * 64 if failure == 'hash' else gate.digest(binary), 'env': environment(root)}
                gate.write_private(directory / 'plan.json', plan)
                if failure != 'permit-timeout':
                    gate.write_private(directory / 'permit.json', {'nonce': 'b' * 64 if failure == 'nonce' else plan['nonce'], 'owner': own})
                if failure == 'cancelled':
                    gate.write_private(directory / 'cancelled', {'cancelled': True})
                calls = []
                def capture(command, **kwargs):
                    calls.append((command, kwargs))
                    if tuple(command) == gate.MARKER_COMMAND:
                        return ('nonzero', 1, b'', b'', True) if failure == 'marker' else ('completed', 0, gate.MARKER, b'', True)
                    self.assertEqual(command, gate.native_command(binary))
                    return ('policy_denied', -9, b'PolicyDenied', b'', True) if failure == 'denied' else ('completed', 0, b'synthetic output', b'', True)
                with ExitStack() as stack:
                    stack.enter_context(patch.object(gate, 'time', clock))
                    stack.enter_context(patch.object(gate.signal, 'signal'))
                    alarm = stack.enter_context(patch.object(gate.signal, 'setitimer'))
                    stack.enter_context(patch.object(gate, 'process_info', return_value=own))
                    stack.enter_context(patch.object(gate, 'same_process', side_effect=[True, False] if failure == 'parent-lost' else None, return_value=failure != 'parent'))
                    stack.enter_context(patch.object(gate, 'capture_owned', side_effect=capture))
                    self.assertEqual(gate.child(directory), 0 if failure in (None, 'denied') else 1)
                    self.assertEqual(alarm.call_args.args, (gate.signal.ITIMER_REAL, 0))
                native = [call for call in calls if call[0][0] == str(binary)]
                self.assertEqual(len(native), 1 if failure in (None, 'denied') else 0)
                if native:
                    self.assertEqual(native[0][1]['timeout'], 75)
                    self.assertTrue(native[0][1]['stop_on_denial'])
                    result = gate.read_private(directory / 'result.json')
                    self.assertTrue(result['joined'])
                    self.assertEqual(result['code'], -9 if failure == 'denied' else 0)

    def test_parent_requires_marker_ancestry_and_supervisor_exit_not_open_success(self):
        for failure in (None, 'unavailable', 'open', 'missing', 'marker', 'ancestry', 'denied', 'pipe-unresolved', 'no-result'):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                binary = root / 'dnssd.test'
                binary.write_bytes(b'synthetic binary')
                clock, calls = Clock(), []
                directory = root / 'terminal-launch'
                def launch(command, **kwargs):
                    calls.append(command)
                    self.assertEqual(command[:3], ['/usr/bin/open', '-a', str(gate.TERMINAL)])
                    self.assertEqual(command[3], str(directory / 'diagnostic.command'))
                    plan = gate.read_private(directory / 'plan.json')
                    if failure != 'missing':
                        gate.write_private(directory / 'started.json', {'nonce': plan['nonce'], 'owner': owner()})
                        gate.write_private(directory / 'marker.json', {'nonce': plan['nonce'], 'owner': owner(),
                            'markerVerified': failure != 'marker', 'markerOwnerJoined': True})
                        if failure != 'no-result':
                            for name in ('stdout', 'stderr'):
                                path = directory / name
                                path.write_bytes(b'synthetic-private' if name == 'stdout' else b'')
                                path.chmod(0o600)
                            gate.write_private(directory / 'result.json', {'nonce': plan['nonce'], 'owner': owner(),
                                'state': 'policy_denied' if failure == 'denied' else 'capture_failed' if failure == 'pipe-unresolved' else 'completed',
                                'code': -9 if failure == 'denied' else 0, 'joined': True, 'sourceUnchanged': True})
                    return ('nonzero', 1, b'', b'', True) if failure == 'open' else ('completed', 0, b'', b'', True)
                checked = []
                def same(_owner):
                    checked.append(_owner)
                    return len(checked) == 1 and failure not in {'open', 'marker'}
                with ExitStack() as stack:
                    stack.enter_context(patch.object(gate, 'time', clock))
                    stack.enter_context(patch.object(gate, 'availability', return_value=failure != 'unavailable'))
                    stack.enter_context(patch.object(gate, 'process_info', return_value=owner(os.getpid())))
                    stack.enter_context(patch.object(gate, 'same_process', side_effect=same))
                    stack.enter_context(patch.object(gate, 'origin_observation', return_value=native_observation(failure != 'ancestry')))
                    stack.enter_context(patch.object(gate, 'capture_owned', side_effect=launch))
                    state, code, out, err, result = gate.capture(binary, env=environment(root), private=root)
                self.assertEqual(len(calls), 0 if failure == 'unavailable' else 1)
                self.assertEqual(result['status'], 'observed' if failure is None else 'unavailable' if failure in {'missing', 'unavailable'} else 'failed')
                self.assertEqual(state, 'completed' if failure is None else 'not_run' if failure == 'unavailable' else 'policy_denied' if failure == 'denied' else 'capture_failed')
                self.assertEqual(result['promptState'], 'unobserved')
                self.assertFalse(result['responsibleIdentityVerified'])
                self.assertFalse(result['terminalAppCleanupVerified'])
                self.assertNotIn('synthetic-private', json.dumps(result))
                self.assertNotIn(temporary, json.dumps(result))
                if failure != 'unavailable':
                    self.assertTrue((directory / 'cancelled').exists())
                    command = (directory / 'diagnostic.command').read_text().splitlines()[1]
                    self.assertEqual(shlex.split(command)[1:7], [sys.executable, '-I', '-B', str(gate.SCRIPT), '--child', str(directory)])
                if failure == 'pipe-unresolved':
                    self.assertTrue(result['nativeOwnerJoined'])
                    self.assertEqual(result['nativeProcessStatus'], 'capture_failed')
                    self.assertNotEqual(result['status'], 'observed')
                if failure == 'missing':
                    self.assertGreaterEqual(clock.now, 1130)
                    self.assertFalse(result['supervisorTerminationObserved'])
                if failure in {'open', 'missing', 'marker', 'ancestry'}:
                    self.assertFalse((directory / 'permit.json').exists())


    def test_five_native_entries_check_denial_before_scope_work(self):
        root = SCRIPT.resolve().parents[2]
        files = {
            'dnssd_native_acceptance_test.go': ('LocalOnly', 'SelectedLoopback', 'SelectedInterface'),
            'dnssd_raw_receive_test.go': ('RawReceive',),
            'dnssd_legacy_native_test.go': ('LegacySerialization',),
        }
        for filename, cases in files.items():
            text = (root / 'internal/nearby' / filename).read_text()
            for case in cases:
                entry = 'func TestDNSService' + case + 'Capability(t *testing.T) {\n\trequireDNSServiceBatchPermission(t)\n'
                self.assertIn(entry, text)
            self.assertNotIn('t.Parallel()', text)
        native = (root / 'internal/nearby/dnssd_native_acceptance_test.go').read_text()
        legacy = (root / 'internal/nearby/dnssd_legacy_native_test.go').read_text()
        self.assertLess(native.index('dnssdNativeDiagnosticBatch.observe(r.FailureKind)'), native.index('t.Log("NEARBY_DNSSD_CAPABILITY'))
        self.assertLess(legacy.index('dnssdNativeDiagnosticBatch.observe(result.FailureKind)'), legacy.index('t.Log("NEARBY_DNSSD_LEGACY_CAPABILITY'))
        self.assertEqual(gate.RUNTIME_BYTES, 65536)
        self.assertEqual(gate.RUNTIME_SECONDS, 75)
        self.assertEqual(gate.native_command(Path('/synthetic/dnssd.test'))[1:],
            ['-test.v=test2json', '-test.run=' + gate.SELECTOR, '-test.count=1', '-test.timeout=60s'])

    def test_preexisting_directory_is_never_modified(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'terminal-launch').mkdir()
            with patch.object(gate, 'availability', return_value=True):
                result = gate.capture(root / 'dnssd.test', env=environment(root), private=root)[-1]
            self.assertEqual(result['status'], 'failed')
            self.assertEqual(list((root / 'terminal-launch').iterdir()), [])


if __name__ == '__main__':
    unittest.main()
