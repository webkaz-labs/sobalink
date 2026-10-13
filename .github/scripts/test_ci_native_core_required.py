"""Synthetic required Core/package/official-artifact policy fixtures; no native proof."""
import copy
from contextlib import ExitStack
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from test_ci_native_core import event, raw
from test_ci_dnssd_required import archive

SCRIPT = Path(__file__).with_name('ci-native-core-required.py')
SPEC = importlib.util.spec_from_file_location('ci_native_core_required', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
exec(compile(SCRIPT.read_bytes(), str(SCRIPT), 'exec'), gate.__dict__)
SHA, TREE, HASH = 'a' * 40, 'b' * 40, 'c' * 64
PLATFORM = 'windows-amd64'
INVENTORY = [gate.packages.MODULE + '/cmd/example', gate.packages.CORE, gate.packages.MODULE + '/internal/example']


def identity(platform=PLATFORM, attempt=1):
    return {'repository': gate.REPOSITORY, 'workflow': gate.WORKFLOW, 'job': gate.TARGETS[platform][0],
        'runId': 10, 'runAttempt': attempt, 'event': 'workflow_dispatch', 'eventHead': SHA, 'commit': SHA, 'tree': TREE}


def policy_hashes():
    return {name: hashlib.sha256((gate.ROOT / name).read_bytes()).hexdigest() for name in gate.POLICY_FILES}


def fixture(platform=PLATFORM, attempt=1, *, complete=True):
    process = {'process': 'completed', 'returncode': 0, 'ownersJoined': True, 'executionState': 'completed',
        'observedReturncode': 0, 'groupAbsenceObservedAfterWait': True if platform == 'darwin-arm64' else None, 'wallSeconds': 4.0}
    names = [gate.collector.LEASE, gate.collector.LEASE_PROOF, gate.collector.MIXED, *sorted(gate.SKIPS[platform])]
    events = [event('start')]
    for name in names:
        events += [event('run', 1, name), event('skip' if name in gate.SKIPS[platform] else 'pass', 1, name, Elapsed=1)]
    events.append(event('pass', 2, Elapsed=2))
    suite = gate.collector.timing(raw(events), names, process)
    result = gate.empty_result()
    result.update(phase='complete', sourceVerified=True, cleanupComplete=True, timingComplete=True,
        ownersState='joined', inventorySha256=gate.collector.aggregate(names),
        preparation={name: copy.deepcopy(process) for name in ('prepare', 'verify', 'modules', 'inventory')},
        runs={'suite': suite}, metrics=[{'suite': 'native-core-suite', 'elapsed_seconds': 4.0, 'returncode': 0, 'status': 'passed'}])
    goos, goarch = platform.split('-')
    primitives = ('waitid', 'getpgid', 'killpg', 'P_PID', 'WEXITED', 'WNOHANG', 'WNOWAIT', 'CLD_EXITED', 'CLD_KILLED', 'CLD_DUMPED')
    provenance = {'ciIdentity': identity(platform, attempt), 'platform': platform, 'sourceSha256': HASH,
        'sourceFiles': 100, 'policySha256': policy_hashes(),
        'toolchain': {'go': 'go1.27.1', 'goos': goos, 'goarch': goarch, 'runner': gate.TARGETS[platform][1], 'race': True},
        'goSha256': HASH, 'collectorRuntime': {'pythonVersion': [3, 14, 0], 'darwinMinimumPython': [3, 13, 0],
            'darwinNonReapingWaitRequired': goos == 'darwin', 'requiredPrimitives': {key: True for key in primitives} if goos == 'darwin' else {}, 'available': True},
        'commands': copy.deepcopy(gate.collector.COMMANDS), 'watchdogSeconds': dict(gate.collector.BOUNDS),
        'race': True, 'count': 1, 'packageTimeoutSeconds': 600,
        'ownershipMechanism': 'windows-job-object' if goos == 'windows' else 'unreaped-leader-posix-process-group'}
    remaining = {'packages': gate.packages.partition(INVENTORY)['remaining'], 'returncode': 0,
        'metric': {'suite': 'local-race-windows-remaining', 'elapsed_seconds': 1.0, 'returncode': 0, 'status': 'passed'}}
    return {'schema': 1, 'requiredPassed': complete, 'provenance': provenance, 'result': result,
        'inventory': list(INVENTORY), 'inventorySha256': gate.packages.fingerprint(INVENTORY),
        'groups': gate.packages.partition(INVENTORY), 'remaining': remaining if complete else None,
        'remainingSourceVerified': complete, 'remainingInventoryVerified': complete}


def artifact(platform=PLATFORM, attempt=1, value=None, identifier=301):
    data = archive(value or fixture(platform, attempt))
    return {'id': identifier, 'name': f'{gate.ARTIFACT}-{platform}-10-{attempt}', 'size_in_bytes': len(data),
        'digest': 'sha256:' + hashlib.sha256(data).hexdigest(), 'expired': False,
        'created_at': '2026-01-01T00:00:40Z', 'updated_at': '2026-01-01T00:00:41Z',
        'workflow_run': {'id': 10, 'head_sha': SHA, 'head_branch': 'main'}}, data


def job(platform=PLATFORM, attempt=1, identifier=100):
    return {'id': identifier, 'name': gate.TARGETS[platform][0], 'run_id': 10, 'run_attempt': attempt,
        'head_sha': SHA, 'labels': [gate.TARGETS[platform][1]], 'status': 'completed', 'conclusion': 'success',
        'started_at': '2026-01-01T00:00:01Z', 'completed_at': '2026-01-01T00:01:00Z',
        'steps': [{'name': name, 'status': 'completed', 'conclusion': 'success',
            'started_at': '2026-01-01T00:00:' + ('05Z' if i == 0 else '15Z' if i == 1 else '30Z'),
            'completed_at': '2026-01-01T00:00:' + ('10Z' if i == 0 else '20Z' if i == 1 else '50Z')}
            for i, name in enumerate(gate.STEPS)]}


def run(attempt=1):
    return {'id': 10, 'run_attempt': attempt, 'head_sha': SHA, 'head_branch': 'main', 'event': 'workflow_dispatch',
        'name': 'Cross-platform CI', 'path': gate.WORKFLOW, 'status': 'completed', 'conclusion': 'success',
        'created_at': '2026-01-01T00:00:00Z', 'run_started_at': f'2026-01-01T00:0{attempt-1}:00Z'}


class ReceiptTests(unittest.TestCase):
    def test_complete_exact_partition_and_explicit_existing_skips(self):
        for platform in gate.TARGETS:
            value = fixture(platform)
            self.assertEqual(gate.validate_receipt(value, identity(platform), policy_hashes()), value)
            self.assertFalse(value['result']['runs']['suite']['allTestsPassed'])
            early = fixture(platform, complete=False)
            gate.validate_receipt(early, identity(platform), policy_hashes(), complete=False)
            with self.assertRaises(ValueError): gate.validate_receipt(early, identity(platform), policy_hashes())

    def test_missing_and_unknown_fields_fail_closed(self):
        value = fixture()
        paths = [(), ('provenance',), ('result',), ('result', 'runs', 'suite'),
                 ('result', 'preparation', 'inventory'), ('provenance', 'ciIdentity'), ('remaining',)]
        for path in paths:
            target = value
            for part in path: target = target[part]
            for missing in [*target, None]:
                changed = copy.deepcopy(value); dest = changed
                for part in path: dest = dest[part]
                if missing is None: dest['unexpected'] = 'synthetic'
                else: del dest[missing]
                with self.subTest(path=path, missing=missing), self.assertRaises((ValueError, KeyError, TypeError)):
                    gate.validate_receipt(changed, identity(), policy_hashes())

    def test_bool_int_forgery_source_mismatch_and_changed_execution_fail(self):
        changes = [(('schema',), True), (('provenance', 'ciIdentity', 'runAttempt'), True),
            (('provenance', 'race'), 1), (('provenance', 'count'), True), (('provenance', 'packageTimeoutSeconds'), 601),
            (('provenance', 'sourceFiles'), True), (('provenance', 'toolchain', 'race'), 1),
            (('provenance', 'commands', 'suite'), ['go', 'test', '-run=Subset', './internal/core']),
            (('provenance', 'watchdogSeconds', 'suite'), 999), (('result', 'runs', 'suite', 'expectedCount'), True),
            (('result', 'runs', 'suite', 'allTestsPassed'), True), (('result', 'runs', 'suite', 'accountingComplete'), False),
            (('result', 'runs', 'suite', 'eventStreamValid'), False), (('result', 'runs', 'suite', 'packageTimeoutObserved'), True),
            (('result', 'runs', 'suite', 'passedCount'), 99), (('result', 'runs', 'suite', 'subtestFailed'), 1),
            (('result', 'runs', 'suite', 'subtestStarted'), 1),
            (('result', 'sourceVerified'), False), (('result', 'cleanupComplete'), False), (('result', 'releaseValidation'), True),
            (('remaining', 'returncode'), 1), (('remainingSourceVerified',), False), (('remainingInventoryVerified',), False)]
        for path, replacement in changes:
            value = fixture(); target = value
            for part in path[:-1]: target = target[part]
            target[path[-1]] = replacement
            with self.subTest(path=path), self.assertRaises((ValueError, KeyError, TypeError)):
                gate.validate_receipt(value, identity(), policy_hashes())

    def test_no_missing_duplicate_reordered_or_extra_package(self):
        for mutate in (lambda v: v['groups']['remaining'].append(gate.packages.CORE),
            lambda v: v['groups']['remaining'].pop(), lambda v: v['groups']['remaining'].reverse(),
            lambda v: v['remaining']['packages'].append(gate.packages.MODULE + '/new'),
            lambda v: v['inventory'].append(gate.packages.CORE)):
            value=fixture(); mutate(value)
            with self.assertRaises(ValueError): gate.validate_receipt(value, identity(), policy_hashes())

    def test_skip_cannot_replace_an_ordinary_pass(self):
        value = fixture(); suite = value['result']['runs']['suite']
        suite['tests'][0]['outcome'] = 'skip'; suite['passedCount'] -= 1; suite['skippedCount'] += 1
        with self.assertRaises(ValueError): gate.validate_receipt(value, identity(), policy_hashes())

    def test_joined_genuine_failure_can_only_admit_complement(self):
        value = fixture(complete=False); suite = value['result']['runs']['suite']
        value['result']['timingComplete'] = False
        suite.update(process='nonzero', executionState='nonzero', returncode=1, observedReturncode=1,
                     packageOutcome='fail', passedCount=suite['passedCount']-1, failedCount=1)
        suite['tests'][0]['outcome'] = 'fail'
        value['result']['metrics'][0].update(returncode=1, status='failed')
        gate.validate_receipt(value, identity(), policy_hashes(), complete=False)
        value['remaining'] = fixture()['remaining']; value.update(requiredPassed=True, remainingSourceVerified=True, remainingInventoryVerified=True)
        with self.assertRaises(ValueError): gate.validate_receipt(value, identity(), policy_hashes())
        value=fixture(complete=False); value['result']['runs']['suite']['ownersJoined'] = False
        with self.assertRaises(ValueError): gate.validate_receipt(value, identity(), policy_hashes(), complete=False)

    def test_darwin_runtime_and_owned_group_observations_are_required(self):
        for mutate in (lambda v: v['provenance']['collectorRuntime'].update(pythonVersion=[3,12,9]),
            lambda v: v['provenance']['collectorRuntime']['requiredPrimitives'].update(waitid=False),
            lambda v: v['provenance']['collectorRuntime']['requiredPrimitives'].update(WNOWAIT=1),
            lambda v: v['result']['runs']['suite'].update(groupAbsenceObservedAfterWait=False)):
            value=fixture('darwin-arm64'); mutate(value)
            with self.assertRaises(ValueError): gate.validate_receipt(value, identity('darwin-arm64'), policy_hashes())


class RunnerTests(unittest.TestCase):
    def model(self, *, failed=False, joined=True):
        stack = ExitStack()
        self.addCleanup(stack.close)
        root = Path(stack.enter_context(tempfile.TemporaryDirectory()))
        export = root / gate.ARTIFACT; export.mkdir()
        value = fixture(complete=False)
        source = policy_hashes()
        if failed:
            suite = value['result']['runs']['suite']
            value['result']['timingComplete'] = False
            suite.update(process='nonzero', executionState='nonzero', returncode=1, observedReturncode=1,
                packageOutcome='fail', passedCount=suite['passedCount']-1, failedCount=1)
            suite['tests'][0]['outcome'] = 'fail'
            value['result']['metrics'][0].update(returncode=1, status='failed')
        if not joined:
            value['result']['ownersState'] = 'unresolved'
            value['result']['runs']['suite']['ownersJoined'] = False
        def collect(env, result):
            self.assertTrue(all(Path(env[key]).parent == root for key in ('TMPDIR','TMP','TEMP')))
            result.update(copy.deepcopy(value['result']))
        env = {'RUNNER_TEMP': str(root), 'GITHUB_OUTPUT': str(root/'output'), 'SOBALINK_CI_METRICS': str(root/'metrics.jsonl')}
        for manager in (patch.dict(os.environ, env, clear=True),
            patch.object(gate,'context',return_value=identity()),
            patch.object(gate,'source_inventory',return_value=source),
            patch.object(gate,'native_identity',return_value=value['provenance']['toolchain']),
            patch.object(gate.collector,'runtime_capability',return_value=value['provenance']['collectorRuntime']),
            patch.object(gate.shutil,'which',return_value='/synthetic/go'),
            patch.object(gate.collector.native,'digest',return_value=HASH),
            patch.object(gate.packages,'query',return_value='\n'.join(INVENTORY)+'\n'),
            patch.object(gate.collector,'run_diagnostic',side_effect=collect)):
            stack.enter_context(manager)
        return root, export

    def test_one_collection_and_one_complement_then_no_reuse(self):
        root,export=self.model()
        gate.collect(PLATFORM,export,identity())
        self.assertEqual((root/'output').read_text(),'remaining_safe=true\n')
        self.assertEqual(len(gate.collector.metrics.read_records()),1)
        with patch.object(gate.packages,'run_group',return_value=fixture()['remaining']) as run_group:
            gate.remaining(PLATFORM,export,identity())
            run_group.assert_called_once_with('remaining',gate.packages.partition(INVENTORY)['remaining'])
            with self.assertRaises(ValueError):gate.remaining(PLATFORM,export,identity())
            self.assertEqual(run_group.call_count,1)
        self.assertTrue(gate.read_receipt(export/'receipt.json')['requiredPassed'])
        self.assertFalse(list(root.glob('soba-native-core-private-*')))

    def test_failed_core_stays_failed_but_joined_failure_still_executes_complement(self):
        root,export=self.model(failed=True)
        with self.assertRaises(ValueError):gate.collect(PLATFORM,export,identity())
        self.assertEqual((root/'output').read_text(),'remaining_safe=true\n')
        self.assertEqual(gate.collector.metrics.read_records()[0]['status'],'failed')
        with patch.object(gate.packages,'run_group',return_value=fixture()['remaining']) as run_group:
            with self.assertRaises(ValueError):gate.remaining(PLATFORM,export,identity())
            run_group.assert_called_once()
        self.assertFalse(gate.read_receipt(export/'receipt.json')['requiredPassed'])

    def test_unresolved_ownership_never_releases_private_state_or_authorizes_remaining(self):
        root,export=self.model(joined=False)
        with self.assertRaises(ValueError):gate.collect(PLATFORM,export,identity())
        self.assertFalse((root/'output').exists())
        self.assertTrue(list(root.glob('soba-native-core-private-*')))
        with patch.object(gate.packages,'run_group') as run_group:
            with self.assertRaises(ValueError):gate.remaining(PLATFORM,export,identity())
            run_group.assert_not_called()

    def test_source_or_inventory_change_rejects_complement_before_execution(self):
        root,export=self.model()
        gate.collect(PLATFORM,export,identity())
        for changed in (patch.object(gate,'source_inventory',return_value=dict(policy_hashes(), extra=HASH)),
                        patch.object(gate.packages,'query',return_value='\n'.join(INVENTORY+[gate.packages.MODULE+'/new'])+'\n')):
            with changed, patch.object(gate.packages,'run_group') as run_group:
                with self.assertRaises(ValueError):gate.remaining(PLATFORM,export,identity())
                run_group.assert_not_called()

    def test_contradictory_terminal_process_flags_do_not_authorize_complement(self):
        for field, replacement in (('process', 'nonzero'), ('executionState', 'nonzero')):
            value=fixture(complete=False)
            value['result']['runs']['suite'][field]=replacement
            value['result']['timingComplete']=False
            value['result']['metrics'][0].update(returncode=1,status='failed')
            with self.assertRaises(ValueError):
                gate.validate_receipt(value,identity(),policy_hashes(),complete=False)

    def test_joined_interrupted_capture_shape_only_authorizes_failure_continuation(self):
        value=fixture(complete=False); suite=value['result']['runs']['suite']
        value['result']['timingComplete']=False
        suite.update(process='watchdog_timeout',executionState='watchdog_timeout',returncode=-9,observedReturncode=None)
        value['result']['metrics'][0].update(returncode=-9,status='failed')
        gate.validate_receipt(value,identity(),policy_hashes(),complete=False)
        with self.assertRaises(ValueError):gate.validate_core(value['result'],PLATFORM)

    def test_late_release_outputs_are_exact_regular_owned_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); (root/'logs').mkdir(); (root/'source.go').write_text('synthetic')
            (root/'logs/go-cache.txt').write_text('synthetic')
            (root/'logs/dependencies.txt').write_text('synthetic')
            def query(command):
                if command[1]=='status':return b''
                if '--others' in command:return b'logs/go-cache.txt\0logs/dependencies.txt\0'
                return b'source.go\0'
            with patch.object(gate,'ROOT',root),patch.object(gate.collector.native,'checked',side_effect=query):
                self.assertEqual(set(gate.source_inventory(late=True,release=True)),{'source.go'})
                with self.assertRaises(ValueError):gate.source_inventory(late=True)
                (root/'logs/go-cache.txt').unlink()
                os.link(root/'source.go',root/'logs/go-cache.txt')
                with self.assertRaises(ValueError):gate.source_inventory(late=True,release=True)
                (root/'logs/go-cache.txt').unlink()
                (root/'logs/go-cache.txt').symlink_to(root/'source.go')
                with self.assertRaises(ValueError):gate.source_inventory(late=True,release=True)


class IdentityTests(unittest.TestCase):
    def environment(self):
        return {'GITHUB_ACTIONS': 'true', 'GITHUB_REPOSITORY': gate.REPOSITORY, 'GITHUB_JOB': 'native',
            'GITHUB_WORKFLOW': 'Cross-platform CI', 'GITHUB_EVENT_NAME': 'pull_request',
            'GITHUB_REF': 'refs/pull/7/merge', 'GITHUB_SHA': SHA, 'GITHUB_RUN_ID': '10', 'GITHUB_RUN_ATTEMPT': '1',
            'GITHUB_WORKFLOW_REF': gate.REPOSITORY + '/' + gate.WORKFLOW + '@refs/pull/7/merge'}
    def event(self):
        return {'number': 7, 'repository': {'full_name': gate.REPOSITORY, 'private': False, 'default_branch': 'main'},
            'pull_request': {'merge_commit_sha': SHA, 'head': {'sha': 'd'*40}, 'base': {'repo': {'full_name': gate.REPOSITORY}}}}
    def test_pr_actual_merge_and_head_are_distinct_and_diagnostic_create_rejected(self):
        with patch.dict(os.environ, self.environment(), clear=True):
            value=gate.ci_identity(self.event(), SHA, TREE, PLATFORM)
            self.assertEqual((value['eventHead'],value['commit']),('d'*40,SHA))
            for key, replacement in [('GITHUB_EVENT_NAME','create'), ('GITHUB_SHA','e'*40), ('GITHUB_RUN_ATTEMPT','0')]:
                with patch.dict(os.environ, {key:replacement}), self.assertRaises(ValueError):
                    gate.ci_identity(self.event(),SHA,TREE,PLATFORM)
    def test_prerelease_binds_separate_event_head_and_tested_checkout(self):
        env=self.environment();env.update(GITHUB_WORKFLOW='Experimental prerelease', GITHUB_EVENT_NAME='workflow_dispatch',
            GITHUB_REF='refs/heads/main', GITHUB_SHA='d'*40, TESTED_COMMIT=SHA,
            GITHUB_WORKFLOW_REF=gate.REPOSITORY+'/'+gate.RELEASE_WORKFLOW+'@refs/heads/main')
        event=self.event();event['inputs']={'tested_commit':SHA}
        with patch.dict(os.environ,env,clear=True):
            value=gate.ci_identity(event,SHA,TREE,PLATFORM)
            self.assertEqual((value['eventHead'],value['commit']),('d'*40,SHA))
            event['inputs']['tested_commit']='e'*40
            with self.assertRaises(ValueError): gate.ci_identity(event,SHA,TREE,PLATFORM)


class ArtifactTests(unittest.TestCase):
    def audit(self, platform=PLATFORM, *, effective=1, producing=1, mutate=None):
        meta,data=artifact(platform,producing)
        current,previous=job(platform,effective,101),job(platform,producing,100)
        state={'artifact':meta,'current':current,'previous':previous,'run':run(effective),'prior':run(producing)}
        if mutate: mutate(state)
        def api(path):
            if '/artifacts?' in path:return {'total_count':1,'artifacts':[state['artifact']]}
            if path=='actions/artifacts/301':return state['artifact']
            if '/jobs?' in path:
                selected=state['current'] if f'/attempts/{effective}/' in path else state['previous']
                return {'total_count':1,'jobs':[selected]}
            if path==f'actions/runs/10/attempts/{producing}':return state['prior']
            raise AssertionError(path)
        return gate.audit_artifact(api,state['run'],SHA,TREE,platform,download=lambda _:data)
    def test_exact_artifact_and_legitimate_carried_forward_results(self):
        for platform in gate.TARGETS:
            self.assertEqual(self.audit(platform)['producingAttempt'],1)
            proof=self.audit(platform,effective=2,producing=1)
            self.assertEqual((proof['producingAttempt'],proof['effectiveAttempt']),(1,2))
    def test_failed_skipped_missing_or_out_of_order_phase_cannot_pass(self):
        for index in range(3):
            for state in ('skipped','failure'):
                with self.subTest(index=index,state=state),self.assertRaises(ValueError):
                    self.audit(mutate=lambda s:s['current']['steps'][index].update(conclusion=state))
        with self.assertRaises(ValueError):self.audit(mutate=lambda s:s['current']['steps'].pop(1))
        with self.assertRaises(ValueError):self.audit(mutate=lambda s:s['current']['steps'][1].update(started_at='2026-01-01T00:00:02Z'))
    def test_official_source_attempt_hash_size_and_timestamps_are_bound(self):
        changes=[lambda s:s['artifact'].update(expired=True),lambda s:s['artifact'].update(digest='sha256:'+'d'*64),
            lambda s:s['artifact'].update(size_in_bytes=1),lambda s:s['artifact']['workflow_run'].update(head_sha='d'*40),
            lambda s:s['artifact'].update(created_at='2026-01-01T00:00:12Z'),
            lambda s:s['artifact'].update(name=gate.ARTIFACT+'-'+PLATFORM+'-10-2'),
            lambda s:s['current'].update(run_attempt=True),lambda s:s['current'].update(labels=['ubuntu-24.04'])]
        for mutate in changes:
            with self.assertRaises(ValueError):self.audit(mutate=mutate)
    def test_prior_artifact_requires_actually_carried_forward_identical_job(self):
        for mutate in (lambda s:s['current'].update(started_at='2026-01-01T00:01:01Z'),
                       lambda s:s['previous']['steps'][0].update(completed_at='2026-01-01T00:00:09Z'),
                       lambda s:s['prior'].update(head_sha='d'*40)):
            with self.assertRaises(ValueError):self.audit(effective=2,producing=1,mutate=mutate)


class WorkflowAndBundleTests(unittest.TestCase):
    def test_collector_bytes_and_existing_partition_are_unchanged(self):
        self.assertEqual(hashlib.sha256((gate.ROOT/'.github/scripts/ci-native-core.py').read_bytes()).hexdigest(),
                         'd5e6b47a41cfd608e3d4f34a9f9949a351b07ac60299243ca1d68d677b881389')
        self.assertEqual(hashlib.sha256((gate.ROOT/'.github/scripts/ci-windows-packages.py').read_bytes()).hexdigest(),
                         '94e8976ace4f8a54c23fbe02aacd474ac1491e850822bd8e9befb731c4f42070')
    def test_early_core_precedes_frontend_and_policy_but_linux_path_remains(self):
        for name in ('ci.yml','prerelease.yml'):
            text=(gate.ROOT/'.github/workflows'/name).read_text()
            native=text.split('  native:\n',1)[1]
            self.assertLess(native.index('name: Collect required whole-Core'),native.index('name: Test and reproduce locked frontend'))
            self.assertEqual(text.count('ci-native-core-required.py core'),1)
            self.assertEqual(text.count('ci-native-core-required.py remaining'),1)
            self.assertIn("if [[ '${{ matrix.goos }}' == 'linux' ]]; then",native)
            self.assertIn('go test -race -count=1 -timeout=10m ./...',native)
            self.assertIn('steps.native-core.outputs.remaining_safe',native)
            self.assertNotIn('continue-on-error:',native.split('name: Collect required whole-Core')[1].split('      - ',1)[0])
    def test_downloaded_release_bundle_imports_and_validates_without_checkout(self):
        workflow=(gate.ROOT/'.github/workflows/prerelease.yml').read_text()
        region=workflow.split('name: release-validation-tools\n',1)[1].split('          if-no-files-found:',1)[0]
        names=[line.strip() for line in region.splitlines() if line.strip().startswith('.github/scripts/')]
        with tempfile.TemporaryDirectory() as directory:
            folder=Path(directory)/'tools';folder.mkdir()
            for name in names:shutil.copyfile(gate.ROOT/name,folder/Path(name).name)
            value=fixture();payload=Path(directory)/'synthetic.json'
            payload.write_text(json.dumps({'value':value,'identity':identity(),'policy':policy_hashes()}))
            code="import importlib.util,json,sys; from pathlib import Path; p=Path(sys.argv[1]); s=importlib.util.spec_from_file_location('gate',p); m=importlib.util.module_from_spec(s); exec(compile(p.read_bytes(),str(p),'exec'),m.__dict__); d=json.loads(Path(sys.argv[2]).read_text()); m.validate_receipt(d['value'],d['identity'],d['policy'])"
            result=subprocess.run([os.sys.executable,'-I','-B','-c',code,str(folder/SCRIPT.name),str(payload)],cwd=directory,capture_output=True)
            self.assertEqual(result.returncode,0,result.stderr.decode())


if __name__=='__main__':unittest.main()
