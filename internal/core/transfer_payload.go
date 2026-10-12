package core

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// Lock order is Core.mu, outgoingBatch.mu, outgoingPayload.mu. Each batch
// keeps its reference until its request-body owners have physically joined.
// A failed retryable child keeps its reference; a failed final removal keeps
// both the reference and reservation reachable through the releasing record.
type outgoingPayload struct {
	mu       sync.Mutex
	spool    string
	reserved int64
	refs     int
}

func (p *outgoingPayload) release() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refs > 1 {
		p.refs--
		return nil
	}
	if p.spool != "" {
		if err := os.RemoveAll(p.spool); err != nil {
			return err
		}
	}
	p.spool, p.reserved, p.refs = "", 0, 0
	return nil
}

// Called only with Core.mu held. Payload storage is counted once, while each
// retained manifest and receipt is charged independently. ignore replaces a
// preparation with its children in the same atomic admission.
func (c *Core) checkOutgoingCapacityLocked(lim transfer.Limits, manifest transfer.Manifest, count int, ignore *transferPreparation) error {
	if count < 1 || count > lim.MaxBatches {
		return errOutgoingHistory
	}
	if c.orphanSpoolError != "" {
		return errors.New("retained staging could not be inventoried; inspect private staging before sending")
	}
	history, metadata, reserved := count+int(c.outboxChildren.Load()), c.outboxMetadata.Load(), c.orphanSpoolBytes+c.outboxReserved.Load()
	addMetadata := func(n int64) bool {
		if n < 0 || n > lim.MaxMetadataBytes-metadata {
			return false
		}
		metadata += n
		return true
	}
	for i := 0; i < count; i++ {
		if !addMetadata(transfer.ManifestMetadataBytes(manifest)) {
			return errOutgoingHistory
		}
	}
	seen := map[*outgoingPayload]bool{}
	addBatch := func(b *outgoingBatch) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		if !addMetadata(transfer.ManifestMetadataBytes(b.Manifest)) {
			return errOutgoingHistory
		}
		n := b.reserved
		if b.payload != nil {
			if seen[b.payload] {
				return nil
			}
			seen[b.payload] = true
			b.payload.mu.Lock()
			n = b.payload.reserved
			b.payload.mu.Unlock()
		}
		if n > lim.MaxReservedBytes-reserved {
			return errOutgoingStaging
		}
		reserved += n
		return nil
	}
	for _, b := range c.outgoing {
		history++
		if err := addBatch(b); err != nil {
			return err
		}
	}
	for _, p := range c.transferPreparations {
		// Completed dispatch receipts are bounded independently by this same
		// history budget and remain available after child history is cleared.
		if p == ignore {
			continue
		}
		history++
		if !addMetadata(p.metadataBytes()) {
			return errOutgoingHistory
		}
		if p.receipt == nil {
			if err := addBatch(p.batch); err != nil {
				return err
			}
		}
	}
	if history > lim.MaxBatches {
		return errOutgoingHistory
	}
	var total int64
	for _, entry := range manifest.Entries {
		if entry.Size < 0 || entry.Size > lim.MaxReservedBytes-total {
			return errOutgoingStaging
		}
		total += entry.Size
	}
	if reserved > lim.MaxReservedBytes-total {
		return errOutgoingStaging
	}
	return nil
}

func (c *Core) outgoingBackendCurrent(b *outgoingBatch) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.closing && (b.backend == nil || c.node == b.backend) && c.profile.Settings.Network == b.Network
}

// The queue retains bounded descriptors, not one blocked goroutine per child.
// Every initial send and retry shares this process-wide dispatch budget.
type outgoingRun struct {
	ctx   context.Context
	batch *outgoingBatch
	done  func()
	stop  func() bool // initialized under Core.mu before the queue is visible
}

func (c *Core) enqueueOutgoing(run *outgoingRun) {
	c.mu.Lock()
	run.stop = context.AfterFunc(run.ctx, c.pumpOutgoing)
	c.outgoingQueue = append(c.outgoingQueue, run)
	c.mu.Unlock()
	c.pumpOutgoing()
}
func (c *Core) pumpOutgoing() {
	c.mu.Lock()
	limit := finiteInt(policyOrDefault(c.capacity).Number("resources", "transferConcurrentRecipients"))
	var start, cancelled []*outgoingRun
	pending := c.outgoingQueue[:0]
	for _, run := range c.outgoingQueue {
		if run.ctx.Err() != nil {
			cancelled = append(cancelled, run)
		} else if c.outgoingActive < limit {
			c.outgoingActive++
			start = append(start, run)
		} else {
			pending = append(pending, run)
		}
	}
	clear(c.outgoingQueue[len(pending):])
	c.outgoingQueue = pending
	c.mu.Unlock()
	for _, run := range cancelled {
		c.finishOutgoingRun(run, nil, run.ctx.Err(), false)
	}
	for _, run := range start {
		go c.executeOutgoingRun(run)
	}
}
func (c *Core) executeOutgoingRun(run *outgoingRun) {
	ctx, application, err := c.peerHTTPApplication(context.WithValue(run.ctx, outgoingSelectionKey{}, run.batch))
	if err == nil {
		run.batch.mu.Lock()
		run.batch.State = "awaiting-acceptance"
		run.batch.mu.Unlock()
		if run.batch.outboxID != "" {
			err = c.deliverOutbox(ctx, run.batch)
		} else {
			err = c.deliver(ctx, run.batch)
		}
		// This joins physical source readers, not merely their request contexts.
		application.waitRequests()
	}
	c.finishOutgoingRun(run, application, err, true)
}
func (c *Core) finishOutgoingRun(run *outgoingRun, application *peerHTTPApplication, err error, active bool) {
	if run.batch.outboxID != "" {
		c.finishOutboxRun(run, application, err, active)
		return
	}
	run.stop()
	b := run.batch
	b.mu.Lock()
	if err == nil {
		b.State = "completed"
	} else if run.ctx.Err() != nil {
		b.State = "cancelled"
		b.Error = "Transfer cancelled. Already saved files remain on the receiver."
	} else if errors.Is(err, errReceiverDeclined) {
		b.State = "declined"
		b.Error = err.Error()
	} else {
		b.State = "failed"
		b.Error = err.Error()
		if code := networkErrorCode(err); code == "peer_disk_space_low" || code == "peer_disk_space_unknown" || code == "peer_storage_unavailable" {
			b.Error = code
		}
	}
	if b.State == "completed" || b.State == "cancelled" || b.State == "declined" {
		if err := b.releaseSpoolLocked(); err != nil {
			b.Error = "could not remove staged files; clear this transfer to retry cleanup"
		}
	}
	if b.cancel != nil {
		b.cancel()
	}
	b.mu.Unlock()
	terminal := func() { b.mu.Lock(); b.running = false; close(b.runDone); b.mu.Unlock() }
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
	run.done()
	if active {
		c.pumpOutgoing()
	}
}

type outgoingSelectionKey struct{}
