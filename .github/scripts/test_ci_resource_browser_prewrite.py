"""Closed contracts plus one bounded pinned-Node value check; no browser/network."""
import copy
import importlib.util
from pathlib import Path
import re
import os
import shutil
import subprocess
import stat
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
        self.assertEqual({k for k in GATE.NATIVE_KEYS if safe[k]}, set())
        self.assertEqual({k for k in GATE.NATIVE_KEYS if browser[k]}, {'SOBALINK_RUN_RESOURCE_BROWSER_NATIVE'})
        self.assertEqual(len({safe['HOME'], browser['HOME']}), 2)
        self.assertEqual(len(GATE.SAFE), 4)
        self.assertIn('TestResourceBrowserCatalogSelectionCanonicalOrder', GATE.SAFE)
        for value in (safe, browser):
            self.assertEqual(value['GOPROXY'], 'off')
            self.assertEqual(value['CGO_ENABLED'], '0')
            self.assertTrue(all(k not in value for k in ('HTTP_PROXY', 'HTTPS_PROXY', 'NODE_OPTIONS', 'PWDEBUG', 'GITHUB_TOKEN')))
        self.assertEqual(GATE.CHROME_PATH, '/opt/google/chrome/chrome')
        self.assertEqual(GATE.CHROME_VERSION, '154.0.8037.97')
        facts = {'mode': stat.S_IFREG | 0o755, 'uid': 0, 'size': 20}
        GATE.validate_root_facts(facts, 100, True)
        GATE.validate_root_facts(dict(facts, mode=stat.S_IFREG | 0o4755), 100, True)
        # Kernel virtual files may advertise zero or a page, regardless of bytes
        # returned. The production reader applies a cap+1 stream limit instead.
        for size in (0, 4096): GATE.validate_root_facts(dict(facts, mode=stat.S_IFREG | 0o444, size=size), 128)
        for change in ({'uid': 1000}, {'mode': stat.S_IFREG | 0o777}, {'mode': stat.S_IFLNK | 0o755},
                       {'mode': stat.S_IFREG | 0o644}, {'mode': stat.S_IFREG | 0o2755}, {'size': 101}):
            with self.assertRaises(RuntimeError): GATE.validate_root_facts(dict(facts, **change), 100, True)
        elf = b'\x7fELF\x02\x01\x01' + b'\0' * 11 + b'\x3e\x00'
        GATE.validate_chrome_binary(elf)
        for bad in (b'', b'#!/bin/sh', elf[:4] + b'\x01' + elf[5:], elf[:18] + b'\xb7\x00'):
            with self.assertRaises(RuntimeError): GATE.validate_chrome_binary(bad)
        # These executable statements are exact from upstream AppArmor v4.0.1
        # profiles/apparmor.d/chrome; comments below are deliberately synthetic.
        profile = '# synthetic leading comment\nabi <abi/4.0>,\ninclude <tunables/global>\n\nprofile chrome /opt/google/chrome/chrome flags=(unconfined) {\n  userns,\n  # synthetic local-override comment\n  include if exists <local/chrome>\n}\n'
        image = {'os': 'ubuntu24', 'version': '20261004.327.1'}
        texts = {'profile': profile, 'local': None, 'disabled': False, 'enabled': 'Y', 'restricted': '1', 'clone': '1', 'namespaces': '65536'}
        GATE.validate_chrome_support(image, texts)
        GATE.validate_chrome_support(image, dict(texts, local='# synthetic comment\n\n'))
        for change in ({'profile': profile.replace('/opt/google/chrome/chrome', '/other/chrome')},
                       {'profile': profile.replace('userns,', '')}, {'profile': profile.replace('userns,', 'userns,\n  deny network,')}, {'profile': ''}, {'local': 'deny userns,'},
                       {'disabled': True}, {'enabled': 'N'}, {'restricted': '0'}, {'clone': '0'},
                       {'namespaces': '0'}, {'namespaces': '2147483648'}, {'namespaces': 'unknown'}):
            with self.assertRaises(RuntimeError): GATE.validate_chrome_support(image, dict(texts, **change))
        for changed in ({'os': 'ubuntu22', 'version': GATE.IMAGE_VERSION}, {'os': 'ubuntu24', 'version': 'next'}, {}):
            with self.assertRaises(RuntimeError): GATE.validate_chrome_support(changed, texts)
        config = (GATE.ROOT / 'web/playwright.resource-native.config.mjs').read_text()
        self.assertIn("assert.equal(chromium, '/opt/google/chrome/chrome'", config)
        self.assertIn("channel: 'chrome'", config)
        self.assertIn('chromiumSandbox: true', config)

    def b1(self):
        native = {'schema': 2, 'failureStage': 'none', 'accepted': True, 'browserJoined': True, 'coreClosed': True, 'lockClosed': True, 'httpCountsOnly': True, 'savedServiceNavigationCovered': False, 'pixelReviewPerformed': False}
        scope = {'schema': 1, 'complete': True, 'descendantsReaped': True, 'playwrightExit': 0, 'forced': False, 'deadlineExceeded': False, 'errors': 0, 'observed': 30, 'reaped': 30}
        browser = {'schema': 3, 'diagnostic': {'status': 'none', 'unit': 'none', 'line': 0, 'column': 0, 'category': 'none'}, 'expected': 1, 'observed': 1, 'passed': 1, 'errors': 0, 'selectionValid': True, 'unexpected': False, 'accepted': True}
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
        for diagnostic in ({'status': 'failed', 'unit': 'fixture', 'line': 1, 'column': 1, 'category': 'unclassified'},
                           {'status': 'timedOut', 'unit': 'case', 'line': 100000, 'column': 10000, 'category': 'browser_page'},
                           {'status': 'unavailable', 'unit': 'unavailable', 'line': 0, 'column': 0, 'category': 'unavailable'}):
            failed = dict(self.b1()[2], passed=0, accepted=False, diagnostic=diagnostic)
            self.assertEqual(GATE.validate_b1_receipt('browser', failed)['diagnostic'], diagnostic)
            with self.assertRaises(RuntimeError): GATE.validate_b1(self.b1()[0], self.b1()[1], failed, self.b1()[3])
        for diagnostic in ({'status': 'failed', 'unit': '/private/file', 'line': 1, 'column': 1, 'category': 'unclassified'},
                           {'status': 'failed', 'unit': 'fixture', 'line': 0, 'column': 1, 'category': 'fixture_guard'},
                           {'status': 'failed', 'unit': 'fixture', 'line': True, 'column': 1, 'category': 'fixture_guard'},
                           {'status': 'unavailable', 'unit': 'fixture', 'line': 0, 'column': 0, 'category': 'unavailable'},
                           {'status': 'none', 'unit': 'none', 'line': 0, 'column': 0, 'category': 'none', 'message': 'forbidden'}):
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
        for category in GATE.BROWSER_CATEGORIES[1:]:
            value = {'status': 'failed', 'unit': 'unavailable', 'line': 0, 'column': 0, 'category': category}
            self.assertEqual(GATE.validate_browser_diagnostic(value), value)
        # Exercise the actual pure JS decoder, not a Python copy of its logic.
        # The fixed script never calls reporter.onEnd (the only write path).
        node_path = shutil.which('node')
        self.assertIsNotNone(node_path, 'Pinned Node missing')
        node = Path(node_path).resolve()
        self.assertTrue(node.is_absolute() and node.is_file() and not node.is_symlink() and os.access(node, os.X_OK), 'Pinned Node identity invalid')
        reporter_url = (GATE.ROOT / 'web/browser/resource-native-runner.mjs').as_uri()
        script = "import Reporter, { diagnostic } from " + repr(reporter_url) + ";\n" + r"""
import assert from 'node:assert/strict';
try {
  assert.equal(process.version, 'v24.19.0');
  const category = (message, status = 'failed') => diagnostic({ message }, status).category;
  assert.equal(diagnostic(undefined, 'failed').category, 'unavailable');
  for (const message of [undefined, null, 7, '', 'x'.repeat(65537)]) assert.equal(category(message), 'unavailable');
  for (const message of ['ordinary synthetic failure', 'Private fixture descriptor was invalid; contents withheld', 'Real local browser authentication failed; private details withheld', 'prefix browserType.launch: denied', 'browserType.launcher: denied', 'prefix Owned private fixture root required', 'Chromium sandboxing failed!']) assert.equal(category(message), 'unclassified');
  assert.equal(category('Error: Private fixture descriptor was invalid; contents withheld'), 'fixture_descriptor');
  assert.equal(category('Error: Real local browser authentication failed; private details withheld'), 'fixture_authentication');
  for (const label of ['Owned private fixture root required', 'Automatic capture forbidden', 'Service workers forbidden']) {
    for (const prefix of ['', 'AssertionError: ', 'AssertionError [ERR_ASSERTION]: ']) assert.equal(category(prefix + label), 'fixture_guard');
    assert.equal(category(label + ' spoof'), 'unclassified');
  }
  for (const prefix of ['', 'Error: ', 'TimeoutError: ']) {
    assert.equal(category(prefix + 'browserType.launch: synthetic'), 'browser_launch');
    assert.equal(category(prefix + 'browser.newContext: synthetic'), 'browser_context');
    assert.equal(category(prefix + 'browserContext.newPage: synthetic'), 'browser_page');
  }
  assert.equal(category('Error: browserType.launch: synthetic\nChromium sandboxing failed!'), 'browser_launch_sandbox');
  assert.equal(category('Error: browser.newContext: Chromium sandboxing failed!'), 'browser_context');
  const boundary = 'browserType.launch:' + 'x'.repeat(65536 - 'browserType.launch:'.length);
  assert.equal(category(boundary), 'browser_launch'); assert.equal(category(boundary + 'x'), 'unavailable');
  const unavailableLocation = diagnostic({ message: 'browserType.launch: synthetic' }, 'timedOut');
  assert.deepEqual(unavailableLocation, { status: 'timedOut', unit: 'unavailable', line: 0, column: 0, category: 'browser_launch' });
  assert.deepEqual(diagnostic({ message: 'browserType.launch: synthetic' }, 'unknown'), { status: 'unavailable', unit: 'unavailable', line: 0, column: 0, category: 'unavailable' });
  const owner = new Reporter(); owner.onError({ message: 'first synthetic failure' });
  const first = owner.firstFailure;
  owner.onError({ message: 'browserType.launch: later failure' });
  owner.onTestEnd({ title: 'resource-native-local-catalog-en-settings', expectedStatus: 'passed' }, { status: 'failed', retry: 0, attachments: [], errors: [{ message: 'browserContext.newPage: later failure' }] });
  assert.strictEqual(owner.firstFailure, first); assert.equal(owner.firstFailure.category, 'unclassified');
} catch { process.exitCode = 1; }
"""
        try:
            result = subprocess.run([str(node), '--input-type=module', '-e', script], cwd=GATE.ROOT,
                                    env={'PATH': '/usr/bin:/bin', 'HOME': '/nonexistent', 'LANG': 'C.UTF-8', 'LC_ALL': 'C.UTF-8', 'TZ': 'UTC'},
                                    stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                    close_fds=True, timeout=10, check=False)
        except (OSError, subprocess.TimeoutExpired): self.fail('Bounded Node diagnostic value check did not complete')
        self.assertEqual(result.returncode, 0, 'Pinned Node diagnostic value contract failed')

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
        result.update(phase='B1', predicate='command', completedCohorts=1, platformTeardownReliedOn=True)
        result['safe'] = {'status': 'passed', 'passedTests': 4}
        result['B1'].update(status='failed', predicate='command', command=self.outcome(1))
        self.assertEqual(GATE.validate_result(result)['completedCohorts'], 1)
        self.assertEqual(result['F-S1']['status'], 'not-selected')
        self.assertEqual(result['F-S1']['passedTests'], 0)
        self.assertFalse(result['F-S1']['command']['spawned'])
        passed = copy.deepcopy(result)
        passed.update(phase='complete', predicate='none', completedCohorts=2, accepted=True, nativeCleanupProven=True, platformTeardownReliedOn=False)
        passed['B1'].update(status='passed', passedTests=1, predicate='none', command=self.outcome(0),
                            receipts={kind: {'availability': 'valid', 'value': value} for kind, value in zip(GATE.RECEIPT_FILES, self.b1())})
        self.assertTrue(GATE.validate_result(passed)['accepted'])
        self.assertEqual(passed['selection'], 'current-b1-only')
        for changed in ({'status': 'passed', 'passedTests': 1}, {'status': 'not-started'}, {'passedTests': 1}):
            bad = copy.deepcopy(passed); bad['F-S1'].update(changed)
            with self.assertRaises(RuntimeError): GATE.validate_result(bad)
        with self.assertRaises(RuntimeError): GATE.validate_result(dict(passed, completedCohorts=3))
        for changed in ({'returnCode': True}, {'returnCode': 256}, {'stderrPresent': True}, {'raw': 'forbidden'}):
            with self.assertRaises(RuntimeError): GATE.validate_command(dict(self.outcome(0), **changed))
        for code in (-9, 0, 1, 2): self.assertEqual(GATE.validate_command(self.outcome(code))['returnCode'], code)
        unknown = dict(GATE.empty_command(), spawned=True, returnCategory='unobserved')
        self.assertFalse(GATE.validate_command(unknown)['returnCodeObserved'])
        for key, value in (('accepted', True), ('platformTeardownObserved', True), ('phase', '/private/path'), ('predicate', 'raw stderr')):
            with self.subTest(key=key), self.assertRaises(RuntimeError): GATE.validate_result(dict(result, **{key: value}))
        with self.assertRaises(RuntimeError): GATE.validate_result(dict(result, rawLog='no'))

    def test_closed_provenance_never_implies_approval(self):
        provenance = {'schema': 1, 'hashMeaning': 'Measured hosted input identities; no independent source approval inferred.', 'commit': 'a' * 40, 'existingChrome': GATE.chrome_provenance({'binary': {'sha256': 'a' * 64}, 'profile': {'sha256': 'b' * 64}, 'helper': None, 'local': None}), 'priorFS1': dict(GATE.PRIOR_FS1)}
        self.assertEqual(GATE.validate_provenance(provenance), provenance)
        self.assertFalse(provenance['priorFS1']['executedInCurrentRun'])
        chrome = provenance['existingChrome']
        self.assertFalse(chrome['loadedProfileObserved'])
        self.assertFalse(chrome['loadedProfileReadAttempted'])
        self.assertFalse(chrome['packageSignatureVerified'])
        self.assertFalse(chrome['helperPresent'])
        with_helper = GATE.chrome_provenance({'binary': {'sha256': 'a' * 64}, 'profile': {'sha256': 'b' * 64}, 'helper': {'sha256': 'c' * 64, 'setuid': True, 'mode': 0o4755}, 'local': {'sha256': 'd' * 64}})
        self.assertEqual(GATE.validate_chrome_provenance(with_helper), with_helper)
        for change in ({'version': 'next'}, {'channel': 'chromium'}, {'imageVersion': 'next'}, {'loadedProfileObserved': True},
                       {'packageSignatureVerified': True}, {'helperSetuid': True}, {'binarySha256': '/private/path'}, {'rawProfile': 'forbidden'}):
            with self.assertRaises(RuntimeError): GATE.validate_chrome_provenance(dict(chrome, **change))
        for change in ({'run': 1}, {'attempt': True}, {'executedInCurrentRun': True}, {'raw': 'forbidden'}):
            bad = copy.deepcopy(provenance); bad['priorFS1'].update(change)
            with self.assertRaises(RuntimeError): GATE.validate_provenance(bad)
        for extra in ({'rawLog': 'private'}, {'chromiumSha256': '/private/chrome'}, {'approvedSource': True}):
            with self.assertRaises(RuntimeError): GATE.validate_provenance(dict(provenance, **extra))


if __name__ == '__main__':
    unittest.main()
