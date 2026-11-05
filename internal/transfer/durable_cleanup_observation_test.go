package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// This injects a cleanup callback failure after ownership admission. It models
// a permitted saved-receipt/retained-anchor sequence, not proof of lifecycle
// pre-admission contention or of the cause of any native Windows failure.
func durableCleanupObservationFixture(t *testing.T) (*Manager, string, DurableStatus, Batch) {
	t.Helper()
	dir := t.TempDir()
	m := durableTestManager(t, dir, true)
	offered, batch := durableTestAccepted(t, m)
	cleanupCalls := 0
	m.durable.fault = func(stage string) error {
		if stage == "before_cleanup" {
			cleanupCalls++
			return ErrBusy
		}
		return nil
	}
	saved, err := m.ReceiveDurableFile(context.Background(), testDurablePeer, offered.Lease, "file-one", strings.NewReader("synthetic payload"))
	if err != nil || saved.State != Completed || saved.Receipt == nil || len(saved.Receipt.Files) != 1 || cleanupCalls != 1 {
		t.Fatal("injected cleanup failure changed the saved receipt or cleanup attempt")
	}
	if m.durable.blocked != nil || m.durableReservedLocked() == 0 {
		t.Fatal("retained anchor was not charged independently of the saved receipt")
	}
	return m, dir, saved, batch
}

func assertDurableCleanupObservation(t *testing.T, m *Manager, saved DurableStatus, batch Batch, cleanup, sameOutput bool) (string, string) {
	t.Helper()
	status, err := m.StatusDurable(testDurablePeer, saved.Lease)
	if err != nil || status.State != Completed || !reflect.DeepEqual(status.Receipt, saved.Receipt) {
		t.Fatal("cleanup observation changed the certified saved receipt")
	}
	r := m.durable.record(saved.Lease.DeliveryID)
	if r == nil || len(r.Files) != 1 || r.Files[0].Phase != "saved" || r.Files[0].Anchor == "" || r.Files[0].Cleanup != cleanup || r.StageCleanup {
		t.Fatal("unexpected retained anchor ledger observation")
	}
	entries, err := os.ReadDir(filepath.Join(batch.Destination, durableStage))
	if err != nil || len(entries) != 1 || entries[0].Name() != r.Files[0].Anchor {
		t.Fatal("stage did not retain exactly the recorded anchor")
	}
	anchor := filepath.Join(batch.Destination, durableStage, r.Files[0].Anchor)
	output := filepath.Join(batch.Destination, r.Files[0].Entry.Path)
	anchorInfo, err := os.Lstat(anchor)
	if err != nil || !anchorInfo.Mode().IsRegular() {
		t.Fatal("retained anchor was not a regular file")
	}
	outputInfo, err := os.Lstat(output)
	if err != nil || !outputInfo.Mode().IsRegular() || os.SameFile(anchorInfo, outputInfo) != sameOutput {
		t.Fatal("anchor and saved output did not have the expected same-file relationship")
	}
	return anchor, output
}

func assertDurableCleanupObservationCleaned(t *testing.T, m *Manager, saved DurableStatus, batch Batch) {
	t.Helper()
	if err := m.MaintainDurable(); err != nil {
		t.Fatal("explicit maintenance did not reclaim the known anchor")
	}
	r := m.durable.record(saved.Lease.DeliveryID)
	if r == nil || len(r.Files) != 1 || r.Files[0].Anchor != "" || r.Files[0].Cleanup || r.StageIdentity != "" || m.durableReservedLocked() != 0 {
		t.Fatal("explicit maintenance retained cleanup obligations")
	}
	if _, err := os.Lstat(filepath.Join(batch.Destination, durableStage)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("explicit maintenance retained the private stage")
	}
	status, err := m.StatusDurable(testDurablePeer, saved.Lease)
	if err != nil || status.State != Completed || !reflect.DeepEqual(status.Receipt, saved.Receipt) {
		t.Fatal("successful cleanup changed the certified saved receipt")
	}
}

func TestDurableCleanupObservationSavedAnchorSurvivesReopen(t *testing.T) {
	m, dir, saved, batch := durableCleanupObservationFixture(t)
	assertDurableCleanupObservation(t, m, saved, batch, false, true)
	if err := m.Close(); err != nil {
		t.Fatal("fixture close failed")
	}
	// Reopening inventories the stage but does not itself remove the anchor.
	// No maintenance call or background scheduler precedes this observation.
	m = durableTestManager(t, dir, false)
	assertDurableCleanupObservation(t, m, saved, batch, false, true)
	assertDurableCleanupObservationCleaned(t, m, saved, batch)
}

func TestDurableCleanupObservationIdentityRefusalPersistsIntent(t *testing.T) {
	m, dir, saved, batch := durableCleanupObservationFixture(t)
	anchor, output := assertDurableCleanupObservation(t, m, saved, batch, false, true)
	m.durable.fault = nil
	// Preserve the saved output while replacing only the synthetic anchor with
	// different identity and identical content. This exercises identity refusal,
	// not an OS removal/sharing refusal, and needs no additional production hook.
	if os.Remove(anchor) != nil || os.WriteFile(anchor, []byte("synthetic payload"), 0600) != nil {
		t.Fatal("synthetic anchor replacement failed")
	}
	if err := m.MaintainDurable(); !errors.Is(err, ErrDurableRecovery) {
		t.Fatal("maintenance did not refuse the replaced anchor identity")
	}
	if m.durable.blocked != nil {
		t.Fatal("identity cleanup refusal invalidated certified receipts")
	}
	assertDurableCleanupObservation(t, m, saved, batch, true, false)
	if err := m.Close(); err != nil {
		t.Fatal("fixture close failed")
	}
	m = durableTestManager(t, dir, false)
	assertDurableCleanupObservation(t, m, saved, batch, true, false)
	if m.durable.admissionBlocked == nil {
		t.Fatal("reopen admitted the replaced anchor")
	}
	// Restoring the original hardlink is fixture repair, not a production retry
	// policy: normal cleanup must never delete an identity-mismatched entry.
	if os.Remove(anchor) != nil || os.Link(output, anchor) != nil {
		t.Fatal("synthetic anchor identity restoration failed")
	}
	assertDurableCleanupObservationCleaned(t, m, saved, batch)
}
