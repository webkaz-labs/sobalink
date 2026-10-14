"""Synthetic package evidence and bounded local children; never build or run macOS code."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import struct
import sys
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('ci-macos-package.py')
SPEC = importlib.util.spec_from_file_location('ci_macos_package', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), gate.__dict__)
ROOT = SCRIPT.resolve().parents[2]
COMMIT, TREE = 'a' * 40, 'b' * 40
PRIVATE = 'synthetic-private-not-for-export'


def process(state='completed', code=0, stdout=b'', stderr=b'', *, version=False, binary=None):
    return gate.process_record((state, code, stdout, stderr, [len(stdout), len(stderr)], True),
                               version=version, binary=binary)


def version(original=False):
    if original:
        return process('nonzero', -6, stderr=b'dyld[123]: missing LC_UUID load command in /tmp/' + PRIVATE.encode())
    return process(stdout=('sobalink ' + gate.VERSION + ' (soba)\n').encode(), version=True)


def build(original=False):
    return {'archiveSha256': '1' * 64, 'binarySha256': '2' * 64,
            'checksumsSha256': '3' * 64, 'metadataSha256': '4' * 64,
            'nativeSha256': '5' * 64,
            'macho': {'uuidCount': 0 if original else 1,
                      'uuid': None if original else gate.candidate_uuid(COMMIT, '5' * 64),
                      'minimumOS': 13 << 16, 'sdk': 26 << 16,
                      'signature': {'present': True, 'metadataValid': True, 'adhoc': True,
                                    'linkerSigned': True, 'sha256': '6' * 64}},
            'codesign': process(stderr=b'/tmp/soba: valid on disk\n/tmp/soba: satisfies its Designated Requirement\n',
                                binary=Path('/tmp/soba')),
            'version': version(original)}


def passing_result():
    result = gate.empty_result()
    result.update(phase='complete', sourceVerified=True, cleanupComplete=True)
    for name, original in (('original', True), ('candidate', False)):
        lane = result['lanes'][name]
        lane.update(sourceCommit=gate.BASELINE if original else COMMIT,
                    sourceTree=gate.BASELINE_TREE if original else TREE,
                    stage='complete', sourceVerified=True, reproducible=True,
                    builds=[build(original), build(original)],
                    smoke=None if original else process())
    gate.evaluate(result)
    return result


def provenance():
    return {'schema': 1, 'baselineCommit': gate.BASELINE, 'baselineTree': gate.BASELINE_TREE,
            'candidateCommit': COMMIT, 'candidateTree': TREE, 'version': gate.VERSION,
            'goVersion': gate.GO_VERSION, 'sourceDateEpoch': 1, 'changedFiles': sorted(gate.DELTA),
            'runId': 1, 'runAttempt': 1, 'repository': 'webkaz-labs/sobalink',
            'workflow': gate.WORKFLOW, 'job': 'diagnostic-macos-package'}


def macho(*, with_uuid=True, with_signature=True, flags=0x20002, extra=()):
    commands = []
    if with_uuid:
        commands.append(struct.pack('<2I16s', 0x1b, 24, bytes.fromhex('1234567890abcdef1234567890abcdef')))
    commands.append(struct.pack('<6I', 0x32, 24, 1, 13 << 16, 26 << 16, 0))
    commands.extend(extra)
    directory = struct.pack('>11I', 0xfade0c02, 44, 0x20400, flags, 0, 0, 0, 0, 0, 0, 0)
    signature = struct.pack('>5I', 0xfade0cc0, 20 + len(directory), 1, 0, 20) + directory
    if with_signature:
        start = 32 + sum(map(len, commands)) + 16
        commands.append(struct.pack('<4I', 0x1d, 16, start, len(signature)))
    table = b''.join(commands)
    header = struct.pack('<8I', 0xfeedfacf, 0x100000c, 0, 2, len(commands), len(table), 0, 0)
    return header + table + (signature if with_signature else b'')


def u32(data, offset, value, endian='<'):
    value_bytes = bytearray(data)
    struct.pack_into(endian + 'I', value_bytes, offset, value)
    return bytes(value_bytes)


class SourcePolicyTests(unittest.TestCase):
    def test_baseline_delta_and_workflow_are_literal_closed_contracts(self):
        self.assertEqual(gate.BASELINE, '2a775d546d418393de22597fb9a805f60b97bd7d')
        self.assertEqual(gate.BASELINE_TREE, '2fa25da5c2111c64fbdd4b20c3ad81f942b624d1')
        self.assertEqual(gate.VERSION, '0.0.0-dev.264')
        self.assertEqual(gate.GO_VERSION, 'go1.27.1')
        self.assertEqual(gate.DELTA, frozenset({
            '.github/workflows/activation-diagnostic.yml', '.github/scripts/ci-diagnostic.py',
            '.github/scripts/test_ci_diagnostic.py', '.github/scripts/ci-macos-package.py',
            '.github/scripts/test_ci_macos_package.py', '.github/scripts/source_provenance.py',
            '.github/scripts/test_source_provenance.py', 'internal/distribution/distribution.go',
            'internal/distribution/distribution_test.go', 'internal/distribution/native_build.go',
            'internal/distribution/native_build_test.go', 'docs/DISTRIBUTION.md'}))
        workflow = (ROOT / gate.WORKFLOW).read_text()
        job = workflow.split('  diagnostic-macos-package:\n', 1)[1].split('\n  diagnostic-', 1)[0]
        for required in ("if: needs.diagnostic-selection.outputs.suite == 'macos-package'",
                         'runs-on: macos-26', 'timeout-minutes: 60', 'timeout-minutes: 55',
                         'ref: ${{ github.sha }}', 'persist-credentials: false', 'fetch-depth: 0',
                         'python -B .github/scripts/ci-macos-package.py',
                         "steps.macos-package.outputs.sanitized_ready == 'true'"):
            self.assertIn(required, job)
        for forbidden in ('matrix:', 'secrets.', 'contents: write', 'id-token:', 'codesign',
                          'package-tool', '.tar.gz', '.zip', '*.json', '.log'):
            self.assertNotIn(forbidden, job)

    def run_preflight(self, *, environment=None, outputs=None, system='Darwin', machine='arm64', source_tree=TREE):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'event.json'
            ref = 'refs/heads/diagnostic/macos-package/' + COMMIT[:12] + '-fixture'
            path.write_text(json.dumps({'ref_type': 'branch', 'ref': ref.removeprefix('refs/heads/'),
                'repository': {'full_name': 'webkaz-labs/sobalink', 'private': False}}))
            env = dict(GITHUB_ACTIONS='true', GITHUB_JOB='diagnostic-macos-package',
                       RUNNER_OS='macOS', RUNNER_ARCH='ARM64',
                       GITHUB_WORKFLOW='Activation diagnostics (not release validation)',
                       GOTOOLCHAIN='local', GOWORK='off', GOFLAGS=gate.GOFLAGS,
                       GITHUB_EVENT_PATH=str(path), GITHUB_EVENT_NAME='create',
                       GITHUB_REPOSITORY='webkaz-labs/sobalink', GITHUB_REF=ref, GITHUB_SHA=COMMIT,
                       GITHUB_WORKFLOW_REF='webkaz-labs/sobalink/' + gate.WORKFLOW + '@' + ref,
                       GITHUB_RUN_ID='12', GITHUB_RUN_ATTEMPT='1')
            env.update(environment or {})
            responses = {
                ('git', 'rev-parse', 'HEAD'): (COMMIT + '\n').encode(),
                ('git', 'ls-files', '--others', '--exclude-standard', '-z'): b'',
                ('git', 'rev-parse', gate.BASELINE + '^{tree}'): (gate.BASELINE_TREE + '\n').encode(),
                ('git', 'merge-base', '--is-ancestor', gate.BASELINE, COMMIT): b'',
                ('git', 'diff', '--name-only', gate.BASELINE, COMMIT): ('\n'.join(sorted(gate.DELTA)) + '\n').encode(),
                ('go', 'env', 'GOVERSION'): b'go1.27.1\n',
                ('go', 'env', 'GOHOSTOS', 'GOHOSTARCH'): b'darwin\narm64\n',
                ('node', '--version'): b'v24.19.0\n', ('npm', '--version'): b'11.9.0\n',
                ('git', 'log', '-1', '--format=%ct', gate.BASELINE): b'1\n'}
            responses.update(outputs or {})
            calls = []
            def checked(command, **kwargs):
                calls.append(tuple(command))
                value = responses[tuple(command)]
                if isinstance(value, Exception):
                    raise value
                return value
            with patch.dict(os.environ, env, clear=True), patch.object(gate.platform, 'system', return_value=system), \
                    patch.object(gate.platform, 'machine', return_value=machine), \
                    patch.object(gate, 'checked', side_effect=checked), \
                    patch.object(gate, 'source_identity', return_value=source_tree):
                result = gate.preflight()
            return result, calls

    def test_preflight_binds_exact_source_ancestor_tools_and_workflow(self):
        result, calls = self.run_preflight()
        self.assertEqual(result, (COMMIT, TREE, 1, sorted(gate.DELTA)))
        self.assertIn(('git', 'merge-base', '--is-ancestor', gate.BASELINE, COMMIT), calls)
        self.assertIn(('go', 'env', 'GOHOSTOS', 'GOHOSTARCH'), calls)

    def test_source_identity_requires_exact_clean_commit_and_valid_tree(self):
        for head, tree, status, accepted in (
                (COMMIT, TREE, b'', True), ('c' * 40, TREE, b'', False),
                (COMMIT, PRIVATE, b'', False), (COMMIT, TREE, b' M tracked.py\n', False)):
            responses = [(head + '\n').encode(), (tree + '\n').encode(), status]
            with self.subTest(head=head, tree=tree, status=status), \
                    patch.object(gate, 'checked', side_effect=responses):
                if accepted:
                    self.assertEqual(gate.source_identity(ROOT, COMMIT), TREE)
                else:
                    with self.assertRaises(gate.DiagnosticFailure) as raised:
                        gate.source_identity(ROOT, COMMIT)
                    self.assertEqual(raised.exception.failure, 'source_changed')

    def test_preflight_rejects_other_hosts_events_sources_and_tool_versions(self):
        for change in ({'system': 'Linux'}, {'machine': 'x86_64'},
                       *({'environment': {name: value}} for name, value in (
                           ('GITHUB_ACTIONS', 'false'), ('GITHUB_JOB', 'native'),
                           ('RUNNER_OS', 'Linux'), ('RUNNER_ARCH', 'X64'),
                           ('GITHUB_EVENT_NAME', 'push'), ('GITHUB_SHA', 'c' * 40),
                           ('GITHUB_WORKFLOW_REF', 'synthetic-other-workflow'),
                           ('GITHUB_WORKFLOW', 'Cross-platform CI'), ('GOWORK', ''),
                           ('GOTOOLCHAIN', 'auto'), ('GOFLAGS', ''),
                           ('GITHUB_RUN_ID', '0'), ('GITHUB_RUN_ATTEMPT', '-1')))):
            with self.subTest(change=change), self.assertRaises((gate.DiagnosticFailure, ValueError)):
                self.run_preflight(**change)
        for command, value in (
                (('git', 'ls-files', '--others', '--exclude-standard', '-z'), b'extra.py\0'),
                (('git', 'rev-parse', gate.BASELINE + '^{tree}'), ('c' * 40).encode()),
                (('git', 'merge-base', '--is-ancestor', gate.BASELINE, COMMIT), gate.DiagnosticFailure('preflight_failed')),
                (('git', 'diff', '--name-only', gate.BASELINE, COMMIT), b'internal/core/app.go\n'),
                (('git', 'diff', '--name-only', gate.BASELINE, COMMIT), b'internal/distribution/distribution.go\n'),
                (('go', 'env', 'GOVERSION'), b'go1.26.0\n'),
                (('go', 'env', 'GOHOSTOS', 'GOHOSTARCH'), b'linux\narm64\n'),
                (('node', '--version'), b'v24.18.0\n'), (('npm', '--version'), b'11.8.0\n'),
                (('git', 'log', '-1', '--format=%ct', gate.BASELINE), b'-1\n')):
            with self.subTest(command=command, value=value), self.assertRaises(gate.DiagnosticFailure):
                self.run_preflight(outputs={command: value})


class MachOTests(unittest.TestCase):
    def parse(self, data):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'synthetic-macho'
            path.write_bytes(data)
            return gate.bounded_macho(path)

    def test_uuid_and_adhoc_signature_are_independent_bounded_facts(self):
        for uuid in (False, True):
            value = self.parse(macho(with_uuid=uuid))
            self.assertEqual(value['uuidCount'], int(uuid))
            self.assertEqual(value['uuid'], '1234567890abcdef1234567890abcdef' if uuid else None)
            self.assertEqual(value['minimumOS'], 13 << 16)
            self.assertEqual(value['sdk'], 26 << 16)
            self.assertEqual({key: value['signature'][key] for key in ('present', 'metadataValid', 'adhoc', 'linkerSigned')},
                             {'present': True, 'metadataValid': True, 'adhoc': True, 'linkerSigned': True})
            self.assertRegex(value['signature']['sha256'], r'^[0-9a-f]{64}$')
        value = self.parse(macho(with_signature=False))
        self.assertEqual(value['signature'], {'present': False, 'metadataValid': False,
            'adhoc': False, 'linkerSigned': False, 'sha256': None})
        value = self.parse(macho(flags=0))
        self.assertFalse(value['signature']['adhoc'])
        self.assertFalse(value['signature']['linkerSigned'])

    def test_malformed_headers_and_load_commands_fail_closed(self):
        data = macho()
        cases = [b'', data[:31], data[:-1], u32(data, 0, 0), u32(data, 4, 0x1000007),
                 u32(data, 12, 6), u32(data, 16, 0), u32(data, 16, 1025),
                 u32(data, 20, 1024 * 1024 + 1), u32(data, 20, len(data)),
                 u32(data, 36, 0), u32(data, 36, 12), u32(data, 36, 32),
                 u32(data, 64, 2), u32(data, 76, 1),
                 macho(extra=(struct.pack('<2I16s', 0x1b, 24, b'\0' * 16),)),
                 macho(extra=(struct.pack('<6I', 0x32, 24, 1, 13 << 16, 26 << 16, 0),)),
                 u32(data, 56, 0)]
        for index, value in enumerate(cases):
            with self.subTest(index=index), self.assertRaises(gate.DiagnosticFailure):
                self.parse(value)

    def test_signature_bounds_slots_and_code_directory_fail_closed(self):
        data = macho()
        # UUID (24) + build-version (24) precede the signature command (16).
        signature_command, blob = 80, 96
        cases = [u32(data, signature_command + 8, 0),
                 u32(data, signature_command + 8, len(data)),
                 u32(data, signature_command + 12, 11),
                 u32(data, signature_command + 12, 16 * 1024 * 1024 + 1),
                 u32(data, blob, 0, '>'), u32(data, blob + 4, 11, '>'),
                 u32(data, blob + 4, 65, '>'), u32(data, blob + 8, 0, '>'),
                 u32(data, blob + 8, 65, '>'), u32(data, blob + 12, 1, '>'),
                 u32(data, blob + 16, 12, '>'), u32(data, blob + 16, 63, '>'),
                 u32(data, blob + 20, 0, '>'), u32(data, blob + 24, 43, '>'),
                 u32(data, blob + 24, 100, '>')]
        for index, value in enumerate(cases):
            with self.subTest(index=index), self.assertRaises(gate.DiagnosticFailure):
                self.parse(value)


class SanitizedProcessTests(unittest.TestCase):
    def test_loader_stderr_is_classified_without_copying_private_suffixes(self):
        cases = [(b'dyld[123]: missing LC_UUID load command in /tmp/' + PRIVATE.encode(), 'dyld_missing_uuid'),
                 (b'dyld: missing LC_UUID', 'dyld_missing_uuid'),
                 (b'dyld: Library not loaded: /tmp/' + PRIVATE.encode() + b'\n' + PRIVATE.encode(), 'dyld_library_not_loaded'),
                 (b'dyld[7]: Symbol not found: ' + PRIVATE.encode() + b'\n' + PRIVATE.encode(), 'dyld_symbol_not_found'),
                 (b'dyld: missing LC_UUID load command\n' + PRIVATE.encode(), 'unknown'),
                 (PRIVATE.encode(), 'unknown'), (b'\xff\xfe', 'unknown'), (b'', 'empty')]
        for stderr, expected in cases:
            with self.subTest(stderr=stderr):
                value = process('nonzero', -6, stderr=stderr)
                self.assertEqual(value['stderr']['class'], expected)
                self.assertEqual(value['stderr']['sha256'], hashlib.sha256(stderr).hexdigest())
                self.assertNotIn(PRIVATE, json.dumps(value))
                self.assertNotIn('/tmp/', json.dumps(value))
                if expected in ('unknown', 'empty'):
                    self.assertEqual(value['stderr']['diagnostics'], [])

    def test_codesign_output_exports_only_fixed_diagnostics(self):
        binary = Path('/tmp') / PRIVATE
        for suffix, expected in (('valid on disk', 'codesign_valid'),
                                 ('satisfies its Designated Requirement', 'codesign_valid'),
                                 ('code object is not signed at all', 'codesign_invalid'),
                                 ('invalid signature (code or signature have been modified)', 'codesign_invalid'),
                                 (PRIVATE, 'unknown')):
            value = process(stderr=(str(binary) + ': ' + suffix + '\n').encode(), binary=binary)
            self.assertEqual(value['stderr']['class'], expected)
            self.assertNotIn(PRIVATE, json.dumps(value))

    def test_stdout_version_requires_exact_bytes_and_explicit_version_mode(self):
        expected = ('sobalink ' + gate.VERSION + ' (soba)\n').encode()
        self.assertEqual(process(stdout=expected, version=True)['stdout']['versionText'], expected.decode().rstrip('\n'))
        for value in (expected.rstrip(b'\n'), expected + PRIVATE.encode(), expected + b'\n',
                      expected.replace(b'\n', b'\r\n'), PRIVATE.encode(), b'\xff'):
            result = process(stdout=value, version=True)
            self.assertIsNone(result['stdout']['versionText'])
            self.assertEqual(result['stdout']['sha256'], hashlib.sha256(value).hexdigest())
            self.assertNotIn(PRIVATE, json.dumps(result))
        self.assertIsNone(process(stdout=expected)['stdout']['versionText'])


class EvaluationTests(unittest.TestCase):
    def test_only_paired_reproduced_cause_and_candidate_package_proof_pass(self):
        report = passing_result()
        for key in ('diagnosticPassed', 'baselineAbortReproduced', 'missingUUIDCauseObserved',
                    'candidateVerified', 'matchedNativeIdentity'):
            self.assertTrue(report[key], key)
        self.assertFalse(report['releaseValidation'])

    def test_partial_different_or_unreproduced_observations_cannot_turn_green(self):
        cases = []
        def case(name, update):
            value = passing_result()
            update(value)
            cases.append((name, value))
        for lane_name in ('original', 'candidate'):
            case(lane_name + '-unreproduced', lambda r, n=lane_name: r['lanes'][n].update(reproducible=False))
            case(lane_name + '-source-unverified', lambda r, n=lane_name: r['lanes'][n].update(sourceVerified=False))
            case(lane_name + '-failure', lambda r, n=lane_name: r['lanes'][n].update(failureClass='build_failed'))
            case(lane_name + '-missing-build', lambda r, n=lane_name: r['lanes'][n]['builds'].pop())
            case(lane_name + '-signature-absent', lambda r, n=lane_name: r['lanes'][n]['builds'][0]['macho']['signature'].update(present=False))
            case(lane_name + '-signature-invalid', lambda r, n=lane_name: r['lanes'][n]['builds'][0]['macho']['signature'].update(metadataValid=False))
            case(lane_name + '-signature-not-adhoc', lambda r, n=lane_name: r['lanes'][n]['builds'][0]['macho']['signature'].update(adhoc=False))
            case(lane_name + '-codesign-failed', lambda r, n=lane_name: r['lanes'][n]['builds'][0].update(codesign=process('nonzero', 1)))
        case('baseline-success', lambda r: r['lanes']['original']['builds'][0].update(version=version()))
        case('different-abort', lambda r: r['lanes']['original']['builds'][0]['version'].update(returncode=-11))
        case('different-loader-cause', lambda r: r['lanes']['original']['builds'][0].update(version=process('nonzero', -6, stderr=PRIVATE.encode())))
        case('baseline-uuid-present', lambda r: r['lanes']['original']['builds'][0]['macho'].update(uuidCount=1))
        case('candidate-uuid-absent', lambda r: r['lanes']['candidate']['builds'][0]['macho'].update(uuidCount=0))
        case('candidate-uuid-mismatched', lambda r: r['lanes']['candidate']['builds'][0]['macho'].update(uuid='0' * 32))
        case('candidate-version-failed', lambda r: r['lanes']['candidate']['builds'][0].update(version=version(True)))
        case('candidate-smoke-failed', lambda r: r['lanes']['candidate'].update(smoke=process('nonzero', 1)))
        case('candidate-smoke-missing', lambda r: r['lanes']['candidate'].update(smoke=None))
        case('candidate-incomplete', lambda r: r['lanes']['candidate'].update(stage='smoke'))
        case('native-mismatch', lambda r: r['lanes']['candidate']['builds'][0].update(nativeSha256='7' * 64))
        case('sdk-mismatch', lambda r: r['lanes']['candidate']['builds'][0]['macho'].update(sdk=25 << 16))
        case('minimum-mismatch', lambda r: r['lanes']['candidate']['builds'][0]['macho'].update(minimumOS=14 << 16))
        case('source-unverified', lambda r: r.update(sourceVerified=False))
        case('cleanup-incomplete', lambda r: r.update(cleanupComplete=False))
        case('top-level-failure', lambda r: r.update(failureClass='source_changed'))
        case('candidate-child-remains', lambda r: r['lanes']['candidate']['smoke'].update(groupAbsent=False))
        for name, report in cases:
            with self.subTest(name=name):
                gate.evaluate(report)
                self.assertFalse(report['diagnosticPassed'])
                self.assertFalse(report['releaseValidation'])


class ExportPolicyTests(unittest.TestCase):
    def test_complete_report_is_safe_and_valid(self):
        report, proof = passing_result(), provenance()
        gate.validate_export(report, proof)
        self.assertNotIn(PRIVATE, json.dumps([report, proof]))

    def test_early_failure_can_export_finite_unverified_evidence(self):
        report, proof = gate.empty_result(), provenance()
        report.update(phase='complete', cleanupComplete=True, failureClass='preflight_failed')
        proof.update(candidateCommit=None, candidateTree=None, sourceDateEpoch=None,
                     changedFiles=[], runId=None, runAttempt=None)
        gate.evaluate(report)
        gate.validate_export(report, proof)
        self.assertFalse(report['diagnosticPassed'])

    def test_export_binds_lane_sources_to_provenance_and_reproduction_claims(self):
        for lane_name in ('original', 'candidate'):
            for key in ('sourceCommit', 'sourceTree'):
                report, proof = passing_result(), provenance()
                report['lanes'][lane_name][key] = 'c' * 40
                with self.subTest(lane=lane_name, key=key), self.assertRaises(gate.DiagnosticFailure):
                    gate.validate_export(report, proof)
            for key in ('archiveSha256', 'binarySha256', 'checksumsSha256', 'metadataSha256', 'nativeSha256'):
                report = passing_result()
                report['lanes'][lane_name]['builds'][1][key] = '7' * 64
                with self.subTest(lane=lane_name, key=key), self.assertRaises(gate.DiagnosticFailure):
                    gate.validate_export(report, provenance())
        report = passing_result()
        report['candidateVerified'] = False
        with self.assertRaises(gate.DiagnosticFailure):
            gate.validate_export(report, provenance())

    def test_unknown_fields_and_private_strings_are_rejected_at_every_depth(self):
        for location in ('report', 'lanes', 'lane', 'build', 'macho', 'signature', 'process', 'stdout', 'stderr', 'provenance'):
            for payload in (True, PRIVATE):
                report, proof = passing_result(), provenance()
                item = report['lanes']['candidate']['builds'][0]
                target = {'report': report, 'lanes': report['lanes'], 'lane': report['lanes']['candidate'],
                          'build': item, 'macho': item['macho'], 'signature': item['macho']['signature'],
                          'process': item['version'], 'stdout': item['version']['stdout'],
                          'stderr': item['version']['stderr'], 'provenance': proof}[location]
                target['extraField'] = payload
                with self.subTest(location=location, payload=payload), self.assertRaises(gate.DiagnosticFailure):
                    gate.validate_export(report, proof)
        for field in ('state', 'stdout', 'stderr'):
            report = passing_result()
            value = report['lanes']['candidate']['builds'][0]['version']
            if field == 'state': value[field] = PRIVATE
            elif field == 'stdout': value[field]['versionText'] = PRIVATE
            else: value[field]['diagnostics'] = [PRIVATE]
            with self.subTest(field=field), self.assertRaises(gate.DiagnosticFailure):
                gate.validate_export(report, provenance())

    def test_wrong_types_hashes_bounds_and_fixed_provenance_are_rejected(self):
        cases = [lambda r, p: r.update(schema=True),
                 lambda r, p: r.update(sourceVerified=1),
                 lambda r, p: r.update(releaseValidation=True),
                 lambda r, p: r['lanes']['candidate']['builds'][0].update(binarySha256='A' * 64),
                 lambda r, p: r['lanes']['candidate']['builds'][0]['version'].update(groupAbsent=1),
                 lambda r, p: r['lanes']['candidate']['builds'][0]['version']['stdout'].update(capturedBytes=-1),
                 lambda r, p: r['lanes']['candidate']['builds'][0]['version']['stdout'].update(observedBytes=0),
                 lambda r, p: r['lanes']['candidate']['builds'][0]['version']['stdout'].update(sha256='a' * 40),
                 lambda r, p: p.update(baselineCommit='c' * 40),
                 lambda r, p: p.update(baselineTree='c' * 40),
                 lambda r, p: p.update(repository='other/project'),
                 lambda r, p: p.update(workflow='.github/workflows/ci.yml'),
                 lambda r, p: p.update(job='native'),
                 lambda r, p: p.update(changedFiles=['internal/core/app.go']),
                 lambda r, p: p.update(runId=0)]
        for index, mutation in enumerate(cases):
            report, proof = passing_result(), provenance()
            mutation(report, proof)
            with self.subTest(index=index), self.assertRaises(gate.DiagnosticFailure):
                gate.validate_export(report, proof)


class BoundedCaptureTests(unittest.TestCase):
    def run_python(self, source, **kwargs):
        return gate.capture([sys.executable, '-B', '-c', source], timeout=3, **kwargs)

    def test_success_nonzero_and_exclusive_process_group_are_observed(self):
        state, code, stdout, stderr, totals, absent = self.run_python(
            'import os, sys; print(os.getpid(), os.getpgrp()); sys.stderr.write("fixture\\n")')
        self.assertEqual((state, code, stderr, absent), ('completed', 0, b'fixture\n', True))
        pid, group = map(int, stdout.split())
        self.assertEqual(pid, group)
        self.assertTrue(gate.group_absent(pid))
        self.assertEqual(totals, [len(stdout), len(stderr)])
        observation = self.run_python('import sys; sys.stdout.write("fixture"); sys.exit(7)')
        self.assertEqual(observation, ('nonzero', 7, b'fixture', b'', [7, 0], True))

    def test_timeout_kills_and_reaps_owned_group(self):
        observation = gate.capture([sys.executable, '-B', '-c', 'import time; time.sleep(2)'], timeout=0.08)
        self.assertEqual(observation[0], 'timeout')
        self.assertEqual(observation[1], -signal.SIGKILL)
        self.assertTrue(observation[5])

    def test_each_stream_is_bounded_and_limit_stops_child(self):
        for descriptor in (1, 2):
            observation = self.run_python('import os, time; os.write(' + str(descriptor) + ', b"x" * 8192); time.sleep(2)', limit=128)
            self.assertEqual(observation[0], 'output_limit')
            self.assertEqual(len(observation[descriptor + 1]), 128)
            self.assertGreater(observation[4][descriptor - 1], 128)
            self.assertTrue(observation[5])

    def test_launch_failure_and_group_probe_fail_closed(self):
        with tempfile.TemporaryDirectory() as temporary:
            observation = gate.capture([str(Path(temporary) / 'does-not-exist')])
        self.assertEqual(observation, ('launch_failed', None, b'', b'', [0, 0], True))
        for effect, expected in ((ProcessLookupError, True), (PermissionError, False), (None, False)):
            with patch.object(gate.os, 'killpg', side_effect=effect):
                self.assertEqual(gate.group_absent(123), expected)

    def test_checked_failure_keeps_only_sanitized_process_evidence(self):
        observation = ('nonzero', 7, PRIVATE.encode(), PRIVATE.encode(), [len(PRIVATE), len(PRIVATE)], True)
        with patch.object(gate, 'capture', return_value=observation), self.assertRaises(gate.DiagnosticFailure) as raised:
            gate.checked(['synthetic-command'], failure='build_failed')
        self.assertEqual(raised.exception.failure, 'build_failed')
        self.assertNotIn(PRIVATE, json.dumps(raised.exception.process))


if __name__ == '__main__':
    unittest.main()
