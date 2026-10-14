package core

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// A recorded remote receipt is distinct from local payload reclamation. This
// fixture gives every recipient exact saved evidence without opening a socket.
func completeCleanupFixture(t *testing.T) (*Core, OutboxRecord, *outgoingBatch) {
	t.Helper()
	c, r, b := outboxCompletionFixture(t)
	row := c.outbox.state.Intents[r.OutboxID]
	for i := range row.Deliveries {
		d := &row.Deliveries[i]
		d.Attempted = true
		d.Lease = &transfer.DurableLease{Version: 1, DeliveryID: d.Offer.DeliveryID, Digest: d.Offer.Digest, RecipientID: d.Trust.ID, ReceiverEpoch: strings.Repeat("d", 32), Token: strings.Repeat("e", 32), ExpiresAt: r.ExpiresAt, ReceiptUntil: r.ExpiresAt.Add(transfer.DurableReceiptGrace)}
		e := r.Entries[0]
		receipt := transfer.DurableReceipt{Lease: *d.Lease, State: transfer.Completed, SavedAt: time.Now(), Files: []transfer.FileAck{{BatchID: d.Offer.DeliveryID, FileID: e.ID, StoredName: e.Path, Size: e.Size, SHA256: e.SHA256}}}
		if err := c.recordOutboxStatus(r.OutboxID, i, transfer.DurableStatus{Lease: *d.Lease, State: transfer.Completed, Receipt: &receipt}); err != nil {
			t.Fatal(err)
		}
	}
	return c, r, b
}

func TestOutboxCleanupRetriesAfterConfirmedCompletion(t *testing.T) {
	c, r, b := completeCleanupFixture(t)
	o := c.outbox
	row := o.state.Intents[r.OutboxID]
	p := o.state.Payloads[row.PayloadID]
	owned := filepath.Join(o.path(), "payloads", p.ID)
	held := filepath.Join(o.path(), "payloads", p.ID+"-held")
	// Temporarily make the exact owned directory unavailable. Restore the same
	// native object before maintenance; this is not a replacement or permission
	// change and cannot authorize deletion of an unrelated directory.
	if err := os.Rename(owned, held); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(held, owned) })
	o.completions[b.ID] = outboxCompletion{joined: true}
	err := o.withOwnedLocked(func() error { return o.drainCompletionLocked(b.ID) })
	if err == nil || !p.CleanupPending || p.Released || len(o.completions) != 0 || o.runs[b.ID] != nil {
		t.Fatal("fixture did not isolate a cleanup failure after durable completion", err)
	}
	if err := os.Rename(held, owned); err != nil {
		t.Fatal(err)
	}
	before := cloneOutboxRecord(o.viewLocked(row))
	c.pumpOutbox()
	if !p.Released || p.CleanupPending {
		t.Fatal("ordinary in-process maintenance did not retry confirmed payload cleanup")
	}
	after := o.viewLocked(row)
	if !reflect.DeepEqual(after, before) || len(o.runs) != 0 || len(o.completions) != 0 {
		t.Fatal("cleanup changed exact delivery evidence or admitted another run")
	}
	for _, entry := range p.Manifest.Entries {
		if _, err := os.Stat(filepath.Join(owned, entry.ID)); !os.IsNotExist(err) {
			t.Fatal("owned staged bytes remain after successful cleanup", err)
		}
	}
	if _, err := os.Stat(filepath.Join(owned, "owner")); err != nil {
		t.Fatal("replay ownership evidence disappeared before the retention horizon", err)
	}
}

func pendingCleanupFixture(t *testing.T, c *Core) *outboxPayload {
	t.Helper()
	r := outboxPrepareFixture(t, c, "pending-cleanup")
	o := c.outbox
	prep := o.prepared[r.OutboxID]
	prep.discarded = true
	prep.cancel()
	p := o.state.Payloads[prep.intent.PayloadID]
	p.CleanupPending = true
	if err := o.saveLocked(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOutboxCleanupMaintenanceIsBoundedAndFair(t *testing.T) {
	c := outboxFixture(t)
	o := c.outbox
	var payloads []*outboxPayload
	for range outboxCleanupPerTick + 3 {
		payloads = append(payloads, pendingCleanupFixture(t, c))
	}
	countReleased := func() int {
		count := 0
		for _, p := range payloads {
			if p.Released {
				count++
			}
		}
		return count
	}
	if err := o.withOwnedLocked(o.retryPendingCleanupLocked); err != nil {
		t.Fatal(err)
	}
	if countReleased() != outboxCleanupPerTick {
		t.Fatal("one maintenance tick exceeded or failed its finite cleanup budget")
	}
	if err := o.withOwnedLocked(o.retryPendingCleanupLocked); err != nil {
		t.Fatal(err)
	}
	if countReleased() != len(payloads) {
		t.Fatal("bounded cleanup did not reach the remaining owned payloads")
	}
	writes := 0
	o.afterWrite = func() error { writes++; return nil }
	if err := o.withOwnedLocked(o.retryPendingCleanupLocked); err != nil || writes != 0 {
		t.Fatal("already completed cleanup repeated state publication", err)
	}
}

func TestOutboxCleanupMaintenanceSkipsPersistentFailuresFairly(t *testing.T) {
	c := outboxFixture(t)
	o := c.outbox
	var payloads []*outboxPayload
	for range outboxCleanupPerTick + 3 {
		payloads = append(payloads, pendingCleanupFixture(t, c))
	}
	sort.Slice(payloads, func(i, j int) bool { return payloads[i].ID < payloads[j].ID })
	for _, p := range payloads[:outboxCleanupPerTick] {
		owned := filepath.Join(o.path(), "payloads", p.ID)
		if err := os.Rename(owned, owned+"-held"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Rename(owned+"-held", owned) })
	}
	if err := o.withOwnedLocked(o.retryPendingCleanupLocked); err != nil {
		t.Fatal(err)
	}
	for _, p := range payloads {
		if p.Released {
			t.Fatal("first tick exceeded its budget of unavailable owners")
		}
	}
	if err := o.withOwnedLocked(o.retryPendingCleanupLocked); err != nil {
		t.Fatal(err)
	}
	for i, p := range payloads {
		if p.Released != (i >= outboxCleanupPerTick) {
			t.Fatal("persistent cleanup failures starved later exact owners")
		}
	}
	for _, p := range payloads[:outboxCleanupPerTick] {
		owned := filepath.Join(o.path(), "payloads", p.ID)
		if err := os.Rename(owned+"-held", owned); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.withOwnedLocked(o.retryPendingCleanupLocked); err != nil {
		t.Fatal(err)
	}
	for _, p := range payloads {
		if !p.Released {
			t.Fatal("restored owner was not reclaimed")
		}
	}
}

func TestOutboxCleanupMaintenanceUncertainPublicationStopsBatch(t *testing.T) {
	c := outboxFixture(t)
	o := c.outbox
	var payloads []*outboxPayload
	for range 3 {
		payloads = append(payloads, pendingCleanupFixture(t, c))
	}
	sort.Slice(payloads, func(i, j int) bool { return payloads[i].ID < payloads[j].ID })
	// Hold the synthetic clock so the successful pending write is byte-identical.
	now := o.state.HighWater
	o.now = func() time.Time { return now }
	beforeBytes := c.outboxReserved.Load()
	beforeDisk := append([]byte(nil), o.confirmedDisk...)
	beforeCertified := map[string]OutboxRecord{}
	for id, row := range o.certified {
		beforeCertified[id] = cloneOutboxRecord(row)
	}
	writes := 0
	o.afterWrite = func() error {
		writes++
		if writes == 2 {
			return config.ErrAtomicCommitted
		}
		return nil
	}
	if err := o.withOwnedLocked(o.retryPendingCleanupLocked); err == nil || o.frozen != "outbox_storage_uncertain" || writes != 2 {
		t.Fatal("uncertain release publication did not freeze and stop the cleanup batch", err)
	}
	// The first write republishes the already pending disposition, so the
	// certified bytes remain exact even when the following release is uncertain.
	if !bytes.Equal(beforeDisk, o.confirmedDisk) || !reflect.DeepEqual(beforeCertified, o.certified) || c.outboxReserved.Load() != beforeBytes {
		t.Fatal("uncertain cleanup exposed unconfirmed evidence or byte capacity")
	}
	for _, p := range payloads[1:] {
		if p.Released || !p.CleanupPending {
			t.Fatal("cleanup advanced after uncertain publication")
		}
		for _, entry := range p.Manifest.Entries {
			if _, err := os.Stat(filepath.Join(o.path(), "payloads", p.ID, entry.ID)); err != nil {
				t.Fatal("later payload changed after uncertainty", err)
			}
		}
	}
	if err := o.withOwnedLocked(o.retryPendingCleanupLocked); err == nil || writes != 2 {
		t.Fatal("frozen cleanup retried publication", err)
	}
}

func TestOutboxCleanupMaintenanceWaitsForPhysicalStaging(t *testing.T) {
	c := outboxFixture(t)
	o := c.outbox
	p := pendingCleanupFixture(t, c)
	var prep *outboxPreparation
	for _, value := range o.prepared {
		if value.intent.PayloadID == p.ID {
			prep = value
		}
	}
	prep.staging = true
	if err := o.withOwnedLocked(o.retryPendingCleanupLocked); err != nil || p.Released {
		t.Fatal("cleanup passed a still-owned discarded staging operation", err)
	}
	path := filepath.Join(o.path(), "payloads", p.ID, p.Manifest.Entries[0].ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("active staging bytes were removed", err)
	}
	prep.staging = false
	if err := o.withOwnedLocked(o.retryPendingCleanupLocked); err != nil || !p.Released {
		t.Fatal("joined staging owner did not release cleanup", err)
	}
}

func TestOutboxCleanupMaintenancePreservesForeignReplacement(t *testing.T) {
	c := outboxFixture(t)
	o := c.outbox
	p := pendingCleanupFixture(t, c)
	owned := filepath.Join(o.path(), "payloads", p.ID)
	held := filepath.Join(o.path(), "payloads", p.ID+"-held")
	if err := os.Rename(owned, held); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(owned, 0700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(owned, "foreign")
	if err := os.WriteFile(foreign, []byte("synthetic unrelated content"), 0600); err != nil {
		t.Fatal(err)
	}
	before := c.outboxReserved.Load()
	if err := o.withOwnedLocked(o.retryPendingCleanupLocked); err != nil || p.Released || !p.CleanupPending {
		t.Fatal("foreign directory was adopted or pending evidence disappeared", err)
	}
	if content, err := os.ReadFile(foreign); err != nil || string(content) != "synthetic unrelated content" || c.outboxReserved.Load() != before {
		t.Fatal("unknown inventory was changed or unconfirmed bytes were uncharged", err)
	}
	// The original object can become available later without replacement or
	// broad deletion. Move the unrelated fixture aside, then restore that owner.
	if err := os.Rename(owned, owned+"-foreign"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(held, owned); err != nil {
		t.Fatal(err)
	}
	if err := o.withOwnedLocked(o.retryPendingCleanupLocked); err != nil || !p.Released {
		t.Fatal("restored exact owner was not retried", err)
	}
}

func TestOutboxRetirementReleasesKnownMixedOutcome(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "maintenance", true: "resumed-removal"}[restart], func(t *testing.T) {
			c, r, b := completeCleanupFixture(t)
			o := c.outbox
			row := o.state.Intents[r.OutboxID]
			last := len(row.Deliveries) - 1
			d := &row.Deliveries[last]
			d.Attempted, d.Exposure, d.Cancelled = false, false, true
			d.Lease, d.Receipt = nil, nil
			child := &row.View.Children[last]
			child.Status, child.DeliveryOutcome, child.Files = "cancelled", "not_attempted", []OutboxFile{}
			outboxSummarize(row, o.now())
			delete(o.runs, b.ID)
			if row.View.DeliveryOutcome != "partial" {
				t.Fatal("fixture did not produce a known mixed aggregate")
			}
			if err := o.saveLocked(); err != nil {
				t.Fatal(err)
			}
			p := o.state.Payloads[row.PayloadID]
			if err := o.cleanupPayloadLocked(p); err != nil || !p.Released {
				t.Fatal("terminal mixed fixture retained a physical payload", err)
			}
			horizon, _ := outboxRequestHorizon(r.OutboxID)
			o.now = func() time.Time { return horizon }
			if restart {
				// A namespace-removal cut is safe for this exact known mixture.
				// Exercise the same persisted-state validator and removal recovery
				// used on open, with the existing synthetic clock.
				p.NamespaceRemoval = true
				if err := o.saveLocked(); err != nil {
					t.Fatal(err)
				}
				validator := &outboxCoordinator{core: c, state: o.state}
				if err := validator.validateLocked(); err != nil {
					t.Fatal("persisted known mixed removal state was rejected", err)
				}
				if err := o.resumeNamespaceRemovalLocked(); err != nil {
					t.Fatal("known mixed removal did not resume", err)
				}
			}
			if err := o.retireLocked(); err != nil || o.state.Intents[r.OutboxID] != nil || len(o.state.Payloads) != 0 {
				t.Fatal("known mixed terminal evidence failed safe retirement", err)
			}
			if !outboxRequestRetired(r.OutboxID, horizon) || outboxRequestFresh(r.OutboxID, horizon) {
				t.Fatal("retirement made the original admission key reusable")
			}
		})
	}
}

func TestOutboxRetirementRetainsUncertainMixedOutcome(t *testing.T) {
	for _, outcome := range []string{"unknown", "partial"} {
		t.Run(outcome, func(t *testing.T) {
			c, r, b := completeCleanupFixture(t)
			o := c.outbox
			row := o.state.Intents[r.OutboxID]
			last := len(row.Deliveries) - 1
			d, child := &row.Deliveries[last], &row.View.Children[last]
			d.Cancelled = true
			child.Status, child.DeliveryOutcome = "cancelled", outcome
			if outcome == "unknown" {
				d.Receipt = nil
				child.Files = []OutboxFile{}
			} else {
				d.Receipt.State = transfer.Partial
			}
			outboxSummarize(row, o.now())
			delete(o.runs, b.ID)
			if err := o.saveLocked(); err != nil {
				t.Fatal(err)
			}
			p := o.state.Payloads[row.PayloadID]
			if err := o.cleanupPayloadLocked(p); err != nil || !p.Released {
				t.Fatal("terminal cleanup fixture failed", err)
			}
			validator := &outboxCoordinator{core: c, state: o.state}
			if err := validator.validateLocked(); err != nil {
				t.Fatal("uncertain retained fixture invalid", err)
			}
			before := cloneOutboxRecord(row.View)
			horizon, _ := outboxRequestHorizon(r.OutboxID)
			o.now = func() time.Time { return horizon }
			if err := o.retireLocked(); err != nil || o.state.Intents[r.OutboxID] != row || o.state.Payloads[p.ID] != p || !reflect.DeepEqual(before, row.View) {
				t.Fatal("retirement erased uncertain recipient evidence", err)
			}
			p.NamespaceRemoval = true
			if err := validator.validateLocked(); err == nil {
				t.Fatal("persisted removal accepted uncertain recipient evidence")
			}
			p.NamespaceRemoval = false
		})
	}
}
