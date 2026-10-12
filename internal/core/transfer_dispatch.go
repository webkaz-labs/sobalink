package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/diskspace"
	"github.com/webkaz-labs/sobalink/internal/policy"
	"github.com/webkaz-labs/sobalink/internal/transfer"
)

const transferPreparationLifetime = 15 * time.Minute

// These DTOs are local-control only. The receiver continues to see one
// ordinary offer per explicitly reviewed recipient.
type TransferDispatchReview struct {
	Version        int              `json:"version"`
	ProcessID      string           `json:"processId"`
	PreparationID  string           `json:"preparationId"`
	ReviewRevision string           `json:"reviewRevision"`
	PeerIDs        []string         `json:"peerIds"`
	Entries        []transfer.Entry `json:"entries"`
	TotalBytes     int64            `json:"totalBytes"`
	Status         string           `json:"status"`
}
type TransferDispatchChild struct {
	PeerID     string `json:"peerId"`
	TransferID string `json:"transferId"`
}
type TransferDispatchReceipt struct {
	Version       int                     `json:"version"`
	ProcessID     string                  `json:"processId"`
	PreparationID string                  `json:"preparationId"`
	DispatchID    string                  `json:"dispatchId"`
	Children      []TransferDispatchChild `json:"children"`
	Status        string                  `json:"status"`
}
type transferPreparation struct {
	life      context.Context
	batch     *outgoingBatch
	peers     []Trust
	backend   NetworkBackend
	signature string
	revision  string
	processID string
	receipt   *TransferDispatchReceipt
	expiry    *time.Timer
	discarded bool
}

func (p *transferPreparation) metadataBytes() int64 {
	n := int64(512)
	for _, peer := range p.peers {
		n += int64(256 + len(peer.ID) + len(peer.Network))
	}
	return n
}
func dispatchInvalid() error {
	return &localCommandError{"transfer_dispatch_invalid", "select unique explicit recipients, files and the current process before sending"}
}
func dispatchStale() error {
	return &localCommandError{"transfer_dispatch_stale", "the prepared send changed or expired; review the files and exact recipients again"}
}
func (c *Core) checkDispatchProcessLocked(process string) error {
	if process == "" || process != resourceCatalogProcessToken(c.resourceNonce) || c.resourceNonce == "" || c.resourceLock == nil || c.closing || c.ctx == nil || c.ctx.Err() != nil {
		return dispatchStale()
	}
	return nil
}
func (c *Core) dispatchPeersLocked(ids []string) ([]Trust, error) {
	lim := transferLimitsFor(c.capacity, "transferSpoolBytes")
	if len(ids) == 0 || len(ids) > lim.MaxBatches {
		return nil, dispatchInvalid()
	}
	seen := map[string]bool{}
	peers := make([]Trust, 0, len(ids))
	for _, id := range ids {
		if !config.ValidPeerID(id) || seen[id] {
			return nil, dispatchInvalid()
		}
		seen[id] = true
		found := false
		for _, p := range c.profile.Peers {
			if p.ID == id && p.Network == c.profile.Settings.Network && !p.Paused {
				peers = append(peers, Trust{ID: p.ID, Network: p.Network, Generation: p.Generation})
				found = true
				break
			}
		}
		if !found {
			return nil, dispatchStale()
		}
	}
	if c.node == nil {
		return nil, dispatchStale()
	}
	return peers, nil
}
func (c *Core) preparationCurrentLocked(p *transferPreparation) error {
	if err := c.checkDispatchProcessLocked(p.processID); err != nil {
		return err
	}
	if p.discarded || p.backend != c.node {
		return dispatchStale()
	}
	ids := make([]string, len(p.peers))
	for i, peer := range p.peers {
		ids[i] = peer.ID
	}
	peers, err := c.dispatchPeersLocked(ids)
	if err != nil {
		return err
	}
	for i, peer := range peers {
		if peer != p.peers[i] {
			return dispatchStale()
		}
	}
	return nil
}
func dispatchSignature(process string, ids []string, manifest transfer.Manifest) string {
	// Initial manifests use placeholder digests. Content is separately bound by
	// the final review revision, which never accepts replacement payload bytes.
	return resourceDigest(struct {
		Process  string
		Peers    []string
		Manifest transfer.Manifest
	}{process, ids, manifest})
}
func (c *Core) reservePreparation(ctx context.Context, id, process string, ids []string, manifest transfer.Manifest, lim transfer.Limits) (*transferPreparation, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !config.ValidPeerID(id) || len(id) > 128 {
		return nil, false, dispatchInvalid()
	}
	if err := c.checkDispatchProcessLocked(process); err != nil {
		return nil, false, err
	}
	sig := dispatchSignature(process, ids, manifest)
	if old := c.transferPreparations[id]; old != nil {
		if old.signature != sig {
			return nil, false, dispatchInvalid()
		}
		return old, true, nil
	}
	c.reapDispatchReceiptsLocked()
	peers, err := c.dispatchPeersLocked(ids)
	if err != nil {
		return nil, false, err
	}
	if err := transfer.ValidateManifest(manifest, lim); err != nil {
		return nil, false, err
	}
	current := transferLimitsFor(c.capacity, "transferSpoolBytes")
	if err := transfer.ValidateManifest(manifest, current); err != nil {
		return nil, false, err
	}
	lim.MaxBatches = min(lim.MaxBatches, current.MaxBatches)
	lim.MaxReservedBytes = min(lim.MaxReservedBytes, current.MaxReservedBytes)
	lim.MaxMetadataBytes = min(lim.MaxMetadataBytes, current.MaxMetadataBytes)
	p := &transferPreparation{peers: peers, backend: c.node, signature: sig, processID: process}
	if p.metadataBytes() > lim.MaxMetadataBytes {
		return nil, false, errOutgoingHistory
	}
	lim.MaxMetadataBytes -= p.metadataBytes()
	if err := c.checkOutgoingCapacityLocked(lim, manifest, 1, nil); err != nil {
		return nil, false, err
	}
	life, cancel := context.WithCancel(c.ctx)
	p.life = life
	var total int64
	for _, entry := range manifest.Entries {
		total += entry.Size
	}
	p.batch = &outgoingBatch{ID: id, Manifest: manifest, Files: map[string]string{}, State: "prepared", Created: time.Now().UTC(), staging: true, reserved: total, cancel: cancel, payload: &outgoingPayload{refs: 1, reserved: total}}
	if c.transferPreparations == nil {
		c.transferPreparations = map[string]*transferPreparation{}
	}
	c.transferPreparations[id] = p
	return p, false, nil
}
func (p *transferPreparation) reviewLocked() TransferDispatchReview {
	b := p.batch
	ids := make([]string, len(p.peers))
	for i, peer := range p.peers {
		ids[i] = peer.ID
	}
	var total int64
	for _, entry := range b.Manifest.Entries {
		total += entry.Size
	}
	return TransferDispatchReview{1, p.processID, b.ID, p.revision, ids, append([]transfer.Entry{}, b.Manifest.Entries...), total, "prepared"}
}
func (c *Core) preparationResult(process, id string) (any, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if err := c.checkDispatchProcessLocked(process); err != nil {
		return nil, err
	}
	p := c.transferPreparations[id]
	if p == nil || p.discarded {
		return nil, dispatchStale()
	}
	if p.receipt != nil {
		receipt := *p.receipt
		receipt.Children = append([]TransferDispatchChild{}, receipt.Children...)
		return receipt, nil
	}
	p.batch.mu.Lock()
	defer p.batch.mu.Unlock()
	if p.batch.staging {
		return nil, &localCommandError{"transfer_dispatch_preparing", "the selected files are still being prepared; check this preparation again"}
	}
	if p.revision == "" {
		return nil, dispatchStale()
	}
	return p.reviewLocked(), nil
}
func (c *Core) finishPreparation(ctx context.Context, p *transferPreparation) (any, error) {
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if err := c.preparationCurrentLocked(p); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	p.batch.mu.Lock()
	p.batch.staging = false
	p.revision = resourceDigest(struct {
		Domain, Nonce, Salt string
		Review              TransferDispatchReview
		Peers               []Trust
	}{"transfer-dispatch-v1", c.resourceNonce, randomID(), p.reviewLocked(), p.peers})
	result := p.reviewLocked()
	p.batch.mu.Unlock()
	p.expiry = time.AfterFunc(transferPreparationLifetime, func() { _ = c.discardPreparationOwner(p.processID, p.batch.ID, false, p) })
	c.mu.Unlock()
	return result, nil
}

// discard is idempotent and never removes committed children. A staging
// writer is cancelled first; only its joined deferred cleanup frees payload.
func (c *Core) discardPreparation(process, id string, stagingJoined bool) error {
	return c.discardPreparationOwner(process, id, stagingJoined, nil)
}
func (c *Core) discardPreparationOwner(process, id string, stagingJoined bool, expected *transferPreparation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.transferPreparations[id]
	if p == nil || (expected != nil && p != expected) {
		return nil
	}
	if p.processID != process {
		return dispatchStale()
	}
	if p.receipt != nil {
		return &localCommandError{"transfer_dispatch_committed", "this send was already committed; use its individual transfer actions"}
	}
	p.discarded = true
	if p.expiry != nil {
		p.expiry.Stop()
	}
	b := p.batch
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		b.cancel()
	}
	if b.staging && !stagingJoined {
		return nil
	}
	b.staging = false
	if err := b.releaseSpoolLocked(); err != nil {
		return err
	}
	delete(c.transferPreparations, id)
	return nil
}
func (c *Core) prepareDispatchPaths(ctx context.Context, id, process string, ids, paths []string) (result any, resultErr error) {
	defer func() { resultErr = diskspace.NormalizeError(resultErr) }()
	done, err := c.beginWork()
	if err != nil {
		return nil, err
	}
	defer done()
	// Before enumeration, reject unknown recipients without touching payloads.
	c.mu.RLock()
	err = c.checkDispatchProcessLocked(process)
	if err == nil {
		_, err = c.dispatchPeersLocked(ids)
	}
	c.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	stage, finish := c.operationContext(ctx, "stagingSeconds")
	defer finish()
	stop := context.AfterFunc(c.ctx, finish)
	defer stop()
	lim := c.transferLimits()
	plan, err := c.planTransferSources(stage, id, paths, lim)
	if err != nil {
		return nil, err
	}
	p, existing, err := c.reservePreparation(stage, id, process, ids, plan.Manifest(), lim)
	if err != nil {
		return nil, err
	}
	if existing {
		return c.preparationResult(process, id)
	}
	stopLifetime := context.AfterFunc(p.life, finish)
	defer stopLifetime()
	complete := false
	defer func() {
		if !complete {
			_ = c.discardPreparationOwner(process, id, true, p)
		}
	}()
	if err = c.stageOutgoingPlan(ctx, stage, p.batch, plan); err != nil {
		return nil, err
	}
	result, err = c.finishPreparation(stage, p)
	if err != nil {
		return nil, err
	}
	complete = true
	return result, nil
}
func (c *Core) commitDispatch(ctx context.Context, process, id, revision string, confirm bool) (any, error) {
	done, workErr := c.beginWork()
	if workErr != nil {
		return nil, workErr
	}
	defer done()
	if !confirm || revision == "" {
		return nil, dispatchInvalid()
	}
	c.mu.Lock()
	p := c.transferPreparations[id]
	if p == nil || p.processID != process || p.revision != revision {
		c.mu.Unlock()
		return nil, dispatchStale()
	}
	// Lost-response recovery is an observation, not a fresh trust admission.
	if p.receipt != nil {
		result := *p.receipt
		result.Children = append([]TransferDispatchChild{}, result.Children...)
		c.mu.Unlock()
		return result, nil
	}
	c.mu.Unlock()
	if err := c.dispatchOnline(ctx, p); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.transferPreparations[id] != p {
		c.mu.Unlock()
		return nil, dispatchStale()
	}
	if p.receipt != nil {
		result := *p.receipt
		result.Children = append([]TransferDispatchChild{}, result.Children...)
		c.mu.Unlock()
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if err := c.preparationCurrentLocked(p); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	lim := transferLimitsFor(c.capacity, "transferSpoolBytes")
	b := p.batch
	b.mu.Lock()
	if b.staging || b.payload == nil {
		b.mu.Unlock()
		c.mu.Unlock()
		return nil, dispatchStale()
	}
	manifest := b.Manifest
	manifest.ID = "00000000000000000000000000000000"
	b.mu.Unlock()
	if err := transfer.ValidateManifest(manifest, lim); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	// The receipt remains separately charged after commit.
	if lim.MaxBatches < 2 || p.metadataBytes() > lim.MaxMetadataBytes {
		c.mu.Unlock()
		return nil, errOutgoingHistory
	}
	lim.MaxBatches--
	lim.MaxMetadataBytes -= p.metadataBytes()
	if err := c.checkOutgoingCapacityLocked(lim, manifest, len(p.peers), p); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	receipt := TransferDispatchReceipt{Version: 1, ProcessID: process, PreparationID: id, DispatchID: id, Status: "committed", Children: make([]TransferDispatchChild, 0, len(p.peers))}
	batches := make([]*outgoingBatch, 0, len(p.peers))
	lives := make([]context.Context, 0, len(p.peers))
	b.mu.Lock()
	for _, peer := range p.peers {
		childID := randomID()
		life, cancel := context.WithCancel(c.ctx)
		child := &outgoingBatch{ID: childID, PeerID: peer.ID, Network: peer.Network, Generation: peer.Generation, Manifest: transfer.Manifest{ID: childID, Entries: append([]transfer.Entry{}, manifest.Entries...)}, Files: b.Files, Spool: b.Spool, State: "queued", Created: time.Now().UTC(), cancel: cancel, reserved: b.reserved, payload: b.payload, backend: p.backend, dispatchID: id}
		batches = append(batches, child)
		lives = append(lives, life)
		receipt.Children = append(receipt.Children, TransferDispatchChild{peer.ID, childID})
	}
	b.payload.mu.Lock()
	b.payload.refs = len(batches)
	b.payload.mu.Unlock()
	b.payload = nil
	b.reserved = 0
	b.Spool = ""
	b.Files = nil
	b.Manifest = transfer.Manifest{}
	b.mu.Unlock()
	p.receipt = &receipt
	if b.cancel != nil {
		b.cancel()
	}
	if p.expiry != nil {
		p.expiry.Stop()
	}
	for _, child := range batches {
		c.outgoing[child.ID] = child
	}
	c.mu.Unlock()
	for i, child := range batches {
		if err := c.runOutgoing(lives[i], child); err != nil {
			child.mu.Lock()
			child.State = "failed"
			child.Error = err.Error()
			if lives[i].Err() != nil {
				child.State = "cancelled"
				_ = child.releaseSpoolLocked()
			}
			child.mu.Unlock()
		}
	}
	result := receipt
	result.Children = append([]TransferDispatchChild{}, receipt.Children...)
	return result, nil
}
func (c *Core) transferDispatchCommand(ctx context.Context, name, id string, raw json.RawMessage) (any, error) {
	switch name {
	case "transfer.dispatch.prepare":
		var v struct {
			ProcessID string   `json:"processId"`
			PeerIDs   []string `json:"peerIds"`
			Paths     []string `json:"paths"`
		}
		if err := decodePayload(raw, &v); err != nil {
			return nil, err
		}
		return c.prepareDispatchPaths(ctx, id, v.ProcessID, v.PeerIDs, v.Paths)
	case "transfer.dispatch.commit":
		var v struct {
			ProcessID      string `json:"processId"`
			PreparationID  string `json:"preparationId"`
			ReviewRevision string `json:"reviewRevision"`
			Confirm        bool   `json:"confirm"`
		}
		if err := decodePayload(raw, &v); err != nil {
			return nil, err
		}
		return c.commitDispatch(ctx, v.ProcessID, v.PreparationID, v.ReviewRevision, v.Confirm)
	case "transfer.dispatch.status", "transfer.dispatch.discard":
		var v struct {
			ProcessID     string `json:"processId"`
			PreparationID string `json:"preparationId"`
		}
		if err := decodePayload(raw, &v); err != nil {
			return nil, err
		}
		if name == "transfer.dispatch.status" {
			return c.preparationResult(v.ProcessID, v.PreparationID)
		}
		c.mu.RLock()
		err := c.checkDispatchProcessLocked(v.ProcessID)
		c.mu.RUnlock()
		if err != nil {
			return nil, err
		}
		if err := c.discardPreparation(v.ProcessID, v.PreparationID, false); err != nil {
			return nil, err
		}
		return map[string]any{"version": 1, "processId": v.ProcessID, "preparationId": v.PreparationID, "status": "discarded"}, nil
	}
	return nil, errors.New("unsupported dispatch action")
}

// Availability is observed once before atomic commit. No pending preparation
// or queued child wakes itself when a recipient later comes online.
func (c *Core) dispatchOnline(ctx context.Context, p *transferPreparation) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	state, err := p.backend.State(ctx)
	if err != nil || !state.Snapshot.Running {
		return dispatchStale()
	}
	for _, peer := range p.peers {
		found := false
		for _, current := range state.Snapshot.Peers {
			if c.dispatchPeerAvailable(p.backend, peer, current, time.Now()) {
				found = true
				break
			}
		}
		if !found {
			return dispatchStale()
		}
	}
	return nil
}
func (c *Core) discardPreparedForPeer(peer string) error {
	c.mu.RLock()
	var selected []*transferPreparation
	for _, p := range c.transferPreparations {
		if p.receipt != nil {
			continue
		}
		for _, recipient := range p.peers {
			if peer == "" || recipient.ID == peer {
				selected = append(selected, p)
				break
			}
		}
	}
	c.mu.RUnlock()
	var err error
	for _, p := range selected {
		err = errors.Join(err, c.discardPreparationOwner(p.processID, p.batch.ID, false, p))
	}
	return err
}

// Reap only consumed receipts whose children have all been explicitly cleared.
// Their review cannot be replayed: a later preparation uses a fresh revision.
func (c *Core) reapDispatchReceiptsLocked() {
	for id, p := range c.transferPreparations {
		if p.receipt == nil {
			continue
		}
		retained := false
		for _, child := range p.receipt.Children {
			if c.outgoing[child.TransferID] != nil {
				retained = true
				break
			}
		}
		if !retained {
			delete(c.transferPreparations, id)
		}
	}
}

// Direct LAN (and some mixed routes) has no transport presence bit. Use the
// same fresh authenticated application contact that backs Snapshot's online
// projection, never a configured peer or a successful raw socket alone. This
// is a readiness observation, not authority: the exact trust/backend binding
// is rechecked here and by every outgoing HTTP request.
func (c *Core) dispatchPeerAvailable(backend NetworkBackend, selected Trust, current policy.Peer, now time.Time) bool {
	if current.ID != selected.ID || current.Expired || len(current.IPs) == 0 {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closing || c.node != backend || c.profile.Settings.Network != selected.Network {
		return false
	}
	approved := false
	for _, peer := range c.profile.Peers {
		if peer.ID == selected.ID && peer.Network == selected.Network && peer.Generation == selected.Generation && !peer.Paused {
			approved = true
			break
		}
	}
	if !approved {
		return false
	}
	if current.Online {
		return true
	}
	checked := c.confirmed[current.ID]
	observation := c.discoveryObservations[current.ID]
	if !freshDiscoveryCheck(checked, now) || observation.State != "confirmed" || observation.CheckedAt.Before(checked) {
		return false
	}
	origin := c.discoveredOrigins[current.ID]
	if _, direct := backend.(*directLANBackend); direct && origin == nil {
		return false
	}
	return origin == nil || (origin.Identity() != nil && !channelClosed(origin.StopRequested()))
}
