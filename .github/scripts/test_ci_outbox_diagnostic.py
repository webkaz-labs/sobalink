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
        self.assertIn("          go-version: '1.27.1'\n          cache: false\n", text)
        self.assertEqual(text.count('SOBALINK_RUN_DIRECT_INTEGRATION:'), 1)
        self.assertIn("          SOBALINK_RUN_DIRECT_INTEGRATION: '1'\n", text)

    def test_toolchain_and_engine_are_verified_before_any_go_test(self):
        text = self.job()
        ordered = ["test \"$(go env GOVERSION)\" = go1.27.1",
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
        self.assertEqual(set(expected), {PACKAGE + ':' + name for name in names})
        self.assertIn(PACKAGE + ':TestOutboxProcessCommitRetrySlowBusyInvocation', expected)
        self.assertEqual(helper[helper.index('--') + 1:], [
            'go', 'test', '-race', '-count=1', '-timeout=2m', '-tags=' + TAGS,
            './internal/core', '-run', '^TestOutboxProcessCommitRetry'])

    def test_three_restart_samples_each_require_exact_pass_and_stop_on_failure(self):
        _, restart = self.commands()
        self.assertEqual(restart, [
            'python', '-B', '.github/scripts/ci-go-test.py', '--exact', '--expect',
            PACKAGE + ':TestOutboxProcessRestart', '--', 'go', 'test', '-race',
            '-count=1', '-timeout=4m', '-tags=' + TAGS, './internal/core',
            '-run', '^TestOutboxProcessRestart$'])
        text = self.job().split('      - name: Require three independent exact outbox restart passes\n')[1]
        self.assertIn('        timeout-minutes: 14\n', text)
        self.assertIn('          set -euo pipefail\n          for attempt in 1 2 3; do\n', text)
        self.assertIn("            printf 'outbox_restart_attempt=%s\\n' \"$attempt\"\n", text)
        self.assertTrue(text.endswith('          done\n'))
        for forbidden in ('||', 'continue', 'break', 'exit 0', '-count=3', 'set +e',
                          'until ', 'while ', 'sleep ', 'tee ', 'cat '):
            self.assertNotIn(forbidden, text)


if __name__ == '__main__':
    unittest.main()
