package core

import (
	"github.com/webkaz-labs/sobalink/internal/transfer"
	"reflect"
)

func validateOutboxEvidence(offer transfer.DurableOffer, lease *transfer.DurableLease, status transfer.DurableStatus, prior []OutboxFile) ([]OutboxFile, error) {
	if lease == nil || !reflect.DeepEqual(*lease, status.Lease) || !outboxLeaseMatches(offer, status.Lease) {
		return nil, outboxError("outbox_receipt_mismatch")
	}
	switch status.State {
	case transfer.Pending, transfer.Accepted, transfer.Receiving, transfer.Partial, transfer.Completed, transfer.Cancelled, transfer.Rejected:
	default:
		return nil, outboxError("outbox_receipt_mismatch")
	}
	if status.Receipt == nil {
		if status.State == transfer.Completed || len(prior) != 0 {
			return nil, outboxError("outbox_receipt_regressed")
		}
		return nil, nil
	}
	receipt := status.Receipt
	if !reflect.DeepEqual(receipt.Lease, status.Lease) || receipt.State != status.State || receipt.SavedAt.IsZero() {
		return nil, outboxError("outbox_receipt_mismatch")
	}
	expected := map[string]transfer.Entry{}
	for _, entry := range offer.Manifest.Entries {
		expected[entry.ID] = entry
	}
	seen := map[string]bool{}
	files := []OutboxFile{}
	for _, ack := range receipt.Files {
		entry, ok := expected[ack.FileID]
		if !ok || seen[ack.FileID] || ack.BatchID != offer.DeliveryID || ack.Size != entry.Size || ack.SHA256 != entry.SHA256 || ack.StoredName != entry.Path {
			return nil, outboxError("outbox_receipt_mismatch")
		}
		seen[ack.FileID] = true
		files = append(files, OutboxFile{ack.BatchID, ack.FileID, ack.Size, ack.SHA256})
	}
	if status.State == transfer.Completed && len(seen) != len(expected) {
		return nil, outboxError("outbox_receipt_mismatch")
	}
	for _, old := range prior {
		if !seen[old.FileID] {
			return nil, outboxError("outbox_receipt_regressed")
		}
	}
	return files, nil
}
func validOutboxStatus(s string) bool {
	switch s {
	case "queued", "waiting_for_peer", "sending", "awaiting_receipt", "paused", "blocked_permission", "blocked_recovery", "expired", "cancel_requested", "cancelled", "completed":
		return true
	}
	return false
}
