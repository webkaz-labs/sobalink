"""Source policy and mocked ownership tests; no acceptance runner is launched."""
import importlib.util
import json
import os
from pathlib import Path
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
        self.assertEqual(set(self.passing()), gate.KEYS)
        for change in ({'expected': 7}, {'expected': True}, {'schema': True}, {'scope': 'other'},
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
            self.assertEqual(gate.validate_chromium(browsers, executable, cache), '123')
            for revision in ('124', 'other', True):
                with self.assertRaises(ValueError):
                    gate.validate_chromium({'browsers': [{'name': 'chromium', 'revision': revision}]}, executable, cache)
            executable.chmod(0o600)
            with self.assertRaises(ValueError):
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
