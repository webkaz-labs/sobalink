"""Synthetic text diagnostic policy checks; never launch sockets or Chromium."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('ci-text-diagnostic.py')
SPEC = importlib.util.spec_from_file_location('ci_text_diagnostic', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), gate.__dict__)


def markers(case, phases=None):
    source = (gate.ROOT / 'internal/core' / gate.FIXTURE_FILES[case]).read_text().splitlines()
    return [{'Action': 'output', 'Package': gate.PACKAGE, 'Test': gate.CASES[case],
             'Output': '    ' + gate.FIXTURE_FILES[case] + ':' + str(next(
                 i for i, line in enumerate(source, 1) if line.strip() == 't.Log("SOBA_TEXT_PHASE ' + phase + '")'))
             + ': SOBA_TEXT_PHASE ' + phase + '\n'} for phase in (gate.FIXTURE_PHASES[case] if phases is None else phases)]


def events(case='restart', phases=None):
    package = {'Package': gate.PACKAGE}
    test = {**package, 'Test': gate.CASES[case]}
    return [{'Action': 'start', **package}, {'Action': 'run', **test},
            *markers(case, phases),
            {'Action': 'output', **test, 'Output': 'synthetic-private-body-id-path-token\n'},
            {'Action': 'pass', **test}, {'Action': 'pass', **package}]


def raw(rows):
    return b''.join(json.dumps(row).encode() + b'\n' for row in rows)


class TextDiagnosticTests(unittest.TestCase):
    def test_exact_cases_require_ordered_once_run_pass_and_zero_exit(self):
        self.assertEqual(tuple(gate.CASES.values()), ('TestDurableTextProcessRestartACKLossAndHiddenReplay',
                                                    'TestDurableTextThreeProcessBrowser'))
        for case in gate.CASES:
            good = events(case)
            self.assertIs(gate.test_events(raw(good), case, 0), True)
            for rows in ([], good[1:], good[:-1], good + good, good[:1] + good[2:],
                         [good[1], good[0], *good[2:]], good[:3] + [good[-1], good[-2]],
                         good[:2] + [good[1]] + good[2:], good + [good[2]]):
                with self.subTest(case=case, rows=rows), self.assertRaises(ValueError):
                    gate.test_events(raw(rows), case, 0)
            for status in (1, 2, 66, -9, False, None, '0'):
                with self.subTest(status=status), self.assertRaises(ValueError):
                    gate.test_events(raw(good), case, status)

    def test_skips_failures_extras_and_other_packages_cannot_pass(self):
        for mutation in ({'Action': 'skip'}, {'Action': 'fail'}, {'Action': 'pause'}, {'Action': 'cont'},
                         {'Test': 'TestOther'}, {'Test': gate.CASES['restart'] + '/subtest'},
                         {'Package': 'example.test/other'}):
            rows = events()
            rows[-2].update(mutation)
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                gate.test_events(raw(rows), 'restart', 0)
        for extra in ({'Action': 'build-output', 'ImportPath': 'example.test/pkg', 'Output': 'private'},
                      {'Action': 'build-fail', 'ImportPath': 'example.test/pkg'},
                      {'Action': 'run', 'Package': gate.PACKAGE, 'Test': 'TestOther'}):
            with self.assertRaises(ValueError):
                gate.test_events(raw(events()[:2] + [extra] + events()[2:]), 'restart', 0)
        with self.assertRaises(ValueError):
            gate.test_events(raw(events()), 'nearby', 0)

    def test_malformed_duplicate_truncated_and_oversized_events_fail_closed(self):
        good = raw(events())
        for candidate in (good[:-1], good[:-3], good + b'{\n', b'\xff\n', b'[]\n', b'null\n',
                          good.replace(b'"Action": "run"', b'"Action": "run", "Action": "pass"'),
                          good.replace(b'"Action": "run"', b'"Action": "run", "Elapsed": NaN'),
                          good.replace(b'"Action": "run"', b'"Action": "run", "Elapsed": true'),
                          good.replace(b'"Output":', b'"Output": 1, "extra":'),
                          b'x' * gate.CAPTURE_BYTES + b'\n', good.decode()):
            with self.subTest(candidate_type=type(candidate)), self.assertRaises((ValueError, UnicodeError)):
                gate.test_events(candidate, 'restart', 0)

    def test_phase_markers_are_source_bound_ordered_complete_and_test_owned(self):
        for case in gate.CASES:
            for count in range(1, len(gate.FIXTURE_PHASES[case]) + 1):
                phases = gate.FIXTURE_PHASES[case][:count]
                rows = events(case, phases)
                self.assertEqual(gate.phase_sequence(raw(rows), case), list(phases))
                if count != len(gate.FIXTURE_PHASES[case]):
                    with self.assertRaises(ValueError):
                        gate.test_events(raw(rows), case, 0)
            rows = events(case)
            invalid = [events(case, []), events(case, gate.FIXTURE_PHASES[case][1:]),
                       events(case, [gate.FIXTURE_PHASES[case][0]] * 2),
                       events(case, list(reversed(gate.FIXTURE_PHASES[case]))),
                       rows[:1] + [rows[2], rows[1]] + rows[3:], rows + [rows[2]]]
            for changes in ({'Test': 'TestOther'}, {'Test': None}, {'Action': 'pass'},
                            {'Output': rows[2]['Output'].replace(': SOBA', '0: SOBA')},
                            {'Output': rows[2]['Output'].replace('start', 'synthetic-private')},
                            {'Output': rows[2]['Output'] + 'synthetic-private'},
                            {'Output': rows[2]['Output'] * 2}):
                modified = events(case)
                modified[2].update(changes)
                invalid.append(modified)
            for candidate in invalid:
                with self.assertRaises(ValueError):
                    gate.phase_sequence(raw(candidate), case)

    def test_fixed_browser_receipt_requires_every_exact_typed_field(self):
        good = gate.summary.TEXT_EXPECTED
        self.assertEqual(gate.summary.text_summary(good), good)
        for key, value in good.items():
            for changed in (None, 1 if type(value) is bool else 'synthetic-private',
                            False if value is True else [], 999):
                with self.subTest(key=key, changed=changed), self.assertRaises(ValueError):
                    gate.summary.text_summary(dict(good, **{key: changed}))
            missing = dict(good)
            del missing[key]
            with self.assertRaises(ValueError):
                gate.summary.text_summary(missing)
        with self.assertRaises(ValueError):
            gate.summary.text_summary(dict(good, body='synthetic-private'))
        report = gate.empty_result()
        report['browser'] = dict(good)
        with self.assertRaises(ValueError):
            gate.fixed_result(report)
        report['browserPassed'] = True
        with self.assertRaises(ValueError):
            gate.fixed_result(report)

    def test_layout_evidence_has_exact_closed_regions_and_bounded_numbers(self):
        source = (gate.ROOT / 'web/browser/text-native-layout.mjs').read_text().split('].map(pair', 1)[0]
        self.assertEqual(tuple(re.findall(r"\['([^']+)',", source)), gate.LAYOUT_ROLES)
        def layout():
            return {'viewportWidth': 320, 'regions': [dict(role=role, clientWidth=300, scrollWidth=341,
                leftOverflow=0, rightOverflow=21) for role in gate.LAYOUT_ROLES]}
        def failure():
            return {'schema': 1, 'phase': 'outbox-fit-ja', 'errorKind': 'assertion', 'layout': layout()}
        value = failure()
        self.assertEqual(gate.browser_failure(value), value)
        self.assertIsNot(gate.browser_failure(value)['layout']['regions'][0], value['layout']['regions'][0])
        for bad in (True, -1, 8193, 1.5, float('inf'), float('nan'), '320', None):
            for field in ('clientWidth', 'scrollWidth', 'leftOverflow', 'rightOverflow'):
                changed = failure()
                changed['layout']['regions'][0][field] = bad
                with self.subTest(field=field, bad=bad), self.assertRaises(ValueError):
                    gate.browser_failure(changed)
            changed = failure()
            changed['layout']['viewportWidth'] = bad
            with self.assertRaises(ValueError):
                gate.browser_failure(changed)
        changes = [lambda v: v.update(phase='detail-exact'), lambda v: v.update(errorKind='timeout'),
                   lambda v: v['layout'].update(text='synthetic-private'),
                   lambda v: v['layout'].update(regions=[]),
                   lambda v: v['layout']['regions'][0].update(role='synthetic-private'),
                   lambda v: v['layout']['regions'][1].update(role=gate.LAYOUT_ROLES[0]),
                   lambda v: v['layout']['regions'][0].update(body='synthetic-private'),
                   lambda v: v['layout']['regions'].reverse()]
        for change in changes:
            changed = failure()
            change(changed)
            with self.assertRaises(ValueError):
                gate.browser_failure(changed)
        code, exported, _, _ = self.simulate(failure='browser-process', browser_failure_payload=value)
        self.assertEqual(code, 1)
        self.assertEqual(exported['browserFailure'], value)
        code, exported, _, _ = self.simulate(failure='browser-process', source_drift=True, browser_failure_payload=value)
        self.assertEqual(code, 1)
        self.assertIsNone(exported['browserFailure'])

    def test_receipt_and_hash_inputs_reject_unsafe_file_types(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            regular = root / 'regular'
            regular.write_text(json.dumps(gate.summary.TEXT_EXPECTED))
            self.assertEqual(gate.summary.text_summary(gate.summary.read_fixed(regular, 8192)), gate.summary.TEXT_EXPECTED)
            self.assertRegex(gate.digest(regular), r'^[0-9a-f]{64}$')
            symlink = root / 'symlink'
            symlink.symlink_to(regular)
            directory = root / 'directory'
            directory.mkdir()
            fifo = root / 'fifo'
            os.mkfifo(fifo)
            for path in (symlink, directory, fifo):
                for reader in (gate.digest, lambda p: gate.summary.read_fixed(p, 8192)):
                    with self.subTest(path=path.name), self.assertRaises(ValueError):
                        reader(path)
            hardlink = root / 'hardlink'
            os.link(regular, hardlink)
            for reader in (gate.digest, lambda p: gate.summary.read_fixed(p, 8192)):
                with self.assertRaises(ValueError):
                    reader(hardlink)
            hardlink.unlink()
            regular.write_bytes(b'x' * 8193)
            with self.assertRaises(ValueError):
                gate.summary.read_fixed(regular, 8192)
            regular.write_text('{"accepted":true,"accepted":true}')
            with self.assertRaises(ValueError):
                gate.summary.read_fixed(regular, 8192)
            asset = root / 'assets'
            asset.mkdir()
            (asset / 'index.html').write_text('synthetic')
            self.assertEqual(set(gate.assets(asset)), {'index.html'})
            (asset / 'bad').symlink_to(regular)
            with self.assertRaises(ValueError):
                gate.assets(asset)
            (asset / 'bad').unlink()
            os.mkfifo(asset / 'bad')
            with self.assertRaises(ValueError):
                gate.assets(asset)

    def test_source_inventory_rejects_unsafe_names_ignored_inputs_and_drift(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'source').write_text('synthetic source')
            def inventory(names=b'source\0', others=b'', status=b''):
                values = {('git', 'status', '--porcelain', '--untracked-files=no'): status,
                          ('git', 'ls-files', '--others', '-z'): others,
                          ('git', 'ls-files', '-z'): names}
                with patch.object(gate, 'ROOT', root), patch.object(gate, 'checked', side_effect=lambda cmd: values[tuple(cmd)]):
                    return gate.source_inventory()
            before = inventory()
            for names in (b'', b'source', b'source\0source\0', b'../source\0', b'/source\0', b'x\\source\0'):
                with self.assertRaises(ValueError):
                    inventory(names=names)
            for other in (b'internal/core/extra_test.go\0', b'.github/scripts/__pycache__/extra.pyc\0',
                          b'web/src/extra.ts\0', b'web/node_modules-shadow/extra.mjs\0'):
                with self.assertRaises(ValueError):
                    inventory(others=other)
            with self.assertRaises(ValueError):
                inventory(status=b' M source\n')
            (root / 'source').write_text('changed')
            self.assertNotEqual(before, inventory())
            binary = root / 'core.test'
            binary.write_bytes(b'synthetic binary')
            assets = root / 'web/dist'
            assets.mkdir(parents=True)
            (assets / 'index.html').write_text('synthetic assets')
            expected_assets, expected_binary = gate.assets(assets), gate.digest(binary)
            with patch.object(gate, 'ROOT', root), patch.object(gate, 'source_inventory', return_value=before):
                gate.verify_source(before, expected_assets, expected_binary, binary)
                (assets / 'index.html').write_text('changed')
                with self.assertRaises(ValueError):
                    gate.verify_source(before, expected_assets, expected_binary, binary)
                (assets / 'index.html').write_text('synthetic assets')
                binary.write_bytes(b'changed')
                with self.assertRaises(ValueError):
                    gate.verify_source(before, expected_assets, expected_binary, binary)
            with patch.object(gate, 'source_inventory', return_value={'source': 'f' * 64}):
                with self.assertRaises(ValueError):
                    gate.verify_source(before, expected_assets, expected_binary, binary)

    def test_first_failure_is_finite_and_cannot_be_overwritten_by_source_check(self):
        report = gate.empty_result()
        gate.fail(report, 'restart-process')
        gate.fail(report, 'verify-source')
        self.assertEqual(report['firstFailure'], 'restart-process')
        self.assertEqual(gate.fixed_result(report), report)
        for value in ('synthetic-private', 'none', '', None):
            with self.assertRaises(ValueError):
                gate.fail(report, value)
        for changes in ({'firstFailure': 'synthetic-private'}, {'restartProcess': 'synthetic-private'},
                        {'browserPhase': 'synthetic-private'}, {'restartPhase': 1},
                        {'releaseValidation': True}, {'physicalLANVerified': True}, {'schema': True},
                        {'diagnosticPassed': True}, {'body': 'synthetic-private'}):
            with self.assertRaises(ValueError):
                gate.fixed_result(dict(gate.empty_result(), **changes))

    def simulate(self, *, failure=None, source_drift=False, browser_failure_payload=None):
        """Run only orchestration against synthetic files and mocked commands."""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'internal/core').mkdir(parents=True)
            for file in gate.FIXTURE_FILES.values():
                (root / 'internal/core' / file).write_bytes((gate.ROOT / 'internal/core' / file).read_bytes())
            runner = root / 'runner'
            runner.mkdir()
            web = root / 'web'
            (web / 'dist').mkdir(parents=True)
            (web / 'dist/index.html').write_text('synthetic')
            (web / 'package-lock.json').write_text('{}')
            for name in ('go', 'node', 'chromium'):
                (root / name).write_text('synthetic ' + name)
            sha = 'a' * 40
            ref = 'diagnostic/text/' + sha[:12] + '-synthetic'
            event = root / 'event.json'
            event.write_text(json.dumps({'ref_type': 'branch', 'ref': ref,
                'repository': {'full_name': 'webkaz-labs/sobalink', 'private': False}}))
            output = root / 'github-output'
            env = {'GITHUB_ACTIONS': 'true', 'RUNNER_TEMP': str(runner), 'GITHUB_EVENT_PATH': str(event),
                   'GITHUB_EVENT_NAME': 'create', 'GITHUB_REPOSITORY': 'webkaz-labs/sobalink',
                   'GITHUB_REF': 'refs/heads/' + ref, 'GITHUB_SHA': sha, 'GITHUB_OUTPUT': str(output),
                   'RUNNER_OS': 'Linux', 'RUNNER_ARCH': 'X64'}
            active = None
            calls = []
            def capture(cmd, **kwargs):
                nonlocal active
                calls.append((cmd, kwargs))
                data = b''
                if cmd[:3] == ['git', 'rev-parse', 'HEAD']:
                    data = sha.encode()
                elif cmd[:2] == ['git', 'rev-parse']:
                    data = b'b' * 40
                elif cmd == ['go', 'env', 'GOVERSION']:
                    data = b'go1.27.1'
                elif cmd == ['node', '--version']:
                    data = b'v24.19.0'
                elif cmd == ['npm', '--version']:
                    data = b'11.9.0'
                elif cmd[:3] == ['go', 'env', '-json']:
                    data = json.dumps({'GOVERSION': 'go1.27.1', 'GOHOSTOS': 'linux', 'GOHOSTARCH': 'amd64',
                                       'GOOS': 'linux', 'GOARCH': 'amd64', 'CGO_ENABLED': '1'}).encode()
                elif cmd[:2] == ['node', '--input-type=module']:
                    data = str(root / 'chromium').encode()
                elif cmd[:2] == ['go', 'test']:
                    Path(cmd[cmd.index('-o') + 1]).write_text('synthetic Core binary')
                elif cmd[0].endswith('core.test'):
                    active = next(case for case, test in gate.CASES.items() if '-test.run=^' + test + '$' in cmd)
                    if active == 'browser':
                        receipt = dict(gate.summary.TEXT_EXPECTED)
                        if failure == 'browser-receipt':
                            receipt['commitCalls'] = 2
                        Path(kwargs['env']['SOBA_TEXT_BROWSER_SUMMARY']).write_text(json.dumps(receipt))
                        if browser_failure_payload is not None:
                            Path(kwargs['env']['SOBA_TEXT_BROWSER_FAILURE']).write_text(json.dumps(browser_failure_payload))
                    if failure == active + '-process':
                        return 'nonzero', 1, b'synthetic-private-body-id-path-token', b'synthetic-private-error'
                    data = b'synthetic-private-body-id-path-token'
                elif cmd[:3] == ['go', 'tool', 'test2json']:
                    rows = events(active, gate.FIXTURE_PHASES[active][:5] if failure == active + '-process' else None)
                    if failure == active + '-process':
                        rows[-2]['Action'] = rows[-1]['Action'] = 'fail'
                    data = raw(rows)
                    if failure == active + '-events':
                        data = data[:-1]
                elif cmd != ['npm', 'run', 'build']:
                    raise AssertionError('unexpected synthetic command')
                return 'completed', 0, data, b''
            with patch.dict(os.environ, env, clear=True), patch.object(gate, 'ROOT', root), \
                 patch.object(gate.native, 'capture', side_effect=capture), \
                 patch.object(gate, 'source_inventory', return_value={'synthetic': 'c' * 64}), \
                 patch.object(gate.shutil, 'which', side_effect=lambda name: str(root / name)), \
                 patch.object(gate, 'verify_source', side_effect=ValueError('synthetic-private') if source_drift else None) as verified, \
                 contextlib.redirect_stdout(io.StringIO()) as printed:
                status = gate.main([])
            result = json.loads((runner / 'soba-text-sanitized/result.json').read_text())
            provenance = json.loads((runner / 'soba-text-sanitized/provenance.json').read_text())
            self.assertEqual(output.read_text(), 'sanitized_ready=true\n')
            exported = json.dumps([result, provenance]) + printed.getvalue()
            for private in ('synthetic-private', str(root), 'TestDurableText', 'internal/core', 'text-native-summary.json'):
                self.assertNotIn(private, exported)
            verified.assert_called_once()
            return status, result, provenance, calls

    def test_success_needs_both_real_case_gates_fixed_receipt_and_final_hashes(self):
        status, result, provenance, calls = self.simulate()
        self.assertEqual(status, 0)
        self.assertTrue(result['diagnosticPassed'])
        self.assertTrue(result['sourceVerified'])
        self.assertEqual(result['browser'], gate.summary.TEXT_EXPECTED)
        for case in gate.CASES:
            self.assertEqual(result[case + 'Phase'], 'complete')
        self.assertEqual(provenance['versions'], gate.VERSIONS)
        self.assertEqual(sum(cmd[:2] == ['go', 'test'] for cmd, _ in calls), 1)
        runs = [(cmd, kw) for cmd, kw in calls if cmd[0].endswith('core.test')]
        self.assertEqual(len(runs), 2)
        for cmd, kw in runs:
            self.assertIn('-test.timeout=4m', cmd)
            self.assertIn('-test.count=1', cmd)
            self.assertEqual(kw['timeout'], 270)
            self.assertEqual(kw['limit'], gate.CAPTURE_BYTES)
            self.assertEqual(kw['env']['SOBALINK_RUN_DIRECT_INTEGRATION'], '1')
            self.assertEqual(kw['env']['SOBALINK_RUN_TRANSFER_BROWSER_NATIVE'], '1')

    def test_failed_process_event_receipt_and_source_checks_never_publish_success(self):
        for failure in ('restart-process', 'restart-events', 'browser-process', 'browser-events', 'browser-receipt'):
            for drift in (False, True):
                with self.subTest(failure=failure, drift=drift):
                    status, result, _, _ = self.simulate(failure=failure, source_drift=drift)
                    self.assertEqual(status, 1)
                    self.assertFalse(result['diagnosticPassed'])
                    self.assertEqual(result['firstFailure'], failure)
                    self.assertEqual(result['sourceVerified'], not drift)
                    case = failure.split('-')[0]
                    expected_phase = 'unavailable' if drift or failure.endswith('-events') else gate.FIXTURE_PHASES[case][4] if failure.endswith('-process') else 'complete'
                    self.assertEqual(result[case + 'Phase'], expected_phase)
        status, result, _, _ = self.simulate(source_drift=True)
        self.assertEqual(status, 1)
        self.assertEqual(result['firstFailure'], 'verify-source')

    def test_fixed_browser_failure_is_closed_and_matches_text_reporter_phases(self):
        source = (gate.ROOT / 'web/browser/text-native-reporter.mjs').read_text()
        phases = source.split('PHASES = Object.freeze([', 1)[1].split('])', 1)[0]
        self.assertEqual(set(re.findall(r"'([^']+)'", phases)), gate.BROWSER_PHASES)
        for phase in gate.BROWSER_PHASES:
            for kind in gate.summary.nearby.BROWSER_ERROR_KINDS:
                value = {'schema': 1, 'phase': phase, 'errorKind': kind}
                self.assertEqual(gate.browser_failure(value), value)
                self.assertIsNot(gate.browser_failure(value), value)
        good = {'schema': 1, 'phase': 'detail-exact', 'errorKind': 'locator_missing'}
        for changes in ({'schema': True}, {'phase': 'synthetic-private'}, {'errorKind': 'synthetic-private'},
                        {'phase': []}, {'errorKind': None}, {'body': 'synthetic-private'}):
            with self.assertRaises(ValueError):
                gate.browser_failure(dict(good, **changes))
        for key in good:
            value = dict(good)
            del value[key]
            with self.assertRaises(ValueError):
                gate.browser_failure(value)
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'failure.json'
            for raw in (b'{"schema":1,"schema":1}', b'{', b'x' * 8193):
                path.write_bytes(raw)
                with self.assertRaises(ValueError):
                    gate.browser_failure(gate.summary.read_fixed(path, 8192))

    def test_first_browser_failure_is_retained_without_receipt_or_exit_override(self):
        record = {'schema': 1, 'phase': 'detail-exact', 'errorKind': 'locator_missing'}
        for failure in (None, 'browser-process'):
            status, result, _, _ = self.simulate(failure=failure, browser_failure_payload=record)
            self.assertEqual(status, 1)
            self.assertFalse(result['browserPassed'])
            self.assertIsNone(result['browser'])
            self.assertEqual(result['browserFailure'], record)
            self.assertEqual(result['firstFailure'], failure or 'browser-evidence')
        status, result, _, _ = self.simulate(failure='browser-process', browser_failure_payload=dict(record, body='synthetic-private'))
        self.assertEqual(status, 1)
        self.assertEqual(result['firstFailure'], 'browser-process')
        self.assertEqual(result['browserFailure'], {'schema': 1, 'phase': 'unavailable', 'errorKind': 'evidence_unavailable'})
        status, result, _, _ = self.simulate(failure='browser-process', browser_failure_payload=record, source_drift=True)
        self.assertEqual(status, 1)
        self.assertIsNone(result['browserFailure'])
        self.assertEqual(result['browserPhase'], 'unavailable')

    def test_literal_text_linux_job_reuses_pins_and_closed_uploads(self):
        workflow = (gate.ROOT / '.github/workflows/activation-diagnostic.yml').read_text()
        job = workflow.split('  diagnostic-text:\n', 1)[1].split('  diagnostic-dnssd:\n', 1)[0]
        self.assertNotRegex(job, re.compile(r'^  [a-z][a-z0-9-]*:', re.M))
        self.assertIn("if: needs.diagnostic-selection.outputs.suite == 'text'", job)
        self.assertIn('runs-on: ubuntu-24.04', job)
        self.assertEqual(job.count('runs-on:'), 1)
        for pin in ('actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1',
                    'actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16',
                    'actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1',
                    'actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a',
                    "go-version: '1.27.1'", "node-version: '24.19.0'", 'npm@11.9.0',
                    'persist-credentials: false', 'contents: read'):
            self.assertIn(pin, job)
        for forbidden in ('matrix:', 'nearby', 'continue-on-error:', 'actions/cache', 'secrets.',
                          'contents: write', 'id-token:', 'ci-required', 'ts_omit_udptransport'):
            self.assertNotIn(forbidden, job)
        artifact = job.split('uses: actions/upload-artifact', 1)[1]
        self.assertIn("if: always() && steps.text.outputs.sanitized_ready == 'true'", job)
        self.assertEqual(re.findall(r'\$\{\{ runner.temp \}\}/([^\n]+)', artifact),
                         ['soba-text-sanitized/result.json', 'soba-text-sanitized/provenance.json'])
        self.assertIn('go run ./cmd/prepare-engine --verify', job)
        self.assertIn('node web/node_modules/@playwright/test/cli.js install --with-deps chromium', job)

    def test_production_sources_deadlines_and_sandbox_baseline_are_reused(self):
        source = SCRIPT.read_text()
        self.assertIn("commit) != 'text'", source)
        self.assertIn('exec(compile(path.read_bytes()', source)
        self.assertIn('sys.dont_write_bytecode = True', source)
        self.assertIn("'-test.timeout=4m'", source)
        self.assertIn("'-race', '-tags=' + TAGS, '-c'", source)
        self.assertIn("GOPROXY='off'", source)
        self.assertLess(source.index("checked(['npm', 'run', 'build']"), source.index("checked(['go', 'test', '-race'"))
        self.assertIn('verify_source(before, asset_hashes, binary_hash, binary)', source)
        self.assertIn('summary.text_summary(summary.read_fixed(', source)
        config = (gate.ROOT / 'web/playwright.text-native.config.mjs').read_text()
        self.assertIn("import existing from './playwright.transfer-native.config.mjs'", config)
        self.assertNotIn('chromiumSandbox', config)
        self.assertNotIn('--no-sandbox', source)
        existing = (gate.ROOT / '.github/workflows/ci.yml').read_text()
        for test in gate.CASES.values():
            self.assertIn('-run \'^' + test + "$'", existing)


if __name__ == '__main__':
    unittest.main()
