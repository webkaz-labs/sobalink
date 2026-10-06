#!/usr/bin/env python3
"""Prove socket-free production boundary tests detect removed UDP/TCP guards."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile


def run(source: Path, go: str):
    source = source.resolve()
    policy = source / 'net/underlayguard/policy.go'
    original = policy.read_text(encoding='utf-8')
    udp = 'if !p.permitsUDP(ap) {'
    if original.count(udp) != 1:
        raise SystemExit('unexpected UDP guard source')
    tcp_start = '\tif err != nil || !validAddress(ap) || !slices.Contains(p.relays, ap) ||'
    if original.count(tcp_start) != 1:
        raise SystemExit('unexpected TCP guard source')
    start = original.index(tcp_start)
    end = original.index(' {\n', start)
    condition = original[start + len('\tif '):end]
    weakened = {
        'udp': original.replace(udp, 'if false && !p.permitsUDP(ap) {'),
        'tcp': original[:start] + '\tif false && (' + condition + ')'+original[end:],
    }
    controls = {
        'udp': ('./wgengine/magicsock', 'TestUnderlayGuardFakeUDPBoundaries', 'denied UDP did not fail closed'),
        'tcp': ('./net/underlayguard', 'TestTCPExactDestinationsAndFamilies', 'denied TCP reached dialer'),
    }
    env = os.environ.copy()
    env.update({'GOWORK': 'off', 'SOBALINK_RUN_UNDERLAY_NATIVE': '0'})
    with tempfile.TemporaryDirectory(prefix='underlay-negative-') as temporary:
        temporary = Path(temporary)
        for kind, body in weakened.items():
            altered = temporary / (kind + '.go')
            altered.write_text(body, encoding='utf-8')
            overlay = temporary / (kind + '.json')
            overlay.write_text(json.dumps({'Replace': {str(policy): str(altered)}}), encoding='utf-8')
            package, test, marker = controls[kind]
            result = subprocess.run([go, 'test', '-mod=readonly', '-count=1', '-timeout=45s',
                '-tags=ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy',
                '-overlay=' + str(overlay), '-run=^'+test+'$', package],
                # Compilation may be cold on a new native target; test runtime
                # remains bounded by Go's separate 45-second timeout.
                cwd=source, env=env, capture_output=True, text=True, timeout=300)
            output = result.stdout + result.stderr
            if result.returncode == 0 or ('--- FAIL: '+test) not in output or marker not in output:
                raise SystemExit(kind + ' negative control did not reach the expected test assertion')
            print(kind + ' guard-removal negative control: expected assertion failed')

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--go', default='go')
    args = parser.parse_args()
    run(args.source, args.go)
