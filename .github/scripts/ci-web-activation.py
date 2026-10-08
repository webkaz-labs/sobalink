#!/usr/bin/env python3
"""Fixed Linux synthetic-owner acceptance; never export private diagnostics."""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
TAGS = 'ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,managed_restart_native,web_activation_native'
SCOPE = 'Linux real helper/HTTP/browser with synthetic owners; Core and OS-open excluded'
BOOLS = ('accepted', 'allSevenPassed', 'runnerExitedSuccessfully', 'timedOut', 'outputOverflow', 'nativeCleanupProven')
KEYS = {'schema', 'expected', 'scope', *BOOLS}
VERSIONS = {'go': 'go1.27.1', 'node': 'v24.19.0', 'npm': '11.9.0'}


def validate_toolchain(versions, binaries):
    if not isinstance(versions, dict) or versions != VERSIONS or any(type(v) is not str for v in versions.values()):
        raise ValueError('toolchain versions mismatch')
    if not isinstance(binaries, dict) or set(binaries) != {'go', 'node'} or any(type(v) is not str or not re.fullmatch('[0-9a-f]{64}', v) for v in binaries.values()):
        raise ValueError('invalid toolchain digest')
    return {'versions': dict(versions), 'binarySha256': dict(binaries)}


def validate_report(value):
    if not isinstance(value, dict) or set(value) != KEYS:
        raise ValueError('invalid result schema')
    if type(value['schema']) is not int or value['schema'] != 1 or type(value['expected']) is not int or value['expected'] != 7 or value['scope'] != SCOPE:
        raise ValueError('invalid fixed result')
    if any(type(value[key]) is not bool for key in BOOLS):
        raise ValueError('invalid result types')
    accepted = (value['allSevenPassed'] and value['runnerExitedSuccessfully'] and not value['timedOut'] and not value['outputOverflow'] and value['nativeCleanupProven'])
    if value['accepted'] != accepted:
        raise ValueError('inconsistent acceptance')
    return {key: value[key] for key in sorted(KEYS)}


def failed_report():
    return {'schema': 1, 'expected': 7, 'scope': SCOPE, **dict.fromkeys(BOOLS, False)}


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def asset_hashes(directory):
    paths = sorted(directory.rglob('*'))
    if not (directory / 'index.html').is_file() or any(p.is_symlink() for p in paths):
        raise ValueError('invalid synthetic assets')
    return {p.relative_to(directory).as_posix(): digest(p) for p in paths if p.is_file()}


def child_environment(private, binary, assets, chromium, report):
    # Only explicit non-secret process configuration reaches the browser wrapper.
    return {'PATH': os.environ.get('PATH', ''), 'HOME': str(private), 'TMPDIR': str(private), 'LANG': 'C.UTF-8',
            'SOBA_WEB_ACTIVATION_ACCEPTANCE': '1', 'SOBA_WEB_ACTIVATION_BINARY': str(binary),
            'SOBA_WEB_ACTIVATION_ASSETS': str(assets), 'SOBA_ACTIVATION_CHROMIUM': str(chromium),
            'SOBA_ACTIVATION_REPORT': str(report)}


def run_command(command, log, cwd=ROOT, env=None, timeout=600):
    with log.open('ab') as output:
        subprocess.run(command, cwd=cwd, env=env, stdout=output, stderr=subprocess.STDOUT, check=True, shell=False, timeout=timeout)


def main(argv=None):
    if (sys.argv[1:] if argv is None else argv) or sys.platform != 'linux' or os.environ.get('GITHUB_ACTIONS') != 'true':
        print('FAIL: fixed Linux CI entry required.')
        return 1
    private = None
    accepted = False
    export_ready = False
    report = failed_report()
    provenance = {'schema': 1, 'scope': 'synthetic-owner-web-acceptance', 'stage': 'preflight'}
    export = Path(os.environ['RUNNER_TEMP']) / 'soba-web-activation-sanitized'
    try:
        os.umask(0o077)
        export.mkdir(mode=0o700, exist_ok=False)
        export_ready = True
        private = Path(tempfile.mkdtemp(prefix='swa-', dir=os.environ['RUNNER_TEMP']))
        private.chmod(0o700)
        log = private / 'private-build-and-run.log'
        def read_command(command, cwd=ROOT):
            with log.open('ab') as errors:
                return subprocess.check_output(command, cwd=cwd, stderr=errors, text=True, timeout=10).strip()
        commit = read_command(['git', 'rev-parse', 'HEAD'])
        tree = read_command(['git', 'rev-parse', 'HEAD^{tree}'])
        if not re.fullmatch('[0-9a-f]{40}', commit) or not re.fullmatch('[0-9a-f]{40}', tree):
            raise ValueError('invalid source identity')
        if read_command(['git', 'status', '--porcelain', '--untracked-files=no']):
            raise ValueError('modified source')
        versions = {'go': read_command(['go', 'env', 'GOVERSION']), 'node': read_command(['node', '--version']), 'npm': read_command(['npm', '--version'])}
        binaries = {name: digest(Path(shutil.which(name))) for name in ('go', 'node')}
        provenance['toolchain'] = validate_toolchain(versions, binaries)
        web = ROOT / 'web'
        # Resolve only the executable supplied by the installed, lockfile-pinned
        # Playwright package. No arbitrary browser input or launch overrides.
        chromium = Path(read_command(['node', '--input-type=module', '-e', "import {chromium} from '@playwright/test'; console.log(chromium.executablePath())"], web))
        if not chromium.is_absolute() or not chromium.is_file():
            raise ValueError('missing pinned Chromium')
        browsers = json.loads((web / 'node_modules/playwright-core/browsers.json').read_text())
        revision = next(b['revision'] for b in browsers['browsers'] if b['name'] == 'chromium')
        if not re.fullmatch('[0-9]+', revision):
            raise ValueError('invalid browser revision')
        tracked = read_command(['git', 'ls-files']).splitlines()
        before = {name: digest(ROOT / name) for name in tracked}
        provenance.update(commit=commit, tree=tree, sourceSha256=before, lockSha256=digest(web / 'package-lock.json'), chromiumRevision=revision, chromiumSha256=digest(chromium), stage='compile')
        binary = private / 'soba-web-activation.test'
        env = dict(os.environ, GOTOOLCHAIN='local', GOWORK='off', GOPROXY='off', GOSUMDB='off', GOTELEMETRY='off', GOFLAGS='-mod=readonly -buildvcs=false')
        run_command(['go', 'test', '-race', '-tags=' + TAGS, '-c', '-o', str(binary), './cmd/soba'], log, env=env)
        provenance['fixtureSha256'] = digest(binary)
        provenance['stage'] = 'assets'
        assets = private / 'synthetic-assets'
        run_command(['node', 'node_modules/vite/bin/vite.js', 'build', '--config', 'playwright.activation.vite.config.mjs', '--outDir', str(assets)], log, cwd=web, timeout=120)
        provenance['assetSha256'] = asset_hashes(assets)
        wrapper_report = private / 'wrapper-result.json'
        provenance['stage'] = 'runtime'
        # The reviewed wrapper owns its complete 23-minute failure horizon.
        # The 40-minute CI step also allows bounded offline preparation; a job
        # timeout is containment, never a passing cleanup proof.
        result = subprocess.run(['node', 'browser/activation-runner.mjs'], cwd=web,
                                env=child_environment(private, binary, assets, chromium, wrapper_report),
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, shell=False)
        if wrapper_report.is_symlink() or wrapper_report.stat().st_size > 4096:
            raise ValueError('invalid result file')
        report = validate_report(json.loads(wrapper_report.read_text()))
        if result.returncode != 0 and report['accepted']:
            raise ValueError('runner result mismatch')
        if before != {name: digest(ROOT / name) for name in tracked} or digest(binary) != provenance['fixtureSha256'] or asset_hashes(assets) != provenance['assetSha256']:
            raise ValueError('source or artifact changed')
        accepted = result.returncode == 0 and report['accepted']
        provenance['stage'] = 'passed' if accepted else 'runtime-failed'
        if accepted:
            shutil.rmtree(private)
            private = None
    except Exception:
        # Never serialize exceptions, child output, private paths or environment.
        report = failed_report()
        accepted = False
    try:
        if not export_ready:
            raise ValueError('export directory was not created')
        # Reconstruct exports, never copy raw framework artifacts. The workflow
        # uploads only these two literal paths, after this write completes.
        (export / 'result.json').write_text(json.dumps(validate_report(report), sort_keys=True) + '\n')
        (export / 'provenance.json').write_text(json.dumps(provenance, sort_keys=True) + '\n')
        output = os.environ.get('GITHUB_OUTPUT')
        if output:
            with open(output, 'a', encoding='utf-8') as result_output:
                result_output.write('sanitized_ready=true\n')
    except Exception:
        accepted = False
    print('PASS: seven synthetic-owner cases and cleanup verified.' if accepted else 'FAIL: Web acceptance not established; private diagnostics withheld.')
    # A retained private directory is intentionally not deleted after failure;
    # ephemeral runner process-scope teardown must contain uncertain descendants.
    return 0 if accepted else 1


if __name__ == '__main__':
    raise SystemExit(main())
