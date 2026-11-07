#!/usr/bin/env python3
"""Opt-in, private, compile-only browser cache model; never acceptance proof."""
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
OMIT = 'ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy'
GOFLAGS = '-mod=readonly -tags=' + OMIT
UI_TAGS = 'soba_e2e,' + OMIT
# These six reviewed variants model the native-browser lane's race builds.
# Tests themselves are deliberately not executed or represented as passed.
RACE_TARGETS = (
    ('fixture', './cmd/soba-e2e', UI_TAGS),
    ('core-diagnostics', './internal/core', 'lanlink_integration,ts_omit_udptransport,' + OMIT),
    ('inspection', './internal/directlan', 'resource_inspection_native,' + OMIT),
    ('ui-launch', './internal/webui', OMIT),
    ('core-browser', './internal/core', 'lanlink_integration,directlan_integration,' + OMIT),
    ('core-nearby', './internal/core', 'lanlink_integration,directlan_integration,' + OMIT + ',ts_omit_udptransport'),
)
PHASES = {'prepare', 'verify-engine', 'verify-modules', 'verify-prewarmed-modules',
          'writer-prewarm', 'consumer-baseline', 'consumer-prewarmed',
          'archive-cache-baseline', 'archive-cache-prewarmed'} | {'race-' + row[0] for row in RACE_TARGETS}


class ProfileFailure(ValueError):
    def __init__(self, phase):
        if phase not in PHASES:
            raise ValueError('unknown profile phase')
        self.phase = phase


def require(condition):
    if not condition:
        raise ValueError('profile validation failed')


def query(*command):
    return subprocess.check_output(command, cwd=ROOT, text=True, timeout=30).strip()


def context(env):
    sha = env.get('GITHUB_SHA', '')
    require(re.fullmatch('[0-9a-f]{40}', sha) is not None)
    branch = re.fullmatch(r'refs/heads/profile-browser-cache/([0-9a-f]{12})-(bp|pb)', env.get('GITHUB_REF', ''))
    require(branch is not None and branch[1] == sha[:12])
    require(env.get('GITHUB_REPOSITORY') == 'webkaz-labs/sobalink'
            and env.get('GITHUB_EVENT_NAME') == 'create'
            and env.get('GITHUB_WORKFLOW') == 'Browser cache profile (not acceptance)'
            and env.get('GITHUB_JOB') == 'profile'
            and env.get('RUNNER_OS') == 'Linux' and env.get('RUNNER_ARCH') == 'X64'
            and env.get('GOTOOLCHAIN') == 'local' and env.get('GOWORK') == 'off'
            and env.get('GOFLAGS') == GOFLAGS)
    run, attempt = env.get('GITHUB_RUN_ID', ''), env.get('GITHUB_RUN_ATTEMPT', '')
    require(re.fullmatch('[1-9][0-9]{0,19}', run) and re.fullmatch('[1-9][0-9]{0,5}', attempt))
    require(query('git', 'rev-parse', 'HEAD') == sha
            and Path(query('git', 'rev-parse', '--show-toplevel')).resolve() == ROOT
            and not query('git', 'status', '--porcelain', '--untracked-files=no'))
    versions = json.loads(query('go', 'env', '-json', 'GOVERSION', 'GOOS', 'GOARCH',
                               'GOHOSTOS', 'GOHOSTARCH', 'CGO_ENABLED', 'CC'))
    require(versions == {'GOVERSION': 'go1.27.1', 'GOOS': 'linux', 'GOARCH': 'amd64',
                         'GOHOSTOS': 'linux', 'GOHOSTARCH': 'amd64', 'CGO_ENABLED': '1', 'CC': 'gcc'})
    image = env.get('ImageVersion', '')
    require(re.fullmatch('[0-9][0-9.]{0,50}', image) is not None and 'GOMAXPROCS' not in env)
    compressor = re.search(r'v([0-9]+\.[0-9]+\.[0-9]+)', query('zstd', '--version'))
    require(compressor is not None)
    return {'commit': sha, 'tree': query('git', 'rev-parse', 'HEAD^{tree}'),
            'run_id': int(run), 'run_attempt': int(attempt), 'order': branch[2],
            'go': versions, 'cpu_count': os.cpu_count(), 'runner_image_version': image,
            'zstd_version': compressor[1],
            'workflow_sha256': hashlib.sha256((ROOT / '.github/workflows/browser-cache-profile.yml').read_bytes()).hexdigest(),
            'ci_workflow_sha256': hashlib.sha256((ROOT / '.github/workflows/ci.yml').read_bytes()).hexdigest()}


def inventory(path):
    rows = []
    for item in sorted(path.rglob('*')):
        require(not item.is_symlink())
        if item.is_file():
            st = item.stat()
            rows.append((item.relative_to(path).as_posix(), st.st_size, st.st_mtime_ns))
        else:
            require(item.is_dir())
    return rows


def measured(label, command, env, private, timings, timeout=600):
    require(label in PHASES)
    print('Measuring ' + label, flush=True)
    start = time.monotonic()
    with (private / (label + '.private.log')).open('xb') as output:
        try:
            subprocess.run(command, cwd=ROOT, env=env, stdin=subprocess.DEVNULL,
                           stdout=output, stderr=subprocess.STDOUT, check=True, timeout=timeout)
        except subprocess.SubprocessError:
            raise ProfileFailure(label) from None
    timings[label] = round(time.monotonic() - start, 6)
    print('Finished ' + label, flush=True)


def archive_estimate(cache, modules, private):
    # Match tar+zstd's broad format, but normalize metadata and names so only
    # cache content changes the estimate. No archive leaves this private job.
    require(cache.parent == modules.parent == private)
    require(cache.name in ('cache-baseline', 'cache-prewarmed') and modules.name == 'modules')
    rows = inventory(cache)
    tar = ['tar', '--posix', '--sort=name', '--mtime=@0', '--owner=0', '--group=0',
           '--numeric-owner', '--pax-option=delete=atime,delete=ctime',
           '--transform=s,^' + cache.name + ',go-build,', '-C', str(private),
           '-cf', '-', modules.name, cache.name]
    pipeline = shlex.join(tar) + ' | zstd -T0 -q | wc -c'
    start = time.monotonic()
    with (private / (cache.name + '-archive.private.log')).open('xb') as error:
        try:
            size = subprocess.check_output(['bash', '-o', 'pipefail', '-c', pipeline],
                                           stderr=error, text=True, timeout=180).strip()
        except subprocess.SubprocessError:
            raise ProfileFailure('archive-' + cache.name) from None
    require(re.fullmatch('[1-9][0-9]*', size) is not None)
    return {'build_cache_files': len(rows), 'build_cache_bytes': sum(row[1] for row in rows),
            'module_and_build_zstd_bytes_estimate': int(size),
            'compression_seconds': round(time.monotonic() - start, 6)}


def build_fixture(label, cache, env, private, timings, *, offline=True):
    child = dict(env, GOCACHE=str(cache))
    if offline:
        child.update(GOPROXY='off', GOSUMDB='off')
    target = private / (label + '.binary')
    require(not target.exists())
    measured(label, ['go', 'build', '-tags=' + UI_TAGS, '-o', str(target), './cmd/soba-e2e'],
             child, private, timings)
    return hashlib.sha256(target.read_bytes()).hexdigest()


def profile(private, identity, env):
    baseline, modules = private / 'cache-baseline', private / 'modules'
    baseline.mkdir(mode=0o700)
    modules.mkdir(mode=0o700)
    child = dict(env, GOCACHE=str(baseline), GOMODCACHE=str(modules), GOCACHEPROG='', GOENV='off')
    timings = {}
    measured('prepare', ['go', 'run', './cmd/prepare-engine'], child, private, timings)
    measured('verify-engine', ['go', 'run', './cmd/prepare-engine', '--verify'], child, private, timings)
    for label, package, tags in RACE_TARGETS:
        measured('race-' + label,
                 ['go', 'test', '-c', '-race', '-tags=' + tags, '-o', str(private / (label + '.test')), package],
                 child, private, timings)
    measured('verify-modules', ['go', 'mod', 'verify'], child, private, timings)
    original_modules = inventory(modules)
    before = archive_estimate(baseline, modules, private)
    prewarmed = private / 'cache-prewarmed'
    shutil.copytree(baseline, prewarmed)
    # A genuine new seed may also lack non-race-only module downloads. Include
    # those in writer cost and archive growth, then freeze modules for both
    # consumers so their timed difference isolates compilation reuse.
    expected_binary = build_fixture('writer-prewarm', prewarmed, child, private, timings, offline=False)
    measured('verify-prewarmed-modules', ['go', 'mod', 'verify'],
             dict(child, GOCACHE=str(prewarmed)), private, timings)
    frozen_modules = inventory(modules)
    after = archive_estimate(prewarmed, modules, private)
    consumer_order = ('baseline', 'prewarmed') if identity['order'] == 'bp' else ('prewarmed', 'baseline')
    seeds = {'baseline': baseline, 'prewarmed': prewarmed}
    for name in consumer_order:
        # Independent regular-file copies, never shared hardlinks. Seed B and P
        # stay untouched by consumers, including when consumer order reverses.
        destination = private / ('consumer-' + name)
        shutil.copytree(seeds[name], destination)
        try:
            actual = build_fixture('consumer-' + name, destination, child, private, timings)
            require(actual == expected_binary and inventory(modules) == frozen_modules)
        finally:
            # Only this experiment's disposable consumer copy is removed.
            shutil.rmtree(destination)
    require(query('git', 'rev-parse', 'HEAD') == identity['commit']
            and not query('git', 'status', '--porcelain', '--untracked-files=no'))
    return {'schema': 1, 'experiment': 'browser-nonrace-seed-v1', 'success': True,
            'acceptance_validated': False, 'production_cache_written': False,
            'remote_transfer_measured': False, 'seed_model': 'six-native-race-compile-variants',
            'consumer_module_condition': 'post-prewarm-fixed-offline',
            'seed_compile_targets': [{'package': package, 'tags': tags, 'race': True}
                                     for _, package, tags in RACE_TARGETS],
            'identity': identity, 'go_flags': GOFLAGS, 'fixture_package': './cmd/soba-e2e', 'fixture_tags': UI_TAGS,
            'modules_files': len(frozen_modules), 'modules_bytes': sum(row[1] for row in frozen_modules),
            'module_bytes_added_by_prewarm': sum(row[1] for row in frozen_modules) - sum(row[1] for row in original_modules),
            'baseline': before, 'prewarmed': after, 'seconds': timings,
            'consumer_builds_identical': True,
            'consumer_build_seconds_saved': round(timings['consumer-baseline'] - timings['consumer-prewarmed'], 6),
            'archive_bytes_added_estimate': after['module_and_build_zstd_bytes_estimate'] - before['module_and_build_zstd_bytes_estimate']}


def main():
    identity = context(os.environ)
    os.umask(0o077)
    export = Path(os.environ['RUNNER_TEMP']) / 'browser-cache-profile'
    export.mkdir(mode=0o700, exist_ok=False)
    private = Path(tempfile.mkdtemp(prefix='browser-cache-profile-private-', dir=os.environ['RUNNER_TEMP']))
    try:
        result = profile(private, identity, os.environ)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        # Neither compiler output nor local paths/exception text are published.
        result = {'schema': 1, 'experiment': 'browser-nonrace-seed-v1', 'success': False,
                  'acceptance_validated': False, 'production_cache_written': False,
                  'identity': identity, 'failure': 'profile_incomplete',
                  'phase': error.phase if isinstance(error, ProfileFailure) else 'validation'}
    (export / 'result.json').write_text(json.dumps(result, indent=2, allow_nan=False) + '\n', encoding='utf-8')
    print('Compile-only profile ' + ('completed' if result['success'] else 'incomplete'))
    return 0 if result['success'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
