package core

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// This captures the local backend selected while authenticating the HTTP
// request. The lease is evidence, never authority to use a replacement backend.
type durableTextTransportKey struct{}
type durableTextTransport struct {
	backend   NetworkBackend
	recipient string
}

func durableTextReceiverError(code string) error {
	return &localCommandError{code, code}
}

// Caller holds Core.mu. No generation lock spans a storage operation: the
// ordinary incoming admission lease is released only after the handler's
// transaction has returned, including a late or uncertain publication.
func (c *Core) admitDurableTextReceiverLocked(ctx context.Context, trust Trust, recipient string) (func(), error) {
	if c.closing || c.ctx.Err() != nil || ctx.Err() != nil || !c.currentDurableTextTrustLocked(trust, true) {
		return nil, durableTextReceiverError("message_permission")
	}
	if selected, ok := ctx.Value(durableTextTransportKey{}).(durableTextTransport); ok {
		if selected.backend == nil || selected.backend != c.node || selected.recipient != recipient {
			return nil, durableTextReceiverError("message_permission")
		}
	} else if _, networkRequest := ctx.Value(peerIncomingRequestKey{}).(*peerIncomingRequest); networkRequest {
		return nil, durableTextReceiverError("message_permission")
	}
	release, err := incomingPeerMessageCommit(ctx)
	if err != nil {
		return nil, durableTextReceiverError("message_permission")
	}
	return release, nil
}

func (c *Core) currentDurableTextTrustLocked(trust Trust, requireUnpaused bool) bool {
	if trust.ID == "" || trust.Network == "" || trust.Generation == 0 || trust.RevocationEpoch == "" || trust.Network != c.profile.Settings.Network || c.managedDenied[trust.ID] || c.startup.Revocations[trust.ID] != trust.RevocationEpoch {
		return false
	}
	for _, current := range c.profile.Peers {
		if current.ID == trust.ID && current.Network == trust.Network && current.Generation == trust.Generation && current.RevocationEpoch == trust.RevocationEpoch && (!requireUnpaused || !current.Paused) {
			return true
		}
	}
	return false
}

func (c *Core) durableTextReceiverClockLocked() (time.Time, error) {
	if c.messageStore == nil || c.profile.DurableFiles == nil || c.profile.DurableFiles.Phase != "ready" || c.durableProfileError != "" {
		return time.Time{}, messageStoreError("message_store_recovery_required")
	}
	if c.messageStore.frozen != "" {
		return time.Time{}, messageStoreError(c.messageStore.frozen)
	}
	now := c.messageStore.now().UTC()
	if now.Before(c.messageStore.state.HighWater) {
		return time.Time{}, durableTextReceiverError("message_clock_rollback")
	}
	return now, nil
}

func incomingTextRelationshipMatches(row *messageIncoming, trust Trust) bool {
	return row.PeerID == trust.ID && row.Network == trust.Network && row.TrustGeneration == trust.Generation && row.RevocationEpoch == trust.RevocationEpoch
}

func sameDurableTextOffer(a, b DurableTextOffer) bool {
	return a.Version == b.Version && a.MessageID == b.MessageID && a.RecipientID == b.RecipientID && a.TextBytes == b.TextBytes && a.TextSHA256 == b.TextSHA256 && a.CreatedAt.Equal(b.CreatedAt) && a.ExpiresAt.Equal(b.ExpiresAt) && a.Digest == b.Digest
}

func (c *Core) offerDurableText(ctx context.Context, trust Trust, offer DurableTextOffer) (DurableTextStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero DurableTextStatus
	release, err := c.admitDurableTextReceiverLocked(ctx, trust, offer.RecipientID)
	if err != nil {
		return zero, err
	}
	defer release()
	now, err := c.durableTextReceiverClockLocked()
	if err != nil {
		return zero, err
	}
	digest, err := durableTextDigest(offer)
	if err != nil || digest != offer.Digest {
		return zero, durableTextReceiverError("message_invalid")
	}
	if offer.SentAt.IsZero() || offer.SentAt.Before(now.Add(-textClockSkew)) || offer.SentAt.After(now.Add(textClockSkew)) || offer.CreatedAt.After(now.Add(textClockSkew)) {
		return zero, durableTextReceiverError("message_clock")
	}
	if row := c.messageStore.state.Incoming[offer.MessageID]; row != nil {
		if !incomingTextRelationshipMatches(row, trust) {
			return zero, durableTextReceiverError("message_permission")
		}
		if !sameDurableTextOffer(row.Offer, offer) {
			return zero, durableTextReceiverError("message_conflict")
		}
		if row.State == "offered" && !now.Before(row.Offer.ExpiresAt) {
			if err = c.expireDurableTextLocked(offer.MessageID); err != nil {
				return zero, err
			}
			row = c.messageStore.state.Incoming[offer.MessageID]
		}
		// An ACK can disappear after the reservation was saved. Never issue a
		// replacement token, change its horizon, or resurrect terminal state.
		return durableTextStatusForRow(row, now), nil
	}
	horizon, _ := textRequestHorizon(offer.MessageID)
	if !now.Before(horizon) || !now.Before(offer.ExpiresAt) {
		return zero, durableTextReceiverError("message_expired")
	}
	if offer.TextBytes > policyOrDefault(c.capacity).Number("resources", "messageTextBytes") {
		return zero, durableTextReceiverError("message_too_large")
	}
	lease := transfer.DurableLease{Version: DurableTextVersion, DeliveryID: offer.MessageID, ReceiverEpoch: c.messageStore.state.Epoch, Token: randomID(), Digest: offer.Digest, RecipientID: offer.RecipientID, ExpiresAt: offer.ExpiresAt, ReceiptUntil: horizon}
	err = c.mutateMessageStoreLocked(func(state *messageEnvelope) error {
		state.Incoming[offer.MessageID] = &messageIncoming{PeerID: trust.ID, Network: trust.Network, TrustGeneration: trust.Generation, RevocationEpoch: trust.RevocationEpoch, Offer: offer, Lease: lease, State: "offered"}
		return nil
	})
	if err != nil {
		return zero, err
	}
	return durableTextStatusForRow(c.messageStore.state.Incoming[offer.MessageID], now), nil
}

func (c *Core) incomingDurableTextLocked(trust Trust, lease transfer.DurableLease) (*messageIncoming, error) {
	row := c.messageStore.state.Incoming[lease.DeliveryID]
	if row == nil || lease.ReceiverEpoch != c.messageStore.state.Epoch {
		return nil, durableTextReceiverError("message_unknown_lease")
	}
	if !incomingTextRelationshipMatches(row, trust) {
		return nil, durableTextReceiverError("message_permission")
	}
	if !sameTextLease(row.Lease, lease) {
		return nil, durableTextReceiverError("message_unknown_lease")
	}
	return row, nil
}

func (c *Core) deliverDurableText(ctx context.Context, trust Trust, request DurableTextDeliver) (DurableTextStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero DurableTextStatus
	release, err := c.admitDurableTextReceiverLocked(ctx, trust, request.Lease.RecipientID)
	if err != nil {
		return zero, err
	}
	defer release()
	now, err := c.durableTextReceiverClockLocked()
	if err != nil {
		return zero, err
	}
	row, err := c.incomingDurableTextLocked(trust, request.Lease)
	if err != nil {
		return zero, err
	}
	if !validText(request.Text) || int64(len(request.Text)) != row.Offer.TextBytes || textSHA256(request.Text) != row.Offer.TextSHA256 {
		return zero, durableTextReceiverError("message_conflict")
	}
	if row.State == "received" {
		// The body may have been explicitly removed from visible history.
		// Return the immutable receipt without touching body, visibility or time.
		return durableTextStatusForRow(row, now), nil
	}
	if row.State != "offered" {
		if row.State == "expired" {
			return zero, durableTextReceiverError("message_expired")
		}
		return zero, durableTextReceiverError("message_permission")
	}
	if !now.Before(row.Offer.ExpiresAt) {
		if err = c.expireDurableTextLocked(row.Offer.MessageID); err != nil {
			return zero, err
		}
		return zero, durableTextReceiverError("message_expired")
	}
	receipt := DurableTextReceipt{Lease: row.Lease, MessageID: row.Offer.MessageID, TextBytes: row.Offer.TextBytes, TextSHA256: row.Offer.TextSHA256, SavedAt: now}
	err = c.mutateMessageStoreLocked(func(state *messageEnvelope) error {
		stored := state.Incoming[request.Lease.DeliveryID]
		stored.State, stored.Text, stored.BodyRetained, stored.Visible = "received", request.Text, true, true
		stored.Receipt = &receipt
		return nil
	})
	if err != nil {
		return zero, err
	}
	return durableTextStatusForRow(c.messageStore.state.Incoming[request.Lease.DeliveryID], now), nil
}

func (c *Core) expireDurableTextLocked(id string) error {
	return c.mutateMessageStoreLocked(func(state *messageEnvelope) error {
		if row := state.Incoming[id]; row != nil && row.State == "offered" {
			row.State = "expired"
		}
		return nil
	})
}

func (c *Core) statusDurableText(ctx context.Context, trust Trust, lease transfer.DurableLease) (DurableTextStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero DurableTextStatus
	release, err := c.admitDurableTextReceiverLocked(ctx, trust, lease.RecipientID)
	if err != nil {
		return zero, err
	}
	defer release()
	now, err := c.durableTextReceiverClockLocked()
	if err != nil {
		return zero, err
	}
	row, err := c.incomingDurableTextLocked(trust, lease)
	if err != nil {
		return zero, err
	}
	if row.State == "offered" && !now.Before(row.Offer.ExpiresAt) {
		if err = c.expireDurableTextLocked(row.Offer.MessageID); err != nil {
			return zero, err
		}
		row = c.messageStore.state.Incoming[lease.DeliveryID]
	}
	return durableTextStatusForRow(row, now), nil
}

func durableTextStatusForRow(row *messageIncoming, now time.Time) DurableTextStatus {
	status := DurableTextStatus{Lease: row.Lease, State: row.State, ReceiverNow: now}
	if row.Receipt != nil {
		receipt := *row.Receipt
		status.Receipt = &receipt
	}
	if row.State == "expired" {
		status.ReasonCode = "message_expired"
	} else if row.State == "cancelled" {
		status.ReasonCode = "message_permission"
	}
	return status
}

// Called by maintenance while holding Core.mu. Deleting a hidden terminal
// record is safe only after its timestamped ID is permanently inadmissible.
// Retained received evidence survives expiry/revocation and visible rows are
// never silently removed merely to recover evidence capacity.
func (c *Core) maintainDurableTextReceiverLocked(now time.Time) error {
	clock, err := c.durableTextReceiverClockLocked()
	if err != nil {
		return err
	}
	// The store's clock is authoritative, including deterministic test clocks.
	now = clock
	changed := false
	for _, row := range c.messageStore.state.Incoming {
		trust := Trust{ID: row.PeerID, Network: row.Network, Generation: row.TrustGeneration, RevocationEpoch: row.RevocationEpoch}
		if row.State == "offered" && (!now.Before(row.Offer.ExpiresAt) || !c.currentDurableTextTrustLocked(trust, false)) || row.State != "offered" && !row.Visible && !now.Before(row.Lease.ReceiptUntil) {
			changed = true
			break
		}
	}
	if !changed {
		return nil
	}
	return c.mutateMessageStoreLocked(func(state *messageEnvelope) error {
		for id, row := range state.Incoming {
			wasOffered := row.State == "offered"
			if wasOffered {
				trust := Trust{ID: row.PeerID, Network: row.Network, Generation: row.TrustGeneration, RevocationEpoch: row.RevocationEpoch}
				if !c.currentDurableTextTrustLocked(trust, false) {
					row.State = "cancelled"
				} else if !now.Before(row.Offer.ExpiresAt) {
					row.State = "expired"
				}
			}
			if !wasOffered && !row.Visible && !now.Before(row.Lease.ReceiptUntil) {
				delete(state.Incoming, id)
			}
		}
		return nil
	})
}

// Routes have a bounded decoder and share the authenticated request's exact
// transport context. Status and already admitted deliveries use hard bounds,
// not newly lowered admission policy limits.
func (c *Core) serveDurableTextHTTP(w http.ResponseWriter, r *http.Request, trust Trust) bool {
	if !strings.HasPrefix(r.URL.Path, "/v1/durable-messages/") {
		return false
	}
	if r.Method != http.MethodPost {
		peerFailure(w, http.StatusMethodNotAllowed)
		return true
	}
	var input any
	var recipient string
	var err error
	var offer DurableTextOffer
	var delivery DurableTextDeliver
	var lease transfer.DurableLease
	limit := int64(8192)
	switch r.URL.Path {
	case DurableTextOfferPath:
		input = &offer
	case DurableTextDeliverPath:
		input = &delivery
		limit = int64(6*messageHardTextBytes + 8192)
	case DurableTextStatusPath:
		input = &lease
	default:
		peerFailure(w, http.StatusNotFound)
		return true
	}
	backend := c.nodeCopy()
	if err = readJSON(r, limit, input); err != nil {
		code := "message_invalid"
		if errors.Is(err, errPeerRequestTooLarge) {
			code = "request_too_large"
		}
		replyDurableTextError(w, durableTextReceiverError(code))
		return true
	}
	switch r.URL.Path {
	case DurableTextOfferPath:
		recipient = offer.RecipientID
	case DurableTextDeliverPath:
		recipient = delivery.Lease.RecipientID
	case DurableTextStatusPath:
		recipient = lease.RecipientID
	}
	if _, err = c.durableRecipientID(r, recipient); err == nil {
		var remote netip.AddrPort
		remote, err = netip.ParseAddrPort(r.RemoteAddr)
		if err == nil {
			var peer string
			peer, err = c.authenticated(r.Context(), remote)
			if peer != trust.ID {
				err = durableTextReceiverError("message_permission")
			}
		}
	}
	if err != nil {
		replyDurableTextError(w, durableTextReceiverError("message_permission"))
		return true
	}
	ctx := context.WithValue(r.Context(), durableTextTransportKey{}, durableTextTransport{backend: backend, recipient: recipient})
	var result any
	switch r.URL.Path {
	case DurableTextOfferPath:
		result, err = c.offerDurableText(ctx, trust, offer)
	case DurableTextDeliverPath:
		result, err = c.deliverDurableText(ctx, trust, delivery)
	case DurableTextStatusPath:
		result, err = c.statusDurableText(ctx, trust, lease)
	}
	if err != nil {
		replyDurableTextError(w, err)
	} else {
		reply(w, http.StatusOK, result)
	}
	return true
}

func replyDurableTextError(w http.ResponseWriter, err error) {
	code := "message_store_unavailable"
	var local *localCommandError
	if errors.As(err, &local) {
		code = local.code
	}
	status := http.StatusConflict
	switch code {
	case "message_invalid":
		status = http.StatusBadRequest
	case "message_permission":
		status = http.StatusForbidden
	case "message_too_large", "request_too_large":
		status = http.StatusRequestEntityTooLarge
	case "message_active_capacity", "message_peer_capacity", "message_storage_busy":
		status = http.StatusTooManyRequests
	case "message_evidence_capacity", "message_storage_capacity", "message_store_unavailable", "message_storage_uncertain", "message_store_recovery_required":
		status = http.StatusInsufficientStorage
	}
	reply(w, status, map[string]string{"code": code, "error": code})
}
