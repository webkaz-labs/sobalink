"""Two bounded native chains retain every gate and bind the join to this run."""
import copy
from pathlib import Path
import re
import unittest

import ci_full_validation as full

ROOT = Path(__file__).resolve().parents[2]


class ContextOverlapTests(unittest.TestCase):
    def setUp(self):
        self.sources = {path: (ROOT / path).read_bytes() for path in full.SOURCES}
        self.workflow = self.sources[full.WORKFLOW].decode()
        self.start = '      - name: Verify Core context control over pinned TLS\n'
        self.after = '      - name: Verify direct LAN WireGuard and Core native applications\n'
        self.wait = '      - name: Wait for Core context checks\n        wait: core-context\n'
        self.region = self.workflow[self.workflow.index(self.start):self.workflow.index(self.after)]

    def test_one_background_core_gate_preserves_managed_order_and_join(self):
        full.validate_parallel_workflow(self.workflow.encode())
        self.assertEqual(re.findall(r'^      - name: (.+)$', self.region, re.M), [
            'Verify Core context control over pinned TLS',
            'Verify context control over fixed loopback TCP',
            'Verify managed session tls',
            'Verify managed session outbound-tcp',
            'Verify managed session native-caller',
            'Verify managed session native-simultaneous',
            'Verify managed session native-udp-capacity',
            'Verify managed activation acceptance',
            'Verify managed restart acceptance',
            'Wait for Core context checks',
        ])
        self.assertEqual(self.region.count('        background: true\n'), 1)
        self.assertEqual(self.region.count('        wait:'), 1)
        first, foreground = self.region.split('      - name: Verify context control over fixed', 1)
        self.assertIn('SOBALINK_CI_METRICS: ${{ runner.temp }}/sobalink-ci-context-metrics.jsonl', first)
        self.assertNotIn('SOBALINK_CI_METRICS:', foreground)
        self.assertEqual(first.count('ci-metrics.py run --suite'), 1)
        self.assertEqual(foreground.count('ci-metrics.py run --suite'), 8)
        for forbidden in ('GITHUB_ENV', 'GITHUB_PATH', 'cache/save', 'package-tool',
                          'prepare-engine', 'continue-on-error', 'GOMAXPROCS', '-p='):
            self.assertNotIn(forbidden, self.region)

    def test_mutations_cannot_weaken_gates_joins_or_writer_isolation(self):
        changes = [
            self.region.replace('        background: true\n', ''),
            self.region.replace('        background: true\n', '        background: true\n        continue-on-error: true\n'),
            self.region.replace(self.wait, ''),
            self.region.replace(self.wait, self.wait + '        if: always()\n'),
            self.region.replace(self.wait, self.wait + '        continue-on-error: true\n'),
            self.region.replace('wait: core-context', 'wait-all:'),
            self.region.replace('id: core-context', 'id: other'),
            self.region.replace('sobalink-ci-context-metrics.jsonl', 'sobalink-ci-metrics.jsonl'),
            self.region.replace('timeout-minutes: 5', 'timeout-minutes: 10'),
            self.region.replace('ci-context-control.py core-tls', 'ci-context-control.py fixed-tcp'),
            self.region.replace('        timeout-minutes: 4', '        background: true\n        timeout-minutes: 4', 1),
        ]
        for index, changed in enumerate(changes):
            with self.subTest(index=index), self.assertRaises(ValueError):
                full.validate_parallel_workflow(self.workflow.replace(self.region, changed).encode())
        for changed in (self.workflow.replace('--include "$RUNNER_TEMP/sobalink-ci-context-metrics.jsonl" ', ''),
                        self.workflow.replace(self.wait, '').replace(self.after, self.after + self.wait)):
            with self.assertRaises(ValueError):
                full.validate_parallel_workflow(changed.encode())

    def test_runtime_audit_requires_every_gate_and_same_run_commit_join(self):
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
        for key, value in (('run_id', 11), ('run_attempt', 2), ('head_sha', 'b' * 40)):
            rows = copy.deepcopy(original)
            rows[0][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                audit(rows)


if __name__ == '__main__':
    unittest.main()
