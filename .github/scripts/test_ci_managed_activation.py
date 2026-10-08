"""Pure source, selector, environment and workflow acceptance policy checks."""
import importlib.util
import io
import os
from pathlib import Path
import re
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = Path(__file__).with_name('ci-managed-activation.py')
SPEC = importlib.util.spec_from_file_location('ci_managed_activation', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)
FILES = {'activation': 'internal/core/direct_lan_activation_native_test.go',
         'restart': 'cmd/soba/upgrade_restart_acceptance_test.go'}


def inventory(source):
    found = {}
    for name, body in re.findall(r'^func (Test\w+)\(t \*testing.T\) \{\n(.*?)(?=^func |\Z)', source, re.M | re.S):
        runs = re.findall(r'\bt\.Run\((.*?), func\(t \*testing.T\)', body)
        children = ()
        if runs:
            if runs != ['mode']:
                raise ValueError('unreviewed subtest grammar')
            tables = re.findall(r'for _, mode := range \[\]string\{([^}]+)\}', body)
            if len(tables) != 1:
                raise ValueError('unreviewed subtest table')
            children = tuple(re.findall(r'"([^\"]+)"', tables[0]))
            if not children or len(children) != len(set(children)):
                raise ValueError('empty or duplicate subtests')
        if name in found:
            raise ValueError('duplicate test')
        found[name] = children
    if len(re.findall(r'\bt\.Run\(', source)) != sum(bool(v) for v in found.values()):
        raise ValueError('subtest outside reviewed body')
    return found


class ManagedActivationPolicyTests(unittest.TestCase):
    def test_exact_source_inventory_tags_and_helper(self):
        for suite, path in FILES.items():
            source = (ROOT / path).read_text(encoding='utf-8')
            actual = inventory(source)
            tag = gate.SUITES[suite][1]
            expected_tag = tag if suite == 'activation' else tag + ' && (linux || darwin || windows)'
            self.assertEqual(source.splitlines()[0], '//go:build ' + expected_tag)
            if suite == 'restart':
                self.assertEqual(actual.pop('TestManagedRestartChildFixture'), ())
                self.assertIn('"-test.run=^TestManagedRestartChildFixture$"', source)
            self.assertEqual(actual, gate.SUITES[suite][3])
            self.assertEqual((len(actual), sum(map(len, actual.values()))), (7, 6) if suite == 'activation' else (6, 11))

    def test_exact_expectations_and_closed_selectors(self):
        for suite, (package, tag, timeout, cases) in gate.SUITES.items():
            cmd = gate.command(suite)
            cut = cmd.index('--')
            expected = ['github.com/webkaz-labs/sobalink/' + package + ':' + name + suffix
                        for name, children in cases.items()
                        for suffix in ('', *('/' + child for child in children))]
            self.assertEqual(cmd[2:cut:2], ['--expect'] * len(expected))
            self.assertEqual(cmd[3:cut:2], expected)
            self.assertEqual(len(expected), len(set(expected)))
            self.assertEqual(cmd[cut+1:], ['go', 'test', '-race', '-count=1', '-v', '-timeout=' + timeout,
                                          '-tags=' + tag + ',' + gate.PRODUCT_TAGS,
                                          '-run=^(' + '|'.join(cases) + ')$', './' + package])
            for name in cases:
                self.assertTrue(re.fullmatch(cmd[-2][5:], name))
                self.assertIsNone(re.search(cmd[-2][5:], name + 'Extra'))
            self.assertNotIn('TestManagedRestartChildFixture', cmd[-2])

    def test_driver_environment_and_private_temp(self):
        for suite in gate.SUITES:
            before = dict(os.environ)
            with mock.patch.dict(os.environ, GITHUB_ACTIONS='true'), mock.patch.object(gate.tempfile, 'mkdtemp', return_value='/synthetic/sa-fixture') as mkdir, mock.patch.object(gate.shutil, 'rmtree') as cleanup, mock.patch.object(gate.subprocess, 'run', return_value=mock.Mock(returncode=0)) as run:
                self.assertEqual(gate.main([suite]), 0)
                mkdir.assert_called_once_with(prefix='sa-', dir=None if os.name == 'nt' else '/tmp')
                kwargs = run.call_args.kwargs
                self.assertEqual(run.call_args.args, (gate.command(suite),))
                self.assertNotIn('timeout', kwargs)
                self.assertFalse(kwargs['shell'])
                env = kwargs['env']
                for key in ('TMPDIR', 'TMP', 'TEMP'):
                    self.assertEqual(env[key], '/synthetic/sa-fixture')
                for key in ('GOPROXY', 'GOSUMDB', 'GOTELEMETRY'):
                    self.assertEqual(env[key], 'off')
                self.assertEqual(env['GOTOOLCHAIN'], 'local')
                self.assertFalse(any(k.upper().startswith('TS_') or k.upper() in ('HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY') for k in env))
                if suite == 'activation':
                    self.assertEqual(env['SOBALINK_RUN_ACTIVATION_NATIVE'], '1')
                cleanup.assert_called_once_with('/synthetic/sa-fixture')
            self.assertEqual(dict(os.environ), before)

    def test_interruption_and_failure_retain_possible_live_child_files(self):
        for outcome, expected in ((OSError('private'), 1), (KeyboardInterrupt(), 130), (1, 1), (-15, 143)):
            with mock.patch.dict(os.environ, GITHUB_ACTIONS='true'), mock.patch.object(gate.tempfile, 'mkdtemp', return_value='/synthetic/sa-fixture'), mock.patch.object(gate.shutil, 'rmtree') as cleanup, mock.patch.object(gate.subprocess, 'run') as run, mock.patch.object(gate.sys, 'stderr', io.StringIO()) as stderr:
                if isinstance(outcome, BaseException):
                    run.side_effect = outcome
                else:
                    run.return_value.returncode = outcome
                self.assertEqual(gate.main(['restart']), expected)
                cleanup.assert_not_called()
                self.assertNotIn('private', stderr.getvalue())
                self.assertIn('retained for runner cleanup', stderr.getvalue())
                self.assertNotIn('timeout', run.call_args.kwargs)

    def test_requires_bounded_ci_context_and_rejects_unknown_arguments(self):
        with mock.patch.dict(os.environ, GITHUB_ACTIONS='false'), mock.patch.object(gate.subprocess, 'run') as run, mock.patch.object(gate.tempfile, 'mkdtemp') as mkdir, mock.patch.object(gate.sys, 'stderr', io.StringIO()):
            self.assertEqual(gate.main(['restart']), 2)
            run.assert_not_called()
            mkdir.assert_not_called()
        for args in ([], ['all'], ['restart', './...']):
            with mock.patch.object(gate.subprocess, 'run') as run, mock.patch.object(gate.sys, 'stderr', io.StringIO()), self.assertRaises(SystemExit):
                gate.main(args)
            run.assert_not_called()

    def test_both_native_workflows_require_both_gates(self):
        for workflow in ('ci', 'prerelease'):
            source = (ROOT / '.github/workflows' / (workflow + '.yml')).read_text(encoding='utf-8')
            native = source.split('  native:\n', 1)[1].split('    steps:', 1)[0]
            self.assertIn('    timeout-minutes: 60', native)
            for suite, bound in (('activation', 10), ('restart', 4)):
                step = source.split('      - name: Verify managed ' + suite + ' acceptance\n')[1].split('      - name:')[0]
                self.assertIn('timeout-minutes: ' + str(bound), step)
                self.assertIn('python .github/scripts/ci-managed-activation.py ' + suite, step)
                self.assertNotIn('continue-on-error', step)
                self.assertNotIn('\n        if:', step)
            for runner in ('ubuntu-24.04', 'ubuntu-24.04-arm', 'macos-26', 'windows-2025'):
                self.assertIn(runner, source)


if __name__ == '__main__':
    unittest.main()
