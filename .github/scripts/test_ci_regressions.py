"""Offline selection checks; never replace the shared discovery with a subset."""
import contextlib
import importlib.util
import io
from pathlib import Path
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('regressions', Path(__file__).with_name('ci-regressions.py'))
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


def plan(*paths):
    return dict(scope='full', reason='unproven_or_shared_runtime_path',
                base_sha='a' * 40, changed_paths=list(paths))


class SelectionTests(unittest.TestCase):
    def test_product_source_keeps_shared_discovery_and_native_contracts(self):
        for path in ('internal/core/core.go', 'cmd/soba/help.go', 'poc/native/shell.go',
                     'web/src/components/Example.tsx', 'web/dist/index.html'):
            self.assertFalse(gate.full_native_regressions(plan(path)), path)

    def test_policy_dependencies_build_inputs_and_unknown_paths_keep_full_matrix(self):
        for path in ('.github/scripts/ci-go-test.py', '.github/scripts/test_ci_go_test.py',
                     '.github/workflows/ci.yml', 'go.mod', 'go.sum', 'poc/native/go.mod',
                     'web/package-lock.json', 'web/package.json', 'AGENTS.md',
                     'cmd/prepare-engine/main.go', 'internal/engineadaptation/source.go',
                     'internal/lifecycleadaptation/source.go', 'new/location.go',
                     'internal/core/new-input.json', 'docs/DEVELOPMENT_PRINCIPLES.en.md',
                     '../internal/core/core.go', 'internal//core.go', 'internal/core/../x.go',
                     'internal\\core\\x.go'):
            self.assertTrue(gate.full_native_regressions(plan('internal/core/core.go', path)), path)

    def test_forced_scheduled_or_invalid_proof_never_selects_subset(self):
        original = plan('internal/core/core.go')
        for key, value in (('reason', 'scheduled_full'), ('reason', 'forced_full'),
                           ('reason', 'classification_failed'), ('reason', 'unverified_inputs'),
                           ('base_sha', None), ('base_sha', 'x'), ('scope', 'unknown'),
                           ('changed_paths', []), ('changed_paths', [None]), ('changed_paths', [{}]), ('changed_paths', 'internal/core/core.go'),
                           ('changed_paths', ['internal/core/core.go'] * 2)):
            changed = original | {key: value}
            self.assertTrue(gate.full_native_regressions(changed), (key, value))
        self.assertTrue(gate.full_native_regressions(None))

    def test_platform_contracts_are_explicit_and_do_not_substitute_linux_for_macos(self):
        windows = gate.native_tests('win32', 'nt')
        linux = gate.native_tests('linux', 'posix')
        darwin = gate.native_tests('darwin', 'posix')
        self.assertEqual(windows, gate.NATIVE_TESTS)
        self.assertEqual(linux, gate.NATIVE_TESTS + gate.POSIX_TESTS)
        self.assertEqual(darwin, gate.NATIVE_TESTS + gate.POSIX_TESTS + gate.DARWIN_TESTS)
        for names in (windows, linux, darwin):
            self.assertEqual(len(names), len(set(names)))
            for method in ('test_capture_is_small_private_and_preserves_nonzero',
                           'test_capture_stops_exact_child_on_timeout_and_handles_launch_failure',
                           'test_capture_stdin_is_bounded_and_converter_receives_only_private_bytes'):
                self.assertIn('test_ci_nearby_native.NativeNearbyTests.' + method, names)
        for names in (windows, linux, darwin):
            self.assertIn('test_ci_native_ui_scope.NativeUIScopeTests.test_race_wrapper_uses_utf8_and_still_requires_valid_ipc_pass', names)
        self.assertIn('test_ci_native_core.RealPosixOwnershipTests', darwin)

    def test_common_target_avoids_a_fifth_full_run_without_losing_other_targets(self):
        env = dict(GITHUB_ACTIONS='true', GITHUB_WORKFLOW='Cross-platform CI', GITHUB_JOB='native',
                   RUNNER_ENVIRONMENT='github-hosted', RUNNER_OS='Linux', RUNNER_ARCH='X64')
        for selected in (plan('.github/workflows/ci.yml'), {}, None):
            self.assertTrue(gate.full_suite('common', selected, 'linux', 'x86_64', env))
            self.assertFalse(gate.full_suite('native', selected, 'linux', 'x86_64', env))
            for platform, machine in (('linux', 'aarch64'), ('darwin', 'arm64'), ('win32', 'AMD64'),
                                      ('linux', 'unknown')):
                self.assertTrue(gate.full_suite('native', selected, platform, machine, env))
            for key in env:
                self.assertTrue(gate.full_suite('native', selected, 'linux', 'x86_64', env | {key: ''}))
            self.assertTrue(gate.full_suite('native', selected, 'linux', 'x86_64', {}))

    def test_all_named_contracts_resolve_without_running_them(self):
        loader = unittest.TestLoader()
        selected = loader.loadTestsFromNames(gate.native_tests('darwin', 'posix'))
        self.assertEqual(loader.errors, [])
        self.assertGreater(selected.countTestCases(), 50)

    def test_common_always_discovers_every_file(self):
        with patch.object(gate, 'verified_plan', return_value=plan('internal/core/core.go')), \
                patch.object(gate, 'suite', return_value=unittest.TestSuite()) as selected, \
                patch.object(gate.unittest, 'TextTestRunner') as runner:
            runner.return_value.run.return_value.wasSuccessful.return_value = True
            self.assertEqual(gate.main(['common', '--plan', 'synthetic.json']), 0)
            selected.assert_called_once_with(True, exclude_common=False)

    def test_failed_tests_fail_the_command(self):
        with patch.object(gate, 'verified_plan', return_value=plan('internal/core/core.go')), \
                patch.object(gate, 'suite', return_value=unittest.TestSuite()) as selected, \
                patch.object(gate.unittest, 'TextTestRunner') as runner:
            runner.return_value.run.return_value.wasSuccessful.return_value = False
            self.assertEqual(gate.main(['native', '--plan', 'synthetic.json']), 1)
            selected.assert_called_once_with(False, exclude_common=False)

    def test_new_unmarked_method_class_and_module_remain_in_broad_discovery(self):
        class Existing(unittest.TestCase):
            def test_reviewed(self): pass
            def test_new(self): pass
        Existing.test_reviewed._ci_common_only = True
        class New(unittest.TestCase):
            def test_new(self): pass
        New.__module__ = 'test_new_unclassified_module'
        cases = [Existing('test_reviewed'), Existing('test_new'), New('test_new')]
        original = unittest.TestSuite([unittest.TestSuite(cases[:2]), cases[2]])
        with patch.object(gate.unittest.TestLoader, 'discover', return_value=original), \
                contextlib.redirect_stdout(io.StringIO()):
            common = gate.suite(True)
            native = gate.suite(True, exclude_common=True)
        self.assertEqual(common.countTestCases(), 3)
        self.assertEqual([case.id() for case in native], [case.id() for case in cases[1:]])
        self.assertEqual(original.countTestCases(), 3)

    def test_class_markers_inherited_methods_and_overrides_cannot_exempt_other_methods(self):
        class Parent(unittest.TestCase):
            _ci_common_only = True
            def test_reviewed(self): pass
            def test_unmarked(self): pass
        Parent.test_reviewed._ci_common_only = True
        class Child(Parent):
            def test_added(self): pass
        class Override(Parent):
            def test_reviewed(self): pass
        cases = [Parent('test_unmarked'), Child('test_reviewed'), Child('test_added'),
                 Override('test_reviewed')]
        native = gate.without_common_only(unittest.TestSuite(cases))
        self.assertEqual(list(native), cases)

    def test_only_an_exact_boolean_method_marker_allows_exclusion(self):
        class Fixture(unittest.TestCase):
            def test_case(self): pass
        for value in (None, False, 1, 'true', object()):
            with self.subTest(value=value):
                Fixture.test_case._ci_common_only = value
                case = Fixture('test_case')
                self.assertEqual(list(gate.without_common_only(unittest.TestSuite([case]))), [case])
        Fixture.test_case._ci_common_only = True
        self.assertEqual(gate.without_common_only(unittest.TestSuite([Fixture('test_case')])).countTestCases(), 0)

    def test_only_known_shared_native_broad_execution_excludes_common_methods(self):
        env = dict(GITHUB_ACTIONS='true', GITHUB_WORKFLOW='Cross-platform CI', GITHUB_JOB='native',
                   RUNNER_ENVIRONMENT='github-hosted')
        cases = [('native', plan('.github/workflows/ci.yml'), env, True, True),
                 ('common', plan('.github/workflows/ci.yml'), env, True, False),
                 ('native', plan('internal/core/core.go'), env, False, False),
                 ('native', None, env, True, True),
                 *[('native', None, env | {key: ''}, True, False) for key in env]]
        for role, value, environment, full, exclude in cases:
            with self.subTest(role=role, environment=environment, full=full), \
                    patch.dict(gate.os.environ, environment, clear=True), \
                    patch.object(gate.sys, 'platform', 'linux'), \
                    patch.object(gate.platform, 'machine', return_value='aarch64'), \
                    patch.object(gate, 'verified_plan', return_value=value), \
                    patch.object(gate, 'suite', return_value=unittest.TestSuite()) as selected, \
                    patch.object(gate.unittest, 'TextTestRunner') as runner, \
                    contextlib.redirect_stdout(io.StringIO()):
                runner.return_value.run.return_value.wasSuccessful.return_value = True
                self.assertEqual(gate.main([role, '--plan', 'synthetic.json']), 0)
                selected.assert_called_once_with(full, exclude_common=exclude)

    def test_exclusion_never_filters_ordinary_selection_or_accepts_an_empty_result(self):
        with self.assertRaises(ValueError):
            gate.suite(False, exclude_common=True)
        with patch.object(gate.unittest.TestLoader, 'discover',
                          return_value=unittest.TestSuite([unittest.FunctionTestCase(lambda: None)])), \
                patch.object(gate, 'without_common_only', return_value=unittest.TestSuite()), \
                self.assertRaises(ValueError):
            gate.suite(True, exclude_common=True)

    def test_real_discovery_omits_only_explicitly_marked_full_proof_methods(self):
        def cases(selected):
            for case in selected:
                if isinstance(case, unittest.TestSuite):
                    yield from cases(case)
                else:
                    yield case
        common = list(cases(gate.suite(True)))
        native = list(gate.without_common_only(unittest.TestSuite(common)))
        excluded = {case.id() for case in common} - {case.id() for case in native}
        self.assertTrue(excluded)
        self.assertTrue(all(name.startswith('test_ci_full_validation.FullProofTests.') for name in excluded))
        self.assertEqual(excluded, {case.id() for case in common
            if getattr(vars(type(case)).get(case._testMethodName), '__dict__', {}).get('_ci_common_only') is True})
        self.assertEqual(len(common), len(native) + len(excluded))

    def test_empty_or_failed_selection_is_not_a_pass(self):
        for errors in ([], ['synthetic import failure']):
            with patch.object(gate.unittest, 'TestLoader') as factory:
                factory.return_value.errors = errors
                factory.return_value.discover.return_value = unittest.TestSuite()
                for exclude in (False, True):
                    with self.subTest(exclude=exclude), self.assertRaises(ValueError):
                        gate.suite(True, exclude_common=exclude)


if __name__ == '__main__':
    unittest.main()
