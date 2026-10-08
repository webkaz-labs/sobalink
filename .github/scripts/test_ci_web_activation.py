"""Pure tests: fixed Web acceptance schema, environment, selection and workflow."""
import importlib.util
import json
import os
from pathlib import Path
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('web_gate', Path(__file__).with_name('ci-web-activation.py'))
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


def passed_report():
    result = gate.failed_report()
    result.update(accepted=True, allSevenPassed=True, runnerExitedSuccessfully=True, nativeCleanupProven=True)
    result['diagnostics'].update(summaryAvailable=True, selectionValid=True, observed=7, passed=7)
    for row in result['diagnostics']['cases']:
        row.update(started=True, status='passed')
        row['lifecycle'].update(stage='finished', workCompleted=True, stopRequested=True, noWaitableChildren=True, registeredChildren=1, observedExits=1, reaped=1, supervisorExit='zero', **dict.fromkeys(('supervisorStarted', 'supervisorExited', 'proofRead', 'allDescendantsReaped', 'registeredNativeExits', 'contextClosed', 'profileRemoved'), True))
    return result


class WebActivationPolicyTests(unittest.TestCase):
    def test_exact_schema_accepts_pass_and_failure(self):
        failed = gate.failed_report()
        self.assertFalse(gate.validate_report(failed)['accepted'])
        passed = passed_report()
        self.assertTrue(gate.validate_report(passed)['accepted'])
        self.assertEqual(set(passed), gate.KEYS)

    def test_schema_rejects_leaks_types_and_partial_success(self):
        original = passed_report()
        mutations = [{'error': 'synthetic private detail'}, {'expected': 6}, {'schema': True}, {'scope': 'other'}, {'timedOut': 0}, {'timedOut': True}, {'nativeCleanupProven': False}, {'allSevenPassed': False}, {'outputOverflow': True}, {'runnerExitedSuccessfully': False}]
        for change in mutations:
            with self.subTest(change=tuple(change)):
                with self.assertRaises(ValueError):
                    gate.validate_report(dict(original, **change))
        for key in gate.KEYS:
            broken = dict(original)
            del broken[key]
            with self.assertRaises(ValueError):
                gate.validate_report(broken)

    def test_fixed_diagnostic_boundary_rejects_sensitive_and_untyped_values(self):
        self.assertEqual(gate.validate_diagnostics(gate.empty_diagnostics()), gate.empty_diagnostics())
        mutations = [lambda d: d.update(error='synthetic private detail'), lambda d: d.update(observed=True), lambda d: d.update(globalErrors=256), lambda d: d['cases'].pop(), lambda d: d['cases'][0].update(id='synthetic secret'), lambda d: d['cases'][1].update(id=d['cases'][0]['id']), lambda d: d['cases'][0].update(status='other'), lambda d: d['cases'][0]['lifecycle'].update(path='/synthetic/private'), lambda d: d['cases'][0]['lifecycle'].update(reaped=True), lambda d: d['cases'][0]['lifecycle'].update(supervisorExit=0), lambda d: d['cases'][0]['lifecycle'].update(stage='https://example.invalid/private'), lambda d: d['cases'][0]['lifecycle'].update(workStage='cleanup-proof'), lambda d: d['cases'][0]['lifecycle'].update(oldExit=95)]
        for mutation in mutations:
            value = gate.empty_diagnostics()
            mutation(value)
            with self.assertRaises(ValueError):
                gate.validate_diagnostics(value)
        broken = passed_report()
        broken['diagnostics'] = gate.empty_diagnostics()
        with self.assertRaises(ValueError):
            gate.validate_report(broken)
        for row in range(7):
            broken = passed_report()
            broken['diagnostics']['cases'][row]['started'] = False
            with self.assertRaises(ValueError):
                gate.validate_report(broken)

    def test_supervisor_counts_remain_observations_not_cleanup_substitutes(self):
        report = passed_report()
        report['diagnostics']['cases'][0]['lifecycle'].update(registeredChildren=2, observedExits=2, reaped=1)
        self.assertTrue(gate.validate_report(report)['accepted'])
        report['diagnostics']['cases'][0]['lifecycle']['allDescendantsReaped'] = False
        with self.assertRaises(ValueError):
            gate.validate_report(report)

    def test_runtime_environment_has_no_inherited_authority(self):
        with mock.patch.dict(os.environ, {'PATH': '/synthetic/bin', 'SECRET_TOKEN': 'synthetic', 'DEBUG': '1', 'PWDEBUG': '1', 'HTTPS_PROXY': 'synthetic'}):
            env = gate.child_environment(Path('/synthetic/private'), Path('/synthetic/binary'), Path('/synthetic/assets'), Path('/synthetic/chromium'), Path('/synthetic/result'))
        self.assertEqual(set(env), {'PATH', 'HOME', 'TMPDIR', 'LANG', 'SOBA_WEB_ACTIVATION_ACCEPTANCE', 'SOBA_WEB_ACTIVATION_BINARY', 'SOBA_WEB_ACTIVATION_ASSETS', 'SOBA_ACTIVATION_CHROMIUM', 'SOBA_ACTIVATION_REPORT'})
        self.assertEqual(env['SOBA_WEB_ACTIVATION_ACCEPTANCE'], '1')
        self.assertEqual(env['HOME'], env['TMPDIR'])

    def test_exact_tags_and_seven_case_contract(self):
        self.assertEqual(gate.TAGS, 'ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,managed_restart_native,web_activation_native')
        contract = (ROOT / 'web/browser/activation-contract.mjs').read_text(encoding='utf-8')
        self.assertEqual(sum(line.startswith("  '") for line in contract.splitlines()), 7)
        wrapper = (ROOT / 'web/browser/activation-runner.mjs').read_text(encoding='utf-8')
        for text in ['process.argv.length, 2', "chmod(root, 0o700)", 'summary.expected === 7', 'summary.observed === 7', 'summary.passed === 7', 'summary.globalErrors === 0', '23 * 60_000']:
            self.assertIn(text, wrapper)
        config = (ROOT / 'web/playwright.activation.config.mjs').read_text(encoding='utf-8')
        for text in ['workers: 1', 'retries: 0', "screenshot: 'off'", "trace: 'off'", "video: 'off'", "serviceWorkers: 'block'"]:
            self.assertIn(text, config)

    def test_workflow_independent_pr_trigger_and_exact_exports(self):
        workflow = (ROOT / '.github/workflows/ci.yml').read_text(encoding='utf-8')
        job = workflow.split('\n  web-activation:\n', 1)[1].split('\n  browser:\n', 1)[0]
        self.assertIn("github.event_name == 'pull_request'", job)
        self.assertIn('github.event.pull_request.head.repo.full_name == github.repository', job)
        self.assertIn("github.event_name == 'workflow_dispatch' && inputs.force_full", job)
        self.assertNotIn('needs:', job)
        self.assertNotIn('endpoint_following_acceptance', job)
        self.assertIn('timeout-minutes: 55', job)
        self.assertIn('timeout-minutes: 40', job)
        self.assertIn('persist-credentials: false', job)
        self.assertIn("steps.web-activation.outputs.sanitized_ready == 'true'", job)
        exports = job.split('          path: |\n', 1)[1].split('          if-no-files-found:', 1)[0]
        self.assertEqual(exports.splitlines(), ['            ${{ runner.temp }}/soba-web-activation-sanitized/result.json', '            ${{ runner.temp }}/soba-web-activation-sanitized/provenance.json'])
        self.assertNotIn('*', exports)
        self.assertIn('node --test web/browser/activation-diagnostics.test.mjs', job)

    def test_cold_runner_prefetch_precedes_offline_gate(self):
        workflow = (ROOT / '.github/workflows/ci.yml').read_text(encoding='utf-8')
        job = workflow.split('\n  web-activation:\n', 1)[1].split('\n  browser:\n', 1)[0]
        required = ['go run ./cmd/prepare-engine\n', 'go run ./cmd/prepare-engine --verify', 'go mod download\n', 'go list -mod=readonly -deps -test -tags=' + gate.TAGS + ' ./cmd/soba >/dev/null', 'git diff --exit-code -- go.mod go.sum', 'go mod verify', 'python .github/scripts/ci-web-activation.py']
        positions = [job.index(text) for text in required]
        self.assertEqual(positions, sorted(positions))
        self.assertEqual(job.count('go mod download\n'), 1)
        self.assertNotIn('go mod download all', job)
        self.assertNotIn('git checkout', job)
        self.assertNotIn('git restore', job)
        self.assertNotIn('git reset', job)

    def test_toolchain_provenance_exact_types_and_fields(self):
        digests = {'go': 'a' * 64, 'node': 'b' * 64}
        result = gate.validate_toolchain(dict(gate.VERSIONS), digests)
        self.assertEqual(result, {'versions': gate.VERSIONS, 'binarySha256': digests})
        for versions in [{}, dict(gate.VERSIONS, go='other'), dict(gate.VERSIONS, npm=True), dict(gate.VERSIONS, private='synthetic')]:
            with self.assertRaises(ValueError):
                gate.validate_toolchain(versions, digests)
        for values in [{}, dict(digests, go=True), dict(digests, node='private-path'), dict(digests, go='A' * 64), dict(digests, extra='c' * 64)]:
            with self.assertRaises(ValueError):
                gate.validate_toolchain(gate.VERSIONS, values)

    def test_compile_only_and_sole_runtime_entry(self):
        source = Path(gate.__file__).read_text(encoding='utf-8')
        self.assertIn("['go', 'test', '-race', '-tags=' + TAGS, '-c', '-o', str(binary), './cmd/soba']", source)
        self.assertIn("['node', 'browser/activation-runner.mjs']", source)
        self.assertNotIn("'test', '--config'", source)
        self.assertIn("GOPROXY='off'", source)
        self.assertIn('stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL', source)
        self.assertIn('if accepted:\n            shutil.rmtree(private)', source)
        self.assertNotIn('str(exc)', source)


if __name__ == '__main__':
    unittest.main()
