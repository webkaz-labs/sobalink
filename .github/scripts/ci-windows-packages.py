#!/usr/bin/env python3
"""Run the entire Windows race inventory in two disjoint, serial package groups.

Only package scheduling differs from the ordinary whole-inventory invocation.
Core is never divided into test selections; all tests/examples/fuzz seeds retain
Go's normal execution rules, race detector, count and package timeout.
"""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
CORE = 'github.com/webkaz-labs/sobalink/internal/core'
MODULE = 'github.com/webkaz-labs/sobalink'
GOFLAGS = '-mod=readonly -tags=ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy'
BASE = ('go', 'test', '-race', '-count=1', '-timeout=10m')
GROUPS = ('core', 'remaining')
POLICY_FILES = ('.github/workflows/ci.yml', '.github/scripts/ci-windows-packages.py',
                '.github/scripts/ci-metrics.py', '.github/scripts/ci-nearby-diagnostic.py', 'go.mod', 'go.sum')
PACKAGE = re.compile(re.escape(MODULE) + r'(?:/[A-Za-z0-9_][A-Za-z0-9_.-]*)*\Z')


def module(name):
    path = Path(__file__).with_name(name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


metrics = module('ci-metrics')


def query(command):
    result = subprocess.run(command, cwd=ROOT, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL, check=False, shell=False, timeout=90)
    if result.returncode or len(result.stdout) > 8 * 1024 * 1024:
        raise ValueError('source or package query failed')
    return result.stdout.decode('utf-8')


def fingerprint(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def packages(raw):
    if not isinstance(raw, str) or not raw.endswith('\n'):
        raise ValueError('incomplete package inventory')
    names = raw.splitlines()
    if (not names or len(names) > 4096 or len(set(names)) != len(names)
            or any(not PACKAGE.fullmatch(name) for name in names) or names.count(CORE) != 1):
        raise ValueError('invalid package inventory')
    return names


def partition(inventory):
    groups = {'core': [CORE], 'remaining': [name for name in inventory if name != CORE]}
    validate_partition(inventory, groups)
    return groups


def validate_partition(inventory, groups):
    if (type(inventory) is not list or type(groups) is not dict
            or packages('\n'.join(inventory) + '\n') != inventory or set(groups) != set(GROUPS)
            or groups['core'] != [CORE] or not groups['remaining']
            or groups['remaining'] != [name for name in inventory if name != CORE]):
        raise ValueError('package groups must be an exact ordered partition')
    combined = groups['core'] + groups['remaining']
    if len(combined) != len(inventory) or len(set(combined)) != len(combined) or set(combined) != set(inventory):
        raise ValueError('package inventory coverage differs')


def native_identity():
    if (Path.cwd().resolve() != ROOT or sys.platform != 'win32' or os.environ.get('GITHUB_ACTIONS') != 'true'
            or os.environ.get('RUNNER_OS') != 'Windows' or os.environ.get('RUNNER_ARCH') != 'X64'
            or os.environ.get('GOTOOLCHAIN') != 'local' or os.environ.get('GOWORK') != 'off'
            or os.environ.get('GOFLAGS') != GOFLAGS):
        raise ValueError('ordinary native Windows environment required')
    actual = json.loads(query(['go', 'env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH',
                              'GOOS', 'GOARCH', 'CGO_ENABLED']))
    expected = {'GOVERSION': 'go1.27.1', 'GOHOSTOS': 'windows', 'GOHOSTARCH': 'amd64',
                'GOOS': 'windows', 'GOARCH': 'amd64', 'CGO_ENABLED': '1'}
    if actual != expected:
        raise ValueError('native Windows toolchain differs')


def verify_untracked(raw, nearby):
    # Earlier ordinary policy tests create CPython caches. They cannot supply
    # Go source and this runner loads local helpers from source bytes explicitly.
    # Keep all other untracked/ignored source subject to the existing allowlist.
    if raw and not raw.endswith('\0'):
        raise ValueError('incomplete untracked inventory')
    names = raw.split('\0')
    cache = re.compile(r'\.github/scripts/__pycache__/[A-Za-z0-9_-]+\.cpython-[0-9]{2,3}(?:\.opt-[12])?\.pyc\Z')
    retained = [name for name in names if name and not cache.fullmatch(name)]
    nearby.validate_untracked(('\0'.join(retained) + ('\0' if retained else '')).encode(), after_build=True)


def source_identity():
    commit, tree = (query(['git', 'rev-parse', value]).strip() for value in ('HEAD', 'HEAD^{tree}'))
    if (not re.fullmatch('[0-9a-f]{40}', commit) or not re.fullmatch('[0-9a-f]{40}', tree)
            or commit != os.environ.get('GITHUB_SHA')
            or query(['git', 'status', '--porcelain', '--untracked-files=no']).strip()):
        raise ValueError('source identity differs')
    nearby = module('ci-nearby-diagnostic')
    verify_untracked(query(['git', 'ls-files', '--others', '-z']), nearby)
    if any((ROOT / prefix.rstrip('/')).is_symlink() for prefix in nearby.DEPENDENCY_PREFIXES):
        raise ValueError('unprepared dependency source')
    return {'commit': commit, 'tree': tree,
            'policySha256': {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in POLICY_FILES}}


def run_group(group, selected):
    before = metrics.read_records()
    # Reuse ordinary CI process execution and metrics; do not change test/child
    # ownership, output streaming, worker parallelism, or Go's timeout handling.
    code = metrics.run_suite('local-race-windows-' + group, list(BASE) + selected)
    after = metrics.read_records()
    if (len(after) != len(before) + 1 or after[:-1] != before
            or after[-1]['suite'] != 'local-race-windows-' + group
            or code != (after[-1]['returncode'] if after[-1]['returncode'] >= 0 else 128 - after[-1]['returncode'])):
        raise ValueError('complete phase timing receipt required')
    return {'packages': selected, 'returncode': code, 'metric': after[-1]}


def validate_receipt(value, source, inventory):
    if (not isinstance(value, dict) or set(value) != {'schema', 'source', 'inventory', 'inventorySha256',
            'groups', 'phases', 'sourceVerified', 'inventoryVerified', 'complete'}
            or type(value['schema']) is not int or value['schema'] != 1
            or value['source'] != source or value['inventory'] != inventory
            or value['inventorySha256'] != fingerprint(inventory)):
        raise ValueError('source-bound package receipt differs')
    validate_partition(inventory, value['groups'])
    if (value['sourceVerified'] is not True or value['inventoryVerified'] is not True or value['complete'] is not True
            or list(value['phases']) != list(GROUPS)):
        raise ValueError('both complete package phases are required')
    for group in GROUPS:
        phase = value['phases'][group]
        if (set(phase) != {'packages', 'returncode', 'metric'} or phase['packages'] != value['groups'][group]
                or type(phase['returncode']) is not int or phase['returncode'] != 0):
            raise ValueError('package phase did not pass')
        metric = metrics.validate_record(phase['metric'])
        if metric['suite'] != 'local-race-windows-' + group or metric['status'] != 'passed' or metric['returncode'] != 0:
            raise ValueError('package phase timing did not pass')
    return True


def main(argv=None):
    if (sys.argv[1:] if argv is None else argv):
        print('error: Fixed Windows package-race entry required.', file=sys.stderr)
        return 1
    receipt = {'schema': 1, 'source': None, 'inventory': [], 'inventorySha256': None,
               'groups': {}, 'phases': {}, 'sourceVerified': False, 'inventoryVerified': False, 'complete': False}
    source, inventory, destination = None, None, None
    try:
        native_identity()
        destination = Path(os.environ['RUNNER_TEMP']) / 'windows-package-race.json'
        if destination.exists():
            destination = None
            raise ValueError('existing package receipt cannot be reused')
        source = receipt['source'] = source_identity()
        inventory = receipt['inventory'] = packages(query(['go', 'list', '-race', './...']))
        receipt['inventorySha256'] = fingerprint(inventory)
        receipt['groups'] = partition(inventory)
        # Preserve ordinary go-test coverage on a nonzero group: the other
        # whole group still runs, and neither result can replace the first.
        for group in GROUPS:
            receipt['phases'][group] = run_group(group, receipt['groups'][group])
        receipt['sourceVerified'] = source_identity() == source
        receipt['inventoryVerified'] = packages(query(['go', 'list', '-race', './...'])) == inventory
        receipt['complete'] = receipt['sourceVerified'] and receipt['inventoryVerified'] and all(
            receipt['phases'][group]['returncode'] == 0 for group in GROUPS)
        validate_receipt(receipt, source, inventory)
    except (Exception, KeyboardInterrupt):
        receipt['complete'] = False
    try:
        if destination is None:
            raise ValueError('no fresh package receipt destination')
        with destination.open('x', encoding='utf-8') as output:
            json.dump(receipt, output, indent=2, allow_nan=False)
            output.write('\n')
        # A missing, partial or substituted receipt can never produce success.
        validate_receipt(json.loads(destination.read_text(encoding='utf-8')), source, inventory)
    except (OSError, ValueError, TypeError, KeyError):
        receipt['complete'] = False
    print('Windows whole-package race coverage passed.' if receipt['complete'] else
          'error: Windows whole-package race coverage is incomplete.')
    return 0 if receipt['complete'] else 1


if __name__ == '__main__':
    sys.exit(main())
