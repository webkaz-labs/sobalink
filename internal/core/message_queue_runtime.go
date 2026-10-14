package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/webkaz-labs/sobalink/internal/transfer"
)

// Core.mu is the only lock for preparations, runs and completion obligations.
// A run keeps its finite dispatch slot until all physical request owners join.
// Neither a cancelled context nor a terminal view is proof of that join.
type messageQueueRuntime struct {
	prepared      map[string]*messageQueuePreparation
	runs          map[string]*messageQueueRun
	completions   map[string]*messageQueueCompletion
	next          map[string]time.Time
	pendingCancel map[string]bool
}
type messageQueuePreparation struct {
	row       messageOutgoing
	backend   NetworkBackend
	discarded bool
}
type messageQueueRun struct {
	id        string
	ctx       context.Context
	cancel    context.CancelFunc
	joined    chan struct{}
	selection messageQueueSelection
}
type messageQueueCompletion struct {
	code    string
	lease   *transfer.DurableLease
	receipt *DurableTextReceipt
	joined  bool
}

func (c *Core) messageQueueRuntimeLocked() *messageQueueRuntime {
	if c.messageQueue == nil {
		c.messageQueue = &messageQueueRuntime{prepared: map[string]*messageQueuePreparation{}, runs: map[string]*messageQueueRun{}, completions: map[string]*messageQueueCompletion{}, next: map[string]time.Time{}, pendingCancel: map[string]bool{}}
	}
	return c.messageQueue
}
func (c *Core) messageQueueNowLocked() time.Time {
	if c.messageStore != nil && c.messageStore.now != nil {
		return c.messageStore.now().UTC()
	}
	return time.Now().UTC()
}
func (c *Core) messageQueueGuardLocked() error {
	if err := c.messageStoreReadyLocked(); err != nil {
		return err
	}
	if c.messageQueueNowLocked().Before(c.messageStore.state.HighWater) {
		return messageStoreError("message_clock_rollback")
	}
	if c.closing || c.ctx == nil || c.ctx.Err() != nil {
		return messageStoreError("message_stopping")
	}
	return nil
}

// The store counts preparations in the same evidence/body admission envelope.
// Promotion excludes the preparation only once its exact ID is in the candidate.
func (c *Core) messageQueueReservationsLocked(committed map[string]*messageOutgoing) (rows int, bytes int64, active int, perPeer map[string]int) {
	perPeer = map[string]int{}
	if c.messageQueue == nil {
		return
	}
	for id, p := range c.messageQueue.prepared {
		if p.discarded || committed[id] != nil {
			continue
		}
		rows++
		active++
		perPeer[p.row.Trust.ID]++
		bytes += messageOutgoingReservation(&p.row)
	}
	return
}
func cloneMessageOutgoing(row *messageOutgoing) messageOutgoing {
	value := *row
	if row.Lease != nil {
		lease := *row.Lease
		value.Lease = &lease
	}
	if row.Receipt != nil {
		receipt := *row.Receipt
		value.Receipt = &receipt
	}
	value.View.Receipt = nil
	return value
}
func messagePreparationView(p *messageQueuePreparation) TextOutboxRecord {
	view := p.row.View
	if p.row.BodyRetained {
		view.Text = p.row.Text
	}
	return view
}
func messageQueueSignature(peer, text string, expiry time.Time) string {
	return resourceDigest(struct {
		Peer, Text string
		Expiry     time.Time
	}{peer, text, expiry.UTC()})
}

// Cancel descriptors under Core.mu; lifecycle callers must join after unlocking.
// Closing does not manufacture a durable cancellation: surviving intents remain
// eligible after restart with their original IDs, deadlines and leases.
func (c *Core) stopMessageQueueLocked() []<-chan struct{} {
	return c.stopMessageQueuePeerLocked("")
}

func (c *Core) stopMessageQueuePeerLocked(peerID string) []<-chan struct{} {
	var joins []<-chan struct{}
	if c.messageQueue == nil {
		return joins
	}
	for _, run := range c.messageQueue.runs {
		if peerID != "" && run.selection.trust.ID != peerID {
			continue
		}
		run.cancel()
		joins = append(joins, run.joined)
	}
	return joins
}

func messageQueueListMap(view TextOutboxRecord) map[string]any {
	view.Text = ""
	// Pagination uses the original canonical locator, not an invented list ID.
	encoded, _ := json.Marshal(view)
	row := map[string]any{}
	_ = json.Unmarshal(encoded, &row)
	row["id"] = view.OutboxID
	return row
}
