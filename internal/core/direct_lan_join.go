package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
)

// This admission is private, operation-specific and never supplied by callers.
// Only the bounded invitation exchange runs without Core.op; the Node-owned
// persistence callback acquires it nonblockingly while holding Node.mu.
type directLANJoinAdmission struct {
	core         *Core
	backend      *directLANBackend
	owner        *managedCompletionOwner
	store        *directLANStore
	node         *directlan.Node
	invitation   directlan.Invitation
	limits       lanStoreLimits
	limitsSource *lanStoreLimits
}

func (c *Core) captureDirectLANJoinLocked(ctx context.Context, raw json.RawMessage) (*directLANJoinAdmission, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.ctx == nil || c.ctx.Err() != nil {
		return nil, directlan.ErrUnavailable
	}
	c.mu.RLock()
	closing := c.closing
	c.mu.RUnlock()
	if closing {
		return nil, directlan.ErrUnavailable
	}
	var input struct {
		Invitation string `json:"invitation"`
	}
	if err := decodePayload(raw, &input); err != nil {
		return nil, err
	}
	invitation, err := directlan.ParseInvitation(input.Invitation)
	if err != nil {
		return nil, codedDirectLANError(err)
	}
	s := c.directLANStoreCopy()
	b, ok := c.nodeCopy().(*directLANBackend)
	if s == nil || !ok || b == nil || b.store != s || b.Node == nil {
		return nil, codedDirectLANError(directlan.ErrUnavailable)
	}
	b.mu.Lock()
	ready := b.ready && !b.closed && b.ctx != nil && b.ctx.Err() == nil
	b.mu.Unlock()
	if !ready {
		return nil, codedDirectLANError(directlan.ErrUnavailable)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := cloneDirectLANState(s.state)
	if err := invitation.ValidateFor(state.Identity.PublicKey(), time.Now()); err != nil {
		return nil, codedDirectLANError(err)
	}
	state.Peers = []directlan.Peer{invitation.Host}
	if _, err := directLANConfig(state); err != nil {
		return nil, codedDirectLANError(err)
	}
	if s.recovery {
		return nil, codedDirectLANError(directlan.ErrRecovery)
	}
	o := b.completion
	if o != nil {
		if !o.coreCurrent(invitation.Host.Key) || !o.authorityCurrent() || o.store != s || o.node != b.Node ||
			s.contextPublication != o.receipt || s.reviewRevision != o.revision || !s.contextPublicationCurrentLocked(o.process) ||
			contextConfigurationDigest(s.state) != o.configuration || s.limits.Load() != o.limitsSource || *s.currentCapacity() != o.limits {
			return nil, codedDirectLANError(directlan.ErrUnavailable)
		}
	}
	return &directLANJoinAdmission{core: c, backend: b, owner: o, store: s, node: b.Node, invitation: invitation,
		limits: *s.currentCapacity(), limitsSource: s.limits.Load()}, nil
}

func (a *directLANJoinAdmission) finishLocked(ctx context.Context) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, b, s := a.core, a.backend, a.store
	c.mu.RLock()
	closing := c.closing
	c.mu.RUnlock()
	b.mu.Lock()
	ready := b.ready && !b.closed && b.ctx != nil && b.ctx.Err() == nil
	b.mu.Unlock()
	if closing || !ready {
		return nil, codedDirectLANError(directlan.ErrRecovery)
	}
	if c.ctx.Err() != nil || c.nodeCopy() != b || c.directLANStoreCopy() != s || b.Node != a.node || b.completion != a.owner {
		return nil, codedDirectLANError(directlan.ErrRecovery)
	}
	if a.owner != nil && (!a.owner.coreCurrent(a.invitation.Host.Key) || !a.owner.authorityCurrent()) {
		return nil, codedDirectLANError(directlan.ErrRecovery)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recovery || s.limits.Load() != a.limitsSource || *s.currentCapacity() != a.limits {
		return nil, codedDirectLANError(directlan.ErrRecovery)
	}
	projection, err := s.managedFixedEndpointProjectionLocked(time.Now())
	if err != nil {
		return nil, codedDirectLANError(err)
	}
	for _, peer := range projection.Peers {
		if peer == a.invitation.Host {
			return map[string]any{"peerId": peer.Key, "paired": true, "trusted": false}, nil
		}
	}
	return nil, codedDirectLANError(directlan.ErrRecovery)
}

func (c *Core) executeDirectLANJoin(ctx context.Context, raw json.RawMessage) (any, error) {
	c.op.Lock()
	admission, err := c.captureDirectLANJoinLocked(ctx, raw)
	c.op.Unlock()
	if err != nil {
		return nil, err
	}
	return admission.run(ctx, admission.node.PairInvitation)
}

// pair is the already captured Node method in production. Keeping this bounded
// runner separate permits inert command-lifecycle tests without a live Node.
func (admission *directLANJoinAdmission) run(ctx context.Context, pair func(context.Context, directlan.Invitation) error) (any, error) {
	c := admission.core
	pairCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	if err := pair(pairCtx, admission.invitation); err != nil {
		return nil, codedDirectLANError(err)
	}
	c.op.Lock()
	defer c.op.Unlock()
	return admission.finishLocked(pairCtx)
}
