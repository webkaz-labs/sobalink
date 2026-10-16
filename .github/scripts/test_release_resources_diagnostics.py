"""Fixed stage reports must not disclose remote exceptions or private values."""
import contextlib
import importlib.util
import io
import os
import pathlib
import textwrap
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

ROOT = pathlib.Path(__file__).resolve().parents[2]
SECRET = 'private-token-private-url-private-path'


class ResourceDiagnosticsTests(unittest.TestCase):
    def workflow(self):
        return (ROOT / '.github/workflows/release-resources.yml').read_text()

    def code(self):
        marker = "          python - <<'PY' > /dev/null\n"
        return textwrap.dedent(self.workflow().split(marker, 1)[1].split('          PY\n', 1)[0])

    def run_fixture(self, stage=None, error=None):
        release = SimpleNamespace(release_state=Mock(return_value=(None, {'id': 1})),
                                  require=Mock(), download_public=Mock())
        release.load_validator = Mock(return_value=release)
        loader = SimpleNamespace(exec_module=Mock())
        selected = {'import': loader.exec_module, 'historical-validator': release.load_validator, 'release-state': release.release_state,
                    'release-id': release.require, 'public-download-and-assets': release.download_public}
        if stage:
            selected[stage].side_effect = error
        stdout, stderr = io.StringIO(), io.StringIO()
        env = {'RELEASE_VERSION': '0.3.0-alpha.2', 'TESTED_COMMIT': 'a' * 40,
               'RELEASE_ID': '1', 'RUNNER_TEMP': '/synthetic'}
        with patch.dict(os.environ, env), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr), \
             patch.object(importlib.util, 'spec_from_file_location', return_value=SimpleNamespace(loader=loader)), \
             patch.object(importlib.util, 'module_from_spec', return_value=release):
            if stage:
                with self.assertRaises(SystemExit) as outcome:
                    exec(compile(self.code(), '<workflow-fixture>', 'exec'), {})
                self.assertEqual(outcome.exception.code, 1)
            else:
                exec(compile(self.code(), '<workflow-fixture>', 'exec'), {})
        return release, stdout.getvalue() + stderr.getvalue()

    def test_success_retains_identity_and_download_calls(self):
        release, output = self.run_fixture()
        release.release_state.assert_called_once_with('0.3.0-alpha.2', 'a' * 40, required=True, published=True)
        release.require.assert_called_once_with(True, 'published release identity changed')
        release.download_public.assert_called_once()
        self.assertEqual(output, '')

    def test_every_failure_stage_is_fixed_and_secret_free(self):
        for stage in ('import', 'historical-validator', 'release-state', 'release-id', 'public-download-and-assets'):
            with self.subTest(stage=stage):
                _, output = self.run_fixture(stage, ValueError(SECRET))
                self.assertEqual(output, 'Resource verification failed: ' + stage + ' / ValueError / unclassified\n')

    def test_native_provenance_failure_has_fixed_target_code(self):
        _, output = self.run_fixture('public-download-and-assets', ValueError('invalid native package provenance: darwin-arm64'))
        self.assertEqual(output, 'Resource verification failed: public-download-and-assets / ValueError / native-darwin-arm64\n')

    def test_custom_exception_name_is_not_printed(self):
        error = type('PrivateExceptionName', (Exception,), {})(SECRET)
        _, output = self.run_fixture('release-state', error)
        self.assertEqual(output, 'Resource verification failed: release-state / Exception / unclassified\n')

    def test_shell_reports_fixed_stages_and_retains_crypto_arguments(self):
        workflow = self.workflow()
        for stage in ('python-verification', 'packslip-install', 'packslip-signature', 'bundle-binding', 'github-provenance'):
            self.assertIn('          stage=' + stage + '\n', workflow)
        self.assertIn("          trap 'printf \"Resource verification failed: %s\\n\" \"$stage\" >&2' ERR\n", workflow)
        for required in ('--identity "$RELEASE_IDENTITY" --issuer https://token.actions.githubusercontent.com',
                         '--cert-identity "$RELEASE_IDENTITY" --cert-oidc-issuer https://token.actions.githubusercontent.com',
                         '--source-digest "$TESTED_COMMIT" --source-ref refs/heads/main --deny-self-hosted-runners'):
            self.assertIn(required, workflow)
        self.assertNotIn('continue-on-error', workflow)


if __name__ == '__main__':
    unittest.main()
