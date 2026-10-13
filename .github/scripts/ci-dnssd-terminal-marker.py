#!/usr/bin/env python3
"""Exact-source network-free Terminal marker and closed ancestry observations."""
import hashlib
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
POLICY_FILES = ('.github/workflows/activation-diagnostic.yml', '.github/scripts/ci-diagnostic.py',
    '.github/scripts/ci-dnssd-terminal.py', '.github/scripts/ci-dnssd-terminal-marker.py',
    '.github/scripts/test_ci_dnssd_terminal.py', '.github/scripts/test_ci_dnssd_terminal_marker.py',
    '.github/scripts/test_ci_dnssd_terminal_origin.py',
    '.github/scripts/ci-nearby-native.py', '.github/scripts/ci-nearby-diagnostic.py')


def module(name):
    path = Path(__file__).with_name(name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


def empty_result():
    return {'schema': 1, 'diagnosticKind': 'terminal_marker_only', 'markerProbeCompleted': False,
            'originExplanationCompleted': False,
            'terminalLaunch': None, 'failureKind': None, 'nativeNetworkDiagnosticRun': False,
            'selectedInterfaceVerified': False, 'rawReceiveVerified': False,
            'networkMulticastVerified': False, 'physicalLANVerified': False,
            'automaticCoreBrowserVerified': False, 'releaseValidation': False}


def marker_completed(launch):
    observation = launch['gateObservation']
    classified = observation is not None and (
        launch['status'] == 'observed' and launch['reason'] == 'observed' and observation['verified']
        or launch['status'] == 'failed' and launch['reason'] == 'ancestry_unverified' and not observation['verified'])
    return (launch['mode'] == 'marker_only' and classified and all(launch[key] for key in (
        'availabilityVerified', 'openCompleted', 'markerVerified', 'markerOwnerJoined',
        'supervisorTerminationObserved')))


def main(argv=None):
    if (sys.argv[1:] if argv is None else argv) or sys.platform != 'darwin' or os.environ.get('GITHUB_ACTIONS') != 'true':
        print('FAIL: fixed hosted Darwin marker entry required.')
        return 1
    report, provenance = empty_result(), {'schema': 1, 'stage': 'preflight', 'launchMethod': 'ordinary_terminal_command'}
    private, export = None, None
    failure = 'preflight_unavailable'
    try:
        os.umask(0o077)
        candidate = Path(os.environ['RUNNER_TEMP']) / 'soba-dnssd-sanitized'
        candidate.mkdir(mode=0o700, exist_ok=False)
        export = candidate
        native, shared, selector = module('ci-nearby-native'), module('ci-nearby-diagnostic'), module('ci-diagnostic')
        terminal = module('ci-dnssd-terminal')
        read = lambda command: native.checked(command).decode().strip()
        provenance['stage'], failure = 'source-identity', 'source_identity_invalid'
        commit, tree = read(['git', 'rev-parse', 'HEAD']), read(['git', 'rev-parse', 'HEAD^{tree}'])
        if not all(re.fullmatch('[0-9a-f]{40}', value) for value in (commit, tree)):
            raise ValueError('invalid source identity')
        provenance.update(commit=commit, tree=tree)
        provenance['stage'], failure = 'source-event', 'source_event_invalid'
        event_path = Path(os.environ['GITHUB_EVENT_PATH'])
        if event_path.stat().st_size > native.CAPTURE_BYTES:
            raise ValueError('event size exceeded')
        event = shared.parse_json(event_path.read_text(encoding='utf-8'))
        if selector.select(os.environ['GITHUB_EVENT_NAME'], event, os.environ['GITHUB_REPOSITORY'],
                os.environ['GITHUB_REF'], os.environ['GITHUB_SHA'], commit) != 'dnssd':
            raise ValueError('wrong marker suite')
        provenance['stage'], failure = 'source-inventory', 'source_inventory_invalid'
        before = native.source_inventory()
        provenance['sourceSha256'] = hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest()
        provenance['policySha256'] = {name: before[name] for name in POLICY_FILES}
        private = Path(tempfile.mkdtemp(prefix='soba-terminal-marker-', dir=os.environ['RUNNER_TEMP']))
        provenance['stage'], failure = 'marker', 'marker_incomplete'
        # No Go, compiler, SDK, binary, native opt-in or network permit is used.
        _, _, _, _, launch = terminal.capture(None, env={}, private=private, marker_only=True)
        report['terminalLaunch'] = terminal.receipt(launch)
        if launch['mode'] != 'marker_only':
            raise ValueError('wrong launch mode')
        report['markerProbeCompleted'] = marker_completed(launch)
        origin = launch['originObservation']
        report['originExplanationCompleted'] = origin is not None and origin['workerJoined'] and origin['queriesJoined'] and origin['reason'] not in {
            'deadline', 'worker_incomplete', 'cleanup_unresolved', 'gate_incomplete'}
        provenance['stage'], failure = 'verify-source', 'source_changed'
        if before != native.source_inventory():
            raise ValueError('source changed')
        provenance['stage'] = 'classified' if report['markerProbeCompleted'] and report['originExplanationCompleted'] else 'marker-incomplete'
        if not report['markerProbeCompleted'] or not report['originExplanationCompleted']:
            report['failureKind'] = 'marker_incomplete' if not report['markerProbeCompleted'] else 'origin_incomplete'
    except Exception:
        report['markerProbeCompleted'], report['failureKind'] = False, failure
        report['originExplanationCompleted'] = False
    finally:
        if private is not None:
            try:
                shutil.rmtree(private)
            except OSError:
                report['markerProbeCompleted'], report['failureKind'] = False, 'private_cleanup_failed'
                report['originExplanationCompleted'] = False
    try:
        if export is None:
            raise ValueError('export unavailable')
        (export / 'result.json').write_text(json.dumps(report, sort_keys=True) + '\n', encoding='utf-8')
        (export / 'provenance.json').write_text(json.dumps(provenance, sort_keys=True) + '\n', encoding='utf-8')
        with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
            output.write('sanitized_ready=true\n')
    except Exception:
        report['markerProbeCompleted'] = False
        report['originExplanationCompleted'] = False
    complete = report['markerProbeCompleted'] and report['originExplanationCompleted']
    print('CLASSIFIED: network-free marker and closed ancestry observations recorded; native diagnostic not run.'
          if complete else 'FAIL: network-free explanation incomplete; private details withheld.')
    return 0 if complete else 1


if __name__ == '__main__':
    raise SystemExit(main())
