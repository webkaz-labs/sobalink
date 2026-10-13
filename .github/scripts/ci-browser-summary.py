#!/usr/bin/env python3
"""Reconstruct bounded fixed browser evidence; never export unchecked bytes."""
import importlib.util
import json
import os
from pathlib import Path
import stat
import sys

sys.dont_write_bytecode = True
SOURCE = Path(__file__).with_name('ci-nearby-diagnostic.py')
SPEC = importlib.util.spec_from_file_location('nearby_fixed_evidence', SOURCE)
nearby = importlib.util.module_from_spec(SPEC)
exec(compile(SOURCE.read_bytes(), str(SOURCE), 'exec'), nearby.__dict__)

TEXT_EXPECTED = {
    'schema': 1, 'accepted': True, 'observed': 1, 'passed': 1, 'errors': 0,
    'selectedRecipients': 1, 'explicitQueueMode': True, 'exactReview': True,
    'explicitConsent': True, 'prepareCalls': 1, 'commitCalls': 1,
    'commitResponseLost': True, 'originalStatusRecovered': True,
    'receiverSavedRows': 1, 'untouchedPeerRows': 0, 'receiptMatched': True,
    'locales': ['en', 'ja'], 'widths': [375, 320],
    'browserCommandExited': True, 'coreChildrenJoined': True,
}


def text_summary(value):
    if type(value) is not dict or set(value) != set(TEXT_EXPECTED):
        raise ValueError('invalid fixed text summary')
    for key, expected in TEXT_EXPECTED.items():
        actual = value[key]
        if type(actual) is not type(expected) or actual != expected:
            raise ValueError('invalid fixed text evidence')
        if type(expected) is list and any(type(a) is not type(b) for a, b in zip(actual, expected)):
            raise ValueError('invalid fixed text evidence list')
    return dict(TEXT_EXPECTED)


def read_fixed(path, limit):
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or before.st_size > limit:
        raise ValueError('invalid fixed evidence file')
    fd = os.open(path, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0))
    with os.fdopen(fd, 'rb') as stream:
        current = os.fstat(stream.fileno())
        if (current.st_dev, current.st_ino) != (before.st_dev, before.st_ino):
            raise ValueError('fixed evidence file changed')
        raw = stream.read(limit + 1)
    if len(raw) > limit:
        raise ValueError('fixed evidence file exceeded bound')
    return nearby.parse_json(raw.decode('utf-8'))


def write_fixed(path, value):
    raw = (json.dumps(value, sort_keys=True, separators=(',', ':')) + '\n').encode('utf-8')
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(raw)


def reconstruct(case, status, directory, destination=None):
    if case not in ('setup', 'text') or type(status) is not int or status < 0 or status > 255:
        raise ValueError('invalid fixed browser summary request')
    if directory.is_symlink() or not directory.is_dir():
        raise ValueError('fixed evidence directory unavailable')
    destination = directory if destination is None else destination
    if destination.is_symlink() or not destination.is_dir():
        raise ValueError('fixed evidence destination unavailable')
    success = directory / (case + '-native-summary.json')
    failure = directory / 'setup-native-failure.json' if case == 'setup' else None
    if destination != directory:
        for name in (case + '-native-summary.json', 'setup-native-failure.json' if case == 'setup' else None):
            if name is not None and os.path.lexists(destination / name):
                (destination / name).unlink()
    value, failed = None, None
    accepted = False
    try:
        if failure is not None and os.path.lexists(failure):
            failed = nearby.browser_failure(read_fixed(failure, nearby.BROWSER_FAILURE_MAX_BYTES))
        if status == 0 and failed is None:
            value = read_fixed(success, 8192)
            value = nearby.browser_summary(value) if case == 'setup' else text_summary(value)
            accepted = True
    except (OSError, UnicodeError, ValueError, TypeError):
        accepted = False
    finally:
        # Original inputs remain outside the artifact directory. Remove them
        # before exporting only the reconstructed closed record.
        for path in (success, failure):
            if path is not None and os.path.lexists(path):
                path.unlink()
    if accepted:
        write_fixed(destination / success.name, value)
    elif failed is not None:
        write_fixed(destination / failure.name, failed)
    return accepted


def main():
    try:
        if len(sys.argv) != 3:
            raise ValueError('invalid fixed summary arguments')
        case, status = sys.argv[1], int(sys.argv[2])
        root = Path(os.environ['RUNNER_TEMP'])
        directory = root / ('soba-' + case + '-browser-evidence')
        accepted = reconstruct(case, status, directory, root / 'soba-playwright-screenshots')
    except (OSError, ValueError, KeyError, TypeError):
        accepted = False
    print('Fixed browser summary accepted' if accepted else 'Fixed browser evidence incomplete or failed')
    return 0 if accepted else 1


if __name__ == '__main__':
    raise SystemExit(main())
