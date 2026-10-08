package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// One job owns both manual reviewed input and authenticated follow observations.
// It is joined by Close BEFORE Core.op, never by its retired generation.
type endpointFollowingJob struct {
	deliveries    []endpointDeliveryResult
	cancel        context.CancelFunc
	done          chan struct{}
	backend       *directLANBackend
	transaction   *EndpointTransaction // op
	saved, active bool                 // published by closing done
	err           error
}

func (c *Core) endpointBackendLocked() *directLANBackend {
	c.mu.RLock()
	defer c.mu.RUnlock()
	switch root := c.node.(type) {
	case *directLANBackend:
		return root
	case *mixedBackend:
		b, _ := root.nodes["direct-lan"].(*directLANBackend)
		return b
	}
	return nil
}

// Core.op held. The short preflight authenticates the observation and copies a
// reducer proposal. The worker checks the exact receipt/state again before seal.
func (c *Core) startEndpointFollowingLocked(ctx context.Context, b *directLANBackend, old *endpointNodeOwner, mutation endpointmeta.Mutation, receipt *contextPublicationReceipt, state string, released <-chan error, exports ...endpointMoveDelivery) (*endpointFollowingJob, error) {
	deadline, bounded := ctx.Deadline()
	if !bounded || ctx.Err() != nil || !time.Now().Before(deadline) || b == nil || old == nil || old.origin == nil {
		return nil, endpointmeta.ErrReview
	}
	c.mu.Lock()
	if c.closing || c.ctx.Err() != nil || c.endpointJob != nil {
		c.mu.Unlock()
		return nil, directlan.ErrUnavailable
	}
	if old.observed.IsZero() {
		old.observed = time.Now()
	}
	// Closing the old connection must not cancel accepted input. Preserve its
	// original deadline, with Core cancellation, and never extend it on handoff.
	parent := c.ctx
	if released == nil {
		parent = ctx
	}
	run, cancel := context.WithDeadline(parent, deadline)
	job := &endpointFollowingJob{cancel: cancel, done: make(chan struct{}), backend: b}
	c.endpointJob = job
	c.mu.Unlock()
	observeEndpointAcceptance(c, "job-accepted", nil)
	go func() {
		defer func() {
			observeEndpointAcceptance(c, "job-finished", job.err)
			b.replacing.Store(nil)
			cancel()
			c.mu.Lock()
			if c.endpointJob == job {
				c.endpointJob = nil
			}
			close(job.done)
			c.mu.Unlock()
		}()
		if released != nil {
			observeEndpointAcceptance(c, "job-wait-wire-release", nil)
			select {
			case <-run.Done():
				job.err = run.Err()
				return
			case err, ok := <-released:
				if !ok || err != nil {
					job.err = directlan.ErrRetirementIncomplete
					return
				}
			}
		}
		observeEndpointAcceptance(c, "job-release-complete", nil)
		var planned []endpointmeta.Envelope
		t, err := c.saveEndpointTransactionWithOwner(run, b, mutation, old, time.Now, func(s *directLANStore) error {
			if s.contextPublication != receipt || privateRevision(s.state) != state {
				return endpointmeta.ErrReview
			}
			if len(exports) > 0 {
				var err error
				planned, err = s.preflightEndpointMoveExportsLocked(mutation, exports, old.observed)
				if err != nil {
					return err
				}
				for _, proof := range planned {
					if proof.Update.Lifetime == "finite" {
						absolute, _ := time.Parse(time.RFC3339Nano, proof.Update.Expires)
						cutoff := old.observed.Add(absolute.Sub(old.observed.UTC()))
						if old.proposalCutoff.IsZero() || cutoff.Before(old.proposalCutoff) {
							old.proposalCutoff = cutoff
						}
					}
				}
			}
			return nil
		})
		observeEndpointAcceptance(c, "job-save-returned", err)
		c.op.Lock()
		job.transaction = t
		c.op.Unlock()
		if t != nil {
			job.saved = t.durable
		}
		var proofs []endpointmeta.Envelope
		if err == nil && len(exports) > 0 {
			proofs, err = c.issueEndpointMoveProofs(run, t, exports, planned)
		}
		if err == nil {
			err = c.publishEndpointTransaction(run, t)
			observeEndpointAcceptance(c, "job-publish-returned", err)
		}
		if err == nil {
			job.active = true
			deliveryCtx := run
			if !t.proposalCutoff.IsZero() {
				var cancelDelivery context.CancelFunc
				deliveryCtx, cancelDelivery = context.WithDeadline(run, t.proposalCutoff)
				defer cancelDelivery()
			}
			for i, proof := range proofs {
				result := endpointDeliveryResult{PeerID: exports[i].PeerID, Sequence: proof.Update.Sequence, Outcome: "unconfirmed"}
				reply, attempts, sendErr := c.deliverEndpointWithRetry(deliveryCtx, b, t.receipt, t.deliveries[exports[i].PeerID], proof)
				result.Attempts = attempts
				observeEndpointAcceptance(c, "job-delivery-returned", sendErr)
				if sendErr == nil {
					result.Outcome = reply.Outcome
				}
				job.deliveries = append(job.deliveries, result)
			}
		}
		job.err = err
	}()
	return job, nil
}

func endpointObservationReply(envelope endpointmeta.Envelope, outcome string) *endpointmeta.UpdateReply {
	digest, _ := envelope.Digest()
	return &endpointmeta.UpdateReply{Version: 2, Operation: "endpoint-update", OK: true, PairBinding: envelope.Update.PairBinding, Sequence: envelope.Update.Sequence, UpdateDigest: digest, Outcome: outcome}
}

func (o *managedCompletionOwner) admitEndpoint(ctx context.Context, r *directlan.ManagedEndpointRequest) (_ *endpointmeta.UpdateReply, result error) {
	stage := "admission-enter"
	defer func() {
		if o != nil && o.core != nil {
			observeEndpointAcceptance(o.core, stage, result)
		}
	}()
	if o == nil || o.core == nil || ctx == nil || !o.core.op.TryLock() {
		return nil, directlan.ErrUnavailable
	}
	defer o.core.op.Unlock()
	stage = "admission-owner"
	if r == nil || r.Node() != o.node || !r.Current() || !o.authorityCurrent() || !o.coreCurrent(r.PeerKey()) || o.backend.currentCompletion() != o {
		return nil, directlan.ErrUntrusted
	}
	stage = "admission-receipt"
	s := o.store
	s.mu.Lock()
	defer s.mu.Unlock()
	// Reuse exact receipt/binding/current-projection admission, without granting a
	// bound session or treating a structural signature as publication authority.
	if _, err := o.currentReplyLocked(r.PeerKey(), endpointmeta.BoundRequest{Version: 2, Operation: "pair-context-status", PairBinding: r.Envelope().Update.PairBinding}, time.Now()); err != nil {
		return nil, err
	}
	m, err := s.endpointModelLocked(time.Now(), false)
	if err != nil {
		return nil, err
	}
	stage = "admission-proposal"
	mutation, outcome, err := endpointmeta.ProposeReceive(*m, r.PeerKey(), r.Envelope(), nil, time.Now())
	if err != nil {
		return nil, err
	}
	if !r.Current() {
		return nil, directlan.ErrUntrusted
	}
	if outcome != "candidate" {
		return endpointObservationReply(r.Envelope(), outcome), nil
	}
	stage = "admission-start-job"
	old := &endpointNodeOwner{node: o.node, origin: r.Origin()}
	_, err = o.core.startEndpointFollowingLocked(ctx, o.backend, old, mutation, s.contextPublication, privateRevision(s.state), r.Released())
	// No acknowledgement is emitted for a pending transaction. Idempotence comes
	// from the durable signed high-water, not this volatile scheduling result.
	return nil, err
}

// Live manual import has the same proposal and transaction path as automatic
// follow. It owns short operation sections only; review never tears down routes.
func (c *Core) liveEndpointCommand(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	input, err := decodeDirectLANEndpointInput(raw)
	if err != nil {
		return nil, directLANEndpointError(err)
	}
	if input.Cancel || input.TransactionID != "" {
		return nil, directLANEndpointError(endpointmeta.ErrInvalid)
	}
	preview := name == "direct-lan.endpoint.inspect" || name == "direct-lan.endpoint.follow.preview"
	want := ""
	switch name {
	case "direct-lan.endpoint.inspect":
		if input.Action == "" {
			input.Action = "receive"
		}
	case "direct-lan.endpoint.accept":
		want = "receive"
	case "direct-lan.endpoint.reapprove-current":
		want = "reapprove"
	case "direct-lan.endpoint.revoke":
		want = "revoke"
	case "direct-lan.endpoint.expire":
		want = "expire"
	case "direct-lan.endpoint.follow.preview", "direct-lan.endpoint.follow.apply":
		want = "grant-follow"
		if input.Action == "disable-follow" {
			want = "disable-follow"
		}
	default:
		return nil, directLANEndpointError(endpointmeta.ErrInvalid)
	}
	if want != "" {
		if input.Action != "" && input.Action != want {
			return nil, directLANEndpointError(endpointmeta.ErrInvalid)
		}
		input.Action = want
	}
	c.op.Lock()
	b := c.endpointBackendLocked()
	if b == nil || b.Node == nil || b.store == nil {
		c.op.Unlock()
		return nil, directlan.ErrUnavailable
	}
	o := b.currentCompletion()
	if o == nil || !o.coreCurrent(input.PeerID) {
		c.op.Unlock()
		return nil, directlan.ErrUnavailable
	}
	var retired *directlan.TransportRetirement
	if !o.activationCurrent() {
		// Recovery is restricted to this exact owner reaching its original deadline,
		// with successful physical retirement. Never revive the old epoch.
		if o.deadline.IsZero() || time.Now().Before(o.deadline) {
			c.op.Unlock()
			return nil, directlan.ErrUnavailable
		}
		retired, err = b.Node.ObserveManagedRetirement(b.replacementOwner)
		if err != nil {
			c.op.Unlock()
			return nil, err
		}
	}
	s := b.store
	s.mu.Lock()
	if s.contextPublication != o.receipt || !s.contextPublicationCurrentLocked(o.process) {
		s.mu.Unlock()
		c.op.Unlock()
		return nil, endpointmeta.ErrReview
	}
	now := time.Now()
	m, err := s.endpointModelLocked(now, false)
	var review directLANEndpointReview
	var mutation endpointmeta.Mutation
	if err == nil {
		review, mutation, err = inspectDirectLANEndpoint(*m, input, now)
	}
	if err == nil {
		err = s.endpointDeadlineErrorLocked(mutation.State, mutation.Kind, now)
	}
	if err == nil {
		review.EndpointUpdatesEnabled = true
		review.Revision = s.endpointReviewRevisionLocked(o.process, input, review)
	}
	if err != nil {
		s.mu.Unlock()
		c.op.Unlock()
		return nil, directLANEndpointError(err)
	}
	if preview {
		s.mu.Unlock()
		c.op.Unlock()
		return review, nil
	}
	if input.ExpectedRevision == "" || input.ExpectedRevision != review.Revision {
		s.mu.Unlock()
		c.op.Unlock()
		return nil, directLANEndpointReviewChanged()
	}
	if review.Outcome != "candidate" {
		s.mu.Unlock()
		c.op.Unlock()
		return map[string]any{"saved": false, "changed": false, "outcome": review.Outcome, "endpointUpdatesEnabled": true}, nil
	}
	old := &endpointNodeOwner{node: b.Node, retired: retired}
	if retired != nil {
		old.origin = retired.Origin()
	} else {
		old.origin, err = b.Node.CaptureTransportOrigin()
	}
	if err != nil {
		s.mu.Unlock()
		c.op.Unlock()
		return nil, err
	}
	// Manual calls without a deadline get one bounded operation; no consent or
	// proof lifetime is derived from this operational bound.
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	job, err := c.startEndpointFollowingLocked(bounded, b, old, mutation, s.contextPublication, privateRevision(s.state), nil)
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
	result := map[string]any{"saved": job.saved, "active": job.active, "endpointUpdatesEnabled": true, "outcome": "saved_pending_activation"}
	if !job.saved {
		result["outcome"] = "rejected"
	}
	if job.active {
		result["outcome"] = "applied"
	}
	return result, directLANEndpointError(job.err)
}

// Core.op held. Exact owned replacement alone suppresses destructive ordinary
// unavailable maintenance. Expiry/revocation still run; no grant is renewed.
func (c *Core) endpointReplacementOwnedLocked() bool {
	b := c.endpointBackendLocked()
	if b == nil {
		return false
	}
	t := b.endpointTransaction
	if t == nil || t.core != c || t.backend != b || t.store != b.store || t.failure != nil || t.published || b.endpointStopped.Load() {
		return false
	}
	c.mu.RLock()
	valid := !c.closing && c.ctx.Err() == nil && c.node == t.root && c.endpointJob != nil && c.endpointJob.backend == b
	c.mu.RUnlock()
	return valid
}

func (c *Core) liveEndpointStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	input, err := decodeDirectLANEndpointInput(raw)
	if err != nil || input != (directLANEndpointInput{}) {
		return nil, directLANEndpointError(endpointmeta.ErrInvalid)
	}
	c.op.Lock()
	defer c.op.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := c.endpointBackendLocked()
	if b == nil || b.store == nil {
		return nil, directlan.ErrUnavailable
	}
	state := "saved_unavailable"
	if c.endpointReplacementOwnedLocked() {
		state = "reconnecting"
	} else if o := b.currentCompletion(); o != nil && o.activationCurrent() {
		state = "active"
	}
	s := b.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.endpointFileCurrentLocked(); err != nil {
		return nil, directLANEndpointError(err)
	}
	if s.recovery {
		state = "recovery_required"
	}
	view := map[string]any{"state": state, "endpoint": s.state.Selection.Listen, "endpointUpdatesEnabled": true, "recoveryRequired": s.recovery}
	if s.state.Metadata != nil {
		view["peers"] = endpointPeerViews(*s.state.Metadata)
		view["storeRevision"] = s.state.Metadata.Revision
	}
	if t := b.endpointTransaction; t != nil {
		view["saved"] = t.durable
		view["cleanupPending"] = t.failure != nil
	}
	return view, nil
}
