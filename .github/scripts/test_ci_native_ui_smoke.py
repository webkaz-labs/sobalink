"""Offline command contracts, not evidence of running a real native archive."""
from pathlib import Path
import struct
import subprocess
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import native_ui_smoke as smoke


class NativeUIArchiveSmokeTests(unittest.TestCase):
    def commands(self, host, target, *, image=None, signature=None, failure=None):
        commands = []
        def run(command, **kwargs):
            commands.append((command, kwargs))
            self.assertTrue(kwargs['check'])
            self.assertTrue(kwargs['capture_output'])
            self.assertIn(kwargs['timeout'], (30, 60))
            if failure and command[0] == failure:
                raise subprocess.CalledProcessError(1, command)
            if '--render' in command:
                Path(command[-1]).write_bytes(image if image is not None else
                    b'\x89PNG\r\n\x1a\n' + b'\0' * 8 + struct.pack('>II', 460, 560))
            return SimpleNamespace(stderr=signature if signature is not None else
                                   'Signature=adhoc\nTeamIdentifier=not set\n')
        with patch.object(smoke.sys, 'platform', host), patch.object(smoke.subprocess, 'run', side_effect=run):
            smoke.verify_scope('synthetic-companion', 'synthetic-daemon', target)
        return commands

    def test_actual_binary_panes_and_both_signature_checks(self):
        for host, target in (('win32', 'windows-amd64'), ('darwin', 'darwin-arm64')):
            calls = self.commands(host, target)
            self.assertEqual(len(calls), 5)
            self.assertTrue(all(c[0][:5] == ['synthetic-companion', '--demo', '--locale', 'ja', '--render-pane']
                                for c in calls[1:3]))
            self.assertEqual([c[0][5] for c in calls[1:3]], ['messages', 'files'])
            for command, kwargs in calls[3:]:
                if host == 'win32':
                    self.assertIn('Get-AuthenticodeSignature -LiteralPath', command[-1])
                    self.assertIn("-ne 'NotSigned'", command[-1])
                    self.assertIn(kwargs['env']['SOBA_TEST_SIGNATURE_BINARY'], ('synthetic-companion', 'synthetic-daemon'))
                else:
                    self.assertEqual(command[:3], ['codesign', '-dv', '--verbose=2'])

    def test_wrong_host_bad_image_signature_or_command_failure_blocks(self):
        with patch.object(smoke.sys, 'platform', 'linux'), self.assertRaises(ValueError):
            smoke.verify_scope('companion', 'daemon', 'windows-amd64')
        for image in (b'', b'not a png', b'\x89PNG\r\n\x1a\n' + b'\0' * 16):
            with self.subTest(image=image), self.assertRaises(ValueError):
                self.commands('darwin', 'darwin-arm64', image=image)
        for signature in ('', 'Signature=adhoc', 'TeamIdentifier=not set', 'Authority=Developer ID'):
            with self.subTest(signature=signature), self.assertRaises(ValueError):
                self.commands('darwin', 'darwin-arm64', signature=signature)
        for host, target, failure in (('darwin', 'darwin-arm64', 'codesign'),
                                      ('win32', 'windows-amd64', 'pwsh'),
                                      ('darwin', 'darwin-arm64', 'synthetic-companion')):
            with self.subTest(failure=failure), self.assertRaises(subprocess.CalledProcessError):
                self.commands(host, target, failure=failure)

    def test_only_explicit_scoped_smoke_uses_the_extra_checks(self):
        source = Path(__file__).with_name('smoke-package.py').read_text(encoding='utf-8')
        self.assertIn('if sys.argv[3:] not in ([], ["--native-ui-scope"]):', source)
        self.assertIn('if native_ui_scope:\n        if native_ui is None:', source)
        self.assertIn('verify_scope(companion, binary, build["target"])', source)
        self.assertNotIn('upload', source)


if __name__ == '__main__':
    unittest.main()
