package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func terminalUnknownOutbox(t *testing.T, c *Core) OutboxRecord {
	t.Helper()
	r := outboxCommitFixture(t, c, outboxPrepareFixture(t, c, "unknown-evidence"))
	row := c.outbox.state.Intents[r.OutboxID]
	for i := range row.Deliveries {
		row.Deliveries[i].Attempted = true
		row.View.Children[i].DeliveryOutcome = "unknown"
	}
	value, err := c.cancelOutbox(context.Background(), r.OutboxID, "")
	if err != nil {
		t.Fatal(err)
	}
	return value.(OutboxRecord)
}

func TestOutboxTerminalUnknownEvidenceReleasesOnlyActiveBudget(t *testing.T) {
	c := outboxFixture(t)
	r := terminalUnknownOutbox(t, c)
	o := c.outbox
	row := o.state.Intents[r.OutboxID]
	// Lowered live quotas do not evict retained evidence or consume its slots.
	lim := c.transferLimits()
	lim.MaxBatches = 1
	lim.MaxMetadataBytes = transfer.ManifestMetadataBytes(row.Deliveries[0].Offer.Manifest)
	c.mu.Lock()
	err := c.checkOutgoingCapacityLocked(lim, row.Deliveries[0].Offer.Manifest, 1, nil)
	c.mu.Unlock()
	if err != nil || c.outboxActiveChildren.Load() != 0 || c.outboxActiveMetadata.Load() != 0 || c.outboxChildren.Load() != 2 || c.outboxMetadata.Load() == 0 {
		t.Fatalf("retained evidence consumed active capacity: %v", err)
	}
	if c.outboxReserved.Load() != outboxMarkerBytes || !o.state.Payloads[row.PayloadID].Released {
		t.Fatal("certified cleanup lost retained marker accounting")
	}
	future := r.ExpiresAt.Add(outboxRequestMaximumLifetime + time.Hour)
	o.now = func() time.Time { return future }
	if err := o.retireLocked(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o.viewLocked(row), r) || o.state.Intents[r.OutboxID] == nil {
		t.Fatal("unknown evidence was discarded at its retention horizon")
	}
	// Reset the synthetic clock before simulating a new process.
	o.now = time.Now
	outboxReopenFixture(t, c)
	if got := c.outbox.viewLocked(c.outbox.state.Intents[r.OutboxID]); !reflect.DeepEqual(got, r) || c.outboxActiveChildren.Load() != 0 {
		t.Fatal("restart changed retained evidence or recharged terminal slots")
	}
	outboxPrepareFixture(t, c, "new-after-evidence")
}

func TestOutboxActiveBudgetRequiresPhysicalJoinAndCertifiedFence(t *testing.T) {
	c, r, b := outboxCompletionFixture(t)
	o := c.outbox
	row := o.state.Intents[r.OutboxID]
	beforeBytes := c.outboxReserved.Load()
	for i := range row.Deliveries {
		row.Deliveries[i].Cancelled = true
		row.View.Children[i].Status = "cancelled"
	}
	outboxSummarize(row, o.now())
	o.updateAccountingLocked()
	if c.outboxActiveChildren.Load() != 2 {
		t.Fatal("uncertified terminal candidates released slots")
	}
	if err := o.saveLocked(); err != nil {
		t.Fatal(err)
	}
	if c.outboxActiveChildren.Load() != 1 || c.outboxReserved.Load() != beforeBytes {
		t.Fatal("terminal run lost its slot or physical bytes")
	}
	o.completions[b.ID] = outboxCompletion{joined: false}
	if err := o.withOwnedLocked(func() error { return o.drainCompletionLocked(b.ID) }); err != nil {
		t.Fatal(err)
	}
	if c.outboxActiveChildren.Load() != 1 || c.outboxReserved.Load() != beforeBytes {
		t.Fatal("unjoined completion released its reservation")
	}
	o.completions[b.ID] = outboxCompletion{joined: true}
	if err := o.withOwnedLocked(func() error { return o.drainCompletionLocked(b.ID) }); err != nil {
		t.Fatal(err)
	}
	if c.outboxActiveChildren.Load() != 0 || c.outboxActiveMetadata.Load() != 0 || c.outboxReserved.Load() != outboxMarkerBytes || c.outboxChildren.Load() != 2 {
		t.Fatal("joined certified completion did not release only live capacity")
	}
}

func TestOutboxDiscardedStagingStillConsumesActiveBudget(t *testing.T) {
	c := outboxFixture(t)
	r := outboxPrepareFixture(t, c, "discard-staging")
	o := c.outbox
	p := o.prepared[r.OutboxID]
	p.staging = true // Hold the physical owner independently of cancellation.
	before := c.outboxReserved.Load()
	if _, err := c.discardOutbox(r.OutboxID, r.ProcessID); err != nil {
		t.Fatal(err)
	}
	o.updateAccountingLocked()
	if c.outboxActiveChildren.Load() != 2 || c.outboxActiveMetadata.Load() == 0 || c.outboxReserved.Load() != before {
		t.Fatal("discard released a retained staging owner")
	}
	c.abortOutboxPreparation(p)
	if c.outboxActiveChildren.Load() != 0 || c.outboxActiveMetadata.Load() != 0 || c.outboxChildren.Load() != 2 || c.outboxMetadata.Load() == 0 || c.outboxReserved.Load() != outboxMarkerBytes {
		t.Fatal("joined discard did not separate evidence and physical charges")
	}
}

func TestOutboxUncertainTerminalSaveChargesAllRetainedEvidence(t *testing.T) {
	c := outboxFixture(t)
	terminalUnknownOutbox(t, c)
	r := outboxCommitFixture(t, c, outboxPrepareFixture(t, c, "uncertain-terminal"))
	o := c.outbox
	beforeBytes := c.outboxReserved.Load()
	o.write = func(string, []byte) error { return config.ErrAtomicCommitted }
	if _, err := c.cancelOutbox(context.Background(), r.OutboxID, ""); err == nil {
		t.Fatal("fixture did not fail terminal persistence")
	}
	if o.frozen == "" || c.outboxActiveChildren.Load() != c.outboxChildren.Load() || c.outboxActiveMetadata.Load() != c.outboxMetadata.Load() || c.outboxReserved.Load() < beforeBytes {
		t.Fatal("uncertain state released retained admission or physical bytes")
	}
}

func TestOutboxUncertainCleanupNeverReleasesBytes(t *testing.T) {
	c, r, b := completeCleanupFixture(t)
	o := c.outbox
	payload := o.state.Payloads[o.state.Intents[r.OutboxID].PayloadID]
	before := c.outboxReserved.Load()
	o.afterWrite = func() error {
		if payload.Released {
			return config.ErrAtomicCommitted
		}
		return nil
	}
	o.completions[b.ID] = outboxCompletion{joined: true}
	if err := o.withOwnedLocked(func() error { return o.drainCompletionLocked(b.ID) }); err == nil {
		t.Fatal("fixture did not interrupt cleanup certification")
	}
	if c.outboxReserved.Load() != before || c.outboxActiveChildren.Load() != c.outboxChildren.Load() {
		t.Fatal("uncertified cleanup released byte or evidence reservations")
	}
}

func TestOutboxEvidenceHardCapPreservesReplayAndCleanup(t *testing.T) {
	c := outboxFixture(t)
	r := terminalUnknownOutbox(t, c)
	o := c.outbox
	source := o.state.Intents[r.OutboxID]
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	// Share an already-cleaned payload; every retained child still has its own
	// exact delivery identity and evidence reservation.
	for len(o.state.Intents)*len(source.Deliveries) < outboxMaxChildren {
		var row outboxIntent
		if err := json.Unmarshal(data, &row); err != nil {
			t.Fatal(err)
		}
		id := newOutboxRequestID(time.Now())
		row.View.OutboxID, row.View.RequestID = id, id
		for i := range row.Deliveries {
			delivery := randomID()
			row.View.Children[i].DeliveryID = delivery
			row.Deliveries[i].Offer.DeliveryID = delivery
			row.Deliveries[i].Offer.Manifest.ID = delivery
			row.Deliveries[i].Offer.Digest, err = transfer.DurableDigest(row.Deliveries[i].Offer)
			if err != nil {
				t.Fatal(err)
			}
		}
		o.state.Intents[id] = &row
	}
	if err := o.saveLocked(); err != nil {
		t.Fatal(err)
	}
	if err := o.validateLocked(); err != nil {
		t.Fatal(err)
	}
	if c.outboxChildren.Load() != outboxMaxChildren || c.outboxActiveChildren.Load() != 0 {
		t.Fatal("fixture did not fill the separate evidence pool")
	}
	manifest := source.Deliveries[0].Offer.Manifest
	_, _, err = c.reserveOutbox(context.Background(), newOutboxRequestID(time.Now()), resourceCatalogProcessToken(c.resourceNonce), []string{"peer-b"}, manifest, time.Time{})
	if networkErrorCode(err) != "outbox_evidence_capacity" || o.frozen != "" {
		t.Fatalf("hard evidence quota returned the wrong refusal: %v", err)
	}
	got, err := c.commitOutbox(context.Background(), r.OutboxID, "", r.ReviewRevision, true)
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("full evidence pool blocked original request replay: %v", err)
	}
	if err := o.cleanupPayloadLocked(o.state.Payloads[source.PayloadID]); err != nil {
		t.Fatal("full evidence pool blocked existing cleanup", err)
	}
	lim := c.transferLimits()
	c.mu.Lock()
	err = c.checkOutgoingCapacityLocked(lim, manifest, 1, nil)
	c.mu.Unlock()
	if err != nil {
		t.Fatal("full evidence pool consumed ordinary transfer capacity", err)
	}
}

func TestOutboxTerminalPayloadRemainsChargedAfterCleanupFailure(t *testing.T) {
	c, r, b := completeCleanupFixture(t)
	o := c.outbox
	p := o.state.Payloads[o.state.Intents[r.OutboxID].PayloadID]
	owned := filepath.Join(o.path(), "payloads", p.ID)
	held := owned + "-held"
	if err := os.Rename(owned, held); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Rename(held, owned) }()
	before := c.outboxReserved.Load()
	o.completions[b.ID] = outboxCompletion{joined: true}
	if err := o.withOwnedLocked(func() error { return o.drainCompletionLocked(b.ID) }); err == nil {
		t.Fatal("fixture did not fail physical cleanup")
	}
	if c.outboxActiveChildren.Load() != 0 || c.outboxReserved.Load() != before {
		t.Fatal("failed payload cleanup released bytes or retained terminal slots")
	}
	lim := c.transferLimits()
	lim.MaxReservedBytes = before
	c.mu.Lock()
	err := c.checkOutgoingCapacityLocked(lim, transfer.Manifest{Entries: []transfer.Entry{{Size: 1}}}, 1, nil)
	c.mu.Unlock()
	if !errors.Is(err, errOutgoingStaging) {
		t.Fatalf("retained bytes did not block excess staging: %v", err)
	}
}

func TestOutboxReceiptOnlyRunRechargesTerminalAdmission(t *testing.T) {
	c := outboxFixture(t)
	r := terminalUnknownOutbox(t, c)
	o := c.outbox
	row := o.state.Intents[r.OutboxID]
	for i := range row.Deliveries {
		d := &row.Deliveries[i]
		d.Lease = &transfer.DurableLease{Version: 1, DeliveryID: d.Offer.DeliveryID, Digest: d.Offer.Digest, RecipientID: d.Trust.ID, ReceiverEpoch: strings.Repeat("d", 32), Token: strings.Repeat("e", 32), ExpiresAt: r.ExpiresAt, ReceiptUntil: r.ExpiresAt.Add(transfer.DurableReceiptGrace)}
	}
	if err := o.saveLocked(); err != nil {
		t.Fatal(err)
	}
	// Keep the queue behind its finite execution limit. pumpOutbox must charge
	// terminal receipt queries before exposing these descriptors to admission.
	c.outgoingActive = finiteInt(c.capacity.Number("resources", "transferConcurrentRecipients"))
	c.pumpOutbox()
	if len(o.runs) != 2 || c.outboxActiveChildren.Load() != 2 || c.outboxActiveMetadata.Load() == 0 {
		t.Fatal("receipt-only run escaped active admission accounting")
	}
	c.cancel()
	c.pumpOutgoing()
	c.wg.Wait()
}

func TestOutboxPendingLimitRemainsIndependentOfHistory(t *testing.T) {
	c := outboxFixture(t)
	c.capacity.Logical["transferHistoryEntries"] = capacity.Limited(256)
	c.capacity.Resources["transferPending"] = capacity.Limited(2)
	r := outboxCommitFixture(t, c, outboxPrepareFixture(t, c, "active"))
	manifest := c.outbox.state.Intents[r.OutboxID].Deliveries[0].Offer.Manifest
	lim := c.transferLimits()
	c.mu.Lock()
	err := c.checkOutgoingCapacityLocked(lim, manifest, 1, nil)
	c.mu.Unlock()
	if !errors.Is(err, errOutgoingHistory) {
		t.Fatal("raised history bypassed shared pending admission", err)
	}
	_, _, err = c.reserveOutbox(context.Background(), newOutboxRequestID(time.Now()), resourceCatalogProcessToken(c.resourceNonce), []string{"peer-b"}, manifest, time.Time{})
	if networkErrorCode(err) != "outbox_capacity" {
		t.Fatal("raised history bypassed outbox pending admission", err)
	}
}

func TestOutboxReceiptOnlyQueriesRespectActiveBounds(t *testing.T) {
	for _, bound := range []string{"global", "per_peer"} {
		t.Run(bound, func(t *testing.T) {
			c := outboxFixture(t)
			for i := 0; i < 3; i++ {
				r := terminalUnknownOutbox(t, c)
				row := c.outbox.state.Intents[r.OutboxID]
				for j := range row.Deliveries {
					d := &row.Deliveries[j]
					d.Lease = &transfer.DurableLease{Version: 1, DeliveryID: d.Offer.DeliveryID, Digest: d.Offer.Digest, RecipientID: d.Trust.ID, ReceiverEpoch: strings.Repeat("d", 32), Token: strings.Repeat("e", 32), ExpiresAt: r.ExpiresAt, ReceiptUntil: r.ExpiresAt.Add(transfer.DurableReceiptGrace)}
				}
			}
			o := c.outbox
			if err := o.saveLocked(); err != nil {
				t.Fatal(err)
			}
			c.capacity.Logical["transferHistoryEntries"] = capacity.Limited(256)
			want := 3
			if bound == "global" {
				c.capacity.Resources["transferPending"] = capacity.Limited(3)
			} else {
				c.capacity.Resources["transferPendingPerPeer"] = capacity.Limited(1)
				want = 2
			}
			c.outgoingActive = finiteInt(c.capacity.Number("resources", "transferConcurrentRecipients"))
			c.pumpOutbox()
			if len(o.runs) != want || int(c.outboxActiveChildren.Load()) != want || c.outboxChildren.Load() != 6 {
				t.Fatalf("receipt queries exceeded %s active quota or lost evidence: runs=%d active=%d", bound, len(o.runs), c.outboxActiveChildren.Load())
			}
			c.cancel()
			c.pumpOutgoing()
			c.wg.Wait()
		})
	}
}
