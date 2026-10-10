"""Fixed-value admission/provenance/export checks; no Go or target execution.

Importing this test module has no project effects. The script under test is loaded
only when tests are explicitly invoked. No sockets, process spawning, downloads,
fixture files, environment mutation, raw evidence, or user-specific data are used.
"""
import copy
import importlib.util
import json
from pathlib import Path
import unittest


def encoded(value):
    return (json.dumps(value, sort_keys=True, indent=2, ensure_ascii=True) + '\n').encode('ascii')


class ResourceProcessRestartValues(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location(
            'resource_process_restart', Path(__file__).with_name('ci-resource-process-restart.py'))
        cls.gate = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.gate)

    def approved(self):
        # Deliberately small synthetic independent allowlist; production passes
        # every entry from the separately pinned anchor and approved public tree.
        return {
            'source': {'source/cmd/example/main.go': '1' * 64},
            'toolchain': {'toolchain/src/runtime/example.go': '2' * 64},
            'dependencies': {'modules/example.org/module@v1.0.0/example.go': '3' * 64},
            'generatedMain': {'generated/cmd/example.test.go': '4' * 64},
        }

    def context(self):
        g = self.gate
        source = {'commit': 'a' * 40, 'tree': 'b' * 40, 'parent': 'c' * 40, 'parentTree': 'd' * 40,
                  'baselineProjectionSHA256': 'e' * 64, 'sourceManifestSHA256': 'f' * 64}
        build = {}
        for role in g.PUBLIC_IMAGE_ROLES:
            build[role] = {
                'goVersion': 'go1.27.1', 'modulePath': 'github.com/webkaz-labs/sobalink',
                'moduleVersion': 'v0.0.0-20260101000000-' + source['commit'][:12],
                'packagePath': g.PUBLIC_PACKAGE_PATHS[role], 'revision': source['commit'],
                'vcsModified': False, 'goos': 'linux', 'goarch': 'amd64', 'goamd64': 'v1',
                'cgoEnabled': False, 'trimpath': True, 'buildvcs': True, 'pgo': 'off',
                'tags': g.PUBLIC_TAGS, 'buildMode': 'exe', 'compiler': 'gc',
                'settingsVerified': True, 'imageSHA256': '5' * 64,
            }
        outer = {key: False for key in g.PUBLIC_OUTER_BOOL_KEYS}
        outer.update(groupSignals=0, leaderExitObservedBeforeOuterDeadline=None, returncode=None,
                     commandAndJoinElapsedMicros=None, rawOutputSeenBytes=0, rawOutputRetainedBytes=0)
        return {
            'phase': 'leaf-prerequisites', 'outcome': 'not_run', 'failureCategory': 'prerequisite_failed',
            'source': source, 'hashes': {key: '6' * 64 for key in g.PUBLIC_HASH_KEYS},
            'actionPins': dict(g.PUBLIC_ACTION_PINS),
            'image': {'label': 'ubuntu-24.04', 'ImageOS': 'ubuntu24', 'ImageVersion': '20261004.327.1',
                      'runnerEnvironment': 'github-hosted', 'architecture': 'X64'},
            'toolHashes': {key: '7' * 64 for key in g.PUBLIC_TOOL_HASH_KEYS},
            'toolVersions': {'python': '3.12.3', 'git': '2.51.0', 'go': 'go1.27.1'},
            'buildInfo': build,
            'prerequisites': {'validationScope': 'completed_successful_cohorts_only',
                              'verifiedRun': 0, 'verifiedPass': 0, 'completedCohorts': 0,
                              'unclassifiedOrNotRunCohorts': 5, 'allChecksPassed': False},
            'outer': outer, 'testOutput': {'run': 0, 'pass': 0, 'fail': 0, 'notRun': 1,
                                         'syntaxValidated': False},
            'innerReceipt': None, 'before': self.approved(), 'after': self.approved(),
        }

    def bindings(self, context):
        return self.gate._public_inner_bindings(context)

    def failed_receipt(self, context):
        g = self.gate
        return dict(self.bindings(context),
                    scenario='source-built-controller-restart-v1', target='linux-amd64',
                    selector='^' + g.PUBLIC_TEST_NAME + '$', outcome='failed', stage='bootstrap_timeout',
                    owners=1, clis=0, checkpoints=0, originalExpiryPreserved=False,
                    stableIdPreserved=False, historyEqual=False, noReplay=False, maintenance=0,
                    joined=False,
                    evidenceScope='instrumented source-built Linux entry only; no installed-binary or other-target acceptance',
                    receiveState='legacy_review_required; no transfer acceptance or repair',
                    unjoined={key: 0 for key in g.PUBLIC_UNJOINED_BOUNDS})

    def failure(self):
        context = self.context()
        context.update(phase='p1-native', outcome='failed', failureCategory='native_failed')
        context['outer'].update(invocationAttempted=True, processJoined=True, readersJoined=True,
                                ownedGroupAbsent=True, groupSignalAttempted=True, groupSignals=1,
                                returncode=1, commandAndJoinElapsedMicros=1000000,
                                leaderExitObservedBeforeOuterDeadline=True, rawOutputSeenBytes=2000,
                                rawOutputRetainedBytes=2000, inputsUnchanged=True, sourceIdentityUnchanged=True)
        context['prerequisites'].update(verifiedRun=83, verifiedPass=83, completedCohorts=5,
                                        unclassifiedOrNotRunCohorts=0, allChecksPassed=True)
        context['testOutput'] = {'run': 1, 'pass': 0, 'fail': 1, 'notRun': 0, 'syntaxValidated': True}
        context['innerReceipt'] = self.failed_receipt(context)
        return context

    def success(self):
        context = self.failure()
        context.update(outcome='passed', failureCategory='none')
        context['outer'].update(returncode=0, fixtureRootTerminallyAbsent=True, allChecksPassed=True)
        context['testOutput'].update({'pass': 1, 'fail': 0})
        context['innerReceipt'].update(outcome='passed', stage='', owners=5, clis=40, checkpoints=26,
                                       originalExpiryPreserved=True, stableIdPreserved=True,
                                       historyEqual=True, noReplay=True, maintenance=2, joined=True)
        return context

    def artifacts(self, context=None):
        return self.gate.construct_public_artifacts(context or self.context(), self.approved())

    def assert_artifact_rejected(self, artifacts):
        with self.assertRaises(ValueError):
            self.gate.validate_public_artifacts(artifacts, self.approved())

    def test_not_run_exports_exactly_six_canonical_files(self):
        artifacts = self.artifacts()
        self.assertEqual(set(artifacts), set(self.gate.PUBLIC_ARTIFACT_NAMES))
        self.assertTrue(self.gate.validate_public_artifacts(artifacts, self.approved()))
        self.assertLessEqual(sum(map(len, artifacts.values())), 16 * 1024 * 1024)
        result = json.loads(artifacts['result.json'])
        self.assertTrue(all(value is None for value in result['observedCounts'].values()))
        self.assertEqual(result['intendedCounts']['nodeAllocations'], 9)
        self.assertEqual(json.loads(artifacts['child-error-category.json']),
                         {'schema': 1, 'category': 'unclassified', 'inspected': False})
        self.assertEqual(artifacts['run-pass-summary.txt'],
                         (self.gate.PUBLIC_TEST_NAME + '\nRUN=0\nPASS=0\nFAIL=0\nNOT_RUN=1\nOUTPUT_VALIDATED=false\n').encode())

    def test_success_requires_independent_receipt_and_outer_facts(self):
        context = self.success()
        artifacts = self.artifacts(context)
        self.assertTrue(self.gate.validate_public_artifacts(artifacts, self.approved()))
        result = json.loads(artifacts['result.json'])
        self.assertEqual(result['observedCounts']['ownerRecords'], 5)
        self.assertIsNone(result['observedCounts']['nodeAllocations'])
        for key in ('processJoined', 'readersJoined', 'ownedGroupAbsent', 'groupSignalAttempted',
                    'inputsUnchanged', 'sourceIdentityUnchanged', 'fixtureRootTerminallyAbsent'):
            bad = copy.deepcopy(context)
            bad['outer'][key] = False
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.artifacts(bad)
        bad = copy.deepcopy(context)
        bad['innerReceipt'] = None
        with self.assertRaises(ValueError):
            self.artifacts(bad)

    def test_retained_group_already_absent_keeps_attempt_and_zero_signals(self):
        context = self.success()
        context['outer']['groupSignals'] = 0
        self.assertTrue(self.gate.validate_public_artifacts(self.artifacts(context), self.approved()))
        context['outer']['groupSignalAttempted'] = False
        with self.assertRaises(ValueError):
            self.artifacts(context)

    def test_failed_receipt_only_reports_observed_inner_counters(self):
        artifacts = self.artifacts(self.failure())
        self.assertTrue(self.gate.validate_public_artifacts(artifacts, self.approved()))
        result = json.loads(artifacts['result.json'])
        self.assertEqual(result['observedCounts'], {'ownerRecords': 1, 'cliInvocationsScheduled': 0,
                         'checkpoints': 0, 'maintenanceDelta': 0, 'nodeAllocations': None})
        self.assertEqual(result['intendedCounts']['productOwnerIncarnations'], 5)
        result['observedCounts']['ownerRecords'] = 5
        artifacts['result.json'] = encoded(result)
        self.assert_artifact_rejected(artifacts)
        bad = self.failure()
        bad['processAccounting'] = {'productOwnerIncarnations': 5, 'productCLIInvocations': 40,
                                    'actualNodeAllocations': 9}
        with self.assertRaises(ValueError):
            self.artifacts(bad)

    def test_unknown_keys_at_every_public_artifact_level_rejected(self):
        original = self.artifacts(self.failure())
        for name in self.gate.PUBLIC_ARTIFACT_NAMES:
            if not name.endswith('.json'):
                continue
            bad = dict(original)
            value = json.loads(bad[name])
            value['unexpected'] = 'synthetic-private-value'
            bad[name] = encoded(value)
            with self.subTest(name=name):
                self.assert_artifact_rejected(bad)
        bad = dict(original)
        bad['extra.json'] = b'{}\n'
        self.assert_artifact_rejected(bad)

    def test_private_shaped_strings_never_escape_closed_fields(self):
        for value in ('/home/example/private', 'C:\\Users\\example\\private', '../private',
                      'https://example.invalid/private?token=synthetic', 'person@example.invalid',
                      'line\nprivate', 'unclassified; private'):
            for where in ('failureCategory', 'phase', 'source', 'toolVersion', 'buildModule', 'childCategory'):
                artifacts = self.artifacts(self.failure())
                if where == 'childCategory':
                    child = json.loads(artifacts['child-error-category.json'])
                    child['category'] = value
                    artifacts['child-error-category.json'] = encoded(child)
                    with self.subTest(where=where, value=value):
                        self.assert_artifact_rejected(artifacts)
                    continue
                context = self.failure()
                if where in ('failureCategory', 'phase'):
                    context[where] = value
                elif where == 'source':
                    context['source']['commit'] = value
                elif where == 'toolVersion':
                    context['toolVersions']['python'] = value
                else:
                    context['buildInfo']['product']['modulePath'] = value
                with self.subTest(where=where, value=value), self.assertRaises(ValueError):
                    self.artifacts(context)

    def test_inventory_paths_bound_to_independent_allowlist(self):
        for path in ('source/unknown.go', 'source/../private', '/private', 'other/file',
                     'source/C:\\private', 'source/person@example.invalid'):
            bad = self.context()
            bad['before']['source'] = {path: '1' * 64}
            with self.subTest(path=path), self.assertRaises(ValueError):
                self.artifacts(bad)
        bad = self.artifacts()
        inputs = json.loads(bad['input-hashes.json'])
        inputs['approved']['source'] = {'source/unapproved.go': '1' * 64}
        bad['input-hashes.json'] = encoded(inputs)
        self.assert_artifact_rejected(bad)

    def test_equality_flags_are_recomputed_not_trusted(self):
        context = self.context()
        context['after']['source']['source/cmd/example/main.go'] = '8' * 64
        artifacts = self.artifacts(context)
        inputs = json.loads(artifacts['input-hashes.json'])
        self.assertFalse(inputs['equalities']['source']['afterMatchesApproved'])
        inputs['equalities']['source']['afterMatchesApproved'] = True
        artifacts['input-hashes.json'] = encoded(inputs)
        self.assert_artifact_rejected(artifacts)

    def test_bool_is_not_count_and_range_is_bounded(self):
        cases = [('groupSignals', True), ('groupSignals', 2), ('returncode', False),
                 ('returncode', -129), ('commandAndJoinElapsedMicros', True),
                 ('commandAndJoinElapsedMicros', -1), ('rawOutputRetainedBytes', 8388609),
                 ('rawOutputSeenBytes', -1)]
        for key, value in cases:
            bad = self.failure()
            bad['outer'][key] = value
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                self.artifacts(bad)
        for value in (True, -1, 84, 1.0, '1'):
            bad = self.context()
            bad['prerequisites']['verifiedRun'] = value
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.artifacts(bad)

    def test_duplicate_keys_nan_noncanonical_and_oversize_rejected(self):
        original = self.artifacts()
        for name in (n for n in original if n.endswith('.json')):
            bad = dict(original)
            bad[name] = b'{"schema":1,' + bad[name].lstrip()[1:]
            with self.subTest(name=name):
                self.assert_artifact_rejected(bad)
        for raw in (b'{"schema":NaN}', b'{}\n\n', b'\xff', b'{} extra'):
            bad = dict(original)
            bad['result.json'] = raw
            self.assert_artifact_rejected(bad)
        bad = dict(original)
        bad['run-pass-summary.txt'] = b'x' * (16 * 1024 * 1024)
        self.assert_artifact_rejected(bad)

    def test_failure_receipt_strict_schema_type_ranges_and_bindings(self):
        context = self.failure()
        receipt, bindings = context['innerReceipt'], self.bindings(context)
        self.assertEqual(self.gate.validate_failure_receipt(encoded(receipt), bindings), receipt)
        for key, value in (('owners', True), ('owners', 6), ('clis', 41), ('checkpoints', -1),
                           ('maintenance', 1 << 64), ('maintenance', False), ('joined', 0),
                           ('stage', '/private'), ('stage', 'unknown_stage'), ('source', 'e' * 40),
                           ('originalExpiryPreserved', True), ('outcome', 'passed')):
            bad = dict(receipt, **{key: value})
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                self.gate.validate_failure_receipt(encoded(bad), bindings)
        for key, value in (('handle', 513), ('reservation', 4), ('driver', 2), ('start', 46), ('exit', True)):
            bad = copy.deepcopy(receipt)
            bad['unjoined'][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.gate.validate_failure_receipt(encoded(bad), bindings)
        for bad in (dict(receipt, private='synthetic'), dict(receipt, processAccounting={'owners': 5})):
            with self.assertRaises(ValueError):
                self.gate.validate_failure_receipt(encoded(bad), bindings)
        duplicate = b'{"owners":1,' + encoded(receipt)[1:]
        with self.assertRaises(ValueError):
            self.gate.validate_failure_receipt(duplicate, bindings)
        bad = copy.deepcopy(receipt)
        bad['unjoined']['unknown'] = 0
        with self.assertRaises(ValueError):
            self.gate.validate_failure_receipt(encoded(bad), bindings)

    def test_failed_child_joins_do_not_erase_other_unjoined_facts(self):
        context = self.failure()
        receipt = dict(context['innerReceipt'], owners=5, clis=40, joined=True,
                       stage='observer_cleanup_unjoined')
        receipt['unjoined'] = dict(receipt['unjoined'], handle=1)
        actual = self.gate.validate_failure_receipt(encoded(receipt), self.bindings(context))
        self.assertTrue(actual['joined'])
        self.assertEqual(actual['unjoined']['handle'], 1)
        self.assertEqual(actual['outcome'], 'failed')

    def test_failure_stdout_exact_complete_grammar_only(self):
        context = self.failure()
        receipt = json.dumps(context['innerReceipt'], separators=(',', ':'))
        test = self.gate.PUBLIC_TEST_NAME
        lines = ['=== RUN   ' + test, '    resource_process_acceptance_native_test.go:91: ' + receipt,
                 '    resource_process_acceptance_native_test.go:92: P1 failed; protected evidence retained; stage=bootstrap_timeout',
                 '--- FAIL: ' + test + ' (1.00s)', 'FAIL']
        raw = ('\n'.join(lines) + '\n').encode()
        self.assertEqual(self.gate.validate_failure_stdout(raw, self.bindings(context)), context['innerReceipt'])
        variants = [raw + b'private\n', raw.replace(b'bootstrap_timeout', b'unknown_stage'),
                    raw.replace(b'FAIL', b'PASS'), b'\n'.join(raw.splitlines()[:-1]),
                    raw.replace(b'=== RUN   ', b'=== RUN   TestOther/'),
                    ('\n'.join(lines[:2] + [lines[1]] + lines[2:]) + '\n').encode()]
        for bad in variants:
            with self.subTest(length=len(bad)), self.assertRaises(ValueError):
                self.gate.validate_failure_stdout(bad, self.bindings(context))

    def test_summary_and_provenance_cross_file_tampering_rejected(self):
        artifacts = self.artifacts()
        artifacts['run-pass-summary.txt'] = artifacts['run-pass-summary.txt'].replace(b'PASS=0', b'PASS=1')
        self.assert_artifact_rejected(artifacts)
        for key in ('commit', 'tree', 'parent', 'parentTree'):
            artifacts = self.artifacts()
            value = json.loads(artifacts['provenance.json'])
            value['source'][key] = '0' * 40
            artifacts['provenance.json'] = encoded(value)
            with self.subTest(key=key):
                self.assert_artifact_rejected(artifacts)
        artifacts = self.artifacts()
        value = json.loads(artifacts['provenance.json'])
        value['inputCounts']['source'] = True
        artifacts['provenance.json'] = encoded(value)
        self.assert_artifact_rejected(artifacts)

    def test_official_archive_policy_is_exact_and_not_an_observed_claim(self):
        artifacts = self.artifacts()
        provenance = json.loads(artifacts['provenance.json'])
        self.assertNotIn('setupGo', provenance['actionPins'])
        self.assertEqual(provenance['goAcquisitionPolicy']['scheme'],
                         'official_go_archive_sha256_before_extract_v1')
        self.assertEqual(provenance['goAcquisitionPolicy']['archiveBytes'], 70553950)
        self.assertEqual(provenance['goAcquisitionPolicy']['archiveSHA256'],
                         '63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445')
        provenance['goAcquisitionPolicy']['archiveBytes'] = True
        artifacts['provenance.json'] = encoded(provenance)
        self.assert_artifact_rejected(artifacts)

    def test_admission_exact_create_public_image_attempt_one(self):
        g = self.gate
        values = {'GITHUB_ACTIONS': 'true', 'GITHUB_EVENT_NAME': 'create', 'GITHUB_REPOSITORY': g.REPOSITORY,
                  'GITHUB_REF': 'refs/heads/' + g.BRANCH, 'GITHUB_RUN_ATTEMPT': '1',
                  'GITHUB_WORKSPACE': str(g.ROOT), 'RUNNER_ENVIRONMENT': 'github-hosted',
                  'RUNNER_OS': 'Linux', 'RUNNER_ARCH': 'X64', 'ImageOS': 'ubuntu24',
                  'ImageVersion': '20261004.327.1', 'GITHUB_SHA': 'a' * 40, 'GITHUB_RUN_ID': '123'}
        event = {'ref_type': 'branch', 'ref': g.BRANCH,
                 'repository': {'full_name': g.REPOSITORY, 'private': False, 'default_branch': 'main'}}
        g.verify_event(values, event)
        for key, value in (('GITHUB_EVENT_NAME', 'workflow_dispatch'), ('GITHUB_EVENT_NAME', 'push'),
                           ('GITHUB_RUN_ATTEMPT', '2'), ('RUNNER_ENVIRONMENT', 'self-hosted'),
                           ('RUNNER_ARCH', 'ARM64'), ('ImageVersion', '20261005.1'),
                           ('GITHUB_REF', 'refs/heads/main'), ('GITHUB_SHA', 'not-a-commit')):
            with self.subTest(key=key, value=value), self.assertRaises((ValueError, RuntimeError)):
                g.verify_event(dict(values, **{key: value}), event)
        bad = copy.deepcopy(event)
        bad['repository']['private'] = True
        with self.assertRaises((ValueError, RuntimeError)):
            g.verify_event(values, bad)
        bad = copy.deepcopy(event)
        bad['repository']['private'] = 0
        with self.assertRaises((ValueError, RuntimeError)):
            g.verify_event(values, bad)
        bad = dict(event, ref_type='tag')
        with self.assertRaises((ValueError, RuntimeError)):
            g.verify_event(values, bad)

    def test_leaf_prerequisite_rejects_extra_skip_missing_duplicate(self):
        valid = b'=== RUN   TestSynthetic\n--- PASS: TestSynthetic (0.00s)\nPASS\n'
        self.assertEqual(self.gate.validate_leaf_output(valid, ['TestSynthetic']), 1)
        for bad in (valid + b'private\n', valid.replace(b'--- PASS:', b'--- SKIP:'),
                    valid.replace(b'=== RUN   TestSynthetic\n', b''), valid + valid,
                    valid.replace(b'TestSynthetic (', b'TestOther (')):
            with self.subTest(length=len(bad)), self.assertRaises((ValueError, RuntimeError)):
                self.gate.validate_leaf_output(bad, ['TestSynthetic'])

    def test_archive_member_values_reject_unsafe_shapes(self):
        value = {'name': 'go/src/example.go', 'kind': 'file', 'size': 3,
                 'mode': 0o644, 'linkname': '', 'pax': {}, 'sparse': False}
        self.assertEqual(self.gate.validate_archive_member_values(value), value)
        changes = [('name', '../go/example'), ('name', '/go/example'),
                   ('name', 'go/../example'), ('name', 'go/example\\link'),
                   ('kind', 'symlink'), ('kind', 'hardlink'), ('kind', 'fifo'),
                   ('linkname', 'target'), ('sparse', True), ('size', True),
                   ('size', -1), ('size', (32 << 20) + 1), ('mode', 0o4755),
                   ('pax', {'path': 'go/src/example.go'}), ('pax', {'size': '3'})]
        for key, replacement in changes:
            with self.subTest(key=key, replacement=replacement), self.assertRaises((ValueError, RuntimeError)):
                self.gate.validate_archive_member_values(dict(value, **{key: replacement}))

    def test_archive_exact_two_unicode_pax_headers(self):
        for name in self.gate.GO_PAX_PATHS:
            value = {'name': name, 'kind': 'file', 'size': 3, 'mode': 0o644,
                     'linkname': '', 'pax': {'path': name}, 'sparse': False}
            self.assertEqual(self.gate.validate_archive_member_values(value), value)
            for pax in ({}, {'path': name + '.extra'}, {'path': name, 'mtime': '0'}):
                with self.subTest(name=name, pax=pax), self.assertRaises((ValueError, RuntimeError)):
                    self.gate.validate_archive_member_values(dict(value, pax=pax))

    def test_archive_directory_shape_is_closed(self):
        value = {'name': 'go', 'kind': 'directory', 'size': 0, 'mode': 0o755,
                 'linkname': '', 'pax': {}, 'sparse': False}
        self.assertEqual(self.gate.validate_archive_member_values(value), value)
        for key, replacement in (('size', 1), ('mode', 0o777), ('name', 'other')):
            with self.subTest(key=key), self.assertRaises((ValueError, RuntimeError)):
                self.gate.validate_archive_member_values(dict(value, **{key: replacement}))


    def test_partial_prerequisites_do_not_claim_zero_failures(self):
        context = self.context()
        context['prerequisites'].update(verifiedRun=69, verifiedPass=69, completedCohorts=2,
                                        unclassifiedOrNotRunCohorts=3)
        result = json.loads(self.artifacts(context)['result.json'])
        self.assertEqual(result['prerequisites']['validationScope'], 'completed_successful_cohorts_only')
        self.assertEqual(result['prerequisites']['unclassifiedOrNotRunCohorts'], 3)
        self.assertNotIn('observedFail', result['prerequisites'])
        self.assertNotIn('observedSkip', result['prerequisites'])
        for change in ({'verifiedRun': 83, 'verifiedPass': 83}, {'completedCohorts': 5},
                       {'allChecksPassed': True}, {'verifiedRun': 68, 'verifiedPass': 68},
                       {'unclassifiedOrNotRunCohorts': 0}):
            bad = copy.deepcopy(context)
            bad['prerequisites'].update(change)
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.artifacts(bad)

    def test_validated_output_requires_a_valid_inner_receipt(self):
        context = self.context()
        context['testOutput']['syntaxValidated'] = True
        with self.assertRaises(ValueError):
            self.artifacts(context)
        context = self.failure()
        context['innerReceipt'] = None
        context['testOutput'].update(run=0, **{'pass': 0, 'fail': 0})
        with self.assertRaises(ValueError):
            self.artifacts(context)

    def native_terminal(self):
        outer = self.failure()['outer']
        return dict(outer, id='p1-native', failureCategories=['native_validation_failed'],
                    firstFailure=None, elapsedSeconds=1.0, rawBytes=outer['rawOutputRetainedBytes'],
                    fixtureReceipt=None)

    def test_native_marker_rejects_falsey_or_malformed_terminal(self):
        for value in (None, {}, False, 0, [], '', {'invocationAttempted': False}):
            with self.subTest(value=value), self.assertRaises((ValueError, RuntimeError)):
                self.gate.validate_native_terminal_for_export(value, True)
        self.assertIsNone(self.gate.validate_native_terminal_for_export(None, False))
        valid = self.native_terminal()
        self.assertEqual(self.gate.validate_native_terminal_for_export(valid, True), valid)
        for value in (valid, {}, False, 0, [], ''):
            with self.subTest(value=value), self.assertRaises((ValueError, RuntimeError)):
                self.gate.validate_native_terminal_for_export(value, False)
        for key, replacement in (('rawBytes', True), ('fixtureReceipt', False),
                                 ('failureCategories', 'native_validation_failed'),
                                 ('firstFailure', {}), ('elapsedSeconds', True)):
            bad = dict(valid, **{key: replacement})
            with self.subTest(key=key), self.assertRaises((ValueError, RuntimeError)):
                self.gate.validate_native_terminal_for_export(bad, True)
        bad = dict(valid, private='/private/example')
        with self.assertRaises((ValueError, RuntimeError)):
            self.gate.validate_native_terminal_for_export(bad, True)

    def test_execution_state_rejects_falsey_or_wrong_types(self):
        for value in ({}, False, 0, [], '', None):
            with self.subTest(value=value), self.assertRaises((ValueError, RuntimeError)):
                self.gate.validate_execution_state_for_export(value)
        valid = {'phase': 'p1-native', 'failureCategory': 'native_failed', 'outcome': 'FAILED',
                 'before': None, 'after': None, 'buildInfo': None, 'binaries': None, 'native': None,
                 'configHash': None, 'manifestHash': None, 'prerequisiteCounts': {'observer32': 32},
                 'toolchainVerified': True}
        self.assertEqual(self.gate.validate_execution_state_for_export(valid), valid)
        for key, replacement in (('prerequisiteCounts', {'model37': 32}), ('before', False),
                                 ('toolchainVerified', 1), ('native', 'not-validated-here')):
            if key == 'native':
                with self.assertRaises((ValueError, RuntimeError)):
                    self.gate.validate_native_terminal_for_export(replacement, True)
                continue
            with self.subTest(key=key), self.assertRaises((ValueError, RuntimeError)):
                self.gate.validate_execution_state_for_export(dict(valid, **{key: replacement}))


    def test_git_commit_time_offsets_preserve_same_instant(self):
        expected_vcs='2026-10-10T10:35:00Z'
        expected_pseudo='v0.0.0-20261010103500-'+'a'*12
        for timestamp in ('2026-10-10T10:35:00+00:00', '2026-10-10T03:35:00-07:00',
                          '2026-10-10T16:05:00+05:30', '2026-10-10T19:35:00+09:00',
                          '2026-10-10T10:05:00-00:30'):
            with self.subTest(timestamp=timestamp):
                instant=self.gate.git_commit_time_utc(timestamp)
                self.assertEqual(instant.utcoffset().total_seconds(), 0)
                self.assertEqual(instant.strftime('%Y-%m-%dT%H:%M:%SZ'), expected_vcs)
                self.assertEqual('v0.0.0-'+instant.strftime('%Y%m%d%H%M%S')+'-'+'a'*12,
                                 expected_pseudo)

    def test_git_commit_time_utc_day_and_year_rollover(self):
        cases=(('2026-01-01T00:30:00+01:00', '2025-12-31T23:30:00Z', '20251231233000'),
               ('2025-12-31T23:30:00-01:00', '2026-01-01T00:30:00Z', '20260101003000'))
        for timestamp,expected_vcs,pseudo_time in cases:
            with self.subTest(timestamp=timestamp):
                instant=self.gate.git_commit_time_utc(timestamp)
                self.assertEqual(instant.utcoffset().total_seconds(), 0)
                self.assertEqual(instant.strftime('%Y-%m-%dT%H:%M:%SZ'), expected_vcs)
                self.assertEqual('v0.0.0-'+instant.strftime('%Y%m%d%H%M%S')+'-'+'b'*12,
                                 'v0.0.0-'+pseudo_time+'-'+'b'*12)

    def test_git_commit_time_rejects_noncanonical_invalid_or_naive(self):
        values=(None, 0, True, [], {}, b'2026-10-10T10:35:00+00:00',
                '2026-10-10T10:35:00', '2026-10-10T10:35:00Z', '2026-10-10 10:35:00+00:00',
                '2026-10-10T10:35:00.000+00:00', '2026-10-10T10:35:00+0000',
                '2026-10-10T10:35:00+24:00', '2026-10-10T10:35:00+00:60',
                '2026-10-10T10:35:00-00:00', '0000-10-10T10:35:00+00:00',
                '2026-02-29T10:35:00+00:00', '2026-13-10T10:35:00+00:00',
                '2026-10-32T10:35:00+00:00', '2026-10-10T24:00:00+00:00',
                '2026-10-10T10:60:00+00:00', '2026-10-10T10:35:60+00:00',
                '2026-10-10T10:35:00+00:00\n', '2026-10-10T10:35:00+00:00 extra',
                '0001-01-01T00:00:00+01:00', '9999-12-31T23:59:59-01:00')
        for timestamp in values:
            with self.subTest(timestamp=timestamp), self.assertRaisesRegex(RuntimeError, '^commit_timestamp_invalid$'):
                self.gate.git_commit_time_utc(timestamp)


    def test_archive_read1_skips_socket_after_final_payload(self):
        events = []

        class Response:
            closed = False

            def isclosed(self):
                return self.closed

            def read1(self, limit):
                events.append(('read1', limit))
                self.closed = True
                return b'synthetic-final-payload'

        response = Response()

        class Socket:
            def settimeout(self, value):
                if response.closed:
                    raise OSError(9, 'synthetic closed descriptor')
                events.append(('settimeout', value))

        def remaining():
            events.append(('remaining',))
            return 2.5

        socket = Socket()
        self.assertEqual(self.gate.archive_response_read1(response, socket, remaining),
                         b'synthetic-final-payload')
        self.assertEqual(self.gate.archive_response_read1(response, socket, remaining), b'')
        self.assertEqual(events, [('remaining',), ('settimeout', 2.5),
                                  ('read1', 65536), ('remaining',)])

    def test_archive_read1_closed_response_still_checks_deadline(self):
        events = []

        class Response:
            def isclosed(self):
                events.append(('isclosed',))
                return True

        class Socket:
            def settimeout(self, value):
                raise AssertionError('closed response must not touch socket')

        def remaining():
            events.append(('remaining',))
            raise RuntimeError('acquisition_deadline')

        with self.assertRaisesRegex(RuntimeError, '^acquisition_deadline$'):
            self.gate.archive_response_read1(Response(), Socket(), remaining)
        self.assertEqual(events, [('remaining',)])

    def test_archive_read1_unknown_length_eof_is_unchanged(self):
        events = []
        blocks = iter((b'synthetic-body', b''))
        timeouts = iter((3.0, 2.0))

        class Response:
            def isclosed(self):
                return False

            def read1(self, limit):
                events.append(('read1', limit))
                return next(blocks)

        class Socket:
            def settimeout(self, value):
                events.append(('settimeout', value))

        def remaining():
            events.append(('remaining',))
            return next(timeouts)

        response, socket = Response(), Socket()
        self.assertEqual(self.gate.archive_response_read1(response, socket, remaining), b'synthetic-body')
        self.assertEqual(self.gate.archive_response_read1(response, socket, remaining), b'')
        self.assertEqual(events, [('remaining',), ('settimeout', 3.0), ('read1', 65536),
                                  ('remaining',), ('settimeout', 2.0), ('read1', 65536)])

    def test_archive_read1_propagates_errors_without_retry(self):
        for fail_at in ('settimeout', 'read1'):
            events = []
            error = OSError(9, 'synthetic failure')

            class Response:
                def isclosed(self):
                    return False

                def read1(self, limit):
                    events.append(('read1', limit))
                    raise error

            class Socket:
                def settimeout(self, value):
                    events.append(('settimeout', value))
                    if fail_at == 'settimeout':
                        raise error

            def remaining():
                events.append(('remaining',))
                return 1.0

            with self.subTest(fail_at=fail_at), self.assertRaises(OSError) as caught:
                self.gate.archive_response_read1(Response(), Socket(), remaining)
            self.assertIs(caught.exception, error)
            expected = [('remaining',), ('settimeout', 1.0)]
            if fail_at == 'read1':
                expected.append(('read1', 65536))
            self.assertEqual(events, expected)

    def test_acquisition_failure_line_is_closed(self):
        sentinel = '/synthetic/private/item https://invalid.example/hidden Authorization=fake-token'
        errors = ((TimeoutError(sentinel), 'timeout'),
                  (OSError(self.gate.errno.EBADF, sentinel, sentinel), 'invalid_descriptor'),
                  (OSError(self.gate.errno.EACCES, sentinel, sentinel), 'os_error'),
                  (RuntimeError(sentinel), 'runtime_error'),
                  (ValueError(sentinel), 'unclassified'))
        for stage in self.gate.ACQUISITION_STAGES:
            for error, category in errors:
                with self.subTest(stage=stage, category=category):
                    line = self.gate.acquisition_failure_line(stage, error)
                    self.assertEqual(line, 'phase=acquire stage=' + stage +
                                     ' category=' + category + ' outcome=failed')
                    self.assertNotIn(sentinel, line)
        for stage in (None, {}, False, 'unexpected=stage'):
            self.assertEqual(self.gate.acquisition_failure_line(stage, ValueError(sentinel)),
                             'phase=acquire stage=unknown category=unclassified outcome=failed')


    def test_dependency_anchor_classes_are_closed(self):
        pairs = (('adapted/example/source.go', 'adapted_file'),
                 ('modules/example.invalid/demo@v1.0.0/source.go', 'module_file'),
                 ('modules/cache/download/example.invalid/demo/@v/v1.0.0.info', 'module_info'),
                 ('modules/cache/download/example.invalid/demo/@v/v1.0.0.mod', 'module_mod'),
                 ('modules/cache/download/example.invalid/demo/@v/v1.0.0.ziphash', 'module_ziphash'),
                 ('modules/cache/download/example.invalid/demo/@v/other', 'unknown'),
                 ('synthetic-private-value', 'unknown'), (None, 'unknown'), ({}, 'unknown'))
        for name, category in pairs:
            with self.subTest(category=category):
                self.assertEqual(self.gate.dependency_anchor_class(name), category)

    def test_metadata_inventory_cursor_identifies_first_failing_anchor(self):
        gate = self.gate
        first = 'modules/cache/download/example.invalid/demo/@v/v1.0.0.info'
        second = 'modules/example.invalid/demo@v1.0.0/source.go'
        third = 'modules/z.example.invalid/later@v1.0.0/source.go'
        data = {first: b'synthetic-info', second: b'synthetic-source', third: b'synthetic-later'}
        expected = {name: gate.hashlib.sha256(raw).hexdigest() for name, raw in data.items()}
        old_reader, old_path = gate.regular_bytes, gate.materialized_path
        try:
            gate.materialized_path = lambda name: name
            for failure in ('none', 'missing', 'mismatch'):
                calls = []

                def reader(name, maximum):
                    calls.append((name, maximum))
                    if name == second and failure == 'missing':
                        raise FileNotFoundError(2, 'synthetic-hidden-detail', name)
                    return b'synthetic-changed' if name == second and failure == 'mismatch' else data[name]

                gate.regular_bytes = reader
                context = {'stage': 'dependency_integrity', 'anchorClass': 'none', 'ordinal': 0}
                with self.subTest(failure=failure):
                    if failure == 'none':
                        self.assertEqual(gate.hash_inventory(expected, context), expected)
                    else:
                        exception = FileNotFoundError if failure == 'missing' else RuntimeError
                        with self.assertRaises(exception):
                            gate.hash_inventory(expected, context)
                    self.assertEqual(context, {'stage': 'dependency_integrity',
                                               'anchorClass': 'module_file',
                                               'ordinal': 3 if failure == 'none' else 2})
                    expected_calls = [(first, 256 << 20), (second, 256 << 20)]
                    if failure == 'none':
                        expected_calls.append((third, 256 << 20))
                    self.assertEqual(calls, expected_calls)
            gate.regular_bytes = lambda name, maximum: data[name]
            self.assertEqual(gate.hash_inventory(expected), expected)
            wrong = dict(expected, **{first: '0' * 64})
            with self.assertRaisesRegex(RuntimeError, '^pinned_input_drift$'):
                gate.hash_inventory(wrong)
        finally:
            gate.regular_bytes, gate.materialized_path = old_reader, old_path

    def test_metadata_failure_line_is_closed(self):
        gate = self.gate
        sentinel = '/synthetic/private/item https://invalid.example/hidden Authorization=fake-token'
        errors = [(TimeoutError(sentinel), 'timeout'),
                  (FileNotFoundError(2, sentinel, sentinel), 'missing_input'),
                  (PermissionError(13, sentinel, sentinel), 'permission_denied'),
                  (OSError(gate.errno.EBADF, sentinel, sentinel), 'invalid_descriptor'),
                  (OSError(5, sentinel, sentinel), 'os_error'),
                  (RuntimeError(sentinel), 'runtime_error'),
                  (RuntimeError('pinned_input_drift', sentinel), 'runtime_error'),
                  (ValueError(sentinel), 'unclassified')]
        errors.extend((RuntimeError(key), value) for key, value in gate.METADATA_FAILURE_CHECKS.items())
        for stage in gate.METADATA_STAGES:
            context = {'stage': stage, 'anchorClass': 'none', 'ordinal': 0}
            for error, category in errors:
                with self.subTest(stage=stage, category=category):
                    line = gate.metadata_failure_line(context, error)
                    self.assertEqual(line, 'phase=metadata stage=' + stage +
                                     ' anchor=none ordinal=0 category=' + category + ' outcome=failed')
                    self.assertNotIn(sentinel, line)
        for anchor in gate.METADATA_ANCHOR_CLASSES:
            if anchor == 'none':
                continue
            for ordinal in (1, 7526):
                context = {'stage': 'dependency_integrity', 'anchorClass': anchor, 'ordinal': ordinal}
                self.assertEqual(gate.metadata_failure_line(context, RuntimeError('pinned_input_drift')),
                                 'phase=metadata stage=dependency_integrity anchor=' + anchor +
                                 ' ordinal=' + str(ordinal) + ' category=pinned_input_drift outcome=failed')
        invalid = [None, {}, False, {'stage': sentinel, 'anchorClass': 'none', 'ordinal': 0},
                   {'stage': 'go_list', 'anchorClass': 'module_info', 'ordinal': 1},
                   {'stage': 'dependency_integrity', 'anchorClass': 'none', 'ordinal': 1},
                   {'stage': 'dependency_integrity', 'anchorClass': sentinel, 'ordinal': 1},
                   {'stage': 'go_list', 'anchorClass': 'none', 'ordinal': 0, 'extra': sentinel}]
        invalid.extend({'stage': 'dependency_integrity', 'anchorClass': 'module_info', 'ordinal': value}
                       for value in (True, False, -1, 0, 7527, 1.0, None, '1'))
        for context in invalid:
            self.assertEqual(gate.metadata_failure_line(context, ValueError(sentinel)),
                             'phase=metadata stage=unknown anchor=none ordinal=0 category=unclassified outcome=failed')


if __name__ == '__main__':
    unittest.main()
