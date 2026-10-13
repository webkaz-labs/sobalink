"""Closed nearby diagnostic receipts and workflow, without network execution."""
import copy
import importlib.util
import json
from pathlib import Path
import unittest

SCRIPT = Path(__file__).with_name('ci-nearby-diagnostic.py')
SPEC = importlib.util.spec_from_file_location('ci_nearby_diagnostic', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


def event(action, case='manual', test=True, **fields):
    package, name = gate.CASES[case]
    return {'Action': action, 'Package': package, **({'Test': name} if test else {}), **fields}


def raw(events):
    return b'\n'.join(json.dumps(e).encode() for e in events)


def capability(status='verified', **changes):
    value = {'scope': 'isolated_loopback', 'status': status,
             'reason': 'bidirectional_scoped_mdns' if status == 'verified' else 'native_receive_timeout',
             'bidirectionalCandidates': status == 'verified', 'ownersJoined': True}
    value.update(changes)
    return value


def events(case='manual', terminal='pass', marker=None):
    rows = [event('start', case, False), event('run', case)]
    if marker is not None:
        rows.append(event('output', case, Output='    fixture_test.go:1: ' + gate.MARKER + json.dumps(marker) + '\n'))
    rows.extend([event(terminal, case), event('pass', case, False)])
    return rows


class NearbyDiagnosticTests(unittest.TestCase):
    def test_untracked_acceptance_inputs_cannot_retain_exact_head_success(self):
        allowed = b'web/node_modules/react/index.js\0.sobalink-deps/tailscale/tsnet/tsnet.go\0.sobalink-deps/wireguard/device/device.go\0.sobalink-deps/gvisor/pkg/tcpip/tcpip.go\0'
        gate.validate_untracked(allowed)
        gate.validate_untracked(allowed + b'web/tsconfig.tsbuildinfo\0', after_build=True)
        for path in ('internal/core/extra.go', 'internal/core/extra_test.go',
                     'internal/nearby/extra_test.go', 'web/browser/extra.acceptance.mjs',
                     'web/src/extra.ts', 'web/src/node_modules/shadow/index.js',
                     '.github/scripts/extra.py', '.github/scripts/__pycache__/ci_diagnostic.cpython-312.pyc',
                     '.sobalink-deps/extra.go', '.sobalink-deps/tailscale-shadow/extra.go',
                     'web/node_modules-shadow/extra.mjs', 'web/node_modules',
                     'web/node_modules/../src/extra.ts', 'web/tsconfig.tsbuildinfo/extra.js'):
            for after in (False, True):
                with self.subTest(path=path, after_build=after), self.assertRaises(ValueError):
                    gate.validate_untracked(allowed + path.encode() + b'\0', after_build=after)
        with self.assertRaises(ValueError):
            gate.validate_untracked(b'web/tsconfig.tsbuildinfo\0')
        with self.assertRaises(ValueError):
            gate.validate_untracked(b'internal/core/extra.go')

    def test_complete_untracked_inventory_is_checked_at_each_boundary(self):
        source = SCRIPT.read_text()
        self.assertIn("['git', 'ls-files', '--others', '-z']", source)
        self.assertNotIn('--exclude-standard', source)
        self.assertIn('exec(compile(path.read_bytes()', source)
        self.assertIn('sys.dont_write_bytecode = True', source)
        self.assertEqual(source.count('require_source_inventory(log, after_build=True)'), 2)
        self.assertLess(source.index('        require_source_inventory(log)\n'), source.index("inventory = read(['git', 'ls-files'])"))
        self.assertLess(source.index('        require_source_inventory(log, after_build=True)\n'), source.index("run(['go', 'test', '-race'"))
        self.assertGreater(source.rindex('        require_source_inventory(log, after_build=True)\n'), source.index("for case, (package, name) in CASES.items()"))
        job = (gate.ROOT / '.github/workflows/activation-diagnostic.yml').read_text().split('  diagnostic-nearby:\n', 1)[1]
        self.assertIn('python -B -m unittest', job)
        self.assertIn('run: python -B .github/scripts/ci-nearby-diagnostic.py', job)

    def test_manual_and_browser_require_exact_once_run_and_pass(self):
        for case in ('manual', 'browser'):
            self.assertIs(gate.test_events(raw(events(case)), case, 0), True)
            for bad in ([], events(case, 'skip'), events(case, 'fail'), events(case) + events(case),
                        events(case)[1:], events(case)[:-1],
                        [event('start', case, False), event('pass', case), event('pass', case, False)]):
                with self.subTest(case=case, bad=bad), self.assertRaises(ValueError):
                    gate.test_events(raw(bad), case, 0)
            with self.assertRaises(ValueError):
                gate.test_events(raw(events(case)), case, 1)

    def test_wrong_package_subtest_or_case_cannot_substitute(self):
        for change in ({'Package': 'example.test/other'}, {'Test': 'TestOther'},
                       {'Test': gate.CASES['manual'][1] + '/child'}):
            rows = events()
            rows[1].update(change)
            with self.assertRaises(ValueError):
                gate.test_events(raw(rows), 'manual', 0)
        with self.assertRaises(ValueError):
            gate.test_events(raw(events()), 'other', 0)

    def test_capability_verified_requires_both_proofs_and_pass(self):
        record = capability()
        self.assertEqual(gate.test_events(raw(events('discovery', marker=record)), 'discovery', 0), record)
        for changes in ({'ownersJoined': False}, {'bidirectionalCandidates': False},
                        {'reason': 'native_receive_timeout'}, {'scope': 'physical_lan'}):
            with self.assertRaises(ValueError):
                gate.capability(dict(record, **changes))
        with self.assertRaises(ValueError):
            gate.test_events(raw(events('discovery', 'skip', record)), 'discovery', 0)

    def test_unavailable_skip_is_classified_without_native_proof(self):
        record = capability('unavailable')
        got = gate.test_events(raw(events('discovery', 'skip', record)), 'discovery', 0)
        self.assertEqual(got['status'], 'unavailable')
        self.assertFalse(got['bidirectionalCandidates'])
        for reason in gate.UNAVAILABLE:
            self.assertEqual(gate.capability(dict(record, reason=reason))['status'], 'unavailable')
        for terminal in ('pass', 'fail'):
            with self.assertRaises(ValueError):
                gate.test_events(raw(events('discovery', terminal, record)), 'discovery', 0)
        with self.assertRaises(ValueError):
            gate.capability(dict(record, bidirectionalCandidates=True))

    def test_missing_duplicate_opt_out_and_failed_markers_do_not_pass(self):
        good = events('discovery', marker=capability())
        bad = [events('discovery'), good[:3] + [good[2]] + good[3:],
               events('discovery', 'skip', capability('not_run', reason='opt_in_required', ownersJoined=False)),
               events('discovery', 'fail', capability('failed', reason='cleanup_not_joined', ownersJoined=False))]
        for rows in bad:
            with self.assertRaises(ValueError):
                gate.test_events(raw(rows), 'discovery', 0)
        with self.assertRaises(ValueError):
            gate.test_events(raw(events('manual', marker=capability())), 'manual', 0)

    def test_capability_exports_only_closed_typed_fields(self):
        record = capability()
        for changes in ({'secret': 'synthetic-private'}, {'status': 'unrecognized'}, {'reason': 'private-output'},
                        {'ownersJoined': 1}, {'bidirectionalCandidates': 1}, {'status': []}):
            with self.assertRaises(ValueError):
                gate.capability(dict(record, **changes))
        for key in record:
            reduced = dict(record)
            del reduced[key]
            with self.assertRaises(ValueError):
                gate.capability(reduced)

    def test_json_duplicates_and_nonfinite_values_are_rejected(self):
        for text in ('{"a":1,"a":2}', '{"a":NaN}', '{"a":Infinity}'):
            with self.assertRaises(ValueError):
                gate.parse_json(text)
        for text in (b'not-json', b'{"Action":"pass","Action":"pass","Package":"a"}'):
            with self.assertRaises(ValueError):
                gate.test_events(text, 'manual', 0)

    def test_browser_receipt_requires_all_fixed_evidence(self):
        expected = copy.deepcopy(gate.BROWSER_EXPECTED)
        self.assertEqual(gate.browser_summary(expected), expected)
        for key in expected:
            reduced = copy.deepcopy(expected)
            del reduced[key]
            with self.assertRaises(ValueError):
                gate.browser_summary(reduced)
        for changes in ({'schema': True}, {'cores': True}, {'nativeMulticastVerified': True},
                        {'receivingTrustUnchanged': False}, {'approvalResponseLost': False},
                        {'coreChildrenJoined': False}, {'path': 'private-path'},
                        {'locales': ['en', 'ja', 'extra']}, {'widths': [True, 320]},
                        {'private': 'synthetic'}):
            with self.assertRaises(ValueError):
                gate.browser_summary(dict(expected, **changes))

    def test_failure_evidence_is_closed_and_separate_from_success(self):
        record = {'schema': 1, 'phase': 'fingerprint-b', 'errorKind': 'assertion'}
        self.assertEqual(gate.browser_failure(record), record)
        for phase in gate.BROWSER_PHASES:
            for kind in gate.BROWSER_ERROR_KINDS:
                self.assertEqual(gate.browser_failure(dict(record, phase=phase, errorKind=kind))['phase'], phase)
        for changes in ({'schema': True}, {'phase': 'synthetic-private-endpoint'},
                        {'errorKind': 'synthetic-private-body'}, {'message': 'synthetic-private-token'},
                        {'selector': 'synthetic-private-text'}, {'stack': 'synthetic-private-path'},
                        {'phase': []}, {'errorKind': None}, {'accepted': True}):
            with self.assertRaises(ValueError):
                gate.browser_failure(dict(record, **changes))
        source = SCRIPT.read_text()
        self.assertIn("if (private / 'browser-failure.json').exists():", source)
        self.assertIn("report['browserFailure'] = browser_failure", source)
        self.assertIsNone(gate.empty_result()['browserFailure'])

    def test_failure_enums_match_browser_and_native_export(self):
        import re
        js = (gate.ROOT / 'web/browser/nearby-native-diagnostics.mjs').read_text()
        phases = set(re.findall(r"'([^']+)'", js.split('export const PHASES = Object.freeze([', 1)[1].split('])', 1)[0]))
        kinds = set(re.findall(r"'([^']+)'", js.split('export const ERROR_KINDS = Object.freeze([', 1)[1].split('])', 1)[0]))
        self.assertEqual(phases, gate.BROWSER_PHASES)
        self.assertEqual(kinds, gate.BROWSER_ERROR_KINDS)
        native = (gate.ROOT / 'internal/core/setup_browser_fixture_integration_test.go').read_text()
        native_phases = set(re.findall(r'"([^"]+)"', native.split('switch v.Phase {', 1)[1].split('default:', 1)[0]))
        native_kinds = set(re.findall(r'"([^"]+)"', native.split('switch v.ErrorKind {', 1)[1].split('default:', 1)[0]))
        self.assertEqual(phases, native_phases)
        self.assertEqual(kinds, native_kinds)
        for phase in phases:
            for kind in kinds:
                record = {'schema': 1, 'phase': phase, 'errorKind': kind}
                self.assertEqual(gate.browser_failure(record), record)
        self.assertIn('os.O_WRONLY|os.O_CREATE|os.O_EXCL', native)
        self.assertIn('decoder.DisallowUnknownFields()', native)
        self.assertIn('info.Size() > 1024', native)
        fixture = (gate.ROOT / 'web/browser/nearby-native-fixtures.mjs').read_text()
        self.assertLess(fixture.index('if (testInfo.errors.length) failure(testInfo.errors[0])'), fixture.index("currentPhase = 'cleanup'"))
        reporter = (gate.ROOT / 'web/browser/nearby-native-reporter.mjs').read_text()
        self.assertIn('extends TransferNativeReporter', reporter)
        self.assertNotIn('console.', reporter)
        self.assertNotIn('error.message', reporter)

    def test_failed_default_makes_no_acceptance_claim(self):
        result = gate.empty_result()
        for key in ('diagnosticPassed', 'manualCorePassed', 'browserPassed', 'nativeDiscoveryVerified', 'physicalLANVerified', 'releaseValidation'):
            self.assertIs(result[key], False)
        self.assertIsNone(result['nativeDiscovery'])
        self.assertIsNone(result['browser'])

    def test_nearby_ref_repeats_exact_head_guard_in_runner(self):
        selector = gate.module('ci-diagnostic')
        sha = 'a' * 40
        ref = 'diagnostic/nearby/' + sha[:12] + '-attempt1'
        args = dict(event_name='create', event={'ref_type': 'branch', 'ref': ref,
                    'repository': {'full_name': selector.REPOSITORY, 'private': False}},
                    repository=selector.REPOSITORY, ref='refs/heads/' + ref, sha=sha, checkout_sha=sha)
        self.assertEqual(selector.select(**args), 'nearby')
        for change in ({'checkout_sha': 'b' * 40}, {'sha': 'b' * 40}, {'event_name': 'push'},
                       {'ref': args['ref'] + '/other'}, {'ref': args['ref'].replace('attempt1', 'UPPER')}):
            with self.assertRaises(ValueError):
                selector.select(**dict(args, **change))
        source = SCRIPT.read_text()
        self.assertIn("selector.select(os.environ['GITHUB_EVENT_NAME']", source)
        self.assertIn("commit) != 'nearby'", source)

    def test_single_ubuntu_lane_preserves_pins_and_permission_floor(self):
        workflow = (gate.ROOT / '.github/workflows/activation-diagnostic.yml').read_text()
        job = workflow.split('  diagnostic-nearby:\n', 1)[1]
        self.assertIn("if: needs.diagnostic-selection.outputs.suite == 'nearby'", job)
        self.assertIn('runs-on: ubuntu-24.04', job)
        self.assertEqual(job.count('runs-on:'), 1)
        self.assertNotIn('matrix:', job)
        self.assertIn('persist-credentials: false', job)
        for pin in ('actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1',
                    'actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16',
                    'actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1',
                    'actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a'):
            self.assertIn(pin, job)
        for forbidden in ('continue-on-error:', 'contents: write', 'id-token:', 'secrets.', 'actions/cache', 'ip route', 'iptables', 'sysctl', 'chmod 777'):
            self.assertNotIn(forbidden, job)
        self.assertIn('contents: read', job)
        self.assertIn('go-version: \'1.27.1\'', job)
        self.assertIn('node-version: \'24.19.0\'', job)
        self.assertIn('npm@11.9.0', job)

    def test_only_reconstructed_literal_artifacts_are_uploaded(self):
        workflow = (gate.ROOT / '.github/workflows/activation-diagnostic.yml').read_text().split('  diagnostic-nearby:\n', 1)[1]
        artifact = workflow.split('uses: actions/upload-artifact', 1)[1]
        for file in ('soba-nearby-sanitized/result.json', 'soba-nearby-sanitized/provenance.json'):
            self.assertIn(file, artifact)
        for forbidden in ('*.json', 'private', 'playwright-results', 'screenshots', 'trace.zip'):
            self.assertNotIn(forbidden, artifact)
        self.assertIn("if: always() && steps.nearby.outputs.sanitized_ready == 'true'", workflow)
        source = SCRIPT.read_text()
        self.assertNotIn('str(exc)', source)
        self.assertIn("json.dumps(report, sort_keys=True)", source)
        self.assertIn("GOPROXY='off'", source)
        self.assertIn("'-c', '-o', str(binary), package", source)
        self.assertIn("'-test.run=^' + name + '$'", source)

    def test_assets_are_source_reproduced_before_compilation(self):
        source = SCRIPT.read_text()
        self.assertLess(source.index("run(['npm', 'run', 'build']"), source.index("run(['go', 'test', '-race'"))
        self.assertIn("assets(web / 'dist') != asset_hashes", source)
        self.assertIn("before != {name: digest(ROOT / name) for name in inventory}", source)
        fixture = (gate.ROOT / 'internal/core/route_process_integration_test.go').read_text()
        self.assertIn('assets, err := webembed.Assets()', fixture)
        self.assertIn('c.StartWeb(assets)', fixture)

    def test_existing_browser_sandbox_and_csp_baseline_is_reused(self):
        config = (gate.ROOT / 'web/playwright.nearby-native.config.mjs').read_text()
        self.assertIn("import existing from './playwright.transfer-native.config.mjs'", config)
        self.assertNotIn('chromiumSandbox', config)
        self.assertNotIn('--no-sandbox', config)
        fixture = (gate.ROOT / 'web/browser/nearby-native-fixtures.mjs').read_text()
        self.assertIn('script-src \'self\'', fixture)
        self.assertIn("url.hostname === '127.0.0.1'", fixture)
        self.assertIn('(info.mode & 0o077) === 0', fixture)
        self.assertNotIn('ignoreHTTPSErrors', fixture)


if __name__ == '__main__':
    unittest.main()
