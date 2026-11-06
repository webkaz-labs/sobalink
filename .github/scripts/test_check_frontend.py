"""Small fake npm/process fixtures; no frontend dependency downloads or builds."""
import contextlib
import importlib.util
import io
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


frontend = load('check_frontend', 'check-frontend.py')


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

    def commands(self):
        for call in self.process.call_args_list:
            self.assertEqual(call.kwargs, dict(cwd=self.web, check=True))
        return [call.args[0] for call in self.process.call_args_list]

    def test_default_browser_and_local_run_all_tests_and_two_builds(self):
        self.assertEqual(frontend.main([]), 0)
        npm = frontend.npm
        self.assertEqual(self.commands(), [[npm, 'ci', '--no-audit', '--no-fund'],
            [npm, 'run', 'test'], [npm, 'run', 'build'],
            [npm, 'ci', '--no-audit', '--no-fund'], [npm, 'run', 'build']])
        self.assertEqual([call.args[0] for call in self.versions.call_args_list],
                         [['node', '--version'], [npm, '--version']])

    def test_build_only_installs_and_reproduces_twice_without_tests(self):
        self.assertEqual(frontend.main(['--build-only']), 0)
        npm = frontend.npm
        self.assertEqual(self.commands(), [[npm, 'ci', '--no-audit', '--no-fund'],
            [npm, 'run', 'build'], [npm, 'ci', '--no-audit', '--no-fund'], [npm, 'run', 'build']])

    def test_build_only_does_not_repeat_tests_for_any_full_ci_intent(self):
        for event in ('pull_request', 'push', 'schedule', 'workflow_dispatch'):
            with self.subTest(event=event), patch.dict(os.environ, {
                    'GITHUB_ACTIONS': 'true', 'GITHUB_EVENT_NAME': event, 'FORCE_FULL': 'true'}):
                self.process.reset_mock()
                self.versions.side_effect = ['v24.19.0', '11.9.0']
                self.assertEqual(frontend.main(['--build-only']), 0)
                self.assertNotIn([frontend.npm, 'run', 'test'], self.commands())
                self.assertEqual(len(self.commands()), 4)

    def test_every_failed_process_stops_the_check_in_both_modes(self):
        for args, count in (([], 5), (['--build-only'], 4)):
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

    def test_stale_assets_nondeterministic_rebuild_and_changed_lock_fail(self):
        for mode, message in (('stale', 'checked-in frontend dist differs'),
                              ('unstable', 'not byte-identical'), ('lock', 'changed dependency lock')):
            for args in ([], ['--build-only']):
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

    def test_unknown_obsolete_or_abbreviated_flags_are_rejected(self):
        with contextlib.redirect_stderr(io.StringIO()):
            for args in (['--skip-tests'], ['--native-plan', 'synthetic.json'], ['--build']):
                with self.assertRaises(SystemExit):
                    frontend.main(args)
        self.process.assert_not_called()


if __name__ == '__main__':
    unittest.main()
