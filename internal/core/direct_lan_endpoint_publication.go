package core

import (
	"context"
	"errors"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// endpointProjectionIdentity excludes ownership callbacks, which are fresh per
// generation, but includes every projected public authority datum and inactive
// classification. The directlan projection also carries its own private seal.
func endpointProjectionIdentity(cfg directlan.Config) string {
	return privateRevision(struct {
		Identity         directlan.Identity
		Listen           string
		Prefixes         interface{}
		Peers            []directlan.Peer
		Pairs            map[string]endpointmeta.PairContext
		Denied, Inactive []string
	}{cfg.Identity, cfg.Listen.String(), cfg.AllowedPrefixes, cfg.Peers, cfg.PairContexts, cfg.DeniedPeerKeys, cfg.InactiveEndpointPeerKeys()})
}

// publishEndpointTransaction is the private continuation of the Core-owned
// endpoint job. Its observation handoff releases ALL old counted wire work
// before save and publish; public commands never call it under Core.op.
// The opaque saved result alone does not claim an active route.
func (c *Core) publishEndpointTransaction(ctx context.Context, t *EndpointTransaction) error {
	if ctx == nil || t == nil {
		return endpointmeta.ErrReview
	}
	c.op.Lock()
	if t.published || t.staging || !c.endpointTransactionOwnerCurrentLocked(ctx, t) {
		c.op.Unlock()
		return endpointmeta.ErrReview
	}
	old, ok := t.old.(*endpointNodeOwner)
	if !ok || old.retired == nil {
		c.op.Unlock()
		return endpointmeta.ErrReview
	}
	s, b := t.store, t.backend
	c.mu.RLock()
	denied := make(map[string]bool, len(c.managedDenied))
	for key, value := range c.managedDenied {
		denied[key] = value
	}
	c.mu.RUnlock()
	s.mu.Lock()
	err := c.endpointTransactionCurrentLocked(t, time.Now())
	var cfg directlan.Config
	if err == nil {
		cfg, err = s.managedCurrentEndpointProjectionLocked(time.Now())
	}
	if err != nil {
		s.mu.Unlock()
		c.op.Unlock()
		return err
	}
	for _, peer := range cfg.Peers {
		if denied[peer.Key] || denied[mixedID("direct-lan", peer.Key)] {
			s.mu.Unlock()
			c.op.Unlock()
			return directlan.ErrUntrusted
		}
	}
	projection := endpointProjectionIdentity(cfg)
	// Retain the original immutable deadline bounds, never projection's newly
	// derived wall-time cutoffs. The proposal freshness bound applies at commit;
	// ongoing authority uses the retained active-record bounds only.
	o := &managedCompletionOwner{core: c, backend: b, root: t.root, node: b.Node, store: s, process: t.process,
		configuration: contextConfigurationDigest(s.state), receipt: t.receipt, revision: s.reviewRevision,
		limits: t.limits, limitsSource: t.limitsSource, currentEndpoints: true, epoch: directlan.NewContextEpoch()}
	for _, bound := range t.deadlines {
		if o.deadline.IsZero() || bound.monotonic.Before(o.deadline) {
			o.deadline = bound.monotonic
		}
	}
	if previous := b.currentCompletion(); previous != nil {
		o.activation = previous.activation
	}
	o.authority.Store(o.epoch)
	resources := b.resources
	cfg.FlowLimit, cfg.ListenerLimit, cfg.InvitationLimit, cfg.PacketQueueLimit = resources.Flows, resources.Listeners, resources.Invitations, resources.PacketQueue
	cfg.PeerLimitCurrent = s.transportPeerLimit
	cfg.AuthorityCurrent, cfg.CompletionAdmission = o.authorityCurrent, o.admit
	cfg.EndpointAdmission = o.admitEndpoint
	cfg.AuthorityDeadline = o.deadline
	// This generation owns its own history-preserving one-peer legacy delta;
	// the invalidated predecessor callback is never reused.
	cfg.Persist = o.persistLegacyAddition
	t.staging = true
	s.mu.Unlock()
	c.op.Unlock()

	// Native construction and all cleanup waits are outside Core/store/Node locks.
	// Node registers staging before allocation; Stop sees it even before return.
	candidate, buildErr := b.Node.PrepareManagedTransport(ctx, b.replacementOwner, old.retired, cfg, t.fence)
	c.op.Lock()
	t.candidate = candidate
	t.constructionReturned = true
	if buildErr == nil && !c.endpointTransactionOwnerCurrentLocked(ctx, t) {
		buildErr = endpointmeta.ErrReview
	}
	s.mu.Lock()
	if buildErr == nil {
		buildErr = c.endpointTransactionCurrentLocked(t, time.Now())
	}
	if buildErr == nil {
		var current directlan.Config
		current, buildErr = s.managedCurrentEndpointProjectionLocked(time.Now())
		if buildErr == nil && endpointProjectionIdentity(current) != projection {
			buildErr = endpointmeta.ErrReview
		}
	}
	if buildErr == nil {
		// All expensive protected-file/model checks precede Node.mu. Only immutable
		// cutoffs and cancellation/Stop signals run inside the atomic publication.
		buildErr = b.Node.PublishManagedTransport(b.replacementOwner, candidate, func() bool {
			if ctx.Err() != nil || c.ctx.Err() != nil || b.endpointStopped.Load() || t.deadlineErrorAt(time.Now()) != nil || !o.authorityCurrent() ||
				t.published || t.candidate != candidate || b.endpointTransaction != t || s.endpointTransaction != t || s.contextPublication != t.receipt ||
				s.limits.Load() != t.limitsSource || *s.currentCapacity() != t.limits || c.lanStartWriteRevision.Load() != t.policyRevision || c.lanStartUncertain.Load() {
				return false
			}
			s.contextEpoch = o.epoch
			b.successor.Store(o)
			t.published = true
			b.replacing.CompareAndSwap(t, nil)
			t.staging = false
			t.candidate = nil
			b.endpointTransaction, s.endpointTransaction = nil, nil
			return true
		})
	}
	if buildErr == nil {
		// Core.op still excludes another replacement. Capture each exact target
		// from this published reviewed config before any later send unlocks it.
		t.deliveries = make(map[string]*directlan.EndpointDelivery, len(cfg.Peers))
		for _, peer := range cfg.Peers {
			if target, err := b.Node.CaptureEndpointDelivery(peer.Key, peer.Endpoint.String()); err == nil {
				t.deliveries[peer.Key] = target
			}
		}
		s.mu.Unlock()
		c.op.Unlock()
		return nil
	}
	t.failure = buildErr
	o.invalidate()
	s.mu.Unlock()
	c.op.Unlock()

	// Failed publication is saved-but-unavailable, never a rollback or success.
	return c.finishEndpointTransactionCleanup(ctx, t)
}

// Retrying this private cleanup join never retries publication. Cancellation or
// failed physical close retains the exact slot and candidate. A later caller
// may observe real completion without treating a timeout as acknowledgement.
func (c *Core) finishEndpointTransactionCleanup(ctx context.Context, t *EndpointTransaction) error {
	if ctx == nil || t == nil {
		return endpointmeta.ErrReview
	}
	c.op.Lock()
	if t.core != c || t.published || !t.staging || !t.joined || t.failure == nil || t.backend.endpointTransaction != t {
		c.op.Unlock()
		return endpointmeta.ErrReview
	}
	candidate, failure := t.candidate, t.failure
	// staging may still be in construction with Node retaining its owner. This
	// helper is entered only after the synchronous Prepare returned; the caller
	// marks constructionReturned under Core.op before exposing cleanup admission.
	if !t.constructionReturned {
		c.op.Unlock()
		return endpointmeta.ErrReview
	}
	c.op.Unlock()
	var cleanupErr error
	if candidate != nil {
		candidate.Abort()
		cleanupErr = candidate.WaitClosed(ctx)
	}
	c.op.Lock()
	defer c.op.Unlock()
	if cleanupErr != nil {
		t.failure = errors.Join(failure, cleanupErr)
		return t.failure
	}
	s, b := t.store, t.backend
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.endpointTransaction == t && s.endpointTransaction == t && !t.published {
		b.endpointTransaction, s.endpointTransaction = nil, nil
		t.staging, t.candidate = false, nil
	}
	return failure
}
