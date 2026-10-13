"""Read-only, exact-source full-CI proof. Never reuse another run's jobs."""
import ast
import base64
import datetime
import hashlib
import json
from pathlib import Path
import re

REPOSITORY = 'webkaz-labs/sobalink'
WORKFLOW = '.github/workflows/ci.yml'
SOURCES = (WORKFLOW, '.github/scripts/ci-impact.py', '.github/scripts/ci-coverage.py',
           '.github/scripts/ci-go-test.py', '.github/scripts/ci_full_validation.py',
           '.github/scripts/ci-browser-summary.py', '.github/scripts/ci-nearby-diagnostic.py',
           '.github/scripts/ci-nearby-native.py', '.github/scripts/ci-nearby-required.py',
           '.github/scripts/release-validation.py')
TARGETS = {
    'linux-amd64': ('native (ubuntu-24.04, linux, amd64)', 'ubuntu-24.04'),
    'linux-arm64': ('native (ubuntu-24.04-arm, linux, arm64)', 'ubuntu-24.04-arm'),
    'darwin-arm64': ('native (macos-26, darwin, arm64)', 'macos-26'),
    'windows-amd64': ('native (windows-2025, windows, amd64)', 'windows-2025'),
}
MAX_PAGES = 10


def require(ok, message):
    if not ok:
        raise ValueError(message)


def positive(value):
    return type(value) is int and value > 0


def digest(data):
    return hashlib.sha256(data).hexdigest()


def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode('utf-8')


def timestamp(value):
    require(isinstance(value, str), 'missing CI attempt time')
    try:
        result = datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))
    except ValueError as exc:
        raise ValueError('invalid CI attempt time') from exc
    require(result.tzinfo is not None, 'CI attempt time lacks timezone')
    return result


def pages(api, path, key):
    rows, identifiers, total = [], set(), None
    separator = '&' if '?' in path else '?'
    for page in range(1, MAX_PAGES + 1):
        data = api(path + separator + f'per_page=100&page={page}')
        count, batch = data.get('total_count'), data.get(key)
        require(type(count) is int and count >= 0 and isinstance(batch, list) and len(batch) <= 100,
                'invalid CI inventory')
        require(total is None or total == count, 'CI inventory changed during pagination')
        total = count
        for item in batch:
            require(isinstance(item, dict) and positive(item.get('id')) and item['id'] not in identifiers,
                    'duplicate or invalid CI inventory ID')
            identifiers.add(item['id'])
            rows.append(item)
        require(len(rows) <= total, 'CI inventory count mismatch')
        if len(batch) < 100:
            require(len(rows) == total, 'incomplete CI inventory')
            return rows
    raise ValueError('CI inventory exceeds audit limit')


def identity(run, commit):
    require(run.get('head_sha') == commit and run.get('head_branch') == 'main'
            and run.get('name') == 'Cross-platform CI' and run.get('path') == WORKFLOW
            and run.get('event') in ('push', 'workflow_dispatch', 'schedule')
            and positive(run.get('id')) and positive(run.get('run_attempt')),
            'unexpected canonical CI identity')


def attempt_time(api, run, commit):
    identity(run, commit)
    started, created = timestamp(run.get('run_started_at')), timestamp(run.get('created_at'))
    require(started >= created, 'CI attempt precedes run creation')
    if run['run_attempt'] > 1:
        previous = api(f"actions/runs/{run['id']}/attempts/{run['run_attempt'] - 1}")
        identity(previous, commit)
        require(previous['id'] == run['id'] and previous['run_attempt'] == run['run_attempt'] - 1
                and started > timestamp(previous.get('run_started_at')),
                'ambiguous CI rerun chronology')
    return started


def select_candidate(api, commit):
    """Every manual dispatch is full intent; never select only successful runs."""
    require(bool(re.fullmatch('[0-9a-f]{40}', commit)), 'invalid full-validation source')
    runs = pages(api, f'actions/workflows/ci.yml/runs?branch=main&head_sha={commit}', 'workflow_runs')
    require(bool(runs), 'CI has not run for this source')
    times = {}
    for run in runs:
        times[run['id']] = attempt_time(api, run, commit)
    manual = [run for run in runs if run['event'] == 'workflow_dispatch']
    require(bool(manual), 'dispatch Cross-platform CI with force_full for this exact main source')
    # An in-flight manual full validation is unresolved even if it started first.
    require(all(run.get('status') == 'completed' for run in manual), 'manual full validation is pending')
    newest = max(times[run['id']] for run in manual)
    selected = [run for run in manual if times[run['id']] == newest]
    require(len(selected) == 1, 'ambiguous latest full-validation attempt')
    selected = selected[0]
    require(selected.get('conclusion') == 'success', 'latest full validation did not succeed')
    for run in runs:
        if times[run['id']] >= newest:
            require(run.get('status') == 'completed' and run.get('conclusion') == 'success',
                    'newer canonical CI has unresolved or failed checks')
    return selected


def source_bytes(api, commit, local_root=None):
    sources = {}
    for path in SOURCES:
        item = api(f'contents/{path}?ref={commit}')
        require(item.get('type') == 'file' and item.get('path') == path
                and item.get('encoding') == 'base64' and type(item.get('size')) is int
                and 0 < item['size'] <= 1024 * 1024, 'invalid source policy object')
        try:
            data = base64.b64decode(''.join(item['content'].split()), validate=True)
        except (KeyError, ValueError, TypeError) as exc:
            raise ValueError('invalid encoded source policy') from exc
        require(len(data) == item['size'], 'incomplete source policy')
        # Bind bytes to Git's immutable blob identity, not only the requested URL.
        blob = hashlib.sha1(b'blob ' + str(len(data)).encode('ascii') + b'\0' + data).hexdigest()
        require(item.get('sha') == blob, 'source policy blob mismatch')
        if local_root is not None:
            require((Path(local_root) / path).read_bytes() == data, 'checkout policy differs from exact source')
        sources[path] = data
    # Publication consumes these two scripts from the gate artifact, not a new
    # checkout. Verify both loaded tools still match the immutable source.
    for name in ('ci_full_validation.py', 'release-validation.py'):
        require(Path(__file__).with_name(name).read_bytes() == sources['.github/scripts/' + name],
                'loaded release tool differs from exact source')
    return sources


def workflow_metadata(workflow):
    """Accept only the workflow's canonical block-mapping YAML surface.

    Literal/folded scalar contents are commands/data, not YAML keys. Reject
    other mapping syntax rather than partially interpreting quoted/escaped keys,
    flow mappings, aliases or anchors as if they were ordinary scalar values.
    """
    result, scalar_indent, offset, mappings = [], None, 0, {}
    for line in workflow.splitlines(keepends=True):
        start, offset = offset, offset + len(line)
        if not line.strip() or line.lstrip().startswith('#'):
            continue
        indent = len(line) - len(line.lstrip(' '))
        if scalar_indent is not None and indent > scalar_indent:
            continue
        scalar_indent = None
        require('\t' not in line[:indent + 1], 'unsupported workflow indentation')
        match = re.fullmatch(r' *(?:- )?([A-Za-z_][A-Za-z0-9_.-]*):(?: +(.*))?\n?', line)
        require(match is not None, 'unsupported workflow mapping syntax')
        key, value = match.group(1), match.group(2) or ''
        item = line[indent:].startswith('- ')
        mapping_indent = indent + (2 if item else 0)
        for depth in list(mappings):
            if depth > mapping_indent or (item and depth == mapping_indent):
                del mappings[depth]
        seen = mappings.setdefault(mapping_indent, set())
        require(key not in seen, 'duplicate workflow mapping key')
        seen.add(key)
        # Expressions are scalar values even when they contain format braces.
        plain = re.sub(r'\$\{\{.*?\}\}', 'EXPRESSION', value)
        require(not any(c in plain for c in '{}')
                and not re.search(r'(?:^|[ ,\[])[&*][A-Za-z_]', plain)
                and not plain.startswith('!'), 'unsupported workflow mapping value')
        result.append((start, indent, key, value))
        if value.startswith(('|', '>')):
            require(re.fullmatch(r'[|>][-+]?', value) is not None,
                    'unsupported workflow block scalar')
            scalar_indent = mapping_indent
    return result


def validate_parallel_workflow(data):
    """Fail closed on changes to the deliberately narrow native async region.

    This is a source-policy check, not a general YAML parser. The approved
    spelling/layout is intentionally literal so unexpected syntax needs review.
    Runtime proof still requires every individual gate and the wait to succeed.
    """
    workflow = data.decode('utf-8')
    metadata = workflow_metadata(workflow)
    groups = (
        ('Verify direct LAN session natural rekey and idle lifecycle', 'natural-lifecycle', 9),
        ('Verify guarded relay real-time lease continuity', 'guarded-lease', 9),
        ('Verify relay-only real-time lease and idle continuity', 'relay-only-lease', 10),
    )
    marker = '      - name: '
    starts = []
    for name, identifier, timeout in groups:
        header = (marker + name + '\n        id: ' + identifier
                  + "\n        background: true\n        if: steps.ci-plan.outputs.long_required != 'false'"
                  + '\n        timeout-minutes: ' + str(timeout) + '\n        run: |\n')
        require(workflow.count(header) == 1, 'invalid real-time background step policy')
        starts.append(workflow.index(header))
    fence = (marker + 'Wait for real-time lifecycle and lease checks\n'
             '        wait: [natural-lifecycle, guarded-lease, relay-only-lease]\n')
    package = marker + 'Build native package and smoke archive contents\n'
    require(workflow.count(fence + package) == 1, 'missing real-time wait before packaging')
    end = workflow.index(fence)
    require(starts == sorted(starts) and starts[-1] < end, 'invalid real-time step order')
    region = workflow[starts[0]:end]
    require(len(re.findall(r'^      - ', region, re.M)) == 3,
            'unexpected work inside real-time parallel region')
    # Only these three steps may run asynchronously. No cancel, relaxed failure,
    # nested parallel group, or alternate join may weaken the evidence boundary.
    controls = [key for _, _, key, _ in metadata
                if key in {'background', 'parallel', 'wait', 'wait-all', 'cancel'}]
    require(controls == ['background', 'background', 'background', 'wait'],
            'unexpected workflow concurrency or failure policy')
    require(not any(key == 'continue-on-error' and starts[0] <= pos < end + len(fence)
                    for pos, _, key, _ in metadata), 'real-time failures cannot be ignored')
    require(workflow.index(marker + 'Verify recovery with the ordinary direct-enabled transport\n') < starts[0],
            'real-time checks must follow serial native checks')
    # No shared timing writer is added: only natural-lifecycle records a metric
    # in this region, and all serial metric writers have already completed.
    require(region.count('.github/scripts/ci-metrics.py run') == 1
            and region.count('--suite natural-lifecycle --') == 1,
            'parallel timing writers require isolated storage')


def validate_resource_inspection_workflow(data):
    """Require the reviewed serial five-case invocation, including its preflight.

    Compare source text rather than execute workflow commands. Any command,
    opt-in, preflight, selector, timeout or failure-policy change needs review.
    """
    workflow = data.decode('utf-8')
    workflow_metadata(workflow)
    management_start, _ = validate_resource_management_workflow(data)
    _, group_end = validate_resource_group_catalog_workflow(data)
    expected = """      - name: Verify native remote resource inspection
        if: steps.ci-plan.outputs.long_required != 'false'
        timeout-minutes: 12
        run: |
          set -euo pipefail
          python - <<'PYTHON'
          import os
          import subprocess
          import sys

          # Fail closed without removing or printing proxy overrides.
          proxies = ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY")
          if any(os.environ.get(name) for name in proxies):
              raise SystemExit("Native inspection requires an isolated environment without proxy overrides")
          actual = subprocess.check_output(["go", "env", "GOVERSION", "GOOS", "GOARCH"], text=True, timeout=30).splitlines()
          if actual != ["go1.27.1", "${{ matrix.goos }}", "${{ matrix.goarch }}"]:
              raise SystemExit("Native inspection requires the exact Go toolchain and matrix target")
          env = os.environ.copy()
          env["SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE"] = "reviewed-production-loopback-v1"
          env["SOBALINK_RUN_ACTIVATION_NATIVE"] = "1"
          # One exact execution; fixture-owned numeric loopback and synthetic profiles.
          subprocess.run([
              sys.executable, ".github/scripts/ci-go-test.py", "--exact",
              "--expect", "github.com/webkaz-labs/sobalink/internal/core:TestResourceInspectionNativeFirstRemoteInspect",
              "--expect", "github.com/webkaz-labs/sobalink/internal/core:TestResourceInspectionNativeRestartPreservesOriginalExpiry",
              "--expect", "github.com/webkaz-labs/sobalink/internal/core:TestResourceInspectionNativeReverseRestartPreservesOriginalExpiry",
              "--expect", "github.com/webkaz-labs/sobalink/internal/core:TestResourceInspectionNativeRevokeDeniesNewInspection",
              "--expect", "github.com/webkaz-labs/sobalink/internal/core:TestResourceInspectionNativePortCollisionPreservesOwner",
              "--",
              "go", "test", "-race", "-count=1", "-v", "-timeout=8m",
              "-tags=ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,directlan_activation_native,resource_inspection_native",
              "-run=^TestResourceInspectionNative(FirstRemoteInspect|RestartPreservesOriginalExpiry|ReverseRestartPreservesOriginalExpiry|RevokeDeniesNewInspection|PortCollisionPreservesOwner)$",
              "./internal/core",
          ], env=env, check=True, timeout=10 * 60)
          PYTHON
"""
    marker = '      - name: Verify native remote resource inspection\n'
    require(workflow.count(marker) == 1, 'missing or duplicate native inspection gate')
    start = workflow.index(marker)
    following = re.search(r'^      - ', workflow[start + len(marker):], re.M)
    require(following is not None, 'missing serial native inspection boundary')
    end = start + len(marker) + following.start()
    # Trailing YAML comments are not executable policy. Anything else appended
    # to the step (including another command after the heredoc) fails closed.
    block = re.sub(r'(?:^ *#[^\n]*\n|^ *\n)+\Z', '', workflow[start:end], flags=re.M)
    require(block == expected, 'invalid native inspection execution policy')
    recovery = '      - name: Verify recovery with the ordinary direct-enabled transport\n'
    parallel = '      - name: Verify direct LAN session natural rekey and idle lifecycle\n'
    require(workflow.count(recovery) == 1 and workflow.count(parallel) == 1
            and workflow.index(recovery) < start < workflow.index(parallel)
            and end == management_start, 'native inspection must immediately precede management')
    # Opt-ins belong only to this child invocation, never the workflow/job env.
    # Following management/group children have separately validated opt-ins.
    outside = workflow[:start] + workflow[group_end:]
    require('SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE' not in outside
            and 'SOBALINK_RUN_ACTIVATION_NATIVE' not in outside,
            'native inspection opt-ins escaped their step')


def validate_resource_management_workflow(data):
    """Require the reviewed serial three-case management invocation verbatim."""
    group_start, group_end = validate_resource_group_catalog_workflow(data)
    workflow = data.decode('utf-8')
    workflow_metadata(workflow)
    expected = """      - name: Verify native remote resource management
        if: steps.ci-plan.outputs.long_required != 'false'
        timeout-minutes: 12
        run: |
          set -euo pipefail
          python - <<'PYTHON'
          import os
          import subprocess
          import sys

          actual = subprocess.check_output(["go", "env", "GOVERSION", "GOOS", "GOARCH"], text=True, timeout=30).splitlines()
          if actual != ["go1.27.1", "${{ matrix.goos }}", "${{ matrix.goarch }}"]:
              raise SystemExit("Native management requires the exact Go toolchain and matrix target")
          # Only the child receives these opt-ins and an isolated proxy environment.
          proxies = ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY")
          env = {name: value for name, value in os.environ.items()
                 if name not in proxies and name != "SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE"}
          env["SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE"] = "reviewed-production-loopback-v2"
          env["SOBALINK_RUN_ACTIVATION_NATIVE"] = "1"
          # One exact two-Core loopback execution, including in-process Core restart.
          subprocess.run([
              sys.executable, ".github/scripts/ci-go-test.py", "--exact",
              "--expect", "github.com/webkaz-labs/sobalink/internal/core:TestResourceManagementNativePreviewApplyReplayAndMigration",
              "--expect", "github.com/webkaz-labs/sobalink/internal/core:TestResourceManagementNativeRevokeReplacementAndProtocolSeparation",
              "--expect", "github.com/webkaz-labs/sobalink/internal/core:TestResourceManagementNativeRestartPreservesOriginalExpiryAndHistory",
              "--",
              "go", "test", "-race", "-count=1", "-v", "-timeout=8m",
              "-tags=ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,resource_management_native,resource_inspection_native,directlan_activation_native",
              "-run=^TestResourceManagementNative(PreviewApplyReplayAndMigration|RevokeReplacementAndProtocolSeparation|RestartPreservesOriginalExpiryAndHistory)$",
              "./internal/core",
          ], env=env, check=True, timeout=10 * 60)
          PYTHON
"""
    marker = '      - name: Verify native remote resource management\n'
    require(workflow.count(marker) == 1, 'missing or duplicate native management gate')
    start = workflow.index(marker)
    following = re.search(r'^      - ', workflow[start + len(marker):], re.M)
    require(following is not None, 'missing serial native management boundary')
    end = start + len(marker) + following.start()
    block = re.sub(r'(?:^ *#[^\n]*\n|^ *\n)+\Z', '', workflow[start:end], flags=re.M)
    require(block == expected, 'invalid native management execution policy')
    inspection = '      - name: Verify native remote resource inspection\n'
    parallel = '      - name: Verify direct LAN session natural rekey and idle lifecycle\n'
    require(workflow.count(inspection) == 1 and workflow.count(parallel) == 1
            and workflow.index(inspection) < start < workflow.index(parallel)
            and end == group_start, 'native management must immediately precede fixed-group catalog')
    # The group child removes inherited management opt-in without enabling it.
    outside = workflow[:start] + workflow[group_end:]
    require('SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE' not in outside,
            'native management opt-in escaped its step')
    return start, end


def validate_resource_group_catalog_workflow(data):
    """Require the reviewed serial single-case fixed-group invocation verbatim."""
    workflow = data.decode('utf-8')
    workflow_metadata(workflow)
    expected = """      - name: Verify native fixed-group resource catalog
        if: steps.ci-plan.outputs.long_required != 'false'
        timeout-minutes: 8
        run: |
          set -euo pipefail
          python - <<'PYTHON'
          import os
          import subprocess
          import sys

          actual = subprocess.check_output(["go", "env", "GOVERSION", "GOOS", "GOARCH"], text=True, timeout=30).splitlines()
          if actual != ["go1.27.1", "${{ matrix.goos }}", "${{ matrix.goarch }}"]:
              raise SystemExit("Native fixed-group catalog requires the exact Go toolchain and matrix target")
          # Only this child receives the reviewed group opt-in and proxy isolation.
          proxies = ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "TS_PROXY")
          unrelated = ("SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE", "SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE")
          env = {name: value for name, value in os.environ.items()
                 if name not in proxies and name not in unrelated}
          env["SOBALINK_RUN_RESOURCE_GROUP_CATALOG_NATIVE"] = "reviewed-three-core-loopback-v1"
          env["SOBALINK_RUN_ACTIVATION_NATIVE"] = "1"
          # One fixed two-target run on three owned in-process loopback Cores.
          subprocess.run([
              sys.executable, ".github/scripts/ci-go-test.py", "--exact",
              "--expect", "github.com/webkaz-labs/sobalink/internal/core:TestResourceGroupCatalogNativeFixedTwoTargetOneShot",
              "--",
              "go", "test", "-race", "-count=1", "-v", "-timeout=4m",
              "-tags=ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,directlan_activation_native,resource_inspection_native,resource_management_native,resource_group_catalog_native",
              "-run=^TestResourceGroupCatalogNativeFixedTwoTargetOneShot$",
              "./internal/core",
          ], env=env, check=True, timeout=6 * 60)
          PYTHON
"""
    marker = '      - name: Verify native fixed-group resource catalog\n'
    require(workflow.count(marker) == 1, 'missing or duplicate native fixed-group catalog gate')
    start = workflow.index(marker)
    following = re.search(r'^      - ', workflow[start + len(marker):], re.M)
    require(following is not None, 'missing serial native fixed-group catalog boundary')
    end = start + len(marker) + following.start()
    block = re.sub(r'(?:^ *#[^\n]*\n|^ *\n)+\Z', '', workflow[start:end], flags=re.M)
    require(block == expected, 'invalid native fixed-group catalog execution policy')
    management = '      - name: Verify native remote resource management\n'
    parallel = '      - name: Verify direct LAN session natural rekey and idle lifecycle\n'
    require(workflow.count(management) == 1 and workflow.count(parallel) == 1
            and workflow.index(management) < start < workflow.index(parallel)
            and end == workflow.index(parallel), 'native fixed-group catalog must precede the async region')
    outside = workflow[:start] + workflow[end:]
    require('SOBALINK_RUN_RESOURCE_GROUP_CATALOG_NATIVE' not in outside,
            'native fixed-group catalog opt-in escaped its step')
    return start, end


def validate_outbox_workflow(workflow_bytes):
    """Require the exact no-skip native and real-browser durable outbox gates."""
    workflow = workflow_bytes.decode('utf-8')
    expected = ("      - name: Verify durable outbox restart across Core processes\n        timeout-minutes: 6\n        env:\n          SOBALINK_RUN_DIRECT_INTEGRATION: '1'\n        run: |\n          set -euo pipefail\n          python .github/scripts/ci-metrics.py run --suite outbox-process-restart -- python .github/scripts/ci-go-test.py --exact --expect github.com/webkaz-labs/sobalink/internal/core:TestOutboxProcessRestart -- go test -race -count=1 -timeout=4m -tags=lanlink_integration,directlan_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./internal/core -run '^TestOutboxProcessRestart$'\n",
                "      - name: Verify durable outbox recovery with separate agents and actual browser\n        timeout-minutes: 8\n        env:\n          SOBALINK_RUN_DIRECT_INTEGRATION: '1'\n          SOBALINK_RUN_TRANSFER_BROWSER_NATIVE: '1'\n          SOBA_OUTBOX_BROWSER_SUMMARY: ${{ runner.temp }}/soba-playwright-screenshots/outbox-native-summary.json\n        run: |\n          set -euo pipefail\n          python .github/scripts/ci-go-test.py --exact --expect github.com/webkaz-labs/sobalink/internal/core:TestOutboxThreeProcessBrowser -- go test -race -tags=lanlink_integration,directlan_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./internal/core -run '^TestOutboxThreeProcessBrowser$' -count=1 -timeout=4m\n")
    for block in expected:
        marker = block.splitlines(keepends=True)[0]
        require(workflow.count(marker) == 1 and workflow.count(block) == 1,
                'invalid durable outbox execution policy')
        start = workflow.index(marker)
        following = workflow[start + len(block):]
        require(following.startswith('      - '), 'invalid durable outbox step boundary')


def validate_durable_text_workflow(workflow_bytes):
    """Require one exact no-skip text recovery gate in the common native job."""
    workflow = workflow_bytes.decode('utf-8')
    metadata = workflow_metadata(workflow)
    expected = """      - name: Verify durable text restart and lost ACK across Core processes
        timeout-minutes: 6
        env:
          SOBALINK_RUN_DIRECT_INTEGRATION: '1'
        run: |
          set -euo pipefail
          python .github/scripts/ci-metrics.py run --suite durable-text-process-restart -- python .github/scripts/ci-go-test.py --exact --expect github.com/webkaz-labs/sobalink/internal/core:TestDurableTextProcessRestartACKLossAndHiddenReplay -- go test -race -count=1 -timeout=4m -tags=lanlink_integration,directlan_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./internal/core -run '^TestDurableTextProcessRestartACKLossAndHiddenReplay$'
"""
    marker = expected.splitlines(keepends=True)[0]
    require(workflow.count(marker) == 1 and workflow.count(expected) == 1,
            'invalid durable text execution policy')
    start = workflow.index(marker)
    require(workflow[start + len(expected):].startswith('      - '),
            'invalid durable text step boundary')
    jobs = [(offset, key) for offset, indent, key, _ in metadata if indent == 2]
    native = [offset for offset, key in jobs if key == 'native']
    require(len(native) == 1, 'missing durable text native job')
    end = next((offset for offset, _ in jobs if offset > native[0]), len(workflow))
    require(native[0] < start < end, 'durable text gate must run in the common native job')


def validate_setup_and_text_browser_workflow(workflow_bytes):
    """Bind manual setup and actual-browser gates, exits and fixed exports."""
    workflow = workflow_bytes.decode('utf-8')
    metadata = workflow_metadata(workflow)
    jobs = [(offset, key) for offset, indent, key, _ in metadata if indent == 2]
    expected = (
        ('native', """      - name: Verify manual nearby setup across native Core peers
        timeout-minutes: 6
        env:
          SOBALINK_RUN_LAN_INTEGRATION: '1'
        run: |
          set -euo pipefail
          python .github/scripts/ci-metrics.py run --suite nearby-manual-core -- python .github/scripts/ci-go-test.py --exact --expect github.com/webkaz-labs/sobalink/internal/core:TestLANCoreNearbySetupManualIntegration -- go test -race -count=1 -timeout=3m -tags=lanlink_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,ts_omit_udptransport ./internal/core -run '^TestLANCoreNearbySetupManualIntegration$'
"""),
        ('browser', """      - name: Verify nearby setup with separate agents and actual browser
        timeout-minutes: 8
        env:
          SOBALINK_RUN_LAN_INTEGRATION: '1'
          SOBALINK_RUN_SETUP_BROWSER_NATIVE: '1'
          SOBA_SETUP_BROWSER_SUMMARY: ${{ runner.temp }}/soba-setup-browser-evidence/setup-native-summary.json
          SOBA_SETUP_BROWSER_FAILURE: ${{ runner.temp }}/soba-setup-browser-evidence/setup-native-failure.json
        run: |
          set -euo pipefail
          mkdir -m 700 "$RUNNER_TEMP/soba-setup-browser-evidence"
          status=0
          python .github/scripts/ci-go-test.py --exact --expect github.com/webkaz-labs/sobalink/internal/core:TestNearbySetupTwoProcessBrowser -- go test -race -tags=lanlink_integration,directlan_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,ts_omit_udptransport ./internal/core -run '^TestNearbySetupTwoProcessBrowser$' -count=1 -timeout=3m || status=$?
          summary_status=0
          python -B .github/scripts/ci-browser-summary.py setup "$status" || summary_status=$?
          if [ "$status" -ne 0 ]; then exit "$status"; fi
          exit "$summary_status"
"""),
        ('browser', """      - name: Verify durable text recovery with separate agents and actual browser
        timeout-minutes: 8
        env:
          SOBALINK_RUN_DIRECT_INTEGRATION: '1'
          SOBALINK_RUN_TRANSFER_BROWSER_NATIVE: '1'
          SOBA_TEXT_BROWSER_SUMMARY: ${{ runner.temp }}/soba-text-browser-evidence/text-native-summary.json
        run: |
          set -euo pipefail
          mkdir -m 700 "$RUNNER_TEMP/soba-text-browser-evidence"
          status=0
          python .github/scripts/ci-go-test.py --exact --expect github.com/webkaz-labs/sobalink/internal/core:TestDurableTextThreeProcessBrowser -- go test -race -tags=lanlink_integration,directlan_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./internal/core -run '^TestDurableTextThreeProcessBrowser$' -count=1 -timeout=4m || status=$?
          summary_status=0
          python -B .github/scripts/ci-browser-summary.py text "$status" || summary_status=$?
          if [ "$status" -ne 0 ]; then exit "$status"; fi
          exit "$summary_status"
"""),
    )
    for job, block in expected:
        marker = block.splitlines(keepends=True)[0]
        require(workflow.count(marker) == 1 and workflow.count(block) == 1,
                'invalid setup or text browser execution policy')
        start = workflow.index(marker)
        require(workflow[start + len(block):].startswith('      - '),
                'invalid setup or text browser step boundary')
        positions = [offset for offset, key in jobs if key == job]
        require(len(positions) == 1, 'missing setup or text browser job')
        end = next((offset for offset, _ in jobs if offset > positions[0]), len(workflow))
        require(positions[0] < start < end, 'setup or text browser gate is in the wrong job')
    artifact = """      - name: Preserve sanitized UI screenshots and typography measurements
        if: always()
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7
        with:
          name: soba-browser-screenshots
          path: |
            ${{ runner.temp }}/soba-playwright-screenshots/*.png
            ${{ runner.temp }}/soba-playwright-screenshots/typography-*.json
            ${{ runner.temp }}/soba-playwright-screenshots/playwright-report.json
            ${{ runner.temp }}/soba-playwright-screenshots/transfer-native-summary.json
            ${{ runner.temp }}/soba-playwright-screenshots/dispatch-native-summary.json
            ${{ runner.temp }}/soba-playwright-screenshots/outbox-native-summary.json
            ${{ runner.temp }}/soba-playwright-screenshots/setup-native-summary.json
            ${{ runner.temp }}/soba-playwright-screenshots/setup-native-failure.json
            ${{ runner.temp }}/soba-playwright-screenshots/text-native-summary.json
          if-no-files-found: warn
          retention-days: 14
"""
    require(workflow.count(artifact) == 1, 'invalid fixed browser summary export policy')
    require(workflow.count('soba-setup-browser-evidence') == 3
            and workflow.count('soba-text-browser-evidence') == 2,
            'private browser evidence must not be exported')


def validate_linux_nearby_workflow(workflow_bytes):
    """Bind the one qualified Linux no-skip gate; preserve diagnostic semantics."""
    workflow = workflow_bytes.decode('utf-8')
    metadata = workflow_metadata(workflow)
    expected = """      - name: Verify Linux nearby native discovery without skips
        if: matrix.goos == 'linux'
        timeout-minutes: 8
        env:
          SOBALINK_NEARBY_NATIVE_PLATFORM: ${{ matrix.goos }}-${{ matrix.goarch }}
        run: |
          set -euo pipefail
          python -B .github/scripts/ci-metrics.py run --suite nearby-native-linux -- python -B .github/scripts/ci-nearby-required.py
"""
    marker = expected.splitlines(keepends=True)[0]
    require(workflow.count(marker) == 1 and workflow.count(expected) == 1,
            'invalid Linux nearby execution policy')
    start = workflow.index(marker)
    require(workflow[start + len(expected):].startswith('      - '),
            'invalid Linux nearby step boundary')
    jobs = [(offset, key) for offset, indent, key, _ in metadata if indent == 2]
    native = [offset for offset, key in jobs if key == 'native']
    require(len(native) == 1, 'missing Linux nearby native job')
    end = next((offset for offset, _ in jobs if offset > native[0]), len(workflow))
    require(native[0] < start < end, 'Linux nearby must run in the native job')
    require('SOBALINK_NEARBY_NATIVE_PLATFORM' not in workflow[:start] + workflow[start + len(expected):],
            'Linux nearby opt-in escaped its step')


def required_jobs(sources):
    """Read literal required-step constants from the protected source; no exec."""
    validate_parallel_workflow(sources[WORKFLOW])
    validate_outbox_workflow(sources[WORKFLOW])
    validate_durable_text_workflow(sources[WORKFLOW])
    validate_linux_nearby_workflow(sources[WORKFLOW])
    validate_setup_and_text_browser_workflow(sources[WORKFLOW])
    validate_resource_inspection_workflow(sources[WORKFLOW])
    tree = ast.parse(sources['.github/scripts/ci-coverage.py'].decode('utf-8'))
    names = {'FAST_STEPS', 'LONG_STEPS', 'FULL_ONLY_STEPS', 'BROWSER_STEPS',
             'LINUX_NEARBY_TARGETS', 'LINUX_NEARBY_STEPS'}
    values = {}
    for node in tree.body:
        if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name):
            name = node.targets[0].id
            if name in names:
                require(name not in values, 'duplicate required-step policy')
                values[name] = ast.literal_eval(node.value)
    require(set(values) == names, 'missing required-step policy')
    bindings = [node.id for node in ast.walk(tree) if isinstance(node, ast.Name)
                and isinstance(node.ctx, ast.Store) and node.id in names]
    require(len(bindings) == len(names) and set(bindings) == names,
            'required-step policy must have one literal assignment')
    for value in values.values():
        require(isinstance(value, tuple) and value and all(isinstance(v, str) and v for v in value)
                and len(set(value)) == len(value), 'invalid required-step policy')
    require(len(values['LONG_STEPS']) == 3, 'unexpected real-time gate policy')
    require(values['FAST_STEPS'].count('Verify durable outbox restart across Core processes') == 1
            and values['BROWSER_STEPS'].count('Verify durable outbox recovery with separate agents and actual browser') == 1,
            'missing durable outbox required gates')
    require(values['FAST_STEPS'].count('Verify durable text restart and lost ACK across Core processes') == 1,
            'missing durable text required gate')
    require(values['FAST_STEPS'].count('Verify manual nearby setup across native Core peers') == 1
            and values['BROWSER_STEPS'].count('Verify nearby setup with separate agents and actual browser') == 1
            and values['BROWSER_STEPS'].count('Verify durable text recovery with separate agents and actual browser') == 1,
            'missing setup or text browser required gates')
    require(values['FULL_ONLY_STEPS'] == ('Verify native remote resource inspection',
                                         'Verify native remote resource management',
                                         'Verify native fixed-group resource catalog'),
            'unexpected full-only gate policy')
    require(values['LINUX_NEARBY_TARGETS'] == ('linux-amd64', 'linux-arm64')
            and values['LINUX_NEARBY_STEPS'] == ('Verify Linux nearby native discovery without skips',),
            'unexpected qualified Linux nearby policy')
    native_steps = values['FAST_STEPS'] + values['LONG_STEPS'] + values['FULL_ONLY_STEPS']
    require(len(set(native_steps)) == len(native_steps), 'overlapping required native gates')
    result = {name: native_steps for name, _ in TARGETS.values()}
    require(not set(native_steps).intersection(values['LINUX_NEARBY_STEPS']), 'overlapping Linux nearby gate')
    for target in values['LINUX_NEARBY_TARGETS']:
        result[TARGETS[target][0]] += values['LINUX_NEARBY_STEPS']
    result[TARGETS['windows-amd64'][0]] += ('Verify Windows receive-retirement directory barriers',)
    result.update({
        'impact': ('Select minimum CI scope',),
        'browser': values['BROWSER_STEPS'],
        'manifest-smoke': ('Exercise signing and verification offline with a disposable test key',),
        'ci-required': ('Require every check selected for this change',),
        'web-activation': ('Prepare locked dependencies and official Chromium', 'Verify fixed Web acceptance policy',
                           'Compile and run seven private synthetic-owner cases'),
        'product-activation': ('Prepare locked dependencies and official Chromium', 'Verify fixed product acceptance policy',
                               'Build normal assets and run two private production-entry cases'),
    })
    return result


def audit_jobs(api, run, required):
    jobs = pages(api, f"actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs", 'jobs')
    index = {}
    for job in jobs:
        require(job.get('run_id') == run['id'] and job.get('run_attempt') == run['run_attempt']
                and job.get('head_sha') == run['head_sha'] and isinstance(job.get('name'), str)
                and job['name'] not in index, 'invalid effective CI job identity')
        index[job['name']] = job
    proof = []
    for name, steps in sorted(required.items()):
        job = index.get(name, {})
        require(job.get('status') == 'completed' and job.get('conclusion') == 'success',
                'required full-validation job did not pass: ' + name)
        for expected, label in TARGETS.values():
            if name == expected:
                require(label in job.get('labels', []), 'native CI runner target mismatch')
        selected = []
        for step in steps:
            matches = [s for s in job.get('steps', []) if s.get('name') == step]
            require(len(matches) == 1 and matches[0].get('status') == 'completed'
                    and matches[0].get('conclusion') == 'success', 'required full-validation step did not pass: ' + step)
            selected.append(step)
        # GitHub may carry successful work into the effective current attempt
        # with new IDs and original timestamps. Do not demand physical reruns.
        proof.append({'name': name, 'id': job['id'], 'effective_attempt': run['run_attempt'],
                      'required_steps_passed': selected})
    return proof


def fingerprint(run):
    result = {key: run.get(key) for key in ('id', 'run_attempt', 'head_sha', 'head_branch', 'name', 'path',
                                           'event', 'status', 'conclusion', 'created_at', 'run_started_at')}
    result['head_commit_id'] = run.get('head_commit', {}).get('id')
    result['head_tree'] = run.get('head_commit', {}).get('tree_id')
    return result


def audit(api, commit, latest=None, local_root=None, previous_proof=None):
    repository = api('')
    require(repository.get('full_name') == REPOSITORY and repository.get('private') is False
            and repository.get('default_branch') == 'main', 'canonical release repository changed')
    require(api('git/ref/heads/main').get('object', {}).get('sha') == commit, 'main moved before full-CI audit')
    run = latest if latest is not None else select_candidate(api, commit)
    identity(run, commit)
    metadata = api(f"actions/runs/{run['id']}")
    require(fingerprint(metadata) == fingerprint(run), 'full-CI attempt changed before audit')
    commit_info = api('git/commits/' + commit)
    tree = commit_info.get('tree', {}).get('sha')
    require(commit_info.get('sha') == commit and bool(re.fullmatch('[0-9a-f]{40}', tree or ''))
            and run.get('head_commit', {}).get('id') == commit
            and run['head_commit'].get('tree_id') == tree, 'full-CI source tree mismatch')
    sources = source_bytes(api, commit, local_root)
    hashes = {path: digest(data) for path, data in sources.items()}
    if previous_proof is not None:
        require(set(previous_proof) == {'schema', 'repository', 'head_sha', 'head_tree', 'source_sha256',
                                        'full_validation', 'jobs', 'required_jobs_sha256'}
                and type(previous_proof.get('schema')) is int and previous_proof['schema'] == 1 and previous_proof.get('repository') == REPOSITORY
                and previous_proof.get('head_sha') == commit and previous_proof.get('head_tree') == tree
                and previous_proof.get('source_sha256') == hashes, 'stored full-CI proof source mismatch')
    required = required_jobs(sources)
    jobs = audit_jobs(api, run, required)
    current = select_candidate(api, commit)
    require(fingerprint(current) == fingerprint(run), 'full-CI selection changed during audit')
    require(fingerprint(api(f"actions/runs/{run['id']}")) == fingerprint(run), 'full-CI attempt changed during audit')
    require(api('git/ref/heads/main').get('object', {}).get('sha') == commit, 'main moved during full-CI audit')
    proof = {'schema': 1, 'repository': REPOSITORY, 'head_sha': commit, 'head_tree': tree,
             'source_sha256': hashes, 'full_validation': fingerprint(run), 'jobs': jobs,
             'required_jobs_sha256': digest(encoded(required))}
    if previous_proof is not None:
        proof['gate_proof_sha256'] = digest(encoded(previous_proof))
    return proof
