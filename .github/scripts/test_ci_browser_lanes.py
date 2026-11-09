"""Split browser work keeps complete same-source coverage and one cache writer."""
import copy
import json
from pathlib import Path
import re
import unittest

import ci_full_validation as full
from test_ci_coverage import coverage, jobs
import test_ci_full_validation as proof_tests

ROOT = Path(__file__).resolve().parents[2]


def lane(workflow, name):
    matches = re.findall(r'^  ' + re.escape(name) + r':\n.*?(?=^  [a-z][a-z-]*:\n|\Z)', workflow, re.M | re.S)
    if len(matches) != 1:
        raise ValueError('missing or duplicate lane')
    return matches[0]


class BrowserLaneTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.sources = {p: (ROOT / p).read_bytes() for p in full.SOURCES}
        cls.workflow = cls.sources[full.WORKFLOW].decode()

    def test_coverage_is_partitioned_with_only_cold_seed_preparation(self):
        ui, native = (lane(self.workflow, name) for name in ('browser', 'browser-native'))
        full.validate_browser_lanes_workflow(self.workflow.encode())
        self.assertEqual(re.findall(r'^    (?:needs|if):.*$', ui, re.M),
                         re.findall(r'^    (?:needs|if):.*$', native, re.M))
        self.assertIn('python .github/scripts/check-frontend.py', ui)
        self.assertNotIn('check-frontend.py', native)
        self.assertIn('npm --prefix web ci', native)
        self.assertNotIn('go test -race', ui)
        self.assertNotIn('npm --prefix web run test:browser\n', native)
        self.assertEqual(self.workflow.count('SOBA_UI_BROWSER_TEST=1 '), 1)
        self.assertEqual(ui.count('go build -tags=soba_e2e,'), 1)
        self.assertEqual(native.count('go build -tags=soba_e2e,'), 1)
        self.assertEqual(self.workflow.count('go build -tags=soba_e2e,'), 2)
        self.assertIn("      - name: Warm UI fixture compilation only for a new browser seed\n"
                      "        if: success() && steps.browser-go-cache-info.outputs.scope != '' && "
                      "steps.browser-go-cache.outputs.cache-matched-key == ''\n", native)
        self.assertIn('SOBA_UI_BROWSER_TEST=1 ', native)
        self.assertIn('actions/cache/restore@', ui)
        self.assertIn('actions/cache/restore@', native)
        self.assertNotIn('actions/cache/save@', ui)
        self.assertEqual(native.count('actions/cache/save@'), 1)
        self.assertIn('name: soba-browser-screenshots\n', ui)
        self.assertNotIn('native-summary.json', ui)
        self.assertIn('name: soba-native-browser-summaries\n', native)
        self.assertNotIn('/playwright-report.json', native)
        self.assertNotIn('/soba-playwright-screenshots/*.png', native)

    def test_every_native_gate_is_required_in_each_browser_scope(self):
        for scope in ('frontend', 'native-short', 'full'):
            coverage.evaluate_jobs(jobs(scope), scope)
            for gate in coverage.NATIVE_BROWSER_STEPS:
                for mode in ('missing', 'duplicate', 'failure', 'skipped', 'cancelled', 'pending'):
                    data = jobs(scope)
                    native = next(j for j in data if j['name'] == 'browser-native')
                    record = next(s for s in native['steps'] if s['name'] == gate)
                    if mode == 'missing': native['steps'].remove(record)
                    elif mode == 'duplicate': native['steps'].append(copy.deepcopy(record))
                    elif mode == 'pending': record['status'] = 'in_progress'
                    else: record['conclusion'] = mode
                    with self.subTest(scope=scope, gate=gate, mode=mode), self.assertRaises(ValueError):
                        coverage.evaluate_jobs(data, scope)
            for mode in ('missing', 'duplicate', 'failure', 'skipped', 'cancelled', 'pending'):
                data = jobs(scope)
                native = next(j for j in data if j['name'] == 'browser-native')
                if mode == 'missing': data.remove(native)
                elif mode == 'duplicate': data.append(copy.deepcopy(native))
                elif mode == 'pending': native['status'] = 'in_progress'
                else: native['conclusion'] = mode
                with self.subTest(scope=scope, job=mode), self.assertRaises(ValueError):
                    coverage.evaluate_jobs(data, scope)
        for scope in ('docs', 'go', 'native-ui'):
            coverage.evaluate_jobs(jobs(scope), scope)

    def test_native_required_inventory_cannot_drop_or_move_a_gate(self):
        original = self.sources['.github/scripts/ci-coverage.py']
        for gate in coverage.NATIVE_BROWSER_STEPS:
            sources = dict(self.sources)
            sources['.github/scripts/ci-coverage.py'] = original.replace(json.dumps(gate).encode(), json.dumps('unreviewed gate').encode(), 1)
            with self.subTest(gate=gate), self.assertRaises(ValueError):
                full.required_jobs(sources)

    def test_lane_mutations_cannot_weaken_source_or_join(self):
        for name in ('browser', 'browser-native'):
            block = lane(self.workflow, name)
            mutations = [block.replace('    needs: impact', '    needs: other'),
                         block.replace('    if: always()', '    if: false && always()'),
                         block.replace("go-version: '1.27.1'", "go-version: '1.27.3'"),
                         block.replace('wait: '+('browser-chromium' if name == 'browser' else 'native-browser-chromium'), 'wait: other'),
                         block.replace('        background: true', '        background: true\n        continue-on-error: true', 1),
                         block.replace('actions/cache/restore@', 'actions/cache/save@', 1),
                         block.replace('          path: |', '          path: .', 1),
                         block.replace('      - name:', '      - if: false\n        name:', 1),
                         block + '      - run: echo unreviewed\n']
            for mutated in mutations:
                self.assertNotEqual(block, mutated)
                with self.subTest(lane=name), self.assertRaises(ValueError):
                    full.validate_browser_lanes_workflow(self.workflow.replace(block, mutated).encode())
        changed = self.workflow.replace('browser, browser-native, go-unit', 'browser, go-unit')
        with self.assertRaises(ValueError): full.validate_browser_lanes_workflow(changed.encode())

    def test_full_proof_binds_new_lane_to_current_run_attempt_and_head(self):
        fixture = proof_tests.FullProofTests()
        fixture.setUp()
        original = copy.deepcopy(fixture.jobs)
        for key, value in (('run_id', 999999), ('run_attempt', 999), ('head_sha', 'f' * 40),
                           ('conclusion', 'skipped'), ('status', 'in_progress')):
            fixture.jobs[:] = copy.deepcopy(original)
            native = next(j for j in fixture.jobs if j['name'] == 'browser-native')
            native[key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                full.audit_jobs(fixture.api, fixture.runs[0], fixture.required, skipped=fixture.skipped)
        fixture.jobs[:] = copy.deepcopy(original)
        full.audit_jobs(fixture.api, fixture.runs[0], fixture.required, skipped=fixture.skipped)

    def test_timing_receipt_includes_both_lanes_and_fixed_gates(self):
        data = coverage.job_timings(jobs('frontend'))
        indexed = {entry['job']: entry for entry in data}
        self.assertEqual([s['step'] for s in indexed['browser-native']['steps']], list(coverage.NATIVE_BROWSER_STEPS))
        self.assertEqual([s['step'] for s in indexed['browser']['steps']], list(coverage.BROWSER_STEPS))
