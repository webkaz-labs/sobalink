package core

// A completion is process-local and bounded by the existing admitted deliveries.
// The corresponding run stays in runs until its physical owners have joined and
// this disposition is durably recorded. A crash retains the original persisted
// intent/lease for reconciliation; this descriptor grants no new send authority.
type outboxCompletion struct {
	code   string
	joined bool
}

// Do not retain errors, request contexts, or arbitrary peer-supplied strings.
func outboxCompletionCode(err error) string {
	code := networkErrorCode(err)
	switch code {
	case "outbox_storage_busy", "outbox_permission_changed", "outbox_identity_changed",
		"outbox_peer_upgrade_required", "outbox_peer_offline", "outbox_capacity",
		"outbox_disabled", "outbox_expired", "outbox_recovery_required",
		"outbox_storage_uncertain", "outbox_clock_rollback", "outbox_payload_missing",
		"outbox_payload_recovery_required", "durable_unknown_lease", "durable_recovery_required",
		"durable_conflict", "outbox_receipt_mismatch", "outbox_receipt_regressed",
		"outbox_payload_corrupt", "outbox_clock_skew", "outbox_receiver_declined",
		"outbox_receiver_approval", "outbox_receipt_pending", "durable_capacity",
		"durable_clock", "durable_expired", "durable_permission", "durable_unsupported_storage", "busy":
		return code
	default:
		return ""
	}
}

// The coordinator mutex and lifecycle scope are retained by the caller.
func (o *outboxCoordinator) drainCompletionsLocked() error {
	for id := range o.completions {
		if err := o.drainCompletionLocked(id); err != nil {
			return err
		}
	}
	return nil
}

func (o *outboxCoordinator) drainCompletionLocked(id string) error {
	completion, ok := o.completions[id]
	if !ok || !completion.joined {
		return nil
	}
	if err := o.checkLocked(); err != nil {
		return err
	}
	if err := o.clockLocked(); err != nil {
		return err
	}
	b := o.runs[id]
	if b == nil {
		return outboxError("outbox_recovery_required")
	}
	row := o.state.Intents[b.outboxID]
	if row == nil {
		return outboxError("outbox_recovery_required")
	}
	index := -1
	for i, d := range row.Deliveries {
		if d.Offer.DeliveryID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return outboxError("outbox_recovery_required")
	}
	d := &row.Deliveries[index]
	child := &row.View.Children[index]
	now := o.now()
	// Current durable cancellation/expiry and exact saved evidence dominate an
	// older worker result. Recheck permission if it changed while waiting.
	switch {
	case d.Cancelled:
		child.Status = "cancelled"
	case !now.Before(row.View.ExpiresAt):
		child.Status = "expired"
		if child.DeliveryOutcome != "confirmed" {
			child.ReasonCode = "outbox_expired"
		}
	case child.DeliveryOutcome == "confirmed":
		child.Status = "completed"
	case o.core.ctx.Err() != nil:
		child.Status = "queued"
	default:
		code := completion.code
		peer, trusted := o.core.trust(d.Trust.ID)
		switch {
		case !trusted || peer.Network != d.Trust.Network || peer.Generation != d.Trust.Generation || peer.RevocationEpoch != d.Trust.RevocationEpoch || !o.core.peerEpochsValid(map[string]string{peer.ID: d.Trust.RevocationEpoch}) || !o.core.outgoingBackendCurrent(b):
			code = "outbox_permission_changed"
		case !o.core.durableProfileReady() || row.Identity != o.core.resourceIdentity:
			code = "outbox_identity_changed"
		case peer.Paused:
			code = "outbox_paused"
		}
		child.Status, child.ReasonCode = "waiting_for_peer", code
		switch code {
		case "outbox_permission_changed", "outbox_identity_changed", "outbox_peer_upgrade_required":
			child.Status = "blocked_permission"
		case "durable_unknown_lease", "durable_recovery_required", "durable_conflict", "outbox_receipt_mismatch", "outbox_receipt_regressed", "outbox_payload_corrupt", "outbox_clock_skew":
			child.Status = "blocked_recovery"
		case "outbox_paused":
			child.Status = "paused"
		case "outbox_receiver_declined":
			child.Status = "cancelled"
			d.Cancelled = true
		case "outbox_receiver_approval", "outbox_receipt_pending":
			child.Status = "awaiting_receipt"
		}
	}
	outboxSummarize(row, now)
	if err := o.saveLocked(); err != nil {
		// Uncertain publication keeps both the last-certified public projection
		// and the run fence. Neither Busy nor failed persistence forgets work.
		return err
	}
	delete(o.completions, id)
	delete(o.runs, id)
	return o.cleanupPayloadLocked(o.state.Payloads[row.PayloadID])
}
