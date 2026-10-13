package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

func (c *Core) outboxCopy() *outboxCoordinator { c.mu.RLock(); defer c.mu.RUnlock(); return c.outbox }
func (c *Core) outboxCommand(ctx context.Context, name, id string, raw json.RawMessage) (any, error) {
	var v struct {
		ProcessID      string    `json:"processId"`
		PeerIDs        []string  `json:"peerIds"`
		Paths          []string  `json:"paths"`
		ExpiresAt      time.Time `json:"expiresAt"`
		OutboxID       string    `json:"outboxId"`
		RequestID      string    `json:"requestId"`
		ReviewRevision string    `json:"reviewRevision"`
		Confirm        bool      `json:"confirm"`
		DeliveryID     string    `json:"deliveryId"`
	}
	if err := decodePayload(raw, &v); err != nil {
		return nil, err
	}
	o := c.outboxCopy()
	if name == "outbox.list" {
		if o == nil {
			c.mu.RLock()
			blocked := c.profile.DurableFiles != nil
			reason := c.durableProfileError
			c.mu.RUnlock()
			if blocked {
				if reason == "" {
					reason = "outbox_recovery_required"
				}
				return map[string]any{"version": 1, "kind": "outbox", "status": "blocked_recovery", "reasonCode": reason, "items": []OutboxRecord{}}, nil
			}
			return map[string]any{"version": 1, "kind": "outbox", "status": "blocked_recovery", "reasonCode": "outbox_recovery_required", "items": []OutboxRecord{}}, nil
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		status, reason := "ready", o.frozen
		if reason == "" {
			reason = c.outboxGuardReason()
		}
		if reason != "" {
			status = "blocked_recovery"
		}
		return map[string]any{"version": 1, "kind": "outbox", "status": status, "reasonCode": reason, "items": o.listLocked()}, nil
	}
	if o == nil {
		return nil, outboxError("outbox_disabled")
	}
	if name == "outbox.prepare" {
		return c.prepareOutboxPaths(ctx, id, v.ProcessID, v.PeerIDs, v.Paths, v.ExpiresAt)
	}
	key := v.OutboxID
	if key == "" {
		key = v.RequestID
	}
	if key == "" || v.OutboxID != "" && v.RequestID != "" && v.OutboxID != v.RequestID {
		return nil, outboxError("outbox_invalid")
	}
	switch name {
	case "outbox.status":
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.frozen != "" {
			if _, ok := o.certified[key]; !ok {
				return nil, outboxError(o.frozen)
			}
		}

		if row := o.state.Intents[key]; row != nil {
			return o.viewLocked(row), nil
		}
		p := o.prepared[key]
		if o.frozen != "" {
			return nil, outboxError(o.frozen)
		}
		if p == nil {
			if err := o.clockLocked(); err != nil {
				return nil, err
			}
			if outboxRequestRetired(key, o.now()) {
				if err := o.withOwnedLocked(o.saveLocked); err != nil {
					return nil, err
				}
				return nil, outboxError("outbox_retired")
			}
			return nil, outboxError("outbox_not_found")
		}
		if p.staging {
			return nil, outboxError("outbox_preparing")
		}
		return cloneOutboxRecord(p.intent.View), nil
	case "outbox.commit":
		return c.commitOutbox(ctx, key, v.ProcessID, v.ReviewRevision, v.Confirm)
	case "outbox.discard":
		return c.discardOutbox(key, v.ProcessID)
	case "outbox.cancel":
		return c.cancelOutbox(ctx, key, v.DeliveryID)
	}
	return nil, outboxError("outbox_invalid")
}
func (c *Core) outboxPeers(ids []string) ([]Trust, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(ids) == 0 || len(ids) > outboxMaxChildren {
		return nil, outboxError("outbox_invalid")
	}
	seen := map[string]bool{}
	peers := make([]Trust, 0, len(ids))
	for _, id := range ids {
		if !config.ValidPeerID(id) || seen[id] {
			return nil, outboxError("outbox_invalid")
		}
		seen[id] = true
		found := false
		for _, p := range c.profile.Peers {
			if p.ID == id && p.Network == c.profile.Settings.Network && !p.Paused && !c.managedDenied[id] {
				peers = append(peers, Trust{ID: p.ID, Network: p.Network, Generation: p.Generation, RevocationEpoch: p.RevocationEpoch})
				found = true
				break
			}
		}
		if !found {
			return nil, outboxError("outbox_permission_changed")
		}
	}
	return peers, nil
}
func (c *Core) reserveOutbox(ctx context.Context, id, process string, ids []string, manifest transfer.Manifest, expires time.Time) (*outboxPreparation, *OutboxRecord, error) {
	o := c.outboxCopy()
	if o == nil || !c.durableProfileReady() {
		return nil, nil, outboxError("outbox_disabled")
	}
	c.mu.RLock()
	err := c.checkDispatchProcessLocked(process)
	c.mu.RUnlock()
	if err != nil {
		return nil, nil, outboxError("outbox_stale")
	}
	peers, err := c.outboxPeers(ids)
	if err != nil {
		return nil, nil, err
	}
	node := c.nodeCopy()
	if node == nil {
		return nil, nil, outboxError("outbox_identity_unavailable")
	}
	identityCtx, identityCancel := context.WithTimeout(ctx, 3*time.Second)
	state, err := node.State(identityCtx)
	identityCancel()
	if err != nil || state.SelfID == "" {
		return nil, nil, outboxError("outbox_identity_unavailable")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	var prepared *outboxPreparation
	var previous *OutboxRecord
	ownedErr := o.withOwnedLocked(func() error {
		var innerErr error
		prepared, previous, innerErr = func() (*outboxPreparation, *OutboxRecord, error) {
			if err = o.checkLocked(); err != nil {
				return nil, nil, err
			}
			if err = o.clockLocked(); err != nil {
				return nil, nil, err
			}
			now := o.now().UTC()

			if expires.IsZero() {
				if old := o.state.Intents[id]; old != nil {
					expires = old.View.ExpiresAt
				} else if p := o.prepared[id]; p != nil {
					expires = p.intent.View.ExpiresAt
				} else {
					expires = now.Add(24 * time.Hour)
				}
			}
			expires = expires.UTC()
			if !config.ValidPeerID(id) || len(id) > 128 {
				return nil, nil, outboxError("outbox_invalid")
			}
			sig := resourceDigest(struct {
				Peers    []string
				Manifest transfer.Manifest
				Expiry   time.Time
			}{ids, manifest, expires})
			if old := o.state.Intents[id]; old != nil {
				if old.Signature != sig {
					return nil, nil, outboxError("outbox_conflict")
				}
				v := o.viewLocked(old)
				return nil, &v, nil
			}
			if old := o.prepared[id]; old != nil {
				if old.intent.Signature != sig || old.discarded {
					return nil, nil, outboxError("outbox_conflict")
				}
				if old.staging {
					return nil, nil, outboxError("outbox_preparing")
				}
				v := cloneOutboxRecord(old.intent.View)
				return nil, &v, nil
			}
			created, validID := outboxRequestCreated(id)
			if !validID || !outboxRequestFresh(id, now) {
				if outboxRequestRetired(id, now) {
					if err := o.saveLocked(); err != nil {
						return nil, nil, err
					}
					return nil, nil, outboxError("outbox_retired")
				}
				return nil, nil, outboxError("outbox_invalid")
			}
			if expires.After(created.Add(outboxRequestMaximumLifetime)) || expires.Sub(now) < time.Minute-5*time.Second || expires.Sub(now) > 7*24*time.Hour {
				return nil, nil, outboxError("outbox_invalid")
			}
			for _, peer := range peers {
				if supported, known := o.capabilities[peer.ID]; known && !supported {
					return nil, nil, outboxError("outbox_peer_upgrade_required")
				}
			}
			if err = transfer.ValidateManifest(manifest, c.transferLimits()); err != nil {
				return nil, nil, err
			}
			encoded, _ := json.Marshal(o.state)
			estimate := int64(len(encoded)) + outboxPreparationMetadata(manifest, len(peers))
			for _, prep := range o.prepared {
				estimate += outboxPreparationMetadata(transfer.Manifest{Entries: prep.intent.View.Entries}, len(prep.intent.Deliveries))
			}
			for _, row := range o.state.Intents {
				estimate += int64(len(row.Deliveries)) * int64(outboxReceiptReserve+len(row.View.Entries)*512)
			}
			if estimate > outboxMaxMetadata {
				return nil, nil, outboxError("outbox_evidence_capacity")
			}

			livePayloads := 0
			for _, p := range o.state.Payloads {
				if !p.Released {
					livePayloads++
				}
			}
			if len(o.prepared) >= outboxMaxChildren || int(c.outboxChildren.Load())+len(peers) > outboxMaxChildren || livePayloads >= outboxMaxChildren || len(o.state.Payloads) >= outboxMaxChildren {
				return nil, nil, outboxError("outbox_evidence_capacity")
			}
			var size int64
			for _, e := range manifest.Entries {
				size += e.Size
			}
			c.mu.Lock()
			capacity := transferLimitsFor(c.capacity, "transferSpoolBytes")
			capacity.MaxReservedBytes -= outboxMarkerBytes
			capacity.MaxMetadataBytes -= 1024 // active row overhead, separate from retained evidence
			err = c.checkOutgoingCapacityLocked(capacity, manifest, len(peers), nil)
			if err == nil {
				for _, peer := range peers {
					_, ordinary := c.outgoingPendingLocked(peer.ID, nil)
					if ordinary+o.activePeerCountLocked(peer.ID) >= capacity.MaxPendingPerPeer {
						err = outboxError("outbox_capacity")
						break
					}
				}
			}
			if err == nil {
				c.outboxReserved.Add(size + outboxMarkerBytes)
				c.outboxChildren.Add(int64(len(peers)))
				c.outboxMetadata.Add(transfer.ManifestMetadataBytes(manifest)*int64(len(peers)) + 1024)
				c.outboxActiveChildren.Add(int64(len(peers)))
				c.outboxActiveMetadata.Add(transfer.ManifestMetadataBytes(manifest)*int64(len(peers)) + 1024)
			}
			c.mu.Unlock()
			if err != nil {
				return nil, nil, outboxError("outbox_capacity")
			}
			payload := &outboxPayload{ID: randomID(), Marker: randomID(), Bytes: size, Manifest: manifest, FileIdentities: map[string]string{}}
			row := OutboxRecord{Version: 1, Kind: "outbox", OutboxID: id, RequestID: id, ProcessID: process, Admission: "prepared", Status: "prepared", DeliveryOutcome: "not_attempted", PeerIDs: append([]string{}, ids...), Entries: append([]transfer.Entry{}, manifest.Entries...), Children: []OutboxChild{}, TotalBytes: size, CreatedAt: now, ExpiresAt: expires, UpdatedAt: now}
			intent := &outboxIntent{View: row, Identity: c.resourceIdentity, Network: peers[0].Network, SenderID: state.SelfID, Signature: sig, PayloadID: payload.ID, Deliveries: []outboxDelivery{}}
			for _, peer := range peers {
				delivery := randomID()
				intent.View.Children = append(intent.View.Children, OutboxChild{PeerID: peer.ID, DeliveryID: delivery, Status: "prepared", DeliveryOutcome: "not_attempted", Files: []OutboxFile{}})
				intent.Deliveries = append(intent.Deliveries, outboxDelivery{Trust: peer})
			}
			life, cancel := context.WithCancel(c.ctx)
			p := &outboxPreparation{intent: intent, ctx: life, cancel: cancel, staging: true}
			o.prepared[id] = p
			o.state.Payloads[payload.ID] = payload
			if err = o.createPayloadLocked(payload); err != nil {
				p.discarded = true
				cancel()
				return nil, nil, err
			}
			return p, nil, nil
		}()
		return innerErr
	})
	return prepared, previous, ownedErr
}

func (o *outboxCoordinator) finishPreparationLocked(p *outboxPreparation) error {
	if p.discarded || p.ctx.Err() != nil {
		return outboxError("outbox_stale")
	}
	if err := o.verifyPayloadLocked(o.state.Payloads[p.intent.PayloadID]); err != nil {
		return err
	}
	r := &p.intent.View
	for i := range p.intent.Deliveries {
		d := &p.intent.Deliveries[i]
		d.Offer = transfer.DurableOffer{Version: transfer.DurableVersion, DeliveryID: r.Children[i].DeliveryID, RecipientID: d.Trust.ID, Manifest: transfer.Manifest{ID: r.Children[i].DeliveryID, Entries: append([]transfer.Entry{}, r.Entries...)}, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt}
		digest, err := transfer.DurableDigest(d.Offer)
		if err != nil {
			return err
		}
		d.Offer.Digest = digest
	}
	r.ReviewRevision = resourceDigest(struct {
		Nonce  string
		Record OutboxRecord
		Peers  []outboxDelivery
	}{o.core.resourceNonce, *r, p.intent.Deliveries})
	r.UpdatedAt = o.now().UTC()
	p.staging = false
	return o.saveLocked()
}
func (c *Core) prepareOutboxPaths(ctx context.Context, id, process string, ids, paths []string, expires time.Time) (any, error) {
	done, err := c.beginWork()
	if err != nil {
		return nil, err
	}
	defer done()
	if _, err = c.outboxPeers(ids); err != nil {
		return nil, err
	}
	stage, finish := c.operationContext(ctx, "stagingSeconds")
	defer finish()
	stop := context.AfterFunc(c.ctx, finish)
	defer stop()
	plan, err := c.planTransferSources(stage, id, paths, c.transferLimits())
	if err != nil {
		return nil, err
	}
	p, old, err := c.reserveOutbox(stage, id, process, ids, plan.Manifest(), expires)
	if err != nil {
		return nil, err
	}
	if old != nil {
		return *old, nil
	}
	stop = context.AfterFunc(p.ctx, finish)
	defer stop()
	if err = c.stageOutboxPlan(stage, p, plan); err != nil {
		c.abortOutboxPreparation(p)
		return nil, err
	}
	o := c.outboxCopy()
	o.mu.Lock()
	defer o.mu.Unlock()
	return cloneOutboxRecord(p.intent.View), nil
}
func (c *Core) commitOutbox(ctx context.Context, id, process, revision string, confirm bool) (any, error) {
	c.op.Lock()
	defer c.op.Unlock()
	if !c.durableProfileReady() {
		return nil, outboxError("outbox_disabled")
	}
	if !confirm || revision == "" {
		return nil, outboxError("outbox_invalid")
	}
	done, err := c.beginWork()
	if err != nil {
		return nil, err
	}
	defer done()
	o := c.outboxCopy()
	if o == nil {
		return nil, outboxError("outbox_disabled")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return outboxOwnedCall(o, func() (any, error) {
		if o.frozen != "" {
			return nil, outboxError(o.frozen)
		}
		if old := o.state.Intents[id]; old != nil {
			if old.View.ReviewRevision != revision {
				return nil, outboxError("outbox_conflict")
			}
			return o.viewLocked(old), nil
		}
		if err := o.checkLocked(); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p := o.prepared[id]
		if p == nil {
			if err := o.clockLocked(); err != nil {
				return nil, err
			}
			if outboxRequestRetired(id, o.now()) {
				if err := o.saveLocked(); err != nil {
					return nil, err
				}
				return nil, outboxError("outbox_retired")
			}
		}
		if p == nil || p.staging || p.discarded || p.intent.View.ProcessID != process || p.intent.View.ReviewRevision != revision {
			return nil, outboxError("outbox_stale")
		}
		c.mu.RLock()
		err = c.checkDispatchProcessLocked(process)
		c.mu.RUnlock()
		if err != nil {
			return nil, outboxError("outbox_stale")
		}
		peers, err := c.outboxPeers(p.intent.View.PeerIDs)
		if err != nil {
			return nil, err
		}
		for i, peer := range peers {
			if peer != p.intent.Deliveries[i].Trust {
				return nil, outboxError("outbox_permission_changed")
			}
		}
		if !o.now().Before(p.intent.View.CreatedAt.Add(transferPreparationLifetime)) || !o.now().Before(p.intent.View.ExpiresAt) {
			return nil, outboxError("outbox_stale")
		}
		if err = o.verifyPayloadLocked(o.state.Payloads[p.intent.PayloadID]); err != nil {
			return nil, err
		}
		c.mu.RLock()
		for _, d := range p.intent.Deliveries {
			if !c.outboxTrustCurrentLocked(d.Trust) {
				c.mu.RUnlock()
				return nil, outboxError("outbox_permission_changed")
			}
		}
		c.mu.RUnlock()
		intent := p.intent
		intent.View.ProcessID = ""
		intent.View.Admission = "enqueued"
		intent.View.Status = "queued"
		intent.View.UpdatedAt = o.now().UTC()
		for i := range intent.View.Children {
			intent.View.Children[i].Status = "queued"
		}
		o.state.Intents[id] = intent
		delete(o.prepared, id)
		p.cancel()
		if err = o.saveLocked(); err != nil {
			return nil, err
		}
		return o.viewLocked(intent), nil
	})
}

func (c *Core) discardOutbox(id, process string) (any, error) {
	o := c.outboxCopy()
	if o == nil {
		return nil, outboxError("outbox_disabled")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return outboxOwnedCall(o, func() (any, error) {
		if o.state.Intents[id] != nil {
			return nil, outboxError("outbox_already_enqueued")
		}
		p := o.prepared[id]
		if p == nil {
			return nil, outboxError("outbox_not_found")
		}
		if p.intent.View.ProcessID != process {
			return nil, outboxError("outbox_stale")
		}
		p.discarded = true
		p.cancel()
		p.intent.View.Status = "discarded"
		p.intent.View.UpdatedAt = o.now().UTC()
		for i := range p.intent.View.Children {
			p.intent.View.Children[i].Status = "discarded"
		}
		// The preparation owner performs cleanup after its physical staging writer joins.
		if !p.staging {
			if err := o.cleanupPayloadLocked(o.state.Payloads[p.intent.PayloadID]); err != nil {
				return nil, err
			}
		}
		return cloneOutboxRecord(p.intent.View), nil
	})
}

func (c *Core) cancelOutbox(ctx context.Context, id, delivery string) (any, error) {
	o := c.outboxCopy()
	if o == nil {
		return nil, outboxError("outbox_recovery_required")
	}
	o.mu.Lock()
	var joins []<-chan struct{}
	var row *outboxIntent
	err := o.withOwnedLocked(func() error {
		if err := o.checkLocked(); err != nil {
			return err
		}
		row = o.state.Intents[id]
		if row == nil {
			return outboxError("outbox_not_found")
		}
		found := delivery == ""
		for _, d := range row.Deliveries {
			found = found || d.Offer.DeliveryID == delivery
		}
		if !found {
			return outboxError("outbox_not_found")
		}
		for i := range row.Deliveries {
			d := &row.Deliveries[i]
			if delivery != "" && d.Offer.DeliveryID != delivery {
				continue
			}
			d.Cancelled = true
			if row.View.Children[i].Status != "completed" {
				row.View.Children[i].Status = "cancel_requested"
			}
		}
		outboxSummarize(row, o.now())
		if err := o.saveLocked(); err != nil {
			return err
		}
		for i, d := range row.Deliveries {
			if !d.Cancelled {
				continue
			}
			if b := o.runs[d.Offer.DeliveryID]; b != nil {
				b.mu.Lock()
				if b.cancel != nil {
					b.cancel()
				}
				if b.runDone != nil {
					joins = append(joins, b.runDone)
				}
				b.mu.Unlock()
			} else if row.View.Children[i].Status != "completed" {
				row.View.Children[i].Status = "cancelled"
			}
		}
		outboxSummarize(row, o.now())
		return o.saveLocked()
	})
	o.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, join := range joins {
		select {
		case <-join:
		case <-ctx.Done():
			o.mu.Lock()
			result := o.viewLocked(row)
			o.mu.Unlock()
			return result, nil
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err = o.cleanupPayloadLocked(o.state.Payloads[row.PayloadID]); err != nil {
		return nil, err
	}
	return o.viewLocked(row), nil
}

func outboxSummarize(v *outboxIntent, now time.Time) {
	confirmed, partial, unknown := 0, false, false
	status := "completed"
	reason := ""
	priority := map[string]int{"completed": 0, "cancelled": 1, "expired": 2, "queued": 3, "waiting_for_peer": 4, "paused": 5, "sending": 6, "awaiting_receipt": 7, "blocked_permission": 8, "blocked_recovery": 9, "cancel_requested": 10}
	for _, child := range v.View.Children {
		if child.DeliveryOutcome == "confirmed" {
			confirmed++
		}
		partial = partial || child.DeliveryOutcome == "partial"
		unknown = unknown || child.DeliveryOutcome == "unknown"
		if priority[child.Status] > priority[status] {
			status = child.Status
			reason = child.ReasonCode
		}
	}
	outcome := "not_attempted"
	if confirmed == len(v.View.Children) {
		outcome = "confirmed"
	} else if confirmed > 0 || partial {
		outcome = "partial"
	} else if unknown {
		outcome = "unknown"
	}
	v.View.Status = status
	v.View.ReasonCode = reason
	v.View.DeliveryOutcome = outcome
	v.View.UpdatedAt = now.UTC()
}
func outboxLeaseMatches(offer transfer.DurableOffer, lease transfer.DurableLease) bool {
	return lease.Version == 1 && lease.DeliveryID == offer.DeliveryID && lease.Digest == offer.Digest && lease.RecipientID == offer.RecipientID && lease.ExpiresAt.Equal(offer.ExpiresAt) && lease.ReceiptUntil.Equal(offer.ExpiresAt.Add(transfer.DurableReceiptGrace)) && outboxOwnedID(lease.ReceiverEpoch) && outboxOwnedID(lease.Token)
}

// Core.op serializes reviewed enqueue with trust/profile mutations; Core.mu
// protects the short validation, and is never held across lifecycle acquisition.
func (c *Core) outboxTrustCurrentLocked(expected Trust) bool {
	if c.profile.Settings.Network != expected.Network || c.startup.Revocations[expected.ID] != expected.RevocationEpoch || c.managedDenied[expected.ID] {
		return false
	}
	for _, peer := range c.profile.Peers {
		if peer.ID == expected.ID && peer.Network == expected.Network && peer.Generation == expected.Generation && peer.RevocationEpoch == expected.RevocationEpoch && !peer.Paused {
			return true
		}
	}
	return false
}

// A healthy sender snapshot alone cannot grant readiness while another store or
// the final profile barrier is unavailable. Keep this reason consistent in the
// local list and per-peer queue eligibility projections.
func (c *Core) outboxGuardReason() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.durableProfileError != "" {
		return c.durableProfileError
	}
	if c.profile.DurableFiles == nil || c.profile.DurableFiles.Phase != "ready" {
		return "outbox_recovery_required"
	}
	return ""
}
