"""Lightweight archive-binary checks replacing the separate UI HEAD lane.

Images use built-in fictional demo state, remain temporary and are not uploaded.
Unsigned/ad-hoc state is test-build evidence, never authenticated release signing.
"""
import os
from pathlib import Path
import struct
import subprocess
import sys
import tempfile


def verify_scope(companion, daemon, target):
    if {'win32': 'windows-amd64', 'darwin': 'darwin-arm64'}.get(sys.platform) != target:
        raise ValueError('native UI archive smoke requires the matching supported host')
    with tempfile.TemporaryDirectory(prefix='soba-native-ui-smoke-') as directory:
        for pane in ('devices', 'messages', 'files'):
            output = Path(directory) / (pane + '.png')
            command = [str(companion), '--demo', '--locale', 'ja']
            if pane != 'devices':
                command += ['--render-pane', pane]
            command += ['--render', str(output)]
            subprocess.run(command, check=True, capture_output=True, timeout=60)
            data = output.read_bytes()
            if (len(data) < 24 or data[:8] != b'\x89PNG\r\n\x1a\n'
                    or struct.unpack('>II', data[16:24]) != (460, 560)):
                raise ValueError('packaged native UI pane did not render its expected image')
    for binary in (companion, daemon):
        if target == 'windows-amd64':
            env = os.environ.copy()
            env['SOBA_TEST_SIGNATURE_BINARY'] = str(binary)
            command = ['pwsh', '-NoLogo', '-NoProfile', '-NonInteractive', '-Command',
                       "$ErrorActionPreference = 'Stop'; "
                       "if ((Get-AuthenticodeSignature -LiteralPath $env:SOBA_TEST_SIGNATURE_BINARY).Status -ne 'NotSigned') "
                       "{ throw 'Unexpected test-binary signing state' }"]
            subprocess.run(command, env=env, check=True, capture_output=True, timeout=30)
        else:
            result = subprocess.run(['codesign', '-dv', '--verbose=2', str(binary)],
                                    check=True, capture_output=True, text=True, timeout=30)
            if 'Signature=adhoc' not in result.stderr or 'TeamIdentifier=not set' not in result.stderr:
                raise ValueError('native UI test binary has unexpected signing state')
