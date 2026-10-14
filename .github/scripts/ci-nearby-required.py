#!/usr/bin/env python3
"""Require native nearby RUN/PASS proof on qualified Linux CI targets only."""
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import sys
import tempfile

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
QUALIFIED_TARGETS = ('linux-amd64', 'linux-arm64')


def native_module():
    path = Path(__file__).with_name('ci-nearby-native.py')
    spec = importlib.util.spec_from_file_location('ci_nearby_native', path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


def main(argv=None):
    if ((sys.argv[1:] if argv is None else argv) or sys.platform != 'linux'
            or os.environ.get('GITHUB_ACTIONS') != 'true'):
        print('FAIL: fixed Linux native CI entry required.')
        return 1
    evidence, verified, private = None, False, None
    try:
        os.umask(0o077)
        native = native_module()
        shared = native.module('ci-nearby-diagnostic')
        key = os.environ.get('SOBALINK_NEARBY_NATIVE_PLATFORM')
        if key not in QUALIFIED_TARGETS:
            raise ValueError('target has no qualified required gate')
        versions = shared.parse_json(native.checked(['go', 'env', '-json', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH', 'GOOS', 'GOARCH', 'CGO_ENABLED']))
        native.identities(key, versions)
        commit = native.checked(['git', 'rev-parse', 'HEAD']).decode().strip()
        if not re.fullmatch('[0-9a-f]{40}', commit) or commit != os.environ.get('GITHUB_SHA'):
            raise ValueError('unbound native source')
        native.checked(['git', 'diff', '--quiet', 'HEAD', '--'])
        # Normal CI already prepared its locked dependencies. No broad clean or
        # extra download is needed; reject extra files in this compiled package.
        if native.checked(['git', 'ls-files', '--others', '-z', '--', 'internal/nearby']):
            raise ValueError('unexpected nearby source input')
        private = Path(tempfile.mkdtemp(prefix='soba-nearby-required-', dir=os.environ['RUNNER_TEMP']))
        env = dict(os.environ, GOTOOLCHAIN='local', GOWORK='off', GOTELEMETRY='off',
                   GOPROXY='off', GOSUMDB='off', GOFLAGS='-mod=readonly -buildvcs=false',
                   CGO_ENABLED='1', TMPDIR=str(private), TMP=str(private), TEMP=str(private))
        binary = private / 'nearby.test'
        native.checked(['go', 'test', '-race', '-c', '-o', str(binary), './internal/nearby'], env=env, timeout=300)
        binary_hash = native.digest(binary)
        env['SOBALINK_NATIVE_NEARBY_LOOPBACK'] = '1'
        state, code, stdout, _ = native.capture([str(binary), '-test.v=test2json',
            '-test.run=^' + native.TEST + '$', '-test.count=1', '-test.timeout=20s'],
            env=env, timeout=30, limit=native.RUNTIME_BYTES)
        if state in ('completed', 'nonzero'):
            converted, _, data, _ = native.capture(['go', 'tool', 'test2json', '-p', native.PACKAGE],
                env=env, timeout=15, limit=2 * native.RUNTIME_BYTES, stdin=stdout)
            if converted == 'completed':
                evidence, accepted = native.native_events(data, code)
                verified = accepted and state == 'completed' and evidence['status'] == 'verified'
        native.checked(['git', 'diff', '--quiet', 'HEAD', '--'])
        if (native.checked(['git', 'ls-files', '--others', '-z', '--', 'internal/nearby'])
                or binary_hash != native.digest(binary)):
            raise ValueError('native source or binary changed')
    except Exception:
        verified = False
    finally:
        if private is not None:
            try:
                shutil.rmtree(private)
            except OSError:
                verified = False
    # The original closed failure/unavailable marker is retained, but neither
    # an unavailable skip nor an incomplete lifecycle can satisfy required CI.
    print(json.dumps({'schema': 1, 'nativeDiscoveryVerified': verified,
                      'nativeDiscovery': evidence, 'physicalLANVerified': False,
                      'automaticCoreBrowserVerified': False, 'releaseValidation': False}, sort_keys=True))
    print('PASS: native discovery RUN/PASS and cleanup verified.' if verified else
          'FAIL: required native discovery proof missing; private details withheld.')
    return 0 if verified else 1


if __name__ == '__main__':
    raise SystemExit(main())
