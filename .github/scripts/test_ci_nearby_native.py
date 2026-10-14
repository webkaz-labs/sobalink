"""Native receipt, bounded private capture and workflow policy; no socket tests."""
import copy
import importlib.util
import json
import io
import os
from pathlib import Path
import re
import sys
import tempfile
import unittest
from unittest.mock import patch
from contextlib import ExitStack

SCRIPT = Path(__file__).with_name('ci-nearby-native.py')
SPEC = importlib.util.spec_from_file_location('ci_nearby_native', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), gate.__dict__)


def capability(status='verified', **changes):
    reasons = {'verified': 'bidirectional_scoped_mdns', 'unavailable': 'native_socket_unavailable',
               'failed': 'cleanup_not_joined', 'not_run': 'opt_in_required'}
    result = {'scope': 'isolated_loopback', 'status': status, 'reason': reasons[status],
            'bidirectionalCandidates': status == 'verified', 'ownersJoined': status == 'verified',
            'socketFailureStage': 'none', 'sendFailureKind': 'none', 'bindFailureKind': 'none', 'bindSocketRole': 'none', 'windowsNoAncillaryProbe': None, **changes}
    if result['socketFailureStage'] == 'socket_write' and 'sendFailureKind' not in changes:
        result['sendFailureKind'] = 'other'
    if result['socketFailureStage'] == 'socket_bind':
        if 'bindFailureKind' not in changes:
            result['bindFailureKind'] = 'other'
        if 'bindSocketRole' not in changes:
            result['bindSocketRole'] = 'group_receive'
    return result


def events(record=None, terminal=None):
    record = capability() if record is None else record
    terminal = terminal or {'verified': 'pass', 'unavailable': 'skip', 'failed': 'fail', 'not_run': 'skip'}[record['status']]
    def row(action, test=False, **values):
        return {'Action': action, 'Package': gate.PACKAGE, **({'Test': gate.TEST} if test else {}), **values}
    return [row('start'), row('run', True),
            row('output', True, Output='    fixture.go:1: NEARBY_NATIVE_CAPABILITY ' + json.dumps(record) + '\n'),
            row(terminal, True), row('fail' if terminal == 'fail' else 'pass')]


def raw(rows):
    return b'\n'.join(json.dumps(row).encode() for row in rows)


class NativeNearbyTests(unittest.TestCase):
    def test_verified_requires_exact_run_pass_and_zero_exit(self):
        record = capability()
        self.assertEqual(gate.native_events(raw(events(record)), 0), (record, True))
        for rows in ([], events(record) + events(record)):
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                gate.native_events(raw(rows), 0)
        for code in (None, True):
            with self.subTest(code=code), self.assertRaises(ValueError):
                gate.native_events(raw(events(record)), code)
        for rows, code in ((events(record)[:-1], 0), (events(record)[1:], 0),
                           (events(record, 'skip'), 0), (events(record), 1),
                           (events(record), -9), ([events(record)[1], events(record)[0], *events(record)[2:]], 0)):
            self.assertEqual(gate.native_events(raw(rows), code), (record, False))

    def test_unavailable_is_exact_skip_without_verification(self):
        shared = gate.module('ci-nearby-diagnostic')
        for reason in shared.UNAVAILABLE:
            record = capability('unavailable', reason=reason, socketFailureStage='socket_bind')
            got, accepted = gate.native_events(raw(events(record)), 0)
            self.assertTrue(accepted)
            self.assertEqual(got, record)
            self.assertFalse(got['bidirectionalCandidates'])
            self.assertEqual(got['status'], 'unavailable')
        for terminal in ('pass', 'fail'):
            record = capability('unavailable')
            self.assertEqual(gate.native_events(raw(events(record, terminal)), 0), (record, False))

    def test_failed_runtime_retains_original_closed_cleanup_and_socket_stage(self):
        shared = gate.module('ci-nearby-diagnostic')
        for reason in shared.FAILED:
            record = capability('failed', reason=reason, socketFailureStage='socket_read')
            self.assertEqual(gate.native_events(raw(events(record)), 1), (record, False))
        with self.assertRaises(ValueError):
            gate.native_events(raw(events(capability('failed'))), None)
        record = capability('failed')
        self.assertEqual(gate.native_events(raw(events(record)), 0), (record, False))
        for terminal in ('skip', 'pass'):
            self.assertEqual(gate.native_events(raw(events(record, terminal)), 1), (record, False))

    def test_closed_marker_cannot_export_private_fields_or_raw_os_error(self):
        for changes in ({'message': 'synthetic-private'}, {'reason': 'synthetic-private'},
                        {'socketFailureStage': 'synthetic-private'}, {'scope': 'physical_lan'},
                        {'ownersJoined': 1}, {'bidirectionalCandidates': 1}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                gate.native_events(raw(events(capability('failed', **changes))), 1)
        rows = events(capability('failed'))
        rows.insert(3, copy.deepcopy(rows[2]))
        with self.assertRaises(ValueError):
            gate.native_events(raw(rows), 1)
        for key in capability():
            record = capability()
            del record[key]
            rows = events()
            rows[2]['Output'] = 'NEARBY_NATIVE_CAPABILITY ' + json.dumps(record)
            with self.assertRaises(ValueError):
                gate.native_events(raw(rows), 0)

    def test_probe_invariants_cannot_remain_an_accepted_skip(self):
        probe = {'status': 'failed', 'reason': 'foreign_or_missing_packet_metadata',
                 'socketFailureStage': 'none', 'sendFailureKind': 'none', 'metadataVerified': False,
                 'ownersJoined': True, 'productionDiscoveryVerified': False}
        bad = capability('unavailable', ownersJoined=True, socketFailureStage='socket_write', windowsNoAncillaryProbe=probe)
        with self.assertRaises(ValueError):
            gate.native_events(raw(events(bad)), 0)
        failed = dict(bad, status='failed', reason=probe['reason'])
        self.assertEqual(gate.native_events(raw(events(failed)), 1), (failed, False))

    def test_unknown_test_package_subtest_optout_and_build_metadata_rejected(self):
        for change in ({'Package': 'example.test/foreign'}, {'Test': gate.TEST + '/child'}, {'Action': 'build-fail'}):
            rows = events()
            rows[1].update(change)
            with self.assertRaises(ValueError):
                gate.native_events(raw(rows), 0)
        record = capability('not_run')
        self.assertEqual(gate.native_events(raw(events(record)), 0), (record, False))
        with self.assertRaises(ValueError):
            gate.native_events(b'x' * (2 * gate.RUNTIME_BYTES + 1), 0)
        for payload in (b'not-json', b'{"Action":"run","Action":"fail","Package":"a"}'):
            with self.assertRaises(ValueError):
                gate.native_events(payload, 0)

    def test_capture_is_small_private_and_preserves_nonzero(self):
        state, code, out, err = gate.capture([sys.executable, '-c',
            'import sys; print("synthetic-out"); print("synthetic-error", file=sys.stderr); sys.exit(3)'])
        self.assertEqual((state, code), ('nonzero', 3))
        self.assertEqual(out.strip(), b'synthetic-out')
        self.assertEqual(err.strip(), b'synthetic-error')
        state, code, out, err = gate.capture([sys.executable, '-c',
            'import sys; print("x" * 4096); print("y" * 4096, file=sys.stderr)'], limit=64)
        self.assertEqual(state, 'output_limit')
        self.assertLessEqual(len(out), 64)
        self.assertLessEqual(len(err), 64)

    def test_capture_stops_exact_child_on_timeout_and_handles_launch_failure(self):
        result = gate.capture([sys.executable, '-c', 'import time; time.sleep(5)'], timeout=0.05)
        self.assertEqual(result[0], 'timeout')
        self.assertIsNotNone(result[1])
        with patch.object(gate.subprocess, 'Popen', side_effect=OSError('synthetic-private')):
            self.assertEqual(gate.capture(['synthetic-tool']), ('launch_failed', None, b'', b''))

    def test_capture_stdin_is_bounded_and_converter_receives_only_private_bytes(self):
        result = gate.capture([sys.executable, '-c', 'import sys; sys.stdout.buffer.write(sys.stdin.buffer.read())'], stdin=b'synthetic')
        self.assertEqual(result[:3], ('completed', 0, b'synthetic'))
        result = gate.capture([sys.executable, '-c', 'import sys; sys.stdin.buffer.read()'], stdin=b'x' * 65, limit=64)
        self.assertEqual(result[0], 'output_limit')

    def test_platform_identity_is_actual_native_toolchain_not_cross_compile(self):
        for key, (goos, goarch, runner, runner_os, runner_arch) in gate.PLATFORMS.items():
            versions = {'GOVERSION': gate.GO_VERSION, 'GOHOSTOS': goos, 'GOHOSTARCH': goarch,
                        'GOOS': goos, 'GOARCH': goarch, 'CGO_ENABLED': '1'}
            system = 'win32' if goos == 'windows' else goos
            with patch.dict(os.environ, RUNNER_OS=runner_os, RUNNER_ARCH=runner_arch):
                result = gate.identities(key, versions, host_platform=system, machine=goarch)
                self.assertEqual(result, {'go': gate.GO_VERSION, 'goos': goos, 'goarch': goarch, 'runner': runner, 'race': True})
                for field in versions:
                    with self.subTest(key=key, field=field), self.assertRaises(ValueError):
                        gate.identities(key, dict(versions, **{field: 'other'}), host_platform=system, machine=goarch)
                with self.assertRaises(ValueError):
                    gate.identities(key, versions, host_platform='foreign', machine=goarch)
                with self.assertRaises(ValueError):
                    gate.identities(key, versions, host_platform=system, machine='foreign')
            with patch.dict(os.environ, RUNNER_OS='foreign', RUNNER_ARCH=runner_arch), self.assertRaises(ValueError):
                gate.identities(key, versions, host_platform=system, machine=goarch)

    def test_source_inventory_rejects_even_ignored_inputs_and_symlinks(self):
        with patch.object(gate, 'checked', side_effect=[b'', b'ignored.py\0']), self.assertRaises(ValueError):
            gate.source_inventory()
        with patch.object(gate, 'checked', return_value=b'modified'), self.assertRaises(ValueError):
            gate.source_inventory()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'fixture').write_text('synthetic')
            with patch.object(gate, 'ROOT', root), patch.object(gate, 'checked', side_effect=[b'', b'', b'fixture\0']):
                self.assertEqual(gate.source_inventory(), {'fixture': gate.digest(root / 'fixture')})
            for name in ('../fixture', '/fixture', 'fixture\\child', 'fixture/../child'):
                with patch.object(gate, 'ROOT', root), patch.object(gate, 'checked', side_effect=[b'', b'', name.encode() + b'\0']), self.assertRaises(ValueError):
                    gate.source_inventory()

    def test_fail_closed_result_and_hosted_only_entry(self):
        result = gate.empty_result()
        self.assertEqual(result['processStatus'], 'not_run')
        self.assertIsNone(result['nativeDiscovery'])
        for key in ('diagnosticPassed', 'nativeDiscoveryVerified', 'eventsAccepted',
                    'physicalLANVerified', 'automaticCoreBrowserVerified', 'releaseValidation'):
            self.assertIs(result[key], False)
        with patch.dict(os.environ, GITHUB_ACTIONS='false'), patch('sys.stdout', new_callable=io.StringIO):
            self.assertEqual(gate.main([]), 1)

    def test_mocked_runner_exports_only_bound_generic_evidence_and_honest_exit(self):
        # Synthetic process results only: this never launches a Go/socket test.
        for status, process_state, code, terminal, expected_exit in (
                ('verified', 'completed', 0, 'pass', 0),
                ('unavailable', 'completed', 0, 'skip', 0),
                ('failed', 'nonzero', 1, 'fail', 1),
                ('unavailable', 'nonzero', 1, 'fail', 1),
                ('verified', 'timeout', -9, 'pass', 1),
                ('verified', 'output_limit', -9, 'pass', 1)):
            with self.subTest(status=status, process_state=process_state), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                tool = root / 'fake-go'
                tool.write_bytes(b'synthetic tool')
                output = root / 'output'
                event = root / 'event'
                sha, tree = 'a' * 40, 'b' * 40
                ref = 'diagnostic/nearby/' + sha[:12] + '-synthetic'
                event.write_text(json.dumps({'ref_type': 'branch', 'ref': ref,
                    'repository': {'full_name': 'webkaz-labs/sobalink', 'private': False}}))
                inventory = {name: '0' * 64 for name in (
                    '.github/workflows/activation-diagnostic.yml', '.github/scripts/ci-nearby-native.py',
                    '.github/scripts/test_ci_nearby_native.py', '.github/scripts/ci-nearby-diagnostic.py',
                    '.github/scripts/ci-go-test.py', '.github/scripts/ci-diagnostic.py',
                    'internal/nearby/native_acceptance_test.go', 'go.mod', 'go.sum')}
                checked_calls, capture_calls = [], []
                def checked(command, **kwargs):
                    checked_calls.append((command, kwargs))
                    if command == ['git', 'rev-parse', 'HEAD']:
                        return sha.encode()
                    if command == ['git', 'rev-parse', 'HEAD^{tree}']:
                        return tree.encode()
                    if command[:2] == ['go', 'env']:
                        return b'{}'
                    if command[:3] == ['go', 'mod', 'download']:
                        return b''
                    if command[:3] == ['go', 'test', '-race']:
                        Path(command[command.index('-o') + 1]).write_bytes(b'synthetic binary')
                        return b''
                    raise AssertionError('unexpected command')
                record = capability(status, socketFailureStage='none' if status == 'verified' else 'socket_read')
                def capture(command, **kwargs):
                    capture_calls.append((command, kwargs))
                    if command[:3] == ['go', 'tool', 'test2json']:
                        self.assertEqual(kwargs['stdin'], b'synthetic-private-native-output')
                        return 'completed', 0, raw(events(record, terminal)), b'synthetic-private-converter-error'
                    self.assertEqual(command[1:], ['-test.v=test2json', '-test.run=^' + gate.TEST + '$', '-test.count=1', '-test.timeout=20s'])
                    self.assertEqual(kwargs['env']['SOBALINK_NATIVE_NEARBY_LOOPBACK'], '1')
                    return process_state, code, b'synthetic-private-native-output', b'synthetic-private-os-error'
                with ExitStack() as stack:
                    stack.enter_context(patch.dict(os.environ, GITHUB_ACTIONS='true', RUNNER_TEMP=str(root),
                        GITHUB_EVENT_PATH=str(event), GITHUB_EVENT_NAME='create',
                        GITHUB_REPOSITORY='webkaz-labs/sobalink', GITHUB_REF='refs/heads/' + ref,
                        GITHUB_SHA=sha, GITHUB_OUTPUT=str(output), SOBALINK_NEARBY_NATIVE_PLATFORM='linux-arm64'))
                    stack.enter_context(patch.object(gate, 'checked', side_effect=checked))
                    stack.enter_context(patch.object(gate, 'capture', side_effect=capture))
                    stack.enter_context(patch.object(gate, 'source_inventory', return_value=inventory))
                    stack.enter_context(patch.object(gate, 'identities', return_value={
                        'go': gate.GO_VERSION, 'goos': 'linux', 'goarch': 'arm64', 'runner': 'ubuntu-24.04-arm', 'race': True}))
                    stack.enter_context(patch.object(gate.shutil, 'which', return_value=str(tool)))
                    printed = stack.enter_context(patch('sys.stdout', new_callable=io.StringIO))
                    self.assertEqual(gate.main([]), expected_exit)
                exported = root / 'soba-nearby-native-sanitized'
                result_text = (exported / 'result.json').read_text()
                provenance_text = (exported / 'provenance.json').read_text()
                result, provenance = json.loads(result_text), json.loads(provenance_text)
                self.assertEqual(set(result), set(gate.empty_result()))
                self.assertEqual(result['diagnosticPassed'], expected_exit == 0)
                self.assertEqual(result['nativeDiscoveryVerified'], status == 'verified' and expected_exit == 0)
                self.assertEqual(result['processStatus'], process_state)
                if process_state in ('completed', 'nonzero'):
                    self.assertEqual(result['nativeDiscovery'], record)
                else:
                    self.assertIsNone(result['nativeDiscovery'])
                self.assertEqual(provenance['commit'], sha)
                self.assertEqual(provenance['tree'], tree)
                self.assertEqual(provenance['evidenceJobs'], gate.EVIDENCE_JOBS)
                self.assertEqual(provenance['test'], {'package': gate.PACKAGE, 'name': gate.TEST})
                self.assertEqual(provenance['stage'], 'passed' if expected_exit == 0 else 'runtime-failed')
                self.assertEqual(output.read_text(), 'sanitized_ready=true\n')
                for text in (result_text, provenance_text, printed.getvalue()):
                    self.assertNotIn('synthetic-private', text)
                    self.assertNotIn(str(root), text)
                self.assertEqual(sorted(path.name for path in exported.iterdir()), ['provenance.json', 'result.json'])
                self.assertFalse(list(root.glob('soba-nearby-native-*/*test*')))
                if status == 'unavailable' and expected_exit == 0:
                    self.assertIn('UNAVAILABLE:', printed.getvalue())
                    self.assertNotIn('PASS:', printed.getvalue())
                compiled = next(kwargs for command, kwargs in checked_calls if command[:3] == ['go', 'test', '-race'])
                self.assertEqual(compiled['env']['GOPROXY'], 'off')
                self.assertEqual(compiled['env']['GOFLAGS'], '-mod=readonly -buildvcs=false')

    def test_runner_reuses_one_existing_fixture_with_bounded_capture_and_no_heavy_engine(self):
        source = SCRIPT.read_text()
        self.assertIn("module('ci-nearby-diagnostic')", source)
        self.assertIn("shared.capability(shared.parse_json", source)
        self.assertIn("shared.test_events(raw, 'discovery', returncode)", source)
        self.assertIn("['go', 'mod', 'download', *MODULES]", source)
        self.assertEqual(gate.MODULES, ('golang.org/x/net@v0.59.0', 'golang.org/x/sys@v0.48.0'))
        self.assertIn("['go', 'test', '-race', '-c', '-o', str(binary), './internal/nearby']", source)
        self.assertIn("'-test.run=^' + TEST + '$'", source)
        self.assertIn("'-test.timeout=20s'", source)
        self.assertIn("timeout=30, limit=RUNTIME_BYTES", source)
        self.assertIn("env['SOBALINK_NATIVE_NEARBY_LOOPBACK'] = '1'", source)
        self.assertIn("env.update(GOPROXY='off', GOSUMDB='off')", source)
        for forbidden in ('prepare-engine', "'./internal/core'", 'npm', 'node_modules', 'shell=True', 'str(exc)', 'print(stdout', 'print(stderr', 'write_bytes('):
            self.assertNotIn(forbidden, source)
        self.assertEqual(source.count('source_inventory()'), 4)
        self.assertIn("provenance['testSha256'] != digest(binary)", source)
        self.assertIn("provenance['goSha256'] != digest(tool)", source)

    def test_workflow_has_three_new_native_platforms_and_reuses_fourth_ubuntu_job(self):
        workflow = (gate.ROOT / '.github/workflows/activation-diagnostic.yml').read_text()
        browser = workflow.split('  diagnostic-nearby:\n', 1)[1].split('  diagnostic-nearby-native:\n', 1)[0]
        job = re.split(r'^  [a-z][a-z0-9-]*:\n', workflow.split('  diagnostic-nearby-native:\n', 1)[1], maxsplit=1, flags=re.M)[0]
        self.assertEqual(browser.count('runs-on:'), 1)
        self.assertIn('runs-on: ubuntu-24.04\n', browser)
        self.assertNotIn('matrix:', browser)
        self.assertIn("if: needs.diagnostic-selection.outputs.suite == 'nearby'", job)
        self.assertIn('needs: diagnostic-selection', job)
        self.assertIn('fail-fast: false', job)
        self.assertEqual(job.count('- runner:'), 3)
        for key in ('linux-arm64', 'darwin-arm64', 'windows-amd64'):
            self.assertIn('platform: ' + key, job)
            self.assertIn('runner: ' + gate.PLATFORMS[key][2], job)
            self.assertEqual(gate.EVIDENCE_JOBS[key], 'diagnostic-nearby-native')
        self.assertEqual(gate.EVIDENCE_JOBS['linux-amd64'], 'diagnostic-nearby')
        self.assertNotIn('runner: ubuntu-24.04\n', job)
        for pin in ('actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1',
                    'actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16',
                    'actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a'):
            self.assertIn(pin, job)
        for required in ('persist-credentials: false', 'contents: read', "go-version: '1.27.1'", 'cache: false',
                         'shell: bash', 'run: python -B .github/scripts/ci-nearby-native.py'):
            self.assertIn(required, job)
        for forbidden in ('continue-on-error:', 'contents: write', 'id-token:', 'secrets.', 'actions/cache',
                          'ip route', 'iptables', 'sysctl', 'sudo', 'chmod', 'prepare-engine',
                          'setup-node', 'npm', 'playwright', './internal/core', 'go test ./...'):
            self.assertNotIn(forbidden, job)
        artifact = job.split('uses: actions/upload-artifact', 1)[1]
        self.assertIn("if: always() && steps.native-nearby.outputs.sanitized_ready == 'true'", job)
        self.assertIn('name: diagnostic-soba-nearby-native-${{ matrix.platform }}-sanitized', artifact)
        for name in ('result.json', 'provenance.json'):
            self.assertIn('${{ runner.temp }}/soba-nearby-native-sanitized/' + name, artifact)
        for forbidden in ('*.json', 'private', '.log', '.jsonl', 'nearby.test', 'trace', 'screenshot'):
            self.assertNotIn(forbidden, artifact)


if __name__ == '__main__':
    unittest.main()
