"""Closed DNS-SD receipts and synthetic process results; no native execution."""
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from contextlib import ExitStack
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('ci-dnssd-diagnostic.py')
SPEC = importlib.util.spec_from_file_location('ci_dnssd_diagnostic', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), gate.__dict__)


def record(scope, status='verified', **changes):
    reason, flags = gate.PROOFS[scope]
    value = {'scope': scope, 'status': status, 'reason': reason if status == 'verified' else 'deadline',
             'failureStage': 'none', 'failureKind': 'none',
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
        rows += [dict(base, Action='run'), dict(base, Action='output', Output=gate.MARKER + json.dumps(value)),
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

    def test_exact_batch_requires_four_runs_and_matching_terminals(self):
        records = {scope: record(scope) for scope in gate.PROOFS}
        self.assertEqual(gate.test_events(raw(events(records)), 0), (records, True))
        unavailable = {scope: record(scope, 'unavailable') for scope in gate.PROOFS}
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
        for failure in (None, 'c-compile', 'c-link', 'go-compile', 'unavailable', 'runtime-failed', *preflight):
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
                def module(name):
                    return native if name == 'ci-nearby-native' else real_module(name)
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
                records = {scope: record(scope, 'unavailable' if failure == 'unavailable' else 'verified') for scope in gate.PROOFS}
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
                    self.assertEqual(result['capabilities'], records)
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
        self.assertIn('before != native.source_inventory()', source)
        self.assertIn("'-race', '-p=1', '-c'", source)
        self.assertNotIn('str(exc)', source)


if __name__ == '__main__':
    unittest.main()
