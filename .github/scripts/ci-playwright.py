#!/usr/bin/env python3
"""Install complete official Chromium dependencies through Ubuntu's archive.

The hosted Ubuntu mirror can deliver apt payloads very slowly. Use an isolated
source configuration for this command only, retaining Ubuntu signature checks
and Playwright's complete dependency selection. Never reuse test results or
substitute the runner's unrelated Chromium build.
"""
import json
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
KEYRING = Path('/usr/share/keyrings/ubuntu-archive-keyring.gpg')
SOURCES = '''Types: deb
URIs: https://archive.ubuntu.com/ubuntu
Suites: noble noble-updates noble-backports
Components: main restricted universe multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg

Types: deb
URIs: https://security.ubuntu.com/ubuntu
Suites: noble-security
Components: main restricted universe multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
'''


def install():
    release = platform.freedesktop_os_release()
    if (release.get('ID') != 'ubuntu' or release.get('VERSION_ID') != '24.04'
            or platform.machine() != 'x86_64' or not KEYRING.is_file()):
        raise ValueError('Playwright CI setup requires Ubuntu 24.04 x64 and its archive keyring')
    node = shutil.which('node')
    cli = ROOT / 'web/node_modules/@playwright/test/cli.js'
    if not node or not cli.is_file():
        raise ValueError('Install the locked Web dependencies before Playwright CI setup')
    with tempfile.TemporaryDirectory(prefix='soba-playwright-apt-') as temporary:
        directory = Path(temporary)
        # apt's unprivileged download user must be able to read these inputs.
        directory.chmod(0o755)
        sources = directory / 'ubuntu.sources'
        sources.write_text(SOURCES)
        sources.chmod(0o644)
        config = directory / 'apt.conf'
        config.write_text(
            f'Dir::Etc::sourcelist {json.dumps(str(sources))};\n'
            'Dir::Etc::sourceparts "-";\n'
            'APT::Get::List-Cleanup "false";\n'
            'APT::Update::Error-Mode "any";\n'
            'Acquire::Retries "2";\n'
            'Acquire::https::Timeout "30";\n')
        config.chmod(0o644)
        started = time.monotonic()
        # Playwright normally invokes sudo internally, which drops APT_CONFIG.
        # Elevate only install-deps, with the per-command config explicitly set.
        # The locked CLI still chooses every Chromium and tools dependency.
        subprocess.run(['sudo', '--', 'env', f'APT_CONFIG={config}', node,
                        str(cli), 'install-deps', 'chromium'], check=True, timeout=300)
        print(f'Official Playwright system dependencies: {time.monotonic() - started:.1f}s', flush=True)
    started = time.monotonic()
    # Keep browser binaries and their normal dependency validation in the
    # invoking user's cache, not root's cache. Do not cache installed apt state.
    subprocess.run([node, str(cli), 'install', 'chromium'], check=True, timeout=180)
    print(f'Official Playwright Chromium: {time.monotonic() - started:.1f}s', flush=True)


if __name__ == '__main__':
    install()
