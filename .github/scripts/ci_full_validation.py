"""Read-only, exact-source full-CI proof. Never reuse another run's jobs."""
import ast
import base64
import datetime
import hashlib
import importlib.util
import json
from pathlib import Path
import re

REPOSITORY = 'webkaz-labs/sobalink'
WORKFLOW = '.github/workflows/ci.yml'
WINDOWS_PACKAGES_HELPER = '.github/scripts/ci-windows-packages.py'
WINDOWS_PACKAGES_SHA256 = '94e8976ace4f8a54c23fbe02aacd474ac1491e850822bd8e9befb731c4f42070'
# Admit only the reviewed read-only cache prelude before CI's first workload.
# This binds its action pins, paths, trust scopes, keys, and event predicates.
CI_NATIVE_CACHE_PRELUDE_SHA256 = '171cbbb627e6d321d377fbb9442fbcef8b1b8c0b33ec30c697ca8926f85dfef1'
# Reviewed upstream bytes and installer; cache hits never bypass verification.
CI_FONT_POLICY_SHA256 = {
    '.github/actions/japanese-fonts/action.yml': '800ce0427f95dfd4c4fd93a20df0beba15edf4337975646d558a524855653508',
    '.github/fonts/noto-cjk-jp.json': 'd699e475e98fe6ac65a64d4735a8f59413e6250f8ab902bd6325999a34b41ebc',
    '.github/scripts/ci-fonts.py': 'eb5c56f0019098330250157818838f3f9d1b40447c75682e1ca7e34460cbe33e',
}

# Reviewed reusable execution and source-mode boundary; never grants full credit.
NATIVE_UI_POLICY_SHA256 = {
    '.github/workflows/native-ui-checks.yml': '7596e04c5751d5ada7aae0c96088db27e491722101359dc4db96eb5ec0605ae6',
    '.github/scripts/ci-native-ui-source.py': 'cd17be782f7b9ff34a98cc077b9a2f55369a89ce0030653a28e97250c5fdd819',
    '.github/scripts/native_ui_smoke.py': '3d3bcf0a5a7ffc89c095595922e27a4346260ed7cdc2197fd3476327b73c37d7',
}
SOURCES = (WORKFLOW, '.github/actions/japanese-fonts/action.yml', '.github/fonts/noto-cjk-jp.json', '.github/scripts/ci-fonts.py', '.github/scripts/ci-browser-cache.py', '.github/scripts/test_ci_browser_cache.py', '.github/scripts/test_check_frontend.py', '.github/workflows/native-poc.yml', '.github/workflows/native-ui-checks.yml', '.github/scripts/ci-native-ui-source.py',
           '.github/scripts/native_ui_smoke.py', '.github/scripts/ci-regressions.py', '.github/scripts/test_ci_regressions.py', WINDOWS_PACKAGES_HELPER, '.github/scripts/ci-metrics.py', '.github/scripts/ci-impact.py', '.github/scripts/ci-coverage.py',
           '.github/scripts/ci-go-test.py', '.github/scripts/ci_full_validation.py',
           '.github/scripts/ci-browser-summary.py', '.github/scripts/ci-nearby-diagnostic.py',
           '.github/scripts/ci-nearby-screenshots.py',
           '.github/scripts/ci-nearby-native.py', '.github/scripts/ci-nearby-required.py',
           'internal/nearby/dnssd_native_acceptance_test.go', 'internal/nearby/dnssd_capability_test.go',
           '.github/scripts/release-validation.py', '.github/scripts/source_provenance.py')
DNSSD_SOURCES = (
    '.github/workflows/activation-diagnostic.yml',
    '.github/scripts/ci-diagnostic.py',
    '.github/scripts/ci-dnssd-diagnostic.py',
    '.github/scripts/test_ci_dnssd_diagnostic.py',
    '.github/scripts/ci-dnssd-terminal.py',
    '.github/scripts/test_ci_dnssd_terminal.py',
    '.github/scripts/test_ci_dnssd_terminal_origin.py',
    '.github/scripts/test_ci_dnssd_terminal_marker.py',
    '.github/scripts/ci-nearby-native.py',
    '.github/scripts/ci-nearby-diagnostic.py',
    '.github/scripts/ci-go-test.py',
    'internal/nearby/provider_darwin.go',
    'internal/nearby/dnssd_bridge_darwin.c',
    'internal/nearby/dnssd_bridge_darwin.h',
    'internal/nearby/dnssd_native_acceptance_test.go',
    'internal/nearby/dnssd_raw_receive_test.go',
    'internal/nearby/dnssd_capability_test.go',
    'internal/nearby/dnssd_wire_evidence_test.go',
    'internal/nearby/dnssd_legacy_native_test.go',
    'internal/nearby/dnssd_legacy_send_unix_test.go',
    'internal/nearby/dnssd_socket_diagnostic_test.go',
    'internal/nearby/dnssd_socket_native_test.go',
    'internal/nearby/dnssdprobe/probe_darwin.go',
    'go.mod',
    'go.sum',
    '.github/workflows/ci.yml',
    '.github/workflows/prerelease.yml',
    '.github/scripts/ci-dnssd-required.py',
    '.github/scripts/ci-dnssd-terminal-marker.py',
    '.github/scripts/test_ci_dnssd_required.py',
    '.github/scripts/ci-coverage.py',
    '.github/scripts/ci_full_validation.py',
    '.github/scripts/ci-metrics.py',
    '.github/scripts/check-frontend.py',
    'cmd/prepare-engine/main.go',
    'internal/nearby/provider_raw.go',
    'internal/nearby/records.go',
    '.github/scripts/ci-upload-artifact.py',
    '.github/scripts/test_ci_upload_artifact.py',
)
CORE_SOURCES = (
    '.github/workflows/activation-diagnostic.yml',
    '.github/scripts/ci-native-core.py',
    '.github/scripts/test_ci_native_core.py',
    '.github/scripts/ci-diagnostic.py',
    '.github/scripts/test_ci_diagnostic.py',
    '.github/scripts/ci-go-test.py',
    '.github/scripts/ci-metrics.py',
    '.github/scripts/ci-nearby-native.py',
    '.github/scripts/ci-nearby-diagnostic.py',
    'go.mod',
    'go.sum',
    '.github/workflows/ci.yml',
    '.github/scripts/ci-windows-packages.py',
    '.github/workflows/prerelease.yml',
    '.github/scripts/ci-native-core-required.py',
    '.github/scripts/test_ci_native_core_required.py',
    '.github/scripts/ci-dnssd-required.py',
    '.github/scripts/ci-dnssd-diagnostic.py',
    '.github/scripts/ci-dnssd-terminal.py',
    '.github/scripts/ci-coverage.py',
    '.github/scripts/ci_full_validation.py',
    '.github/scripts/check-frontend.py',
    'cmd/prepare-engine/main.go',
    '.github/scripts/ci-upload-artifact.py',
    '.github/scripts/test_ci_upload_artifact.py',
)
SOURCES = tuple(dict.fromkeys((*SOURCES, *DNSSD_SOURCES, *CORE_SOURCES,
    '.github/scripts/ci-pairing-ui.py', 'web/playwright.pairing-ui.config.mjs',
    'web/browser/pairing-v2.acceptance.mjs')))
TARGETS = {
    'linux-amd64': ('native (ubuntu-24.04, linux, amd64)', 'ubuntu-24.04'),
    'linux-arm64': ('native (ubuntu-24.04-arm, linux, arm64)', 'ubuntu-24.04-arm'),
    'darwin-arm64': ('native (macos-26, darwin, arm64)', 'macos-26'),
    'windows-amd64': ('native (windows-2025, windows, amd64)', 'windows-2025'),
}
INTEGRATION_TARGETS = {target: (name.replace('native (', 'native-integration ('), runner)
                       for target, (name, runner) in TARGETS.items()}

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
    # Publication consumes these scripts from the gate artifact, not a new
    # checkout. Verify all loaded tools still match the immutable source.
    for name in ('ci_full_validation.py', 'release-validation.py', 'source_provenance.py',
                 'ci-dnssd-required.py', 'ci-dnssd-diagnostic.py', 'ci-dnssd-terminal.py',
                 'ci-nearby-native.py', 'ci-nearby-diagnostic.py', 'ci-go-test.py',
                 'ci-native-core-required.py', 'ci-native-core.py', 'ci-windows-packages.py', 'ci-metrics.py',
                 'ci-upload-artifact.py'):
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


NATIVE_COMPANION_STEP = '      - name: Test experimental native companion\n        if: matrix.goos == \'windows\' || matrix.goos == \'darwin\'\n        working-directory: poc/native\n        run: |\n          set -euo pipefail\n          unset GOROOT\n          test "$("$SOBALINK_NATIVE_GO" env GOVERSION)" = go1.27.3\n          "$SOBALINK_NATIVE_GO" test -race -count=1 -v -timeout=15m -tags=native_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./...\n          "$SOBALINK_NATIVE_GO" vet ./...\n          test -z "$("$(dirname "$SOBALINK_NATIVE_GO")/gofmt" -l .)"\n'


# Bind the eight reviewed rows, unchanged unit producer names, target runners,
# scope predicate, fail-fast:false and job defaults. A new target/lane is reviewable.
CI_NATIVE_PREPARATION = """      - name: Check exact toolchain and native target
        run: |
          set -euo pipefail
          test "$(go env GOVERSION)" = go1.27.1
          test "$(go env GOOS)" = '${{ matrix.goos }}'
          test "$(go env GOARCH)" = '${{ matrix.goarch }}'
          go run ./cmd/prepare-engine
          go run ./cmd/prepare-engine --verify
          if [[ '${{ matrix.suite }}' == 'integration' ]]; then
            # Offline acceptance must not depend on an earlier unit lane.
            # Fetch the root module's pinned requirements, not broad tool graphs.
            go mod download
            git diff --exit-code -- go.mod go.sum
          fi
          go mod verify
          if [[ '${{ matrix.suite }}' == 'unit' ]]; then
            python -B .github/scripts/ci-regressions.py native --plan "$RUNNER_TEMP/ci-impact-plan.json"
          fi
"""

CI_NATIVE_MATRIX_HEADER_SHA256 = '266c44fe500fa47942dd58733089ede7c625cc5870755a42056dbbe51dd0f1c7'
NATIVE_MATRIX_CONDITIONS = {'uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7': '',
 "Download this run's impact plan": "needs.impact.result == 'success'",
 'Validate impact plan or require full coverage': '',
 'Set up isolated native UI toolchain': "matrix.suite == 'integration' && (matrix.goos == 'windows' || matrix.goos == 'darwin')",
 'Retain native UI toolchain path': "matrix.suite == 'integration' && (matrix.goos == 'windows' || matrix.goos == 'darwin')",
 'uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6': '',
 'Locate Go module and compilation caches': '',
 'Select isolated development Go cache scope': '',
 'Restore trusted main Go caches': "github.repository == 'webkaz-labs/sobalink' && github.ref == 'refs/heads/main' && "
                                   "github.event.repository.default_branch == 'main' && (github.event_name == 'push' || github.event_name "
                                   "== 'workflow_dispatch' || github.event_name == 'schedule')",
 'Restore isolated development Go caches': "steps.development-go-cache-scope.outputs.scope != ''",
 'Restore trusted main seed after development cache miss': "steps.development-go-cache-scope.outputs.scope != '' && "
                                                           "steps.development-go-cache.outputs.cache-matched-key == ''",
 'Report Go cache results': 'always()',
 'Collect required whole-Core race evidence': "matrix.suite == 'unit' && (matrix.goos == 'windows' || matrix.goos == 'darwin')",
 'uses: actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1 # v7.1.0': '',
 'Reproduce locked frontend assets': '',
 'Check exact toolchain and native target': '',
 'Verify macOS five-scope discovery without skips': "matrix.suite == 'unit' && (matrix.goos == 'darwin')",
 'Preserve required macOS discovery proof': "matrix.suite == 'unit' && (always() && matrix.goos == 'darwin')",
 'Verify Windows receive-retirement directory barriers': "matrix.suite == 'unit' && (matrix.goos == 'windows')",
 'Verify remaining native package race coverage': "matrix.suite == 'unit' && (always() && !cancelled() && (matrix.goos == 'windows' || "
                                                  "matrix.goos == 'darwin') && steps.native-core.outputs.remaining_safe == 'true')",
 'Preserve required native package proof': "matrix.suite == 'unit' && (always() && (matrix.goos == 'windows' || matrix.goos == 'darwin'))",
 'Local and mock tests, race detector, and vet': "matrix.suite == 'unit'",
 'Verify adapted engine admission and native underlay denial': "matrix.suite == 'unit'",
 'Verify Core context control over pinned TLS': "matrix.suite == 'integration'",
 'Verify context control over fixed loopback TCP': "matrix.suite == 'integration'",
 'Verify managed session tls': "matrix.suite == 'integration'",
 'Verify managed session outbound-tcp': "matrix.suite == 'integration'",
 'Verify managed session native-caller': "matrix.suite == 'integration'",
 'Verify managed session native-simultaneous': "matrix.suite == 'integration'",
 'Verify managed session native-udp-capacity': "matrix.suite == 'integration'",
 'Verify managed activation acceptance': "matrix.suite == 'integration'",
 'Verify managed restart acceptance': "matrix.suite == 'integration'",
 'Wait for Core context checks': '',
 'Verify direct LAN WireGuard and Core native applications': "matrix.suite == 'integration'",
 'Verify explicit dispatch with three separate Core processes': "matrix.suite == 'integration'",
 'Verify durable outbox restart across Core processes': "matrix.suite == 'integration'",
 'Verify durable text restart and lost ACK across Core processes': "matrix.suite == 'integration'",
 'Wait for direct LAN application checks': '',
 'Verify manual nearby setup across native Core peers': "matrix.suite == 'integration'",
 'Verify TOFU request approval across native Core peers': "matrix.suite == 'integration'",
 'Verify full verification request approval across native Core peers': "matrix.suite == 'integration'",
 'Verify request rejection and cancellation across native Core peers': "matrix.suite == 'integration'",
 'Verify Linux nearby native discovery without skips': "matrix.suite == 'unit' && (matrix.goos == 'linux')",
 'Verify direct LAN synthetic expiry and rekey': "matrix.suite == 'integration'",
 'Verify explicit WAN discovery with isolated native STUN': "matrix.suite == 'integration'",
 'Verify guarded production direct and relay underlays': "matrix.suite == 'integration'",
 'Verify paired transport and Core applications over an isolated relay': "matrix.suite == 'integration'",
 'Verify recovery with the ordinary direct-enabled transport': "matrix.suite == 'integration'",
 'Verify direct LAN session natural rekey and idle lifecycle': "matrix.suite == 'integration' && (steps.ci-plan.outputs.long_required != "
                                                               "'false')",
 'Verify guarded relay real-time lease continuity': "matrix.suite == 'integration' && (steps.ci-plan.outputs.long_required != 'false')",
 'Verify relay-only real-time lease and idle continuity': "matrix.suite == 'integration' && (steps.ci-plan.outputs.long_required != "
                                                          "'false')",
 'Verify native remote resource inspection': "matrix.suite == 'integration' && (steps.ci-plan.outputs.long_required != 'false')",
 'Verify native remote resource management': "matrix.suite == 'integration' && (steps.ci-plan.outputs.long_required != 'false')",
 'Verify native fixed-group resource catalog': "matrix.suite == 'integration' && (steps.ci-plan.outputs.long_required != 'false')",
 'Wait for real-time lifecycle and lease checks': '',
 'Test experimental native companion': "matrix.suite == 'integration' && (matrix.goos == 'windows' || matrix.goos == 'darwin')",
 'Build native package and smoke archive contents': "matrix.suite == 'integration'",
 'Upload native packages': "matrix.suite == 'integration'",
 'Save Go caches after integration checks pass on main': "matrix.suite == 'integration' && (success() && "
                                                         "steps.ci-plan.outputs.long_required != 'false' && github.repository == "
                                                         "'webkaz-labs/sobalink' && github.ref == 'refs/heads/main' && "
                                                         "github.event.repository.default_branch == 'main' && (github.event_name == 'push' "
                                                         "|| github.event_name == 'workflow_dispatch') && steps.go-cache.outputs.cache-hit "
                                                         "!= 'true')",
 'Save development Go caches after integration checks pass': "matrix.suite == 'integration' && (success() && "
                                                             "steps.development-go-cache-scope.outputs.scope != '' && "
                                                             "steps.development-go-cache.outputs.cache-hit != 'true')",
 'Preserve bounded Core failure context': "matrix.suite == 'unit' && (always() && (matrix.goos == 'windows' || matrix.goos == 'darwin') && "
                                          "steps.native-core.outputs.failure_ready == 'true')",
 'Summarize measured native suites': 'always()',
 'Preserve native suite timings': 'always()'}


def validate_native_matrix_workflow(data):
    """Require exhaustive lane ownership, not a normalized legacy workflow."""
    workflow = data.decode('utf-8')
    metadata = workflow_metadata(workflow)
    require(workflow.startswith('name: Cross-platform CI\n'), 'unexpected native workflow identity')
    jobs = [(pos, key) for pos, indent, key, _ in metadata if indent == 2]
    positions = [pos for pos, key in jobs if key == 'native']
    require(len(positions) == 1, 'missing or duplicate native matrix')
    start = positions[0]
    end = next((pos for pos, _ in jobs if pos > start), len(workflow))
    region = workflow[start:end]
    require('    steps:\n' in region, 'missing native matrix steps')
    header = region.split('    steps:\n', 1)[0] + '    steps:\n'
    require(digest(header.encode()) == CI_NATIVE_MATRIX_HEADER_SHA256, 'native matrix identity or failure policy differs')
    seen = set()
    for block in re.findall(r'^      - .*?(?=^      - |\Z)', region, re.M | re.S):
        first = block.splitlines()[0].removeprefix('      - ')
        name = first.removeprefix('name: ') if first.startswith('name: ') else first
        require(name in NATIVE_MATRIX_CONDITIONS and name not in seen, 'unclassified or duplicate native matrix step')
        seen.add(name)
        conditions = re.findall(r'^        if: (.+)$', block, re.M)
        expected = NATIVE_MATRIX_CONDITIONS[name]
        require(conditions == ([expected] if expected else []), 'native matrix step ownership differs: ' + name)
        if name != "Download this run's impact plan":
            require(not re.search(r'^        continue-on-error:', block, re.M), 'native matrix failure cannot be ignored')
    require(seen == set(NATIVE_MATRIX_CONDITIONS), 'native matrix step inventory differs')
    require(region.count(CI_NATIVE_PREPARATION) == 1, 'native lane dependency preparation differs')
    require(region.count('name: native-timings-${{ matrix.suite }}-${{ matrix.goos }}-${{ matrix.goarch }}') == 1,
            'native matrix timing artifacts must be isolated')
    require(region.count('name: package-${{ matrix.goos }}-${{ matrix.goarch }}') == 1,
            'native matrix package artifact identity differs')
    require(region.count('uses: actions/cache/save@') == 2,
            'native matrix must retain one writer per cache namespace')
    preparation = """          if [[ '${{ matrix.suite }}' == 'unit' ]]; then
            python -B .github/scripts/ci-regressions.py native --plan "$RUNNER_TEMP/ci-impact-plan.json"
          fi
"""
    require(region.count(preparation) == 1, 'native Python regressions must run once per target')


def native_lane_expected(block, suite, workflow):
    """Add the reviewed CI-only predicate to expected text, never to input text.

    Prerelease keeps its unconditional gates. This does not normalize or relax
    an actual workflow; callers still compare the complete expected block.
    """
    if not workflow.startswith('name: Cross-platform CI\n'):
        return block
    require(suite in ('unit', 'integration'), 'unknown native suite')
    def guarded(match):
        step = match.group()
        condition = re.search(r'^        if: (.+)$', step, re.M)
        guard = "matrix.suite == '" + suite + "'"
        if condition:
            return step[:condition.start()] + '        if: ' + guard + ' && (' + condition[1] + ')' + step[condition.end():]
        first = step.index('\n') + 1
        return step[:first] + '        if: ' + guard + '\n' + step[first:]
    return re.sub(r'^      - .*?(?=^      - |\Z)', guarded, block, flags=re.M | re.S)


def validate_native_companion_workflow(data):
    workflow = data.decode("utf-8")
    require(workflow.count(native_lane_expected(NATIVE_COMPANION_STEP, 'integration', workflow)) == 1, "native companion test gate differs")


# Bind the entire reviewed jobs, not just the concurrency keys. This includes
# setup actions, conditional installation, disjoint writer commands, the joins,
# and every consumer. Extra steps/overrides cannot hide outside a hashed prelude.
CI_PREPARATION_JOB_SHA256 = {
    'documents': 'ec5a17c2b3f063f0681dad5328fd238620bfbf400503181090ac062d976b86bc',
    'product-activation': 'f589adb4e68ae6f7c78a9ae09edd9ecd7c8d098d22d98c9485921d2062244c9a',
    'web-activation': '59ea77cb223611e42bbee0a12d733d8085a0fefb2259b268e6a78aba0df9d3cf',
}


def validate_preparation_overlap_workflow(data):
    """Require closed docs and activation dependency DAGs before proof credit.

    The docs group has an implicit failure join. Activation has one Go-only
    background writer, one foreground npm/Chromium chain, and an explicit wait
    before any acceptance consumer. No cancellation or relaxed failure is valid.
    """
    workflow = data.decode('utf-8')
    jobs = [(pos, key) for pos, indent, key, _ in workflow_metadata(workflow) if indent == 2]
    for name, expected in CI_PREPARATION_JOB_SHA256.items():
        positions = [pos for pos, key in jobs if key == name]
        require(len(positions) == 1, 'missing or duplicate preparation job: ' + name)
        start = positions[0]
        end = next((pos for pos, _ in jobs if pos > start), len(workflow))
        require(digest(workflow[start:end].encode()) == expected,
                'preparation job dependency or failure policy differs: ' + name)


# Review the complete bounded two-chain region, including commands, ownership,
# timeout bounds, private metrics file and explicit failure join. This is not
# test-result reuse: the runtime audit still requires each gate and its wait.
CI_APPLICATION_OVERLAP_SHA256 = '27da3c865648303e6ff73cd17ef0d40cbdf2a4d83e1f1aa56dfc9b1cec22ec9e'


def validate_application_overlap_workflow(data):
    workflow = data.decode('utf-8')
    start = '      - name: Verify direct LAN WireGuard and Core native applications\n'
    end = '      - name: Verify manual nearby setup across native Core peers\n'
    require(workflow.count(start) == workflow.count(end) == 1,
            'missing or duplicate native application overlap boundary')
    region = workflow[workflow.index(start):workflow.index(end)]
    require(digest(region.encode()) == CI_APPLICATION_OVERLAP_SHA256,
            'native application overlap commands or failure policy differ')
    require(workflow.index('      - name: Check exact toolchain and native target\n')
            < workflow.index(start) < workflow.index(end)
            < workflow.index('      - name: Verify direct LAN session natural rekey and idle lifecycle\n'),
            'application overlap must follow preparation and join before real-time gates')
    summary = ('      - name: Summarize measured native suites\n'
               '        if: always()\n'
               '        run: python .github/scripts/ci-metrics.py summary '
               '--include "$RUNNER_TEMP/sobalink-ci-directlan-metrics.jsonl" '
               '--include "$RUNNER_TEMP/sobalink-ci-context-metrics.jsonl" '
               '--output "$RUNNER_TEMP/ci-suite-metrics.json"\n')
    require(workflow.count(summary) == 1 and workflow.index(summary) > workflow.index(end),
            'parallel application metrics must be summarized after their join')


CI_CONTEXT_OVERLAP_SHA256 = '5718f69c171e808c909d5e8958bae48373874ed9d574b06b5a6c72d5f856b5c7'


def validate_context_overlap_workflow(data):
    """Bind one private Core fixture chain and the unchanged managed gate order."""
    workflow = data.decode('utf-8')
    start = '      - name: Verify Core context control over pinned TLS\n'
    end = '      - name: Verify direct LAN WireGuard and Core native applications\n'
    require(workflow.count(start) == workflow.count(end) == 1,
            'missing or duplicate Core context overlap boundary')
    region = workflow[workflow.index(start):workflow.index(end)]
    require(digest(region.encode()) == CI_CONTEXT_OVERLAP_SHA256,
            'Core context overlap commands or failure policy differ')
    require(workflow.index('      - name: Check exact toolchain and native target\n')
            < workflow.index(start) < workflow.index(end),
            'Core context overlap must follow preparation and join before application checks')


def validate_parallel_workflow(data):
    """Fail closed on changes to the deliberately narrow native async region.

    This is a source-policy check, not a general YAML parser. The approved
    spelling/layout is intentionally literal so unexpected syntax needs review.
    Runtime proof still requires every individual gate and the wait to succeed.
    """
    workflow = data.decode('utf-8')
    metadata = workflow_metadata(workflow)
    validate_application_overlap_workflow(data)
    validate_context_overlap_workflow(data)
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
        header = native_lane_expected(header, 'integration', workflow)
        require(workflow.count(header) == 1, 'invalid real-time background step policy')
        starts.append(workflow.index(header))
    fence = (marker + 'Wait for real-time lifecycle and lease checks\n'
             '        wait: [natural-lifecycle, guarded-lease, relay-only-lease]\n')
    package = marker + 'Build native package and smoke archive contents\n'
    require(workflow.count(fence + native_lane_expected(NATIVE_COMPANION_STEP, 'integration', workflow) + package) == 1, 'missing real-time wait before packaging')
    end = workflow.index(fence)
    require(starts == sorted(starts) and starts[-1] < end, 'invalid real-time step order')
    region = workflow[starts[0]:end]
    expected = tuple(name for name, _, _ in groups) + (
        'Verify native remote resource inspection',
        'Verify native remote resource management',
        'Verify native fixed-group resource catalog',
    )
    require(tuple(re.findall(r'^      - name: ([^\n]+)$', region, re.M)) == expected
            and len(re.findall(r'^      - ', region, re.M)) == len(expected),
            'unexpected work inside real-time parallel region')
    # Docs, native fixture gates, activation and browser preparation are the
    # only async regions. Each has a separate closed source policy below.
    controls = [key for _, _, key, _ in metadata
                if key in {'background', 'parallel', 'wait', 'wait-all', 'cancel'}]
    require(controls == ['parallel',
                         'background', 'wait',
                         'background', 'wait',
                         'background', 'background', 'background', 'wait',
                         'background', 'wait', 'background', 'wait',
                         'background', 'wait', 'background', 'wait'],
            'unexpected workflow concurrency or failure policy')
    require(not any(key == 'continue-on-error' and starts[0] <= pos < end + len(fence)
                    for pos, _, key, _ in metadata), 'real-time failures cannot be ignored')
    require(workflow.index(marker + 'Verify recovery with the ordinary direct-enabled transport\n') < starts[0],
            'real-time checks must follow serial native checks')
    # The three resource commands do not use the shared timing writer. Only
    # natural-lifecycle records a metric while these gates overlap.
    require(region.count('.github/scripts/ci-metrics.py run') == 1
            and region.count('--suite natural-lifecycle --') == 1,
            'parallel timing writers require isolated storage')


def validate_fixture_diagnostics_workflow(data):
    """Require all eleven privacy regressions before browser acceptance."""
    workflow = data.decode('utf-8')
    expected = """      - name: Verify privacy-safe fixture diagnostics
        timeout-minutes: 10
        run: |
          set -euo pipefail
          python - <<'PYTHON'
          import os
          import subprocess
          import sys

          # Browser fixtures use POSIX private-file permissions. Keep these
          # bounded diagnostic regressions on the existing Linux browser lane.
          actual = subprocess.check_output(["go", "env", "GOVERSION", "GOOS", "GOARCH"], text=True, timeout=30).splitlines()
          if actual != ["go1.27.1", "linux", "amd64"]:
              raise SystemExit("Fixture diagnostics require the exact browser toolchain and target")
          groups = (
              ("cmd/soba-e2e", "soba_e2e", (
                  "TestStartupDiagnosticClosedVocabulary",
                  "TestStartupArgumentFailureWritesDiagnostic",
              )),
              ("internal/core", "lanlink_integration,ts_omit_udptransport", (
                  "TestNativeLANCommandDiagnosticRetainsOnlyAllowedCodes",
                  "TestNativeLANCommandDiagnosticPreservesProductionListenClassification",
                  "TestNativeLANHeldRelayPortDiagnosticIntegration",
              )),
              ("internal/directlan", "resource_inspection_native", (
                  "TestInspectionDialResultClassifications",
                  "TestInspectionDialResultStagesAndPrivacy",
                  "TestInspectionDialResultFirstTerminalResult",
                  "TestInspectionDialResultNoObservation",
                  "TestInspectionDialInitialWakeObservation",
                  "TestInspectionDialResultNeverFormatsError",
              )),
          )
          for package, tags, tests in groups:
              # Only each child receives the isolated environment. The held-port
              # negative control uses an owned numeric loopback listener.
              env = {key: value for key, value in os.environ.items()
                     if key.upper() not in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "TS_PROXY")
                     and not key.upper().startswith("TS_")}
              if package == "internal/core":
                  env["SOBALINK_RUN_LAN_INTEGRATION"] = "1"
              command = [sys.executable, ".github/scripts/ci-go-test.py"]
              for name in tests:
                  command += ["--expect", "github.com/webkaz-labs/sobalink/" + package + ":" + name]
              command += ["--", "go", "test", "-race", "-count=1", "-v", "-timeout=2m",
                          "-tags=" + tags + ",ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy",
                          "-run=^(" + "|".join(tests) + ")$", "./" + package]
              subprocess.run(command, env=env, check=True, timeout=3 * 60)
          PYTHON
"""
    marker = '      - name: Verify privacy-safe fixture diagnostics\n'
    require(workflow.count(marker) == 1, 'missing or duplicate fixture diagnostic gate')
    browser = workflow.index('\n  browser:\n')
    start = workflow.index(marker)
    next_job = re.search(r'^  [A-Za-z0-9_-]+:\n', workflow[browser + len('\n  browser:\n'):], re.M)
    browser_end = browser + len('\n  browser:\n') + next_job.start() if next_job else len(workflow)
    region = workflow[browser:browser_end]
    prepare = """      # This preparer imports only the adaptation helpers, not embedded Web assets.
      - name: Prepare verified browser engine
        id: browser-engine
        background: true
        timeout-minutes: 10
        run: |
          set -euo pipefail
          go run ./cmd/prepare-engine
          go run ./cmd/prepare-engine --verify
          go mod verify
"""
    node = "      - uses: actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1 # v7.1.0\n"
    install = """      - name: Wait for verified browser engine
        wait: browser-engine
      - name: Prepare verified Japanese fonts
        timeout-minutes: 5
        uses: ./.github/actions/japanese-fonts
      - name: Install official Chromium and fonts
        id: browser-chromium
        background: true
        run: |
          set -euo pipefail
          node web/node_modules/@playwright/test/cli.js install --with-deps chromium
"""
    ready = """      - name: Wait for browser runtime preparation
        wait: browser-chromium
      - name: Verify actual UI workflows with isolated Playwright fixtures
"""
    require(region.count(prepare + node) == 1 and region.count(install + expected + ready) == 1,
            'invalid browser preparation overlap or wait policy')
    require(region.index('      - uses: actions/setup-go@') < region.index(prepare)
            < region.index('      - name: Test and reproduce locked frontend assets\n')
            < region.index(install), 'browser preparation dependency order differs')
    require(browser < start < browser_end and workflow.count(expected) == 1,
            'invalid fixture diagnostic execution policy')


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
    expected = native_lane_expected(expected, 'integration', workflow)
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
            and workflow.index(recovery) < workflow.index(parallel) < start
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
    expected = native_lane_expected(expected, 'integration', workflow)
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
            and workflow.index(parallel) < workflow.index(inspection) < start
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
    expected = native_lane_expected(expected, 'integration', workflow)
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
    fence = '      - name: Wait for real-time lifecycle and lease checks\n'
    require(workflow.count(management) == workflow.count(parallel) == workflow.count(fence) == 1
            and workflow.index(parallel) < workflow.index(management) < start < workflow.index(fence)
            and end == workflow.index(fence), 'native fixed-group catalog must finish immediately before the join')
    outside = workflow[:start] + workflow[end:]
    require('SOBALINK_RUN_RESOURCE_GROUP_CATALOG_NATIVE' not in outside,
            'native fixed-group catalog opt-in escaped its step')
    return start, end


def validate_outbox_workflow(workflow_bytes):
    """Require the exact no-skip native and real-browser durable outbox gates."""
    workflow = workflow_bytes.decode('utf-8')
    expected = ("      - name: Verify durable outbox restart across Core processes\n        timeout-minutes: 6\n        env:\n          SOBALINK_RUN_DIRECT_INTEGRATION: '1'\n        run: |\n          set -euo pipefail\n          python .github/scripts/ci-metrics.py run --suite outbox-process-restart -- python .github/scripts/ci-go-test.py --exact --expect github.com/webkaz-labs/sobalink/internal/core:TestOutboxProcessRestart -- go test -race -count=1 -timeout=4m -tags=lanlink_integration,directlan_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./internal/core -run '^TestOutboxProcessRestart$'\n",
                "      - name: Verify durable outbox recovery with separate agents and actual browser\n        timeout-minutes: 8\n        env:\n          SOBALINK_RUN_DIRECT_INTEGRATION: '1'\n          SOBALINK_RUN_TRANSFER_BROWSER_NATIVE: '1'\n          SOBA_OUTBOX_BROWSER_SUMMARY: ${{ runner.temp }}/soba-playwright-screenshots/outbox-native-summary.json\n        run: |\n          set -euo pipefail\n          python .github/scripts/ci-go-test.py --exact --expect github.com/webkaz-labs/sobalink/internal/core:TestOutboxThreeProcessBrowser -- go test -race -tags=lanlink_integration,directlan_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy ./internal/core -run '^TestOutboxThreeProcessBrowser$' -count=1 -timeout=4m\n")
    expected = (native_lane_expected(expected[0], 'integration', workflow), expected[1])
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
    expected = native_lane_expected(expected, 'integration', workflow)
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


REQUEST_SETUP_CASES = (
    ('Verify TOFU request approval across native Core peers', 'nearby-request-tofu-core', 'TestLANCoreRequestTOFUIntegration'),
    ('Verify full verification request approval across native Core peers', 'nearby-request-full-core', 'TestLANCoreRequestFullVerificationIntegration'),
    ('Verify request rejection and cancellation across native Core peers', 'nearby-request-reject-cancel-core', 'TestLANCoreRequestRejectCancelIntegration'),
)


def validate_request_setup_workflow(workflow_bytes):
    """Require all request-approval cases in every native CI/release lane."""
    workflow = workflow_bytes.decode("utf-8")
    jobs = [(offset, key) for offset, indent, key, _ in workflow_metadata(workflow) if indent == 2]
    native = [offset for offset, key in jobs if key == "native"]
    require(len(native) == 1, "missing request setup native job")
    end = next((offset for offset, _ in jobs if offset > native[0]), len(workflow))
    for name, suite, test in REQUEST_SETUP_CASES:
        block = f"""      - name: {name}
        timeout-minutes: 6
        env:
          SOBALINK_RUN_LAN_INTEGRATION: '1'
        run: |
          set -euo pipefail
          python .github/scripts/ci-metrics.py run --suite {suite} -- python .github/scripts/ci-go-test.py --exact --expect github.com/webkaz-labs/sobalink/internal/core:{test} -- go test -race -count=1 -timeout=3m -tags=lanlink_integration,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy,ts_omit_udptransport ./internal/core -run '^{test}$'
"""
        block = native_lane_expected(block, 'integration', workflow)
        marker = block.splitlines(keepends=True)[0]
        require(workflow.count(marker) == 1 and workflow.count(block) == 1, "invalid request setup execution policy")
        start = workflow.index(marker)
        require(native[0] < start < end and workflow[start + len(block):].startswith("      - "),
                "invalid request setup native step boundary")


def validate_pairing_ui_workflow(workflow_bytes):
    """Bind the synthetic UI gate separately from native acceptance."""
    workflow = workflow_bytes.decode("utf-8")
    block = """      - name: Verify synthetic request pairing UI without skips
        timeout-minutes: 6
        run: |
          set -euo pipefail
          python -B .github/scripts/ci-pairing-ui.py
"""
    marker = block.splitlines(keepends=True)[0]
    require(workflow.count(marker) == 1 and workflow.count(block) == 1, "invalid synthetic pairing UI gate")
    jobs = [(offset, key) for offset, indent, key, _ in workflow_metadata(workflow) if indent == 2]
    browser = [offset for offset, key in jobs if key == "browser"]
    require(len(browser) == 1, "missing pairing UI browser job")
    end = next((offset for offset, _ in jobs if offset > browser[0]), len(workflow))
    start = workflow.index(marker)
    require(browser[0] < start < end and workflow[start + len(block):].startswith("      - "),
            "invalid pairing UI browser step boundary")


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
        id: setup-browser
        timeout-minutes: 8
        env:
          SOBALINK_RUN_LAN_INTEGRATION: '1'
          SOBALINK_RUN_SETUP_BROWSER_NATIVE: '1'
          SOBA_SETUP_BROWSER_SUMMARY: ${{ runner.temp }}/soba-setup-browser-evidence/setup-native-summary.json
          SOBA_SETUP_BROWSER_FAILURE: ${{ runner.temp }}/soba-setup-browser-evidence/setup-native-failure.json
          SOBA_SETUP_BROWSER_SCREENSHOTS: ${{ runner.temp }}/soba-setup-browser-illustrations
        run: |
          set -euo pipefail
          mkdir -m 700 "$RUNNER_TEMP/soba-setup-browser-evidence"
          export SOBA_SETUP_BROWSER_SCREENSHOTS_PREPARED=0
          if mkdir -m 700 "$SOBA_SETUP_BROWSER_SCREENSHOTS" 2>/dev/null; then
            export SOBA_SETUP_BROWSER_SCREENSHOTS_PREPARED=1
          else
            unset SOBA_SETUP_BROWSER_SCREENSHOTS
          fi
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
        if job == 'native':
            block = native_lane_expected(block, 'integration', workflow)
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
    expected = native_lane_expected(expected, 'unit', workflow)
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


def validate_macos_nearby_workflow(workflow_bytes):
    """One required five-scope Terminal run and its mandatory closed artifact."""
    workflow = workflow_bytes.decode('utf-8')
    metadata = workflow_metadata(workflow)
    preparation = CI_NATIVE_PREPARATION
    expected = """      - name: Verify macOS five-scope discovery without skips
        if: matrix.goos == 'darwin'
        timeout-minutes: 10
        env:
          CGO_ENABLED: '1'
        run: |
          set -euo pipefail
          python -B .github/scripts/ci-metrics.py run --suite nearby-five-scope-macos -- python -B .github/scripts/ci-dnssd-required.py
      - name: Preserve required macOS discovery proof
        if: always() && matrix.goos == 'darwin'
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7
        with:
          name: soba-dnssd-required-${{ github.run_id }}-${{ github.run_attempt }}
          path: ${{ runner.temp }}/soba-dnssd-required/receipt.json
          if-no-files-found: error
          retention-days: 90
"""
    expected = native_lane_expected(expected, 'unit', workflow)
    marker = expected.splitlines(keepends=True)[0]
    require(workflow.count(marker) == 1 and workflow.count(expected) == 1,
            'invalid macOS nearby execution/export policy')
    start = workflow.index(marker)
    require(workflow[start + len(expected):].startswith('      - '), 'invalid macOS nearby step boundary')
    jobs = [(offset, key) for offset, indent, key, _ in metadata if indent == 2]
    native = [offset for offset, key in jobs if key == 'native']
    require(len(native) == 1, 'missing macOS nearby native job')
    end = next((offset for offset, _ in jobs if offset > native[0]), len(workflow))
    require(native[0] < start < end, 'macOS nearby must run in the native job')
    region = workflow[native[0]:end]
    require(region.count(preparation + expected) == 1,
            'macOS nearby must immediately follow successful exact preparation')
    require(region.index('      - name: Reproduce locked frontend assets\n')
            < region.index(preparation) < region.index(expected)
            < region.index('      - name: Verify remaining native package race coverage\n'),
            'macOS nearby must precede unrelated native gates after preparation')
    outside = workflow[:start] + workflow[start + len(expected):]
    require('ci-dnssd-required.py' not in outside and 'soba-dnssd-required' not in outside
            and 'SOBALINK_DNSSD_' not in workflow and 'TestDNSService' not in workflow
            and 'nearby-selected-interface-macos' not in workflow,
            'duplicate or fallback macOS discovery gate')


def validate_native_packages_workflow(sources):
    """Require the reviewed collector, disjoint complement, and both routes."""
    require(digest(sources[WINDOWS_PACKAGES_HELPER]) == WINDOWS_PACKAGES_SHA256,
            'package partition runner differs from reviewed policy')
    require(digest(sources['.github/scripts/ci-native-core.py']) == 'd5e6b47a41cfd608e3d4f34a9f9949a351b07ac60299243ca1d68d677b881389',
            'native Core collector differs from reviewed policy')
    require(digest(sources['.github/scripts/ci-native-core-required.py']) == '23d78172edea2511cc2fae956bf35bd8394c62023fed62469b25e015e8079131',
            'required native Core runner differs from reviewed policy')
    early = """      - name: Collect required whole-Core race evidence
        id: native-core
        if: matrix.goos == 'windows' || matrix.goos == 'darwin'
        env:
          SOBALINK_CORE_NATIVE_PLATFORM: ${{ matrix.goos }}-${{ matrix.goarch }}
        run: python -B .github/scripts/ci-native-core-required.py core
"""
    later = """      - name: Verify remaining native package race coverage
        if: always() && !cancelled() && (matrix.goos == 'windows' || matrix.goos == 'darwin') && steps.native-core.outputs.remaining_safe == 'true'
        env:
          SOBALINK_CORE_NATIVE_PLATFORM: ${{ matrix.goos }}-${{ matrix.goarch }}
        run: python -B .github/scripts/ci-native-core-required.py remaining
      - name: Preserve required native package proof
        if: always() && (matrix.goos == 'windows' || matrix.goos == 'darwin')
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7
        with:
          name: soba-native-core-required-${{ matrix.goos }}-${{ matrix.goarch }}-${{ github.run_id }}-${{ github.run_attempt }}
          path: ${{ runner.temp }}/soba-native-core-required/receipt.json
          if-no-files-found: error
          retention-days: 14
"""
    failure = """      - name: Preserve bounded Core failure context
        if: always() && (matrix.goos == 'windows' || matrix.goos == 'darwin') && steps.native-core.outputs.failure_ready == 'true'
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7
        with:
          name: soba-native-core-failure-${{ matrix.goos }}-${{ matrix.goarch }}-${{ github.run_id }}-${{ github.run_attempt }}
          path: |
            ${{ runner.temp }}/soba-native-core-failure/failure.json
            ${{ runner.temp }}/soba-native-core-failure/partial-core.json
          if-no-files-found: error
          retention-days: 14
"""
    ordinary = """      - name: Local and mock tests, race detector, and vet
        run: |
          set -euo pipefail
          if [[ '${{ matrix.goos }}' == 'linux' ]]; then
            python .github/scripts/ci-metrics.py run --suite local-race -- go test -race -count=1 -timeout=10m ./...
          fi
          # Enforce offline route-revocation recovery using the real protected fixture.
          python .github/scripts/ci-metrics.py run --suite offline-route-race -- go test -race -count=1 -tags=soba_e2e,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy -run='^TestRoute(BrowserFixture|OfflineFailed)' -timeout=2m ./internal/core
          # Repeat real native IPC cleanup; exact timer policy is tested virtually.
          python .github/scripts/ci-metrics.py run --suite ipc-repeat-race -- go test -race -count=5 -timeout=2m ./internal/control
          python .github/scripts/ci-metrics.py run --suite native-vet -- go vet ./...
          unformatted="$(gofmt -l cmd internal web)"
          if [[ -n "$unformatted" ]]; then
            printf 'Go files need gofmt (check checkout line endings):\\n%s\\n' "$unformatted" >&2
            exit 1
          fi
"""
    release = """      - name: Native race tests (local and mocked services only)
        run: |
          set -euo pipefail
          if [[ '${{ matrix.goos }}' == 'linux' ]]; then
            go test -race -count=1 -timeout=10m ./... 2>&1 | tee logs/race-tests.txt
          fi
          # Enforce offline route-revocation recovery using the real protected fixture.
          go test -race -count=1 -tags=soba_e2e,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy -run='^TestRoute(BrowserFixture|OfflineFailed)' -timeout=2m ./internal/core 2>&1 | tee -a logs/race-tests.txt
          # Exercise actual named-pipe/Unix cleanup repeatedly before publication.
          go test -race -count=25 -timeout=3m ./internal/control 2>&1 | tee logs/ipc-regression.txt
"""
    setup_go = """      - uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6
        with:
          go-version: '1.27.1'
          cache: false
"""
    setup_node = """      - uses: actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1 # v7.1.0
        with:
          node-version: '24.19.0'
          check-latest: false
          package-manager-cache: false
"""
    for path, tail in ((WORKFLOW, ordinary), ('.github/workflows/prerelease.yml', release)):
        workflow = sources[path].decode('utf-8')
        early_step = native_lane_expected(early, 'unit', workflow)
        later_step = native_lane_expected(later, 'unit', workflow)
        failure_step = native_lane_expected(failure, 'unit', workflow)
        tail_step = native_lane_expected(tail, 'unit', workflow) if path == WORKFLOW else tail
        metadata = workflow_metadata(workflow)
        jobs = [(offset, key) for offset, indent, key, _ in metadata if indent == 2]
        native = [offset for offset, key in jobs if key == 'native']
        require(len(native) == 1, 'missing native package job')
        end = next((offset for offset, _ in jobs if offset > native[0]), len(workflow))
        for block in (early_step, later_step + tail_step, failure_step):
            marker = block.splitlines(keepends=True)[0]
            require(workflow.count(marker) == workflow.count(block) == 1,
                    'invalid native package execution or receipt policy')
            start = workflow.index(marker)
            require(native[0] < start < end and workflow[start + len(block):].startswith('      - '),
                    'invalid native package step boundary')
        region = workflow[native[0]:end]
        if path == WORKFLOW:
            require(region.count(setup_go) == 1 and region.count(early_step + setup_node) == 1,
                    'Core must follow exact Go and cache setup before other workloads')
            setup_start, core_start = region.index(setup_go), region.index(early_step)
            require(setup_start < core_start, 'exact Go setup must precede Core collection')
            prelude = region[setup_start + len(setup_go):core_start]
            require(digest(prelude.encode('utf-8')) == CI_NATIVE_CACHE_PRELUDE_SHA256,
                    'early CI cache setup differs from reviewed read-only policy')
        else:
            require(region.count(setup_go + early_step + setup_node) == 1,
                    'release Core must immediately follow exact Go setup before other work or cache restore')
        require(region.index('      - uses: actions/setup-go@') < region.index(early_step)
                < region.index('      - uses: actions/setup-node@')
                < region.index('      - name: Reproduce locked frontend assets\n')
                < region.index(later_step), 'early Core setup ordering differs')
        require(workflow.count('python -B .github/scripts/ci-native-core-required.py core') == 1
                and workflow.count('python -B .github/scripts/ci-native-core-required.py remaining') == 1
                and 'python -B .github/scripts/ci-windows-packages.py' not in workflow
                and 'GOMAXPROCS' not in workflow and 'GOTRACEBACK' not in workflow,
                'native package runner escaped its exact steps')


def validate_browser_cache_workflow(sources):
    """Bind cache scope, exact inputs and post-success save; never skip tests."""
    require(digest(sources['.github/scripts/ci-browser-cache.py']) == '0657be81ee4cb662d205b281548b1645c1e6e2184ef19749d2a55180940f937b',
            'browser cache selector differs from reviewed policy')
    workflow = sources[WORKFLOW].decode('utf-8')
    jobs = [(pos, key) for pos, indent, key, _ in workflow_metadata(workflow) if indent == 2]
    starts = [pos for pos, key in jobs if key == 'browser']
    require(len(starts) == 1, 'browser cache job missing or duplicated')
    start = starts[0]
    end = next((pos for pos, _ in jobs if pos > start), len(workflow))
    region = workflow[start:end]
    restore_start = '      # Browser caches are acceleration only, never native/release evidence.\n'
    save_start = '      - name: Save isolated browser Go cache after all checks pass\n'
    require(workflow.count(restore_start) == 1 and workflow.count(save_start) == 1,
            'browser cache steps escaped the required job')
    require(restore_start in region and save_start in region, 'missing browser cache steps')
    a = region.index(restore_start)
    report = region.index('      - name: Report browser Go cache result\n', a)
    b = region.index('      - ', report + len('      - name: Report browser Go cache result\n'))
    require(digest(region[a:b].encode()) == 'e8b50e17f60117fd0588bd781e2454904fca8064d29ec5cb7885a8d9c314e01b',
            'browser cache restore policy differs')
    require(region[:a].endswith("          go-version: '1.27.1'\n          cache: false\n"),
            'browser cache must immediately follow exact Go setup')
    tail = region[region.index(save_start):].rstrip() + '\n'
    require(digest(tail.encode()) == 'b24ac384b3a1a4d9527f4171257f46209d2711763ab7b2ad3a0bbe5c3892cf3c',
            'browser cache must save only after all work succeeds')


def validate_frontend_workflow(sources):
    """Require native reproduction and same-source browser tests in full proof."""
    require(digest(sources['.github/scripts/check-frontend.py']) == 'e56e4ae637f86134c7215f40f39b2d29cd6302384bf2301acd30a4bfda803a46',
            'frontend checker differs from reviewed policy')
    native = """      - name: Reproduce locked frontend assets
        run: |
          npm install --global npm@11.9.0 --no-audit --no-fund
          python .github/scripts/check-frontend.py --build-only
"""
    browser = """      - name: Test and reproduce locked frontend assets
        run: |
          set -euo pipefail
          npm install --global npm@11.9.0 --no-audit --no-fund
          python .github/scripts/check-frontend.py
          git diff --exit-code -- web/dist
"""
    for path, expected in ((WORKFLOW, (('native', native), ('browser', browser))),
                           ('.github/workflows/prerelease.yml', (('native', native),))):
        workflow = sources[path].decode('utf-8')
        jobs = [(pos, key) for pos, indent, key, _ in workflow_metadata(workflow) if indent == 2]
        require(workflow.count('python .github/scripts/check-frontend.py') == len(expected),
                'frontend checks escaped their required steps')
        for job, block in expected:
            positions = [pos for pos, key in jobs if key == job]
            require(len(positions) == 1, 'missing or duplicate frontend job')
            start = positions[0]
            end = next((pos for pos, _ in jobs if pos > start), len(workflow))
            region = workflow[start:end]
            marker = block.splitlines(keepends=True)[0]
            require(region.count(marker) == 1 and region.count(block) == 1,
                    'frontend execution differs from reviewed policy')
            require(region[region.index(block) + len(block):].startswith('      - '),
                    'invalid frontend step boundary')
    browser_header = """  browser:
    # Real Go management HTTP and embedded assets; networking peers are fixtures.
    needs: impact
    if: always() && !cancelled() && github.event.repository.private == false && needs.impact.outputs.scope != 'docs' && needs.impact.outputs.scope != 'go' && needs.impact.outputs.scope != 'native-ui'
    runs-on: ubuntu-24.04
    timeout-minutes: 25
    steps:
"""
    require(sources[WORKFLOW].decode('utf-8').count(browser_header) == 1,
            'common frontend tests must run for full and native-short scopes')


def validate_regression_workflow(sources):
    """Portable regressions must finish in addition to every native lane."""
    require(digest(sources['.github/scripts/ci-regressions.py']) == 'b3539272f38593e6608b350bf933b01200fd34b1b810c77e6f5dad0388243ac8',
            'regression selector differs from reviewed policy')
    workflow = sources[WORKFLOW].decode('utf-8')
    metadata = workflow_metadata(workflow)
    jobs = [(offset, key) for offset, indent, key, _ in metadata if indent == 2]
    selected = [offset for offset, key in jobs if key == 'policy-regressions']
    require(len(selected) == 1, 'missing or duplicate shared regression job')
    start = selected[0]
    end = next((offset for offset, _ in jobs if offset > start), len(workflow))
    require(digest(workflow[start:end].encode('utf-8')) == 'ebbfd1545f49814702136f1cc8db2ae1d6c44712b4542f106c2014db752bb956',
            'shared regression execution differs from reviewed policy')
    require(workflow.count('python -B .github/scripts/ci-regressions.py common --plan') == 1
            and workflow.count('python -B .github/scripts/ci-regressions.py native --plan') == 1,
            'regression execution escaped its required steps')
    require(workflow.count('needs: [impact, documents, policy-regressions, native, native-ui, browser, go-unit, manifest-smoke]') == 1,
            'aggregate must wait for shared regressions')


def validate_native_ui_workflow(sources):
    """Bind the shared commands and caller/source boundary, not manual success."""
    for path, expected in NATIVE_UI_POLICY_SHA256.items():
        require(digest(sources[path]) == expected, 'native UI reusable policy differs')
    callers = (
        (WORKFLOW, 'native-ui', """  native-ui:
    # The reusable jobs retain the verified merge scope and exact source.
    name: native-ui
    needs: impact
    if: needs.impact.outputs.scope == 'native-ui' && github.event.repository.private == false
    uses: ./.github/workflows/native-ui-checks.yml
    with:
      mode: scope

"""),
        ('.github/workflows/native-poc.yml', 'ci-lane-smoke', """  ci-lane-smoke:
    name: ci-lane-smoke
    if: github.event_name == 'workflow_dispatch' && inputs.ci_lane_smoke && github.event.repository.private == false
    uses: ./.github/workflows/native-ui-checks.yml
    with:
      mode: smoke

"""),
    )
    for path, name, expected in callers:
        workflow = sources[path].decode('utf-8')
        # The PoC trigger has scalar path-filter lists outside this mapping.
        headers = list(re.finditer(r'^jobs:[ \t]*\n', workflow, re.M))
        require(len(headers) == 1, 'native UI workflow needs one jobs mapping')
        workflow = workflow[headers[0].end():]
        jobs = [(offset, key) for offset, indent, key, _ in workflow_metadata(workflow) if indent == 2]
        starts = [offset for offset, key in jobs if key == name]
        require(len(starts) == 1, 'missing or duplicate native UI caller')
        start = starts[0]
        end = next((offset for offset, _ in jobs if offset > start), len(workflow))
        require(workflow[start:end] == expected, 'native UI caller qualification differs')
    expected = {
        'NATIVE_UI_TARGETS': {target: 'native-ui / companion (' + target + ')' for target in TARGETS},
        'NATIVE_UI_SCOPE_STEPS': ('Validate native UI source and required scope',
            'Check companion toolchain and prepare verified engine',
            'Run complete companion race suite with protected IPC evidence',
            'Vet MyGo lifetimes and check companion formatting',
            'Compile companion and check deterministic kit packaging'),
        'NATIVE_UI_PRODUCT_STEPS': ('Prepare locked frontend package inventory for native UI',
            'Build product package for native UI', 'Smoke product package for native UI'),
    }
    tree = ast.parse(sources['.github/scripts/ci-coverage.py'].decode('utf-8'))
    values = {}
    for node in tree.body:
        if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name):
            name = node.targets[0].id
            if name in expected:
                require(name not in values, 'duplicate native UI aggregate policy')
                values[name] = ast.literal_eval(node.value)
    bindings = [node.id for node in ast.walk(tree) if isinstance(node, ast.Name)
                and isinstance(node.ctx, ast.Store) and node.id in expected]
    require(values == expected and len(bindings) == len(expected),
            'native UI required job or step identity differs')


def validate_font_workflow(sources):
    """Require the reviewed installer and successful, non-optional font setup."""
    for path, expected in CI_FONT_POLICY_SHA256.items():
        require(digest(sources[path]) == expected, 'Japanese font source policy differs')
    workflow = sources[WORKFLOW].decode('utf-8')
    jobs = [(pos, key) for pos, indent, key, _ in workflow_metadata(workflow) if indent == 2]
    marker = '      - name: Prepare verified Japanese fonts\n'
    for name, conditional, consumer in (
            ('documents', "        if: steps.document-selection.outputs.render_required == 'true'\n",
             '          - name: Install isolated locked document rendering dependencies\n'),
            ('browser', '', '      - name: Install official Chromium and fonts\n')):
        positions = [pos for pos, key in jobs if key == name]
        require(len(positions) == 1, 'missing Japanese font consumer job')
        start = positions[0]
        end = next((pos for pos, _ in jobs if pos > start), len(workflow))
        job = workflow[start:end]
        expected = marker + conditional + '        timeout-minutes: 5\n        uses: ./.github/actions/japanese-fonts\n'
        require(job.count(marker) == job.count(consumer) == job.count(expected) == 1
                and job.index(expected) < job.index(consumer),
                'Japanese font setup or failure policy differs')
        if name == 'browser':
            require(job.count(expected + consumer) == 1, 'Japanese browser font preparation order differs')
        else:
            # The complete document job hash additionally binds the parallel
            # system/npm dependency group and its join before rendering.
            validate_preparation_overlap_workflow(sources[WORKFLOW])


def required_jobs(sources, *, include_skipped=False):
    """Read literal required-step constants from the protected source; no exec."""
    validate_font_workflow(sources)
    validate_native_ui_workflow(sources)
    validate_browser_cache_workflow(sources)
    validate_native_matrix_workflow(sources[WORKFLOW])
    validate_regression_workflow(sources)
    validate_frontend_workflow(sources)
    validate_native_packages_workflow(sources)
    validate_native_companion_workflow(sources[WORKFLOW])
    validate_native_companion_workflow(sources[".github/workflows/prerelease.yml"])
    validate_outbox_workflow(sources[WORKFLOW])
    validate_durable_text_workflow(sources[WORKFLOW])
    validate_parallel_workflow(sources[WORKFLOW])
    validate_linux_nearby_workflow(sources[WORKFLOW])
    validate_macos_nearby_workflow(sources[WORKFLOW])
    validate_setup_and_text_browser_workflow(sources[WORKFLOW])
    validate_request_setup_workflow(sources[WORKFLOW])
    validate_pairing_ui_workflow(sources[WORKFLOW])
    validate_request_setup_workflow(sources['.github/workflows/prerelease.yml'])
    validate_resource_inspection_workflow(sources[WORKFLOW])
    validate_fixture_diagnostics_workflow(sources[WORKFLOW])
    validate_preparation_overlap_workflow(sources[WORKFLOW])
    tree = ast.parse(sources['.github/scripts/ci-coverage.py'].decode('utf-8'))
    names = {'NATIVE_SHARED_STEPS', 'NATIVE_UNIT_STEPS', 'NATIVE_SHARED_CONTROL_STEPS', 'WINDOWS_BARRIER_STEPS',
             'REGRESSION_STEPS', 'FAST_STEPS', 'LONG_STEPS', 'FULL_ONLY_STEPS', 'BROWSER_STEPS',
             'LINUX_NEARBY_TARGETS', 'LINUX_NEARBY_STEPS', 'MACOS_NEARBY_TARGETS', 'MACOS_NEARBY_STEPS', 'NATIVE_PACKAGE_TARGETS', 'NATIVE_PACKAGE_STEPS', 'NATIVE_UI_STEPS'}
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
    require(values['REGRESSION_STEPS'] == ('Run shared CI policy and source regressions',),
            'unexpected shared regression policy')
    require(values['FAST_STEPS'].count('Reproduce locked frontend assets') == 1
            and values['BROWSER_STEPS'].count('Test and reproduce locked frontend assets') == 1,
            'native reproduction and common frontend tests are required')
    require(len(values['LONG_STEPS']) == 3, 'unexpected real-time gate policy')
    require(values['BROWSER_STEPS'].count('Prepare verified Japanese fonts') == 1,
            'missing Japanese font required gate')
    require(values['BROWSER_STEPS'].count('Verify privacy-safe fixture diagnostics') == 1,
            'missing fixture diagnostic required gate')
    require(all(values['BROWSER_STEPS'].count(name) == 1 for name in (
        'Prepare verified browser engine', 'Wait for verified browser engine',
        'Install official Chromium and fonts', 'Wait for browser runtime preparation')),
        'missing browser preparation required gate')
    require(values['FAST_STEPS'].count('Verify durable outbox restart across Core processes') == 1
            and values['BROWSER_STEPS'].count('Verify durable outbox recovery with separate agents and actual browser') == 1,
            'missing durable outbox required gates')
    require(values['FAST_STEPS'].count('Verify durable text restart and lost ACK across Core processes') == 1,
            'missing durable text required gate')
    require(all(values['FAST_STEPS'].count(name) == 1 for name, _, _ in REQUEST_SETUP_CASES),
            'missing request setup required gates')
    require(values['BROWSER_STEPS'].count('Verify synthetic request pairing UI without skips') == 1, 'missing synthetic pairing UI gate')
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
    require(values['MACOS_NEARBY_TARGETS'] == ('darwin-arm64',)
            and values['MACOS_NEARBY_STEPS'] == ('Verify macOS five-scope discovery without skips', 'Preserve required macOS discovery proof'),
            'unexpected qualified macOS nearby policy')
    require(values['NATIVE_SHARED_STEPS'] == ('Reproduce locked frontend assets', 'Check exact toolchain and native target')
            and values['NATIVE_UNIT_STEPS'] == ('Local and mock tests, race detector, and vet',
                                               'Verify adapted engine admission and native underlay denial')
            and values['NATIVE_SHARED_CONTROL_STEPS'] == ('Wait for Core context checks',
                                                         'Wait for direct LAN application checks',
                                                         'Wait for real-time lifecycle and lease checks')
            and values['WINDOWS_BARRIER_STEPS'] == ('Verify Windows receive-retirement directory barriers',),
            'native suite partition differs from reviewed policy')
    require(values['FAST_STEPS'].count('Verify explicit dispatch with three separate Core processes') == 1,
            'missing explicit dispatch required gate')
    require(values['NATIVE_UI_STEPS'] == ('Test experimental native companion',), 'unexpected companion gate policy')
    require(values['NATIVE_PACKAGE_TARGETS'] == ('windows-amd64', 'darwin-arm64')
            and values['NATIVE_PACKAGE_STEPS'] == ('Collect required whole-Core race evidence',
                'Verify remaining native package race coverage', 'Preserve required native package proof'),
            'unexpected native package coverage policy')
    inventory = (values['FAST_STEPS'] + values['LONG_STEPS'] + values['FULL_ONLY_STEPS']
                 + values['LINUX_NEARBY_STEPS'] + values['MACOS_NEARBY_STEPS']
                 + values['WINDOWS_BARRIER_STEPS'] + values['NATIVE_PACKAGE_STEPS'] + values['NATIVE_UI_STEPS'])
    require(len(set(inventory)) == len(inventory), 'overlapping required native gates')
    result, skipped = {}, {}
    for suite, targets in (('unit', TARGETS), ('integration', INTEGRATION_TARGETS)):
        for target, (name, _) in targets.items():
            selected = set(values['NATIVE_SHARED_STEPS'] + values['NATIVE_SHARED_CONTROL_STEPS'])
            if suite == 'unit':
                selected.update(values['NATIVE_UNIT_STEPS'])
                if target in values['LINUX_NEARBY_TARGETS']:
                    selected.update(values['LINUX_NEARBY_STEPS'])
                if target in values['MACOS_NEARBY_TARGETS']:
                    selected.update(values['MACOS_NEARBY_STEPS'])
                if target == 'windows-amd64':
                    selected.update(values['WINDOWS_BARRIER_STEPS'])
                if target in values['NATIVE_PACKAGE_TARGETS']:
                    selected.update(values['NATIVE_PACKAGE_STEPS'])
            else:
                selected.update(set(values['FAST_STEPS']) - set(values['NATIVE_UNIT_STEPS']))
                selected.update(values['LONG_STEPS'] + values['FULL_ONLY_STEPS'])
                if target in values['NATIVE_PACKAGE_TARGETS']:
                    selected.update(values['NATIVE_UI_STEPS'])
            result[name] = tuple(step for step in inventory if step in selected)
            skipped[name] = tuple(step for step in inventory if step not in selected)
    result.update({
        'impact': ('Select minimum CI scope',),
        'documents': ('Verify document selection and static content', 'Prepare verified Japanese fonts',
                      'Require document checks and selected rendering'),
        'policy-regressions': values['REGRESSION_STEPS'],
        'browser': values['BROWSER_STEPS'],
        'manifest-smoke': ('Exercise signing and verification offline with a disposable test key',),
        'ci-required': ('Require every check selected for this change',),
        'web-activation': ('Prepare verified activation engine and Go dependencies',
                           'Prepare locked dependencies and official Chromium', 'Wait for activation Go preparation',
                           'Verify fixed Web acceptance policy',
                           'Compile and run seven private synthetic-owner cases'),
        'product-activation': ('Prepare verified activation engine and Go dependencies',
                               'Prepare locked dependencies and official Chromium', 'Wait for activation Go preparation',
                               'Verify fixed product acceptance policy',
                               'Build normal assets and run two private production-entry cases'),
    })
    return (result, skipped) if include_skipped else result


def audit_jobs(api, run, required, *, skipped=None):
    jobs = pages(api, f"actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs", 'jobs')
    index, identifiers = {}, set()
    native_names = {name for name, _ in (*TARGETS.values(), *INTEGRATION_TARGETS.values())}
    for job in jobs:
        require(type(job.get('id')) is int and job['id'] > 0 and job['id'] not in identifiers,
                'invalid or duplicate effective CI job ID')
        identifiers.add(job['id'])
        require(type(job.get('run_id')) is int and type(job.get('run_attempt')) is int
                and job.get('run_id') == run['id'] and job.get('run_attempt') == run['run_attempt']
                and job.get('head_sha') == run['head_sha'] and isinstance(job.get('name'), str)
                and job['name'] not in index, 'invalid effective CI job identity')
        require(not job['name'].startswith(('native (', 'native-integration (')) or job['name'] in native_names,
                'unknown native suite or target')
        index[job['name']] = job
    skipped = skipped or {}
    proof = []
    for name, steps in sorted(required.items()):
        job = index.get(name, {})
        require(job.get('status') == 'completed' and job.get('conclusion') == 'success',
                'required full-validation job did not pass: ' + name)
        for expected, label in (*TARGETS.values(), *INTEGRATION_TARGETS.values()):
            if name == expected:
                require(label in job.get('labels', []), 'native CI runner target mismatch')
        selected = []
        for step in steps:
            matches = [s for s in job.get('steps', []) if s.get('name') == step]
            require(len(matches) == 1 and matches[0].get('status') == 'completed'
                    and matches[0].get('conclusion') == 'success', 'required full-validation step did not pass: ' + step)
            selected.append(step)
        for step in skipped.get(name, ()):
            matches = [s for s in job.get('steps', []) if s.get('name') == step]
            require(len(matches) == 1 and matches[0].get('status') == 'completed'
                    and matches[0].get('conclusion') == 'skipped', 'wrong native suite ownership or platform gate: ' + step)
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


def audit(api, commit, latest=None, local_root=None, previous_proof=None, artifact_download=None, job_log=None):
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
                                        'full_validation', 'jobs', 'required_jobs_sha256', 'macos_discovery_proof', 'native_package_proof'}
                and type(previous_proof.get('schema')) is int and previous_proof['schema'] == 1 and previous_proof.get('repository') == REPOSITORY
                and previous_proof.get('head_sha') == commit and previous_proof.get('head_tree') == tree
                and previous_proof.get('source_sha256') == hashes, 'stored full-CI proof source mismatch')
    required, skipped = required_jobs(sources, include_skipped=True)
    jobs = audit_jobs(api, run, required, skipped=skipped)
    path = Path(__file__).with_name('ci-dnssd-required.py')
    spec = importlib.util.spec_from_file_location('ci_dnssd_required', path)
    dnssd = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), dnssd.__dict__)
    require(tuple(dnssd.POLICY_FILES) == DNSSD_SOURCES, 'DNS-SD policy source inventory differs')
    macos_proof = dnssd.audit_artifact(api, run, commit, tree, download=artifact_download, job_log=job_log,
        policy_hashes={name: hashes[name] for name in DNSSD_SOURCES})
    path = Path(__file__).with_name('ci-native-core-required.py')
    spec = importlib.util.spec_from_file_location('ci_native_core_required', path)
    core = importlib.util.module_from_spec(spec)
    exec(compile(path.read_bytes(), str(path), 'exec'), core.__dict__)
    require(tuple(core.POLICY_FILES) == CORE_SOURCES, 'native Core policy source inventory differs')
    package_proof = {platform: core.audit_artifact(api, run, commit, tree, platform, download=artifact_download, job_log=job_log,
        policy_hashes={name: hashes[name] for name in CORE_SOURCES}) for platform in core.TARGETS}
    current = select_candidate(api, commit)
    require(fingerprint(current) == fingerprint(run), 'full-CI selection changed during audit')
    require(fingerprint(api(f"actions/runs/{run['id']}")) == fingerprint(run), 'full-CI attempt changed during audit')
    require(api('git/ref/heads/main').get('object', {}).get('sha') == commit, 'main moved during full-CI audit')
    proof = {'schema': 1, 'repository': REPOSITORY, 'head_sha': commit, 'head_tree': tree,
             'source_sha256': hashes, 'full_validation': fingerprint(run), 'jobs': jobs,
             'required_jobs_sha256': digest(encoded(required)), 'macos_discovery_proof': macos_proof, 'native_package_proof': package_proof}
    if previous_proof is not None:
        proof['gate_proof_sha256'] = digest(encoded(previous_proof))
    return proof
