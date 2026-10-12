//go:build lanlink_integration

package core

import (
	"encoding/json"
	"os"
	"testing"
)

func TestOutboxProcessPhaseDistinguishesOwnershipAndNamespace(t *testing.T) {
	c := outboxFixture(t)
	r := outboxCommitFixture(t, c, outboxPrepareFixture(t, c, "phase"))
	check := func() outboxProcessPhase { return inspectOutboxProcessPhase(c, r.RequestID, r.Children[0].DeliveryID) }
	before, _ := json.Marshal(c.outbox.state)
	phase := check()
	if !phase.Coordinator || !phase.Selected || phase.RunPresent || phase.OwnerCheck != "ok" || !phase.OwnerAdmitted || phase.NamespaceCheck != "ok" {
		t.Fatalf("healthy idle phase was not identified: %+v", phase)
	}
	release := holdOutboxLifecycle(t, c)
	phase = check()
	release()
	if phase.OwnerCheck != "outbox_storage_busy" || phase.OwnerAdmitted || phase.NamespaceCheck != "not_checked" {
		t.Fatalf("lifecycle contention was not identified: %+v", phase)
	}
	original := c.outbox.info
	foreign, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c.outbox.info = foreign
	phase = check()
	c.outbox.info = original
	if phase.OwnerCheck != "ok" || !phase.OwnerAdmitted || phase.NamespaceCheck != "outbox_recovery_required" {
		t.Fatalf("namespace refusal was not identified: %+v", phase)
	}
	after, _ := json.Marshal(c.outbox.state)
	if string(before) != string(after) || c.outbox.frozen != "" || c.outbox.owned != nil {
		t.Fatal("phase observation changed semantic durable state or retained ownership")
	}
}

func TestOutboxProcessPhaseDistinguishesGlobalQueueAndRun(t *testing.T) {
	c := outboxFixture(t)
	r := outboxCommitFixture(t, c, outboxPrepareFixture(t, c, "phase"))
	id := r.Children[0].DeliveryID
	batch := &outgoingBatch{ID: id, outboxID: r.OutboxID, State: "queued", running: true}
	c.outbox.runs[id] = batch
	c.outgoingActive = 1
	c.outgoingQueue = []*outgoingRun{{batch: batch}}
	phase := inspectOutboxProcessPhase(c, r.RequestID, id)
	if !phase.RunPresent || !phase.Running || phase.RunState != "queued" || phase.Active != 1 || phase.Queue != 1 || !phase.SelectedQueued {
		t.Fatalf("queued run was not identified: %+v", phase)
	}
	c.outgoingQueue = nil
	batch.State = "awaiting-acceptance"
	phase = inspectOutboxProcessPhase(c, r.RequestID, id)
	if !phase.RunPresent || !phase.Running || phase.RunState != "awaiting-acceptance" || phase.Queue != 0 || phase.SelectedQueued {
		t.Fatalf("active pre-offer run was not identified: %+v", phase)
	}
	phase = inspectOutboxProcessPhase(c, "unselected", id)
	if phase.Selected || phase.RunPresent || phase.SelectedQueued {
		t.Fatal("phase observation selected a different original request")
	}
}

func TestOutboxProcessPhaseReportsBusyLocksWithoutWaiting(t *testing.T) {
	c := outboxFixture(t)
	c.op.Lock()
	c.refreshMu.Lock()
	c.outbox.mu.Lock()
	phase := inspectOutboxProcessPhase(c, "", "")
	c.outbox.mu.Unlock()
	c.refreshMu.Unlock()
	c.op.Unlock()
	if !phase.OpBusy || !phase.RefreshBusy || !phase.CoordinatorBusy || phase.OwnerCheck != "not_checked" {
		t.Fatalf("busy scheduler locks were not identified: %+v", phase)
	}
	c.mu.Lock()
	phase = inspectOutboxProcessPhase(c, "", "")
	c.mu.Unlock()
	if !phase.CoreBusy || phase.OwnerCheck != "not_checked" {
		t.Fatalf("busy Core lock was not identified: %+v", phase)
	}
}
