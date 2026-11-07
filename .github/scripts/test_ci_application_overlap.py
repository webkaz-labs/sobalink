"""Bounded native step scheduling, failure joins and isolated timing storage."""
import copy
from pathlib import Path
import re
import unittest

import ci_full_validation as full

ROOT = Path(__file__).resolve().parents[2]


class ApplicationOverlapTests(unittest.TestCase):
    def setUp(self):
        self.sources = {path: (ROOT / path).read_bytes() for path in full.SOURCES}
        self.workflow = self.sources[full.WORKFLOW].decode()
        self.start = '      - name: Verify direct LAN WireGuard and Core native applications\n'
        self.after = '      - name: Verify manual nearby setup across native Core peers\n'
        self.wait = '      - name: Wait for direct LAN application checks\n        wait: directlan-applications\n'
        self.region = self.workflow[self.workflow.index(self.start):self.workflow.index(self.after)]

    def test_one_background_chain_and_three_serial_process_gates_join(self):
        full.validate_parallel_workflow(self.workflow.encode())
        self.assertEqual(re.findall(r'^      - name: (.+)$', self.region, re.M), [
            'Verify direct LAN WireGuard and Core native applications',
            'Verify explicit dispatch with three separate Core processes',
            'Verify durable outbox restart across Core processes',
            'Verify durable text restart and lost ACK across Core processes',
            'Wait for direct LAN application checks',
        ])
        self.assertEqual(self.region.count('        background: true\n'), 1)
        self.assertTrue(self.region.endswith(self.wait))
        first, foreground = self.region.split('      - name: Verify explicit dispatch', 1)
        self.assertIn('SOBALINK_CI_METRICS: ${{ runner.temp }}/sobalink-ci-directlan-metrics.jsonl', first)
        self.assertNotIn('SOBALINK_CI_METRICS:', foreground)
        self.assertEqual(first.count('ci-metrics.py run --suite'), 3)
        self.assertEqual(foreground.count('ci-metrics.py run --suite'), 3)
        for forbidden in ('GITHUB_ENV', 'GITHUB_PATH', 'cache/save', 'package-tool', 'prepare-engine',
                          'continue-on-error', 'GOMAXPROCS', '-p='):
            self.assertNotIn(forbidden, self.region)

    def test_mutations_cannot_remove_tests_weaken_join_or_add_shared_writes(self):
        changes = [
            self.region.replace('        background: true\n', ''),
            self.region.replace('        background: true\n', '        background: true\n        continue-on-error: true\n'),
            self.region.replace(self.wait, ''),
            self.region.replace(self.wait, self.wait + '        if: always()\n'),
            self.region.replace(self.wait, self.wait + '        continue-on-error: true\n'),
            self.region.replace('wait: directlan-applications', 'wait-all:'),
            self.region.replace('id: directlan-applications', 'id: other'),
            self.region.replace('sobalink-ci-directlan-metrics.jsonl', 'sobalink-ci-metrics.jsonl'),
            self.region.replace('-count=5', '-count=1'),
            self.region.replace('-race ', ''),
            self.region.replace('-timeout=5m', '-timeout=10m'),
            self.region.replace('        timeout-minutes: 6', '        background: true\n        timeout-minutes: 6', 1),
            self.region.replace(self.wait, '      - run: echo unreviewed\n' + self.wait),
        ]
        for index, changed in enumerate(changes):
            with self.subTest(index=index), self.assertRaises(ValueError):
                full.validate_parallel_workflow(self.workflow.replace(self.region, changed).encode())
        for changed in (self.workflow.replace('--include "$RUNNER_TEMP/sobalink-ci-directlan-metrics.jsonl" ', ''),
                        self.workflow.replace(self.wait, '').replace(self.after, self.after + self.wait)):
            with self.assertRaises(ValueError):
                full.validate_parallel_workflow(changed.encode())

    def test_runtime_audit_requires_successful_wait_and_every_process_gate(self):
        required = {name: names for name, names in full.required_jobs(self.sources).items()
                    if name in {value[0] for value in full.INTEGRATION_TARGETS.values()}}
        run = dict(id=10, run_attempt=1, head_sha='a' * 40)
        runners = dict(full.INTEGRATION_TARGETS.values())
        original = [dict(id=index, name=name, labels=[runners[name]], run_id=10, run_attempt=1, head_sha='a' * 40,
                         status='completed', conclusion='success',
                         steps=[dict(name=step, status='completed', conclusion='success') for step in names])
                    for index, (name, names) in enumerate(required.items(), 100)]
        def audit(rows):
            return full.audit_jobs(lambda path: dict(total_count=len(rows), jobs=rows), run, required)
        self.assertEqual(len(audit(original)), 4)
        for name in re.findall(r'^      - name: (.+)$', self.region, re.M):
            for outcome in ('failure', 'skipped', 'cancelled', 'missing'):
                rows = copy.deepcopy(original)
                step = next(step for step in rows[0]['steps'] if step['name'] == name)
                if outcome == 'missing':
                    rows[0]['steps'].remove(step)
                else:
                    step['conclusion'] = outcome
                with self.subTest(name=name, outcome=outcome), self.assertRaises(ValueError):
                    audit(rows)


if __name__ == '__main__':
    unittest.main()
