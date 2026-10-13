#!/usr/bin/env python3
"""One exact native nearby capability; publish only closed, generic receipts."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
PACKAGE = 'github.com/webkaz-labs/sobalink/internal/nearby'
TEST = 'TestNativeLoopbackDiscoveryCapability'
GO_VERSION = 'go1.27.1'
MODULES = ('golang.org/x/net@v0.59.0', 'golang.org/x/sys@v0.48.0')
PLATFORMS = {
    'linux-amd64': ('linux', 'amd64', 'ubuntu-24.04', 'Linux', 'X64'),
    'linux-arm64': ('linux', 'arm64', 'ubuntu-24.04-arm', 'Linux', 'ARM64'),
    'darwin-arm64': ('darwin', 'arm64', 'macos-26', 'macOS', 'ARM64'),
    'windows-amd64': ('windows', 'amd64', 'windows-2025', 'Windows', 'X64'),
}
# This is the fixed evidence routing plan, not an assertion about another job's
# result. Linux x64 already runs this exact test in the one Core/browser job.
EVIDENCE_JOBS = {key: ('diagnostic-nearby' if key == 'linux-amd64' else 'diagnostic-nearby-native')
                 for key in PLATFORMS}
CAPTURE_BYTES = 1024 * 1024
RUNTIME_BYTES = 64 * 1024
STAGES = {'preflight', 'dependencies', 'compile', 'runtime', 'verify-source', 'passed', 'runtime-failed'}
PROCESS_STATES = {'not_run', 'completed', 'nonzero', 'timeout', 'output_limit', 'launch_failed', 'capture_failed'}


def module(name):
    path = Path(__file__).with_name(name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


def capture(command, *, cwd=ROOT, env=None, timeout=30, limit=CAPTURE_BYTES, stdin=None):
    """Bound both streams in memory; never forward tool/test text or exceptions.

    The runtime child is the socket-owning test binary itself, not a wrapper.
    Its own 20s test deadline precedes this independent process deadline.
    """
    streams = [bytearray(), bytearray()]
    overflow, broken = threading.Event(), threading.Event()
    try:
        process = subprocess.Popen(command, cwd=cwd, env=env, shell=False,
                                   stdin=subprocess.PIPE if stdin is not None else subprocess.DEVNULL,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    except OSError:
        return 'launch_failed', None, b'', b''

    def drain(source, target):
        try:
            while True:
                data = source.read(8192)
                if not data:
                    break
                room = limit - len(target)
                target.extend(data[:max(room, 0)])
                if len(data) > room:
                    overflow.set()
        except (OSError, ValueError):
            broken.set()
        finally:
            source.close()

    readers = [threading.Thread(target=drain, args=(source, target), daemon=True)
               for source, target in zip((process.stdout, process.stderr), streams)]
    for reader in readers:
        reader.start()

    def feed():
        try:
            process.stdin.write(stdin)
            process.stdin.close()
        except (OSError, ValueError):
            broken.set()

    writer = None
    if stdin is not None:
        if len(stdin) > limit:
            overflow.set()
            process.stdin.close()
        else:
            writer = threading.Thread(target=feed, daemon=True)
            writer.start()
    deadline = time.monotonic() + timeout
    state = None
    while process.poll() is None:
        if overflow.is_set() or time.monotonic() >= deadline:
            state = 'output_limit' if overflow.is_set() else 'timeout'
            process.kill()
            break
        time.sleep(0.01)
    try:
        returncode = process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        returncode, state = None, 'capture_failed'
    for thread in readers + ([writer] if writer else []):
        thread.join(timeout=2)
    if any(thread.is_alive() for thread in readers + ([writer] if writer else [])) or broken.is_set():
        state = state or 'capture_failed'
    if overflow.is_set():
        state = 'output_limit'
    return state or ('completed' if returncode == 0 else 'nonzero'), returncode, bytes(streams[0]), bytes(streams[1])


def checked(command, *, env=None, timeout=30):
    state, _, stdout, _ = capture(command, env=env, timeout=timeout)
    if state != 'completed':
        raise ValueError('private command did not complete')
    return stdout


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def source_inventory():
    if checked(['git', 'status', '--porcelain', '--untracked-files=no']).strip():
        raise ValueError('modified tracked source')
    # No prepared engine/frontend inputs are needed or excluded in this job.
    if checked(['git', 'ls-files', '--others', '-z']):
        raise ValueError('untracked source input')
    raw = checked(['git', 'ls-files', '-z'])
    if not raw or not raw.endswith(b'\0'):
        raise ValueError('invalid source inventory')
    names = [value.decode('utf-8') for value in raw[:-1].split(b'\0')]
    if len(names) != len(set(names)):
        raise ValueError('duplicate source input')
    for name in names:
        if any(part in ('', '.', '..') for part in name.split('/')) or '\\' in name or (ROOT / name).is_symlink():
            raise ValueError('invalid source input')
    return {name: digest(ROOT / name) for name in names}


def identities(key, versions, *, host_platform=None, machine=None):
    if key not in PLATFORMS:
        raise ValueError('unknown native platform')
    goos, goarch, runner, runner_os, runner_arch = PLATFORMS[key]
    expected = {'GOVERSION': GO_VERSION, 'GOHOSTOS': goos, 'GOHOSTARCH': goarch,
                'GOOS': goos, 'GOARCH': goarch, 'CGO_ENABLED': '1'}
    local_os = {'linux': 'linux', 'darwin': 'darwin', 'win32': 'windows'}.get(host_platform or sys.platform)
    local_arch = {'x86_64': 'amd64', 'amd64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get((machine or platform.machine()).lower())
    if (versions != expected or local_os != goos or local_arch != goarch
            or os.environ.get('RUNNER_OS') != runner_os or os.environ.get('RUNNER_ARCH') != runner_arch):
        raise ValueError('native toolchain or runner mismatch')
    return {'go': GO_VERSION, 'goos': goos, 'goarch': goarch, 'runner': runner, 'race': True}


def native_events(raw, returncode):
    """Keep a validated failure marker, but accept only the shared success policy."""
    shared, parser = module('ci-nearby-diagnostic'), module('ci-go-test')
    if type(raw) is not bytes or len(raw) > 2 * RUNTIME_BYTES or type(returncode) is not int:
        raise ValueError('invalid native capture')
    markers, lifecycle = [], []
    for line in raw.splitlines():
        event = parser.parse_event(line)
        if event.get('Package') != PACKAGE or event.get('Test') not in (None, TEST):
            raise ValueError('unexpected native test')
        action = event['Action']
        if action != 'output':
            lifecycle.append((bool(event.get('Test')), action))
        if shared.MARKER in event.get('Output', ''):
            if event.get('Test') != TEST or event['Output'].count(shared.MARKER) != 1:
                raise ValueError('invalid native marker owner')
            markers.append(shared.capability(shared.parse_json(event['Output'].split(shared.MARKER, 1)[1].strip())))
    if len(markers) != 1:
        raise ValueError('exact native marker required')
    evidence = markers[0]
    terminal = {'verified': 'pass', 'unavailable': 'skip', 'failed': 'fail'}.get(evidence['status'])
    expected = [(False, 'start'), (True, 'run'), (True, terminal),
                (False, 'fail' if terminal == 'fail' else 'pass')]
    if terminal is None or lifecycle != expected or evidence['status'] == 'failed' or returncode != 0:
        # Preserve the observation, including its original runtime/cleanup stage,
        # but never turn an incomplete or contradictory lifecycle into proof.
        return evidence, False
    # Nonzero exits, skipped verification and success require the same closed
    # policy as the Ubuntu Core/browser lane. No partial receipt can turn green.
    shared.test_events(raw, 'discovery', returncode)
    return evidence, True


def empty_result():
    return {'schema': 1, 'diagnosticPassed': False, 'nativeDiscoveryVerified': False,
            'nativeDiscovery': None, 'processStatus': 'not_run', 'eventsAccepted': False,
            'scope': 'isolated-loopback-native-nearby-only', 'physicalLANVerified': False,
            'automaticCoreBrowserVerified': False, 'releaseValidation': False}


def main(argv=None):
    if (sys.argv[1:] if argv is None else argv) or os.environ.get('GITHUB_ACTIONS') != 'true':
        print('FAIL: fixed hosted native diagnostic entry required.')
        return 1
    report, provenance = empty_result(), {'schema': 1, 'stage': 'preflight'}
    export_ready, private = False, None
    try:
        os.umask(0o077)
        temporary = Path(os.environ['RUNNER_TEMP'])
        export = temporary / 'soba-nearby-native-sanitized'
        export.mkdir(mode=0o700, exist_ok=False)
        export_ready = True
        private = Path(tempfile.mkdtemp(prefix='soba-nearby-native-', dir=temporary))
        shared, selector = module('ci-nearby-diagnostic'), module('ci-diagnostic')
        commit = checked(['git', 'rev-parse', 'HEAD']).decode().strip()
        tree = checked(['git', 'rev-parse', 'HEAD^{tree}']).decode().strip()
        event_path = Path(os.environ['GITHUB_EVENT_PATH'])
        if event_path.stat().st_size > CAPTURE_BYTES:
            raise ValueError('event size exceeded')
        event = shared.parse_json(event_path.read_text(encoding='utf-8'))
        if (selector.select(os.environ['GITHUB_EVENT_NAME'], event, os.environ['GITHUB_REPOSITORY'],
                            os.environ['GITHUB_REF'], os.environ['GITHUB_SHA'], commit) != 'nearby'
                or not re.fullmatch('[0-9a-f]{40}', tree)):
            raise ValueError('wrong exact source or suite')
        before = source_inventory()
        key = os.environ.get('SOBALINK_NEARBY_NATIVE_PLATFORM')
        versions = shared.parse_json(checked(['go', 'env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH', 'GOOS', 'GOARCH', 'CGO_ENABLED']))
        identity = identities(key, versions)
        tool = Path(shutil.which('go'))
        policy_files = ('.github/workflows/activation-diagnostic.yml', '.github/scripts/ci-nearby-native.py',
                        '.github/scripts/test_ci_nearby_native.py', '.github/scripts/ci-nearby-diagnostic.py',
                        '.github/scripts/ci-go-test.py', '.github/scripts/ci-diagnostic.py',
                        'internal/nearby/native_acceptance_test.go', 'go.mod', 'go.sum')
        provenance.update(commit=commit, tree=tree, platform=key, toolchain=identity,
                          sourceSha256=hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest(),
                          policySha256={name: before[name] for name in policy_files},
                          goSha256=digest(tool), test={'package': PACKAGE, 'name': TEST},
                          evidenceJobs=dict(EVIDENCE_JOBS), modules=list(MODULES))
        env = dict(os.environ, GOTOOLCHAIN='local', GOWORK='off', GOTELEMETRY='off',
                   GOFLAGS='-mod=readonly -buildvcs=false', CGO_ENABLED='1',
                   TMPDIR=str(private), TMP=str(private), TEMP=str(private))
        provenance['stage'] = 'dependencies'
        checked(['go', 'mod', 'download', *MODULES], env=env, timeout=120)
        # Download verifies the existing go.sum checksums. Compilation is offline
        # and readonly; never prepare the unrelated engine replacement modules.
        env.update(GOPROXY='off', GOSUMDB='off')
        if before != source_inventory():
            raise ValueError('dependency preparation changed source')
        provenance['stage'] = 'compile'
        binary = private / ('nearby.test.exe' if key == 'windows-amd64' else 'nearby.test')
        checked(['go', 'test', '-race', '-c', '-o', str(binary), './internal/nearby'], env=env, timeout=300)
        provenance['testSha256'] = digest(binary)
        provenance['stage'] = 'runtime'
        env['SOBALINK_NATIVE_NEARBY_LOOPBACK'] = '1'
        state, code, stdout, _ = capture([str(binary), '-test.v=test2json', '-test.run=^' + TEST + '$',
                                          '-test.count=1', '-test.timeout=20s'],
                                         env=env, timeout=30, limit=RUNTIME_BYTES)
        report['processStatus'] = state
        if state in ('completed', 'nonzero'):
            converted, _, data, _ = capture(['go', 'tool', 'test2json', '-p', PACKAGE],
                                             env=env, timeout=15, limit=2 * RUNTIME_BYTES, stdin=stdout)
            if converted == 'completed':
                try:
                    evidence, accepted = native_events(data, code)
                    report.update(nativeDiscovery=evidence, eventsAccepted=accepted,
                                  nativeDiscoveryVerified=accepted and evidence['status'] == 'verified')
                except (ValueError, UnicodeError, TypeError, RecursionError):
                    pass
        provenance['stage'] = 'verify-source'
        if before != source_inventory() or provenance['testSha256'] != digest(binary) or provenance['goSha256'] != digest(tool):
            raise ValueError('source or executable changed')
        report['diagnosticPassed'] = report['eventsAccepted'] and state == 'completed'
        provenance['stage'] = 'passed' if report['diagnosticPassed'] else 'runtime-failed'
    except Exception:
        report['diagnosticPassed'] = False
        report['nativeDiscoveryVerified'] = False
    finally:
        # Nothing raw is persisted or uploaded, even on build/runtime failure.
        # Keep the already reconstructed runtime/cleanup result if cleanup fails.
        if private is not None:
            try:
                shutil.rmtree(private)
            except OSError:
                report['diagnosticPassed'] = False
    try:
        if not export_ready:
            raise ValueError('export unavailable')
        (export / 'result.json').write_text(json.dumps(report, sort_keys=True) + '\n', encoding='utf-8')
        (export / 'provenance.json').write_text(json.dumps(provenance, sort_keys=True) + '\n', encoding='utf-8')
        with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
            output.write('sanitized_ready=true\n')
    except Exception:
        report['diagnosticPassed'] = False
    if report['diagnosticPassed']:
        print('PASS: native discovery verified.' if report['nativeDiscoveryVerified'] else
              'UNAVAILABLE: isolated-loopback native discovery was not verified; see the typed capability receipt.')
    else:
        print('FAIL: native discovery diagnostic incomplete or invariant failed; private details withheld.')
    return 0 if report['diagnosticPassed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
