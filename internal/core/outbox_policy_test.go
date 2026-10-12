package core

import (
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/capacity"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func TestOutboxRetainedEvidenceSurvivesManifestPolicyReduction(t *testing.T) {
	c := outboxFixture(t)
	review := outboxPrepareFixture(t, c, "policy-evidence")
	outboxCommitFixture(t, c, review)
	row := c.outbox.state.Intents[review.OutboxID]
	d := &row.Deliveries[0]
	d.Attempted = true
	d.Lease = &transfer.DurableLease{Version: 1, DeliveryID: d.Offer.DeliveryID, Digest: d.Offer.Digest, RecipientID: d.Trust.ID, ReceiverEpoch: strings.Repeat("d", 32), Token: strings.Repeat("e", 32), ExpiresAt: review.ExpiresAt, ReceiptUntil: review.ExpiresAt.Add(transfer.DurableReceiptGrace)}
	row.View.Children[0].DeliveryOutcome = "unknown"
	c.mu.Lock()
	c.capacity.Resources["transferManifestBytes"] = capacity.Limited(1)
	c.mu.Unlock()
	minimum := 2 * transferJSONBytes(transfer.ManifestMetadataBytes(transfer.Manifest{Entries: review.Entries}))
	if got := c.transferResponseJSONBytes(); got < minimum {
		t.Fatalf("receipt bound shrank below retained manifest: %d < %d", got, minimum)
	}
	e := review.Entries[0]
	receipt := &transfer.DurableReceipt{Lease: *d.Lease, State: transfer.Completed, SavedAt: time.Now(), Files: []transfer.FileAck{{BatchID: d.Offer.DeliveryID, FileID: e.ID, StoredName: e.Path, Size: e.Size, SHA256: e.SHA256}}}
	if err := c.recordOutboxStatus(review.OutboxID, 0, transfer.DurableStatus{Lease: *d.Lease, State: transfer.Completed, Receipt: receipt}); err != nil {
		t.Fatal(err)
	}
	if row.View.Children[0].DeliveryOutcome != "confirmed" {
		t.Fatal("policy reduction discarded exact retained delivery evidence")
	}
}
