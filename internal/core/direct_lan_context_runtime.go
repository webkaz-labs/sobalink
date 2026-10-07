package core

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// This owner is deliberately separate from Core.node. No command, startup,
// application backend or endpoint publication path constructs or exposes it.
// Entry requires a fresh offline process; attemptedNetwork is never reset.
type contextControlOwner struct {
	node          *directlan.Node
	store         *directLANStore
	process       string
	configuration string
	ctx           context.Context
	cancel        context.CancelFunc
	started       chan struct{}
	stopping      atomic.Bool
	closeOnce     sync.Once
	closeErr      error
	// Core.op owns associations. Pointer identity, not serialized inputs or a
	// caller-provided token, joins the admission to its transport attempt.
	operations map[*directlan.ContextAttempt]*contextExchangeOperation
}

type contextExchangeOperation struct {
	owner     *contextControlOwner
	attempt   *directlan.ContextAttempt
	admission contextAdmission
	epoch     *directlan.ContextEpoch
	direction directlan.ContextDirection
	operation directlan.ContextOperation
}

type contextResponseSlot struct {
	owner         *contextControlOwner
	attempt       *directlan.ContextAttempt
	store         *directLANStore
	process       string
	receipt       *contextPublicationReceipt
	epoch         *directlan.ContextEpoch
	writeRevision uint64
	replyDigest   string
	deadline      time.Time
	used          atomic.Bool
}

func (o *contextControlOwner) requestClose() {
	if o == nil {
		return
	}
	o.stopping.Store(true)
	o.cancel()
	if o.node != nil {
		o.node.RequestClose()
	}
}

// Only an outside lifecycle owner joins. Completion callbacks use requestClose
// and return so that Node.Close can join their actual work without deadlocking.
func (o *contextControlOwner) close() error {
	o.requestClose()
	o.closeOnce.Do(func() {
		o.closeErr = o.node.Close()
		<-o.started
	})
	return o.closeErr
}

func (c *Core) currentContextOwnerLocked(o *contextControlOwner) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if o == nil || o.node == nil || o.store == nil || o.ctx == nil || o.process == "" || c.contextControl != o || c.directLAN != o.store || c.lanStartNonce != o.process ||
		c.node != nil || c.attemptedNetwork != "" || c.closing || c.ctx == nil || c.ctx.Err() != nil || o.ctx.Err() != nil || o.stopping.Load() {
		return directlan.ErrUnavailable
	}
	return nil
}

// The projection has only exact saved identities/endpoints, selected prefixes
// and a finite control budget. It carries neither Persist nor WG/application
// configuration. runtimeConfig and the managed-metadata guard stay unchanged.
func (s *directLANStore) contextConfigLocked(now time.Time, limit int) (directlan.ContextControlConfig, string, error) {
	if _, err := s.endpointModelLocked(now, false); err != nil {
		return directlan.ContextControlConfig{}, "", err
	}
	cfg, err := directLANConfig(cloneDirectLANState(s.state))
	if err != nil {
		return directlan.ContextControlConfig{}, "", err
	}
	if limit <= 0 {
		return directlan.ContextControlConfig{}, "", directlan.ErrCapacity
	}
	projection := directlan.ContextControlConfig{Identity: cfg.Identity, Listen: cfg.Listen, AllowedPrefixes: cfg.AllowedPrefixes, Peers: cfg.Peers, ControlLimit: limit}
	return projection, contextConfigurationDigest(s.state), nil
}

func contextConfigurationDigest(s directLANState) string {
	// Config itself has function fields and is not JSON-encodable. Hash only
	// the complete immutable identity, selection and exact saved peer projection.
	return privateRevision(struct {
		Identity  directlan.Identity
		Selection DirectLANSelection
		Peers     []directlan.Peer
	}{s.Identity, s.Selection, s.Peers})
}

// startContextControl has no production caller in this source slice. The Core
// pointer is installed before Start and remains present throughout construction,
// cleanup and any failed Close. No Core.op lock spans Start's socket work.
func (c *Core) startContextControl(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.op.Lock()
	s, process, err := c.contextStoreLocked()
	if err != nil {
		c.op.Unlock()
		return err
	}
	c.mu.RLock()
	existing := c.contextControl
	c.mu.RUnlock()
	if existing != nil {
		c.op.Unlock()
		return directlan.ErrUnavailable
	}
	limit := directRuntimeResources(c.capacityPolicy()).Invitations
	s.mu.Lock()
	cfg, configuration, err := s.contextConfigLocked(time.Now(), limit)
	s.mu.Unlock()
	if err != nil {
		c.op.Unlock()
		return err
	}
	lifetime, cancel := context.WithCancel(c.ctx)
	o := &contextControlOwner{store: s, process: process, configuration: configuration, ctx: lifetime, cancel: cancel, started: make(chan struct{}), operations: make(map[*directlan.ContextAttempt]*contextExchangeOperation)}
	cfg.Completion = func(ctx context.Context, attempt *directlan.ContextAttempt, verified directlan.VerifiedContextExchange) (directlan.ContextResponse, error) {
		return c.completeContextExchange(ctx, o, attempt, verified)
	}
	o.node, err = directlan.NewContextControl(cfg)
	if err != nil {
		cancel()
		c.op.Unlock()
		return err
	}
	c.mu.Lock()
	c.contextControl = o
	stopping := c.closing || c.ctx.Err() != nil || ctx.Err() != nil
	c.mu.Unlock()
	c.op.Unlock()
	if stopping {
		close(o.started)
		o.requestClose()
		c.op.Lock()
		err := c.stopContextControlLocked()
		c.op.Unlock()
		return errors.Join(directlan.ErrUnavailable, err)
	}

	stop := context.AfterFunc(ctx, o.requestClose)
	err = o.node.Start(o.ctx)
	stop()
	close(o.started)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = o.ctx.Err()
	}
	if err != nil {
		o.requestClose()
		// Start has returned and callbacks never wait for Core.op. Joining is
		// safe outside Core.op even when Core.Close is joining the same owner.
		closeErr := o.close()
		c.op.Lock()
		if closeErr == nil {
			c.mu.Lock()
			if c.contextControl == o {
				c.contextControl = nil
			}
			c.mu.Unlock()
		}
		c.op.Unlock()
		return errors.Join(err, closeErr)
	}
	return nil
}

// Caller holds Core.op. Failed cleanup remains charged to this owner and keeps
// ordinary start/replacement excluded; it is never relabeled a successful join.
func (c *Core) stopContextControlLocked() error {
	c.mu.RLock()
	o := c.contextControl
	c.mu.RUnlock()
	if o == nil {
		return nil
	}
	o.requestClose()
	for attempt := range o.operations {
		attempt.Cancel()
		delete(o.operations, attempt)
	}
	o.store.mu.Lock()
	if o.store.contextEpoch != nil {
		o.store.contextEpoch.Invalidate()
		o.store.contextEpoch = nil
	}
	o.store.mu.Unlock()
	err := o.close()
	if err == nil {
		c.mu.Lock()
		if c.contextControl == o {
			c.contextControl = nil
		}
		c.mu.Unlock()
	}
	return err
}

func contextPrepareRequest(m endpointmeta.Snapshot, r endpointmeta.PeerRecord) (endpointmeta.PrepareRequest, error) {
	if r.UpgradePending == nil {
		return endpointmeta.PrepareRequest{}, endpointmeta.ErrReview
	}
	request := endpointmeta.PrepareRequest{Version: 2, Operation: "pair-context-prepare", Sender: m.LocalPeer.Key, Recipient: r.Peer.Key,
		SenderTunnelKey: m.LocalPeer.TunnelKey, RecipientTunnelKey: r.Peer.TunnelKey, SenderNonce: r.UpgradePending.OwnNonce,
		SenderEndpoint: m.LocalPeer.Endpoint, RecipientEndpoint: r.Peer.Endpoint, SenderScope: m.LocalScope}
	_, err := endpointmeta.Encode(request)
	return request, err
}

// This explicit local step precedes capture/arming. Inbound status never calls
// it after reading a request. Prepared-only reopen publication changes no model
// bytes, nonce, revision or deadline and restores no first-commit permission.
func (s *directLANStore) publishContextBeforeArmLocked(ctx context.Context, owner *contextControlOwner, key string, now time.Time) error {
	process := owner.process
	if s.contextPublicationCurrentLocked(process) {
		return nil
	}
	i, err := contextPeer(*s.state.Metadata, key)
	if err != nil {
		return err
	}
	r := s.state.Metadata.Peers[i]
	if contextSavedPair(r) == nil {
		return nil // reviewed-only: first record must itself durably publish
	}
	op := contextRepublish
	if r.PairContext == nil {
		op = contextRepublishPrepared
	}
	a, err := s.captureContextAdmissionLocked(process, contextInputs{Operation: op, PeerKey: key}, now)
	if err != nil {
		return err
	}
	_, err = s.saveContextTransitionWithLivenessLocked(ctx, process, a, contextTranscript{}, now, &contextSaveLiveness{ctx: ctx, owner: owner})
	return err
}

// Core.op is held through freeze and registration, never through Exchange.
// No store mutex is held while acquiring Node gates. There is at most one
// prepare/commit/status association per saved peer across both directions.
func (c *Core) captureContextExchangeLocked(ctx context.Context, o *contextControlOwner, key string, operation directlan.ContextOperation, direction directlan.ContextDirection) (*contextExchangeOperation, error) {
	if err := c.currentContextOwnerLocked(o); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := o.store
	s.mu.Lock()
	a, request, epoch, err := s.contextExchangeAdmissionLocked(ctx, o, key, operation, direction, time.Now())
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for attempt, old := range o.operations {
		if !old.epoch.Valid() || old.admission.inputs.PeerKey == key && old.operation == operation {
			attempt.Cancel()
			delete(o.operations, attempt)
		}
	}
	var attempt *directlan.ContextAttempt
	if direction == directlan.ContextInbound {
		attempt, err = o.node.ArmContextAttempt(key, request, epoch)
	} else {
		attempt, err = o.node.PrepareContextAttempt(key, request, epoch)
	}
	if err != nil {
		return nil, err
	}
	result := &contextExchangeOperation{owner: o, attempt: attempt, admission: a, epoch: epoch, direction: direction, operation: operation}
	o.operations[attempt] = result
	return result, nil
}

func (c *Core) armContextInboundLocked(ctx context.Context, o *contextControlOwner, key string, operation directlan.ContextOperation) (*contextExchangeOperation, error) {
	return c.captureContextExchangeLocked(ctx, o, key, operation, directlan.ContextInbound)
}

func (c *Core) prepareContextOutboundLocked(ctx context.Context, o *contextControlOwner, key string, operation directlan.ContextOperation) (*contextExchangeOperation, error) {
	return c.captureContextExchangeLocked(ctx, o, key, operation, directlan.ContextOutbound)
}

func (s *directLANStore) contextExchangeAdmissionLocked(ctx context.Context, o *contextControlOwner, key string, operation directlan.ContextOperation, direction directlan.ContextDirection, now time.Time) (contextAdmission, endpointmeta.Request, *directlan.ContextEpoch, error) {
	fail := func(err error) (contextAdmission, endpointmeta.Request, *directlan.ContextEpoch, error) {
		return contextAdmission{}, nil, nil, err
	}
	if direction != directlan.ContextInbound && direction != directlan.ContextOutbound {
		return fail(endpointmeta.ErrInvalid)
	}
	m, err := s.endpointModelLocked(now, false)
	if err != nil {
		return fail(err)
	}
	if _, err := directLANConfig(cloneDirectLANState(s.state)); err != nil || contextConfigurationDigest(s.state) != o.configuration {
		return fail(endpointmeta.ErrReview)
	}
	i, err := contextPeer(*m, key)
	if err != nil {
		return fail(err)
	}
	r := m.Peers[i]
	in := contextInputs{PeerKey: key}
	var request endpointmeta.Request
	switch operation {
	case directlan.ContextPrepare:
		prepare, err := contextPrepareRequest(*m, r)
		if err != nil {
			return fail(err)
		}
		request = prepare
		in.Operation = contextRecordInbound
		if direction == directlan.ContextOutbound {
			in.Operation, in.Prepare = contextRecordOutbound, prepare
		}
	case directlan.ContextCommit, directlan.ContextStatus:
		p := contextSavedPair(r)
		if p == nil {
			return fail(endpointmeta.ErrReview)
		}
		binding, err := p.Binding()
		if err != nil {
			return fail(err)
		}
		bound := endpointmeta.BoundRequest{Version: 2, Operation: "pair-context-commit", PairBinding: binding}
		if operation == directlan.ContextStatus {
			bound.Operation = "pair-context-status"
		}
		request, in.Bound = bound, bound
		in.Operation = contextCommit
		if operation == directlan.ContextStatus {
			in.Operation = contextStatusInbound
		}
		if direction == directlan.ContextOutbound {
			if operation == directlan.ContextCommit {
				// The initiator commits this exact prepared binding locally
				// before capturing the commit exchange/confirmation admission.
				local, err := s.captureContextAdmissionLocked(o.process, contextInputs{Operation: contextCommit, PeerKey: key, Bound: bound}, now)
				if err != nil {
					return fail(err)
				}
				if _, err := s.saveContextTransitionWithLivenessLocked(ctx, o.process, local, contextTranscript{Bound: bound}, now, &contextSaveLiveness{ctx: ctx, owner: o}); err != nil {
					return fail(err)
				}
				in.Operation = contextConfirmCommit
			} else {
				if r.PairContext == nil {
					return fail(endpointmeta.ErrReview)
				}
				in.Operation = contextConfirmStatus
			}
		}
	default:
		return fail(endpointmeta.ErrInvalid)
	}
	if err := s.publishContextBeforeArmLocked(ctx, o, key, now); err != nil {
		return fail(err)
	}
	a, err := s.captureContextAdmissionLocked(o.process, in, now)
	if err != nil {
		return fail(err)
	}
	return a, request, s.contextEpochLocked(), nil
}

// Exchange owns the completion synchronously through actual wire cleanup. It
// returns no evidence/response capability after its work has been released.
func (c *Core) exchangeContext(ctx context.Context, operation *contextExchangeOperation) error {
	if operation == nil || operation.attempt == nil || operation.direction != directlan.ContextOutbound {
		return directlan.ErrUnavailable
	}
	err := operation.attempt.Exchange(ctx, func(ctx context.Context, attempt *directlan.ContextAttempt, verified directlan.VerifiedContextExchange) (directlan.ContextResponse, error) {
		return c.completeContextExchange(ctx, operation.owner, attempt, verified)
	})
	// The wire and callback have returned before taking this blocking lock.
	// Also detach attempts whose dial/handshake failed before any completion.
	c.op.Lock()
	if operation.owner.operations[operation.attempt] == operation {
		delete(operation.owner.operations, operation.attempt)
	}
	c.op.Unlock()
	return err
}

func contextExchangeData(operation *contextExchangeOperation, verified directlan.ContextExchangeTranscript) (contextTranscript, error) {
	if verified.Direction() != operation.direction || verified.Operation() != operation.operation {
		return contextTranscript{}, endpointmeta.ErrIdentity
	}
	request := verified.Request()
	in := operation.admission.inputs
	var transcript contextTranscript
	switch in.Operation {
	case contextRecordInbound, contextRecordOutbound:
		var prepare endpointmeta.PrepareRequest
		switch r := request.(type) {
		case endpointmeta.PrepareRequest:
			prepare = r
		case *endpointmeta.PrepareRequest:
			if r == nil {
				return transcript, endpointmeta.ErrInvalid
			}
			prepare = *r
		default:
			return transcript, endpointmeta.ErrInvalid
		}
		if in.Operation == contextRecordInbound {
			transcript.Prepare = prepare
		} else {
			if !reflect.DeepEqual(prepare, in.Prepare) {
				return transcript, endpointmeta.ErrIdentity
			}
			switch reply := verified.Reply().(type) {
			case endpointmeta.PrepareReply:
				transcript.Prepared = reply
			case *endpointmeta.PrepareReply:
				if reply == nil {
					return transcript, endpointmeta.ErrInvalid
				}
				transcript.Prepared = *reply
			default:
				return transcript, endpointmeta.ErrInvalid
			}
		}
	case contextCommit, contextStatusInbound, contextConfirmCommit, contextConfirmStatus:
		var bound endpointmeta.BoundRequest
		switch r := request.(type) {
		case endpointmeta.BoundRequest:
			bound = r
		case *endpointmeta.BoundRequest:
			if r == nil {
				return transcript, endpointmeta.ErrInvalid
			}
			bound = *r
		default:
			return transcript, endpointmeta.ErrInvalid
		}
		if bound != in.Bound {
			return transcript, endpointmeta.ErrIdentity
		}
		if operation.direction == directlan.ContextInbound {
			transcript.Bound = bound
		} else {
			switch reply := verified.Reply().(type) {
			case endpointmeta.ContextReply:
				transcript.Committed = reply
			case *endpointmeta.ContextReply:
				if reply == nil {
					return transcript, endpointmeta.ErrInvalid
				}
				transcript.Committed = *reply
			default:
				return transcript, endpointmeta.ErrInvalid
			}
		}
	default:
		return transcript, endpointmeta.ErrInvalid
	}
	return transcript.frozen(in)
}

func (c *Core) completeContextExchange(ctx context.Context, owner *contextControlOwner, attempt *directlan.ContextAttempt, verified directlan.VerifiedContextExchange) (directlan.ContextResponse, error) {
	if attempt == nil {
		return directlan.ContextResponse{}, endpointmeta.ErrInvalid
	}
	if !c.op.TryLock() {
		attempt.Cancel()
		return directlan.ContextResponse{}, directlan.ErrUnavailable
	}
	defer c.op.Unlock()
	if err := c.currentContextOwnerLocked(owner); err != nil {
		attempt.Cancel()
		return directlan.ContextResponse{}, err
	}
	operation := owner.operations[attempt]
	if operation == nil || operation.owner != owner || operation.attempt != attempt {
		attempt.Cancel()
		return directlan.ContextResponse{}, endpointmeta.ErrReview
	}
	delete(owner.operations, attempt)
	// Claim before store.mu. The transport's Node/generation locks have been
	// released when this returns, including rejection on TryLock contention.
	evidence, err := verified.TryClaimFor(attempt)
	if err != nil {
		return directlan.ContextResponse{}, err
	}
	transcript, err := contextExchangeData(operation, evidence)
	if err != nil {
		return directlan.ContextResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return directlan.ContextResponse{}, err
	}
	s := owner.store
	s.mu.Lock()
	closeOwner := false
	defer func() {
		s.mu.Unlock()
		if closeOwner {
			owner.requestClose()
		}
	}()
	if !operation.epoch.Valid() || s.contextEpoch != operation.epoch {
		return directlan.ContextResponse{}, endpointmeta.ErrReview
	}
	live := &contextSaveLiveness{ctx: ctx, owner: owner, attempt: attempt}
	result, err := s.saveContextTransitionWithLivenessLocked(ctx, owner.process, operation.admission, transcript, time.Now(), live)
	if err != nil {
		closeOwner = s.recovery || result.published
		return directlan.ContextResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return directlan.ContextResponse{}, err
	}
	if operation.direction == directlan.ContextOutbound {
		return directlan.ContextResponse{}, nil
	}
	reply, err := s.contextReplyLocked(owner.process, operation.admission.inputs, transcript, time.Now())
	if err != nil {
		return directlan.ContextResponse{}, err
	}
	deadline, bounded := ctx.Deadline()
	if !bounded || !time.Now().Before(deadline) || ctx.Err() != nil {
		return directlan.ContextResponse{}, directlan.ErrUnavailable
	}
	epoch := s.contextEpochLocked()
	slot := &contextResponseSlot{owner: owner, attempt: attempt, store: s, process: owner.process, receipt: s.contextPublication,
		epoch: epoch, writeRevision: s.reviewRevision, replyDigest: privateRevision(reply), deadline: deadline}
	return directlan.ContextResponse{Reply: reply, Epoch: epoch, Admit: func() bool { return c.admitContextResponse(ctx, slot, attempt, reply) }}, nil
}

// This gate is one synchronous send admission, not a reusable handle. It does
// not hold Core.op/store.mu during TLS I/O; DirectLAN repeats its current
// Node/generation/registration/cancellation checks after this function returns.
func (c *Core) admitContextResponse(ctx context.Context, slot *contextResponseSlot, attempt *directlan.ContextAttempt, reply endpointmeta.Reply) bool {
	if slot == nil || !slot.used.CompareAndSwap(false, true) || !c.op.TryLock() {
		return false
	}
	defer c.op.Unlock()
	if ctx.Err() != nil || !time.Now().Before(slot.deadline) || !slot.epoch.Valid() || slot.attempt != attempt ||
		privateRevision(reply) != slot.replyDigest || c.currentContextOwnerLocked(slot.owner) != nil {
		return false
	}
	s := slot.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contextEpoch != slot.epoch || !slot.epoch.Valid() || s.contextPublication != slot.receipt || s.reviewRevision != slot.writeRevision || !s.contextPublicationCurrentLocked(slot.process) {
		return false
	}
	if _, err := s.endpointModelLocked(time.Now(), false); err != nil {
		return false
	}
	return ctx.Err() == nil && slot.epoch.Valid() && time.Now().Before(slot.deadline)
}
