"""Pure managed-session command, source inventory and workflow policy tests."""

import importlib.util
import io
import os
from pathlib import Path
import re
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = Path(__file__).with_name('ci-managed-session.py')
SPEC = importlib.util.spec_from_file_location('ci_managed_session', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)
FILES = {
    'directlan_managed_session_tls': ('managed_session_tls_test.go', 'managed_cleanup_source_test.go'),
    'directlan_managed_session_outbound_tcp': ('managed_session_outbound_test.go',),
    'directlan_managed_session_native': ('managed_session_native_test.go',),
}
ORDER = ('tls', 'outbound-tcp', 'native-caller', 'native-simultaneous', 'native-udp-capacity')


def inventory(source):
    """Accept only the reviewed subtest grammar; unfamiliar shapes fail closed."""
    result = {}
    functions = re.findall(r'^func (Test\w+)\(t \*testing.T\) \{\n(.*?)(?=^func |\Z)', source, re.M | re.S)
    for name, body in functions:
        runs = re.findall(r'\bt\.Run\((.*?), func\(t \*testing.T\)', body)
        if not runs:
            children = ()
        elif runs == ['operation'] or runs == ['kind']:
            tables = re.findall(r'for _, (?:operation|kind) := range \[\]string\{([^}]+)\}', body)
            if len(tables) != 1:
                raise ValueError('unrecognized string table')
            children = tuple(re.findall(r'"([^\"]+)"', tables[0]))
        elif runs == ['test.name']:
            tables = re.findall(r'\}\{\n(.*?)\n\t\} \{', body, re.S)
            if len(tables) != 1:
                raise ValueError('unrecognized struct table')
            children = tuple(re.findall(r'\{"([^\"]+)",', tables[0]))
        elif runs == ['map[bool]string{false: "invalid-address", true: "accept-error-with-connection"}[withAcceptError]']:
            if 'for _, withAcceptError := range []bool{false, true}' not in body:
                raise ValueError('unrecognized bool table')
            children = ('invalid-address', 'accept-error-with-connection')
        elif runs == ['fmt.Sprintf("caller-%d", callerIndex)']:
            if 'for _, callerIndex := range []int{0, 1}' not in body:
                raise ValueError('unrecognized caller table')
            children = ('caller-0', 'caller-1')
        else:
            raise ValueError('unrecognized subtest grammar')
        if name in result or len(children) != len(set(children)) or (runs and not children):
            raise ValueError('duplicate or empty inventory')
        result[name] = children
    if len(re.findall(r'\bt\.Run\(', source)) != sum(bool(v) for v in result.values()):
        raise ValueError('subtest outside reviewed test body')
    return result


class ManagedSessionPolicyTests(unittest.TestCase):
    def test_exact_source_inventory_and_counts(self):
        self.assertEqual(tuple(gate.SUITES), ORDER)
        for tag, files in FILES.items():
            actual = {}
            for filename in files:
                source = (ROOT / 'internal/directlan' / filename).read_text(encoding="utf-8")
                self.assertTrue(source.startswith('//go:build ' + tag + '\n\n'))
                self.assertNotRegex(source, r'(?m)^func (?:init|TestMain)\(')
                found = inventory(source)
                self.assertFalse(set(actual) & set(found))
                actual.update(found)
            expected = {name: children for suite_tag, _, cases in gate.SUITES.values()
                        if suite_tag == tag for name, children in cases.items()}
            self.assertEqual(actual, expected)
            count = (len(actual), sum(map(len, actual.values())))
            self.assertEqual(count, {'directlan_managed_session_tls': (8, 16),
                                    'directlan_managed_session_outbound_tcp': (1, 13),
                                    'directlan_managed_session_native': (3, 4)}[tag])

    def test_unfamiliar_inventory_fails_closed(self):
        for body in ['t.Run(other, func(t *testing.T) {})',
                     't.Run("new", func(t *testing.T) {})',
                     'for _, callerIndex := range []int{0, 1, 2} {\n t.Run(fmt.Sprintf("caller-%d", callerIndex), func(t *testing.T) {})\n}']:
            with self.assertRaises(ValueError):
                inventory('func TestExtra(t *testing.T) {\n' + body + '\n}\n')

    def test_exact_commands_and_required_parent_and_child_passes(self):
        for suite, (tag, timeout, cases) in gate.SUITES.items():
            command = gate.command(suite)
            cut = command.index('--')
            expected = [gate.MODULE + ':' + name + suffix for name, children in cases.items()
                        for suffix in ('', *('/' + child for child in children))]
            self.assertEqual(command[:2], [gate.sys.executable, str(SCRIPT.with_name('ci-go-test.py'))])
            self.assertEqual(command[2:cut:2], ['--expect'] * len(expected))
            self.assertEqual(command[3:cut:2], expected)
            self.assertEqual(len(expected), len(set(expected)))
            self.assertEqual(command[cut+1:], ['go', 'test', '-race', '-count=1', '-v',
                             '-timeout=' + timeout, '-tags=' + tag + ',' + gate.PRODUCT_TAGS,
                             '-run=^(' + '|'.join(cases) + ')$', './internal/directlan'])
            self.assertEqual(timeout, '90s' if suite in ORDER[:2] else '120s')
            pattern = command[-2][5:]
            for name in cases:
                self.assertIsNotNone(re.fullmatch(pattern, name))
                self.assertIsNone(re.search(pattern, name + 'Extra'))
                self.assertIsNone(re.search(pattern, 'Extra' + name))

    def test_driver_is_offline_bounded_and_preserves_product_flags(self):
        for suite in ORDER:
            with mock.patch.object(gate.subprocess, 'run', return_value=mock.Mock(returncode=0)) as run:
                before = dict(os.environ)
                self.assertEqual(gate.main([suite]), 0)
                run.assert_called_once_with(gate.command(suite), env=dict(before, GOPROXY='off', GOSUMDB='off', GOTOOLCHAIN='local'), check=False, shell=False, timeout=180)
                self.assertEqual(dict(os.environ), before)

    def test_failures_and_timeouts_cannot_pass_or_leak_details(self):
        for code, expected in ((1, 1), (23, 23), (-15, 143)):
            with mock.patch.object(gate.subprocess, 'run', return_value=mock.Mock(returncode=code)):
                self.assertEqual(gate.main(['tls']), expected)
        for error, expected in ((OSError('private'), 1), (gate.subprocess.TimeoutExpired('private', 180), 1), (KeyboardInterrupt(), 130)):
            with mock.patch.object(gate.subprocess, 'run', side_effect=error), mock.patch.object(gate.sys, 'stderr', io.StringIO()) as stderr:
                self.assertEqual(gate.main(['tls']), expected)
                self.assertNotIn('private', stderr.getvalue())

    def test_invalid_arguments_never_execute(self):
        for args in ([], ['build'], ['tls', './...'], ['tls', '--', 'go', 'test'], ['tls', '-tags=extra']):
            with mock.patch.object(gate.subprocess, 'run') as run, mock.patch.object(gate.sys, 'stderr', io.StringIO()), self.assertRaises(SystemExit) as error:
                gate.main(args)
            self.assertEqual(error.exception.code, 2)
            run.assert_not_called()

    def test_both_native_workflows_are_ordered_required_and_bounded(self):
        for workflow in ('ci', 'prerelease'):
            source = (ROOT / '.github/workflows' / (workflow + '.yml')).read_text(encoding="utf-8")
            native = re.search(r'^  native:\n(.*?)(?=^  [\w-]+:|\Z)', source, re.M | re.S)[0]
            offsets = []
            for suite in ORDER:
                invocation = 'python .github/scripts/ci-managed-session.py ' + suite
                self.assertEqual(source.count(invocation), 1)
                steps = re.findall(r'^      - .*?(?=^      - |\Z)', native, re.M | re.S)
                step, = [step for step in steps if invocation in step]
                self.assertIn('timeout-minutes: 4', step)
                self.assertIn('set -euo pipefail', step)
                self.assertNotIn('        if:', step)
                self.assertNotIn('continue-on-error', step)
                self.assertLess(native.index('go test -race -count=1 -timeout=10m ./...'), native.index(step))
                self.assertLess(native.index(step), native.index('go run ./cmd/package-tool build'))
                offsets.append(native.index(step))
                if workflow == 'ci':
                    self.assertIn('ci-metrics.py run --suite managed-session-' + suite + ' -- ' + invocation, step)
                else:
                    self.assertIn('2>&1 | tee logs/managed-session-' + suite + '.txt', step)
            self.assertEqual(offsets, sorted(offsets))

    def test_tag_exclusions_are_exact_and_product_flags_unchanged(self):
        allowed_scripts = {SCRIPT, Path(__file__)}
        allowed_bytecode = {Path(importlib.util.cache_from_source(str(p))) for p in allowed_scripts}
        for tag, files in FILES.items():
            allowed = allowed_scripts | allowed_bytecode | {ROOT / 'internal/directlan' / name for name in files}
            for folder in (ROOT / '.github', ROOT / 'internal', ROOT / 'cmd'):
                for path in folder.rglob('*'):
                    if path.is_file() and path not in allowed and path.suffix in ('.go', '.py', '.pyc', '.yml', '.yaml'):
                        self.assertNotIn(tag.encode(), path.read_bytes(), str(path.relative_to(ROOT)))
        for name in ('ci', 'prerelease'):
            source = (ROOT / '.github/workflows' / (name + '.yml')).read_text(encoding="utf-8")
            self.assertIn('GOFLAGS: -mod=readonly -tags=' + gate.PRODUCT_TAGS + '\n', source)

    def test_plain_synthetic_tests_stay_untagged(self):
        for filename in ('managed_session_test.go', 'managed_session_admission_test.go', 'admission_test.go'):
            source = (ROOT / 'internal/directlan' / filename).read_text(encoding="utf-8")
            self.assertNotIn('//go:build', source)


if __name__ == '__main__':
    unittest.main()
