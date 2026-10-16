"""Socket-free contract check for the installed CLI smoke's private UI probe."""
import contextlib
import importlib.util
import io
import json
import pathlib
import subprocess
import unittest
from unittest.mock import Mock, patch


PATH = pathlib.Path(__file__).with_name('offline-smoke.py')
spec = importlib.util.spec_from_file_location('offline_smoke_ui_contract', PATH)
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class WebCheckReached(Exception):
    pass


class OfflineSmokeUIContractTests(unittest.TestCase):
    def test_captured_probe_explicitly_requests_private_json(self):
        # No executable, socket, browser or real credential is created.
        ui = {'url': 'http://127.0.0.1:43210', 'code': 'fictional-private-ui-code'}
        probes = []
        process = Mock()
        process.poll.return_value = None
        process.communicate.return_value = ('', '')

        def run(argv, **kwargs):
            if 'ui' in argv:
                self.assertEqual(argv[argv.index('ui'):], ['ui', '--json'])
                self.assertIs(kwargs.get('capture_output'), True)
                self.assertIs(kwargs.get('text'), True)
                self.assertEqual(kwargs.get('timeout'), 5)
                probes.append(argv)
                return subprocess.CompletedProcess(argv, 0, json.dumps(ui), '')
            if 'status' in argv:
                body = {'settings': {'network': 'none'}, 'self': {'status': 'idle'},
                        'peers': [], 'services': []}
                return subprocess.CompletedProcess(argv, 0, json.dumps(body), '')
            # The foreground process is already running at this point.
            return subprocess.CompletedProcess(argv, 1 if 'start' in argv else 0, '', '')

        output, errors = io.StringIO(), io.StringIO()
        with patch.object(smoke, 'check_locales'), \
             patch.object(smoke.subprocess, 'Popen', return_value=process), \
             patch.object(smoke.subprocess, 'run', side_effect=run), \
             patch.object(smoke, 'check_web', side_effect=WebCheckReached) as web, \
             contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            with self.assertRaises(WebCheckReached):
                smoke.check('fictional-soba')
        self.assertEqual(len(probes), 1)
        self.assertEqual(web.call_args.args[0], ui)
        self.assertNotIn(ui['code'], output.getvalue() + errors.getvalue())
        self.assertNotIn(ui['url'], output.getvalue() + errors.getvalue())
        process.kill.assert_called_once()
        process.communicate.assert_called_once_with(timeout=5)


if __name__ == '__main__':
    unittest.main()
