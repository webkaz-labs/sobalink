"""Closed Windows-only outbox diagnostic policy; never launch a Go build."""
from pathlib import Path
import re
import shlex
import unittest

ROOT = Path(__file__).resolve().parents[2]
PACKAGE = 'github.com/webkaz-labs/sobalink/internal/core'
TAGS = 'lanlink_integration,directlan_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy'


class OutboxDiagnosticPolicyTests(unittest.TestCase):
    def job(self):
        workflow = (ROOT / '.github/workflows/activation-diagnostic.yml').read_text(encoding='utf-8')
        match = re.search(r'^  diagnostic-outbox:\n.*?(?=^  [a-z][a-z0-9-]*:\n|\Z)',
                          workflow, re.M | re.S)
        self.assertIsNotNone(match)
        return match.group().rstrip() + '\n'

    def commands(self):
        return [shlex.split(line.strip()) for line in self.job().splitlines()
                if line.strip().startswith('python -B .github/scripts/ci-go-test.py ')]

    def test_single_windows_lane_is_create_selected_and_read_only(self):
        text = self.job()
        self.assertIn('    needs: diagnostic-selection\n', text)
        self.assertIn("    if: needs.diagnostic-selection.outputs.suite == 'outbox'\n", text)
        self.assertIn('    runs-on: windows-2025\n', text)
        self.assertIn('    timeout-minutes: 30\n', text)
        self.assertIn('    permissions:\n      contents: read\n', text)
        self.assertIn('    defaults:\n      run:\n        shell: bash\n', text)
        for forbidden in ('strategy:', 'matrix.', 'continue-on-error:', 'always()',
                          'secrets.', 'github.token', 'write', 'actions/cache',
                          'upload-artifact', 'download-artifact', 'setup-node', 'npm ',
                          'Chromium', 'playwright', 'ci-required', 'ci-coverage.py',
                          'workflow_dispatch', 'pull_request', 'go env -json', 'printenv',
                          'Get-ChildItem Env:', 'env |', 'set -x'):
            self.assertNotIn(forbidden, text)
        self.assertEqual(re.findall(r'uses: (\S+)', text), [
            'actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1',
            'actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16'])
        self.assertIn('          persist-credentials: false\n', text)
        self.assertIn("          go-version: '1.27.3'\n          cache: false\n", text)
        self.assertEqual(text.count('SOBALINK_RUN_DIRECT_INTEGRATION:'), 1)
        self.assertIn("          SOBALINK_RUN_DIRECT_INTEGRATION: '1'\n", text)

    def test_toolchain_and_engine_are_verified_before_any_go_test(self):
        text = self.job()
        ordered = ["test \"$(go env GOVERSION)\" = go1.27.3",
                   'test "$(go env GOOS)" = windows',
                   'test "$(go env GOARCH)" = amd64',
                   'go run ./cmd/prepare-engine\n',
                   'go run ./cmd/prepare-engine --verify\n',
                   'go mod download\n', 'git diff --exit-code -- go.mod go.sum\n',
                   'go mod verify\n', 'python -B .github/scripts/ci-go-test.py ']
        offsets = [text.index(value) for value in ordered]
        self.assertEqual(offsets, sorted(offsets))
        self.assertNotIn('go mod download all', text)
        self.assertNotIn('go mod tidy', text)
        self.assertEqual(text.count('go run ./cmd/prepare-engine\n'), 1)
        for policy in ('diagnostic', 'outbox_diagnostic', 'go_test'):
            self.assertIn("-p 'test_ci_" + policy + ".py' -v", text)

    def test_helper_regex_requires_each_existing_and_delayed_invocation_regression(self):
        helper, _ = self.commands()
        expected = [helper[index + 1] for index, word in enumerate(helper) if word == '--expect']
        source = (ROOT / 'internal/core/outbox_process_retry_test.go').read_text(encoding='utf-8')
        names = re.findall(r'^func (TestOutboxProcessCommitRetry\w+)\(t \*testing.T\)', source, re.M)
        self.assertEqual(len(expected), len(set(expected)))
        self.assertEqual(set(expected), {PACKAGE + ':' + name for name in names} | {PACKAGE + ':TestRouteOutboxObservationClosedFrames', PACKAGE + ':TestRouteOutboxObservationHeldLifecycle'})
        self.assertIn(PACKAGE + ':TestOutboxProcessCommitRetrySlowBusyInvocation', expected)
        self.assertEqual(helper[helper.index('--') + 1:], [
            'go', 'test', '-race', '-count=1', '-timeout=2m', '-tags=' + TAGS,
            './internal/core', '-run', '^TestOutboxProcessCommitRetry|^TestRouteOutboxObservation'])

    def test_one_restart_sample_requires_exact_pass_without_retrying_the_workflow(self):
        _, restart = self.commands()
        self.assertEqual(restart, [
            'python', '-B', '.github/scripts/ci-go-test.py', '--exact', '--expect',
            PACKAGE + ':TestOutboxProcessRestart', '--', 'go', 'test', '-race',
            '-count=1', '-timeout=4m', '-tags=' + TAGS, './internal/core',
            '-run', '^TestOutboxProcessRestart$'])
        text = self.job().split('      - name: Observe one independent exact outbox restart sample\n')[1]
        self.assertIn('        timeout-minutes: 14\n', text)
        self.assertIn('          set -euo pipefail\n', text)
        self.assertEqual(text.count('python -B .github/scripts/ci-go-test.py'), 1)
        for forbidden in ('||', 'continue', 'break', 'exit 0', '-count=3', 'set +e',
                          'until ', 'while ', 'for ', 'sleep ', 'tee ', 'cat '):
            self.assertNotIn(forbidden, text)

    def test_observation_is_fixed_counters_without_changing_admission_deadlines(self):
        source = (ROOT / 'internal/core/outbox_process_integration_test.go').read_text(encoding='utf-8')
        helper = (ROOT / 'internal/core/outbox_process_retry_test.go').read_text(encoding='utf-8')
        self.assertIn('context.WithTimeout(ctx, 2*time.Second)', helper)
        self.assertIn('time.NewTimer(20 * time.Millisecond)', helper)
        self.assertIn('context.WithTimeout(context.Background(), 3*time.Minute)', source)
        line, = [line.strip() for line in source.splitlines() if 't.Logf("outbox commit observation:' in line]
        self.assertEqual(line, 't.Logf("outbox commit observation: phase=%s entry=%s attempt_count=%d busy_count=%d elapsed_ms=%d last_invoke_ms=%d max_invoke_ms=%d", phase, entry, attempts, busyCount, time.Since(started).Milliseconds(), lastInvoke.Milliseconds(), maxInvoke.Milliseconds())')
        self.assertIn('if reply.Error == "outbox.commit: outbox_storage_busy"', source)
        self.assertIn('peers[0].invokeResult(t, "outbox.commit", payload)', source)
        self.assertIn('entry := "file"', source)
        self.assertIn('entry = "empty-directory"', source)
        self.assertIn('commit(review, "initial")', source)
        self.assertIn('commit(review, "replay")', source)


    def test_busy_observation_has_only_bounded_fixed_test_evidence(self):
        observation = (ROOT / 'internal/core/route_process_outbox_observation_test.go').read_text(encoding='utf-8')
        child = (ROOT / 'internal/core/route_process_integration_test.go').read_text(encoding='utf-8')
        self.assertTrue(observation.startswith('//go:build lanlink_integration\n'))
        self.assertIn('make([]runtime.StackRecord, 1024)', observation)
        self.assertIn('runtime.GoroutineProfile(records)', observation)
        self.assertIn('runtime.CallersFrames(record.Stack())', observation)
        self.assertIn('if request.Name == "outbox.commit" && networkErrorCode(callErr) == "outbox_storage_busy"', child)
        self.assertIn('routeObserveOutboxBusy(commandMS)', child)
        for forbidden in ('runtime.Stack(', 'pprof.', 'fmt.', 'os.Stdout', 'os.Stderr', 'File:', '[]byte', 'map[string]'):
            self.assertNotIn(forbidden, observation)
        fields = observation.split('type routeOutboxBusyObservation struct {', 1)[1].split('}', 1)[0]
        self.assertEqual(set(line.split()[-1] for line in fields.splitlines() if line.strip()), {'bool', 'int64'})


if __name__ == '__main__':
    unittest.main()
