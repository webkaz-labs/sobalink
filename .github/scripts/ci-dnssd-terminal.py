#!/usr/bin/env python3
"""One ordinary Terminal launch, with privately owned and joined diagnostic children.

This observes process ancestry, not the privacy subsystem's responsible code.
No GUI is inspected or controlled; any permission prompt remains unobserved.
The hosted job owns the Terminal app/window lifecycle, not this helper.
"""
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import secrets
import shlex
import signal
import stat
import subprocess
import sys
import threading
import time

sys.dont_write_bytecode = True
SCRIPT = Path(__file__).resolve()
TERMINAL = Path('/System/Applications/Utilities/Terminal.app')
TERMINAL_EXECUTABLE = str(TERMINAL / 'Contents/MacOS/Terminal')
MARKER = b'SOBALINK_TERMINAL_MARKER\n'
MARKER_COMMAND = (sys.executable, '-I', '-B', '-c', 'print("SOBALINK_TERMINAL_MARKER")')
SELECTOR = '^TestDNSService(LocalOnly|SelectedLoopback|SelectedInterface|RawReceive|LegacySerialization)Capability$'
OPT_INS = ('SOBALINK_DNSSD_LOCAL_ONLY', 'SOBALINK_DNSSD_SELECTED_LOOPBACK',
           'SOBALINK_DNSSD_SELECTED_INTERFACE', 'SOBALINK_DNSSD_RAW_RECEIVE',
           'SOBALINK_DNSSD_LEGACY_SERIALIZATION')
ENV_KEYS = set(OPT_INS) | {'GOMAXPROCS', 'TMPDIR', 'TMP', 'TEMP'}
LAUNCH_SECONDS, TOTAL_SECONDS, RUNTIME_SECONDS = 35, 120, 75
RUNTIME_BYTES, FILE_BYTES = 65536, 4096
STATES = {'not_run', 'completed', 'nonzero', 'timeout', 'output_limit',
          'launch_failed', 'capture_failed', 'policy_denied'}
REASONS = {'not_run', 'observed', 'availability_unavailable', 'open_failed',
           'marker_timeout', 'marker_invalid', 'ancestry_unverified',
           'supervisor_timeout', 'supervisor_unverified', 'runtime_incomplete',
           'policy_denied', 'source_changed', 'private_state_invalid'}


def empty_receipt(*, marker_only=False):
    # nativeOwnerJoined means a direct-child Wait, never descendant cleanup.
    # marker_timeout with supervisorTerminationObserved=false means launch
    # ownership/cleanup remains unresolved, not that nothing was launched.
    return {'schema': 1, 'mode': 'marker_only' if marker_only else 'native_comparison',
            'status': 'not_run', 'reason': 'not_run', 'gateObservation': None,
            'originObservation': None,
            'availabilityVerified': False, 'openCompleted': False,
            'markerVerified': False, 'markerOwnerJoined': False,
            'terminalAncestorObserved': False, 'nativeStartObserved': False,
            'nativeOwnerJoined': False, 'supervisorTerminationObserved': False,
            'policyDeniedObserved': False, 'nativeProcessStatus': 'not_run', 'nativeExitCode': None,
            'promptState': 'unobserved',
            'responsibleIdentityVerified': False, 'terminalAppCleanupVerified': False}


def receipt(value):
    expected = empty_receipt()
    if (type(value) is not dict or set(value) != set(expected)
            or type(value['schema']) is not int or value['schema'] != 1
            or value['mode'] not in {'marker_only', 'native_comparison'}
            or value['status'] not in {'not_run', 'unavailable', 'observed', 'failed'}
            or value['reason'] not in REASONS or value['promptState'] != 'unobserved'
            or value['nativeProcessStatus'] not in STATES
            or value['nativeExitCode'] is not None and (type(value['nativeExitCode']) is not int or not -127 <= value['nativeExitCode'] <= 255)
            or any(type(value[key]) is not bool for key in expected if type(expected[key]) is bool)
            or value['responsibleIdentityVerified'] or value['terminalAppCleanupVerified']):
        raise ValueError('invalid fixed launch receipt')
    if (value['nativeStartObserved'] != (value['nativeExitCode'] is not None)
            or value['policyDeniedObserved'] != (value['nativeProcessStatus'] == 'policy_denied')
            or value['nativeProcessStatus'] == 'completed' and value['nativeExitCode'] != 0
            or value['nativeProcessStatus'] == 'nonzero' and value['nativeExitCode'] in (None, 0)
            or value['mode'] == 'marker_only' and (value['nativeStartObserved'] or value['nativeOwnerJoined']
                or value['nativeExitCode'] is not None or value['nativeProcessStatus'] != 'not_run')
            or value['mode'] == 'native_comparison' and value['status'] == 'observed' and value['nativeProcessStatus'] not in {'completed', 'nonzero'}
            or value['markerVerified'] and not value['markerOwnerJoined']
            or value['nativeStartObserved'] and not (value['markerVerified'] and value['terminalAncestorObserved'])
            or value['nativeOwnerJoined'] and not value['nativeStartObserved']
            or value['status'] == 'observed' and not all(value[key] for key in (
                'availabilityVerified', 'openCompleted', 'markerVerified', 'markerOwnerJoined',
                'terminalAncestorObserved', 'supervisorTerminationObserved'))
            or value['mode'] == 'native_comparison' and value['status'] == 'observed'
                and not (value['nativeStartObserved'] and value['nativeOwnerJoined'])):
        raise ValueError('incomplete fixed launch evidence')
    if value['terminalAncestorObserved'] and value['gateObservation'] is None:
        raise ValueError('missing ancestry observation')
    if value['gateObservation'] is not None:
        validate_observation(value['gateObservation'])
        if value['terminalAncestorObserved'] != value['gateObservation']['verified']:
            raise ValueError('inconsistent ancestry evidence')
    if value['originObservation'] is not None:
        if value['mode'] != 'marker_only':
            raise ValueError('origin explanation requires marker-only mode')
        validate_origin(value['originObservation'])
    return dict(value)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_private(path, value):
    data = json.dumps(value, sort_keys=True).encode()
    if len(data) > FILE_BYTES:
        raise ValueError('private receipt exceeded')
    temporary = path.with_name(path.name + '.incomplete')
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'wb') as target:
        target.write(data)
    if os.path.lexists(path):
        raise ValueError('private receipt already exists')
    # Only one designated writer owns each fixed name in the mode-0700
    # directory. Publish complete single-link bytes, never a partial record.
    os.rename(temporary, path)


def read_private(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as source:
        info = os.fstat(source.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_nlink != 1
                or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size > FILE_BYTES):
            raise ValueError('invalid private file')
        raw = source.read(FILE_BYTES + 1)
    if len(raw) > FILE_BYTES:
        raise ValueError('private file exceeded')
    return json.loads(raw)


def capture_owned(command, *, timeout, limit=RUNTIME_BYTES, env=None, stop_on_denial=False, cancelled=None, on_cleanup=None):
    """Return only after direct-child wait, with bounded streams.

    Commands are fixed. The native binary owns its own route subprocess;
    interrupted runs do not claim that descendant was joined. On cancellation
    the finally block kills/reaps that exact Popen child, never Terminal or a PID
    learned from a receipt. A supervisor alarm also enters this cleanup path.
    """
    process, readers, streams = None, [], [bytearray(), bytearray()]
    overflow, broken, denied = threading.Event(), threading.Event(), threading.Event()
    state, code, joined = 'capture_failed', None, False
    def drain(source, target):
        try:
            while True:
                data = os.read(source.fileno(), 4096)
                if not data:
                    break
                room = limit - len(target)
                target.extend(data[:max(room, 0)])
                if len(data) > room:
                    overflow.set()
                if stop_on_denial and (b'PolicyDenied' in target or re.search(
                        rb'"failureKind"\s*:\s*"permission_denied"', target)):
                    denied.set()
        except (OSError, ValueError):
            broken.set()
        finally:
            source.close()
    try:
        process = subprocess.Popen(command, shell=False, env=env, stdin=subprocess.DEVNULL,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        readers = [threading.Thread(target=drain, args=(source, target), daemon=True)
                   for source, target in zip((process.stdout, process.stderr), streams)]
        for thread in readers:
            thread.start()
        deadline, next_check = time.monotonic() + timeout, 0
        while process.poll() is None:
            if cancelled is not None and time.monotonic() >= next_check:
                if cancelled():
                    state = 'capture_failed'
                    break
                next_check = time.monotonic() + 0.5
            if denied.is_set() or overflow.is_set() or time.monotonic() >= deadline:
                state = 'policy_denied' if denied.is_set() else 'output_limit' if overflow.is_set() else 'timeout'
                break
            time.sleep(0.01)
        else:
            state = 'completed' if process.returncode == 0 else 'nonzero'
    except OSError:
        state = 'launch_failed' if process is None else 'capture_failed'
    finally:
        if process is not None:
            if process.poll() is None:
                process.kill()
            try:
                code = process.wait(timeout=5)
                joined = True
            except subprocess.TimeoutExpired:
                state = 'capture_failed'
            for thread in readers:
                thread.join(timeout=2)
            if any(thread.is_alive() for thread in readers) or broken.is_set():
                state = 'capture_failed'
            if overflow.is_set():
                state = 'output_limit'
            if denied.is_set():
                state = 'policy_denied'
        if on_cleanup is not None:
            on_cleanup((process is None or joined) and not any(thread.is_alive() for thread in readers))
    return state, code, bytes(streams[0]), bytes(streams[1]), joined


class ProcessObservationError(ValueError):
    def __init__(self, kind):
        self.kind = kind
        super().__init__('fixed process observation failure')


def process_info(pid, *, on_cleanup=None):
    # Darwin comm preserves any argv[0] path, but can fall back to a short
    # accounting name. It is display metadata, not kernel executable identity.
    # https://github.com/apple-oss-distributions/adv_cmds/blob/6bed8737a34dbb54782a18f47dccf933a9967a12/ps/print.c
    if type(pid) is not int or pid <= 0:
        raise ProcessObservationError('invalid')
    state, code, raw, error, joined = capture_owned(['/bin/ps', '-ww', '-p', str(pid),
        '-o', 'pid=,ppid=,uid=,lstart=,comm='], env={'LC_ALL': 'C', 'PATH': '/usr/bin:/bin'},
        timeout=2, limit=FILE_BYTES, on_cleanup=on_cleanup)
    if state == 'nonzero' and code == 1 and joined and not raw.strip() and not error.strip():
        return None
    if state != 'completed' or not joined:
        raise ProcessObservationError('unavailable')
    match = re.fullmatch(rb'\s*(\d+)\s+(\d+)\s+(\d+)\s+([A-Z][a-z]{2} [A-Z][a-z]{2} [ 0-9][0-9] [0-9:]{8} [0-9]{4})\s+([^\r\n]+)\s*', raw)
    if not match or int(match[1]) != pid:
        raise ProcessObservationError('invalid')
    try:
        return {'pid': pid, 'ppid': int(match[2]), 'uid': int(match[3]),
                'start': match[4].decode(), 'command': match[5].decode().strip()}
    except UnicodeDecodeError as error:
        raise ProcessObservationError('invalid') from error


def identity(info):
    return {key: info[key] for key in ('pid', 'uid', 'start')} if info is not None else None


def same_process(expected):
    return identity(process_info(expected['pid'])) == expected


def terminal_ancestry(pid):
    visited = set()
    for _ in range(12):
        if pid <= 1 or pid in visited:
            return False
        visited.add(pid)
        info = process_info(pid)
        if info is None or info['uid'] != os.getuid():
            return False
        if info['command'] == TERMINAL_EXECUTABLE:
            return True
        pid = info['ppid']
    return False


# These observers use exactly the existing short-circuit predicates. Classes
# explain a failed gate; they never authorize a path that failed that gate.
COMMAND_CLASSES = {'terminal_exact', 'terminal_other_path', 'login', 'shell', 'other', 'unknown'}
GATE_REASONS = {'verified', 'identity_absent', 'identity_mismatch', 'identity_unavailable',
    'identity_invalid', 'end_reached', 'cycle', 'ancestor_absent', 'other_uid',
    'depth_limit', 'ancestor_unavailable', 'ancestor_invalid'}


def command_class(command):
    if command == TERMINAL_EXECUTABLE:
        return 'terminal_exact'
    if Path(command).name == 'Terminal':
        return 'terminal_other_path'
    if command == '/usr/bin/login':
        return 'login'
    if command in {'/bin/sh', '/bin/bash', '/bin/zsh'}:
        return 'shell'
    return 'other'


def gate_observation(expected):
    comparison = {'state': 'unobserved', 'pidMatched': False, 'uidMatched': False,
                  'startMatched': False, 'matched': False}
    result = {'verified': False, 'reason': 'identity_unavailable',
              'identity': comparison, 'hopCount': 0, 'hops': []}
    try:
        current = process_info(expected['pid'])
    except ProcessObservationError as error:
        comparison['state'] = error.kind
        result['reason'] = 'identity_' + error.kind
        return result
    if current is None:
        comparison['state'], result['reason'] = 'absent', 'identity_absent'
        return result
    comparison.update(state='observed', pidMatched=current['pid'] == expected['pid'],
        uidMatched=current['uid'] == expected['uid'], startMatched=current['start'] == expected['start'],
        matched=identity(current) == expected)
    if not comparison['matched']:
        result['reason'] = 'identity_mismatch'
        return result
    pid, visited = expected['pid'], set()
    for _ in range(12):
        if pid <= 1:
            result['reason'] = 'end_reached'
            return result
        if pid in visited:
            result['reason'] = 'cycle'
            return result
        visited.add(pid)
        row = {'uidRelation': 'unknown', 'commandClass': 'unknown'}
        result['hops'].append(row)
        result['hopCount'] = len(result['hops'])
        try:
            info = process_info(pid)
        except ProcessObservationError as error:
            result['reason'] = 'ancestor_' + error.kind
            return result
        if info is None:
            result['reason'] = 'ancestor_absent'
            return result
        row.update(uidRelation='same' if info['uid'] == os.getuid() else 'other',
                   commandClass=command_class(info['command']))
        if info['uid'] != os.getuid():
            result['reason'] = 'other_uid'
            return result
        if info['command'] == TERMINAL_EXECUTABLE:
            result.update(verified=True, reason='verified')
            return result
        pid = info['ppid']
    result['reason'] = 'depth_limit'
    return result


def validate_observation(value):
    if (type(value) is not dict or set(value) != {'verified', 'reason', 'identity', 'hopCount', 'hops'}
            or type(value['verified']) is not bool or value['reason'] not in GATE_REASONS
            or value['verified'] != (value['reason'] == 'verified')
            or type(value['hopCount']) is not int or not 0 <= value['hopCount'] <= 12
            or type(value['hops']) is not list or len(value['hops']) != value['hopCount']):
        raise ValueError('invalid closed gate observation')
    comparison = value['identity']
    flags = {'pidMatched', 'uidMatched', 'startMatched', 'matched'}
    if (type(comparison) is not dict or set(comparison) != flags | {'state'}
            or comparison['state'] not in {'observed', 'absent', 'unavailable', 'invalid'}
            or any(type(comparison[key]) is not bool for key in flags)
            or comparison['matched'] != all(comparison[key] for key in flags - {'matched'})
            or comparison['state'] != 'observed' and any(comparison[key] for key in flags)
            or value['hops'] and not comparison['matched']):
        raise ValueError('invalid closed identity observation')
    for row in value['hops']:
        if (type(row) is not dict or set(row) != {'uidRelation', 'commandClass'}
                or row['uidRelation'] not in {'same', 'other', 'unknown'}
                or row['commandClass'] not in COMMAND_CLASSES):
            raise ValueError('invalid closed ancestry hop')
    reason, hops = value['reason'], value['hops']
    if reason.startswith('identity_'):
        expected_state = 'observed' if reason == 'identity_mismatch' else reason.removeprefix('identity_')
        if hops or comparison['matched'] or comparison['state'] != expected_state:
            raise ValueError('inconsistent identity failure')
    elif not comparison['matched'] or comparison['state'] != 'observed':
        raise ValueError('ancestry lacks matching identity')
    if any(row != {'uidRelation': 'same', 'commandClass': row['commandClass']}
            or row['commandClass'] in {'terminal_exact', 'unknown'} for row in hops[:-1]):
        raise ValueError('ancestry continued after stop')
    last = hops[-1] if hops else None
    if (value['verified'] and last != {'uidRelation': 'same', 'commandClass': 'terminal_exact'}
            or reason == 'other_uid' and (not last or last['uidRelation'] != 'other' or last['commandClass'] == 'unknown')
            or reason.startswith('ancestor_') and last != {'uidRelation': 'unknown', 'commandClass': 'unknown'}
            or reason in {'end_reached', 'cycle', 'depth_limit'} and last is not None
                and (last['uidRelation'] != 'same' or last['commandClass'] in {'terminal_exact', 'unknown'})
            or reason == 'depth_limit' and len(hops) != 12):
        raise ValueError('inconsistent ancestry stop reason')
    return value


ORIGIN_REASONS = {'terminal_path', 'end_reached', 'cycle', 'depth_limit', 'deadline',
    'owner_mismatch', 'process_absent', 'process_unavailable', 'process_invalid',
    'path_denied', 'path_unavailable', 'path_invalid', 'snapshot_changed',
    'worker_incomplete', 'cleanup_unresolved', 'gate_incomplete'}
PATH_CLASSES = {'terminal_exact', 'login', 'shell', 'other', 'unknown'}


def empty_origin(reason='worker_incomplete'):
    return {'reason': reason, 'hopCount': 0, 'hops': [], 'authorizesNative': False,
            'terminalPathObserved': False, 'workerJoined': False, 'queriesJoined': False}


def kernel_path(pid):
    # Observational only. Apple maps this read-only API to the executable vnode;
    # process security policy still applies. No fallback after a failed query.
    # https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/libsyscall/wrappers/libproc/libproc.c
    # https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/kern/proc_info.c
    import ctypes
    import errno
    try:
        library = ctypes.CDLL('/usr/lib/libproc.dylib', use_errno=True)
        read = library.proc_pidpath
        read.argtypes = (ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32)
        read.restype = ctypes.c_int
        buffer = ctypes.create_string_buffer(4096)
        ctypes.set_errno(0)
        count = read(pid, buffer, len(buffer))
        if count <= 0:
            return ('denied' if ctypes.get_errno() in (errno.EACCES, errno.EPERM) else 'unavailable'), None
        if count >= len(buffer) or buffer.raw[count] != 0 or b'\0' in buffer.raw[:count]:
            return 'invalid', None
        value = buffer.raw[:count].decode('utf-8')
        if not value.startswith('/') or '\n' in value or '\r' in value:
            return 'invalid', None
        return 'observed', value
    except (OSError, AttributeError):
        return 'unavailable', None
    except UnicodeDecodeError:
        return 'invalid', None


def explain_chain(expected):
    """Separate bounded explanation; never changes the authorization gate.

    No new ps query starts after six seconds. An already-started query has at
    most 2s capture + 5s wait + two 2s reader joins before the 20s owner watchdog.
    Path calls occur between joined ps captures. A missing receipt or hard
    timeout never establishes descendant cleanup or explanatory completion.
    """
    result, visited, deadline = empty_origin(), set(), time.monotonic() + 6
    result['queriesJoined'] = True
    def cleaned(joined):
        result['queriesJoined'] = result['queriesJoined'] and joined
    def snapshot(pid):
        if time.monotonic() >= deadline:
            raise TimeoutError('observation deadline')
        value = process_info(pid, on_cleanup=cleaned)
        if not result['queriesJoined']:
            raise ProcessObservationError('unavailable')
        return value
    try:
        current = snapshot(expected['pid'])
        if identity(current) != expected or expected['uid'] != os.getuid():
            result['reason'] = 'owner_mismatch'
            return result
        pid = expected['pid']
        for _ in range(12):
            if pid <= 1 or pid in visited:
                result['reason'] = 'end_reached' if pid <= 1 else 'cycle'
                return result
            visited.add(pid)
            before = snapshot(pid)
            if before is None:
                result['reason'] = 'process_absent'
                return result
            if len(visited) == 1 and identity(before) != expected:
                result['reason'] = 'owner_mismatch'
                return result
            row = {'uidRelation': 'same' if before['uid'] == os.getuid() else 'other',
                'effectiveUidClass': 'zero' if before['uid'] == 0 else 'nonzero',
                'commandClass': command_class(before['command']), 'kernelPathClass': 'unknown',
                'pathStatus': 'unavailable', 'commandMatchesKernelPath': False, 'snapshotStable': False}
            result['hops'].append(row)
            result['hopCount'] = len(result['hops'])
            if time.monotonic() >= deadline:
                raise TimeoutError('observation deadline')
            status, path = kernel_path(pid)
            row['pathStatus'] = status
            if status != 'observed':
                result['reason'] = 'path_' + status
                return result
            row['kernelPathClass'] = command_class(path)
            if row['kernelPathClass'] == 'terminal_other_path':
                row['kernelPathClass'] = 'other'
            row['commandMatchesKernelPath'] = before['command'] == path
            after = snapshot(pid)
            if before != after:
                result['reason'] = 'snapshot_changed'
                return result
            row['snapshotStable'] = True
            if path == TERMINAL_EXECUTABLE:
                result.update(reason='terminal_path', terminalPathObserved=True)
                return result
            pid = before['ppid']
        result['reason'] = 'depth_limit'
    except TimeoutError:
        result['reason'] = 'deadline'
    except ProcessObservationError as error:
        result['reason'] = 'process_' + error.kind
    finally:
        if not result['queriesJoined']:
            result['reason'] = 'cleanup_unresolved'
    return result


def validate_origin(value):
    if (type(value) is not dict or set(value) != set(empty_origin())
            or value['reason'] not in ORIGIN_REASONS or value['authorizesNative'] is not False
            or any(type(value[key]) is not bool for key in ('terminalPathObserved', 'workerJoined', 'queriesJoined'))
            or type(value['hopCount']) is not int or not 0 <= value['hopCount'] <= 12
            or type(value['hops']) is not list or len(value['hops']) != value['hopCount']
            or value['terminalPathObserved'] != (value['reason'] == 'terminal_path')):
        raise ValueError('invalid closed origin explanation')
    keys = {'uidRelation', 'effectiveUidClass', 'commandClass', 'kernelPathClass',
            'pathStatus', 'commandMatchesKernelPath', 'snapshotStable'}
    for row in value['hops']:
        if (type(row) is not dict or set(row) != keys
                or row['uidRelation'] not in {'same', 'other'}
                or row['effectiveUidClass'] not in {'zero', 'nonzero'}
                or row['commandClass'] not in COMMAND_CLASSES - {'unknown'}
                or row['kernelPathClass'] not in PATH_CLASSES
                or row['pathStatus'] not in {'observed', 'denied', 'unavailable', 'invalid'}
                or type(row['commandMatchesKernelPath']) is not bool or type(row['snapshotStable']) is not bool
                or (row['pathStatus'] == 'observed') != (row['kernelPathClass'] != 'unknown')
                or row['pathStatus'] != 'observed' and (row['commandMatchesKernelPath'] or row['snapshotStable'])):
            raise ValueError('invalid closed origin hop')
    if any(not row['snapshotStable'] or row['kernelPathClass'] == 'terminal_exact' for row in value['hops'][:-1]):
        raise ValueError('origin continued after stop')
    if value['terminalPathObserved'] and (not value['queriesJoined'] or not value['hops']
            or not value['hops'][-1]['snapshotStable'] or value['hops'][-1]['kernelPathClass'] != 'terminal_exact'):
        raise ValueError('origin lacks stable terminal observation')
    last = value['hops'][-1] if value['hops'] else None
    if (value['reason'].startswith('path_') and (last is None or last['pathStatus'] != value['reason'][5:])
            or value['reason'] == 'snapshot_changed' and (last is None or last['pathStatus'] != 'observed' or last['snapshotStable'])
            or value['reason'] == 'depth_limit' and (len(value['hops']) != 12 or not last['snapshotStable'])
            or value['reason'] == 'cleanup_unresolved' and value['queriesJoined']
            or value['reason'] == 'worker_incomplete' and (value['queriesJoined'] or value['hops'])):
        raise ValueError('inconsistent origin stop reason')
    return value


def observe_child(directory):
    try:
        info = directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
            return 1
        plan, marker = read_private(directory / 'plan.json'), read_private(directory / 'marker.json')
        owner = started_owner(directory, plan)
        if (plan['markerOnly'] is not True or marker['owner'] != owner or marker['nonce'] != plan['nonce']
                or marker['markerVerified'] is not True or marker['markerOwnerJoined'] is not True
                or time.monotonic() >= plan['launchDeadline'] or (directory / 'cancelled').exists()
                or digest(SCRIPT) != plan['scriptSha256']):
            return 1
        print(json.dumps(validate_origin(explain_chain(owner)), sort_keys=True))
        return 0
    except Exception:
        return 1


def origin_observation(directory, deadline):
    if deadline - time.monotonic() < 21:
        return empty_origin('deadline')
    state, code, out, err, joined = capture_owned(
        [sys.executable, '-I', '-B', str(SCRIPT), '--observe', str(directory)], timeout=20, limit=8192)
    fallback = empty_origin()
    fallback['workerJoined'] = joined
    if state != 'completed' or code != 0 or err or not joined:
        return fallback
    def unique_object(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError('duplicate observation field')
            value[key] = item
        return value
    try:
        result = validate_origin(json.loads(out, object_pairs_hook=unique_object))
        if result['workerJoined']:
            raise ValueError('worker cannot assert owner wait')
        result['workerJoined'] = joined
        return result
    except (ValueError, UnicodeDecodeError):
        return fallback


def availability():
    if sys.platform != 'darwin' or os.getuid() == 0 or os.stat('/dev/console').st_uid != os.getuid():
        return False
    with (TERMINAL / 'Contents/Info.plist').open('rb') as source:
        info = plistlib.load(source)
    if (info.get('CFBundleIdentifier') != 'com.apple.Terminal'
            or info.get('CFBundleExecutable') != 'Terminal' or not Path(TERMINAL_EXECUTABLE).is_file()):
        return False
    state, _, _, _, joined = capture_owned(['/bin/launchctl', 'print', 'gui/' + str(os.getuid())],
                                          timeout=5, limit=1024 * 1024)
    return state == 'completed' and joined


def native_command(binary):
    return [str(binary), '-test.v=test2json', '-test.run=' + SELECTOR, '-test.count=1', '-test.timeout=60s']


def expired(_signal, _frame):
    raise TimeoutError('supervisor deadline')


def child(directory):
    """Private Terminal-owned supervisor; no GUI interactions or alternate path."""
    signal.signal(signal.SIGALRM, expired)
    signal.setitimer(signal.ITIMER_REAL, TOTAL_SECONDS)
    os.umask(0o077)
    try:
        info = directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
            return 1
        plan = read_private(directory / 'plan.json')
        keys = {'nonce', 'parent', 'deadline', 'launchDeadline', 'scriptSha256', 'binarySha256', 'env', 'markerOnly'}
        if (type(plan) is not dict or set(plan) != keys
                or not re.fullmatch('[0-9a-f]{64}', plan['nonce'])
                or type(plan['markerOnly']) is not bool
                or type(plan['env']) is not dict
                or plan['markerOnly'] and (plan['env'] or plan['binarySha256'] is not None)
                or not plan['markerOnly'] and (set(plan['env']) != ENV_KEYS
                    or any(type(value) is not str for value in plan['env'].values())
                    or any(plan['env'][key] != '1' for key in OPT_INS)
                    or plan['env']['GOMAXPROCS'] != '2'
                    or any(plan['env'][key] != str(directory.parent) for key in ('TMPDIR', 'TMP', 'TEMP')))
                or not time.monotonic() < plan['launchDeadline'] <= plan['deadline'] <= time.monotonic() + TOTAL_SECONDS
                or digest(SCRIPT) != plan['scriptSha256'] or not same_process(plan['parent'])):
            return 1
        signal.setitimer(signal.ITIMER_REAL, plan['deadline'] - time.monotonic())
        owner = identity(process_info(os.getpid()))
        write_private(directory / 'started.json', {'nonce': plan['nonce'], 'owner': owner})
        state, code, out, err, joined = capture_owned(MARKER_COMMAND, timeout=5, limit=1024)
        marker_ok = state == 'completed' and code == 0 and out == MARKER and not err and joined
        write_private(directory / 'marker.json', {'nonce': plan['nonce'], 'owner': owner,
            'markerVerified': marker_ok, 'markerOwnerJoined': joined})
        if not marker_ok:
            return 1
        if plan['markerOnly']:
            acknowledgement = directory / 'marker-ack.json'
            while not acknowledgement.exists():
                if (time.monotonic() >= plan['launchDeadline'] or (directory / 'cancelled').exists()
                        or not same_process(plan['parent'])):
                    return 1
                time.sleep(0.1)
            # This acknowledgement permits only exit, never native execution.
            return 0 if read_private(acknowledgement) == {'nonce': plan['nonce'], 'owner': owner} else 1
        permit = directory / 'permit.json'
        while not permit.exists():
            if (time.monotonic() >= plan['launchDeadline'] or (directory / 'cancelled').exists()
                    or not same_process(plan['parent'])):
                return 1
            time.sleep(0.1)
        grant = read_private(permit)
        binary = directory.parent / 'dnssd.test'
        if (grant != {'nonce': plan['nonce'], 'owner': owner}
                or time.monotonic() >= plan['launchDeadline'] or (directory / 'cancelled').exists()
                or not same_process(plan['parent']) or digest(SCRIPT) != plan['scriptSha256']
                or digest(binary) != plan['binarySha256']):
            return 1
        state, code, out, err, joined = capture_owned(native_command(binary), env=plan['env'],
            timeout=min(RUNTIME_SECONDS, plan['deadline'] - time.monotonic()), stop_on_denial=True,
            cancelled=lambda: (directory / 'cancelled').exists() or not same_process(plan['parent']))
        # Raw output never leaves the private directory. No environment, PID,
        # process names, addresses, error strings or paths enter public JSON.
        for name, data in (('stdout', out), ('stderr', err)):
            fd = os.open(directory / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            with os.fdopen(fd, 'wb') as target:
                target.write(data)
        unchanged = digest(SCRIPT) == plan['scriptSha256'] and digest(binary) == plan['binarySha256']
        write_private(directory / 'result.json', {'nonce': plan['nonce'], 'owner': owner,
            'state': state, 'code': code, 'joined': joined, 'sourceUnchanged': unchanged})
        return 0 if joined and unchanged else 1
    except Exception:
        return 1
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)


def started_owner(directory, plan):
    value = read_private(directory / 'started.json')
    if (type(value) is not dict or set(value) != {'nonce', 'owner'} or value['nonce'] != plan['nonce']
            or type(value['owner']) is not dict or set(value['owner']) != {'pid', 'uid', 'start'}
            or type(value['owner']['pid']) is not int or value['owner']['pid'] <= 1
            or value['owner']['uid'] != os.getuid() or type(value['owner']['start']) is not str):
        raise ValueError('invalid supervisor startup')
    return value['owner']


def capture(binary, *, env, private, marker_only=False):
    """Launch once and accept actual child evidence, never open's status alone."""
    result, state, code, out, err = empty_receipt(marker_only=marker_only), 'not_run', None, b'', b''
    directory, owner, owned_directory = private / 'terminal-launch', None, False
    try:
        if type(marker_only) is not bool or (binary is not None if marker_only else binary != private / 'dnssd.test'):
            raise ValueError('unexpected diagnostic binary')
        try:
            available = availability()
        except (OSError, ValueError, plistlib.InvalidFileException):
            available = False
        if not available:
            result.update(status='unavailable', reason='availability_unavailable')
            return state, code, out, err, receipt(result)
        result['availabilityVerified'] = True
        directory.mkdir(mode=0o700)
        owned_directory = True
        started = time.monotonic()
        plan = {'nonce': secrets.token_hex(32), 'parent': identity(process_info(os.getpid())),
            'deadline': started + TOTAL_SECONDS, 'launchDeadline': started + LAUNCH_SECONDS,
            'scriptSha256': digest(SCRIPT), 'binarySha256': None if marker_only else digest(binary),
            'env': {} if marker_only else {key: env[key] for key in ENV_KEYS}, 'markerOnly': marker_only}
        write_private(directory / 'plan.json', plan)
        command = directory / 'diagnostic.command'
        # All variable values are quoted literal path arguments. No user or
        # environment text is evaluated, sourced, or printed by this script.
        script = '#!/bin/sh\nexec ' + ' '.join(shlex.quote(part) for part in
            (sys.executable, '-I', '-B', str(SCRIPT), '--child', str(directory))) + ' </dev/null >/dev/null 2>&1\n'
        fd = os.open(command, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o700)
        with os.fdopen(fd, 'w') as target:
            target.write(script)
        launch, _, _, _, launch_joined = capture_owned(['/usr/bin/open', '-a', str(TERMINAL), str(command)], timeout=10)
        if launch != 'completed' or not launch_joined:
            result.update(status='failed', reason='open_failed')
        else:
            result['openCompleted'] = True
            marker = directory / 'marker.json'
            while not marker.exists() and time.monotonic() < plan['launchDeadline']:
                if owner is None and (directory / 'started.json').exists():
                    owner = started_owner(directory, plan)
                time.sleep(0.05)
            if not marker.exists():
                result.update(status='unavailable', reason='marker_timeout')
            else:
                value = read_private(marker)
                if (type(value) is not dict or set(value) != {'nonce', 'owner', 'markerVerified', 'markerOwnerJoined'}
                        or value['nonce'] != plan['nonce'] or type(value['owner']) is not dict
                        or set(value['owner']) != {'pid', 'uid', 'start'}
                        or value['owner']['uid'] != os.getuid()
                        or value['markerVerified'] is not True or value['markerOwnerJoined'] is not True):
                    result.update(status='failed', reason='marker_invalid')
                else:
                    if owner is not None and owner != value['owner']:
                        raise ValueError('marker owner changed')
                    owner = value['owner']
                    result.update(markerVerified=True, markerOwnerJoined=True)
                    result['gateObservation'] = validate_observation(gate_observation(owner))
                    if not result['gateObservation']['verified']:
                        result.update(status='failed', reason='ancestry_unverified')
                    else:
                        result['terminalAncestorObserved'] = True
                    if marker_only:
                        # Explain this known UID boundary (or a verified chain),
                        # never retry a failed identity/process query via another API.
                        result['originObservation'] = (origin_observation(directory, plan['launchDeadline'])
                            if result['gateObservation']['reason'] in {'other_uid', 'verified'}
                            else empty_origin('gate_incomplete'))
                        write_private(directory / 'marker-ack.json', {'nonce': plan['nonce'], 'owner': owner})
                        while time.monotonic() < plan['deadline'] and same_process(owner):
                            time.sleep(0.1)
                        result['supervisorTerminationObserved'] = not same_process(owner)
                        if not result['supervisorTerminationObserved']:
                            result.update(status='failed', reason='supervisor_timeout')
                        elif result['terminalAncestorObserved']:
                            result.update(status='observed', reason='observed')
                            state, code = 'completed', 0
                    elif result['terminalAncestorObserved']:
                        if time.monotonic() >= plan['launchDeadline']:
                            raise TimeoutError('launch gate expired')
                        write_private(directory / 'permit.json', {'nonce': plan['nonce'], 'owner': owner})
                        final = directory / 'result.json'
                        while time.monotonic() < plan['deadline'] and same_process(owner):
                            time.sleep(0.1)
                        result['supervisorTerminationObserved'] = not same_process(owner)
                        if not result['supervisorTerminationObserved']:
                            result.update(status='failed', reason='supervisor_timeout')
                        elif not final.exists():
                            result.update(status='failed', reason='runtime_incomplete')
                        else:
                            value = read_private(final)
                            if (type(value) is not dict or set(value) != {'nonce', 'owner', 'state', 'code', 'joined', 'sourceUnchanged'}
                                    or value['nonce'] != plan['nonce'] or value['owner'] != owner
                                    or value['state'] not in STATES - {'not_run'}
                                    or not (type(value['code']) is int and -127 <= value['code'] <= 255
                                        or value['state'] == 'launch_failed' and value['code'] is None and value['joined'] is False)
                                    or type(value['joined']) is not bool
                                    or type(value['sourceUnchanged']) is not bool):
                                raise ValueError('invalid private runtime result')
                            state, code = value['state'], value['code']
                            result.update(nativeStartObserved=code is not None, nativeOwnerJoined=value['joined'],
                                nativeProcessStatus=state, nativeExitCode=code, policyDeniedObserved=state == 'policy_denied')
                            for name in ('stdout', 'stderr'):
                                path = directory / name
                                info = path.lstat()
                                if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_uid != os.getuid()
                                        or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size > RUNTIME_BYTES):
                                    raise ValueError('invalid private runtime output')
                            out, err = (directory / 'stdout').read_bytes(), (directory / 'stderr').read_bytes()
                            if not value['sourceUnchanged'] or digest(SCRIPT) != plan['scriptSha256'] or digest(binary) != plan['binarySha256']:
                                result.update(status='failed', reason='source_changed')
                            elif not value['joined'] or state not in {'completed', 'nonzero'}:
                                result.update(status='failed', reason='policy_denied' if state == 'policy_denied' else 'runtime_incomplete')
                            else:
                                result.update(status='observed', reason='observed')
    except Exception:
        result.update(status='failed', reason='private_state_invalid')
    finally:
        if owned_directory:
            # A late LaunchServices delivery can only observe a revoked gate.
            # No process learned from external metadata is ever signalled.
            try:
                write_private(directory / 'cancelled', {'cancelled': True})
            except (OSError, ValueError):
                pass
            if 'plan' in locals() and not result['supervisorTerminationObserved']:
                try:
                    # A missing marker is not proof that LaunchServices never
                    # started the supervisor. Revoke its gate and retain the
                    # private directory for its full independent lifetime.
                    while time.monotonic() < plan['deadline'] + 10:
                        if owner is None and (directory / 'started.json').exists():
                            owner = started_owner(directory, plan)
                        if owner is not None and not same_process(owner):
                            result['supervisorTerminationObserved'] = True
                            break
                        time.sleep(0.1)
                except Exception:
                    pass
    if result['status'] != 'observed':
        state, code, out, err = ('policy_denied' if result['policyDeniedObserved'] else 'capture_failed'), None, b'', b''
    return state, code, out, err, receipt(result)


if __name__ == '__main__':
    entry = {'--child': child, '--observe': observe_child}
    raise SystemExit(entry[sys.argv[1]](Path(sys.argv[2]))
        if len(sys.argv) == 3 and sys.argv[1] in entry and sys.platform == 'darwin' else 1)
