import contextlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import prepare_engine
import run_native


class ToolsTest(unittest.TestCase):
    def test_source_hash_mismatch_fails_before_copy(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp)
            source=root/'source/wgengine/magicsock'
            source.mkdir(parents=True)
            (source/'rebinding_conn.go').write_text('wrong source')
            destination=root/'destination'
            with self.assertRaises(SystemExit):
                prepare_engine.prepare(root/'source',destination)
            self.assertFalse(destination.exists())

    def run_report(self, output, returncode):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp); binary=root/'binary'; binary.write_bytes(b'synthetic fixture')
            report=root/'summary.json'
            stdout=io.StringIO()
            result=subprocess.CompletedProcess([],returncode,output)
            raised=False
            with patch.object(sys,'argv',['run_native.py','--binary',str(binary),'--report',str(report)]),patch('run_native.subprocess.run',return_value=result),contextlib.redirect_stdout(stdout):
                try:
                    run_native.main()
                except SystemExit:
                    raised=True
            self.assertNotIn('synthetic-private-marker',stdout.getvalue())
            data=json.loads(report.read_text())
            self.assertNotIn('synthetic-private-marker',report.read_text())
            return data,raised

    def test_pass_reports_only_allowlisted_aggregate(self):
        output='\n'.join('--- PASS: '+name+' (0.01s)' for name in run_native.TESTS)+'\nnative_batching=true\nnative_allowed_datagrams=7 native_denied_entrypoints=5 native_policy_revocations=1\nsynthetic-private-marker'
        data,raised=self.run_report(output,0)
        self.assertTrue(data['passed']); self.assertFalse(raised)
        self.assertFalse(data['host_no_egress_proven']); self.assertEqual(data['allowed_datagrams'],7)

    def test_skip_or_missing_native_test_fails(self):
        output='\n'.join('--- PASS: '+name+' (0.01s)' for name in run_native.TESTS[:-1])+'\n--- SKIP: '+run_native.TESTS[-1]+' (0.01s)'
        data,raised=self.run_report(output,0)
        self.assertFalse(data['passed']); self.assertTrue(raised)

    def test_failure_does_not_publish_raw_output(self):
        data,raised=self.run_report('--- FAIL: TestLANExperimentNativeLoopback (0.01s)\nsynthetic-private-marker',1)
        self.assertFalse(data['passed']); self.assertTrue(raised)

if __name__=='__main__':unittest.main()
