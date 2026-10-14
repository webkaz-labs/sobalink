"""Closed selection and source parity only; never launch an acceptance runner."""
import importlib.util
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('ci_diagnostic', Path(__file__).with_name('ci-diagnostic.py'))
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


class DiagnosticPolicyTests(unittest.TestCase):
    def args(self, suite='web'):
        sha = 'a' * 40
        ref = 'diagnostic/' + suite + '/' + sha[:12] + '-attempt1'
        return dict(event_name='create', event={'ref_type': 'branch', 'ref': ref,
                    'repository': {'full_name': gate.REPOSITORY, 'private': False}},
                    repository=gate.REPOSITORY, ref='refs/heads/' + ref,
                    sha=sha, checkout_sha=sha)

    def test_exact_closed_suites(self):
        for suite in ('web', 'product', 'activation', 'nearby', 'text', 'dnssd', 'macos-package'):
            self.assertEqual(gate.select(**self.args(suite)), suite)

    def test_macos_package_requires_exact_source_and_closed_branch_shape(self):
        original = self.args('macos-package')
        for change in ({'event_name': 'workflow_dispatch'}, {'event_name': 'push'},
                       {'sha': 'b' * 40}, {'checkout_sha': 'b' * 40},
                       {'ref': original['ref'] + '\n'},
                       {'ref': original['ref'].replace('a' * 12, 'b' * 12)}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                gate.select(**dict(original, **change))
        for suite in ('macos', 'package', 'macos-package-all', 'macos-package/extra',
                      'macos-package-unsigned', 'MACOS-PACKAGE'):
            with self.subTest(suite=suite), self.assertRaises(ValueError):
                gate.select(**self.args(suite))
        args = self.args('macos-package')
        args['event']['ref'] = args['event']['ref'].replace('attempt1', 'attempt2')
        with self.assertRaises(ValueError):
            gate.select(**args)

    def test_rejects_other_events_refs_and_heads(self):
        for change in ({'event_name': 'push'}, {'event_name': 'pull_request'},
                       {'repository': 'other/project'}, {'ref': 'refs/heads/main'},
                       {'ref': self.args()['ref'].replace('/web/', '/all/')},
                       {'ref': self.args()['ref'] + '\n'},
                       {'ref': self.args()['ref'].replace('attempt1', 'a/b')},
                       {'ref': self.args()['ref'].replace('a' * 12, 'b' * 12)},
                       {'sha': 'A' * 40}, {'checkout_sha': 'b' * 40}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                gate.select(**dict(self.args(), **change))
        for change in ({'ref_type': 'tag'}, {'ref': 'diagnostic/web/other'},
                       {'repository': {'full_name': gate.REPOSITORY, 'private': True}},
                       {'repository': {'full_name': gate.REPOSITORY}}):
            args = self.args()
            args['event'].update(change)
            with self.subTest(change=change), self.assertRaises(ValueError):
                gate.select(**args)

    def workflow(self, name):
        return (ROOT / '.github/workflows' / (name + '.yml')).read_text()

    def body(self, workflow, job):
        match = re.search(r'^  ' + re.escape(job) + r':\n.*?(?=^  [a-z][a-z0-9-]*:\n|\Z)', workflow, re.M | re.S)
        self.assertIsNotNone(match)
        text = match.group()
        return text[text.index('    runs-on:'):].strip()

    def test_diagnostics_retain_entire_original_jobs(self):
        original = self.workflow('ci')
        diagnostic = self.workflow('activation-diagnostic')
        for job in ('web-activation', 'product-activation'):
            expected = self.body(original, job).replace('name: soba-' + job + '-sanitized',
                                                       'name: diagnostic-soba-' + job + '-sanitized')
            self.assertEqual(self.body(diagnostic, 'diagnostic-' + job), expected)

    def test_diagnostic_success_cannot_satisfy_full_coverage(self):
        spec = importlib.util.spec_from_file_location('ci_coverage', ROOT / '.github/scripts/ci-coverage.py')
        coverage = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(coverage)
        jobs = [dict(name=name, status='completed', conclusion='success', steps=[])
                for name in ('diagnostic-selection', 'diagnostic-web-activation',
                             'diagnostic-product-activation', 'diagnostic-macos-package',
                             'diagnostic-nearby', 'diagnostic-nearby-native', 'diagnostic-text', 'diagnostic-dnssd')]
        for scope in coverage.SCOPES:
            with self.subTest(scope=scope), self.assertRaises(ValueError):
                coverage.evaluate_jobs(jobs, scope)

    def test_macos_package_diagnostic_is_absent_from_full_validation(self):
        spec = importlib.util.spec_from_file_location('ci_full_validation', ROOT / '.github/scripts/ci_full_validation.py')
        full = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(full)
        sources = {name: (ROOT / name).read_bytes() for name in full.SOURCES}
        required = full.required_jobs(sources)
        self.assertNotIn('diagnostic-macos-package', required)
        self.assertFalse(any(name.startswith('diagnostic-') for name in required))
        self.assertNotIn('Compare original and candidate macOS packaging privately',
                         {step for steps in required.values() for step in steps})
        self.assertNotIn('ci-macos-package.py', self.workflow('ci'))

    def test_macos_package_is_one_exact_head_native_job_with_closed_commands(self):
        text = self.workflow('activation-diagnostic')
        job = re.search(r'^  diagnostic-macos-package:\n.*?(?=^  [a-z][a-z0-9-]*:\n|\Z)',
                        text, re.M | re.S).group()
        self.assertIn("    needs: diagnostic-selection\n    if: needs.diagnostic-selection.outputs.suite == 'macos-package'\n", job)
        self.assertEqual(re.findall(r'^    runs-on: (.+)$', job, re.M), ['macos-26'])
        self.assertIn('    timeout-minutes: 60\n', job)
        self.assertIn('    permissions:\n      contents: read\n', job)
        self.assertIn('    defaults:\n      run:\n        shell: bash\n', job)
        self.assertEqual(re.findall(r'uses: ([^ #\n]+)', job), [
            'actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1',
            'actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16',
            'actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1',
            'actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a'])
        for block in ('          ref: ${{ github.sha }}\n          persist-credentials: false\n          fetch-depth: 0\n',
                      "          go-version: '1.27.1'\n          cache: false\n",
                      "          node-version: '24.19.0'\n          check-latest: false\n          package-manager-cache: false\n",
                      '        id: macos-package\n        timeout-minutes: 55\n        run: python -B .github/scripts/ci-macos-package.py\n'):
            self.assertIn(block, job)
        self.assertEqual(re.findall(r'^        run: (.*(?:\n          [^\n]+)*)', job, re.M), [
            'npm install --global npm@11.9.0 --no-audit --no-fund',
            "|\n          python -B -m unittest discover -s .github/scripts -p 'test_ci_diagnostic.py' -v\n"
            "          python -B -m unittest discover -s .github/scripts -p 'test_ci_macos_package.py' -v",
            'python -B .github/scripts/ci-macos-package.py'])
        for forbidden in ('strategy:', 'matrix:', 'env:', 'sudo', 'codesign', 'notarytool',
                          'security ', 'ci-native-core', 'ci-dnssd', 'ci-nearby',
                          'actions/cache', 'continue-on-error', 'secrets.', 'contents: write',
                          'id-token:', 'npm ci', 'npm run', 'prepare-engine', 'package-tool'):
            self.assertNotIn(forbidden, job)

    def test_macos_package_upload_is_only_closed_sanitized_receipts(self):
        job = self.body(self.workflow('activation-diagnostic'), 'diagnostic-macos-package')
        self.assertIn("if: always() && steps.macos-package.outputs.sanitized_ready == 'true'", job)
        self.assertEqual(job.count('uses: actions/upload-artifact@'), 1)
        artifact = job.split('uses: actions/upload-artifact@', 1)[1]
        self.assertEqual(artifact.split('        with:\n', 1)[1],
                         '          name: diagnostic-soba-macos-package-sanitized\n'
                         '          path: |\n'
                         '            ${{ runner.temp }}/soba-macos-package-sanitized/result.json\n'
                         '            ${{ runner.temp }}/soba-macos-package-sanitized/provenance.json\n'
                         '          if-no-files-found: error\n'
                         '          retention-days: 14')
        for forbidden in ('*', '.log', '.jsonl', '.zip', '.tar', '.pkg', '.dmg', 'private'):
            self.assertNotIn(forbidden, artifact)

    def test_only_create_and_distinct_jobs_without_release_credit(self):
        text = self.workflow('activation-diagnostic')
        trigger = text.split('on:\n', 1)[1].split('\npermissions:', 1)[0]
        self.assertEqual(trigger.strip(), 'create:')
        self.assertEqual(re.findall(r'^  ([a-z][a-z0-9-]*):$', text.split('\njobs:\n')[1], re.M),
                         ['diagnostic-selection', 'diagnostic-product-activation', 'diagnostic-web-activation',
                          'diagnostic-macos-package', 'diagnostic-nearby', 'diagnostic-nearby-native', 'diagnostic-text', 'diagnostic-dnssd'])
        for forbidden in ('continue-on-error:', 'actions/cache', 'secrets.', 'workflow_dispatch:',
                          'ci-coverage.py', 'ci-required:', 'package-tool', 'id-token: write', 'contents: write'):
            self.assertNotIn(forbidden, text)
        self.assertIn("github.repository == 'webkaz-labs/sobalink'", text)
        self.assertIn("github.event.ref_type == 'branch'", text)
        self.assertIn("startsWith(github.ref, 'refs/heads/diagnostic/')", text)
        for suite in ('web', 'product'):
            self.assertIn("if: needs.diagnostic-selection.outputs.suite == '" + suite +
                          "' || needs.diagnostic-selection.outputs.suite == 'activation'", text)
        self.assertIn("if: needs.diagnostic-selection.outputs.suite == 'nearby'", text)
        self.assertIn("if: needs.diagnostic-selection.outputs.suite == 'text'", text)
        self.assertIn("if: needs.diagnostic-selection.outputs.suite == 'macos-package'", text)
        self.assertIn('cancel-in-progress: false', text)
        self.assertIn('not release validation', text)
        self.assertIn('push:\n    branches: [main]', self.workflow('ci'))


if __name__ == '__main__':
    unittest.main()
