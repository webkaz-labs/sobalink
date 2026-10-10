"""Synthetic GitHub API fixtures; no native execution or external state changes."""
import base64
import copy
import hashlib
import importlib.util
from pathlib import Path
import unittest

SPEC = importlib.util.spec_from_file_location('full_validation', Path(__file__).with_name('ci_full_validation.py'))
full = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(full)
ROOT = Path(__file__).resolve().parents[2]
SHA, TREE = 'a' * 40, 'b' * 40


def run(identifier=10, start='2026-01-01T00:00:00Z', event='workflow_dispatch', attempt=1):
    return dict(id=identifier, head_sha=SHA, head_branch='main', name='Cross-platform CI', path=full.WORKFLOW,
                event=event, run_attempt=attempt, status='completed', conclusion='success',
                created_at='2026-01-01T00:00:00Z', run_started_at=start,
                head_commit={'id': SHA, 'tree_id': TREE})


class FullProofTests(unittest.TestCase):
    def setUp(self):
        self.runs = [run()]
        self.previous = {}
        self.main = SHA
        self.repository = dict(full_name=full.REPOSITORY, private=False, default_branch="main")
        self.sources = {p: (ROOT / p).read_bytes() for p in full.SOURCES}
        self.required = full.required_jobs(self.sources)
        self.jobs = []
        self.calls = []
        for number, (name, steps) in enumerate(self.required.items(), 100):
            labels = [label for expected, label in full.TARGETS.values() if expected == name]
            self.jobs.append(dict(id=number, name=name, run_id=10, run_attempt=1, head_sha=SHA,
                                  labels=labels, status='completed', conclusion='success',
                                  started_at='2026-01-01T00:00:01Z', completed_at='2026-01-01T00:01:00Z',
                                  steps=[dict(name=s, status='completed', conclusion='success') for s in steps]))
        self.hook = None

    def api(self, path):
        self.calls.append(path)
        if self.hook:
            changed = self.hook(path)
            if changed is not None:
                return changed
        if not path:
            return self.repository
        if path == 'git/ref/heads/main':
            return {'object': {'sha': self.main}}
        if path == 'git/commits/' + SHA:
            return {'sha': SHA, 'tree': {'sha': TREE}}
        if path.startswith('contents/'):
            name = path[len('contents/'):].split('?')[0]
            data = self.sources[name]
            blob = hashlib.sha1(b'blob ' + str(len(data)).encode() + b'\0' + data).hexdigest()
            return dict(type='file', path=name, encoding='base64', content=base64.b64encode(data).decode(), size=len(data), sha=blob)
        if path.startswith('actions/workflows/'):
            return {'total_count': len(self.runs), 'workflow_runs': copy.deepcopy(self.runs)}
        if path.endswith('/jobs?per_page=100&page=1'):
            return {'total_count': len(self.jobs), 'jobs': copy.deepcopy(self.jobs)}
        if '/attempts/' in path:
            return copy.deepcopy(self.previous[path])
        if path.startswith('actions/runs/'):
            return copy.deepcopy(next(r for r in self.runs if r['id'] == int(path.rsplit('/', 1)[1])))
        self.fail('unexpected API route: ' + path)

    def audit(self, **kwargs):
        return full.audit(self.api, SHA, **kwargs)

    def test_full_proof_binds_source_jobs_and_policy(self):
        proof = self.audit(local_root=ROOT)
        self.assertEqual(proof['head_sha'], SHA)
        self.assertEqual(proof['head_tree'], TREE)
        self.assertEqual(set(proof['source_sha256']), set(full.SOURCES))
        self.assertEqual({j['name'] for j in proof['jobs']}, set(self.required))
        self.assertEqual(proof['full_validation']['id'], 10)
        self.assertNotIn('runner_name', str(proof))
        self.assertIn('.github/scripts/ci-go-test.py', proof['source_sha256'])
        self.assertIn('.github/scripts/ci_full_validation.py', proof['source_sha256'])
        for name, _ in full.TARGETS.values():
            result = next(j for j in proof['jobs'] if j['name'] == name)
            self.assertEqual(result['required_steps_passed'].count('Verify native remote resource inspection'), 1)
            self.assertEqual(result['required_steps_passed'].count('Verify native remote resource management'), 1)
            self.assertEqual(result['required_steps_passed'].count('Verify native fixed-group resource catalog'), 1)

    def test_native_resource_missing_duplicate_or_non_success_blocks_release(self):
        original = copy.deepcopy(self.jobs)
        for name, _ in full.TARGETS.values():
            for gate in ('Verify native remote resource inspection', 'Verify native remote resource management',
                         'Verify native fixed-group resource catalog'):
                for mode in ('missing', 'duplicate', 'skipped', 'failure', 'cancelled', None, 'pending'):
                    self.jobs = copy.deepcopy(original)
                    job = next(j for j in self.jobs if j['name'] == name)
                    step = next(s for s in job['steps'] if s['name'] == gate)
                    if mode == 'missing':
                        job['steps'].remove(step)
                    elif mode == 'duplicate':
                        job['steps'].append(copy.deepcopy(step))
                    elif mode == 'pending':
                        step['status'] = 'in_progress'
                    else:
                        step['conclusion'] = mode
                    with self.subTest(target=name, gate=gate, mode=mode), self.assertRaises(ValueError):
                        self.audit()

    def test_full_only_literal_policy_cannot_omit_or_reclassify_native_resource_gates(self):
        path = '.github/scripts/ci-coverage.py'
        original = self.sources[path]
        declaration = (b'FULL_ONLY_STEPS = (\n    "Verify native remote resource inspection",\n'
                       b'    "Verify native remote resource management",\n'
                       b'    "Verify native fixed-group resource catalog",\n)')
        self.assertEqual(original.count(declaration), 1)
        mutations = [
            original.replace(declaration, b''),
            original.replace(declaration, b'FULL_ONLY_STEPS = ()'),
            original.replace(declaration, b'FULL_ONLY_STEPS = ("Verify native remote resource inspection", "Verify native remote resource management")'),
            original.replace(declaration, b'FULL_ONLY_STEPS = ("Verify native fixed-group resource catalog",)'),
            original.replace(b'FAST_STEPS = (', b'FAST_STEPS = ("Verify native fixed-group resource catalog",'),
            original.replace(b'LONG_STEPS = (', b'LONG_STEPS = ("Verify native fixed-group resource catalog",'),
            original.replace(declaration, b'FULL_ONLY_STEPS = ("Other gate",)'),
            original.replace(declaration, b'FULL_ONLY_STEPS = ("Verify native remote resource inspection",)'),
            original.replace(declaration, b'FULL_ONLY_STEPS = ("Verify native remote resource management",)'),
            original.replace(declaration, b'FULL_ONLY_STEPS = ("Verify native remote resource management", "Verify native remote resource inspection")'),
            original.replace(declaration, b'FULL_ONLY_STEPS = ["Verify native remote resource inspection"]'),
            original.replace(declaration, b'FULL_ONLY_STEPS = tuple(["Verify native remote resource inspection"])'),
            original.replace(declaration, declaration + b'\n' + declaration),
            original.replace(declaration, b'FULL_ONLY_STEPS = ("Verify native remote resource inspection", "Extra gate")'),
            original.replace(b'FAST_STEPS = (', b'FAST_STEPS = ("Verify native remote resource inspection",'),
            original.replace(b'FAST_STEPS = (', b'FAST_STEPS = ("Verify native remote resource management",'),
            original.replace(b'LONG_STEPS = (', b'LONG_STEPS = ("Verify native remote resource management",'),
            original + b'\nFULL_ONLY_STEPS += ("Extra gate",)\n',
            original + b'\nif True:\n    FULL_ONLY_STEPS = ()\n',
        ]
        for index, source in enumerate(mutations):
            self.sources[path] = source
            with self.subTest(mutation=index), self.assertRaises(ValueError):
                full.required_jobs(self.sources)

    def test_management_command_and_exact_event_wrapper_are_bound_to_release_source(self):
        proof = self.audit()
        paths = (full.WORKFLOW, '.github/scripts/ci_full_validation.py', '.github/scripts/ci-go-test.py')
        for path in paths:
            self.assertEqual(proof['source_sha256'][path], hashlib.sha256(self.sources[path]).hexdigest())
        workflow = self.sources[full.WORKFLOW]
        marker = b'      - name: Verify native remote resource management\n'
        before, after = workflow.split(marker)
        self.sources[full.WORKFLOW] = before + marker + after.replace(b'"--exact",', b'', 1)
        with self.assertRaisesRegex(ValueError, 'native management execution policy'):
            self.audit()
        self.sources[full.WORKFLOW] = workflow
        for path in paths:
            original = self.sources[path]
            self.sources[path] = original + b'\n# synthetic changed protected source\n'
            with self.subTest(path=path), self.assertRaises(ValueError):
                self.audit(previous_proof=proof)
            self.sources[path] = original

    def test_fixed_group_command_and_exact_event_wrapper_are_bound_to_release_source(self):
        proof = self.audit()
        paths = (full.WORKFLOW, '.github/scripts/ci_full_validation.py', '.github/scripts/ci-go-test.py')
        for path in paths:
            self.assertEqual(proof['source_sha256'][path], hashlib.sha256(self.sources[path]).hexdigest())
        workflow = self.sources[full.WORKFLOW]
        marker = b'      - name: Verify native fixed-group resource catalog\n'
        before, after = workflow.split(marker)
        self.sources[full.WORKFLOW] = before + marker + after.replace(b'"--exact",', b'', 1)
        with self.assertRaisesRegex(ValueError, 'native fixed-group catalog execution policy'):
            self.audit()
        self.sources[full.WORKFLOW] = workflow
        for path in paths:
            original = self.sources[path]
            self.sources[path] = original + b'\n# synthetic changed protected source\n'
            with self.subTest(path=path), self.assertRaises(ValueError):
                self.audit(previous_proof=proof)
            self.sources[path] = original

    def test_effective_attempt_accepts_github_inherited_success_without_rerun(self):
        self.previous['actions/runs/10/attempts/1'] = run()
        self.runs = [run(start='2026-01-01T00:10:00Z', attempt=2)]
        for job in self.jobs:
            job['id'] += 1000
            job['run_attempt'] = 2
            # Original completion predates attempt 2; GitHub's effective records
            # still contain successful steps under new current-attempt IDs.
        proof = self.audit()
        self.assertTrue(all(j['effective_attempt'] == 2 for j in proof['jobs']))
        self.assertFalse(any('/attempts/1/jobs' in p for p in self.calls))

    def test_new_quick_success_preserves_exact_source_full_proof(self):
        original = self.audit()
        self.runs.append(run(11, '2026-01-01T00:10:00Z', 'push'))
        refreshed = self.audit(previous_proof=original)
        self.assertEqual(refreshed['full_validation']['id'], 10)
        self.assertEqual(refreshed['gate_proof_sha256'], full.digest(full.encoded(original)))

    def test_new_quick_failure_cancel_or_pending_blocks(self):
        for status, conclusion in [('completed', 'failure'), ('completed', 'cancelled'), ('in_progress', None), ('queued', None)]:
            newer = run(11, '2026-01-01T00:10:00Z', 'push')
            newer.update(status=status, conclusion=conclusion)
            self.runs = [run(), newer]
            with self.subTest(status=status, conclusion=conclusion), self.assertRaises(ValueError):
                self.audit()

    def test_new_full_failure_is_not_bypassed_by_older_success(self):
        for status, conclusion in [('completed', 'failure'), ('completed', 'cancelled'), ('in_progress', None)]:
            newer = run(11, '2026-01-01T00:10:00Z')
            newer.update(status=status, conclusion=conclusion)
            self.runs = [run(), newer]
            with self.subTest(status=status), self.assertRaises(ValueError):
                self.audit()

    def test_rerun_of_lower_id_can_be_the_latest_failed_full_intent(self):
        self.previous['actions/runs/9/attempts/1'] = run(9)
        newer = run(9, '2026-01-01T00:20:00Z', attempt=2)
        newer['conclusion'] = 'failure'
        self.runs = [run(10, '2026-01-01T00:10:00Z'), newer]
        with self.assertRaisesRegex(ValueError, 'latest full'):
            self.audit()

    def test_new_full_success_can_supersede_old_failure_and_old_gate_proof(self):
        proof = self.audit()
        self.runs[0]['conclusion'] = 'failure'
        self.runs.append(run(11, '2026-01-01T00:10:00Z'))
        for job in self.jobs:
            job['run_id'] = 11
        result = self.audit(previous_proof=proof)
        self.assertEqual(result['full_validation']['id'], 11)

    def test_schedule_is_canonical_but_never_manual_release_proof(self):
        self.runs = [run(event="schedule")]
        with self.assertRaisesRegex(ValueError, "force_full"):
            self.audit()
        self.runs = [run(), run(11, "2026-01-01T00:10:00Z", event="schedule")]
        self.assertEqual(self.audit()["full_validation"]["id"], 10)
        for status, conclusion in (("queued", None), ("in_progress", None),
                                   ("completed", "failure"), ("completed", "cancelled")):
            self.runs[1].update(status=status, conclusion=conclusion)
            with self.subTest(status=status, conclusion=conclusion), self.assertRaises(ValueError):
                self.audit()
        self.runs = [run(9, event="schedule"), run(10, "2026-01-01T00:10:00Z")]
        self.runs[0]["conclusion"] = "failure"
        self.assertEqual(self.audit()["full_validation"]["id"], 10)

    def test_push_only_success_has_no_explicit_full_intent(self):
        self.runs[0]['event'] = 'push'
        with self.assertRaisesRegex(ValueError, 'force_full'):
            self.audit()

    def test_skipped_short_or_missing_os_and_steps_are_never_full(self):
        original = copy.deepcopy(self.jobs)
        for index, job in enumerate(original):
            for bad in ('skipped', 'failure', 'cancelled', None):
                self.jobs = copy.deepcopy(original)
                self.jobs[index]['conclusion'] = bad
                with self.subTest(job=job['name'], bad=bad), self.assertRaises(ValueError):
                    self.audit()
            self.jobs = copy.deepcopy(original)
            self.jobs.pop(index)
            with self.subTest(missing=job['name']), self.assertRaises(ValueError):
                self.audit()
            for step in range(len(job['steps'])):
                self.jobs = copy.deepcopy(original)
                self.jobs[index]['steps'][step]['conclusion'] = 'skipped'
                with self.subTest(job=job['name'], step=step), self.assertRaises(ValueError):
                    self.audit()
        self.jobs = original

    def test_duplicate_jobs_steps_wrong_source_attempt_or_runner_rejected(self):
        mutations = [lambda j: j.append(copy.deepcopy(j[0])),
                     lambda j: j[0]['steps'].append(copy.deepcopy(j[0]['steps'][0])),
                     lambda j: j[0].update(head_sha='c' * 40),
                     lambda j: j[0].update(run_attempt=2),
                     lambda j: j[0].update(run_id=11),
                     lambda j: j[0].update(labels=['wrong-target'])]
        original = copy.deepcopy(self.jobs)
        for change in mutations:
            self.jobs = copy.deepcopy(original)
            change(self.jobs)
            with self.assertRaises(ValueError):
                self.audit()

    def test_missing_latest_effective_jobs_never_imports_past_attempt(self):
        self.previous['actions/runs/10/attempts/1'] = run()
        self.runs[0] = run(start='2026-01-01T00:10:00Z', attempt=2)
        self.jobs = []
        with self.assertRaises(ValueError):
            self.audit()
        self.assertFalse(any('/attempts/1/jobs' in p for p in self.calls))

    def test_source_blob_hash_tree_and_stored_proof_mismatch_rejected(self):
        proof = self.audit()
        for key, value in [('schema', 0), ('head_sha', 'c' * 40), ('head_tree', 'c' * 40),
                           ('repository', 'synthetic/other'), ('source_sha256', {})]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.audit(previous_proof=dict(proof, **{key: value}))
        self.sources[full.WORKFLOW] += b'\n# synthetic uncommitted change\n'
        with self.assertRaisesRegex(ValueError, 'checkout policy'):
            self.audit(local_root=ROOT)
        self.sources[full.WORKFLOW] = (ROOT / full.WORKFLOW).read_bytes()
        self.runs[0]['head_commit']['tree_id'] = 'c' * 40
        with self.assertRaisesRegex(ValueError, 'tree mismatch'):
            self.audit()

    def test_main_or_attempt_changes_during_read_are_rejected(self):
        def moved(path):
            if path.endswith('/jobs?per_page=100&page=1'):
                self.main = 'c' * 40
        self.hook = moved
        with self.assertRaisesRegex(ValueError, 'main moved'):
            self.audit()
        self.main = SHA
        def rerun(path):
            if path.endswith('/jobs?per_page=100&page=1'):
                self.runs[0]['status'] = 'in_progress'
        self.hook = rerun
        with self.assertRaises(ValueError):
            self.audit()

    def test_pagination_is_complete_unique_and_bounded(self):
        values = [dict(id=i) for i in range(1, 102)]
        def api(path):
            page = int(path.rsplit('=', 1)[1])
            return {'total_count': 101, 'jobs': values[(page - 1) * 100:page * 100]}
        self.assertEqual(len(full.pages(api, 'jobs', 'jobs')), 101)
        for data in ({'total_count': 2, 'jobs': [dict(id=1)]},
                     {'total_count': 2, 'jobs': [dict(id=1), dict(id=1)]},
                     {'total_count': True, 'jobs': []}):
            with self.assertRaises(ValueError):
                full.pages(lambda _: data, 'jobs', 'jobs')

    def test_publication_repository_identity_is_rechecked(self):
        for key, value in [('private', True), ('default_branch', 'other'), ('full_name', 'synthetic/other')]:
            original = dict(self.repository)
            self.repository[key] = value
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, 'repository changed'):
                self.audit()
            self.repository = original

    def test_source_policy_api_mismatch_and_incomplete_metadata_are_rejected(self):
        original = self.api
        for replacement in ({'sha': '0' * 40}, {'size': 1}, {'encoding': 'none'}):
            def changed(path, value=replacement):
                if path.startswith('contents/'):
                    self.hook = None
                    data = original(path)
                    self.hook = changed
                    return dict(data, **value)
            self.hook = changed
            with self.subTest(replacement=replacement), self.assertRaises(ValueError):
                self.audit()
        self.hook = None

    def test_workflow_wires_both_audits_and_keeps_proof_out_of_release_assets(self):
        workflow = (ROOT / '.github/workflows/prerelease.yml').read_text(encoding='utf-8')
        gate = workflow.split('  gate:\n')[1].split('  native:\n')[0]
        publish = workflow.split('  publish:\n')[1].split('  verify-published:\n')[0]
        self.assertIn('.github/scripts/ci_full_validation.py', gate)
        self.assertIn('gate --proof "$RUNNER_TEMP/ci-full-proof.json"', gate)
        self.assertIn('name: ci-full-validation-proof', gate)
        self.assertIn('contents: write\n      actions: read', publish)
        self.assertIn('name: ci-full-validation-proof', publish)
        self.assertIn('publish-ready --proof proof/ci-full-proof.json --revalidated-proof proof/publication-full-proof.json', publish)
        self.assertLess(publish.index('publish-ready --proof'), publish.index('--draft=false'))
        self.assertIn('name: publication-ci-full-validation-proof', publish)
        self.assertNotIn('id-token: write', publish)
        self.assertNotIn('schedule:', workflow)

    def test_invalid_or_ambiguous_rerun_time_fails_closed(self):
        self.previous['actions/runs/10/attempts/1'] = run()
        self.runs[0]['run_attempt'] = 2
        with self.assertRaisesRegex(ValueError, 'chronology'):
            self.audit()
        self.runs = [run(), run(11)]
        with self.assertRaisesRegex(ValueError, 'ambiguous latest'):
            self.audit()


class ParallelWorkflowPolicyTests(unittest.TestCase):
    def setUp(self):
        self.sources = {p: (ROOT / p).read_bytes() for p in full.SOURCES}
        self.workflow = self.sources[full.WORKFLOW].decode('utf-8')

    def test_literal_gate_names_and_join_remain_required(self):
        required = full.required_jobs(self.sources)
        gates = (
            'Verify direct LAN session natural rekey and idle lifecycle',
            'Verify guarded relay real-time lease continuity',
            'Verify relay-only real-time lease and idle continuity',
            'Wait for real-time lifecycle and lease checks',
        )
        for name, _ in full.TARGETS.values():
            for gate in gates:
                self.assertEqual(required[name].count(gate), 1)

    def test_source_preflight_rejects_missing_or_weakened_join(self):
        fence = ('      - name: Wait for real-time lifecycle and lease checks\n'
                 '        wait: [natural-lifecycle, guarded-lease, relay-only-lease]\n')
        mutations = [
            self.workflow.replace(fence, ''),
            self.workflow.replace('wait: [natural-lifecycle, guarded-lease, relay-only-lease]',
                                  'run: echo done'),
            self.workflow.replace('wait: [natural-lifecycle, guarded-lease, relay-only-lease]',
                                  'wait: [natural-lifecycle, guarded-lease]'),
            self.workflow.replace(fence, fence + '        continue-on-error: true\n'),
            self.workflow.replace(fence, fence + '        if: always()\n'),
            self.workflow.replace(fence, '      - name: Cancel lease\n        cancel: guarded-lease\n' + fence),
            self.workflow.replace('        background: true\n', '', 1),
            self.workflow.replace('        background: true\n', '        background: true\n        continue-on-error: true\n', 1),
            self.workflow.replace('id: guarded-lease', 'id: natural-lifecycle'),
            self.workflow.replace('      - name: Verify guarded relay real-time lease continuity\n',
                                  '      - name: Shared writer\n        run: echo unsafe\n      - name: Verify guarded relay real-time lease continuity\n'),
            self.workflow.replace('--suite natural-lifecycle --', '--suite other-suite --'),
        ]
        for i, workflow in enumerate(mutations):
            with self.subTest(mutation=i), self.assertRaises(ValueError):
                full.validate_parallel_workflow(workflow.encode('utf-8'))

    def test_source_preflight_rejects_alternate_yaml_and_duplicate_keys(self):
        first = '      - name: Verify direct LAN session natural rekey and idle lifecycle\n'
        mutations = [
            self.workflow + "\n  extra:\n    steps:\n      - name: Extra\n        'background': true\n        run: echo extra\n",
            self.workflow + "\n  extra:\n    steps:\n      - {name: Extra, background: true, run: echo extra}\n",
            self.workflow.replace('        id: natural-lifecycle\n',
                                  '        id: natural-lifecycle\n        id: replacement\n'),
            self.workflow.replace('      - name: Verify guarded relay real-time lease continuity\n',
                                  "        'continue-on-error': true\n      - name: Verify guarded relay real-time lease continuity\n"),
            self.workflow.replace('      - name: Verify guarded relay real-time lease continuity\n',
                                  '        run: echo override\n      - name: Verify guarded relay real-time lease continuity\n'),
            self.workflow.replace(first, first + '        ? background\n        : true\n'),
            self.workflow.replace(first, first + '        "backgrou\\u006ed": true\n'),
            self.workflow.replace(first, first + '        <<: *unsafe\n'),
            self.workflow.replace(first, first + '        env: &unsafe {}\n'),
            self.workflow.replace(first, first + '        env: {IGNORED: value}\n'),
        ]
        for i, workflow in enumerate(mutations):
            with self.subTest(mutation=i), self.assertRaises(ValueError):
                full.validate_parallel_workflow(workflow.encode('utf-8'))

    def test_packaging_and_cache_writes_follow_the_join(self):
        boundary = self.workflow.index('      - name: Wait for real-time lifecycle and lease checks\n')
        for name in ('Build native package and smoke archive contents',
                     'Save Go caches after all native checks pass on main',
                     'Save development Go caches after all native checks pass',
                     'Summarize measured native suites', 'Preserve native suite timings'):
            self.assertGreater(self.workflow.index('      - name: ' + name + '\n'), boundary)
        self.assertGreater(self.workflow.index('          name: package-${{ matrix.goos }}-${{ matrix.goarch }}'), boundary)


if __name__ == '__main__':
    unittest.main()
