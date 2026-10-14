import contextlib
import importlib.util
import io
from pathlib import Path
import sys
import unittest

spec = importlib.util.spec_from_file_location('wire', Path(__file__).with_name('ci-text-wire-diagnostic.py'))
wire = importlib.util.module_from_spec(spec)
spec.loader.exec_module(wire)

class PrivateCaptureTests(unittest.TestCase):
    def check_run(self, code):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            rc = wire.run_private([sys.executable, '-c', code])
        return rc, out.getvalue()

    def test_filters_both_streams_and_preserves_failure(self):
        rc, out = self.check_run("import sys; print('SECRET'); print('PRIVATE',file=sys.stderr); print('    test.go:42: SOBA_TEXT_WIRE operation=status stage=peer_json_returned category=deadline context=deadline'); print('    test.go:1: SOBA_TEXT_WIRE operation=SECRET stage=peer_json_returned category=none context=none'); print('    test.go:1: SOBA_TEXT_REPLAY index=0 operation=status stage=begin SECRET'); sys.exit(3)")
        self.assertEqual(rc, 1)
        self.assertNotIn('SECRET', out)
        self.assertNotIn('PRIVATE', out)
        self.assertIn('category=deadline context=deadline', out)
        self.assertIn('status=failed', out)

    def test_pass_and_replay_record(self):
        rc, out = self.check_run("print('    test.go:2: SOBA_TEXT_REPLAY index=3 operation=status stage=begin')")
        self.assertEqual(rc, 0)
        self.assertIn('index=3', out)
        self.assertIn('status=passed', out)

    def test_missing_command_safe(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            rc = wire.run_private(['/nonexistent/PRIVATE'])
        self.assertEqual(rc, 1)
        self.assertNotIn('PRIVATE', out.getvalue())

if __name__ == '__main__':
    unittest.main()
