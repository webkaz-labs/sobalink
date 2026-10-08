"""Source policy and mocked ownership tests; no acceptance runner is launched."""
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('product_gate', Path(__file__).with_name('ci-product-activation.py'))
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


class ProductActivationPolicyTests(unittest.TestCase):
    def passing(self):
        return dict(gate.failed_report(), accepted=True, allSelectedPassed=True,
                    runnerExitedSuccessfully=True, nativeCleanupProven=True)

    def test_exact_two_schema_and_failure(self):
        self.assertFalse(gate.validate_report(gate.failed_report())['accepted'])
        self.assertTrue(gate.validate_report(self.passing())['accepted'])
        self.assertEqual(gate.failed_report()['expected'], 2)
        self.assertEqual(gate.failed_report()['schema'], 3)
        self.assertEqual(set(self.passing()), gate.KEYS)
        for change in ({'expected': 7}, {'expected': True}, {'schema': True}, {'schema': 1}, {'schema': 2}, {'scope': 'other'},
                       {'allSevenPassed': True}, {'diagnostics': 'synthetic'}, {'timedOut': 0},
                       {'timedOut': True}, {'outputOverflow': True}, {'nativeCleanupProven': False},
                       {'allSelectedPassed': False}, {'runnerExitedSuccessfully': False}):
            with self.subTest(change=tuple(change)), self.assertRaises(ValueError):
                gate.validate_report(dict(self.passing(), **change))
        for key in gate.KEYS:
            value = self.passing()
            del value[key]
            with self.assertRaises(ValueError):
                gate.validate_report(value)
        for key in gate.BOOLS:
            with self.assertRaises(ValueError):
                gate.validate_report(dict(gate.failed_report(), **{key: 0}))

    def test_diagnostic_inventory_deep_reconstruction_and_closed_keys(self):
        value = gate.empty_diagnostics()
        copied = gate.validate_diagnostics(value)
        self.assertEqual(copied, value)
        self.assertEqual([row['id'] for row in copied['cases']], ['product-web', 'product-cli'])
        targets = [(), ('cases', 0), ('cases', 0, 'lifecycle'), ('cases', 0, 'lifecycle', 'native'), ('cases', 0, 'lifecycle', 'native', 'old')]
        for path in targets:
            for operation in ('add', 'remove'):
                bad = gate.empty_diagnostics()
                target = bad
                for part in path:
                    target = target[part]
                if operation == 'add':
                    target['rawException'] = 'synthetic-private-detail'
                else:
                    del target[next(iter(target))]
                with self.subTest(path=path, operation=operation), self.assertRaises(ValueError):
                    gate.validate_diagnostics(bad)
        copied['cases'][0]['lifecycle']['native']['old']['invalid'] = True
        self.assertFalse(value['cases'][0]['lifecycle']['native']['old']['invalid'])

    def test_diagnostic_case_order_duplicates_statuses_and_start_flags(self):
        for mutation in ('reverse', 'duplicate', 'short', 'long', 'unknown', 'passed-unstarted', 'coerced-start', 'both-flags'):
            value = gate.empty_diagnostics()
            cases = value['cases']
            if mutation == 'reverse': cases.reverse()
            if mutation == 'duplicate': cases[1] = cases[0]
            if mutation == 'short': cases.pop()
            if mutation == 'long': cases.append(cases[0])
            if mutation == 'unknown': cases[0]['status'] = 'synthetic-private-detail'
            if mutation == 'passed-unstarted':
                cases[0]['status'] = 'passed'
                value.update(observed=1, passed=1)
            if mutation == 'coerced-start': cases[0]['started'] = 1
            if mutation == 'both-flags': cases[0].update(diagnosticAvailable=True, diagnosticRejected=True)
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                gate.validate_diagnostics(value)

    def test_diagnostic_counts_reject_coercion_overflow_and_disagreement(self):
        for key, limit in (('globalErrors', 255), ('observed', 2), ('passed', 2)):
            for invalid in (True, False, -1, limit + 1, 1.0, '1', None):
                value = gate.empty_diagnostics()
                value[key] = invalid
                with self.subTest(key=key, invalid=invalid), self.assertRaises(ValueError):
                    gate.validate_diagnostics(value)
        for key in gate.LIFECYCLE_COUNTS:
            for invalid in (True, -1, 256, 1.0, '1'):
                value = gate.empty_lifecycle()
                value[key] = invalid
                with self.subTest(key=key, invalid=invalid), self.assertRaises(ValueError):
                    gate.validate_lifecycle(value)
        for key in ('observed', 'passed'):
            value = gate.empty_diagnostics()
            value[key] = 1
            with self.assertRaises(ValueError):
                gate.validate_diagnostics(value)

    def test_lifecycle_enums_booleans_and_native_roles_are_closed(self):
        for key in gate.LIFECYCLE_ENUMS:
            for invalid in ('synthetic-private-detail', '', 1, True, {}, []):
                value = gate.empty_lifecycle()
                value[key] = invalid
                with self.subTest(key=key), self.assertRaises(ValueError):
                    gate.validate_lifecycle(value)
        for key in gate.LIFECYCLE_BOOLS:
            value = gate.empty_lifecycle()
            value[key] = 1
            with self.assertRaises(ValueError):
                gate.validate_lifecycle(value)
        for key in gate.NATIVE_KEYS:
            value = gate.empty_lifecycle()
            value['native']['old'][key] = 'synthetic-private-detail'
            with self.subTest(key=key), self.assertRaises(ValueError):
                gate.validate_lifecycle(value)
        for change in ({'available': True, 'invalid': True}, {'stage': 'owner-start'}, {'failed': True}, {'ownerStatus': 'connected'}, {'peerStatus': 'connected'}):
            value = gate.empty_lifecycle()
            value['native']['old'].update(change)
            with self.subTest(change=change), self.assertRaises(ValueError):
                gate.validate_lifecycle(value)
        value = gate.empty_lifecycle()
        value['native']['old']['invalid'] = True
        self.assertTrue(gate.validate_lifecycle(value)['native']['old']['invalid'])

    def test_work_and_cleanup_failures_stay_separate_and_evidence_only(self):
        value = self.passing()
        # Missing diagnostics and independently observed states cannot invent
        # a new owner/peer success predicate over the existing product gate.
        self.assertTrue(gate.validate_report(value)['accepted'])
        value.update(accepted=False, allSelectedPassed=False)
        diagnostics = value['diagnostics']
        diagnostics.update(summaryAvailable=True, selectionValid=True, observed=1)
        row = diagnostics['cases'][0]
        row.update(started=True, status='failed', diagnosticAvailable=True)
        row['lifecycle'].update(workPhase='restart-confirm', firstFailurePhase='review', workFailed=True,
                                cleanupPhase='complete', firstCleanupFailurePhase='proof', cleanupFailed=True)
        row['lifecycle']['native']['old'].update(available=True, stage='owner-status', ownerStatus='local-confirmed', peerStatus='connected')
        result = gate.validate_report(value)
        self.assertFalse(result['accepted'])
        life = result['diagnostics']['cases'][0]['lifecycle']
        self.assertEqual(life['firstFailurePhase'], 'review')
        self.assertEqual(life['firstCleanupFailurePhase'], 'proof')
        self.assertEqual(life['native']['old']['ownerStatus'], 'local-confirmed')
        failed = gate.failed_report(result['diagnostics'])
        self.assertFalse(failed['accepted'])
        self.assertEqual(failed['diagnostics'], result['diagnostics'])
        # Actual case failure contradicts allSelectedPassed even though the
        # independently observed owner/peer statuses remain evidence only.
        with self.assertRaises(ValueError):
            gate.validate_report(dict(value, accepted=True, allSelectedPassed=True))
        diagnostics.update(observed=2, passed=2)
        for case in diagnostics['cases']:
            case.update(started=True, status='passed')
        # Original case results now agree. Native statuses and separate work/
        # cleanup observations still do not introduce new success predicates.
        self.assertTrue(gate.validate_report(dict(value, accepted=True, allSelectedPassed=True))['accepted'])

    def test_available_case_summary_must_support_all_selected_passed(self):
        value = self.passing()
        value['diagnostics'].update(summaryAvailable=True, selectionValid=True, observed=2, passed=2)
        for case in value['diagnostics']['cases']:
            case.update(started=True, status='passed')
        self.assertTrue(gate.validate_report(value)['accepted'])
        for change in ({'selectionValid': False}, {'unexpected': True}, {'globalErrors': 1}):
            bad = json.loads(json.dumps(value))
            bad['diagnostics'].update(change)
            with self.subTest(change=change), self.assertRaises(ValueError):
                gate.validate_report(bad)

    def test_report_parser_rejects_duplicates_and_nonfinite_values(self):
        for raw in ('{"diagnostics": {}, "diagnostics": {}}', '{"nested": {"stage": "one", "stage": "two"}}', '{"value": NaN}', '{"value": Infinity}', '{"value": -Infinity}'):
            with self.assertRaises(ValueError):
                gate.strict_json(raw)
        self.assertEqual(gate.strict_json(json.dumps(gate.failed_report())), gate.failed_report())

    def test_browser_diagnostic_vocabulary_matches_python_exactly(self):
        source = (ROOT / 'web/browser/product-activation-diagnostics.mjs').read_text()
        constants = {'caseIds': gate.CASE_IDS, 'statuses': gate.STATUSES, 'workPhases': gate.WORK_PHASES,
                     'cleanupPhases': gate.CLEANUP_PHASES, 'exits': gate.EXITS, 'nativeStages': gate.NATIVE_STAGES,
                     'supervisorFailures': gate.SUPERVISOR_FAILURES, 'resourceFailures': gate.RESOURCE_FAILURES,
                     'nativeStatuses': gate.NATIVE_STATUSES, 'nativeRoles': gate.NATIVE_ROLES,
                     'booleanKeys': gate.LIFECYCLE_BOOLS, 'counterKeys': gate.LIFECYCLE_COUNTS}
        for name, expected in constants.items():
            match = re.search(r'export const ' + name + r' = Object\.freeze\(\[(.*?)\]\)', source, re.S)
            self.assertIsNotNone(match, name)
            self.assertEqual(tuple(re.findall(r"'([^']*)'", match.group(1))), expected, name)
        self.assertIn("export const failurePhases = Object.freeze(['none', 'unknown', ...workPhases])", source)
        self.assertIn("export const cleanupFailurePhases = Object.freeze(['none', 'unknown', ...cleanupPhases])", source)
        gate_source = Path(gate.__file__).read_text()
        self.assertIn('info.st_size > 16384', gate_source)
        self.assertIn('validate_report(strict_json(wrapper_report.read_text()))', gate_source)

    def test_cleanup_detail_fields_are_closed_observations_only(self):
        defaults = {'supervisorFailure': 'none', 'resourceFailure': 'none', 'helperExit': 'not-observed', 'successorExit': 'not-observed'}
        for key, default in defaults.items():
            self.assertEqual(gate.empty_lifecycle()[key], default)
            for allowed in gate.LIFECYCLE_ENUMS[key]:
                value = self.passing()
                value['diagnostics']['cases'][0]['lifecycle'][key] = allowed
                # Existing result and cleanup predicates alone determine pass.
                self.assertTrue(gate.validate_report(value)['accepted'])
            for invalid in ('synthetic-private-detail', 95, True, None, [], {}):
                value = gate.empty_lifecycle()
                value[key] = invalid
                with self.subTest(key=key, invalid=invalid), self.assertRaises(ValueError):
                    gate.validate_lifecycle(value)
            value = gate.empty_lifecycle()
            del value[key]
            with self.assertRaises(ValueError):
                gate.validate_lifecycle(value)

    def test_toolchain_exact(self):
        hashes = {'go': 'a' * 64, 'node': 'b' * 64}
        self.assertEqual(gate.validate_toolchain(gate.VERSIONS, hashes)['binarySha256'], hashes)
        self.assertEqual(gate.VERSIONS, {'go': 'go1.27.1', 'node': 'v24.19.0', 'npm': '11.9.0'})
        for versions in ({}, dict(gate.VERSIONS, npm='11.8.0'), dict(gate.VERSIONS, node=True)):
            with self.assertRaises(ValueError):
                gate.validate_toolchain(versions, hashes)
        for values in ({}, dict(hashes, go=True), dict(hashes, node='A' * 64), dict(hashes, path='synthetic')):
            with self.assertRaises(ValueError):
                gate.validate_toolchain(gate.VERSIONS, values)

    def test_playwright_lock_matches_each_installed_package(self):
        lock = json.loads((ROOT / 'web/package-lock.json').read_text())
        package = json.loads((ROOT / 'web/package.json').read_text())
        names = ('@playwright/test', 'playwright', 'playwright-core')
        installed = {name: dict(lock['packages']['node_modules/' + name], name=name) for name in names}
        version = gate.validate_playwright(lock, package, installed)
        self.assertEqual(version, package['devDependencies']['@playwright/test'])
        for name in names:
            bad = {key: dict(value) for key, value in installed.items()}
            bad[name]['version'] = '0.0.0'
            with self.assertRaises(ValueError):
                gate.validate_playwright(lock, package, bad)
        bad = json.loads(json.dumps(lock))
        bad['packages']['node_modules/playwright']['dependencies']['playwright-core'] = '^' + version
        with self.assertRaises(ValueError):
            gate.validate_playwright(bad, package, installed)
        bad = json.loads(json.dumps(package))
        bad['devDependencies']['@playwright/test'] = '^' + version
        with self.assertRaises(ValueError):
            gate.validate_playwright(lock, bad, installed)

    def test_chromium_revision_path_and_executable(self):
        with tempfile.TemporaryDirectory() as root:
            cache = Path(root).resolve()
            executable = cache / 'chromium-123' / 'chrome-linux64' / 'chrome'
            executable.parent.mkdir(parents=True)
            executable.write_bytes(b'synthetic executable fixture')
            executable.chmod(0o700)
            browsers = {'browsers': [{'name': 'chromium', 'revision': '123'}]}
            with mock.patch.object(gate.os, 'access', return_value=True):
                self.assertEqual(gate.validate_chromium(browsers, executable, cache), '123')
            for revision in ('124', 'other', True):
                with self.assertRaises(ValueError):
                    gate.validate_chromium({'browsers': [{'name': 'chromium', 'revision': revision}]}, executable, cache)
            # os.access(X_OK) is platform-specific; exercise the explicit
            # validator branch portably instead of relying on POSIX chmod.
            with mock.patch.object(gate.os, 'access', return_value=False), self.assertRaises(ValueError):
                gate.validate_chromium(browsers, executable, cache)

    def test_runtime_environment_is_allowlisted(self):
        with mock.patch.dict(os.environ, {'PATH': '/synthetic/bin', 'NODE_OPTIONS': 'synthetic', 'DEBUG': '1', 'PWDEBUG': '1', 'HTTPS_PROXY': 'synthetic', 'SECRET_TOKEN': 'synthetic'}):
            env = gate.child_environment(Path('/synthetic/private'), Path('/synthetic/binary'), Path('/synthetic/chromium'), Path('/synthetic/result'))
        self.assertEqual(set(env), {'PATH', 'HOME', 'TMPDIR', 'LANG', 'SOBA_PRODUCT_ACTIVATION_ACCEPTANCE', 'SOBA_WEB_ACTIVATION_BINARY', 'SOBA_ACTIVATION_CHROMIUM', 'SOBA_PRODUCT_ACTIVATION_REPORT'})
        self.assertEqual(env['SOBA_PRODUCT_ACTIVATION_ACCEPTANCE'], '1')
        self.assertEqual(env['HOME'], env['TMPDIR'])

    def test_cancellation_keeps_exact_child_attached_past_failed_bound(self):
        cancellation = gate.Cancellation()
        child = mock.Mock(returncode=None)
        waits = []
        def wait(timeout):
            waits.append(timeout)
            if len(waits) == 1:
                cancellation.record(signal.SIGTERM, None)
            if len(waits) <= 5:
                raise subprocess.TimeoutExpired('synthetic', timeout)
            child.returncode = 1
            return 1
        child.wait.side_effect = wait
        with mock.patch.object(gate.subprocess, 'Popen', return_value=child) as popen:
            self.assertEqual(cancellation.run_node(['node', 'fixed'], Path('/synthetic'), {}), 1)
        self.assertEqual(len(waits), 6)
        child.send_signal.assert_called_once_with(signal.SIGTERM)
        child.kill.assert_not_called()
        self.assertNotIn('start_new_session', popen.call_args.kwargs)
        self.assertNotIn('timeout', popen.call_args.kwargs)

    def test_handler_records_only_and_cancel_before_spawn_blocks(self):
        cancellation = gate.Cancellation()
        with mock.patch.object(gate.subprocess, 'Popen') as popen:
            cancellation.record(signal.SIGINT, None)
            with self.assertRaises(ValueError):
                cancellation.run_node(['node'], Path('/synthetic'), {})
            popen.assert_not_called()

    def test_no_signal_after_exact_child_reaped(self):
        cancellation = gate.Cancellation()
        child = mock.Mock(returncode=None)
        def wait(timeout):
            child.returncode = 0
            cancellation.record(signal.SIGINT, None)
            return 0
        child.wait.side_effect = wait
        with mock.patch.object(gate.subprocess, 'Popen', return_value=child):
            self.assertEqual(cancellation.run_node(['node'], Path('/synthetic'), {}), 0)
        child.send_signal.assert_not_called()
        with self.assertRaises(ValueError):
            cancellation.checkpoint()

    def test_triple_tags_exact_two_contract_and_normal_assets_before_compile(self):
        self.assertEqual(gate.TAGS, 'ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,managed_restart_native,web_activation_native,product_activation_native')
        contract = (ROOT / 'web/browser/product-activation-contract.mjs').read_text()
        self.assertEqual(sum(line.startswith("  '") for line in contract.splitlines()), 2)
        self.assertIn(gate.SCOPE, contract)
        source = Path(gate.__file__).read_text()
        build = source.index("run_command(['npm', 'run', 'build']")
        assets = source.index("provenance['assetSha256'] = asset_hashes(assets)")
        compile_at = source.index("run_command(['go', 'test', '-race', '-tags=' + TAGS, '-c'")
        runtime = source.index("cancellation.run_node(['node', 'browser/product-activation-runner.mjs']")
        self.assertLess(build, assets)
        self.assertLess(assets, compile_at)
        self.assertLess(compile_at, runtime)
        self.assertIn("assets = web / 'dist'", source)
        self.assertIn("expected_assets = {name[len('web/dist/'):]: value for name, value in before.items() if name.startswith('web/dist/')}", source)
        self.assertLess(source.index('if asset_hashes(assets) != expected_assets:'), build)
        self.assertLess(source.index("if provenance['assetSha256'] != expected_assets:"), compile_at)
        self.assertNotIn('playwright.activation.vite.config', source)
        self.assertNotIn('str(exc)', source)
        self.assertIn("'GOENV': 'off'", source)
        self.assertIn("'GOWORK': 'off'", source)
        self.assertNotIn('os.kill(', source)
        self.assertNotIn('killpg(', source)
        wrapper = (ROOT / 'web/browser/product-activation-runner.mjs').read_text()
        for fragment in ['process.argv.length, 2', 'summary.expected === 2', 'summary.observed === 2', 'summary.passed === 2', 'await scopeExit', "process.on('SIGTERM', cancelOwnedScope)"]:
            self.assertIn(fragment, wrapper)

    def test_workflow_exact_exports_and_independent_scope(self):
        source = (ROOT / '.github/workflows/ci.yml').read_text()
        job = source.split('\n  product-activation:\n', 1)[1].split('\n  web-activation:\n', 1)[0]
        self.assertIn("github.event_name == 'pull_request'", job)
        self.assertIn('github.event.pull_request.head.repo.full_name == github.repository', job)
        self.assertIn("github.event_name == 'workflow_dispatch' && inputs.force_full", job)
        self.assertNotIn('needs:', job)
        self.assertNotIn('endpoint_following_acceptance', job)
        self.assertIn('timeout-minutes: 40', job)
        self.assertIn('timeout-minutes: 25', job)
        self.assertIn('persist-credentials: false', job)
        self.assertIn('test_ci_product_activation.py', job)
        self.assertIn("steps.product-activation.outputs.sanitized_ready == 'true'", job)
        exports = job.split('          path: |\n', 1)[1].split('          if-no-files-found:', 1)[0]
        self.assertEqual(exports.splitlines(), ['            ${{ runner.temp }}/soba-product-activation-sanitized/result.json', '            ${{ runner.temp }}/soba-product-activation-sanitized/provenance.json'])
        self.assertNotIn('*', exports)
        required = ['go run ./cmd/prepare-engine\n', 'go run ./cmd/prepare-engine --verify', 'go mod download\n', 'go list -mod=readonly -deps -test -tags=' + gate.TAGS + ' ./cmd/soba >/dev/null', 'git diff --exit-code -- go.mod go.sum', 'go mod verify', 'python .github/scripts/ci-product-activation.py']
        positions = [job.index(fragment) for fragment in required]
        self.assertEqual(positions, sorted(positions))


if __name__ == '__main__':
    unittest.main()
