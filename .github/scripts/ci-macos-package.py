#!/usr/bin/env python3
"""Compare exact-source original and UUID candidate packages on hosted macOS.

Diagnostic only: no required-CI/release/installed-device credit. Raw process
output stays in bounded memory and is never published. The ordinary Go linker
emits the candidate UUID before its normal ad-hoc signature; this driver only
inspects signatures and never signs, installs, or changes OS permissions.
"""
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import selectors
import shutil
import signal
import struct
import subprocess
import sys
import tarfile
import tempfile
import time

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
BASELINE = '2a775d546d418393de22597fb9a805f60b97bd7d'
BASELINE_TREE = '2fa25da5c2111c64fbdd4b20c3ad81f942b624d1'
VERSION = '0.0.0-dev.264'
GO_VERSION = 'go1.27.1'
GOFLAGS = '-mod=readonly -tags=ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy'
WORKFLOW = '.github/workflows/activation-diagnostic.yml'
ARTIFACT = 'soba-macos-package-sanitized'
# App/runtime inputs may not differ between lanes. All changes are the proposed
# producer/validator contract, its tests, this diagnostic, or its explanation.
DELTA = frozenset({WORKFLOW, '.github/scripts/ci-diagnostic.py',
    '.github/scripts/test_ci_diagnostic.py', '.github/scripts/ci-macos-package.py',
    '.github/scripts/test_ci_macos_package.py', '.github/scripts/source_provenance.py',
    '.github/scripts/test_source_provenance.py', 'internal/distribution/distribution.go',
    'internal/distribution/distribution_test.go', 'internal/distribution/native_build.go',
    'internal/distribution/native_build_test.go', 'docs/DISTRIBUTION.md'})
PROCESS_STATES = {'completed', 'nonzero', 'timeout', 'output_limit', 'launch_failed',
                  'capture_failed', 'child_remaining'}
STAGES = {'not_started', 'source', 'frontend', 'engine', 'build_first', 'build_second',
          'smoke', 'verify_source', 'complete'}
FAILURES = {'none', 'preflight_failed', 'source_changed', 'prepare_failed', 'build_failed',
    'checksums_failed', 'archive_invalid', 'validator_failed', 'macho_invalid',
    'smoke_failed', 'cleanup_failed', 'unknown_failure'}
STDERR_CLASSES = {'empty', 'dyld_missing_uuid', 'dyld_library_not_loaded',
    'dyld_symbol_not_found', 'codesign_valid', 'codesign_invalid', 'unknown'}
CAPTURE_LIMIT = 1024 * 1024
RUNTIME_LIMIT = 64 * 1024
TOTAL_SECONDS = 50 * 60
# The 55-minute step retains five minutes for group/worktree cleanup and export.
OWNERS_JOINED = True
SHA = re.compile(r'[0-9a-f]{40}\Z')
HASH = re.compile(r'[0-9a-f]{64}\Z')


def require(condition, failure='preflight_failed'):
    if not condition:
        raise DiagnosticFailure(failure)


class DiagnosticFailure(Exception):
    def __init__(self, failure, process=None):
        self.failure = failure if failure in FAILURES else 'unknown_failure'
        self.process = process


def digest(path):
    h = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(chunk)
    return h.hexdigest()


def module(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


def group_absent(pid):
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return True
    except PermissionError:
        return False
    return False


def capture(command, *, cwd=ROOT, env=None, timeout=30, limit=CAPTURE_LIMIT):
    """Bound time, both streams and the exclusively owned child process group."""
    global OWNERS_JOINED
    buffers, totals = [bytearray(), bytearray()], [0, 0]
    try:
        process = subprocess.Popen(command, cwd=cwd, env=env, shell=False,
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            start_new_session=True)
    except OSError:
        return 'launch_failed', None, b'', b'', [0, 0], True
    selector = selectors.DefaultSelector()
    for index, stream in enumerate((process.stdout, process.stderr)):
        os.set_blocking(stream.fileno(), False)
        selector.register(stream, selectors.EVENT_READ, index)
    deadline, stopped, state = time.monotonic() + timeout, None, None
    try:
        while selector.get_map() or process.poll() is None:
            now = time.monotonic()
            if stopped is None and now >= deadline:
                state, stopped = 'timeout', now
                try: os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError: pass
            if stopped is not None and now - stopped >= 5:
                state = state or 'capture_failed'
                break
            for key, _ in selector.select(0.05):
                data = os.read(key.fileobj.fileno(), 8192)
                if not data:
                    selector.unregister(key.fileobj)
                    key.fileobj.close()
                    continue
                index = key.data
                totals[index] += len(data)
                room = max(0, limit - len(buffers[index]))
                buffers[index].extend(data[:room])
                if len(data) > room and stopped is None:
                    state, stopped = 'output_limit', time.monotonic()
                    try: os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError: pass
        try:
            code = process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            code, state = None, 'capture_failed'
        absent = group_absent(process.pid)
        if not absent:
            state = state or 'child_remaining'
            try: os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError: pass
            deadline = time.monotonic() + 5
            while not group_absent(process.pid) and time.monotonic() < deadline:
                time.sleep(0.05)
            absent = group_absent(process.pid)
        if not absent: OWNERS_JOINED = False
        return state or ('completed' if code == 0 else 'nonzero'), code, bytes(buffers[0]), bytes(buffers[1]), totals, absent
    finally:
        selector.close()
        for stream in (process.stdout, process.stderr):
            stream.close()
        if process.poll() is None:
            try: os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError: pass
            try: process.wait(timeout=5)
            except subprocess.TimeoutExpired: pass
        if not group_absent(process.pid): OWNERS_JOINED = False


def stderr_class(raw, binary=None):
    if not raw:
        return 'empty', []
    try:
        lines = raw.decode('utf-8').splitlines()
    except UnicodeError:
        return 'unknown', []
    # Never copy a path, PID, missing symbol, library name, or arbitrary suffix.
    uuid = re.compile(r'dyld(?:\[[0-9]{1,10}\])?: missing LC_UUID(?: load command)?(?: in [^\r\n]+)?\Z')
    if lines and all(uuid.fullmatch(line) for line in lines):
        return 'dyld_missing_uuid', ['dyld: missing LC_UUID load command']
    if lines and re.fullmatch(r'dyld(?:\[[0-9]{1,10}\])?: Library not loaded: [^\r\n]+', lines[0]):
        return 'dyld_library_not_loaded', ['dyld: Library not loaded']
    if lines and re.fullmatch(r'dyld(?:\[[0-9]{1,10}\])?: Symbol not found: [^\r\n]+', lines[0]):
        return 'dyld_symbol_not_found', ['dyld: Symbol not found']
    if binary is not None:
        prefix = str(binary) + ': '
        if lines and all(line in (prefix + 'valid on disk', prefix + 'satisfies its Designated Requirement') for line in lines):
            return 'codesign_valid', ['codesign: ' + line[len(prefix):] for line in lines]
        if lines and all(line in (prefix + 'code object is not signed at all',
                prefix + 'invalid signature (code or signature have been modified)',
                prefix + 'a sealed resource is missing or invalid') for line in lines):
            return 'codesign_invalid', ['codesign: signature verification failed']
    return 'unknown', []


def process_record(observation, *, version=False, binary=None):
    state, code, stdout, stderr, totals, absent = observation
    kind, diagnostics = stderr_class(stderr, binary)
    expected = ('sobalink ' + VERSION + ' (soba)\n').encode()
    return {'state': state, 'returncode': code, 'groupAbsent': absent,
        'stdout': {'capturedBytes': len(stdout), 'observedBytes': totals[0],
            'sha256': hashlib.sha256(stdout).hexdigest(),
            'versionText': expected.decode().rstrip('\n') if version and stdout == expected else None},
        'stderr': {'capturedBytes': len(stderr), 'observedBytes': totals[1],
            'sha256': hashlib.sha256(stderr).hexdigest(), 'class': kind, 'diagnostics': diagnostics}}


def checked(command, *, cwd=ROOT, env=None, timeout=30, failure='preflight_failed'):
    observation = capture(command, cwd=cwd, env=env, timeout=timeout)
    if observation[0] != 'completed' or not observation[5]:
        raise DiagnosticFailure(failure, process_record(observation))
    return observation[2]


def source_identity(path, commit):
    actual = checked(['git', 'rev-parse', 'HEAD'], cwd=path).decode('ascii').strip()
    tree = checked(['git', 'rev-parse', 'HEAD^{tree}'], cwd=path).decode('ascii').strip()
    require(actual == commit and SHA.fullmatch(tree), 'source_changed')
    require(not checked(['git', 'status', '--porcelain', '--untracked-files=no'], cwd=path), 'source_changed')
    return tree


def bounded_macho(path):
    """Read only finite load-command and embedded CodeDirectory facts."""
    length = path.stat().st_size
    require(32 <= length <= 256 * 1024 * 1024, 'macho_invalid')
    with path.open('rb') as stream:
        header = stream.read(32)
        magic, cpu, _, filetype, count, size, _, _ = struct.unpack('<8I', header)
        require(magic == 0xfeedfacf and cpu == 0x100000c and filetype == 2
                and 0 < count <= 1024 and 0 < size <= 1024 * 1024 and 32 + size <= length, 'macho_invalid')
        commands = stream.read(size)
        offset, uuids, build, signature = 0, [], None, None
        for _ in range(count):
            require(offset + 8 <= size, 'macho_invalid')
            command, extent = struct.unpack_from('<2I', commands, offset)
            require(extent >= 8 and extent % 8 == 0 and offset + extent <= size, 'macho_invalid')
            if command == 0x1b:
                require(extent == 24, 'macho_invalid')
                uuids.append(commands[offset + 8:offset + 24].hex())
            elif command == 0x32:
                require(build is None and extent >= 24, 'macho_invalid')
                osid, minimum, sdk, tools = struct.unpack_from('<4I', commands, offset + 8)
                require(osid == 1 and extent == 24 + tools * 8, 'macho_invalid')
                build = {'minimum': minimum, 'sdk': sdk}
            elif command == 0x1d:
                require(signature is None and extent == 16, 'macho_invalid')
                signature = struct.unpack_from('<2I', commands, offset + 8)
            offset += extent
        require(offset == size and len(uuids) <= 1 and build is not None, 'macho_invalid')
        sign = {'present': signature is not None, 'metadataValid': False, 'adhoc': False,
                'linkerSigned': False, 'sha256': None}
        if signature is not None:
            start, size = signature
            require(32 + len(commands) <= start < length and 12 <= size <= 16 * 1024 * 1024
                    and start + size <= length, 'macho_invalid')
            stream.seek(start)
            raw = stream.read(size)
            sign['sha256'] = hashlib.sha256(raw).hexdigest()
            magic, extent, slots = struct.unpack_from('>3I', raw)
            require(magic == 0xfade0cc0 and 12 + slots * 8 <= extent <= size and 0 < slots <= 64, 'macho_invalid')
            directories = []
            for index in range(slots):
                kind, item = struct.unpack_from('>2I', raw, 12 + index * 8)
                require(12 + slots * 8 <= item <= extent - 8, 'macho_invalid')
                item_magic, item_size = struct.unpack_from('>2I', raw, item)
                require(8 <= item_size <= extent - item, 'macho_invalid')
                if kind == 0:
                    require(item_magic == 0xfade0c02 and item_size >= 44, 'macho_invalid')
                    directories.append(struct.unpack_from('>I', raw, item + 12)[0])
            require(len(directories) == 1, 'macho_invalid')
            flags = directories[0]
            sign.update(metadataValid=True, adhoc=bool(flags & 2), linkerSigned=bool(flags & 0x20000))
        return {'uuidCount': len(uuids), 'uuid': uuids[0] if uuids else None,
                'minimumOS': build['minimum'], 'sdk': build['sdk'], 'signature': sign}


def extract_archive(archive, destination):
    require(archive.is_file() and not archive.is_symlink() and archive.stat().st_size <= 256 * 1024 * 1024, 'archive_invalid')
    names, total = set(), 0
    with tarfile.open(archive, 'r:gz') as package:
        for entry in package:
            name = entry.name.rstrip('/')
            parts = name.split('/')
            require(name and not any(part in ('', '.', '..') for part in parts)
                    and not any(ch in name for ch in ('\\', ':', '\x00'))
                    and name not in names and len(names) < 20000, 'archive_invalid')
            names.add(name)
            target = destination.joinpath(*parts)
            if entry.isdir():
                target.mkdir(parents=True, exist_ok=True)
            elif entry.isfile():
                total += entry.size
                require(0 <= entry.size <= 128 * 1024 * 1024 and total <= 512 * 1024 * 1024, 'archive_invalid')
                target.parent.mkdir(parents=True, exist_ok=True)
                with package.extractfile(entry) as source, target.open('xb') as output:
                    shutil.copyfileobj(source, output, 1024 * 1024)
                require(target.stat().st_size == entry.size, 'archive_invalid')
            else:
                raise DiagnosticFailure('archive_invalid')


def verify_archive(lane, archive, extracted, commit):
    extract_archive(archive, extracted)
    verifier = module(lane / '.github/scripts/source_provenance.py', 'package_verifier_' + commit)
    share = extracted / 'share/sobalink'
    build = verifier.read_json((share / 'build.json').read_bytes())
    notices = verifier.read_json((share / 'third-party-notices.json').read_bytes())
    bom = verifier.read_json((share / 'bom.cdx.json').read_bytes())
    binary = extracted / 'bin/soba'
    require(build['product'] == 'sobalink' and build['version'] == VERSION
        and build['source_commit'] == commit and build['target'] == 'darwin-arm64'
        and build['binary']['path'] == 'bin/soba' and digest(binary) == build['binary']['sha256'], 'validator_failed')
    for name in ('LICENSE', 'README.md', 'README.en.md', 'SECURITY.md', 'go.mod', 'go.sum'):
        require((share / name).is_file(), 'validator_failed')
    require(bom['bomFormat'] == 'CycloneDX' and bom['specVersion'] == '1.5'
        and notices['modules'] and notices['frontend_modules'], 'validator_failed')
    for dependency in notices['modules'] + notices['frontend_modules'] + [notices['go_standard_library']]:
        require(dependency['notices'], 'validator_failed')
        for notice in dependency['notices']:
            relative = Path(notice['path'])
            require(not relative.is_absolute() and '..' not in relative.parts and '\\' not in notice['path'], 'validator_failed')
            require(digest(share / relative) == notice['sha256'], 'validator_failed')
    verifier.verify_native(build, notices, bom, binary)
    verifier.verify_sources(share, build, notices, bom)
    binary.chmod(0o755)
    return binary, build, verifier.native_digest(build['native_build'])


def empty_lane():
    return {'sourceCommit': None, 'sourceTree': None, 'stage': 'not_started',
        'failureClass': 'none', 'failureProcess': None, 'sourceVerified': False,
        'reproducible': False, 'builds': [], 'smoke': None}


def empty_result():
    return {'schema': 1, 'scope': 'paired-macos-package-loader', 'releaseValidation': False,
        'phase': 'preflight', 'sourceVerified': False, 'cleanupComplete': False,
        'diagnosticPassed': False, 'baselineAbortReproduced': False,
        'missingUUIDCauseObserved': False, 'candidateVerified': False,
        'matchedNativeIdentity': False, 'failureClass': 'none',
        'lanes': {'original': empty_lane(), 'candidate': empty_lane()}}


def lane_run(name, path, commit, private, report, *, deadline, epoch):
    report['sourceCommit'] = commit
    def remaining(maximum):
        require(time.monotonic() < deadline, 'prepare_failed')
        return min(maximum, deadline - time.monotonic())
    env = dict(os.environ, SOURCE_DATE_EPOCH=str(epoch), GOCACHEPROG='')
    try:
        report['stage'] = 'source'
        report['sourceTree'] = source_identity(path, commit)
        report['stage'] = 'frontend'
        checked([sys.executable, '-B', '.github/scripts/check-frontend.py'], cwd=path,
                env=env, timeout=remaining(600), failure='prepare_failed')
        report['stage'] = 'engine'
        for args in (['go', 'run', './cmd/prepare-engine'],
                     ['go', 'run', './cmd/prepare-engine', '--verify'], ['go', 'mod', 'verify']):
            checked(args, cwd=path, env=env, timeout=remaining(600), failure='prepare_failed')
        for index, label in enumerate(('first', 'second')):
            report['stage'] = 'build_' + label
            cache, temporary = private / (name + '-' + label + '-cache'), private / (name + '-' + label + '-tmp')
            cache.mkdir(); temporary.mkdir()
            build_env = dict(env, GOCACHE=str(cache), GOTMPDIR=str(temporary))
            checked(['go', 'run', './cmd/package-tool', 'build', VERSION, 'darwin', 'arm64', commit],
                    cwd=path, env=build_env, timeout=remaining(900), failure='build_failed')
            checked(['go', 'run', './cmd/package-tool', 'checksums'], cwd=path, env=build_env,
                    timeout=remaining(120), failure='checksums_failed')
            archive = path / ('dist/sobalink-' + VERSION + '-darwin-arm64.tar.gz')
            saved = private / (name + '-' + label + '.tar.gz')
            shutil.copyfile(archive, saved)
            extracted = private / (name + '-' + label + '-extracted')
            extracted.mkdir()
            binary, metadata, native_hash = verify_archive(path, saved, extracted, commit)
            item = {'archiveSha256': digest(saved), 'binarySha256': digest(binary),
                    'checksumsSha256': digest(path / 'dist/SHA256SUMS'),
                    'metadataSha256': digest(extracted / 'share/sobalink/build.json'),
                    'nativeSha256': native_hash, 'macho': bounded_macho(binary),
                    'codesign': None, 'version': None}
            report['builds'].append(item)
            item['codesign'] = process_record(capture(['/usr/bin/codesign', '--verify', '--strict', '--verbose=2', str(binary)],
                    cwd=path, env=build_env, timeout=remaining(30), limit=RUNTIME_LIMIT), binary=binary)
            require(item['codesign']['groupAbsent'] and OWNERS_JOINED, 'cleanup_failed')
            item['version'] = process_record(capture([str(binary), '--version'], cwd=path,
                    env=build_env, timeout=remaining(30), limit=RUNTIME_LIMIT), version=True)
            require(item['version']['groupAbsent'] and OWNERS_JOINED, 'cleanup_failed')
            require(digest(binary) == item['binarySha256'] and digest(saved) == item['archiveSha256'], 'source_changed')
        first, second = report['builds']
        report['reproducible'] = all(first[key] == second[key] for key in
            ('archiveSha256', 'binarySha256', 'checksumsSha256', 'metadataSha256', 'nativeSha256'))
        if name == 'candidate' and all(version_passed(item) for item in report['builds']):
            report['stage'] = 'smoke'
            report['smoke'] = process_record(capture([sys.executable, '-B', '.github/scripts/smoke-package.py',
                str(saved), VERSION], cwd=path, env=build_env, timeout=remaining(600)))
        if report['smoke'] is not None and report['smoke']['state'] != 'completed':
            report['failureClass'] = 'smoke_failed'
        require(OWNERS_JOINED, 'cleanup_failed')
        report['stage'] = 'verify_source'
        require(source_identity(path, commit) == report['sourceTree'], 'source_changed')
        # Both production builds and the candidate smoke use their own exact-source
        # verifier. Reverify source/byte bindings after all execution.
        for label, item in zip(('first', 'second'), report['builds']):
            require(digest(private / (name + '-' + label + '.tar.gz')) == item['archiveSha256']
                and digest(private / (name + '-' + label + '-extracted/bin/soba')) == item['binarySha256'], 'source_changed')
        report['sourceVerified'], report['stage'] = True, 'complete'
    except DiagnosticFailure as error:
        report['failureClass'], report['failureProcess'] = error.failure, error.process
    except (AssertionError, KeyError, ValueError, TypeError, OSError, UnicodeError, struct.error, tarfile.TarError):
        report['failureClass'] = 'validator_failed' if report['stage'].startswith('build_') else 'unknown_failure'


def version_passed(item):
    value = item['version']
    return value is not None and value['state'] == 'completed' and value['returncode'] == 0 and value['groupAbsent'] \
        and value['stdout']['versionText'] == 'sobalink ' + VERSION + ' (soba)' and value['stderr']['class'] == 'empty'


def signed(item):
    value, signature = item['codesign'], item['macho']['signature']
    return value is not None and value['state'] == 'completed' and value['returncode'] == 0 and value['groupAbsent'] \
        and signature['present'] and signature['metadataValid'] and signature['adhoc']


def candidate_uuid(commit, native_hash):
    verifier = module(Path(__file__).with_name('source_provenance.py'), 'diagnostic_uuid_identity')
    return verifier.darwin_build_uuid(commit, VERSION, native_hash)


def uuid_matches(item, commit):
    try:
        return item['macho']['uuidCount'] == 1 and item['macho']['uuid'] == candidate_uuid(commit, item['nativeSha256'])
    except (AssertionError, KeyError, TypeError, ValueError):
        return False


def evaluate(report):
    original, candidate = report['lanes']['original'], report['lanes']['candidate']
    observations = original['builds']
    report['baselineAbortReproduced'] = len(observations) == 2 and all(item['version'] is not None
        and item['version']['state'] == 'nonzero' and item['version']['returncode'] == -6
        and item['version']['groupAbsent'] for item in observations)
    report['missingUUIDCauseObserved'] = report['baselineAbortReproduced'] and original['reproducible'] \
        and original['sourceVerified'] and original['failureClass'] == 'none' \
        and all(item['macho']['uuidCount'] == 0 and item['version']['stderr']['class'] == 'dyld_missing_uuid'
                and signed(item) for item in observations)
    report['candidateVerified'] = candidate['stage'] == 'complete' and candidate['failureClass'] == 'none' \
        and candidate['sourceVerified'] and candidate['reproducible'] and len(candidate['builds']) == 2 \
        and all(version_passed(item) and signed(item) and uuid_matches(item, candidate['sourceCommit'])
                for item in candidate['builds']) and candidate['smoke'] is not None \
        and candidate['smoke']['state'] == 'completed' and candidate['smoke']['returncode'] == 0 and candidate['smoke']['groupAbsent']
    all_builds = observations + candidate['builds']
    report['matchedNativeIdentity'] = len(all_builds) == 4 and len({item['nativeSha256'] for item in all_builds}) == 1 \
        and len({item['macho']['sdk'] for item in all_builds}) == 1 and all(item['macho']['minimumOS'] == 13 << 16 for item in all_builds)
    processes = [lane[key] for lane in (original, candidate) for key in ('failureProcess', 'smoke')]
    processes += [item[key] for item in all_builds for key in ('codesign', 'version')]
    report['cleanupComplete'] = report['cleanupComplete'] and all(value is None or value['groupAbsent'] for value in processes)
    report['diagnosticPassed'] = report['failureClass'] == 'none' and report['sourceVerified'] and report['cleanupComplete'] \
        and original['sourceVerified'] and original['failureClass'] == 'none' \
        and report['missingUUIDCauseObserved'] and report['candidateVerified'] and report['matchedNativeIdentity']


def validate_export(report, provenance):
    """Validate closed schemas; arbitrary text is never merely redacted."""
    def shape(value, keys):
        require(type(value) is dict and set(value) == set(keys))
    def boolean(value): require(type(value) is bool)
    def integer(value, low=0, high=0xffffffff):
        require(type(value) is int and low <= value <= high)
    def hash_value(value, pattern=HASH, nullable=False):
        require((nullable and value is None) or (type(value) is str and pattern.fullmatch(value)))
    def process(value):
        if value is None: return
        shape(value, ('state', 'returncode', 'groupAbsent', 'stdout', 'stderr'))
        require(value['state'] in PROCESS_STATES)
        if value['returncode'] is not None: integer(value['returncode'], -255, 255)
        boolean(value['groupAbsent'])
        if value['state'] == 'completed': require(value['returncode'] == 0 and value['groupAbsent'])
        if value['state'] == 'nonzero': require(type(value['returncode']) is int and value['returncode'] != 0)
        for key, extra in (('stdout', ('versionText',)), ('stderr', ('class', 'diagnostics'))):
            stream = value[key]
            shape(stream, ('capturedBytes', 'observedBytes', 'sha256', *extra))
            integer(stream['capturedBytes'], high=CAPTURE_LIMIT)
            integer(stream['observedBytes'], stream['capturedBytes'], 2 ** 31)
            hash_value(stream['sha256'])
        out = value['stdout']
        if out['versionText'] is not None:
            expected = ('sobalink ' + VERSION + ' (soba)\n').encode()
            require(out['versionText'] == expected.decode().rstrip('\n')
                and out['capturedBytes'] == out['observedBytes'] == len(expected)
                and out['sha256'] == hashlib.sha256(expected).hexdigest())
        err = value['stderr']
        diagnostics = {'empty': [], 'unknown': [],
            'dyld_missing_uuid': ['dyld: missing LC_UUID load command'],
            'dyld_library_not_loaded': ['dyld: Library not loaded'],
            'dyld_symbol_not_found': ['dyld: Symbol not found'],
            'codesign_valid': ['codesign: valid on disk', 'codesign: satisfies its Designated Requirement'],
            'codesign_invalid': ['codesign: signature verification failed']}
        require(err['class'] in diagnostics and type(err['diagnostics']) is list)
        if err['class'] == 'codesign_valid':
            require(0 < len(err['diagnostics']) <= 2 and len(set(err['diagnostics'])) == len(err['diagnostics'])
                and all(line in diagnostics['codesign_valid'] for line in err['diagnostics']))
        else: require(err['diagnostics'] == diagnostics[err['class']])
        if err['class'] == 'empty':
            require(err['capturedBytes'] == err['observedBytes'] == 0 and err['sha256'] == hashlib.sha256(b'').hexdigest())
    shape(report, empty_result())
    require(type(report['schema']) is int and report['schema'] == 1
        and report['scope'] == 'paired-macos-package-loader' and report['releaseValidation'] is False
        and report['phase'] in {'preflight', 'pair', 'complete'} and report['failureClass'] in FAILURES)
    for key in ('sourceVerified', 'cleanupComplete', 'diagnosticPassed', 'baselineAbortReproduced',
            'missingUUIDCauseObserved', 'candidateVerified', 'matchedNativeIdentity'):
        boolean(report[key])
    shape(report['lanes'], ('original', 'candidate'))
    for lane in report['lanes'].values():
        shape(lane, empty_lane())
        require(lane['stage'] in STAGES and lane['failureClass'] in FAILURES and type(lane['builds']) is list and len(lane['builds']) <= 2)
        hash_value(lane['sourceCommit'], SHA, True); hash_value(lane['sourceTree'], SHA, True)
        boolean(lane['sourceVerified']); boolean(lane['reproducible'])
        if lane['sourceVerified']: require(lane['sourceCommit'] is not None and lane['sourceTree'] is not None)
        process(lane['failureProcess']); process(lane['smoke'])
        for item in lane['builds']:
            shape(item, ('archiveSha256', 'binarySha256', 'checksumsSha256', 'metadataSha256',
                         'nativeSha256', 'macho', 'codesign', 'version'))
            for key in ('archiveSha256', 'binarySha256', 'checksumsSha256', 'metadataSha256', 'nativeSha256'):
                hash_value(item[key])
            shape(item['macho'], ('uuidCount', 'uuid', 'minimumOS', 'sdk', 'signature'))
            macho = item['macho']
            integer(macho['uuidCount'], 0, 1)
            require((macho['uuidCount'] == 0 and macho['uuid'] is None) or
                (macho['uuidCount'] == 1 and type(macho['uuid']) is str and re.fullmatch('[0-9a-f]{32}', macho['uuid'])))
            integer(macho['minimumOS']); integer(macho['sdk'])
            sign = macho['signature']
            shape(sign, ('present', 'metadataValid', 'adhoc', 'linkerSigned', 'sha256'))
            for key in ('present', 'metadataValid', 'adhoc', 'linkerSigned'): boolean(sign[key])
            hash_value(sign['sha256'], nullable=True)
            require(not sign['metadataValid'] or (sign['present'] and sign['sha256'] is not None))
            require(not (sign['adhoc'] or sign['linkerSigned']) or sign['metadataValid'])
            process(item['codesign']); process(item['version'])
        if lane['reproducible']:
            require(len(lane['builds']) == 2 and all(lane['builds'][0][key] == lane['builds'][1][key]
                for key in ('archiveSha256', 'binarySha256', 'checksumsSha256', 'metadataSha256', 'nativeSha256')))
    shape(provenance, ('schema', 'baselineCommit', 'baselineTree', 'candidateCommit', 'candidateTree',
        'version', 'goVersion', 'sourceDateEpoch', 'changedFiles', 'runId', 'runAttempt', 'repository', 'workflow', 'job'))
    require(type(provenance['schema']) is int and provenance['schema'] == 1
        and provenance['baselineCommit'] == BASELINE and provenance['baselineTree'] == BASELINE_TREE
        and provenance['version'] == VERSION and provenance['goVersion'] == GO_VERSION
        and provenance['repository'] == 'webkaz-labs/sobalink' and provenance['workflow'] == WORKFLOW
        and provenance['job'] == 'diagnostic-macos-package')
    hash_value(provenance['candidateCommit'], SHA, True); hash_value(provenance['candidateTree'], SHA, True)
    if provenance['sourceDateEpoch'] is not None: integer(provenance['sourceDateEpoch'])
    for key in ('runId', 'runAttempt'):
        if provenance[key] is not None: integer(provenance[key], low=1, high=10 ** 20)
    require(type(provenance['changedFiles']) is list and len(provenance['changedFiles']) == len(set(provenance['changedFiles']))
        and all(type(path) is str and path in DELTA for path in provenance['changedFiles']))
    for name, commit, tree in (('original', BASELINE, BASELINE_TREE),
            ('candidate', provenance['candidateCommit'], provenance['candidateTree'])):
        lane = report['lanes'][name]
        if lane['sourceCommit'] is not None: require(lane['sourceCommit'] == commit)
        if lane['sourceTree'] is not None: require(lane['sourceTree'] == tree)
    if report['sourceVerified']:
        require(provenance['candidateCommit'] is not None and provenance['candidateTree'] is not None)
    recalculated = copy.deepcopy(report)
    evaluate(recalculated)
    require(recalculated == report)


def preflight():
    require(platform.system() == 'Darwin' and platform.machine() == 'arm64')
    env = os.environ
    require(env.get('GITHUB_ACTIONS') == 'true' and env.get('GITHUB_JOB') == 'diagnostic-macos-package'
        and env.get('RUNNER_OS') == 'macOS' and env.get('RUNNER_ARCH') == 'ARM64'
        and env.get('GITHUB_WORKFLOW') == 'Activation diagnostics (not release validation)'
        and env.get('GOTOOLCHAIN') == 'local' and env.get('GOWORK') == 'off' and env.get('GOFLAGS') == GOFLAGS)
    commit = checked(['git', 'rev-parse', 'HEAD']).decode('ascii').strip()
    event = json.loads(Path(env['GITHUB_EVENT_PATH']).read_text(encoding='utf-8'))
    selection = module(ROOT / '.github/scripts/ci-diagnostic.py', 'macos_package_selection')
    require(selection.select(env['GITHUB_EVENT_NAME'], event, env['GITHUB_REPOSITORY'],
        env['GITHUB_REF'], env['GITHUB_SHA'], commit) == 'macos-package')
    require(env.get('GITHUB_WORKFLOW_REF') == 'webkaz-labs/sobalink/' + WORKFLOW + '@' + env['GITHUB_REF'])
    tree = source_identity(ROOT, commit)
    require(not checked(['git', 'ls-files', '--others', '--exclude-standard', '-z']))
    require(checked(['git', 'rev-parse', BASELINE + '^{tree}']).decode('ascii').strip() == BASELINE_TREE)
    checked(['git', 'merge-base', '--is-ancestor', BASELINE, commit])
    changed = checked(['git', 'diff', '--name-only', BASELINE, commit]).decode('utf-8').splitlines()
    require(set(changed) <= DELTA and 'internal/distribution/distribution.go' in changed
            and 'internal/distribution/native_build.go' in changed and '.github/scripts/source_provenance.py' in changed)
    require(checked(['go', 'env', 'GOVERSION']).decode().strip() == GO_VERSION
        and checked(['go', 'env', 'GOHOSTOS', 'GOHOSTARCH']).decode().strip() == 'darwin\narm64'
        and checked(['node', '--version']).decode().strip() == 'v24.19.0'
        and checked(['npm', '--version']).decode().strip() == '11.9.0')
    epoch = int(checked(['git', 'log', '-1', '--format=%ct', BASELINE]).decode('ascii').strip())
    require(0 <= epoch <= 0xffffffff)
    for key in ('GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT'):
        require(re.fullmatch('[1-9][0-9]{0,19}', env.get(key, '')))
    return commit, tree, epoch, sorted(changed)


def main():
    global OWNERS_JOINED
    OWNERS_JOINED = True
    deadline = time.monotonic() + TOTAL_SECONDS
    report, provenance = empty_result(), {'schema': 1, 'baselineCommit': BASELINE, 'baselineTree': BASELINE_TREE,
        'candidateCommit': None, 'candidateTree': None, 'version': VERSION, 'goVersion': GO_VERSION,
        'sourceDateEpoch': None, 'changedFiles': [], 'runId': None, 'runAttempt': None,
        'repository': 'webkaz-labs/sobalink', 'workflow': WORKFLOW, 'job': 'diagnostic-macos-package'}
    private, worktrees, export = None, [], None
    try:
        temporary = Path(os.environ['RUNNER_TEMP']).resolve()
        require(temporary.is_dir())
        export = temporary / ARTIFACT
        require(not export.exists())
        export.mkdir(mode=0o700)
        commit, tree, epoch, changed = preflight()
        provenance.update(candidateCommit=commit, candidateTree=tree, sourceDateEpoch=epoch, changedFiles=changed,
            runId=int(os.environ['GITHUB_RUN_ID']), runAttempt=int(os.environ['GITHUB_RUN_ATTEMPT']))
        private = Path(tempfile.mkdtemp(prefix='soba-package-pair-', dir=temporary))
        report['phase'] = 'pair'
        for name, source in (('original', BASELINE), ('candidate', commit)):
            path = private / name
            checked(['git', 'worktree', 'add', '--detach', str(path), source], timeout=30)
            worktrees.append(path)
            lane_run(name, path, source, private, report['lanes'][name], deadline=deadline, epoch=epoch)
            require(OWNERS_JOINED, 'cleanup_failed')
        require(source_identity(ROOT, commit) == tree, 'source_changed')
        report['sourceVerified'] = True
    except DiagnosticFailure as error:
        report['failureClass'] = error.failure
    except Exception:
        report['failureClass'] = 'unknown_failure'
    finally:
        clean = OWNERS_JOINED
        # Do not delete a workspace still owned by a child whose cleanup failed.
        for path in worktrees if OWNERS_JOINED else ():
            try:
                checked(['git', 'worktree', 'remove', '--force', str(path)], timeout=60, failure='cleanup_failed')
            except Exception:
                clean = False
        if private is not None and OWNERS_JOINED:
            try: shutil.rmtree(private)
            except OSError: clean = False
        report['cleanupComplete'] = clean and OWNERS_JOINED
        if not report['cleanupComplete']: report['failureClass'] = 'cleanup_failed'
        report['phase'] = 'complete'
        evaluate(report)
    try:
        require(export is not None)
        validate_export(report, provenance)
        (export / 'result.json').write_text(json.dumps(report, sort_keys=True) + '\n', encoding='utf-8')
        (export / 'provenance.json').write_text(json.dumps(provenance, sort_keys=True) + '\n', encoding='utf-8')
        with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
            output.write('sanitized_ready=true\n')
    except Exception:
        report['diagnosticPassed'] = False
    print('PASS: paired loader diagnosis and candidate package proof verified.' if report['diagnosticPassed'] else
        'FAIL: paired package diagnostic incomplete; consult finite sanitized lane outcomes.')
    return 0 if report['diagnosticPassed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
