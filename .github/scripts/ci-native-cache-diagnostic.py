#!/usr/bin/env python3
"""Restore-only native cache diagnostics. Never native/release acceptance.

Only fixed trusted-main objects are restored. Existing restored roots are never
modified. Owned private copies measure backend reuse and cold companion cost;
no cache namespace is written and no raw output or object content is exported.
"""
from collections import Counter
import hashlib
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
        deadline = start + BOUNDS[phase]
        observe = job.observe if posix else process.poll
        while (code := observe()) is None:
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
    if not match or member_size < match.end():
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


def copy_backend_candidate(source, destination):
    source, destination = Path(source), Path(destination)
    if destination.exists() or destination.is_symlink():
        raise ValueError('candidate destination must not exist')
    a, b = source.resolve(), destination.resolve()
    if a == b or a in b.parents or b in a.parents:
        raise ValueError('source and candidate must not overlap')
    # Finish admission before creating anything. The lab is not a security
    # boundary against concurrent hostile modification; run on an idle owned
    # snapshot, not a cache concurrently written by builds.
    admitted = list(entries(source))
    destination.mkdir(parents=False)
    copied, omitted = Counter(), Counter()
    for path, relative, size in admitted:
        if category(path) == 'go1.27.3_package_archive':
            omitted['files'] += 1
            omitted['bytes'] += size
            continue
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(path, target)
        copied['files'] += 1
        copied['bytes'] += size
    return {'schema': 1, 'copied': dict(copied), 'omitted': dict(omitted)}




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
    original_stamp, module_stamp = snapshot(original), snapshot(modules)
    temporary = Path(os.environ['RUNNER_TEMP']).resolve()
    require(temporary.is_dir() and not temporary.is_symlink())
    needed = 2 * original_inventory['bytes'] + module_inventory['bytes'] + 8 * 1024 ** 3
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
    for sample in range(ROUNDS):
        modes = ('baseline', 'split') if sample % 2 == 0 else ('split', 'baseline')
        for mode in modes:
            require(not prepared.exists())
            row = {'sample': sample + 1, 'mode': mode, 'phases': {}}
            report['samples'].append(row)
            candidate = Path(tempfile.mkdtemp(prefix='candidate-', dir=private))
            backend = candidate / 'backend'
            started = time.monotonic()
            if mode == 'baseline':
                full_copy(seed, backend)
            else:
                row['partition'] = copy_backend_candidate(seed, backend)
            row['copySeconds'] = round(time.monotonic() - started, 6)
            companion = backend if mode == 'baseline' else candidate / 'companion'
            if mode != 'baseline':
                companion.mkdir()
            env = workload_environment(backend, module_copy, candidate)
            run_workload(env, companion, row)
            row['after'] = {'backend': inventory(backend), 'companion': inventory(companion)}
            require(plan['source'] == collector.source_inventory())
            remove_owned_prepared(prepared)
            shutil.rmtree(candidate)
    require(snapshot(original) == original_stamp and snapshot(modules) == module_stamp)
    report['originalRootMetadataUnchanged'] = True
    # Module snapshots can contain read-only Go download/extraction entries.
    # They are private temporary diagnostic files; leave them to runner cleanup.
    require(plan['source'] == collector.source_inventory())


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    if len(argv) == 2 and argv[0] == '--owned':
        return owned_child(argv[1])
    if argv not in (['plan'], ['measure']):
        print('Native cache diagnostic requires its fixed entry point.')
        return 1
    report = {'schema': 1, 'diagnosticPassed': False, 'releaseValidation': False,
              'remoteCacheWrites': False, 'scope': 'cache-inventory-and-companion-recompilation-only',
              'samples': [], 'originalRootMetadataUnchanged': None,
              'limitations': ['no remote cache archive save or compressed restore comparison',
                  'comparison uses a separately prewarmed local seed at a relocated module path',
                  'warmup is reported separately and is not production warm-cache latency',
                  'local copy time is diagnostic setup, not cache restore time',
                  'only recognized Go 1.27.3 package objects are omitted',
                  'unknown cache data is retained; companion starts with an empty build cache',
                  'module copy is shared and warm; paired samples do not measure cold module downloads',
                  'source cache invariance uses file names, sizes and modification times',
                  'disk headroom is monitored, not a filesystem quota',
                  'test event accounting fails closed beyond 12 minutes despite the 15-minute command timeout',
                  'no package reproduction, full native coverage or release approval']}
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
        report.update(commit=plan['commit'], baselineCommit=BASE, platform=plan['platform'],
                      toolchains=['go1.27.1', 'go1.27.3'])
        destination = Path(os.environ['RUNNER_TEMP']) / 'soba-native-cache-sanitized'
        destination.mkdir(mode=0o700, exist_ok=False)
        output = destination
        measure(plan, report)
        report['diagnosticPassed'] = True
    except (OSError, ValueError, KeyError, RuntimeError):
        report['failure'] = 'diagnostic_did_not_complete'
    finally:
        if output is not None:
            (output / 'result.json').write_text(json.dumps(report, sort_keys=True, indent=2) + '\n', encoding='utf-8')
            with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as stream:
                print('sanitized_ready=true', file=stream)
    print('Native cache diagnostic completed.' if report['diagnosticPassed'] else 'Native cache diagnostic failed; raw diagnostics withheld.')
    return 0 if report['diagnosticPassed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
