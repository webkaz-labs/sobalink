"""Offline native UI selection, merge coverage and release-boundary contracts."""
import copy
import pathlib
import unittest

from test_ci_impact import impact, classify_paths, blob
from test_ci_coverage import coverage, jobs
import ci_full_validation as full

ROOT = pathlib.Path(__file__).resolve().parents[2]


class NativeUIScopeTests(unittest.TestCase):
    def test_closed_existing_file_and_import_allowlist(self):
        expected = {'frame_view.go', 'view.go', 'conversation_view.go', 'messagepane_view.go',
                    'filepane_view.go', 'tray_view.go', 'theme.go', 'presentation.go',
                    'messagepane_presentation.go', 'filepane_presentation.go',
                    'frame_view_test.go', 'theme_view_test.go', 'presentation_test.go'}
        self.assertEqual(set(impact.NATIVE_UI_IMPORTS), {'poc/native/' + p for p in expected})
        for path in impact.NATIVE_UI_IMPORTS:
            with self.subTest(path=path):
                self.assertEqual(classify_paths(path, 'README.md'), ('native-ui', 'reviewed_native_ui', []))
                impact.validate_go_imports(path, (ROOT / path).read_text(encoding='utf-8'))
                with self.assertRaises(impact.FailClosed):
                    impact.validate_go_imports(path, 'package main; import "net/http"')
                roots = {p: blob() for p in impact.ROOT_DOCS}
                for status, before, after in (
                        ('A', roots, roots | {path: blob()}),
                        ('D', roots | {path: blob()}, roots)):
                    self.assertEqual(impact.classify_delta([{'status': status, 'paths': [path]}], before, after)[0], 'full')

    def test_unreviewed_paths_and_every_runtime_union_stay_full(self):
        for path in ('poc/native/app.go', 'poc/native/model.go', 'poc/native/exchange.go',
                     'poc/native/messagepane.go', 'poc/native/filepane.go', 'poc/native/tray.go',
                     'poc/native/filepane_dialog.go', 'poc/native/go.mod', 'poc/native/go.sum',
                     'poc/native/package_test_build.py', 'poc/native/UI_IMPLEMENTATION.en.md',
                     'poc/native/new_view.go', 'poc/native/theme_windows.go',
                     'poc/native/daemon_integration_test.go', 'internal/uibrand/theme.go',
                     'web/src/App.tsx', 'web/browser/app.spec.mjs', 'web/dist/index.html',
                     'internal/servicepresets/catalog.go', 'cmd/soba/help.go'):
            with self.subTest(path=path):
                self.assertEqual(classify_paths('poc/native/theme.go', path)[0], 'full')

    def test_all_platforms_and_each_named_gate_must_actually_pass(self):
        self.assertFalse(coverage.evaluate_jobs(jobs('native-ui'), 'native-ui'))
        for target, name in coverage.NATIVE_UI_TARGETS.items():
            expected = coverage.NATIVE_UI_SCOPE_STEPS + (coverage.NATIVE_UI_PRODUCT_STEPS if target in coverage.NATIVE_PACKAGE_TARGETS else ())
            for gate in expected:
                for mode in ('missing', 'duplicate', 'skipped', 'failure', 'cancelled', 'pending', None):
                    data = jobs('native-ui')
                    selected = next(j for j in data if j['name'] == name)
                    record = next(s for s in selected['steps'] if s['name'] == gate)
                    if mode == 'missing':
                        selected['steps'].remove(record)
                    elif mode == 'duplicate':
                        selected['steps'].append(copy.deepcopy(record))
                    elif mode == 'pending':
                        record['status'] = 'in_progress'
                    else:
                        record['conclusion'] = mode
                    with self.subTest(target=target, gate=gate, mode=mode), self.assertRaises(ValueError):
                        coverage.evaluate_jobs(data, 'native-ui')

    def test_merge_lane_covers_complete_suite_pinned_tools_and_product(self):
        workflow = (ROOT / coverage.WORKFLOW).read_text(encoding='utf-8')
        caller = workflow.split('  native-ui:\n')[1].split('  manifest-smoke:\n')[0]
        self.assertIn("if: needs.impact.outputs.scope == 'native-ui'", caller)
        self.assertIn('name: native-ui\n', caller)
        self.assertIn('uses: ./.github/workflows/native-ui-checks.yml', caller)
        self.assertIn('mode: scope', caller)
        self.assertNotIn('run:', caller)
        lane = (ROOT / '.github/workflows/native-ui-checks.yml').read_text(encoding='utf-8')
        self.assertIn("name: companion (${{ matrix.goos }}-${{ matrix.goarch }})", lane)
        self.assertIn("  GOTOOLCHAIN: local", lane)
        self.assertIn("  GOWORK: 'off'", lane)
        self.assertIn("  GOFLAGS: -mod=readonly -tags=ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy", lane)
        self.assertIn('fetch-depth: 0', lane)
        self.assertNotIn('pull_request.head.sha', lane)
        self.assertIn("if: inputs.mode == 'scope'", lane)
        self.assertIn('ci-native-ui-source.py "$NATIVE_UI_VALIDATION_MODE" --plan', lane)
        for name in coverage.NATIVE_UI_SCOPE_STEPS + coverage.NATIVE_UI_PRODUCT_STEPS:
            self.assertEqual(lane.count('      - name: ' + name + '\n'), 1)
        self.assertIn('go test -race -count=1 -v -timeout=15m -tags=native_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./...', lane)
        self.assertIn('ci-go-test.py --expect github.com/webkaz-labs/sobalink/poc/native:TestNativeProtectedIPCCoreExchange -- go test', lane)
        self.assertIn('go run github.com/egoist/mygo/cmd/mygo vet .', lane)
        self.assertIn('go vet ./...', lane)
        self.assertIn('gofmt -l .', lane)
        self.assertIn('go build -trimpath', lane)
        self.assertIn('python -B packaging_test.py', lane)
        self.assertIn('go-version: \'1.27.3\'', lane)
        self.assertIn('SOBALINK_NATIVE_GO=$native_go', lane)
        self.assertIn('go-version: \'1.27.1\'', lane)
        self.assertIn('go run ./cmd/package-tool build', lane)
        self.assertIn('python -B .github/scripts/smoke-package.py', lane)
        self.assertIn('"$version" --native-ui-scope', lane)
        self.assertIn("node-version: '24.19.0'", lane)
        self.assertIn('npm install --global npm@11.9.0', lane)
        self.assertIn('npm --prefix web ci --no-audit --no-fund', lane)
        self.assertLess(lane.index('npm --prefix web ci'), lane.index('go run ./cmd/package-tool build'))
        self.assertNotIn('check-frontend.py', lane)
        self.assertNotIn('npm --prefix web run', lane)
        self.assertNotIn('continue-on-error:', lane)
        self.assertNotIn('actions/upload-artifact', lane)

    def test_head_only_duplicate_requires_verified_merge_scope(self):
        workflow = (ROOT / '.github/workflows/native-poc.yml').read_text(encoding='utf-8')
        selection, lane = workflow.split('  isolated-native:\n')
        self.assertIn('fetch-depth: 0', selection)
        self.assertNotIn('pull_request.head.sha', selection)
        self.assertIn('ci-coverage.py plan', selection)
        self.assertIn("if: github.event_name == 'pull_request' && github.event.repository.private == false", selection)
        self.assertIn("if: always() && !cancelled() && !(github.event_name == 'workflow_dispatch' && inputs.ci_lane_smoke) && (needs.merge-scope.result != 'success' || needs.merge-scope.outputs.scope != 'native-ui')", lane)
        self.assertIn('ref: ${{ github.event.pull_request.head.sha || github.sha }}', lane)
        self.assertIn('go test -race -v -count=1 -timeout=15m ./...', lane)

    def test_manual_uses_same_workflow_without_scope_receipt_or_required_check(self):
        workflow = (ROOT / '.github/workflows/native-poc.yml').read_text(encoding='utf-8')
        smoke = workflow.split('  ci-lane-smoke:\n')[1].split('  merge-scope:\n')[0]
        self.assertIn("if: github.event_name == 'workflow_dispatch' && inputs.ci_lane_smoke && github.event.repository.private == false", smoke)
        self.assertIn('name: ci-lane-smoke\n', smoke)
        self.assertIn('uses: ./.github/workflows/native-ui-checks.yml', smoke)
        self.assertIn('mode: smoke', smoke)
        self.assertIn('        default: false', workflow)
        shared = (ROOT / '.github/workflows/native-ui-checks.yml').read_text(encoding='utf-8')
        self.assertIn('  workflow_call:', shared)
        for source in (workflow, shared):
            self.assertNotIn('  ci-required:', source)
            self.assertNotIn('ci-coverage.py finalize', source)
            self.assertNotIn('ci-coverage.json', source)
        self.assertNotIn('  workflow_dispatch:', shared)

    def test_reusable_caller_prefix_is_part_of_each_required_job_identity(self):
        self.assertEqual(coverage.NATIVE_UI_TARGETS,
                         {target: 'native-ui / companion (' + target + ')' for target in coverage.TARGETS})
        for prefix in ('ci-lane-smoke / ', '', 'other / '):
            data = jobs('native-ui')
            for item in data:
                if item['name'] in coverage.NATIVE_UI_TARGETS.values():
                    item['name'] = prefix + item['name'].split(' / ', 1)[1]
            with self.subTest(prefix=prefix), self.assertRaises(ValueError):
                coverage.evaluate_jobs(data, 'native-ui')

    def test_native_ui_is_not_full_or_scheduled_or_release_qualification(self):
        data = jobs('native-ui')
        with self.assertRaises(ValueError):
            coverage.evaluate_jobs(data, 'full')
        with self.assertRaises(ValueError):
            coverage.evaluate_jobs(data, 'native-ui', scheduled=True)
        sources = {path: (ROOT / path).read_bytes() for path in full.SOURCES}
        required = full.required_jobs(sources)
        self.assertTrue(set(coverage.TARGETS.values()).issubset(required))
        self.assertFalse(set(coverage.NATIVE_UI_TARGETS.values()).intersection(required))
        # Even a successful UI-only attempt cannot satisfy exact-source release
        # admission: the independent auditor still requires all backend jobs.
        run = {'id': 1, 'run_attempt': 1, 'head_sha': 'a' * 40}
        for n, item in enumerate(data, 1):
            item.update(run_id=1, run_attempt=1, head_sha=run['head_sha'], id=n)
        with self.assertRaisesRegex(ValueError, 'required full-validation job'):
            full.audit_jobs(lambda _: {'jobs': data, 'total_count': len(data)}, run, required)


if __name__ == '__main__':
    unittest.main()
