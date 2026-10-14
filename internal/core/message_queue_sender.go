package core

import (
	"context"
	"sort"
	"strings"
	"time"
)

func (c *Core) pumpMessageQueue() {
	c.mu.Lock()
	if err := c.messageQueueGuardLocked(); err != nil {
		c.stopMessageQueueLocked()
		c.mu.Unlock()
		return
	}
	queue := c.messageQueueRuntimeLocked()
	if err := c.retireMessageQueueEvidenceLocked(); err != nil {
		c.mu.Unlock()
		return
	}
	for id := range queue.pendingCancel {
		if err := c.persistMessageQueueCancellationLocked(id); err != nil {
			c.mu.Unlock()
			return
		}
	}
	for id := range queue.completions {
		if err := c.drainMessageQueueCompletionLocked(id); err != nil {
			c.mu.Unlock()
			return
		}
	}
	now := c.messageQueueNowLocked()
	for id, p := range queue.prepared {
		if now.Before(p.row.View.CreatedAt.Add(transferPreparationLifetime)) {
			continue
		}
		// The timestamped key is now outside fresh admission, so no tombstone is
		// needed after this process-local preparation's review expires.
		delete(queue.prepared, id)
	}
	ids := make([]string, 0, len(c.messageStore.state.Outgoing))
	for id := range c.messageStore.state.Outgoing {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var planned []*messageQueueRun
	limit := finiteInt(policyOrDefault(c.capacity).Number("resources", "transferConcurrentRecipients"))
	for _, id := range ids {
		row := c.messageStore.state.Outgoing[id]
		if queue.runs[id] != nil {
			continue
		}
		if !row.NoMoreSend && !now.Before(row.View.ExpiresAt) {
			if err := c.mutateMessageStoreLocked(func(next *messageEnvelope) error {
				current := next.Outgoing[id]
				current.NoMoreSend = true
				if current.View.DeliveryOutcome != "confirmed" {
					current.View.Status = "expired"
					current.View.ReasonCode = "message_expired"
				}
				current.View.UpdatedAt = now
				return nil
			}); err != nil {
				break
			}
			row = c.messageStore.state.Outgoing[id]
		}
		if row.NoMoreSend && row.Active {
			if err := c.retireMessageQueueActiveLocked(id); err != nil {
				break
			}
			row = c.messageStore.state.Outgoing[id]
		}
		if row.View.DeliveryOutcome == "confirmed" || now.Before(queue.next[id]) || c.outgoingActive >= limit {
			continue
		}
		if row.View.Status == "needs_review" {
			continue
		}
		queryOnly := row.NoMoreSend
		if queryOnly && (row.Lease == nil || !now.Before(row.Lease.ReceiptUntil)) {
			continue
		}
		if c.node == nil {
			continue
		}
		selected := messageQueueSelection{id: id, identity: row.Identity, senderID: row.SenderID, trust: row.Trust, backend: c.node, allowSend: !queryOnly}
		if err := c.checkMessageQueueSelectionLocked(row.Trust.ID, &selected, c.node); err != nil {
			// A changed relationship permanently fences sends; a paused relationship
			// simply waits. Neither case permits a receipt inquiry under stale trust.
			paused := false
			for _, peer := range c.profile.Peers {
				if peer.ID == row.Trust.ID && peer.Network == row.Trust.Network && peer.Generation == row.Trust.Generation && peer.RevocationEpoch == row.Trust.RevocationEpoch && peer.Paused {
					paused = true
				}
			}
			code, status := "message_permission", "blocked"
			if paused {
				code, status = "message_paused", "paused"
			}
			if row.View.ReasonCode != code || row.View.Status != status {
				if err := c.mutateMessageStoreLocked(func(next *messageEnvelope) error {
					current := next.Outgoing[id]
					if !paused {
						current.NoMoreSend = true
					}
					current.View.ReasonCode = code
					current.View.Status = status
					current.View.UpdatedAt = now
					return nil
				}); err != nil {
					break
				}
			}
			continue
		}
		life, cancel := context.WithTimeout(c.ctx, 45*time.Second)
		run := &messageQueueRun{id: id, ctx: life, cancel: cancel, joined: make(chan struct{}), selection: selected}
		queue.runs[id] = run
		c.outgoingActive++
		c.wg.Add(1)
		planned = append(planned, run)
	}
	c.mu.Unlock()
	for _, run := range planned {
		go c.executeMessageQueueRun(run)
	}
}
func (c *Core) executeMessageQueueRun(run *messageQueueRun) {
	ctx, application, err := c.peerHTTPApplication(run.ctx)
	if err == nil {
		err = c.deliverMessageQueue(ctx, run)
	}
	c.finishMessageQueueRun(run, application, err)
}

func (c *Core) finishMessageQueueRun(run *messageQueueRun, application *peerHTTPApplication, err error) {
	c.mu.Lock()
	queue := c.messageQueueRuntimeLocked()
	completion := queue.completions[run.id]
	if completion == nil {
		completion = &messageQueueCompletion{}
		queue.completions[run.id] = completion
	}
	completion.code = messageQueueCompletionCode(err)
	c.mu.Unlock()
	// Decoded receipt/lease evidence was copied into the bounded completion
	// descriptor before this physical join, even when peerJSON returned an error.
	if application != nil {
		application.waitRequests()
		application.finish()
	}
	run.cancel()
	c.mu.Lock()
	completion = queue.completions[run.id]
	completion.joined = true
	close(run.joined)
	c.outgoingActive--
	_ = c.drainMessageQueueCompletionLocked(run.id)
	c.mu.Unlock()
	c.wg.Done()
	c.pumpOutgoing()
	c.pumpMessageQueue()
}
func (c *Core) messageQueueRunRow(run *messageQueueRun) (messageOutgoing, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	selected := run.selection
	selected.allowSend = false
	if err := c.checkMessageQueueSelectionLocked(selected.trust.ID, &selected, c.node); err != nil {
		return messageOutgoing{}, err
	}
	return cloneMessageOutgoing(c.messageStore.state.Outgoing[run.id]), nil
}
func (c *Core) deliverMessageQueue(ctx context.Context, run *messageQueueRun) error {
	row, err := c.messageQueueRunRow(run)
	if err != nil {
		return err
	}
	var remote DurableTextStatus
	if row.Lease == nil {
		if row.NoMoreSend {
			return messageStoreError("message_expired")
		}
		var hello struct {
			Protocol     int      `json:"protocol"`
			Product      string   `json:"product"`
			Capabilities []string `json:"capabilities"`
		}
		call, cancel := context.WithTimeout(ctx, 8*time.Second)
		err = c.messageQueueJSON(call, run, true, "GET", "/v1/hello", nil, &hello)
		cancel()
		if err != nil {
			return err
		}
		supported := false
		for _, capability := range hello.Capabilities {
			supported = supported || capability == DurableTextCapability
		}
		if hello.Protocol != 1 || hello.Product != "sobalink" || !supported {
			return messageStoreError("message_peer_upgrade_required")
		}
		// The exact original body-free offer is the only retry allowed without a
		// persisted lease. A lost offer ACK does not make text exposure possible.
		offer := row.Offer
		c.mu.Lock()
		err = c.mutateMessageStoreLocked(func(next *messageEnvelope) error {
			selected := run.selection
			selected.allowSend = true
			if err := c.checkMessageQueueSelectionLocked(selected.trust.ID, &selected, c.node); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			current := next.Outgoing[run.id]
			if current.Lease != nil {
				return messageStoreError("message_conflict")
			}
			current.Attempted = true
			current.View.Status = "waiting_peer"
			current.View.UpdatedAt = c.messageQueueNowLocked()
			offer.SentAt = c.messageQueueNowLocked()
			return nil
		})
		c.mu.Unlock()
		if err != nil {
			return err
		}
		call, cancel = context.WithTimeout(ctx, 8*time.Second)
		err = c.messageQueueJSON(call, run, true, "POST", DurableTextOfferPath, offer, &remote)
		cancel()
		if remote.Lease.DeliveryID != "" {
			if evidenceErr := c.recordMessageQueueStatus(run.id, remote, true); evidenceErr != nil {
				return evidenceErr
			}
		}
		if err != nil {
			return err
		}
		if remote.Lease.DeliveryID == "" {
			return messageStoreError("message_receipt_invalid")
		}
	} else {
		call, cancel := context.WithTimeout(ctx, 8*time.Second)
		err = c.messageQueueJSON(call, run, false, "POST", DurableTextStatusPath, *row.Lease, &remote)
		cancel()
		if remote.Lease.DeliveryID != "" {
			if evidenceErr := c.recordMessageQueueStatus(run.id, remote, false); evidenceErr != nil {
				return evidenceErr
			}
		}
		if err != nil {
			return err
		}
	}
	if remote.State == "received" && remote.Receipt != nil {
		return nil
	}
	if remote.State != "offered" {
		return messageStoreError("message_receipt_invalid")
	}
	row, err = c.messageQueueRunRow(run)
	if err != nil {
		return err
	}
	if row.NoMoreSend {
		return messageStoreError("message_receipt_pending")
	}
	if row.Lease == nil {
		return messageStoreError("message_unknown_lease")
	}
	c.mu.Lock()
	err = c.mutateMessageStoreLocked(func(next *messageEnvelope) error {
		selected := run.selection
		selected.allowSend = true
		if err := c.checkMessageQueueSelectionLocked(selected.trust.ID, &selected, c.node); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		current := next.Outgoing[run.id]
		if current.Lease == nil || !sameTextLease(*current.Lease, *row.Lease) || !current.BodyRetained {
			return messageStoreError("message_unknown_lease")
		}
		current.Attempted = true
		current.ExposurePossible = true
		if current.View.DeliveryOutcome != "confirmed" {
			current.View.DeliveryOutcome = "unknown"
			current.View.Status = "delivering"
		}
		current.View.UpdatedAt = c.messageQueueNowLocked()
		return nil
	})
	c.mu.Unlock()
	if err != nil {
		return err
	}
	call, cancel := context.WithDeadline(ctx, row.View.ExpiresAt)
	remote = DurableTextStatus{}
	err = c.messageQueueJSON(call, run, true, "POST", DurableTextDeliverPath, DurableTextDeliver{Lease: *row.Lease, Text: row.Text}, &remote)
	cancel()
	if remote.Lease.DeliveryID != "" {
		if evidenceErr := c.recordMessageQueueStatus(run.id, remote, false); evidenceErr != nil {
			return evidenceErr
		}
	}
	if err != nil {
		return err
	}
	if remote.State != "received" || remote.Receipt == nil {
		return messageStoreError("message_receipt_pending")
	}
	return nil
}

// Retain authenticated decoded evidence before trying persistence. It survives
// lifecycle-busy, unknown publication and delayed physical completion. This
// completion-only obligation cannot grant another send or a replacement lease.
func (c *Core) recordMessageQueueStatus(id string, status DurableTextStatus, allowNewLease bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.messageStore == nil {
		return messageStoreError("message_store_recovery_required")
	}
	row := c.messageStore.state.Outgoing[id]
	if row == nil {
		return messageStoreError("message_not_found")
	}
	if !textLeaseMatches(row.Offer, status.Lease) {
		return messageStoreError("message_receipt_invalid")
	}
	if row.Lease != nil && !sameTextLease(*row.Lease, status.Lease) {
		return messageStoreError("message_unknown_lease")
	}
	if row.Lease == nil && (!allowNewLease || row.ExposurePossible) {
		return messageStoreError("message_unknown_lease")
	}
	if status.State != "offered" && status.State != "received" && status.State != "expired" && status.State != "cancelled" {
		return messageStoreError("message_receipt_invalid")
	}
	if status.State == "received" && status.Receipt == nil || status.State != "received" && status.Receipt != nil {
		return messageStoreError("message_receipt_invalid")
	}
	if status.Receipt != nil {
		if err := validateTextReceipt(row.Offer, &status.Lease, status.Receipt); err != nil {
			return err
		}
		if row.Receipt != nil && *row.Receipt != *status.Receipt {
			return messageStoreError("message_receipt_invalid")
		}
	}
	// An exact receipt is correctness evidence even when the receiver's current
	// clock has drifted; clock failure prevents new exposure, never erases proof.
	clockInvalid := status.ReceiverNow.IsZero() || status.ReceiverNow.Sub(c.messageQueueNowLocked()).Abs() > textClockSkew
	if clockInvalid && status.Receipt == nil {
		return messageStoreError("message_clock")
	}
	queue := c.messageQueueRuntimeLocked()
	completion := queue.completions[id]
	if completion == nil {
		completion = &messageQueueCompletion{}
		queue.completions[id] = completion
	}
	lease := status.Lease
	if completion.lease != nil && !sameTextLease(*completion.lease, lease) {
		return messageStoreError("message_unknown_lease")
	}
	completion.lease = &lease
	if status.Receipt != nil {
		receipt := *status.Receipt
		if completion.receipt != nil && *completion.receipt != receipt {
			return messageStoreError("message_receipt_invalid")
		}
		completion.receipt = &receipt
	}
	if err := c.persistMessageQueueEvidenceLocked(id, completion); err != nil {
		return err
	}
	if status.Receipt == nil && status.State == "expired" {
		return messageStoreError("message_expired")
	}
	if status.Receipt == nil && status.State == "cancelled" {
		return messageStoreError("message_receiver_cancelled")
	}
	if status.Receipt == nil && status.ReasonCode != "" {
		return messageStoreError(messageQueueCompletionCode(messageStoreError(status.ReasonCode)))
	}
	return nil
}
func (c *Core) persistMessageQueueEvidenceLocked(id string, completion *messageQueueCompletion) error {
	if completion.lease == nil {
		return nil
	}
	return c.mutateMessageStoreLocked(func(next *messageEnvelope) error {
		row := next.Outgoing[id]
		if row == nil {
			return messageStoreError("message_not_found")
		}
		if row.Lease != nil && !sameTextLease(*row.Lease, *completion.lease) {
			return messageStoreError("message_unknown_lease")
		}
		lease := *completion.lease
		row.Lease = &lease
		row.Attempted = true
		if completion.receipt != nil {
			if err := validateTextReceipt(row.Offer, row.Lease, completion.receipt); err != nil {
				return err
			}
			receipt := *completion.receipt
			row.Receipt = &receipt
			row.View.DeliveryOutcome = "confirmed"
			row.View.Status = "completed"
			row.View.ReasonCode = ""
			row.NoMoreSend = true
		}
		row.View.UpdatedAt = c.messageQueueNowLocked()
		return nil
	})
}
func messageQueueCompletionCode(err error) string {
	if err == nil {
		return ""
	}
	code := networkErrorCode(err)
	switch code {
	case "message_cancelled", "message_receiver_cancelled", "message_conflict", "message_unknown_lease", "message_permission", "message_clock", "message_expired", "message_active_capacity", "message_peer_capacity", "message_evidence_capacity", "message_storage_busy", "message_clock_rollback", "message_storage_uncertain", "message_store_unavailable", "message_store_recovery_required", "message_store_busy", "message_store_storage_uncertain", "message_store_clock_rollback", "message_receipt_invalid", "message_receipt_pending", "message_peer_upgrade_required", "message_peer_offline", "message_identity_changed", "message_stopping", "busy":
		return code
	}
	if strings.HasPrefix(code, "message_store_") {
		return "message_store_recovery_required"
	}
	return "message_peer_offline"
}
func (c *Core) drainMessageQueueCompletionLocked(id string) error {
	if c.messageQueue == nil {
		return nil
	}
	queue := c.messageQueue
	if queue.pendingCancel[id] {
		if err := c.persistMessageQueueCancellationLocked(id); err != nil {
			return err
		}
	}
	completion := queue.completions[id]
	if completion == nil || !completion.joined {
		return nil
	}
	if queue.runs[id] == nil {
		return messageStoreError("message_store_recovery_required")
	}
	if completion.lease != nil {
		if err := c.persistMessageQueueEvidenceLocked(id, completion); err != nil {
			return err
		}
	}
	err := c.mutateMessageStoreLocked(func(next *messageEnvelope) error {
		row := next.Outgoing[id]
		if row == nil {
			return messageStoreError("message_not_found")
		}
		now := c.messageQueueNowLocked()
		switch {
		case row.View.DeliveryOutcome == "confirmed":
			row.NoMoreSend = true
			row.View.Status = "completed"
			row.View.ReasonCode = ""
		case row.Cancelled:
			row.NoMoreSend = true
			row.View.Status = "cancelled"
			row.View.ReasonCode = "message_cancelled"
		case !now.Before(row.View.ExpiresAt):
			row.NoMoreSend = true
			row.View.Status = "expired"
			row.View.ReasonCode = "message_expired"
		default:
			code := completion.code
			row.View.Status = "waiting_peer"
			row.View.ReasonCode = code
			switch code {
			case "message_permission", "message_identity_changed", "message_peer_upgrade_required":
				row.NoMoreSend = true
				row.View.Status = "blocked"
			case "message_expired", "message_receiver_cancelled":
				row.NoMoreSend = true
				if code == "message_expired" {
					row.View.Status = "expired"
				} else {
					row.View.Status = "cancelled"
				}
			case "message_unknown_lease", "message_conflict", "message_clock", "message_receipt_invalid", "message_storage_uncertain", "message_store_unavailable", "message_store_recovery_required", "message_store_storage_uncertain", "message_store_clock_rollback", "message_clock_rollback":
				row.NoMoreSend = true
				row.View.Status = "needs_review"
			}
		}
		// A prior confirmed terminal fence and this actual physical join are both
		// required to release active admission. Newly fenced rows retire separately.
		previous := c.messageStore.state.Outgoing[id]
		if previous != nil && previous.NoMoreSend {
			row.Active = false
		}
		row.View.UpdatedAt = now
		if !row.Active && row.NoMoreSend && !row.Visible {
			row.Text = ""
			row.BodyRetained = false
		}
		return nil
	})
	if err != nil {
		return err
	}
	delete(queue.completions, id)
	delete(queue.runs, id)
	queue.next[id] = c.messageQueueNowLocked().Add(5 * time.Second)
	return c.retireMessageQueueActiveLocked(id)
}
func (c *Core) retireMessageQueueActiveLocked(id string) error {
	if c.messageStore == nil {
		return messageStoreError("message_store_recovery_required")
	}
	row := c.messageStore.state.Outgoing[id]
	if row == nil || !row.NoMoreSend || !row.Active {
		return nil
	}
	if c.messageQueue != nil && c.messageQueue.runs[id] != nil {
		return nil
	}
	return c.mutateMessageStoreLocked(func(next *messageEnvelope) error {
		current := next.Outgoing[id]
		current.Active = false
		if current.Cancelled && current.View.DeliveryOutcome != "confirmed" {
			current.View.Status = "cancelled"
		}
		if !current.Visible {
			current.Text = ""
			current.BodyRetained = false
		}
		current.View.UpdatedAt = c.messageQueueNowLocked()
		return nil
	})
}
