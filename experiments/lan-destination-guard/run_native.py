#!/usr/bin/env python3
"""Run bounded native experiment binaries; emit only an allowlisted aggregate."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess

TESTS = {
    'udp': ['TestLANExperimentFakeEntryPoints', 'TestLANExperimentFakeRetryRevocation', 'TestLANExperimentFakeNilPolicy', 'TestLANExperimentFakeIPv6EntryPoints', 'TestLANExperimentNativeLoopback', 'TestLANExperimentNativeIPv6Loopback'],
    'tcp': ['TestLANRemainingTCPFakeBoundaries', 'TestLANRemainingTCPNativeLoopback'],
    'diagnostics': ['TestLANRemainingDiagnosticsDisabled', 'TestLANRemainingDNSFallbackDenied'],
}
MARKERS = {
    'udp': ['native_allowed_datagrams=7 native_denied_entrypoints=5 native_policy_revocations=1', 'native_ipv6_allowed_datagrams=7 native_ipv6_denied_entrypoints=5 native_ipv6_policy_revocations=1'],
    'tcp': ['native_tcp_allowed_connections=4 native_tcp_denied_entrypoints=4 native_tcp_revocations=2 native_tcp_ipv6=true'],
    'diagnostics': [],
}

def run(binary, group, platform):
    result = subprocess.run([str(binary.resolve()), '-test.v', '-test.timeout=60s', '-test.run=^TestLAN'], env=dict(os.environ, LAN_GUARD_REAL_SOCKETS='1'), text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=75)
    tests = TESTS[group] + (['TestLANRemainingRawDiscoDisabled'] if group == 'udp' and platform == 'linux' else [])
    successful = [name for name in tests if '--- PASS: '+name+' ' in result.stdout]
    skipped = any('--- SKIP: '+name+' ' in result.stdout for name in tests)
    ok = result.returncode == 0 and successful == tests and not skipped and all(marker in result.stdout for marker in MARKERS[group])
    return {
        'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
        'tests_passed': successful,
        'passed': ok,
        'batching_available': group == 'udp' and 'native_batching=true' in result.stdout,
    }

def main():
    p = argparse.ArgumentParser()
    for group in TESTS: p.add_argument('--'+group+'-binary',type=Path,required=True)
    p.add_argument('--platform',choices=['linux','darwin','windows'],required=True)
    p.add_argument('--architecture',choices=['amd64','arm64'],required=True)
    p.add_argument('--report',type=Path,required=True)
    args = p.parse_args()
    groups = {group: run(getattr(args,group+'_binary'),group,args.platform) for group in TESTS}
    passed = all(value['passed'] for value in groups.values())
    report = {
        'schema': 'lan-write-boundary-experiment-v2',
        'engine': 'tailscale.com@v1.104.0',
        'platform': args.platform, 'architecture': args.architecture,
        'groups': groups,
        'native_udp_ipv4_ipv6': groups['udp']['passed'],
        'native_tcp_ipv4_ipv6': groups['tcp']['passed'],
        'diagnostics_disabled_tested': groups['diagnostics']['passed'],
        'full_engine_packages': True,
        'host_no_egress_proven': False,
        'physical_lan_proven': False,
        'encrypted_peer_transfer_proven': False,
        'icmp_transmission_tested': False,
        'passed': passed,
    }
    args.report.parent.mkdir(parents=True,exist_ok=True)
    args.report.write_text(json.dumps(report,indent=2)+'\n')
    print(json.dumps(report,sort_keys=True))
    if not passed: raise SystemExit('LAN experiment failed; no no-egress claim is made')
if __name__=='__main__': main()
