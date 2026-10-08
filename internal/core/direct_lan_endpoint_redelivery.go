package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// Retrying a lost acknowledgement republishes the exact existing proof, with
// no signing, sequence increment or deadline renewal. Destination and digest
// are explicitly reviewed against the current protected state each time.
func (c *Core) endpointRedeliveryCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	in, err := decodeDirectLANEndpointExportInput(raw)
	if err != nil || in.Operation != "" || in.Lifetime != "" || in.Expires != "" {
		return nil, directLANEndpointError(endpointmeta.ErrInvalid)
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c.op.Lock()
	b := c.endpointBackendLocked()
	if b == nil || b.Node == nil || b.currentCompletion() == nil || !b.currentCompletion().activationCurrent() {
		c.op.Unlock()
		return nil, directlan.ErrUnavailable
	}
	o := b.currentCompletion()
	o.mu.Lock()
	s := b.store
	s.mu.Lock()
	m, err := s.endpointModelLocked(time.Now(), false)
	var proof endpointmeta.Envelope
	destination := ""
	if err == nil {
		var record *endpointmeta.PeerRecord
		record, err = endpointRecord(*m, in.PeerID)
		if err == nil {
			if record.EndpointState.IssuedProof == nil {
				err = endpointmeta.ErrReview
			} else {
				proof = *record.EndpointState.IssuedProof
				destination = record.Peer.Endpoint
				if proof.Update.Operation == "set" && proof.Update.Endpoint != m.LocalPeer.Endpoint {
					err = endpointmeta.ErrReview
				} else {
					err = endpointmeta.Inspect(proof, *record.PairContext, in.PeerID, time.Now())
				}
			}
		}
	}
	if err == nil {
		var projection managedFixedEndpointProjection
		projection, err = o.projectionLocked(time.Now())
		if _, ok := projection.PairContexts[in.PeerID]; !ok {
			err = endpointmeta.ErrReview
		}
	}
	if err != nil {
		s.mu.Unlock()
		o.mu.Unlock()
		c.op.Unlock()
		return nil, directLANEndpointError(err)
	}
	digest, _ := proof.Digest()
	var issuedCutoff time.Time
	if proof.Update.Lifetime == "finite" {
		bound, ok := s.endpointIssuedDeadlines[digest]
		if !ok || bound.expired || !time.Now().Before(bound.monotonic) {
			s.mu.Unlock()
			o.mu.Unlock()
			c.op.Unlock()
			return nil, directLANEndpointError(endpointmeta.ErrExpired)
		}
		issuedCutoff = bound.monotonic
	}
	revision := privateRevision(struct {
		Purpose, Process, File, State, Peer, Destination, Digest string
		Revision                                                 uint64
	}{"endpoint-redelivery-v1", o.process, s.fileDigest, privateRevision(s.state), in.PeerID, destination, digest, s.reviewRevision})
	if name == "direct-lan.endpoint.delivery.preview" {
		s.mu.Unlock()
		o.mu.Unlock()
		c.op.Unlock()
		return map[string]any{"revision": revision, "peerId": in.PeerID, "destination": destination, "proofDigest": digest, "endpoint": proof.Update.Endpoint, "operation": proof.Update.Operation, "sequence": proof.Update.Sequence, "lifetime": proof.Update.Lifetime, "expires": proof.Update.Expires}, nil
	}
	if in.ExpectedRevision == "" || in.ExpectedRevision != revision {
		s.mu.Unlock()
		o.mu.Unlock()
		c.op.Unlock()
		return nil, directLANEndpointReviewChanged()
	}
	live := &contextSaveLiveness{ctx: bounded, core: c.ctx, ordinary: o}
	// No delta, same sole successful writer and exact original owner cutoffs.
	err = s.writeContextPublicationLocked(o.process, cloneDirectLANState(s.state), live)
	if err == nil {
		_, err = o.projectionLocked(time.Now())
	}
	if err == nil {
		err = live.err()
	}
	if err == nil {
		o.receipt, o.revision = s.contextPublication, s.reviewRevision
		o.configuration = contextConfigurationDigest(s.state)
		o.epoch = directlan.NewContextEpoch()
		s.contextEpoch = o.epoch
		o.authority.Store(o.epoch)
		err = live.err()
	}
	if err != nil {
		o.invalidate()
		s.mu.Unlock()
		o.mu.Unlock()
		c.op.Unlock()
		return nil, directLANEndpointError(err)
	}
	target, err := b.Node.CaptureEndpointDelivery(in.PeerID, destination)
	if err != nil {
		s.mu.Unlock()
		o.mu.Unlock()
		c.op.Unlock()
		return nil, err
	}
	// Join ownership is Core-wide, not old-generation work. No Core operation
	// lock is reacquired after network I/O, so Close can cancel then join safely.
	done, err := c.beginWork()
	s.mu.Unlock()
	o.mu.Unlock()
	c.op.Unlock()
	if err != nil {
		return nil, err
	}
	defer done()
	run, stop := context.WithCancel(bounded)
	defer stop()
	detach := context.AfterFunc(c.ctx, stop)
	defer detach()
	if !issuedCutoff.IsZero() {
		var cancelProof context.CancelFunc
		run, cancelProof = context.WithDeadline(run, issuedCutoff)
		defer cancelProof()
	}
	reply, sendErr := b.Node.DeliverEndpointUpdate(run, target, proof)
	outcome := "unconfirmed"
	if sendErr == nil {
		outcome = reply.Outcome
	}
	return endpointDeliveryResult{PeerID: in.PeerID, Sequence: proof.Update.Sequence, Outcome: outcome}, nil
}

// At most one finite issued proof per retained peer. These are volatile first
// observation cutoffs, separate from inbound/application endpoint authority.
func (s *directLANStore) observeIssuedEndpointDeadlinesLocked(m endpointmeta.Snapshot, now time.Time) {
	next := make(map[string]directLANEndpointDeadline)
	for _, record := range m.Peers {
		if record.EndpointState == nil || record.EndpointState.IssuedProof == nil {
			continue
		}
		proof := record.EndpointState.IssuedProof
		if proof.Update.Lifetime != "finite" {
			continue
		}
		digest, _ := proof.Digest()
		absolute, _ := time.Parse(time.RFC3339Nano, proof.Update.Expires)
		bound, ok := s.endpointIssuedDeadlines[digest]
		if !ok || bound.abs != proof.Update.Expires {
			bound = directLANEndpointDeadline{abs: proof.Update.Expires, monotonic: now.Add(absolute.Sub(now.UTC()))}
		}
		bound.expired = bound.expired || !now.Before(bound.monotonic) || !now.UTC().Before(absolute)
		next[digest] = bound
	}
	s.endpointIssuedDeadlines = next
}

// Keep both the actual retained proof and a private prospective proof until the
// sole publisher decides adoption. At most two bounds per retained peer exist
// during this operation; callers prune against s.state only after the outcome.
func (s *directLANStore) preserveIssuedEndpointCandidateLocked(candidate endpointmeta.Snapshot, now time.Time) {
	retained := s.endpointIssuedDeadlines
	s.observeIssuedEndpointDeadlinesLocked(candidate, now)
	for digest, bound := range retained {
		if _, ok := s.endpointIssuedDeadlines[digest]; !ok {
			s.endpointIssuedDeadlines[digest] = bound
		}
	}
}
func (s *directLANStore) pruneIssuedEndpointDeadlinesLocked(now time.Time) {
	if s.state.Metadata == nil {
		s.endpointIssuedDeadlines = nil
		return
	}
	s.observeIssuedEndpointDeadlinesLocked(*s.state.Metadata, now)
}
