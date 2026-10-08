package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

type endpointMoveDelivery struct {
	PeerID   string `json:"peerId"`
	Lifetime string `json:"lifetime"`
	Expires  string `json:"expires,omitempty"`
}
type endpointDeliveryResult struct {
	PeerID   string `json:"peerId"`
	Sequence string `json:"sequence"`
	Outcome  string `json:"outcome"`
}
type endpointMoveInput struct {
	Endpoint         string                 `json:"endpoint"`
	Deliveries       []endpointMoveDelivery `json:"deliveries"`
	ExpectedRevision string                 `json:"expectedRevision,omitempty"`
}
type endpointMoveReview struct {
	Revision         string                 `json:"revision"`
	PreviousEndpoint string                 `json:"previousEndpoint"`
	Endpoint         string                 `json:"endpoint"`
	Destinations     map[string]string      `json:"destinations"`
	Deliveries       []endpointMoveDelivery `json:"deliveries"`
}

func decodeEndpointMove(raw json.RawMessage) (endpointMoveInput, error) {
	var input endpointMoveInput
	if len(raw) > 4*endpointmeta.MaxFrameBytes {
		return input, endpointmeta.ErrCapacity
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		return input, endpointmeta.ErrInvalid
	}
	return input, nil
}

// Pure preview of the existing local-endpoint reducer. Destinations are current
// approved peers, never caller-supplied addresses; delivery permission is exact.
func previewEndpointMove(m endpointmeta.Snapshot, in endpointMoveInput, now time.Time, budget int) (endpointMoveReview, endpointmeta.Mutation, error) {
	mutation := endpointmeta.Mutation{Kind: "local-endpoint", LocalEndpoint: in.Endpoint}
	review := endpointMoveReview{PreviousEndpoint: m.LocalPeer.Endpoint, Endpoint: in.Endpoint, Deliveries: append([]endpointMoveDelivery(nil), in.Deliveries...), Destinations: make(map[string]string)}
	digest, err := endpointmeta.PreviewMutation(m, mutation)
	if err != nil {
		return review, mutation, err
	}
	fence, err := endpointmeta.Fence(m, mutation, digest, base64.RawURLEncoding.EncodeToString(make([]byte, 32)), now, budget)
	if err != nil {
		return review, mutation, err
	}
	next, err := endpointmeta.FinishPending(fence, false, now, budget)
	if err != nil {
		return review, mutation, err
	}
	if len(in.Deliveries) > len(m.Peers) {
		return review, mutation, endpointmeta.ErrCapacity
	}
	seen := map[string]bool{}
	for _, delivery := range in.Deliveries {
		if seen[delivery.PeerID] {
			return review, mutation, endpointmeta.ErrInvalid
		}
		seen[delivery.PeerID] = true
		record, err := endpointRecord(next, delivery.PeerID)
		if err != nil {
			return review, mutation, err
		}
		if record.EndpointState.ReceiveStatus != "initial" && !endpointmeta.SavedEligibility(next, record.EndpointState.PairBinding, now) {
			return review, mutation, endpointmeta.ErrReview
		}
		if _, err := endpointmeta.PrepareExport(next, delivery.PeerID, endpointmeta.ExportOptions{Operation: "set", Lifetime: delivery.Lifetime, Expires: delivery.Expires}, now, budget); err != nil {
			return review, mutation, err
		}
		review.Destinations[delivery.PeerID] = record.Peer.Endpoint
	}
	return review, mutation, nil
}

// Public command dispatcher supplies no operation lock. Full local choice is
// reviewed before the shared coordinator disrupts the old generation.
func (c *Core) localEndpointMoveCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	in, err := decodeEndpointMove(raw)
	if err != nil {
		return nil, directLANEndpointError(err)
	}
	c.op.Lock()
	b := c.endpointBackendLocked()
	if b == nil || b.Node == nil || b.currentCompletion() == nil || !b.currentCompletion().activationCurrent() {
		c.op.Unlock()
		return nil, directlan.ErrUnavailable
	}
	o := b.currentCompletion()
	s := b.store
	s.mu.Lock()
	m, err := s.endpointModelLocked(time.Now(), false)
	var review endpointMoveReview
	var mutation endpointmeta.Mutation
	if err == nil {
		var budget int
		budget, err = s.endpointBudgetLocked()
		if err == nil {
			review, mutation, err = previewEndpointMove(*m, in, time.Now(), budget)
		}
	}
	if err != nil {
		s.mu.Unlock()
		c.op.Unlock()
		return nil, directLANEndpointError(err)
	}
	expected := in.ExpectedRevision
	in.ExpectedRevision = ""
	review.Revision = privateRevision(struct {
		Purpose, Process, File, State string
		Revision                      uint64
		Input                         endpointMoveInput
		Review                        endpointMoveReview
	}{"local-endpoint-move-v1", o.process, s.fileDigest, privateRevision(s.state), s.reviewRevision, in, review})
	if name == "direct-lan.endpoint.move.preview" {
		s.mu.Unlock()
		c.op.Unlock()
		return review, nil
	}
	if expected == "" || expected != review.Revision {
		s.mu.Unlock()
		c.op.Unlock()
		return nil, directLANEndpointReviewChanged()
	}
	origin, err := b.Node.CaptureTransportOrigin()
	if err != nil {
		s.mu.Unlock()
		c.op.Unlock()
		return nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	job, err := c.startEndpointFollowingLocked(bounded, b, &endpointNodeOwner{node: b.Node, origin: origin}, mutation, s.contextPublication, privateRevision(s.state), nil, in.Deliveries...)
	s.mu.Unlock()
	c.op.Unlock()
	if err != nil {
		return nil, directLANEndpointError(err)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-job.done:
	}
	return map[string]any{"saved": job.saved, "active": job.active, "deliveries": job.deliveries, "endpointUpdatesEnabled": true}, directLANEndpointError(job.err)
}

// After fence/join/finish, extend the SAME retained transaction with existing
// signed-export/high-water deltas. No proof leaves Core before each sole-writer
// publication succeeds. A failure retains saved-but-unavailable state.
func (c *Core) issueEndpointMoveProofs(ctx context.Context, t *EndpointTransaction, deliveries []endpointMoveDelivery, planned []endpointmeta.Envelope) ([]endpointmeta.Envelope, error) {
	c.op.Lock()
	defer c.op.Unlock()
	if !c.endpointTransactionOwnerCurrentLocked(ctx, t) {
		return nil, endpointmeta.ErrReview
	}
	s := t.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(planned) != len(deliveries) {
		return nil, endpointmeta.ErrReview
	}
	proofs := make([]endpointmeta.Envelope, 0, len(deliveries))
	for i, delivery := range deliveries {
		now := time.Now()
		err := c.endpointTransactionCurrentLocked(t, now)
		if err != nil {
			t.failure = err
			return nil, err
		}
		budget, err := s.endpointBudgetLocked()
		if err != nil {
			t.failure = err
			return nil, err
		}
		m := *s.state.Metadata
		proof := planned[i]
		body := proof.Update
		record, err := endpointRecord(m, delivery.PeerID)
		if err == nil {
			err = endpointmeta.Inspect(proof, *record.PairContext, delivery.PeerID, now)
		}
		if err != nil {
			t.failure = err
			return nil, err
		}
		next, err := endpointmeta.ProposePreparedIssued(m, delivery.PeerID, proof, now, budget)
		if err != nil {
			t.failure = err
			return nil, err
		}
		issuedObserved := now
		if owner, ok := t.old.(*endpointNodeOwner); ok && !owner.observed.IsZero() {
			issuedObserved = owner.observed
		}
		s.preserveIssuedEndpointCandidateLocked(next, issuedObserved)
		if body.Lifetime == "finite" {
			absolute, _ := time.Parse(time.RFC3339Nano, body.Expires)
			observed := now
			if owner, ok := t.old.(*endpointNodeOwner); ok && !owner.observed.IsZero() {
				observed = owner.observed
			}
			cutoff := observed.Add(absolute.Sub(observed.UTC()))
			if t.proposalCutoff.IsZero() || cutoff.Before(t.proposalCutoff) {
				t.proposalCutoff = cutoff
			}
		}
		if err = s.preflightEndpointSnapshotLocked(next, false); err == nil {
			var state directLANState
			state, err = s.stateWithEndpointMetadataLocked(next)
			if err == nil {
				err = s.writeContextPublicationLocked(t.process, state, &contextSaveLiveness{ctx: ctx, core: c.ctx, endpoint: t})
			}
		}
		s.pruneIssuedEndpointDeadlinesLocked(time.Now())
		t.snapshotLocked()
		if err != nil {
			t.failure = err
			return nil, err
		}
		t.receipt = s.contextPublication
		if err = c.endpointTransactionCurrentLocked(t, time.Now()); err != nil {
			t.failure = err
			return nil, err
		}
		proofs = append(proofs, proof)
	}
	return proofs, nil
}

// Accepted-apply preflight runs before old.begin. It computes the complete
// cumulative batch with exact private signed proofs and full Core file budgets.
// Proofs remain private until their real successful whole-state publication.
func (s *directLANStore) preflightEndpointMoveExportsLocked(mutation endpointmeta.Mutation, deliveries []endpointMoveDelivery, observed time.Time) ([]endpointmeta.Envelope, error) {
	if mutation.Kind != "local-endpoint" || observed.IsZero() || s.state.Metadata == nil {
		return nil, endpointmeta.ErrReview
	}
	if uint64(len(deliveries)) > ^uint64(0)-2 || s.reviewRevision > ^uint64(0)-2-uint64(len(deliveries)) {
		return nil, endpointmeta.ErrCapacity
	}
	budget, err := s.endpointBudgetLocked()
	if err != nil {
		return nil, err
	}
	before := *cloneDirectLANMetadata(s.state.Metadata)
	review, err := endpointmeta.PreviewMutation(before, mutation)
	if err != nil {
		return nil, err
	}
	fence, err := endpointmeta.Fence(before, mutation, review, base64.RawURLEncoding.EncodeToString(make([]byte, 32)), observed, budget)
	if err != nil {
		return nil, err
	}
	final, err := endpointmeta.FinishPending(fence, false, observed, budget)
	if err != nil {
		return nil, err
	}
	temp := &directLANStore{state: cloneDirectLANState(s.state)}
	temp.state, err = temp.stateWithEndpointMetadataLocked(fence)
	if err != nil {
		return nil, err
	}
	temp.state, err = temp.stateWithEndpointMetadataLocked(final)
	if err != nil {
		return nil, err
	}
	proofs := make([]endpointmeta.Envelope, 0, len(deliveries))
	for _, delivery := range deliveries {
		body, err := endpointmeta.PrepareExport(*temp.state.Metadata, delivery.PeerID, endpointmeta.ExportOptions{Operation: "set", Lifetime: delivery.Lifetime, Expires: delivery.Expires}, observed, budget)
		if err != nil {
			return nil, err
		}
		proof, err := s.state.Identity.SignEndpointUpdate(body)
		if err != nil {
			return nil, err
		}
		next, err := endpointmeta.ProposeIssued(*temp.state.Metadata, delivery.PeerID, proof, observed, budget)
		if err != nil {
			return nil, err
		}
		temp.state, err = temp.stateWithEndpointMetadataLocked(next)
		if err != nil {
			return nil, err
		}
		data, err := json.MarshalIndent(temp.state, "", "  ")
		if err != nil {
			return nil, err
		}
		// Actual final ObservedAt is sampled after the join; reserve maximal UTC
		// fractional width. Every proof itself is already exact and is never reissued.
		if int64(len(data)+1+len("2000-01-01T00:00:00.999999999Z")-len(next.ObservedAt)) > s.currentCapacity().bytes {
			return nil, endpointmeta.ErrCapacity
		}
		proofs = append(proofs, proof)
	}
	return proofs, nil
}
