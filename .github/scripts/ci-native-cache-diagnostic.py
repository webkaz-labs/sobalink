#!/usr/bin/env python3
"""Restore-only native cache diagnostics. Never native/release acceptance.

Only fixed trusted-main objects are restored. Existing restored roots are never
modified. Archive pilots use original snapshots; the older warm probe is retained;
no cache namespace is written and no raw output or object content is exported.
"""
from collections import Counter
import hashlib
import errno
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import threading
import time
import unicodedata

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
REPOSITORY = 'webkaz-labs/sobalink'
BASE = '50e869360c4e505dcffe47cc891611a2ebb4a126'
WORKFLOW = '.github/workflows/native-cache-diagnostic.yml'
FILES = (WORKFLOW, '.github/scripts/ci-native-cache-diagnostic.py',
         '.github/scripts/test_ci_native_cache_diagnostic.py')
REF = re.compile(r'refs/heads/cache-diagnostic/([0-9a-f]{12})-([a-z0-9][a-z0-9-]{0,31})\Z')
TAGS = 'ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy'
COMPANION = 'github.com/webkaz-labs/sobalink/poc/native'
IPC = 'TestNativeProtectedIPCCoreExchange'
PLATFORMS = {
    'windows-amd64': ('windows', 'amd64', 'windows-2025', 'Windows', 'X64',
        'trusted-main-go-v1-Windows-X64-windows-2025-go1.27.1-a16aac3bd6cd248716ae3e22a5411192b5e58aad19a0fd2c9673845e078a9099-' + BASE),
    'darwin-arm64': ('darwin', 'arm64', 'macos-26', 'macOS', 'ARM64',
        'trusted-main-go-v1-macOS-ARM64-macos-26-go1.27.1-aa9f75d771c0e2f64f5c8182dfe079371423399f22eec00a67c7c501dd9ea224-' + BASE),
}
CAPTURE_BYTES = 16 * 1024 * 1024
BOUNDS = {'prepare': 300, 'verify': 120, 'modules': 180,
          'backend_inventory': 720, 'companion_inventory': 900,
          'companion_suite': 960, 'companion_vet': 600}
ROUNDS = 2
MIN_FREE_BYTES = 1024 ** 3


def module(name):
    path = Path(__file__).with_name(name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


collector = module('ci-native-core')
OwnedGroup, OwnedJob = collector.OwnedGroup, collector.OwnedJob


def require(value):
    if not value:
        raise ValueError('native cache diagnostic boundary differs')


def checked(command, **kwargs):
    return collector.native.checked(command, **kwargs).decode('utf-8').strip()


def select(event_name, event, repository, ref, sha, checkout_sha, ancestor, changed):
    match = REF.fullmatch(ref)
    metadata = event.get('repository') or {}
    require(event_name == 'create' and event.get('ref_type') == 'branch'
            and repository == REPOSITORY and metadata.get('full_name') == REPOSITORY
            and metadata.get('private') is False and metadata.get('default_branch') == 'main'
            and re.fullmatch('[0-9a-f]{40}', sha) and sha == checkout_sha
            and match and match[1] == sha[:12]
            and event.get('ref') == ref.removeprefix('refs/heads/')
            and ancestor == BASE and sorted(changed) == sorted('A\t' + p for p in FILES))
    return sha


def admission():
    env = os.environ
    require(env.get('GITHUB_ACTIONS') == 'true' and env.get('GITHUB_JOB') == 'measure'
            and env.get('GITHUB_WORKFLOW') == 'Native cache diagnostics (restore only)'
            and Path.cwd().resolve() == ROOT
            and env.get('GITHUB_WORKFLOW_REF') == REPOSITORY + '/' + WORKFLOW + '@' + env.get('GITHUB_REF', ''))
    event_path = Path(env['GITHUB_EVENT_PATH'])
    require(event_path.stat().st_size <= 1024 * 1024)
    event = json.loads(event_path.read_text(encoding='utf-8'))
    head = checked(['git', 'rev-parse', 'HEAD'])
    ancestor = checked(['git', 'merge-base', BASE, head])
    changed = checked(['git', 'diff', '--name-status', BASE, head]).splitlines()
    select(env['GITHUB_EVENT_NAME'], event, env['GITHUB_REPOSITORY'],
           env['GITHUB_REF'], env['GITHUB_SHA'], head, ancestor, changed)
    source = collector.source_inventory()
    require(all(name in source for name in FILES))
    target = env.get('SOBALINK_CACHE_DIAGNOSTIC_PLATFORM')
    require(target in PLATFORMS)
    goos, arch, runner, runner_os, runner_arch, key = PLATFORMS[target]
    identity = checked(['go', 'env', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH', 'GOOS', 'GOARCH']).splitlines()
    require(identity == ['go1.27.1', goos, arch, goos, arch]
            and env.get('RUNNER_OS') == runner_os and env.get('RUNNER_ARCH') == runner_arch
            and collector.runtime_capability()['available'])
    companion = env.get('SOBALINK_NATIVE_GO', '')
    require(Path(companion).is_absolute() and Path(companion).is_file())
    clean = dict(env)
    clean.pop('GOROOT', None)
    identity3 = checked([companion, 'env', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH'], env=clean).splitlines()
    require(identity3 == ['go1.27.3', goos, arch])
    paths = checked(['go', 'env', 'GOMODCACHE', 'GOCACHE']).splitlines()
    require(len(paths) == 2 and all(Path(p).is_absolute() and not any(c in p for c in '\n\r\0') for p in paths))
    roots = [Path(p).resolve() for p in paths]
    require(roots[0] != roots[1] and all(ROOT not in p.parents and p != ROOT for p in roots)
            and roots[0] not in roots[1].parents and roots[1] not in roots[0].parents)
    return {'commit': head, 'base': BASE, 'platform': target, 'runner': runner, 'key': key,
            'roots': roots, 'source': source}


def fixed_command(phase):
    native = os.environ.get('SOBALINK_NATIVE_GO', '')
    require(Path(native).is_absolute())
    commands = {
        'prepare': ['go', 'run', './cmd/prepare-engine'],
        'verify': ['go', 'run', './cmd/prepare-engine', '--verify'],
        'modules': ['go', 'mod', 'verify'],
        'backend_inventory': ['go', 'test', '-race', '-count=1', '-json', '-timeout=10m', '-list=^(Test|Example|Fuzz)', './internal/core'],
        'companion_inventory': [native, 'test', '-race', '-count=1', '-json', '-timeout=15m', '-tags=native_integration,' + TAGS, '-list=^(Test|Example|Fuzz)', './...'],
        'companion_suite': [native, 'test', '-race', '-count=1', '-json', '-timeout=15m', '-tags=native_integration,' + TAGS, './...'],
        'companion_vet': [native, 'vet', './...'],
    }
    require(phase in commands)
    return commands[phase]


def owned_child(phase):
    if phase in ARCHIVE_BOUNDS:
        return archive_owned(phase)
    if phase not in BOUNDS or sys.platform not in ('win32', 'darwin') or sys.stdin.buffer.read(1) != b'G':
        return 126
    command = fixed_command(phase)
    directory = ROOT / 'poc/native' if phase.startswith('companion_') else ROOT
    return subprocess.run(command, cwd=directory, stdin=subprocess.DEVNULL,
                          shell=False, check=False).returncode

def capture(phase, env):
    """Bound both private streams and own every descendant before gate release."""
    start = time.monotonic()
    output = [bytearray(), bytearray()]
    overflow, broken = threading.Event(), threading.Event()
    process, job, readers = None, None, []
    posix = sys.platform != 'win32'
    status, code, joined = 'launch_failed', None, False
    execution_state, observed_code, group_absent = 'launch_failed', None, None

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
        job = OwnedGroup() if posix else OwnedJob()
        process = subprocess.Popen([sys.executable, '-I', '-B', str(Path(__file__).resolve()), '--owned', phase],
            cwd=ROOT, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            shell=False, start_new_session=sys.platform != 'win32')
        readers = [threading.Thread(target=drain, args=(source, target), daemon=True)
                   for source, target in zip((process.stdout, process.stderr), output)]
        for reader in readers:
            reader.start()
        # A failed assignment leaves the launcher blocked and unable to spawn.
        job.assign(process)
        process.stdin.write(b'G')
        process.stdin.close()
        status = 'completed'
        deadline = start + (ARCHIVE_BOUNDS[phase] if phase in ARCHIVE_BOUNDS else BOUNDS[phase])
        if phase in ARCHIVE_BOUNDS:
            deadline = min(deadline, float(env['SOBALINK_ARCHIVE_DEADLINE']))
        observe = job.observe if posix else process.poll
        while (code := observe()) is None:
            if phase in ARCHIVE_BOUNDS and archive_budget_exceeded(env):
                status = 'artifact_limit'
                break
            if shutil.disk_usage(env['GOTMPDIR']).free < MIN_FREE_BYTES:
                status = 'disk_headroom'
                break
            if overflow.is_set() or broken.is_set() or time.monotonic() >= deadline:
                status = 'output_limit' if overflow.is_set() else 'capture_failed' if broken.is_set() else 'watchdog_timeout'
                break
            time.sleep(0.02)
        if status == 'completed':
            status = 'completed' if code == 0 else 'nonzero'
            if not posix and job.active():
                status = 'unjoined_children'
    except (OSError, ValueError):
        status = 'capture_failed' if process is not None else 'launch_failed'
    finally:
        execution_state, observed_code = status, code
        cleanup_ok = True
        # Terminate also on unexpected exceptions; never release an unassigned
        # gate or use a breakaway fallback. Query failures remain unresolved.
        if process is not None:
            if process.stdin and not process.stdin.closed:
                process.stdin.close()
            if not posix or job is None or job.pgid is None:
                # A POSIX launcher with failed assignment is still gated and
                # has no descendants; no numeric group signal is authorized.
                if process.poll() is None:
                    try:
                        process.kill()
                    except OSError:
                        cleanup_ok = False
        if job is not None:
            try:
                if posix:
                    if job.pgid is not None:
                        # Signal only a still-live inherited group while its
                        # leader is unreaped; an exited owner takes no signal.
                        job.terminate()
                elif job.active():
                    job.terminate()
            except OSError:
                cleanup_ok = False
            finally:
                if posix:
                    job.finish_signals()
        if process is not None:
            try:
                code = process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                cleanup_ok = False
        if job is not None:
            try:
                deadline = time.monotonic() + 10
                while job.active() and time.monotonic() < deadline:
                    time.sleep(0.02)
                absent = job.active() == 0
                cleanup_ok &= absent
                if posix:
                    group_absent = absent
            except OSError:
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
            'executionState': execution_state, 'observedReturncode': observed_code,
            'groupAbsenceObservedAfterWait': group_absent, 'wallSeconds': round(time.monotonic() - start, 6)}, bytes(output[0])


DATA_NAME = re.compile(r'[0-9a-f]{64}-d\Z')
ACTION_NAME = re.compile(r'[0-9a-f]{64}-a\Z')
OBJECT_HEADER = re.compile(rb'go object [a-z0-9]+ [a-z0-9]+ (go1\.27\.[13])(?: |\n)')
ACTION_RECORD_BYTES = 175
ACTION_REFERENCE_CATEGORIES = ('go1.27.1_package_archive', 'go1.27.3_package_archive',
                               'other_present', 'missing', 'unparseable')
MAX_ENTRIES = 500_000


def category(path):
    if ACTION_NAME.fullmatch(path.name):
        return 'action_metadata'
    if not DATA_NAME.fullmatch(path.name):
        return 'other_data'
    with path.open('rb') as source:
        header = source.read(512)
    # Recognize an ar archive with its first Go package-definition member.
    # A version string elsewhere in arbitrary data is not sufficient.
    if (len(header) < 68 or header[:8] != b'!<arch>\n'
            or header[8:24].strip() != b'__.PKGDEF'
            or header[66:68] != b'`\n'):
        return 'unclassified_cache_data'
    try:
        member_size = int(header[56:66].strip())
    except ValueError:
        return 'unclassified_cache_data'
    match = OBJECT_HEADER.match(header[68:])
    if (not match or member_size < match.end()
            or member_size > path.stat().st_size - 68):
        return 'unclassified_cache_data'
    return match.group(1).decode('ascii') + '_package_archive'


def entries(root):
    root = Path(root)
    if root.is_symlink() or not root.is_dir():
        raise ValueError('cache root must be a real directory')
    count = 0
    stack = [root]
    while stack:
        directory = stack.pop()
        for entry in sorted(directory.iterdir()):
            count += 1
            if count > MAX_ENTRIES:
                raise ValueError('cache entry bound exceeded')
            mode = entry.lstat().st_mode
            if stat.S_ISDIR(mode):
                stack.append(entry)
            elif stat.S_ISREG(mode):
                yield entry, entry.relative_to(root), entry.stat().st_size
            else:
                raise ValueError('cache contains unsupported filesystem entries')


def inventory(root):
    counts, sizes = Counter(), Counter()
    for path, _, size in entries(root):
        name = category(path)
        counts[name] += 1
        sizes[name] += size
    return {'schema': 1, 'categories': {
        name: {'files': counts[name], 'bytes': sizes[name]}
        for name in sorted(counts)},
        'files': sum(counts.values()), 'bytes': sum(sizes.values())}


def action_output(path, relative):
    """Read only canonical records emitted by Go's v1 putIndexEntry.

    Go 1.27.1 cmd/go/internal/cache/cache.go defines a 175-byte entry:
    v1, 64-hex action ID, 64-hex output ID, two space-padded 20-byte int64
    fields (size and Unix nanoseconds), and LF. Stricter than Go's reader:
    noncanonical records stay unparseable, never candidates for deletion.
    """
    if (not ACTION_NAME.fullmatch(path.name)
            or relative.parts != (path.name[:2], path.name)):
        return None
    with path.open('rb') as stream:
        raw = stream.read(ACTION_RECORD_BYTES + 1)
    if (len(raw) != ACTION_RECORD_BYTES or raw[:3] != b'v1 '
            or raw[67:68] != b' ' or raw[132:133] != b' '
            or raw[153:154] != b' ' or raw[174:175] != b'\n'
            or raw[3:67] != path.name[:64].encode('ascii')
            or not re.fullmatch(rb'[0-9a-f]{64}', raw[68:132])):
        return None
    for field in (raw[133:153], raw[154:174]):
        if not re.fullmatch(rb' *[0-9]+', field):
            return None
        value = int(field)
        if value > 2 ** 63 - 1 or f'{value:20d}'.encode('ascii') != field:
            return None
    return raw[68:132].decode('ascii')


def action_reference_inventory(root):
    # Admit the complete idle snapshot, including directory/entry bounds and
    # symlink rejection, before reading records or following hex-only refs.
    root = Path(root)
    admitted = list(entries(root))
    counts, sizes, targets = Counter(), Counter(), {}
    for path, relative, size in admitted:
        if not ACTION_NAME.fullmatch(path.name):
            continue
        output = action_output(path, relative)
        name = 'unparseable'
        if output is not None:
            if output not in targets:
                target = root / output[:2] / (output + '-d')
                try:
                    mode = target.lstat().st_mode
                except FileNotFoundError:
                    targets[output] = 'missing'
                else:
                    require(stat.S_ISREG(mode) or stat.S_ISDIR(mode))
                    # Go also stores cached executables inside -d directories.
                    # A present directory is not a dangling/missing reference.
                    kind = category(target) if stat.S_ISREG(mode) else 'other_present'
                    targets[output] = kind if kind in ACTION_REFERENCE_CATEGORIES[:2] else 'other_present'
            name = targets[output]
        counts[name] += 1
        sizes[name] += size
    return {'schema': 1, 'scope': 'action-reference-inventory-only-not-cache-hit-proof',
            'recordBytes': ACTION_RECORD_BYTES,
            'categories': {name: {'files': counts[name], 'bytes': sizes[name]}
                           for name in ACTION_REFERENCE_CATEGORIES},
            'files': sum(counts.values()), 'bytes': sum(sizes.values())}


def admit_destination(source, destination):
    source, destination = Path(source), Path(destination)
    if destination.exists() or destination.is_symlink():
        raise ValueError('candidate destination must not exist')
    a, b = source.resolve(), destination.resolve()
    if a == b or a in b.parents or b in a.parents:
        raise ValueError('source and candidate must not overlap')


def copy_candidate(source, destination, role):
    source, destination = Path(source), Path(destination)
    require(role in ('backend', 'companion'))
    admit_destination(source, destination)
    # Finish admission before creating anything. The lab is not a security
    # boundary against concurrent hostile modification; run on an idle owned
    # snapshot, not a cache concurrently written by builds.
    admitted = list(entries(source))
    destination.mkdir(parents=False)
    copied, omitted = Counter(files=0, bytes=0), Counter(files=0, bytes=0)
    excluded = ('go1.27.3' if role == 'backend' else 'go1.27.1') + '_package_archive'
    for path, relative, size in admitted:
        if category(path) == excluded:
            omitted['files'] += 1
            omitted['bytes'] += size
            continue
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(path, target)
        copied['files'] += 1
        copied['bytes'] += size
    return {'schema': 1, 'role': role, 'copied': dict(copied), 'omitted': dict(omitted)}


def copy_backend_candidate(source, destination):
    return copy_candidate(source, destination, 'backend')


def copy_warm_pair(source, backend, companion):
    # Validate both destinations before creating either one. Never hardlink:
    # later writes, trimming or regeneration must not modify the seed or peer.
    for destination in (backend, companion):
        admit_destination(source, destination)
    a, b = Path(backend).resolve(), Path(companion).resolve()
    require(a != b and a not in b.parents and b not in a.parents)
    return {'backend': copy_candidate(source, backend, 'backend'),
            'companion': copy_candidate(source, companion, 'companion')}


def split_bytes(value):
    # Unknown outputs and all action metadata remain in both roots. Missing
    # outputs are ordinary cache misses, not evidence that a test was executed.
    known = sum(value['categories'].get(version + '_package_archive', {}).get('bytes', 0)
                for version in ('go1.27.1', 'go1.27.3'))
    return value['bytes'] * 2 - known




def snapshot(root):
    digest = hashlib.sha256()
    for _, relative, size in entries(root):
        value = root / relative
        digest.update(str(relative).encode('utf-8'))
        digest.update(str((size, value.stat().st_mtime_ns)).encode('ascii'))
    return digest.hexdigest()


def full_copy(source, destination):
    require(not destination.exists())
    admitted = list(entries(source))
    destination.mkdir()
    for path, relative, _ in admitted:
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(path, target)


def companion_inventory(raw):
    names, life = [], []
    for event in collector.events(raw):
        action = event['Action']
        if action in collector.parser.BUILD_ACTIONS:
            require(action != 'build-fail')
            continue
        require(event['Package'] == COMPANION and event.get('Test') is None)
        if action == 'output':
            value = event['Output'].removesuffix('\n').removesuffix('\r')
            if collector.TEST_NAME.fullmatch(value):
                names.append(value)
            else:
                require(re.fullmatch(r'ok\s+' + re.escape(COMPANION) + r'\s+[0-9]+\.[0-9]+s', value))
        else:
            life.append(action)
    require(life == ['start', 'pass'] and IPC in names and len(names) <= 4096 and len(names) == len(set(names)))
    return names


def companion_summary(raw, names, process):
    previous = collector.PACKAGE
    try:
        collector.PACKAGE = COMPANION
        result = collector.timing(raw, names, process)
    finally:
        collector.PACKAGE = previous
    accepted = (collector.execution_complete(result)
            and any(t['test'] == IPC and t['outcome'] == 'pass' for t in result['tests']))
    summary = {name: result[name] for name in ('expectedCount', 'passedCount', 'skippedCount', 'failedCount', 'subtestSkipped')}
    summary.update(accepted=accepted, accountingComplete=result['accountingComplete'],
                   eventStreamValid=result['eventStreamValid'],
                   failedTopLevelTests=[t['test'] for t in result['tests'] if t['outcome'] == 'fail'])
    return summary


def companion_result(raw, names, process):
    summary = companion_summary(raw, names, process)
    require(summary['accepted'])
    return summary


def measure_phase(phase, env, row, *, allow_test_failure=False):
    require(shutil.disk_usage(env['GOTMPDIR']).free >= MIN_FREE_BYTES)
    process, raw = capture(phase, env)
    row['phases'][phase] = {'seconds': process['wallSeconds'], 'status': process['process'],
                            'returncode': process['returncode'], 'ownersJoined': process['ownersJoined']}
    accepted_states = ('completed', 'nonzero') if allow_test_failure and phase == 'companion_suite' else ('completed',)
    require(process['process'] in accepted_states and process['ownersJoined'])
    return raw, process


def run_workload(env, companion, row):
    for phase in ('prepare', 'verify', 'modules', 'backend_inventory'):
        raw, process = measure_phase(phase, env, row)
        if phase == 'backend_inventory':
            row['backendTestCount'] = len(collector.inventory(raw))
    native_env = dict(env, GOCACHE=str(companion))
    raw, _ = measure_phase('companion_inventory', native_env, row)
    names = companion_inventory(raw)
    raw, process = measure_phase('companion_suite', native_env, row, allow_test_failure=True)
    row['companionTests'] = companion_summary(raw, names, process)
    require(row['companionTests']['accepted'])
    measure_phase('companion_vet', native_env, row)


def workload_environment(backend, modules, scratch):
    # Preserve the runner's runtime temporary-directory variables. Native IPC
    # tests deliberately use short profiles and enforce a 100-byte socket path.
    # Only compiler scratch is redirected; it does not change os.TempDir().
    env = dict(os.environ, GOCACHE=str(backend), GOMODCACHE=str(modules),
               GOTOOLCHAIN='local', GOENV='off', GOWORK='off', GOCACHEPROG='',
               GOTELEMETRY='off', GOPROXY='off', GOSUMDB='off',
               GOFLAGS='-mod=readonly -tags=' + TAGS, GOTMPDIR=str(scratch))
    env.pop('GOROOT', None)
    return env


def remove_owned_prepared(prepared):
    require(prepared == ROOT / '.sobalink-deps' and not prepared.is_symlink())
    list(entries(prepared))  # Admit the entire owned tree before permission changes.
    for directory, _, files in os.walk(prepared):
        path = Path(directory)
        path.chmod(path.stat().st_mode | stat.S_IWUSR | stat.S_IXUSR)
        for name in files:
            path = Path(directory) / name
            path.chmod(path.stat().st_mode | stat.S_IWUSR)
    shutil.rmtree(prepared)


def measure(plan, report):
    require(os.environ.get('CACHE_HIT') == 'true' and os.environ.get('CACHE_MATCHED_KEY') == plan['key'])
    modules, original = plan['roots']
    original_inventory = inventory(original)
    module_inventory = inventory(modules)
    report['restored'] = {'build': original_inventory, 'modules': module_inventory, 'match': 'exact'}
    report['actionReferences'] = {'restored': action_reference_inventory(original)}
    original_stamp, module_stamp = snapshot(original), snapshot(modules)
    temporary = Path(os.environ['RUNNER_TEMP']).resolve()
    require(temporary.is_dir() and not temporary.is_symlink())
    needed = (original_inventory['bytes'] + split_bytes(original_inventory)
              + module_inventory['bytes'] + 8 * 1024 ** 3)
    report['space'] = {'neededBytes': needed, 'freeBytes': shutil.disk_usage(temporary).free}
    require(report['space']['freeBytes'] >= needed)
    prepared = ROOT / '.sobalink-deps'
    require(not prepared.exists())
    # On a failure, retain owned paths for hosted runner cleanup. Never delete
    # files while a command may still have unresolved descendants.
    private = Path(tempfile.mkdtemp(prefix='soba-cache-diagnostic-', dir=temporary))
    module_copy = private / 'modules'
    full_copy(modules, module_copy)
    # Without -trimpath, Go hashes package source directories. Relocating the
    # module cache invalidates archived action IDs. Warm both pinned toolchains
    # at this one fixed private module path before comparing derived copies.
    seed = private / 'adapted-seed'
    full_copy(original, seed)
    warmup_scratch = private / 'warmup-scratch'
    warmup_scratch.mkdir()
    report['warmup'] = {'phases': {}}
    run_workload(workload_environment(seed, module_copy, warmup_scratch), seed, report['warmup'])
    require(plan['source'] == collector.source_inventory())
    remove_owned_prepared(prepared)
    report['comparisonSeed'] = inventory(seed)
    report['actionReferences']['comparisonSeed'] = action_reference_inventory(seed)
    seed_stamp = snapshot(seed)
    report['candidatePayload'] = {
        'baselineBuildBytes': report['comparisonSeed']['bytes'],
        'splitWarmBuildBytes': split_bytes(report['comparisonSeed']),
        'duplicatedMetadataAndUnknownBytes': split_bytes(report['comparisonSeed']) - report['comparisonSeed']['bytes']}
    for sample in range(ROUNDS):
        modes = ('baseline', 'split-warm') if sample % 2 == 0 else ('split-warm', 'baseline')
        for mode in modes:
            require(not prepared.exists())
            # Seed growth during path adaptation is measured, not estimated
            # from the restored root. Preserve compiler scratch headroom too.
            require(shutil.disk_usage(private).free >= split_bytes(report['comparisonSeed']) + 4 * MIN_FREE_BYTES)
            row = {'sample': sample + 1, 'mode': mode, 'phases': {}}
            report['samples'].append(row)
            candidate = Path(tempfile.mkdtemp(prefix='candidate-', dir=private))
            backend = candidate / 'backend'
            companion = backend if mode == 'baseline' else candidate / 'companion'
            started = time.monotonic()
            if mode == 'baseline':
                full_copy(seed, backend)
            else:
                row['partition'] = copy_warm_pair(seed, backend, companion)
            row['copySeconds'] = round(time.monotonic() - started, 6)
            row['before'] = {'backend': inventory(backend), 'companion': inventory(companion)}
            env = workload_environment(backend, module_copy, candidate)
            run_workload(env, companion, row)
            row['after'] = {'backend': inventory(backend), 'companion': inventory(companion)}
            require(plan['source'] == collector.source_inventory())
            require(snapshot(seed) == seed_stamp)
            remove_owned_prepared(prepared)
            shutil.rmtree(candidate)
    require(snapshot(original) == original_stamp and snapshot(modules) == module_stamp)
    report['originalRootMetadataUnchanged'] = True
    report['comparisonSeedMetadataUnchanged'] = True
    # Module snapshots can contain read-only Go download/extraction entries.
    # They are private temporary diagnostic files; leave them to runner cleanup.
    require(plan['source'] == collector.source_inventory())


# Archive measurements use original restored roots, never the adapted warm seed.
# Only owned generated archives are accepted. The relative-prefix GNU POSIX/PAX
# format deliberately differs from actions/cache's absolute-path (-P) format.
ARCHIVE_OBJECTS = ('combined', 'unit-bundle', 'modules', 'build', 'backend', 'companion')
ARCHIVE_PILOT = ('combined', 'unit-bundle', 'companion')
ARCHIVE_ORDERS = (ARCHIVE_PILOT, tuple(reversed(ARCHIVE_PILOT)))
ARCHIVE_POLICIES = {
    'A': (('combined',), ('combined',), ('combined',)),
    'H': (('combined', 'unit-bundle'), ('unit-bundle',), ('combined',)),
    'R': (('modules', 'build'), ('modules', 'build'), ('modules', 'build')),
    'F': (('modules', 'backend', 'companion'), ('modules', 'backend'),
          ('modules', 'backend', 'companion')),
    'Q': (('unit-bundle', 'companion'), ('unit-bundle',), ('unit-bundle', 'companion')),
}
ARCHIVE_BOUNDS = {'archive_tar_version': 15, 'archive_pack_version': 15,
                  'archive_unpack_version': 15, 'archive_pack': 240,
                  'archive_validate': 240, 'archive_extract': 240}
ARCHIVE_SECONDS = 32 * 60
ARCHIVE_MAX_BYTES = 16 * 1024 ** 3
ARCHIVE_MAX_FILE_BYTES = 4 * 1024 ** 3
ARCHIVE_MAX_SPEC_BYTES = 256 * 1024 ** 2
ARCHIVE_RESULT_BYTES = 256 * 1024
ARCHIVE_NAME_BYTES = 1024
ARCHIVE_NAME_TOTAL_BYTES = 32 * 1024 ** 2
ARCHIVE_PAX_BYTES = 8192
ARCHIVE_RESERVE_BYTES = 2 * 1024 ** 3
ARCHIVE_ACTION = '55cc8345863c7cc4c66a329aec7e433d2d1c52a9'
ARCHIVE_RESERVED = re.compile(r'(CON|PRN|AUX|NUL|CONIN\$|CONOUT\$|CLOCK\$|COM[1-9¹²³]|LPT[1-9¹²³])(?:\..*)?\Z', re.I)


def archive_tick(deadline):
    require(time.monotonic() < deadline)


def archive_name(name):
    require(isinstance(name, str) and 0 < len(name.encode('utf-8')) <= ARCHIVE_NAME_BYTES
            and not any(ord(c) < 32 or ord(c) == 127 or c in '\\:' for c in name)
            and not name.startswith(('/', '-')))
    require(all(part not in ('', '.', '..') and not part.endswith(('.', ' '))
                and not ARCHIVE_RESERVED.fullmatch(part) for part in name.split('/')))
    # Apply the same cross-platform naming contract on every target.
    require(unicodedata.normalize('NFC', name) == name)
    return name


def archive_identity(info):
    require(not (getattr(info, 'st_file_attributes', 0) & 0x400))  # Windows reparse point.
    return (info.st_dev, info.st_ino, info.st_mode, info.st_size,
            info.st_mtime_ns, info.st_ctime_ns, info.st_nlink, getattr(info, 'st_file_attributes', 0),
            getattr(info, 'st_birthtime_ns', None))


def archive_handle_identity(path_info, handle_info, *, windows=None):
    """Link an admitted path to its open handle without mixing API semantics.

    CPython Windows lstat can expose creation time as ctime and executable
    suffix-derived mode bits, while fstat exposes ChangeTime and attribute
    modes. Compare shared fields across APIs, then retain the COMPLETE raw
    identities for separate path/path and handle/handle invariance checks.
    See https://github.com/python/cpython/issues/157671.
    """
    windows = sys.platform == 'win32' if windows is None else windows
    path_identity, handle_identity = archive_identity(path_info), archive_identity(handle_info)
    require(stat.S_ISREG(handle_info.st_mode) and handle_info.st_nlink == 1)
    if not windows:
        require(path_identity == handle_identity)
        return handle_identity
    require(path_info.st_ino != 0 and handle_info.st_ino != 0)
    shared_path, shared_handle = list(path_identity), list(handle_identity)
    shared_path[2] &= ~0o111
    shared_handle[2] &= ~0o111
    births = [getattr(info, 'st_birthtime_ns', None) for info in (path_info, handle_info)]
    require(all(value is None for value in births) or all(type(value) is int for value in births))
    if births[0] is not None:
        shared_path[5], shared_handle[5] = births
    # Older runtimes without birthtime must still agree on raw ctime. Nothing
    # else is omitted: device, file ID, type/read-write bits, size, mtime,
    # link count and attributes must match. Reparse points fail in identity().
    require(shared_path == shared_handle)
    return handle_identity


def archive_hash(path, deadline, expected=None, limit=None):
    limit = ARCHIVE_MAX_FILE_BYTES if limit is None else limit
    before = path.lstat()
    identity = archive_identity(before)
    require(stat.S_ISREG(before.st_mode) and before.st_nlink == 1
            and before.st_size <= limit)
    if expected is not None:
        require(identity == expected)
    digest, count = hashlib.sha256(), 0
    with path.open('rb') as stream:
        opened = archive_handle_identity(before, os.fstat(stream.fileno()))
        while block := stream.read(1024 * 1024):
            archive_tick(deadline)
            count += len(block)
            require(count <= before.st_size)
            digest.update(block)
        require(archive_identity(os.fstat(stream.fileno())) == opened)
    require(count == before.st_size and archive_identity(path.lstat()) == identity)
    return digest.hexdigest()


def archive_snapshot(roots, deadline):
    """Hash one idle snapshot including empty directories; never export names/IDs.

    Not a sandbox against a hostile concurrent writer. Changed identities,
    content, directory membership, hardlinks, symlinks and reparse points fail.
    """
    require(set(roots) == {'modules', 'build'})
    paths = [Path(p) for p in roots.values()]
    for p in paths:
        require(p.is_absolute() and not p.is_symlink() and p.resolve() == p)
    require(paths[0] != paths[1] and paths[0] not in paths[1].parents and paths[1] not in paths[0].parents)
    result, seen, total, name_bytes = {}, set(), 0, 0
    for label, root in roots.items():
        pending = [(root, label)]
        while pending:
            archive_tick(deadline)
            path, name = pending.pop()
            archive_name(name)
            name_bytes += len(name.encode('utf-8')) + 1
            require(name_bytes <= ARCHIVE_NAME_TOTAL_BYTES)
            require(name.casefold() not in seen and len(result) < MAX_ENTRIES)
            seen.add(name.casefold())
            info = path.lstat()
            identity = archive_identity(info)
            require(stat.S_IMODE(info.st_mode) & ~0o777 == 0)
            row = {'kind': 'directory' if stat.S_ISDIR(info.st_mode) else 'file',
                   'size': 0, 'mode': stat.S_IMODE(info.st_mode), 'identity': identity,
                   'source': path, 'category': 'directory'}
            if row['kind'] == 'directory':
                children = []
                with os.scandir(path) as iterator:
                    for child in iterator:
                        require(len(result) + len(pending) + len(children) < MAX_ENTRIES)
                        children.append((Path(child.path), name + '/' + child.name))
                pending.extend(sorted(children, reverse=True))
            else:
                require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1)
                total += info.st_size
                require(total <= ARCHIVE_MAX_BYTES)
                row.update(size=info.st_size, sha256=archive_hash(path, deadline, identity),
                           category=category(path) if label == 'build' else 'module_data')
            require(archive_identity(path.lstat()) == identity)
            result[name] = row
    return dict(sorted(result.items()))


def archive_projection(snapshot, object_name):
    require(object_name in ARCHIVE_OBJECTS)
    result = {}
    for name, row in snapshot.items():
        module = name == 'modules' or name.startswith('modules/')
        selected = (object_name == 'combined'
            or object_name == 'unit-bundle' and (module or row['category'] != 'go1.27.3_package_archive')
            or object_name == 'modules' and module
            or object_name == 'build' and not module
            or object_name == 'backend' and not module and row['category'] != 'go1.27.3_package_archive'
            or object_name == 'companion' and not module and row['category'] != 'go1.27.1_package_archive')
        if not selected:
            continue
        if not module and object_name in ('unit-bundle', 'backend', 'companion'):
            prefix = 'companion' if object_name == 'companion' else 'backend'
            name = prefix + name[len('build'):]
        result[archive_name(name)] = row
    return result


def archive_totals(rows):
    return {'files': sum(row['kind'] == 'file' for row in rows.values()),
            'directories': sum(row['kind'] == 'directory' for row in rows.values()),
            'payloadBytes': sum(row['size'] for row in rows.values())}


def archive_stream_bound(rows):
    # One bounded local PAX header plus normal header/padding per member, with
    # GNU tar's record/trailer padding. No full uncompressed tar is written.
    return sum(row['size'] for row in rows.values()) + len(rows) * (ARCHIVE_PAX_BYTES + 1536) + 10240


ARCHIVE_STAGE_FIELDS = ('st_dev', 'st_ino', 'st_mode', 'st_size', 'st_mtime_ns',
                        'st_ctime_ns', 'st_nlink', 'st_file_attributes', 'st_birthtime_ns')
ARCHIVE_STAGE_OPERATIONS = frozenset((
    'stage_fresh', 'stage_mkdir', 'deadline_check', 'source_stat_before',
    'source_identity_before', 'stage_directory_create', 'source_open',
    'stage_file_create', 'source_handle_stat_before', 'handle_identity_initial',
    'source_read', 'payload_bound', 'stage_write', 'source_handle_stat_after',
    'handle_identity_after', 'stage_file_close', 'source_close', 'payload_verify',
    'source_stat_after', 'source_identity_after', 'metadata_copy',
    'source_stat_after_metadata', 'source_identity_after_metadata'))


def archive_stage_equal(expected, actual):
    # Only already-captured identities are compared. No post-failure I/O and
    # no values, paths, member names or hashes enter the diagnostic record.
    return {name: left == right for name, left, right in zip(ARCHIVE_STAGE_FIELDS, expected, actual)}


def archive_stage_failure(operation, error, equalities=None):
    """Reconstruct a closed, bounded record; never stringify an exception."""
    operation = operation if type(operation) is str and operation in ARCHIVE_STAGE_OPERATIONS else 'unknown_operation'
    category = 'unexpected_failure'
    if isinstance(error, OSError):
        # Standard numeric error codes are used only for classification. They
        # are never exported; unknown/noninteger codes have one fixed bucket.
        try:
            winerror, code = getattr(error, 'winerror', None), getattr(error, 'errno', None)
        except Exception:
            winerror, code = None, None
        windows = {2: 'io_missing', 3: 'io_missing', 5: 'io_permission',
                   32: 'io_sharing_or_lock', 33: 'io_sharing_or_lock',
                   80: 'io_exists', 112: 'io_disk_full', 183: 'io_exists',
                   206: 'io_path_length', 50: 'io_unsupported'}
        portable = {errno.ENOENT: 'io_missing', errno.EACCES: 'io_permission', errno.EPERM: 'io_permission',
                    errno.EEXIST: 'io_exists', errno.ENOSPC: 'io_disk_full', errno.ENAMETOOLONG: 'io_path_length'}
        category = (windows.get(winerror, 'io_other') if type(winerror) is int else
                    portable.get(code, 'io_other') if type(code) is int else 'io_other')
    elif isinstance(error, NotImplementedError):
        category = 'io_unsupported'
    elif isinstance(error, ValueError):
        category = {'stage_fresh': 'stage_not_fresh', 'deadline_check': 'deadline_rejected',
                    'source_identity_before': 'source_identity_rejected',
                    'source_identity_after': 'source_identity_rejected',
                    'source_identity_after_metadata': 'source_identity_rejected',
                    'handle_identity_initial': 'handle_identity_rejected',
                    'handle_identity_after': 'handle_identity_changed',
                    'payload_bound': 'payload_size_rejected',
                    'payload_verify': 'payload_rejected'}.get(operation, 'invariant_rejected')
    result = {'status': 'failed', 'operation': operation, 'category': category,
              'operationAttribution': 'last-marked-boundary-may-precede-unwind-error'}
    if type(equalities) is dict:
        safe = {name: equalities[name] for name in ARCHIVE_STAGE_FIELDS
                if name in equalities and type(equalities[name]) is bool}
        if safe:
            result['fieldEqual'] = safe
    return result


def archive_stage(rows, stage, deadline, diagnostic=None):
    progress = {'operation': 'stage_fresh'}

    def step(operation):
        progress.clear()
        progress['operation'] = operation

    def equal(expected, actual):
        progress['fieldEqual'] = archive_stage_equal(expected, actual)

    if diagnostic is not None:
        diagnostic.clear()
        diagnostic['status'] = 'started'
    try:
        require(not stage.exists())
        step('stage_mkdir')
        stage.mkdir()
        for name, row in rows.items():
            step('deadline_check')
            archive_tick(deadline)
            source, target = row['source'], stage / name
            step('source_stat_before')
            before = source.lstat()
            step('source_identity_before')
            observed = archive_identity(before)
            equal(row['identity'], observed)
            require(observed == row['identity'])
            if row['kind'] == 'directory':
                step('stage_directory_create')
                target.mkdir()
            else:
                digest, count = hashlib.sha256(), 0
                step('source_open')
                with source.open('rb') as incoming:
                    step('stage_file_create')
                    with target.open('xb') as outgoing:
                        step('source_handle_stat_before')
                        handle_info = os.fstat(incoming.fileno())
                        step('handle_identity_initial')
                        try:
                            opened = archive_handle_identity(before, handle_info)
                        except ValueError:
                            # Raw path/handle equality may legitimately differ
                            # in Windows mode/ctime. This identifies observations,
                            # not the compatible-field rejection by itself.
                            equal(tuple(getattr(before, field, 0 if field == 'st_file_attributes' else None) for field in ARCHIVE_STAGE_FIELDS),
                                  tuple(getattr(handle_info, field, 0 if field == 'st_file_attributes' else None) for field in ARCHIVE_STAGE_FIELDS))
                            raise
                        while True:
                            step('source_read')
                            block = incoming.read(1024 * 1024)
                            if not block:
                                break
                            step('deadline_check')
                            archive_tick(deadline)
                            count += len(block)
                            step('payload_bound')
                            require(count <= row['size'])
                            step('stage_write')
                            outgoing.write(block)
                            digest.update(block)
                        step('source_handle_stat_after')
                        handle_info = os.fstat(incoming.fileno())
                        step('handle_identity_after')
                        observed = archive_identity(handle_info)
                        equal(opened, observed)
                        require(observed == opened)
                        step('stage_file_close')
                    step('source_close')
                step('payload_verify')
                require(count == row['size'] and digest.hexdigest() == row['sha256'])
            step('source_stat_after')
            after = source.lstat()
            step('source_identity_after')
            observed = archive_identity(after)
            equal(row['identity'], observed)
            require(observed == row['identity'])
        # Finalize directories last so read-only module trees remain writable
        # while staging, then retain their source modes and mtimes for packing.
        for name, row in sorted(rows.items(), reverse=True):
            step('metadata_copy')
            shutil.copystat(row['source'], stage / name, follow_symlinks=False)
            step('source_stat_after_metadata')
            after = row['source'].lstat()
            step('source_identity_after_metadata')
            observed = archive_identity(after)
            equal(row['identity'], observed)
            require(observed == row['identity'])
    except Exception as error:
        if diagnostic is not None:
            diagnostic.clear()
            diagnostic.update(archive_stage_failure(progress['operation'], error, progress.get('fieldEqual')))
        # Preserve existing failure propagation. Unexpected exceptions also
        # fail closed through main's sanitized handler, with no raw traceback.
        if isinstance(error, (OSError, ValueError, KeyError, RuntimeError)):
            raise
        raise ValueError('native cache diagnostic boundary differs') from None
    if diagnostic is not None:
        diagnostic.clear()
        diagnostic['status'] = 'completed'


def archive_remove(path):
    # Caller must own the entire tree and have joined every child. Never use
    # this against the original source, arbitrary archives, or unresolved work.
    require(path.is_dir() and not path.is_symlink())
    for directory, subdirs, files in os.walk(path, followlinks=False):
        for item in [Path(directory), *(Path(directory) / n for n in subdirs + files)]:
            info = item.lstat()
            archive_identity(info)
            require(stat.S_ISDIR(info.st_mode) or stat.S_ISREG(info.st_mode) and info.st_nlink == 1)
            item.chmod(stat.S_IMODE(info.st_mode) | stat.S_IWUSR | (stat.S_IXUSR if item.is_dir() else 0))
    shutil.rmtree(path)


def archive_tools(platform):
    require(platform in ('windows-amd64', 'darwin-arm64', 'linux-fixture'))
    if platform == 'windows-amd64':
        # The cache toolkit chooses Git's GNU tar on Windows, not System32's
        # BSD tar. Fail closed if that installation or its zstd is unavailable.
        tar = Path(os.environ['ProgramFiles']) / 'Git/usr/bin/tar.exe'
        names = ('zstd', 'zstd')
    else:
        tar = Path(shutil.which('gtar' if platform == 'darwin-arm64' else 'tar') or '')
        names = ('zstdmt', 'unzstd') if platform == 'darwin-arm64' else ('zstd', 'zstd')
    paths = [tar, *(Path(shutil.which(n) or '') for n in names)]
    require(all(p.is_absolute() and p.is_file() for p in paths))
    return {'tar': str(paths[0]), 'pack': str(paths[1]), 'unpack': str(paths[2]), 'platform': platform}


def archive_command(phase, spec):
    require(phase in ARCHIVE_BOUNDS)
    tools = spec['tools']
    if phase.endswith('_version'):
        role = phase.removeprefix('archive_').removesuffix('_version')
        return [tools[role], '--version']
    require(phase in ('archive_pack', 'archive_extract'))
    private = Path(spec['private'])
    tar = tools['tar']
    platform = tools['platform']
    if phase == 'archive_pack':
        command = [tar, '--posix', '-cf', (private / 'cache.tzst').as_posix(), '--exclude', 'cache.tzst',
                   '-C', (private / 'stage').as_posix(), '--null', '--verbatim-files-from', '--no-recursion',
                   '--files-from', (private / 'manifest.bin').as_posix()]
        compressor = 'zstdmt' if platform == 'darwin-arm64' else 'zstd -T0'
    else:
        command = [tar, '-xf', (private / 'cache.tzst').as_posix(), '-C', (private / 'destination').as_posix(),
                   '--no-same-owner', '--same-permissions']
        compressor = 'unzstd' if platform == 'darwin-arm64' else 'zstd -d'
    if platform == 'windows-amd64':
        command.append('--force-local')
    if platform == 'darwin-arm64':
        command.append('--delay-directory-restore')
    return command + ['--use-compress-program', compressor]


class ArchiveReader:
    def __init__(self, stream, limit):
        self.stream, self.limit, self.count = stream, limit, 0

    def read(self, size):
        require(0 <= size <= 1024 * 1024)
        data = self.stream.read(size)
        self.count += len(data)
        require(self.count <= self.limit)
        return data

    def exact(self, size):
        data = self.read(size)
        require(len(data) == size)
        return data


def archive_octal(raw):
    require(re.fullmatch(rb'[0-7]+[\0 ]*', raw.lstrip(b' ')))
    return int(raw.strip(b'\0 '), 8)


def archive_pax(raw):
    # Never feed an attacker-declared size into tarfile's allocating PAX parser.
    require(len(raw) <= ARCHIVE_PAX_BYTES)
    result = {}
    while raw:
        space = raw.find(b' ')
        require(0 < space <= 8 and raw[:space].isdigit())
        count = int(raw[:space])
        require(space + 3 <= count <= len(raw) and raw[count - 1:count] == b'\n')
        key, separator, value = raw[space + 1:count - 1].partition(b'=')
        require(separator and key in (b'path', b'atime', b'ctime', b'mtime') and key not in result)
        if key == b'path':
            result[key] = value.decode('utf-8')
        else:
            require(re.fullmatch(rb'-?[0-9]{1,20}(?:\.[0-9]{1,20})?', value))
            result[key] = value
        raw = raw[count:]
    return result


def archive_mode_matches(mode, row, windows):
    require(type(mode) is int and mode & ~0o777 == 0)
    # Git/MSYS tar's executable bits are not Python's Windows mode model.
    # Verify regular-file read-only semantics there; do not claim ACL/mode identity.
    return ((row['kind'] == 'directory' or bool(mode & 0o200) == bool(row['mode'] & 0o200))
            if windows else mode == row['mode'])


def archive_validate_stream(stream, expected, limit, windows=False):
    reader, seen, pending = ArchiveReader(stream, limit), set(), None
    payload, headers, pax_bytes, padding = 0, 0, 0, 0
    while True:
        header = reader.exact(512)
        if header == bytes(512):
            require(reader.exact(512) == bytes(512) and pending is None)
            padding += 1024
            while block := reader.read(1024 * 1024):
                require(not any(block))
                padding += len(block)
            break
        headers += 512
        require(header[257:265] == b'ustar\x0000')
        checksum = archive_octal(header[148:156])
        require(checksum == sum(header[:148]) + 8 * 32 + sum(header[156:]))
        size, mode = archive_octal(header[124:136]), archive_octal(header[100:108])
        kind = header[156:157]
        require(kind in (b'0', b'\0', b'5', b'x') and not header[157:257].rstrip(b'\0'))
        if kind == b'x':
            require(pending is None and size <= ARCHIVE_PAX_BYTES)
            pending = archive_pax(reader.exact(size))
            pax_bytes += size
        else:
            prefix = header[345:500].split(b'\0', 1)[0]
            name = header[:100].split(b'\0', 1)[0]
            name = ((prefix + b'/') if prefix else b'') + name
            # GNU tar's short header stub can end mid-codepoint when a long
            # UTF-8 path has a valid complete PAX override. Do not decode it.
            name = pending[b'path'] if pending and b'path' in pending else name.decode('utf-8')
            if kind == b'5':
                name = name.removesuffix('/')
            archive_name(name)
            require(name in expected and name not in seen)
            row = expected[name]
            require(archive_mode_matches(mode, row, windows) and size == row['size']
                    and row['kind'] == ('directory' if kind == b'5' else 'file'))
            seen.add(name)
            digest, remaining = hashlib.sha256(), size
            while remaining:
                block = reader.exact(min(remaining, 1024 * 1024))
                digest.update(block)
                remaining -= len(block)
            if kind != b'5':
                require(digest.hexdigest() == row['sha256'])
            payload += size
            pending = None
        pad = (-size) % 512
        require(reader.exact(pad) == bytes(pad))
        padding += pad
    require(seen == set(expected))
    return {'streamBytes': reader.count, 'filePayloadBytes': payload,
            'headerBytes': headers, 'paxPayloadBytes': pax_bytes, 'paddingBytes': padding,
            'membersVerified': len(seen)}


def archive_owned(phase):
    if phase not in ARCHIVE_BOUNDS or sys.stdin.buffer.read(1) != b'G':
        return 126
    try:
        path = Path(os.environ['SOBALINK_ARCHIVE_SPEC'])
        require(path.is_absolute() and path.stat().st_size <= ARCHIVE_MAX_SPEC_BYTES)
        spec = json.loads(path.read_bytes())
        if phase != 'archive_validate':
            return subprocess.run(archive_command(phase, spec), stdin=subprocess.DEVNULL,
                                  shell=False, check=False).returncode
        private = Path(spec['private'])
        # Entire decompressor/validator remains under the existing owned job.
        # Drain incrementally, never communicate() an unbounded tar into memory.
        process = subprocess.Popen([spec['tools']['unpack'], '-d', '-c', '--', str(private / 'cache.tzst')],
                                   stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, shell=False)
        try:
            result = archive_validate_stream(process.stdout, spec['expected'], spec['streamBound'],
                                             windows=spec['tools']['platform'] == 'windows-amd64')
            require(process.wait(timeout=10) == 0)
            print(json.dumps(result))
            return 0
        finally:
            process.stdout.close()
            if process.poll() is None:
                process.kill()
                process.wait(timeout=10)
    except (OSError, ValueError, KeyError, subprocess.TimeoutExpired):
        return 1  # Raw member names, source paths and parser errors stay private.


def archive_budget_exceeded(env):
    archive = Path(env['GOTMPDIR']) / 'cache.tzst'
    return archive.exists() and archive.stat().st_size > int(env['SOBALINK_ARCHIVE_FILE_BOUND'])


def archive_capture(phase, env, spec, deadline, row):
    archive_tick(deadline)
    require(phase in ARCHIVE_BOUNDS)
    path = Path(env['SOBALINK_ARCHIVE_SPEC'])
    preparing = time.monotonic()
    raw = json.dumps(spec, separators=(',', ':')).encode('utf-8')
    require(len(raw) <= ARCHIVE_MAX_SPEC_BYTES)
    path.write_bytes(raw)
    preparation = time.monotonic() - preparing
    process, output = capture(phase, dict(env, SOBALINK_ARCHIVE_DEADLINE=str(deadline),
        SOBALINK_ARCHIVE_FILE_BOUND=str(spec.get('streamBound', 0) + 64 * 1024 ** 2)))
    row[phase] = {key: process[key] for key in ('process', 'ownersJoined', 'returncode', 'wallSeconds')}
    row[phase]['specPreparationSeconds'] = round(preparation, 6)
    require(process['ownersJoined'] and process['process'] == 'completed')
    return output


def archive_version(raw, role):
    pattern = (rb'tar \(GNU tar\) ([0-9]+\.[0-9]+(?:\.[0-9]+)?)' if role == 'tar' else
               rb'(?:Zstandard CLI|zstd command line interface)[^\r\n]{0,80} v([0-9]+\.[0-9]+(?:\.[0-9]+)?)')
    match = re.search(pattern, raw)
    require(match is not None)
    return match.group(1).decode('ascii')


def archive_verify_destination(destination, rows, deadline, windows=False):
    seen = set()
    pending = [destination]
    while pending:
        archive_tick(deadline)
        directory = pending.pop()
        with os.scandir(directory) as children:
            for child in children:
                path = Path(child.path)
                name = archive_name(path.relative_to(destination).as_posix())
                require(name in rows and name not in seen)
                seen.add(name)
                row, info = rows[name], path.lstat()
                archive_identity(info)
                require(archive_mode_matches(stat.S_IMODE(info.st_mode), row, windows))
                if row['kind'] == 'directory':
                    require(stat.S_ISDIR(info.st_mode))
                    pending.append(path)
                else:
                    require(stat.S_ISREG(info.st_mode) and info.st_size == row['size']
                            and archive_hash(path, deadline) == row['sha256'])
    require(seen == set(rows))


def archive_accounting(projections, samples):
    result = {}
    for label, (writer, unit, integration) in ARCHIVE_POLICIES.items():
        # Unique object lists are intentional. Baseline A's object is shared
        # with H; M is counted once for F's writer/integration, not once per role.
        result[label] = {}
        for role, names in (('writer', writer), ('unit', unit), ('integration', integration)):
            totals = [archive_totals(projections[name]) for name in names]
            measured = all(name in samples for name in names)
            result[label][role] = {'objects': list(names), 'objectCount': len(names),
                'payloadBytes': sum(value['payloadBytes'] for value in totals),
                'files': sum(value['files'] for value in totals),
                'directories': sum(value['directories'] for value in totals),
                'compressedBytes': sum(samples[n]['compressedBytes'] for n in names) if measured else None,
                'componentSeconds': sum(samples[n]['phases']['archive_pack' if role == 'writer' else 'archive_extract']['wallSeconds']
                                        for n in names) if measured else None,
                'measurement': 'derived-component-sum' if measured else 'unmeasured',
                'remoteTransferSeconds': None}
    return result


def archive_environment(tools, private):
    # Ignore inherited compressor/tar knobs so both blocks have the same fixed
    # profile. Runtime temporary-directory variables are deliberately untouched.
    env = {key: value for key, value in os.environ.items()
           if key.upper() not in ('TAR_OPTIONS', 'TAPE', 'POSIXLY_CORRECT')
           and not key.upper().startswith('ZSTD_')}
    env.update(GOTMPDIR=str(private), SOBALINK_ARCHIVE_SPEC=str(private / 'spec.json'))
    env['PATH'] = os.pathsep.join(dict.fromkeys([str(Path(tools[n]).parent) for n in ('pack', 'unpack')])) + os.pathsep + env['PATH']
    names = ('zstdmt', 'unzstd') if tools['platform'] == 'darwin-arm64' else ('zstd', 'zstd')
    for role, name in zip(('pack', 'unpack'), names):
        found = shutil.which(name, path=env['PATH'])
        require(found is not None and Path(found).resolve() == Path(tools[role]).resolve())
    return env


def measure_archives(plan, report):
    require(os.environ.get('CACHE_HIT') == 'true' and os.environ.get('CACHE_MATCHED_KEY') == plan['key'])
    started = time.monotonic()
    deadline = started + ARCHIVE_SECONDS
    roots = dict(zip(('modules', 'build'), plan['roots']))
    before = archive_snapshot(roots, deadline)
    report['snapshotSeconds'] = round(time.monotonic() - started, 6)
    report['restored'] = archive_totals(before) | {'match': 'exact', 'source': 'original-restored-roots'}
    planning = time.monotonic()
    projections = {name: archive_projection(before, name) for name in ARCHIVE_OBJECTS}
    report['objects'] = {name: archive_totals(rows) for name, rows in projections.items()}
    report['planningSeconds'] = round(time.monotonic() - planning, 6)
    temporary = Path(os.environ['RUNNER_TEMP']).resolve()
    require(temporary.is_dir() and not temporary.is_symlink())
    for root in roots.values():
        require(temporary != root and root not in temporary.parents)
    # Sequential lifetime: one stage + one compressed archive + one fresh
    # destination; original roots already consume disk and are not copied again.
    max_payload = max(archive_totals(projections[n])['payloadBytes'] for n in ARCHIVE_PILOT)
    max_archive = max(archive_stream_bound(projections[n]) for n in ARCHIVE_PILOT) + 64 * 1024 ** 2
    required = max_payload + max_archive + ARCHIVE_RESERVE_BYTES
    report['space'] = {'prepackAdditionalRequiredBytes': required,
                       'worstCaseCoexistenceBytes': 2 * max_payload + max_archive + ARCHIVE_RESERVE_BYTES,
                       'extractionAdmission': 'fresh-destination-payload-plus-reserve-after-measured-pack',
                       'initialFreeBytes': shutil.disk_usage(temporary).free,
                       'compressedObjectBoundBytes': max_archive, 'diskQuotaEnforced': False}
    require(report['space']['initialFreeBytes'] >= required)
    private = Path(tempfile.mkdtemp(prefix='soba-cache-archive-', dir=temporary))
    require(all(root != private and root not in private.parents and private not in root.parents for root in roots.values()))
    tools = archive_tools(plan['platform'])
    env = archive_environment(tools, private)
    tool_identity = {role: archive_hash(Path(tools[role]).resolve(), deadline) for role in ('tar', 'pack', 'unpack')}
    spec = {'tools': tools, 'private': str(private)}
    report['tools'] = {'versions': {}, 'phases': {}, 'cacheAction': ARCHIVE_ACTION, 'cacheToolkitVersion': '6.1.0'}
    for role in ('tar', 'pack', 'unpack'):
        raw = archive_capture('archive_' + role + '_version', env, spec, deadline, report['tools']['phases'])
        report['tools']['versions'][role] = archive_version(raw, role)
    # Capture hosted image identity only if it matches a closed public format.
    image = os.environ.get('ImageVersion', '')
    require(re.fullmatch(r'[0-9]{8}\.[0-9]+(?:\.[0-9]+)?', image))
    report['runnerImageVersion'] = image
    report['toolArgumentProfile'] = {'windows-amd64': 'gnu-posix-zstd-T0-force-local',
        'darwin-arm64': 'gnu-posix-zstdmt-delay-directory-restore',
        'linux-fixture': 'gnu-posix-zstd-T0-synthetic-only'}[plan['platform']]
    report['metadataVerification'] = ('windows-file-readonly-not-posix-modes-or-acls'
        if plan['platform'] == 'windows-amd64' else 'posix-permission-bits-not-acls-or-xattrs')
    min_free = shutil.disk_usage(private).free
    report['blocks'] = []
    for block, order in enumerate(ARCHIVE_ORDERS, 1):
        measured = {}
        for object_name in order:
            archive_tick(deadline)
            require(shutil.disk_usage(private).free >= required)
            rows = projections[object_name]
            row = {'block': block, 'object': object_name, 'position': len(measured) + 1, 'phases': {}}
            report['samples'].append(row)
            sample_started = at = time.monotonic()
            row['staging'] = {}
            archive_stage(rows, private / 'stage', deadline, row['staging'])
            row['stagingSeconds'] = round(time.monotonic() - at, 6)
            at = time.monotonic()
            (private / 'manifest.bin').write_bytes(b''.join(n.encode('utf-8') + b'\0' for n in rows))
            expected = {n: {k: r[k] for k in ('kind', 'size', 'mode', 'sha256') if k in r} for n, r in rows.items()}
            spec.update(expected=expected, streamBound=archive_stream_bound(rows))
            row['manifestSeconds'] = round(time.monotonic() - at, 6)
            archive_capture('archive_pack', env, spec, deadline, row['phases'])
            packed = private / 'cache.tzst'
            require(packed.stat().st_size <= spec['streamBound'] + 64 * 1024 ** 2)
            row['compressedBytes'] = packed.stat().st_size
            # Only this generated file is validated/extracted. Source bytes and
            # modes, complete membership and bounded PAX framing are all checked.
            at = time.monotonic()
            packed_identity = archive_identity(packed.lstat())
            packed_hash = archive_hash(packed, deadline, packed_identity, max_archive)
            identity_seconds = time.monotonic() - at
            raw = archive_capture('archive_validate', env, spec, deadline, row['phases'])
            validated = json.loads(raw)
            require(set(validated) == {'streamBytes', 'filePayloadBytes', 'headerBytes', 'paxPayloadBytes', 'paddingBytes', 'membersVerified'}
                    and all(type(v) is int and v >= 0 for v in validated.values()))
            row['archiveValidation'] = validated
            at = time.monotonic()
            require(archive_hash(packed, deadline, packed_identity, max_archive) == packed_hash)
            row['archiveIdentityCheckSeconds'] = round(identity_seconds + time.monotonic() - at, 6)
            require(shutil.disk_usage(private).free >= archive_totals(rows)['payloadBytes'] + ARCHIVE_RESERVE_BYTES)
            (private / 'destination').mkdir()
            archive_capture('archive_extract', env, spec, deadline, row['phases'])
            at = time.monotonic()
            archive_verify_destination(private / 'destination', rows, deadline,
                                       windows=plan['platform'] == 'windows-amd64')
            row['destinationValidationSeconds'] = round(time.monotonic() - at, 6)
            require(archive_identity(packed.lstat()) == packed_identity)
            row['destinationContentMetadataAndMembershipVerified'] = True
            min_free = min(min_free, shutil.disk_usage(private).free)
            # No finally cleanup: failure/unknown ownership retains private work.
            at = time.monotonic()
            archive_remove(private / 'destination')
            archive_remove(private / 'stage')
            packed.unlink()
            (private / 'manifest.bin').unlink()
            row['cleanupSeconds'] = round(time.monotonic() - at, 6)
            row['sampleTotalSeconds'] = round(time.monotonic() - sample_started, 6)
            measured[object_name] = row
        report['blocks'].append({'block': block, 'order': list(order), 'policies': archive_accounting(projections, measured)})
    at = time.monotonic()
    require(archive_snapshot(roots, deadline) == before)
    require(plan['source'] == collector.source_inventory())
    report['sourceRevalidationSeconds'] = round(time.monotonic() - at, 6)
    require(all(archive_hash(Path(tools[role]).resolve(), deadline) == digest for role, digest in tool_identity.items()))
    report['toolExecutableIdentityUnchanged'] = True
    report['originalContentIdentityAndMembershipUnchanged'] = True
    report['space'].update(observedFreeSpaceLowWaterBytes=min_free,
                         observedFreeSpaceDecreaseBytes=max(0, report['space']['initialFreeBytes'] - min_free),
                         observation='phase-boundary-filesystem-free-space-not-process-peak')
    (private / 'spec.json').unlink()
    private.rmdir()
    report['elapsedSeconds'] = round(time.monotonic() - started, 6)


def archive_report():
    return {'schema': 3, 'diagnosticPassed': False, 'releaseValidation': False,
            'remoteCacheWrites': False, 'scope': 'original-snapshot-native-streaming-archive-pilot',
            'samples': [], 'originalContentIdentityAndMembershipUnchanged': None,
            'plan': {'blocks': 2, 'orders': [list(order) for order in ARCHIVE_ORDERS],
                     'objectSamples': 6, 'archiveBudgetSeconds': ARCHIVE_SECONDS,
                     'measuredPolicies': ['A', 'H', 'Q'], 'unmeasuredPolicies': ['R', 'F'],
                     'automaticRetries': 0, 'confirmatory': False},
            'limitations': ['local streaming archive pilot; no candidate remote upload/download or production speed claim',
                'two forward/reverse blocks on one runner are not confirmatory or a physical cold-disk measurement',
                'source is the original idle restored snapshot; no Go workload or adapted seed is used',
                'safe relative prefixes replace absolute -P archive members; not byte-identical production archives',
                'explicit NUL verbatim nonrecursive member lists and no-same-owner extraction are safety adaptations',
                'all source directories including empty ones and metadata/unknown objects are retained in each projection',
                'independent hash/format validation includes an additional decompression pass outside extraction timing',
                'pre-extraction validation reads the compressed archive and may warm filesystem caches',
                'writer and consumer policy totals are derived sums of object measurements, not measured job latency',
                'staging and validation are diagnostic isolation, not production baseline writer costs',
                'production partition/request/finalization overhead and CI critical path remain unmeasured',
                'R/F compressed bytes and times remain unmeasured; no linear compressed-size estimate is made',
                'one stage, compressed object and fresh destination coexist; no uncompressed tar intermediate',
                'disk admission and polling are conservative headroom checks, not a hard filesystem quota',
                'source snapshots and paths stay private; uploaded output is bounded fixed-label aggregate JSON',
                'POSIX permission bits or Windows regular-file read-only semantics are checked; ACLs/xattrs are not validated',
                'existing pinned compiler fixtures remain required; native workload routing/release acceptance is not established']}


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    if len(argv) == 2 and argv[0] == '--owned':
        return owned_child(argv[1])
    if argv not in (['plan'], ['measure'], ['archive']):
        print('Native cache diagnostic requires its fixed entry point.')
        return 1
    report = {'schema': 2, 'diagnosticPassed': False, 'releaseValidation': False,
              'remoteCacheWrites': False, 'scope': 'cache-inventory-and-split-warm-reuse-only',
              'samples': [], 'originalRootMetadataUnchanged': None, 'comparisonSeedMetadataUnchanged': None,
              'limitations': ['no remote cache archive save or compressed restore comparison',
                  'comparison uses a separately prewarmed local seed at a relocated module path',
                  'warmup is reported separately and is not production warm-cache latency',
                  'local copy time is diagnostic setup, not cache restore time',
                  'each split root omits only recognized package archives from the other pinned toolchain',
                  'action metadata and unknown cache data are duplicated in both warm roots',
                  'action-reference counts are read-only; present output does not prove size, hash or action validity',
                  'noncanonical action records remain unparseable; no metadata is pruned',
                  'warm means seeded compilation objects; every test executes with count=1',
                  'module copy is shared and warm; paired samples do not measure cold module downloads',
                  'source cache invariance uses file names, sizes and modification times',
                  'disk headroom is monitored, not a filesystem quota',
                  'test event accounting fails closed beyond 12 minutes despite the 15-minute command timeout',
                  'no package reproduction, full native coverage or release approval']}
    if argv == ['archive']:
        report = archive_report()
    output = None
    try:
        os.umask(0o077)
        plan = admission()
        if argv == ['plan']:
            with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as stream:
                print('gomodcache=' + str(plan['roots'][0]), file=stream)
                print('gocache=' + str(plan['roots'][1]), file=stream)
                print('key=' + plan['key'], file=stream)
            print('Exact trusted-main cache diagnostic admitted.')
            return 0
        report.update(commit=plan['commit'], baselineCommit=BASE, platform=plan['platform'], runner=plan['runner'],
                      toolchains=['go1.27.1', 'go1.27.3'])
        destination = Path(os.environ['RUNNER_TEMP']) / 'soba-native-cache-sanitized'
        destination.mkdir(mode=0o700, exist_ok=False)
        output = destination
        if argv == ['archive']:
            measure_archives(plan, report)
        else:
            measure(plan, report)
        report['diagnosticPassed'] = True
    except (OSError, ValueError, KeyError, RuntimeError):
        report['failure'] = 'diagnostic_did_not_complete'
    finally:
        if output is not None:
            encoded = json.dumps(report, sort_keys=True, indent=2) + '\n'
            if len(encoded.encode('utf-8')) > ARCHIVE_RESULT_BYTES:
                report = {'schema': 3, 'diagnosticPassed': False, 'failure': 'sanitized_output_limit'}
                encoded = json.dumps(report) + '\n'
            (output / 'result.json').write_text(encoded, encoding='utf-8')
            with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as stream:
                print('sanitized_ready=true', file=stream)
    print('Native cache diagnostic completed.' if report['diagnosticPassed'] else 'Native cache diagnostic failed; raw diagnostics withheld.')
    return 0 if report['diagnosticPassed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
