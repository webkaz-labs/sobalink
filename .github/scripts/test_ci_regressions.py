"""Offline selection checks; never replace the shared discovery with a subset."""
import importlib.util
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
            selected.assert_called_once_with(True)

    def test_failed_tests_fail_the_command(self):
        with patch.object(gate, 'verified_plan', return_value=plan('internal/core/core.go')), \
                patch.object(gate, 'suite', return_value=unittest.TestSuite()) as selected, \
                patch.object(gate.unittest, 'TextTestRunner') as runner:
            runner.return_value.run.return_value.wasSuccessful.return_value = False
            self.assertEqual(gate.main(['native', '--plan', 'synthetic.json']), 1)
            selected.assert_called_once_with(False)

    def test_empty_or_failed_selection_is_not_a_pass(self):
        for errors in ([], ['synthetic import failure']):
            with patch.object(gate.unittest, 'TestLoader') as factory:
                factory.return_value.errors = errors
                factory.return_value.discover.return_value = unittest.TestSuite()
                with self.assertRaises(ValueError):
                    gate.suite(True)


if __name__ == '__main__':
    unittest.main()
