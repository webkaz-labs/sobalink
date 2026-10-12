package core

import (
	"context"
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"strings"
	"testing"
	"time"
)

func TestOutboxRetirementReleasesMarkerReservation(t *testing.T) {
	c, _ := releasedOutboxPreparation(t)
	o := c.outbox
	if got := c.outboxReserved.Load(); got != outboxMarkerBytes {
		t.Fatalf("released payload retained %d bytes, want marker %d", got, outboxMarkerBytes)
	}
	// A refreshed inventory replaces stale extra charges; marker ownership is
	// accounted independently and must not become an everlasting orphan charge.
	o.orphanBytes = 99
	if err := o.inventoryLocked(); err != nil {
		t.Fatal(err)
	}
	o.updateAccountingLocked()
	if o.orphanBytes != 0 || c.outboxReserved.Load() != outboxMarkerBytes {
		t.Fatal("inventory retained stale orphan bytes or lost the marker reservation")
	}
	if err := o.retireLocked(); err != nil {
		t.Fatal(err)
	}
	if got := c.outboxReserved.Load(); got != 0 {
		t.Fatalf("retired namespace retained %d bytes", got)
	}
}

func TestOutboxCancelledLeaseRevocationRemainsRestartable(t *testing.T) {
	c := outboxFixture(t)
	r := outboxCommitFixture(t, c, outboxPrepareFixture(t, c, "cancel-revocation"))
	o := c.outbox
	row := o.state.Intents[r.OutboxID]
	d := &row.Deliveries[0]
	d.Attempted = true
	d.Lease = &transfer.DurableLease{Version: 1, DeliveryID: d.Offer.DeliveryID, Digest: d.Offer.Digest, RecipientID: d.Trust.ID, ReceiverEpoch: strings.Repeat("d", 32), Token: strings.Repeat("e", 32), ExpiresAt: r.ExpiresAt, ReceiptUntil: r.ExpiresAt.Add(transfer.DurableReceiptGrace)}
	row.View.Children[0].DeliveryOutcome = "unknown"
	if _, err := c.cancelOutbox(context.Background(), r.OutboxID, ""); err != nil {
		t.Fatal(err)
	}
	c.profile.Peers = nil
	c.pumpOutbox()
	if row.View.Children[0].Status != "cancelled" {
		t.Fatal("revocation overwrote durable cancellation disposition")
	}
	if err := o.validateLocked(); err != nil {
		t.Fatal(err)
	}
	outboxReopenFixture(t, c)
}

func TestOutboxDiscardedReviewMetadataHasBoundedLifetime(t *testing.T) {
	c := outboxFixture(t)
	r := outboxPrepareFixture(t, c, "discard-lifetime")
	o := c.outbox
	if _, err := c.discardOutbox(r.OutboxID, r.ProcessID); err != nil {
		t.Fatal(err)
	}
	if err := o.retireLocked(); err != nil {
		t.Fatal(err)
	}
	if o.prepared[r.OutboxID] == nil || c.outboxMetadata.Load() == 0 {
		t.Fatal("discarded review lost its bounded reconciliation window or accounting")
	}
	future := r.CreatedAt.Add(transferPreparationLifetime)
	o.now = func() time.Time { return future }
	c.pumpOutbox()
	if len(o.prepared) != 0 || c.outboxMetadata.Load() != 0 || c.outboxReserved.Load() != 0 {
		t.Fatal("joined and expired process-only review remained retained")
	}
}
