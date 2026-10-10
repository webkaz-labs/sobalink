"""Closed value-only contract tests; no processes, browsers or networking."""
import copy
import importlib.util
from pathlib import Path
import re
import unittest

PATH = Path(__file__).with_name('ci-resource-browser-prewrite.py')
SPEC = importlib.util.spec_from_file_location('resource_browser_prewrite', PATH)
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)


class ClosedContracts(unittest.TestCase):
    def event(self):
        sha = 'a' * 40
        values = {'GITHUB_ACTIONS': 'true', 'GITHUB_EVENT_NAME': 'create', 'GITHUB_REPOSITORY': GATE.REPOSITORY,
                  'GITHUB_REF': 'refs/heads/' + GATE.BRANCH, 'GITHUB_RUN_ATTEMPT': '1', 'GITHUB_SHA': sha}
        event = {'ref_type': 'branch', 'ref': GATE.BRANCH, 'repository': {'full_name': GATE.REPOSITORY, 'private': False, 'default_branch': 'main'}}
        return values, event, sha

    def test_exact_branch_event_and_attempt(self):
        values, event, sha = self.event()
        self.assertEqual(GATE.validate_event(values, event, sha), sha)
        for key, value in (('GITHUB_RUN_ATTEMPT', '2'), ('GITHUB_EVENT_NAME', 'workflow_dispatch'), ('GITHUB_REF', 'refs/heads/main'), ('GITHUB_SHA', 'b' * 40)):
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                GATE.validate_event(dict(values, **{key: value}), event, sha)
        for key, value in (('ref_type', 'tag'), ('ref', GATE.BRANCH + '-retry')):
            with self.subTest(key=key), self.assertRaises(RuntimeError): GATE.validate_event(values, dict(event, **{key: value}), sha)

    def test_playwright_163_full_title_contract(self):
        # Installed1.63 TestCase._grepTitleWithTags walks parent Suite titles,
        # adds this.title/tags, and joins with spaces. File title is present.
        file = 'resource-native-local-catalog.acceptance.mjs'
        full_title = ' '.join((file, GATE.TITLE))
        self.assertFalse(re.search('^' + GATE.TITLE + '$', full_title))
        self.assertTrue(GATE.browser_selection(file, GATE.TITLE, full_title))
        self.assertTrue(GATE.browser_selection(file, GATE.TITLE, 'project ' + full_title))
        # The grep intentionally accepts a plain title; the file+registry
        # contract independently rejects absent/extra source and extra cases.
        self.assertTrue(re.search(GATE.GREP, GATE.TITLE))
        for args in (('', GATE.TITLE, GATE.TITLE, 1), ('other.mjs', GATE.TITLE, full_title, 1),
                     (file, GATE.TITLE, full_title, 2), (file, GATE.TITLE + '-extra', full_title + '-extra', 1),
                     (file, GATE.TITLE, full_title + ' extra', 1)):
            self.assertFalse(GATE.browser_selection(*args))

    def go_output(self, names):
        return ('\n'.join('=== RUN   ' + name + '\n--- PASS: ' + name + ' (0.00s)' for name in names) + '\nPASS\n').encode()

    def test_go_exact_once_multiset_not_order(self):
        names = ['TestB', 'TestA']
        self.assertEqual(GATE.exact_tests(self.go_output(names), sorted(names)), 2)
        for output in (self.go_output(['TestA']), self.go_output(['TestA', 'TestA']), self.go_output(['TestA', 'TestB', 'TestC']),
                       self.go_output(names).replace(b'--- PASS: TestB', b'--- SKIP: TestB'), self.go_output(names) + b'PASS\n',
                       self.go_output(names).replace(b'TestA', b'TestA/subtest')):
            with self.assertRaises(RuntimeError): GATE.exact_tests(output, sorted(names))
        failed = b'=== RUN   TestA\n--- FAIL: TestA (0.01s)\nFAIL\n'
        self.assertEqual(GATE.go_counts(failed, ['TestA']), {'available': True, 'run': 1, 'pass': 0, 'fail': 1, 'skip': 0, 'extra': 0})
        self.assertEqual(GATE.go_counts(self.go_output(['TestOther']), ['TestA'])['extra'], 2)
        self.assertEqual(GATE.go_counts(failed.replace(b'FAIL: TestA', b'SKIP: TestA'), ['TestA'])['skip'], 1)
        self.assertFalse(GATE.go_counts(b'\xff', ['TestA'])['available'])
        self.assertEqual(GATE.go_counts(b'    --- FAIL: TestA/subcase (0.00s)\n', ['TestA'])['extra'], 1)

    def test_exact_graph_and_registry_fingerprints(self):
        packages, active, names = {'example.invalid/fixture'}, {'example.invalid/fixture/file.go': 'a' * 64}, ['TestFixture']
        previous = (GATE.REVIEWED_PACKAGES_SHA256, GATE.REVIEWED_ACTIVE_GO_SHA256, GATE.REVIEWED_REGISTRY_SHA256)
        try:
            GATE.REVIEWED_PACKAGES_SHA256 = GATE.canonical_hash(sorted(packages))
            GATE.REVIEWED_ACTIVE_GO_SHA256 = GATE.canonical_hash(active)
            GATE.REVIEWED_REGISTRY_SHA256 = GATE.canonical_hash(names)
            GATE.validate_graph_fingerprints(packages, active, names)
            for values in ((packages | {'example.invalid/extra'}, active, names),
                           (packages, dict(active, extra='b' * 64), names),
                           (packages, active, names + ['TestExtra'])):
                with self.assertRaisesRegex(RuntimeError, '^metadata_(packages|source_hashes|registry)$'): GATE.validate_graph_fingerprints(*values)
        finally:
            GATE.REVIEWED_PACKAGES_SHA256, GATE.REVIEWED_ACTIVE_GO_SHA256, GATE.REVIEWED_REGISTRY_SHA256 = previous

    def test_actual_buildinfo_fields_and_offset_instant(self):
        binary = Path('/owned/core.test'); commit = 'a' * 40
        settings = {'-buildmode': 'exe', '-compiler': 'gc', '-tags': GATE.TAGS, '-trimpath': 'true',
                    'CGO_ENABLED': '0', 'GOARCH': 'amd64', 'GOOS': 'linux', 'GOAMD64': 'v1',
                    'vcs': 'git', 'vcs.revision': commit, 'vcs.modified': 'false', 'vcs.time': '2026-10-10T20:00:00+09:00'}
        def raw(changed=None):
            values = dict(settings, **(changed or {}))
            return (str(binary) + ': go1.27.1\n\tpath\t' + GATE.CORE + '.test\n\tmod\tgithub.com/webkaz-labs/sobalink\tv0.0.0\t\n' + ''.join('\tbuild\t' + k + '=' + v + '\n' for k, v in values.items())).encode()
        self.assertEqual(GATE.validate_build_info(raw(), binary, commit), '2026-10-10T11:00:00+00:00')
        for changed in ({'CGO_ENABLED': '1'}, {'GOOS': 'darwin'}, {'-tags': 'wrong'}, {'vcs.revision': 'b' * 40}, {'vcs.modified': 'true'}, {'unexpected': 'setting'}):
            with self.assertRaisesRegex(RuntimeError, '^buildinfo_settings$'): GATE.validate_build_info(raw(changed), binary, commit)
        with self.assertRaises(RuntimeError): GATE.validate_build_info(raw().replace(b': go1.27.1', b': go1.27.2'), binary, commit)

    def test_native_environment_is_disjoint_and_fresh(self):
        safe = GATE.environment(Path('/owned/safe/home'), Path('/owned/safe/tmp'), 'safe')
        browser = GATE.environment(Path('/owned/b1/home'), Path('/owned/b1/tmp'), 'B1')
        fault = GATE.environment(Path('/owned/fs/home'), Path('/owned/fs/tmp'), 'F-S1')
        self.assertEqual({k for k in GATE.NATIVE_KEYS if safe[k]}, set())
        self.assertEqual({k for k in GATE.NATIVE_KEYS if browser[k]}, {'SOBALINK_RUN_RESOURCE_BROWSER_NATIVE'})
        self.assertEqual({k for k in GATE.NATIVE_KEYS if fault[k]}, {'SOBALINK_RUN_ACTIVATION_NATIVE', 'SOBALINK_RUN_RESOURCE_GROUP_PREWRITE_NATIVE'})
        self.assertEqual(len({safe['HOME'], browser['HOME'], fault['HOME']}), 3)
        self.assertEqual(len(GATE.SAFE), 4)
        self.assertIn('TestResourceBrowserCatalogSelectionCanonicalOrder', GATE.SAFE)
        for value in (safe, browser, fault):
            self.assertEqual(value['GOPROXY'], 'off')
            self.assertEqual(value['CGO_ENABLED'], '0')
            self.assertTrue(all(k not in value for k in ('HTTP_PROXY', 'HTTPS_PROXY', 'NODE_OPTIONS', 'PWDEBUG', 'GITHUB_TOKEN')))

    def b1(self):
        native = {'schema': 2, 'failureStage': 'none', 'accepted': True, 'browserJoined': True, 'coreClosed': True, 'lockClosed': True, 'httpCountsOnly': True, 'savedServiceNavigationCovered': False, 'pixelReviewPerformed': False}
        scope = {'schema': 1, 'complete': True, 'descendantsReaped': True, 'playwrightExit': 0, 'forced': False, 'deadlineExceeded': False, 'errors': 0, 'observed': 30, 'reaped': 30}
        browser = {'schema': 2, 'diagnostic': {'status': 'none', 'unit': 'none', 'line': 0, 'column': 0}, 'expected': 1, 'observed': 1, 'passed': 1, 'errors': 0, 'selectionValid': True, 'unexpected': False, 'accepted': True}
        http = {'schema': 1, 'requests': 10, 'stateRequests': 3, 'stateReady': 2, 'loginRequests': 1, 'listRequests': 2, 'listResponses': 2, 'snapshots': 1, 'snapshotResponses': 1, 'blocked': 0, 'errors': 0, 'captures': 2, 'completed': True, 'requestsJoined': True}
        return [native, scope, browser, http]

    def test_b1_requires_actual_joins_and_closed_receipts(self):
        self.assertTrue(GATE.validate_b1(*self.b1())['nativeCleanupProven'])
        for index, key, value in ((0, 'coreClosed', False), (1, 'forced', True), (1, 'descendantsReaped', False),
                                  (2, 'observed', 2), (2, 'passed', True), (3, 'listRequests', 3), (3, 'requestsJoined', False), (0, 'privateRaw', 'forbidden')):
            values = self.b1(); values[index][key] = value
            with self.subTest(key=key), self.assertRaises(RuntimeError): GATE.validate_b1(*values)
        for stage in GATE.FAILURE_STAGES:
            failed = dict(self.b1()[0], accepted=False, failureStage=stage)
            self.assertEqual(GATE.validate_b1_receipt('native', failed), failed)
        for diagnostic in ({'status': 'failed', 'unit': 'fixture', 'line': 1, 'column': 1},
                           {'status': 'timedOut', 'unit': 'case', 'line': 100000, 'column': 10000},
                           {'status': 'unavailable', 'unit': 'unavailable', 'line': 0, 'column': 0}):
            failed = dict(self.b1()[2], passed=0, accepted=False, diagnostic=diagnostic)
            self.assertEqual(GATE.validate_b1_receipt('browser', failed)['diagnostic'], diagnostic)
            with self.assertRaises(RuntimeError): GATE.validate_b1(self.b1()[0], self.b1()[1], failed, self.b1()[3])
        for diagnostic in ({'status': 'failed', 'unit': '/private/file', 'line': 1, 'column': 1},
                           {'status': 'failed', 'unit': 'fixture', 'line': 0, 'column': 1},
                           {'status': 'failed', 'unit': 'fixture', 'line': True, 'column': 1},
                           {'status': 'unavailable', 'unit': 'fixture', 'line': 0, 'column': 0},
                           {'status': 'none', 'unit': 'none', 'line': 0, 'column': 0, 'message': 'forbidden'}):
            with self.assertRaises(RuntimeError): GATE.validate_browser_diagnostic(diagnostic)
        # Pure fixture: nonzero command does not suppress independently available
        # receipts, and absent/invalid data never becomes a guessed failure stage.
        previous = GATE.read_json
        fixtures = dict(zip(GATE.RECEIPT_FILES.values(), self.b1()))
        def read(path):
            if path.name == 'scope-proof.json': raise FileNotFoundError()
            if path.name == 'http-observations.json': return {'raw': 'forbidden'}
            return fixtures[path.name]
        try:
            GATE.read_json = read
            values = GATE.b1_diagnostics(Path('/owned'), True)
            self.assertEqual(values['native']['availability'], 'valid')
            self.assertEqual(values['scope'], {'availability': 'missing', 'value': None})
            self.assertEqual(values['http'], {'availability': 'invalid', 'value': None})
            self.assertTrue(all(row['availability'] == 'unavailable' for row in GATE.b1_diagnostics(Path('/owned'), False).values()))
        finally: GATE.read_json = previous
        # Source-backed order is lexical, shared by actual Go decoding and JS
        # selection. The pure Go selected case exercises the production helper.
        canonical = ['local_service', 'local_settings', 'transfer_activity']
        self.assertEqual(sorted(['local_settings', 'local_service', 'transfer_activity']), canonical)
        go = (GATE.ROOT / 'internal/core/resource_browser_native_test.go').read_text()
        fixture = (GATE.ROOT / 'web/browser/resource-native-fixtures.mjs').read_text()
        self.assertIn('func TestResourceBrowserCatalogSelectionCanonicalOrder(', go)
        self.assertIn("const [service, settings, transfers] = command.payload.sources", fixture)
        self.assertIn("sources: [{ kind: 'local_service' }, { kind: 'local_settings'", fixture)

    def outcome(self, code):
        value = GATE.empty_command()
        value.update(spawned=True, returnCodeObserved=True, returnCode=code,
                     returnCategory='zero' if code == 0 else 'signal' if code < 0 else 'nonzero',
                     processJoined=True, groupAbsent=True, readersJoined=True, captureComplete=True,
                     stdoutBytes=100, stderrBytes=0, stderrPresent=False,
                     go={'available': True, 'run': 1, 'pass': int(code == 0), 'fail': int(code != 0), 'skip': 0, 'extra': 0})
        return value

    def test_closed_result_preserves_partial_progress(self):
        result = GATE.result_template()
        result.update(phase='F-S1', predicate='command', completedCohorts=2, platformTeardownReliedOn=True, continuationAfterFS1='allowed')
        result['safe'] = {'status': 'passed', 'passedTests': 4}
        result['F-S1'].update(status='failed', predicate='command', command=self.outcome(1))
        result['B1'].update(status='passed', passedTests=1, command=self.outcome(0),
                            receipts={kind: {'availability': 'valid', 'value': value} for kind, value in zip(GATE.RECEIPT_FILES, self.b1())})
        self.assertEqual(GATE.validate_result(result)['completedCohorts'], 2)
        self.assertFalse(result['accepted'])  # A later B1 PASS cannot erase F-S1 failure.
        self.assertEqual(GATE.continuation_reason(result['F-S1']['command'], True), 'allowed')
        self.assertEqual(GATE.continuation_reason(result['F-S1']['command'], False), 'blocked-inputs')
        for key in ('processJoined', 'groupAbsent', 'readersJoined'):
            unjoined = self.outcome(1)
            unjoined.update(captureComplete=False, stdoutBytes=None, stderrBytes=None, stderrPresent=None,
                            go={'available': False, 'run': 0, 'pass': 0, 'fail': 0, 'skip': 0, 'extra': 0})
            unjoined[key] = False
            if key == 'processJoined': unjoined.update(returnCodeObserved=False, returnCode=None, returnCategory='unobserved')
            self.assertEqual(GATE.continuation_reason(unjoined, True), 'blocked-cleanup')
        absent = GATE.empty_command()
        self.assertEqual(GATE.continuation_reason(absent, True), 'blocked-cleanup')
        for changed in ({'returnCode': True}, {'returnCode': 256}, {'stderrPresent': True}, {'raw': 'forbidden'}):
            with self.assertRaises(RuntimeError): GATE.validate_command(dict(self.outcome(0), **changed))
        for code in (-9, 0, 1, 2): self.assertEqual(GATE.validate_command(self.outcome(code))['returnCode'], code)
        unknown = dict(GATE.empty_command(), spawned=True, returnCategory='unobserved')
        self.assertFalse(GATE.validate_command(unknown)['returnCodeObserved'])
        for key, value in (('accepted', True), ('platformTeardownObserved', True), ('phase', '/private/path'), ('predicate', 'raw stderr')):
            with self.subTest(key=key), self.assertRaises(RuntimeError): GATE.validate_result(dict(result, **{key: value}))
        with self.assertRaises(RuntimeError): GATE.validate_result(dict(result, rawLog='no'))

    def test_closed_provenance_never_implies_approval(self):
        provenance = {'schema': 1, 'hashMeaning': 'Measured hosted input identities; no independent source approval inferred.', 'commit': 'a' * 40, 'chromiumSha256': 'b' * 64}
        self.assertEqual(GATE.validate_provenance(provenance), provenance)
        for extra in ({'rawLog': 'private'}, {'chromiumSha256': '/private/chrome'}, {'approvedSource': True}):
            with self.assertRaises(RuntimeError): GATE.validate_provenance(dict(provenance, **extra))


if __name__ == '__main__':
    unittest.main()
