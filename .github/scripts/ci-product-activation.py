#!/usr/bin/env python3
"""Fixed two-case Linux product-entry acceptance; sanitized exports only."""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
TAGS = 'ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,managed_restart_native,web_activation_native,product_activation_native'
SCOPE = 'Linux production Web and CLI with real Core owners and synthetic loopback peers; OS-open stubbed; other behavior not established'
BOOLS = ('accepted', 'allSelectedPassed', 'runnerExitedSuccessfully', 'timedOut', 'outputOverflow', 'nativeCleanupProven')
KEYS = {'schema', 'expected', 'scope', 'diagnostics', *BOOLS}
VERSIONS = {'go': 'go1.27.1', 'node': 'v24.19.0', 'npm': '11.9.0'}
CASE_IDS = ('product-web', 'product-cli')
STATUSES = ('not-started', 'passed', 'failed', 'timedOut', 'skipped', 'interrupted')
WORK_PHASES = ('not-started', 'preflight', 'native-ready', 'old-login', 'full-panel', 'review', 'popup', 'helper', 'restart-confirm', 'management-ready', 'old-exit', 'successor-start', 'open-before', 'open-request', 'open-observed', 'open-disabled', 'fresh-login', 'cli-start', 'cli-pty-complete', 'status', 'controller-proof', 'complete')
FAILURE_PHASES = ('none', 'unknown', *WORK_PHASES)
CLEANUP_PHASES = ('not-started', 'context', 'supervisor', 'proof', 'safety', 'remove', 'complete')
CLEANUP_FAILURE_PHASES = ('none', 'unknown', *CLEANUP_PHASES)
EXITS = ('not-observed', 'zero', 'one', 'race', 'other', 'signal', 'spawn-error', 'profile-rejected', 'watchdog', 'mode-rejected', 'owner-failed', 'supervisor-failed', 'registration-failed')
SUPERVISOR_FAILURES = ('none', 'origin-write', 'observer-wait', 'start-observe', 'progress-write', 'wait-error', 'child-exit', 'extra-observe', 'extra-close')
RESOURCE_FAILURES = ('none', 'slave-close', 'master-close', 'capture-error', 'helper-exit', 'result-parse', 'result-write')
NATIVE_STAGES = ('not-started', 'owner-start', 'peer-open', 'peer-review', 'peer-apply', 'foreground', 'identity', 'management-announced', 'cli-launch', 'review-read', 'owner-status', 'peer-status', 'review-binding', 'network-ready', 'persisted-context', 'pair-binding', 'proof-written', 'cli-terminal', 'cli-review', 'cli-review-binding', 'cli-apply', 'cli-complete')
NATIVE_STATUSES = ('unobserved', 'idle', 'restart-required', 'preparing', 'exchanging', 'local-confirmed', 'connected', 'network-started', 'failed', 'cancelled', 'other')
NATIVE_ROLES = ('old', 'successor', 'cli')
LIFECYCLE_BOOLS = ('workCompleted', 'workFailed', 'cleanupFailed', 'oldReadyObserved', 'oldExitObserved', 'successorStartObserved', 'cliCompleteObserved', 'controllerProofObserved', 'peerConfirmed', 'ownerConfirmed', 'ordinaryReady', 'originalReviewPreserved', 'supervisorStarted', 'supervisorExited', 'proofRead', 'stopRequested', 'supervisorDeadlineExpired', 'noWaitableChildren', 'allDescendantsReaped', 'registeredNativeExits', 'successorRegistered', 'contextClosed', 'profileRemoved', 'outputOverflow', 'privateOutputDetected')
LIFECYCLE_COUNTS = ('registeredChildren', 'observedExits', 'reaped', 'blockedRequests', 'runtimeErrors')
LIFECYCLE_ENUMS = {'workPhase': WORK_PHASES, 'firstFailurePhase': FAILURE_PHASES, 'cleanupPhase': CLEANUP_PHASES,
                   'firstCleanupFailurePhase': CLEANUP_FAILURE_PHASES, 'supervisorExit': EXITS, 'oldExit': EXITS,
                   'supervisorFailure': SUPERVISOR_FAILURES, 'resourceFailure': RESOURCE_FAILURES,
                   'helperExit': EXITS, 'successorExit': EXITS}
LIFECYCLE_KEYS = {*LIFECYCLE_ENUMS, *LIFECYCLE_BOOLS, *LIFECYCLE_COUNTS, 'native'}
NATIVE_KEYS = {'available', 'invalid', 'stage', 'failed', 'ownerStatus', 'peerStatus'}
CASE_KEYS = {'id', 'started', 'status', 'diagnosticAvailable', 'diagnosticRejected', 'lifecycle'}
DIAGNOSTIC_KEYS = {'summaryAvailable', 'selectionValid', 'unexpected', 'globalErrors', 'observed', 'passed', 'cases'}


def empty_lifecycle():
    return {'workPhase': 'not-started', 'firstFailurePhase': 'none', 'cleanupPhase': 'not-started',
            'firstCleanupFailurePhase': 'none', 'supervisorExit': 'not-observed', 'oldExit': 'not-observed',
            'supervisorFailure': 'none', 'resourceFailure': 'none', 'helperExit': 'not-observed', 'successorExit': 'not-observed',
            **dict.fromkeys(LIFECYCLE_BOOLS, False), **dict.fromkeys(LIFECYCLE_COUNTS, 0),
            'native': {role: {'available': False, 'invalid': False, 'stage': 'not-started', 'failed': False,
                              'ownerStatus': 'unobserved', 'peerStatus': 'unobserved'} for role in NATIVE_ROLES}}


def empty_diagnostics():
    return {'summaryAvailable': False, 'selectionValid': False, 'unexpected': False, 'globalErrors': 0,
            'observed': 0, 'passed': 0, 'cases': [
                {'id': name, 'started': False, 'status': 'not-started', 'diagnosticAvailable': False,
                 'diagnosticRejected': False, 'lifecycle': empty_lifecycle()} for name in CASE_IDS]}


def validate_lifecycle(value):
    if type(value) is not dict or set(value) != LIFECYCLE_KEYS:
        raise ValueError('invalid fixed lifecycle schema')
    if any(type(value[key]) is not str or value[key] not in choices for key, choices in LIFECYCLE_ENUMS.items()):
        raise ValueError('invalid fixed lifecycle phase')
    if any(type(value[key]) is not bool for key in LIFECYCLE_BOOLS) or any(type(value[key]) is not int or not 0 <= value[key] <= 255 for key in LIFECYCLE_COUNTS):
        raise ValueError('invalid fixed lifecycle value')
    native = value['native']
    if type(native) is not dict or set(native) != set(NATIVE_ROLES):
        raise ValueError('invalid fixed native inventory')
    observations = {}
    for role in NATIVE_ROLES:
        row = native[role]
        if type(row) is not dict or set(row) != NATIVE_KEYS:
            raise ValueError('invalid fixed native schema')
        if any(type(row[key]) is not bool for key in ('available', 'invalid', 'failed')) or type(row['stage']) is not str or row['stage'] not in NATIVE_STAGES or any(type(row[key]) is not str or row[key] not in NATIVE_STATUSES for key in ('ownerStatus', 'peerStatus')):
            raise ValueError('invalid fixed native value')
        if row['available'] and row['invalid'] or not row['available'] and (row['stage'] != 'not-started' or row['failed'] or row['ownerStatus'] != 'unobserved' or row['peerStatus'] != 'unobserved'):
            raise ValueError('inconsistent fixed native observation')
        observations[role] = {key: row[key] for key in sorted(NATIVE_KEYS)}
    return {**{key: value[key] for key in sorted(LIFECYCLE_KEYS - {'native'})}, 'native': observations}


def validate_diagnostics(value):
    if type(value) is not dict or set(value) != DIAGNOSTIC_KEYS or any(type(value[key]) is not bool for key in ('summaryAvailable', 'selectionValid', 'unexpected')):
        raise ValueError('invalid fixed diagnostic schema')
    if any(type(value[key]) is not int or not 0 <= value[key] <= limit for key, limit in (('globalErrors', 255), ('observed', 2), ('passed', 2))) or value['passed'] > value['observed']:
        raise ValueError('invalid fixed diagnostic count')
    if type(value['cases']) is not list or len(value['cases']) != 2:
        raise ValueError('invalid fixed case inventory')
    cases = []
    for name, row in zip(CASE_IDS, value['cases']):
        if type(row) is not dict or set(row) != CASE_KEYS or type(row['id']) is not str or row['id'] != name or type(row['status']) is not str or row['status'] not in STATUSES or any(type(row[key]) is not bool for key in ('started', 'diagnosticAvailable', 'diagnosticRejected')):
            raise ValueError('invalid fixed case schema')
        if not row['started'] and row['status'] == 'passed' or row['diagnosticAvailable'] and row['diagnosticRejected']:
            raise ValueError('inconsistent fixed case evidence')
        cases.append({'id': name, 'started': row['started'], 'status': row['status'],
                      'diagnosticAvailable': row['diagnosticAvailable'], 'diagnosticRejected': row['diagnosticRejected'],
                      'lifecycle': validate_lifecycle(row['lifecycle'])})
    if value['observed'] != sum(row['status'] != 'not-started' for row in cases) or value['passed'] != sum(row['status'] == 'passed' for row in cases):
        raise ValueError('inconsistent fixed diagnostic counts')
    return {**{key: value[key] for key in sorted(DIAGNOSTIC_KEYS - {'cases'})}, 'cases': cases}


def strict_json(text):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('duplicate report field')
            result[key] = value
        return result
    def nonfinite(value):
        raise ValueError('nonfinite report value')
    return json.loads(text, object_pairs_hook=unique, parse_constant=nonfinite)


def validate_toolchain(versions, binaries):
    if not isinstance(versions, dict) or versions != VERSIONS or any(type(v) is not str for v in versions.values()):
        raise ValueError('toolchain versions mismatch')
    if not isinstance(binaries, dict) or set(binaries) != {'go', 'node'} or any(type(v) is not str or not re.fullmatch('[0-9a-f]{64}', v) for v in binaries.values()):
        raise ValueError('invalid toolchain digest')
    return {'versions': dict(versions), 'binarySha256': dict(binaries)}


def validate_report(value):
    if not isinstance(value, dict) or set(value) != KEYS:
        raise ValueError('invalid result schema')
    if type(value['schema']) is not int or value['schema'] != 3 or type(value['expected']) is not int or value['expected'] != 2 or value['scope'] != SCOPE:
        raise ValueError('invalid fixed result')
    if any(type(value[key]) is not bool for key in BOOLS):
        raise ValueError('invalid result types')
    diagnostics = validate_diagnostics(value['diagnostics'])
    if diagnostics['summaryAvailable'] and value['allSelectedPassed'] and not (
            diagnostics['selectionValid'] and not diagnostics['unexpected'] and diagnostics['globalErrors'] == 0
            and diagnostics['observed'] == 2 and diagnostics['passed'] == 2
            and all(row['started'] and row['status'] == 'passed' for row in diagnostics['cases'])):
        raise ValueError('inconsistent fixed case result evidence')
    # Closed diagnostic observations are evidence only. Do not derive a new
    # acceptance condition from owner/peer statuses or missing annotations.
    accepted = (value['allSelectedPassed'] and value['runnerExitedSuccessfully'] and not value['timedOut'] and not value['outputOverflow'] and value['nativeCleanupProven'])
    if value['accepted'] != accepted:
        raise ValueError('inconsistent acceptance')
    return {key: diagnostics if key == 'diagnostics' else value[key] for key in sorted(KEYS)}


def failed_report(diagnostics=None):
    return {'schema': 3, 'expected': 2, 'scope': SCOPE, 'diagnostics': validate_diagnostics(empty_diagnostics() if diagnostics is None else diagnostics), **dict.fromkeys(BOOLS, False)}


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def asset_hashes(directory):
    paths = sorted(directory.rglob('*'))
    if directory.is_symlink() or not (directory / 'index.html').is_file() or any(p.is_symlink() for p in paths):
        raise ValueError('invalid production assets')
    return {p.relative_to(directory).as_posix(): digest(p) for p in paths if p.is_file()}


def child_environment(private, binary, chromium, report):
    # Only explicit non-secret process configuration reaches the browser wrapper.
    return {'PATH': os.environ.get('PATH', ''), 'HOME': str(private), 'TMPDIR': str(private), 'LANG': 'C.UTF-8',
            'SOBA_PRODUCT_ACTIVATION_ACCEPTANCE': '1', 'SOBA_WEB_ACTIVATION_BINARY': str(binary),
            'SOBA_ACTIVATION_CHROMIUM': str(chromium), 'SOBA_PRODUCT_ACTIVATION_REPORT': str(report)}


def validate_playwright(lock, package, installed):
    names = ('@playwright/test', 'playwright', 'playwright-core')
    if type(lock.get('lockfileVersion')) is not int or lock['lockfileVersion'] != 3:
        raise ValueError('invalid lockfile')
    packages = lock['packages']
    version = packages['node_modules/@playwright/test']['version']
    if type(version) is not str or not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', version):
        raise ValueError('unpinned Playwright')
    for name in names:
        if packages['node_modules/' + name]['version'] != version or installed[name]['version'] != version or installed[name]['name'] != name:
            raise ValueError('installed Playwright mismatch')
    for manifest in (package, packages['']):
        if any(manifest['devDependencies'][name] != version for name in ('@playwright/test', 'playwright-core')):
            raise ValueError('root Playwright mismatch')
    for name, dependency in (('@playwright/test', 'playwright'), ('playwright', 'playwright-core')):
        if installed[name]['dependencies'][dependency] != version or packages['node_modules/' + name]['dependencies'][dependency] != version:
            raise ValueError('Playwright dependency mismatch')
    return version


def validate_chromium(browsers, executable, cache):
    entries = [item for item in browsers['browsers'] if item['name'] == 'chromium']
    if len(entries) != 1:
        raise ValueError('invalid Chromium entry')
    revision = entries[0]['revision']
    if type(revision) is not str or not re.fullmatch('[0-9]+', revision):
        raise ValueError('invalid Chromium revision')
    # This gate has one fixed Linux x64 runner; the pinned package must resolve
    # its normal Chromium binary, not a channel, headless-shell or override.
    expected = cache / ('chromium-' + revision) / 'chrome-linux64' / 'chrome'
    if not executable.is_absolute() or executable != expected or executable.is_symlink() or executable.resolve() != expected or not executable.is_file() or not os.access(executable, os.X_OK):
        raise ValueError('unexpected Chromium executable')
    return revision


class Cancellation:
    """Only the main flow signals an exact, unreaped Popen child.

    Handlers merely record intent, avoiding reentrant waitpid/PID-reuse races.
    There is deliberately no deadline that abandons the owned Node process.
    Native budgets are 360/375/390/405 seconds; failed cleanup stays attached
    beyond the last bound. A hard CI kill cannot establish a passing CI step.
    """
    def __init__(self):
        self.requested = False
        self.previous = {}

    def record(self, signum, frame):
        self.requested = True

    def __enter__(self):
        for signum in (signal.SIGTERM, signal.SIGINT):
            self.previous[signum] = signal.signal(signum, self.record)
        return self

    def __exit__(self, kind, value, traceback):
        for signum, handler in self.previous.items():
            signal.signal(signum, handler)

    def checkpoint(self):
        if self.requested:
            raise ValueError('cancelled')

    def run_node(self, command, cwd, env):
        self.checkpoint()
        child = subprocess.Popen(command, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, shell=False)
        forwarded = False
        while True:
            # wait(timeout) reaps and records returncode before we can signal.
            # The handler cannot send a signal between waitpid and that record.
            if self.requested and not forwarded and child.returncode is None:
                try:
                    child.send_signal(signal.SIGTERM)
                    forwarded = True
                except OSError:
                    # A failed signal does not release ownership. Keep joining
                    # and retry only while this Popen object is still unreaped.
                    pass
            try:
                code = child.wait(timeout=0.25)
                return code
            except subprocess.TimeoutExpired:
                continue
            except BaseException:
                # Unexpected Python cancellation/errors also initiate cleanup;
                # never unwind past the still-owned native descendant scope.
                self.requested = True


def run_command(command, log, cwd=ROOT, env=None, timeout=600):
    with log.open('ab') as output:
        subprocess.run(command, cwd=cwd, env=env, stdout=output, stderr=subprocess.STDOUT, check=True, shell=False, timeout=timeout)


def _main(cancellation, argv=None):
    if (sys.argv[1:] if argv is None else argv) or sys.platform != 'linux' or os.uname().machine != 'x86_64' or os.environ.get('GITHUB_ACTIONS') != 'true':
        print('FAIL: fixed Linux CI entry required.')
        return 1
    private = None
    accepted = False
    export_ready = False
    report = failed_report()
    provenance = {'schema': 1, 'scope': SCOPE, 'stage': 'preflight'}
    export = Path(os.environ['RUNNER_TEMP']) / 'soba-product-activation-sanitized'
    try:
        os.umask(0o077)
        export.mkdir(mode=0o700, exist_ok=False)
        export_ready = True
        private = Path(tempfile.mkdtemp(prefix='spa-ci-', dir=os.environ['RUNNER_TEMP']))
        private.chmod(0o700)
        log = private / 'private-build-and-run.log'
        # Ignore Node/browser launch overrides, debug hooks and inherited proxy
        # authority. Standard HOME is needed only to find installed Chromium.
        preparation_env = {'PATH': os.environ.get('PATH', ''), 'HOME': os.environ['HOME'], 'LANG': 'C.UTF-8',
                           'GOTOOLCHAIN': 'local', 'GOENV': 'off', 'GOWORK': 'off', 'GOTELEMETRY': 'off', 'npm_config_offline': 'true'}
        def read_command(command, cwd=ROOT):
            cancellation.checkpoint()
            with log.open('ab') as errors:
                return subprocess.check_output(command, cwd=cwd, env=preparation_env, stderr=errors, text=True, timeout=10).strip()
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
        lock = json.loads((web / 'package-lock.json').read_text())
        installed = {name: json.loads((web / 'node_modules' / name / 'package.json').read_text())
                     for name in ('@playwright/test', 'playwright', 'playwright-core')}
        playwright_version = validate_playwright(lock, json.loads((web / 'package.json').read_text()), installed)
        installed_paths = {name + '/package.json': web / 'node_modules' / name / 'package.json' for name in installed}
        installed_paths['playwright-core/browsers.json'] = web / 'node_modules/playwright-core/browsers.json'
        installed_hashes = {name: digest(path) for name, path in installed_paths.items()}
        # Resolve only the executable supplied by the installed, lockfile-pinned
        # Playwright package. No arbitrary browser input or launch overrides.
        chromium = Path(read_command(['node', '--input-type=module', '-e', "import {chromium} from '@playwright/test'; console.log(chromium.executablePath())"], web))
        browsers = json.loads((web / 'node_modules/playwright-core/browsers.json').read_text())
        revision = validate_chromium(browsers, chromium, Path(preparation_env['HOME']).resolve() / '.cache/ms-playwright')
        tracked = read_command(['git', 'ls-files']).splitlines()
        if any((ROOT / name).is_symlink() for name in tracked):
            raise ValueError('symlink source')
        before = {name: digest(ROOT / name) for name in tracked}
        provenance.update(commit=commit, tree=tree, sourceSha256=before, lockSha256=digest(web / 'package-lock.json'), playwrightVersion=playwright_version, playwrightManifestSha256=installed_hashes, chromiumRevision=revision, chromiumSha256=digest(chromium), stage='assets')
        # Normal product assets must exist BEFORE go:embed compiles the binary.
        # Never use the synthetic fixture's Vite config or a private outDir.
        assets = web / 'dist'
        expected_assets = {name[len('web/dist/'):]: value for name, value in before.items() if name.startswith('web/dist/')}
        if asset_hashes(assets) != expected_assets:
            raise ValueError('untracked or modified embedded assets')
        cancellation.checkpoint()
        run_command(['npm', 'run', 'build'], log, cwd=web, env=preparation_env, timeout=120)
        provenance['assetSha256'] = asset_hashes(assets)
        if provenance['assetSha256'] != expected_assets:
            raise ValueError('frontend build changed embedded asset inventory')
        if before != {name: digest(ROOT / name) for name in tracked}:
            raise ValueError('source changed during frontend build')
        cancellation.checkpoint()
        provenance['stage'] = 'compile'
        binary = private / 'soba-product-activation.test'
        env = dict(preparation_env, GOTOOLCHAIN='local', GOWORK='off', GOPROXY='off', GOSUMDB='off', GOTELEMETRY='off', GOFLAGS='-mod=readonly -buildvcs=false')
        run_command(['go', 'test', '-race', '-tags=' + TAGS, '-c', '-o', str(binary), './cmd/soba'], log, env=env)
        provenance['fixtureSha256'] = digest(binary)
        if asset_hashes(assets) != provenance['assetSha256']:
            raise ValueError('production assets changed during compile')
        wrapper_report = private / 'wrapper-result.json'
        provenance['stage'] = 'runtime'
        # The native scope owns 360/375/390/405s budgets and keeps failed
        # cleanup attached beyond 405s. CI hard kill is failure containment.
        code = cancellation.run_node(['node', 'browser/product-activation-runner.mjs'], cwd=web,
                                     env=child_environment(private, binary, chromium, wrapper_report))
        info = wrapper_report.lstat()
        # Fixed two-case diagnostics, including three native roles per case.
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_size > 16384:
            raise ValueError('invalid result file')
        report = validate_report(strict_json(wrapper_report.read_text()))
        cancellation.checkpoint()
        if code != 0 and report['accepted']:
            raise ValueError('runner result mismatch')
        if before != {name: digest(ROOT / name) for name in tracked} or digest(binary) != provenance['fixtureSha256'] or asset_hashes(assets) != provenance['assetSha256']:
            raise ValueError('source or artifact changed')
        if digest(chromium) != provenance['chromiumSha256'] or binaries != {name: digest(Path(shutil.which(name))) for name in ('go', 'node')}:
            raise ValueError('tool or browser changed')
        if installed_hashes != {name: digest(path) for name, path in installed_paths.items()}:
            raise ValueError('Playwright manifests changed')
        cancellation.checkpoint()
        accepted = code == 0 and report['accepted']
        provenance['stage'] = 'passed' if accepted else 'runtime-failed'
        if accepted:
            shutil.rmtree(private)
            private = None
    except Exception:
        # Never serialize exceptions, child output, private paths or environment.
        report = failed_report(report['diagnostics'])
        accepted = False
    try:
        if cancellation.requested:
            accepted = False
            report = failed_report(report['diagnostics'])
            provenance['stage'] = 'cancelled'
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
    if cancellation.requested:
        accepted = False
    print('PASS: two production-entry cases and cleanup verified.' if accepted else 'FAIL: Product acceptance not established; private diagnostics withheld.')
    # A retained private directory is intentionally not deleted after failure;
    # ephemeral runner process-scope teardown must contain uncertain descendants.
    return 0 if accepted else 1


def main(argv=None):
    with Cancellation() as cancellation:
        return _main(cancellation, argv)


if __name__ == '__main__':
    raise SystemExit(main())
