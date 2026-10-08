package directlan

import (
	"context"
	"net"
	"reflect"
	"time"

	"github.com/webkaz-labs/sobalink/internal/endpointmeta"
)

// managedCandidateConfig validates copied data, not persistence. The retained
// complete pre-transition model supplies membership, including inactive pairs;
// the old runtime peer map is deliberately not a membership authority.
func (n *Node) managedCandidateConfig(retired *TransportRetirement, cfg Config, retained endpointmeta.Snapshot) (Config, error) {
	if !retired.valid() || retired.g.n != n || cfg.currentEndpoints == nil || cfg.AuthorityCurrent == nil || cfg.CompletionAdmission == nil {
		return Config{}, ErrUntrusted
	}
	if err := retained.ValidateAt(time.Now()); err != nil {
		return Config{}, err
	}
	cfg = cfg.withDefaults()
	cfg.ReplacementOwner = n.cfg.ReplacementOwner
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	old := retired.g.cfg
	if cfg.Identity != old.Identity || cfg.Listen.Addr().Is4() != old.Listen.Addr().Is4() || !reflect.DeepEqual(cfg.AllowedPrefixes, old.AllowedPrefixes) ||
		cfg.FlowLimit != old.FlowLimit || cfg.ListenerLimit != old.ListenerLimit || cfg.InvitationLimit != old.InvitationLimit || cfg.PacketQueueLimit != old.PacketQueueLimit || cfg.ControlLimit != old.ControlLimit {
		return Config{}, ErrPolicy
	}
	if retained.LocalPeer.Key != cfg.Identity.PublicKey() || retained.LocalPeer.TunnelKey != cfg.Identity.TunnelKey() || retained.LocalPeer.Endpoint != old.Listen.String() {
		return Config{}, ErrIdentity
	}
	if cfg.Listen != old.Listen && (cfg.currentEndpoints.localEndpoint != cfg.Listen.String() || cfg.currentEndpoints.previousLocalEndpoint != old.Listen.String()) {
		return Config{}, ErrIdentity
	}
	for _, peer := range cfg.Peers {
		found := false
		for _, record := range retained.Peers {
			if record.Peer.Key != peer.Key {
				continue
			}
			if record.PairRevocation != nil || record.Peer.TunnelKey != peer.TunnelKey || record.Peer.Name != peer.Name {
				return Config{}, ErrUntrusted
			}
			pair, managed := cfg.PairContexts[peer.Key]
			if record.PairContext != nil {
				if !managed || !record.ContextConfirmed || !reflect.DeepEqual(pair, *record.PairContext) {
					return Config{}, ErrUntrusted
				}
			} else if managed || record.EndpointState != nil || record.UpgradePending != nil {
				return Config{}, ErrUntrusted
			}
			found = true
			break
		}
		if !found {
			return Config{}, ErrUntrusted
		}
	}
	if cfg.PeerLimitCurrent != nil {
		limit := cfg.PeerLimitCurrent()
		if limit < 0 || limit > 0 && len(cfg.Peers) > limit {
			return Config{}, ErrCapacity
		}
	}
	return cloneGenerationConfig(cfg), nil
}

// PrepareManagedTransport is an internal Core integration seam, not a wire or
// public command. Core supplies a current sole-writer receipt and original
// deadlines by independently checking its opaque transaction before and after
// construction. Projection alone cannot open traffic. Ordinary PrepareTransport
// and NewNode retain their protected/current-projection rejection guards.
func (n *Node) PrepareManagedTransport(ctx context.Context, owner *ManagedTransportOwner, retired *TransportRetirement, cfg Config, retained endpointmeta.Snapshot) (*PreparedTransport, error) {
	if !n.managedReplacementOwner(owner) || ctx == nil || ctx.Err() != nil || n.contextControl {
		return nil, ErrUnavailable
	}
	n.mu.Lock()
	if n.closed || n.closing.Load() || n.nonTransportRecovery || !n.started || !retired.valid() || retired.g.n != n || n.generation.Load() != retired.g {
		n.mu.Unlock()
		return nil, ErrRecovery
	}
	if err := retired.completed(); err != nil {
		n.mu.Unlock()
		return nil, err
	}
	if n.building.Load() != nil || n.staged.Load() != nil {
		n.mu.Unlock()
		return nil, ErrCapacity
	}
	// Pure model work under Node.mu does not wait for an owner or call Core.
	candidateCfg, err := n.managedCandidateConfig(retired, cfg, retained)
	if err != nil {
		n.mu.Unlock()
		return nil, err
	}
	// Construction has a detached lifetime. The transaction context is watched
	// only while p still owns staging; publication transfers cancellation to Node.
	p := &PreparedTransport{node: n, retired: retired, build: newGenerationBuild(context.Background()), done: make(chan struct{}), transaction: ctx, transferred: make(chan struct{}), managed: true}
	p.self = p
	build := p.build
	n.staged.Store(p)
	go p.supervise()
	go p.construct(candidateCfg)
	n.mu.Unlock()
	select {
	case <-ctx.Done():
		p.Abort()
		return p, ctx.Err()
	case <-build.done:
		return p, build.err
	}
}

// PublishManagedTransport consumes an exact staged owner once. The caller holds
// Core.op and store.mu after full file/receipt/projection/policy revalidation.
// current MUST be a signal-only predicate: no locks, I/O or owner waits. It is
// checked again inside the generation admission gate, immediately before swap.
func (n *Node) PublishManagedTransport(owner *ManagedTransportOwner, p *PreparedTransport, current func() bool) error {
	if !n.managedReplacementOwner(owner) || p == nil || p.self != p || current == nil {
		return ErrUntrusted
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.node != n || !p.managed || p.published || p.publishUsed || p.build == nil || p.retired == nil || n.staged.Load() != p || n.generation.Load() != p.retired.g || n.closed || n.closing.Load() || n.nonTransportRecovery || !n.started {
		return ErrRecovery
	}
	if err := p.retired.completed(); err != nil {
		return err
	}
	select {
	case <-p.build.done:
	default:
		return ErrUnavailable
	}
	if p.build.err != nil || p.build.ctx.Err() != nil || p.transaction.Err() != nil {
		return ErrRecovery
	}
	g := p.build.generation.Load()
	if g == nil || g == p.retired.g || g.n != n || g.traffic.Load() || g.underlay == nil {
		return ErrRecovery
	}
	p.publishUsed = true
	if !g.admit(func() bool {
		if n.closing.Load() || p.transaction.Err() != nil || p.build.ctx.Err() != nil || !current() {
			return false
		}
		n.peers = g.peers
		n.bind, n.tunnel, n.engine = g.bind, g.tunnel, g.engine.Load()
		n.underlay = g.underlay.listener
		n.recovery = false
		n.generation.Store(g)
		p.published = true
		n.staged.Store(nil)
		g.traffic.Store(true)
		close(g.published)
		close(p.transferred)
		return true
	}) {
		return net.ErrClosed
	}
	return nil
}

func (n *Node) managedReplacementOwner(owner *ManagedTransportOwner) bool {
	return owner != nil && owner.self == owner && n.cfg.ReplacementOwner == owner
}

// ObserveManagedRetirement returns only the exact already joined predecessor.
// It grants no traffic and cannot revive an expired epoch. Core must independently
// validate a fresh approved proposal and its original receipt before replacing it.
func (n *Node) ObserveManagedRetirement(owner *ManagedTransportOwner) (*TransportRetirement, error) {
	if !n.managedReplacementOwner(owner) {
		return nil, ErrUntrusted
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	g := n.generation.Load()
	if n.closed || n.closing.Load() || n.nonTransportRecovery || !n.started || g == nil || !g.traffic.Load() || n.staged.Load() != nil || n.building.Load() != nil {
		return nil, ErrRecovery
	}
	g.mu.Lock()
	sealed := g.sealed
	g.mu.Unlock()
	if !sealed {
		return nil, ErrUnavailable
	}
	if err := g.retirement.completed(); err != nil {
		return nil, err
	}
	return g.retirement, nil
}
