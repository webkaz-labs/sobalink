"""Synthetic GitHub API fixtures; no native execution or external state changes."""
import base64
import copy
import hashlib
import importlib.util
from pathlib import Path
import unittest
import test_ci_dnssd_required as dnssd_fixture
import test_ci_native_core_required as core_fixture
from test_ci_upload_artifact import upload_log

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
                                  steps=[dict(name=s, status='completed', conclusion='success', started_at='2026-01-01T00:00:30Z' if s == dnssd_fixture.gate.STEPS[1] else '2026-01-01T00:00:05Z', completed_at='2026-01-01T00:00:50Z' if s == dnssd_fixture.gate.STEPS[1] else '2026-01-01T00:00:29Z') for s in steps]))
        for target in core_fixture.gate.TARGETS:
            native = next(j for j in self.jobs if j['name'] == full.TARGETS[target][0])
            phases = core_fixture.job(target)['steps']
            for step in native['steps']:
                match = next((phase for phase in phases if phase['name'] == step['name']), None)
                if match:
                    step.update(match)
        self.core_artifacts = {target: core_fixture.artifact(target, identifier=301+i)
                               for i, target in enumerate(core_fixture.gate.TARGETS)}
        self.hook = None
        self.producing_jobs = copy.deepcopy(self.jobs)
        self.artifact, self.archive = dnssd_fixture.artifact()
        self.refresh_job_logs()

    def refresh_job_logs(self, run_id=10):
        self.job_logs = {}
        for target, (meta, _) in self.core_artifacts.items():
            producer = next(j for j in self.jobs if j['name'] == full.TARGETS[target][0])
            self.job_logs[producer['id']] = upload_log(meta, run_id=run_id)
            if target == 'darwin-arm64':
                self.job_logs[producer['id']] += upload_log(self.artifact, run_id=run_id)

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
        for meta, _ in self.core_artifacts.values():
            if path == 'actions/artifacts/' + str(meta['id']):
                return copy.deepcopy(meta)
        if path == 'actions/artifacts/300':
            return copy.deepcopy(self.artifact)
        if path.endswith('/artifacts?per_page=100&page=1'):
            return {'total_count': 3, 'artifacts': [copy.deepcopy(self.artifact), *[copy.deepcopy(meta) for meta, _ in self.core_artifacts.values()]]}
        if path.endswith('/jobs?per_page=100&page=1'):
            selected = self.producing_jobs if '/attempts/1/' in path and self.runs[0]['run_attempt'] > 1 else self.jobs
            return {'total_count': len(selected), 'jobs': copy.deepcopy(selected)}
        if '/attempts/' in path:
            return copy.deepcopy(self.previous[path])
        if path.startswith('actions/runs/'):
            return copy.deepcopy(next(r for r in self.runs if r['id'] == int(path.rsplit('/', 1)[1])))
        self.fail('unexpected API route: ' + path)

    def audit(self, **kwargs):
        return full.audit(self.api, SHA, artifact_download=lambda identifier: self.archive if identifier == 300 else next(data for meta, data in self.core_artifacts.values() if meta['id'] == identifier), job_log=lambda identifier:self.job_logs[identifier], **kwargs)

    def test_shared_upload_helper_is_source_bound_and_packaged_for_publication(self):
        helper = '.github/scripts/ci-upload-artifact.py'
        tests = '.github/scripts/test_ci_upload_artifact.py'
        for inventory in (full.SOURCES, full.DNSSD_SOURCES, full.CORE_SOURCES,
                          dnssd_fixture.gate.POLICY_FILES, core_fixture.gate.POLICY_FILES):
            self.assertIn(helper, inventory)
            self.assertIn(tests, inventory)
        prerelease = (ROOT / '.github/workflows/prerelease.yml').read_text(encoding='utf-8')
        self.assertEqual(prerelease.count('            ' + helper + '\n'), 1)
        self.sources[helper] += b'\n# synthetic source mutation\n'
        with self.assertRaisesRegex(ValueError, 'loaded release tool differs'):
            self.audit()

    def test_native_package_gate_and_receipt_require_unique_actual_success(self):
        steps = core_fixture.gate.STEPS
        self.assertIn(full.WINDOWS_PACKAGES_HELPER, full.SOURCES)
        self.assertIn('.github/scripts/ci-metrics.py', full.SOURCES)
        original = copy.deepcopy(self.jobs)
        for gate in steps:
            for target, (name, _) in full.TARGETS.items():
                self.assertEqual(gate in self.required[name], target in core_fixture.gate.TARGETS)
            for mode in ('missing', 'duplicate', 'skipped', 'failure', 'cancelled', None, 'pending', 'wrong-target'):
                self.jobs = copy.deepcopy(original)
                job = next(j for j in self.jobs if j['name'] == full.TARGETS['windows-amd64'][0])
                record = next(s for s in job['steps'] if s['name'] == gate)
                if mode in ('missing', 'wrong-target'):
                    job['steps'].remove(record)
                    if mode == 'wrong-target':
                        next(j for j in self.jobs if j['name'] == full.TARGETS['linux-amd64'][0])['steps'].append(record)
                elif mode == 'duplicate':
                    job['steps'].append(copy.deepcopy(record))
                elif mode == 'pending':
                    record['status'] = 'in_progress'
                else:
                    record['conclusion'] = mode
                with self.subTest(gate=gate, mode=mode), self.assertRaises(ValueError):
                    self.audit()

    def test_native_literal_policy_preserves_complement_vet_and_failure_handling(self):
        for path in (full.WORKFLOW, '.github/workflows/prerelease.yml'):
            original = self.sources[path]
            for old, new in ((b"if: matrix.goos == 'windows' || matrix.goos == 'darwin'", b'if: false'),
                    (b"== 'linux'", b"== 'windows'"),
                    (b'ci-native-core-required.py core', b'ci-native-core.py'),
                    (b'ci-native-core-required.py remaining', b'ci-native-core-required.py core'),
                    (b'run: python -B .github/scripts/ci-native-core-required.py core',
                     b'continue-on-error: true\n        run: python -B .github/scripts/ci-native-core-required.py core'),
                    (b'-race -count=1 -timeout=10m ./...', b'-race -count=1 -timeout=20m ./...'),
                    (b"steps.native-core.outputs.remaining_safe == 'true'", b'true'),
                    (b'soba-native-core-required/receipt.json', b'soba-native-core-required/core.json'),
                    (b'-timeout=10m ./...', b'-timeout=10m ./... || true')):
                self.assertIn(old, original)
                self.sources[path] = original.replace(old, new, 1)
                with self.subTest(path=path, old=old), self.assertRaises(ValueError):
                    self.audit()
            self.sources[path] = original
        original = self.sources[full.WORKFLOW]
        self.sources[full.WORKFLOW] = original.replace(b'-- go vet ./...', b'-- go vet ./internal/core')
        with self.assertRaises(ValueError): self.audit()
        self.sources[full.WORKFLOW] = original

    def test_failure_context_upload_is_exact_separate_and_not_a_required_success_step(self):
        self.assertTrue(all('Preserve bounded Core failure context' not in names for names in self.required.values()))
        for path in (full.WORKFLOW,'.github/workflows/prerelease.yml'):
            original=self.sources[path]
            marker=b'      - name: Preserve bounded Core failure context\n'
            start=original.index(marker);end=original.index(b'      - ',start+len(marker))
            block=original[start:end]
            mutations=[original.replace(block,b'',1),original.replace(block,block+block,1)]
            for old,new in [(b"steps.native-core.outputs.failure_ready == 'true'",b'true'),
                (b'soba-native-core-failure-',b'soba-native-core-required-'),
                (b'soba-native-core-failure/failure.json',b'soba-native-core-required/core.json'),
                (b'soba-native-core-failure/partial-core.json',b'soba-native-core-failure/**'),
                (b'if-no-files-found: error',b'if-no-files-found: warn')]:
                mutations.append(original[:start]+block.replace(old,new)+original[end:])
            for changed in mutations:
                self.sources[path]=changed
                with self.assertRaises(ValueError):full.required_jobs(self.sources)
            self.sources[path]=original

    def test_early_core_placement_rejects_inserted_work_or_cache_before_collection(self):
        for path in (full.WORKFLOW, '.github/workflows/prerelease.yml'):
            original = self.sources[path]
            marker = b'      - name: Collect required whole-Core race evidence\n'
            for inserted in (b'      - name: Unrelated workload\n        run: echo synthetic\n',
                             b'      - uses: actions/cache/restore@synthetic\n'):
                self.sources[path] = original.replace(marker, inserted + marker, 1)
                with self.subTest(path=path, inserted=inserted), self.assertRaises(ValueError):
                    full.required_jobs(self.sources)
            self.sources[path] = original

    def test_windows_helper_policy_rejects_altered_flags_missing_phase_or_union(self):
        original = self.sources[full.WINDOWS_PACKAGES_HELPER]
        for old, new in ((b"'-race', ", b''), (b"'-count=1'", b"'-count=2'"),
                         (b"'-timeout=10m'", b"'-timeout=20m'"),
                         (b"GROUPS = ('core', 'remaining')", b"GROUPS = ('core',)"),
                         (b'validate_partition(inventory, groups)', b'no_partition(inventory, groups)'),
                         (b"['go', 'list', '-race', './...']", b"['go', 'list', '-race', './internal/core']"),
                         (b'source_identity() == source', b'True')):
            self.assertIn(old, original)
            self.sources[full.WINDOWS_PACKAGES_HELPER] = original.replace(old, new, 1)
            with self.subTest(change=old), self.assertRaises(ValueError):
                self.audit()
        self.sources[full.WINDOWS_PACKAGES_HELPER] = original
        self.sources['.github/scripts/ci-coverage.py'] = self.sources['.github/scripts/ci-coverage.py'].replace(
            b"'Collect required whole-Core race evidence', 'Verify remaining native package race coverage', 'Preserve required native package proof'",
            b"'Collect required whole-Core race evidence'")
        with self.assertRaises(ValueError):
            self.audit()

    def test_linux_nearby_exact_step_source_and_target_are_required(self):
        step = 'Verify Linux nearby native discovery without skips'
        self.assertIn('.github/scripts/ci-nearby-native.py', full.SOURCES)
        self.assertIn('.github/scripts/ci-nearby-required.py', full.SOURCES)
        for target, (name, _) in full.TARGETS.items():
            self.assertEqual(step in self.required[name], target in ('linux-amd64', 'linux-arm64'))
        original = copy.deepcopy(self.jobs)
        for target in ('linux-amd64', 'linux-arm64'):
            for mode in ('missing', 'duplicate', 'skipped', 'failure', 'cancelled', None, 'pending'):
                self.jobs = copy.deepcopy(original)
                job = next(j for j in self.jobs if j['name'] == full.TARGETS[target][0])
                record = next(s for s in job['steps'] if s['name'] == step)
                if mode == 'missing':
                    job['steps'].remove(record)
                elif mode == 'duplicate':
                    job['steps'].append(copy.deepcopy(record))
                elif mode == 'pending':
                    record['status'] = 'in_progress'
                else:
                    record['conclusion'] = mode
                with self.subTest(target=target, mode=mode), self.assertRaises(ValueError):
                    self.audit()

    def test_macos_nearby_exact_step_and_target_are_required(self):
        step = 'Verify macOS five-scope discovery without skips'
        for target, (name, _) in full.TARGETS.items():
            self.assertEqual(step in self.required[name], target == 'darwin-arm64')
        original = copy.deepcopy(self.jobs)
        for mode in ('missing', 'duplicate', 'skipped', 'failure', 'cancelled', None, 'pending', 'wrong-target'):
            self.jobs = copy.deepcopy(original)
            job = next(j for j in self.jobs if j['name'] == full.TARGETS['darwin-arm64'][0])
            record = next(s for s in job['steps'] if s['name'] == step)
            if mode in ('missing', 'wrong-target'):
                job['steps'].remove(record)
                if mode == 'wrong-target':
                    next(j for j in self.jobs if j['name'] == full.TARGETS['linux-amd64'][0])['steps'].append(record)
            elif mode == 'duplicate':
                job['steps'].append(copy.deepcopy(record))
            elif mode == 'pending':
                record['status'] = 'in_progress'
            else:
                record['conclusion'] = mode
            with self.subTest(mode=mode), self.assertRaises(ValueError):
                self.audit()

    def test_macos_nearby_literal_admission_rejects_weakened_commands_and_scope(self):
        original = self.sources[full.WORKFLOW]
        marker = b'      - name: Verify macOS five-scope discovery without skips\n'
        start = original.index(marker)
        end = original.index(b'      - ', start + len(marker))
        block = original[start:end]
        changes = ((b"if: matrix.goos == 'darwin'", b'if: false'),
                   (b"if: matrix.goos == 'darwin'", b"if: matrix.goos == 'linux'"),
                   (b"if: matrix.goos == 'darwin'", b"if: always() && matrix.goos == 'darwin'"),
                   (b'timeout-minutes: 10', b'timeout-minutes: 60'),
                   (b"CGO_ENABLED: '1'", b"CGO_ENABLED: '0'"),
                   (b'ci-dnssd-required.py', b'ci-dnssd-diagnostic.py'),
                   (b'ci-dnssd-required.py\n', b'ci-dnssd-required.py || true\n'),
                   (marker, marker + b'        continue-on-error: true\n'),
                   (b'        env:\n', b'        env:\n          SOBALINK_DNSSD_RAW_RECEIVE: \'1\'\n'))
        mutations = [original.replace(block, b'', 1), original.replace(block, block + block, 1),
                     original.replace(block, block + b'        continue-on-error: true\n', 1),
                     original.replace(b'env:\n', b'env:\n  SOBALINK_DNSSD_SELECTED_INTERFACE: \'1\'\n', 1)]
        for before, after in changes:
            self.assertIn(before, block)
            mutations.append(original.replace(block, block.replace(before, after), 1))
        moved = original.replace(block, b'', 1)
        insert = moved.index(b'      - ', moved.index(b'  browser:\n'))
        mutations.append(moved[:insert] + block + moved[insert:])
        for index, value in enumerate(mutations):
            with self.subTest(mutation=index), self.assertRaises(ValueError):
                full.validate_macos_nearby_workflow(value)

    def test_macos_nearby_runs_once_immediately_after_exact_preparation(self):
        workflow = self.sources[full.WORKFLOW]
        full.validate_macos_nearby_workflow(workflow)
        preparation = workflow.index(b'      - name: Check exact toolchain and native target\n')
        execution = workflow.index(b'      - name: Verify macOS five-scope discovery without skips\n')
        upload = workflow.index(b'      - name: Preserve required macOS discovery proof\n')
        remaining = workflow.index(b'      - name: Verify remaining native package race coverage\n')
        activation = workflow.index(b'      - name: Verify managed activation acceptance\n')
        self.assertLess(preparation, execution)
        self.assertLess(execution, upload)
        self.assertLess(upload, remaining)
        self.assertLess(remaining, activation)
        self.assertTrue(workflow[:execution].endswith(
            b"          python -m unittest discover -s .github/scripts -p 'test_*.py' -v\n"))
        self.assertEqual(workflow.count(b'-- python -B .github/scripts/ci-dnssd-required.py\n'), 1)

    def test_macos_nearby_early_receipt_cannot_mask_later_activation_failure(self):
        native = next(job for job in self.jobs if job['name'] == full.TARGETS['darwin-arm64'][0])
        self.assertTrue(all(step['conclusion'] == 'success' for step in native['steps']
                            if step['name'] in dnssd_fixture.gate.STEPS))
        activation = next(step for step in native['steps'] if step['name'] == 'Verify managed activation acceptance')
        activation['conclusion'] = 'failure'
        for conclusion in ('success', 'failure'):
            native['conclusion'] = conclusion
            with self.subTest(job_conclusion=conclusion), self.assertRaises(ValueError):
                self.audit()

    def test_macos_nearby_order_rejects_unprepared_delayed_or_interrupted_execution(self):
        original = self.sources[full.WORKFLOW]
        preparation = original.index(b'      - name: Check exact toolchain and native target\n')
        execution = original.index(b'      - name: Verify macOS five-scope discovery without skips\n')
        end = original.index(b'      - name: Verify Windows receive-retirement directory barriers\n')
        for start, targets in ((execution, ('Check exact toolchain and native target',
                                          'Verify remaining native package race coverage',
                                          'Verify direct LAN synthetic expiry and rekey')),
                               (preparation, ('Test and reproduce locked frontend assets',
                                              'Local and mock tests, race detector, and vet'))):
            block = original[start:end]
            stripped = original[:start] + original[end:]
            for target in targets:
                insertion = stripped.index(('      - name: ' + target + '\n').encode())
                moved = stripped[:insertion] + block + stripped[insertion:]
                with self.subTest(start=start, target=target), self.assertRaises(ValueError):
                    full.validate_macos_nearby_workflow(moved)
        inserted = original[:execution] + b'      - name: Unrelated workload\n        run: echo synthetic\n' + original[execution:]
        with self.assertRaises(ValueError):
            full.validate_macos_nearby_workflow(inserted)

    def test_macos_nearby_preparation_cannot_skip_failures_or_weaken_policy(self):
        original = self.sources[full.WORKFLOW]
        start = original.index(b'      - name: Check exact toolchain and native target\n')
        end = original.index(b'      - name: Verify macOS five-scope discovery without skips\n')
        block = original[start:end]
        for old, new in ((b'        run: |\n', b'        if: false\n        run: |\n'),
                         (b'        run: |\n', b'        continue-on-error: true\n        run: |\n'),
                         (b'          set -euo pipefail\n', b''),
                         (b' = go1.27.1', b' = go1.27.0'),
                         (b" = '${{ matrix.goos }}'", b" = 'darwin'"),
                         (b" = '${{ matrix.goarch }}'", b" = 'arm64'"),
                         (b'          go run ./cmd/prepare-engine\n', b''),
                         (b'          go run ./cmd/prepare-engine --verify\n', b''),
                         (b'          go mod verify\n', b'          go mod verify || true\n'),
                         (b"-p 'test_*.py'", b"-p 'test_ci_coverage.py'")):
            self.assertIn(old, block)
            changed = original[:start] + block.replace(old, new, 1) + original[end:]
            with self.subTest(change=old), self.assertRaises(ValueError):
                full.validate_macos_nearby_workflow(changed)

    def test_macos_nearby_literal_coverage_policy_cannot_reclassify_the_gate(self):
        path = '.github/scripts/ci-coverage.py'
        original = self.sources[path]
        target = b"MACOS_NEARBY_TARGETS = ('darwin-arm64',)"
        steps = b"MACOS_NEARBY_STEPS = ('Verify macOS five-scope discovery without skips', 'Preserve required macOS discovery proof')"
        mutations = [original.replace(target, b''), original.replace(steps, b''),
                     original.replace(target, b"MACOS_NEARBY_TARGETS = ('linux-amd64',)"),
                     original.replace(target, b"MACOS_NEARBY_TARGETS = ('darwin-arm64', 'windows-amd64')"),
                     original.replace(steps, b"MACOS_NEARBY_STEPS = ('Other gate',)"),
                     original.replace(steps, b'MACOS_NEARBY_STEPS = ()'),
                     original.replace(steps, steps + b'\n' + steps),
                     original.replace(b'FAST_STEPS = (', b'FAST_STEPS = ("Verify macOS five-scope discovery without skips",'),
                     original + b'\nMACOS_NEARBY_STEPS += ("Other gate",)\n']
        for index, source in enumerate(mutations):
            sources = dict(self.sources, **{path: source})
            with self.subTest(mutation=index), self.assertRaises(ValueError):
                full.required_jobs(sources)

    def test_macos_nearby_wrapper_and_capability_sources_are_bound_to_proof(self):
        proof = self.audit()
        paths = (full.WORKFLOW, '.github/scripts/ci_full_validation.py', '.github/scripts/ci-coverage.py',
                 '.github/scripts/ci-go-test.py', 'internal/nearby/dnssd_native_acceptance_test.go',
                 'internal/nearby/dnssd_capability_test.go')
        for path in paths:
            self.assertIn(path, full.SOURCES)
            self.assertEqual(proof['source_sha256'][path], hashlib.sha256(self.sources[path]).hexdigest())
            original = self.sources[path]
            self.sources[path] = original + b'\n// synthetic changed protected source\n'
            with self.subTest(path=path), self.assertRaises(ValueError):
                self.audit(previous_proof=proof)
            with self.subTest(path=path, checkout=True), self.assertRaises(ValueError):
                self.audit(local_root=ROOT)
            self.sources[path] = original

    def test_linux_nearby_literal_admission_rejects_weakened_commands_and_scope(self):
        original = self.sources[full.WORKFLOW]
        marker = b'      - name: Verify Linux nearby native discovery without skips\n'
        start = original.index(marker)
        end = original.index(b'      - ', start + len(marker))
        block = original[start:end]
        changes = ((b"if: matrix.goos == 'linux'", b'if: false'),
                   (b"if: matrix.goos == 'linux'", b"if: matrix.goos == 'linux' && matrix.goarch == 'arm64'"),
                   (b'timeout-minutes: 8', b'timeout-minutes: 80'),
                   (b'ci-nearby-required.py', b'ci-nearby-native.py'),
                   (b'SOBALINK_NEARBY_NATIVE_PLATFORM:', b'OTHER_PLATFORM:'),
                   (b'python -B ', b'python '),
                   (marker, marker + b'        continue-on-error: true\n'))
        mutations = [original.replace(block, b'', 1), original.replace(block, block + block, 1)]
        for before, after in changes:
            self.assertIn(before, block)
            mutations.append(original.replace(block, block.replace(before, after), 1))
        moved = original.replace(block, b'', 1)
        browser = moved.index(b'  browser:\n')
        insert = moved.index(b'      - ', browser)
        mutations.append(moved[:insert] + block + moved[insert:])
        for index, value in enumerate(mutations):
            with self.subTest(mutation=index), self.assertRaises(ValueError):
                full.validate_linux_nearby_workflow(value)
        path = '.github/scripts/ci-coverage.py'
        for before, after in ((b"LINUX_NEARBY_TARGETS = ('linux-amd64', 'linux-arm64')", b"LINUX_NEARBY_TARGETS = ('darwin-arm64',)"),
                              (b"LINUX_NEARBY_STEPS = ('Verify Linux nearby native discovery without skips',)", b"LINUX_NEARBY_STEPS = ('Other gate',)")):
            sources = dict(self.sources)
            self.assertIn(before, sources[path])
            sources[path] = sources[path].replace(before, after)
            with self.assertRaises(ValueError):
                full.required_jobs(sources)

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
        self.assertIn('.github/scripts/ci-browser-summary.py', proof['source_sha256'])
        self.assertIn('.github/scripts/ci-nearby-diagnostic.py', proof['source_sha256'])
        for name, _ in full.TARGETS.values():
            result = next(j for j in proof['jobs'] if j['name'] == name)
            self.assertEqual(result['required_steps_passed'].count('Verify native remote resource inspection'), 1)
            self.assertEqual(result['required_steps_passed'].count('Verify native remote resource management'), 1)
            self.assertEqual(result['required_steps_passed'].count('Verify native fixed-group resource catalog'), 1)
            self.assertEqual(result['required_steps_passed'].count('Verify durable text restart and lost ACK across Core processes'), 1)
            self.assertEqual(result['required_steps_passed'].count('Verify manual nearby setup across native Core peers'), 1)

    def test_setup_and_text_browser_missing_duplicate_or_non_success_blocks_release(self):
        original = copy.deepcopy(self.jobs)
        gates = [(name, 'Verify manual nearby setup across native Core peers') for name, _ in full.TARGETS.values()]
        gates += [('browser', 'Verify nearby setup with separate agents and actual browser'),
                  ('browser', 'Verify durable text recovery with separate agents and actual browser')]
        for name, gate in gates:
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

    def test_setup_and_text_browser_exact_execution_and_exports_are_required(self):
        original = self.sources[full.WORKFLOW]
        gates = (
            ('Verify manual nearby setup across native Core peers', 'TestLANCoreNearbySetupManualIntegration', '3m'),
            ('Verify nearby setup with separate agents and actual browser', 'TestNearbySetupTwoProcessBrowser', '3m'),
            ('Verify durable text recovery with separate agents and actual browser', 'TestDurableTextThreeProcessBrowser', '4m'),
        )
        for gate, test, timeout in gates:
            marker = ('      - name: ' + gate + '\n').encode()
            start = original.index(marker)
            end = original.index(b'      - ', start + len(marker))
            block = original[start:end]
            changes = [
                (b'--exact ', b''), (b'-race ', b''), (b'-count=1', b'-count=0'),
                (('-timeout=' + timeout).encode(), b'-timeout=0'),
                (test.encode(), b'TestOther'),
                (b"INTEGRATION: '1'", b"INTEGRATION: '0'"),
                (marker, marker + b'        if: false\n'),
                (marker, marker + b"        if: matrix.goos != 'windows'\n"),
                (marker, marker + b'        continue-on-error: true\n'),
            ]
            if 'actual browser' in gate:
                changes += [(b'ci-browser-summary.py', b'other-summary.py'),
                            (b' || status=$?', b' || true'),
                            (b' || summary_status=$?', b' || true'),
                            (b'exit "$summary_status"', b'exit 0'),
                            (b'if [ "$status" -ne 0 ]; then exit "$status"; fi', b'true')]
            if test == 'TestNearbySetupTwoProcessBrowser':
                changes += [(b'export SOBA_SETUP_BROWSER_SCREENSHOTS_PREPARED=0', b'true'),
                            (b'mkdir -m 700 "$SOBA_SETUP_BROWSER_SCREENSHOTS"', b'mkdir -p -m 700 "$SOBA_SETUP_BROWSER_SCREENSHOTS"'),
                            (b'mkdir -m 700 "$SOBA_SETUP_BROWSER_SCREENSHOTS"', b'mkdir -m 755 "$SOBA_SETUP_BROWSER_SCREENSHOTS"'),
                            (b'export SOBA_SETUP_BROWSER_SCREENSHOTS_PREPARED=1', b'true'),
                            (b'unset SOBA_SETUP_BROWSER_SCREENSHOTS', b'true')]
            mutations = [original.replace(block, b'', 1), original.replace(block, block + block, 1)]
            moved = original.replace(block, b'', 1)
            wrong_job = b'  native:\n' if 'actual browser' in gate else b'  browser:\n'
            wrong_start = moved.index(wrong_job)
            wrong_step = moved.index(b'      - ', wrong_start)
            mutations.append(moved[:wrong_step] + block + moved[wrong_step:])
            for before, after in changes:
                self.assertIn(before, block)
                mutations.append(original.replace(block, block.replace(before, after), 1))
            for index, value in enumerate(mutations):
                with self.subTest(gate=gate, mutation=index), self.assertRaises(ValueError):
                    full.validate_setup_and_text_browser_workflow(value)
        for filename in ('setup-native-summary.json', 'setup-native-failure.json', 'text-native-summary.json'):
            line = ('            ${{ runner.temp }}/soba-playwright-screenshots/' + filename + '\n').encode()
            self.assertEqual(original.count(line), 1)
            for replacement in (b'', b'            ${{ runner.temp }}/**\n'):
                with self.subTest(export=filename, replacement=replacement), self.assertRaises(ValueError):
                    full.validate_setup_and_text_browser_workflow(original.replace(line, replacement, 1))

    def test_setup_and_text_browser_gates_cannot_be_omitted_from_literal_policy(self):
        path = '.github/scripts/ci-coverage.py'
        original = self.sources[path]
        for gate in ('Verify manual nearby setup across native Core peers',
                     'Verify nearby setup with separate agents and actual browser',
                     'Verify durable text recovery with separate agents and actual browser'):
            needle = ('"' + gate + '"').encode()
            self.assertEqual(original.count(needle), 1)
            self.sources[path] = original.replace(needle, b'"Other gate"', 1)
            with self.subTest(gate=gate), self.assertRaises(ValueError):
                full.required_jobs(self.sources)

    def test_fixed_browser_summary_sources_are_bound_to_checkout(self):
        for path in ('.github/scripts/ci-browser-summary.py', '.github/scripts/ci-nearby-diagnostic.py'):
            original = self.sources[path]
            self.sources[path] += b'\n# synthetic changed source\n'
            with self.subTest(path=path), self.assertRaisesRegex(ValueError, 'checkout policy'):
                self.audit(local_root=ROOT)
            self.sources[path] = original

    def test_durable_text_missing_duplicate_or_non_success_blocks_release(self):
        original = copy.deepcopy(self.jobs)
        gate = 'Verify durable text restart and lost ACK across Core processes'
        for name, _ in full.TARGETS.values():
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
                with self.subTest(target=name, mode=mode), self.assertRaises(ValueError):
                    self.audit()

    def test_durable_text_exact_execution_contract_is_required(self):
        original = self.sources[full.WORKFLOW]
        marker = b'      - name: Verify durable text restart and lost ACK across Core processes\n'
        start = original.index(marker)
        end = original.index(b'      - ', start + len(marker))
        block = original[start:end]
        changes = (
            (b'--expect github.com/webkaz-labs/sobalink/internal/core:TestDurableTextProcessRestartACKLossAndHiddenReplay', b'--expect Other'),
            (b"-run '^TestDurableTextProcessRestartACKLossAndHiddenReplay$'", b"-run '^Other$'"),
            (b'--exact ', b''),
            (b'-race ', b''),
            (b'-count=1', b'-count=0'),
            (b'-timeout=4m', b'-timeout=0'),
            (b'timeout-minutes: 6', b'timeout-minutes: 60'),
            (b'SOBALINK_RUN_DIRECT_INTEGRATION: \'1\'', b'SOBALINK_RUN_DIRECT_INTEGRATION: \'0\''),
            (b'lanlink_integration,directlan_integration,', b'lanlink_integration,'),
            (marker, marker + b'        if: false\n'),
            (marker, marker + b"        if: matrix.goos != 'windows'\n"),
            (marker, marker + b'        continue-on-error: true\n'),
            (b"HiddenReplay$'\n", b"HiddenReplay$' || true\n"),
        )
        mutations = [original.replace(block, b'', 1), original.replace(block, block + block, 1)]
        for before, after in changes:
            self.assertEqual(block.count(before), 1)
            mutations.append(original.replace(block, block.replace(before, after, 1), 1))
        # Keeping the exact command only in the browser job cannot qualify any
        # of the four required native targets.
        moved = original.replace(block, b'', 1)
        browser_start = moved.index(b'  browser:\n')
        browser_step = moved.index(b'      - ', browser_start)
        mutations.append(moved[:browser_step] + block + moved[browser_step:])
        for index, source in enumerate(mutations):
            with self.subTest(mutation=index), self.assertRaisesRegex(ValueError, 'durable text'):
                full.validate_durable_text_workflow(source)

    def test_durable_text_required_gate_cannot_be_omitted_or_reclassified(self):
        path = '.github/scripts/ci-coverage.py'
        original = self.sources[path]
        entry = b'    "Verify durable text restart and lost ACK across Core processes",\n'
        self.assertEqual(original.count(entry), 1)
        missing = original.replace(entry, b'', 1)
        for index, source in enumerate((
                missing,
                original.replace(entry, entry + entry, 1),
                missing.replace(b'LONG_STEPS = (\n', b'LONG_STEPS = (\n' + entry, 1),
                missing.replace(b'FULL_ONLY_STEPS = (\n', b'FULL_ONLY_STEPS = (\n' + entry, 1),
                missing.replace(b'BROWSER_STEPS = (', b'BROWSER_STEPS = (' + entry, 1))):
            self.sources[path] = source
            with self.subTest(mutation=index), self.assertRaises(ValueError):
                full.required_jobs(self.sources)

    def test_durable_outbox_missing_or_skipped_gate_blocks_release(self):
        original = copy.deepcopy(self.jobs)
        for name, gate in [(name, 'Verify durable outbox restart across Core processes')
                           for name, _ in full.TARGETS.values()] + [
                               ('browser', 'Verify durable outbox recovery with separate agents and actual browser')]:
            for mode in ('missing', 'duplicate', 'skipped', 'failure', 'cancelled', 'pending'):
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
                with self.subTest(target=name, mode=mode), self.assertRaises(ValueError):
                    self.audit()

    def test_durable_outbox_exact_execution_contract_is_required(self):
        original = self.sources[full.WORKFLOW]
        for change in (
            (b'--expect github.com/webkaz-labs/sobalink/internal/core:TestOutboxProcessRestart', b'--expect Other'),
            (b"./internal/core -run '^TestOutboxProcessRestart$'", b"./internal/core -run '^Other$'"),
            (b'--expect github.com/webkaz-labs/sobalink/internal/core:TestOutboxThreeProcessBrowser', b'--expect Other'),
            (b'      - name: Verify durable outbox recovery with separate agents and actual browser\n',
             b'      - name: Verify durable outbox recovery with separate agents and actual browser\n        if: false\n'),
        ):
            self.sources[full.WORKFLOW] = original.replace(*change, 1)
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, 'durable outbox'):
                full.required_jobs(self.sources)

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
        self.assertTrue(any('/attempts/1/jobs' in p for p in self.calls))
        self.assertEqual(proof['macos_discovery_proof']['producingAttempt'], 1)

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
        value = dnssd_fixture.fixture()
        value['provenance']['ciIdentity']['runId'] = 11
        self.artifact, self.archive = dnssd_fixture.artifact(value)
        self.artifact['name'] = dnssd_fixture.gate.ARTIFACT + '-11-1'
        self.artifact['workflow_run']['id'] = 11
        for target in core_fixture.gate.TARGETS:
            value = core_fixture.fixture(target)
            value['provenance']['ciIdentity']['runId'] = 11
            meta, data = core_fixture.artifact(target, value=value, identifier=self.core_artifacts[target][0]['id'])
            meta['name'] = meta['name'].replace('-10-1', '-11-1')
            meta['workflow_run']['id'] = 11
            self.core_artifacts[target] = (meta, data)
        self.refresh_job_logs(run_id=11)
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
