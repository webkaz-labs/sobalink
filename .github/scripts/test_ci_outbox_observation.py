"""Keep normal-CI outbox observations scalar and independent of acceptance."""
from pathlib import Path
import hashlib
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]


class OutboxObservationTests(unittest.TestCase):
    def source(self, name):
        return (ROOT / 'internal/core' / name).read_text(encoding='utf-8')

    def test_only_scalar_counter_state_and_fixed_summary(self):
        source = self.source('outbox_process_observation_test.go')
        fields = source.split('type outboxProcessCommitObservation struct {', 1)[1].split('}', 1)[0]
        self.assertEqual(set(line.split()[-1] for line in fields.splitlines() if line.strip()),
                         {'int', 'time.Duration', 'int64'})
        summary = source.split('func (o outboxProcessCommitObservation) summary(', 1)[1].split('\nfunc Test', 1)[0]
        self.assertIn('phase != "initial" && phase != "replay"', summary)
        self.assertIn('phase = "other"', summary)
        self.assertIn('entry := "file"', summary)
        self.assertIn('entry = "empty-directory"', summary)
        self.assertEqual(summary.count('fmt.Sprintf('), 1)
        for forbidden in ('runtime.', 'pprof.', 'os.', 'net.', 'go func(', 'time.Sleep(', 'time.NewTimer(', 'json.', '[]byte'):
            self.assertNotIn(forbidden, source)

    def test_parent_observes_and_returns_the_original_reply(self):
        source = self.source('outbox_process_integration_test.go')
        self.assertIn('context.WithTimeout(context.Background(), 3*time.Minute)', source)
        expected = '''reply, code, err := retryOutboxProcessCommit(ctx, func() (json.RawMessage, string) {
			observation.start()
			invoked := time.Now()
			reply := peers[0].invokeResult(t, "outbox.commit", payload)
			observation.record(reply.Error, time.Since(invoked), reply.OutboxCommitMS)
			return reply.Value, reply.Error
		})'''
        self.assertIn(expected, source)
        self.assertIn('defer func() { t.Log(observation.summary(phase, directory, time.Since(started))) }()', source)
        self.assertIn('if err != nil || code != "" {', source)
        self.assertLess(source.index('defer func() { t.Log(observation.summary('), source.index('reply, code, err := retryOutboxProcessCommit('))
        self.assertIn('return catalogProcessDecode[browserOutboxRecord](t, reply)', source)
        self.assertEqual(source.count('observation.record('), 1)
        self.assertEqual(source.count('observation.summary('), 1)

    def test_child_times_only_the_existing_outbox_command(self):
        source = self.source('route_process_integration_test.go')
        expected = '''var commandStarted time.Time
			if request.Name == "outbox.commit" {
				commandStarted = time.Now()
			}
			value, callErr = c.Command(call, webui.Command{RequestID: requestID, Name: request.Name, Payload: request.Payload})
			if !commandStarted.IsZero() {
				elapsed := time.Since(commandStarted).Milliseconds()
				outboxCommitMS = &elapsed
			}
			stop()'''
        self.assertIn(expected, source)
        self.assertRegex(source, r'OutboxCommitMS\s+\*int64 `json:",omitempty"`')
        self.assertIn('reply := processReply{Value: raw, OutboxCommitMS: outboxCommitMS}', source)
        self.assertIn('context.WithTimeout(ctx, 30*time.Second)', source)
        self.assertIn('case <-time.After(40 * time.Second):', source)

    def test_retry_control_flow_is_unchanged(self):
        source = self.source('outbox_process_retry_test.go')
        helper = source.split('func retryOutboxProcessCommit(', 1)[1].split('\nfunc Test', 1)[0]
        # Pin only the unchanged retry helper, not its newly added tests. A
        # future retry-policy change must be reviewed separately from telemetry.
        self.assertEqual(hashlib.sha256(helper.encode()).hexdigest(), 'd330d2605535c15866a1e7c2ed8199a0a83fa40ebe97d2faa0a908c71651982e')
        self.assertIn('func TestOutboxProcessCommitRetryObservedResults(', source)


if __name__ == '__main__':
    unittest.main()
