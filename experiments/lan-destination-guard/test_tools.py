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
import prepare_remaining
import run_native

class ToolsTest(unittest.TestCase):
    def test_source_hash_mismatch_fails_before_copy(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);source=root/'source/wgengine/magicsock';source.mkdir(parents=True)
            (source/'rebinding_conn.go').write_text('wrong source')
            with self.assertRaises(SystemExit):prepare_engine.prepare(root/'source',root/'destination')
            self.assertFalse((root/'destination').exists())
    def test_every_remaining_source_hash_is_required(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp)
            for path in prepare_remaining.PINS:
                p=root/path;p.parent.mkdir(parents=True,exist_ok=True);p.write_text('wrong source')
            with self.assertRaises(SystemExit):prepare_remaining.verify(root)
    def report(self, success=True, skipped=False):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);binary=root/'binary';binary.write_bytes(b'synthetic fixture');report=root/'summary.json'
            argv=['run_native.py','--platform','linux','--architecture','amd64','--report',str(report)]
            results=[]
            for group,tests in run_native.TESTS.items():
                argv+=['--'+group+'-binary',str(binary)]
                tests=tests+(['TestLANRemainingRawDiscoDisabled'] if group=='udp' else [])
                output='\n'.join('--- PASS: '+name+' (0.01s)' for name in tests)+'\n'+'\n'.join(run_native.MARKERS[group])+'\nnative_batching=true\nsynthetic-private-marker'
                if skipped:output=output.replace('--- PASS: '+tests[-1],'--- SKIP: '+tests[-1])
                results.append(subprocess.CompletedProcess([],0 if success else 1,output))
            stdout=io.StringIO();raised=False
            with patch.object(sys,'argv',argv),patch('run_native.subprocess.run',side_effect=results),contextlib.redirect_stdout(stdout):
                try:run_native.main()
                except SystemExit:raised=True
            self.assertNotIn('synthetic-private-marker',stdout.getvalue()+report.read_text())
            return json.loads(report.read_text()),raised
    def test_pass_reports_only_allowlisted_aggregate(self):
        data,raised=self.report();self.assertTrue(data['passed']);self.assertFalse(raised)
        self.assertFalse(data['host_no_egress_proven']);self.assertFalse(data['physical_lan_proven']);self.assertFalse(data['icmp_transmission_tested'])
    def test_skip_is_not_pass(self):
        data,raised=self.report(skipped=True);self.assertFalse(data['passed']);self.assertTrue(raised)
    def test_failure_does_not_publish_raw_output(self):
        data,raised=self.report(success=False);self.assertFalse(data['passed']);self.assertTrue(raised)
if __name__=='__main__':unittest.main()
