import json
import unittest
from run_operations import TESTS, summarize

class SummaryTests(unittest.TestCase):
    def output(self):
        return '\n'.join('--- PASS: '+name+' (1.00s)' for name in TESTS)+'\n12 ordered rounds across a real two-minute lease\n'
    def test_success(self):
        self.assertTrue(summarize(self.output(),0,'synthetic-hash')['passed'])
    def test_missing_case(self):
        self.assertFalse(summarize(self.output().replace('--- PASS: '+TESTS[0], '--- SKIP: '+TESTS[0]),0,'synthetic-hash')['passed'])
    def test_nonzero(self):
        self.assertFalse(summarize(self.output(),1,'synthetic-hash')['passed'])
    def test_lease_marker_required(self):
        self.assertFalse(summarize(self.output().split('\n12')[0],0,'synthetic-hash')['passed'])
    def test_skip_even_with_pass_marker(self):
        self.assertFalse(summarize(self.output()+'--- SKIP: '+TESTS[0],0,'synthetic-hash')['passed'])
    def test_private_output_never_serialized(self):
        marker='synthetic-private-capability-must-not-be-published'
        result=summarize(self.output()+marker,0,'synthetic-hash')
        self.assertNotIn(marker,json.dumps(result))
        self.assertFalse(result['physical_lan_proven'])
        self.assertFalse(result['os_login_or_suspend_proven'])
        self.assertFalse(result['automatic_remote_deployment_proven'])
        self.assertFalse(result['automatic_election_proven'])

if __name__ == '__main__':
    unittest.main()
