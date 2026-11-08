"""Closed package scheduling and same-run failure checks; no native proof."""
import copy
from pathlib import Path
import unittest
from unittest.mock import patch

import ci_full_validation as full
from test_ci_coverage import coverage, jobs

ROOT = Path(__file__).resolve().parents[2]
BUILD = 'Reproduce macOS packages with clean caches'
WAIT = 'Wait for macOS package reproduction'
SMOKE = 'Build native package and smoke archive contents'


class MacOSPackageOverlapTests(unittest.TestCase):
    def setUp(self):
        self.sources = {path: (ROOT / path).read_bytes() for path in full.SOURCES}
        self.workflow = self.sources[full.WORKFLOW].decode()
        self.start = '      - name: ' + BUILD + '\n'
        self.after = '      # Start wait-heavy gates after their exact tagged caches are warm.\n'
        self.block = self.workflow[self.workflow.index(self.start):self.workflow.index(self.after)]
        self.wait = '      - name: ' + WAIT + '\n        wait: macos-package\n'

    def test_only_macos_builds_overlap_and_all_publication_follows_join(self):
        full.validate_native_matrix_workflow(self.workflow.encode())
        full.validate_parallel_workflow(self.workflow.encode())
        self.assertIn("if: matrix.suite == 'integration' && matrix.goos == 'darwin'\n", self.block)
        self.assertEqual(self.block.count('go run ./cmd/package-tool build'), 2)
        self.assertEqual(self.block.count('go run ./cmd/package-tool checksums'), 2)
        self.assertIn('cmp "$clean_packages/first-SHA256SUMS" dist/SHA256SUMS', self.block)
        self.assertIn('export GOCACHEPROG=\n', self.block)
        for name in ('first', 'second'):
            self.assertIn(f'export GOCACHE="$clean_packages/{name}-cache" GOTMPDIR="$clean_packages/{name}-tmp"', self.block)
        for forbidden in ('smoke-package.py', 'GITHUB_ENV', 'GITHUB_PATH', 'prepare-engine',
                          'cache/save', 'ci-metrics.py', 'continue-on-error'):
            self.assertNotIn(forbidden, self.block)
        self.assertLess(self.workflow.index('Verify recovery with the ordinary direct-enabled transport'),
                        self.workflow.index(self.start))
        self.assertLess(self.workflow.index(self.start), self.workflow.index('Verify direct LAN session natural rekey'))
        self.assertLess(self.workflow.index('Test experimental native companion'), self.workflow.index(self.wait))
        self.assertTrue(self.workflow[self.workflow.index(self.wait) + len(self.wait):].startswith('      - name: ' + SMOKE))
        for gate in (SMOKE, 'Upload native packages', 'Save Go caches after integration checks pass on main',
                     'Save development Go caches after integration checks pass', 'Summarize measured native suites'):
            self.assertGreater(self.workflow.index('      - name: ' + gate), self.workflow.index(self.wait))

    def test_source_mutations_cannot_weaken_reproduction_or_failure_propagation(self):
        edits = (
            ('        background: true\n', ''),
            ('id: macos-package', 'id: another-owner'),
            (" && matrix.goos == 'darwin'", ''),
            ('timeout-minutes: 15', 'timeout-minutes: 30'),
            ('export GOCACHEPROG=', 'export GOCACHEPROG=unreviewed'),
            ('second-cache', 'first-cache'),
            ('second-tmp', 'first-tmp'),
            ('cp dist/SHA256SUMS', 'true # cp dist/SHA256SUMS'),
            ('cmp "$clean_packages/first-SHA256SUMS" dist/SHA256SUMS', 'true'),
            ('set -euo pipefail', 'set +e'),
            ('        background: true\n', '        background: true\n        continue-on-error: true\n'),
        )
        for before, after in edits:
            self.assertIn(before, self.block)
            changed = self.workflow.replace(self.block, self.block.replace(before, after, 1))
            with self.subTest(before=before), self.assertRaises(ValueError):
                full.validate_parallel_workflow(changed.encode())
        build = '          go run ./cmd/package-tool build "$version" \'darwin\' \'arm64\' "$commit"\n'
        self.assertEqual(self.block.count(build), 2)
        with self.assertRaises(ValueError):
            full.validate_parallel_workflow(self.workflow.replace(self.block, self.block.replace(build, '', 1)).encode())

    def test_join_cannot_be_removed_weakened_or_moved_past_smoke(self):
        edits = ('', self.wait.replace('wait: macos-package', 'wait-all:'),
                 self.wait + '        if: always()\n', self.wait + '        continue-on-error: true\n')
        for replacement in edits:
            with self.subTest(replacement=replacement), self.assertRaises(ValueError):
                full.validate_parallel_workflow(self.workflow.replace(self.wait, replacement).encode())
        for boundary in ('      - name: Test experimental native companion\n',
                         '      - name: Upload native packages\n'):
            moved = self.workflow.replace(self.wait, '').replace(boundary, self.wait + boundary)
            with self.subTest(boundary=boundary), self.assertRaises(ValueError):
                full.validate_parallel_workflow(moved.encode())
        moved = self.workflow.replace(self.block, '').replace(
            '      - name: Verify recovery with the ordinary direct-enabled transport\n',
            self.block + '      - name: Verify recovery with the ordinary direct-enabled transport\n')
        with self.assertRaises(ValueError):
            full.validate_parallel_workflow(moved.encode())

    def test_aggregate_requires_build_and_join_for_full_and_short_scopes(self):
        for scope in ('full', 'native-short'):
            original = jobs(scope)
            coverage.evaluate_jobs(original, scope)
            for gate in (BUILD, WAIT, SMOKE):
                for outcome in ('failure', 'skipped', 'cancelled', 'missing', 'duplicate', 'pending'):
                    rows = copy.deepcopy(original)
                    job = next(row for row in rows if row['name'] == coverage.INTEGRATION_TARGETS['darwin-arm64'])
                    step = next(row for row in job['steps'] if row['name'] == gate)
                    if outcome == 'missing':
                        job['steps'].remove(step)
                    elif outcome == 'duplicate':
                        job['steps'].append(copy.deepcopy(step))
                    elif outcome == 'pending':
                        step['status'] = 'in_progress'
                    else:
                        step['conclusion'] = outcome
                    with self.subTest(scope=scope, gate=gate, outcome=outcome), self.assertRaises(ValueError):
                        coverage.evaluate_jobs(rows, scope)
            for suite in ('unit', 'integration'):
                for target in coverage.TARGETS:
                    expectations = coverage.native_step_expectations(target, suite, scope)
                    self.assertEqual(expectations[BUILD], 'success' if suite == 'integration' and target == 'darwin-arm64' else 'skipped')
                    self.assertEqual(expectations[WAIT], 'success')

    def test_required_join_cannot_disappear_from_audit_inventory(self):
        sources = dict(self.sources)
        source = sources['.github/scripts/ci-coverage.py']
        entry = b'    "Wait for macOS package reproduction",\n'
        self.assertEqual(source.count(entry), 1)
        sources['.github/scripts/ci-coverage.py'] = source.replace(entry, b'')
        with self.assertRaisesRegex(ValueError, 'audited inventory'):
            full.required_jobs(sources)
        with patch.object(coverage, 'FAST_STEPS', tuple(name for name in coverage.FAST_STEPS if name != WAIT)):
            with self.assertRaisesRegex(ValueError, 'audited inventory'):
                coverage.native_step_expectations('darwin-arm64', 'integration', 'full')

    def test_full_proof_requires_exact_mac_only_build_and_shared_wait(self):
        required, skipped = full.required_jobs(self.sources, include_skipped=True)
        run = dict(id=10, run_attempt=1, head_sha='a' * 40)
        runners = dict((*full.TARGETS.values(), *full.INTEGRATION_TARGETS.values()))
        rows = [dict(id=index, name=name, labels=[runners[name]], run_id=10, run_attempt=1, head_sha='a' * 40,
                     status='completed', conclusion='success',
                     steps=[dict(name=step, status='completed', conclusion='success') for step in required[name]]
                     + [dict(name=step, status='completed', conclusion='skipped') for step in skipped[name]])
                for index, name in enumerate(runners, 100)]
        native_required = {name: required[name] for name in runners}
        native_skipped = {name: skipped[name] for name in runners}
        def audit(data):
            return full.audit_jobs(lambda _: dict(total_count=len(data), jobs=data), run, native_required, skipped=native_skipped)
        self.assertEqual(len(audit(rows)), 8)
        for job_index, job in enumerate(rows):
            self.assertIn(WAIT, required[job['name']])
            for gate in (BUILD, WAIT):
                for outcome in ('failure', 'cancelled', 'missing', 'duplicate', 'wrong-outcome'):
                    changed = copy.deepcopy(rows)
                    item = next(step for step in changed[job_index]['steps'] if step['name'] == gate)
                    if outcome == 'missing':
                        changed[job_index]['steps'].remove(item)
                    elif outcome == 'duplicate':
                        changed[job_index]['steps'].append(copy.deepcopy(item))
                    else:
                        item['conclusion'] = ('success' if item['conclusion'] == 'skipped' else 'skipped') if outcome == 'wrong-outcome' else outcome
                    with self.subTest(job=job['name'], gate=gate, outcome=outcome), self.assertRaises(ValueError):
                        audit(changed)


if __name__ == '__main__':
    unittest.main()
