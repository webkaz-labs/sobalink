#!/usr/bin/env python3
"""Install only pinned Japanese Regular/Bold fonts; cache bytes are never trusted.

The upstream CJK JP OTFs preserve the family, glyphs and metrics of the matching
JP faces in Ubuntu's larger multi-language TTC package. Other browser/runtime
system dependencies remain managed by their existing official installers.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]
LOCK = ROOT / '.github/fonts/noto-cjk-jp.json'
FILES = ('Sans/OTF/Japanese/NotoSansCJKjp-Regular.otf',
         'Sans/OTF/Japanese/NotoSansCJKjp-Bold.otf', 'LICENSE')
FAMILY = 'Noto Sans CJK JP'


def manifest(path=LOCK):
    data = json.loads(path.read_text(encoding='utf-8'))
    if (data.get('version') != '2.004' or
            not re.fullmatch(r'[0-9a-f]{40}', data.get('commit', '')) or
            [item.get('path') for item in data.get('files', [])] != list(FILES)):
        raise ValueError('invalid pinned Japanese font manifest')
    for item in data['files']:
        if (not re.fullmatch(r'[0-9a-f]{64}', item.get('sha256', '')) or
                type(item.get('bytes')) is not int or not 0 < item['bytes'] < 20_000_000):
            raise ValueError('invalid Japanese font digest or size')
    return data


def verified(path, item):
    return (not path.is_symlink() and path.is_file() and path.stat().st_size == item['bytes']
            and hashlib.sha256(path.read_bytes()).hexdigest() == item['sha256'])


def obtain(cache, commit, item):
    target = cache / Path(item['path']).name
    if verified(target, item):
        return target, 'cache'
    # Only an official, commit-addressed upstream URL can supply missing bytes.
    url = f"https://raw.githubusercontent.com/notofonts/noto-cjk/{commit}/{item['path']}"
    with tempfile.TemporaryDirectory(prefix='download-', dir=cache) as temporary:
        candidate = Path(temporary) / target.name
        subprocess.run(['curl', '--fail', '--silent', '--show-error', '--location',
                        '--proto', '=https', '--proto-redir', '=https',
                        '--connect-timeout', '10', '--max-time', '60',
                        '--retry', '1', '--retry-max-time', '90',
                        '--max-filesize', str(item['bytes']), '--output', str(candidate), url], check=True, timeout=130)
        if not verified(candidate, item):
            raise ValueError(f'Japanese font digest mismatch: {target.name}')
        candidate.replace(target)
    return target, 'download'


def install(cache, directory, data):
    cache.mkdir(parents=True, exist_ok=True)
    directory.mkdir(parents=True, exist_ok=True)
    with ThreadPoolExecutor(max_workers=3) as pool:
        obtained = list(pool.map(lambda item: obtain(cache, data['commit'], item), data['files']))
    for item, (source, origin) in zip(data['files'], obtained):
        # Recheck even cache hits before copying or letting fontconfig parse them.
        if not verified(source, item):
            raise ValueError(f'Japanese font cache changed: {source.name}')
        target = directory / source.name
        if target.is_symlink():
            raise ValueError('Japanese font install target must not be a symlink')
        if not verified(target, item):
            shutil.copyfile(source, target)
        if not verified(target, item):
            raise ValueError('Japanese font installation digest mismatch')
        print(f'Japanese font {source.name}: verified {origin}', flush=True)
    for style in ('Regular', 'Bold'):
        font = directory / f'NotoSansCJKjp-{style}.otf'
        actual = subprocess.check_output(
            ['fc-scan', '--format=%{family}|%{style}|%{fontversion}', str(font)],
            text=True, timeout=30)
        if actual != f'{FAMILY}|{style}|131334':
            raise ValueError(f'Japanese font family/style/version mismatch: {style}')
    subprocess.run(['fc-cache', '-f', str(directory)], check=True, timeout=60)
    for style in ('Regular', 'Bold'):
        actual = subprocess.check_output(
            ['fc-match', '--format=%{file}\n%{family}|%{style}|%{fontversion}', f'{FAMILY}:lang=ja:style={style}'],
            text=True, timeout=30).splitlines()
        expected_file = (directory / f'NotoSansCJKjp-{style}.otf').resolve()
        if (len(actual) != 2 or Path(actual[0]).resolve() != expected_file
                or actual[1] != f'{FAMILY}|{style}|131334'):
            raise ValueError(f'Pinned Japanese font not selected by renderer: {style}')
    print('Japanese font family, Regular/Bold styles and version verified', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('--cache-dir', required=True, type=Path)
    parser.add_argument('--font-dir', required=True, type=Path)
    args = parser.parse_args()
    install(args.cache_dir, args.font_dir, manifest())


if __name__ == '__main__':
    main()
