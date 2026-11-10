package core

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Test-only counters retain no request, path, identity, error or wall-clock
// timestamp. Recording a completed invocation never retries or changes it.
type outboxProcessCommitObservation struct {
	attempts, completed, busy, childSamples int
	lastInvoke, maxInvoke                   time.Duration
	maxCommandMS                            int64
	wait                                    outboxProcessWaitObservation
}

func (o *outboxProcessCommitObservation) start() { o.attempts++ }

func (o *outboxProcessCommitObservation) record(code string, elapsed time.Duration, commandMS *int64) {
	o.completed++
	if code == "outbox.commit: outbox_storage_busy" {
		o.busy++
	}
	o.lastInvoke = elapsed
	if elapsed > o.maxInvoke {
		o.maxInvoke = elapsed
	}
	if commandMS != nil && *commandMS >= 0 {
		o.childSamples++
		if *commandMS > o.maxCommandMS {
			o.maxCommandMS = *commandMS
		}
	}
}

func (o outboxProcessCommitObservation) summary(phase string, directory bool, elapsed time.Duration) string {
	if phase != "initial" && phase != "replay" {
		phase = "other"
	}
	entry := "file"
	if directory {
		entry = "empty-directory"
	}
	summary := fmt.Sprintf("outbox commit observation: phase=%s entry=%s attempt_count=%d completed_count=%d busy_count=%d elapsed_ms=%d last_invoke_ms=%d max_invoke_ms=%d child_samples=%d max_command_ms=%d", phase, entry, o.attempts, o.completed, o.busy, elapsed.Milliseconds(), o.lastInvoke.Milliseconds(), o.maxInvoke.Milliseconds(), o.childSamples, o.maxCommandMS)
	if o.wait.Samples > 0 || o.wait.JoinUS > 0 {
		summary += fmt.Sprintf(" wait_samples=%d wait_unavailable=%d wait_command_missing=%d wait_core_op=%d wait_core_mu=%d wait_outbox_mu=%d wait_other=%d maintenance_refresh_samples=%d receiver_maintenance_samples=%d sampler_work_us=%d sampler_join_us=%d", o.wait.Samples, o.wait.Unavailable, o.wait.CommandMissing, o.wait.CoreOp, o.wait.CoreMu, o.wait.OutboxMu, o.wait.Other, o.wait.MaintenanceRefresh, o.wait.ReceiverMaintenance, o.wait.SamplerUS, o.wait.JoinUS)
	}
	return summary
}

func TestOutboxProcessCommitObservationClosedCounters(t *testing.T) {
	var o outboxProcessCommitObservation
	child := int64(7)
	o.start()
	o.record("outbox.commit: outbox_storage_busy", 8*time.Millisecond, &child)
	child = 2
	o.start()
	o.record("", 3*time.Millisecond, &child)
	want := "outbox commit observation: phase=initial entry=file attempt_count=2 completed_count=2 busy_count=1 elapsed_ms=31 last_invoke_ms=3 max_invoke_ms=8 child_samples=2 max_command_ms=7"
	if got := o.summary("initial", false, 31*time.Millisecond); got != want {
		t.Fatal("commit observation lost its fixed counters")
	}
	child = -1
	for _, code := range []string{"outbox_storage_busy", "outbox.commit: outbox_storage_busy extra", "outbox.cancel: outbox_storage_busy", "synthetic/private-value"} {
		o.start()
		o.record(code, time.Millisecond, &child)
	}
	o.start()
	o.record("", time.Millisecond, nil)
	if o.attempts != 7 || o.completed != 7 || o.busy != 1 || o.childSamples != 2 || o.maxCommandMS != 7 {
		t.Fatal("observation accepted a noncanonical busy code or invalid child duration")
	}
	got := o.summary("synthetic/private-value\ninitial", true, time.Second)
	if !strings.HasPrefix(got, "outbox commit observation: phase=other entry=empty-directory ") || strings.Contains(got, "synthetic") || strings.Contains(got, "\n") {
		t.Fatal("free-form content escaped the fixed observation")
	}
	if !strings.Contains(o.summary("replay", false, time.Second), "phase=replay") {
		t.Fatal("replay phase was not retained")
	}
}

func TestOutboxProcessCommitObservationUnfinishedAttempt(t *testing.T) {
	var o outboxProcessCommitObservation
	o.start()
	want := "outbox commit observation: phase=initial entry=file attempt_count=1 completed_count=0 busy_count=0 elapsed_ms=40000 last_invoke_ms=0 max_invoke_ms=0 child_samples=0 max_command_ms=0"
	if o.summary("initial", false, 40*time.Second) != want {
		t.Fatal("unfinished invocation was reported as completed")
	}
}

func TestOutboxProcessCommitObservationWaitSummary(t *testing.T) {
	var o outboxProcessCommitObservation
	o.wait = outboxProcessWaitObservation{Samples: 4, Unavailable: 1, CoreOp: 2, CoreMu: 1, MaintenanceRefresh: 3, ReceiverMaintenance: 1, SamplerUS: 1234, JoinUS: 17}
	want := " wait_samples=4 wait_unavailable=1 wait_command_missing=0 wait_core_op=2 wait_core_mu=1 wait_outbox_mu=0 wait_other=0 maintenance_refresh_samples=3 receiver_maintenance_samples=1 sampler_work_us=1234 sampler_join_us=17"
	if !strings.HasSuffix(o.summary("initial", false, time.Second), want) {
		t.Fatal("fixed wait counters or separate observer costs changed")
	}
}
