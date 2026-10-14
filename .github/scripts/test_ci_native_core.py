"""Deterministic Native timing policy/ownership models; no Go execution."""
import contextlib
import ctypes
from datetime import datetime, timedelta, timezone
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

SCRIPT = Path(__file__).with_name('ci-native-core.py')
SPEC = importlib.util.spec_from_file_location('ci_native_core', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), gate.__dict__)


def event(action, second=0, test=None, **fields):
    value = {'Action': action, 'Package': gate.PACKAGE,
             'Time': (datetime(2026, 1, 1, tzinfo=timezone.utc) + timedelta(seconds=second)).isoformat().replace('+00:00', 'Z')}
    if test is not None:
        value['Test'] = test
    value.update(fields)
    return value


def raw(rows):
    return b''.join(json.dumps(row).encode() + b'\n' for row in rows)


def process(**changes):
    return dict({'process': 'completed', 'returncode': 0, 'ownersJoined': True, 'wallSeconds': 2.0}, **changes)


def success(names=('TestAlpha', 'TestBeta')):
    rows = [event('start')]
    for i, name in enumerate(names):
        rows += [event('run', i, name), event('output', i, name, Output='synthetic-private-path-identity-body\n'),
                 event('pass', i + 1, name, Elapsed=1)]
    return rows + [event('pass', len(names), Elapsed=len(names))]


class TimingTests(unittest.TestCase):
    def test_complete_exhaustive_accounting_and_no_raw_output(self):
        report = gate.timing(raw(success()), ['TestAlpha', 'TestBeta'], process())
        self.assertTrue(report['accountingComplete'])
        self.assertTrue(report['allTestsPassed'])
        self.assertEqual((report['startedCount'], report['terminalCount'], report['passedCount']), (2, 2, 2))
        self.assertEqual(report['tests'][1]['startedSeconds'], 1)
        self.assertEqual(report['tests'][1]['finishedSeconds'], 2)
        self.assertNotIn('synthetic-private', json.dumps(report))
        self.assertNotIn('2026-01-01', json.dumps(report))

    def test_observed_skips_are_complete_but_never_pass_claims(self):
        rows = success()
        rows[3]['Action'] = 'skip'
        report = gate.timing(raw(rows), ['TestAlpha', 'TestBeta'], process())
        self.assertTrue(report['accountingComplete'])
        self.assertFalse(report['allTestsPassed'])
        self.assertEqual(report['skippedCount'], 1)
        self.assertEqual(report['notRunCount'], 0)
        self.assertEqual(report['returncode'], 0)

    def test_parallel_lifecycle_and_subtests_are_separate_and_closed(self):
        rows = [event('start'), event('run', 0, 'TestAlpha'), event('pause', 0, 'TestAlpha'),
                event('run', 0, 'TestBeta'), event('pass', 1, 'TestBeta', Elapsed=1),
                event('cont', 1, 'TestAlpha'), event('run', 1, 'TestAlpha/private-subtest-value'),
                event('skip', 2, 'TestAlpha/private-subtest-value', Elapsed=1),
                event('pass', 2, 'TestAlpha', Elapsed=1), event('pass', 2, Elapsed=2)]
        report = gate.timing(raw(rows), ['TestAlpha', 'TestBeta'], process())
        self.assertTrue(report['accountingComplete'])
        self.assertFalse(report['allTestsPassed'])
        self.assertEqual(report['subtestSkipped'], 1)
        self.assertNotIn('private-subtest-value', json.dumps(report))
        self.assertEqual(report['tests'][0]['pauseCount'], 1)
        self.assertEqual(report['tests'][0]['continueCount'], 1)

    def test_timeout_preserves_prior_progress_running_and_not_run(self):
        rows = [event('start'), event('run', 0, 'TestAlpha'), event('pass', 4, 'TestAlpha', Elapsed=4),
                event('run', 599, 'TestBeta'), event('output', 600, Output='panic: test timed out after 10m0s\n'),
                event('output', 600, Output='synthetic private stack path\n'), event('fail', 600.301, Elapsed=600.301)]
        report = gate.timing(raw(rows), ['TestAlpha', 'TestBeta', 'TestGamma'], process(process='nonzero', returncode=1))
        self.assertTrue(report['eventStreamValid'])
        self.assertFalse(report['accountingComplete'])
        self.assertTrue(report['packageTimeoutObserved'])
        self.assertEqual((report['passedCount'], report['runningCount'], report['notRunCount']), (1, 1, 1))
        self.assertEqual(report['progress'][0], {'throughSeconds': 30, 'started': 1, 'terminal': 1})
        self.assertEqual(report['progress'][-1]['started'], 2)
        self.assertNotIn('private stack', json.dumps(report))

    def test_duplicate_missing_unexpected_and_unordered_events_fail_closed(self):
        good = success()
        candidates = [[], good[1:], good[:-1], good + good,
            good[:2] + [good[1]] + good[2:], [good[1], good[0], *good[2:]],
            good[:-1] + [event('run', 2, 'TestUnexpected'), good[-1]],
            good[:2] + [event('cont', 0, 'TestAlpha')] + good[2:],
            good[:2] + [event('run', 0, 'TestBeta/sub')] + good[2:],
            good[:-1] + [dict(good[-1], Package='synthetic-private-package')]]
        for rows in candidates:
            with self.subTest(rows=rows):
                report = gate.timing(raw(rows), ['TestAlpha', 'TestBeta'], process())
                self.assertFalse(report['allTestsPassed'])
                self.assertFalse(report['accountingComplete'])

    def test_malformed_truncated_duplicate_fields_and_clock_fail_closed(self):
        good = raw(success())
        candidates = [good[:-1], good + b'{\n', good + b'\xff\n', b'[]\n',
            good.replace(b'"Action": "run"', b'"Action": "run", "Action": "pass"', 1),
            good.replace(b'"Elapsed": 1', b'"Elapsed": NaN', 1),
            good.replace(b'2026-01-01T00:00:01Z', b'2025-01-01T00:00:01Z', 1),
            good.replace(b'2026-01-01T00:00:01Z', b'private-time', 1)]
        for candidate in candidates:
            report = gate.timing(candidate, ['TestAlpha', 'TestBeta'], process())
            self.assertFalse(report['allTestsPassed'])
            self.assertFalse(report['eventStreamValid'])
        with patch.object(gate, 'CAPTURE_BYTES', 8):
            self.assertFalse(gate.timing(good, ['TestAlpha', 'TestBeta'], process())['eventStreamValid'])

    def test_zero_exit_cannot_override_failures_cleanup_or_incomplete_subtests(self):
        for change in ({'ownersJoined': False}, {'returncode': 1}, {'process': 'watchdog_timeout'}):
            report = gate.timing(raw(success()), ['TestAlpha', 'TestBeta'], process(**change))
            self.assertFalse(report['allTestsPassed'])
        rows = success()
        rows[3]['Action'] = 'fail'
        self.assertFalse(gate.timing(raw(rows), ['TestAlpha', 'TestBeta'], process())['allTestsPassed'])
        rows = success()
        rows.insert(2, event('run', 0, 'TestAlpha/sub'))
        self.assertFalse(gate.timing(raw(rows), ['TestAlpha', 'TestBeta'], process())['accountingComplete'])

    def test_complete_inventory_includes_every_default_entrypoint_once(self):
        names = [gate.LEASE, gate.LEASE_PROOF, gate.MIXED, 'ExampleValue', 'FuzzValue', 'TestCoreReceiveCrashChild']
        rows = [event('start')] + [event('output', Output=name + '\n') for name in names]
        rows += [event('output', Output='ok  \t' + gate.PACKAGE + '\t0.001s\n'), event('pass', Elapsed=.001)]
        self.assertEqual(gate.inventory(raw(rows)), names)
        for bad in (rows[:-1], rows + rows, rows[:2] + [rows[1]] + rows[2:],
                    rows[:1] + [event('output', Output='synthetic-private-value\n')] + rows[1:],
                    rows[:1] + [event('run', test=gate.LEASE)] + rows[1:]):
            with self.assertRaises(ValueError):
                gate.inventory(raw(bad))

    def test_preparation_then_exactly_one_full_suite(self):
        calls = []
        names = [gate.LEASE, gate.LEASE_PROOF, gate.MIXED]
        def capture(phase, env):
            calls.append(phase)
            return process(), raw(success(names)) if phase == 'suite' else b''
        result = {'preparation': {}, 'runs': {}, 'metrics': []}
        with patch.object(gate, 'capture', side_effect=capture), patch.object(gate, 'inventory', return_value=names), patch.object(gate.metrics, 'append_record'):
            gate.run_diagnostic({}, result)
        self.assertEqual(calls, ['prepare', 'verify', 'modules', 'inventory', 'suite'])
        self.assertTrue(result['runs']['suite']['allTestsPassed'])
        self.assertEqual([row['suite'] for row in result['metrics']], ['native-core-suite'])
        self.assertEqual(result['inventorySha256'], gate.aggregate(names))

    def test_contradictory_failure_events_cannot_complete_timing(self):
        rows = success()
        rows[3]['Action'] = 'fail'
        report = gate.timing(raw(rows), ['TestAlpha', 'TestBeta'], process())
        self.assertTrue(report['accountingComplete'])
        self.assertFalse(gate.execution_complete(report))
        rows[3]['Action'] = 'skip'
        report = gate.timing(raw(rows), ['TestAlpha', 'TestBeta'], process())
        self.assertTrue(gate.execution_complete(report))
        rows = [event('start'), event('run', 0, 'TestAlpha'), event('run', 0, 'TestAlpha/sub'),
                event('fail', 1, 'TestAlpha/sub', Elapsed=1), event('pass', 1, 'TestAlpha', Elapsed=1),
                event('pass', 1, Elapsed=1)]
        self.assertFalse(gate.execution_complete(gate.timing(raw(rows), ['TestAlpha'], process())))

    def test_zero_exit_capture_failures_produce_failed_metrics(self):
        for state in ('output_limit', 'capture_failed', 'cleanup_unresolved'):
            def capture(phase, env):
                if phase == 'suite':
                    return process(process=state), raw(success([gate.LEASE, gate.LEASE_PROOF, gate.MIXED]))
                return process(), b''
            result = {'preparation': {}, 'runs': {}, 'metrics': []}
            with patch.object(gate, 'capture', side_effect=capture), patch.object(gate, 'inventory', return_value=[gate.LEASE, gate.LEASE_PROOF, gate.MIXED]), patch.object(gate.metrics, 'append_record'):
                gate.run_diagnostic({}, result)
            self.assertEqual(result['metrics'][0]['status'], 'failed')
            self.assertEqual(result['metrics'][0]['returncode'], 1)
            self.assertEqual(result['runs']['suite']['returncode'], 0)

    def test_every_terminal_requires_present_bounded_numeric_elapsed(self):
        for index in (3, -1):
            for value in (None, 721, 1e100, True, -1, float('inf')):
                rows = success()
                if value is None:
                    rows[index].pop('Elapsed')
                else:
                    rows[index]['Elapsed'] = value
                report = gate.timing(raw(rows), ['TestAlpha', 'TestBeta'], process())
                self.assertFalse(report['eventStreamValid'])
                self.assertFalse(report['accountingComplete'])
                self.assertFalse(report['allTestsPassed'])

    def test_example_and_fuzz_seed_execution_is_fully_accounted(self):
        names = ['ExampleValue', 'FuzzValue']
        rows = [event('start'), event('run', 0, names[0]), event('pass', 1, names[0], Elapsed=1),
                event('run', 1, names[1]), event('run', 1, names[1] + '/seed#0'),
                event('pass', 2, names[1] + '/seed#0', Elapsed=1), event('pass', 2, names[1], Elapsed=1), event('pass', 2, Elapsed=2)]
        report = gate.timing(raw(rows), names, process())
        self.assertTrue(report['allTestsPassed'])
        self.assertEqual(report['subtestTerminal'], 1)

    def test_unjoined_process_stops_following_commands(self):
        result = {'preparation': {}, 'runs': {}, 'metrics': []}
        with patch.object(gate, 'capture', return_value=(process(ownersJoined=False), b'')) as capture:
            with self.assertRaises(ValueError):
                gate.run_diagnostic({}, result)
        self.assertEqual(capture.call_count, 1)


class OwnershipTests(unittest.TestCase):
    def setup_capture(self, *, fail_assign=False, running=False, active=0, output=b''):
        calls = []
        job = Mock()
        job.assign.side_effect = lambda p: calls.append('assign')
        if fail_assign:
            def denied(p):
                calls.append('assign-denied')
                raise OSError('synthetic private error')
            job.assign.side_effect = denied
        job.active.return_value = active
        job.observe.return_value = None if running else 0
        job.terminate.side_effect = lambda: (calls.append('terminate'), setattr(job.active, 'return_value', 0))
        job.close.side_effect = lambda: calls.append('close')
        class Input(io.BytesIO):
            def write(self, value):
                calls.append('gate')
                return super().write(value)
        child = Mock(stdin=Input(), stdout=io.BytesIO(output), stderr=io.BytesIO())
        child.poll.return_value = None if running or fail_assign else 0
        child.wait.side_effect = lambda **kw: (calls.append('wait'), 1 if running or fail_assign else 0)[1]
        child.kill.side_effect = lambda: (calls.append('kill'), setattr(child.poll, 'return_value', 1))
        return calls, job, child

    def test_x64_windows_abi_sizes_offsets_and_no_breakaway(self):
        self.assertEqual(ctypes.sizeof(gate.DWORD), 4)
        if ctypes.sizeof(gate.SIZE_T) == 8:
            self.assertEqual(ctypes.sizeof(gate.BasicLimits), 64)
            self.assertEqual(ctypes.sizeof(gate.ExtendedLimits), 144)
            self.assertEqual(ctypes.sizeof(gate.Accounting), 48)
            self.assertEqual(gate.Accounting.ActiveProcesses.offset, 40)
            self.assertEqual(gate.BasicLimits.LimitFlags.offset, 16)
        source = SCRIPT.read_text()
        self.assertIn("LimitFlags = 0x2000", source)
        self.assertNotIn('CREATE_BREAKAWAY_FROM_JOB', source)

    def test_assignment_precedes_gate_and_isolated_launcher_is_joined(self):
        calls, job, child = self.setup_capture()
        with patch.object(gate.sys, 'platform', 'win32'), patch.object(gate, 'OwnedJob', return_value=job), patch.object(gate.subprocess, 'Popen', return_value=child) as start:
            result, output = gate.capture('suite', {})
        self.assertTrue(result['ownersJoined'])
        self.assertEqual(result['process'], 'completed')
        self.assertLess(calls.index('assign'), calls.index('gate'))
        self.assertLess(calls.index('wait'), calls.index('close'))
        self.assertEqual(start.call_args.args[0][1:3], ['-I', '-B'])
        self.assertFalse(start.call_args.kwargs['shell'])

    def test_assignment_failure_never_releases_gate_and_joins_child(self):
        calls, job, child = self.setup_capture(fail_assign=True)
        with patch.object(gate.sys, 'platform', 'win32'), patch.object(gate, 'OwnedJob', return_value=job), patch.object(gate.subprocess, 'Popen', return_value=child):
            result, _ = gate.capture('suite', {})
        self.assertNotIn('gate', calls)
        self.assertIn('kill', calls)
        self.assertIn('wait', calls)
        self.assertTrue(result['ownersJoined'])
        self.assertEqual(result['process'], 'capture_failed')

    def test_timeout_terminates_owned_descendants_and_joins(self):
        calls, job, child = self.setup_capture(running=True, active=2)
        with patch.object(gate.sys, 'platform', 'win32'), patch.object(gate, 'OwnedJob', return_value=job), patch.object(gate.subprocess, 'Popen', return_value=child), \
                patch.dict(gate.BOUNDS, {'suite': 0}):
            result, _ = gate.capture('suite', {})
        self.assertEqual(result['process'], 'watchdog_timeout')
        self.assertTrue(result['ownersJoined'])
        self.assertIn('terminate', calls)
        self.assertIn('wait', calls)

    def test_output_limit_remains_private_and_bounded(self):
        calls, job, child = self.setup_capture(output=b'private-output' * 100)
        with patch.object(gate.sys, 'platform', 'win32'), patch.object(gate, 'OwnedJob', return_value=job), patch.object(gate.subprocess, 'Popen', return_value=child), \
                patch.object(gate, 'CAPTURE_BYTES', 20):
            result, output = gate.capture('suite', {})
        self.assertEqual(len(output), 20)
        self.assertEqual(result['process'], 'output_limit')
        self.assertNotIn('private-output', json.dumps(result))

    def test_query_termination_and_wait_failure_are_never_join_proof(self):
        for failure in ('query', 'terminate', 'wait', 'close'):
            calls, job, child = self.setup_capture(running=True, active=2)
            if failure == 'query':
                job.active.side_effect = OSError('private')
            elif failure == 'terminate':
                job.terminate.side_effect = OSError('private')
            elif failure == 'wait':
                child.wait.side_effect = subprocess.TimeoutExpired('private-command', 10)
            else:
                job.close.side_effect = OSError('private')
            with patch.object(gate.sys, 'platform', 'win32'), patch.object(gate, 'OwnedJob', return_value=job), patch.object(gate.subprocess, 'Popen', return_value=child), \
                    patch.dict(gate.BOUNDS, {'suite': 0}):
                result, _ = gate.capture('suite', {})
            self.assertFalse(result['ownersJoined'])
            self.assertEqual(result['process'], 'cleanup_unresolved')

    def test_owned_child_accepts_only_fixed_phase_after_exact_gate(self):
        for phase, data in (('arbitrary', b'G'), ('suite', b''), ('suite', b'X')):
            with patch.object(gate.sys, 'platform', 'win32'), patch.object(gate.sys, 'stdin', Mock(buffer=io.BytesIO(data))), \
                    patch.object(gate.subprocess, 'run') as run:
                self.assertEqual(gate.owned_child(phase), 126)
                run.assert_not_called()
        with patch.object(gate.sys, 'platform', 'win32'), patch.object(gate.sys, 'stdin', Mock(buffer=io.BytesIO(b'G'))), \
                patch.object(gate.subprocess, 'run', return_value=Mock(returncode=7)) as run:
            self.assertEqual(gate.owned_child('suite'), 7)
            self.assertEqual(run.call_args.args[0], gate.COMMANDS['suite'])


class NativeIdentityTests(unittest.TestCase):
    def test_darwin_runtime_requires_version_and_every_nonreaping_primitive(self):
        names = ('waitid', 'getpgid', 'killpg', 'P_PID', 'WEXITED', 'WNOHANG', 'WNOWAIT', 'CLD_EXITED', 'CLD_KILLED', 'CLD_DUMPED')
        with contextlib.ExitStack() as stack:
            stack.enter_context(patch.object(gate.sys, 'platform', 'darwin'))
            for name in names:
                stack.enter_context(patch.object(gate.os, name, Mock() if name in names[:3] else 1, create=True))
            for version in ((3, 13, 0), (3, 14, 1)):
                with patch.object(gate.sys, 'version_info', version):
                    value = gate.runtime_capability()
                    self.assertTrue(value['available'])
                    self.assertEqual(value['pythonVersion'], list(version))
                    self.assertTrue(value['darwinNonReapingWaitRequired'])
                    self.assertEqual(set(value['requiredPrimitives']), set(names))
            with patch.object(gate.sys, 'version_info', (3, 14, 'synthetic-private-version')):
                self.assertIsNone(gate.runtime_capability()['pythonVersion'])
                self.assertFalse(gate.runtime_capability()['available'])
            with patch.object(gate.sys, 'version_info', (3, 12, 9)):
                self.assertFalse(gate.runtime_capability()['available'])
            with patch.object(gate.sys, 'version_info', (3, 14, 1)):
                for name in names:
                    with patch.object(gate.os, name, None):
                        self.assertFalse(gate.runtime_capability()['available'])

    def test_native_targets_require_matching_go_host_target_and_runner(self):
        for key, platform, machine, runner_os, runner_arch in (
                ('windows-amd64', 'win32', 'AMD64', 'Windows', 'X64'),
                ('darwin-arm64', 'darwin', 'arm64', 'macOS', 'ARM64')):
            goos, goarch = key.split('-')
            versions = {'GOVERSION': 'go1.27.1', 'GOHOSTOS': goos, 'GOHOSTARCH': goarch,
                        'GOOS': goos, 'GOARCH': goarch, 'CGO_ENABLED': '1'}
            with patch.dict(os.environ, {'RUNNER_OS': runner_os, 'RUNNER_ARCH': runner_arch}, clear=True):
                self.assertEqual(gate.native.identities(key, versions, host_platform=platform, machine=machine)['goos'], goos)
                for field, value in (('GOOS', 'linux'), ('GOHOSTOS', 'linux'), ('GOARCH', '386'),
                                     ('GOHOSTARCH', '386'), ('GOVERSION', 'go1.27.0'), ('CGO_ENABLED', '0')):
                    with self.subTest(key=key, field=field), self.assertRaises(ValueError):
                        gate.native.identities(key, dict(versions, **{field: value}), host_platform=platform, machine=machine)
                for host, arch in (('linux', machine), (platform, 'riscv64')):
                    with self.assertRaises(ValueError):
                        gate.native.identities(key, versions, host_platform=host, machine=arch)
                with patch.dict(os.environ, {'RUNNER_ARCH': 'mismatch'}), self.assertRaises(ValueError):
                    gate.native.identities(key, versions, host_platform=platform, machine=machine)


class PosixOwnershipTests(unittest.TestCase):
    setup_capture = OwnershipTests.setup_capture
    # Model the POSIX adapter independently of Windows Job Object guarantees.
    def test_new_session_precedes_gate_and_direct_child_is_waited(self):
        calls, group, child = self.setup_capture()
        with patch.object(gate.sys, 'platform', 'darwin'), patch.object(gate, 'OwnedGroup', return_value=group), patch.object(gate.subprocess, 'Popen', return_value=child) as start:
            result, _ = gate.capture('suite', {})
        self.assertTrue(start.call_args.kwargs['start_new_session'])
        self.assertLess(calls.index('assign'), calls.index('gate'))
        self.assertIn('wait', calls)
        self.assertTrue(result['ownersJoined'])

    def test_group_binding_query_and_termination_are_exact_and_fail_closed(self):
        with patch.object(gate.signal, 'SIGKILL', 9, create=True), patch.object(gate.os, 'waitid', create=True), patch.object(gate.os, 'P_PID', 1, create=True), patch.object(gate.os, 'WEXITED', 4, create=True), patch.object(gate.os, 'WNOHANG', 1, create=True), patch.object(gate.os, 'WNOWAIT', 16777216, create=True):
            self.check_group_binding()

    def check_group_binding(self):
        group = gate.OwnedGroup()
        child = Mock(pid=123456)
        with patch.object(gate.os, 'getpgid', return_value=123456, create=True):
            group.assign(child)
        with patch.object(group, 'observe', return_value=None), patch.object(gate.os, 'killpg', create=True) as kill:
            self.assertEqual(group.active(), 1)
            group.terminate()
        self.assertEqual(kill.call_args_list[0].args, (123456, 0))
        self.assertEqual(kill.call_args_list[1].args, (123456, gate.signal.SIGKILL))
        group.signals_closed = False
        with patch.object(group, 'observe', return_value=None), patch.object(gate.os, 'killpg', side_effect=ProcessLookupError, create=True):
            self.assertEqual(group.active(), 0)
            group.terminate()
        group.signals_closed = False
        with patch.object(group, 'observe', return_value=None), patch.object(gate.os, 'killpg', side_effect=PermissionError, create=True):
            with self.assertRaises(OSError):
                group.active()
            with self.assertRaises(OSError):
                group.terminate()
        with patch.object(gate.os, 'getpgid', return_value=123457, create=True), self.assertRaises(OSError):
            group.assign(child)

    def test_unresolved_group_never_becomes_cleanup_proof(self):
        calls, group, child = self.setup_capture(active=1)
        group.active.side_effect = OSError('synthetic-private-group-query')
        with patch.object(gate.sys, 'platform', 'darwin'), patch.object(gate, 'OwnedGroup', return_value=group), patch.object(gate.subprocess, 'Popen', return_value=child):
            report, _ = gate.capture('suite', {})
        self.assertFalse(report['ownersJoined'])
        self.assertEqual(report['process'], 'cleanup_unresolved')
        self.assertNotIn('synthetic-private-group-query', json.dumps(report))


class PosixExitCleanupTests(unittest.TestCase):
    setup_capture = OwnershipTests.setup_capture

    @contextlib.contextmanager
    def fixture(self, observations, *, active=0, read_error=False, signal_error=False):
        calls, _, child = self.setup_capture()
        group = object.__new__(gate.OwnedGroup)
        group.pgid, group.process, group.signals_closed = 123456, child, False
        group.assign = Mock(side_effect=lambda process: calls.append('assign'))
        group.observe = Mock(side_effect=observations)
        def query():
            calls.append('query')
            if isinstance(active, Exception):
                raise active
            return active
        group.active = Mock(side_effect=query)
        child.poll = Mock(side_effect=AssertionError('reaping poll forbidden'))
        if read_error:
            child.stdout = Mock()
            child.stdout.read.side_effect = OSError('synthetic-private-reader-error')
        def signal(pgid, signum):
            self.assertFalse(group.signals_closed)
            self.assertNotIn('wait', calls)
            calls.append('signal')
            if signal_error:
                raise PermissionError('synthetic-private-signal-error')
        with patch.object(gate.sys, 'platform', 'darwin'), patch.object(gate, 'OwnedGroup', return_value=group), patch.object(gate.subprocess, 'Popen', return_value=child), patch.object(gate.os, 'killpg', side_effect=signal, create=True) as kill, patch.object(gate.signal, 'SIGKILL', 9, create=True):
            yield calls, group, child, kill

    def test_normal_exit_withholds_signals_and_requires_wait_before_absence(self):
        with self.fixture([0, 0]) as (calls, group, child, kill):
            report, _ = gate.capture('suite', {})
            kill.assert_not_called()
        self.assertEqual(report['process'], 'completed')
        self.assertTrue(report['ownersJoined'])
        self.assertTrue(report['groupAbsenceObservedAfterWait'])
        self.assertTrue(group.signals_closed)
        self.assertLess(calls.index('wait'), calls.index('query'))
        child.poll.assert_not_called()

    def test_exit_just_before_signal_keeps_watchdog_failure_without_signal(self):
        with self.fixture([None, 0]) as (calls, group, child, kill), patch.dict(gate.BOUNDS, {'suite': 0}):
            report, _ = gate.capture('suite', {})
            kill.assert_not_called()
        self.assertEqual(report['process'], 'watchdog_timeout')
        self.assertEqual(report['executionState'], 'watchdog_timeout')
        self.assertTrue(report['ownersJoined'])
        self.assertTrue(group.signals_closed)

    def test_live_timeout_signals_once_before_fence_and_wait(self):
        with self.fixture([None, None]) as (calls, group, child, kill), patch.dict(gate.BOUNDS, {'suite': 0}):
            report, _ = gate.capture('suite', {})
            self.assertEqual(kill.call_count, 1)
            with self.assertRaises(OSError):
                group.terminate()
            self.assertEqual(kill.call_count, 1)
        self.assertEqual(report['process'], 'watchdog_timeout')
        self.assertTrue(group.signals_closed)
        self.assertLess(calls.index('signal'), calls.index('wait'))

    def test_residual_or_reused_group_after_wait_is_failure_without_signal(self):
        ticks = iter(range(0, 1000, 20))
        with self.fixture([0, 0], active=1) as (calls, group, child, kill), patch.object(gate.time, 'monotonic', side_effect=lambda: next(ticks)):
            report, _ = gate.capture('suite', {})
            kill.assert_not_called()
        self.assertEqual(report['process'], 'cleanup_unresolved')
        self.assertEqual(report['executionState'], 'completed')
        self.assertFalse(report['ownersJoined'])
        self.assertFalse(report['groupAbsenceObservedAfterWait'])
        self.assertTrue(group.signals_closed)

    def test_group_query_errors_never_become_absence(self):
        for error in (PermissionError('synthetic-private-query-denied'), OSError('synthetic-private-query-error')):
            with self.fixture([0, 0], active=error) as (calls, group, child, kill):
                report, _ = gate.capture('suite', {})
                kill.assert_not_called()
            self.assertEqual(report['process'], 'cleanup_unresolved')
            self.assertFalse(report['ownersJoined'])
            self.assertIsNone(report['groupAbsenceObservedAfterWait'])
            self.assertNotIn('synthetic-private', json.dumps(report))

    def test_live_signal_permission_failure_is_retained_without_retry(self):
        with self.fixture([None, None], signal_error=True) as (calls, group, child, kill), patch.dict(gate.BOUNDS, {'suite': 0}):
            report, _ = gate.capture('suite', {})
            self.assertEqual(kill.call_count, 1)
            with self.assertRaises(OSError):
                group.terminate()
            self.assertEqual(kill.call_count, 1)
        self.assertEqual(report['process'], 'cleanup_unresolved')
        self.assertEqual(report['executionState'], 'watchdog_timeout')
        self.assertFalse(report['ownersJoined'])
        self.assertTrue(group.signals_closed)
        self.assertNotIn('synthetic-private', json.dumps(report))

    def test_reader_error_prevents_completion_even_with_absent_group(self):
        with self.fixture([0, 0], read_error=True) as (calls, group, child, kill):
            report, _ = gate.capture('suite', {})
            kill.assert_not_called()
        self.assertEqual(report['process'], 'capture_failed')
        self.assertTrue(report['groupAbsenceObservedAfterWait'])
        self.assertNotIn('synthetic-private', json.dumps(report))


@unittest.skipUnless(os.name == 'posix' and hasattr(os, 'waitid'), 'real POSIX waitid required')
class RealPosixOwnershipTests(unittest.TestCase):
    def test_socket_free_exit_child_wait_and_timeout_never_signal_after_reap(self):
        # This host's process semantics only; passing on Linux is not Darwin proof.
        real_start, real_killpg = subprocess.Popen, os.killpg
        cases = [
            ('print("synthetic-complete")', 2, 'completed'),
            ('subprocess.run([sys.executable, "-I", "-c", "pass"], check=True)', 2, 'completed'),
            ('time.sleep(60)', .05, 'watchdog_timeout'),
        ]
        for body, bound, expected in cases:
            children, signals = [], []
            def start(command, **kwargs):
                program = 'import subprocess,sys,time\nassert sys.stdin.buffer.read(1) == b"G"\n' + body
                child = real_start([gate.sys.executable, '-I', '-c', program], **kwargs)
                child.poll = Mock(side_effect=AssertionError('reaping poll forbidden'))
                children.append(child)
                return child
            def killpg(pgid, signum):
                if signum:
                    self.assertIsNone(children[0].returncode)
                signals.append(signum)
                return real_killpg(pgid, signum)
            with self.subTest(expected=expected, body=body), patch.object(gate.sys, 'platform', 'darwin'), patch.object(gate.subprocess, 'Popen', side_effect=start), patch.object(gate.os, 'killpg', side_effect=killpg), patch.dict(gate.BOUNDS, {'suite': bound}):
                report, _ = gate.capture('suite', dict(os.environ))
            self.assertEqual(report['process'], expected)
            self.assertEqual(report['executionState'], expected)
            self.assertTrue(report['ownersJoined'])
            self.assertTrue(report['groupAbsenceObservedAfterWait'])
            self.assertEqual(gate.signal.SIGKILL in signals, expected == 'watchdog_timeout')
            self.assertIsNotNone(children[0].returncode)
            children[0].poll.assert_not_called()

    def test_retained_leader_nonreaping_observation_and_signal_fence(self):
        child = subprocess.Popen([gate.sys.executable, '-I', '-c', 'pass'], start_new_session=True)
        group = gate.OwnedGroup()
        group.assign(child)
        try:
            deadline = gate.time.monotonic() + 2
            while group.observe() is None and gate.time.monotonic() < deadline:
                gate.time.sleep(.01)
            self.assertEqual(group.observe(), 0)
            self.assertIsNone(child.returncode)
            with patch.object(gate.os, 'killpg') as kill:
                group.terminate()
            kill.assert_not_called()
            self.assertTrue(group.signals_closed)
            self.assertEqual(child.wait(timeout=2), 0)
            with patch.object(gate.os, 'killpg') as kill, self.assertRaises(OSError):
                group.terminate()
            kill.assert_not_called()
        finally:
            # This literal pass child needs no signal. Preserve an earlier
            # failure while always fencing before its final direct wait.
            group.finish_signals()
            child.wait(timeout=2)

    def test_lost_child_and_missing_nonreaping_support_prohibit_signals(self):
        group = gate.OwnedGroup()
        group.process, group.pgid = Mock(pid=123456), 123456
        with patch.object(gate.os, 'waitid', side_effect=ChildProcessError), patch.object(gate.os, 'killpg') as kill, self.assertRaises(OSError):
            group.terminate()
        kill.assert_not_called()
        with patch.object(gate, 'hasattr', return_value=False, create=True), self.assertRaises(OSError):
            gate.OwnedGroup()


class MainReceiptTests(unittest.TestCase):
    @contextlib.contextmanager
    def fixture(self):
        # Model hosted metadata only. Filesystem behavior remains the current
        # host's real pathlib/tempfile behavior, never a forged WindowsPath.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            event_path, output_path = root / 'event.json', root / 'outputs.txt'
            sha = 'a' * 40
            ref = 'diagnostic/native-core/' + sha[:12] + '-attempt1'
            event_path.write_text(json.dumps({'ref_type': 'branch', 'ref': ref,
                'repository': {'full_name': 'webkaz-labs/sobalink', 'private': False}}))
            env = {'RUNNER_TEMP': directory, 'GITHUB_ACTIONS': 'true', 'GITHUB_EVENT_NAME': 'create',
                'GITHUB_EVENT_PATH': str(event_path), 'GITHUB_OUTPUT': str(output_path),
                'GITHUB_REPOSITORY': 'webkaz-labs/sobalink', 'GITHUB_REF': 'refs/heads/' + ref,
                'GITHUB_SHA': sha, 'SOBALINK_CORE_NATIVE_PLATFORM': 'windows-amd64', 'GOTOOLCHAIN': 'local', 'GOWORK': 'off', 'GOFLAGS': gate.GOFLAGS}
            def checked(command):
                if command == ['git', 'rev-parse', 'HEAD']:
                    return sha.encode()
                if command == ['git', 'rev-parse', 'HEAD^{tree}']:
                    return ('b' * 40).encode()
                return b'{}'
            original_module = gate.module
            def module(name):
                return Mock(select=Mock(return_value='native-core')) if name == 'ci-diagnostic' else original_module(name)
            with patch.object(gate, 'module', side_effect=module), patch.dict(os.environ, env, clear=True), patch.object(gate.sys, 'platform', 'win32'), patch.object(gate, 'checked', side_effect=checked), patch.object(gate.native, 'identities', return_value={'go': 'go1.27.1', 'goos': 'windows', 'goarch': 'amd64', 'runner': 'windows-2025', 'race': True}), patch.object(gate.shutil, 'which', return_value='/synthetic/go'), patch.object(gate.native, 'digest', return_value='f' * 64), patch.object(gate, 'source_inventory', return_value={name: 'c' * 64 for name in gate.POLICY_FILES}) as source, contextlib.redirect_stdout(io.StringIO()):
                yield root, output_path, source

    @staticmethod
    def completed(env, result):
        result['inventorySha256'] = gate.aggregate([gate.LEASE, gate.LEASE_PROOF, gate.MIXED])
        result['preparation'] = {phase: process() for phase in ('prepare', 'verify', 'modules', 'inventory')}
        for phase, names in (('suite', [gate.LEASE, gate.LEASE_PROOF, gate.MIXED]),):
            result['runs'][phase] = gate.timing(raw(success(names)), names, process())

    def test_success_receipt_is_source_verified_cleaned_and_not_release_credit(self):
        with self.fixture() as (root, output, source), patch.object(gate, 'run_diagnostic', side_effect=self.completed):
            self.assertEqual(gate.main([]), 0)
            report = json.loads((root / 'soba-native-core-sanitized/result.json').read_text())
            self.assertTrue(report['sourceVerified'])
            self.assertTrue(report['cleanupComplete'])
            self.assertTrue(report['timingComplete'])
            self.assertFalse(report['releaseValidation'])
            self.assertEqual(output.read_text(), 'sanitized_ready=true\n')
            self.assertEqual(list(root.glob('soba-native-core-private-*')), [])
            self.assertNotIn(str(root), json.dumps(report))

    def test_missing_substituted_inventory_or_preparation_never_completes(self):
        for mode in ('inventory', 'row', 'preparation', 'unexpected-run', 'count'):
            with self.fixture() as (root, output, source):
                def run(env, result):
                    self.completed(env, result)
                    if mode == 'inventory':
                        result.pop('inventorySha256')
                    elif mode == 'row':
                        result['runs']['suite']['tests'][0]['test'] = 'TestSubstituted'
                    elif mode == 'preparation':
                        result['preparation'].pop('inventory')
                    elif mode == 'unexpected-run':
                        result['runs']['other'] = result['runs']['suite']
                    else:
                        result['runs']['suite']['expectedCount'] += 1
                with patch.object(gate, 'run_diagnostic', side_effect=run):
                    self.assertEqual(gate.main([]), 1)
                report = json.loads((root / 'soba-native-core-sanitized/result.json').read_text())
                self.assertFalse(report['timingComplete'])

    def test_unavailable_runtime_is_recorded_before_any_process_launch(self):
        capability = {'pythonVersion': [3, 12, 0], 'darwinMinimumPython': [3, 13, 0],
                      'darwinNonReapingWaitRequired': True, 'requiredPrimitives': {'waitid': False}, 'available': False}
        with self.fixture() as (root, output, source), patch.object(gate, 'runtime_capability', return_value=capability), patch.object(gate, 'checked') as checked, patch.object(gate, 'capture') as capture:
            self.assertEqual(gate.main([]), 1)
            checked.assert_not_called()
            capture.assert_not_called()
            report = json.loads((root / 'soba-native-core-sanitized/result.json').read_text())
            provenance = json.loads((root / 'soba-native-core-sanitized/provenance.json').read_text())
            self.assertEqual(report['phase'], 'runtime-unavailable')
            self.assertFalse(report['timingComplete'])
            self.assertEqual(provenance['collectorRuntime'], capability)
            self.assertEqual(provenance['requestedCommit'], 'a' * 40)
            self.assertFalse(report['sourceVerified'])
            for field in ('commit', 'tree', 'sourceSha256'):
                self.assertIsNone(provenance[field])

    def test_missing_policy_hash_and_changed_toolchain_never_complete(self):
        for mode in ('policy', 'toolchain'):
            with self.fixture() as (root, output, source), patch.object(gate, 'run_diagnostic', side_effect=self.completed) as run:
                if mode == 'policy':
                    source.return_value = {'fixture': 'c' * 64}
                    self.assertEqual(gate.main([]), 1)
                    run.assert_not_called()
                else:
                    with patch.object(gate.native, 'digest', side_effect=['f' * 64, 'e' * 64]):
                        self.assertEqual(gate.main([]), 1)
                report = json.loads((root / 'soba-native-core-sanitized/result.json').read_text())
                self.assertFalse(report['timingComplete'])
                self.assertFalse(report['sourceVerified'])

    def test_preflight_failure_never_claims_source_or_runtime_proof(self):
        with self.fixture() as (root, output, source), patch.object(gate, 'run_diagnostic', side_effect=ValueError('synthetic-private-exception')):
            self.assertEqual(gate.main([]), 1)
            report = json.loads((root / 'soba-native-core-sanitized/result.json').read_text())
            self.assertFalse(report['sourceVerified'])
            self.assertFalse(report['timingComplete'])
            self.assertEqual(report['runs'], {})
            self.assertNotIn('synthetic-private-exception', json.dumps(report))

    def test_source_drift_unjoined_owners_and_contradictions_cannot_complete(self):
        for mode in ('source', 'join', 'failure'):
            with self.fixture() as (root, output, source):
                def run(env, result):
                    self.completed(env, result)
                    if mode == 'join':
                        result['runs']['suite']['ownersJoined'] = False
                    if mode == 'failure':
                        result['runs']['suite']['failedCount'] = 1
                if mode == 'source':
                    source.side_effect = [{name: 'c' * 64 for name in gate.POLICY_FILES}, {name: 'e' * 64 for name in gate.POLICY_FILES}]
                with patch.object(gate, 'run_diagnostic', side_effect=run):
                    self.assertEqual(gate.main([]), 1)
                report = json.loads((root / 'soba-native-core-sanitized/result.json').read_text())
                self.assertFalse(report['timingComplete'])
                if mode == 'source':
                    self.assertFalse(report['sourceVerified'])
                if mode == 'join':
                    self.assertFalse(report['cleanupComplete'])
                    self.assertTrue(list(root.glob('soba-native-core-private-*')))

    def test_capture_exception_retains_unknown_ownership_and_stops_cleanup(self):
        with self.fixture() as (root, output, source), patch.object(gate, 'capture', side_effect=RuntimeError('synthetic-private-capture-failure')) as capture:
            self.assertEqual(gate.main([]), 1)
            capture.assert_called_once()
            report = json.loads((root / 'soba-native-core-sanitized/result.json').read_text())
            self.assertEqual(report['ownersState'], 'in_flight')
            self.assertFalse(report['sourceVerified'])
            self.assertFalse(report['cleanupComplete'])
            self.assertFalse(report['timingComplete'])
            self.assertEqual(report['preparation'], {})
            self.assertTrue(list(root.glob('soba-native-core-private-*')))
            self.assertNotIn('synthetic-private-capture-failure', json.dumps(report))

    def test_existing_export_directory_is_never_overwritten(self):
        with self.fixture() as (root, output, source):
            export = root / 'soba-native-core-sanitized'
            export.mkdir()
            target = export / 'result.json'
            target.write_text('existing')
            self.assertEqual(gate.main([]), 1)
            self.assertEqual(target.read_text(), 'existing')
            self.assertFalse(output.exists())


class WorkflowPolicyTests(unittest.TestCase):
    def test_exact_required_environment_keeps_diagnostic_route_separate(self):
        normal = (gate.ROOT / '.github/workflows/ci.yml').read_text()
        diagnostic = (gate.ROOT / '.github/workflows/activation-diagnostic.yml').read_text()
        self.assertNotIn('ci-native-core-required.py', diagnostic)
        self.assertEqual(normal.count('python -B .github/scripts/ci-native-core-required.py core'), 1)
        self.assertIn('GOFLAGS: ' + gate.GOFLAGS, normal)
        self.assertIn('GOFLAGS: ' + gate.GOFLAGS, diagnostic)
        self.assertEqual(gate.COMMANDS['suite'], ['go', 'test', '-race', '-count=1', '-json', '-timeout=10m', './internal/core'])
        for phase in ('suite', 'inventory'):
            self.assertIn('-race', gate.COMMANDS[phase])
            self.assertIn('-count=1', gate.COMMANDS[phase])
            self.assertIn('-timeout=10m', gate.COMMANDS[phase])
            for forbidden in ('-short', '-parallel', '-p=', '-skip', '-shuffle'):
                self.assertFalse(any(value.startswith(forbidden) for value in gate.COMMANDS[phase]))
        self.assertEqual(set(gate.COMMANDS), {'prepare', 'verify', 'modules', 'inventory', 'suite'})
        self.assertFalse(any(value.startswith('-run') for command in gate.COMMANDS.values() for value in command))


if __name__ == '__main__':
    unittest.main()
