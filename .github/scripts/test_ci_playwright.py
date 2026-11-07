"""No privileged package installation in portable policy tests."""
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('ci_playwright', ROOT / '.github/scripts/ci-playwright.py')
setup = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(setup)


class PlaywrightSetupTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.cli = self.root / 'web/node_modules/@playwright/test/cli.js'
        self.cli.parent.mkdir(parents=True)
        self.cli.write_text('// synthetic fixture, never executed\n')
        self.keyring = self.root / 'ubuntu-archive-keyring.gpg'
        self.keyring.write_bytes(b'synthetic fixture')
        for target, value in (('ROOT', self.root), ('KEYRING', self.keyring)):
            context = patch.object(setup, target, value)
            context.start()
            self.addCleanup(context.stop)
        for target, value in (('freedesktop_os_release', {'ID': 'ubuntu', 'VERSION_ID': '24.04'}),
                              ('machine', 'x86_64')):
            context = patch.object(setup.platform, target, return_value=value)
            context.start()
            self.addCleanup(context.stop)
        context = patch.object(setup.shutil, 'which', return_value='/official/node')
        context.start()
        self.addCleanup(context.stop)

    def test_complete_official_dependencies_then_user_browser_install(self):
        calls = []
        def execute(command, **kwargs):
            calls.append(command)
            self.assertTrue(kwargs['check'])
            self.assertGreater(kwargs['timeout'], 0)
            if len(calls) == 1:
                self.assertEqual(command[:3], ['sudo', '--', 'env'])
                config = Path(command[3].removeprefix('APT_CONFIG='))
                self.config = config
                text = config.read_text()
                self.assertIn('Dir::Etc::sourceparts "-";', text)
                self.assertIn('Acquire::https::Timeout "30";', text)
                self.assertIn('APT::Get::List-Cleanup "false";', text)
                self.assertIn('APT::Update::Error-Mode "any";', text)
                self.assertEqual((config.parent / 'ubuntu.sources').read_text(), setup.SOURCES)
                if os.name == 'posix':
                    self.assertEqual(config.parent.stat().st_mode & 0o777, 0o755)
                self.assertEqual(command[4:], ['/official/node', str(self.cli), 'install-deps', 'chromium'])
            else:
                self.assertEqual(command, ['/official/node', str(self.cli), 'install', 'chromium'])
                self.assertFalse(self.config.exists())
        with patch.object(setup.subprocess, 'run', side_effect=execute):
            setup.install()
        self.assertEqual(len(calls), 2)
        self.assertFalse(self.config.exists())

    def test_dependency_failure_is_fatal_and_prevents_browser_install(self):
        for error in (subprocess.CalledProcessError(1, 'synthetic'),
                      subprocess.TimeoutExpired('synthetic', 300)):
            with self.subTest(error=error), patch.object(setup.subprocess, 'run', side_effect=error) as run:
                with self.assertRaises(type(error)):
                    setup.install()
                run.assert_called_once()
                self.assertFalse(Path(run.call_args.args[0][3].removeprefix('APT_CONFIG=')).exists())

    def test_browser_failure_is_fatal(self):
        with patch.object(setup.subprocess, 'run', side_effect=[None, subprocess.CalledProcessError(1, 'synthetic')]):
            with self.assertRaises(subprocess.CalledProcessError):
                setup.install()

    def test_unknown_os_architecture_or_missing_keyring_fails_before_install(self):
        for release in ({'ID': 'debian', 'VERSION_ID': '13'}, {'ID': 'ubuntu', 'VERSION_ID': '22.04'}, {}):
            with patch.object(setup.platform, 'freedesktop_os_release', return_value=release), patch.object(setup.subprocess, 'run') as run:
                with self.assertRaises(ValueError):
                    setup.install()
                run.assert_not_called()
        with patch.object(setup.platform, 'machine', return_value='aarch64'), patch.object(setup.subprocess, 'run') as run:
            with self.assertRaises(ValueError):
                setup.install()
            run.assert_not_called()
        self.keyring.unlink()
        with patch.object(setup.subprocess, 'run') as run:
            with self.assertRaises(ValueError):
                setup.install()
            run.assert_not_called()

    def test_missing_locked_cli_fails_before_install(self):
        self.cli.unlink()
        with patch.object(setup.subprocess, 'run') as run:
            with self.assertRaises(ValueError):
                setup.install()
            run.assert_not_called()

    def test_sources_remain_official_signed_and_do_not_weaken_validation(self):
        self.assertEqual(setup.SOURCES.count('Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg'), 2)
        self.assertEqual([line for line in setup.SOURCES.splitlines() if line.startswith('URIs:')],
                         ['URIs: https://archive.ubuntu.com/ubuntu', 'URIs: https://security.ubuntu.com/ubuntu'])
        self.assertIn('Suites: noble noble-updates noble-backports', setup.SOURCES)
        self.assertIn('Suites: noble-security', setup.SOURCES)
        source = (ROOT / '.github/scripts/ci-playwright.py').read_text()
        for forbidden in ('trusted=yes', 'allow-unauthenticated', 'AllowInsecure',
                          'PLAYWRIGHT_SKIP_VALIDATE', 'executablePath', 'sources.list.d/'):
            self.assertNotIn(forbidden, source)

    def test_all_seven_existing_consumers_share_the_helper(self):
        for name, expected in (('ci.yml', 4), ('activation-diagnostic.yml', 4)):
            source = (ROOT / '.github/workflows' / name).read_text()
            self.assertEqual(source.count('python3 -B .github/scripts/ci-playwright.py'), expected)
            self.assertNotIn('install --with-deps chromium', source)


if __name__ == '__main__':
    unittest.main()
