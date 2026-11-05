package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

// Exercise the real commit and existing retry helper with controlled ownership.
// Channels hold the owner through the unchanged two-second deadline. No wider
// deadline, network retries or acceptance changes are needed.
func TestOutboxAdmissionBusyReproductionPreservesEvidence(t *testing.T) {
	c := outboxFixture(t)
	review := outboxPrepareFixture(t, c, "synthetic-busy-reproduction")
	o := c.outbox
	before, err := json.Marshal(o.state)
	if err != nil {
		t.Fatal("synthetic state encoding failed")
	}
	path := filepath.Join(c.dir, outboxNamespace, "state.json")
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("synthetic state read failed")
	}
	prepared := o.prepared[review.OutboxID]
	certified := append([]byte(nil), o.confirmedDisk...)
	release := holdOutboxLifecycle(t, c)
	defer release()
	calls := 0
	_, code, err := retryOutboxProcessCommit(context.Background(), func() (json.RawMessage, string) {
		calls++
		value, err := c.commitOutbox(context.Background(), review.OutboxID, review.ProcessID, review.ReviewRevision, true)
		if value != nil || networkErrorCode(err) != "outbox_storage_busy" {
			t.Fatal("held ownership did not refuse before admission")
		}
		return nil, "outbox.commit: " + networkErrorCode(err)
	})
	if !errors.Is(err, context.DeadlineExceeded) || code != "outbox.commit: outbox_storage_busy" || calls < 2 {
		t.Fatal("controlled contention escaped the unchanged retry budget")
	}
	after, err := json.Marshal(o.state)
	if err != nil {
		t.Fatal("synthetic state encoding failed")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("synthetic state read failed")
	}
	if !bytes.Equal(before, after) || !bytes.Equal(disk, current) || !bytes.Equal(certified, o.confirmedDisk) || o.prepared[review.OutboxID] != prepared || prepared.ctx.Err() != nil || o.frozen != "" {
		t.Fatal("refused attempts changed preparation or durable evidence")
	}
}

// The retry budget includes command latency. One late refusal cannot establish
// that lifecycle ownership was held continuously for that whole budget.
func TestOutboxAdmissionSlowCommandThenSingleBusyReproduction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		started := time.Now()
		_, code, err := retryOutboxProcessCommit(context.Background(), func() (json.RawMessage, string) {
			calls++
			// This models command latency before a single refusal. It is not
			// a claim that any particular production holder caused that delay.
			time.Sleep(2*time.Second + time.Millisecond)
			return nil, "outbox.commit: outbox_storage_busy"
		})
		if !errors.Is(err, context.DeadlineExceeded) || code != "outbox.commit: outbox_storage_busy" || calls != 1 || time.Since(started) != 2*time.Second+time.Millisecond {
			t.Fatal("slow command no longer explains a single terminal busy result")
		}
	})
}

// A lifecycle holder can need the narrower coordinator lock. Blocking lifecycle
// acquisition while retaining that lock would turn a safe refusal into a cycle.
func TestOutboxAdmissionTryLockBreaksControlledInversion(t *testing.T) {
	c := outboxFixture(t)
	o := c.outbox
	o.mu.Lock()
	coordinatorLocked := true
	defer func() {
		if coordinatorLocked {
			o.mu.Unlock()
		}
	}()
	entered, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- c.resourceLock.WithOwnership(c.dir, func() error {
			close(entered)
			o.mu.Lock()
			o.mu.Unlock()
			return nil
		})
	}()
	<-entered
	callbackRan := false
	err := o.withOwnedLocked(func() error { callbackRan = true; return nil })
	if callbackRan || networkErrorCode(err) != "outbox_storage_busy" || o.owned != nil || o.frozen != "" {
		t.Fatal("contended ownership did not refuse without entering or freezing")
	}
	o.mu.Unlock()
	coordinatorLocked = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("controlled lifecycle holder failed")
		}
	case <-time.After(time.Second):
		t.Fatal("releasing the narrower lock did not unblock the holder")
	}
}
