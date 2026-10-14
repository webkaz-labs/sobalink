"""Synthetic required Core/package/official-artifact policy fixtures; no native proof."""
import copy
from contextlib import ExitStack, chdir, redirect_stdout
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch, Mock

from test_ci_native_core import event, raw
from test_ci_dnssd_required import archive
from test_ci_upload_artifact import upload_log
import test_ci_dnssd_required as dnssd_fixtures

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
        root = Path(stack.enter_context(tempfile.TemporaryDirectory())).resolve()
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
        self.assertEqual((root/'output').read_text(encoding='utf-8'),'remaining_safe=true\n')
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
        self.assertEqual((root/'output').read_text(encoding='utf-8'),'remaining_safe=true\n')
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
            root=Path(directory).resolve(); (root/'logs').mkdir(); (root/'source.go').write_text('synthetic',encoding='utf-8')
            (root/'logs/go-cache.txt').write_text('synthetic',encoding='utf-8')
            (root/'logs/dependencies.txt').write_text('synthetic',encoding='utf-8')
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
            'pull_request': {'merge_commit_sha': SHA, 'head': {'sha': 'd'*40}, 'base': {'ref': 'main', 'sha': 'e'*40, 'repo': {'full_name': gate.REPOSITORY}}}}
    def test_pr_actual_merge_and_head_are_distinct_and_diagnostic_create_rejected(self):
        native=Mock(); native.checked.side_effect=lambda command: ((SHA+'\n'+TREE+'\n') if command[1]=='rev-parse' else ' '.join([SHA,'e'*40,'d'*40])).encode()
        with patch.dict(os.environ, self.environment(), clear=True), patch.object(gate.shared,'module',return_value=native):
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


class ActualContextTests(unittest.TestCase):
    """Real synthetic Git objects and actual entry/context; native execution is mocked."""
    def setUp(self):
        self.stack=ExitStack(); self.addCleanup(self.stack.close)
        # Match production ROOT and cwd, including temporary-path aliases.
        self.folder=Path(self.stack.enter_context(tempfile.TemporaryDirectory())).resolve()
        self.root=self.folder/'repo'; self.root.mkdir()
        self.temporary=self.folder/'runner'; self.temporary.mkdir()
        self.output=self.folder/'output'
        def git(*args):
            return subprocess.check_output(['git','-c','user.name=Synthetic','-c','user.email=synthetic@example.invalid',
                                            *args],cwd=self.root,stderr=subprocess.DEVNULL).decode().strip()
        self.git=git
        git('init','-q'); (self.root/'synthetic.txt').write_text('synthetic fixture\n',encoding='utf-8')
        git('add','synthetic.txt'); self.tree=git('write-tree')
        self.base=git('commit-tree',self.tree,'-m','synthetic base')
        self.head=git('commit-tree',self.tree,'-p',self.base,'-m','synthetic head')
        self.commit=git('commit-tree',self.tree,'-p',self.base,'-p',self.head,'-m','synthetic merge')
        git('update-ref','HEAD',self.commit)
        self.event=IdentityTests().event()
        self.event['pull_request'].update(merge_commit_sha=self.commit)
        self.event['pull_request']['base']['sha']=self.base
        self.event['pull_request']['head']['sha']=self.head
        self.event_path=self.folder/'event.json'; self.write_event()
        self.env=IdentityTests().environment()
        self.env.update(GITHUB_SHA=self.commit,GITHUB_EVENT_PATH=str(self.event_path),RUNNER_TEMP=str(self.temporary),
                        GITHUB_OUTPUT=str(self.output),SOBALINK_CORE_NATIVE_PLATFORM=PLATFORM)
        native=gate.shared.module('ci-nearby-native')
        original_module=gate.shared.module
        for manager in (chdir(self.root), patch.dict(os.environ,self.env),patch.object(gate,'ROOT',self.root),
            patch.object(gate.shared,'ROOT',self.root),patch.object(native,'ROOT',self.root),
            patch.object(gate.collector.native,'ROOT',self.root),
            patch.dict(native.capture.__kwdefaults__,{'cwd':self.root}),
            patch.dict(gate.collector.native.capture.__kwdefaults__,{'cwd':self.root}),
            patch.object(gate.shared,'module',side_effect=lambda name:native if name=='ci-nearby-native' else original_module(name))):
            self.stack.enter_context(manager)
        # Every negative case must begin with a valid actual identity.
        self.check_identity()

    def write_event(self):
        self.event_path.write_text(json.dumps(self.event),encoding='utf-8')

    def check_identity(self):
        core=gate.context(PLATFORM)
        discovery=gate.shared.ci_identity(self.event,self.commit,self.tree)
        self.assertEqual(core['eventHead'],self.head)
        self.assertEqual(core['commit'],self.commit)
        self.assertEqual(core['tree'],self.tree)
        self.assertEqual({k:v for k,v in core.items() if k!='job'}, {k:v for k,v in discovery.items() if k!='job'})

    def test_actual_ordered_merge_accepts_missing_null_matching_and_stale_hints_for_both_callers(self):
        for kind,hint in [('missing',None),('null',None),('matching',self.commit),('different','f'*40)]:
            with self.subTest(kind=kind):
                self.event['pull_request']['merge_commit_sha']=hint
                if kind=='missing': del self.event['pull_request']['merge_commit_sha']
                self.write_event(); self.check_identity()
                self.assertEqual(gate.merge_hint_state(self.event,self.commit),kind)

    def test_missing_null_wrong_base_head_and_malformed_hint_never_admit(self):
        original=copy.deepcopy(self.event)
        mutations=[lambda e:e.pop('pull_request'),lambda e:e.update(pull_request=None),
            lambda e:e['pull_request'].pop('base'),lambda e:e['pull_request'].pop('head')]
        for side in ('base','head'):
            for value in (None,'f'*40,0,'0'*40):
                mutations.append(lambda e,side=side,value=value:e['pull_request'][side].update(sha=value))
            mutations.append(lambda e,side=side:e['pull_request'][side].pop('sha'))
        mutations += [lambda e:e['pull_request']['base'].update(ref='other'),
            lambda e:e['pull_request']['base']['repo'].update(full_name='synthetic/other')]
        for hint in ('bad\nsynthetic-private',True,{},'0'*40,7):
            mutations.append(lambda e,hint=hint:e['pull_request'].update(merge_commit_sha=hint))
        for index,mutate in enumerate(mutations):
            self.event=copy.deepcopy(original); mutate(self.event); self.write_event()
            for call in (lambda:gate.context(PLATFORM),lambda:gate.shared.ci_identity(self.event,self.commit,self.tree)):
                with self.subTest(index=index),self.assertRaises(ValueError):call()

    def test_actual_missing_reversed_or_extra_parents_reject_even_matching_hint(self):
        for parents in ([self.base],[self.head,self.base],[self.base,self.head,'f'*40]):
            if parents[-1]=='f'*40: parents[-1]=self.git('commit-tree',self.tree,'-m','synthetic extra')
            commit=self.git('commit-tree',self.tree,*[arg for parent in parents for arg in ('-p',parent)],'-m','synthetic mismatch')
            self.git('update-ref','HEAD',commit)
            self.event['pull_request']['merge_commit_sha']=commit; self.write_event()
            with patch.dict(os.environ,{'GITHUB_SHA':commit}):
                for call in (lambda:gate.context(PLATFORM),lambda:gate.shared.ci_identity(self.event,commit,self.tree)):
                    with self.subTest(parents=len(parents)),self.assertRaisesRegex(ValueError,'identity_pr_parents'):call()

    def test_checkout_tree_event_ref_workflow_and_attempt_remain_exact(self):
        with chdir(self.folder):
            for call in (lambda:gate.context(PLATFORM),lambda:gate.shared.ci_identity(self.event,self.commit,self.tree)):
                with self.assertRaisesRegex(ValueError,'identity_environment'):call()
        for commit,tree in [('f'*40,self.tree),(self.commit,'f'*40),(self.commit,None)]:
            with self.subTest(commit=commit,tree=tree),self.assertRaises(ValueError):
                gate.shared.ci_identity(self.event,commit,tree)
        for key,value in [('GITHUB_SHA','f'*40),('GITHUB_REF','refs/pull/8/merge'),('GITHUB_WORKFLOW_REF','synthetic'),
            ('GITHUB_RUN_ID','0'),('GITHUB_RUN_ATTEMPT','0'),('GITHUB_JOB','other'),('GITHUB_REPOSITORY','synthetic/other')]:
            with patch.dict(os.environ,{key:value}),self.subTest(key=key),self.assertRaises(ValueError):gate.context(PLATFORM)
        self.git('update-ref','HEAD',self.head)
        with self.assertRaisesRegex(ValueError,'identity_checkout'):
            gate.shared.ci_identity(self.event,self.commit,self.tree)

    def entry_failure(self,stage,predicate='unclassified',*,partial=True,hint='matching'):
        stream=io.StringIO()
        with redirect_stdout(stream):self.assertEqual(gate.main(['core']),1)
        self.assertEqual(stream.getvalue(),'FAIL: Required native package phase is incomplete.\n')
        self.assertEqual(self.output.read_text(encoding='utf-8'),'failure_ready=true\n')
        folder=self.temporary/gate.FAILURE_ARTIFACT
        failure=json.loads((folder/'failure.json').read_text(encoding='utf-8'))
        self.assertEqual(failure,{'schema':1,'successEligible':False,'stage':stage,'predicate':predicate,
                                 'mergeHint':hint,'partialCorePresent':partial})
        self.assertEqual(set(p.name for p in folder.iterdir()),{'failure.json','partial-core.json'} if partial else {'failure.json'})
        for path in folder.iterdir():
            self.assertLess(path.stat().st_size,32768)
            self.assertNotIn('synthetic-private',path.read_text(encoding='utf-8'))
            with self.assertRaises(ValueError):gate.validate_receipt(json.loads(path.read_text(encoding='utf-8')),identity(),{name:HASH for name in gate.POLICY_FILES})
        self.assertFalse((self.temporary/gate.ARTIFACT/'receipt.json').exists())
        return folder

    def test_actual_main_context_failure_exports_closed_predicate_without_partial_or_remaining(self):
        self.event['pull_request']['head']['sha']='f'*40; self.write_event()
        with patch.object(gate.collector,'runtime_capability') as runtime:
            self.entry_failure('identity','identity_pr_parents',partial=False)
            runtime.assert_not_called()

    def test_actual_main_unparseable_event_exports_no_event_or_parser_text(self):
        self.event_path.write_text('{synthetic-private',encoding='utf-8')
        with patch.object(gate.collector,'runtime_capability') as runtime:
            self.entry_failure('event-read',partial=False,hint='unobserved');runtime.assert_not_called()

    def test_actual_main_malformed_hint_exports_only_closed_classification(self):
        self.event['pull_request']['merge_commit_sha']='synthetic-private';self.write_event()
        self.entry_failure('identity','identity_merge_hint',partial=False,hint='malformed')

    def test_actual_main_runtime_failure_exports_empty_partial_without_native_execution(self):
        with patch.object(gate.collector,'runtime_capability',return_value={'available':False}),patch.object(gate,'native_identity') as native:
            folder=self.entry_failure('runtime','runtime_unavailable'); native.assert_not_called()
        self.assertEqual(json.loads((folder/'partial-core.json').read_text(encoding='utf-8'))['phase'],'preflight')

    def test_actual_main_native_failure_sanitizes_injected_exception(self):
        with patch.object(gate.collector,'runtime_capability',return_value={'available':True}),patch.object(gate,'native_identity',side_effect=ValueError('synthetic-private\ntext')):
            self.entry_failure('native-identity')

    def preflight(self):
        for manager in (patch.object(gate.collector,'runtime_capability',return_value={'available':True}),
            patch.object(gate,'native_identity',return_value={}),patch.object(gate.shutil,'which',return_value='/synthetic/go'),
            patch.object(gate.collector.native,'digest',return_value=HASH)):
            self.stack.enter_context(manager)

    def test_actual_main_source_failure_never_authorizes_complement(self):
        self.preflight()
        with patch.object(gate,'source_inventory',side_effect=ValueError('synthetic-private')):
            self.entry_failure('source-inventory')

    def test_actual_main_preparation_failure_exports_only_closed_process_fields(self):
        self.preflight()
        def fail_prepare(env,result):
            result.update(phase='prepare',ownersState='joined')
            result['preparation']['prepare']={'process':'nonzero','executionState':'nonzero','returncode':1,
                'observedReturncode':1,'ownersJoined':True,'groupAbsenceObservedAfterWait':None,'wallSeconds':0.1}
            raise ValueError('synthetic-private')
        with patch.object(gate,'source_inventory',return_value={name:HASH for name in gate.POLICY_FILES}),patch.object(gate.collector,'run_diagnostic',side_effect=fail_prepare):
            folder=self.entry_failure('collector-prepare')
        partial=json.loads((folder/'partial-core.json').read_text(encoding='utf-8'))
        self.assertEqual(partial['preparation']['prepare']['process'],'nonzero')
        self.assertTrue(partial['cleanupComplete']);self.assertFalse(partial['sourceVerified'])
        self.assertFalse(list(self.temporary.glob('soba-native-core-private-*')))

    def test_existing_failure_destination_is_never_read_changed_or_removed(self):
        target=self.temporary/gate.FAILURE_ARTIFACT
        for mode in ('file','directory','symlink'):
            with self.subTest(mode=mode):
                if mode=='file':target.write_text('synthetic-private',encoding='utf-8')
                elif mode=='directory':target.mkdir();(target/'failure.json').write_text('synthetic-private',encoding='utf-8')
                elif os.name!='nt':target.symlink_to(self.event_path)
                with ExitStack() as local:
                    if mode=='symlink' and os.name=='nt':
                        # Model only exclusive mkdir's existing-link boundary;
                        # no Windows symlink privilege or native proof is claimed.
                        mkdir=Path.mkdir
                        def exclusive(path,*args,**kwargs):
                            if path==target:raise FileExistsError('synthetic existing link')
                            return mkdir(path,*args,**kwargs)
                        local.enter_context(patch.object(Path,'mkdir',exclusive))
                    local.enter_context(patch.object(gate.collector,'runtime_capability',return_value={'available':False}))
                    local.enter_context(patch.object(gate,'read_receipt',side_effect=AssertionError('must not read preexisting destination')))
                    self.assertEqual(gate.main(['core']),1)
                self.assertFalse(self.output.exists())
                if mode=='directory':
                    self.assertEqual((target/'failure.json').read_text(encoding='utf-8'),'synthetic-private');(target/'failure.json').unlink();target.rmdir()
                elif mode!='symlink' or os.name!='nt':
                    self.assertTrue(target.is_symlink() if mode=='symlink' else target.read_text(encoding='utf-8')=='synthetic-private');target.unlink()
                else:self.assertFalse(target.exists())
                # Remove only the fixture's own known Core directory between cases.
                export=self.temporary/gate.ARTIFACT
                if export.exists():shutil.rmtree(export)

    def test_preexisting_core_directory_is_never_used_as_failure_partial(self):
        export=self.temporary/gate.ARTIFACT;export.mkdir()
        (export/'core.json').write_text('synthetic-private',encoding='utf-8')
        with patch.object(gate.collector,'runtime_capability') as runtime:
            self.entry_failure('export-directory',partial=False);runtime.assert_not_called()
        self.assertEqual((export/'core.json').read_text(encoding='utf-8'),'synthetic-private')

    def test_failure_export_write_error_is_suppressed_without_ready_output(self):
        original=gate.write_receipt
        def write(path,value):
            if path.parent.name==gate.FAILURE_ARTIFACT:raise OSError('synthetic-private')
            return original(path,value)
        with patch.object(gate.collector,'runtime_capability',return_value={'available':False}),patch.object(gate,'write_receipt',side_effect=write):
            self.assertEqual(gate.main(['core']),1)
        self.assertFalse(self.output.exists())
        self.assertEqual(list((self.temporary/gate.FAILURE_ARTIFACT).iterdir()),[])
        self.assertFalse((self.temporary/gate.ARTIFACT/'receipt.json').exists())

    def test_invalid_partial_suppresses_failure_output_and_never_exports_injected_text(self):
        self.preflight()
        def invalid(env,result):
            result['phase']='synthetic-private';raise ValueError('synthetic-private')
        with patch.object(gate,'source_inventory',return_value={name:HASH for name in gate.POLICY_FILES}),patch.object(gate.collector,'run_diagnostic',side_effect=invalid):
            self.assertEqual(gate.main(['core']),1)
        self.assertFalse(self.output.exists())
        self.assertEqual(list((self.temporary/gate.FAILURE_ARTIFACT).iterdir()),[])
        self.assertFalse((self.temporary/gate.ARTIFACT/'receipt.json').exists())


class FailureSummaryTests(unittest.TestCase):
    def test_summary_drops_every_unexported_string_and_rejects_exported_string_injection(self):
        value=fixture(complete=False)
        value['provenance']={'arbitrary':'synthetic-private'}
        suite=value['result']['runs']['suite']
        suite['tests']='synthetic-private';suite['progress']='synthetic-private'
        value['result']['metrics']='synthetic-private'
        summary=gate.partial_core(value)
        self.assertNotIn('synthetic-private',json.dumps(summary))
        self.assertIs(summary['successEligible'],False)
        for path in [('result','phase'),('result','ownersState'),('result','runs','suite','process'),
            ('result','runs','suite','packageOutcome'),('result','runs','suite','wallSeconds'),
            ('result','runs','suite','expectedCount'),('result','runs','suite','allTestsPassed')]:
            changed=copy.deepcopy(value);target=changed
            for key in path[:-1]:target=target[key]
            target[path[-1]]='synthetic-private'
            with self.subTest(path=path),self.assertRaises(ValueError):gate.partial_core(changed)
        self.assertEqual(gate.failure_predicate(ValueError('synthetic-private')),'unclassified')


class ArtifactTests(unittest.TestCase):
    def audit(self, platform=PLATFORM, *, effective=1, producing=1, mutate=None):
        meta,data=artifact(platform,producing)
        current,previous=job(platform,effective,101),job(platform,producing,100)
        state={'artifact':meta,'current':current,'previous':previous,'run':run(effective),'prior':run(producing),
               'log':upload_log(meta)}
        if mutate: mutate(state)
        calls = {}
        def api(path):
            calls[path] = calls.get(path, 0) + 1
            if calls[path] == 2 and path == state.get('mutatePath'):
                state['onReadback'](state)
            if '/artifacts?' in path:
                artifacts=([state['artifact']] if state['artifact'] is not None else [])+state.get('extraArtifacts',[])
                return {'total_count':len(artifacts),'artifacts':copy.deepcopy(artifacts)}
            if state['artifact'] is not None and path==f"actions/artifacts/{state['artifact']['id']}":return copy.deepcopy(state['artifact'])
            if '/jobs?' in path:
                selected=state['current'] if f'/attempts/{effective}/' in path else state['previous']
                return {'total_count':1,'jobs':[copy.deepcopy(selected)]}
            if path==f'actions/runs/10/attempts/{producing}':return state['prior']
            raise AssertionError(path)
        def job_log(identifier):
            self.assertEqual(identifier, 101 if effective == producing else 100)
            return state['log']
        return gate.audit_artifact(api,state['run'],SHA,TREE,platform,download=lambda _:data,job_log=job_log)
    def test_exact_artifact_and_legitimate_carried_forward_results(self):
        for platform in gate.TARGETS:
            self.assertEqual(self.audit(platform)['producingAttempt'],1)
            proof=self.audit(platform,effective=2,producing=1)
            self.assertEqual((proof['producingAttempt'],proof['effectiveAttempt']),(1,2))
    def test_one_second_post_step_metadata_uses_original_upload_association(self):
        for platform in gate.TARGETS:
            for effective in (1,2):
                proof=self.audit(platform,effective=effective,producing=1,
                    mutate=lambda s:s['artifact'].update(created_at='2026-01-01T00:00:51Z',updated_at='2026-01-01T00:00:51Z'))
                self.assertEqual(proof['producingJobId'],101 if effective == 1 else 100)

    def test_same_name_replacement_future_metadata_and_bad_logs_are_rejected(self):
        changes=[lambda s:s['artifact'].update(id=302),
            lambda s:s['artifact'].update(created_at='2026-01-01T00:01:01Z'),
            lambda s:s['artifact'].update(updated_at='2026-01-01T00:01:01Z'),
            lambda s:s['artifact'].update(updated_at='2026-01-01T00:00:39Z'),
            lambda s:s.update(log=b''),lambda s:s.update(log=s['log']+s['log']),
            lambda s:s.update(log=upload_log(dict(s['artifact'],id=302)))]
        for platform in gate.TARGETS:
            for mutate in changes:
                with self.subTest(platform=platform),self.assertRaises(ValueError):self.audit(platform,mutate=mutate)

    def test_artifact_and_original_job_readback_mutations_fail(self):
        for platform in gate.TARGETS:
            for effective in (1, 2):
                for target in ('artifact', 'producer'):
                    def mutate(state):
                        path = 'actions/artifacts/301' if target == 'artifact' else 'actions/runs/10/attempts/1/jobs?per_page=100&page=1'
                        def change(value):
                            if target == 'artifact': value['artifact']['updated_at']='2026-01-01T00:00:42Z'
                            else: value['current' if effective == 1 else 'previous']['id']=999
                        state.update(mutatePath=path, onReadback=change)
                    with self.subTest(platform=platform,effective=effective,target=target),self.assertRaises(ValueError):
                        self.audit(platform,effective=effective,mutate=mutate)

    def test_failure_artifacts_never_match_required_or_discovery_selectors_even_on_later_attempt(self):
        extras=[{'name':gate.FAILURE_ARTIFACT+'-'+target+'-10-'+str(attempt)}
                for target in gate.TARGETS for attempt in (1,2)]
        for index,row in enumerate(extras,400):row['id']=index
        self.assertTrue(all(not row['name'].startswith(prefix+'-') for row in extras
                            for prefix in (gate.ARTIFACT,gate.shared.ARTIFACT)))
        for effective in (1,2):
            proof=self.audit(effective=effective,producing=1,mutate=lambda s:s.update(extraArtifacts=extras))
            self.assertEqual(proof['producingAttempt'],1)
            with self.assertRaises(ValueError):
                self.audit(effective=effective,producing=1,mutate=lambda s:s.update(artifact=None,extraArtifacts=extras))
            def failed(s):
                s.update(extraArtifacts=extras);s['current']['conclusion']='failure'
            with self.assertRaises(ValueError):self.audit(effective=effective,producing=1,mutate=failed)
        value={'schema':1,'successEligible':False,'stage':'runtime','predicate':'runtime_unavailable',
               'mergeHint':'matching','partialCorePresent':False}
        for names in (('failure.json',),('receipt.json',)):
            data=archive(value,names=names)
            meta={'size_in_bytes':len(data),'digest':'sha256:'+hashlib.sha256(data).hexdigest()}
            with self.assertRaises(ValueError):
                receipt,_=gate.shared.artifact_receipt(data,meta)
                gate.validate_receipt(receipt,identity(),policy_hashes())

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
            text=(gate.ROOT/'.github/workflows'/name).read_text(encoding='utf-8')
            native=text.split('  native:\n',1)[1]
            self.assertLess(native.index('name: Collect required whole-Core'),native.index('name: Test and reproduce locked frontend'))
            self.assertEqual(text.count('ci-native-core-required.py core'),1)
            self.assertEqual(text.count('ci-native-core-required.py remaining'),1)
            self.assertIn("if [[ '${{ matrix.goos }}' == 'linux' ]]; then",native)
            self.assertIn('go test -race -count=1 -timeout=10m ./...',native)
            self.assertIn('steps.native-core.outputs.remaining_safe',native)
            self.assertNotIn('continue-on-error:',native.split('name: Collect required whole-Core')[1].split('      - ',1)[0])
    def test_downloaded_release_bundle_imports_and_validates_without_checkout(self):
        workflow=(gate.ROOT/'.github/workflows/prerelease.yml').read_text(encoding='utf-8')
        region=workflow.split('name: release-validation-tools\n',1)[1].split('          if-no-files-found:',1)[0]
        names=[line.strip() for line in region.splitlines() if line.strip().startswith('.github/scripts/')]
        with tempfile.TemporaryDirectory() as directory:
            folder=Path(directory).resolve()/'tools';folder.mkdir()
            for name in names:shutil.copyfile(gate.ROOT/name,folder/Path(name).name)
            value=fixture();payload=Path(directory).resolve()/'synthetic.json'
            payload.write_text(json.dumps({'value':value,'identity':identity(),'policy':policy_hashes()}),encoding='utf-8')
            code="import importlib.util,json,sys; from pathlib import Path; p=Path(sys.argv[1]); s=importlib.util.spec_from_file_location('gate',p); m=importlib.util.module_from_spec(s); exec(compile(p.read_bytes(),str(p),'exec'),m.__dict__); d=json.loads(Path(sys.argv[2]).read_text(encoding='utf-8')); m.validate_receipt(d['value'],d['identity'],d['policy'])"
            result=subprocess.run([os.sys.executable,'-I','-B','-c',code,str(folder/SCRIPT.name),str(payload)],cwd=directory,capture_output=True)
            self.assertEqual(result.returncode,0,result.stderr.decode())


class FixturePortabilityTests(unittest.TestCase):
    """Exercise fixture setup without relaxing the actual admission predicates."""
    def assert_fixture_cases(self):
        cases = [
            ActualContextTests('test_actual_ordered_merge_accepts_missing_null_matching_and_stale_hints_for_both_callers'),
            ActualContextTests('test_actual_missing_reversed_or_extra_parents_reject_even_matching_hint'),
            ActualContextTests('test_checkout_tree_event_ref_workflow_and_attempt_remain_exact'),
            RunnerTests('test_one_collection_and_one_complement_then_no_reuse'),
            RunnerTests('test_late_release_outputs_are_exact_regular_owned_files'),
            dnssd_fixtures.RequiredProofTests('test_prepared_inventory_rejects_ignored_sources_and_symlink_roots'),
        ]
        result = unittest.TestResult()
        unittest.TestSuite(cases).run(result)
        self.assertEqual(result.testsRun, len(cases))
        self.assertEqual(result.skipped, [])
        self.assertEqual(result.errors + result.failures, [])

    def test_noncanonical_temporary_spelling_preserves_identity_and_negative_guards(self):
        # A lexical alias works on every platform, including Windows without
        # depending on the volume's 8.3-name or case-normalization settings.
        original = tempfile.TemporaryDirectory.__enter__
        def enter(temporary):
            directory = Path(original(temporary))
            (directory/'spelling').mkdir()
            return str(directory/'spelling'/'..')
        with patch.object(tempfile.TemporaryDirectory, '__enter__', enter):
            self.assert_fixture_cases()

    def test_symlinked_temporary_ancestor_preserves_identity_and_negative_guards(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            target = root/'canonical'; target.mkdir()
            alias = root/'alias'; alias.symlink_to(target, target_is_directory=True)
            # Keep the alias above ROOT.parent, like a system temp ancestor.
            (alias/'nested').mkdir()
            with patch.object(tempfile, 'tempdir', str(alias/'nested')):
                self.assert_fixture_cases()

    def test_workflow_fixtures_use_utf8_with_legacy_default_encoding(self):
        original = Path.open
        def legacy_open(path, mode='r', buffering=-1, encoding=None, errors=None, newline=None):
            if 'b' not in mode and encoding in (None, 'locale'):
                encoding = 'cp1252'
            return original(path, mode, buffering, encoding, errors, newline)
        result = unittest.TestResult()
        with patch.object(Path, 'open', legacy_open):
            unittest.defaultTestLoader.loadTestsFromTestCase(WorkflowAndBundleTests).run(result)
        self.assertEqual(result.testsRun, 3)
        self.assertEqual(result.skipped, [])
        self.assertEqual(result.errors + result.failures, [])


if __name__=='__main__':unittest.main()
