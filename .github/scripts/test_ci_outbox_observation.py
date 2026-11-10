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
                         {'int', 'time.Duration', 'int64', 'outboxProcessWaitObservation'})
        summary = source.split('func (o outboxProcessCommitObservation) summary(', 1)[1].split('\nfunc Test', 1)[0]
        self.assertIn('phase != "initial" && phase != "replay"', summary)
        self.assertIn('phase = "other"', summary)
        self.assertIn('entry := "file"', summary)
        self.assertIn('entry = "empty-directory"', summary)
        self.assertEqual(summary.count('fmt.Sprintf('), 2)
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
			if reply.OutboxCommitWait != nil {
				observation.wait.add(*reply.OutboxCommitWait)
			}
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
			var stopObservation func() outboxProcessWaitObservation
			if request.Name == "outbox.commit" {
				stopObservation = observeOutboxProcessWait()
				commandStarted = time.Now()
			}
			value, callErr = c.Command(call, webui.Command{RequestID: requestID, Name: request.Name, Payload: request.Payload})
			if !commandStarted.IsZero() {
				elapsed := time.Since(commandStarted).Milliseconds()
				outboxCommitMS = &elapsed
				joinStarted := time.Now()
				observed := stopObservation()
				observed.JoinUS = time.Since(joinStarted).Microseconds()
				outboxCommitWait = &observed
			}
			stop()'''
        self.assertIn(expected, source)
        self.assertRegex(source, r'OutboxCommitMS\s+\*int64\s+`json:",omitempty"`')
        self.assertIn('reply := processReply{Value: raw, OutboxCommitMS: outboxCommitMS, OutboxCommitWait: outboxCommitWait}', source)
        self.assertIn('context.WithTimeout(ctx, 30*time.Second)', source)
        self.assertIn('case <-time.After(40 * time.Second):', source)

    def test_sampler_is_bounded_test_only_and_scalar(self):
        source = self.source('outbox_process_wait_test.go')
        fields = source.split('type outboxProcessWaitObservation struct {', 1)[1].split('}', 1)[0]
        self.assertEqual(set(line.split()[-1] for line in fields.splitlines() if line.strip()), {'int', 'int64'})
        sampler = source.split('func observeOutboxProcessWait()', 1)[1].split('\nfunc Test', 1)[0]
        self.assertIn('const outboxWaitProfileLimit = 4096', source)
        self.assertIn('100 * time.Millisecond, 400 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond', sampler)
        self.assertIn('observeOutboxProcessWaitUsing(runtime.GoroutineProfile)', sampler)
        self.assertIn('n, ok := profile(records)', sampler)
        self.assertIn('case <-stop:', sampler)
        self.assertIn('<-done', sampler)
        for forbidden in ('runtime.Stack(', 'pprof.', 'unsafe.', 'SetMutexProfileFraction', 'SetBlockProfileRate', 'TryLock(', 'TryRLock(', 'fmt.', 'os.', 'net.'):
            self.assertNotIn(forbidden, source)
        for name in ('ObservedRealLocks', 'UnavailableAndMissing', 'QuickStop', 'SampleLimit', 'StopJoinsActiveProfile', 'RejectsMultipleCommands'):
            self.assertIn('func TestOutboxProcessWait' + name + '(', source)
        self.assertIn('mutex && op', source)
        self.assertIn('commands != 1', source)

    def test_retry_control_flow_is_unchanged(self):
        source = self.source('outbox_process_retry_test.go')
        helper = source.split('func retryOutboxProcessCommit(', 1)[1].split('\nfunc Test', 1)[0]
        # Pin only the unchanged retry helper, not its newly added tests. A
        # future retry-policy change must be reviewed separately from telemetry.
        self.assertEqual(hashlib.sha256(helper.encode()).hexdigest(), 'd330d2605535c15866a1e7c2ed8199a0a83fa40ebe97d2faa0a908c71651982e')
        self.assertIn('func TestOutboxProcessCommitRetryObservedResults(', source)


if __name__ == '__main__':
    unittest.main()
