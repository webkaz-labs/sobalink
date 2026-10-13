#!/usr/bin/env python3
"""Two exact existing text cases; export only reconstructed fixed evidence."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import stat
import sys
import tempfile

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
PACKAGE = 'github.com/webkaz-labs/sobalink/internal/core'
TAGS = 'lanlink_integration,directlan_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy'
CASES = {
    'restart': 'TestDurableTextProcessRestartACKLossAndHiddenReplay',
    'browser': 'TestDurableTextThreeProcessBrowser',
}
FIXTURE_PHASES = {
    'restart': ('start', 'prepare', 'commit', 'sender-restart', 'saved-ack-loss', 'receiver-restart',
                'receipt-recovery', 'history-hide', 'hidden-replay', 'child-join', 'complete'),
    'browser': ('start', 'browser-launch', 'review-wait', 'review-verify', 'recovery-wait',
                'recovery-verify', 'browser-result', 'core-save', 'child-join', 'summary', 'complete'),
}
FIXTURE_FILES = {'restart': 'durable_text_process_integration_test.go',
                 'browser': 'durable_text_browser_integration_test.go'}
MARKER = 'SOBA_TEXT_PHASE '
VERSIONS = {'go': 'go1.27.1', 'node': 'v24.19.0', 'npm': '11.9.0'}
CAPTURE_BYTES = 8 * 1024 * 1024
PHASES = {'none', 'preflight', 'assets', 'compile', 'restart-process', 'restart-events',
          'browser-process', 'browser-events', 'browser-receipt', 'browser-evidence', 'verify-source', 'cleanup', 'export'}
BROWSER_PHASES = {'fixture-setup', 'session', 'open-peer', 'queue-mode', 'prepare', 'review-exact',
    'review-consent', 'review-fit', 'review-checkpoint', 'commit', 'lost-response', 'recover',
    'recovery-checkpoint', 'outbox-open', 'outbox-status-en', 'detail-request', 'detail-exact',
    'detail-receipt-en', 'detail-fit-en', 'escape', 'locale-ja', 'outbox-open-ja', 'outbox-status-ja',
    'outbox-receipt-ja', 'outbox-fit-ja', 'command-counts', 'runtime-counts', 'result-write',
    'body-complete', 'fixture-cleanup', 'unavailable'}
SCOPE = 'linux-synthetic-loopback-text-restart-and-browser'


def module(name):
    path = Path(__file__).with_name(name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


native = module('ci-nearby-native')
summary = module('ci-browser-summary')
parser = module('ci-go-test')


def checked(command, *, cwd=None, env=None, timeout=30):
    state, code, stdout, _ = native.capture(command, cwd=ROOT if cwd is None else cwd,
                                           env=env, timeout=timeout, limit=CAPTURE_BYTES)
    if state != 'completed' or code != 0:
        raise ValueError('private command did not complete')
    return stdout


def phase_sequence(raw, case):
    """Accept only an ordered prefix of literal markers at bound source lines."""
    if (case not in CASES or type(raw) is not bytes or not raw.endswith(b'\n')
            or len(raw) > CAPTURE_BYTES):
        raise ValueError('invalid text phase capture')
    source = ROOT / 'internal/core' / FIXTURE_FILES[case]
    literals = {}
    for number, line in enumerate(source.read_text(encoding='utf-8').splitlines(), 1):
        match = re.fullmatch(r'\s*t\.Log\("SOBA_TEXT_PHASE ([a-z-]+)"\)', line)
        if match:
            if match[1] in literals:
                raise ValueError('duplicate source phase')
            literals[match[1]] = number
    if set(literals) != set(FIXTURE_PHASES[case]):
        raise ValueError('source phase inventory changed')
    pattern = re.compile(r'    ' + re.escape(FIXTURE_FILES[case]) + r':([1-9][0-9]*): SOBA_TEXT_PHASE ([a-z-]+)\n')
    phases, lifecycle = [], []
    start = [(False, 'start'), (True, 'run')]
    for line in raw.splitlines():
        event = parser.parse_event(line)
        if event.get('Package') != PACKAGE or event.get('Test') not in (None, CASES[case]):
            raise ValueError('unexpected text phase owner')
        if event['Action'] != 'output':
            lifecycle.append((event.get('Test') is not None, event['Action']))
        output = event.get('Output', '')
        if MARKER in output:
            match = pattern.fullmatch(output)
            if (lifecycle != start or event['Action'] != 'output' or event.get('Test') != CASES[case] or not match
                    or literals.get(match[2]) != int(match[1])):
                raise ValueError('unbound text phase marker')
            phases.append(match[2])
    endings = [[], [(False, 'fail')]] + [[(True, end)] for end in ('pass', 'fail', 'skip')]
    endings += [[(True, end), (False, 'fail' if end == 'fail' else 'pass')] for end in ('pass', 'fail', 'skip')]
    if (lifecycle[:2] != start or lifecycle[2:] not in endings
            or not phases or tuple(phases) != FIXTURE_PHASES[case][:len(phases)]):
        raise ValueError('invalid text phase sequence')
    return phases


def test_events(raw, case, returncode):
    """Require ordered exact-once RUN/PASS and package PASS, with no skips."""
    if (case not in CASES or type(returncode) is not int or returncode != 0
            or type(raw) is not bytes or not raw.endswith(b'\n') or len(raw) > CAPTURE_BYTES):
        raise ValueError('invalid text case capture')
    if tuple(phase_sequence(raw, case)) != FIXTURE_PHASES[case]:
        raise ValueError('text phase evidence incomplete')
    lifecycle = []
    for line in raw.splitlines():
        event = parser.parse_event(line)
        if (event.get('Package') != PACKAGE or event.get('Test') not in (None, CASES[case])
                or event['Action'] in parser.BUILD_ACTIONS):
            raise ValueError('unexpected text test event')
        if event['Action'] == 'output':
            if not lifecycle or len(lifecycle) == 4:
                raise ValueError('output outside package lifecycle')
        else:
            lifecycle.append((event.get('Test') is not None, event['Action']))
    if lifecycle != [(False, 'start'), (True, 'run'), (True, 'pass'), (False, 'pass')]:
        raise ValueError('exact text case did not pass')
    return True


def digest(path):
    """Hash a stable regular file without following links or blocking on pipes."""
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1:
        raise ValueError('unsafe input file')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        current = os.fstat(stream.fileno())
        identity = lambda value: (value.st_dev, value.st_ino, value.st_mode, value.st_nlink,
                                  value.st_size, value.st_mtime_ns, value.st_ctime_ns)
        if identity(current) != identity(before):
            raise ValueError('input file changed')
        value = hashlib.sha256()
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            value.update(block)
        if identity(os.fstat(stream.fileno())) != identity(before) or identity(path.lstat()) != identity(before):
            raise ValueError('input file changed')
    return value.hexdigest()


def source_inventory(*, after_build=False):
    if checked(['git', 'status', '--porcelain', '--untracked-files=no']).strip():
        raise ValueError('modified tracked source')
    summary.nearby.validate_untracked(checked(['git', 'ls-files', '--others', '-z']), after_build=after_build)
    if any((ROOT / prefix.rstrip('/')).is_symlink() for prefix in summary.nearby.DEPENDENCY_PREFIXES):
        raise ValueError('unprepared dependency root')
    raw = checked(['git', 'ls-files', '-z'])
    if not raw or not raw.endswith(b'\0'):
        raise ValueError('invalid source inventory')
    names = [name.decode('utf-8') for name in raw[:-1].split(b'\0')]
    if len(names) != len(set(names)):
        raise ValueError('duplicate source input')
    for name in names:
        if any(part in ('', '.', '..') for part in name.split('/')) or '\\' in name:
            raise ValueError('invalid source path')
        if any(parent.is_symlink() for parent in (ROOT / name).parents if parent != ROOT):
            raise ValueError('unsafe source directory')
    return {name: digest(ROOT / name) for name in names}


def assets(directory):
    if directory.is_symlink() or not directory.is_dir():
        raise ValueError('invalid asset root')
    result = {}
    for path in sorted(directory.rglob('*')):
        mode = path.lstat().st_mode
        if stat.S_ISDIR(mode):
            continue
        result[path.relative_to(directory).as_posix()] = digest(path)
    if 'index.html' not in result:
        raise ValueError('missing embedded assets')
    return result


def aggregate(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def verify_source(before, asset_hashes, binary_hash, binary):
    if (source_inventory(after_build=True) != before or assets(ROOT / 'web/dist') != asset_hashes
            or digest(binary) != binary_hash):
        raise ValueError('source or executable changed')


LAYOUT_ROLES = ('document', 'dialog', 'modal-body', 'outbox', 'text-outbox', 'card', 'review', 'body',
    'identifier', 'actions', 'header', 'navigation', 'conversation-slot', 'conversation', 'timeline',
    'message-meta', 'composer-area', 'composer', 'composer-tools', 'send', 'mode', 'preparation')


def fixed_layout(value):
    width = lambda v: type(v) is int and 0 <= v <= 8192
    fields = {'role', 'clientWidth', 'scrollWidth', 'leftOverflow', 'rightOverflow'}
    if (type(value) is not dict or set(value) != {'viewportWidth', 'regions'}
            or not width(value['viewportWidth']) or type(value['regions']) is not list
            or len(value['regions']) != len(LAYOUT_ROLES)):
        raise ValueError('invalid fixed text layout')
    regions = []
    for role, row in zip(LAYOUT_ROLES, value['regions']):
        if (type(row) is not dict or set(row) != fields or row['role'] != role
                or any(not width(row[field]) for field in fields - {'role'})):
            raise ValueError('invalid fixed text layout region')
        regions.append({field: row[field] for field in sorted(fields)})
    return {'viewportWidth': value['viewportWidth'], 'regions': regions}


def browser_failure(value):
    if (type(value) is not dict or set(value) not in ({'schema', 'phase', 'errorKind'}, {'schema', 'phase', 'errorKind', 'layout'})
            or type(value['schema']) is not int or value['schema'] != 1
            or type(value['phase']) is not str or value['phase'] not in BROWSER_PHASES
            or type(value['errorKind']) is not str or value['errorKind'] not in summary.nearby.BROWSER_ERROR_KINDS):
        raise ValueError('invalid fixed text browser failure')
    record = {'schema': 1, 'phase': value['phase'], 'errorKind': value['errorKind']}
    if 'layout' in value:
        if value['phase'] not in ('review-fit', 'detail-fit-en', 'outbox-fit-ja') or value['errorKind'] != 'assertion':
            raise ValueError('layout outside fit failure')
        record['layout'] = fixed_layout(value['layout'])
    return record


def empty_result():
    return {'schema': 1, 'scope': SCOPE, 'diagnosticPassed': False, 'restartPassed': False,
            'browserPassed': False, 'sourceVerified': False, 'browser': None, 'browserFailure': None,
            'restartProcess': 'not_run', 'browserProcess': 'not_run', 'firstFailure': 'none',
            'restartPhase': 'not_run', 'browserPhase': 'not_run',
            'physicalLANVerified': False, 'releaseValidation': False}


def fail(report, phase):
    if phase not in PHASES - {'none'}:
        raise ValueError('unknown failure phase')
    if report['firstFailure'] == 'none':
        report['firstFailure'] = phase
    report['diagnosticPassed'] = False


def fixed_result(report):
    expected = empty_result()
    if type(report) is not dict or set(report) != set(expected):
        raise ValueError('invalid fixed result')
    for key, default in expected.items():
        if key not in ('browser', 'browserFailure') and type(report[key]) is not type(default):
            raise ValueError('invalid fixed result type')
    if (report['schema'] != 1 or report['scope'] != SCOPE or report['physicalLANVerified']
            or report['releaseValidation'] or report['firstFailure'] not in PHASES
            or any(report[key] not in native.PROCESS_STATES for key in ('restartProcess', 'browserProcess'))
            or any(report[case + 'Phase'] not in ('not_run', 'unavailable', *FIXTURE_PHASES[case]) for case in CASES)):
        raise ValueError('invalid fixed result value')
    browser = None if report['browser'] is None else summary.text_summary(report['browser'])
    failure = None if report['browserFailure'] is None else browser_failure(report['browserFailure'])
    if (report['browserPassed'] != (browser is not None)
            or report['browserPassed'] and failure is not None
            or any(report[case + 'Passed'] and report[case + 'Process'] != 'completed' for case in CASES)
            or report['diagnosticPassed'] != (report['restartPassed'] and report['browserPassed']
                                             and report['sourceVerified'] and report['firstFailure'] == 'none')
            or report['diagnosticPassed'] and any(report[case + 'Phase'] != 'complete' for case in CASES)):
        raise ValueError('inconsistent fixed result')
    return {**report, 'browser': browser, 'browserFailure': failure}


def main(argv=None):
    if ((sys.argv[1:] if argv is None else argv) or sys.platform != 'linux'
            or os.environ.get('GITHUB_ACTIONS') != 'true'):
        print('FAIL: fixed Linux hosted text diagnostic entry required.')
        return 1
    report, provenance = empty_result(), {'schema': 1, 'scope': SCOPE}
    export_ready, private, export = False, None, None
    before = asset_hashes = binary_hash = binary = None
    phase = 'preflight'
    try:
        os.umask(0o077)
        temporary = Path(os.environ['RUNNER_TEMP'])
        export = temporary / 'soba-text-sanitized'
        export.mkdir(mode=0o700, exist_ok=False)
        export_ready = True
        private = Path(tempfile.mkdtemp(prefix='soba-text-', dir=temporary))
        commit = checked(['git', 'rev-parse', 'HEAD']).decode().strip()
        tree = checked(['git', 'rev-parse', 'HEAD^{tree}']).decode().strip()
        selector = module('ci-diagnostic')
        event = summary.read_fixed(Path(os.environ['GITHUB_EVENT_PATH']), CAPTURE_BYTES)
        if (selector.select(os.environ['GITHUB_EVENT_NAME'], event, os.environ['GITHUB_REPOSITORY'],
                            os.environ['GITHUB_REF'], os.environ['GITHUB_SHA'], commit) != 'text'
                or not re.fullmatch('[0-9a-f]{40}', tree)):
            raise ValueError('wrong exact-head text diagnostic')
        before = source_inventory()
        versions = {key: checked(command).decode().strip() for key, command in (
            ('go', ['go', 'env', 'GOVERSION']), ('node', ['node', '--version']), ('npm', ['npm', '--version']))}
        if versions != VERSIONS:
            raise ValueError('toolchain mismatch')
        identities = summary.nearby.parse_json(checked(['go', 'env', '-json', 'GOVERSION', 'GOHOSTOS',
                                                       'GOHOSTARCH', 'GOOS', 'GOARCH', 'CGO_ENABLED']))
        native.identities('linux-amd64', identities)
        web = ROOT / 'web'
        chromium = Path(checked(['node', '--input-type=module', '-e',
            "import {chromium} from '@playwright/test'; console.log(chromium.executablePath())"], cwd=web).decode().strip())
        if not chromium.is_absolute():
            raise ValueError('pinned Chromium unavailable')
        # All provenance values are fixed labels or validated source/tool hashes.
        provenance.update(commit=commit, tree=tree, versions=dict(VERSIONS), sourceSha256=aggregate(before),
                          lockSha256=digest(web / 'package-lock.json'), chromiumSha256=digest(chromium),
                          toolchainSha256={name: digest(Path(shutil.which(name)).resolve()) for name in ('go', 'node')})
        phase = 'assets'
        asset_hashes = assets(web / 'dist')
        checked(['npm', 'run', 'build'], cwd=web, timeout=120)
        if assets(web / 'dist') != asset_hashes or source_inventory(after_build=True) != before:
            raise ValueError('embedded assets do not reproduce source')
        provenance['assetsSha256'] = aggregate(asset_hashes)
        phase = 'compile'
        env = dict(os.environ, GOTOOLCHAIN='local', GOWORK='off', GOPROXY='off', GOSUMDB='off',
                   GOTELEMETRY='off', GOFLAGS='-mod=readonly -buildvcs=false', CGO_ENABLED='1', TMPDIR=str(private))
        binary = private / 'core.test'
        checked(['go', 'test', '-race', '-tags=' + TAGS, '-c', '-o', str(binary), './internal/core'], env=env, timeout=600)
        binary_hash = digest(binary)
        provenance['fixtureSha256'] = binary_hash
        env.update(SOBALINK_RUN_DIRECT_INTEGRATION='1', SOBALINK_RUN_TRANSFER_BROWSER_NATIVE='1',
                   SOBA_TRANSFER_BROWSER_NODE=shutil.which('node'), SOBA_TRANSFER_BROWSER_CHROMIUM=str(chromium),
                   SOBA_TEXT_BROWSER_SUMMARY=str(private / 'text-native-summary.json'),
                   SOBA_TEXT_BROWSER_FAILURE=str(private / 'text-native-failure.json'))
        for case, name in CASES.items():
            phase = case + '-process'
            # Own the binary directly so the bounded capture does not time out
            # a wrapper while leaving the test owner running. Keep Go's 4m gate.
            state, code, stdout, _ = native.capture([str(binary), '-test.v=test2json', '-test.run=^' + name + '$',
                '-test.count=1', '-test.timeout=4m'], cwd=ROOT / 'internal/core', env=env, timeout=270, limit=CAPTURE_BYTES)
            report[case + 'Process'] = state
            report[case + 'Phase'] = 'unavailable'
            if state != 'completed' or code != 0:
                fail(report, phase)
            if case == 'browser' and os.path.lexists(private / 'text-native-failure.json'):
                try:
                    report['browserFailure'] = browser_failure(summary.read_fixed(private / 'text-native-failure.json', 8192))
                except Exception:
                    report['browserFailure'] = {'schema': 1, 'phase': 'unavailable', 'errorKind': 'evidence_unavailable'}
                fail(report, 'browser-evidence')
            phase = case + '-events'
            converted, converted_code, data, _ = native.capture(['go', 'tool', 'test2json', '-p', PACKAGE],
                cwd=ROOT, env=env, timeout=15, limit=CAPTURE_BYTES, stdin=stdout)
            if converted != 'completed' or converted_code != 0:
                raise ValueError('text event conversion failed')
            report[case + 'Phase'] = phase_sequence(data, case)[-1]
            if state != 'completed' or code != 0 or report['browserFailure'] is not None:
                raise ValueError('text process did not complete')
            test_events(data, case, code)
            if case == 'browser':
                phase = 'browser-receipt'
                report['browser'] = summary.text_summary(summary.read_fixed(private / 'text-native-summary.json', 8192))
            report[case + 'Passed'] = True
    except Exception:
        # Never serialize raw child/framework output, exceptions or private paths.
        fail(report, phase)
    finally:
        if before is not None and asset_hashes is not None and binary_hash is not None:
            try:
                verify_source(before, asset_hashes, binary_hash, binary)
                report['sourceVerified'] = True
            except Exception:
                fail(report, 'verify-source')
                report['browserFailure'] = None
                for case in CASES:
                    if report[case + 'Phase'] != 'not_run':
                        report[case + 'Phase'] = 'unavailable'
    report['diagnosticPassed'] = (report['restartPassed'] and report['browserPassed']
                                  and report['sourceVerified'] and report['firstFailure'] == 'none')
    if report['diagnosticPassed']:
        try:
            shutil.rmtree(private)
        except OSError:
            fail(report, 'cleanup')
    try:
        if not export_ready:
            raise ValueError('export unavailable')
        summary.write_fixed(export / 'result.json', fixed_result(report))
        summary.write_fixed(export / 'provenance.json', provenance)
        with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
            output.write('sanitized_ready=true\n')
    except Exception:
        fail(report, 'export')
    print('PASS: exact text restart and browser diagnostics verified; release validation excluded.' if report['diagnosticPassed'] else
          'FAIL: text diagnostic acceptance not established; private details withheld.')
    return 0 if report['diagnosticPassed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
