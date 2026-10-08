package core

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// managedCompletionOwner binds ordinary authority to one admitted root/child.
type managedCompletionOwner struct {
	mu                     sync.RWMutex
	core                   *Core
	backend                *directLANBackend
	root                   NetworkBackend
	activation             *managedActivationAdmission
	authority              atomic.Pointer[directlan.ContextEpoch]
	node                   *directlan.Node
	store                  *directLANStore
	process, configuration string
	receipt                *contextPublicationReceipt
	epoch                  *directlan.ContextEpoch
	revision               uint64
	limits                 lanStoreLimits
	limitsSource           *lanStoreLimits
	stopped                atomic.Bool
	currentEndpoints       bool      // exact current-projection startup or successor
	deadline               time.Time // earliest original process cutoff; immutable
}

func (o *managedCompletionOwner) invalidate() {
	if o != nil {
		o.stopped.Store(true)
		o.authority.Load().Invalidate()
	}
}

// Only the coordinator supplies this one-use whole-state admission.
func (c *Core) newManagedCompletionBackendLocked(s *directLANStore, a *managedActivationAdmission) (*directLANBackend, error) {
	if a == nil || a.consumed || !c.activationOwnerCurrent(c.ctx, a) {
		return nil, directlan.ErrUnavailable
	}
	c.mu.RLock()
	joined := c.contextControl == nil
	denied := make(map[string]bool, len(c.managedDenied))
	for key, blocked := range c.managedDenied {
		denied[key] = blocked
	}
	c.mu.RUnlock()
	if !joined {
		return nil, directlan.ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.matchActivationLocked(a, time.Now()) != nil || s.contextPublication != a.receipt || !s.contextPublicationCurrentLocked(c.lanStartNonce) || s.contextEpoch != nil {
		return nil, endpointmeta.ErrReview
	}
	projection, err := s.activationProjectionLocked(a, time.Now())
	if err != nil {
		return nil, err
	}
	cfg, err := directLANConfig(cloneDirectLANState(s.state))
	if a.currentEndpoints {
		cfg, err = s.managedCurrentEndpointProjectionLocked(time.Now())
	}
	if err != nil {
		return nil, err
	}
	for _, peer := range projection.Peers {
		if denied[peer.Key] || denied[mixedID("direct-lan", peer.Key)] {
			return nil, directlan.ErrUntrusted
		}
	}
	cfg.Peers, cfg.PairContexts, cfg.DeniedPeerKeys = projection.Peers, projection.PairContexts, projection.DeniedPeerKeys
	resources := directRuntimeResources(c.capacityPolicy())
	cfg.FlowLimit, cfg.ListenerLimit, cfg.InvitationLimit, cfg.PacketQueueLimit = resources.Flows, resources.Listeners, resources.Invitations, resources.PacketQueue
	cfg.PeerLimitCurrent = s.transportPeerLimit
	// Membership additions use an exact evidence-preserving pairing delta.
	b := &directLANBackend{ctx: c.ctx, store: s, resources: resources, replacementOwner: directlan.NewManagedTransportOwner()}
	cfg.ReplacementOwner = b.replacementOwner
	o := &managedCompletionOwner{core: c, backend: b, store: s, activation: a, process: c.lanStartNonce,
		configuration: contextConfigurationDigest(s.state), receipt: s.contextPublication,
		revision: s.reviewRevision, limits: *s.currentCapacity(), limitsSource: s.limits.Load(),
		currentEndpoints: a.currentEndpoints, deadline: earliestEndpointDeadline(a.deadlines)}
	cfg.CompletionAdmission = o.admit
	cfg.EndpointAdmission = o.admitEndpoint
	cfg.Persist = o.persistLegacyAddition
	cfg.AuthorityCurrent = o.authorityCurrent
	cfg.AuthorityDeadline = o.deadline
	var n *directlan.Node
	if a.currentEndpoints {
		// This is the sole production issuer across the trusted internal Core /
		// directlan boundary. No external API accepts a Config, callback or owner.
		// Consume the exact admission even if construction fails; the transport
		// owner rechecks its original receipt/file/process/cutoffs on both sides.
		a.consumed = true
		startup := directlan.NewManagedStartupOwner(cfg, func() bool {
			return c.activationOwnerCurrent(c.ctx, a) && s.contextEpoch == nil &&
				s.contextPublication == a.receipt && s.contextPublicationCurrentLocked(a.process) &&
				s.matchActivationLocked(a, time.Now()) == nil
		})
		n, err = directlan.NewManagedStartupNode(startup)
	} else {
		n, err = directlan.NewNode(cfg)
	}
	if err != nil {
		return nil, err
	}
	// This is a NEW volatile response epoch after successful context-owner join.
	// It is not the old context arm and does not manufacture a durable receipt.
	a.consumed = true
	o.epoch = directlan.NewContextEpoch()
	o.authority.Store(o.epoch)
	s.contextEpoch = o.epoch
	o.node, b.Node, b.completion = n, n, o
	return b, nil
}

// Called under Core.op. b.closed is latched before its Node join; the separate
// signal is invalidated even earlier by Close, including failed-close retries.
func (o *managedCompletionOwner) current(r *directlan.ManagedCompletionRequest) bool {
	if o == nil || o.core == nil || o.backend == nil || o.node == nil || o.store == nil || o.stopped.Load() || !o.epoch.Valid() || r == nil || r.Node() != o.node || !r.Current() {
		return false
	}
	if !o.coreCurrent(r.PeerKey()) {
		return false
	}
	b := o.backend
	if !b.mu.TryLock() {
		return false
	}
	current := b.Node == o.node && b.currentCompletion() == o && b.store == o.store && b.ready && !b.closed && b.ctx != nil && b.ctx.Err() == nil
	b.mu.Unlock()
	return current && !o.stopped.Load()
}

// This is only the Core ownership/negative-policy part of the admission. It
// cannot substitute for the transport-minted request or store receipt checks.
func (o *managedCompletionOwner) coreCurrent(key string) bool {
	if o == nil || o.core == nil || o.backend == nil || o.store == nil || o.process == "" {
		return false
	}
	c := o.core
	c.mu.RLock()
	defer c.mu.RUnlock()
	rootCurrent := c.node == o.root && o.root != nil
	if mixed, ok := o.root.(*mixedBackend); ok {
		rootCurrent = rootCurrent && mixed.nodes["direct-lan"] == o.backend
	} else {
		rootCurrent = rootCurrent && o.root == o.backend
	}
	return rootCurrent && c.directLAN == o.store && c.contextControl == nil && c.lanStartNonce == o.process &&
		!c.closing && c.ctx != nil && c.ctx.Err() == nil && !c.managedCleanupPending && c.managedCleanupError == nil && (c.managedRemoval == nil || o.activation != nil && c.managedRemoval == o.activation.removal && c.managedRemoval.joined) &&
		!c.managedDenied[key] && !c.managedDenied[mixedID("direct-lan", key)]
}

// currentReplyLocked is pure with respect to authority: it reobserves the
// protected file/clock but never reduces, saves, confirms, republishes or mints
// an epoch. A caller-provided confirmed flag cannot qualify without the exact
// frozen same-process publication receipt and complete initial projection.
func (o *managedCompletionOwner) currentReplyLocked(key string, request endpointmeta.BoundRequest, now time.Time) (endpointmeta.ContextReply, error) {
	s := o.store
	if o.stopped.Load() || !o.epoch.Valid() || s.contextEpoch != o.epoch || s.contextPublication != o.receipt ||
		s.reviewRevision != o.revision || s.limits.Load() != o.limitsSource || *s.currentCapacity() != o.limits || !s.contextPublicationCurrentLocked(o.process) || contextConfigurationDigest(s.state) != o.configuration {
		return endpointmeta.ContextReply{}, endpointmeta.ErrReview
	}
	if request.Version != 2 || (request.Operation != "pair-context-commit" && request.Operation != "pair-context-status") {
		return endpointmeta.ContextReply{}, endpointmeta.ErrInvalid
	}
	projection, err := o.projectionLocked(now)
	if err != nil {
		return endpointmeta.ContextReply{}, err
	}
	// Capacity changes invalidate frozen owner admission even when the file fits.
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil || int64(len(data))+1 > o.limits.bytes || (o.limits.peers > 0 && int64(len(projection.Peers)) > o.limits.peers) {
		return endpointmeta.ContextReply{}, directlan.ErrCapacity
	}
	pair, ok := projection.PairContexts[key]
	if !ok {
		return endpointmeta.ContextReply{}, directlan.ErrUntrusted
	}
	binding, err := pair.Binding()
	if err != nil || binding != request.PairBinding {
		return endpointmeta.ContextReply{}, directlan.ErrUntrusted
	}
	return endpointmeta.ContextReply{Version: 2, Operation: request.Operation, OK: true, PairBinding: binding, State: "committed"}, nil
}

type managedCompletionSlot struct {
	epoch    *directlan.ContextEpoch
	receipt  *contextPublicationReceipt
	revision uint64
	owner    *managedCompletionOwner
	request  *directlan.ManagedCompletionRequest
	reply    endpointmeta.ContextReply
	deadline time.Time
	used     atomic.Bool
}

func (o *managedCompletionOwner) admit(ctx context.Context, request *directlan.ManagedCompletionRequest) (directlan.ContextResponse, error) {
	if o == nil || o.core == nil || ctx == nil || ctx.Err() != nil || !o.core.op.TryLock() {
		return directlan.ContextResponse{}, directlan.ErrUnavailable
	}
	defer o.core.op.Unlock()
	deadline, bounded := ctx.Deadline()
	if !bounded || !time.Now().Before(deadline) || !o.current(request) {
		return directlan.ContextResponse{}, directlan.ErrUnavailable
	}
	o.store.mu.Lock()
	defer o.store.mu.Unlock()
	reply, err := o.currentReplyLocked(request.PeerKey(), request.Request(), time.Now())
	if err != nil {
		return directlan.ContextResponse{}, err
	}
	slot := &managedCompletionSlot{owner: o, request: request, reply: reply, deadline: deadline, epoch: o.epoch, receipt: o.receipt, revision: o.revision}
	return directlan.ContextResponse{Reply: reply, Epoch: o.epoch, Admit: func() bool { return slot.admit(ctx) }}, nil
}
func (s *managedCompletionSlot) admit(ctx context.Context) bool {
	if s == nil || !s.used.CompareAndSwap(false, true) || s.owner == nil || ctx == nil || ctx.Err() != nil || !time.Now().Before(s.deadline) {
		return false
	}
	o := s.owner
	if !o.core.op.TryLock() {
		return false
	}
	defer o.core.op.Unlock()
	if s.epoch != o.epoch || !s.epoch.Valid() || s.receipt != o.receipt || s.revision != o.revision || !o.current(s.request) {
		return false
	}
	o.store.mu.Lock()
	reply, err := o.currentReplyLocked(s.request.PeerKey(), s.request.Request(), time.Now())
	o.store.mu.Unlock()
	// Core.Close may signal cancellation while waiting for Core.op. Reobserve
	// it after the protected-file read; Node's asynchronous close watcher is
	// not a substitute for this synchronous final admission check. Do not hold
	// store.mu while revisiting Core/backend/transport ownership.
	return err == nil && reply == s.reply && ctx.Err() == nil && time.Now().Before(s.deadline) && o.current(s.request)
}

// Signal-only, safe under Node/WG/generation locks. Every attempted publication
// invalidates the referenced epoch synchronously before the writer is invoked.
func (o *managedCompletionOwner) authorityCurrent() bool {
	return o != nil && !o.stopped.Load() && o.authority.Load().Valid() && o.core != nil && o.core.ctx != nil && o.core.ctx.Err() == nil && (o.deadline.IsZero() || time.Now().Before(o.deadline)) && (o.backend == nil || !o.backend.endpointStopped.Load())
}

// Core.op held; never invoke with Node.mu or backend.mu held.
func (o *managedCompletionOwner) activationCurrent() bool {
	if o == nil {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if !o.authorityCurrent() || !o.coreCurrent("") {
		return false
	}
	s := o.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contextPublication != o.receipt || !s.contextPublicationCurrentLocked(o.process) || s.contextEpoch != o.epoch || s.reviewRevision != o.revision || s.limits.Load() != o.limitsSource || *s.currentCapacity() != o.limits || contextConfigurationDigest(s.state) != o.configuration {
		return false
	}
	_, err := o.projectionLocked(time.Now())
	return err == nil && o.authorityCurrent()
}

func (o *managedCompletionOwner) projectionLocked(now time.Time) (managedFixedEndpointProjection, error) {
	if o.currentEndpoints {
		cfg, err := o.store.managedCurrentEndpointProjectionLocked(now)
		if err != nil {
			return managedFixedEndpointProjection{}, err
		}
		return managedFixedEndpointProjection{Peers: cfg.Peers, PairContexts: cfg.PairContexts, DeniedPeerKeys: cfg.DeniedPeerKeys}, nil
	}
	return o.store.managedFixedEndpointProjectionLocked(now)
}
