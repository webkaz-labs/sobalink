"""Synthetic observer tests. These are not hosted evidence or qualification."""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch
import zipfile

SCRIPT = Path(__file__).with_name('ci-aggregate-observer.py')
SPEC = importlib.util.spec_from_file_location('aggregate_observer', SCRIPT)
observer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(observer)


class ObserverTests(unittest.TestCase):
    def test_historical_env_is_explicit_and_does_not_modify_original(self):
        original = {'GITHUB_RUN_ID': '20', 'GITHUB_RUN_ATTEMPT': '1', 'GITHUB_SHA': 'a'*40,
            'GITHUB_EVENT_NAME': 'create', 'GITHUB_OUTPUT': '/synthetic/output',
            'GITHUB_STEP_SUMMARY': '/synthetic/summary', 'GH_TOKEN': 'synthetic-secret'}
        changed = observer.historical_environment(original, Path('/synthetic/event'))
        self.assertEqual(original['GITHUB_RUN_ID'], '20')
        self.assertEqual(changed['GITHUB_RUN_ID'], str(observer.TARGET_RUN))
        self.assertEqual(changed['GITHUB_RUN_ATTEMPT'], '2')
        self.assertEqual(changed['GITHUB_SHA'], observer.TARGET_COMMIT)
        self.assertEqual(changed['GITHUB_REF'], 'refs/pull/51/merge')
        self.assertEqual(changed['FORCE_FULL'], 'false')
        self.assertNotIn('GITHUB_OUTPUT', changed); self.assertNotIn('GITHUB_STEP_SUMMARY', changed)
        self.assertEqual(changed['GH_TOKEN'], original['GH_TOKEN'])

    def test_observer_identity_cannot_be_historical_or_unbounded(self):
        env = {'GITHUB_REPOSITORY': observer.REPOSITORY, 'GITHUB_EVENT_NAME': 'create',
            'GITHUB_REF': 'refs/heads/aggregate-observer/synthetic-once', 'GITHUB_SHA': 'a'*40,
            'GITHUB_RUN_ID': '20', 'GITHUB_RUN_ATTEMPT': '1'}
        event = {'repository':{'full_name':observer.REPOSITORY,'private':False},'ref_type':'branch','ref':'aggregate-observer/synthetic-once'}
        env['GITHUB_EVENT_PATH']='/synthetic/event'
        with patch.object(Path,'read_text',return_value=json.dumps(event)), patch.object(observer,'git',return_value='a'*40):
            self.assertEqual(observer.observer_identity(env), {'run': 20, 'attempt': 1, 'commit': 'a'*40})
        for key, value in [('GITHUB_RUN_ID', str(observer.TARGET_RUN)), ('GITHUB_REF', 'refs/heads/main'),
                           ('GITHUB_EVENT_NAME', 'pull_request'), ('GITHUB_SHA', 'synthetic-secret')]:
            with self.assertRaises(ValueError): observer.observer_identity(dict(env, **{key:value}))

    def test_exact_commit_tree_parents_and_clean_bytes_required(self):
        correct = [observer.TARGET_COMMIT, observer.TARGET_TREE,
            ' '.join([observer.TARGET_COMMIT, observer.TARGET_BASE, observer.TARGET_HEAD]), '']
        for index in range(4):
            values = list(correct); values[index] = 'synthetic-mismatch'
            with patch.object(observer, 'git', side_effect=values), self.assertRaises(ValueError):
                observer.verify_source(Path('/synthetic/baseline'))
        with patch.object(observer, 'git', side_effect=correct):
            observer.verify_source(Path('/synthetic/baseline'))

    def test_unknown_frame_and_exception_text_never_escape(self):
        try: raise OSError('synthetic-secret https://invalid.example/private /synthetic/private')
        except Exception as error:
            report = observer.closed_failure(error, Path('/synthetic/baseline'), 'synthetic-secret')
        self.assertEqual(report, {'stage':'unknown','class':'io_failed','frames':[]})
        class SecretError(Exception): pass
        self.assertEqual(observer.error_class(SecretError('synthetic-secret')), 'unknown')
        self.assertEqual(observer.error_class(subprocess.CalledProcessError(1, ['synthetic-secret'], stderr='synthetic-secret')), 'nonzero')

    def test_only_fixed_original_source_frames_are_emitted(self):
        root = Path('/synthetic/baseline')
        source = 'def download_job_log():\n    raise ValueError("synthetic-secret")\n'
        namespace = {}
        exec(compile(source, str(root / '.github/scripts/ci-upload-artifact.py'), 'exec'), namespace)
        try: namespace['download_job_log']()
        except Exception as error: result = observer.closed_failure(error, root, 'unknown')
        self.assertEqual(result, {'stage':'job_log_capture','class':'validation_failed',
            'frames':[{'module':'ci-upload-artifact.py','function':'download_job_log','line':2}]})
        self.assertNotIn('synthetic-secret', json.dumps(result))

    def test_capture_status_only_is_observed_and_scoped_to_active_fetch(self):
        root=Path('/synthetic/baseline')
        for status in (*observer.CAPTURE_STATES, 'synthetic-secret', ['synthetic-secret']):
            capture_scope={'answer':(status, 'synthetic-secret', b'synthetic-secret', b'synthetic-secret')}
            exec(compile('def capture():\n    return answer\n',str(root/'.github/scripts/ci-nearby-native.py'),'exec'),capture_scope)
            fetch_scope={'capture':capture_scope['capture']}
            exec(compile('def download_job_log():\n    return capture()\n',str(root/'.github/scripts/ci-upload-artifact.py'),'exec'),fetch_scope)
            record=observer.CaptureObserver(root)
            with record.enabled():
                returned=fetch_scope['download_job_log']()
            self.assertIs(returned,capture_scope['answer'])
            expected=status if type(status) is str and status in observer.CAPTURE_STATES else 'unknown'
            self.assertEqual(record.last,dict({'fetch':'job_log_capture','status':expected}, **({'error':'unknown'} if expected == 'nonzero' else {})))
            self.assertIsNone(record.active)
            self.assertNotIn('synthetic-secret',json.dumps(record.last))
            with record.enabled():
                capture_scope['capture']()
            self.assertEqual(record.last,dict({'fetch':'job_log_capture','status':expected}, **({'error':'unknown'} if expected == 'nonzero' else {})))

    def test_unexpected_capture_shape_is_closed_unknown(self):
        root=Path('/synthetic/baseline');record=observer.CaptureObserver(root)
        from types import SimpleNamespace
        frame=SimpleNamespace(f_code=SimpleNamespace(co_filename=str(root/'.github/scripts/ci-nearby-native.py'),co_name='capture'))
        for value in ('synthetic-secret', (), ('synthetic-secret',), ['synthetic-secret']):
            record.active='job_log_capture';record.observe(frame,'return',value)
            self.assertEqual(record.last,{'fetch':'job_log_capture','status':'unknown'})

    def fixture(self, finalize):
        raw_io = io.BytesIO()
        with zipfile.ZipFile(raw_io, 'w') as archive: archive.writestr('ci-impact-plan.json', '{}')
        raw = raw_io.getvalue()
        run = {'id':observer.TARGET_RUN,'run_attempt':2,'head_sha':observer.TARGET_HEAD,
            'event':'pull_request','path':'.github/workflows/ci.yml','name':'Cross-platform CI',
            'head_branch':'feature/nearby-durable-text','status':'completed','conclusion':'failure',
            'repository':{'full_name':observer.REPOSITORY},'pull_requests':[{'number':51,
                'head':{'sha':observer.TARGET_HEAD},'base':{'sha':observer.TARGET_BASE,'ref':'main'}}]}
        meta = {'id':observer.PLAN_ID,'name':'ci-impact-plan','expired':False,'size_in_bytes':len(raw),
            'digest':'sha256:'+hashlib.sha256(raw).hexdigest(),
            'workflow_run':{'id':observer.TARGET_RUN,'head_sha':observer.TARGET_HEAD}}
        coverage = Mock(finalize=Mock(side_effect=finalize))
        coverage.read_plan.return_value = {}
        coverage.classify.return_value = {}
        coverage.api.side_effect = lambda path: meta if 'artifacts/' in path else run
        native = Mock(); native.capture.return_value = ('completed',0,raw,b'synthetic-secret')
        stack = contextlib.ExitStack()
        stack.enter_context(patch.object(observer, 'PLAN_SIZE', len(raw)))
        stack.enter_context(patch.object(observer, 'PLAN_DIGEST', hashlib.sha256(raw).hexdigest()))
        stack.enter_context(patch.object(observer, 'verify_source'))
        stack.enter_context(patch.object(observer, 'closed_cli_version', return_value='2.102.0'))
        stack.enter_context(patch.object(observer, 'load', side_effect=[coverage,native]))
        self.addCleanup(stack.close)
        return coverage, native

    def test_success_handled_exception_and_failure_are_always_nonqualifying_and_clean(self):
        for fail in (False, True):
            with self.subTest(fail=fail):
                paths=[]
                def finalize(plan, output):
                    paths.append(Path(output)); Path(output).write_text('synthetic-private-receipt')
                    self.assertEqual(os.environ['GITHUB_RUN_ATTEMPT'], '2')
                    self.assertNotIn('GITHUB_STEP_SUMMARY', os.environ)
                    print('synthetic-secret')
                    os.write(2, b'synthetic-secret\n')
                    try: raise ValueError('handled-synthetic-secret')
                    except ValueError: pass
                    if fail: raise ValueError('terminal-synthetic-secret')
                coverage, _ = self.fixture(finalize)
                original=dict(os.environ)
                report=observer.run_observation(Path('/synthetic/baseline'),{'run':20,'attempt':1,'commit':'a'*40})
                self.assertEqual(dict(os.environ), original)
                self.assertFalse(report['qualifying'])
                self.assertEqual(report['schema'], 'aggregate-observation-v1')
                self.assertTrue(report['privateOutputDeleted']); self.assertTrue(report['sourceRechecked'])
                self.assertTrue(paths and not paths[0].exists() and not paths[0].parent.exists())
                self.assertEqual(report['result'], 'rejected' if fail else 'observed_success_not_qualification')
                self.assertNotIn('synthetic-secret', json.dumps(report))
                self.assertNotIn('macos_discovery_proof', report); self.assertNotIn('schema_version', report)
                self.assertEqual(coverage.finalize.call_count, 1)

    def test_source_mismatch_prevents_loading_or_execution(self):
        with patch.object(observer,'verify_source',side_effect=ValueError('synthetic-secret')), patch.object(observer,'load') as load:
            report=observer.run_observation(Path('/synthetic/baseline'),{'run':20,'attempt':1,'commit':'a'*40})
        load.assert_not_called()
        self.assertEqual(report['stage'],'source_identity'); self.assertFalse(report['qualifying'])
        self.assertTrue(report['privateOutputDeleted']); self.assertFalse(report['sourceRechecked'])

    def test_plan_mismatch_stops_before_original_finalize(self):
        coverage,_=self.fixture(Mock())
        coverage.classify.return_value={'different':True}
        report=observer.run_observation(Path('/synthetic/baseline'),{'run':20,'attempt':1,'commit':'a'*40})
        coverage.finalize.assert_not_called()
        self.assertEqual(report['stage'],'selection')
        self.assertFalse(report['planSelectionReproduced'])
        self.assertTrue(report['privateOutputDeleted'])

    def test_post_failure_integrity_is_checked_without_hiding_original_failure(self):
        coverage,_=self.fixture(Mock(side_effect=ValueError('synthetic-secret')))
        with patch.object(observer,'verify_source',side_effect=[None,ValueError('synthetic-secret')]):
            report=observer.run_observation(Path('/synthetic/baseline'),{'run':20,'attempt':1,'commit':'a'*40})
        self.assertEqual(report['result'],'rejected')
        self.assertEqual(report['stage'],'finalize_other')
        self.assertEqual(report['integrityFailures'],['source_identity'])
        self.assertFalse(report['sourceRechecked']);self.assertTrue(report['targetRechecked'])
        self.assertTrue(report['privateOutputDeleted'])

    def test_target_advancing_during_observation_cannot_be_reported_as_success(self):
        coverage,_=self.fixture(Mock())
        original=coverage.api.side_effect;run_reads=[]
        def api(path):
            value=original(path)
            if '/runs/' in path:
                run_reads.append(path)
                if len(run_reads)>1:return dict(value,run_attempt=3)
            return value
        coverage.api.side_effect=api
        report=observer.run_observation(Path('/synthetic/baseline'),{'run':20,'attempt':1,'commit':'a'*40})
        self.assertEqual(report['result'],'rejected')
        self.assertEqual(report['stage'],'target_identity')
        self.assertEqual(report['integrityFailures'],['target_identity'])
        self.assertFalse(report['targetRechecked']);self.assertTrue(report['privateOutputDeleted'])

    def test_create_event_and_observer_checkout_must_match(self):
        env={'GITHUB_REPOSITORY':observer.REPOSITORY,'GITHUB_EVENT_NAME':'create',
             'GITHUB_REF':'refs/heads/aggregate-observer/synthetic-once','GITHUB_SHA':'a'*40,
             'GITHUB_RUN_ID':'20','GITHUB_RUN_ATTEMPT':'1','GITHUB_EVENT_PATH':'/synthetic/event'}
        correct={'repository':{'full_name':observer.REPOSITORY,'private':False},
                 'ref_type':'branch','ref':'aggregate-observer/synthetic-once'}
        for event in (dict(correct,ref_type='tag'),dict(correct,ref='synthetic-secret'),
                      dict(correct,repository={'full_name':observer.REPOSITORY,'private':True})):
            with patch.object(Path,'read_text',return_value=json.dumps(event)),patch.object(observer,'git',return_value='a'*40):
                with self.assertRaises(ValueError):observer.observer_identity(env)
        with patch.object(Path,'read_text',return_value=json.dumps(correct)),patch.object(observer,'git',return_value='b'*40):
            with self.assertRaises(ValueError):observer.observer_identity(env)

    def test_closed_cli_refusal_never_echoes_unrecognized_stderr(self):
        for prefix in (b'', b'gh: '):
            self.assertEqual(observer.closed_capture_error(prefix + observer.ESCAPE_REFUSAL + b'\n'),
                             'escape_sequence_refused')
        for value in (b'synthetic-secret', b'HTTP 403 synthetic-secret',
                      observer.ESCAPE_REFUSAL + b' synthetic-secret\n', b'x'*1025, [], None):
            self.assertEqual(observer.closed_capture_error(value), 'unknown')

    def test_refusal_callback_retains_only_fixed_error(self):
        from types import SimpleNamespace
        root = Path('/synthetic/baseline'); record = observer.CaptureObserver(root)
        frame = SimpleNamespace(f_code=SimpleNamespace(co_filename=str(root/'.github/scripts/ci-nearby-native.py'), co_name='capture'))
        record.active = 'job_log_capture'
        value = ('nonzero', 1, b'synthetic-secret', observer.ESCAPE_REFUSAL + b'\n')
        record.observe(frame, 'return', value)
        self.assertEqual(record.last, {'fetch':'job_log_capture', 'status':'nonzero', 'error':'escape_sequence_refused'})
        self.assertNotIn('synthetic-secret', json.dumps(record.last))

    def test_cli_version_is_only_bounded_numeric_release(self):
        with patch.object(observer.subprocess, 'run', return_value=Mock(stdout=b'gh version 2.102.0 (date)\nsynthetic-secret\n')):
            self.assertEqual(observer.closed_cli_version(), '2.102.0')
        for value in (b'gh version synthetic-secret', b'gh version 1.2.3-synthetic-secret', b'x'*1025):
            with patch.object(observer.subprocess, 'run', return_value=Mock(stdout=value)):
                with self.assertRaises(ValueError): observer.closed_cli_version()

    def test_workflow_has_no_upload_no_matrix_and_fixed_baseline(self):
        text=SCRIPT.parents[1].joinpath('workflows/aggregate-observer.yml').read_text()
        self.assertNotIn('upload-artifact',text); self.assertNotIn('matrix:',text)
        self.assertNotIn('ci-required:',text); self.assertNotIn('workflow_dispatch:',text)
        self.assertIn('ref: '+observer.TARGET_COMMIT,text)
        self.assertIn('refs/heads/aggregate-observer/',text)
        self.assertIn('actions: read',text); self.assertNotIn(': write',text)


if __name__ == '__main__':
    unittest.main()
