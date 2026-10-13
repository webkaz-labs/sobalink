"""Synthetic full-CI receipt/ZIP fixtures; no native discovery or GitHub access."""
import copy
from contextlib import ExitStack
import hashlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch, Mock
import zipfile

from test_ci_dnssd_diagnostic import record, events, raw
from test_ci_dnssd_terminal import native_observation
import importlib.util

SCRIPT = Path(__file__).with_name('ci-dnssd-required.py')
SPEC = importlib.util.spec_from_file_location('ci_dnssd_required', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), gate.__dict__)
SHA, TREE, HASH = 'a' * 40, 'b' * 40, 'c' * 64


def identity(attempt=1):
    return {'repository': gate.REPOSITORY, 'workflow': gate.WORKFLOW, 'job': gate.JOB, 'runId': 10,
            'runAttempt': attempt, 'event': 'workflow_dispatch', 'eventHead': SHA, 'commit': SHA, 'tree': TREE}


def policy_hashes():
    return {name: hashlib.sha256((gate.ROOT / name).read_bytes()).hexdigest() for name in gate.POLICY_FILES}


def fixture(attempt=1):
    report = gate.diag.empty_result()
    records = {scope: record(scope) for scope in gate.diag.CASES.values()}
    stdout = b''
    for name, scope in gate.diag.CASES.items():
        marker = gate.diag.LEGACY_MARKER if scope == gate.diag.LEGACY_SCOPE else gate.diag.MARKER
        stdout += (f'=== RUN   {name}\n' + marker + json.dumps(records[scope]) + f'\n--- PASS: {name} (0.01s)\n').encode()
    stdout += b'PASS\n'
    collector = gate.diag.collector_observation(stdout, b'', 'completed', 0, raw(events(records)), b'')
    collector['acceptanceStatus'] = 'accepted'
    terminal = gate.module('ci-dnssd-terminal').empty_receipt()
    terminal.update(status='observed', reason='observed', nativeOriginObservation=native_observation(),
        nativeOriginVerified=True, availabilityVerified=True, openCompleted=True, markerVerified=True,
        markerOwnerJoined=True, terminalAncestorObserved=True, nativeStartObserved=True, nativeOwnerJoined=True,
        supervisorTerminationObserved=True, nativeProcessStatus='completed', nativeExitCode=0)
    report.update(diagnosticPassed=True, cCompilePassed=True, cLinkPassed=True, goCompilePassed=True,
        processStatus='completed', eventsAccepted=True, terminalLaunch=terminal, collector=collector,
        capabilities=records, selectedInterfaceVerified=True, rawReceiveVerified=True,
        legacySerializationVerified=True, legacyReceivePath='selected_interface')
    compiler = gate.diag.compiler_identity(b'Apple clang version 17.0.0 (clang-1700.3.19.1)', '26.0', '25A354')
    provenance = {'schema': 1, 'stage': 'passed', 'commit': SHA, 'tree': TREE, 'ciIdentity': identity(attempt),
        'sourceFiles': 100, 'sourceSha256': HASH, 'sourceRechecked': True, 'toolchainRechecked': True,
        'privateCleanupVerified': True,
        'goObserved': {'GOVERSION': 'go1.27.1', 'GOHOSTOS': 'darwin', 'GOHOSTARCH': 'arm64', 'GOOS': 'darwin', 'GOARCH': 'arm64', 'CGO_ENABLED': '1'},
        'runnerObserved': {'os': 'macOS', 'arch': 'ARM64', 'pythonArch': 'arm64'},
        'toolchain': {'go': 'go1.27.1', 'goos': 'darwin', 'goarch': 'arm64', 'runner': 'macos-26', 'race': True},
        'platform': 'darwin-arm64', 'compilerObserved': {key: compiler[key] for key in ('vendor', 'version', 'build')},
        'sdkVersionObserved': '26.0', 'sdkBuildObserved': '25A354', 'compiler': compiler,
        'sdkFiles': {key: 'available' for key in gate.diag.SDK_FILES},
        'toolchainSha256': {'go': HASH, 'clang': HASH, 'sdk': {key: HASH for key in gate.diag.SDK_FILES}},
        'tests': gate.diag.CASES, 'policySha256': policy_hashes(), 'modules': list(gate.module('ci-nearby-native').MODULES),
        'cObjectSha256': HASH, 'cLinkedSha256': HASH, 'linkage': 'implicit-system-umbrella',
        'cLinkLibraries': ['libSystem.B.dylib'], 'testSha256': HASH, 'launchMethod': 'ordinary_terminal_command',
        'originVerification': 'exact-four-hop-kernel-path-with-final-recheck', 'testDriverDenialFence': True}
    return {'schema': 1, 'requiredPassed': True, 'result': report, 'provenance': provenance}


def archive(value, *, names=('receipt.json',)):
    output = io.BytesIO()
    with zipfile.ZipFile(output, 'w', compression=zipfile.ZIP_DEFLATED) as target:
        for name in names:
            target.writestr(name, json.dumps(value))
    return output.getvalue()


def artifact(value=None, *, attempt=1):
    data = archive(value if value is not None else fixture(attempt))
    return {'id': 300, 'name': f'{gate.ARTIFACT}-10-{attempt}', 'size_in_bytes': len(data),
        'digest': 'sha256:' + hashlib.sha256(data).hexdigest(), 'expired': False,
        'created_at': '2026-01-01T00:00:40Z', 'updated_at': '2026-01-01T00:00:41Z',
        'workflow_run': {'id': 10, 'head_sha': SHA, 'head_branch': 'main'}}, data


def job(attempt=1, identifier=100):
    return {'id': identifier, 'name': gate.JOB, 'run_id': 10, 'run_attempt': attempt,
        'head_sha': SHA, 'labels': ['macos-26'], 'status': 'completed', 'conclusion': 'success',
        'started_at': '2026-01-01T00:00:01Z', 'completed_at': '2026-01-01T00:01:00Z',
        'steps': [{'name': name, 'status': 'completed', 'conclusion': 'success',
            'started_at': '2026-01-01T00:00:30Z' if name == gate.STEPS[1] else '2026-01-01T00:00:05Z',
            'completed_at': '2026-01-01T00:00:50Z' if name == gate.STEPS[1] else '2026-01-01T00:00:29Z'} for name in gate.STEPS]}


def run(attempt=1):
    return {'id': 10, 'run_attempt': attempt, 'head_sha': SHA, 'head_branch': 'main',
        'event': 'workflow_dispatch', 'name': 'Cross-platform CI', 'path': gate.WORKFLOW,
        'status': 'completed', 'conclusion': 'success', 'created_at': '2026-01-01T00:00:00Z',
        'run_started_at': f'2026-01-01T00:0{attempt-1}:00Z'}


class RequiredProofTests(unittest.TestCase):
    def test_complete_receipt_and_fixed_closed_shapes(self):
        value = fixture()
        self.assertEqual(gate.validate_receipt(value, identity(), policy_hashes()), value)
        for target in ('result', 'provenance'):
            for key in value[target]:
                changed = copy.deepcopy(value)
                del changed[target][key]
                with self.subTest(target=target, missing=key), self.assertRaises((ValueError, KeyError)):
                    gate.validate_receipt(changed, identity(), policy_hashes())
        for mutate in (
            lambda v: v.update(private='synthetic-private'),
            lambda v: v['result']['collector'].update(private='synthetic-private'),
            lambda v: v['provenance']['ciIdentity'].update(runAttempt=True),
            lambda v: v['result']['collector']['testLifecycleCounts'][next(iter(gate.diag.CASES))].update(run=True),
            lambda v: v['provenance'].update(sourceFiles=True),
            lambda v: v['provenance']['toolchain'].update(race=1),
        ):
            changed = copy.deepcopy(value); mutate(changed)
            with self.assertRaises(ValueError): gate.validate_receipt(changed, identity(), policy_hashes())

    def test_unavailable_skip_and_each_incomplete_join_fail_required_admission(self):
        for scope in gate.diag.CASES.values():
            value = fixture()
            value['result']['capabilities'][scope] = record(scope, 'unavailable')
            with self.subTest(scope=scope), self.assertRaises(ValueError):
                gate.validate_receipt(value, identity(), policy_hashes())
        paths = [('provenance', key) for key in ('sourceRechecked', 'toolchainRechecked', 'privateCleanupVerified', 'testDriverDenialFence')]
        paths += [('result', 'terminalLaunch', key) for key in ('nativeOwnerJoined', 'markerOwnerJoined', 'nativeOriginVerified', 'supervisorTerminationObserved')]
        paths += [('result', 'terminalLaunch', 'nativeOriginObservation', key) for key in ('workerJoined', 'queriesJoined', 'chainRechecked', 'supervisorExecutableMatched', 'interpreterReferenceVerified')]
        for path in paths:
            value = fixture(); current = value
            for part in path[:-1]: current = current[part]
            current[path[-1]] = False
            with self.subTest(path=path), self.assertRaises(ValueError):
                gate.validate_receipt(value, identity(), policy_hashes())

    def test_native_converter_flags_policy_and_limits_are_not_replaceable(self):
        changes = [(('result', 'terminalLaunch', 'nativeExitCode'), 7),
            (('result', 'collector', 'converterExitCode'), 7), (('result', 'collector', 'acceptanceStatus'), 'unaccepted'),
            (('result', 'collector', 'packageLifecycleCounts', 'skip'), 1),
            (('result', 'collector', 'completeMarkerLineCounts', next(iter(gate.diag.CASES))), 2),
            (('result', 'physicalLANVerified'), True), (('result', 'networkMulticastVerified'), True),
            (('result', 'automaticCoreBrowserVerified'), True), (('result', 'terminalLaunch', 'responsibleIdentityVerified'), True),
            (('result', 'terminalLaunch', 'terminalAppCleanupVerified'), True),
            (('provenance', 'policySha256', '.github/scripts/ci-dnssd-terminal.py'), 'd' * 64),
            (('provenance', 'toolchainSha256', 'sdk', 'dns_sd.h'), 'missing'),
            (('provenance', 'ciIdentity', 'tree'), 'd' * 40)]
        for path, replacement in changes:
            value = fixture(); current = value
            for part in path[:-1]: current = current[part]
            current[path[-1]] = replacement
            with self.subTest(path=path), self.assertRaises(ValueError): gate.validate_receipt(value, identity(), policy_hashes())

    def test_archive_exact_members_hash_size_crc_and_duplicate_json(self):
        value = fixture(); meta, data = artifact(value)
        self.assertEqual(gate.artifact_receipt(data, meta)[0], value)
        for changed in (dict(meta, size_in_bytes=True), dict(meta, size_in_bytes=len(data)+1), dict(meta, digest='sha256:'+'d'*64)):
            with self.assertRaises(ValueError): gate.artifact_receipt(data, changed)
        for names in (('result.json', 'provenance.json'), ('../receipt.json',), ('receipt.json', 'private.txt'), ('receipt.json', 'receipt.json')):
            bad = archive(value, names=names)
            changed = dict(meta, size_in_bytes=len(bad), digest='sha256:'+hashlib.sha256(bad).hexdigest())
            with self.assertRaises(ValueError): gate.artifact_receipt(bad, changed)
        output=io.BytesIO()
        with zipfile.ZipFile(output,'w') as target: target.writestr('receipt.json', '{"schema":1,"schema":1}')
        bad=output.getvalue()
        with self.assertRaises(ValueError): gate.artifact_receipt(bad,dict(meta,size_in_bytes=len(bad),digest='sha256:'+hashlib.sha256(bad).hexdigest()))

    def test_fresh_failure_receipt_is_written_and_not_success(self):
        value=fixture(); value['result']['capabilities']['os_local_only']=record('os_local_only','unavailable')
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            with self.assertRaises(ValueError): gate.write_receipt(root,value['result'],value['provenance'])
            self.assertIs(json.loads((root/'receipt.json').read_text())['requiredPassed'],False)

    def test_full_ci_event_binds_pull_merge_and_rejects_diagnostic_create(self):
        event={'repository':{'full_name':gate.REPOSITORY,'private':False,'default_branch':'main'}, 'number':7,
            'pull_request':{'merge_commit_sha':SHA,'head':{'sha':'d'*40},'base':{'ref':'main','sha':'e'*40,'repo':{'full_name':gate.REPOSITORY}}}}
        env={'GITHUB_REPOSITORY':gate.REPOSITORY,'GITHUB_WORKFLOW':'Cross-platform CI','GITHUB_JOB':'native',
            'GITHUB_SHA':SHA,'GITHUB_EVENT_NAME':'pull_request','GITHUB_REF':'refs/pull/7/merge',
            'GITHUB_WORKFLOW_REF':gate.REPOSITORY+'/'+gate.WORKFLOW+'@refs/pull/7/merge','GITHUB_RUN_ID':'10','GITHUB_RUN_ATTEMPT':'1'}
        native=Mock(); native.checked.side_effect=lambda command: ((SHA+'\n'+TREE+'\n') if command[1]=='rev-parse' else ' '.join([SHA,'e'*40,'d'*40])).encode()
        with patch.dict(os.environ,env,clear=True), patch.object(Path,'cwd',return_value=gate.ROOT), patch.object(gate,'module',return_value=native):
            self.assertEqual(gate.ci_identity(event,SHA,TREE)['eventHead'],'d'*40)
            for key,replacement in (('GITHUB_EVENT_NAME','create'),('GITHUB_SHA','d'*40),('GITHUB_RUN_ATTEMPT','0'),('GITHUB_WORKFLOW','Activation diagnostic'),('GITHUB_JOB','diagnostic-dnssd')):
                with patch.dict(os.environ,{key:replacement}),self.assertRaises(ValueError):gate.ci_identity(event,SHA,TREE)

    def test_required_entry_compiles_once_and_launches_once_then_writes_bound_receipt(self):
        expected = fixture()
        records = expected['result']['capabilities']
        stdout = b''
        for name, scope in gate.diag.CASES.items():
            marker = gate.diag.LEGACY_MARKER if scope == gate.diag.LEGACY_SCOPE else gate.diag.MARKER
            stdout += (f'=== RUN   {name}\n' + marker + json.dumps(records[scope]) + f'\n--- PASS: {name} (0.01s)\n').encode()
        stdout += b'PASS\n'
        for unavailable in (False, True):
            with tempfile.TemporaryDirectory() as temporary, ExitStack() as stack:
                root = Path(temporary)
                clang, go, sdk = root/'clang', root/'go', root/'sdk'
                clang.write_bytes(b'synthetic clang'); go.write_bytes(b'synthetic go')
                for relative in gate.diag.SDK_FILES.values():
                    path = sdk/relative; path.parent.mkdir(parents=True, exist_ok=True); path.write_bytes(b'synthetic sdk')
                provider = root/'internal/nearby/provider_darwin.go'; provider.parent.mkdir(parents=True); provider.write_text('package nearby\n')
                event = root/'event.json'; event.write_text('{}')
                output = root/'output'
                native, terminal = gate.module('ci-nearby-native'), gate.module('ci-dnssd-terminal')
                original_module = gate.diag.module
                commands = []
                def capture(command, **kwargs):
                    commands.append(command)
                    data = b''
                    if command[:2] == ['git', 'rev-parse']:
                        data = (TREE if command[-1] == 'HEAD^{tree}' else SHA).encode()
                    elif command[:2] == ['go', 'env']: data = json.dumps(expected['provenance']['goObserved']).encode()
                    elif command[-1] == '--version': data = b'Apple clang version 17.0.0 (clang-1700.3.19.1)'
                    elif command[-1] == 'clang': data = str(clang).encode()
                    elif command[-1] == '--show-sdk-path': data = str(sdk).encode()
                    elif command[-1] == '--show-sdk-version': data = b'26.0'
                    elif command[-1] == '--show-sdk-build-version': data = b'25A354'
                    elif command[0] == 'otool': data = b'synthetic:\n /usr/lib/libSystem.B.dylib (compatibility version 1.0, current version 1.0)\n'
                    elif command[:3] == ['go', 'tool', 'test2json']:
                        selected = dict(records)
                        if unavailable: selected['os_local_only'] = record('os_local_only', 'unavailable')
                        data = raw(events(selected))
                    elif '-o' in command: Path(command[command.index('-o')+1]).write_bytes(b'synthetic compiled object')
                    else: raise AssertionError(command)
                    return 'completed', 0, data, b''
                stack.enter_context(patch.dict(os.environ, GITHUB_ACTIONS='true', RUNNER_TEMP=temporary,
                    GITHUB_EVENT_PATH=str(event), GITHUB_OUTPUT=str(output), RUNNER_OS='macOS', RUNNER_ARCH='ARM64'))
                stack.enter_context(patch.object(gate.diag.sys,'platform','darwin'))
                stack.enter_context(patch.object(gate.diag,'ROOT',root))
                stack.enter_context(patch.object(gate.diag,'module',side_effect=lambda name:native if name=='ci-nearby-native' else terminal if name=='ci-dnssd-terminal' else original_module(name)))
                stack.enter_context(patch.object(native,'capture',side_effect=capture))
                stack.enter_context(patch.object(native,'checked',side_effect=lambda command,**kwargs:capture(command,**kwargs)[2]))
                stack.enter_context(patch.object(native,'identities',return_value=expected['provenance']['toolchain']))
                stack.enter_context(patch.object(native.platform,'machine',return_value='arm64'))
                launch = stack.enter_context(patch.object(terminal,'capture',return_value=('completed',0,stdout,b'',expected['result']['terminalLaunch'])))
                inventory = stack.enter_context(patch.object(gate,'source_inventory',return_value=policy_hashes()))
                stack.enter_context(patch.object(gate,'ci_identity',return_value=identity()))
                stack.enter_context(patch.object(gate.diag.shutil,'which',return_value=str(go)))
                printed = stack.enter_context(patch('sys.stdout',new_callable=io.StringIO))
                self.assertEqual(gate.diag.main([],required=gate),1 if unavailable else 0)
                value=json.loads((root/'soba-dnssd-required/receipt.json').read_text())
                self.assertEqual(value['requiredPassed'],not unavailable)
                self.assertEqual(launch.call_count,1)
                self.assertEqual(sum(command[:3]==['go','test','-race'] for command in commands),1)
                self.assertFalse(any(command[:3]==['go','mod','download'] for command in commands))
                self.assertEqual(inventory.call_count,2)
                self.assertNotIn(temporary,printed.getvalue())
                if not unavailable:gate.validate_receipt(value,identity(),policy_hashes())

    def test_prepared_inventory_rejects_ignored_sources_and_symlink_roots(self):
        native=gate.module('ci-nearby-native'); shared=gate.module('ci-nearby-diagnostic')
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary); (root/'tracked.go').write_text('package synthetic\n')
            raw=b'.sobalink-deps/tailscale/file.go\0web/node_modules/package/file.js\0web/tsconfig.tsbuildinfo\0.github/scripts/__pycache__/ci-test.cpython-312.pyc\0'
            def checked(command):
                if '--porcelain' in command: return b''
                return raw if '--others' in command else b'tracked.go\0'
            with patch.object(gate,'ROOT',root),patch.object(gate,'module',side_effect=lambda name:native if name=='ci-nearby-native' else shared),patch.object(native,'checked',side_effect=checked):
                self.assertEqual(set(gate.source_inventory()),{'tracked.go'})
                for name in (b'internal/nearby/ignored.go\0',b'web/private.py\0',b'.github/scripts/__pycache__/evil.py\0',b'web/node_modules/../injected.go\0'):
                    raw=name
                    with self.assertRaises(ValueError):gate.source_inventory()
                raw=b''
                original = Path.is_symlink
                with patch.object(Path, 'is_symlink', autospec=True, side_effect=lambda path:path == root/'web/node_modules' or original(path)):
                    with self.assertRaises(ValueError):gate.source_inventory()


class ArtifactAdmissionTests(unittest.TestCase):
    def setUp(self):
        self.run=run(); self.original=job(); self.current=job(); self.meta,self.data=artifact(); self.artifacts=[self.meta]
        self.old_run=run()

    def api(self,path):
        if path.endswith('/artifacts?per_page=100&page=1'):return {'total_count':len(self.artifacts),'artifacts':copy.deepcopy(self.artifacts)}
        if path=='actions/artifacts/300':return copy.deepcopy(self.meta)
        if path.endswith('/jobs?per_page=100&page=1'):
            attempt=int(path.split('/attempts/')[1].split('/')[0]); selected=self.current if attempt==self.run['run_attempt'] else self.original
            return {'total_count':1,'jobs':[copy.deepcopy(selected)]}
        if path=='actions/runs/10/attempts/1':return copy.deepcopy(self.old_run)
        raise AssertionError(path)

    def audit(self):return gate.audit_artifact(self.api,self.run,SHA,TREE,download=lambda _:self.data)

    def carry(self):
        self.run=run(2); self.current=job(2,101); self.old_run['conclusion']='failure'

    def test_exact_current_and_successful_effective_carry_forward(self):
        self.assertEqual(self.audit()['producingAttempt'],1)
        self.carry(); proof=self.audit()
        self.assertEqual((proof['producingAttempt'],proof['effectiveAttempt'],proof['producingJobId'],proof['effectiveJobId']),(1,2,100,101))

    def test_core_failure_artifacts_do_not_collide_with_current_or_carried_discovery_success(self):
        extras=[{'name':'soba-native-core-failure-'+target+'-10-'+str(attempt)}
                for target in ('windows-amd64','darwin-arm64') for attempt in (1,2)]
        for index,row in enumerate(extras,400):row['id']=index
        for carried in (False,True):
            self.setUp()
            if carried:self.carry()
            self.artifacts.extend(extras)
            self.assertEqual(self.audit()['producingAttempt'],1)
            self.artifacts=extras
            with self.assertRaises(ValueError):self.audit()

    def test_pull_request_api_head_is_separate_from_test_merge_source(self):
        self.run.update(event='pull_request', head_sha='d'*40, head_branch='synthetic-feature')
        self.current['head_sha']='d'*40
        value=fixture()
        value['provenance']['ciIdentity'].update(event='pull_request', eventHead='d'*40)
        self.meta,self.data=artifact(value)
        self.meta['workflow_run'].update(head_sha='d'*40, head_branch='synthetic-feature')
        self.artifacts=[self.meta]
        proof=self.audit()
        self.assertEqual((proof['commit'],proof['tree']),(SHA,TREE))
        self.assertNotEqual(proof['commit'],self.run['head_sha'])

    def test_carry_requires_original_success_and_matching_actual_execution(self):
        for mutate in (
            lambda:self.original.update(conclusion='failure'), lambda:self.original.update(head_sha='d'*40),
            lambda:self.original.update(run_attempt=2), lambda:self.original.update(started_at='2026-01-01T00:00:02Z'),
            lambda:self.original['steps'][0].update(conclusion='skipped'),
            lambda:self.original['steps'][1].update(completed_at='2026-01-01T00:00:51Z'),
            lambda:self.old_run.update(head_sha='d'*40), lambda:self.old_run.update(run_started_at=self.run['run_started_at']),
            lambda:self.current.update(completed_at='2026-01-01T00:02:00Z')):
            self.setUp();self.carry();mutate()
            with self.assertRaises(ValueError):self.audit()

    def test_artifact_identity_source_attempt_and_step_window_are_exact(self):
        for mutate in (
            lambda:self.artifacts.clear(), lambda:self.artifacts.append(dict(self.meta,id=301)),
            lambda:self.meta.update(expired=True), lambda:self.meta.update(name=gate.ARTIFACT+'-10-2'),
            lambda:self.meta.update(created_at='2026-01-01T00:00:02Z'),
            lambda:self.meta['workflow_run'].update(head_sha='d'*40),
            lambda:self.meta.update(digest='sha256:'+'d'*64),
            lambda:self.current['steps'][1].update(conclusion='failure'),
            lambda:self.current['steps'][0].update(completed_at='2026-01-01T00:00:31Z'),
        ):
            self.setUp();mutate()
            with self.assertRaises(ValueError):self.audit()
        for field in ('commit','tree','eventHead','runId','runAttempt'):
            self.setUp();value=fixture();value['provenance']['ciIdentity'][field]='d'*40 if field in ('commit','tree','eventHead') else 2
            self.meta,self.data=artifact(value);self.artifacts=[self.meta]
            with self.assertRaises(ValueError):self.audit()


if __name__=='__main__':unittest.main()
