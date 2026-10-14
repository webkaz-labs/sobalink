package core

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
)

func (c *Core) messageQueueCommand(ctx context.Context, name, id string, raw json.RawMessage) (any, error) {
	var input struct {
		ProcessID      string    `json:"processId"`
		PeerID         string    `json:"peerId"`
		Text           string    `json:"text"`
		ExpiresAt      time.Time `json:"expiresAt"`
		OutboxID       string    `json:"outboxId"`
		ReviewRevision string    `json:"reviewRevision"`
		Confirm        bool      `json:"confirm"`
		Cursor         string    `json:"cursor"`
		Revision       string    `json:"revision"`
	}
	if err := decodePayload(raw, &input); err != nil {
		return nil, err
	}
	if name == "message.queue.prepare" {
		return c.prepareMessageQueue(ctx, id, input.ProcessID, input.PeerID, input.Text, input.ExpiresAt)
	}
	if name == "message.queue.list" {
		return c.listMessageQueue(input.Cursor, input.Revision)
	}
	if _, ok := textRequestCreated(input.OutboxID); !ok {
		return nil, messageStoreError("message_invalid")
	}
	switch name {
	case "message.queue.status":
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.reconcileMessageQueueStatusLocked(input.OutboxID)
	case "message.queue.commit":
		return c.commitMessageQueue(ctx, input.OutboxID, input.ProcessID, input.ReviewRevision, input.Confirm)
	case "message.queue.cancel":
		return c.cancelMessageQueue(ctx, input.OutboxID)
	case "message.queue.discard":
		return c.discardMessageQueue(input.OutboxID, input.ProcessID)
	}
	return nil, messageStoreError("message_invalid")
}

// Only the explicit status command invokes reconciliation. Internal lookups,
// especially cancellation of an uncertain new admission, remain read-only.
func (c *Core) reconcileMessageQueueStatusLocked(id string) (TextOutboxRecord, error) {
	if c.messageStore == nil {
		return TextOutboxRecord{}, messageStoreError("message_store_recovery_required")
	}
	// An explicit inquiry for the exact changed outgoing row may re-certify only
	// its already-published candidate. Unrelated or unknown IDs cannot clear a
	// whole-store uncertainty, and reconciliation never repeats a mutation/send.
	if s := c.messageStore; s.frozen == "message_storage_uncertain" && s.candidate != nil && s.candidate.Outgoing[id] != nil && !reflect.DeepEqual(s.state.Outgoing[id], s.candidate.Outgoing[id]) {
		if err := c.reconcileMessageStoreLocked(); err != nil {
			view, _ := c.messageQueueViewLocked(id)
			return view, err
		}
	}
	return c.messageQueueStatusLocked(id)
}

func (c *Core) messageQueueStatusLocked(id string) (TextOutboxRecord, error) {
	if c.messageStore == nil {
		return TextOutboxRecord{}, messageStoreError("message_store_recovery_required")
	}
	// An uncertified candidate may contain this locator. Never turn an unknown
	// commit into not_found or tell a client to prepare a different message ID.
	if c.messageStore.frozen != "" {
		return c.messageQueueViewLocked(id)
	}
	if c.messageStore.state.Outgoing[id] != nil {
		return c.messageQueueViewLocked(id)
	}
	if c.messageQueue != nil {
		if p := c.messageQueue.prepared[id]; p != nil {
			return messagePreparationView(p), nil
		}
	}
	if horizon, ok := textRequestHorizon(id); ok && !c.messageQueueNowLocked().Before(horizon) {
		return TextOutboxRecord{}, messageStoreError("message_retired")
	}
	return TextOutboxRecord{}, messageStoreError("message_not_found")
}
func (c *Core) listMessageQueue(cursor, revision string) (any, error) {
	c.mu.Lock()
	rows := []map[string]any{}
	if c.messageStore == nil {
		c.mu.Unlock()
		return nil, messageStoreError("message_store_recovery_required")
	}
	for _, row := range c.messageStore.state.Outgoing {
		view := row.View
		if row.Receipt != nil {
			receipt := *row.Receipt
			view.Receipt = &receipt
		}
		rows = append(rows, messageQueueListMap(view))
	}
	if c.messageQueue != nil && c.messageStore.frozen == "" {
		for id, p := range c.messageQueue.prepared {
			if c.messageStore.state.Outgoing[id] == nil {
				rows = append(rows, messageQueueListMap(messagePreparationView(p)))
			}
		}
	}
	reason := c.messageStore.frozen
	c.mu.Unlock()
	page, err := c.pageRows(rows, cursor, revision)
	if err != nil {
		return nil, err
	}
	status := "ready"
	if reason != "" {
		status = "blocked"
	}
	return struct {
		localPage
		Kind       string `json:"kind"`
		Status     string `json:"status"`
		ReasonCode string `json:"reasonCode"`
	}{page, "text_outbox", status, reason}, nil
}
func (c *Core) messageQueueTrustLocked(id string) (Trust, error) {
	for _, peer := range c.profile.Peers {
		if peer.ID == id && peer.Network == c.profile.Settings.Network && !peer.Paused && !c.managedDenied[id] && peer.RevocationEpoch != "" && c.startup.Revocations[id] == peer.RevocationEpoch {
			return Trust{ID: peer.ID, Network: peer.Network, Generation: peer.Generation, RevocationEpoch: peer.RevocationEpoch}, nil
		}
	}
	return Trust{}, messageStoreError("message_permission")
}
func (c *Core) prepareMessageQueue(ctx context.Context, id, process, peerID, text string, expires time.Time) (any, error) {
	if !validText(text) || !config.ValidPeerID(peerID) {
		return nil, messageStoreError("message_invalid")
	}
	defaultExpiry := expires.IsZero()
	created, validID := textRequestCreated(id)
	if !validID {
		return nil, messageStoreError("message_invalid")
	}
	c.mu.Lock()
	if err := c.messageQueueGuardLocked(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if err := c.checkDispatchProcessLocked(process); err != nil {
		c.mu.Unlock()
		return nil, messageStoreError("message_stale")
	}
	queue := c.messageQueueRuntimeLocked()
	now := c.messageQueueNowLocked()
	if old := c.messageStore.state.Outgoing[id]; old != nil {
		if expires.IsZero() {
			expires = old.View.ExpiresAt
		}
		if old.Signature != messageQueueSignature(peerID, text, expires) {
			c.mu.Unlock()
			return nil, messageStoreError("message_conflict")
		}
		view, err := c.messageQueueViewLocked(id)
		c.mu.Unlock()
		return view, err
	}
	if old := queue.prepared[id]; old != nil {
		if expires.IsZero() {
			expires = old.row.View.ExpiresAt
		}
		if old.discarded || old.row.Signature != messageQueueSignature(peerID, text, expires) {
			c.mu.Unlock()
			return nil, messageStoreError("message_conflict")
		}
		if old.row.View.ProcessID != process || !now.Before(old.row.View.CreatedAt.Add(transferPreparationLifetime)) {
			c.mu.Unlock()
			return nil, messageStoreError("message_stale")
		}
		view := messagePreparationView(old)
		c.mu.Unlock()
		return view, nil
	}
	if !textRequestFresh(id, now) {
		c.mu.Unlock()
		return nil, messageStoreError("message_stale")
	}
	if expires.IsZero() {
		expires = now.Add(24 * time.Hour)
	}
	expires = expires.UTC()
	if !expires.After(now) || expires.Before(created.Add(time.Minute)) || expires.After(created.Add(textMaximumLifetime)) {
		c.mu.Unlock()
		return nil, messageStoreError("message_invalid")
	}
	policy := policyOrDefault(c.capacity)
	if int64(len(text)) > min(policy.Number("logical", "messageBytes"), policy.Number("resources", "messageTextBytes"), int64(messageHardTextBytes)) {
		c.mu.Unlock()
		return nil, messageStoreError("message_too_large")
	}
	trust, err := c.messageQueueTrustLocked(peerID)
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	backend := c.node
	identity := c.resourceIdentity
	c.mu.Unlock()
	if backend == nil {
		return nil, messageStoreError("message_identity_unavailable")
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	state, err := backend.State(call)
	cancel()
	if err != nil || !config.ValidPeerID(state.SelfID) {
		return nil, messageStoreError("message_identity_unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.messageQueueGuardLocked(); err != nil {
		return nil, err
	}
	if err := c.checkDispatchProcessLocked(process); err != nil {
		return nil, messageStoreError("message_stale")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if c.node != backend || c.resourceIdentity != identity || !c.outboxTrustCurrentLocked(trust) {
		return nil, messageStoreError("message_permission")
	}
	// A concurrent repeat can only return the same immutable review.
	if queue.prepared[id] != nil || c.messageStore.state.Outgoing[id] != nil {
		previous, err := c.messageQueueStatusLocked(id)
		if err != nil {
			return nil, err
		}
		if previous.PeerID != peerID || previous.SHA256 != textSHA256(text) || !defaultExpiry && !previous.ExpiresAt.Equal(expires) {
			return nil, messageStoreError("message_conflict")
		}
		return previous, nil
	}
	now = c.messageQueueNowLocked()
	if !textRequestFresh(id, now) || !now.Before(expires) {
		return nil, messageStoreError("message_stale")
	}
	// Discarded reviews retain their conflict fence only for the bounded review
	// lifetime; after that their original timestamp is statelessly inadmissible.
	for key, p := range queue.prepared {
		if p.discarded && !now.Before(p.row.View.CreatedAt.Add(transferPreparationLifetime)) {
			delete(queue.prepared, key)
		}
	}
	if len(queue.prepared) >= messageStoreMaxRows {
		return nil, messageStoreError("message_evidence_capacity")
	}
	offer := DurableTextOffer{Version: DurableTextVersion, MessageID: id, RecipientID: peerID, TextBytes: int64(len(text)), TextSHA256: textSHA256(text), CreatedAt: created, ExpiresAt: expires}
	digest, err := durableTextDigest(offer)
	if err != nil {
		return nil, err
	}
	offer.Digest = digest
	row := messageOutgoing{Identity: identity, Network: trust.Network, SenderID: state.SelfID, Trust: trust, Signature: messageQueueSignature(peerID, text, expires), Offer: offer, Text: text, BodyRetained: true, Visible: true, Active: true}
	row.View = TextOutboxRecord{Version: DurableTextVersion, Kind: "text_outbox", OutboxID: id, RequestID: id, MessageID: id, PeerID: peerID, ProcessID: process, Admission: "prepared", Status: "prepared", DeliveryOutcome: "not_attempted", TextBytes: int64(len(text)), SHA256: offer.TextSHA256, CreatedAt: created, ExpiresAt: expires, UpdatedAt: now}
	row.View.ReviewRevision = resourceDigest(struct {
		Domain, Nonce, Random, Process, Identity, Sender string
		Trust                                            Trust
		Offer                                            DurableTextOffer
	}{"sobalink/text-review/v1", c.resourceNonce, randomID(), process, identity, state.SelfID, trust, offer})
	preparation := &messageQueuePreparation{row: row, backend: backend}
	queue.prepared[id] = preparation
	if err := c.messageAdmissionCapacityLocked(c.messageStore.state); err != nil {
		delete(queue.prepared, id)
		return nil, err
	}
	return messagePreparationView(preparation), nil
}
func (c *Core) commitMessageQueue(ctx context.Context, id, process, revision string, confirm bool) (any, error) {
	if !confirm || revision == "" {
		return nil, messageStoreError("message_review_required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.messageQueueGuardLocked(); err != nil {
		return nil, err
	}
	if old := c.messageStore.state.Outgoing[id]; old != nil {
		if old.View.ReviewRevision != revision {
			return nil, messageStoreError("message_conflict")
		}
		return c.messageQueueViewLocked(id)
	}
	queue := c.messageQueueRuntimeLocked()
	p := queue.prepared[id]
	if p == nil || p.discarded || p.row.View.ProcessID != process || p.row.View.ReviewRevision != revision {
		return nil, messageStoreError("message_stale")
	}
	if err := c.checkDispatchProcessLocked(process); err != nil {
		return nil, messageStoreError("message_stale")
	}
	err := c.mutateMessageStoreLocked(func(next *messageEnvelope) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		now := c.messageQueueNowLocked()
		if !now.Before(p.row.View.CreatedAt.Add(transferPreparationLifetime)) || !now.Before(p.row.View.ExpiresAt) {
			return messageStoreError("message_stale")
		}
		if c.node != p.backend || c.resourceIdentity != p.row.Identity || !c.outboxTrustCurrentLocked(p.row.Trust) {
			return messageStoreError("message_permission")
		}
		row := cloneMessageOutgoing(&p.row)
		row.View.ProcessID = ""
		row.View.Admission = "enqueued"
		row.View.Status = "queued"
		row.View.UpdatedAt = now
		next.Outgoing[id] = &row
		return nil
	})
	if err != nil {
		return nil, err
	}
	delete(queue.prepared, id)
	return c.messageQueueViewLocked(id)
}
func (c *Core) discardMessageQueue(id, process string) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.messageQueueGuardLocked(); err != nil {
		return nil, err
	}
	if c.messageStore.state.Outgoing[id] != nil {
		return nil, messageStoreError("message_already_enqueued")
	}
	p := c.messageQueueRuntimeLocked().prepared[id]
	if p == nil {
		return nil, messageStoreError("message_not_found")
	}
	if p.row.View.ProcessID != process || c.checkDispatchProcessLocked(process) != nil {
		return nil, messageStoreError("message_stale")
	}
	p.discarded = true
	p.row.Text = ""
	p.row.BodyRetained = false
	p.row.Visible = false
	p.row.Active = false
	p.row.NoMoreSend = true
	p.row.View.Status = "discarded"
	p.row.View.UpdatedAt = c.messageQueueNowLocked()
	return messagePreparationView(p), nil
}

// The process-local fence is installed before storage is attempted. A failed
// cancellation save cannot let its cancelled worker restart under the old row.
func (c *Core) persistMessageQueueCancellationLocked(id string) error {
	queue := c.messageQueueRuntimeLocked()
	if !queue.pendingCancel[id] {
		return nil
	}
	err := c.mutateMessageStoreLocked(func(next *messageEnvelope) error {
		row := next.Outgoing[id]
		if row == nil {
			return messageStoreError("message_not_found")
		}
		row.Cancelled = true
		row.NoMoreSend = true
		if row.View.DeliveryOutcome != "confirmed" {
			row.View.Status = "cancel_requested"
			row.View.ReasonCode = "message_cancelled"
		}
		row.View.UpdatedAt = c.messageQueueNowLocked()
		return nil
	})
	if err == nil {
		delete(queue.pendingCancel, id)
	}
	return err
}
func (c *Core) cancelMessageQueue(ctx context.Context, id string) (any, error) {
	c.mu.Lock()
	if c.messageStore == nil {
		c.mu.Unlock()
		return nil, messageStoreError("message_store_recovery_required")
	}
	if c.messageStore.state.Outgoing[id] == nil {
		if candidate := c.messageStore.candidate; candidate != nil && candidate.Outgoing[id] != nil {
			// A failed admission may still be published. Preserve this exact local
			// cancel intent without promoting it; explicit status/restart recovery
			// must not permit a same-process pump to send before this fence saves.
			queue := c.messageQueueRuntimeLocked()
			queue.pendingCancel[id] = true
			if run := queue.runs[id]; run != nil {
				run.cancel()
			}
		}
		_, err := c.messageQueueStatusLocked(id)
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, messageStoreError("message_not_found")
	}
	queue := c.messageQueueRuntimeLocked()
	queue.pendingCancel[id] = true
	run := queue.runs[id]
	if run != nil {
		run.cancel()
	}
	err := c.persistMessageQueueCancellationLocked(id)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if run != nil {
		select {
		case <-run.joined:
		case <-ctx.Done():
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.messageQueueViewLocked(id)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.drainMessageQueueCompletionLocked(id); err != nil {
		return nil, err
	}
	if err := c.retireMessageQueueActiveLocked(id); err != nil {
		return nil, err
	}
	return c.messageQueueViewLocked(id)
}
