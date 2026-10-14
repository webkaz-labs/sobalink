#!/usr/bin/env python3
"""One original Mac revocation case; publish only source-bound closed evidence."""
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
PACKAGE = 'github.com/webkaz-labs/sobalink/internal/core'
TEST = 'TestLANCorePeerApplicationsIntegration'
SOURCE = 'internal/core/lan_integration_test.go'
TAGS = 'lanlink_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,ts_omit_udptransport'
MARKER = 'revocation_observation_v1 '
CAPTURE_BYTES = 8 * 1024 * 1024


def module(name):
    path = Path(__file__).with_name(name + '.py')
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), path)
    value = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), value.__dict__)
    return value


native = module('ci-nearby-native')
source = module('ci-text-diagnostic')
parser = module('ci-go-test')
FIELDS = ('stage', 'read_bytes', 'read_result', 'target_released', 'app_trusted',
          'core_grants', 'range_policies', 'range_flows', 'durable_pairs',
          'fresh_message', 'receiver_message_observed_at_snapshot')
VALUES = (r'(stream|complete)', r'([01])', r'(none|eof|closed|timeout|other)',
          r'(true|false)', r'(true|false)', r'([0-9]{1,5})', r'(-1|[0-9]{1,5})',
          r'(-1|[0-9]{1,5})', r'([0-9]{1,5})', r'(not_run|not_acknowledged|acknowledged)',
          r'(not_run|absent|present)')


def observations(raw, returncode):
    """Reject spoofed, repeated, skipped or incomplete exact test lifecycles."""
    if (type(raw) is not bytes or not raw.endswith(b'\n') or len(raw) > CAPTURE_BYTES
            or type(returncode) is not int or returncode not in (0, 1)):
        raise ValueError('invalid capture')
    lines = (ROOT / SOURCE).read_text(encoding='utf-8').splitlines()
    locations = [i for i, line in enumerate(lines, 1) if 't.Logf("' + MARKER in line]
    if len(locations) != 1:
        raise ValueError('observation source changed')
    pattern = re.compile(r'    lan_integration_test\.go:' + str(locations[0]) + r': ' + MARKER
                         + ' '.join(key + '=' + value for key, value in zip(FIELDS, VALUES)) + r'\n')
    lifecycle, rows = [], []
    start = [(False, 'start'), (True, 'run')]
    for line in raw.splitlines():
        event = parser.parse_event(line)
        if event.get('Package') != PACKAGE or event.get('Test') not in (None, TEST):
            raise ValueError('unexpected event owner')
        if event['Action'] != 'output':
            lifecycle.append((event.get('Test') is not None, event['Action']))
        elif not lifecycle or len(lifecycle) == 4:
            raise ValueError('output outside lifecycle')
        output = event.get('Output', '')
        if MARKER in output:
            match = pattern.fullmatch(output)
            if lifecycle != start or event.get('Test') != TEST or event['Action'] != 'output' or not match:
                raise ValueError('unbound observation')
            row = dict(zip(FIELDS, match.groups()))
            for key in ('read_bytes', 'core_grants', 'range_policies', 'range_flows', 'durable_pairs'):
                row[key] = int(row[key])
            for key in ('target_released', 'app_trusted'):
                row[key] = row[key] == 'true'
            if (row['stage'] == 'stream' and (row['fresh_message'] != 'not_run'
                    or row['receiver_message_observed_at_snapshot'] != 'not_run')
                    or row['stage'] == 'complete' and (row['fresh_message'] == 'not_run'
                    or row['receiver_message_observed_at_snapshot'] == 'not_run')):
                raise ValueError('invalid observation stage')
            if (any(row[key] > 65535 for key in ('core_grants', 'range_policies', 'range_flows', 'durable_pairs'))
                    or row['stage'] == 'stream' and row['read_bytes'] == 0 and row['read_result'] not in ('none', 'timeout')):
                raise ValueError('inconsistent observation')
            rows.append(row)
    terminal = 'pass' if returncode == 0 else 'fail'
    if lifecycle != start + [(True, terminal), (False, terminal)]:
        raise ValueError('incomplete or contradictory terminal events')
    stages = [row['stage'] for row in rows]
    if stages not in ([], ['complete'], ['stream', 'complete']) or returncode == 0 and rows:
        raise ValueError('invalid failure observations')
    if len(rows) == 2 and any(rows[0][key] != rows[1][key] for key in ('read_bytes', 'read_result')):
        raise ValueError('read observation changed')
    return {'testStatus': terminal, 'observations': rows}


def checked(command, *, env=None, timeout=30):
    state, code, stdout, _ = native.capture(command, cwd=ROOT, env=env, timeout=timeout, limit=CAPTURE_BYTES)
    if state != 'completed' or code != 0:
        raise ValueError('private command failed')
    return stdout


def main(argv=None):
    if ((sys.argv[1:] if argv is None else argv) or sys.platform != 'darwin'
            or os.environ.get('GITHUB_ACTIONS') != 'true'):
        print('FAIL: fixed hosted Mac diagnostic entry required.')
        return 1
    report = {'schema': 1, 'scope': 'mac-original-core-revocation', 'diagnosticPassed': False,
              'processStatus': 'not_run', 'testStatus': 'unavailable', 'observations': [],
              'sourceVerified': False, 'privateCleanupVerified': False, 'firstFailure': 'none', 'releaseValidation': False}
    provenance = {'schema': 1, 'race': False, 'runner': 'macos-26', 'test': TEST}
    stage, private, export = 'preflight', None, None
    before = binary = binary_hash = None
    try:
        os.umask(0o077)
        temporary = Path(os.environ['RUNNER_TEMP'])
        export = temporary / 'soba-revocation-sanitized'
        export.mkdir(mode=0o700, exist_ok=False)
        private = Path(tempfile.mkdtemp(prefix='soba-revocation-', dir=temporary))
        commit = checked(['git', 'rev-parse', 'HEAD']).decode().strip()
        tree = checked(['git', 'rev-parse', 'HEAD^{tree}']).decode().strip()
        selector = module('ci-diagnostic')
        event = source.summary.read_fixed(Path(os.environ['GITHUB_EVENT_PATH']), CAPTURE_BYTES)
        if (selector.select(os.environ['GITHUB_EVENT_NAME'], event, os.environ['GITHUB_REPOSITORY'],
                            os.environ['GITHUB_REF'], os.environ['GITHUB_SHA'], commit) != 'revocation'
                or not re.fullmatch('[0-9a-f]{40}', tree)):
            raise ValueError('wrong exact source')
        before = source.source_inventory()
        versions = source.summary.nearby.parse_json(checked(['go', 'env', '-json', 'GOVERSION',
            'GOHOSTOS', 'GOHOSTARCH', 'GOOS', 'GOARCH', 'CGO_ENABLED']))
        native.identities('darwin-arm64', versions)
        provenance.update(commit=commit, tree=tree, sourceSha256=source.aggregate(before),
                          goVersion='go1.27.1', goSha256=source.digest(Path(shutil.which('go')).resolve()))
        env = dict(os.environ, GOTOOLCHAIN='local', GOWORK='off', GOTELEMETRY='off',
                   GOFLAGS='-mod=readonly -buildvcs=false', CGO_ENABLED='1', TMPDIR=str(private))
        stage = 'dependencies'
        checked(['go', 'run', './cmd/prepare-engine'], env=env, timeout=300)
        checked(['go', 'run', './cmd/prepare-engine', '--verify'], env=env, timeout=120)
        checked(['go', 'mod', 'download'], env=env, timeout=300)
        checked(['go', 'mod', 'verify'], env=env, timeout=120)
        if source.source_inventory() != before:
            raise ValueError('source changed')
        env.update(GOPROXY='off', GOSUMDB='off')
        stage = 'compile'
        binary = private / 'core.test'
        checked(['go', 'test', '-c', '-tags=' + TAGS, '-o', str(binary), './internal/core'], env=env, timeout=600)
        binary_hash = source.digest(binary)
        provenance['testSha256'] = binary_hash
        # Match the original CI's isolated-loopback environment and non-race case.
        env = {k: v for k, v in env.items() if k.upper() not in ('HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'TS_PROXY')
               and not k.upper().startswith('TS_')}
        env['SOBALINK_RUN_LAN_INTEGRATION'] = '1'
        stage = 'runtime'
        state, code, stdout, _ = native.capture([str(binary), '-test.v=test2json', '-test.run=^' + TEST + '$',
            '-test.count=1', '-test.timeout=5m'], cwd=ROOT / 'internal/core', env=env, timeout=330, limit=CAPTURE_BYTES)
        report['processStatus'] = state
        if state != 'completed':
            report['firstFailure'] = 'runtime'
        stage = 'events'
        if state in ('completed', 'nonzero'):
            conversion, conversion_code, data, _ = native.capture(['go', 'tool', 'test2json', '-p', PACKAGE],
                cwd=ROOT, env=env, timeout=15, limit=CAPTURE_BYTES, stdin=stdout)
            if conversion != 'completed' or conversion_code != 0:
                raise ValueError('event conversion failed')
            report.update(observations(data, code))
        else:
            raise ValueError('runtime incomplete')
    except Exception:
        if report['firstFailure'] == 'none':
            report['firstFailure'] = stage
    finally:
        if before is not None and binary_hash is not None:
            try:
                if source.source_inventory() != before or source.digest(binary) != binary_hash:
                    raise ValueError('source changed')
                if source.digest(Path(shutil.which('go')).resolve()) != provenance['goSha256']:
                    raise ValueError('toolchain changed')
                checked(['go', 'run', './cmd/prepare-engine', '--verify'], env=env, timeout=120)
                report['sourceVerified'] = True
            except Exception:
                report['observations'] = []
                if report['firstFailure'] == 'none':
                    report['firstFailure'] = 'verify-source'
        if private is not None:
            try:
                shutil.rmtree(private)
                if os.path.lexists(private):
                    raise OSError('private directory remains')
                report['privateCleanupVerified'] = True
            except OSError:
                if report['firstFailure'] == 'none':
                    report['firstFailure'] = 'cleanup'
    report['diagnosticPassed'] = (report['processStatus'] == 'completed' and report['testStatus'] == 'pass'
                                   and report['sourceVerified'] and report['privateCleanupVerified'] and report['firstFailure'] == 'none')
    try:
        if export is None:
            raise ValueError('export unavailable')
        source.summary.write_fixed(export / 'result.json', report)
        source.summary.write_fixed(export / 'provenance.json', provenance)
        with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as output:
            output.write('sanitized_ready=true\n')
    except Exception:
        report['diagnosticPassed'] = False
    print('PASS: one original Mac revocation case passed; release validation excluded.' if report['diagnosticPassed'] else
          'FAIL: Mac revocation diagnostic did not pass; private details withheld.')
    return 0 if report['diagnosticPassed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
