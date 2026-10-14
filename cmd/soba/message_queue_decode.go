package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

func textQueueCLISameOriginal(a, b core.TextOutboxRecord) bool {
	return a.OutboxID == b.OutboxID && a.RequestID == b.RequestID && a.MessageID == b.MessageID && a.PeerID == b.PeerID && a.TextBytes == b.TextBytes && a.SHA256 == b.SHA256 && a.CreatedAt.Equal(b.CreatedAt) && a.ExpiresAt.Equal(b.ExpiresAt)
}
func textQueueCLISameReview(a, b core.TextOutboxRecord) bool {
	return textQueueCLISameOriginal(a, b) && a.ReviewRevision == b.ReviewRevision
}
func textQueueCLIWorkStatus(status string) bool {
	switch status {
	case "prepared", "discarded", "queued", "waiting_peer", "delivering", "completed", "cancel_requested", "cancelled", "expired", "paused", "blocked", "needs_review":
		return true
	}
	return false
}
func textQueueCLIDecode(raw []byte, process, id string, ja, list bool) (core.TextOutboxRecord, error) {
	var r core.TextOutboxRecord
	invalid := func() (core.TextOutboxRecord, error) { return r, textQueueCLIInvalid(ja) }
	if resource.Decode(raw, 16<<20, &r) != nil {
		return invalid()
	}
	if !outboxCLIRequiredFields(raw, "version", "kind", "outboxId", "requestId", "messageId", "peerId", "reviewRevision", "admission", "status", "deliveryOutcome", "reasonCode", "textBytes", "sha256", "createdAt", "expiresAt", "updatedAt") {
		return invalid()
	}
	created, ok := textQueueCLIRequestTime(r.OutboxID)
	if !ok || r.Version != 1 || r.Kind != "text_outbox" || r.RequestID != r.OutboxID || r.MessageID != r.OutboxID || id != "" && r.OutboxID != id || !config.ValidPeerID(r.PeerID) || !resource.ValidDigest(r.ReviewRevision) || !resource.ValidDigest(r.SHA256) || !textQueueCLIWorkStatus(r.Status) || r.ReasonCode != "" && !validErrorCode(r.ReasonCode) || r.TextBytes < 1 || r.TextBytes > textQueueCLIHardTextBytes || !r.CreatedAt.Equal(created) || r.ExpiresAt.Before(created.Add(time.Minute)) || r.ExpiresAt.After(created.Add(7*24*time.Hour)) || r.UpdatedAt.IsZero() {
		return invalid()
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if body, present := fields["text"]; present {
		if list || len(body) == 0 || !textQueueCLIValidText(r.Text) || int64(len(r.Text)) != r.TextBytes || textQueueCLIHash(r.Text) != r.SHA256 {
			return invalid()
		}
	}
	switch r.Admission {
	case "prepared":
		if !resource.ValidDigest(r.ProcessID) || process != "" && r.ProcessID != process || r.Status != "prepared" && r.Status != "discarded" || r.DeliveryOutcome != "not_attempted" {
			return invalid()
		}
		if !list && r.Status == "prepared" && r.Text == "" || r.Status == "discarded" && r.Text != "" {
			return invalid()
		}
	case "enqueued":
		if _, present := fields["processId"]; present {
			return invalid()
		}
		if r.Status == "prepared" || r.Status == "discarded" {
			return invalid()
		}
	default:
		return invalid()
	}
	switch r.DeliveryOutcome {
	case "not_attempted", "unknown":
		if r.Receipt != nil || r.Status == "completed" {
			return invalid()
		}
	case "confirmed":
		if r.Admission != "enqueued" || r.Receipt == nil || !textQueueCLIReceiptValid(r, fields["receipt"]) {
			return invalid()
		}
	default:
		return invalid()
	}
	return r, nil
}
func textQueueCLIOfferDigest(r core.TextOutboxRecord) string {
	offer := core.DurableTextOffer{Version: 1, MessageID: r.MessageID, RecipientID: r.PeerID, TextBytes: r.TextBytes, TextSHA256: r.SHA256, CreatedAt: r.CreatedAt.UTC(), ExpiresAt: r.ExpiresAt.UTC()}
	data, err := json.Marshal(offer)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte("sobalink/durable-text/v1\x00"), data...))
	return hex.EncodeToString(sum[:])
}
func textQueueCLIReceiptValid(r core.TextOutboxRecord, raw []byte) bool {
	if r.Receipt == nil || !outboxCLIRequiredFields(raw, "lease", "messageId", "textBytes", "textSHA256", "savedAt") {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || !outboxCLIRequiredFields(fields["lease"], "version", "deliveryId", "digest", "receiverEpoch", "token", "recipientId", "expiresAt", "receiptUntil") {
		return false
	}
	receipt, lease := r.Receipt, r.Receipt.Lease
	horizon := r.CreatedAt.Add(8*24*time.Hour + 5*time.Minute)
	return receipt.MessageID == r.MessageID && receipt.TextBytes == r.TextBytes && receipt.TextSHA256 == r.SHA256 && !receipt.SavedAt.IsZero() && !receipt.SavedAt.Before(r.CreatedAt.Add(-5*time.Minute)) && !receipt.SavedAt.After(horizon) && lease.Version == 1 && lease.DeliveryID == r.MessageID && lease.RecipientID == r.PeerID && lease.ExpiresAt.Equal(r.ExpiresAt) && lease.ReceiptUntil.Equal(horizon) && lease.Digest == textQueueCLIOfferDigest(r) && textQueueCLIHex(lease.ReceiverEpoch, 16) && textQueueCLIHex(lease.Token, 16)
}

type textQueueCLIListItem struct {
	core.TextOutboxRecord
	ID string `json:"id"`
}
type textQueueCLIPage struct {
	Version    int                    `json:"version"`
	Kind       string                 `json:"kind"`
	Status     string                 `json:"status"`
	ReasonCode string                 `json:"reasonCode"`
	Items      []textQueueCLIListItem `json:"items"`
	Revision   string                 `json:"revision"`
	NextCursor string                 `json:"nextCursor,omitempty"`
	Total      int                    `json:"total"`
}

func textQueueCLIListDecode(raw []byte, cursor, revision string, ja bool) (textQueueCLIPage, error) {
	var page textQueueCLIPage
	invalid := func() (textQueueCLIPage, error) { return page, textQueueCLIInvalid(ja) }
	if resource.Decode(raw, 16<<20, &page) != nil || !outboxCLIRequiredFields(raw, "version", "kind", "status", "reasonCode", "items", "revision", "total") || page.Version != 1 || page.Kind != "text_outbox" || !resource.ValidDigest(page.Revision) || revision != "" && page.Revision != revision || page.Total < len(page.Items) || page.Total > 2048 || page.Items == nil || page.Status != "ready" && page.Status != "blocked" {
		return invalid()
	}
	if page.Status == "ready" && page.ReasonCode != "" || page.Status == "blocked" && !validErrorCode(page.ReasonCode) {
		return invalid()
	}
	var fields struct {
		Items []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(raw, &fields) != nil {
		return invalid()
	}
	previous := cursor
	for i, item := range page.Items {
		if item.ID != item.OutboxID || item.ID <= previous || !outboxCLIRequiredFields(fields.Items[i], "id") {
			return invalid()
		}
		// Validate the original shape before removing only the pagination alias.
		var row map[string]json.RawMessage
		if json.Unmarshal(fields.Items[i], &row) != nil {
			return invalid()
		}
		delete(row, "id")
		record, err := json.Marshal(row)
		if err != nil {
			return invalid()
		}
		if _, err = textQueueCLIDecode(record, "", item.ID, ja, true); err != nil {
			return invalid()
		}
		previous = item.ID
	}
	if page.NextCursor != "" && (len(page.Items) == 0 || page.NextCursor != previous || page.Total <= len(page.Items)) {
		return invalid()
	}
	if cursor == "" && page.NextCursor == "" && page.Total != len(page.Items) {
		return invalid()
	}
	return page, nil
}
