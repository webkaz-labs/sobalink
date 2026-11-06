"""Small fake npm/process fixtures; no frontend dependency downloads or builds."""
import contextlib
import importlib.util
import io
import os
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


frontend = load('check_frontend', 'check-frontend.py')
policy = load('frontend_selection_fixture', 'ci-regressions.py')


def plan(*paths, reason='unproven_or_shared_runtime_path', scope='full'):
    return dict(scope=scope, reason=reason, base_sha='a' * 40, changed_paths=list(paths))


class SelectionTests(unittest.TestCase):
    def selected(self, result):
        # Import the real shared selection policy, replacing only its Git/event
        # proof input. The proof's mismatch/fallback cases have separate tests.
        spec = SimpleNamespace(loader=Mock())
        with patch.object(frontend.importlib.util, 'spec_from_file_location', return_value=spec), \
                patch.object(frontend.importlib.util, 'module_from_spec', return_value=policy), \
                patch.object(policy, 'verified_plan', return_value=result) as verify:
            selected = frontend.native_tests(Path('synthetic-plan.json'))
            verify.assert_called_once_with(Path('synthetic-plan.json'))
            return selected

    def test_verified_product_edits_share_the_full_browser_suite(self):
        for path in ('internal/core/core.go', 'poc/native/shell.go',
                     'web/src/Example.tsx', 'web/browser/example.mjs'):
            self.assertFalse(self.selected(plan(path)), path)
        for reason in ('reviewed_presentation', 'reviewed_safe_scope_union'):
            self.assertFalse(self.selected(plan('cmd/soba/help.go', 'web/src/Example.tsx',
                                                scope='native-short', reason=reason)))

    def test_policy_toolchain_dependencies_unknown_or_full_intent_repeat_tests(self):
        for path in ('.github/scripts/check-frontend.py', '.github/scripts/ci-regressions.py',
                     '.github/workflows/ci.yml', 'web/package.json', 'web/package-lock.json',
                     'web/vite.config.ts', 'web/tsconfig.json', 'go.mod', 'go.sum',
                     'cmd/prepare-engine/main.go', 'internal/engineadaptation/source.go',
                     'unknown/source.ts'):
            self.assertTrue(self.selected(plan('web/src/Example.tsx', path)), path)
        for reason in ('forced_full', 'scheduled_full', 'classification_failed',
                       'unverified_inputs', 'generated_assets_without_source_change'):
            self.assertTrue(self.selected(plan('web/src/Example.tsx', reason=reason)), reason)
        for result in (None, {}, plan(), plan('web/src/Example.tsx', scope='unknown'),
                       plan('web/src/Example.tsx', scope='frontend')):
            self.assertTrue(self.selected(result), result)

    def test_linux_common_python_exception_does_not_skip_full_frontend_tests(self):
        env = dict(GITHUB_ACTIONS='true', GITHUB_WORKFLOW='Cross-platform CI', GITHUB_JOB='native',
                   RUNNER_ENVIRONMENT='github-hosted', RUNNER_OS='Linux', RUNNER_ARCH='X64')
        with patch.dict(os.environ, env, clear=True):
            self.assertTrue(self.selected(plan('.github/workflows/ci.yml')))

    def test_real_missing_plan_import_path_fails_closed(self):
        with patch.dict(os.environ, {'GITHUB_SHA': ''}), tempfile.TemporaryDirectory() as directory:
            self.assertTrue(frontend.native_tests(Path(directory) / 'missing.json'))


class ProcessTests(unittest.TestCase):
    def setUp(self):
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        directory = self.stack.enter_context(tempfile.TemporaryDirectory())
        self.web = Path(directory) / 'web'
        (self.web / 'dist').mkdir(parents=True)
        (self.web / 'dist/index.html').write_bytes(b'<main>synthetic</main>')
        (self.web / 'package-lock.json').write_bytes(b'{"synthetic": true}\n')
        self.stack.enter_context(patch.object(frontend, 'web', self.web))
        self.stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
        self.versions = self.stack.enter_context(patch.object(
            frontend.subprocess, 'check_output', side_effect=['v24.19.0\n', '11.9.0\n']))
        self.process = self.stack.enter_context(patch.object(frontend.subprocess, 'run'))
        self.selection = self.stack.enter_context(patch.object(frontend, 'native_tests', return_value=False))

    def commands(self):
        for call in self.process.call_args_list:
            self.assertEqual(call.kwargs, dict(cwd=self.web, check=True))
        return [call.args[0] for call in self.process.call_args_list]

    def test_default_browser_release_and_local_run_all_tests_and_two_builds(self):
        self.assertEqual(frontend.main([]), 0)
        self.selection.assert_not_called()
        npm = frontend.npm
        self.assertEqual(self.commands(), [[npm, 'ci', '--no-audit', '--no-fund'],
            [npm, 'run', 'test'], [npm, 'run', 'build'],
            [npm, 'ci', '--no-audit', '--no-fund'], [npm, 'run', 'build']])
        self.assertEqual([call.args[0] for call in self.versions.call_args_list],
                         [['node', '--version'], [npm, '--version']])

    def test_ordinary_native_run_still_installs_and_reproduces_twice(self):
        self.assertEqual(frontend.main(['--native-plan', 'synthetic.json']), 0)
        self.selection.assert_called_once_with(Path('synthetic.json'))
        npm = frontend.npm
        self.assertEqual(self.commands(), [[npm, 'ci', '--no-audit', '--no-fund'],
            [npm, 'run', 'build'], [npm, 'ci', '--no-audit', '--no-fund'], [npm, 'run', 'build']])

    def test_full_native_plan_retains_the_full_sequence(self):
        self.selection.return_value = True
        self.assertEqual(frontend.main(['--native-plan', 'synthetic.json']), 0)
        self.assertEqual(len(self.commands()), 5)
        self.assertEqual(self.commands()[1], [frontend.npm, 'run', 'test'])

    def test_every_failed_process_stops_the_check_in_both_modes(self):
        for args, count in (([], 5), (['--native-plan', 'synthetic.json'], 4)):
            for failure in range(count):
                self.process.reset_mock()
                self.versions.side_effect = ['v24.19.0', '11.9.0']
                self.process.side_effect = [None] * failure + [subprocess.CalledProcessError(7, 'fake-npm')]
                with self.subTest(args=args, failure=failure), self.assertRaises(subprocess.CalledProcessError):
                    frontend.main(args)
                self.assertEqual(len(self.commands()), failure + 1)

    def test_invalid_toolchain_cannot_start_installation(self):
        for values in (['v24.0.0', '11.9.0'], ['v24.19.0', '11.0.0']):
            self.versions.side_effect = values
            with self.assertRaises(AssertionError):
                frontend.main([])
            self.process.assert_not_called()

    def test_plan_verification_error_cannot_become_a_skipped_test_success(self):
        self.selection.side_effect = ValueError('synthetic invalid proof')
        with self.assertRaises(ValueError):
            frontend.main(['--native-plan', 'synthetic.json'])
        self.process.assert_not_called()

    def test_stale_assets_nondeterministic_rebuild_and_changed_lock_fail(self):
        for mode, message in (('stale', 'checked-in frontend dist differs'),
                              ('unstable', 'not byte-identical'), ('lock', 'changed dependency lock')):
            for args in ([], ['--native-plan', 'synthetic.json']):
                self.versions.side_effect = ['v24.19.0', '11.9.0']
                (self.web / 'dist/index.html').write_bytes(b'<main>synthetic</main>')
                builds = 0
                def fake_npm(command, **kwargs):
                    nonlocal builds
                    if command[1:] == ['run', 'build']:
                        builds += 1
                        if mode == 'stale' or (mode == 'unstable' and builds == 2):
                            (self.web / 'dist/index.html').write_bytes(b'<main>changed</main>')
                        if mode == 'lock':
                            with (self.web / 'package-lock.json').open('ab') as output:
                                output.write(b' ')
                self.process.side_effect = fake_npm
                with self.subTest(mode=mode, args=args), self.assertRaisesRegex(AssertionError, message):
                    frontend.main(args)

    def test_missing_entrypoint_is_not_a_pass(self):
        (self.web / 'dist/index.html').unlink()
        with self.assertRaisesRegex(AssertionError, 'missing frontend entry point'):
            frontend.main([])
        self.process.assert_not_called()

    def test_no_unverified_skip_flag_or_abbreviated_plan_flag(self):
        with contextlib.redirect_stderr(io.StringIO()):
            for args in (['--skip-tests'], ['--native', 'synthetic.json'], ['--native-plan']):
                with self.assertRaises(SystemExit):
                    frontend.main(args)
        self.process.assert_not_called()


if __name__ == '__main__':
    unittest.main()
