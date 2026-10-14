#!/usr/bin/env python3
"""Require the exact synthetic pairing UI inventory; no native acceptance claim."""
import importlib.util
import json
import os
from pathlib import Path
import sys

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
EXPECTED = tuple(prefix + '-' + case for prefix in ('ja-320', 'en-320', 'ja-375', 'en-375', 'ja-1280', 'en-1280')
                 for case in ('request-keyboard-layout', 'stable-incoming-card-no-focus-steal'))
LIMIT = 8 * 1024 * 1024


def unique(pairs):
    out = {}
    for key, value in pairs:
        if key in out:
            raise ValueError('duplicate report key')
        out[key] = value
    return out


def validate(raw, returncode):
    if type(raw) is not bytes or len(raw) > LIMIT or type(returncode) is not int or returncode != 0:
        raise ValueError('pairing UI process did not pass')
    def invalid(_):
        raise ValueError('invalid numeric report value')
    report = json.loads(raw, object_pairs_hook=unique, parse_constant=invalid)
    if not isinstance(report, dict) or report.get('errors') != []:
        raise ValueError('pairing UI report errors')
    specs = []
    def walk(suites):
        if type(suites) is not list:
            raise ValueError('invalid suite inventory')
        for suite in suites:
            if type(suite) is not dict or type(suite.get('specs', [])) is not list:
                raise ValueError('invalid suite')
            specs.extend(suite.get('specs', []))
            walk(suite.get('suites', []))
    walk(report.get('suites'))
    titles = []
    for spec in specs:
        if (type(spec) is not dict or spec.get('file') != 'pairing-v2.acceptance.mjs'
                or spec.get('ok') is not True or type(spec.get('tests')) is not list or len(spec['tests']) != 1):
            raise ValueError('invalid pairing UI case')
        titles.append(spec.get('title'))
        test = spec['tests'][0]
        if (type(test) is not dict or test.get('expectedStatus') != 'passed' or test.get('status') != 'expected'
                or type(test.get('results')) is not list or len(test['results']) != 1):
            raise ValueError('skipped, flaky or duplicate UI execution')
        result = test['results'][0]
        if (type(result) is not dict or result.get('status') != 'passed' or type(result.get('retry')) is not int or result['retry'] != 0
                or result.get('errors') != [] or result.get('error') is not None):
            raise ValueError('pairing UI case did not pass exactly once')
    if len(titles) != len(EXPECTED) or set(titles) != set(EXPECTED):
        raise ValueError('pairing UI inventory differs')
    stats = report.get('stats', {})
    if type(stats) is not dict or any(type(stats.get(k)) is not int or stats[k] != v for k, v in
           (('expected', 12), ('unexpected', 0), ('flaky', 0), ('skipped', 0))):
        raise ValueError('pairing UI totals differ')
    return True


def main(argv=None):
    if (sys.argv[1:] if argv is None else argv):
        print('FAIL: fixed pairing UI invocation required.')
        return 1
    try:
        path = Path(__file__).with_name('ci-nearby-native.py')
        spec = importlib.util.spec_from_file_location('pairing_ui_capture', path)
        native = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(native)
        env = dict(os.environ)
        env.pop('SOBA_CHROMIUM_PATH', None)
        env.pop('PLAYWRIGHT_JSON_OUTPUT_NAME', None)
        env.pop('PLAYWRIGHT_JSON_OUTPUT_DIR', None)
        state, code, stdout, _ = native.capture([
            'node', 'node_modules/@playwright/test/cli.js', 'test',
            '--config=playwright.pairing-ui.config.mjs', '--forbid-only', '--retries=0',
            '--workers=1', '--reporter=json'], cwd=ROOT / 'web', env=env, timeout=240, limit=LIMIT)
        if state != 'completed':
            raise ValueError('pairing UI capture incomplete')
        validate(stdout, code)
    except Exception:
        print('FAIL: exact synthetic pairing UI checks did not pass; private output withheld.')
        return 1
    print('PASS: 12/12 exact synthetic pairing UI checks; no retries or skips. Native acceptance is separate.')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
