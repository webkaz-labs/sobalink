import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('ci_fonts', ROOT / '.github/scripts/ci-fonts.py')
fonts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fonts)


class FontTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.data = b'fixed fixture bytes'
        self.item = {'path': fonts.FILES[0], 'bytes': len(self.data),
                     'sha256': hashlib.sha256(self.data).hexdigest()}
        self.commit = 'a' * 40

    def download(self, command, **kwargs):
        self.assertEqual(kwargs, {'check': True, 'timeout': 130})
        self.assertEqual(command[-1], f'https://raw.githubusercontent.com/notofonts/noto-cjk/{self.commit}/{self.item["path"]}')
        self.assertIn('--fail', command)
        self.assertEqual(command[command.index('--proto') + 1], '=https')
        self.assertEqual(command[command.index('--proto-redir') + 1], '=https')
        self.assertEqual(command[command.index('--max-time') + 1], '60')
        self.assertEqual(command[command.index('--max-filesize') + 1], str(self.item['bytes']))
        Path(command[command.index('--output') + 1]).write_bytes(self.data)

    def test_manifest_pins_only_two_exact_faces_and_license(self):
        data = fonts.manifest()
        self.assertEqual(data['commit'], '523d033d6cb47f4a80c58a35753646f5c3608a78')
        self.assertEqual(sum(item['bytes'] for item in data['files']), 33504657)
        self.assertEqual([item['path'] for item in data['files']], list(fonts.FILES))
        path = self.root / 'manifest.json'
        for field, value in (('commit', 'main'), ('version', 'latest')):
            altered = dict(data, **{field: value})
            path.write_text(json.dumps(altered))
            with self.assertRaises(ValueError):
                fonts.manifest(path)
        data['files'][0]['path'] = '../../other.otf'
        path.write_text(json.dumps(data))
        with self.assertRaises(ValueError):
            fonts.manifest(path)

    def test_cold_download_is_verified_then_warm_cache_needs_no_network(self):
        with patch.object(fonts.subprocess, 'run', side_effect=self.download) as run:
            target, origin = fonts.obtain(self.root, self.commit, self.item)
        self.assertEqual(origin, 'download')
        self.assertTrue(fonts.verified(target, self.item))
        run.assert_called_once()
        with patch.object(fonts.subprocess, 'run', side_effect=AssertionError('network used on cache hit')):
            self.assertEqual(fonts.obtain(self.root, self.commit, self.item), (target, 'cache'))

    def test_corrupt_cache_is_redownloaded_not_trusted(self):
        target = self.root / Path(self.item['path']).name
        target.write_bytes(b'poisoned fixture')
        with patch.object(fonts.subprocess, 'run', side_effect=self.download):
            self.assertEqual(fonts.obtain(self.root, self.commit, self.item), (target, 'download'))
        self.assertTrue(fonts.verified(target, self.item))

    def test_bad_download_and_network_failure_do_not_install_or_cache(self):
        target = self.root / Path(self.item['path']).name
        def bad(command, **kwargs):
            Path(command[command.index('--output') + 1]).write_bytes(b'wrong bytes')
        with patch.object(fonts.subprocess, 'run', side_effect=bad), self.assertRaises(ValueError):
            fonts.obtain(self.root, self.commit, self.item)
        self.assertFalse(target.exists())
        with patch.object(fonts.subprocess, 'run', side_effect=subprocess.TimeoutExpired('curl', 130)), self.assertRaises(subprocess.TimeoutExpired):
            fonts.obtain(self.root, self.commit, self.item)
        self.assertEqual(list(self.root.iterdir()), [])

    def test_wrong_size_is_not_a_hit_even_with_matching_digest(self):
        target = self.root / 'font.otf'
        target.write_bytes(self.data)
        self.assertFalse(fonts.verified(target, dict(self.item, bytes=1)))

    def test_install_verifies_family_style_version_and_render_visibility(self):
        data = fonts.manifest()
        for item in data['files']:
            item.update(bytes=len(self.data), sha256=self.item['sha256'])
            (self.root / Path(item['path']).name).write_bytes(self.data)
        expected = [f'{fonts.FAMILY}|{style}|131334' for style in ('Regular', 'Bold')]
        expected += [str(self.root / 'installed' / f'NotoSansCJKjp-{style}.otf') + f'\n{fonts.FAMILY}|{style}|131334' for style in ('Regular', 'Bold')]
        with patch.object(fonts.subprocess, 'check_output', side_effect=expected) as probe, patch.object(fonts.subprocess, 'run') as run:
            fonts.install(self.root, self.root / 'installed', data)
        self.assertEqual(probe.call_count, 4)
        run.assert_called_once_with(['fc-cache', '-f', str(self.root / 'installed')], check=True, timeout=60)
        with patch.object(fonts.subprocess, 'check_output', return_value='Fallback Sans|Regular|1'), self.assertRaises(ValueError):
            fonts.install(self.root, self.root / 'installed', data)
        with patch.object(fonts.subprocess, 'check_output', side_effect=expected[:2] + [f'/other/NotoSansCJK-Regular.ttc\n{fonts.FAMILY}|Regular|131334']), patch.object(fonts.subprocess, 'run'), self.assertRaises(ValueError):
            fonts.install(self.root, self.root / 'installed', data)

    def test_symlink_cache_is_not_trusted(self):
        if not hasattr(Path, 'symlink_to'):
            self.skipTest('symlinks unavailable')
        original = self.root / 'original'
        original.write_bytes(self.data)
        target = self.root / 'symlink'
        try:
            target.symlink_to(original)
        except OSError:
            self.skipTest('symlink privileges unavailable')
        self.assertFalse(fonts.verified(target, self.item))


class WorkflowContractTests(unittest.TestCase):
    def test_every_former_font_consumer_uses_one_shared_verified_action(self):
        counts = {'ci.yml': 2, 'docs-review.yml': 1, 'native-poc.yml': 1,
                  'native-ui-checks.yml': 1, 'activation-diagnostic.yml': 2}
        for name, count in counts.items():
            source = (ROOT / '.github/workflows' / name).read_text()
            self.assertEqual(source.count('uses: ./.github/actions/japanese-fonts'), count, name)
            self.assertNotIn('fonts-noto-cjk', source)
            self.assertNotIn('fonts-noto-core', source)
        ci = (ROOT / '.github/workflows/ci.yml').read_text()
        self.assertIn("if: steps.document-selection.outputs.render_required == 'true'\n        timeout-minutes: 5\n        uses: ./.github/actions/japanese-fonts", ci)
        for name in ('native-poc.yml', 'native-ui-checks.yml'):
            self.assertIn("if: runner.os == 'Linux'\n        timeout-minutes: 5\n        uses: ./.github/actions/japanese-fonts",
                          (ROOT / '.github/workflows' / name).read_text())
        self.assertIn('node web/node_modules/@playwright/test/cli.js install --with-deps chromium', ci)
        poc = (ROOT / '.github/workflows/native-poc.yml').read_text().split('  workflow_dispatch:', 1)[0]
        for path in ('.github/actions/japanese-fonts/**', '.github/fonts/**', '.github/scripts/ci-fonts.py'):
            self.assertIn(path, poc)
        self.assertIn('Preserve sanitized UI screenshots and typography measurements', ci)
        self.assertIn('Require document checks and selected rendering', ci)

    def test_cache_contains_only_pinned_upstream_files_not_acceptance_results(self):
        action = (ROOT / '.github/actions/japanese-fonts/action.yml').read_text()
        self.assertEqual(action.count("hashFiles('.github/fonts/noto-cjk-jp.json')"), 2)
        self.assertEqual(action.count('path: ${{ runner.temp }}/soba-ci-fonts'), 2)
        self.assertNotIn('restore-keys', action)
        self.assertNotIn('continue-on-error', action)
        self.assertIn('if ! command -v fc-match', action)
        self.assertIn('apt-get install --yes --no-install-recommends fontconfig', action)
        self.assertNotIn('fonts-noto', action)
        self.assertIn('ci-fonts.py --cache-dir', action)
        self.assertIn('working-directory: ${{ github.workspace }}', action)
        self.assertLess(action.index('ci-fonts.py'), action.index('actions/cache/save'))


if __name__ == '__main__':
    unittest.main()
