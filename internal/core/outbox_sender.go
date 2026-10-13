package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

type outboxLeaseHeaderKey struct{}

func (c *Core) pumpOutbox() {
	o := c.outboxCopy()
	if o == nil {
		return
	}
	o.mu.Lock()
	var planned []*outgoingBatch
	var contexts []context.Context
	defer func() {
		o.mu.Unlock()
		for i, b := range planned {
			if err := c.runOutgoing(contexts[i], b); err != nil {
				o.mu.Lock()
				delete(o.runs, b.ID)
				o.mu.Unlock()
				b.cancel()
			}
		}
	}()
	_ = o.withOwnedLocked(func() error {
		func() {
			if !c.durableProfileReady() || o.checkLocked() != nil || o.clockLocked() != nil || c.ctx.Err() != nil {
				for _, b := range o.runs {
					b.mu.Lock()
					if b.cancel != nil {
						b.cancel()
					}
					b.mu.Unlock()
				}
				return
			}
			if o.drainCompletionsLocked() != nil {
				return
			}
			if o.retryPendingCleanupLocked() != nil {
				return
			}
			now := o.now().UTC()
			if o.retireLocked() != nil {
				return
			}
			for id, p := range o.prepared {
				if p.discarded && !p.staging && !now.Before(p.intent.View.CreatedAt.Add(transferPreparationLifetime)) && o.state.Payloads[p.intent.PayloadID] == nil {
					delete(o.prepared, id)
					o.updateAccountingLocked()
					continue
				}
				if !p.discarded && !now.Before(p.intent.View.CreatedAt.Add(transferPreparationLifetime)) {
					p.discarded = true
					p.cancel()
					p.intent.View.Status = "discarded"
					p.intent.View.UpdatedAt = now
					for i := range p.intent.View.Children {
						p.intent.View.Children[i].Status = "discarded"
					}
					if !p.staging {
						_ = o.cleanupPayloadLocked(o.state.Payloads[p.intent.PayloadID])
					}
				}
			}
			for _, row := range o.state.Intents {
				for i := range row.Deliveries {
					d := &row.Deliveries[i]
					child := &row.View.Children[i]
					if o.runs[d.Offer.DeliveryID] != nil || now.Before(o.next[d.Offer.DeliveryID]) || child.DeliveryOutcome == "confirmed" {
						continue
					}
					if !d.Cancelled && !now.Before(row.View.ExpiresAt) && child.Status != "cancelled" {
						child.Status = "expired"
						child.ReasonCode = "outbox_expired"
						outboxSummarize(row, now)
						if o.saveLocked() != nil {
							return
						}
					}
					queryOnly := d.Cancelled || !now.Before(row.View.ExpiresAt)
					if !queryOnly && (child.Status == "blocked_recovery" || child.Status == "blocked_permission") {
						continue
					}
					if d.Cancelled && child.Status != "cancelled" {
						child.Status = "cancelled"
						outboxSummarize(row, now)
						if o.saveLocked() != nil {
							return
						}
					}
					if queryOnly && (d.Lease == nil || !now.Before(d.Lease.ReceiptUntil)) {
						_ = o.cleanupPayloadLocked(o.state.Payloads[row.PayloadID])
						continue
					}
					peer, ok := c.trust(d.Trust.ID)
					if !ok || peer.Network != d.Trust.Network || peer.Generation != d.Trust.Generation || peer.RevocationEpoch != d.Trust.RevocationEpoch || !c.peerEpochsValid(map[string]string{peer.ID: d.Trust.RevocationEpoch}) {
						if !queryOnly {
							child.Status = "blocked_permission"
						}
						child.ReasonCode = "outbox_permission_changed"
						outboxSummarize(row, now)
						if o.saveLocked() != nil {
							return
						}
						continue
					}
					if peer.Paused {
						if !queryOnly {
							child.Status = "paused"
							child.ReasonCode = "outbox_paused"
							outboxSummarize(row, now)
							if o.saveLocked() != nil {
								return
							}
						}
						continue
					}
					backend := c.nodeCopy()
					if backend == nil {
						if !queryOnly {
							child.Status = "waiting_for_peer"
							child.ReasonCode = "outbox_peer_offline"
							outboxSummarize(row, now)
						}
						continue
					}
					life, cancel := context.WithTimeout(c.ctx, 45*time.Second)
					b := &outgoingBatch{ID: d.Offer.DeliveryID, PeerID: d.Trust.ID, Network: d.Trust.Network, Generation: d.Trust.Generation, Manifest: d.Offer.Manifest, State: "queued", Created: row.View.CreatedAt, cancel: cancel, backend: backend, outboxID: row.View.OutboxID}
					o.runs[b.ID] = b
					o.next[b.ID] = now.Add(5 * time.Second)
					planned = append(planned, b)
					contexts = append(contexts, life)
				}
			}
		}()
		return nil
	})
}

func (c *Core) outboxAuthority(row *outboxIntent, d outboxDelivery, b *outgoingBatch) error {
	peer, ok := c.trust(d.Trust.ID)
	if !ok || peer.Paused || peer.Generation != d.Trust.Generation || peer.Network != d.Trust.Network || peer.RevocationEpoch != d.Trust.RevocationEpoch || !c.peerEpochsValid(map[string]string{peer.ID: d.Trust.RevocationEpoch}) || !c.outgoingBackendCurrent(b) {
		return outboxError("outbox_permission_changed")
	}
	if !c.durableProfileReady() || row.Identity != c.resourceIdentity {
		return outboxError("outbox_identity_changed")
	}
	return nil
}
func (c *Core) deliverOutbox(ctx context.Context, b *outgoingBatch) error {
	o := c.outboxCopy()
	if o == nil || !c.durableProfileReady() {
		return outboxError("outbox_disabled")
	}
	o.mu.Lock()
	row := o.state.Intents[b.outboxID]
	index := -1
	if row != nil {
		for i, d := range row.Deliveries {
			if d.Offer.DeliveryID == b.ID {
				index = i
				break
			}
		}
	}
	if index < 0 {
		o.mu.Unlock()
		return outboxError("outbox_recovery_required")
	}
	d := row.Deliveries[index]
	queryOnly := d.Cancelled || !o.now().Before(row.View.ExpiresAt)
	err := o.checkLocked()
	if err == nil {
		err = o.clockLocked()
	}
	o.mu.Unlock()
	if err != nil {
		return err
	}
	if err = c.outboxAuthority(row, d, b); err != nil {
		return err
	}
	state, err := b.backend.State(ctx)
	if err != nil {
		return outboxError("outbox_peer_offline")
	}
	if state.SelfID == "" || state.SelfID != row.SenderID {
		return outboxError("outbox_identity_changed")
	}
	peer, err := c.currentPeer(ctx, b.PeerID)
	if err != nil || !c.dispatchPeerAvailable(b.backend, d.Trust, peer, o.now()) {
		return outboxError("outbox_peer_offline")
	}
	var remote transfer.DurableStatus
	if d.Lease == nil {
		if queryOnly {
			return outboxError("outbox_expired")
		}
		var hello struct {
			Protocol     int      `json:"protocol"`
			Product      string   `json:"product"`
			Capabilities []string `json:"capabilities"`
		}
		call, cancel := context.WithTimeout(ctx, 8*time.Second)
		err = c.peerJSON(call, b.PeerID, "GET", "/v1/hello", nil, &hello)
		cancel()
		if err != nil {
			return err
		}
		supported := hello.Protocol == 1 && hello.Product == "sobalink"
		advertised := false
		for _, cap := range hello.Capabilities {
			advertised = advertised || cap == transfer.DurableCapability
		}
		supported = supported && advertised
		o.mu.Lock()
		o.capabilities[b.PeerID] = supported
		o.mu.Unlock()
		if !supported {
			return outboxError("outbox_peer_upgrade_required")
		}
		o.mu.Lock()
		err = o.withOwnedLocked(func() error {
			if err := transfer.ValidateManifest(d.Offer.Manifest, c.transferLimits()); err != nil {
				return outboxError("outbox_capacity")
			}
			if row.Deliveries[index].Cancelled || !o.now().Before(row.View.ExpiresAt) {
				return outboxError("outbox_expired")
			}
			row.Deliveries[index].Attempted = true
			row.View.Children[index].DeliveryOutcome = "unknown"
			row.View.Children[index].Status = "awaiting_receipt"
			row.View.Children[index].ReasonCode = "outbox_receiver_approval"
			outboxSummarize(row, o.now())
			return o.saveLocked()
		})
		o.mu.Unlock()
		if err != nil {
			return err
		}
		offer := d.Offer
		offer.SentAt = o.now().UTC()
		call, cancel = context.WithTimeout(ctx, 8*time.Second)
		err = c.peerJSON(call, b.PeerID, "POST", transfer.DurableOfferPath, offer, &remote)
		cancel()
		if err != nil {
			return err
		}
		if remote.ReceiverNow.IsZero() || remote.ReceiverNow.Sub(o.now()).Abs() > transfer.DurableClockSkew {
			return outboxError("outbox_clock_skew")
		}
		if !outboxLeaseMatches(d.Offer, remote.Lease) {
			return outboxError("outbox_receipt_mismatch")
		}
		o.mu.Lock()
		lease := remote.Lease
		err = o.withOwnedLocked(func() error { row.Deliveries[index].Lease = &lease; return o.saveLocked() })
		if err == nil {
			d.Lease = &lease
		}
		o.mu.Unlock()
		if err != nil {
			return err
		}
	} else {
		call, cancel := context.WithTimeout(ctx, 8*time.Second)
		err = c.peerJSON(call, b.PeerID, "POST", transfer.DurableStatusPath, transfer.DurableRequest{Lease: *d.Lease}, &remote)
		cancel()
		if err != nil {
			return err
		}
	}
	if err = c.recordOutboxStatus(b.outboxID, index, remote); err != nil {
		return err
	}
	if remote.State == transfer.Completed {
		return nil
	}
	if remote.State == transfer.Cancelled || remote.State == transfer.Rejected {
		return outboxError("outbox_receiver_declined")
	}
	if queryOnly {
		return outboxError("outbox_receipt_pending")
	}
	if remote.State == transfer.Pending {
		return outboxError("outbox_receiver_approval")
	}
	// Every filesystem effect, including empty directories, requires a lease echo
	// whose possible exposure was durably fenced before this mutating request.
	if err = c.outboxAuthority(row, d, b); err != nil {
		return err
	}
	o.mu.Lock()
	_, err = c.admitOutboxExposureLocked(ctx, o, row, index, "", b)
	o.mu.Unlock()
	if err != nil {
		return err
	}

	startCtx, startCancel := context.WithDeadline(ctx, row.View.ExpiresAt)
	remote = transfer.DurableStatus{}
	err = c.peerJSON(startCtx, b.PeerID, "POST", transfer.DurableStartPath, transfer.DurableRequest{Lease: *d.Lease}, &remote)
	startCancel()
	if remote.Lease.DeliveryID != "" {
		if evidenceErr := c.recordOutboxStatus(b.outboxID, index, remote); evidenceErr != nil {
			return evidenceErr
		}
	}
	if err != nil {
		return err
	}
	if remote.State == transfer.Completed {
		return nil
	}
	for _, entry := range d.Offer.Manifest.Entries {
		if entry.Kind != transfer.File {
			continue
		}
		saved := false
		if remote.Receipt != nil {
			for _, ack := range remote.Receipt.Files {
				if ack.FileID == entry.ID {
					saved = true
				}
			}
		}
		if saved {
			continue
		}
		if err = c.outboxAuthority(row, d, b); err != nil {
			return err
		}
		o.mu.Lock()
		f, openErr := c.admitOutboxExposureLocked(ctx, o, row, index, entry.ID, b)
		o.mu.Unlock()
		if openErr != nil {
			return openErr
		}

		encoded, _ := json.Marshal(*d.Lease)
		call, cancel := context.WithDeadline(ctx, row.View.ExpiresAt)
		call = context.WithValue(call, outboxLeaseHeaderKey{}, base64.RawURLEncoding.EncodeToString(encoded))
		remote = transfer.DurableStatus{}
		err = c.peerRequest(call, b.PeerID, "POST", transfer.DurableFilePath+"?fileID="+entry.ID, f, "application/octet-stream", &remote)
		cancel()
		// The request owns the file and its physical reader. A save response that was
		// already published remains evidence even if the later owner join failed.
		if remote.Lease.DeliveryID != "" {
			if evidenceErr := c.recordOutboxStatus(b.outboxID, index, remote); evidenceErr != nil {
				return evidenceErr
			}
		}
		if err != nil {
			return err
		}
	}
	call, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	remote = transfer.DurableStatus{}
	err = c.peerJSON(call, b.PeerID, "POST", transfer.DurableStatusPath, transfer.DurableRequest{Lease: *d.Lease}, &remote)
	if remote.Lease.DeliveryID != "" {
		if evidenceErr := c.recordOutboxStatus(b.outboxID, index, remote); evidenceErr != nil {
			return evidenceErr
		}
	}
	if err != nil {
		return err
	}
	if remote.State != transfer.Completed {
		return outboxError("outbox_receipt_pending")
	}
	return nil
}
func (c *Core) recordOutboxStatus(id string, index int, status transfer.DurableStatus) error {
	o := c.outboxCopy()
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.withOwnedLocked(func() error {
		row := o.state.Intents[id]
		d := &row.Deliveries[index]
		child := &row.View.Children[index]
		if status.ReasonCode != "" {
			return outboxError(status.ReasonCode)
		}
		files, err := validateOutboxEvidence(d.Offer, d.Lease, status, child.Files)
		if err != nil {
			return err
		}
		receipt := status.Receipt
		if receipt == nil {
			return nil
		}
		d.Receipt = receipt
		child.Files = files
		if status.State == transfer.Completed {
			child.DeliveryOutcome = "confirmed"
			if !d.Cancelled && o.now().Before(row.View.ExpiresAt) {
				child.Status = "completed"
			}
			child.ReasonCode = ""
		} else if len(files) > 0 {
			child.DeliveryOutcome = "partial"
		}
		outboxSummarize(row, o.now())
		return o.saveLocked()
	})
}

func (c *Core) finishOutboxRun(run *outgoingRun, application *peerHTTPApplication, err error, active bool) {
	run.stop()
	b := run.batch
	o := c.outboxCopy()
	if o != nil {
		o.mu.Lock()
		// Keep the existing run as the bounded cleanup/replanning fence. No
		// lifecycle wait is allowed here: its owner may be joining this run.
		o.completions[b.ID] = outboxCompletion{code: outboxCompletionCode(err)}
		o.mu.Unlock()
	}
	terminal := func() {
		b.mu.Lock()
		if b.cancel != nil {
			b.cancel()
		}
		b.running = false
		close(b.runDone)
		b.mu.Unlock()
	}
	if application != nil {
		application.finishWithTerminal(terminal)
	} else {
		terminal()
	}
	if active {
		c.mu.Lock()
		c.outgoingActive--
		c.mu.Unlock()
	}
	if o != nil {
		o.mu.Lock()
		completion := o.completions[b.ID]
		// running=false and runDone precede application origin-lease release.
		// Only the returned physical join permits persistence to clear the fence.
		completion.joined = true
		o.completions[b.ID] = completion
		_ = o.withOwnedLocked(func() error { return o.drainCompletionLocked(b.ID) })
		o.mu.Unlock()
	}
	run.done()
	if active {
		c.pumpOutgoing()
	}
}
func (c *Core) outboxEligibility(id string) map[string]any {
	if reason := c.outboxGuardReason(); reason != "" {
		return map[string]any{"allowed": false, "reasonCode": reason}
	}
	o := c.outboxCopy()
	if o == nil {
		return map[string]any{"allowed": false, "reasonCode": "outbox_recovery_required"}
	}
	peers, err := c.outboxPeers([]string{id})
	if err != nil || len(peers) != 1 {
		return map[string]any{"allowed": false, "reasonCode": "outbox_permission_changed"}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.frozen != "" {
		return map[string]any{"allowed": false, "reasonCode": o.frozen}
	}
	if supported, known := o.capabilities[id]; known && !supported {
		return map[string]any{"allowed": false, "reasonCode": "outbox_peer_upgrade_required"}
	}
	return map[string]any{"allowed": true, "reasonCode": ""}
}

func (c *Core) admitOutboxExposureLocked(ctx context.Context, o *outboxCoordinator, row *outboxIntent, index int, fileID string, batch *outgoingBatch) (*os.File, error) {
	var file *os.File
	err := o.withOwnedLocked(func() error {
		if err := o.checkLocked(); err != nil {
			return err
		}
		if err := o.clockLocked(); err != nil {
			return err
		}
		if row.Deliveries[index].Cancelled || !o.now().Before(row.View.ExpiresAt) {
			return outboxError("outbox_expired")
		}
		payload := o.state.Payloads[row.PayloadID]
		if fileID == "" {
			if err := o.verifyPayloadLocked(payload); err != nil {
				return outboxError("outbox_payload_corrupt")
			}
		}
		if fileID != "" {
			root, err := o.payloadRootLocked(payload)
			if err != nil {
				return err
			}
			file, err = root.Open(fileID)
			root.Close()
			if err != nil {
				return err
			}
			var expected transfer.Entry
			found := false
			for _, entry := range payload.Manifest.Entries {
				if entry.ID == fileID && entry.Kind == transfer.File {
					expected = entry
					found = true
					break
				}
			}
			info, e := file.Stat()
			identity, idErr := outboxNativeIdentity(file)
			if !found || e != nil || idErr != nil || !info.Mode().IsRegular() || info.Size() != expected.Size || identity != payload.FileIdentities[fileID] {
				return outboxError("outbox_payload_corrupt")
			}
			hash := sha256.New()
			n, e := io.Copy(hash, io.LimitReader(stagingContextReader{ctx, file}, expected.Size+1))
			if e != nil || n != expected.Size || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
				return outboxError("outbox_payload_corrupt")
			}
			if _, e = file.Seek(0, io.SeekStart); e != nil {
				return e
			}
		}
		if err := transfer.ValidateManifest(row.Deliveries[index].Offer.Manifest, c.transferLimits()); err != nil {
			return outboxError("outbox_capacity")
		}
		// Revalidate after hashing the retained source. Trust, the active network,
		// and readiness can change while preparing the request-owned descriptor.
		if err := c.outboxAuthority(row, row.Deliveries[index], batch); err != nil {
			return err
		}
		if row.Deliveries[index].Cancelled || !o.now().Before(row.View.ExpiresAt) {
			return outboxError("outbox_expired")
		}
		row.Deliveries[index].Exposure = true
		row.View.Children[index].Status = "sending"
		if len(row.View.Children[index].Files) == 0 {
			row.View.Children[index].DeliveryOutcome = "unknown"
		}
		outboxSummarize(row, o.now())
		return o.saveLocked()
	})
	if err != nil && file != nil {
		file.Close()
		file = nil
	}
	return file, err
}
