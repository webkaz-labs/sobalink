"""Socket-free classification and marker-only authorization boundary tests."""
import copy
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from contextlib import ExitStack
from unittest.mock import patch

from test_ci_dnssd_terminal import gate, owner, observation, Clock, environment

ENTRY = Path(__file__).with_name('ci-dnssd-terminal-marker.py')
namespace = {'__file__': str(ENTRY), '__name__': 'marker_entry'}
exec(compile(ENTRY.read_bytes(), str(ENTRY), 'exec'), namespace)


def process(pid=123, ppid=1, command='/bin/sh', **changes):
    return dict(owner(pid), ppid=ppid, command=command, **changes)


class TerminalMarkerTests(unittest.TestCase):
    def test_all_ancestry_reasons_preserve_the_existing_gate(self):
        first = process()
        deep = [process(pid=123 + i, ppid=124 + i) for i in range(12)]
        cases = [
            ('verified', [process(ppid=42), process(42, command=gate.TERMINAL_EXECUTABLE)]),
            ('end_reached', [process()]),
            ('end_reached', [process(command='/synthetic/Terminal')]),
            ('other_uid', [dict(process(command=gate.TERMINAL_EXECUTABLE), uid=-1)]),
            ('cycle', [process(ppid=123)]),
            ('depth_limit', deep),
            ('ancestor_absent', [None]),
            ('ancestor_unavailable', [gate.ProcessObservationError('unavailable')]),
            ('ancestor_invalid', [gate.ProcessObservationError('invalid')]),
        ]
        for reason, chain in cases:
            with self.subTest(reason=reason, chain=chain):
                with patch.object(gate, 'process_info', side_effect=[first, *chain]):
                    result = gate.gate_observation(owner())
                self.assertEqual(result['reason'], reason)
                self.assertEqual(gate.validate_observation(result), result)
                self.assertLessEqual(result['hopCount'], 12)
                with patch.object(gate, 'process_info', side_effect=[first, *chain]):
                    try:
                        original = gate.same_process(owner()) and gate.terminal_ancestry(123)
                    except gate.ProcessObservationError:
                        original = False
                self.assertEqual(result['verified'], original)
                text = json.dumps(result)
                for private in ('/synthetic', '/bin/sh', gate.TERMINAL_EXECUTABLE, owner()['start']):
                    self.assertNotIn(private, text)
        with patch.object(gate, 'process_info', side_effect=[first, process(command='/synthetic/Terminal')]):
            result = gate.gate_observation(owner())
        self.assertEqual(result['hops'][0]['commandClass'], 'terminal_other_path')
        self.assertFalse(result['verified'])

    def test_identity_absence_mismatch_and_observation_failure_are_distinct(self):
        cases = [
            (None, 'identity_absent', 'absent'),
            (dict(process(), start='different'), 'identity_mismatch', 'observed'),
            (dict(process(), uid=-1), 'identity_mismatch', 'observed'),
            (gate.ProcessObservationError('unavailable'), 'identity_unavailable', 'unavailable'),
            (gate.ProcessObservationError('invalid'), 'identity_invalid', 'invalid'),
        ]
        for current, reason, state in cases:
            with self.subTest(reason=reason), patch.object(gate, 'process_info', side_effect=[current]) as read:
                result = gate.gate_observation(owner())
            self.assertEqual(result['reason'], reason)
            self.assertEqual(result['identity']['state'], state)
            self.assertEqual(result['hopCount'], 0)
            self.assertFalse(result['verified'])
            self.assertEqual(read.call_count, 1)
            gate.validate_observation(result)

    def test_command_classes_are_fixed_and_never_grant_other_paths_or_uids(self):
        for command, expected in [(gate.TERMINAL_EXECUTABLE, 'terminal_exact'),
                ('/synthetic/Terminal', 'terminal_other_path'), ('Terminal', 'terminal_other_path'),
                ('/usr/bin/login', 'login'), ('/bin/zsh', 'shell'),
                ('/synthetic/private-program', 'other')]:
            self.assertEqual(gate.command_class(command), expected)
        with patch.object(gate, 'process_info', side_effect=[process(), dict(process(command='/usr/bin/login'), uid=-1)]):
            result = gate.gate_observation(owner())
        self.assertEqual(result['reason'], 'other_uid')
        self.assertEqual(result['hops'], [{'uidRelation': 'other', 'commandClass': 'login'}])
        self.assertFalse(result['verified'])

    def test_closed_observation_rejects_raw_context_and_false_success(self):
        baseline = observation()
        for modify in (
            lambda value: value.update(rawPath='/synthetic/private'),
            lambda value: value.update(reason='synthetic-private'),
            lambda value: value.update(hopCount=13),
            lambda value: value['identity'].update(pid=123),
            lambda value: value['identity'].update(state='absent'),
            lambda value: value['hops'][0].update(commandClass='/synthetic/private'),
            lambda value: value['hops'][0].update(uidRelation=501),
            lambda value: value['hops'][0].update(commandClass='terminal_other_path'),
        ):
            value = copy.deepcopy(baseline)
            modify(value)
            with self.assertRaises(ValueError):
                gate.validate_observation(value)

    def test_marker_only_child_never_reads_binary_or_accepts_network_permit(self):
        for kind in ('acknowledged', 'ack_missing', 'ack_wrong_nonce', 'network_permit_only', 'mixed_native_env'):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as temporary:
                root, clock = Path(temporary), Clock()
                directory = root / 'terminal-launch'
                directory.mkdir(mode=0o700)
                own = owner(os.getpid())
                plan = {'markerOnly': True, 'nonce': 'a' * 64, 'parent': owner(77),
                    'deadline': 1120, 'launchDeadline': 1035, 'scriptSha256': gate.digest(gate.SCRIPT),
                    'binarySha256': None, 'env': environment(root) if kind == 'mixed_native_env' else {}}
                gate.write_private(directory / 'plan.json', plan)
                if kind in {'acknowledged', 'ack_wrong_nonce'}:
                    gate.write_private(directory / 'marker-ack.json', {'nonce': 'b' * 64 if kind == 'ack_wrong_nonce' else plan['nonce'], 'owner': own})
                if kind == 'network_permit_only':
                    gate.write_private(directory / 'permit.json', {'nonce': plan['nonce'], 'owner': own})
                commands = []
                def capture(command, **kwargs):
                    self.assertEqual(tuple(command), gate.MARKER_COMMAND)
                    commands.append(command)
                    return 'completed', 0, gate.MARKER, b'', True
                original_digest = gate.digest
                def digest(path):
                    self.assertEqual(path, gate.SCRIPT)
                    return original_digest(path)
                with ExitStack() as stack:
                    stack.enter_context(patch.object(gate, 'time', clock))
                    stack.enter_context(patch.object(gate.signal, 'signal'))
                    stack.enter_context(patch.object(gate.signal, 'setitimer'))
                    stack.enter_context(patch.object(gate, 'process_info', return_value=own))
                    stack.enter_context(patch.object(gate, 'same_process', return_value=True))
                    stack.enter_context(patch.object(gate, 'capture_owned', side_effect=capture))
                    stack.enter_context(patch.object(gate, 'digest', side_effect=digest))
                    stack.enter_context(patch.object(gate, 'native_command', side_effect=AssertionError('native forbidden')))
                    self.assertEqual(gate.child(directory), 0 if kind == 'acknowledged' else 1)
                self.assertEqual(len(commands), 0 if kind == 'mixed_native_env' else 1)
                self.assertFalse((directory / 'result.json').exists())
                self.assertFalse((root / 'dnssd.test').exists())

    def test_marker_only_parent_acknowledges_exit_without_a_network_permit(self):
        for verified in (True, False, None):
            with self.subTest(verified=verified), tempfile.TemporaryDirectory() as temporary:
                root, clock = Path(temporary), Clock()
                directory = root / 'terminal-launch'
                def launch(command, **kwargs):
                    self.assertEqual(command[:3], ['/usr/bin/open', '-a', str(gate.TERMINAL)])
                    plan = gate.read_private(directory / 'plan.json')
                    self.assertTrue(plan['markerOnly'])
                    self.assertEqual(plan['env'], {})
                    self.assertIsNone(plan['binarySha256'])
                    gate.write_private(directory / 'marker.json', {'nonce': plan['nonce'], 'owner': owner(), 'markerVerified': True, 'markerOwnerJoined': True})
                    return 'completed', 0, b'', b'', True
                with ExitStack() as stack:
                    stack.enter_context(patch.object(gate, 'time', clock))
                    stack.enter_context(patch.object(gate, 'availability', return_value=True))
                    stack.enter_context(patch.object(gate, 'process_info', return_value=owner(os.getpid())))
                    observed = observation(bool(verified))
                    if verified is None:
                        observed.update(reason='ancestor_unavailable', hops=[{'uidRelation': 'unknown', 'commandClass': 'unknown'}])
                    stack.enter_context(patch.object(gate, 'gate_observation', return_value=observed))
                    explain = stack.enter_context(patch.object(gate, 'origin_observation', return_value=gate.empty_origin()))
                    stack.enter_context(patch.object(gate, 'same_process', return_value=False))
                    stack.enter_context(patch.object(gate, 'capture_owned', side_effect=launch))
                    _, _, _, _, result = gate.capture(None, env={}, private=root, marker_only=True)
                self.assertTrue((directory / 'marker-ack.json').is_file())
                self.assertFalse((directory / 'permit.json').exists())
                self.assertEqual(result['mode'], 'marker_only')
                self.assertEqual(result['terminalAncestorObserved'], bool(verified))
                self.assertEqual(explain.call_count, 0 if verified is None else 1)
                self.assertTrue(result['supervisorTerminationObserved'])
                self.assertFalse(result['nativeStartObserved'])
                self.assertEqual(result['nativeProcessStatus'], 'not_run')
                self.assertIsNone(result['nativeExitCode'])

    def test_marker_receipt_cannot_claim_native_execution(self):
        value = gate.empty_receipt(marker_only=True)
        value.update(nativeStartObserved=True, nativeExitCode=0, nativeProcessStatus='completed')
        with self.assertRaises(ValueError):
            gate.receipt(value)

    def test_marker_completion_rejects_populated_private_failures(self):
        value = gate.empty_receipt(marker_only=True)
        value.update(status='failed', reason='ancestry_unverified', gateObservation=observation(False),
            availabilityVerified=True, openCompleted=True, markerVerified=True,
            markerOwnerJoined=True, supervisorTerminationObserved=True)
        self.assertTrue(namespace['marker_completed'](gate.receipt(value)))
        for changes in ({'reason': 'private_state_invalid'}, {'reason': 'source_changed'},
                        {'reason': 'supervisor_timeout'}, {'status': 'unavailable'},
                        {'supervisorTerminationObserved': False}, {'gateObservation': None}):
            self.assertFalse(namespace['marker_completed'](dict(value, **changes)))
        value.update(status='observed', reason='observed', terminalAncestorObserved=True,
                     gateObservation=observation(True))
        self.assertTrue(namespace['marker_completed'](gate.receipt(value)))
        self.assertFalse(namespace['marker_completed'](dict(value, status='failed')))
        self.assertFalse(namespace['marker_completed'](dict(value, reason='ancestry_unverified')))

    def test_verified_observation_cannot_skip_an_earlier_stop(self):
        for row in ({'uidRelation': 'other', 'commandClass': 'shell'},
                    {'uidRelation': 'same', 'commandClass': 'unknown'},
                    {'uidRelation': 'same', 'commandClass': 'terminal_exact'}):
            value = observation(True)
            value['hops'].insert(0, row)
            value['hopCount'] = 2
            with self.assertRaises(ValueError):
                gate.validate_observation(value)

    def test_entry_classifies_a_failed_gate_without_native_or_build_commands(self):
        for fail in (False, True, 'source_changed', 'cleanup_failed', 'origin_timeout'):
            with self.subTest(fail=fail), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                sha, tree = 'a' * 40, 'b' * 40
                branch = 'diagnostic/dnssd/' + sha[:12] + '-synthetic-marker'
                event, output = root / 'event.json', root / 'output'
                event.write_text(json.dumps({'ref_type': 'branch', 'ref': branch,
                    'repository': {'full_name': 'webkaz-labs/sobalink', 'private': False}}))
                native = namespace['module']('ci-nearby-native')
                real_module = namespace['module']
                def module(name):
                    return native if name == 'ci-nearby-native' else gate if name == 'ci-dnssd-terminal' else real_module(name)
                def checked(command):
                    self.assertIn(command, (['git', 'rev-parse', 'HEAD'], ['git', 'rev-parse', 'HEAD^{tree}']))
                    return (tree if command[-1] == 'HEAD^{tree}' else sha).encode()
                def capture(binary, *, env, private, marker_only):
                    self.assertIsNone(binary)
                    self.assertEqual(env, {})
                    self.assertTrue(marker_only)
                    if fail is True:
                        raise ValueError('synthetic-private')
                    result = gate.empty_receipt(marker_only=True)
                    result['originObservation'] = (gate.empty_origin('worker_incomplete') if fail == 'origin_timeout'
                        else dict(gate.empty_origin('process_absent'), workerJoined=True, queriesJoined=True))
                    result.update(status='failed', reason='ancestry_unverified', gateObservation=observation(False),
                        availabilityVerified=True, openCompleted=True, markerVerified=True,
                        markerOwnerJoined=True, supervisorTerminationObserved=True)
                    return 'capture_failed', None, b'', b'', result
                with ExitStack() as stack:
                    stack.enter_context(patch.dict(os.environ, GITHUB_ACTIONS='true', RUNNER_TEMP=temporary,
                        GITHUB_EVENT_PATH=str(event), GITHUB_OUTPUT=str(output), GITHUB_EVENT_NAME='create',
                        GITHUB_REPOSITORY='webkaz-labs/sobalink', GITHUB_SHA=sha, GITHUB_REF='refs/heads/' + branch))
                    stack.enter_context(patch.object(namespace['sys'], 'platform', 'darwin'))
                    stack.enter_context(patch.dict(namespace, module=module))
                    stack.enter_context(patch.object(native, 'checked', side_effect=checked))
                    inventory = {name: '0' * 64 for name in namespace['POLICY_FILES']}
                    stack.enter_context(patch.object(native, 'source_inventory', side_effect=[inventory,
                        dict(inventory, synthetic='1' * 64) if fail == 'source_changed' else inventory]))
                    stack.enter_context(patch.object(gate, 'capture', side_effect=capture))
                    if fail == 'cleanup_failed':
                        stack.enter_context(patch.object(namespace['shutil'], 'rmtree', side_effect=OSError('synthetic-private')))
                    printed = stack.enter_context(patch('sys.stdout', new_callable=io.StringIO))
                    self.assertEqual(namespace['main']([]), 1 if fail else 0)
                result_text = (root / 'soba-dnssd-sanitized/result.json').read_text()
                result = json.loads(result_text)
                self.assertEqual(result['markerProbeCompleted'], not fail or fail == 'origin_timeout')
                self.assertEqual(result['originExplanationCompleted'], not fail)
                self.assertFalse(result['nativeNetworkDiagnosticRun'])
                self.assertFalse(result['networkMulticastVerified'])
                self.assertFalse(result['releaseValidation'])
                self.assertEqual(output.read_text(), 'sanitized_ready=true\n')
                self.assertNotIn('synthetic-private', result_text + printed.getvalue())
                self.assertNotIn(temporary, result_text + printed.getvalue())

    def test_workflow_restores_only_the_same_bounded_native_route(self):
        root = ENTRY.resolve().parents[2]
        job = (root / '.github/workflows/activation-diagnostic.yml').read_text().split('  diagnostic-dnssd:\n', 1)[1]
        for forbidden in ('matrix:', 'sudo', 'osascript', 'run: python -B .github/scripts/ci-dnssd-terminal-marker.py'):
            self.assertNotIn(forbidden, job)
        self.assertEqual(job.count('runs-on: macos-26'), 1)
        self.assertIn('run: python -B .github/scripts/ci-dnssd-diagnostic.py', job)
        self.assertIn("go-version: '1.27.1'", job)
        self.assertIn('timeout-minutes: 12', job)
        self.assertIn('timeout-minutes: 10', job)
        for filename in ('result.json', 'provenance.json'):
            self.assertIn('${{ runner.temp }}/soba-dnssd-sanitized/' + filename, job)
        self.assertNotIn('*.json', job)


if __name__ == '__main__':
    unittest.main()
