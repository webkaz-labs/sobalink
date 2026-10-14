package core

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func (c *Core) validateMessageEnvelopeLocked(s messageEnvelope) error {
	fail := func() error { return messageStoreError("message_store_recovery_required") }
	if len(s.Outgoing)+len(s.Incoming) > messageStoreMaxRows {
		return messageStoreError("message_evidence_capacity")
	}
	if s.Version != messageStoreVersion || s.Identity == "" || s.Identity != c.resourceIdentity || c.profile.DurableFiles == nil || s.Epoch != c.profile.DurableFiles.Epoch || !canonicalHex(s.Epoch, 16) || s.HighWater.IsZero() || s.Immediate == nil || s.Outgoing == nil || s.Incoming == nil {
		return fail()
	}
	seen := map[string]bool{}
	for _, m := range s.Immediate {
		key := historyMessageKey(m)
		if !validStoredImmediateMessage(m) || seen[key] {
			return fail()
		}
		seen[key] = true
	}
	for id, row := range s.Outgoing {
		if row == nil || row.Identity != s.Identity || row.SenderID == "" || !config.ValidPeerID(row.SenderID) || row.Network != row.Trust.Network || row.Trust.Generation == 0 || row.Trust.RevocationEpoch == "" || !config.ValidPeerID(row.Trust.ID) || row.Signature == "" {
			return fail()
		}
		r, o := row.View, row.Offer
		if r.Version != 1 || r.Kind != "text_outbox" || r.OutboxID != id || r.RequestID != id || r.MessageID != id || r.PeerID != row.Trust.ID || r.ProcessID != "" || r.ReviewRevision == "" || r.Admission != "enqueued" || r.Text != "" || r.Receipt != nil || len(r.ReasonCode) > 128 || !validTextOutboxStatus(r.Status) || r.UpdatedAt.IsZero() {
			return fail()
		}
		digest, err := durableTextDigest(o)
		if err != nil || digest != o.Digest || o.MessageID != id || o.RecipientID != r.PeerID || o.TextBytes != r.TextBytes || o.TextSHA256 != r.SHA256 || !o.CreatedAt.Equal(r.CreatedAt) || !o.ExpiresAt.Equal(r.ExpiresAt) {
			return fail()
		}
		if row.BodyRetained {
			if !validText(row.Text) || int64(len(row.Text)) != o.TextBytes || textSHA256(row.Text) != o.TextSHA256 {
				return fail()
			}
		} else if row.Text != "" || row.Visible || row.Active || !row.NoMoreSend {
			return fail()
		}
		if row.Lease != nil && !textLeaseMatches(o, *row.Lease) {
			return fail()
		}
		if row.ExposurePossible && (row.Lease == nil || !row.Attempted) {
			return fail()
		}
		if !row.Active && !row.NoMoreSend {
			return fail()
		}
		if row.Receipt != nil && validateTextReceipt(o, row.Lease, row.Receipt) != nil {
			return fail()
		}
		switch r.DeliveryOutcome {
		case "not_attempted":
			if row.ExposurePossible || row.Receipt != nil {
				return fail()
			}
		case "unknown":
			if !row.ExposurePossible || row.Receipt != nil {
				return fail()
			}
		case "confirmed":
			if row.Receipt == nil {
				return fail()
			}
		default:
			return fail()
		}
		if row.Receipt != nil && r.DeliveryOutcome != "confirmed" {
			return fail()
		}
	}
	for id, row := range s.Incoming {
		if row == nil || !config.ValidPeerID(row.PeerID) || row.TrustGeneration == 0 || row.Network == "" || row.RevocationEpoch == "" || row.Offer.MessageID != id || row.Lease.ReceiverEpoch != s.Epoch || !textLeaseMatches(row.Offer, row.Lease) {
			return fail()
		}
		if row.BodyRetained {
			if !validText(row.Text) || int64(len(row.Text)) != row.Offer.TextBytes || textSHA256(row.Text) != row.Offer.TextSHA256 {
				return fail()
			}
		} else if row.Text != "" || row.Visible {
			return fail()
		}
		switch row.State {
		case "offered", "expired", "cancelled":
			if row.BodyRetained || row.Visible || row.Receipt != nil {
				return fail()
			}
		case "received":
			if validateTextReceipt(row.Offer, &row.Lease, row.Receipt) != nil {
				return fail()
			}
		default:
			return fail()
		}
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fail()
	}
	if len(data) > messageStoreHardBytes {
		return messageStoreError("message_evidence_capacity")
	}
	return nil
}
func validStoredImmediateMessage(m Message) bool {
	return validText(m.Text) && len(m.Text) <= messageHardTextBytes && config.ValidPeerID(m.ID) && !strings.HasPrefix(m.ID, "mt1_") && config.ValidPeerID(m.PeerID) && !m.CreatedAt.IsZero() && (m.Direction == "incoming" || m.Direction == "outgoing") && (m.Status == "pending" || m.Status == "received" || m.Status == "sent" || m.Status == "failed")
}
func validTextOutboxStatus(status string) bool {
	switch status {
	case "queued", "waiting_peer", "delivering", "sending", "completed", "cancel_requested", "cancelled", "expired", "needs_review", "paused", "blocked", "failed":
		return true
	}
	return false
}

// Existing rows cannot replace immutable authority, regress exact evidence or
// reveal a body hidden by reviewed history cleanup. Evidence retirement is
// conservative: unknown outcomes and visible history are never dropped.
func validateMessageTransition(before, after messageEnvelope) error {
	fail := func() error { return messageStoreError("message_conflict") }
	if before.Version != after.Version || before.Identity != after.Identity || before.Epoch != after.Epoch || after.HighWater.Before(before.HighWater) {
		return fail()
	}
	for id, old := range before.Outgoing {
		next := after.Outgoing[id]
		if next == nil {
			horizon, ok := textRequestHorizon(id)
			if !ok || after.HighWater.Before(horizon) || old.Active || !old.NoMoreSend || old.Visible || old.View.DeliveryOutcome == "unknown" {
				return fail()
			}
			continue
		}
		if old.Identity != next.Identity || old.Network != next.Network || old.SenderID != next.SenderID || old.Signature != next.Signature || !reflect.DeepEqual(old.Trust, next.Trust) || old.Offer.Digest != next.Offer.Digest || old.View.ReviewRevision != next.View.ReviewRevision {
			return fail()
		}
		if old.Lease != nil && (next.Lease == nil || !sameTextLease(*old.Lease, *next.Lease)) {
			return fail()
		}
		if old.Receipt != nil && !reflect.DeepEqual(old.Receipt, next.Receipt) {
			return fail()
		}
		if !old.Visible && next.Visible || !old.BodyRetained && next.BodyRetained || old.Attempted && !next.Attempted || old.ExposurePossible && !next.ExposurePossible || old.NoMoreSend && !next.NoMoreSend || old.Cancelled && !next.Cancelled || !old.Active && next.Active {
			return fail()
		}
		if old.BodyRetained && next.BodyRetained && old.Text != next.Text {
			return fail()
		}
	}
	for id, old := range before.Incoming {
		next := after.Incoming[id]
		if next == nil {
			horizon, ok := textRequestHorizon(id)
			if !ok || after.HighWater.Before(horizon) || old.Visible || old.State == "offered" {
				return fail()
			}
			continue
		}
		if old.PeerID != next.PeerID || old.Network != next.Network || old.TrustGeneration != next.TrustGeneration || old.RevocationEpoch != next.RevocationEpoch || old.Offer.Digest != next.Offer.Digest || !sameTextLease(old.Lease, next.Lease) {
			return fail()
		}
		if old.State != "offered" && old.State != next.State {
			return fail()
		}
		if old.Receipt != nil && (!reflect.DeepEqual(old.Receipt, next.Receipt) || !old.Visible && next.Visible || !old.BodyRetained && next.BodyRetained) {
			return fail()
		}
		if old.BodyRetained && next.BodyRetained && old.Text != next.Text {
			return fail()
		}
	}
	return nil
}

// Whole-snapshot reservation includes worst-case JSON escaping before an
// incoming body exists, plus the maximum exact receipt/status expansion. Once
// final, actual retained bytes remain charged until a certified cleanup write.
func messageOutgoingReservation(row *messageOutgoing) int64 {
	actual, _ := json.Marshal(row)
	base := *row
	base.Lease, base.Receipt = nil, nil
	base.View.Status, base.View.DeliveryOutcome, base.View.ReasonCode = "", "", ""
	base.View.UpdatedAt = base.View.CreatedAt
	base.Attempted, base.ExposurePossible, base.Cancelled, base.NoMoreSend, base.Active, base.Visible = false, false, false, false, false, false
	encoded, _ := json.Marshal(base)
	// All mutable lease/receipt/outcome expansion is charged once, so recording
	// a lease later consumes this reservation rather than increasing admission.
	reserve := int64(len(encoded)) + messageReceiptReserve
	if row.Receipt != nil || row.NoMoreSend && !row.Active && !row.ExposurePossible {
		reserve = int64(len(actual))
	}
	return max(reserve, int64(len(actual))) + int64(len(row.View.OutboxID)) + 4
}
func messageIncomingReservation(row *messageIncoming) int64 {
	actual, _ := json.Marshal(row)
	reserved := int64(len(actual))
	if row.State == "offered" {
		reserved += 6*row.Offer.TextBytes + messageReceiptReserve
	}
	return reserved + int64(len(row.Offer.MessageID)) + 4
}
func messageSnapshotReserved(s messageEnvelope) int64 {
	base := s
	base.Outgoing, base.Incoming = map[string]*messageOutgoing{}, map[string]*messageIncoming{}
	data, _ := json.Marshal(base)
	reserved := int64(len(data))
	for _, row := range s.Outgoing {
		reserved += messageOutgoingReservation(row)
	}
	for _, row := range s.Incoming {
		reserved += messageIncomingReservation(row)
	}
	return reserved
}
func (c *Core) messageAdmissionCapacityLocked(state messageEnvelope) error {
	return c.checkMessageCapacityWithAdmissionLocked(state, state, true)
}
func (c *Core) checkMessageCapacityLocked(before, after messageEnvelope) error {
	return c.checkMessageCapacityWithAdmissionLocked(before, after, false)
}
func (c *Core) checkMessageCapacityWithAdmissionLocked(before, after messageEnvelope, admission bool) error {
	preparedRows, preparedBytes, preparedActive, preparedPeers := c.messageQueueReservationsLocked(after.Outgoing)
	if len(after.Outgoing)+len(after.Incoming)+preparedRows > messageStoreMaxRows {
		return messageStoreError("message_evidence_capacity")
	}
	outActive, inActive := preparedActive, 0
	outPeer, inPeer := map[string]int{}, map[string]int{}
	for peer, n := range preparedPeers {
		outPeer[peer] = n
	}
	for _, row := range after.Outgoing {
		if row.Active {
			outActive++
			outPeer[row.View.PeerID]++
		}
	}
	for _, row := range after.Incoming {
		if row.State == "offered" {
			inActive++
			inPeer[row.PeerID]++
		}
	}
	if outActive > messageActiveLimit || inActive > messageActiveLimit {
		return messageStoreError("message_active_capacity")
	}
	for _, n := range outPeer {
		if n > messageActivePeerLimit {
			return messageStoreError("message_peer_capacity")
		}
	}
	for _, n := range inPeer {
		if n > messageActivePeerLimit {
			return messageStoreError("message_peer_capacity")
		}
	}
	reserved := messageSnapshotReserved(after) + preparedBytes
	if reserved > messageStoreHardBytes {
		return messageStoreError("message_evidence_capacity")
	}
	admission = admission || len(after.Immediate) > len(before.Immediate)
	for id := range after.Outgoing {
		if before.Outgoing[id] == nil {
			admission = true
		}
	}
	for id := range after.Incoming {
		if before.Incoming[id] == nil {
			admission = true
		}
	}
	// A lowered configured quota cannot strand previously reserved receipt/body
	// finalization, status, cancellation, or known-record cleanup.
	if admission {
		if reserved > min(c.messageHistoryStorageBytesLocked(), int64(messageStoreHardBytes)) {
			return messageStoreError("message_evidence_capacity")
		}
	}
	return nil
}
