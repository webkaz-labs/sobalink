#!/usr/bin/env python3
"""Windows-only Core timing evidence. Never a native/release acceptance gate.

Raw Go events stay bounded and private. Only inventoried top-level test names,
relative timings, counts and closed classifications are reconstructed publicly.
The exhaustive invocation changes neither suite selection nor its 10m timeout.
"""
import ctypes
from datetime import datetime
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
PACKAGE = 'github.com/webkaz-labs/sobalink/internal/core'
GOFLAGS = '-mod=readonly -tags=ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy'
TEST_NAME = re.compile(r'(?:Test|Example|Fuzz)[A-Za-z0-9_]*\Z')
LEASE = 'TestMessageQueuePersistedLeaseUnknownNeverReoffersOrDowngrades'
LEASE_PROOF = 'TestMessageQueueUnknownLeaseFixtureReadsBeforeReply'
MIXED = 'TestMixedUnknownReadinessNeverSelectsAnotherRoute'
COMPILE_TAGS = 'ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,directlan_activation_native,resource_inspection_native'
BASE = ['go', 'test', '-race', '-count=1', '-json', '-timeout=10m']
COMMANDS = {
    'prepare': ['go', 'run', './cmd/prepare-engine'],
    'verify': ['go', 'run', './cmd/prepare-engine', '--verify'],
    'modules': ['go', 'mod', 'verify'],
    'compile-core': ['go', 'test', '-c', '-p=1', '-tags=' + COMPILE_TAGS + ',resource_management_native,resource_group_catalog_native', '-o', 'core-tags.test.exe', './internal/core'],
    'compile-stubs': ['go', 'test', '-c', '-p=1', '-tags=' + COMPILE_TAGS, '-o', 'resourceacceptance.test.exe', './internal/resourceacceptance'],
    'inventory': BASE + ['-list=^(Test|Example|Fuzz)', './internal/core'],
    'lease': BASE + ['-run=^(' + LEASE + '|' + LEASE_PROOF + ')$', './internal/core'],
    'mixed': BASE + ['-run=^' + MIXED + '$', './internal/core'],
    'suite': BASE + ['./internal/core'],
}
BOUNDS = {'prepare': 300, 'verify': 120, 'modules': 120, 'compile-core': 720, 'compile-stubs': 120, 'inventory': 720,
          'lease': 660, 'mixed': 660, 'suite': 660}
CAPTURE_BYTES = 16 * 1024 * 1024
MAX_TESTS = 4096
MAX_EVENTS = 100000
MAX_SUBTESTS = 20000


def module(name):
    path = Path(__file__).with_name(name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


parser = module('ci-go-test')
metrics = module('ci-metrics')
native = module('ci-nearby-native')


# Fixed-width Win32 ABI; c_ulong is 64-bit on some non-Windows test hosts.
DWORD, HANDLE, SIZE_T = ctypes.c_uint32, ctypes.c_void_p, ctypes.c_size_t


class BasicLimits(ctypes.Structure):
    _fields_ = [('PerProcessUserTimeLimit', ctypes.c_int64), ('PerJobUserTimeLimit', ctypes.c_int64),
                ('LimitFlags', DWORD), ('MinimumWorkingSetSize', SIZE_T),
                ('MaximumWorkingSetSize', SIZE_T), ('ActiveProcessLimit', DWORD),
                ('Affinity', SIZE_T), ('PriorityClass', DWORD), ('SchedulingClass', DWORD)]


class IOCounters(ctypes.Structure):
    _fields_ = [(key, ctypes.c_uint64) for key in ('ReadOperationCount', 'WriteOperationCount',
                'OtherOperationCount', 'ReadTransferCount', 'WriteTransferCount', 'OtherTransferCount')]


class ExtendedLimits(ctypes.Structure):
    _fields_ = [('BasicLimitInformation', BasicLimits), ('IoInfo', IOCounters),
                ('ProcessMemoryLimit', SIZE_T), ('JobMemoryLimit', SIZE_T),
                ('PeakProcessMemoryUsed', SIZE_T), ('PeakJobMemoryUsed', SIZE_T)]


class Accounting(ctypes.Structure):
    _fields_ = [(key, ctypes.c_int64) for key in ('TotalUserTime', 'TotalKernelTime',
                'ThisPeriodTotalUserTime', 'ThisPeriodTotalKernelTime')] + [
                (key, DWORD) for key in ('TotalPageFaultCount', 'TotalProcesses',
                'ActiveProcesses', 'TotalTerminatedProcesses')]


class OwnedJob:
    """Own only one gated fixed-command launcher and its inherited descendants."""
    def __init__(self):
        self.api = ctypes.WinDLL('kernel32', use_last_error=True)
        signatures = {
            'CreateJobObjectW': ([ctypes.c_void_p, ctypes.c_wchar_p], HANDLE),
            'SetInformationJobObject': ([HANDLE, ctypes.c_int, ctypes.c_void_p, DWORD], ctypes.c_int),
            'AssignProcessToJobObject': ([HANDLE, HANDLE], ctypes.c_int),
            'QueryInformationJobObject': ([HANDLE, ctypes.c_int, ctypes.c_void_p, DWORD, ctypes.c_void_p], ctypes.c_int),
            'TerminateJobObject': ([HANDLE, ctypes.c_uint32], ctypes.c_int),
            'CloseHandle': ([HANDLE], ctypes.c_int),
        }
        for name, (arguments, result) in signatures.items():
            function = getattr(self.api, name)
            function.argtypes, function.restype = arguments, result
        self.handle = self.api.CreateJobObjectW(None, None)
        if not self.handle:
            raise OSError('job creation failed')
        limits = ExtendedLimits()
        limits.BasicLimitInformation.LimitFlags = 0x2000  # KILL_ON_JOB_CLOSE, no breakaway.
        if not self.api.SetInformationJobObject(self.handle, 9, ctypes.byref(limits), ctypes.sizeof(limits)):
            self.close()
            raise OSError('job configuration failed')

    def assign(self, process):
        if not self.api.AssignProcessToJobObject(self.handle, int(process._handle)):
            raise OSError('job assignment failed')

    def active(self):
        value = Accounting()
        if not self.api.QueryInformationJobObject(self.handle, 1, ctypes.byref(value), ctypes.sizeof(value), None):
            raise OSError('job query failed')
        return value.ActiveProcesses

    def terminate(self):
        if not self.api.TerminateJobObject(self.handle, 1):
            raise OSError('job termination failed')

    def close(self):
        if self.handle:
            handle, self.handle = self.handle, None
            if not self.api.CloseHandle(handle):
                raise OSError('job close failed')


def owned_child(phase):
    # -I disables user-site/PYTHONPATH; all imports above are local or stdlib.
    # No command or descendant can start until the parent assigns this process.
    if phase not in COMMANDS or sys.platform != 'win32' or sys.stdin.buffer.read(1) != b'G':
        return 126
    command = list(COMMANDS[phase])
    if phase in ('compile-core', 'compile-stubs'):
        destination = command.index('-o') + 1
        command[destination] = str(Path(os.environ['TMP']) / command[destination])
    return subprocess.run(command, cwd=ROOT, stdin=subprocess.DEVNULL,
                          shell=False, check=False).returncode


def capture(phase, env):
    """Bound both private streams and own every descendant before gate release."""
    start = time.monotonic()
    output = [bytearray(), bytearray()]
    overflow, broken = threading.Event(), threading.Event()
    process, job, readers = None, None, []
    status, code, joined = 'launch_failed', None, False

    def drain(source, target):
        try:
            while block := source.read(8192):
                room = CAPTURE_BYTES - len(target)
                target.extend(block[:max(0, room)])
                if len(block) > room:
                    overflow.set()
        except (OSError, ValueError):
            broken.set()
        finally:
            source.close()

    try:
        job = OwnedJob()
        process = subprocess.Popen([sys.executable, '-I', '-B', str(Path(__file__).resolve()), '--owned', phase],
            cwd=ROOT, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            shell=False)
        readers = [threading.Thread(target=drain, args=(source, target), daemon=True)
                   for source, target in zip((process.stdout, process.stderr), output)]
        for reader in readers:
            reader.start()
        # A failed assignment leaves the launcher blocked and unable to spawn.
        job.assign(process)
        process.stdin.write(b'G')
        process.stdin.close()
        status = 'completed'
        deadline = start + BOUNDS[phase]
        while process.poll() is None:
            if overflow.is_set() or broken.is_set() or time.monotonic() >= deadline:
                status = 'output_limit' if overflow.is_set() else 'capture_failed' if broken.is_set() else 'watchdog_timeout'
                break
            time.sleep(0.02)
        code = process.poll()
        if status == 'completed':
            status = 'completed' if code == 0 else 'nonzero'
            if job.active():
                status = 'unjoined_children'
    except (OSError, ValueError):
        status = 'capture_failed' if process is not None else 'launch_failed'
    finally:
        cleanup_ok = True
        # Terminate also on unexpected exceptions; never release an unassigned
        # gate or use a breakaway fallback. Query failures remain unresolved.
        if process is not None:
            if process.stdin and not process.stdin.closed:
                process.stdin.close()
            if process.poll() is None:
                try:
                    process.kill()
                except OSError:
                    cleanup_ok = False
        if job is not None:
            try:
                if job.active():
                    job.terminate()
                deadline = time.monotonic() + 10
                while job.active() and time.monotonic() < deadline:
                    time.sleep(0.02)
                cleanup_ok &= job.active() == 0
            except OSError:
                cleanup_ok = False
        if process is not None:
            try:
                code = process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                cleanup_ok = False
        for reader in readers:
            reader.join(timeout=5)
        cleanup_ok &= all(not reader.is_alive() for reader in readers)
        if job is not None:
            try:
                job.close()
            except OSError:
                cleanup_ok = False
        joined = cleanup_ok
        if not joined:
            status = 'cleanup_unresolved'
        elif overflow.is_set():
            status = 'output_limit'
        elif broken.is_set():
            status = 'capture_failed'
    return {'process': status, 'returncode': code, 'ownersJoined': joined,
            'wallSeconds': round(time.monotonic() - start, 6)}, bytes(output[0])


def events(raw):
    if type(raw) is not bytes or len(raw) > CAPTURE_BYTES:
        raise ValueError('invalid capture')
    rows = raw.splitlines(keepends=True)
    if len(rows) > MAX_EVENTS:
        raise ValueError('event bound exceeded')
    for row in rows:
        if not row.endswith(b'\n') or len(row) > 65536:
            raise ValueError('invalid event framing')
        yield parser.parse_event(row)


def inventory(raw):
    names, lifecycle = [], []
    for event in events(raw):
        action = event['Action']
        if action in parser.BUILD_ACTIONS:
            if action == 'build-fail':
                raise ValueError('inventory build failure')
            continue
        if event['Package'] != PACKAGE or event.get('Test') is not None:
            raise ValueError('unexpected inventory owner')
        if action == 'output':
            value = event['Output'].removesuffix('\n').removesuffix('\r')
            if TEST_NAME.fullmatch(value):
                names.append(value)
            elif not re.fullmatch(r'ok\s+' + re.escape(PACKAGE) + r'\s+[0-9]+\.[0-9]+s', value):
                raise ValueError('unexpected inventory output')
        else:
            lifecycle.append(action)
    if (lifecycle != ['start', 'pass'] or not names or len(names) > MAX_TESTS
            or len(set(names)) != len(names) or LEASE not in names or LEASE_PROOF not in names or MIXED not in names):
        raise ValueError('incomplete inventory')
    return names


def relative_time(event, origin):
    value = event.get('Time')
    if not isinstance(value, str) or not value.endswith('Z'):
        raise ValueError('invalid event clock')
    current = datetime.fromisoformat(value[:-1] + '+00:00').timestamp()
    delta = current - (current if origin is None else origin)
    if not math.isfinite(delta) or delta < 0 or delta > 720:
        raise ValueError('invalid relative clock')
    return current, round(delta, 6)


def timing(raw, expected, process):
    """Account every inventory entry, including observed skips and missing tests."""
    if (not expected or len(expected) > MAX_TESTS or len(set(expected)) != len(expected)
            or any(not TEST_NAME.fullmatch(name) for name in expected)):
        raise ValueError('invalid expected tests')
    result = dict(process, accountingComplete=False, allTestsPassed=False, eventStreamValid=True,
                  packageOutcome='missing', packageSeconds=None, packageTimeoutObserved=False,
                  expectedCount=len(expected), startedCount=0, terminalCount=0,
                  passedCount=0, failedCount=0, skippedCount=0, runningCount=0, notRunCount=0,
                  subtestStarted=0, subtestTerminal=0, subtestSkipped=0, subtestFailed=0,
                  tests=[], progress=[])
    rows = {name: {'test': name, 'outcome': 'not_run', 'startedSeconds': None,
                  'finishedSeconds': None, 'elapsedSeconds': None, 'pauseCount': 0,
                  'continueCount': 0} for name in expected}
    states, origin, last_time = {}, None, 0
    package_started, package_terminal = False, False
    try:
        for event in events(raw):
            action = event['Action']
            if action in parser.BUILD_ACTIONS:
                if action == 'build-fail':
                    raise ValueError('build failed')
                continue
            if event['Package'] != PACKAGE or package_terminal:
                raise ValueError('unexpected package event')
            test = event.get('Test')
            top = test.split('/', 1)[0] if test else None
            if top is not None and top not in rows:
                raise ValueError('unexpected test owner')
            if action in ('output', 'attr', 'artifacts'):
                if not package_started:
                    raise ValueError('output before package start')
                if event.get('Output') == 'panic: test timed out after 10m0s\n':
                    result['packageTimeoutObserved'] = True
                continue
            current, relative = relative_time(event, origin)
            if relative < last_time:
                raise ValueError('nonmonotonic event clock')
            last_time = relative
            if test is None:
                if action == 'start' and not package_started:
                    package_started, origin = True, current
                    continue
                if action not in ('pass', 'fail', 'skip') or not package_started:
                    raise ValueError('invalid package lifecycle')
                if 'Elapsed' not in event or event['Elapsed'] > 720:
                    raise ValueError('missing or unbounded package timing')
                package_terminal = True
                result['packageOutcome'] = action
                result['packageSeconds'] = event.get('Elapsed')
                continue
            if not package_started or len(test) > 1024 or len(states) > MAX_TESTS + MAX_SUBTESTS:
                raise ValueError('invalid test lifecycle')
            if action == 'run' and test not in states:
                states[test] = 'running'
                if test == top:
                    rows[top].update(outcome='running', startedSeconds=relative)
                else:
                    if top not in states or states[top] not in ('running', 'paused'):
                        raise ValueError('subtest before parent')
                    result['subtestStarted'] += 1
            elif action == 'pause' and states.get(test) == 'running':
                states[test] = 'paused'
                if test == top:
                    rows[top]['pauseCount'] += 1
            elif action == 'cont' and states.get(test) == 'paused':
                states[test] = 'running'
                if test == top:
                    rows[top]['continueCount'] += 1
            elif action in ('pass', 'fail', 'skip') and states.get(test) in ('running', 'paused'):
                if 'Elapsed' not in event or event['Elapsed'] > 720 or any(name.startswith(test + '/') and state in ('running', 'paused') for name, state in states.items()):
                    raise ValueError('incomplete child or terminal timing')
                states[test] = action
                if test == top:
                    rows[top].update(outcome=action, finishedSeconds=relative, elapsedSeconds=event.get('Elapsed'))
                else:
                    result['subtestTerminal'] += 1
                    result['subtestSkipped'] += action == 'skip'
                    result['subtestFailed'] += action == 'fail'
            else:
                raise ValueError('duplicate or unordered test lifecycle')
    except (ValueError, UnicodeError, RecursionError, OverflowError):
        result['eventStreamValid'] = False
    result['observedThroughSeconds'] = last_time
    result['tests'] = list(rows.values())
    for row in result['tests']:
        outcome = row['outcome']
        result['startedCount'] += outcome != 'not_run'
        result['terminalCount'] += outcome in ('pass', 'fail', 'skip')
        key = {'pass': 'passedCount', 'fail': 'failedCount', 'skip': 'skippedCount',
               'running': 'runningCount', 'not_run': 'notRunCount'}[outcome]
        result[key] += 1
    # Event-clock buckets permit pace analysis without raw timestamps/output.
    for boundary in range(30, int(last_time) + 31, 30):
        result['progress'].append({'throughSeconds': boundary,
            'started': sum(row['startedSeconds'] is not None and row['startedSeconds'] <= boundary for row in rows.values()),
            'terminal': sum(row['finishedSeconds'] is not None and row['finishedSeconds'] <= boundary for row in rows.values())})
    result['accountingComplete'] = (result['eventStreamValid'] and package_started and package_terminal
        and result['terminalCount'] == len(expected) and result['subtestStarted'] == result['subtestTerminal'])
    result['allTestsPassed'] = (result['accountingComplete'] and process['process'] == 'completed'
        and process['returncode'] == 0 and process['ownersJoined'] and result['packageOutcome'] == 'pass'
        and result['passedCount'] == len(expected) and not result['subtestSkipped'] and not result['subtestFailed'])
    return result


def checked(command):
    state, code, stdout, _ = native.capture(command, timeout=30, limit=CAPTURE_BYTES)
    if state != 'completed' or code != 0:
        raise ValueError('metadata command failed')
    return stdout


def source_inventory():
    if checked(['git', 'status', '--porcelain', '--untracked-files=no']).strip():
        raise ValueError('modified source')
    # Reuse the existing exact prepared-dependency allowlist, including ignored
    # files; no arbitrary untracked Go/Python files can participate in this run.
    nearby = module('ci-nearby-diagnostic')
    nearby.validate_untracked(checked(['git', 'ls-files', '--others', '-z']))
    if any((ROOT / name.rstrip('/')).is_symlink() for name in nearby.DEPENDENCY_PREFIXES):
        raise ValueError('unsafe dependency root')
    raw = checked(['git', 'ls-files', '-z'])
    if not raw or not raw.endswith(b'\0'):
        raise ValueError('invalid source inventory')
    names = raw[:-1].decode('utf-8').split('\0')
    if len(names) != len(set(names)):
        raise ValueError('duplicate source')
    result = {}
    for name in names:
        path = ROOT / name
        if (any(part in ('', '.', '..') for part in name.split('/')) or '\\' in name
                or path.is_symlink() or not path.is_file()
                or any(parent.is_symlink() for parent in path.parents if parent != ROOT)):
            raise ValueError('unsafe source')
        result[name] = hashlib.sha256(path.read_bytes()).hexdigest()
    return result


def aggregate(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def execution_complete(row):
    # Native Go should never pass after a fail event; do not trust a forged or
    # contradictory zero exit. Observed ordinary-suite skips remain explicit.
    return (row['accountingComplete'] and row['process'] == 'completed' and row['returncode'] == 0
            and row['ownersJoined'] and row['packageOutcome'] == 'pass'
            and not row['failedCount'] and not row['subtestFailed'])


def capture_phase(phase, env, result):
    # An exception before a receipt returns must never be mistaken for proof
    # that every owner was joined merely because earlier rows are complete.
    result['ownersState'] = 'in_flight'
    process, raw = capture(phase, env)
    result['ownersState'] = 'joined' if process['ownersJoined'] else 'unresolved'
    return process, raw


def run_diagnostic(env, result):
    for phase in ('prepare', 'verify', 'modules', 'compile-core', 'compile-stubs', 'inventory'):
        result['phase'] = phase
        process, raw = capture_phase(phase, env, result)
        result['preparation'][phase] = process
        if process['process'] != 'completed' or not process['ownersJoined']:
            raise ValueError('preparation failed')
        if phase == 'inventory':
            names = inventory(raw)
    result['inventorySha256'] = aggregate(names)
    for phase, expected in (('lease', [LEASE, LEASE_PROOF]), ('mixed', [MIXED]), ('suite', names)):
        result['phase'] = phase
        process, raw = capture_phase(phase, env, result)
        result['runs'][phase] = timing(raw, expected, process)
        # Reuse the content-free timing record contract, without wrapping raw
        # stdout through ci-metrics.run_suite (which intentionally inherits it).
        record = {'suite': 'windows-core-' + phase, 'elapsed_seconds': process['wallSeconds'],
                  'returncode': process['returncode'] if process['returncode'] is not None else 126}
        accepted = (result['runs'][phase]['allTestsPassed'] if phase != 'suite'
                    else execution_complete(result['runs'][phase]))
        if record['returncode'] == 0 and not accepted:
            record['returncode'] = 1
        record['status'] = 'passed' if record['returncode'] == 0 else 'failed'
        metrics.validate_record(record)
        metrics.append_record(record)
        result['metrics'].append(record)
        if not process['ownersJoined']:
            raise ValueError('cleanup unresolved')


def main(argv=None):
    args = sys.argv[1:] if argv is None else argv
    if len(args) == 2 and args[0] == '--owned':
        return owned_child(args[1])
    if args or sys.platform != 'win32' or os.environ.get('GITHUB_ACTIONS') != 'true':
        print('FAIL: fixed hosted Windows timing entry required.')
        return 1
    result = {'schema': 1, 'scope': 'windows-core-exact-suite-timing', 'phase': 'preflight',
              'sourceVerified': False, 'cleanupComplete': False, 'timingComplete': False,
              'ownersState': 'not_started',
              'releaseValidation': False, 'preparation': {}, 'runs': {}, 'metrics': []}
    provenance = {'schema': 1, 'commit': None, 'tree': None, 'sourceSha256': None,
                  'inventorySha256': None, 'runner': 'windows-2025', 'go': 'go1.27.1',
                  'race': True, 'count': 1, 'packageTimeoutSeconds': 600,
                  'tags': GOFLAGS.split('-tags=')[1].split(','), 'cgo': True}
    private, export, source = None, None, None
    export_ready = False
    try:
        os.umask(0o077)
        temporary = Path(os.environ['RUNNER_TEMP'])
        export = temporary / 'soba-windows-core-sanitized'
        export.mkdir(mode=0o700, exist_ok=False)
        export_ready = True
        private = Path(tempfile.mkdtemp(prefix='soba-windows-core-private-', dir=temporary))
        commit = checked(['git', 'rev-parse', 'HEAD']).decode().strip()
        tree = checked(['git', 'rev-parse', 'HEAD^{tree}']).decode().strip()
        selector = module('ci-diagnostic')
        event_path = Path(os.environ['GITHUB_EVENT_PATH'])
        if event_path.stat().st_size > CAPTURE_BYTES:
            raise ValueError('event size exceeded')
        event = json.loads(event_path.read_text(encoding='utf-8'), object_pairs_hook=parser.unique_object)
        if (selector.select(os.environ['GITHUB_EVENT_NAME'], event, os.environ['GITHUB_REPOSITORY'],
                os.environ['GITHUB_REF'], os.environ['GITHUB_SHA'], commit) != 'windows-core'
                or not re.fullmatch('[0-9a-f]{40}', tree)):
            raise ValueError('wrong exact source')
        versions = json.loads(checked(['go', 'env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH',
                                     'GOOS', 'GOARCH', 'CGO_ENABLED']))
        native.identities('windows-amd64', versions)
        if (os.environ.get('GOFLAGS') != GOFLAGS or os.environ.get('GOTOOLCHAIN') != 'local'
                or os.environ.get('GOWORK') != 'off' or any(key in os.environ for key in ('GOMAXPROCS', 'GOTRACEBACK'))):
            raise ValueError('changed ordinary CI settings')
        source = source_inventory()
        provenance.update(commit=commit, tree=tree, sourceSha256=aggregate(source))
        env = dict(os.environ, TMPDIR=str(private), TMP=str(private), TEMP=str(private))
        os.environ['SOBALINK_CI_METRICS'] = str(private / 'metrics.jsonl')
        run_diagnostic(env, result)
        result['phase'] = 'verify-source'
        if source_inventory() != source:
            raise ValueError('source changed')
        result['sourceVerified'] = True
        provenance['inventorySha256'] = result.pop('inventorySha256', None)
        suite = result['runs']['suite']
        result['timingComplete'] = (all(result['runs'][phase]['allTestsPassed'] for phase in ('lease', 'mixed'))
            and execution_complete(suite))
        result['phase'] = 'complete'
    except (Exception, KeyboardInterrupt):
        # Exceptions, paths, environment, test bodies and stacks remain private.
        pass
    finally:
        if private is not None and result['ownersState'] in ('not_started', 'joined') and all(row['ownersJoined'] for row in result['preparation'].values()) and all(
                row['ownersJoined'] for row in result['runs'].values()):
            try:
                shutil.rmtree(private)
                result['cleanupComplete'] = not private.exists()
            except OSError:
                result['cleanupComplete'] = False
        result['timingComplete'] &= result['cleanupComplete'] and result['sourceVerified']
        if export_ready:
            try:
                (export / 'result.json').write_text(json.dumps(result, indent=2, allow_nan=False) + '\n', encoding='utf-8')
                (export / 'provenance.json').write_text(json.dumps(provenance, indent=2, allow_nan=False) + '\n', encoding='utf-8')
                with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
                    print('sanitized_ready=true', file=output)
            except (OSError, ValueError):
                result['timingComplete'] = False
    print('Windows Core timing complete; no release validation.' if result['timingComplete'] else
          'FAIL: Windows Core timing incomplete; inspect sanitized evidence only.')
    return 0 if result['timingComplete'] else 1


if __name__ == '__main__':
    sys.exit(main())
